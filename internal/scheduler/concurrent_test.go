package scheduler

import (
	"context"
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

func newConcurrentStrategyTest(t *testing.T, providers map[string]config.ProviderConfig) (*ConcurrentStrategy, *health.Checker) {
	t.Helper()

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	return NewConcurrentStrategy(client, rl, h, 500*time.Millisecond, 0), h
}

func newProbeReadFailureServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}

		conn, rw, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("Hijack failed: %v", err)
			return
		}
		defer conn.Close()

		_, _ = rw.WriteString("HTTP/1.1 200 OK\r\n")
		_, _ = rw.WriteString("Content-Type: text/event-stream\r\n")
		_, _ = rw.WriteString("Content-Length: 64\r\n")
		_, _ = rw.WriteString("\r\n")
		_, _ = rw.WriteString("data: {\"choices\":[")
		_ = rw.Flush()
	}))
}

func TestConcurrentStrategy_ProbeReadFailureMarksProviderUnhealthy(t *testing.T) {
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
	strategy := NewConcurrentStrategy(client, rl, h, 500*time.Millisecond, 0)

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

func TestConcurrentStrategy_SoftFailure_StreamProbeWaitsForSlowerSuccess(t *testing.T) {
	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer softSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
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

	strategy, _ := newConcurrentStrategyTest(t, providers)

	softReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	successReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)

	result, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: softReq},
		{ProviderName: "success", Provider: providers["success"], UpstreamModel: "model", Request: successReq},
	})
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

	body := string(readSchedulerResponseBody(t, result.Response))
	if !strings.Contains(body, "\"content\":\"ok\"") {
		t.Fatalf("success stream body missing winning content: %q", body)
	}
}

func TestConcurrentStrategy_SoftFailure_AllSoftReturnsLastFallback(t *testing.T) {
	softSrv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"soft-1","choices":[{"message":{"role":"assistant","content":"blocked-1"},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv1.Close()

	softSrv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"soft-2","choices":[{"message":{"role":"assistant","content":"blocked-2"},"finish_reason":"content_filter"}]}`))
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

	strategy, _ := newConcurrentStrategyTest(t, providers)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv1.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv2.URL, nil)

	result, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "soft-1", Provider: providers["soft-1"], UpstreamModel: "model", Request: req1},
		{ProviderName: "soft-2", Provider: providers["soft-2"], UpstreamModel: "model", Request: req2},
	})
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

	body := string(readSchedulerResponseBody(t, result.Response))
	if !strings.Contains(body, `"id":"soft-2"`) {
		t.Fatalf("soft fallback body = %q, want response from last soft failure", body)
	}
}

func TestConcurrentStrategy_SoftFailure_HardFailureOutranksSoftFailure(t *testing.T) {
	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"soft","choices":[{"message":{"role":"assistant","content":"blocked"},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv.Close()

	hardSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"upstream failed"}`))
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

	strategy, _ := newConcurrentStrategyTest(t, providers)

	softReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	hardReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, hardSrv.URL, nil)

	result, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: softReq},
		{ProviderName: "hard", Provider: providers["hard"], UpstreamModel: "model", Request: hardReq},
	})
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

	body := string(readSchedulerResponseBody(t, result.Response))
	if !strings.Contains(body, "upstream failed") {
		t.Fatalf("hard failure body = %q, want upstream error body", body)
	}
}

func TestConcurrentStrategy_HardFailure_ReturnsLastHardFailure(t *testing.T) {
	hardSrv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"hard-1"}`))
	}))
	defer hardSrv1.Close()

	hardSrv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"hard-2"}`))
	}))
	defer hardSrv2.Close()

	providers := map[string]config.ProviderConfig{
		"hard-1": {
			Endpoint:  hardSrv1.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"hard-2": {
			Endpoint:  hardSrv2.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	strategy, _ := newConcurrentStrategyTest(t, providers)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, hardSrv1.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, hardSrv2.URL, nil)
	result, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "hard-1", Provider: providers["hard-1"], UpstreamModel: "model", Request: req1},
		{ProviderName: "hard-2", Provider: providers["hard-2"], UpstreamModel: "model", Request: req2},
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "hard-2" {
		t.Fatalf("winner = %q, want %q", result.Winner, "hard-2")
	}
	if result.FailureKind != FailureKindHard {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindHard)
	}
	if result.Response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", result.Response.StatusCode, http.StatusBadGateway)
	}

	body := string(readSchedulerResponseBody(t, result.Response))
	if !strings.Contains(body, "hard-2") {
		t.Fatalf("hard failure body = %q, want last hard failure body", body)
	}
}

