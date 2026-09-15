package bridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeTokenProvider 是一个可计数的 TokenProvider，用于验证 401 刷新重试行为。
type fakeTokenProvider struct {
	accessToken   string
	newToken      string
	refreshCalls  int
	refreshErr    error
	refreshResult string
	getErr        error
}

func (f *fakeTokenProvider) GetAccessToken(ctx context.Context) (string, bool, error) {
	if f.getErr != nil {
		return "", false, f.getErr
	}
	return f.accessToken, false, nil
}

func (f *fakeTokenProvider) ForceRefresh(ctx context.Context) (string, error) {
	f.refreshCalls++
	if f.refreshErr != nil {
		return "", f.refreshErr
	}
	if f.refreshResult != "" {
		return f.refreshResult, nil
	}
	return f.newToken, nil
}

func TestHandleModels_AccessControl(t *testing.T) {
	t.Run("missing provider header returns 400", func(t *testing.T) {
		handler, _ := makeTestHandler(t)
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer test-bridge-token")
		w := httptest.NewRecorder()

		handler.HandleModels(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for missing provider header, got %d", w.Code)
		}
	})

	t.Run("unsupported provider type returns 400", func(t *testing.T) {
		handler, _ := makeTestHandler(t)
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer test-bridge-token")
		req.Header.Set("X-Oh-My-API-Bridge-Provider", "unsupported-provider")
		w := httptest.NewRecorder()

		handler.HandleModels(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for unsupported provider, got %d", w.Code)
		}
	})
}

func TestHandleModels_PassthroughUpstreamResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证 GET、无 body、Accept 与 Authorization
		if r.Method != http.MethodGet {
			t.Errorf("upstream method = %q, want GET", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 {
			t.Errorf("upstream body length = %d, want 0", len(body))
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("upstream Accept = %q, want application/json", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Upstream", "keep-me")
		w.Header().Add("X-Multi", "first")
		w.Header().Add("X-Multi", "second")
		w.Header().Set("Connection", "close") // hop-by-hop，应被排除
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"grok-3"}]}`))
	}))
	defer upstream.Close()

	oldURL := xaiModelsURL
	xaiModelsURL = upstream.URL + "/v1/models"
	defer func() { xaiModelsURL = oldURL }()

	fake := &fakeTokenProvider{accessToken: "old-token"}
	handler, _ := makeTestHandler(t)
	handler.registry.providers["xai-oauth"] = fake

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	w := httptest.NewRecorder()

	handler.HandleModels(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "grok-3") {
		t.Errorf("body = %q, want contains grok-3", w.Body.String())
	}
	if w.Header().Get("X-Custom-Upstream") != "keep-me" {
		t.Errorf("expected custom upstream header preserved, got %q", w.Header().Get("X-Custom-Upstream"))
	}
	// 多值普通响应头应完整透传，不被覆盖成单个值
	multi := w.Header().Values("X-Multi")
	if len(multi) != 2 || multi[0] != "first" || multi[1] != "second" {
		t.Errorf("expected X-Multi multi-value [first second] preserved, got %v", multi)
	}
	if w.Header().Get("Connection") != "" {
		t.Errorf("expected hop-by-hop Connection header excluded, got %q", w.Header().Get("Connection"))
	}
	if fake.refreshCalls != 0 {
		t.Errorf("refresh should not be called on 200, got %d", fake.refreshCalls)
	}
}

func TestHandleModels_NoUpstreamResponseReturns502(t *testing.T) {
	// 上游 URL 指向一个无法连接的地址，proxy 返回 error，无响应。
	oldURL := xaiModelsURL
	xaiModelsURL = "http://127.0.0.1:0/v1/models"
	defer func() { xaiModelsURL = oldURL }()

	handler, _ := makeTestHandler(t)
	fake := &fakeTokenProvider{accessToken: "old-token"}
	handler.registry.providers["xai-oauth"] = fake

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	w := httptest.NewRecorder()

	handler.HandleModels(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 for no upstream response, got %d", w.Code)
	}
	if fake.refreshCalls != 0 {
		t.Errorf("refresh should not be called when no upstream response, got %d", fake.refreshCalls)
	}
}

func TestHandleModels_First401TriggersSingleRefreshAndRetry(t *testing.T) {
	var firstToken, retryToken string
	hitCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount++
		auth := r.Header.Get("Authorization")
		if hitCount == 1 {
			firstToken = auth
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		retryToken = auth
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"grok-3"}]}`))
	}))
	defer upstream.Close()

	oldURL := xaiModelsURL
	xaiModelsURL = upstream.URL + "/v1/models"
	defer func() { xaiModelsURL = oldURL }()

	handler, _ := makeTestHandler(t)
	fake := &fakeTokenProvider{accessToken: "old-token", newToken: "new-token"}
	handler.registry.providers["xai-oauth"] = fake

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	w := httptest.NewRecorder()

	handler.HandleModels(w, req)

	if fake.refreshCalls != 1 {
		t.Fatalf("ForceRefresh should be called exactly once, got %d", fake.refreshCalls)
	}
	if firstToken != "Bearer old-token" {
		t.Errorf("first request token = %q, want Bearer old-token", firstToken)
	}
	if retryToken != "Bearer new-token" {
		t.Errorf("retry request token = %q, want Bearer new-token", retryToken)
	}
	if hitCount != 2 {
		t.Errorf("upstream hit count = %d, want 2", hitCount)
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 after retry", w.Code)
	}
}

func TestHandleModels_RefreshFailureReturns502(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	oldURL := xaiModelsURL
	xaiModelsURL = upstream.URL + "/v1/models"
	defer func() { xaiModelsURL = oldURL }()

	handler, _ := makeTestHandler(t)
	fake := &fakeTokenProvider{accessToken: "old-token", refreshErr: fmt.Errorf("refresh failed")}
	handler.registry.providers["xai-oauth"] = fake

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	w := httptest.NewRecorder()

	handler.HandleModels(w, req)

	if fake.refreshCalls != 1 {
		t.Fatalf("ForceRefresh should be called once before returning 502, got %d", fake.refreshCalls)
	}
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 on refresh failure, got %d", w.Code)
	}
}

func TestHandleModels_Second401DoesNotRefresh(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	oldURL := xaiModelsURL
	xaiModelsURL = upstream.URL + "/v1/models"
	defer func() { xaiModelsURL = oldURL }()

	handler, _ := makeTestHandler(t)
	fake := &fakeTokenProvider{accessToken: "old-token", newToken: "new-token"}
	handler.registry.providers["xai-oauth"] = fake

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	w := httptest.NewRecorder()

	handler.HandleModels(w, req)

	if fake.refreshCalls != 1 {
		t.Fatalf("ForceRefresh should be called once (first 401), got %d", fake.refreshCalls)
	}
	// 第二次 401 应直接透传，不再刷新。
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 passthrough, got %d", w.Code)
	}
}

func TestHandleModels_Non401StatusPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer upstream.Close()

	oldURL := xaiModelsURL
	xaiModelsURL = upstream.URL + "/v1/models"
	defer func() { xaiModelsURL = oldURL }()

	handler, _ := makeTestHandler(t)
	fake := &fakeTokenProvider{accessToken: "old-token"}
	handler.registry.providers["xai-oauth"] = fake

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	w := httptest.NewRecorder()

	handler.HandleModels(w, req)

	if fake.refreshCalls != 0 {
		t.Errorf("non-401 status should not trigger refresh, got %d", fake.refreshCalls)
	}
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 passthrough, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "boom") {
		t.Errorf("body = %q, want contains boom", w.Body.String())
	}
}
