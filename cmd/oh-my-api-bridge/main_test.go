package main

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/bridge"
)

// withTempHome 临时把 HOME 指向空目录，避免登录状态写入真实 HOME，
// 并清除远程会话检测相关的环境变量，保证图形环境判定不被 CI/SSH 环境干扰。
func withTempHome(t *testing.T) {
	t.Helper()
	realHome := os.Getenv("HOME")
	fakeHome := t.TempDir()
	if err := os.Setenv("HOME", fakeHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", realHome) })

	remoteVars := []string{"SSH_TTY", "SSH_CLIENT", "CLOUD_SHELL", "CODESPACES", "CODESPACE_NAME", "GITPOD_WORKSPACE_ID", "REPL_ID", "STACKBLITZ"}
	for _, v := range remoteVars {
		if old, ok := os.LookupEnv(v); ok {
			_ = os.Unsetenv(v)
			key := v
			val := old
			t.Cleanup(func() { _ = os.Setenv(key, val) })
		}
	}
}

// stubLoginSteps 用固定桩替换登录三步，不访问网络。
// 返回的 device 用于让 startDeviceCode 立即返回；pollDeviceToken 立即返回 tok；
// completeLogin 立即返回状态。测试可检查各步调用时机。
func stubLoginSteps(t *testing.T, device *bridge.DeviceCodeResponse, tok *bridge.DeviceTokenResult) {
	t.Helper()
	origStart := startDeviceCode
	origPoll := pollDeviceToken
	origComplete := completeLogin
	startDeviceCode = func(ctx context.Context) (*bridge.DeviceCodeResponse, string, error) {
		return device, "https://auth.x.ai/oauth2/token", nil
	}
	pollDeviceToken = func(ctx context.Context, tokenEndpoint string, d *bridge.DeviceCodeResponse) (*bridge.DeviceTokenResult, error) {
		return tok, nil
	}
	completeLogin = func(ctx context.Context, r *bridge.DeviceTokenResult, tokenEndpoint string) (*bridge.OAuthState, error) {
		return &bridge.OAuthState{}, nil
	}
	t.Cleanup(func() {
		startDeviceCode = origStart
		pollDeviceToken = origPoll
		completeLogin = origComplete
	})
}

// captureStdout 捕获函数执行期间的 stdout。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

func TestRun_AuthLoginDispatchesAndDoesNotServe(t *testing.T) {
	withTempHome(t)
	stubLoginSteps(t,
		&bridge.DeviceCodeResponse{VerificationURIComplete: "https://auth.x.ai/activate?code=ABC", UserCode: "ABCDEF"},
		&bridge.DeviceTokenResult{AccessToken: "at", RefreshToken: "rt"},
	)

	var serviceCalled bool
	origService := runService
	runService = func(cfg *bridge.Config) error {
		serviceCalled = true
		return nil
	}
	t.Cleanup(func() { runService = origService })

	out := captureStdout(t, func() {
		if err := run([]string{"auth", "login"}, nil); err != nil {
			t.Fatalf("run auth login: %v", err)
		}
	})

	if serviceCalled {
		t.Fatal("auth login must not start the HTTP server")
	}
	if !contains(out, "https://auth.x.ai/activate?code=ABC") {
		t.Errorf("output should show verification URL, got %q", out)
	}
	if !contains(out, "ABCDEF") {
		t.Errorf("output should show user code, got %q", out)
	}
}

// TestRun_AuthLoginInteractionOrder 验证设备码返回后立即打印 URL 并启动浏览器，
// 然后才轮询；浏览器启动不阻塞轮询，且 URL 出现在轮询完成（token 返回）之前。
func TestRun_AuthLoginInteractionOrder(t *testing.T) {
	withTempHome(t)

	var mu sync.Mutex
	var steps []string
	var browserLaunched bool

	origStart := startDeviceCode
	origPoll := pollDeviceToken
	origComplete := completeLogin
	startDeviceCode = func(ctx context.Context) (*bridge.DeviceCodeResponse, string, error) {
		mu.Lock()
		steps = append(steps, "device_code")
		mu.Unlock()
		return &bridge.DeviceCodeResponse{VerificationURIComplete: "https://x.ai/v", UserCode: "U"}, "https://auth.x.ai/oauth2/token", nil
	}
	pollDeviceToken = func(ctx context.Context, tokenEndpoint string, d *bridge.DeviceCodeResponse) (*bridge.DeviceTokenResult, error) {
		mu.Lock()
		steps = append(steps, "poll")
		mu.Unlock()
		return &bridge.DeviceTokenResult{AccessToken: "at", RefreshToken: "rt"}, nil
	}
	completeLogin = func(ctx context.Context, r *bridge.DeviceTokenResult, tokenEndpoint string) (*bridge.OAuthState, error) {
		mu.Lock()
		steps = append(steps, "complete")
		mu.Unlock()
		return &bridge.OAuthState{}, nil
	}
	t.Cleanup(func() {
		startDeviceCode = origStart
		pollDeviceToken = origPoll
		completeLogin = origComplete
	})

	launch := func(url string) (bool, error) {
		mu.Lock()
		browserLaunched = true
		mu.Unlock()
		return true, nil
	}

	out := captureStdout(t, func() {
		if err := run([]string{"auth", "login"}, launch); err != nil {
			t.Fatalf("run auth login: %v", err)
		}
	})

	mu.Lock()
	got := append([]string(nil), steps...)
	launched := browserLaunched
	mu.Unlock()

	if !launched {
		t.Fatal("browser should have been launched")
	}
	// 顺序必须是 device_code → poll → complete（即 URL 展示与浏览器启动在轮询前）。
	want := []string{"device_code", "poll", "complete"}
	if len(got) != len(want) {
		t.Fatalf("step order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step[%d] = %q, want %q (full order %v)", i, got[i], want[i], got)
		}
	}
	// URL 必须在输出中（设备码阶段即打印，轮询前可见）。
	if !contains(out, "https://x.ai/v") {
		t.Errorf("verification URL should be printed, got %q", out)
	}
}

