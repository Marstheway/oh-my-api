package scheduler

import (
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

// TestFailoverStrategy_SingleProvider_Success 单个 provider 成功返回
func TestFailoverStrategy_SingleProvider_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"test": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Provider: providers["test"], UpstreamModel: "model", Request: req},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "test" {
		t.Errorf("winner = %q, want %q", result.Winner, "test")
	}

	if !h.IsHealthy(health.MakeHealthKey("test", "")) {
		t.Error("provider should be healthy after success")
	}
}

// TestFailoverStrategy_FirstFail_SecondSuccess 第一个失败，第二个成功
func TestFailoverStrategy_FirstFail_SecondSuccess(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success"}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"fail": {
			Endpoint:  failSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, failSrv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)

	tasks := []Task{
		{ProviderName: "fail", Provider: providers["fail"], UpstreamModel: "model", Request: req1},
		{ProviderName: "success", Provider: providers["success"], UpstreamModel: "model", Request: req2},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute should succeed after failover: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "success" {
		t.Errorf("winner = %q, want %q", result.Winner, "success")
	}

	// 成功的 provider 应该健康
	if !h.IsHealthy(health.MakeHealthKey("success", "")) {
		t.Error("success provider should be healthy")
	}
}

// TestFailoverStrategy_AllFail 全部失败返回最后一个错误
func TestFailoverStrategy_AllFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"fail1": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"fail2": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)

	tasks := []Task{
		{ProviderName: "fail1", Provider: providers["fail1"], UpstreamModel: "model", Request: req1},
		{ProviderName: "fail2", Provider: providers["fail2"], UpstreamModel: "model", Request: req2},
	}

	// 全部失败时，应该返回最后一个结果（HTTP 5xx 响应），而不是错误
	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute should not return error for HTTP 5xx: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Response.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", result.Response.StatusCode, http.StatusInternalServerError)
	}

	if result.Winner != "fail2" {
		t.Errorf("winner = %q, want %q (last provider)", result.Winner, "fail2")
	}
}

// TestFailoverStrategy_SkipUnhealthy 跳过不健康 provider
func TestFailoverStrategy_SkipUnhealthy(t *testing.T) {
	unhealthySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer unhealthySrv.Close()

	healthySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"healthy"}`))
	}))
	defer healthySrv.Close()

	providers := map[string]config.ProviderConfig{
		"unhealthy": {
			Endpoint:  unhealthySrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"healthy": {
			Endpoint:  healthySrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)

	// 标记第一个 provider 为不健康
	h.ReportFailure(health.MakeHealthKey("unhealthy", ""))
	h.ReportFailure(health.MakeHealthKey("unhealthy", ""))
	h.ReportFailure(health.MakeHealthKey("unhealthy", ""))

	strategy := NewFailoverStrategy(client, rl, h)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, unhealthySrv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, healthySrv.URL, nil)

	tasks := []Task{
		{ProviderName: "unhealthy", Provider: providers["unhealthy"], UpstreamModel: "model", Request: req1},
		{ProviderName: "healthy", Provider: providers["healthy"], UpstreamModel: "model", Request: req2},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute should succeed by skipping unhealthy provider: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "healthy" {
		t.Errorf("winner = %q, want %q", result.Winner, "healthy")
	}
}

// TestFailoverStrategy_ForceTryWhenAllUnhealthy 全部不健康时强制尝试
func TestFailoverStrategy_ForceTryWhenAllUnhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"test": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)

	// 标记为不健康
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))

	strategy := NewFailoverStrategy(client, rl, h)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Provider: providers["test"], UpstreamModel: "model", Request: req},
	}

	// 全部不健康时应该强制尝试，返回成功结果
	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute should succeed with forced try: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "test" {
		t.Errorf("winner = %q, want %q", result.Winner, "test")
	}

	// 成功后应该恢复健康状态
	if !h.IsHealthy(health.MakeHealthKey("test", "")) {
		t.Error("provider should be healthy after success")
	}
}

func TestFailoverStrategy_ProtocolGranularityIsolation(t *testing.T) {
	respSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer respSrv.Close()

	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chat"}`))
	}))
	defer chatSrv.Close()

	providers := map[string]config.ProviderConfig{
		"token-hub": {
			Endpoint:  chatSrv.URL,
			APIKey:    "k",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h)

	respKey := health.MakeHealthKey("token-hub", "openai.response")
	h.ReportFailure(respKey)
	h.ReportFailure(respKey)
	h.ReportFailure(respKey)

	respReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, respSrv.URL, nil)
	chatReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, chatSrv.URL, nil)

	tasks := []Task{
		{ProviderName: "token-hub", Provider: providers["token-hub"], UpstreamModel: "m", OutboundProtocol: "openai.response", Request: respReq},
		{ProviderName: "token-hub", Provider: providers["token-hub"], UpstreamModel: "m", OutboundProtocol: "openai", Request: chatReq},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", result.Response.StatusCode)
	}
}

// TestFailoverStrategy_Timeout 超时中断
func TestFailoverStrategy_Timeout(t *testing.T) {
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond) // 模拟慢响应
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"slow"}`))
	}))
	defer slowSrv.Close()

	providers := map[string]config.ProviderConfig{
		"slow": {
			Endpoint:  slowSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	// 使用较短的超时
	client := provider.NewClient(providers, "50ms")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, slowSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "slow", Provider: providers["slow"], UpstreamModel: "model", Request: req},
	}

	_, err := strategy.Execute(context.Background(), tasks)
	// 由于客户端超时（50ms），请求应该返回错误
	if err == nil {
		t.Error("Execute should fail on timeout")
	}
}

// TestFailoverStrategy_ContextCancel context 取消中断
func TestFailoverStrategy_ContextCancel(t *testing.T) {
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"slow"}`))
	}))
	defer slowSrv.Close()

	providers := map[string]config.ProviderConfig{
		"slow": {
			Endpoint:  slowSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "50ms")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, slowSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "slow", Provider: providers["slow"], UpstreamModel: "model", Request: req},
	}

	// 使用更短的超时来确保快速失败
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := strategy.Execute(ctx, tasks)
	// HTTP 超时或 context 超时都应该返回错误
	if err == nil {
		t.Error("Execute should fail when context/cancel is cancelled")
	}
}

// TestFailoverStrategy_NoTasks 空 tasks 返回 ErrNoTasks
func TestFailoverStrategy_NoTasks(t *testing.T) {
	providers := map[string]config.ProviderConfig{}
	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h)

	_, err := strategy.Execute(context.Background(), nil)
	if err != ErrNoTasks {
		t.Errorf("error = %v, want %v", err, ErrNoTasks)
	}

	_, err = strategy.Execute(context.Background(), []Task{})
	if err != ErrNoTasks {
		t.Errorf("error = %v, want %v", err, ErrNoTasks)
	}
}

// TestFailoverStrategy_RateLimitSkip 限流跳过，尝试下一个
func TestFailoverStrategy_RateLimitSkip(t *testing.T) {
	rateLimitedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer rateLimitedSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success"}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"rateLimited": {
			Endpoint:  rateLimitedSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 1}, // 限制为 1 QPM
		},
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h)

	// 消耗限流令牌
	rl.Allow("rateLimited", "model")

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, rateLimitedSrv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)

	tasks := []Task{
		{ProviderName: "rateLimited", Provider: providers["rateLimited"], UpstreamModel: "model", Request: req1},
		{ProviderName: "success", Provider: providers["success"], UpstreamModel: "model", Request: req2},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute should succeed by skipping rate limited provider: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "success" {
		t.Errorf("winner = %q, want %q", result.Winner, "success")
	}
}
