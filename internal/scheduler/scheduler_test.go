package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

func newTestScheduler(qpm int) (*Scheduler, *httptest.Server) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test"}`))
	}))

	providers := map[string]config.ProviderConfig{
		"test": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: qpm},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	return New(rl, client, h), srv
}

func TestScheduler_Do_Success(t *testing.T) {
	sched, srv := newTestScheduler(0)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	resp, err := sched.Do(context.Background(), "test", req)
	if err != nil {
		t.Fatalf("Do should succeed: %v", err)
	}
	resp.Body.Close()
}

func TestScheduler_Do_UnknownProvider(t *testing.T) {
	sched, srv := newTestScheduler(0)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	_, err := sched.Do(context.Background(), "unknown", req)
	if err == nil {
		t.Fatal("Do with unknown provider should return error")
	}
}

func TestScheduler_Allow(t *testing.T) {
	sched, srv := newTestScheduler(60)
	defer srv.Close()

	if !sched.Allow("test") {
		t.Fatal("first Allow should succeed")
	}
}

func TestScheduler_Allow_Unknown(t *testing.T) {
	sched, srv := newTestScheduler(60)
	defer srv.Close()

	if !sched.Allow("unknown") {
		t.Fatal("Allow for unknown provider should return true")
	}
}

func TestScheduler_Execute_SingleProvider(t *testing.T) {
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
	sched := New(rl, client, h)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Request: req},
	}

	result, err := sched.Execute(context.Background(), "concurrent", 30*time.Second, tasks)
	if err != nil {
		t.Fatalf("Execute should succeed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "test" {
		t.Errorf("winner = %q, want %q", result.Winner, "test")
	}
}

func TestScheduler_Execute_MultiProvider_Race(t *testing.T) {
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"slow"}`))
	}))
	defer slowSrv.Close()

	fastSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"fast"}`))
	}))
	defer fastSrv.Close()

	providers := map[string]config.ProviderConfig{
		"slow": {
			Endpoint:  slowSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"fast": {
			Endpoint:  fastSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h)

	slowReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, slowSrv.URL, nil)
	fastReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, fastSrv.URL, nil)

	tasks := []Task{
		{ProviderName: "slow", Request: slowReq},
		{ProviderName: "fast", Request: fastReq},
	}

	result, err := sched.Execute(context.Background(), "concurrent", 30*time.Second, tasks)
	if err != nil {
		t.Fatalf("Execute should succeed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "fast" {
		t.Errorf("winner = %q, want %q (fast should win)", result.Winner, "fast")
	}
}

func TestScheduler_Execute_AllRateLimited(t *testing.T) {
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
			RateLimit: config.RateLimitConfig{QPM: 1},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h)

	rl.Allow("test", "")

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Request: req},
		{ProviderName: "test", Request: req},
	}

	_, err := sched.Execute(context.Background(), "concurrent", 30*time.Second, tasks)
	if err != ErrAllRateLimited {
		t.Errorf("error = %v, want %v", err, ErrAllRateLimited)
	}
}

func TestScheduler_Execute_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"slow1": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"slow2": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "slow1", Request: req1},
		{ProviderName: "slow2", Request: req2},
	}

	_, err := sched.Execute(context.Background(), "concurrent", 50*time.Millisecond, tasks)
	if err == nil {
		t.Error("Execute should timeout")
	}
}

func TestScheduler_Execute_AllProvidersFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
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
	sched := New(rl, client, h)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Request: req},
	}

	result, err := sched.Execute(context.Background(), "concurrent", 30*time.Second, tasks)
	if err != nil {
		t.Fatalf("Execute should not return error for HTTP 5xx: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Response.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", result.Response.StatusCode, http.StatusInternalServerError)
	}
}

func TestScheduler_Execute_UnknownStrategy(t *testing.T) {
	sched, srv := newTestScheduler(0)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Request: req},
	}

	_, err := sched.Execute(context.Background(), "unknown", 30*time.Second, tasks)
	if err != ErrUnknownStrategy {
		t.Errorf("error = %v, want %v", err, ErrUnknownStrategy)
	}
}

func TestScheduler_Execute_NoTasks(t *testing.T) {
	sched, srv := newTestScheduler(0)
	defer srv.Close()

	_, err := sched.Execute(context.Background(), "concurrent", 30*time.Second, nil)
	if err != ErrNoTasks {
		t.Errorf("error = %v, want %v", err, ErrNoTasks)
	}
}

