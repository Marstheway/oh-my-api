package scheduler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

// TestExecuteNode_DisabledLeafRejected 验证 ExecuteNode 直接执行被禁用的叶子节点时，
// 返回 ErrNoProviderAvailable 而不是向后端发请求。
func TestExecuteNode_DisabledLeafRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"should-not-be-called"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"disabled-prov": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0, 0)

	node := &RunNode{
		IsLeaf: true,
		Task: Task{
			ProviderName:     "disabled-prov",
			Provider:         providers["disabled-prov"],
			UpstreamModel:    "model",
			DisableTimeRange: []string{"00:00-24:00"},
		},
		RequestFactory: func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, srv.URL, nil)
		},
	}

	_, err := scheduler.ExecuteNode(context.Background(), node)
	if err != ErrNoProviderAvailable {
		t.Fatalf("expected ErrNoProviderAvailable, got %v", err)
	}
}

// TestAllProvidersDisabled_EmptyTasks 边界：空任务列表返回 false。
func TestAllProvidersDisabled_EmptyTasks(t *testing.T) {
	if allProvidersDisabled(nil, time.Now()) {
		t.Error("allProvidersDisabled with nil tasks should return false")
	}
	if allProvidersDisabled([]Task{}, time.Now()) {
		t.Error("allProvidersDisabled with empty tasks should return false")
	}
}

// TestAllProvidersDisabledOrUnhealthy_EmptyTasks 边界：空任务列表返回 false。
func TestAllProvidersDisabledOrUnhealthy_EmptyTasks(t *testing.T) {
	h := health.NewChecker(3, 30*time.Second)
	if allProvidersDisabledOrUnhealthy(nil, h, time.Now()) {
		t.Error("allProvidersDisabledOrUnhealthy with nil tasks should return false")
	}
	if allProvidersDisabledOrUnhealthy([]Task{}, h, time.Now()) {
		t.Error("allProvidersDisabledOrUnhealthy with empty tasks should return false")
	}
}