func TestRun_AuthLoginBrowserOpened(t *testing.T) {
	withTempHome(t)
	stubLoginSteps(t,
		&bridge.DeviceCodeResponse{VerificationURIComplete: "https://x.ai/v", UserCode: "U"},
		&bridge.DeviceTokenResult{AccessToken: "at", RefreshToken: "rt"},
	)

	var openedURL string
	launch := func(url string) (bool, error) {
		openedURL = url
		return true, nil
	}

	out := captureStdout(t, func() {
		if err := run([]string{"auth", "login"}, launch); err != nil {
			t.Fatalf("run auth login: %v", err)
		}
	})
	if openedURL != "https://x.ai/v" {
		t.Errorf("browser launcher should receive verification URL, got %q", openedURL)
	}
	if contains(out, "no local graphical browser") {
		t.Errorf("should not print manual-open note when browser opened, got %q", out)
	}
}

func TestRun_AuthLoginBrowserSkip(t *testing.T) {
	withTempHome(t)
	stubLoginSteps(t,
		&bridge.DeviceCodeResponse{VerificationURIComplete: "https://x.ai/v", UserCode: "U"},
		&bridge.DeviceTokenResult{AccessToken: "at", RefreshToken: "rt"},
	)

	// 非图形环境：launcher 返回 opened=false，不阻塞登录。
	launch := func(url string) (bool, error) {
		return false, nil
	}
	out := captureStdout(t, func() {
		if err := run([]string{"auth", "login"}, launch); err != nil {
			t.Fatalf("run auth login: %v", err)
		}
	})
	if !contains(out, "no local graphical browser") {
		t.Errorf("should print manual-open note when browser not opened, got %q", out)
	}
}

func TestRun_AuthLoginBrowserFailureNonFatal(t *testing.T) {
	withTempHome(t)
	stubLoginSteps(t,
		&bridge.DeviceCodeResponse{VerificationURIComplete: "https://x.ai/v", UserCode: "U"},
		&bridge.DeviceTokenResult{AccessToken: "at", RefreshToken: "rt"},
	)

	// 浏览器启动失败：只提示，不影响登录成功退出。
	launch := func(url string) (bool, error) {
		return false, context.DeadlineExceeded
	}
	out := captureStdout(t, func() {
		if err := run([]string{"auth", "login"}, launch); err != nil {
			t.Fatalf("browser failure must not fail login: %v", err)
		}
	})
	if !contains(out, "could not open browser") {
		t.Errorf("should note browser failure, got %q", out)
	}
}

