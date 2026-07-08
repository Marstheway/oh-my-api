package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: qpm},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	return New(rl, client, h, 500*time.Millisecond, 0), srv
}

func newSchedulerHTTPResponse(status int, contentType, body string) *http.Response {
	header := make(http.Header)
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func readSchedulerResponseBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	return body
}

func mustParseURL(rawURL string) *url.URL {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	return u
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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h, 500*time.Millisecond, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Request: req},
	}

	result, err := sched.Execute(context.Background(), "concurrent", tasks)
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

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h, 500*time.Millisecond, 0)

	slowReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, slowSrv.URL, nil)
	fastReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, fastSrv.URL, nil)

	tasks := []Task{
		{ProviderName: "slow", Request: slowReq},
		{ProviderName: "fast", Request: fastReq},
	}

	result, err := sched.Execute(context.Background(), "concurrent", tasks)
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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 1},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h, 500*time.Millisecond, 0)

	rl.Allow("test", "")

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Request: req},
		{ProviderName: "test", Request: req},
	}

	_, err := sched.Execute(context.Background(), "concurrent", tasks)
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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"slow2": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h, 500*time.Millisecond, 0)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "slow1", Request: req1},
		{ProviderName: "slow2", Request: req2},
	}

	_, err := sched.Execute(context.Background(), "concurrent", tasks)
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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h, 500*time.Millisecond, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Request: req},
	}

	result, err := sched.Execute(context.Background(), "concurrent", tasks)
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

	_, err := sched.Execute(context.Background(), "unknown", tasks)
	if err != ErrUnknownStrategy {
		t.Errorf("error = %v, want %v", err, ErrUnknownStrategy)
	}
}

func TestScheduler_Execute_NoTasks(t *testing.T) {
	sched, srv := newTestScheduler(0)
	defer srv.Close()

	_, err := sched.Execute(context.Background(), "concurrent", nil)
	if err != ErrNoTasks {
		t.Errorf("error = %v, want %v", err, ErrNoTasks)
	}
}

// ============================================================================
// parseResponse / classifyResponse / probeStreamPrefix 测试
// ============================================================================

func TestParseResponse_ClassifiesNonStreamSoftFailures(t *testing.T) {
	tests := []struct {
		name           string
		protocol       string
		body           string
		wantFailure    FailureKind
		wantReason     string
		wantUsageTotal int
	}{
		{
			name:     "openai chat finish_reason content_filter",
			protocol: "openai",
			body: `{
				"choices": [{"message": {"role": "assistant", "content": "I can't help with that request."}, "finish_reason": "content_filter"}],
				"usage": {"prompt_tokens": 10, "completion_tokens": 0, "total_tokens": 10}
			}`,
			wantFailure:    FailureKindSoft,
			wantReason:     "content_filter_finish_reason",
			wantUsageTotal: 10,
		},
		{
			name:     "openai responses incomplete_details content_filter",
			protocol: "openai.responses",
			body: `{
				"status": "incomplete",
				"output": [{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "I can't help with that request."}]}],
				"incomplete_details": {"reason": "content_filter"},
				"usage": {"input_tokens": 4, "output_tokens": 0, "total_tokens": 4}
			}`,
			wantFailure:    FailureKindSoft,
			wantReason:     "content_filter_incomplete",
			wantUsageTotal: 4,
		},
		{
			name:     "anthropic stop_reason content_filter",
			protocol: "anthropic",
			body: `{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "text", "text": "I can't help with that request."}],
				"stop_reason": "content_filter",
				"usage": {"input_tokens": 6, "output_tokens": 0}
			}`,
			wantFailure:    FailureKindSoft,
			wantReason:     "content_filter_stop_reason",
			wantUsageTotal: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := newSchedulerHTTPResponse(http.StatusOK, "application/json", tt.body)

			result, err := parseResponse(resp, "winner", "model", tt.protocol, 500*time.Millisecond, 0)
			if err != nil {
				t.Fatalf("parseResponse failed: %v", err)
			}
			defer result.Response.Body.Close()

			if result.FailureKind != tt.wantFailure {
				t.Fatalf("FailureKind = %q, want %q", result.FailureKind, tt.wantFailure)
			}
			if result.FailureReason != tt.wantReason {
				t.Fatalf("FailureReason = %q, want %q", result.FailureReason, tt.wantReason)
			}
			if result.Usage == nil {
				t.Fatal("usage should not be nil")
			}
			if result.Usage.TotalTokens != tt.wantUsageTotal {
				t.Fatalf("TotalTokens = %d, want %d", result.Usage.TotalTokens, tt.wantUsageTotal)
			}

			restoredBody := readSchedulerResponseBody(t, result.Response)
			if string(restoredBody) != tt.body {
				t.Fatalf("restored body mismatch: got %q want %q", string(restoredBody), tt.body)
			}
		})
	}
}