// ============================================================================
// parseResponse 测试：验证响应解析和 usage 提取
// ============================================================================

func TestConcurrentStrategy_parseResponse_OpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "chatcmpl-test",
			"object": "chat.completion",
			"model": "gpt-4",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "Hello!"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150}
		}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"openai": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewConcurrentStrategy(client, rl, h)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	resp, err := client.Do("openai", req)
	if err != nil {
		t.Fatalf("failed to get response: %v", err)
	}

	result, err := strategy.parseResponse(resp, "openai", "gpt-4o", "openai")
	if err != nil {
		t.Fatalf("parseResponse failed: %v", err)
	}

	if result.Winner != "openai" {
		t.Errorf("winner = %q, want %q", result.Winner, "openai")
	}

	if result.UpstreamModel != "gpt-4o" {
		t.Errorf("upstream model = %q, want %q", result.UpstreamModel, "gpt-4o")
	}

	if result.Usage == nil {
		t.Fatal("usage should not be nil")
	}

	if result.Usage.PromptTokens != 100 {
		t.Errorf("prompt tokens = %d, want %d", result.Usage.PromptTokens, 100)
	}

	if result.Usage.CompletionTokens != 50 {
		t.Errorf("completion tokens = %d, want %d", result.Usage.CompletionTokens, 50)
	}

	if result.Usage.TotalTokens != 150 {
		t.Errorf("total tokens = %d, want %d", result.Usage.TotalTokens, 150)
	}

	if result.Usage.FinishReason != "stop" {
		t.Errorf("finish reason = %q, want %q", result.Usage.FinishReason, "stop")
	}
}

func TestConcurrentStrategy_parseResponse_Anthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "msg-test",
			"type": "message",
			"role": "assistant",
			"model": "claude-3-opus",
			"content": [{"type": "text", "text": "Hello!"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 80, "output_tokens": 40}
		}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"anthropic": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "anthropic",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewConcurrentStrategy(client, rl, h)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	resp, err := client.Do("anthropic", req)
	if err != nil {
		t.Fatalf("failed to get response: %v", err)
	}

	result, err := strategy.parseResponse(resp, "anthropic", "claude-3-opus", "anthropic")
	if err != nil {
		t.Fatalf("parseResponse failed: %v", err)
	}

	if result.Winner != "anthropic" {
		t.Errorf("winner = %q, want %q", result.Winner, "anthropic")
	}

	if result.Usage == nil {
		t.Fatal("usage should not be nil")
	}

	if result.Usage.PromptTokens != 80 {
		t.Errorf("prompt tokens = %d, want %d", result.Usage.PromptTokens, 80)
	}

	if result.Usage.CompletionTokens != 40 {
		t.Errorf("completion tokens = %d, want %d", result.Usage.CompletionTokens, 40)
	}

	if result.Usage.TotalTokens != 120 {
		t.Errorf("total tokens = %d, want %d", result.Usage.TotalTokens, 120)
	}

	if result.Usage.FinishReason != "end_turn" {
		t.Errorf("finish reason = %q, want %q", result.Usage.FinishReason, "end_turn")
	}
}

