package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/config"
)

func TestDoWithMeta_ResponsesToolChoiceLearnsFallback(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request map[string]json.RawMessage
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		if string(request["tool_choice"]) == `"required"` {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"tool_choice.function is required when type is function"}}`))
	}))
	defer srv.Close()

	client := NewClient(map[string]config.ProviderConfig{
		"provider-a": {Endpoint: srv.URL, Protocols: []string{"openai.responses"}},
	}, 5*time.Second, 0, 0)
	meta := RequestMeta{UpstreamModel: "model-a", OutboundProtocol: "openai.responses"}

	resp, err := client.DoWithMeta("provider-a", meta, responsesCompatRequest(t, srv.URL))
	if err != nil {
		t.Fatalf("first DoWithMeta: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", resp.StatusCode)
	}

	resp, err = client.DoWithMeta("provider-a", meta, responsesCompatRequest(t, srv.URL))
	if err != nil {
		t.Fatalf("second DoWithMeta: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 3 (probe, fallback, cached fallback)", len(requests))
	}
	if string(requests[0]["tool_choice"]) == `"required"` {
		t.Fatalf("first request unexpectedly used fallback: %s", requests[0]["tool_choice"])
	}
	for i := 1; i < len(requests); i++ {
		if string(requests[i]["tool_choice"]) != `"required"` {
			t.Fatalf("request %d tool_choice = %s, want required", i, requests[i]["tool_choice"])
		}
		var tools []json.RawMessage
		if err := json.Unmarshal(requests[i]["tools"], &tools); err != nil || len(tools) != 1 {
			t.Fatalf("request %d tools = %s, want one tool", i, requests[i]["tools"])
		}
	}
}

func TestDoWithMeta_NormalResponsesRequestIsSentOnce(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	client := NewClient(map[string]config.ProviderConfig{
		"provider-a": {Endpoint: srv.URL, Protocols: []string{"openai.responses"}},
	}, 5*time.Second, 0, 0)

	resp, err := client.DoWithMeta("provider-a", RequestMeta{UpstreamModel: "model-a", OutboundProtocol: "openai.responses"}, responsesCompatRequest(t, srv.URL))
	if err != nil {
		t.Fatalf("DoWithMeta: %v", err)
	}
	resp.Body.Close()
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestDoWithMeta_FallbackFailureDoesNotLearn(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"tool_choice.function is required when type is function"}}`))
	}))
	defer srv.Close()
	client := NewClient(map[string]config.ProviderConfig{
		"provider-a": {Endpoint: srv.URL, Protocols: []string{"openai.responses"}},
	}, 5*time.Second, 0, 0)
	meta := RequestMeta{UpstreamModel: "model-a", OutboundProtocol: "openai.responses"}

	resp, err := client.DoWithMeta("provider-a", meta, responsesCompatRequest(t, srv.URL))
	if err != nil {
		t.Fatalf("DoWithMeta: %v", err)
	}
	resp.Body.Close()
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if client.responsesCompat.cache.has(responsesCompatKey("provider-a", meta)) {
		t.Fatal("cache unexpectedly learned failed fallback")
	}
}

func TestDoWithMeta_CanceledAfterProbeDoesNotFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	client := NewClient(map[string]config.ProviderConfig{
		"provider-a": {},
	}, 5*time.Second, 0, 0)
	client.SetTransport("provider-a", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			t.Fatal("fallback request was sent after context cancellation")
		}
		cancel()
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"tool_choice.function is required when type is function"}}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	}))

	req := responsesCompatRequest(t, "http://provider.invalid/v1/responses").WithContext(ctx)
	resp, err := client.DoWithMeta("provider-a", RequestMeta{UpstreamModel: "model-a", OutboundProtocol: "openai.responses"}, req)
	if resp != nil {
		resp.Body.Close()
		t.Fatal("response = non-nil, want nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestDoWithMeta_CachedFallbackFailureEvicts(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	client := NewClient(map[string]config.ProviderConfig{
		"provider-a": {Endpoint: srv.URL, Protocols: []string{"openai.responses"}},
	}, 5*time.Second, 0, 0)
	meta := RequestMeta{UpstreamModel: "model-a", OutboundProtocol: "openai.responses"}
	key := responsesCompatKey("provider-a", meta)
	client.responsesCompat.cache.add(key)

	resp, err := client.DoWithMeta("provider-a", meta, responsesCompatRequest(t, srv.URL))
	if err != nil {
		t.Fatalf("DoWithMeta: %v", err)
	}
	resp.Body.Close()
	if calls != 1 {
		t.Fatalf("calls = %d, want cached fallback only", calls)
	}
	if client.responsesCompat.cache.has(key) {
		t.Fatal("cache entry was not evicted after fallback failure")
	}
}

func TestResponsesCompatCacheExpires(t *testing.T) {
	cache := newResponsesCompatCache()
	now := time.Now()
	cache.now = func() time.Time { return now }
	key := responsesCompatCacheKey{providerName: "provider-a"}
	cache.add(key)
	now = now.Add(responsesCompatCacheTTL)
	if cache.has(key) {
		t.Fatal("expired cache entry still matched")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func responsesCompatKey(providerName string, meta RequestMeta) responsesCompatCacheKey {
	return responsesCompatCacheKey{
		providerName:     providerName,
		upstreamModel:    meta.UpstreamModel,
		outboundProtocol: meta.OutboundProtocol,
		rule:             codec.ResponsesToolChoiceSingleFunctionCompatRule,
	}
}

func responsesCompatRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	body := []byte(`{"model":"model-a","tools":[{"type":"function","name":"read_file"},{"type":"function","name":"write_file"}],"tool_choice":{"type":"function","name":"write_file"}}`)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	req.ContentLength = int64(len(body))
	return req
}
