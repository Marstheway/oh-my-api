package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyToXAI(t *testing.T) {
	t.Run("successful proxy with 200", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 验证 Authorization 头包含 access token
			auth := r.Header.Get("Authorization")
			if auth != "Bearer test-xai-token" {
				t.Errorf("expected Authorization 'Bearer test-xai-token', got %q", auth)
			}

			// 验证 Content-Type
			ct := r.Header.Get("Content-Type")
			if ct != "application/json" {
				t.Errorf("expected Content-Type 'application/json', got %q", ct)
			}

			// 验证请求体
			body, _ := io.ReadAll(r.Body)
			var req map[string]interface{}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("cannot parse request body: %v", err)
			}
			if req["model"] != "grok-3" {
				t.Errorf("expected model 'grok-3', got %v", req["model"])
			}

			w.Header().Set("x-custom", "test-value")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		}))
		defer upstream.Close()

		// 注入 mock server URL
		origURL := xaiResponsesURL
		xaiResponsesURL = upstream.URL
		defer func() { xaiResponsesURL = origURL }()

		body := bytes.NewReader([]byte(`{"model":"grok-3"}`))
		resp, err := proxyToXAI(context.Background(), xaiResponsesURL, body, "test-xai-token")
		if err != nil {
			t.Fatalf("unexpected proxy error: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
		if resp.Header.Get("x-custom") != "test-value" {
			t.Errorf("expected x-custom header 'test-value', got %q", resp.Header.Get("x-custom"))
		}

		// 验证响应体
		var result map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("cannot decode response: %v", err)
		}
		if result["status"] != "ok" {
			t.Errorf("expected status 'ok', got %q", result["status"])
		}
	})

	t.Run("proxy copies upstream headers", func(t *testing.T) {
		src := http.Header{
			"Content-Type":      {"application/json"},
			"X-Request-Id":      {"12345"},
			"Connection":        {"keep-alive"},
			"Transfer-Encoding": {"chunked"},
		}

		dst := http.Header{}
		copyUpstreamHeaders(dst, src)

		if dst.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type to be copied")
		}
		if dst.Get("X-Request-Id") != "12345" {
			t.Errorf("expected X-Request-Id to be copied")
		}

		// hop-by-hop headers 不应被复制
		if dst.Get("Connection") != "" {
			t.Error("Connection header should not be copied")
		}
		if dst.Get("Transfer-Encoding") != "" {
			t.Error("Transfer-Encoding header should not be copied")
		}
	})
}

func TestProxyHopHeaders(t *testing.T) {
	hopHeaders := []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	}

	for _, h := range hopHeaders {
		t.Run(h, func(t *testing.T) {
			if !proxyHopHeaders[h] {
				t.Errorf("expected %q to be in proxyHopHeaders set", h)
			}
		})
	}

	// 验证非 hop-by-hop headers 不在集合中
	nonHopHeaders := []string{
		"Content-Type",
		"Content-Length",
		"X-Request-Id",
	}
	for _, h := range nonHopHeaders {
		if proxyHopHeaders[h] {
			t.Errorf("expected %q NOT to be in proxyHopHeaders set", h)
		}
	}
}

func TestProxyToXAI_RealRequest(t *testing.T) {
	// 创建一个 mock xAI server，验证请求格式并实际调用 proxyToXAI
	mockXAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("expected Bearer auth header, got %q", auth)
		}

		// 透传标准 Responses JSON
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp-123","object":"response","status":"completed","output":[]}`))
	}))
	defer mockXAI.Close()

	// 注入 mock server URL
	origURL := xaiResponsesURL
	xaiResponsesURL = mockXAI.URL
	defer func() { xaiResponsesURL = origURL }()

	body := bytes.NewReader([]byte(`{"model":"grok-3"}`))
	resp, err := proxyToXAI(context.Background(), xaiResponsesURL, body, "test-token")
	if err != nil {
		t.Fatalf("unexpected proxy error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestProxyRequestBodyPassthrough(t *testing.T) {
	// 验证请求体原样透传，不做协议改写
	originalBody := `{"model":"grok-3","input":"hello world","stream":true}`
	body := bytes.NewReader([]byte(originalBody))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, xaiResponsesURL, body)
	if err != nil {
		t.Fatalf("cannot create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")

	// 读取 body 验证内容完整
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("cannot read body: %v", err)
	}
	if string(bodyBytes) != originalBody {
		t.Errorf("body was modified during request creation:\nexpected: %s\ngot:      %s", originalBody, string(bodyBytes))
	}
}

func TestProxyToXAI_ChatCompletions(t *testing.T) {
	// 验证 Chat Completions 代理：命中 /v1/chat/completions、携带 OAuth bearer 与 JSON body，
	// 并完整保留上游响应。
	var gotMethod, gotPath, gotAuth, gotCT string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	origURL := xaiChatURL
	xaiChatURL = upstream.URL + "/v1/chat/completions"
	defer func() { xaiChatURL = origURL }()

	body := bytes.NewReader([]byte(`{"model":"grok-3","messages":[{"role":"user","content":"hi"}]}`))
	resp, err := proxyToXAI(context.Background(), xaiChatURL, body, "test-xai-token")
	if err != nil {
		t.Fatalf("unexpected proxy error: %v", err)
	}
	defer resp.Body.Close()

	if gotMethod != http.MethodPost {
		t.Errorf("upstream method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer test-xai-token" {
		t.Errorf("upstream Authorization = %q, want Bearer test-xai-token", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("upstream Content-Type = %q, want application/json", gotCT)
	}
	if string(gotBody) != `{"model":"grok-3","messages":[{"role":"user","content":"hi"}]}` {
		t.Errorf("upstream body = %q, want original chat body", string(gotBody))
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestProxyToXAIModels_UsesGetNoBody(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotAccept string
	var gotBodyLen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		body, _ := io.ReadAll(r.Body)
		gotBodyLen = len(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"grok-3"}]}`))
	}))
	defer srv.Close()

	// 替换测试用的模型列表 URL
	oldURL := xaiModelsURL
	xaiModelsURL = srv.URL + "/v1/models"
	defer func() { xaiModelsURL = oldURL }()

	resp, err := proxyModelsToXAI(context.Background(), "test-access-token")
	if err != nil {
		t.Fatalf("proxyModelsToXAI error: %v", err)
	}
	defer resp.Body.Close()

	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", gotPath)
	}
	if gotBodyLen != 0 {
		t.Errorf("request body length = %d, want 0", gotBodyLen)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept header = %q, want application/json", gotAccept)
	}
	if gotAuth != "Bearer test-access-token" {
		t.Errorf("Authorization = %q, want Bearer test-access-token", gotAuth)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status code = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Errorf("expected upstream Content-Type preserved, got %q", resp.Header.Get("Content-Type"))
	}
}
