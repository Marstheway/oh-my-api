package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func makeStateFile(t *testing.T, content string) string {
	t.Helper()
	// 将状态文件放在独立的 fake HOME 下，使 ensureStateDirSafe 的 HOME 约束
	// 与逐级目录链校验可通过；同时避免触碰真实 ~/.hermes。
	realHome := os.Getenv("HOME")
	fakeHome := t.TempDir()
	if err := os.Setenv("HOME", fakeHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", realHome) })
	path, err := DefaultAuthStatePath()
	if err != nil {
		t.Fatalf("default path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("cannot write test state: %v", err)
	}
	return path
}

// makeStateFileInHome 在临时 HOME 的默认 bridge 状态路径下创建状态文件，
// 使 ensureStateDirSafe 的安全目录链校验（要求位于 HOME 下）能够通过，
// 同时真正验证生产状态路径的写入行为。
func makeStateFileInHome(t *testing.T, content string) string {
	t.Helper()
	realHome := os.Getenv("HOME")
	fakeHome := t.TempDir()
	if err := os.Setenv("HOME", fakeHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", realHome) })
	path, err := DefaultAuthStatePath()
	if err != nil {
		t.Fatalf("default path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("cannot write test state: %v", err)
	}
	return path
}

func testJWT(exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return header + "." + payload + ".sig"
}

func TestDefaultAuthStatePath(t *testing.T) {
	realHome := os.Getenv("HOME")
	fakeHome := t.TempDir()
	if err := os.Setenv("HOME", fakeHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	defer os.Setenv("HOME", realHome)

	path, err := DefaultAuthStatePath()
	if err != nil {
		t.Fatalf("DefaultAuthStatePath: %v", err)
	}
	want := filepath.Join(fakeHome, ".oh-my-api", "bridge", "auth.json")
	if path != want {
		t.Fatalf("DefaultAuthStatePath = %q, want %q", path, want)
	}
	// 路径不得包含 .hermes。
	if strings.Contains(path, ".hermes") {
		t.Fatalf("DefaultAuthStatePath should not reference .hermes: %q", path)
	}
}

func TestXAIOAuthProvider_GetAccessToken(t *testing.T) {
	t.Run("fresh token returns without refresh", func(t *testing.T) {
		statePath := makeStateFile(t, fmt.Sprintf(`{
  "access_token": %q,
  "refresh_token": "test-refresh",
  "expires_in": 7200,
  "last_refresh": %q,
  "token_endpoint": "https://auth.x.ai/oauth2/token"
}`, testJWT(time.Now().Add(2*time.Hour)), time.Now().Add(-30*time.Second).UTC().Format(time.RFC3339)))

		p := NewXAIOAuthProvider(statePath)
		token, refreshed, err := p.GetAccessToken(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if token == "" {
			t.Fatal("expected token, got empty")
		}
		if refreshed {
			t.Error("expected refreshed=false for fresh token")
		}
	})

	t.Run("state file not found", func(t *testing.T) {
		p := NewXAIOAuthProvider("/no/such/file.json")
		_, _, err := p.GetAccessToken(context.Background())
		if err == nil {
			t.Fatal("expected error for missing state")
		}
	})

	t.Run("no tokens available", func(t *testing.T) {
		statePath := makeStateFile(t, `{"token_endpoint":"https://auth.x.ai/oauth2/token"}`)
		p := NewXAIOAuthProvider(statePath)
		_, _, err := p.GetAccessToken(context.Background())
		if err == nil {
			t.Fatal("expected error when no tokens available")
		}
	})

	t.Run("missing access token but refresh token triggers refresh", func(t *testing.T) {
		tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				t.Fatalf("cannot parse form: %v", err)
			}
			if r.FormValue("client_id") != xaiOAuthClientID {
				t.Fatalf("client_id = %q", r.FormValue("client_id"))
			}
			if r.FormValue("grant_type") != "refresh_token" {
				t.Fatalf("grant_type = %q", r.FormValue("grant_type"))
			}
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Fatalf("Content-Type = %q", r.Header.Get("Content-Type"))
			}
			if r.Header.Get("Accept") != "application/json" {
				t.Fatalf("Accept = %q", r.Header.Get("Accept"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "new-access",
				"refresh_token": "new-refresh",
				"expires_in":    3600,
			})
		}))
		defer tokenSrv.Close()
		orig := xaiHTTPClient
		xaiHTTPClient = &http.Client{Timeout: 20 * time.Second, Transport: interceptXAITransport(tokenSrv)}
		defer func() { xaiHTTPClient = orig }()

		statePath := makeStateFileInHome(t, `{
  "refresh_token": "only-refresh",
  "expires_in": 3600,
  "last_refresh": "2024-01-01T00:00:00Z",
  "token_endpoint": "https://auth.x.ai/oauth2/token"
}`)
		p := NewXAIOAuthProvider(statePath)
		token, refreshed, err := p.GetAccessToken(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if token != "new-access" {
			t.Fatalf("token = %q", token)
		}
		if !refreshed {
			t.Fatal("expected refreshed=true")
		}
	})
}

func TestXAIOAuthProvider_NeedsRefresh(t *testing.T) {
	now := time.Now()

	t.Run("needs refresh when expired", func(t *testing.T) {
		state := &OAuthState{
			AccessToken:  testJWT(now.Add(-time.Minute)),
			RefreshToken: "ref",
			ExpiresIn:    1,
			LastRefresh:  now.Add(-2 * time.Minute),
		}
		p := &XAIOAuthProvider{}
		if !p.needsRefresh(state) {
			t.Error("expected needsRefresh true for expired token")
		}
	})

	t.Run("needs refresh within skew window", func(t *testing.T) {
		lastRefresh := now.Add(-3500 * time.Second)
		state := &OAuthState{
			AccessToken: testJWT(lastRefresh.Add(100 * time.Second)),
			ExpiresIn:   3600,
			LastRefresh: lastRefresh,
		}
		p := &XAIOAuthProvider{}
		if !p.needsRefresh(state) {
			t.Error("expected needsRefresh true within skew window")
		}
	})

	t.Run("no refresh for fresh long-lived token", func(t *testing.T) {
		state := &OAuthState{
			AccessToken: testJWT(now.Add(2 * time.Hour)),
			ExpiresIn:   7200,
			LastRefresh: now,
		}
		p := &XAIOAuthProvider{}
		if p.needsRefresh(state) {
			t.Error("expected needsRefresh false for fresh token")
		}
	})

	t.Run("no refresh when expires_in missing", func(t *testing.T) {
		state := &OAuthState{AccessToken: testJWT(now.Add(2 * time.Hour)), LastRefresh: now.Add(-10 * time.Minute)}
		p := &XAIOAuthProvider{}
		if p.needsRefresh(state) {
			t.Error("expected needsRefresh false when expires_in missing")
		}
	})

	t.Run("no refresh when last_refresh zero", func(t *testing.T) {
		state := &OAuthState{ExpiresIn: 1}
		p := &XAIOAuthProvider{}
		if p.needsRefresh(state) {
			t.Error("expected needsRefresh false when last_refresh zero")
		}
	})

	t.Run("short-lived token uses 120s skew", func(t *testing.T) {
		// JWT 剩余 200s（> 120s 窗口），不应刷新。
		lastRefresh := now.Add(-(15*60 - 200) * time.Second)
		state := &OAuthState{
			AccessToken: testJWT(lastRefresh.Add(15 * time.Minute)),
			ExpiresIn:   15 * 60,
			LastRefresh: lastRefresh,
		}
		p := &XAIOAuthProvider{}
		if p.needsRefresh(state) {
			t.Error("expected needsRefresh false for short-lived token >120s before expiry")
		}
	})
}

func TestXAIOAuthProvider_TokenRefresh(t *testing.T) {
	t.Run("successful refresh preserves old refresh token when absent", func(t *testing.T) {
		tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access-only", "expires_in": 7200})
		}))
		defer tokenSrv.Close()
		orig := xaiHTTPClient
		xaiHTTPClient = &http.Client{Timeout: 20 * time.Second, Transport: interceptXAITransport(tokenSrv)}
		defer func() { xaiHTTPClient = orig }()

		statePath := makeStateFileInHome(t, `{
  "access_token": "old-access",
  "refresh_token": "old-refresh",
  "expires_in": 1,
  "last_refresh": "2020-01-01T00:00:00Z",
  "token_endpoint": "https://auth.x.ai/oauth2/token"
}`)
		p := NewXAIOAuthProvider(statePath)
		if _, err := p.ForceRefresh(context.Background()); err != nil {
			t.Fatalf("unexpected refresh error: %v", err)
		}
		data, _ := os.ReadFile(statePath)
		if !strings.Contains(string(data), `"refresh_token": "old-refresh"`) {
			t.Fatalf("expected old refresh token preserved, got %s", string(data))
		}
		if !strings.Contains(string(data), `"access_token": "new-access-only"`) {
			t.Fatalf("expected new access token, got %s", string(data))
		}
	})

	t.Run("refresh without token endpoint redis discovers", func(t *testing.T) {
		var discCalls, tokenCalls int
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/.well-known/openid-configuration":
				discCalls++
				if r.Header.Get("Accept") != "application/json" {
					t.Errorf("discovery Accept header = %q", r.Header.Get("Accept"))
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"token_endpoint":         "https://auth.x.ai/oauth2/token",
					"authorization_endpoint": "https://auth.x.ai/oauth2/authorize",
				})
			case "/oauth2/token":
				tokenCalls++
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed", "expires_in": 3600})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer backend.Close()
		orig := xaiHTTPClient
		xaiHTTPClient = &http.Client{Timeout: 20 * time.Second, Transport: interceptXAITransport(backend)}
		defer func() { xaiHTTPClient = orig }()

		statePath := makeStateFileInHome(t, `{
  "access_token": "old",
  "refresh_token": "rt",
  "expires_in": 1,
  "last_refresh": "2020-01-01T00:00:00Z"
}`)
		p := NewXAIOAuthProvider(statePath)
		if _, err := p.ForceRefresh(context.Background()); err != nil {
			t.Fatalf("unexpected refresh error: %v", err)
		}
		if discCalls == 0 {
			t.Fatal("expected discovery to be called when token endpoint missing")
		}
		if tokenCalls == 0 {
			t.Fatal("expected token endpoint to be called")
		}
	})

	t.Run("rejects non x.ai token endpoint", func(t *testing.T) {
		statePath := makeStateFile(t, `{
  "refresh_token": "rt",
  "token_endpoint": "https://attacker.example/token"
}`)
		p := NewXAIOAuthProvider(statePath)
		_, err := p.ForceRefresh(context.Background())
		if err == nil {
			t.Fatal("expected endpoint validation error")
		}
	})

	t.Run("refresh 400 clears tokens and writes fixed last_auth_error", func(t *testing.T) {
		tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			// 故意返回敏感/detail 内容，断言其不进入状态或返回错误。
			_, _ = w.Write([]byte(`{"error":"invalid_grant","detail":"SECRET_DETAIL_LEAK"}`))
		}))
		defer tokenSrv.Close()
		orig := xaiHTTPClient
		xaiHTTPClient = &http.Client{Timeout: 20 * time.Second, Transport: interceptXAITransport(tokenSrv)}
		defer func() { xaiHTTPClient = orig }()

		statePath := makeStateFileInHome(t, `{
  "access_token": "old",
  "refresh_token": "rt",
  "expires_in": 1,
  "last_refresh": "2020-01-01T00:00:00Z",
  "token_endpoint": "https://auth.x.ai/oauth2/token"
}`)
		p := NewXAIOAuthProvider(statePath)
		_, err := p.ForceRefresh(context.Background())
		if err == nil {
			t.Fatal("expected refresh error")
		}
		if strings.Contains(err.Error(), "SECRET_DETAIL_LEAK") {
			t.Fatalf("error leaks upstream detail: %q", err.Error())
		}
		data, _ := os.ReadFile(statePath)
		if strings.Contains(string(data), `"access_token": "old"`) {
			t.Fatalf("expected access token cleared, got %s", string(data))
		}
		if !strings.Contains(string(data), "last_auth_error") {
			t.Fatalf("expected last_auth_error written, got %s", string(data))
		}
		if !strings.Contains(string(data), `"reason": "runtime_refresh_failure"`) {
			t.Fatalf("expected reason=runtime_refresh_failure, got %s", string(data))
		}
		if !strings.Contains(string(data), `"relogin_required": true`) {
			t.Fatalf("expected relogin_required=true on 400, got %s", string(data))
		}
		if strings.Contains(string(data), "SECRET_DETAIL_LEAK") {
			t.Fatalf("state leaks upstream detail: %s", string(data))
		}
		// 固定安全 message 不含原始 detail。
		if !strings.Contains(string(data), "xAI OAuth refresh rejected the refresh token") {
			t.Fatalf("expected fixed safe message, got %s", string(data))
		}
	})

	t.Run("refresh 401 clears tokens with relogin_required true", func(t *testing.T) {
		tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		}))
		defer tokenSrv.Close()
		orig := xaiHTTPClient
		xaiHTTPClient = &http.Client{Timeout: 20 * time.Second, Transport: interceptXAITransport(tokenSrv)}
		defer func() { xaiHTTPClient = orig }()

		statePath := makeStateFileInHome(t, `{
  "access_token": "old",
  "refresh_token": "rt",
  "expires_in": 1,
  "last_refresh": "2020-01-01T00:00:00Z",
  "token_endpoint": "https://auth.x.ai/oauth2/token"
}`)
		p := NewXAIOAuthProvider(statePath)
		_, err := p.ForceRefresh(context.Background())
		if err == nil {
			t.Fatal("expected refresh error")
		}
		data, _ := os.ReadFile(statePath)
		if strings.Contains(string(data), `"access_token": "old"`) {
			t.Fatalf("expected access token cleared, got %s", string(data))
		}
		if !strings.Contains(string(data), `"code": "xai_refresh_failed"`) {
			t.Fatalf("expected xai_refresh_failed code, got %s", string(data))
		}
		if !strings.Contains(string(data), `"relogin_required": true`) {
			t.Fatalf("expected relogin_required=true on 401, got %s", string(data))
		}
	})

	t.Run("refresh 403 clears tokens with tier_denied and relogin_required false", func(t *testing.T) {
		tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden"}`))
		}))
		defer tokenSrv.Close()
		orig := xaiHTTPClient
		xaiHTTPClient = &http.Client{Timeout: 20 * time.Second, Transport: interceptXAITransport(tokenSrv)}
		defer func() { xaiHTTPClient = orig }()

		statePath := makeStateFileInHome(t, `{
  "access_token": "old",
  "refresh_token": "rt",
  "token_endpoint": "https://auth.x.ai/oauth2/token"
}`)
		p := NewXAIOAuthProvider(statePath)
		_, err := p.ForceRefresh(context.Background())
		if err == nil {
			t.Fatal("expected refresh error")
		}
		data, _ := os.ReadFile(statePath)
		if !strings.Contains(string(data), `"code": "xai_oauth_tier_denied"`) {
			t.Fatalf("expected tier_denied code, got %s", string(data))
		}
		if !strings.Contains(string(data), `"relogin_required": false`) {
			t.Fatalf("expected relogin_required=false on 403, got %s", string(data))
		}
	})

	t.Run("relogin overwrites last_auth_error", func(t *testing.T) {
		statePath := makeStateFileInHome(t, fmt.Sprintf(`{
  "access_token": "old",
  "refresh_token": "rt",
  "token_endpoint": %q,
  "last_auth_error": {"code":"xai_refresh_failed","reason":"runtime_refresh_failure","relogin_required":true}
}`, "https://auth.x.ai/oauth2/token"))
		state, err := readOAuthState(statePath)
		if err != nil {
			t.Fatalf("read state: %v", err)
		}
		if state.LastAuthError == nil {
			t.Fatal("expected last_auth_error present")
		}
		// 模拟重新登录成功：写入新 token 并清除错误。
		state.AccessToken = "fresh"
		state.RefreshToken = "fresh-rt"
		state.LastAuthError = nil
		state.LastRefresh = time.Now().UTC()
		if err := writeOAuthState(state); err != nil {
			t.Fatalf("write state: %v", err)
		}
		data, _ := os.ReadFile(statePath)
		if strings.Contains(string(data), "last_auth_error") {
			t.Fatalf("expected last_auth_error removed after relogin, got %s", string(data))
		}
	})

	t.Run("rediscovery requires authorization_endpoint", func(t *testing.T) {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/.well-known/openid-configuration":
				// 缺失 authorization_endpoint：必须被拒绝。
				_ = json.NewEncoder(w).Encode(map[string]any{
					"token_endpoint": "https://auth.x.ai/oauth2/token",
				})
			case "/oauth2/token":
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed", "expires_in": 3600})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer backend.Close()
		orig := xaiHTTPClient
		xaiHTTPClient = &http.Client{Timeout: 20 * time.Second, Transport: interceptXAITransport(backend)}
		defer func() { xaiHTTPClient = orig }()

		// 状态文件不含 token_endpoint，触发 rediscovery。
		statePath := makeStateFile(t, `{
  "access_token": "old",
  "refresh_token": "rt",
  "expires_in": 1,
  "last_refresh": "2020-01-01T00:00:00Z"
}`)
		p := NewXAIOAuthProvider(statePath)
		_, err := p.ForceRefresh(context.Background())
		if err == nil {
			t.Fatal("expected rediscovery error for missing authorization_endpoint")
		}
		if !strings.Contains(err.Error(), "authorization_endpoint") {
			t.Fatalf("expected authorization_endpoint error, got %v", err)
		}
	})
}

