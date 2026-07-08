package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// Test handlers using in-memory Manager (no file persistence needed for CRUD tests)

func TestAdminRuntimeConfigHandler_GetDraft(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.GET("/admin/runtime-config/draft", h.GetDraft)

	req := httptest.NewRequest("GET", "/admin/runtime-config/draft", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp DraftResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Len(t, resp.ModelGroups, 2) // gpt-4 and gpt-4o
	assert.Equal(t, "gpt-4", resp.ModelGroups[0].Name)

	// 验证 providers 字段已脱敏（只包含 endpoint 和 protocol）
	assert.Len(t, resp.Providers, 1)
	assert.Contains(t, resp.Providers, "openai")
	assert.Equal(t, "https://api.openai.com", resp.Providers["openai"].Endpoint)
	assert.Equal(t, []string{"openai.chat"}, resp.Providers["openai"].Protocols)

	// 验证 auth_keys 字段已脱敏（只返回 name）
	assert.Len(t, resp.AuthKeys, 1)
	assert.Equal(t, "test", resp.AuthKeys[0].Name)
}

func TestAdminRuntimeConfigHandler_GetModelGroup(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.GET("/admin/runtime-config/draft/model-groups/:name", h.GetModelGroup)

	// Found
	req := httptest.NewRequest("GET", "/admin/runtime-config/draft/model-groups/gpt-4", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Not found
	req = httptest.NewRequest("GET", "/admin/runtime-config/draft/model-groups/unknown", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAdminRuntimeConfigHandler_CreateModelGroup(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.POST("/admin/runtime-config/draft/model-groups", h.CreateModelGroup)

	// Success
	input := runtimeconfig.ModelGroupInput{
		Name:  "claude-3",
		Model: "anthropic/claude-3",
	}
	body, _ := json.Marshal(input)
	req := httptest.NewRequest("POST", "/admin/runtime-config/draft/model-groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	// Conflict - already exists
	input = runtimeconfig.ModelGroupInput{
		Name:  "gpt-4", // already in config
		Model: "openai/gpt-4",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/model-groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)

	// Bad request - empty name
	input = runtimeconfig.ModelGroupInput{
		Name:  "",
		Model: "openai/gpt-4",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/model-groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAdminRuntimeConfigHandler_UpdateModelGroup(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.PUT("/admin/runtime-config/draft/model-groups/:name", h.UpdateModelGroup)

	// Success - update mode
	input := runtimeconfig.ModelGroupInput{
		Name:  "gpt-4",
		Mode:  "failover",
		Model: "openai/gpt-4",
	}
	body, _ := json.Marshal(input)
	req := httptest.NewRequest("PUT", "/admin/runtime-config/draft/model-groups/gpt-4", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Not found
	input = runtimeconfig.ModelGroupInput{
		Name:  "unknown",
		Model: "openai/gpt-4",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("PUT", "/admin/runtime-config/draft/model-groups/unknown", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// Conflict - rename to existing
	input = runtimeconfig.ModelGroupInput{
		Name:  "gpt-4o", // already in config
		Model: "openai/gpt-4",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("PUT", "/admin/runtime-config/draft/model-groups/gpt-4", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestAdminRuntimeConfigHandler_DeleteModelGroup(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.DELETE("/admin/runtime-config/draft/model-groups/:name", h.DeleteModelGroup)

	// Success - delete unused group
	req := httptest.NewRequest("DELETE", "/admin/runtime-config/draft/model-groups/gpt-4o", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)

	// Not found
	req = httptest.NewRequest("DELETE", "/admin/runtime-config/draft/model-groups/unknown", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// Conflict - referenced by redirect
	req = httptest.NewRequest("DELETE", "/admin/runtime-config/draft/model-groups/gpt-4", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestAdminRuntimeConfigHandler_CreateAdaptiveModelGroup(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.POST("/admin/runtime-config/draft/model-groups", h.CreateModelGroup)

	// 合法 adaptive group：只包含 provider/model 叶子
	input := runtimeconfig.ModelGroupInput{
		Name: "adaptive-ok",
		Mode: "adaptive",
		Models: []runtimeconfig.ModelEntryInput{
			{Model: "openai/gpt-4o"},
			{Model: "openai/gpt-4"},
		},
	}
	body, _ := json.Marshal(input)
	req := httptest.NewRequest("POST", "/admin/runtime-config/draft/model-groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	// 非法 adaptive group：包含内部引用（不含 /）
	input = runtimeconfig.ModelGroupInput{
		Name: "adaptive-bad",
		Mode: "adaptive",
		Models: []runtimeconfig.ModelEntryInput{
			{Model: "openai/gpt-4o"},
			{Model: "internal-group"},
		},
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/model-groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAdminRuntimeConfigHandler_UpdateAdaptiveModelGroup(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.PUT("/admin/runtime-config/draft/model-groups/:name", h.UpdateModelGroup)

	// 合法更新：将现有 group 更新为 adaptive 模式（只包含 provider/model 叶子）
	input := runtimeconfig.ModelGroupInput{
		Name: "gpt-4",
		Mode: "adaptive",
		Models: []runtimeconfig.ModelEntryInput{
			{Model: "openai/gpt-4o"},
			{Model: "openai/gpt-4"},
		},
	}
	body, _ := json.Marshal(input)
	req := httptest.NewRequest("PUT", "/admin/runtime-config/draft/model-groups/gpt-4", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// 非法更新：adaptive 模式包含内部引用（不含 /）
	input = runtimeconfig.ModelGroupInput{
		Name: "gpt-4o",
		Mode: "adaptive",
		Models: []runtimeconfig.ModelEntryInput{
			{Model: "openai/gpt-4o"},
			{Model: "internal-group"},
		},
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("PUT", "/admin/runtime-config/draft/model-groups/gpt-4o", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAdminRuntimeConfigHandler_Apply(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.POST("/admin/runtime-config/apply", h.Apply)

	req := httptest.NewRequest("POST", "/admin/runtime-config/apply", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp ApplyResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

// Helper functions

func createBasicTestConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Listen: ":18000",
			Admin:  config.AdminConfig{Password: "test-password"},
		},
		Inbound: config.InboundConfig{
			Auth: config.AuthConfig{
				Keys: []config.KeyConfig{
					{Name: "test", Key: "sk-test"},
				},
			},
		},
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{
				"openai": {
					Endpoint: "https://api.openai.com",
					APIKey:   "sk-openai",
					Protocols: []string{"openai.chat"},
				},
			},
		},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Mode: "concurrent", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "gpt-4o", Mode: "concurrent", Models: config.ModelEntries{{Model: "openai/gpt-4o", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "gpt4", Target: "gpt-4"}},
	}
}

func createInMemoryManager(t *testing.T, cfg *config.Config) *runtimeconfig.Manager {
	t.Helper()

	// Create a temp config file for YAML store initialization
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Write config to file
	data, err := configToYAML(cfg)
	require.NoError(t, err)
	err = os.WriteFile(configPath, data, 0644)
	require.NoError(t, err)

	// Create rebuilder and reinit mocks
	rebuilder := &runtimeconfig.DefaultRuntimeRebuilder{
		NewResolver: func(c *config.Config) (*model.Resolver, error) {
			return &model.Resolver{}, nil
		},
		NewScheduler: func(c *config.Config, r *model.Resolver) (*scheduler.Scheduler, error) {
			return &scheduler.Scheduler{}, nil
		},
	}
	reinit := &runtimeconfig.DefaultReinitHandler{
		InitFunc: func(c *config.Config, r *model.Resolver, s *scheduler.Scheduler) {},
	}

	mgr, err := runtimeconfig.NewManager(cfg, configPath, rebuilder, reinit)
	require.NoError(t, err)
	return mgr
}

func configToYAML(cfg *config.Config) ([]byte, error) {
	// Simplified YAML output for test purposes
	type yamlServer struct {
		Listen string             `yaml:"listen"`
		Admin  config.AdminConfig `yaml:"admin"`
	}
	type yamlAuth struct {
		Keys []config.KeyConfig `yaml:"keys"`
	}
	type yamlInbound struct {
		Auth yamlAuth `yaml:"auth"`
	}
	type yamlProvider struct {
		Endpoint  string   `yaml:"endpoint"`
		APIKey    string   `yaml:"api_key"`
		Protocols []string `yaml:"protocols"`
	}
	type yamlConfig struct {
		Server      yamlServer                 `yaml:"server"`
		Inbound     yamlInbound                `yaml:"inbound"`
		Providers   map[string]yamlProvider    `yaml:"providers"`
		ModelGroups []config.ModelGroupConfig  `yaml:"model_groups"`
		Redirect    config.RedirectConfigs     `yaml:"redirect"`
	}

	yc := yamlConfig{
		Server: yamlServer{
			Listen: cfg.Server.Listen,
			Admin:  cfg.Server.Admin,
		},
		Inbound: yamlInbound{
			Auth: yamlAuth{Keys: cfg.Inbound.Auth.Keys},
		},
		Providers:   make(map[string]yamlProvider),
		ModelGroups: cfg.ModelGroups,
		Redirect:    cfg.Redirect,
	}

	for k, v := range cfg.Providers.Items {
		yc.Providers[k] = yamlProvider{
			Endpoint: v.Endpoint,
			APIKey:   v.APIKey,
			Protocols: v.Protocols,
		}
	}

	return yaml.Marshal(yc)
}

func TestAdminRuntimeConfigHandler_GetProviders(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.GET("/admin/runtime-config/draft/providers", h.GetProviders)

	req := httptest.NewRequest("GET", "/admin/runtime-config/draft/providers", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp []runtimeconfig.ProviderOutput
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Len(t, resp, 1)
	assert.Equal(t, "openai", resp[0].Name)
	assert.Len(t, resp[0].Endpoints, 1)
	assert.Equal(t, "https://api.openai.com", resp[0].Endpoints[0].URL)
	// 验证 API Key 未脱敏（完整返回）
	assert.Equal(t, "sk-openai", resp[0].APIKey)
	assert.Equal(t, []string{"openai.chat"}, resp[0].Protocols)
}

func TestAdminRuntimeConfigHandler_GetProvider(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.GET("/admin/runtime-config/draft/providers/:name", h.GetProvider)

	// Found
	req := httptest.NewRequest("GET", "/admin/runtime-config/draft/providers/openai", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp runtimeconfig.ProviderOutput
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "openai", resp.Name)
	assert.Equal(t, "sk-openai", resp.APIKey)

	// Not found
	req = httptest.NewRequest("GET", "/admin/runtime-config/draft/providers/unknown", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAdminRuntimeConfigHandler_CreateProvider(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.POST("/admin/runtime-config/draft/providers", h.CreateProvider)

	// Success
	input := runtimeconfig.ProviderInput{
		Name:     "anthropic",
		Endpoint: "https://api.anthropic.com",
		APIKey:   "sk-ant-test",
		Protocols: []string{"anthropic.messages"},
	}
	body, _ := json.Marshal(input)
	req := httptest.NewRequest("POST", "/admin/runtime-config/draft/providers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	// Conflict - already exists
	input = runtimeconfig.ProviderInput{
		Name:     "openai", // already in config
		Endpoint: "https://api.openai.com",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/providers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)

	// Bad request - empty name
	input = runtimeconfig.ProviderInput{
		Endpoint: "https://api.openai.com",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/providers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAdminRuntimeConfigHandler_UpdateProvider(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.PUT("/admin/runtime-config/draft/providers/:name", h.UpdateProvider)

	// Success - update endpoint
	input := runtimeconfig.ProviderInput{
		Name:     "openai",
		Endpoint: "https://api.openai.com/v2",
		APIKey:   "sk-openai-new",
	}
	body, _ := json.Marshal(input)
	req := httptest.NewRequest("PUT", "/admin/runtime-config/draft/providers/openai", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Rename provider
	input = runtimeconfig.ProviderInput{
		Name:     "openai-renamed",
		Endpoint: "https://api.openai.com",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("PUT", "/admin/runtime-config/draft/providers/openai", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Not found
	input = runtimeconfig.ProviderInput{
		Name:     "nonexistent",
		Endpoint: "https://api.openai.com",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("PUT", "/admin/runtime-config/draft/providers/nonexistent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAdminRuntimeConfigHandler_DeleteProvider(t *testing.T) {
	cfg := createBasicTestConfig()
	// 先删除 model_group 对 openai 的引用，避免反向引用检查失败
	cfg.ModelGroups = []config.ModelGroupConfig{}
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.DELETE("/admin/runtime-config/draft/providers/:name", h.DeleteProvider)

	// Success
	req := httptest.NewRequest("DELETE", "/admin/runtime-config/draft/providers/openai", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)

	// Not found
	req = httptest.NewRequest("DELETE", "/admin/runtime-config/draft/providers/unknown", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}