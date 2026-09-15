package scheduler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

type resetNetError struct{}

func (resetNetError) Error() string   { return "connection reset" }
func (resetNetError) Timeout() bool   { return false }
func (resetNetError) Temporary() bool { return true }

func TestIsRetryableAttempt(t *testing.T) {
	ctx := context.Background()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name   string
		ctx    context.Context
		result *Result
		err    error
		want   bool
	}{
		{name: "eof", ctx: ctx, err: io.EOF, want: true},
		{name: "unexpected eof", ctx: ctx, err: io.ErrUnexpectedEOF, want: true},
		{name: "wrapped eof", ctx: ctx, err: fmt.Errorf("read: %w", io.EOF), want: true},
		{name: "net reset", ctx: ctx, err: resetNetError{}, want: true},
		{name: "wrapped net reset", ctx: ctx, err: fmt.Errorf("post: %w", resetNetError{}), want: true},
		{name: "unknown error", ctx: ctx, err: errors.New("boom"), want: false},
		{name: "net timeout", ctx: ctx, err: timeoutNetError{}, want: false},
		{name: "dns timeout", ctx: ctx, err: &net.DNSError{Err: "timeout", IsTimeout: true}, want: false},
		{name: "prefill timeout", ctx: ctx, err: ErrPrefillTimeout, want: false},
		{name: "attempt timeout", ctx: ctx, err: ErrAttemptTimeout, want: false},
		{name: "parent canceled", ctx: canceled, err: context.Canceled, want: false},
		{name: "http 500", ctx: ctx, result: &Result{FailureKind: FailureKindHard, Response: &http.Response{StatusCode: 500}}, want: true},
		{name: "http 529", ctx: ctx, result: &Result{FailureKind: FailureKindHard, Response: &http.Response{StatusCode: 529}}, want: true},
		{name: "http 429", ctx: ctx, result: &Result{FailureKind: FailureKindHard, Response: &http.Response{StatusCode: 429}}, want: false},
		{name: "http 400", ctx: ctx, result: &Result{FailureKind: FailureKindHard, Response: &http.Response{StatusCode: 400}}, want: false},
		{name: "quota", ctx: ctx, result: &Result{FailureKind: FailureKindHard, FailureReason: failureReasonQuotaExceeded, Response: &http.Response{StatusCode: 402}}, want: false},
		{name: "soft content filter", ctx: ctx, result: &Result{FailureKind: FailureKindSoft, FailureReason: failureReasonContentFilterFinish, Response: &http.Response{StatusCode: 200}}, want: false},
		{name: "success", ctx: ctx, result: &Result{FailureKind: FailureKindSuccess, Response: &http.Response{StatusCode: 200}}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableAttempt(tt.ctx, tt.result, tt.err)
			if got != tt.want {
				t.Fatalf("isRetryableAttempt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFailover_RetriesSameLeafOn500ThenSucceeds(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer srv.Close()

	strategy, providers := newRetryFailover(t, srv.URL, 3)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{{
		ProviderName:  "flaky",
		Provider:      providers["flaky"],
		UpstreamModel: "model",
		Retries:       2,
		Request:       req,
	}}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	defer result.Response.Body.Close()
	if hits.Load() != 3 {
		t.Fatalf("hits = %d, want 3", hits.Load())
	}
	if result.Winner != "flaky" {
		t.Fatalf("winner = %q, want flaky", result.Winner)
	}
}

func TestFailover_RetriesDoesNotApplyTo400(t *testing.T) {
	var hits atomic.Int32
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer failSrv.Close()
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer okSrv.Close()

	providers := map[string]config.ProviderConfig{
		"bad": {Endpoint: failSrv.URL, APIKey: "k", Protocols: []string{"openai"}},
		"ok":  {Endpoint: okSrv.URL, APIKey: "k", Protocols: []string{"openai"}},
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	strategy := NewFailoverStrategy(client, ratelimit.NewManager(providers), health.NewChecker(3, 30*time.Second), 500*time.Millisecond, 0, 0)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, failSrv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, okSrv.URL, nil)
	result, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "bad", Provider: providers["bad"], UpstreamModel: "m", Retries: 2, Request: req1},
		{ProviderName: "ok", Provider: providers["ok"], UpstreamModel: "m", Request: req2},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	defer result.Response.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("bad leaf hits = %d, want 1 (400 is not retryable)", hits.Load())
	}
	if result.Winner != "ok" {
		t.Fatalf("winner = %q, want ok", result.Winner)
	}
}

func TestFailover_RetriesDoesNotApplyTo429(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	strategy, providers := newRetryFailover(t, srv.URL, 3)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	result, err := strategy.Execute(context.Background(), []Task{{
		ProviderName: "flaky", Provider: providers["flaky"], UpstreamModel: "model", Retries: 2, Request: req,
	}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Response != nil {
		defer result.Response.Body.Close()
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}

func TestFailover_RetriesDoesNotApplyToSoftFailure(t *testing.T) {
	var hits atomic.Int32
	body := `{"id":"x","choices":[{"finish_reason":"content_filter","message":{"role":"assistant","content":""}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	strategy, providers := newRetryFailover(t, srv.URL, 3)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	result, err := strategy.Execute(context.Background(), []Task{{
		ProviderName: "flaky", Provider: providers["flaky"], UpstreamModel: "model", Retries: 2, Request: req,
	}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Response != nil {
		defer result.Response.Body.Close()
	}
	if result.FailureKind != FailureKindSoft {
		t.Fatalf("FailureKind = %s, want soft", result.FailureKind)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}

func TestFailover_RetriesDoesNotRetryPrefillTimeout(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"slow": {Endpoint: srv.URL, APIKey: "k", Protocols: []string{"openai"}},
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	strategy := NewFailoverStrategy(client, ratelimit.NewManager(providers), health.NewChecker(3, 30*time.Second), 50*time.Millisecond, 0, 0)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	_, err := strategy.Execute(context.Background(), []Task{{
		ProviderName: "slow", Provider: providers["slow"], UpstreamModel: "model", Retries: 2, Stream: true, Request: req,
	}})
	if !errors.Is(err, ErrPrefillTimeout) {
		t.Fatalf("err = %v, want ErrPrefillTimeout", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1 (timeout is not retryable)", hits.Load())
	}
}

func TestFailover_RetriesHealthReportedOnceAfterExhaustion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"flaky": {Endpoint: srv.URL, APIKey: "k", Protocols: []string{"openai"}},
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewFailoverStrategy(client, ratelimit.NewManager(providers), h, 500*time.Millisecond, 0, 0)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	result, err := strategy.Execute(context.Background(), []Task{{
		ProviderName: "flaky", Provider: providers["flaky"], UpstreamModel: "model", Retries: 2, Request: req,
	}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Response != nil {
		defer result.Response.Body.Close()
	}
	key := health.MakeHealthKey("flaky", "")
	if !h.IsHealthy(key) {
		t.Fatal("one exhausted retry loop must report a single failure; provider should still be healthy at threshold 3")
	}
}

func TestExecuteTask_SuccessAfterRetryDoesNotReportIntermediateFailure(t *testing.T) {
	h := health.NewChecker(1, 30*time.Second)
	key := health.MakeHealthKey("flaky", "")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 2 && !h.IsHealthy(key) {
			t.Error("intermediate 500 must not ReportFailure")
		}
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer srv.Close()

	rt, providers := newRetryRuntime(t, srv.URL, h, 0)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	result, err := rt.executeTask(context.Background(), &Task{
		ProviderName: "flaky", Provider: providers["flaky"], UpstreamModel: "model", Retries: 1, Request: req,
	})
	if err != nil {
		t.Fatalf("executeTask: %v", err)
	}
	defer result.Response.Body.Close()
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
	if !h.IsHealthy(key) {
		t.Fatal("final success must leave provider healthy")
	}
}

func TestFailover_RetrySkippedWhenQPMExhausted(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"flaky": {Endpoint: srv.URL, APIKey: "k", Protocols: []string{"openai"}, RateLimit: config.RateLimitConfig{QPM: 1}},
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	strategy := NewFailoverStrategy(client, ratelimit.NewManager(providers), health.NewChecker(3, 30*time.Second), 500*time.Millisecond, 0, 0)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	result, err := strategy.Execute(context.Background(), []Task{{
		ProviderName: "flaky", Provider: providers["flaky"], UpstreamModel: "model", Retries: 2, Request: req,
	}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Response != nil {
		defer result.Response.Body.Close()
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1 (retry must not fire without a QPM token)", hits.Load())
	}
}

func TestLoadBalance_RetrySkippedWhenQPMExhausted(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"flaky": {Endpoint: srv.URL, APIKey: "k", Protocols: []string{"openai"}, RateLimit: config.RateLimitConfig{QPM: 1}},
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	strategy := NewLoadBalanceStrategy(client, ratelimit.NewManager(providers), health.NewChecker(3, 30*time.Second), 500*time.Millisecond, 0, 0)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	result, err := strategy.Execute(context.Background(), []Task{{
		ProviderName: "flaky", Provider: providers["flaky"], UpstreamModel: "model", Weight: 1, Retries: 2, Request: req,
	}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Response != nil {
		defer result.Response.Body.Close()
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1 (retry must not fire without a QPM token)", hits.Load())
	}
}

func TestExecuteTask_ReplaysBodyOnRetry(t *testing.T) {
	wantBody := []byte(`{"hello":"world"}`)
	var second []byte
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		got, _ := io.ReadAll(r.Body)
		if n == 1 {
			if !bytes.Equal(got, wantBody) {
				t.Errorf("first body = %q, want %q", got, wantBody)
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		second = append([]byte(nil), got...)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer srv.Close()

	rt, providers := newRetryRuntime(t, srv.URL, health.NewChecker(3, 30*time.Second), 0)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, bytes.NewReader(wantBody))
	result, err := rt.executeTask(context.Background(), &Task{
		ProviderName: "flaky", Provider: providers["flaky"], UpstreamModel: "model", Retries: 1, Request: req,
	})
	if err != nil {
		t.Fatalf("executeTask: %v", err)
	}
	defer result.Response.Body.Close()
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
	if !bytes.Equal(second, wantBody) {
		t.Fatalf("retry body = %q, want %q", second, wantBody)
	}
}

func TestExecuteTask_NotReplayableDoesNotRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rt, providers := newRetryRuntime(t, srv.URL, health.NewChecker(3, 30*time.Second), 0)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, io.NopCloser(bytes.NewReader([]byte("once"))))
	req.GetBody = nil
	result, err := rt.executeTask(context.Background(), &Task{
		ProviderName: "flaky", Provider: providers["flaky"], UpstreamModel: "model", Retries: 2, Request: req,
	})
	if err != nil {
		t.Fatalf("executeTask: %v", err)
	}
	if result.Response != nil {
		defer result.Response.Body.Close()
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1 (non-replayable request must not retry)", hits.Load())
	}
}

func TestConcurrent_RetryAbortedWhenSiblingWins(t *testing.T) {
	var slowHits atomic.Int32
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowHits.Add(1)
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer fast.Close()

	providers := map[string]config.ProviderConfig{
		"slow": {Endpoint: slow.URL, APIKey: "k", Protocols: []string{"openai"}},
		"fast": {Endpoint: fast.URL, APIKey: "k", Protocols: []string{"openai"}},
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	strategy := NewConcurrentStrategy(client, ratelimit.NewManager(providers), health.NewChecker(3, 30*time.Second), 500*time.Millisecond, 0, 0)
	slowReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, slow.URL, nil)
	fastReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, fast.URL, nil)
	result, err := strategy.Execute(context.Background(), []Task{
		{ProviderName: "slow", Provider: providers["slow"], UpstreamModel: "m", Retries: 2, Request: slowReq},
		{ProviderName: "fast", Provider: providers["fast"], UpstreamModel: "m", Request: fastReq},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	defer result.Response.Body.Close()
	if result.Winner != "fast" {
		t.Fatalf("winner = %q, want fast", result.Winner)
	}
	time.Sleep(500 * time.Millisecond)
	if slowHits.Load() != 1 {
		t.Fatalf("slow hits = %d, want 1 (race cancel must not retry the loser)", slowHits.Load())
	}
}

func newRetryFailover(t *testing.T, url string, healthThreshold int) (*FailoverStrategy, map[string]config.ProviderConfig) {
	t.Helper()
	providers := map[string]config.ProviderConfig{
		"flaky": {Endpoint: url, APIKey: "k", Protocols: []string{"openai"}},
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	return NewFailoverStrategy(client, ratelimit.NewManager(providers), health.NewChecker(healthThreshold, 30*time.Second), 500*time.Millisecond, 0, 0), providers
}

func newRetryRuntime(t *testing.T, url string, h *health.Checker, qpm int) (leafRuntime, map[string]config.ProviderConfig) {
	t.Helper()
	providers := map[string]config.ProviderConfig{
		"flaky": {Endpoint: url, APIKey: "k", Protocols: []string{"openai"}, RateLimit: config.RateLimitConfig{QPM: qpm}},
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	return newLeafRuntime("test", client, ratelimit.NewManager(providers), h, 500*time.Millisecond, 0, 0), providers
}
