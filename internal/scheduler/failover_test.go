package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
	"github.com/prometheus/client_golang/prometheus/testutil"
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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"fail2": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"healthy": {
			Endpoint:  healthySrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)

	// 标记第一个 provider 为不健康
	h.ReportFailure(health.MakeHealthKey("unhealthy", ""))
	h.ReportFailure(health.MakeHealthKey("unhealthy", ""))
	h.ReportFailure(health.MakeHealthKey("unhealthy", ""))

	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)

	// 标记为不健康
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))

	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

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

func TestFailoverStrategy_ProbeReadFailureMarksProviderUnhealthy(t *testing.T) {
	srv := newProbeReadFailureServer(t)
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"broken": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(1, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	_, err := strategy.Execute(context.Background(), []Task{{
		ProviderName:  "broken",
		Provider:      providers["broken"],
		UpstreamModel: "model",
		Request:       req,
	}})
	if err == nil {
		t.Fatal("Execute should fail on probe read error")
	}

	if h.IsHealthy(health.MakeHealthKey("broken", "")) {
		t.Fatal("probe read failure should report provider failure")
	}
}

func TestFailoverStrategy_PrefillTimeoutRetriesNextProvider(t *testing.T) {
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"late\"},\"finish_reason\":null}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer slowSrv.Close()

	fastBody := "data: {\"choices\":[{\"delta\":{\"content\":\"fast\"},\"finish_reason\":null}]}\n\n" +
		"data: [DONE]\n\n"
	fastSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fastBody))
	}))
	defer fastSrv.Close()

	providers := map[string]config.ProviderConfig{
		"slow": {
			Endpoint:  slowSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"fast": {
			Endpoint:  fastSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 50*time.Millisecond, 0)

	slowReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, slowSrv.URL, nil)
	fastReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, fastSrv.URL, nil)
	result, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "slow", Provider: providers["slow"], UpstreamModel: "model", Request: slowReq},
		{ProviderName: "fast", Provider: providers["fast"], UpstreamModel: "model", Request: fastReq},
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "fast" {
		t.Fatalf("Winner = %q, want %q", result.Winner, "fast")
	}
	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}

	body := readSchedulerResponseBody(t, result.Response)
	if string(body) != fastBody {
		t.Fatalf("response body mismatch: got %q want %q", string(body), fastBody)
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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	// 使用较短的超时
	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

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
	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 1}, // 限制为 1 QPM
		},
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

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

// TestFailover_LogsProviderUpstreamPair 验证 failover 日志中使用 upstream_identity=provider/model，
// 而非裸 model 字段。
func TestFailover_LogsProviderUpstreamPair(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok"}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"fail-prov": {
			Endpoint:  failSrv.URL,
			APIKey:    "k",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"success-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "k",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	// 捕获 slog 输出
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	old := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(old)

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, failSrv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "fail-prov", Provider: providers["fail-prov"], UpstreamModel: "fail-model", Request: req1},
		{ProviderName: "success-prov", Provider: providers["success-prov"], UpstreamModel: "success-model", Request: req2},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	logs := buf.String()

	// upstream_identity 字段应存在
	if !strings.Contains(logs, `"upstream_identity"`) {
		t.Errorf("log output should contain upstream_identity field, got: %s", logs)
	}

	// 不应出现裸 model 字段（值为 upstreamModel 而不含 provider 前缀）
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if v, ok := entry["model"]; ok {
			s, _ := v.(string)
			if !strings.Contains(s, "/") {
				t.Errorf("log line contains bare model field without provider prefix: %q, line: %s", s, line)
			}
		}
	}
}

func TestFailoverStrategy_ContentFilterRetriesToSecondSuccess(t *testing.T) {
	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft","choices":[{"message":{"role":"assistant","content":"I can't help with that request."},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	softReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	successReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: softReq},
		{ProviderName: "success", Provider: providers["success"], UpstreamModel: "model", Request: successReq},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "success" {
		t.Fatalf("winner = %q, want %q", result.Winner, "success")
	}
	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
}

