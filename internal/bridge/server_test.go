package bridge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServer_RoutesGetModels 复用与 Run 相同的 newBridgeMux 入口，
// 并把 xai-oauth 替换为本地可计数 fake TokenProvider、把 xaiModelsURL
// 替换为本地 httptest.Server，确保不访问真实 OAuth 或 xAI。
func TestServer_RoutesGetModels(t *testing.T) {
	handler, _ := makeTestHandler(t)

	// 用本地 fake 覆盖注册表中的真实 XAIOAuthProvider，避免任何 OAuth 网络请求。
	fake := &fakeTokenProvider{accessToken: "fake-access-token"}
	handler.registry.providers["xai-oauth"] = fake

	// 本地 upstream 验证 GET /v1/models 派发与请求头，不访问真实 xAI。
	var gotMethod, gotPath, gotAccept, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"grok-3"}]}`))
	}))
	defer upstream.Close()

	oldURL := xaiModelsURL
	xaiModelsURL = upstream.URL + "/v1/models"
	defer func() { xaiModelsURL = oldURL }()

	mux := NewBridgeMux(handler)

	t.Run("GET /v1/models is dispatched to model handler", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer test-bridge-token")
		req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
		w := httptest.NewRecorder()

		mux.ServeHTTP(w, req)

		// 路由派发到 HandleModels；本地 upstream 实际收到 GET /v1/models。
		if gotMethod != http.MethodGet {
			t.Errorf("upstream method = %q, want GET", gotMethod)
		}
		if gotPath != "/v1/models" {
			t.Errorf("upstream path = %q, want /v1/models", gotPath)
		}
		if gotAccept != "application/json" {
			t.Errorf("upstream Accept = %q, want application/json", gotAccept)
		}
		if gotAuth != "Bearer fake-access-token" {
			t.Errorf("upstream Authorization = %q, want Bearer fake-access-token", gotAuth)
		}
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
		if !strings.Contains(w.Body.String(), "grok-3") {
			t.Errorf("body = %q, want contains grok-3", w.Body.String())
		}
	})

	t.Run("POST /v1/models is not dispatched to model handler", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer test-bridge-token")
		req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
		w := httptest.NewRecorder()

		mux.ServeHTTP(w, req)

		// ServeMux 精确匹配：POST /v1/models 未注册，应返回 405。
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST /v1/models should be 405 (not dispatched to HandleModels), got %d", w.Code)
		}
	})

	t.Run("GET /v1/models without auth is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		w := httptest.NewRecorder()

		mux.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 for missing auth, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "bearer token") {
			t.Errorf("expected bearer token error, got %q", w.Body.String())
		}
	})

	t.Run("GET /v1/models with unsupported provider is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer test-bridge-token")
		req.Header.Set("X-Oh-My-API-Bridge-Provider", "unsupported-provider")
		w := httptest.NewRecorder()

		mux.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for unsupported provider, got %d", w.Code)
		}
	})
}

func TestServer_RoutesChatCompletions(t *testing.T) {
	handler, _ := makeTestHandler(t)
	fake := &fakeTokenProvider{accessToken: "fake-access-token"}
	handler.registry.providers["xai-oauth"] = fake

	// 本地 upstream 验证 POST /v1/chat/completions 派发到 Chat handler。
	var gotMethod, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion"}`))
	}))
	defer upstream.Close()

	oldURL := xaiChatURL
	xaiChatURL = upstream.URL + "/v1/chat/completions"
	defer func() { xaiChatURL = oldURL }()

	mux := NewBridgeMux(handler)

	t.Run("POST /v1/chat/completions is dispatched to chat handler", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-3"}`))
		req.Header.Set("Authorization", "Bearer test-bridge-token")
		req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		mux.ServeHTTP(w, req)

		if gotMethod != http.MethodPost {
			t.Errorf("upstream method = %q, want POST", gotMethod)
		}
		if gotPath != "/v1/chat/completions" {
			t.Errorf("upstream path = %q, want /v1/chat/completions", gotPath)
		}
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
	})

	t.Run("GET /v1/chat/completions is not dispatched", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
		w := httptest.NewRecorder()

		mux.ServeHTTP(w, req)

		// ServeMux 精确匹配：GET /v1/chat/completions 未注册，应返回 405。
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET /v1/chat/completions should be 405, got %d", w.Code)
		}
	})
}
