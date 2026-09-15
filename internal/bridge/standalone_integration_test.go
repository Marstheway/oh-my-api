package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStandaloneIntegration_LoginThenProxy 验证独立登录保存的状态经真实
// registry/handler 读取后，可完成 Responses 与 Models 代理，并在首次上游
// 401 时恰好强刷一次、第二次 401 直接透传。所有 xAI OAuth 与 API 调用均以
// 注入 transport 的 mock 截获，不访问真实 xAI、不读写 ~/.hermes。
func TestStandaloneIntegration_LoginThenProxy(t *testing.T) {
	// 1. mock xAI OAuth 后端（discovery/device-code/token），供登录与刷新使用。
	var oauthMu sync.Mutex
	var tokenEndpointCalls int
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeDiscovery(w)
		case "/oauth2/device/code":
			// 立即返回可轮询的 device code；用即时 waiter 让 poll 一发即成功。
			writeDeviceCode(w, 1)
		case "/oauth2/token":
			oauthMu.Lock()
			tokenEndpointCalls++
			oauthMu.Unlock()
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			gt := r.FormValue("grant_type")
			if gt == "urn:ietf:params:oauth:grant-type:device_code" {
				// 设备码轮询：直接成功，返回未来 exp 的真实 JWT，避免非 JWT 触发立即主动刷新。
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  testJWT(time.Now().Add(2 * time.Hour)),
					"refresh_token": "rt",
					"expires_in":    7200,
					"token_type":    "Bearer",
				})
				return
			}
			// refresh_token grant：返回带未来 exp 的新 access token。
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "refreshed-access",
				"refresh_token": "refreshed-refresh",
				"expires_in":    7200,
				"token_type":    "Bearer",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer backend.Close()

	withXAIMock(t, backend)
	withTempHome(t)

	// 2. 执行独立登录，将状态原子持久化到默认 bridge 状态路径。
	if _, _, err := Login(context.Background()); err != nil {
		t.Fatalf("standalone login: %v", err)
	}

	// 3. mock xAI API server（Responses/Models）。首次 401，之后 200。
	var apiMu sync.Mutex
	var responsesHits, modelsHits int
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			apiMu.Lock()
			responsesHits++
			n := responsesHits
			apiMu.Unlock()
			auth := r.Header.Get("Authorization")
			if n == 1 {
				// 首次使用登录后的 access token（非 refreshed-access）。
				if auth == "Bearer refreshed-access" {
					t.Errorf("first responses request should use original token, got %q", auth)
				}
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if auth != "Bearer refreshed-access" {
				t.Errorf("retry responses request token = %q, want Bearer refreshed-access", auth)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp-1", "status": "completed"})
		case "/v1/models":
			apiMu.Lock()
			modelsHits++
			n := modelsHits
			apiMu.Unlock()
			if n == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "grok-3"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiSrv.Close()

	oldResp := xaiResponsesURL
	xaiResponsesURL = apiSrv.URL + "/v1/responses"
	defer func() { xaiResponsesURL = oldResp }()
	oldModels := xaiModelsURL
	xaiModelsURL = apiSrv.URL + "/v1/models"
	defer func() { xaiModelsURL = oldModels }()

	// 4. 用真实 registry/handler（默认独立 provider）读取刚登录的状态。
	handler := NewBridgeHandler("test-bridge-token")

	// Responses：首次 401 → 强刷一次 → 重试 200。
	respReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"grok-3"}`))
	respReq.Header.Set("Authorization", "Bearer test-bridge-token")
	respReq.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	respReq.Header.Set("Content-Type", "application/json")
	respW := httptest.NewRecorder()
	handler.HandleResponses(respW, respReq)

	if respW.Code != http.StatusOK {
		t.Fatalf("responses status = %d, want 200 after retry", respW.Code)
	}

	// Models：首次 401 → 强刷一次 → 重试 200。
	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelsReq.Header.Set("Authorization", "Bearer test-bridge-token")
	modelsReq.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	modelsW := httptest.NewRecorder()
	handler.HandleModels(modelsW, modelsReq)

	if modelsW.Code != http.StatusOK {
		t.Fatalf("models status = %d, want 200 after retry", modelsW.Code)
	}

	// 强刷次数：登录时 1 次（device-code grant，计入 token endpoint），
	// 两个代理各强刷 1 次，共 3 次 token endpoint 调用。
	oauthMu.Lock()
	totalTokenCalls := tokenEndpointCalls
	oauthMu.Unlock()
	if totalTokenCalls != 3 {
		t.Fatalf("token endpoint calls = %d, want 3 (login 1 + responses refresh 1 + models refresh 1)", totalTokenCalls)
	}

	apiMu.Lock()
	rh, mh := responsesHits, modelsHits
	apiMu.Unlock()
	if rh != 2 {
		t.Errorf("responses API hits = %d, want 2 (first 401 + retry)", rh)
	}
	if mh != 2 {
		t.Errorf("models API hits = %d, want 2 (first 401 + retry)", mh)
	}

	// 验证状态已更新为刷新后的 token（真实 registry 再次读取应使用新 token）。
	statePath, _ := DefaultAuthStatePath()
	data, err := readOAuthState(statePath)
	if err != nil {
		t.Fatalf("read state after refresh: %v", err)
	}
	if data.AccessToken != "refreshed-access" {
		t.Errorf("state access token = %q, want refreshed-access", data.AccessToken)
	}
}

// TestStandaloneIntegration_Second401PassThrough 验证第二次上游 401 直接透传，
// 不再刷新。使用已登录状态的真实 registry/handler。
func TestStandaloneIntegration_Second401PassThrough(t *testing.T) {
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeDiscovery(w)
		case "/oauth2/device/code":
			writeDeviceCode(w, 1)
		case "/oauth2/token":
			// device-code grant：成功；refresh grant 不会被触发（第二次 401 不刷新）。
			writeTokens(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer backend.Close()
	withXAIMock(t, backend)
	withTempHome(t)

	if _, _, err := Login(context.Background()); err != nil {
		t.Fatalf("standalone login: %v", err)
	}

	// API server 始终返回 401（两次都是 401）。
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer apiSrv.Close()

	old := xaiResponsesURL
	xaiResponsesURL = apiSrv.URL + "/v1/responses"
	defer func() { xaiResponsesURL = old }()

	handler := NewBridgeHandler("test-bridge-token")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"grok-3"}`))
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleResponses(w, req)

	// 第一次 401 触发强刷；第二次 401（重试结果）原样透传为 401，不再刷新。
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 passthrough", w.Code)
	}
}