// TestExecuteNode_LoadBalanceMixedPath_SkipsDisabledLeaf 验证 load-balance
// 混合路径（叶子+group 混搭）中 filterHealthyNodes 会跳过被禁用的叶子节点，
// 并将剩余的 group 节点送到 runNodeSelector 参与加权选择，最终由子 group 的叶子返回成功。
//
// 这是新特性中 filterHealthyNodes 的核心覆盖测试：
// - 禁用叶子被过滤 → isProviderDisabledAt 命中
// - group 节点保留（子 group 始终认为健康）
// - runNodeSelector 覆盖 newRunNodeSelector / nodeWeight / isEmpty / selectOne / remove
func TestExecuteNode_LoadBalanceMixedPath_SkipsDisabledLeaf(t *testing.T) {
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"disabled-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"enabled-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0, 0)

	root := &RunNode{
		IsLeaf: false,
		Name:   "root",
		Mode:   "load-balance",
		Weight: 1,
		Children: []*RunNode{
			// 被禁用的叶子 — filterHealthyNodes 应将其过滤掉
			{
				IsLeaf: true,
				Weight: 1,
				Task: Task{
					ProviderName:     "disabled-prov",
					Provider:         providers["disabled-prov"],
					UpstreamModel:    "model",
					OutboundProtocol: "openai",
					DisableTimeRange: []string{"00:00-24:00"},
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, successSrv.URL, nil)
				},
			},
			// group 节点，包含一个可用叶子 — 应保留并执行成功
			{
				IsLeaf: false,
				Name:   "child-group",
				Mode:   "failover",
				Weight: 1,
				Children: []*RunNode{
					{
						IsLeaf: true,
						Weight: 1,
						Task: Task{
							ProviderName:     "enabled-prov",
							Provider:         providers["enabled-prov"],
							UpstreamModel:    "model",
							OutboundProtocol: "openai",
						},
						RequestFactory: func() (*http.Request, error) {
							return http.NewRequest(http.MethodPost, successSrv.URL, nil)
						},
					},
				},
			},
		},
	}

	result, err := scheduler.ExecuteNode(context.Background(), root)
	if err != nil {
		t.Fatalf("ExecuteNode should succeed (disabled leaf skipped, group leaf succeeds): %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
}

// TestExecuteNode_LoadBalanceMixedPath_LeafSingleAllow 验证混合 LB 对直接叶子
// 只 Allow 一次（QPM=1 仍能打通上游）；限流在 lbStrat.executeTask 内完成。
func TestExecuteNode_LoadBalanceMixedPath_LeafSingleAllow(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"leaf-prov": {
			Endpoint:  srv.URL,
			APIKey:    "k",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 1},
		},
		"group-prov": {
			Endpoint:  srv.URL,
			APIKey:    "k",
			Protocols: []string{"openai.chat"},
			// 子 group 叶子给足配额；本用例通过 weight 让 LB 必选直接叶子
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}
	client := provider.NewClient(providers, 50*time.Millisecond, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h, 500*time.Millisecond, 0, 0)

	root := &RunNode{
		IsLeaf: false,
		Name:   "mixed-lb",
		Mode:   "load-balance",
		Children: []*RunNode{
			{
				IsLeaf: true,
				Weight: 100,
				Task: Task{
					ProviderName:     "leaf-prov",
					Provider:         providers["leaf-prov"],
					UpstreamModel:    "m",
					Weight:           100,
					OutboundProtocol: "openai.chat",
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", nil)
				},
			},
			{
				IsLeaf: false,
				Name:   "child",
				Mode:   "failover",
				Weight: 1,
				Children: []*RunNode{
					{
						IsLeaf: true,
						Task: Task{
							ProviderName:     "group-prov",
							Provider:         providers["group-prov"],
							UpstreamModel:    "m",
							OutboundProtocol: "openai.chat",
						},
						RequestFactory: func() (*http.Request, error) {
							return http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", nil)
						},
					},
				},
			},
		},
	}

	result, err := sched.ExecuteNode(context.Background(), root)
	if err != nil {
		t.Fatalf("expected success with single Allow, err=%v", err)
	}
	if result == nil || result.FailureKind != FailureKindSuccess {
		t.Fatalf("expected success, got %#v", result)
	}
	if result.Response != nil {
		result.Response.Body.Close()
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 upstream hit via direct leaf, got %d", hits.Load())
	}
	if result.Winner != "leaf-prov" {
		t.Fatalf("expected leaf-prov winner, got %q", result.Winner)
	}
}

// TestExecuteNode_LoadBalanceMixedPath_AllDisabled 验证 load-balance 混合路径中
// 所有候选（包括 group 内叶子）均被禁用时，返回 ErrNoProviderAvailable。
func TestExecuteNode_LoadBalanceMixedPath_AllDisabled(t *testing.T) {
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok"}`))
	}))
	defer successSrv.Close()

	disabledCfg := config.ProviderConfig{
		Endpoint:  successSrv.URL,
		APIKey:    "test-key",
		Protocols: []string{"openai"},
		RateLimit: config.RateLimitConfig{QPM: 0},
	}

	providers := map[string]config.ProviderConfig{
		"all-disabled": disabledCfg,
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0, 0)

	root := &RunNode{
		IsLeaf: false,
		Name:   "root",
		Mode:   "load-balance",
		Children: []*RunNode{
			// 被禁用的叶子
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "all-disabled",
					Provider:         disabledCfg,
					UpstreamModel:    "model",
					OutboundProtocol: "openai",
					DisableTimeRange: []string{"00:00-24:00"},
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, successSrv.URL, nil)
				},
			},
			// group 节点，内部叶子也被禁用
			{
				IsLeaf: false,
				Name:   "child-group",
				Mode:   "failover",
				Children: []*RunNode{
					{
						IsLeaf: true,
						Task: Task{
							ProviderName:     "all-disabled",
							Provider:         disabledCfg,
							UpstreamModel:    "model",
							OutboundProtocol: "openai",
							DisableTimeRange: []string{"00:00-24:00"},
						},
						RequestFactory: func() (*http.Request, error) {
							return http.NewRequest(http.MethodPost, successSrv.URL, nil)
						},
					},
				},
			},
		},
	}

	_, err := scheduler.ExecuteNode(context.Background(), root)
	if !errors.Is(err, ErrNoProviderAvailable) {
		t.Fatalf("expected ErrNoProviderAvailable, got %v", err)
	}
}

