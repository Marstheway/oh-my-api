package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestRegistry 创建一个注入 fake xai-oauth provider 的 registry，避免真实网络。
// 复用同包已定义的 fakeTokenProvider。
func newTestRegistry() *ProviderRegistry {
	reg := NewProviderRegistry()
	reg.Register("xai-oauth", &fakeTokenProvider{accessToken: "fake-access-token"})
	return reg
}

// makeTestHandler 返回注入 fake provider 的测试 handler，不依赖真实 xAI 网络或状态文件。
func makeTestHandler(t *testing.T) (*BridgeHandler, string) {
	t.Helper()
	return NewBridgeHandlerWithRegistry("test-bridge-token", newTestRegistry()), ""
}

// makeTestHandlerWithState 返回以真实独立状态文件（写入有效 access token）构造的
// handler，用于需要真实 XAIOAuthProvider 读取路径的场景（如 /healthz 校验）。
func makeTestHandlerWithState(t *testing.T) *BridgeHandler {
	t.Helper()
	realHome := os.Getenv("HOME")
	fakeHome := t.TempDir()
	if err := os.Setenv("HOME", fakeHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", realHome) })
	authPath, err := DefaultAuthStatePath()
	if err != nil {
		t.Fatalf("default path: %v", err)
	}
	content := fmt.Sprintf(`{
  "access_token": %q,
  "refresh_token": "test-refresh",
  "expires_in": 3600,
  "last_refresh": "2026-07-14T00:00:00Z",
  "token_endpoint": "https://auth.x.ai/oauth2/token"
}`, testJWT(time.Now().Add(2*time.Hour)))
	if err := os.MkdirAll(filepath.Dir(authPath), 0700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(authPath, []byte(content), 0600); err != nil {
		t.Fatalf("cannot write test auth.json: %v", err)
	}
	return NewBridgeHandler("test-bridge-token")
}

func TestHandler_AuthRequired(t *testing.T) {
	t.Run("no auth header returns 401", func(t *testing.T) {
		handler, _ := makeTestHandler(t)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		w := httptest.NewRecorder()

		handler.HandleResponses(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", w.Code)
		}
	})

	t.Run("wrong token returns 401", func(t *testing.T) {
		handler, _ := makeTestHandler(t)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req.Header.Set("Authorization", "Bearer wrong-token")
		w := httptest.NewRecorder()

		handler.HandleResponses(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", w.Code)
		}
	})

	t.Run("correct token passes auth check but fails on missing provider header", func(t *testing.T) {
		handler, _ := makeTestHandler(t)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req.Header.Set("Authorization", "Bearer test-bridge-token")
		w := httptest.NewRecorder()

		handler.HandleResponses(w, req)

		// 应该因为缺少 provider 头返回 400，而不是 401
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for missing provider header, got %d", w.Code)
		}
	})
}

func TestHandler_ProviderTypeHeader(t *testing.T) {
	t.Run("missing provider type header returns 400", func(t *testing.T) {
		handler, _ := makeTestHandler(t)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req.Header.Set("Authorization", "Bearer test-bridge-token")
		w := httptest.NewRecorder()

		handler.HandleResponses(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}

		body := w.Body.String()
		if !strings.Contains(body, "X-Oh-My-API-Bridge-Provider") {
			t.Errorf("expected error message about provider, got %q", body)
		}
	})

	t.Run("unsupported provider type returns 400", func(t *testing.T) {
		handler, _ := makeTestHandler(t)
		body := bytes.NewReader([]byte(`{"model":"grok-3"}`))
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
		req.Header.Set("Authorization", "Bearer test-bridge-token")
		req.Header.Set("X-Oh-My-API-Bridge-Provider", "unsupported-provider")
		w := httptest.NewRecorder()

		handler.HandleResponses(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("empty provider type header returns 400", func(t *testing.T) {
		handler, _ := makeTestHandler(t)
		body := bytes.NewReader([]byte(`{"model":"grok-3"}`))
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
		req.Header.Set("Authorization", "Bearer test-bridge-token")
		req.Header.Set("X-Oh-My-API-Bridge-Provider", "   ")
		w := httptest.NewRecorder()

		handler.HandleResponses(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})
}

func TestHandler_ContentTypeValidation(t *testing.T) {
	t.Run("accepts application/json", func(t *testing.T) {
		handler, _ := makeTestHandler(t)
		// 以 mock xAI 上游验证 content-type 校验通过后进入代理路径并透传 200，
		// 不依赖真实网络。
		var gotAuth, gotCT string
		xaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			gotCT = r.Header.Get("Content-Type")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp-1"}`))
		}))
		defer xaiSrv.Close()
		old := xaiResponsesURL
		xaiResponsesURL = xaiSrv.URL + "/v1/responses"
		defer func() { xaiResponsesURL = old }()

		body := bytes.NewReader([]byte(`{"model":"grok-3"}`))
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
		req.Header.Set("Authorization", "Bearer test-bridge-token")
		req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.HandleResponses(w, req)

		// 通过 content-type 校验后代理到 xAI 并透传 200。
		if w.Code != http.StatusOK {
			t.Errorf("expected 200 from proxied upstream, got %d", w.Code)
		}
		if !strings.HasPrefix(gotAuth, "Bearer ") {
			t.Errorf("upstream Authorization = %q, want Bearer prefix", gotAuth)
		}
		if !strings.HasPrefix(gotCT, "application/json") {
			t.Errorf("upstream Content-Type = %q, want application/json", gotCT)
		}
	})

	t.Run("rejects text/plain content type", func(t *testing.T) {
		handler, _ := makeTestHandler(t)
		body := bytes.NewReader([]byte(`some text`))
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
		req.Header.Set("Authorization", "Bearer test-bridge-token")
		req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
		req.Header.Set("Content-Type", "text/plain")
		w := httptest.NewRecorder()

		handler.HandleResponses(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("accepts no content type (assuming JSON)", func(t *testing.T) {
		handler, _ := makeTestHandler(t)
		// 无 Content-Type 时仍进入代理路径，以 mock xAI 验证透传 200。
		xaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp-2"}`))
		}))
		defer xaiSrv.Close()
		old := xaiResponsesURL
		xaiResponsesURL = xaiSrv.URL + "/v1/responses"
		defer func() { xaiResponsesURL = old }()

		body := bytes.NewReader([]byte(`{"model":"grok-3"}`))
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
		req.Header.Set("Authorization", "Bearer test-bridge-token")
		req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
		// no Content-Type header
		w := httptest.NewRecorder()

		handler.HandleResponses(w, req)

		// 不因 content-type 而拒绝，代理并透传 200。
		if w.Code != http.StatusOK {
			t.Errorf("expected 200 from proxied upstream, got %d", w.Code)
		}
	})
}

func TestHandler_Healthz(t *testing.T) {
	t.Run("healthz returns ok with valid auth.json", func(t *testing.T) {
		handler := makeTestHandlerWithState(t)
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		w := httptest.NewRecorder()

		handler.HandleHealthz(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}

		var status map[string]interface{}
		if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
			t.Fatalf("cannot decode response: %v", err)
		}
		if status["status"] != "ok" {
			t.Errorf("expected status 'ok', got %v", status["status"])
		}
	})

	t.Run("healthz returns degraded when auth.json is missing", func(t *testing.T) {
		handler := NewBridgeHandler("test-token")
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		w := httptest.NewRecorder()

		handler.HandleHealthz(w, req)

		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", w.Code)
		}

		var status map[string]interface{}
		if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
			t.Fatalf("cannot decode response: %v", err)
		}
		if status["status"] != "degraded" {
			t.Errorf("expected status 'degraded', got %v", status["status"])
		}
		if status["auth_json_error"] == nil {
			t.Error("expected auth_json_error field")
		}
	})
}

