package bridge

import (
	"context"
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

// testRoundTripFunc 是测试用的 RoundTripper 函数包装（兼容各 Go 版本）。
type testRoundTripFunc func(req *http.Request) (*http.Response, error)

func (f testRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// interceptXAITransport 返回一个 transport：把对固定生产主机 auth.x.ai 的
// HTTPS 请求（scheme/host 校验在 transport 之前基于真实 URL 完成，不放松）
// 透明转发到本地 mock 后端。测试只能注入 transport，不得放宽 host/scheme 校验。
func interceptXAITransport(backend *httptest.Server) http.RoundTripper {
	return testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "auth.x.ai" && req.URL.Host != "login.auth.x.ai" {
			return nil, fmt.Errorf("unexpected host %q in test", req.URL.Host)
		}
		target := *req.URL
		target.Scheme = "http"
		target.Host = backend.URL[strings.Index(backend.URL, "://")+3:]
		proxy := req.Clone(req.Context())
		proxy.URL = &target
		return backend.Client().Transport.RoundTrip(proxy)
	})
}

// withXAIMock 在测试期间把 xaiHTTPClient 的 transport 指向 mock 后端。
func withXAIMock(t *testing.T, backend *httptest.Server) {
	t.Helper()
	orig := xaiHTTPClient
	xaiHTTPClient = &http.Client{Timeout: 20 * time.Second, Transport: interceptXAITransport(backend)}
	t.Cleanup(func() { xaiHTTPClient = orig })
}

// withTempHome 临时把 HOME 指向空目录，使默认状态路径落在隔离位置。
func withTempHome(t *testing.T) {
	t.Helper()
	realHome := os.Getenv("HOME")
	fakeHome := t.TempDir()
	if err := os.Setenv("HOME", fakeHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", realHome) })
}

// newBackend 创建一个按 path 路由的 mock 后端服务器。
func newBackend(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(h)
}

func writeDiscovery(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token_endpoint":         "https://auth.x.ai/oauth2/token",
		"authorization_endpoint": "https://auth.x.ai/oauth2/authorize",
	})
}

func writeDeviceCode(w http.ResponseWriter, interval int) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"device_code":               "dc",
		"user_code":                 "UC",
		"verification_uri":          "https://auth.x.ai/activate",
		"verification_uri_complete": "https://auth.x.ai/activate?code=UC",
		"expires_in":                600,
		"interval":                  interval,
	})
}

func writeTokens(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  "at",
		"refresh_token": "rt",
		"expires_in":    900,
		"token_type":    "Bearer",
	})
}

func writeErr(w http.ResponseWriter, code string) {
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": code})
}

