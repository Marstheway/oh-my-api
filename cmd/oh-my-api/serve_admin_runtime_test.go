package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/handler"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
	"github.com/Marstheway/oh-my-api/internal/router"
	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/gin-gonic/gin"
)

func TestServeAdminRuntimeConfigCRUDApplyLoop(t *testing.T) {
	// 创建临时配置文件
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// 初始配置
	initialYAML := `
server:
  listen: ":18000"
  admin:
    password: "test-password"
    session_secret: "test-secret-key-16-char"

inbound:
  auth:
    keys:
      - name: "default"
        key: "test-key"

providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "test-key"
    protocol: "openai"
  anthropic:
    endpoint: "https://api.anthropic.com"
    api_key: "test-key"
    protocol: "anthropic"

model_groups:
  - name: "gpt-4"
    models:
      - model: "openai/gpt-4"
`
	if err := os.WriteFile(configPath, []byte(initialYAML), 0644); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	// 加载配置
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// 构建初始运行时依赖
	resolver, err := model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	timeout := 120 * time.Second
	client := provider.NewClient(cfg.Providers.Items, timeout, 0, 0)
	rlManager := ratelimit.NewManager(cfg.Providers.Items)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched := scheduler.New(rlManager, client, healthChecker, timeout, 0, 0)
	handler.Init(cfg, resolver, sched)

	// 创建 runtimeconfig manager
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
	if err != nil {
		t.Fatalf("create runtimeconfig manager: %v", err)
	}

	// 设置 Gin router
	gin.SetMode(gin.TestMode)
	r := gin.New()
	router.Setup(r, runtimeManager, nil)
	router.SetupAdmin(r, cfg, runtimeManager, nil)

	// === 1. Create model group ===
	createPayload := map[string]interface{}{
		"name":   "claude-fast",
		"mode":   "loadbalance",
		"models": []map[string]interface{}{{"model": "anthropic/claude-3-5-sonnet"}},
	}
	createBody, _ := json.Marshal(createPayload)

	req := httptest.NewRequest("POST", "/admin/runtime-config/draft/model-groups", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Password", "test-password")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create model group: expected 201, got %d, body: %s", w.Code, w.Body.String())
	}

	// === 2. Apply ===
	applyBody, _ := json.Marshal(map[string]interface{}{"confirm": true})
	req = httptest.NewRequest("POST", "/admin/runtime-config/apply", bytes.NewReader(applyBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Password", "test-password")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("apply: expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var applyResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &applyResp); err != nil {
		t.Fatalf("parse apply response: %v", err)
	}
	if applyResp["success"] != true {
		t.Fatalf("apply: expected success=true, got %v", applyResp)
	}

	// === 3. Verify new model group accessible ===
	req = httptest.NewRequest("GET", "/admin/runtime-config/draft/model-groups/claude-fast", nil)
	req.Header.Set("X-Admin-Password", "test-password")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("get model group: expected 200, got %d", w.Code)
	}

	var groupResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &groupResp); err != nil {
		t.Fatalf("parse model group response: %v", err)
	}
	if groupResp["name"] != "claude-fast" {
		t.Fatalf("expected name=claude-fast, got %v", groupResp["name"])
	}

	// === 4. Update model group ===
	updatePayload := map[string]interface{}{
		"name":   "claude-fast-v2",
		"mode":   "loadbalance",
		"models": []map[string]interface{}{{"model": "anthropic/claude-3-5-sonnet-20241022"}},
	}
	updateBody, _ := json.Marshal(updatePayload)

	req = httptest.NewRequest("PUT", "/admin/runtime-config/draft/model-groups/claude-fast", bytes.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Password", "test-password")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("update model group: expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// === 5. Apply update ===
	req = httptest.NewRequest("POST", "/admin/runtime-config/apply", bytes.NewReader(applyBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Password", "test-password")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("apply update: expected 200, got %d", w.Code)
	}

	// === 6. Verify updated model group ===
	req = httptest.NewRequest("GET", "/admin/runtime-config/draft/model-groups/claude-fast-v2", nil)
	req.Header.Set("X-Admin-Password", "test-password")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("get updated model group: expected 200, got %d", w.Code)
	}

	// === 7. Delete model group ===
	req = httptest.NewRequest("DELETE", "/admin/runtime-config/draft/model-groups/claude-fast-v2", nil)
	req.Header.Set("X-Admin-Password", "test-password")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("delete model group: expected 204, got %d", w.Code)
	}

	// === 8. Apply delete ===
	req = httptest.NewRequest("POST", "/admin/runtime-config/apply", bytes.NewReader(applyBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Password", "test-password")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("apply delete: expected 200, got %d", w.Code)
	}

	// === 9. Verify deleted ===
	req = httptest.NewRequest("GET", "/admin/runtime-config/draft/model-groups/claude-fast-v2", nil)
	req.Header.Set("X-Admin-Password", "test-password")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("get deleted model group: expected 404, got %d", w.Code)
	}
}