func TestHandler_ExtractBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		expect string
	}{
		{"valid bearer", "Bearer my-token", "my-token"},
		{"lowercase bearer", "bearer my-token", "my-token"},
		{"mixed case", "BEARER my-token", "my-token"},
		{"no auth header", "", ""},
		{"wrong scheme", "Basic dXNlcjpwYXNz", ""},
		{"empty value", "Bearer ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			got := extractBearerToken(req)
			if got != tt.expect {
				t.Errorf("expected %q, got %q", tt.expect, got)
			}
		})
	}
}

func TestHandler_LogDoesNotLeakToken(t *testing.T) {
	// 验证日志错误消息不包含敏感 token 信息
	err := func() error {
		// 模拟一个需要脱敏的错误
		return &testError{msg: "some error with access_token value"}
	}()
	safe := tokenErrorSafe(err)
	if strings.Contains(safe, "access_token") && !strings.Contains(safe, "redacted") {
		t.Errorf("tokenErrorSafe should redact messages containing 'access_token': %q", safe)
	}
}

func TestHandler_RequestIDIncluded(t *testing.T) {
	id := newRequestID()
	if id == "" {
		t.Error("request ID should not be empty")
	}
	if len(id) != 16 {
		t.Errorf("expected hex-encoded 8-byte ID (16 hex chars), got %d chars: %q", len(id), id)
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func TestTokenErrorSafe(t *testing.T) {
	t.Run("redacts access_token in message", func(t *testing.T) {
		err := func() error {
			return &testError{msg: "failed with access_token xyz"}
		}()
		safe := tokenErrorSafe(err)
		if !strings.Contains(safe, "redacted") {
			t.Errorf("expected redacted message, got %q", safe)
		}
	})

	t.Run("redacts refresh_token in message", func(t *testing.T) {
		err := func() error {
			return &testError{msg: "refresh_token invalid"}
		}()
		safe := tokenErrorSafe(err)
		if !strings.Contains(safe, "redacted") {
			t.Errorf("expected redacted message, got %q", safe)
		}
	})

	t.Run("truncates long messages", func(t *testing.T) {
		longMsg := strings.Repeat("x", 300)
		err := func() error {
			return &testError{msg: longMsg}
		}()
		safe := tokenErrorSafe(err)
		if len(safe) > 200+3 {
			t.Errorf("expected truncated message, got %d chars", len(safe))
		}
	})

	t.Run("passes through short normal messages", func(t *testing.T) {
		err := func() error {
			return &testError{msg: "connection refused"}
		}()
		safe := tokenErrorSafe(err)
		if safe != "connection refused" {
			t.Errorf("expected passthrough, got %q", safe)
		}
	})
}

func TestHandler_ResponseBodyNotLogged(t *testing.T) {
	// 验证 handler 代理行为且响应不含 access token。
	// 以 mock xAI 上游稳定验证，不访问真实网络。
	handler, _ := makeTestHandler(t)

	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 上游不应收到 bridge token，只收到 xAI OAuth access token。
		if r.Header.Get("Authorization") != "Bearer fake-access-token" {
			t.Errorf("upstream auth = %q, want Bearer fake-access-token", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp-log","status":"completed"}`))
	}))
	defer upstreamSrv.Close()
	old := xaiResponsesURL
	xaiResponsesURL = upstreamSrv.URL + "/v1/responses"
	defer func() { xaiResponsesURL = old }()

	requestBody := `{"model":"grok-3","messages":[{"role":"user","content":"test"}]}`
	body := bytes.NewReader([]byte(requestBody))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleResponses(w, req)

	// 代理成功，上游 200 透传。
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from proxied upstream, got %d (body=%s)", w.Code, w.Body.String())
	}

	// 响应中不包含 bridge token 或 xAI access token 明文。
	respBody := w.Body.String()
	if strings.Contains(respBody, "test-bridge-token") {
		t.Error("response should not contain bridge token")
	}
	if strings.Contains(respBody, "fake-access-token") {
		t.Error("response should not contain access token")
	}
}