func TestConcurrentStrategy_ContentFilter_LegacyOpenAIResponseSingleProviderDoesNotRecoverHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"status":"incomplete",
			"output":[],
			"incomplete_details":{"reason":"content_filter"},
			"usage":{"input_tokens":4,"output_tokens":0,"total_tokens":4}
		}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"token-hub": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	strategy, h := newConcurrentStrategyTest(t, providers)
	respKey := health.MakeHealthKey("token-hub", "openai.response")
	h.ReportFailure(respKey)
	h.ReportFailure(respKey)
	h.ReportFailure(respKey)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	result, err := strategy.Execute(context.Background(), []Task{{
		ProviderName:     "token-hub",
		Provider:         providers["token-hub"],
		UpstreamModel:    "resp-model",
		OutboundProtocol: "openai.response",
		Request:          req,
	}})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindSoft {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSoft)
	}
	if result.FailureReason != failureReasonContentFilterDetail {
		t.Fatalf("FailureReason = %q, want %q", result.FailureReason, failureReasonContentFilterDetail)
	}
	if h.IsHealthy(respKey) {
		t.Fatal("soft content-filter result should not recover unhealthy provider in single-provider fast path")
	}
}

// TestConcurrentStrategy_AttemptMetrics_Success 验证成功场景的 attempt 指标
func TestConcurrentStrategy_AttemptMetrics_Success(t *testing.T) {
	metrics.ResetForTest()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	strategy, _ := newConcurrentStrategyTest(t, providers)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)
	result, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "success", Provider: providers["success"], UpstreamModel: "model", Request: req},
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	// 验证成功 attempt 指标
	successCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"concurrent", "success", "model", "", "openai", "success", "", "",
	))
	if successCount != 1 {
		t.Errorf("expected 1 success attempt, got %f", successCount)
	}
}

// TestConcurrentStrategy_AttemptMetrics_HardFailure 验证硬失败的 attempt 指标
func TestConcurrentStrategy_AttemptMetrics_HardFailure(t *testing.T) {
	metrics.ResetForTest()

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error":"upstream failed"}`))
	}))
	defer failSrv.Close()

	providers := map[string]config.ProviderConfig{
		"fail": {
			Endpoint:  failSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	strategy, _ := newConcurrentStrategyTest(t, providers)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, failSrv.URL, nil)
	result, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "fail", Provider: providers["fail"], UpstreamModel: "fail-model", Request: req},
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	// 验证硬失败 attempt 指标
	failCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"concurrent", "fail", "fail-model", "", "openai", "hard_failure", "http_status", "502",
	))
	if failCount != 1 {
		t.Errorf("expected 1 hard_failure attempt, got %f", failCount)
	}
}

// TestConcurrentStrategy_AttemptMetrics_SoftFailure 验证软失败的 attempt 指标
func TestConcurrentStrategy_AttemptMetrics_SoftFailure(t *testing.T) {
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

	strategy, _ := newConcurrentStrategyTest(t, providers)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	result, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: req},
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindSoft {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSoft)
	}

	// 验证软失败 attempt 指标
	softCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"concurrent", "soft", "model", "", "openai", "soft_failure", "content_filter_finish_reason", "200",
	))
	if softCount != 1 {
		t.Errorf("expected 1 soft_failure attempt, got %f", softCount)
	}
}