func TestFailoverStrategy_StreamSoftFailureClosesAbandonedBodyOnSuccess(t *testing.T) {
	softContinue := make(chan struct{}, 1)
	softClosed := make(chan struct{}, 1)
	defer func() {
		select {
		case softContinue <- struct{}{}:
		default:
		}
	}()

	softCalls := 0
	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		softCalls++
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}

		<-softContinue
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"late\"},\"finish_reason\":null}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}

		select {
		case <-r.Context().Done():
			softClosed <- struct{}{}
		case <-time.After(300 * time.Millisecond):
		}
	}))
	defer softSrv.Close()

	successCalls := 0
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		successCalls++
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	softReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, softSrv.URL, nil)
	successReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, successSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: softReq},
		{ProviderName: "success", Provider: providers["success"], UpstreamModel: "model", Request: successReq},
	}

	result, err := strategy.Execute(ctx, tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "success" {
		t.Fatalf("winner = %q, want %q", result.Winner, "success")
	}
	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
	if softCalls != 1 {
		t.Fatalf("soft provider called %d times, want 1", softCalls)
	}
	if successCalls != 1 {
		t.Fatalf("success provider called %d times, want 1", successCalls)
	}

	body := string(readSchedulerResponseBody(t, result.Response))
	if !strings.Contains(body, "\"content\":\"ok\"") {
		t.Fatalf("success stream body missing winning content: %q", body)
	}

	softContinue <- struct{}{}
	select {
	case <-softClosed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("abandoned soft-failure stream body was not closed")
	}
}

func TestFailoverStrategy_SoftFailureReturnsLaterHardFailureAndKeepsHealth(t *testing.T) {
	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft","choices":[{"message":{"role":"assistant","content":"I can't help with that request."},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv.Close()

	hardSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"upstream failed"}`))
	}))
	defer hardSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"hard": {
			Endpoint:  hardSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	softReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	hardReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, hardSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: softReq},
		{ProviderName: "hard", Provider: providers["hard"], UpstreamModel: "model", Request: hardReq},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "hard" {
		t.Fatalf("winner = %q, want %q", result.Winner, "hard")
	}
	if result.FailureKind != FailureKindHard {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindHard)
	}
	if result.Response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", result.Response.StatusCode, http.StatusInternalServerError)
	}
	if !h.IsHealthy(health.MakeHealthKey("soft", "")) {
		t.Fatal("soft-failing provider should remain healthy")
	}
}

func TestFailoverStrategy_SoftFailureWithRateLimitedFallbackReturnsSoft(t *testing.T) {
	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft","choices":[{"message":{"role":"assistant","content":"I can't help with that request."},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv.Close()

	rateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"late-success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer rateSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"rate-limited": {
			Endpoint:  rateSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 600},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	for i := 0; i < 10; i++ {
		if !rl.Allow("rate-limited", "model") {
			t.Fatalf("failed to exhaust rate-limit token at iteration %d", i)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	softReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, softSrv.URL, nil)
	rateReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, rateSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: softReq},
		{ProviderName: "rate-limited", Provider: providers["rate-limited"], UpstreamModel: "model", Request: rateReq},
	}

	result, err := strategy.Execute(ctx, tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "soft" {
		t.Fatalf("winner = %q, want %q", result.Winner, "soft")
	}
	if result.FailureKind != FailureKindSoft {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSoft)
	}
}

func TestFailoverStrategy_SoftFailureReturnsLastSoftFailureResponse(t *testing.T) {
	softSrv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft-1","choices":[{"message":{"role":"assistant","content":"blocked-1"},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv1.Close()

	softSrv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft-2","choices":[{"message":{"role":"assistant","content":"blocked-2"},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv2.Close()

	providers := map[string]config.ProviderConfig{
		"soft-1": {
			Endpoint:  softSrv1.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"soft-2": {
			Endpoint:  softSrv2.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv1.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv2.URL, nil)
	tasks := []Task{
		{ProviderName: "soft-1", Provider: providers["soft-1"], UpstreamModel: "model", Request: req1},
		{ProviderName: "soft-2", Provider: providers["soft-2"], UpstreamModel: "model", Request: req2},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "soft-2" {
		t.Fatalf("winner = %q, want %q", result.Winner, "soft-2")
	}
	if result.FailureKind != FailureKindSoft {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSoft)
	}
}

func TestFailoverStrategy_SoftFailureDoesNotResetOrIncreaseHealthCount(t *testing.T) {
	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft","choices":[{"message":{"role":"assistant","content":"blocked"},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv.Close()

	hardSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"upstream failed"}`))
	}))
	defer hardSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft-neutral": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)
	healthKey := health.MakeHealthKey("soft-neutral", "")

	h.ReportFailure(healthKey)

	softReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	softResult, err := strategy.Execute(context.Background(), []Task{{ProviderName: "soft-neutral", Provider: providers["soft-neutral"], UpstreamModel: "model", Request: softReq}})
	if err != nil {
		t.Fatalf("soft Execute failed: %v", err)
	}
	softResult.Response.Body.Close()

	if !h.IsHealthy(healthKey) {
		t.Fatal("soft failure should not make provider unhealthy")
	}

	hardReq1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, hardSrv.URL, nil)
	hardResult1, err := strategy.Execute(context.Background(), []Task{{ProviderName: "soft-neutral", Provider: providers["soft-neutral"], UpstreamModel: "model", Request: hardReq1}})
	if err != nil {
		t.Fatalf("first hard Execute failed: %v", err)
	}
	hardResult1.Response.Body.Close()

	if !h.IsHealthy(healthKey) {
		t.Fatal("soft failure should not add or reset failure count before threshold")
	}

	hardReq2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, hardSrv.URL, nil)
	hardResult2, err := strategy.Execute(context.Background(), []Task{{ProviderName: "soft-neutral", Provider: providers["soft-neutral"], UpstreamModel: "model", Request: hardReq2}})
	if err != nil {
		t.Fatalf("second hard Execute failed: %v", err)
	}
	hardResult2.Response.Body.Close()

	if h.IsHealthy(healthKey) {
		t.Fatal("provider should become unhealthy exactly after the second real failure")
	}
}

