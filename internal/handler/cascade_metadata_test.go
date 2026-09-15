package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func ctxInt(v int) *int { return &v }

// startCascadeSessionWithMetadata 启动真实 Hub、注册 spoke 连接并注入元数据快照。
// 返回 hub 与清理函数；清理时关闭连接（触发 session 元数据清除）。
func startCascadeSessionWithMetadata(t *testing.T, hub *cascade.Hub, models []cascade.MetadataModel) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/cascade", hub.ServeHTTP)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/cascade"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, cascade.AuthorizationHeaders("hub-secret"))
	if err != nil {
		t.Fatalf("dial cascade hub: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	payload, _ := cascade.EncodeFrame(cascade.Frame{Type: cascade.FrameRegister, Token: "hub-secret"})
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write register: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read register ack: %v", err)
	}

	body, _ := json.Marshal(models)
	snap, _ := cascade.EncodeFrame(cascade.Frame{Type: cascade.FrameMetadataSnapshot, ID: "snap-1", Body: body})
	if err := conn.WriteMessage(websocket.TextMessage, snap); err != nil {
		t.Fatalf("write metadata snapshot: %v", err)
	}

	// Hub 异步处理快照：等待元数据生效后再执行断言。
	if len(models) > 0 {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, ok := hub.ContextLength("corp-dev", models[0].Model); ok {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("metadata snapshot for %q not applied by hub", models[0].Model)
	}
}

// setupModelsCascadeRouter 构建含 cascade provider 的 /v1/models 测试环境，
// 保存并恢复包级 cascadeHubs 状态。
func setupModelsCascadeRouter(t *testing.T, testCfg *config.Config, hub *cascade.Hub) (*gin.Engine, func()) {
	t.Helper()
	testResolver, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	reg := cascade.NewHubRegistry()
	reg.Set(hub)
	SetCascadeHubs(reg)
	ResetCatalogSource()

	oldCfg, oldResolver := cfg, resolver
	oldHubs := cascadeHubs
	cfg = testCfg
	resolver = testResolver

	router := gin.New()
	router.GET("/v1/models", Models)

	cleanup := func() {
		cfg, resolver = oldCfg, oldResolver
		cascadeHubs = oldHubs
	}
	return router, cleanup
}

// modelsContext 请求 /v1/models 并返回模型名 → context_length 的映射。
func modelsContext(t *testing.T, router *gin.Engine) map[string]*int {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength *int   `json:"context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal models: %v", err)
	}
	out := make(map[string]*int, len(resp.Data))
	for _, m := range resp.Data {
		out[m.ID] = m.ContextLength
	}
	return out
}

func cascadeHubFixture() *cascade.Hub {
	return cascade.NewHub(cascade.HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
}

func TestModels_CascadeSessionMetadataUsed(t *testing.T) {
	hub := cascadeHubFixture()
	startCascadeSessionWithMetadata(t, hub, []cascade.MetadataModel{
		{Model: "local-gpt", ContextLength: ctxInt(128000)},
	})

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"corp-dev": {
				Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
				Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
			},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "hub-model", Models: config.ModelEntries{{Model: "corp-dev/local-gpt"}}},
		},
	}

	router, cleanup := setupModelsCascadeRouter(t, testCfg, hub)
	defer cleanup()

	got := modelsContext(t, router)
	v, ok := got["hub-model"]
	if !ok {
		t.Fatal("expected hub-model in models list")
	}
	if v == nil || *v != 128000 {
		t.Fatalf("hub-model context_length = %v, want 128000 from cascade session", v)
	}
}

func TestModels_CascadeSessionMetadataExplicitOverrideWins(t *testing.T) {
	hub := cascadeHubFixture()
	startCascadeSessionWithMetadata(t, hub, []cascade.MetadataModel{
		{Model: "local-gpt", ContextLength: ctxInt(128000)},
	})

	override := 4096
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"corp-dev": {
				Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
				Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
			},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:          "hub-model",
				Models:        config.ModelEntries{{Model: "corp-dev/local-gpt"}},
				ModelMetadata: config.ModelMetadataConfig{ContextLength: &override},
			},
		},
	}

	router, cleanup := setupModelsCascadeRouter(t, testCfg, hub)
	defer cleanup()

	got := modelsContext(t, router)
	v := got["hub-model"]
	if v == nil || *v != 4096 {
		t.Fatalf("hub-model context_length = %v, want 4096 explicit override", v)
	}
}

func TestModels_CascadeSessionMetadataMinWithCatalog(t *testing.T) {
	hub := cascadeHubFixture()
	startCascadeSessionWithMetadata(t, hub, []cascade.MetadataModel{
		{Model: "local-gpt", ContextLength: ctxInt(128000)},
	})

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"corp-dev": {
				Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
				Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
			},
			"openai": {Endpoint: "https://api.openai.com", APIKey: "k", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:   "hub-model",
				Mode:   "failover",
				Models: config.ModelEntries{{Model: "corp-dev/local-gpt"}, {Model: "openai/gpt-4"}},
			},
		},
	}

	router, cleanup := setupModelsCascadeRouter(t, testCfg, hub)
	defer cleanup()
	setTestCatalogSource(catalogContextIndex{"openai/gpt-4": 64000})

	got := modelsContext(t, router)
	v := got["hub-model"]
	if v == nil || *v != 64000 {
		t.Fatalf("hub-model context_length = %v, want min(128000, 64000)=64000", v)
	}
}

func TestModels_CascadeSessionValueBeatsCatalogForSameLeaf(t *testing.T) {
	hub := cascadeHubFixture()
	startCascadeSessionWithMetadata(t, hub, []cascade.MetadataModel{
		{Model: "local-gpt", ContextLength: ctxInt(200000)},
	})

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"corp-dev": {
				Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
				Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
			},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "hub-model", Models: config.ModelEntries{{Model: "corp-dev/local-gpt"}}},
		},
	}

	router, cleanup := setupModelsCascadeRouter(t, testCfg, hub)
	defer cleanup()
	// 普通 catalog 即使携带同名叶子的值，也应被 cascade session 精确值优先。
	setTestCatalogSource(catalogContextIndex{"corp-dev/local-gpt": 8192})

	got := modelsContext(t, router)
	v := got["hub-model"]
	if v == nil || *v != 200000 {
		t.Fatalf("hub-model context_length = %v, want 200000 from cascade session", v)
	}
}

func TestModels_CascadeSessionDisconnectFallsBackToCatalog(t *testing.T) {
	hub := cascadeHubFixture()
	mux := http.NewServeMux()
	mux.HandleFunc("/cascade", hub.ServeHTTP)
	server := httptest.NewServer(mux)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/cascade"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, cascade.AuthorizationHeaders("hub-secret"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	payload, _ := cascade.EncodeFrame(cascade.Frame{Type: cascade.FrameRegister, Token: "hub-secret"})
	_ = conn.WriteMessage(websocket.TextMessage, payload)
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read ack: %v", err)
	}
	body, _ := json.Marshal([]cascade.MetadataModel{{Model: "local-gpt", ContextLength: ctxInt(128000)}})
	snap, _ := cascade.EncodeFrame(cascade.Frame{Type: cascade.FrameMetadataSnapshot, ID: "snap-1", Body: body})
	_ = conn.WriteMessage(websocket.TextMessage, snap)

	// 等待 hub 应用快照。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := hub.ContextLength("corp-dev", "local-gpt"); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"corp-dev": {
				Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
				Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
			},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "hub-model", Models: config.ModelEntries{{Model: "corp-dev/local-gpt"}}},
		},
	}

	router, cleanup := setupModelsCascadeRouter(t, testCfg, hub)
	defer cleanup()
	setTestCatalogSource(catalogContextIndex{"corp-dev/local-gpt": 32768})

	// 会话活跃：cascade 值优先。
	if v := modelsContext(t, router)["hub-model"]; v == nil || *v != 128000 {
		t.Fatalf("connected context_length = %v, want 128000", v)
	}

	// 断开会话后：cascade 值 miss，回退普通 catalog。
	conn.Close()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := hub.Session(); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if v := modelsContext(t, router)["hub-model"]; v == nil || *v != 32768 {
		t.Fatalf("disconnected context_length = %v, want 32768 from catalog", v)
	}
}

func TestModels_CascadeSessionNoVisibleModelWithoutLeaf(t *testing.T) {
	hub := cascadeHubFixture()
	startCascadeSessionWithMetadata(t, hub, []cascade.MetadataModel{
		{Model: "local-gpt", ContextLength: ctxInt(128000)},
	})

	// Hub 配置完全没有引用 corp-dev 叶子：同步内容不得产生任何可见模型。
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com", APIKey: "k", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "plain-model", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
		},
	}

	router, cleanup := setupModelsCascadeRouter(t, testCfg, hub)
	defer cleanup()

	got := modelsContext(t, router)
	if _, ok := got["local-gpt"]; ok {
		t.Fatal("spoke snapshot must not create visible models on the hub")
	}
	if _, ok := got["corp-dev/local-gpt"]; ok {
		t.Fatal("cascade leaf must not appear without explicit hub config")
	}
}