func TestParseResponse_ProbesStreamSoftFailures(t *testing.T) {
	tests := []struct {
		name       string
		protocol   string
		body       string
		wantReason string
	}{
		{
			name:       "openai chat stream finish_reason content_filter",
			protocol:   "openai",
			body:       "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n",
			wantReason: "content_filter_finish_reason",
		},
		{
			name:       "openai responses stream incomplete_details content_filter",
			protocol:   "openai.response",
			body:       "data: {\"type\":\"response.completed\",\"response\":{\"incomplete_details\":{\"reason\":\"content_filter\"}}}\n\n",
			wantReason: "content_filter_incomplete",
		},
		{
			name:       "anthropic stream stop_reason content_filter",
			protocol:   "anthropic.messages",
			body:       "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"content_filter\"}}\n\n",
			wantReason: "content_filter_stop_reason",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := newSchedulerHTTPResponse(http.StatusOK, "text/event-stream", tt.body)

			result, err := parseResponse(resp, "winner", "model", tt.protocol, 500*time.Millisecond, 0)
			if err != nil {
				t.Fatalf("parseResponse failed: %v", err)
			}
			defer result.Response.Body.Close()

			if result.FailureKind != FailureKindSoft {
				t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSoft)
			}
			if result.FailureReason != tt.wantReason {
				t.Fatalf("FailureReason = %q, want %q", result.FailureReason, tt.wantReason)
			}

			body := readSchedulerResponseBody(t, result.Response)
			if string(body) != tt.body {
				t.Fatalf("soft-failed stream body mismatch: got %q want %q", string(body), tt.body)
			}
		})
	}
}

func TestClassifyResponse_DetectsAssistantRefusalTextOnly(t *testing.T) {
	softBody := []byte(`{
		"choices": [{"message": {"role": "assistant", "content": "I can't help with that request."}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
	}`)

	kind, reason := classifyResponse("openai", softBody, nil)
	if kind != FailureKindSoft {
		t.Fatalf("FailureKind = %q, want %q", kind, FailureKindSoft)
	}
	if reason != "content_filter_phrase" {
		t.Fatalf("FailureReason = %q, want %q", reason, "content_filter_phrase")
	}

	chineseSoftBody := []byte(`{
		"choices": [{"message": {"role": "assistant", "content": "你好，我无法给到相关内容。"}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
	}`)

	kind, reason = classifyResponse("openai", chineseSoftBody, nil)
	if kind != FailureKindSoft {
		t.Fatalf("Chinese refusal FailureKind = %q, want %q", kind, FailureKindSoft)
	}
	if reason != "content_filter_phrase" {
		t.Fatalf("Chinese refusal FailureReason = %q, want %q", reason, "content_filter_phrase")
	}

	hardcodedPhraseOutsideAssistant := []byte(`{
		"choices": [{"message": {"role": "assistant", "content": "Here is a normal answer."}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		"note": "I can't help with that request."
	}`)

	kind, reason = classifyResponse("openai", hardcodedPhraseOutsideAssistant, nil)
	if kind != FailureKindSuccess {
		t.Fatalf("non-assistant phrase should stay success, got %q", kind)
	}
	if reason != "" {
		t.Fatalf("non-assistant phrase should not produce reason, got %q", reason)
	}
}

func TestClassifyResponse_DetectsChineseStreamRefusalText(t *testing.T) {
	kind, reason := classifyResponse("openai", nil, []string{
		`{"choices":[{"delta":{"content":"你好，我无法给到相关内容。"},"finish_reason":null}]}`,
	})
	if kind != FailureKindSoft {
		t.Fatalf("FailureKind = %q, want %q", kind, FailureKindSoft)
	}
	if reason != failureReasonContentFilterPhrase {
		t.Fatalf("FailureReason = %q, want %q", reason, failureReasonContentFilterPhrase)
	}
}

