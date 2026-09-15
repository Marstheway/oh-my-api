package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/bridge"
)

// browserLauncher 尝试在本地图形环境打开一个 URL。
// 返回值表示是否成功打开；失败（无图形环境、启动器不可用）不应阻塞登录流程。
type browserLauncher func(url string) (opened bool, err error)

// defaultGraphicalDetector 对齐 Hermes 的图形环境判定：
//   - 远程会话（SSH 客户端/tty 或已知云 shell 环境变量）不开浏览器；
//   - Linux 需要 $DISPLAY 或 $WAYLAND_DISPLAY，否则不开；
//   - macOS/Windows 默认可开；其他（无 GUI）不开。
func defaultGraphicalDetector() bool {
	if isRemoteSession() {
		return false
	}
	if _, ok := os.LookupEnv("DISPLAY"); ok {
		return true
	}
	if _, ok := os.LookupEnv("WAYLAND_DISPLAY"); ok {
		return true
	}
	// 非 Linux（darwin/windows）默认可开；Linux 无显示服务器则不可开。
	if runtime.GOOS == "linux" {
		return false
	}
	return true
}

// defaultBrowserLauncher 在本地图形环境尝试打开浏览器；
// 非图形环境或启动失败时返回 opened=false，不阻塞登录。
func defaultBrowserLauncher(url string) (bool, error) {
	if !defaultGraphicalDetector() {
		return false, nil
	}
	return tryOpenBrowser(url)
}

// isRemoteSession 对齐 Hermes _is_remote_session：SSH 客户端/tty 或
// 已知云 shell 环境变量视为远程会话，不开本地浏览器。
func isRemoteSession() bool {
	if os.Getenv("SSH_CLIENT") != "" || os.Getenv("SSH_TTY") != "" {
		return true
	}
	for _, v := range []string{"CLOUD_SHELL", "CODESPACES", "CODESPACE_NAME", "GITPOD_WORKSPACE_ID", "REPL_ID", "STACKBLITZ"} {
		if os.Getenv(v) != "" {
			return true
		}
	}
	return false
}

// tryOpenBrowser 按平台尝试打开默认浏览器。
func tryOpenBrowser(url string) (bool, error) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	args = append(args, url)
	if err := exec.Command(cmd, args...).Start(); err != nil {
		return false, err
	}
	return true, nil
}

// startDeviceCode / pollDeviceToken / completeLogin 是登录流程的可替换入口，
// 默认指向 bridge 包对应的拆分步骤，便于测试验证"设备码返回即展示并启动浏览器、
// 再开始轮询"的交互顺序而不访问网络。
var startDeviceCode = func(ctx context.Context) (*bridge.DeviceCodeResponse, string, error) {
	return bridge.StartDeviceCode(ctx)
}
var pollDeviceToken = func(ctx context.Context, tokenEndpoint string, device *bridge.DeviceCodeResponse) (*bridge.DeviceTokenResult, error) {
	return bridge.PollDeviceToken(ctx, tokenEndpoint, device)
}
var completeLogin = func(ctx context.Context, tok *bridge.DeviceTokenResult, tokenEndpoint string) (*bridge.OAuthState, error) {
	return bridge.CompleteLogin(ctx, tok, tokenEndpoint)
}

// runAuthLogin 执行 xAI 设备码登录的分步序列：
// 1) 申请设备码并立即展示 verification URL / user code；
// 2) 按图形环境规则尝试打开浏览器（失败/不可用不阻塞轮询）；
// 3) 轮询 token endpoint 直到成功；
// 4) 持久化状态。
// 浏览器启动与轮询不互相阻塞；该函数成功只完成登录，绝不启动 HTTP server。
func runAuthLogin(launch browserLauncher) error {
	ctx := context.Background()

	// 设备码：收到后立即展示，不等登录成功。
	device, tokenEndpoint, err := startDeviceCode(ctx)
	if err != nil {
		return fmt.Errorf("auth login failed: %w", err)
	}
	// verification_uri_complete 优先；缺失时回退到 verification_uri（对齐 Hermes 展示规则）。
	verifyURL := strings.TrimSpace(device.VerificationURIComplete)
	if verifyURL == "" {
		verifyURL = strings.TrimSpace(device.VerificationURI)
	}
	fmt.Println("To complete xAI OAuth authorization, visit:")
	fmt.Printf("  %s\n", verifyURL)
	fmt.Printf("  User code: %s\n", device.UserCode)

	// 按图形规则尝试打开浏览器；启动失败或不可用时仅提示，不阻塞轮询。
	if launch != nil {
		if opened, berr := launch(verifyURL); berr != nil {
			fmt.Fprintf(os.Stdout, "note: could not open browser automatically (%v); open the URL above manually\n", berr)
		} else if !opened {
			fmt.Println("note: no local graphical browser detected; open the URL above manually")
		}
	}

	// 展示完成、浏览器已尝试启动后，明确告知用户正在等待授权与轮询。
	fmt.Println("Waiting for you to authorize in the browser, then polling for the token...")

	// 轮询（浏览器已非阻塞启动，此处顺序执行直到授权完成）。
	tok, err := pollDeviceToken(ctx, tokenEndpoint, device)
	if err != nil {
		return fmt.Errorf("auth login failed: %w", err)
	}
	if _, err := completeLogin(ctx, tok, tokenEndpoint); err != nil {
		return fmt.Errorf("auth login failed: %w", err)
	}

	statePath, perr := bridge.DefaultAuthStatePath()
	if perr != nil {
		return fmt.Errorf("cannot resolve auth state path: %w", perr)
	}
	fmt.Println("xAI OAuth authorization complete. State saved at:")
	fmt.Printf("  %s\n", statePath)
	return nil
}

const usageText = `Usage:
  oh-my-api-bridge auth login                 Perform xAI OAuth device-code login
  oh-my-api-bridge [-listen ADDR] -bridge-token TOKEN
                                             Run the OAuth bridge proxy service

Service flags:
  -listen       HTTP listen address (default ":8081")
  -bridge-token bridge bearer auth token (required)

Login:
  auth login   completes device-code authorization and stores state at
               ~/.oh-my-api/bridge/auth.json, then exits without starting a server`

func main() {
	if err := run(os.Args[1:], defaultBrowserLauncher); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintln(os.Stderr, usageText)
		os.Exit(1)
	}
}

// runService 是服务模式入口，默认使用 bridge.Run。
// 测试可临时替换为 no-op 以验证分派而不启动真实 server。
var runService = func(cfg *bridge.Config) error {
	return bridge.Run(cfg)
}

// run 是 main 的可测试入口：分派 auth login 与服务模式。
// launch 为浏览器启动器（nil 表示跳过尝试）。
func run(args []string, launch browserLauncher) error {
	if len(args) > 0 && args[0] == "auth" {
		if len(args) < 2 {
			return fmt.Errorf("auth requires a subcommand (login)")
		}
		switch args[1] {
		case "login":
			// 拒绝多余位置参数：auth login 不接受除子命令外的任何参数。
			if len(args) > 2 {
				return fmt.Errorf("auth login takes no extra arguments, got: %v", args[2:])
			}
			return runAuthLogin(launch)
		default:
			return fmt.Errorf("unknown auth subcommand %q (only \"login\" is supported)", args[1])
		}
	}

	// 服务模式：解析服务参数（已移除 -auth-json）。
	cfg, err := bridge.LoadConfig(args)
	if err != nil {
		return err
	}
	return runService(cfg)
}