// TestConcurrentStrategy_AttemptMetrics_Canceled 验证取消场景的 attempt 指标
func TestConcurrentStrategy_AttemptMetrics_Canceled(t *testing.T) {
	metrics.ResetForTest()

	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"slow"}`))
	}))
	defer slowSrv.Close()

	fastSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"fast","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
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

	strategy, _ := newConcurrentStrategyTest(t, providers)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, slowSrv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, fastSrv.URL, nil)

	result, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "slow", Provider: providers["slow"], UpstreamModel: "slow-model", Request: req1},
		{ProviderName: "fast", Provider: providers["fast"], UpstreamModel: "fast-model", Request: req2},
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "fast" {
		t.Fatalf("winner = %q, want %q", result.Winner, "fast")
	}

	// 验证快速 provider 的成功 attempt 指标
	fastSuccessCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"concurrent", "fast", "fast-model", "", "openai", "success", "", "",
	))
	if fastSuccessCount != 1 {
		t.Errorf("expected exactly 1 success attempt for fast provider, got %f", fastSuccessCount)
	}

	// 验证慢速 provider 的 attempt 指标
	// 由于并发竞速，慢速 provider 可能被取消或成功完成
	slowCancelCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"concurrent", "slow", "slow-model", "", "openai", "canceled", "context_canceled", "",
	))
	slowSuccessCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"concurrent", "slow", "slow-model", "", "openai", "success", "", "",
	))

	// 验证没有双重记录：不应该同时存在 canceled 和 success
	if slowCancelCount > 0 && slowSuccessCount > 0 {
		t.Errorf("double recording detected: slow provider has both canceled (%f) and success (%f) attempts",
			slowCancelCount, slowSuccessCount)
	}

	// 验证如果被取消，计数恰好为 1
	if slowCancelCount > 1 {
		t.Errorf("expected at most 1 canceled attempt for slow provider, got %f", slowCancelCount)
	}

	// 验证如果成功完成，计数恰好为 1
	if slowSuccessCount > 1 {
		t.Errorf("expected at most 1 success attempt for slow provider, got %f", slowSuccessCount)
	}

	// 验证总计数不超过 1（要么 canceled 要么 success，不能两者都有）
	totalSlowAttempts := slowCancelCount + slowSuccessCount
	if totalSlowAttempts > 1 {
		t.Errorf("expected at most 1 total attempt for slow provider, got %f (canceled=%f, success=%f)",
			totalSlowAttempts, slowCancelCount, slowSuccessCount)
	}
}

// TestConcurrentStrategy_TokenHubQuotaError_OtherProviderWins 验证并发竞速模式下
// 一个 provider 返回 TokenHub 额度错误时，其他 provider 仍能获胜
func TestConcurrentStrategy_TokenHubQuotaError_OtherProviderWins(t *testing.T) {
	quotaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":"401007","message":"quota exceeded"}}`))
	}))
	defer quotaSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"token-hub": {
			Endpoint:  quotaSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"fast-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	strategy, h := newConcurrentStrategyTest(t, providers)

	quotaReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, quotaSrv.URL, nil)
	successReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)

	result, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "token-hub", Provider: providers["token-hub"], UpstreamModel: "model", Request: quotaReq},
		{ProviderName: "fast-prov", Provider: providers["fast-prov"], UpstreamModel: "model", Request: successReq},
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "fast-prov" {
		t.Fatalf("winner = %q, want %q (fast-prov should win when token-hub returns quota error)", result.Winner, "fast-prov")
	}
	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}

	// 验证 token-hub 未被标记为不健康（因为 isTokenHubTarget 检查失败 ——
	// 测试服务器 URL 不匹配 TokenHub 的已知 host）
	healthKey := health.MakeHealthKey("token-hub", "openai")
	if !h.IsHealthy(healthKey) {
		t.Fatal("token-hub should remain healthy in test (URL doesn't match TokenHub pattern)")
	}
}

// TestConcurrentStrategy_FiltersOutDisabledCandidates 验证 concurrent 多候选时过滤掉禁用时段的 provider，
// 仅对可用候选发请求。
func TestConcurrentStrategy_FiltersOutDisabledCandidates(t *testing.T) {
	disabledSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"disabled","choices":[{"message":{"role":"assistant","content":"disabled"},"finish_reason":"stop"}]}`))
	}))
	defer disabledSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"disabled-prov": {
			Endpoint:           disabledSrv.URL,
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

	strategy, _ := newConcurrentStrategyTest(t, providers)

	disabledReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, disabledSrv.URL, nil)
	successReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)

	result, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "disabled-prov", Provider: providers["disabled-prov"], UpstreamModel: "model", Request: disabledReq},
		{ProviderName: "enabled-prov", Provider: providers["enabled-prov"], UpstreamModel: "model", Request: successReq},
	})
	if err != nil {
		t.Fatalf("Execute should succeed with non-disabled provider: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "enabled-prov" {
		t.Errorf("winner = %q, want %q", result.Winner, "enabled-prov")
	}
}

// TestConcurrentStrategy_SingleDisabledProviderReturnsError 验证 concurrent 单候选
// 处于禁用时段时，返回 ErrNoProviderAvailable。
func TestConcurrentStrategy_SingleDisabledProviderReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"disabled-only": {
			Endpoint:           srv.URL,
			APIKey:             "test-key",
			Protocols: []string{"openai"},
			RateLimit:          config.RateLimitConfig{QPM: 0},
			DisabledTimeRanges: []string{"00:00-24:00"},
		},
	}

	strategy, _ := newConcurrentStrategyTest(t, providers)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	_, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "disabled-only", Provider: providers["disabled-only"], UpstreamModel: "model", Request: req},
	})
	if err != ErrNoProviderAvailable {
		t.Errorf("error = %v, want %v", err, ErrNoProviderAvailable)
	}
}