func TestOAuthState_Permissions(t *testing.T) {
	t.Run("writes 0700 dir and 0600 file", func(t *testing.T) {
		realHome := os.Getenv("HOME")
		fakeHome := t.TempDir()
		if err := os.Setenv("HOME", fakeHome); err != nil {
			t.Fatalf("set HOME: %v", err)
		}
		defer os.Setenv("HOME", realHome)

		statePath, err := DefaultAuthStatePath()
		if err != nil {
			t.Fatalf("default path: %v", err)
		}
		state := &OAuthState{Path: statePath, AccessToken: "a", RefreshToken: "r", TokenEndpoint: "https://auth.x.ai/oauth2/token"}
		state.LastRefresh = time.Now().UTC()
		if err := writeOAuthState(state); err != nil {
			t.Fatalf("write state: %v", err)
		}
		dirInfo, err := os.Stat(filepath.Dir(statePath))
		if err != nil {
			t.Fatalf("stat dir: %v", err)
		}
		if dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("dir perm = %o, want 700", dirInfo.Mode().Perm())
		}
		fileInfo, err := os.Stat(statePath)
		if err != nil {
			t.Fatalf("stat file: %v", err)
		}
		if fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("file perm = %o, want 600", fileInfo.Mode().Perm())
		}
	})

	t.Run("rejects other-writable file", func(t *testing.T) {
		realHome := os.Getenv("HOME")
		fakeHome := t.TempDir()
		if err := os.Setenv("HOME", fakeHome); err != nil {
			t.Fatalf("set HOME: %v", err)
		}
		defer os.Setenv("HOME", realHome)
		statePath := filepath.Join(fakeHome, "auth.json")
		if err := os.WriteFile(statePath, []byte(`{"access_token":"a"}`), 0666); err != nil {
			t.Fatalf("write: %v", err)
		}
		p := NewXAIOAuthProvider(statePath)
		_, _, err := p.GetAccessToken(context.Background())
		if err == nil {
			t.Fatal("expected error for group/other-writable file")
		}
	})

	t.Run("rejects symlink state file", func(t *testing.T) {
		realHome := os.Getenv("HOME")
		fakeHome := t.TempDir()
		if err := os.Setenv("HOME", fakeHome); err != nil {
			t.Fatalf("set HOME: %v", err)
		}
		defer os.Setenv("HOME", realHome)
		realPath := filepath.Join(fakeHome, "real.json")
		if err := os.WriteFile(realPath, []byte(`{"access_token":"a"}`), 0600); err != nil {
			t.Fatalf("write: %v", err)
		}
		linkPath := filepath.Join(fakeHome, "auth.json")
		if err := os.Symlink(realPath, linkPath); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		p := NewXAIOAuthProvider(linkPath)
		_, _, err := p.GetAccessToken(context.Background())
		if err == nil {
			t.Fatal("expected error for symlinked state file")
		}
	})

	t.Run("rejects other-writable directory", func(t *testing.T) {
		realHome := os.Getenv("HOME")
		fakeHome := t.TempDir()
		if err := os.Setenv("HOME", fakeHome); err != nil {
			t.Fatalf("set HOME: %v", err)
		}
		defer os.Setenv("HOME", realHome)
		if err := os.Chmod(fakeHome, 0777); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		statePath := filepath.Join(fakeHome, "auth.json")
		if err := os.WriteFile(statePath, []byte(`{"access_token":"a"}`), 0600); err != nil {
			t.Fatalf("write: %v", err)
		}
		p := NewXAIOAuthProvider(statePath)
		_, _, err := p.GetAccessToken(context.Background())
		if err == nil {
			t.Fatal("expected error for other-writable directory")
		}
	})

	t.Run("rejects intermediate symlinked parent directory", func(t *testing.T) {
		realHome := os.Getenv("HOME")
		fakeHome := t.TempDir()
		if err := os.Setenv("HOME", fakeHome); err != nil {
			t.Fatalf("set HOME: %v", err)
		}
		defer os.Setenv("HOME", realHome)

		// 真实目录树（安全）存放实际状态文件。
		realDir := filepath.Join(fakeHome, "real-bridge")
		if err := os.MkdirAll(realDir, 0o700); err != nil {
			t.Fatalf("mkdir real: %v", err)
		}
		statePath := filepath.Join(realDir, "auth.json")
		if err := os.WriteFile(statePath, []byte(`{"access_token":"a","refresh_token":"r"}`), 0600); err != nil {
			t.Fatalf("write: %v", err)
		}

		// 把 ~/.oh-my-api/bridge 做成指向真实目录的符号链接，模拟中间父目录被劫持。
		ohMyAPI := filepath.Join(fakeHome, ".oh-my-api")
		if err := os.MkdirAll(ohMyAPI, 0o700); err != nil {
			t.Fatalf("mkdir .oh-my-api: %v", err)
		}
		bridgeLink := filepath.Join(ohMyAPI, "bridge")
		if err := os.Symlink(realDir, bridgeLink); err != nil {
			t.Fatalf("symlink bridge: %v", err)
		}
		linkedStatePath := filepath.Join(bridgeLink, "auth.json")

		p := NewXAIOAuthProvider(linkedStatePath)
		_, _, err := p.GetAccessToken(context.Background())
		if err == nil {
			t.Fatal("expected error for intermediate symlinked parent directory")
		}
		if !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("expected symlink rejection, got %v", err)
		}
	})

	t.Run("rejects non-directory parent component", func(t *testing.T) {
		realHome := os.Getenv("HOME")
		fakeHome := t.TempDir()
		if err := os.Setenv("HOME", fakeHome); err != nil {
			t.Fatalf("set HOME: %v", err)
		}
		defer os.Setenv("HOME", realHome)

		// 把一个本应是目录的中间组件创建为普通文件。
		blocker := filepath.Join(fakeHome, ".oh-my-api")
		if err := os.WriteFile(blocker, []byte("not a dir"), 0600); err != nil {
			t.Fatalf("write blocker: %v", err)
		}
		statePath := filepath.Join(blocker, "bridge", "auth.json")
		if err := os.WriteFile(statePath, []byte(`{"access_token":"a"}`), 0600); err == nil {
			t.Fatal("expected write to fail through file-as-dir")
		}
		p := NewXAIOAuthProvider(statePath)
		_, _, err := p.GetAccessToken(context.Background())
		if err == nil {
			t.Fatal("expected error for non-directory parent component")
		}
	})
}