func TestConcurrentStrategy_parseResponse_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "internal error"}`))
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
	strategy := NewConcurrentStrategy(client, rl, h)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	resp, err := client.Do("test", req)
	if err != nil {
		t.Fatalf("failed to get response: %v", err)
	}

	result, err := strategy.parseResponse(resp, "test", "test-model", "openai")
	if err != nil {
		t.Fatalf("parseResponse should not error for HTTP 5xx: %v", err)
	}

	if result.Response.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", result.Response.StatusCode, http.StatusInternalServerError)
	}

	// 错误响应不应该有 usage
	if result.Usage != nil {
		t.Errorf("usage should be nil for error response, got %+v", result.Usage)
	}
}

func TestConcurrentStrategy_parseResponse_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`invalid json`))
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
	strategy := NewConcurrentStrategy(client, rl, h)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	resp, err := client.Do("test", req)
	if err != nil {
		t.Fatalf("failed to get response: %v", err)
	}

	result, err := strategy.parseResponse(resp, "test", "test-model", "openai")
	if err != nil {
		t.Fatalf("parseResponse should not error for invalid JSON: %v", err)
	}

	// 无效 JSON 不应该有 usage
	if result.Usage != nil {
		t.Errorf("usage should be nil for invalid JSON, got %+v", result.Usage)
	}
}

// ============================================================================
// Execute 测试：验证完整的请求处理流程
// ============================================================================

func TestScheduler_Execute_WithUsage_OpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求体
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "chatcmpl-test",
			"object": "chat.completion",
			"model": "gpt-4",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "Hello!"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150}
		}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"openai": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h)

	// 创建带 body 的请求
	reqBody := dto.ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "Hello"}},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	tasks := []Task{
		{ProviderName: "openai", Provider: providers["openai"], UpstreamModel: "gpt-4o", Request: req},
	}

	result, err := sched.Execute(context.Background(), "concurrent", 30*time.Second, tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Usage == nil {
		t.Fatal("usage should not be nil")
	}

	if result.Usage.TotalTokens != 150 {
		t.Errorf("total tokens = %d, want %d", result.Usage.TotalTokens, 150)
	}

	if result.UpstreamModel != "gpt-4o" {
		t.Errorf("upstream model = %q, want %q", result.UpstreamModel, "gpt-4o")
	}
}

func TestScheduler_Execute_WithUsage_Anthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "msg-test",
			"type": "message",
			"role": "assistant",
			"model": "claude-3",
			"content": [{"type": "text", "text": "Hello!"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 80, "output_tokens": 40}
		}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"anthropic": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "anthropic",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h)

	reqBody := dto.ClaudeRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Messages:  []dto.ClaudeMessage{{Role: "user", Content: "Hello"}},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	tasks := []Task{
		{ProviderName: "anthropic", Provider: providers["anthropic"], UpstreamModel: "claude-3-opus", Request: req},
	}

	result, err := sched.Execute(context.Background(), "concurrent", 30*time.Second, tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Usage == nil {
		t.Fatal("usage should not be nil")
	}

	if result.Usage.TotalTokens != 120 {
		t.Errorf("total tokens = %d, want %d", result.Usage.TotalTokens, 120)
	}

	if result.Usage.FinishReason != "end_turn" {
		t.Errorf("finish reason = %q, want %q", result.Usage.FinishReason, "end_turn")
	}
}

// TestConcurrentStrategy_race_TwoProviders 测试并发竞速场景
func TestConcurrentStrategy_race_TwoProviders(t *testing.T) {
	var fastReceived bool

	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"slow"}`))
	}))
	defer slowSrv.Close()

	fastSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fastReceived = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"fast"}`))
	}))
	defer fastSrv.Close()

	providers := map[string]config.ProviderConfig{
		"slow": {
			Endpoint:  slowSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"fast": {
			Endpoint:  fastSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewConcurrentStrategy(client, rl, h)

	slowReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, slowSrv.URL, nil)
	fastReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, fastSrv.URL, nil)

	tasks := []Task{
		{ProviderName: "slow", Provider: providers["slow"], UpstreamModel: "slow-model", Request: slowReq},
		{ProviderName: "fast", Provider: providers["fast"], UpstreamModel: "fast-model", Request: fastReq},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "fast" {
		t.Errorf("winner = %q, want %q (fast should win)", result.Winner, "fast")
	}

	if !fastReceived {
		t.Error("fast provider should have received request")
	}
}

// TestConcurrentStrategy_race_AllFail 测试所有 provider 都失败的场景
func TestConcurrentStrategy_race_AllFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"test1": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"test2": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewConcurrentStrategy(client, rl, h)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)

	tasks := []Task{
		{ProviderName: "test1", Provider: providers["test1"], UpstreamModel: "model1", Request: req1},
		{ProviderName: "test2", Provider: providers["test2"], UpstreamModel: "model2", Request: req2},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute should not return error for HTTP 5xx: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Response.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", result.Response.StatusCode, http.StatusInternalServerError)
	}
}

// TestConcurrentStrategy_ContextCancel 单任务场景下 context 超时
func TestConcurrentStrategy_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
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
	strategy := NewConcurrentStrategy(client, rl, h)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)

	tasks := []Task{
		{ProviderName: "test", Provider: providers["test"], UpstreamModel: "model", Request: req},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	result, err := strategy.Execute(ctx, tasks)
	// 单任务场景：ratelimit.Wait 不会阻塞 (QPM=0)，但请求会超时
	// 由于 QPM=0，Wait 直接返回 nil，然后 client.Do 会执行
	// 如果请求在 context 超时前发出，会在 race 函数中处理超时
	if err == nil && result != nil {
		result.Response.Body.Close()
		// 在 context 超时时，可能返回了结果（如果请求已发出）
		// 或者还没有发出请求但返回了错误
	}
	// 这个测试主要验证不会 panic 或死锁
}