func TestHandler_HealthzDoesNotDoNetworkRefresh(t *testing.T) {
	// 验证 healthz 只检查本地文件，不做网络刷新
	handler := NewBridgeHandler("test-token")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	// 这应该立即返回，不做任何网络调用
	handler.HandleHealthz(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandler_ChatCompletions_Success(t *testing.T) {
	// 验证 Chat handler 鉴权、provider 头、Content-Type 校验通过后代理到 xAI Chat 并透传 200。
	var gotAuth, gotCT string
	xaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[]}`))
	}))
	defer xaiSrv.Close()
	old := xaiChatURL
	xaiChatURL = xaiSrv.URL + "/v1/chat/completions"
	defer func() { xaiChatURL = old }()

	handler, _ := makeTestHandler(t)
	body := bytes.NewReader([]byte(`{"model":"grok-3","messages":[{"role":"user","content":"hi"}]}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleChatCompletions(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 from proxied upstream, got %d", w.Code)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Errorf("upstream Authorization = %q, want Bearer prefix", gotAuth)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Errorf("upstream Content-Type = %q, want application/json", gotCT)
	}
}

func TestHandler_ChatCompletions_401RefreshRetry(t *testing.T) {
	// 验证 Chat handler 上游 401 后触发一次强制刷新并重试，与 Responses 行为一致。
	var hitCount int
	var gotTokens []string
	xaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount++
		gotTokens = append(gotTokens, r.Header.Get("Authorization"))
		if hitCount == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-2","object":"chat.completion","choices":[]}`))
	}))
	defer xaiSrv.Close()
	old := xaiChatURL
	xaiChatURL = xaiSrv.URL + "/v1/chat/completions"
	defer func() { xaiChatURL = old }()

	handler, _ := makeTestHandler(t)
	fake := &fakeTokenProvider{accessToken: "old-token", newToken: "new-token"}
	handler.registry.providers["xai-oauth"] = fake

	body := bytes.NewReader([]byte(`{"model":"grok-3","messages":[]}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleChatCompletions(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 after refresh retry, got %d", w.Code)
	}
	if hitCount != 2 {
		t.Errorf("upstream hit count = %d, want 2", hitCount)
	}
	if fake.refreshCalls != 1 {
		t.Errorf("refresh calls = %d, want 1", fake.refreshCalls)
	}
	if len(gotTokens) != 2 || gotTokens[0] != "Bearer old-token" || gotTokens[1] != "Bearer new-token" {
		t.Errorf("upstream tokens = %v, want [Bearer old-token, Bearer new-token]", gotTokens)
	}
}

func TestHandler_ChatCompletions_TokenError(t *testing.T) {
	// GetAccessToken 返回 error → 502
	handler, _ := makeTestHandler(t)
	fake := &fakeTokenProvider{getErr: fmt.Errorf("no token file")}
	handler.registry.providers["xai-oauth"] = fake

	body := bytes.NewReader([]byte(`{"model":"grok-3"}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleChatCompletions(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 for token error, got %d", w.Code)
	}
}

func TestHandler_ChatCompletions_401RefreshFailure(t *testing.T) {
	// 上游 401 后 ForceRefresh 失败 → 502
	hitCount := 0
	xaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer xaiSrv.Close()
	old := xaiChatURL
	xaiChatURL = xaiSrv.URL + "/v1/chat/completions"
	defer func() { xaiChatURL = old }()

	handler, _ := makeTestHandler(t)
	fake := &fakeTokenProvider{accessToken: "old-token", refreshErr: fmt.Errorf("refresh failed")}
	handler.registry.providers["xai-oauth"] = fake

	body := bytes.NewReader([]byte(`{"model":"grok-3"}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleChatCompletions(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 for refresh failure, got %d", w.Code)
	}
	if hitCount != 1 {
		t.Errorf("upstream hit count = %d, want 1 (no retry after refresh failure)", hitCount)
	}
	if fake.refreshCalls != 1 {
		t.Errorf("refresh calls = %d, want 1", fake.refreshCalls)
	}
}

func TestHandler_ChatCompletions_UpstreamUnreachable(t *testing.T) {
	// 上游不可达，proxyToXAI 返回 nil resp + error → 502
	old := xaiChatURL
	xaiChatURL = "http://127.0.0.1:0/v1/chat/completions"
	defer func() { xaiChatURL = old }()

	handler, _ := makeTestHandler(t)

	body := bytes.NewReader([]byte(`{"model":"grok-3"}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleChatCompletions(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 for unreachable upstream, got %d", w.Code)
	}
}