// TestStandaloneIntegration_HealthzAfterLogin 验证登录后 /healthz 依据独立状态
// 返回 200 {status:ok}；缺失状态时返回 503 {status:degraded,auth_json_error}。
func TestStandaloneIntegration_HealthzAfterLogin(t *testing.T) {
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeDiscovery(w)
		case "/oauth2/device/code":
			writeDeviceCode(w, 1)
		case "/oauth2/token":
			writeTokens(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer backend.Close()
	withXAIMock(t, backend)
	withTempHome(t)

	handler := NewBridgeHandler("test-bridge-token")

	// 登录前：健康降级。
	before := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	bw := httptest.NewRecorder()
	handler.HandleHealthz(bw, before)
	if bw.Code != http.StatusServiceUnavailable {
		t.Fatalf("pre-login healthz = %d, want 503", bw.Code)
	}
	var degraded map[string]interface{}
	if err := json.Unmarshal(bw.Body.Bytes(), &degraded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if degraded["status"] != "degraded" {
		t.Errorf("pre-login status = %v, want degraded", degraded["status"])
	}
	if _, ok := degraded["auth_json_error"]; !ok {
		t.Error("pre-login missing auth_json_error")
	}

	// 登录后：健康正常，错误内容不得包含 token。
	if _, _, err := Login(context.Background()); err != nil {
		t.Fatalf("login: %v", err)
	}
	after := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	aw := httptest.NewRecorder()
	handler.HandleHealthz(aw, after)
	if aw.Code != http.StatusOK {
		t.Fatalf("post-login healthz = %d, want 200", aw.Code)
	}
	var ok map[string]interface{}
	if err := json.Unmarshal(aw.Body.Bytes(), &ok); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ok["status"] != "ok" {
		t.Errorf("post-login status = %v, want ok", ok["status"])
	}
}

// TestStandaloneIntegration_ProactiveRefresh 验证接近过期（短寿命窗口）的已登录
// 状态在代理时被主动刷新，且刷新只发生一次（与 401 重试无关）。
func TestStandaloneIntegration_ProactiveRefresh(t *testing.T) {
	var mu sync.Mutex
	var refreshCalls int
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeDiscovery(w)
		case "/oauth2/device/code":
			writeDeviceCode(w, 1)
		case "/oauth2/token":
			mu.Lock()
			refreshCalls++
			mu.Unlock()
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			if r.FormValue("grant_type") == "urn:ietf:params:oauth:grant-type:device_code" {
				// 登录成功：返回短寿命 token（ExpiresIn 很小，JWT 即将过期），
				// 使代理时处于 Hermes 主动刷新窗口内（短寿命 120s 窗口）。
				shortJWT := testJWT(time.Now().Add(30 * time.Second))
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  shortJWT,
					"refresh_token": "rt",
					"expires_in":    60,
					"token_type":    "Bearer",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "proactively-refreshed",
				"expires_in":   7200,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer backend.Close()
	withXAIMock(t, backend)
	withTempHome(t)

	if _, _, err := Login(context.Background()); err != nil {
		t.Fatalf("login: %v", err)
	}

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer apiSrv.Close()
	old := xaiModelsURL
	xaiModelsURL = apiSrv.URL + "/v1/models"
	defer func() { xaiModelsURL = old }()

	handler := NewBridgeHandler("test-bridge-token")
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	w := httptest.NewRecorder()

	handler.HandleModels(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	mu.Lock()
	rc := refreshCalls
	mu.Unlock()
	// 登录 1 次 + 主动刷新 1 次（短寿命窗口触发），无 401 重试。
	if rc != 2 {
		t.Fatalf("refresh calls = %d, want 2 (login + proactive refresh)", rc)
	}

	statePath, _ := DefaultAuthStatePath()
	data, _ := readOAuthState(statePath)
	if data.AccessToken != "proactively-refreshed" {
		t.Errorf("access token = %q, want proactively-refreshed", data.AccessToken)
	}
}

// TestStandaloneIntegration_Upstream403Passthrough 验证上游 403 原样透传，
// 不触发刷新或清除本地状态。
func TestStandaloneIntegration_Upstream403Passthrough(t *testing.T) {
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeDiscovery(w)
		case "/oauth2/device/code":
			writeDeviceCode(w, 1)
		case "/oauth2/token":
			writeTokens(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer backend.Close()
	withXAIMock(t, backend)
	withTempHome(t)
	if _, _, err := Login(context.Background()); err != nil {
		t.Fatalf("login: %v", err)
	}

	var mu sync.Mutex
	var refreshCalls int
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		refreshCalls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "forbidden by xAI"})
	}))
	defer apiSrv.Close()
	old := xaiModelsURL
	xaiModelsURL = apiSrv.URL + "/v1/models"
	defer func() { xaiModelsURL = old }()

	handler := NewBridgeHandler("test-bridge-token")
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-bridge-token")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")
	w := httptest.NewRecorder()

	handler.HandleModels(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 passthrough", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(body), "forbidden by xAI") {
		t.Errorf("body = %q, want contains forbidden by xAI", string(body))
	}
	mu.Lock()
	rc := refreshCalls
	mu.Unlock()
	if rc != 1 {
		t.Fatalf("API hits = %d, want 1 (no refresh on 403)", rc)
	}
	// 本地状态未被清除。
	statePath, _ := DefaultAuthStatePath()
	data, _ := readOAuthState(statePath)
	if data.AccessToken == "" {
		t.Error("expected local access token preserved on 403 passthrough")
	}
	if data.LastAuthError != nil {
		t.Error("expected no last_auth_error on 403 passthrough")
	}
}