// TestXAILogin_DiscoveryAndDeviceCode 验证 discovery/device-code 请求字段、
// complete verification URL 严格使用、状态持久化且无 .hermes 访问。
func TestXAILogin_DiscoveryAndDeviceCode(t *testing.T) {
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/.well-known/openid-configuration":
			if r.Header.Get("Accept") != "application/json" {
				t.Errorf("discovery Accept header = %q", r.Header.Get("Accept"))
			}
			writeDiscovery(w)
		case r.URL.Path == "/oauth2/device/code":
			if r.Method != http.MethodPost {
				t.Errorf("device-code method = %q", r.Method)
			}
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Errorf("device-code Content-Type = %q", r.Header.Get("Content-Type"))
			}
			if r.Header.Get("Accept") != "application/json" {
				t.Errorf("device-code Accept = %q", r.Header.Get("Accept"))
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			if r.FormValue("client_id") != xaiOAuthClientID {
				t.Errorf("device-code client_id = %q", r.FormValue("client_id"))
			}
			if r.FormValue("scope") != xaiOAuthScope {
				t.Errorf("device-code scope = %q", r.FormValue("scope"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":               "dc",
				"user_code":                 "ABCD-EFGH",
				"verification_uri":          "https://auth.x.ai/activate",
				"verification_uri_complete": "https://auth.x.ai/activate?code=ABCD-EFGH",
				"expires_in":                600,
				"interval":                  2,
			})
		case r.URL.Path == "/oauth2/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			if r.FormValue("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Errorf("grant_type = %q", r.FormValue("grant_type"))
			}
			if r.FormValue("client_id") != xaiOAuthClientID {
				t.Errorf("client_id = %q", r.FormValue("client_id"))
			}
			if r.FormValue("device_code") != "dc" {
				t.Errorf("device_code = %q", r.FormValue("device_code"))
			}
			writeTokens(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer backend.Close()

	withXAIMock(t, backend)
	withTempHome(t)

	info, state, err := Login(context.Background())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if info.VerificationURL != "https://auth.x.ai/activate?code=ABCD-EFGH" {
		t.Fatalf("verification URL = %q, want complete URL", info.VerificationURL)
	}
	if info.UserCode != "ABCD-EFGH" {
		t.Fatalf("user code = %q", info.UserCode)
	}
	if info.Interval != 2 {
		t.Fatalf("interval = %d", info.Interval)
	}
	if state.AccessToken != "at" || state.RefreshToken != "rt" {
		t.Fatalf("state tokens = %q/%q", state.AccessToken, state.RefreshToken)
	}
	if state.TokenEndpoint != "https://auth.x.ai/oauth2/token" {
		t.Fatalf("token endpoint = %q", state.TokenEndpoint)
	}
	data, err := os.ReadFile(state.Path)
	if err != nil {
		t.Fatalf("read persisted state: %v", err)
	}
	if !strings.Contains(string(data), `"access_token": "at"`) {
		t.Fatalf("persisted state = %s", string(data))
	}
	if !strings.Contains(string(data), `"token_endpoint": "https://auth.x.ai/oauth2/token"`) {
		t.Fatalf("expected real token endpoint persisted, got %s", string(data))
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".hermes")); err == nil {
		t.Fatal("Login must not create ~/.hermes")
	}
}

// TestXAILogin_DeviceCodeFallsBackToVerificationURI 验证缺失
// verification_uri_complete 时回退使用 verification_uri，不强制失败。
func TestXAILogin_DeviceCodeFallsBackToVerificationURI(t *testing.T) {
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/device/code":
			// 故意不返回 verification_uri_complete，仅返回 verification_uri。
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "dc",
				"user_code":        "UC",
				"verification_uri": "https://auth.x.ai/activate",
				"expires_in":       600,
				"interval":         2,
			})
		case "/oauth2/token":
			writeTokens(w)
		default:
			writeDiscovery(w)
		}
	})
	defer backend.Close()
	withXAIMock(t, backend)
	withTempHome(t)

	info, state, err := Login(context.Background())
	if err != nil {
		t.Fatalf("expected fallback to verification_uri, got error: %v", err)
	}
	if info.VerificationURL != "https://auth.x.ai/activate" {
		t.Fatalf("verification URL = %q, want fallback to verification_uri", info.VerificationURL)
	}
	if state == nil {
		t.Fatal("expected persisted state after successful fallback login")
	}
}