func TestAuthJSONReadable(t *testing.T) {
	t.Run("usable with access token", func(t *testing.T) {
		statePath := makeStateFile(t, `{"access_token":"tok","refresh_token":"ref"}`)
		if err := AuthJSONReadable(statePath); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("usable with only refresh token", func(t *testing.T) {
		statePath := makeStateFile(t, `{"refresh_token":"ref"}`)
		if err := AuthJSONReadable(statePath); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("no usable tokens returns error", func(t *testing.T) {
		statePath := makeStateFile(t, `{"token_endpoint":"https://auth.x.ai/oauth2/token"}`)
		if err := AuthJSONReadable(statePath); err == nil {
			t.Fatal("expected error when no tokens available")
		}
	})
}

func TestValidateXAITokenEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "default host", url: "https://auth.x.ai/oauth2/token"},
		{name: "subdomain host", url: "https://login.auth.x.ai/token"},
		{name: "non https", url: "http://auth.x.ai/token", wantErr: true},
		{name: "wrong host", url: "https://attacker.example/token", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateXAITokenEndpoint(tt.url)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestAtomicWritePreservesFields(t *testing.T) {
	statePath := makeStateFileInHome(t, `{"access_token":"tok","refresh_token":"ref","expires_in":3600}`)
	state, err := readOAuthState(statePath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	state.AccessToken = "updated"
	state.IDToken = "idtok"
	state.LastRefresh = time.Now().UTC()
	if err := writeOAuthState(state); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _ := os.ReadFile(statePath)
	if !strings.Contains(string(data), `"refresh_token": "ref"`) {
		t.Fatalf("expected refresh token preserved, got %s", string(data))
	}
	if !strings.Contains(string(data), `"id_token": "idtok"`) {
		t.Fatalf("expected id_token written, got %s", string(data))
	}
	if !strings.Contains(string(data), `"access_token": "updated"`) {
		t.Fatalf("expected updated access token, got %s", string(data))
	}
}

// TestOAuthState_NoAuthLockAndNoHermes 声明默认状态目录不生成 auth.lock、
// 不访问 ~/.hermes，且只写入 ~/<bridge state>/auth.json。
func TestOAuthState_NoAuthLockAndNoHermes(t *testing.T) {
	realHome := os.Getenv("HOME")
	fakeHome := t.TempDir()
	if err := os.Setenv("HOME", fakeHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	defer os.Setenv("HOME", realHome)

	statePath, err := DefaultAuthStatePath()
	if err != nil {
		t.Fatalf("default path: %v", err)
	}
	state := &OAuthState{
		Path:          statePath,
		AccessToken:   "a",
		RefreshToken:  "r",
		TokenEndpoint: "https://auth.x.ai/oauth2/token",
	}
	state.LastRefresh = time.Now().UTC()
	if err := writeOAuthState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}

	// 默认状态路径必须落在 ~/.oh-my-api/bridge/ 下。
	if !strings.HasPrefix(statePath, filepath.Join(fakeHome, ".oh-my-api", "bridge")) {
		t.Fatalf("state path %q not under ~/.oh-my-api/bridge", statePath)
	}
	// 不得生成 auth.lock。
	if _, err := os.Stat(statePath + ".lock"); err == nil {
		t.Fatal("unexpected auth.lock created")
	}
	// 不得访问 ~/.hermes。
	if _, err := os.Stat(filepath.Join(fakeHome, ".hermes")); err == nil {
		t.Fatal("unexpected ~/.hermes access")
	}
}

// TestXAIOAuthProvider_ConcurrentRefresh 验证同进程并发 GetAccessToken 的
// 主动刷新去重：第一个触发刷新并写回新鲜状态，后续读到新鲜 token 不再刷新，
// 因此只发起一次网络请求并写出一致状态（进程内互斥锁串行化）。
func TestXAIOAuthProvider_ConcurrentRefresh(t *testing.T) {
	var mu sync.Mutex
	var calls int
	// 返回带未来 exp 的真实 JWT，使刷新后状态在窗口外，验证并发去重。
	freshJWT := testJWT(time.Now().Add(2 * time.Hour))
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": freshJWT, "expires_in": 7200})
	}))
	defer tokenSrv.Close()
	orig := xaiHTTPClient
	xaiHTTPClient = &http.Client{Timeout: 20 * time.Second, Transport: interceptXAITransport(tokenSrv)}
	defer func() { xaiHTTPClient = orig }()

	// 过期状态：触发主动刷新。
	statePath := makeStateFileInHome(t, fmt.Sprintf(`{
  "access_token": %q,
  "refresh_token": "rt",
  "expires_in": 1,
  "last_refresh": "2020-01-01T00:00:00Z",
  "token_endpoint": "https://auth.x.ai/oauth2/token"
}`, testJWT(time.Now().Add(-time.Hour))))
	p := NewXAIOAuthProvider(statePath)

	var wg sync.WaitGroup
	results := make([]string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tok, _, err := p.GetAccessToken(context.Background())
			if err != nil {
				t.Errorf("goroutine %d get token error: %v", idx, err)
				return
			}
			results[idx] = tok
		}(i)
	}
	wg.Wait()

	for i, tok := range results {
		if tok != freshJWT {
			t.Fatalf("goroutine %d token = %q", i, tok)
		}
	}
	mu.Lock()
	totalCalls := calls
	mu.Unlock()
	if totalCalls != 1 {
		t.Fatalf("expected exactly 1 refresh HTTP call under concurrency, got %d", totalCalls)
	}
	data, _ := os.ReadFile(statePath)
	if !strings.Contains(string(data), fmt.Sprintf(`"access_token": %q`, freshJWT)) {
		t.Fatalf("expected consistent persisted state, got %s", string(data))
	}
}

// TestXAIOAuthProvider_NoHermesDependency 放置一个哨兵 Hermes 文件，
// 证明 bridge 独立状态读取/刷新完全不受其影响。
func TestXAIOAuthProvider_NoHermesDependency(t *testing.T) {
	realHome := os.Getenv("HOME")
	fakeHome := t.TempDir()
	if err := os.Setenv("HOME", fakeHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	defer os.Setenv("HOME", realHome)

	// 在 fake HOME 下放置哨兵 ~/.hermes/auth.json，内容为桥不应读取的 token。
	hermesDir := filepath.Join(fakeHome, ".hermes")
	if err := os.MkdirAll(hermesDir, 0700); err != nil {
		t.Fatalf("mkdir hermes: %v", err)
	}
	sentinel := `{"providers":{"xai-oauth":{"tokens":{"access_token":"HERMES_SHOULD_NOT_BE_USED","refresh_token":"HERMES_RT"}}}}`
	if err := os.WriteFile(filepath.Join(hermesDir, "auth.json"), []byte(sentinel), 0600); err != nil {
		t.Fatalf("write hermes sentinel: %v", err)
	}

	// bridge 默认状态路径（独立），不应包含 HERMES token。
	statePath, err := DefaultAuthStatePath()
	if err != nil {
		t.Fatalf("default path: %v", err)
	}
	if strings.Contains(statePath, ".hermes") {
		t.Fatalf("bridge state path references hermes: %q", statePath)
	}

	// 读取一个独立状态文件，确认其内容不被 Hermes 哨兵污染。
	ownStatePath := makeStateFile(t, fmt.Sprintf(`{
  "access_token": "bridge-tok",
  "refresh_token": "bridge-rt",
  "expires_in": 7200,
  "last_refresh": %q,
  "token_endpoint": "https://auth.x.ai/oauth2/token"
}`, time.Now().Add(-30*time.Second).UTC().Format(time.RFC3339)))
	p := NewXAIOAuthProvider(ownStatePath)
	tok, _, err := p.GetAccessToken(context.Background())
	if err != nil {
		t.Fatalf("GetAccessToken: %v", err)
	}
	if tok != "bridge-tok" {
		t.Fatalf("bridge read wrong token %q (possibly from hermes)", tok)
	}
	if strings.Contains(tok, "HERMES") {
		t.Fatalf("bridge token leaked from hermes sentinel: %q", tok)
	}
}

// TestAuthJSONReadable_SafeChecks 验证 AuthJSONReadable 复用与 readOAuthState
// 相同的安全校验：拒绝符号链接与不安全权限。所有子测试在 fake HOME 下进行，
// 以满足 ensureStateDirSafe 的 HOME 约束与逐级目录链校验。
func TestAuthJSONReadable_SafeChecks(t *testing.T) {
	withFakeHome := func(t *testing.T) (string, string) {
		t.Helper()
		realHome := os.Getenv("HOME")
		fakeHome := t.TempDir()
		if err := os.Setenv("HOME", fakeHome); err != nil {
			t.Fatalf("set HOME: %v", err)
		}
		t.Cleanup(func() { _ = os.Setenv("HOME", realHome) })
		return fakeHome, fakeHome
	}

	t.Run("rejects symlink", func(t *testing.T) {
		fakeHome, _ := withFakeHome(t)
		realPath := filepath.Join(fakeHome, "real.json")
		if err := os.WriteFile(realPath, []byte(`{"access_token":"a","refresh_token":"r"}`), 0600); err != nil {
			t.Fatalf("write: %v", err)
		}
		linkPath := filepath.Join(fakeHome, "auth.json")
		if err := os.Symlink(realPath, linkPath); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if err := AuthJSONReadable(linkPath); err == nil {
			t.Fatal("expected error for symlinked state file")
		}
	})

	t.Run("rejects other-writable file", func(t *testing.T) {
		fakeHome, _ := withFakeHome(t)
		statePath := filepath.Join(fakeHome, "auth.json")
		if err := os.WriteFile(statePath, []byte(`{"access_token":"a","refresh_token":"r"}`), 0666); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := AuthJSONReadable(statePath); err == nil {
			t.Fatal("expected error for group/other-writable file")
		}
	})

	t.Run("rejects unsafe directory", func(t *testing.T) {
		fakeHome, _ := withFakeHome(t)
		if err := os.Chmod(fakeHome, 0777); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		// 放置一个属主安全但目录不安全的文件。
		statePath := filepath.Join(fakeHome, "auth.json")
		if err := os.WriteFile(statePath, []byte(`{"access_token":"a","refresh_token":"r"}`), 0600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := AuthJSONReadable(statePath); err == nil {
			t.Fatal("expected error for unsafe directory")
		}
	})

	t.Run("accepts safe state", func(t *testing.T) {
		statePath := makeStateFile(t, `{"access_token":"tok","refresh_token":"ref"}`)
		if err := AuthJSONReadable(statePath); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// TestOAuthState_DirChainSafety 验证写入前逐级校验状态目录链：
// 拒绝目录符号链接与 group/other 可写目录，并在缺失时以 0700 创建。
func TestOAuthState_DirChainSafety(t *testing.T) {
	realHome := os.Getenv("HOME")
	fakeHome := t.TempDir()
	if err := os.Setenv("HOME", fakeHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	defer os.Setenv("HOME", realHome)

	t.Run("creates 0700 chain when missing", func(t *testing.T) {
		statePath, _ := DefaultAuthStatePath()
		state := &OAuthState{Path: statePath, AccessToken: "a", RefreshToken: "r", TokenEndpoint: "https://auth.x.ai/oauth2/token"}
		state.LastRefresh = time.Now().UTC()
		if err := writeOAuthState(state); err != nil {
			t.Fatalf("write: %v", err)
		}
		for _, sub := range []string{".oh-my-api", filepath.Join(".oh-my-api", "bridge")} {
			info, err := os.Stat(filepath.Join(fakeHome, sub))
			if err != nil {
				t.Fatalf("stat %s: %v", sub, err)
			}
			if info.Mode().Perm() != 0o700 {
				t.Fatalf("%s perm = %o, want 700", sub, info.Mode().Perm())
			}
		}
	})

	t.Run("rejects symlinked directory component", func(t *testing.T) {
		// 用独立 HOME 变体：home/.oh-my-api 指向真实目录，bridge 为真实目录，
		// 但把 home 下的某层做成 symlink。这里构造：fakeHome2/.oh-my-api -> 真实 dir。
		home2 := t.TempDir()
		realChain := t.TempDir()
		if err := os.Symlink(realChain, filepath.Join(home2, ".oh-my-api")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if err := os.Setenv("HOME", home2); err != nil {
			t.Fatalf("set HOME: %v", err)
		}
		defer os.Setenv("HOME", realHome)

		statePath, _ := DefaultAuthStatePath()
		state := &OAuthState{Path: statePath, AccessToken: "a", RefreshToken: "r", TokenEndpoint: "https://auth.x.ai/oauth2/token"}
		state.LastRefresh = time.Now().UTC()
		if err := writeOAuthState(state); err == nil {
			t.Fatal("expected error for symlinked directory chain")
		}
	})

	t.Run("rejects group/other writable directory", func(t *testing.T) {
		home2 := t.TempDir()
		chain := filepath.Join(home2, ".oh-my-api")
		// 先以安全权限创建，再以 Chmod 强制设为 group/other 可写（不受 umask 影响），
		// 以验证 ensureStateDirSafe 拒绝不安全的已有目录。
		if err := os.MkdirAll(chain, 0700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Chmod(chain, 0777); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if err := os.Setenv("HOME", home2); err != nil {
			t.Fatalf("set HOME: %v", err)
		}
		defer os.Setenv("HOME", realHome)

		statePath, _ := DefaultAuthStatePath()
		state := &OAuthState{Path: statePath, AccessToken: "a", RefreshToken: "r", TokenEndpoint: "https://auth.x.ai/oauth2/token"}
		state.LastRefresh = time.Now().UTC()
		if err := writeOAuthState(state); err == nil {
			t.Fatal("expected error for group/other-writable directory")
		}
	})
}
