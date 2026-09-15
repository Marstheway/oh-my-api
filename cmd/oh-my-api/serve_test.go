package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func minimalServeConfig() *config.Config {
	return &config.Config{
		Inbound: config.InboundConfig{
			Auth: config.AuthConfig{
				Keys: []config.KeyConfig{
					{Name: "test", Key: "sk-test"},
				},
			},
		},
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{
				"myprovider": {
					APIKey:   "sk-provider",
					Endpoint: "https://api.example.com",
				},
			},
		},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:   "mygroup",
				Models: config.ModelEntries{{Model: "myprovider/gpt-4o"}},
			},
		},
	}
}

func TestRunServeConfigValidation(t *testing.T) {
	cfg := minimalServeConfig()
	cfg.Server.Timeout = "bad"

	called := false
	runServeWithExit(cfg, "", func(code int) {
		called = true
	})

	if !called {
		t.Fatal("expected exitFn to be called on invalid config")
	}
}

func TestRunServeSetsDefaultListen(t *testing.T) {
	cfg := minimalServeConfig()
	cfg.Server.Listen = ""
	cfg.Server.Timeout = "bad"

	called := false
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	runServeWithExit(cfg, configPath, func(code int) {
		called = true
	})

	if !called {
		t.Fatal("expected exitFn to be called on invalid config")
	}
	if cfg.Server.Listen != ":18000" {
		t.Fatalf("expected default listen, got %q", cfg.Server.Listen)
	}
}

func TestRunServeRequiresConfigPath(t *testing.T) {
	cfg := minimalServeConfig()

	called := false
	exitCode := 0
	runServeWithExit(cfg, "", func(code int) {
		called = true
		exitCode = code
	})

	if !called {
		t.Fatal("expected exitFn to be called when configPath is empty")
	}
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
}

func TestRunServeMissingTokenizer(t *testing.T) {
	// 构造一个合法的配置，但 configPath 所在目录不存在 deepseek_v3_tokenizer.json
	cfg := minimalServeConfig()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	var exitCode int
	called := false
	runServeWithExit(cfg, configPath, func(code int) {
		called = true
		exitCode = code
	})

	if !called {
		t.Fatal("expected exitFn to be called when deepseek_v3_tokenizer.json is missing")
	}
	if exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", exitCode)
	}
	// 目录下不应该存在 tokenizer 文件
	_, err := os.Stat(filepath.Join(tmpDir, "deepseek_v3_tokenizer.json"))
	if !os.IsNotExist(err) {
		t.Errorf("tokenizer file should not exist in temp dir, but stat returned: %v", err)
	}
}

// TestRunServeTokenizerExists 验证当 deepseek_v3_tokenizer.json 存在时，
// runServeWithExit 不会因 tokenizer 初始化失败而调用 exitFn(1)。
//
// 由于 token.Init 使用 sync.Once 且只能调用一次，本测试通过子进程隔离运行。
func TestRunServeTokenizerExists(t *testing.T) {
	if os.Getenv("TEST_SERVE_SUBPROCESS") == "1" {
		// 子进程：加载真实配置文件并执行 serve 流程。
		configPath := os.Getenv("TEST_CONFIG_PATH")
		cfg, err := config.Load(configPath)
		if err != nil {
			os.Exit(2)
		}
		runServeWithExit(cfg, configPath, func(code int) {
			os.Exit(code)
		})
		return
	}

	// 父进程：准备临时目录，复制 tokenizer 文件，写入配置，启动子进程。

	// 查找仓库内的 tokenizer 文件
	srcPath := filepath.Join("..", "..", "references", "deepseek_v3_tokenizer.json")
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		t.Skip("deepseek_v3_tokenizer.json not found in references/; skipping")
	}

	tmpDir := t.TempDir()

	srcData, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read tokenizer file: %v", err)
	}
	tokenizerPath := filepath.Join(tmpDir, "deepseek_v3_tokenizer.json")
	if err := os.WriteFile(tokenizerPath, srcData, 0644); err != nil {
		t.Fatalf("write tokenizer file: %v", err)
	}

	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := fmt.Sprintf(`server:
  listen: ":0"
database:
  path: %s
inbound:
  auth:
    keys:
      - name: test
        key: sk-test
providers:
  testp:
    endpoint: https://api.example.com
    api_key: sk-test
    protocol: openai
model_groups:
  - name: test-model
    models:
      - model: testp/gpt-4o
`, filepath.Join(tmpDir, "test.db"))
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunServeTokenizerExists$")
	cmd.Env = append(os.Environ(), "TEST_SERVE_SUBPROCESS=1", "TEST_CONFIG_PATH="+configPath)
	output, runErr := cmd.CombinedOutput()

	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return
		}
		if exitErr, ok := runErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			t.Fatalf("exitFn(1) called (tokenizer init should have succeeded)\nsubprocess output:\n%s", string(output))
		}
		t.Fatalf("subprocess error: %v\nsubprocess output:\n%s", runErr, string(output))
	}
}

func TestNewProviderClientUsesCurrentProviders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"super-grok": {Endpoint: upstream.URL, Protocols: []string{"openai.responses"}},
		}},
	}
	client := newProviderClient(cfg, time.Second, 0, 0)

	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/v1/models", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do("super-grok", req)
	if err != nil {
		t.Fatalf("client.Do(super-grok): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}