func TestServeAdminAuthRequired(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	initialYAML := `
server:
  listen: ":18000"
  admin:
    password: "secret"
    session_secret: "test-secret-key-16-char"

inbound:
  auth:
    keys:
      - name: "default"
        key: "test-key"

providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "test-key"
    protocol: "openai"
`
	if err := os.WriteFile(configPath, []byte(initialYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	resolver, err := model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	timeout := 120 * time.Second
	client := provider.NewClient(cfg.Providers.Items, timeout, 0, 0)
	rlManager := ratelimit.NewManager(cfg.Providers.Items)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched := scheduler.New(rlManager, client, healthChecker, timeout, 0, 0)
	handler.Init(cfg, resolver, sched)

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
	if err != nil {
		t.Fatalf("create runtimeconfig manager: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	router.Setup(r, runtimeManager, nil)
	router.SetupAdmin(r, cfg, runtimeManager, nil)

	// 无密码访问
	req := httptest.NewRequest("GET", "/admin/runtime-config/draft", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without password, got %d", w.Code)
	}

	// 错误密码
	req = httptest.NewRequest("GET", "/admin/runtime-config/draft", nil)
	req.Header.Set("X-Admin-Password", "wrong-password")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong password, got %d", w.Code)
	}

	// 正确密码
	req = httptest.NewRequest("GET", "/admin/runtime-config/draft", nil)
	req.Header.Set("X-Admin-Password", "secret")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct password, got %d", w.Code)
	}
}

func TestServeAdminDisabledWhenNoPassword(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	initialYAML := `
server:
  listen: ":18000"

inbound:
  auth:
    keys:
      - name: "default"
        key: "test-key"

providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "test-key"
    protocol: "openai"
`
	if err := os.WriteFile(configPath, []byte(initialYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	resolver, err := model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	timeout := 120 * time.Second
	client := provider.NewClient(cfg.Providers.Items, timeout, 0, 0)
	rlManager := ratelimit.NewManager(cfg.Providers.Items)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched := scheduler.New(rlManager, client, healthChecker, timeout, 0, 0)
	handler.Init(cfg, resolver, sched)

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
	if err != nil {
		t.Fatalf("create runtimeconfig manager: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	router.Setup(r, runtimeManager, nil)
	router.SetupAdmin(r, cfg, runtimeManager, nil)

	// /admin 路由应该不存在
	req := httptest.NewRequest("GET", "/admin/runtime-config/draft", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when admin disabled, got %d", w.Code)
	}
}

func TestServeAdminAuthKeyCRUDAndApply(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	initialYAML := `
server:
  listen: ":18000"
  admin:
    password: "test-password"

inbound:
  auth:
    keys:
      - name: "default"
        key: "sk-old"

providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "test-key"
    protocols: ["openai.chat"]

model_groups:
  - name: "gpt-4o"
    models:
      - model: "openai/gpt-4o"
`
	if err := os.WriteFile(configPath, []byte(initialYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	resolver, err := model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	timeout := 120 * time.Second
	client := provider.NewClient(cfg.Providers.Items, timeout, 0, 0)
	rlManager := ratelimit.NewManager(cfg.Providers.Items)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched := scheduler.New(rlManager, client, healthChecker, timeout, 0, 0)
	handler.Init(cfg, resolver, sched)

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
	if err != nil {
		t.Fatalf("create runtimeconfig manager: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	router.Setup(r, runtimeManager, nil)
	router.SetupAdmin(r, cfg, runtimeManager, nil)

	adminHeader := map[string]string{"X-Admin-Password": "test-password"}

	// === 1. List 初始 auth keys（应有 1 个 default） ===
	req := httptest.NewRequest("GET", "/admin/runtime-config/draft/auth-keys", nil)
	req.Header.Set("X-Admin-Password", "test-password")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list auth keys: status = %d, want 200", w.Code)
	}
	var keys []map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &keys); err != nil {
		t.Fatalf("unmarshal keys: %v", err)
	}
	if len(keys) != 1 || keys[0]["name"] != "default" {
		t.Fatalf("initial keys = %+v, want 1 key named default", keys)
	}
	// 验证返回完整 key（不脱敏）
	if keys[0]["key"] != "sk-old" {
		t.Errorf("initial key value = %q, want sk-old (should not be masked)", keys[0]["key"])
	}

	// === 2. Create 新 auth key ===
	createBody, _ := json.Marshal(map[string]string{"name": "new", "key": "sk-new"})
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/auth-keys", bytes.NewReader(createBody))
	for k, v := range adminHeader {
		req.Header.Set(k, v)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create auth key: status = %d, want 201, body = %s", w.Code, w.Body.String())
	}

	// === 3. Get 单个 auth key（验证完整 key） ===
	req = httptest.NewRequest("GET", "/admin/runtime-config/draft/auth-keys/new", nil)
	req.Header.Set("X-Admin-Password", "test-password")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get auth key: status = %d, want 200", w.Code)
	}
	var key map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &key); err != nil {
		t.Fatalf("unmarshal key: %v", err)
	}
	if key["name"] != "new" || key["key"] != "sk-new" {
		t.Errorf("get auth key = %+v, want {new sk-new}", key)
	}

	// === 4. Update auth key（改 key 值） ===
	updateBody, _ := json.Marshal(map[string]string{"name": "new", "key": "sk-updated"})
	req = httptest.NewRequest("PUT", "/admin/runtime-config/draft/auth-keys/new", bytes.NewReader(updateBody))
	for k, v := range adminHeader {
		req.Header.Set(k, v)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update auth key: status = %d, want 200", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &key); err != nil {
		t.Fatalf("unmarshal updated key: %v", err)
	}
	if key["key"] != "sk-updated" {
		t.Errorf("updated key value = %q, want sk-updated", key["key"])
	}

	// === 5. Apply ===
	applyBody, _ := json.Marshal(map[string]bool{"confirm": true})
	req = httptest.NewRequest("POST", "/admin/runtime-config/apply", bytes.NewReader(applyBody))
	for k, v := range adminHeader {
		req.Header.Set(k, v)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("apply: status = %d, want 200, body = %s", w.Code, w.Body.String())
	}

	// === 6. 验证 Apply 后新 key 立即生效（通过 /v1 认证） ===
	// sk-updated 应能通过 /v1/models 认证
	req = httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-updated")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("new key after apply: /v1/models status = %d, want 200", w.Code)
	}

	// === 7. Delete default key + Apply ===
	req = httptest.NewRequest("DELETE", "/admin/runtime-config/draft/auth-keys/default", nil)
	req.Header.Set("X-Admin-Password", "test-password")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete auth key: status = %d, want 204", w.Code)
	}

	req = httptest.NewRequest("POST", "/admin/runtime-config/apply", bytes.NewReader(applyBody))
	for k, v := range adminHeader {
		req.Header.Set(k, v)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("apply after delete: status = %d, want 200", w.Code)
	}

	// === 8. 验证旧 key 立即失效 ===
	req = httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-old")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("deleted key after apply: /v1/models status = %d, want 401", w.Code)
	}

	// === 9. 验证配置文件已持久化 ===
	reloadCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(reloadCfg.Inbound.Auth.Keys) != 1 {
		t.Fatalf("reloaded keys len = %d, want 1", len(reloadCfg.Inbound.Auth.Keys))
	}
	if reloadCfg.Inbound.Auth.Keys[0].Name != "new" || reloadCfg.Inbound.Auth.Keys[0].Key != "sk-updated" {
		t.Errorf("reloaded key = %+v, want {new sk-updated}", reloadCfg.Inbound.Auth.Keys[0])
	}
}

// TestApplyRebuildsRateLimiter 验证 Apply 后 ratelimit.Manager 按新配置重建：
// 修改 provider rate_limit.qpm 后 Apply，重建出的 scheduler 使用新配额
// （而不是启动时闭包住的旧 rlManager）。
func TestApplyRebuildsRateLimiter(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	initialYAML := `
server:
  listen: ":18000"
inbound:
  auth:
    keys:
      - name: "default"
        key: "sk-test"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
    rate_limit:
      qpm: 1
model_groups:
  - name: "gpt-4o"
    models:
      - model: "openai/gpt-4o"
`
	if err := os.WriteFile(configPath, []byte(initialYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	timeout := 120 * time.Second
	client := provider.NewClient(cfg.Providers.Items, timeout, 0, 0)
	healthChecker := health.NewChecker(3, 30*time.Second)

	// 启动闭包：provider QPM=1 → burst=1，第二次 Allow 必须失败。
	startupSched := scheduler.New(ratelimit.NewManager(cfg.Providers.Items), client, healthChecker, timeout, 0, 0)
	if !startupSched.Allow("openai") {
		t.Fatal("startup limiter should allow first request")
	}
	if startupSched.Allow("openai") {
		t.Fatal("startup limiter QPM=1 burst must be exhausted on second Allow")
	}

	var rebuilt *scheduler.Scheduler
	rebuilder := &runtimeconfig.DefaultRuntimeRebuilder{
		NewResolver: func(c *config.Config) (*model.Resolver, error) {
			return model.NewResolver(c)
		},
		NewScheduler: func(c *config.Config, r *model.Resolver) (*scheduler.Scheduler, error) {
			return scheduler.New(ratelimit.NewManager(c.Providers.Items), client, healthChecker, timeout, 0, 0), nil
		},
	}
	reinit := &runtimeconfig.DefaultReinitHandler{
		InitFunc: func(c *config.Config, r *model.Resolver, s *scheduler.Scheduler) {
			rebuilt = s
		},
	}
	runtimeManager, err := runtimeconfig.NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create runtimeconfig manager: %v", err)
	}

	// draft 中把 provider QPM 提到 120（burst=2），Apply 后新 limiter 生效。
	err = runtimeManager.UpdateProvider("openai", &runtimeconfig.ProviderInput{
		Name:      "openai",
		Endpoint:  "https://api.openai.com/v1",
		APIKey:    "sk-xxx",
		Protocols: []string{"openai.chat"},
		RateLimit: runtimeconfig.RateLimitInput{QPM: 120},
	})
	if err != nil {
		t.Fatalf("update provider draft: %v", err)
	}

	applyResult := runtimeManager.Apply()
	if !applyResult.Success {
		t.Fatalf("apply failed: %s", applyResult.Message)
	}

	if rebuilt == nil {
		t.Fatal("reinit must be invoked with the rebuilt scheduler after Apply")
	}
	if !rebuilt.Allow("openai") {
		t.Fatal("rebuilt limiter should allow first request under new QPM=120")
	}
	if !rebuilt.Allow("openai") {
		t.Fatal("rebuilt limiter QPM=120 burst=2 should allow second Allow; if blocked, Apply did not rebuild the limiter")
	}
}