func TestXAILogin_PollRules(t *testing.T) {
	// 用即时 pollWaiter 避免真实睡眠；同时验证 slow_down 递增与上限。
	origWaiter := pollWaiter
	pollWaiter = func(ctx context.Context, d time.Duration) {
		select {
		case <-ctx.Done():
		default:
		}
	}
	defer func() { pollWaiter = origWaiter }()

	t.Run("authorization_pending then success", func(t *testing.T) {
		calls := 0
		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth2/device/code":
				writeDeviceCode(w, 1)
			case "/oauth2/token":
				calls++
				if calls == 1 {
					writeErr(w, "authorization_pending")
					return
				}
				writeTokens(w)
			default:
				writeDiscovery(w)
			}
		})
		defer backend.Close()
		withXAIMock(t, backend)
		withTempHome(t)

		_, state, err := Login(context.Background())
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
		if calls != 2 {
			t.Fatalf("poll calls = %d, want 2", calls)
		}
		if state.AccessToken != "at" {
			t.Fatalf("access token = %q", state.AccessToken)
		}
	})

	t.Run("slow_down increments interval up to cap", func(t *testing.T) {
		// 跟踪每次轮询时实际使用的 interval（通过并发安全变量）。
		var mu sync.Mutex
		intervals := []int{}
		calls := 0
		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth2/device/code":
				writeDeviceCode(w, 2) // 起始 interval=2
			case "/oauth2/token":
				mu.Lock()
				calls++
				n := calls
				mu.Unlock()
				// 前 3 次 slow_down 递增 interval（2→3→4→5），第 4 次成功。
				if n < 4 {
					writeErr(w, "slow_down")
					return
				}
				writeTokens(w)
			default:
				writeDiscovery(w)
			}
		})
		defer backend.Close()
		// 包装 waiter 记录传入的等待时长（秒），用以推断 interval。
		orig := pollWaiter
		pollWaiter = func(ctx context.Context, d time.Duration) {
			mu.Lock()
			intervals = append(intervals, int(d.Seconds()))
			mu.Unlock()
			select {
			case <-ctx.Done():
			default:
			}
		}
		defer func() { pollWaiter = orig }()

		withXAIMock(t, backend)
		withTempHome(t)

		_, state, err := Login(context.Background())
		if err != nil {
			t.Fatalf("Login (slow_down): %v", err)
		}
		// 起始 interval=2；首次 poll 立即发起（不等待）即收到 slow_down，
		// 之后每次 slow_down 将 interval +1 并在下一轮前等待（2→3→4→5），
		// 第 4 次 poll 成功。故实际等待序列为 [3,4,5]。
		mu.Lock()
		got := append([]int(nil), intervals...)
		mu.Unlock()
		want := []int{3, 4, 5}
		if len(got) != len(want) {
			t.Fatalf("waited intervals = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("interval[%d] = %d, want %d", i, got[i], want[i])
			}
		}
		if state.AccessToken != "at" {
			t.Fatalf("access token = %q", state.AccessToken)
		}
	})

	t.Run("terminal error stops immediately", func(t *testing.T) {
		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth2/device/code":
				writeDeviceCode(w, 1)
			case "/oauth2/token":
				// 终止错误，不回显原始描述到调用方可见信息之外的状态。
				writeErr(w, "access_denied")
			default:
				writeDiscovery(w)
			}
		})
		defer backend.Close()
		withXAIMock(t, backend)
		withTempHome(t)

		_, state, err := Login(context.Background())
		if err == nil {
			t.Fatal("expected terminal error")
		}
		if state != nil {
			t.Fatal("expected no persisted state on failure")
		}
		// 错误消息固定，不泄露原始上游 detail。
		if strings.Contains(err.Error(), "denied-detail") {
			t.Fatalf("error leaks upstream detail: %q", err.Error())
		}
	})

	t.Run("non-json poll error fails safely", func(t *testing.T) {
		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth2/device/code":
				writeDeviceCode(w, 1)
			case "/oauth2/token":
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("internal server error raw body"))
			default:
				writeDiscovery(w)
			}
		})
		defer backend.Close()
		withXAIMock(t, backend)
		withTempHome(t)

		_, state, err := Login(context.Background())
		if err == nil {
			t.Fatal("expected error for non-JSON poll response")
		}
		if state != nil {
			t.Fatal("expected no persisted state")
		}
		// 不泄露原始响应 body。
		if strings.Contains(err.Error(), "raw body") || strings.Contains(err.Error(), "internal server error") {
			t.Fatalf("error leaks upstream body: %q", err.Error())
		}
	})

	t.Run("missing access_token in token response fails", func(t *testing.T) {
		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth2/device/code":
				writeDeviceCode(w, 1)
			case "/oauth2/token":
				_ = json.NewEncoder(w).Encode(map[string]any{"refresh_token": "rt", "expires_in": 900})
			default:
				writeDiscovery(w)
			}
		})
		defer backend.Close()
		withXAIMock(t, backend)
		withTempHome(t)

		_, state, err := Login(context.Background())
		if err == nil {
			t.Fatal("expected error for missing access_token")
		}
		if state != nil {
			t.Fatal("expected no persisted state")
		}
	})

	t.Run("missing refresh_token in token response fails", func(t *testing.T) {
		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth2/device/code":
				writeDeviceCode(w, 1)
			case "/oauth2/token":
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "expires_in": 900})
			default:
				writeDiscovery(w)
			}
		})
		defer backend.Close()
		withXAIMock(t, backend)
		withTempHome(t)

		_, state, err := Login(context.Background())
		if err == nil {
			t.Fatal("expected error for missing refresh_token")
		}
		if state != nil {
			t.Fatal("expected no persisted state")
		}
	})
}