func TestClassifyResponse_InvalidJSONIsNotSoftFailure(t *testing.T) {
	kind, reason := classifyResponse("openai", []byte(`not-json`), nil)
	if kind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", kind, FailureKindSuccess)
	}
	if reason != "" {
		t.Fatalf("FailureReason = %q, want empty", reason)
	}
}

func TestProbeStreamPrefix_ReleasesAmbiguousStreamUnchanged(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}\n\n" +
		"data: [DONE]\n\n"
	resp := newSchedulerHTTPResponse(http.StatusOK, "text/event-stream", body)

	kind, reason, _, err := probeStreamPrefix(resp, "openai", 500*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("probeStreamPrefix failed: %v", err)
	}
	defer resp.Body.Close()

	if kind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", kind, FailureKindSuccess)
	}
	if reason != "" {
		t.Fatalf("FailureReason = %q, want empty", reason)
	}

	releasedBody := readSchedulerResponseBody(t, resp)
	if string(releasedBody) != body {
		t.Fatalf("released body mismatch: got %q want %q", string(releasedBody), body)
	}
}

func TestProbeStreamPrefix_DrainsReadyEventsBeforePrefixLimitRelease(t *testing.T) {
	payload := "{\"id\":\"" + strings.Repeat("a", streamProbeMaxPrefixBytes) + "\",\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}"
	body := "data: " + payload + "\n\n"

	for i := 0; i < 20; i++ {
		resp := newSchedulerHTTPResponse(http.StatusOK, "text/event-stream", body)

		kind, reason, _, err := probeStreamPrefix(resp, "openai", 500*time.Millisecond, 0)
		if err != nil {
			resp.Body.Close()
			t.Fatalf("probeStreamPrefix failed: %v", err)
		}

		releasedBody := readSchedulerResponseBody(t, resp)
		resp.Body.Close()

		if kind != FailureKindSoft {
			t.Fatalf("iteration %d: FailureKind = %q, want %q", i, kind, FailureKindSoft)
		}
		if reason != failureReasonContentFilterFinish {
			t.Fatalf("iteration %d: FailureReason = %q, want %q", i, reason, failureReasonContentFilterFinish)
		}
		if string(releasedBody) != body {
			t.Fatalf("iteration %d: released body mismatch: got %q want %q", i, string(releasedBody), body)
		}
	}
}

func TestProbeStreamPrefix_CloseAbortsSilentSoftFailedUpstream(t *testing.T) {
	requestClosed := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}

		<-r.Context().Done()
		requestClosed <- struct{}{}
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"soft": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	resp, err := client.Do("soft", req)
	if err != nil {
		t.Fatalf("client.Do failed: %v", err)
	}

	result, err := parseResponse(resp, "soft", "model", "openai", 500*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("parseResponse failed: %v", err)
	}
	if result.FailureKind != FailureKindSoft {
		result.Response.Body.Close()
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSoft)
	}

	if err := result.Response.Body.Close(); err != nil {
		t.Fatalf("closing soft-failure body failed: %v", err)
	}

	select {
	case <-requestClosed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("closing soft-failure body did not abort silent upstream stream")
	}
}

