package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/handler"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/Marstheway/oh-my-api/internal/server"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/Marstheway/oh-my-api/internal/token"
	"github.com/stretchr/testify/require"
)

// TestRedirectApplyIntegration 测试 redirect 的完整 CRUD 和 Apply 流程
// 这是端到端集成测试，模拟真实的 API 启动和运行时配置更新
func TestRedirectApplyIntegration(t *testing.T) {
	// 1. 准备测试配置文件
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// 使用固定端口（测试时确保端口可用）
	port := 18082
	yamlContent := fmt.Sprintf(`
server:
  listen: ":%d"
  admin:
    password: "test-password"
  timeout: "30s"

inbound:
  auth:
    keys:
      - name: "test"
        key: "sk-test-key"

providers:
  openai:
    endpoint: "https://api.openai.com"
    api_key: "sk-openai-key"
    protocol: "openai"

model_groups:
  - name: "gpt-4"
    model: "openai/gpt-4"
  - name: "gpt-4o"
    model: "openai/gpt-4o"

redirect:
  gpt4: "gpt-4"

database:
  path: ":memory:"
`, port)

	err := os.WriteFile(configPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	// 2. 初始化 token estimator
	if err := token.Init(); err != nil {
		t.Logf("token.Init skipped: %v", err)
	}

	// 3. 初始化 stats database
	dbPath := ":memory:"
	err = stats.Init(dbPath)
	require.NoError(t, err)
	defer stats.Close()

	// 4. 加载配置
	cfg, err := config.Load(configPath)
	require.NoError(t, err)

	// 5. 初始化 resolver, client, scheduler
	timeout := 30 * time.Second
	resolver, err := model.NewResolver(cfg)
	require.NoError(t, err)

	client := provider.NewClient(cfg.Providers.Items, timeout, 0, 0)
	rlManager := ratelimit.NewManager(cfg.Providers.Items)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched := scheduler.New(rlManager, client, healthChecker, timeout, 0, 0)
	handler.Init(cfg, resolver, sched)

	// 6. 创建 runtimeconfig manager
	rebuilder := &runtimeconfig.DefaultRuntimeRebuilder{
		NewResolver: func(c *config.Config) (*model.Resolver, error) {
			return model.NewResolver(c)
		},
		NewScheduler: func(c *config.Config, r *model.Resolver) (*scheduler.Scheduler, error) {
			return scheduler.New(ratelimit.NewManager(c.Providers.Items), client, healthChecker, timeout, 0, 0), nil
		},
	}
	reinitHandler := &runtimeconfig.DefaultReinitHandler{
		InitFunc: handler.Init,
	}
	runtimeManager, err := runtimeconfig.NewManager(cfg, configPath, rebuilder, reinitHandler)
	require.NoError(t, err)

	// 7. 启动服务器
	metricsHandler := metrics.Init()

	// 使用 goroutine 启动服务器
	serverErrCh := make(chan error, 1)
	go func() {
		err := server.Run(cfg, metricsHandler, runtimeManager, cascade.NewHubRegistry())
		serverErrCh <- err
	}()

	// 等待服务器启动
	time.Sleep(500 * time.Millisecond)

	// 检查服务器是否正常启动
	select {
	case err := <-serverErrCh:
		t.Fatalf("server failed to start: %v", err)
	default:
	}

	// 8. 登录并获取 session
	baseURL := fmt.Sprintf("http://localhost:%d", port)
	adminBaseURL := baseURL + "/admin/runtime-config"

	client2 := &http.Client{}
	loginData := map[string]string{"password": "test-password"}
	loginBody, _ := json.Marshal(loginData)
	loginResp, err := client2.Post(baseURL+"/admin/login", "application/json", bytes.NewReader(loginBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, loginResp.StatusCode)
	defer loginResp.Body.Close()

	// 获取 session cookie
	var sessionCookie *http.Cookie
	for _, cookie := range loginResp.Cookies() {
		if cookie.Name == "admin_session" {
			sessionCookie = cookie
			break
		}
	}
	require.NotNil(t, sessionCookie, "should have session cookie")

	// Helper function for authenticated requests
	doRequest := func(method, url string, body io.Reader) (*http.Response, []byte) {
		req, _ := http.NewRequest(method, url, body)
		req.AddCookie(sessionCookie)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client2.Do(req)
		require.NoError(t, err)
		respBody, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		resp.Body.Close()
		return resp, respBody
	}

	t.Run("GetDraft", func(t *testing.T) {
		resp, body := doRequest("GET", adminBaseURL+"/draft", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var draft map[string]interface{}
		err = json.Unmarshal(body, &draft)
		require.NoError(t, err)

		redirects := draft["redirect"].([]interface{})
		require.Len(t, redirects, 1)
		found := false
		for _, r := range redirects {
			rc := r.(map[string]interface{})
			if rc["source"] == "gpt4" {
				require.Equal(t, "gpt-4", rc["target"])
				found = true
				break
			}
		}
		require.True(t, found, "redirect gpt4 not found")
	})

	t.Run("CreateRedirect", func(t *testing.T) {
		input := map[string]string{
			"source": "fast-model",
			"target": "gpt-4o",
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("POST", adminBaseURL+"/draft/redirects", bytes.NewReader(body))
		require.Equal(t, http.StatusCreated, resp.StatusCode, "response: %s", string(respBody))

		var redirect map[string]interface{}
		err = json.Unmarshal(respBody, &redirect)
		require.NoError(t, err)
		require.Equal(t, "fast-model", redirect["source"])
		require.Equal(t, "gpt-4o", redirect["target"])
	})

	t.Run("GetRedirectAfterCreate", func(t *testing.T) {
		resp, body := doRequest("GET", adminBaseURL+"/draft/redirects/fast-model", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var redirect map[string]interface{}
		err = json.Unmarshal(body, &redirect)
		require.NoError(t, err)
		require.Equal(t, "fast-model", redirect["source"])
		require.Equal(t, "gpt-4o", redirect["target"])
	})

	t.Run("ApplyAfterCreate", func(t *testing.T) {
		body, _ := json.Marshal(map[string]bool{"confirm": true})
		resp, respBody := doRequest("POST", adminBaseURL+"/apply", bytes.NewReader(body))

		t.Logf("Apply response: %s", string(respBody))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var applyResp map[string]interface{}
		err = json.Unmarshal(respBody, &applyResp)
		require.NoError(t, err)
		require.True(t, applyResp["success"].(bool))

		// 验证配置文件已更新
		reloadCfg, err := config.Load(configPath)
		require.NoError(t, err)
		require.Len(t, reloadCfg.Redirect, 2)
		require.Equal(t, "gpt-4", findRedirectTarget(reloadCfg.Redirect, "gpt4"))
		require.Equal(t, "gpt-4o", findRedirectTarget(reloadCfg.Redirect, "fast-model"))
	})

	t.Run("UpdateRedirect", func(t *testing.T) {
		input := map[string]string{
			"source": "fast-model",
			"target": "gpt-4",
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("PUT", adminBaseURL+"/draft/redirects/fast-model", bytes.NewReader(body))
		require.Equal(t, http.StatusOK, resp.StatusCode, "response: %s", string(respBody))

		var redirect map[string]interface{}
		err = json.Unmarshal(respBody, &redirect)
		require.NoError(t, err)
		require.Equal(t, "gpt-4", redirect["target"])
	})

	t.Run("ApplyAfterUpdate", func(t *testing.T) {
		body, _ := json.Marshal(map[string]bool{"confirm": true})
		resp, respBody := doRequest("POST", adminBaseURL+"/apply", bytes.NewReader(body))

		t.Logf("Apply response: %s", string(respBody))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var applyResp map[string]interface{}
		err = json.Unmarshal(respBody, &applyResp)
		require.NoError(t, err)
		require.True(t, applyResp["success"].(bool))

		// 验证配置文件已更新
		reloadCfg, err := config.Load(configPath)
		require.NoError(t, err)
		require.Len(t, reloadCfg.Redirect, 2)
		require.Equal(t, "gpt-4", findRedirectTarget(reloadCfg.Redirect, "gpt4"))
		require.Equal(t, "gpt-4", findRedirectTarget(reloadCfg.Redirect, "fast-model"))
	})

	t.Run("CreateAliasToAliasRedirect", func(t *testing.T) {
		input := map[string]string{
			"source": "super-fast",
			"target": "fast-model",
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("POST", adminBaseURL+"/draft/redirects", bytes.NewReader(body))
		require.Equal(t, http.StatusCreated, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("ListRedirectsWithChain", func(t *testing.T) {
		resp, body := doRequest("GET", adminBaseURL+"/draft/redirects", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var redirects []map[string]interface{}
		err = json.Unmarshal(body, &redirects)
		require.NoError(t, err)
		require.Len(t, redirects, 3)

		// 找到 super-fast redirect
		var superFastRedirect *map[string]interface{}
		for i := range redirects {
			if redirects[i]["source"] == "super-fast" {
				superFastRedirect = &redirects[i]
				break
			}
		}
		require.NotNil(t, superFastRedirect)
		require.Equal(t, "fast-model", (*superFastRedirect)["target"])
		require.Equal(t, "gpt-4", (*superFastRedirect)["resolved_group"])
		// chainLength: super-fast -> fast-model -> gpt-4 (经过 1 个中间 redirect)
		require.Equal(t, float64(1), (*superFastRedirect)["chain_length"])
	})

	t.Run("ApplyWithAliasChain", func(t *testing.T) {
		body, _ := json.Marshal(map[string]bool{"confirm": true})
		resp, respBody := doRequest("POST", adminBaseURL+"/apply", bytes.NewReader(body))

		t.Logf("Apply response: %s", string(respBody))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var applyResp map[string]interface{}
		err = json.Unmarshal(respBody, &applyResp)
		require.NoError(t, err)
		require.True(t, applyResp["success"].(bool))

		// 验证配置文件已更新
		reloadCfg, err := config.Load(configPath)
		require.NoError(t, err)
		require.Len(t, reloadCfg.Redirect, 3)
		require.Equal(t, "gpt-4", findRedirectTarget(reloadCfg.Redirect, "gpt4"))
		require.Equal(t, "gpt-4", findRedirectTarget(reloadCfg.Redirect, "fast-model"))
		require.Equal(t, "fast-model", findRedirectTarget(reloadCfg.Redirect, "super-fast"))
	})

	t.Run("DeleteRedirect", func(t *testing.T) {
		resp, _ := doRequest("DELETE", adminBaseURL+"/draft/redirects/super-fast", nil)
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("ApplyAfterDelete", func(t *testing.T) {
		body, _ := json.Marshal(map[string]bool{"confirm": true})
		resp, respBody := doRequest("POST", adminBaseURL+"/apply", bytes.NewReader(body))

		t.Logf("Apply response: %s", string(respBody))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var applyResp map[string]interface{}
		err = json.Unmarshal(respBody, &applyResp)
		require.NoError(t, err)
		require.True(t, applyResp["success"].(bool))

		// 验证配置文件已更新
		reloadCfg, err := config.Load(configPath)
		require.NoError(t, err)
		require.Len(t, reloadCfg.Redirect, 2)
		found := false
		for _, rc := range reloadCfg.Redirect {
			if rc.Source == "super-fast" {
				found = true
				break
			}
		}
		require.False(t, found, "super-fast should be deleted")
	})

	t.Run("CircularRedirectPrevention", func(t *testing.T) {
		// 尝试创建自引用的 redirect
		input := map[string]string{
			"source": "self-loop",
			"target": "self-loop",
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("POST", adminBaseURL+"/draft/redirects", bytes.NewReader(body))
		// 应该返回 422 (Unprocessable Entity)
		require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

		t.Logf("Circular redirect error response: %s", string(respBody))
	})
}
func findRedirectTarget(redirects config.RedirectConfigs, source string) string {
	for _, rc := range redirects {
		if rc.Source == source {
			return rc.Target
		}
	}
	return ""
}