// TestExecuteNode_FailoverMixedPath_SkipsDisabledLeaf 验证 failover 混合路径
// 中会跳过被禁用的叶子节点，并继续尝试下一个（group）节点。
func TestExecuteNode_FailoverMixedPath_SkipsDisabledLeaf(t *testing.T) {
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"disabled-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"enabled-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0, 0)

	root := &RunNode{
		IsLeaf: false,
		Name:   "root",
		Mode:   "failover",
		Children: []*RunNode{
			// 被禁用的叶子 — 应被跳过
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "disabled-prov",
					Provider:         providers["disabled-prov"],
					UpstreamModel:    "model",
					OutboundProtocol: "openai",
					DisableTimeRange: []string{"00:00-24:00"},
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, successSrv.URL, nil)
				},
			},
			// group 节点，包含可用叶子 — 应被尝试并成功
			{
				IsLeaf: false,
				Name:   "child-group",
				Mode:   "failover",
				Children: []*RunNode{
					{
						IsLeaf: true,
						Task: Task{
							ProviderName:     "enabled-prov",
							Provider:         providers["enabled-prov"],
							UpstreamModel:    "model",
							OutboundProtocol: "openai",
						},
						RequestFactory: func() (*http.Request, error) {
							return http.NewRequest(http.MethodPost, successSrv.URL, nil)
						},
					},
				},
			},
		},
	}

	result, err := scheduler.ExecuteNode(context.Background(), root)
	if err != nil {
		t.Fatalf("ExecuteNode should succeed (disabled leaf skipped, group leaf succeeds): %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
}

// TestExecuteNode_ConcurrentMixedPath_SkipsDisabledLeaf 验证 concurrent 混合路径
// 中会跳过被禁用的叶子节点，其它可用候选（group）正常参与竞速。
func TestExecuteNode_ConcurrentMixedPath_SkipsDisabledLeaf(t *testing.T) {
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"disabled-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"enabled-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0, 0)

	root := &RunNode{
		IsLeaf: false,
		Name:   "root",
		Mode:   "concurrent",
		Children: []*RunNode{
			// 被禁用的叶子 — 应被跳过
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "disabled-prov",
					Provider:         providers["disabled-prov"],
					UpstreamModel:    "model",
					OutboundProtocol: "openai",
					DisableTimeRange: []string{"00:00-24:00"},
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, successSrv.URL, nil)
				},
			},
			// group 节点，包含可用叶子 — 应正常参与竞速并成功
			{
				IsLeaf: false,
				Name:   "child-group",
				Mode:   "failover",
				Children: []*RunNode{
					{
						IsLeaf: true,
						Task: Task{
							ProviderName:     "enabled-prov",
							Provider:         providers["enabled-prov"],
							UpstreamModel:    "model",
							OutboundProtocol: "openai",
						},
						RequestFactory: func() (*http.Request, error) {
							return http.NewRequest(http.MethodPost, successSrv.URL, nil)
						},
					},
				},
			},
		},
	}

	result, err := scheduler.ExecuteNode(context.Background(), root)
	if err != nil {
		t.Fatalf("ExecuteNode should succeed (disabled leaf skipped, group leaf succeeds): %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
}

// TestExecuteNode_ConcurrentMixedPath_WinnerStreamSurvivesRaceCancel 验证混合
// concurrent（叶子 + 子 group）选出流式胜者后，SSE tail 仍可读完。
func TestExecuteNode_ConcurrentMixedPath_WinnerStreamSurvivesRaceCancel(t *testing.T) {
	winnerURL, releaseTail, winnerCanceled := newHeldOpenAIStreamServer(t)
	loserURL, loserCanceled := newCancelWatchServer(t)

	providers := map[string]config.ProviderConfig{
		"winner-prov": {
			Endpoint:  winnerURL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"loser-prov": {
			Endpoint:  loserURL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0, 0)

	root := &RunNode{
		IsLeaf: false,
		Name:   "root",
		Mode:   "concurrent",
		Children: []*RunNode{
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "loser-prov",
					Provider:         providers["loser-prov"],
					UpstreamModel:    "model",
					OutboundProtocol: "openai",
					Stream:           true,
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, loserURL, nil)
				},
			},
			{
				IsLeaf: false,
				Name:   "child-group",
				Mode:   "failover",
				Children: []*RunNode{
					{
						IsLeaf: true,
						Task: Task{
							ProviderName:     "winner-prov",
							Provider:         providers["winner-prov"],
							UpstreamModel:    "model",
							OutboundProtocol: "openai",
							Stream:           true,
						},
						RequestFactory: func() (*http.Request, error) {
							return http.NewRequest(http.MethodPost, winnerURL, nil)
						},
					},
				},
			},
		},
	}

	result, err := scheduler.ExecuteNode(context.Background(), root)
	if err != nil {
		t.Fatalf("ExecuteNode failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "winner-prov" {
		t.Fatalf("winner = %q, want %q", result.Winner, "winner-prov")
	}
	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}

	select {
	case <-loserCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("loser request was not canceled after winner")
	}

	releaseTail()
	body := string(readSchedulerResponseBody(t, result.Response))
	if !strings.Contains(body, `"content":"hello"`) {
		t.Fatalf("missing probed prefix in stream body: %q", body)
	}
	if !strings.Contains(body, `"content":" world"`) {
		t.Fatalf("winner tail was cut off after mixed-path race cancel: %q", body)
	}

	select {
	case <-winnerCanceled:
		t.Fatal("winner request context was canceled after mixed-path race")
	default:
	}
}