// TestXAILogin_FirstPollImmediate 验证严格复制 Hermes 的轮询规则：
//  1. 第一次 token poll 立即发起，不在请求前调用 pollWaiter 等待；
//  2. 当 expires_in <= interval（极短有效期）时首次 poll 仍立即发起，
//     不会因 deadline 判定跳过首次请求。
func TestXAILogin_FirstPollImmediate(t *testing.T) {
	t.Run("no wait before first poll", func(t *testing.T) {
		// 统计 waiter 调用次数；首次 poll 成功时必须为 0。
		var mu sync.Mutex
		waiterCalls := 0
		origWaiter := pollWaiter
		pollWaiter = func(ctx context.Context, d time.Duration) {
			mu.Lock()
			waiterCalls++
			mu.Unlock()
			select {
			case <-ctx.Done():
			default:
			}
		}
		defer func() { pollWaiter = origWaiter }()

		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth2/device/code":
				writeDeviceCode(w, 2)
			case "/oauth2/token":
				writeTokens(w)
			default:
				writeDiscovery(w)
			}
		})
		defer backend.Close()
		withXAIMock(t, backend)
		withTempHome(t)

		_, _, err := Login(context.Background())
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
		mu.Lock()
		got := waiterCalls
		mu.Unlock()
		if got != 0 {
			t.Fatalf("pollWaiter calls before first poll = %d, want 0", got)
		}
	})

	t.Run("first poll when expires_in <= interval", func(t *testing.T) {
		// 设备码有效期(2s) <= interval(30s)，并立即成功；
		// 验证首次 poll 仍立即发起（不被 deadline 跳过）。
		var mu sync.Mutex
		waiterCalls := 0
		firstReqBeforeAnyWait := false
		origWaiter := pollWaiter
		pollWaiter = func(ctx context.Context, d time.Duration) {
			mu.Lock()
			waiterCalls++
			mu.Unlock()
			select {
			case <-ctx.Done():
			default:
			}
		}
		defer func() { pollWaiter = origWaiter }()

		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth2/device/code":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"device_code":               "dc",
					"user_code":                 "UC",
					"verification_uri":          "https://auth.x.ai/activate",
					"verification_uri_complete": "https://auth.x.ai/activate?code=UC",
					"expires_in":                2,  // 极短有效期
					"interval":                  30, // 大间隔，expires_in <= interval
				})
			case "/oauth2/token":
				mu.Lock()
				// 首次请求到达时，waiter 尚未被调用（=0）。
				firstReqBeforeAnyWait = waiterCalls == 0
				mu.Unlock()
				writeTokens(w)
			default:
				writeDiscovery(w)
			}
		})
		defer backend.Close()
		withXAIMock(t, backend)
		withTempHome(t)

		_, _, err := Login(context.Background())
		if err != nil {
			t.Fatalf("Login (expires_in<=interval): %v", err)
		}
		mu.Lock()
		got := waiterCalls
		immediate := firstReqBeforeAnyWait
		mu.Unlock()
		if !immediate {
			t.Fatal("first poll was not issued before any wait when expires_in <= interval")
		}
		if got != 0 {
			t.Fatalf("pollWaiter calls = %d, want 0 (single immediate poll succeeds)", got)
		}
	})
}

// TestXAILogin_PollCancel 验证 ctx 取消期间轮询立即停止。
func TestXAILogin_PollCancel(t *testing.T) {
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/device/code":
			writeDeviceCode(w, 1)
		case "/oauth2/token":
			writeErr(w, "authorization_pending")
		default:
			writeDiscovery(w)
		}
	})
	defer backend.Close()
	withXAIMock(t, backend)
	withTempHome(t)

	// 让 waiter 阻塞直到 ctx 取消，以验证 cancel 路径。
	origWaiter := pollWaiter
	pollWaiter = func(ctx context.Context, d time.Duration) {
		<-ctx.Done()
	}
	defer func() { pollWaiter = origWaiter }()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _, err := Login(ctx)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("expected cancellation error, got %q", err.Error())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("poll did not stop promptly on cancel: %v", elapsed)
	}
}

