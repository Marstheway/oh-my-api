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

// TestModelGroupApplyIntegration 测试 model group 的完整 CRUD 和 Apply 流程
func TestModelGroupApplyIntegration(t *testing.T) {
	// 1. 准备测试配置文件
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	port := 18083
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
  anthropic:
    endpoint: "https://api.anthropic.com"
    api_key: "sk-anthropic-key"
    protocol: "anthropic"

model_groups:
  - name: "gpt-4"
    model: "openai/gpt-4"
  - name: "claude"
    model: "anthropic/claude-3"

redirect:
  gpt4: "gpt-4"

database:
  path: ":memory:"
`, port)

	err := os.WriteFile(configPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	// 2. 初始化
	if err := token.Init(); err != nil {
		t.Logf("token.Init skipped: %v", err)
	}

	dbPath := ":memory:"
	err = stats.Init(dbPath)
	require.NoError(t, err)
	defer stats.Close()

	cfg, err := config.Load(configPath)
	require.NoError(t, err)

	timeout := 30 * time.Second
	resolver, err := model.NewResolver(cfg)
	require.NoError(t, err)

	client := provider.NewClient(cfg.Providers.Items, timeout, 0)
	rlManager := ratelimit.NewManager(cfg.Providers.Items)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched := scheduler.New(rlManager, client, healthChecker, timeout, 0)
	handler.Init(cfg, resolver, sched)

	rebuilder := &runtimeconfig.DefaultRuntimeRebuilder{
		NewResolver: func(c *config.Config) (*model.Resolver, error) {
			return model.NewResolver(c)
		},
		NewScheduler: func(c *config.Config, r *model.Resolver) (*scheduler.Scheduler, error) {
			return scheduler.New(rlManager, client, healthChecker, timeout, 0), nil
		},
	}
	reinitHandler := &runtimeconfig.DefaultReinitHandler{
		InitFunc: handler.Init,
	}
	runtimeManager, err := runtimeconfig.NewManager(cfg, configPath, rebuilder, reinitHandler)
	require.NoError(t, err)

	metricsHandler := metrics.Init()
	serverErrCh := make(chan error, 1)
	go func() {
		err := server.Run(cfg, metricsHandler, runtimeManager)
		serverErrCh <- err
	}()

	time.Sleep(500 * time.Millisecond)

	select {
	case err := <-serverErrCh:
		t.Fatalf("server failed to start: %v", err)
	default:
	}

	// 3. 登录
	baseURL := fmt.Sprintf("http://localhost:%d", port)
	adminBaseURL := baseURL + "/admin/runtime-config"

	client2 := &http.Client{}
	loginData := map[string]string{"password": "test-password"}
	loginBody, _ := json.Marshal(loginData)
	loginResp, err := client2.Post(baseURL+"/admin/login", "application/json", bytes.NewReader(loginBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, loginResp.StatusCode)
	defer loginResp.Body.Close()

	var sessionCookie *http.Cookie
	for _, cookie := range loginResp.Cookies() {
		if cookie.Name == "admin_session" {
			sessionCookie = cookie
			break
		}
	}
	require.NotNil(t, sessionCookie, "should have session cookie")

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

	t.Run("CreateModelGroup_SingleModel", func(t *testing.T) {
		input := map[string]interface{}{
			"name":  "gpt-4o",
			"model": "openai/gpt-4o",
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("POST", adminBaseURL+"/draft/model-groups", bytes.NewReader(body))
		require.Equal(t, http.StatusCreated, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("CreateModelGroup_MultiModelsWithWeight", func(t *testing.T) {
		input := map[string]interface{}{
			"name": "multi-model-group",
			"mode": "loadbalance",
			"models": []map[string]interface{}{
				{"model": "openai/gpt-4", "weight": 2},
				{"model": "anthropic/claude-3", "weight": 1},
			},
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("POST", adminBaseURL+"/draft/model-groups", bytes.NewReader(body))
		require.Equal(t, http.StatusCreated, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("CreateModelGroup_WithFallback", func(t *testing.T) {
		input := map[string]interface{}{
			"name":     "group-with-fallback",
			"model":    "openai/gpt-4",
			"fallback": []string{"claude", "gpt-4o"},
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("POST", adminBaseURL+"/draft/model-groups", bytes.NewReader(body))
		require.Equal(t, http.StatusCreated, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("CreateModelGroup_WithMetadata", func(t *testing.T) {
		contextLength := 128000
		input := map[string]interface{}{
			"name":  "group-with-metadata",
			"model": "openai/gpt-4",
			"model_metadata": map[string]interface{}{
				"context_length": contextLength,
			},
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("POST", adminBaseURL+"/draft/model-groups", bytes.NewReader(body))
		require.Equal(t, http.StatusCreated, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("CreateModelGroup_ExposureHidden", func(t *testing.T) {
		input := map[string]interface{}{
			"name":     "hidden-group",
			"model":    "openai/gpt-4",
			"exposure": "hidden",
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("POST", adminBaseURL+"/draft/model-groups", bytes.NewReader(body))
		require.Equal(t, http.StatusCreated, resp.StatusCode, "response: %s", string(respBody))
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
		require.Len(t, reloadCfg.ModelGroups, 7) // 初始2个 + 新增5个
	})

	t.Run("UpdateModelGroup_Rename", func(t *testing.T) {
		input := map[string]interface{}{
			"name":  "gpt-4o-updated",
			"model": "openai/gpt-4o",
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("PUT", adminBaseURL+"/draft/model-groups/gpt-4o", bytes.NewReader(body))
		require.Equal(t, http.StatusOK, resp.StatusCode, "response: %s", string(respBody))

		var group map[string]interface{}
		err = json.Unmarshal(respBody, &group)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o-updated", group["name"])
	})

	t.Run("ApplyAfterRename", func(t *testing.T) {
		body, _ := json.Marshal(map[string]bool{"confirm": true})
		resp, respBody := doRequest("POST", adminBaseURL+"/apply", bytes.NewReader(body))

		t.Logf("Apply response: %s", string(respBody))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// 验证配置文件
		reloadCfg, err := config.Load(configPath)
		require.NoError(t, err)

		var found bool
		for _, g := range reloadCfg.ModelGroups {
			if g.Name == "gpt-4o-updated" {
				found = true
				break
			}
		}
		require.True(t, found, "gpt-4o-updated should exist")
	})

	t.Run("UpdateModelGroup_AddFallback", func(t *testing.T) {
		input := map[string]interface{}{
			"name":     "gpt-4",
			"model":    "openai/gpt-4",
			"fallback": []string{"claude"},
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("PUT", adminBaseURL+"/draft/model-groups/gpt-4", bytes.NewReader(body))
		require.Equal(t, http.StatusOK, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("ApplyAfterUpdateFallback", func(t *testing.T) {
		body, _ := json.Marshal(map[string]bool{"confirm": true})
		resp, _ := doRequest("POST", adminBaseURL+"/apply", bytes.NewReader(body))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// 验证 fallback
		reloadCfg, err := config.Load(configPath)
		require.NoError(t, err)

		var gpt4Group *config.ModelGroupConfig
		for i := range reloadCfg.ModelGroups {
			if reloadCfg.ModelGroups[i].Name == "gpt-4" {
				gpt4Group = &reloadCfg.ModelGroups[i]
				break
			}
		}
		require.NotNil(t, gpt4Group)
	})

	t.Run("RenameModelGroup_ReferencedByRedirect", func(t *testing.T) {
		// gpt-4 被 redirect "gpt4" 引用，改名应该自动更新 redirect
		input := map[string]interface{}{
			"name":  "gpt-4-new",
			"model": "openai/gpt-4",
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("PUT", adminBaseURL+"/draft/model-groups/gpt-4", bytes.NewReader(body))
		require.Equal(t, http.StatusOK, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("ApplyAfterRenameReferencedGroup", func(t *testing.T) {
		body, _ := json.Marshal(map[string]bool{"confirm": true})
		resp, _ := doRequest("POST", adminBaseURL+"/apply", bytes.NewReader(body))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// 验证 redirect 也被更新
		reloadCfg, err := config.Load(configPath)
		require.NoError(t, err)
		require.Equal(t, "gpt-4-new", findRedirectTarget(reloadCfg.Redirect, "gpt4"))
	})

	t.Run("DeleteModelGroup_NotReferenced", func(t *testing.T) {
		resp, _ := doRequest("DELETE", adminBaseURL+"/draft/model-groups/hidden-group", nil)
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("ApplyAfterDelete", func(t *testing.T) {
		body, _ := json.Marshal(map[string]bool{"confirm": true})
		resp, _ := doRequest("POST", adminBaseURL+"/apply", bytes.NewReader(body))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		reloadCfg, err := config.Load(configPath)
		require.NoError(t, err)

		for _, g := range reloadCfg.ModelGroups {
			require.NotEqual(t, "hidden-group", g.Name)
		}
	})

	t.Run("DeleteModelGroup_ReferencedByRedirect_ShouldFail", func(t *testing.T) {
		// gpt-4-new 被 redirect "gpt4" 引用，应该删除失败
		resp, respBody := doRequest("DELETE", adminBaseURL+"/draft/model-groups/gpt-4-new", nil)
		require.Equal(t, http.StatusConflict, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("CreateModelGroup_ReferencingOtherGroup", func(t *testing.T) {
		// 创建引用其他 group 的 model group
		input := map[string]interface{}{
			"name": "composite-group",
			"mode": "failover",
			"models": []map[string]interface{}{
				{"model": "claude"}, // 引用其他 group
				{"model": "openai/gpt-4"},
			},
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("POST", adminBaseURL+"/draft/model-groups", bytes.NewReader(body))
		require.Equal(t, http.StatusCreated, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("ApplyWithGroupReference", func(t *testing.T) {
		body, _ := json.Marshal(map[string]bool{"confirm": true})
		resp, _ := doRequest("POST", adminBaseURL+"/apply", bytes.NewReader(body))
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("RenameModelGroup_ReferencedByOtherGroup", func(t *testing.T) {
		// claude 被 composite-group 引用，改名应该自动更新引用
		input := map[string]interface{}{
			"name":  "claude-updated",
			"model": "anthropic/claude-3",
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("PUT", adminBaseURL+"/draft/model-groups/claude", bytes.NewReader(body))
		require.Equal(t, http.StatusOK, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("ApplyAfterRenameReferencedGroup2", func(t *testing.T) {
		body, _ := json.Marshal(map[string]bool{"confirm": true})
		resp, _ := doRequest("POST", adminBaseURL+"/apply", bytes.NewReader(body))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// 验证 composite-group 中的引用被更新
		reloadCfg, err := config.Load(configPath)
		require.NoError(t, err)

		var compositeGroup *config.ModelGroupConfig
		for i := range reloadCfg.ModelGroups {
			if reloadCfg.ModelGroups[i].Name == "composite-group" {
				compositeGroup = &reloadCfg.ModelGroups[i]
				break
			}
		}
		require.NotNil(t, compositeGroup)
		require.Len(t, compositeGroup.Models, 2)
		require.Equal(t, "claude-updated", compositeGroup.Models[0].Model)
	})

	t.Run("DeleteModelGroup_ReferencedByOtherGroup_ShouldFail", func(t *testing.T) {
		// claude-updated 被 composite-group 引用，删除应该失败
		resp, respBody := doRequest("DELETE", adminBaseURL+"/draft/model-groups/claude-updated", nil)
		require.Equal(t, http.StatusConflict, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("CreateModelGroup_ConflictWithExistingName", func(t *testing.T) {
		input := map[string]interface{}{
			"name":  "gpt-4-new", // 已存在
			"model": "openai/gpt-4",
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("POST", adminBaseURL+"/draft/model-groups", bytes.NewReader(body))
		require.Equal(t, http.StatusConflict, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("CreateModelGroup_ConflictWithRedirectAlias", func(t *testing.T) {
		input := map[string]interface{}{
			"name":  "gpt4", // redirect alias
			"model": "openai/gpt-4",
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("POST", adminBaseURL+"/draft/model-groups", bytes.NewReader(body))
		require.Equal(t, http.StatusConflict, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("CreateModelGroup_InvalidMode", func(t *testing.T) {
		input := map[string]interface{}{
			"name":  "invalid-mode-group",
			"model": "openai/gpt-4",
			"mode":  "invalid-mode",
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("POST", adminBaseURL+"/draft/model-groups", bytes.NewReader(body))
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, "response: %s", string(respBody))
	})

	t.Run("CreateModelGroup_InvalidWeight", func(t *testing.T) {
		input := map[string]interface{}{
			"name": "invalid-weight-group",
			"models": []map[string]interface{}{
				{"model": "openai/gpt-4", "weight": 0},
			},
		}
		body, _ := json.Marshal(input)

		resp, respBody := doRequest("POST", adminBaseURL+"/draft/model-groups", bytes.NewReader(body))
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, "response: %s", string(respBody))
	})
}