// nestedAutoFlashThenOhmygpt builds auto=failover(_low load-balance, ohmygpt leaf).
func nestedAutoFlashThenOhmygpt(
	providers map[string]config.ProviderConfig,
	failURL, successURL string,
	ohmygptRequested *bool,
) *RunNode {
	return &RunNode{
		IsLeaf: false,
		Name:   "auto",
		Mode:   "failover",
		Children: []*RunNode{
			{
				IsLeaf: false,
				Name:   "flash",
				Mode:   "load-balance",
				Children: []*RunNode{
					{
						IsLeaf: true,
						Task: Task{
							ProviderName:     "opencode-go",
							Provider:         providers["opencode-go"],
							UpstreamModel:    "deepseek-v4-flash",
							OutboundProtocol: "openai",
						},
						RequestFactory: func() (*http.Request, error) {
							return http.NewRequest(http.MethodPost, failURL, nil)
						},
					},
					{
						IsLeaf: true,
						Task: Task{
							ProviderName:     "opencode-zen",
							Provider:         providers["opencode-zen"],
							UpstreamModel:    "deepseek-v4-flash-free",
							OutboundProtocol: "openai",
						},
						RequestFactory: func() (*http.Request, error) {
							return http.NewRequest(http.MethodPost, failURL, nil)
						},
					},
				},
			},
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "ohmygpt",
					Provider:         providers["ohmygpt"],
					UpstreamModel:    "deepseek-v4-flash",
					OutboundProtocol: "openai",
				},
				RequestFactory: func() (*http.Request, error) {
					if ohmygptRequested != nil {
						*ohmygptRequested = true
					}
					return http.NewRequest(http.MethodPost, successURL, nil)
				},
			},
		},
	}
}