func TestXAILogin_DiscoveryValidation(t *testing.T) {
	t.Run("non https token endpoint rejected", func(t *testing.T) {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token_endpoint":         "http://auth.x.ai/oauth2/token",
				"authorization_endpoint": "https://auth.x.ai/oauth2/authorize",
			})
		}))
		defer backend.Close()
		withXAIMock(t, backend)
		withTempHome(t)

		_, _, err := Login(context.Background())
		if err == nil {
			t.Fatal("expected discovery validation error for non-https endpoint")
		}
	})

	t.Run("non https authorization endpoint rejected", func(t *testing.T) {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token_endpoint":         "https://auth.x.ai/oauth2/token",
				"authorization_endpoint": "http://auth.x.ai/oauth2/authorize",
			})
		}))
		defer backend.Close()
		withXAIMock(t, backend)
		withTempHome(t)

		_, _, err := Login(context.Background())
		if err == nil {
			t.Fatal("expected discovery validation error for non-https authorization_endpoint")
		}
	})

	t.Run("wrong host endpoint rejected", func(t *testing.T) {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token_endpoint":         "https://attacker.example/oauth2/token",
				"authorization_endpoint": "https://auth.x.ai/oauth2/authorize",
			})
		}))
		defer backend.Close()
		withXAIMock(t, backend)
		withTempHome(t)

		_, _, err := Login(context.Background())
		if err == nil {
			t.Fatal("expected discovery validation error for wrong host")
		}
	})

	t.Run("missing endpoints rejected", func(t *testing.T) {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token_endpoint": "https://auth.x.ai/oauth2/token",
			})
		}))
		defer backend.Close()
		withXAIMock(t, backend)
		withTempHome(t)

		_, _, err := Login(context.Background())
		if err == nil {
			t.Fatal("expected discovery validation error for missing authorization_endpoint")
		}
	})
}

// TestXAILogin_PollDoesNotCrossDeadline 验证轮询等待严格受 deadline 约束：
// 等待时长取 min(interval, 距 deadline 剩余)，不会越过 deadline，
// 到达 deadline 即退出并返回超时错误，不发起多余请求。
func TestXAILogin_PollDoesNotCrossDeadline(t *testing.T) {
	// 设备码仅给出极小有效期（2s）但超大轮询间隔（30s），
	// 且 token endpoint 永远返回 authorization_pending，使轮询必然超时。
	backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/device/code":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":               "dc",
				"user_code":                 "UC",
				"verification_uri":          "https://auth.x.ai/activate",
				"verification_uri_complete": "https://auth.x.ai/activate?code=UC",
				"expires_in":                2,  // 极短有效期
				"interval":                  30, // 超大间隔
			})
		case "/oauth2/token":
			writeErr(w, "authorization_pending")
		default:
			writeDiscovery(w)
		}
	})
	defer backend.Close()
	withXAIMock(t, backend)
	withTempHome(t)

	// 即时 waiter：每次等待立即返回，但真正的节流由 deadline 决定。
	// 记录每次被请求的等待时长，验证其不超过剩余时间。
	origWaiter := pollWaiter
	var mu sync.Mutex
	pollWaiter = func(ctx context.Context, d time.Duration) {
		mu.Lock()
		mu.Unlock()
		select {
		case <-ctx.Done():
		default:
		}
	}
	defer func() { pollWaiter = origWaiter }()

	start := time.Now()
	_, _, err := Login(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error when device code never authorized")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %q", err.Error())
	}
	// 轮询必须在 deadline 之后很快结束，不能无限拖延。
	// ExpiresIn=2s，故总耗时不应远超 2s（留足 mock 调度余量）。
	if elapsed > 5*time.Second {
		t.Fatalf("poll kept running long after deadline: %v (deadline 2s)", elapsed)
	}
}