func TestConcurrentStrategy_Execute_UsesOutboundProtocolForClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"status": "incomplete",
			"output": [],
			"incomplete_details": {"reason": "content_filter"},
			"usage": {"input_tokens": 4, "output_tokens": 0, "total_tokens": 4}
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

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewConcurrentStrategy(client, rl, h, 500*time.Millisecond, 0)

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
}

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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewConcurrentStrategy(client, rl, h, 500*time.Millisecond, 0)

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
			Protocols: []string{"anthropic"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewConcurrentStrategy(client, rl, h, 500*time.Millisecond, 0)

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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewConcurrentStrategy(client, rl, h, 500*time.Millisecond, 0)

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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewConcurrentStrategy(client, rl, h, 500*time.Millisecond, 0)

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

func TestConcurrentStrategy_parseResponse_StreamDoesNotPreReadBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
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

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewConcurrentStrategy(client, rl, h, 500*time.Millisecond, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	resp, err := client.Do("test", req)
	if err != nil {
		t.Fatalf("failed to get response: %v", err)
	}

	result, err := strategy.parseResponse(resp, "test", "test-model", "openai")
	if err != nil {
		t.Fatalf("parseResponse failed: %v", err)
	}

	body, err := io.ReadAll(result.Response.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	if !bytes.Contains(body, []byte("data: [DONE]")) {
		t.Fatalf("stream body was consumed by scheduler, got: %q", string(body))
	}

	if result.Usage != nil {
		t.Fatalf("usage should be nil for stream response, got: %+v", result.Usage)
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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h, 500*time.Millisecond, 0)

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

	result, err := sched.Execute(context.Background(), "concurrent", tasks)
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
			Protocols: []string{"anthropic"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h, 500*time.Millisecond, 0)

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

	result, err := sched.Execute(context.Background(), "concurrent", tasks)
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

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewConcurrentStrategy(client, rl, h, 500*time.Millisecond, 0)

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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"test2": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewConcurrentStrategy(client, rl, h, 500*time.Millisecond, 0)

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
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 50*time.Millisecond, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewConcurrentStrategy(client, rl, h, 500*time.Millisecond, 0)

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

// ============================================================================
// Soft Failure Fallback 测试：验证软失败门控 fallback 逻辑
// ============================================================================

// ============================================================================
// TokenHub 错误分类测试
// ============================================================================

func TestClassifyTokenHubError_QuotaErrorCodes(t *testing.T) {
	quotaCodes := []string{"401007", "401008", "403004", "20097"}

	for _, code := range quotaCodes {
		t.Run("quota_error_"+code, func(t *testing.T) {
			target := requestTarget{
				host: "api.lkeap.cloud.tencent.com",
				path: "/plan/v1/chat/completions",
			}
			resp := newSchedulerHTTPResponse(http.StatusOK, "application/json",
				`{"error":{"code":"`+code+`","message":"quota exceeded"}}`)
			resp.Request = &http.Request{
				URL: mustParseURL("https://api.lkeap.cloud.tencent.com/plan/v1/chat/completions"),
			}

			info := classifyTokenHubError(target, resp)

			if info.FailureKind != FailureKindHard {
				t.Fatalf("FailureKind = %q, want %q", info.FailureKind, FailureKindHard)
			}
			if info.HealthActionInfo.Action != HealthActionMarkUnhealthy {
				t.Fatalf("HealthAction = %q, want %q", info.HealthActionInfo.Action, HealthActionMarkUnhealthy)
			}
			if info.HealthActionInfo.CooldownOverride != quotaCooldown {
				t.Fatalf("CooldownOverride = %v, want %v", info.HealthActionInfo.CooldownOverride, quotaCooldown)
			}
			if info.FailureReason != "tokenhub_quota_exceeded" {
				t.Fatalf("FailureReason = %q, want %q", info.FailureReason, "tokenhub_quota_exceeded")
			}
		})
	}
}

func TestClassifyTokenHubError_ContentFilter451001(t *testing.T) {
	target := requestTarget{
		host: "api.lkeap.cloud.tencent.com",
		path: "/plan/v1/chat/completions",
	}
	resp := newSchedulerHTTPResponse(http.StatusOK, "application/json",
		`{"error":{"code":"451001","message":"content filtered"}}`)
	resp.Request = &http.Request{
		URL: mustParseURL("https://api.lkeap.cloud.tencent.com/plan/v1/chat/completions"),
	}

	info := classifyTokenHubError(target, resp)

	if info.FailureKind != FailureKindSoft {
		t.Fatalf("FailureKind = %q, want %q", info.FailureKind, FailureKindSoft)
	}
	if info.HealthActionInfo.Action != HealthActionNone {
		t.Fatalf("HealthAction = %q, want %q", info.HealthActionInfo.Action, HealthActionNone)
	}
	if info.FailureReason != "tokenhub_content_filter" {
		t.Fatalf("FailureReason = %q, want %q", info.FailureReason, "tokenhub_content_filter")
	}
}

func TestClassifyTokenHubError_HTTP429NoHealthEffect(t *testing.T) {
	target := requestTarget{
		host: "api.lkeap.cloud.tencent.com",
		path: "/plan/v1/chat/completions",
	}
	resp := newSchedulerHTTPResponse(http.StatusTooManyRequests, "application/json",
		`{"error":{"code":"429","message":"rate limited"}}`)
	resp.Request = &http.Request{
		URL: mustParseURL("https://api.lkeap.cloud.tencent.com/plan/v1/chat/completions"),
	}

	info := classifyTokenHubError(target, resp)

	if info.FailureKind != FailureKindHard {
		t.Fatalf("FailureKind = %q, want %q", info.FailureKind, FailureKindHard)
	}
	if info.HealthActionInfo.Action != HealthActionNone {
		t.Fatalf("HealthAction = %q, want %q (429 should not mark unhealthy)", info.HealthActionInfo.Action, HealthActionNone)
	}
	if info.FailureReason != "http_status" {
		t.Fatalf("FailureReason = %q, want %q", info.FailureReason, "http_status")
	}
}

func TestClassifyTokenHubError_NonTokenHubProviderRemainsDefault(t *testing.T) {
	target := requestTarget{
		host: "api.openai.com",
		path: "/v1/chat/completions",
	}
	resp := newSchedulerHTTPResponse(http.StatusBadRequest, "application/json",
		`{"error":{"code":"401007","message":"error"}}`)
	resp.Request = &http.Request{
		URL: mustParseURL("https://api.openai.com/v1/chat/completions"),
	}

	info := classifyTokenHubError(target, resp)

	if info.FailureKind != FailureKindHard {
		t.Fatalf("FailureKind = %q, want %q", info.FailureKind, FailureKindHard)
	}
	if info.HealthActionInfo.Action != HealthActionNone {
		t.Fatalf("HealthAction = %q, want %q for non-TokenHub", info.HealthActionInfo.Action, HealthActionNone)
	}
	if info.FailureReason != "http_status" {
		t.Fatalf("FailureReason = %q, want %q", info.FailureReason, "http_status")
	}
}

func TestClassifyTokenHubError_RespRequestURLFallback(t *testing.T) {
	target := requestTarget{
		host: "api.lkeap.cloud.tencent.com",
		path: "/plan/v1/chat/completions",
	}
	resp := newSchedulerHTTPResponse(http.StatusOK, "application/json",
		`{"error":{"code":"401007","message":"quota exceeded"}}`)

	info := classifyTokenHubError(target, resp)

	if info.FailureKind != FailureKindHard {
		t.Fatalf("FailureKind = %q, want %q", info.FailureKind, FailureKindHard)
	}
	if info.HealthActionInfo.Action != HealthActionMarkUnhealthy {
		t.Fatalf("HealthAction = %q, want %q", info.HealthActionInfo.Action, HealthActionMarkUnhealthy)
	}
	if info.HealthActionInfo.CooldownOverride != quotaCooldown {
		t.Fatalf("CooldownOverride = %v, want %v", info.HealthActionInfo.CooldownOverride, quotaCooldown)
	}
}

func TestClassifyTokenHubError_TaskRequestURLFallback(t *testing.T) {
	target := requestTarget{}
	resp := newSchedulerHTTPResponse(http.StatusOK, "application/json",
		`{"error":{"code":"401007","message":"quota exceeded"}}`)

	info := classifyTokenHubError(target, resp)
	if info.HealthActionInfo.Action == HealthActionMarkUnhealthy {
		t.Fatal("should not identify as TokenHub when both target and resp.Request are nil")
	}

	taskReq := &http.Request{
		URL: mustParseURL("https://api.lkeap.cloud.tencent.com/plan/v1/chat/completions"),
	}

	info2 := classifyTokenHubError(makeRequestTarget(resp, taskReq), resp)
	if info2.FailureKind != FailureKindHard {
		t.Fatalf("FailureKind = %q, want %q", info2.FailureKind, FailureKindHard)
	}
	if info2.HealthActionInfo.Action != HealthActionMarkUnhealthy {
		t.Fatalf("HealthAction = %q, want %q", info2.HealthActionInfo.Action, HealthActionMarkUnhealthy)
	}
}

func TestClassifyTokenHubError_AnthropicProtocolQuotaError(t *testing.T) {
	target := requestTarget{
		host: "api.lkeap.cloud.tencent.com",
		path: "/plan/v1/messages",
	}
	resp := newSchedulerHTTPResponse(http.StatusOK, "application/json",
		`{"error":{"code":"20097","message":"quota exceeded"}}`)
	resp.Request = &http.Request{
		URL: mustParseURL("https://api.lkeap.cloud.tencent.com/plan/v1/messages"),
	}

	info := classifyTokenHubError(target, resp)

	if info.FailureKind != FailureKindHard {
		t.Fatalf("FailureKind = %q, want %q", info.FailureKind, FailureKindHard)
	}
	if info.HealthActionInfo.Action != HealthActionMarkUnhealthy {
		t.Fatalf("HealthAction = %q, want %q for Anthropic quota", info.HealthActionInfo.Action, HealthActionMarkUnhealthy)
	}
}

func TestClassifyTokenHubError_UnknownErrorCodeDefaultHardFailure(t *testing.T) {
	target := requestTarget{
		host: "api.lkeap.cloud.tencent.com",
		path: "/plan/v1/chat/completions",
	}
	resp := newSchedulerHTTPResponse(http.StatusBadRequest, "application/json",
		`{"error":{"code":"999999","message":"unknown"}}`)
	resp.Request = &http.Request{
		URL: mustParseURL("https://api.lkeap.cloud.tencent.com/plan/v1/chat/completions"),
	}

	info := classifyTokenHubError(target, resp)

	if info.FailureKind != FailureKindHard {
		t.Fatalf("FailureKind = %q, want %q", info.FailureKind, FailureKindHard)
	}
	if info.HealthActionInfo.Action != HealthActionNone {
		t.Fatalf("HealthAction = %q, want %q for unknown error code", info.HealthActionInfo.Action, HealthActionNone)
	}
	if info.FailureReason != "http_status" {
		t.Fatalf("FailureReason = %q, want %q", info.FailureReason, "http_status")
	}
}

func TestApplyTokenHubClassification_HTTP402_GenericProvider(t *testing.T) {
	result := &Result{
		Response:    newSchedulerHTTPResponse(http.StatusPaymentRequired, "application/json", `{"error":"insufficient balance"}`),
		FailureKind: FailureKindHard,
	}
	// 将 resp.Request.URL 设为非 TokenHub 的 host（如 OpenRouter）
	result.Response.Request = &http.Request{
		URL: mustParseURL("https://api.openrouter.ai/v1/chat/completions"),
	}

	taskReq := &http.Request{
		URL: mustParseURL("https://api.openrouter.ai/v1/chat/completions"),
	}

	applyTokenHubClassification(result, result.Response, taskReq)

	if result.FailureKind != FailureKindHard {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindHard)
	}
	if result.HealthActionInfo.Action != HealthActionMarkUnhealthy {
		t.Fatalf("HealthAction = %q, want %q", result.HealthActionInfo.Action, HealthActionMarkUnhealthy)
	}
	if result.HealthActionInfo.CooldownOverride != quotaCooldown {
		t.Fatalf("CooldownOverride = %v, want %v", result.HealthActionInfo.CooldownOverride, quotaCooldown)
	}
	if result.FailureReason != failureReasonTokenHubQuota {
		t.Fatalf("FailureReason = %q, want %q", result.FailureReason, failureReasonTokenHubQuota)
	}
}

func TestApplyTokenHubClassification_HTTP402_TokenHubProvider(t *testing.T) {
	result := &Result{
		Response:    newSchedulerHTTPResponse(http.StatusPaymentRequired, "application/json", `{"error":{"code":"401007","message":"quota"}}`),
		FailureKind: FailureKindHard,
	}
	result.Response.Request = &http.Request{
		URL: mustParseURL("https://api.lkeap.cloud.tencent.com/v1/chat/completions"),
	}

	taskReq := &http.Request{
		URL: mustParseURL("https://api.lkeap.cloud.tencent.com/v1/chat/completions"),
	}

	applyTokenHubClassification(result, result.Response, taskReq)

	if result.HealthActionInfo.Action != HealthActionMarkUnhealthy {
		t.Fatalf("HealthAction = %q, want %q", result.HealthActionInfo.Action, HealthActionMarkUnhealthy)
	}
	if result.HealthActionInfo.CooldownOverride != quotaCooldown {
		t.Fatalf("CooldownOverride = %v, want %v", result.HealthActionInfo.CooldownOverride, quotaCooldown)
	}
	if result.FailureReason != failureReasonTokenHubQuota {
		t.Fatalf("FailureReason = %q, want %q", result.FailureReason, failureReasonTokenHubQuota)
	}
}