// TestExecuteNode_FailoverSubGroupDeadlineExhausted 验证：父 request deadline 已耗尽时，
// 不得再发起后续候选（死 ctx 上继续尝试只会空转）。f26ac2e 的「继续试」行为已收回。
func TestExecuteNode_FailoverSubGroupDeadlineExhausted(t *testing.T) {
	var ohmygptRequested bool

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	// 模拟耗时失败，拖垮短 deadline
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error":{"message":"upstream connection failed"}}`))
	}))
	defer failSrv.Close()

	providers := map[string]config.ProviderConfig{
		"opencode-go": {
			Endpoint:  failSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"opencode-zen": {
			Endpoint:  failSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"ohmygpt": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 100*time.Millisecond, 0, 0)

	root := nestedAutoFlashThenOhmygpt(providers, failSrv.URL, successSrv.URL, &ohmygptRequested)

	// 短 deadline：子 group 尝试后 ctx 已过期
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err := scheduler.ExecuteNode(ctx, root)

	if ohmygptRequested {
		t.Fatal("ohmygpt RequestFactory must not be called after parent deadline is exhausted")
	}
	if err == nil {
		t.Fatal("expected deadline/cancel error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		// First leaf may surface deadline via client Do; accept either abort form.
		if !IsContextAbort(err) {
			t.Fatalf("expected context abort, got %v", err)
		}
	}
}

// cancel-test helpers: block until request ctx done; signal first start.

func newBlockingServer(t *testing.T) (url string, started <-chan struct{}, cleanup func()) {
	t.Helper()
	ch := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case ch <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	return srv.URL, ch, srv.Close
}

func newOKServer(t *testing.T) (url string, hits *int32, cleanup func()) {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	return srv.URL, &n, srv.Close
}

func providerPair(blockURL, otherURL string) map[string]config.ProviderConfig {
	return map[string]config.ProviderConfig{
		"p1": {Endpoint: blockURL, APIKey: "k", Protocols: []string{"openai"}, RateLimit: config.RateLimitConfig{QPM: 0}},
		"p2": {Endpoint: otherURL, APIKey: "k", Protocols: []string{"openai"}, RateLimit: config.RateLimitConfig{QPM: 0}},
	}
}

func leafNode(name string, p config.ProviderConfig, model, protocol, rawURL string, weight int) *RunNode {
	return &RunNode{
		IsLeaf: true,
		Task: Task{
			ProviderName: name, Provider: p, UpstreamModel: model,
			OutboundProtocol: protocol, Weight: weight,
		},
		RequestFactory: func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, rawURL, nil)
		},
	}
}

func runUntilStartedThenCancel(t *testing.T, started <-chan struct{}, fn func(ctx context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = fn(ctx)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("upstream never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ExecuteNode did not return after cancel")
	}
	return runErr
}

// TestExecuteNode_ClientCancelStopsNestedFailover 验证客户端断开后，
// 嵌套 auto failover 不得继续尝试后续叶子（ohmygpt）。
func TestExecuteNode_ClientCancelStopsNestedFailover(t *testing.T) {
	var ohmygptRequested bool
	blockURL, started, closeBlock := newBlockingServer(t)
	defer closeBlock()
	okURL, _, closeOK := newOKServer(t)
	defer closeOK()

	providers := map[string]config.ProviderConfig{
		"opencode-go":  {Endpoint: blockURL, APIKey: "test-key", Protocols: []string{"openai"}, RateLimit: config.RateLimitConfig{QPM: 0}},
		"opencode-zen": {Endpoint: blockURL, APIKey: "test-key", Protocols: []string{"openai"}, RateLimit: config.RateLimitConfig{QPM: 0}},
		"ohmygpt":      {Endpoint: okURL, APIKey: "test-key", Protocols: []string{"openai"}, RateLimit: config.RateLimitConfig{QPM: 0}},
	}
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(ratelimit.NewManager(providers), provider.NewClient(providers, 120*time.Second, 0, 0), h, 500*time.Millisecond, 0, 0)
	root := nestedAutoFlashThenOhmygpt(providers, blockURL, okURL, &ohmygptRequested)

	runErr := runUntilStartedThenCancel(t, started, func(ctx context.Context) error {
		_, err := scheduler.ExecuteNode(ctx, root)
		return err
	})
	if ohmygptRequested {
		t.Fatal("ohmygpt must not be attempted after client cancel")
	}
	if runErr == nil || !errors.Is(runErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", runErr)
	}
	if !h.IsHealthy(health.MakeHealthKey("opencode-go", "openai")) {
		t.Fatal("client cancel must not ReportFailure on provider health")
	}
}

