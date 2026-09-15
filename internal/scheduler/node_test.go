package scheduler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

// newTestSchedulerForNode 创建带给定 providers map 的 Scheduler。
func newTestSchedulerForNode(providers map[string]config.ProviderConfig) *Scheduler {
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	return New(rl, client, h, 500*time.Millisecond, 0, 0)
}

// makeLeafNode 构建一个叶子 RunNode，每次调用 RequestFactory 时返回独立请求。
func makeLeafNode(providerName, upstreamModel string, prov config.ProviderConfig, targetURL string) *RunNode {
	return &RunNode{
		IsLeaf: true,
		Task: Task{
			ProviderName:  providerName,
			Provider:      prov,
			UpstreamModel: upstreamModel,
		},
		RequestFactory: func() (*http.Request, error) {
			body := bytes.NewBufferString("{}")
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, targetURL, body)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/json")
			return req, nil
		},
	}
}

// -----------------------------------------------------------------
// Test 1：顶层 failover 的第一个候选是返回 success 的子 group
// -----------------------------------------------------------------

func TestExecuteNode_NestedGroup_FailoverChildSuccess(t *testing.T) {
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "k",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}
	sched := newTestSchedulerForNode(providers)

	// 子 group（concurrent），包含一个成功叶子
	subGroup := &RunNode{
		IsLeaf: false,
		Name:   "sub-concurrent",
		Mode:   "concurrent",
		Weight: 1,
		Children: []*RunNode{
			makeLeafNode("prov", "model", providers["prov"], successSrv.URL),
		},
	}

	// 顶层 failover group，第一个候选是子 group
	root := &RunNode{
		IsLeaf:   false,
		Name:     "root",
		Mode:     "failover",
		Children: []*RunNode{subGroup},
	}

	result, err := sched.ExecuteNode(context.Background(), root)
	if err != nil {
		t.Fatalf("ExecuteNode should succeed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
	if result.Winner != "prov" {
		t.Errorf("winner = %q, want %q", result.Winner, "prov")
	}
}

// -----------------------------------------------------------------
// Test 4：子 group hard failure 不触发 group 的 fallback
// -----------------------------------------------------------------

// -----------------------------------------------------------------
// Test 6：并发竞速 - 子 group 先软失败，另一个 leaf 后成功
// -----------------------------------------------------------------

func TestExecuteNode_Concurrent_SubgroupSoftThenLeafSuccess(t *testing.T) {
	// 子 group 内的主链路：soft failure
	subSoftSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"sub-soft","choices":[{"message":{"role":"assistant","content":"I can't help with that request."},"finish_reason":"content_filter"}]}`))
	}))
	defer subSoftSrv.Close()

	// 另一个叶子：延迟后成功
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"leaf-success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"sub-soft": {
			Endpoint:  subSoftSrv.URL,
			APIKey:    "k",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"leaf-success": {
			Endpoint:  successSrv.URL,
			APIKey:    "k",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}
	sched := newTestSchedulerForNode(providers)

	// 子 group：failover 模式，只有 soft failure（无 fallback）
	subGroup := &RunNode{
		IsLeaf: false,
		Name:   "sub",
		Mode:   "failover",
		Weight: 1,
		Children: []*RunNode{
			makeLeafNode("sub-soft", "model", providers["sub-soft"], subSoftSrv.URL),
		},
	}

	// 顶层 concurrent：子 group + 一个 leaf
	root := &RunNode{
		IsLeaf: false,
		Name:   "root",
		Mode:   "concurrent",
		Children: []*RunNode{
			subGroup,
			makeLeafNode("leaf-success", "model", providers["leaf-success"], successSrv.URL),
		},
	}

	result, err := sched.ExecuteNode(context.Background(), root)
	if err != nil {
		t.Fatalf("ExecuteNode should succeed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want success (leaf-success should win)", result.FailureKind)
	}
	if result.Winner != "leaf-success" {
		t.Errorf("winner = %q, want leaf-success", result.Winner)
	}
}

// -----------------------------------------------------------------
// Test 7：顶层 failover + 子层 load-balance（嵌套 group）
// -----------------------------------------------------------------

func TestExecuteNode_NestedGroup_FailoverWithLoadbalanceChild(t *testing.T) {
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"lb-success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"lb-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "k",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}
	sched := newTestSchedulerForNode(providers)

	// 子 group：load-balance 模式
	subGroup := &RunNode{
		IsLeaf: false,
		Name:   "sub-lb",
		Mode:   "load-balance",
		Weight: 1,
		Children: []*RunNode{
			makeLeafNode("lb-prov", "model", providers["lb-prov"], successSrv.URL),
		},
	}

	// 顶层 failover
	root := &RunNode{
		IsLeaf:   false,
		Name:     "root",
		Mode:     "failover",
		Children: []*RunNode{subGroup},
	}

	result, err := sched.ExecuteNode(context.Background(), root)
	if err != nil {
		t.Fatalf("ExecuteNode should succeed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want success", result.FailureKind)
	}
}

// -----------------------------------------------------------------
// 辅助：newTestSchedulerForNode 已在上方定义
// -----------------------------------------------------------------
