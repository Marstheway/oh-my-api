package scheduler

import (
	"io"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
			Endpoint:           srv.URL,
			APIKey:             "test-key",
			Protocols: []string{"openai"},
			RateLimit:          config.RateLimitConfig{QPM: 0},
			DisabledTimeRanges: []string{"00:00-24:00"},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0)

	node := &RunNode{
		IsLeaf: true,
		Task: Task{
			ProviderName:  "disabled-prov",
			Provider:      providers["disabled-prov"],
			UpstreamModel: "model",
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
			Endpoint:           successSrv.URL,
			APIKey:             "test-key",
			Protocols: []string{"openai"},
			RateLimit:          config.RateLimitConfig{QPM: 0},
			DisabledTimeRanges: []string{"00:00-24:00"},
		},
		"enabled-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0)

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

// TestExecuteNode_LoadBalanceMixedPath_AllDisabled 验证 load-balance 混合路径中
// 所有候选（包括 group 内叶子）均被禁用时，返回 ErrNoProviderAvailable。
func TestExecuteNode_LoadBalanceMixedPath_AllDisabled(t *testing.T) {
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok"}`))
	}))
	defer successSrv.Close()

	disabledCfg := config.ProviderConfig{
		Endpoint:           successSrv.URL,
		APIKey:             "test-key",
		Protocols: []string{"openai"},
		RateLimit:          config.RateLimitConfig{QPM: 0},
		DisabledTimeRanges: []string{"00:00-24:00"},
	}

	providers := map[string]config.ProviderConfig{
		"all-disabled": disabledCfg,
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0)

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
			Endpoint:           successSrv.URL,
			APIKey:             "test-key",
			Protocols: []string{"openai"},
			RateLimit:          config.RateLimitConfig{QPM: 0},
			DisabledTimeRanges: []string{"00:00-24:00"},
		},
		"enabled-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0)

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
			Endpoint:           successSrv.URL,
			APIKey:             "test-key",
			Protocols: []string{"openai"},
			RateLimit:          config.RateLimitConfig{QPM: 0},
			DisabledTimeRanges: []string{"00:00-24:00"},
		},
		"enabled-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0)

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

// TestExecuteNode_FailoverSubGroupDeadlineExhausted 复现生产 bug:
// load-balance 子 group 所有候选失败后消耗了父 context 的 deadline，
// executeFailoverNodes 循环头部的 ctx.Err() 导致后续叶子候选被跳过。
//
// 修复后验证：即使 ctx 已过期，父 failover 仍应尝试后续候选
// （RequestFactory 被调用），而不是直接返回 ctx.Err()。
func TestExecuteNode_FailoverSubGroupDeadlineExhausted(t *testing.T) {
	var ohmygptRequested bool

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	// 模拟耗时失败
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

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 100*time.Millisecond, 0)

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
							return http.NewRequest(http.MethodPost, failSrv.URL, nil)
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
							return http.NewRequest(http.MethodPost, failSrv.URL, nil)
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

	// 短 deadline：子 group 400ms+ 尝试后 ctx 已过期
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, _ = scheduler.ExecuteNode(ctx, root)

	// 核心验证：ohmygpt 的 RequestFactory 应被调用（不含 ctx.Err() 短路）
	if !ohmygptRequested {
		t.Fatal("ohmygpt RequestFactory was not called — failover short-circuited on ctx.Err()")
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

	client := provider.NewClient(providers, 2*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 50*time.Millisecond, 0)

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