// TestExecuteTask_ClientCancelDoesNotReportFailure 纯叶子 failover：cancel 不扣健康分，且不打下一个候选。
func TestExecuteTask_ClientCancelDoesNotReportFailure(t *testing.T) {
	blockURL, started, closeBlock := newBlockingServer(t)
	defer closeBlock()
	okURL, p2Hits, closeOK := newOKServer(t)
	defer closeOK()

	providers := providerPair(blockURL, okURL)
	h := health.NewChecker(1, 30*time.Second) // threshold 1: one ReportFailure would unhealth
	scheduler := New(ratelimit.NewManager(providers), provider.NewClient(providers, 120*time.Second, 0, 0), h, 500*time.Millisecond, 0, 0)
	root := &RunNode{
		IsLeaf: false, Name: "group", Mode: "failover",
		Children: []*RunNode{
			leafNode("p1", providers["p1"], "m", "openai", blockURL, 0),
			// allLeaves may materialize factories eagerly; assert via p2Hits (actual Do).
			leafNode("p2", providers["p2"], "m", "openai", okURL, 0),
		},
	}

	_ = runUntilStartedThenCancel(t, started, func(ctx context.Context) error {
		_, err := scheduler.ExecuteNode(ctx, root)
		return err
	})
	if atomic.LoadInt32(p2Hits) != 0 {
		t.Fatalf("p2 must not be hit after cancel, hits=%d", atomic.LoadInt32(p2Hits))
	}
	if !h.IsHealthy(health.MakeHealthKey("p1", "openai")) {
		t.Fatal("cancel must not mark p1 unhealthy")
	}
}

// TestExecuteNode_ClientCancelStopsLoadBalance further sequential candidates.
func TestExecuteNode_ClientCancelStopsLoadBalance(t *testing.T) {
	var hits int32
	started := make(chan struct{}, 2)
	blockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		<-r.Context().Done()
	}))
	defer blockSrv.Close()

	providers := providerPair(blockSrv.URL, blockSrv.URL)
	scheduler := New(ratelimit.NewManager(providers), provider.NewClient(providers, 120*time.Second, 0, 0), health.NewChecker(3, 30*time.Second), 500*time.Millisecond, 0, 0)
	root := &RunNode{
		IsLeaf: false, Name: "lb", Mode: "load-balance",
		Children: []*RunNode{
			leafNode("p1", providers["p1"], "m", "openai", blockSrv.URL, 1),
			leafNode("p2", providers["p2"], "m", "openai", blockSrv.URL, 1),
		},
	}

	runErr := runUntilStartedThenCancel(t, started, func(ctx context.Context) error {
		_, err := scheduler.ExecuteNode(ctx, root)
		return err
	})
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("load-balance must attempt exactly 1 candidate after client cancel, hits=%d", got)
	}
	if runErr == nil || !errors.Is(runErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", runErr)
	}
}