func TestFailoverStrategy_SoftFailurePhaseTwoDoesNotRetryExecutedProvider(t *testing.T) {
	softCalls := 0
	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		softCalls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft","choices":[{"message":{"role":"assistant","content":"I can't help with that request."},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv.Close()

	forcedCalls := 0
	forcedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forcedCalls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"forced","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer forcedSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"forced": {
			Endpoint:  forcedSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	h.ReportFailure(health.MakeHealthKey("forced", ""))
	h.ReportFailure(health.MakeHealthKey("forced", ""))
	h.ReportFailure(health.MakeHealthKey("forced", ""))
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	softReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	forcedReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, forcedSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: softReq},
		{ProviderName: "forced", Provider: providers["forced"], UpstreamModel: "model", Request: forcedReq},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "forced" {
		t.Fatalf("winner = %q, want %q", result.Winner, "forced")
	}
	if softCalls != 1 {
		t.Fatalf("soft provider called %d times, want 1", softCalls)
	}
	if forcedCalls != 1 {
		t.Fatalf("forced provider called %d times, want 1", forcedCalls)
	}
}

// TestFailoverStrategy_AttemptMetrics_402ThenSuccess 验证 402 硬失败后 failover 到成功的 attempt 指标
func TestFailoverStrategy_AttemptMetrics_402ThenSuccess(t *testing.T) {
	metrics.ResetForTest()

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"error":"insufficient quota"}`))
	}))
	defer failSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"fail": {
			Endpoint:  failSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, failSrv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)

	tasks := []Task{
		{ProviderName: "fail", Provider: providers["fail"], UpstreamModel: "fail-model", Request: req1},
		{ProviderName: "success", Provider: providers["success"], UpstreamModel: "success-model", Request: req2},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute should succeed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "success" {
		t.Fatalf("winner = %q, want %q", result.Winner, "success")
	}

	// 验证第一个 provider 的 402 硬失败 attempt 指标
	failCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"failover", "fail", "fail-model", "", "openai", "hard_failure", failureReasonTokenHubQuota, "402",
	))
	if failCount != 1 {
		t.Errorf("expected 1 hard_failure attempt for fail provider, got %f", failCount)
	}

	// 验证第二个 provider 的成功 attempt 指标
	successCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"failover", "success", "success-model", "", "openai", "success", "", "",
	))
	if successCount != 1 {
		t.Errorf("expected 1 success attempt for success provider, got %f", successCount)
	}
}

// TestFailoverStrategy_AttemptMetrics_SoftFailure 验证软失败的 attempt 指标
func TestFailoverStrategy_AttemptMetrics_SoftFailure(t *testing.T) {
	metrics.ResetForTest()

	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft","choices":[{"message":{"role":"assistant","content":"I can't help with that request."},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: req},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindSoft {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSoft)
	}

	// 验证软失败 attempt 指标
	softCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"failover", "soft", "model", "", "openai", "soft_failure", "content_filter_finish_reason", "200",
	))
	if softCount != 1 {
		t.Errorf("expected 1 soft_failure attempt, got %f", softCount)
	}
}

// TestFailoverStrategy_TokenHubQuotaError_ClassifyAndMarkUnhealthy 验证 TokenHub 额度错误
// 被正确分类为 hard_failure+tokenhub_quota_exceeded 并标记 provider 为 1 小时不健康
func TestFailoverStrategy_TokenHubQuotaError_ClassifyAndMarkUnhealthy(t *testing.T) {
	h := health.NewChecker(3, 30*time.Second)
	healthKey := health.MakeHealthKey("token-hub", "openai")

	// 构造一个模拟 TokenHub 额度错误的响应
	resp := newSchedulerHTTPResponse(http.StatusBadRequest, "application/json",
		`{"error":{"code":"401007","message":"quota exceeded"}}`)
	resp.Request = &http.Request{
		URL: mustParseURL("https://api.lkeap.cloud.tencent.com/plan/v1/chat/completions"),
	}

	taskReq := &http.Request{
		URL: mustParseURL("https://api.lkeap.cloud.tencent.com/plan/v1/chat/completions"),
	}

	// 模拟策略内部的处理流程：parseResponse -> applyTokenHubClassification -> applyHealthAction
	result, err := parseResponse(resp, "token-hub", "model", "openai", 500*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("parseResponse failed: %v", err)
	}
	applyTokenHubClassification(result, resp, taskReq)

	if result.FailureKind != FailureKindHard {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindHard)
	}
	if result.FailureReason != failureReasonTokenHubQuota {
		t.Fatalf("FailureReason = %q, want %q", result.FailureReason, failureReasonTokenHubQuota)
	}
	if result.HealthActionInfo.Action != HealthActionMarkUnhealthy {
		t.Fatalf("HealthAction = %q, want %q", result.HealthActionInfo.Action, HealthActionMarkUnhealthy)
	}
	if result.HealthActionInfo.CooldownOverride != quotaCooldown {
		t.Fatalf("CooldownOverride = %v, want %v", result.HealthActionInfo.CooldownOverride, quotaCooldown)
	}

	// 应用健康操作
	applyHealthAction(h, healthKey, result)

	// 验证 provider 被标记为 1 小时不健康
	if h.IsHealthy(healthKey) {
		t.Fatal("token-hub should be marked unhealthy after quota error")
	}
}

// TestFailoverStrategy_TokenHubQuotaError_FailoverToNext 验证 failover 模式下 TokenHub 返回
// 额度错误后，策略尝试下一个 provider
func TestFailoverStrategy_TokenHubQuotaError_FailoverToNext(t *testing.T) {
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"token-hub": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"fallback-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)

	// 预先标记 token-hub 为 1 小时不健康，模拟 TokenHub 额度错误后的状态
	healthKey := health.MakeHealthKey("token-hub", "openai")
	h.MarkUnhealthyFor(healthKey, quotaCooldown)

	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "token-hub", Provider: providers["token-hub"], OutboundProtocol: "openai", UpstreamModel: "model", Request: req},
		{ProviderName: "fallback-prov", Provider: providers["fallback-prov"], OutboundProtocol: "openai", UpstreamModel: "model", Request: req},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute should succeed via failover: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "fallback-prov" {
		t.Fatalf("winner = %q, want %q (should skip unhealthy token-hub)", result.Winner, "fallback-prov")
	}
	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
}

// tokenHubErrorTransport 返回一个 TokenHub 额度错误响应，resp.Request 为 nil
// 迫使 makeRequestTarget 回退到 task.Request.URL 做 host 识别
type tokenHubErrorTransport struct{}

func (t *tokenHubErrorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Status:     "401 Unauthorized",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"401007","message":"quota exceeded"}}`)),
		Request:    nil,
	}, nil
}