// TestRun_AuthLoginFallsBackToVerificationURI 验证 verification_uri_complete
// 缺失时，CLI 实际回退使用 verification_uri 展示，而不是失败或展示空 URL。
// 注意：只桩 pollDeviceToken/completeLogin，保留自定义 startDeviceCode 桩
// （返回缺 complete 的原始 device），避免 stubLoginSteps 覆盖掉该桩。
func TestRun_AuthLoginFallsBackToVerificationURI(t *testing.T) {
	withTempHome(t)

	origStart := startDeviceCode
	startDeviceCode = func(ctx context.Context) (*bridge.DeviceCodeResponse, string, error) {
		// 模拟 xAI 未返回 verification_uri_complete，仅返回 verification_uri。
		return &bridge.DeviceCodeResponse{
			VerificationURI:         "https://auth.x.ai/activate",
			VerificationURIComplete: "",
			UserCode:                "U",
		}, "https://auth.x.ai/oauth2/token", nil
	}
	t.Cleanup(func() { startDeviceCode = origStart })

	// 仅桩 poll/complete 两步，保留上面的 startDeviceCode 桩（缺 complete）。
	origPoll := pollDeviceToken
	origComplete := completeLogin
	pollDeviceToken = func(ctx context.Context, tokenEndpoint string, d *bridge.DeviceCodeResponse) (*bridge.DeviceTokenResult, error) {
		return &bridge.DeviceTokenResult{AccessToken: "at", RefreshToken: "rt"}, nil
	}
	completeLogin = func(ctx context.Context, r *bridge.DeviceTokenResult, tokenEndpoint string) (*bridge.OAuthState, error) {
		return &bridge.OAuthState{}, nil
	}
	t.Cleanup(func() {
		pollDeviceToken = origPoll
		completeLogin = origComplete
	})

	out := captureStdout(t, func() {
		if err := run([]string{"auth", "login"}, nil); err != nil {
			t.Fatalf("run auth login: %v", err)
		}
	})
	// 缺失 complete 时，CLI 应回退展示 verification_uri。
	if !contains(out, "https://auth.x.ai/activate") {
		t.Errorf("should fall back to verification_uri, got %q", out)
	}
}

func TestRun_AuthNoSubcommand(t *testing.T) {
	withTempHome(t)
	if err := run([]string{"auth"}, nil); err == nil {
		t.Fatal("expected error for auth without subcommand")
	}
}

func TestRun_AuthUnknownSubcommand(t *testing.T) {
	withTempHome(t)
	if err := run([]string{"auth", "status"}, nil); err == nil {
		t.Fatal("expected error for unknown auth subcommand")
	}
}

func TestRun_AuthLoginRejectsExtraArgs(t *testing.T) {
	withTempHome(t)
	if err := run([]string{"auth", "login", "extra", "--foo"}, nil); err == nil {
		t.Fatal("expected error for extra arguments after auth login")
	}
}

func TestRun_ServiceModeRejectsPositionalArgs(t *testing.T) {
	withTempHome(t)
	if err := run([]string{"-bridge-token", "secret", "unexpected-arg"}, nil); err == nil {
		t.Fatal("expected error for positional args in service mode")
	}
}

func TestRun_ServiceModeDispatches(t *testing.T) {
	withTempHome(t)

	var gotCfg *bridge.Config
	origService := runService
	runService = func(cfg *bridge.Config) error {
		gotCfg = cfg
		return nil
	}
	t.Cleanup(func() { runService = origService })

	if err := run([]string{"-bridge-token", "secret", "-listen", ":9099"}, nil); err != nil {
		t.Fatalf("run service mode: %v", err)
	}
	if gotCfg == nil {
		t.Fatal("service mode must dispatch to runService")
	}
	if gotCfg.BridgeToken != "secret" {
		t.Errorf("bridge token = %q, want secret", gotCfg.BridgeToken)
	}
	if gotCfg.Listen != ":9099" {
		t.Errorf("listen = %q, want :9099", gotCfg.Listen)
	}
}

func TestRun_ServiceModeMissingToken(t *testing.T) {
	withTempHome(t)
	if err := run([]string{"-listen", ":9099"}, nil); err == nil {
		t.Fatal("expected error for missing bridge token")
	}
}

func TestGraphicalDetector_Rules(t *testing.T) {
	withTempHome(t)

	t.Run("remote session detected via SSH_TTY", func(t *testing.T) {
		withTempHome(t)
		t.Setenv("SSH_TTY", "/dev/pts/0")
		t.Setenv("DISPLAY", ":0")
		if defaultGraphicalDetector() {
			t.Error("remote SSH session should not open browser")
		}
	})

	t.Run("cloud shell env is remote", func(t *testing.T) {
		withTempHome(t)
		t.Setenv("CLOUD_SHELL", "true")
		if defaultGraphicalDetector() {
			t.Error("cloud shell should be treated as remote")
		}
	})

	t.Run("local linux with DISPLAY opens", func(t *testing.T) {
		withTempHome(t)
		t.Setenv("DISPLAY", ":0")
		if !defaultGraphicalDetector() {
			t.Error("local linux with DISPLAY should open browser")
		}
	})

	t.Run("linux without DISPLAY does not open", func(t *testing.T) {
		withTempHome(t)
		os.Unsetenv("DISPLAY")
		os.Unsetenv("WAYLAND_DISPLAY")
		if defaultGraphicalDetector() {
			t.Error("linux without display server should not open browser")
		}
	})
}

// contains 判定 s 是否包含子串 sub。
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