// TestExecuteNode_ClientCancelStopsConcurrent cancels the race and returns abort.
func TestExecuteNode_ClientCancelStopsConcurrent(t *testing.T) {
	blockURL, started, closeBlock := newBlockingServer(t)
	defer closeBlock()

	providers := providerPair(blockURL, blockURL)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(ratelimit.NewManager(providers), provider.NewClient(providers, 120*time.Second, 0, 0), h, 500*time.Millisecond, 0, 0)
	root := &RunNode{
		IsLeaf: false, Name: "race", Mode: "concurrent",
		Children: []*RunNode{
			leafNode("p1", providers["p1"], "m", "openai", blockURL, 0),
			leafNode("p2", providers["p2"], "m", "openai", blockURL, 0),
		},
	}

	runErr := runUntilStartedThenCancel(t, started, func(ctx context.Context) error {
		_, err := scheduler.ExecuteNode(ctx, root)
		return err
	})
	if runErr == nil || !errors.Is(runErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", runErr)
	}
	if !h.IsHealthy(health.MakeHealthKey("p1", "openai")) || !h.IsHealthy(health.MakeHealthKey("p2", "openai")) {
		t.Fatal("concurrent client cancel must not mark providers unhealthy")
	}
}

// TestExecuteNode_FailoverSubGroupPrefillTimeoutFallsThrough 近似复现线上现象：
// flash 子 group 两个流式候选都在 prefill_timeout 内不吐首个事件，
// 父 failover 应继续尝试 ohmygpt，而不是停在 flash 子 group。
func TestExecuteNode_FailoverSubGroupPrefillTimeoutFallsThrough(t *testing.T) {
	var ohmygptRequested bool

	slowStreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"late\"},\"finish_reason\":null}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer slowStreamSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"opencode-go": {
			Endpoint:  slowStreamSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"opencode-zen": {
			Endpoint:  slowStreamSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"ohmygpt": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 2*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 50*time.Millisecond, 0, 0)

	newStreamReq := func(rawURL string) (*http.Request, error) {
		body := io.NopCloser(strings.NewReader(`{"model":"deepseek-v4-flash","stream":true}`))
		req, err := http.NewRequest(http.MethodPost, rawURL, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/event-stream")
		return req, nil
	}

	root := &RunNode{
		IsLeaf: false,
		Name:   "auto",
		Mode:   "failover",
		Children: []*RunNode{
			{
				IsLeaf: false,
				Name:   "flash",
				Mode:   "load-balance",
				Children: []*RunNode{
					{
						IsLeaf: true,
						Task: Task{
							ProviderName:     "opencode-go",
							Provider:         providers["opencode-go"],
							UpstreamModel:    "deepseek-v4-flash",
							OutboundProtocol: "openai",
						},
						RequestFactory: func() (*http.Request, error) {
							return newStreamReq(slowStreamSrv.URL)
						},
					},
					{
						IsLeaf: true,
						Task: Task{
							ProviderName:     "opencode-zen",
							Provider:         providers["opencode-zen"],
							UpstreamModel:    "deepseek-v4-flash-free",
							OutboundProtocol: "openai",
						},
						RequestFactory: func() (*http.Request, error) {
							return newStreamReq(slowStreamSrv.URL)
						},
					},
				},
			},
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "ohmygpt",
					Provider:         providers["ohmygpt"],
					UpstreamModel:    "deepseek-v4-flash",
					OutboundProtocol: "openai",
				},
				RequestFactory: func() (*http.Request, error) {
					ohmygptRequested = true
					return http.NewRequest(http.MethodPost, successSrv.URL, nil)
				},
			},
		},
	}

	result, err := scheduler.ExecuteNode(context.Background(), root)
	if err != nil {
		t.Fatalf("ExecuteNode returned error: %v", err)
	}
	defer result.Response.Body.Close()

	if !ohmygptRequested {
		t.Fatal("ohmygpt RequestFactory was not called after flash prefill timeout")
	}
	if result.Winner != "ohmygpt" {
		t.Fatalf("winner = %q, want %q", result.Winner, "ohmygpt")
	}
}