// TestFailoverStrategy_TokenHubQuotaError_URLRecognitionAndFailover 验证 failover 模式下
// 通过 task.Request.URL host 识别 TokenHub + 私有错误码分类 + 触发 1 小时不健康的完整链路。
// 本测试走策略 Execute 全路径，覆盖 makeRequestTarget → isTokenHubTarget → applyTokenHubClassification → applyHealthAction。
func TestFailoverStrategy_TokenHubQuotaError_URLRecognitionAndFailover(t *testing.T) {
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"token-hub": {
			Endpoint:  "https://api.lkeap.cloud.tencent.com/v1/chat/completions",
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"fallback-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	client.SetTransport("token-hub", &tokenHubErrorTransport{})

	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	// task.Request.URL 指向 TokenHub host，makeRequestTarget 通过 resp.Request==nil 回退时使用它
	tokenHubReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://api.lkeap.cloud.tencent.com/v1/chat/completions", nil)
	fallbackReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)

	tasks := []Task{
		{ProviderName: "token-hub", Provider: providers["token-hub"], OutboundProtocol: "openai", UpstreamModel: "model", Request: tokenHubReq},
		{ProviderName: "fallback-prov", Provider: providers["fallback-prov"], OutboundProtocol: "openai", UpstreamModel: "model", Request: fallbackReq},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "fallback-prov" {
		t.Fatalf("winner = %q, want %q", result.Winner, "fallback-prov")
	}
	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}

	// 验证 token-hub 因额度错误被标记为不健康
	healthKey := health.MakeHealthKey("token-hub", "openai")
	if h.IsHealthy(healthKey) {
		t.Fatal("token-hub should be marked unhealthy via MarkUnhealthyFor after TokenHub quota error")
	}
}