// TestStandaloneIntegration_LoginDoesNotStartServer 验证独立登录只写状态，
// 不启动任何 HTTP server（无监听端口）。此处以 Login 不阻塞、状态落盘为证。
func TestStandaloneIntegration_LoginDoesNotStartServer(t *testing.T) {
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeDiscovery(w)
		case "/oauth2/device/code":
			writeDeviceCode(w, 1)
		case "/oauth2/token":
			writeTokens(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer backend.Close()
	withXAIMock(t, backend)
	withTempHome(t)

	done := make(chan struct{})
	go func() {
		if _, _, err := Login(context.Background()); err != nil {
			t.Errorf("login: %v", err)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Login did not return promptly (may have blocked on server?)")
	}

	statePath, _ := DefaultAuthStatePath()
	if _, err := readOAuthState(statePath); err != nil {
		t.Fatalf("expected state persisted after login: %v", err)
	}
}

// TestStandaloneIntegration_ModelsProxyViaRealHandler 用真实独立 bridge Models
// handler（默认 XAIOAuthProvider + 已登录状态）透传 mock xAI 模型列表，验证：
//   - bridge token 与 X-Oh-My-API-Bridge-Provider 头被接受；
//   - 上游 xAI /v1/models（无 body GET）被调用且响应 body 被消费透传；
//   - 同源包内替换 xaiModelsURL（非导出、非运行时 URL 覆盖）完成 mock 注入。
//
// 该测试在 bridge 包内执行，因 catalog probe 逻辑位于 cmd/oh-my-api 包，
// 跨包测试不得通过导出生产 API 覆盖运行时 URL；此处使用包内非导出
// xaiModelsURL 的安全替换完成 mock 注入。
func TestStandaloneIntegration_ModelsProxyViaRealHandler(t *testing.T) {
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeDiscovery(w)
		case "/oauth2/device/code":
			writeDeviceCode(w, 1)
		case "/oauth2/token":
			writeTokens(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer backend.Close()
	withXAIMock(t, backend)
	withTempHome(t)

	if _, _, err := Login(context.Background()); err != nil {
		t.Fatalf("standalone login: %v", err)
	}

	// mock xAI 模型列表上游（同包内替换 xaiModelsURL，安全测试注入）。
	var xaiHits int
	var gotAuth, gotAccept string
	xaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xaiHits++
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"grok-3"}]}`))
	}))
	defer xaiSrv.Close()
	oldModels := xaiModelsURL
	xaiModelsURL = xaiSrv.URL + "/v1/models"
	defer func() { xaiModelsURL = oldModels }()

	// 用真实独立 bridge handler 挂载 HTTP server。
	handler := NewBridgeHandler("bridge-secret")
	bridgeSrv := httptest.NewServer(NewBridgeMux(handler))
	defer bridgeSrv.Close()

	// 模拟 oh-my-api catalog probe：带 bridge token 与 provider 头向 bridge 发 GET /v1/models。
	req, err := http.NewRequest(http.MethodGet, bridgeSrv.URL+"/v1/models", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer bridge-secret")
	req.Header.Set("X-Oh-My-API-Bridge-Provider", "xai-oauth")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("catalog probe request to bridge: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bridge models status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "grok-3") {
		t.Errorf("bridge models body = %q, want contains grok-3", string(body))
	}
	if xaiHits != 1 {
		t.Fatalf("xai models upstream hits = %d, want 1", xaiHits)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Errorf("xai upstream missing bearer auth, got %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("xai upstream Accept = %q, want application/json", gotAccept)
	}
}