// TestFailoverStrategy_SkipDisabledProvider 验证 failover 跳过处于禁用时段的 provider，
// 自动尝试下一个可用 provider。
func TestFailoverStrategy_SkipDisabledProvider(t *testing.T) {
	disabledSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"disabled"}`))
	}))
	defer disabledSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success"}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"disabled-prov": {
			Endpoint:           disabledSrv.URL,
			APIKey:             "test-key",
			Protocols: []string{"openai"},
			RateLimit:          config.RateLimitConfig{QPM: 0},
			DisabledTimeRanges: []string{"00:00-24:00"}, // 全天禁用
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
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	disabledReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, disabledSrv.URL, nil)
	successReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)

	tasks := []Task{
		{ProviderName: "disabled-prov", Provider: providers["disabled-prov"], UpstreamModel: "model", Request: disabledReq},
		{ProviderName: "enabled-prov", Provider: providers["enabled-prov"], UpstreamModel: "model", Request: successReq},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute should succeed by skipping disabled provider: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "enabled-prov" {
		t.Errorf("winner = %q, want %q", result.Winner, "enabled-prov")
	}
}

// TestFailoverStrategy_AllDisabledReturnsError 验证所有候选 provider 均处于禁用时段时，
// failover 返回 ErrNoProviderAvailable。
func TestFailoverStrategy_AllDisabledReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"disabled-1": {
			Endpoint:           srv.URL,
			APIKey:             "test-key",
			Protocols: []string{"openai"},
			RateLimit:          config.RateLimitConfig{QPM: 0},
			DisabledTimeRanges: []string{"00:00-24:00"},
		},
		"disabled-2": {
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
	strategy := NewFailoverStrategy(client, rl, h, 500*time.Millisecond, 0)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)

	tasks := []Task{
		{ProviderName: "disabled-1", Provider: providers["disabled-1"], UpstreamModel: "model", Request: req1},
		{ProviderName: "disabled-2", Provider: providers["disabled-2"], UpstreamModel: "model", Request: req2},
	}

	_, err := strategy.Execute(context.Background(), tasks)
	if err != ErrNoProviderAvailable {
		t.Errorf("error = %v, want %v", err, ErrNoProviderAvailable)
	}
}
