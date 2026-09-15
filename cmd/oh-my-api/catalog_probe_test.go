package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/adaptor"
	"github.com/Marstheway/oh-my-api/internal/bridge"
	"github.com/Marstheway/oh-my-api/internal/catalog"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/modelsdev"
	"github.com/Marstheway/oh-my-api/internal/provider"
)

func TestResolveCatalogPath_UsesDatabaseDir(t *testing.T) {
	cfg := &config.Config{Database: config.DatabaseConfig{Path: "/tmp/oh-my-api/stats.db"}}
	got := resolveCatalogPath(cfg)
	want := "/tmp/oh-my-api/catalog_upstream.json"
	if got != want {
		t.Fatalf("resolveCatalogPath() = %q, want %q", got, want)
	}
}

func TestBuildCatalogProbeURL_ReusesAdaptor(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		protocol adaptor.Protocol
		want     string
	}{
		{name: "openai root", endpoint: "https://api.openai.com", protocol: adaptor.ProtocolOpenAI, want: "https://api.openai.com/v1/models"},
		{name: "anthropic root", endpoint: "https://api.anthropic.com", protocol: adaptor.ProtocolAnthropic, want: "https://api.anthropic.com/v1/models"},
		{name: "ollama root", endpoint: "http://127.0.0.1:11434", protocol: adaptor.ProtocolOllamaChat, want: "http://127.0.0.1:11434/api/tags"},
		{name: "openai with custom path", endpoint: "https://api.example.com/v3", protocol: adaptor.ProtocolOpenAI, want: "https://api.example.com/v3/models"},
		{name: "anthropic with custom path", endpoint: "https://api.lkeap.cloud.tencent.com/coding/anthropic", protocol: adaptor.ProtocolAnthropic, want: "https://api.lkeap.cloud.tencent.com/coding/anthropic/v1/models"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := adaptor.BuildCatalogURL(tt.endpoint, tt.protocol)
			if got != tt.want {
				t.Fatalf("BuildCatalogURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDoCatalogRefresh_WritesSuccessfully(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "stats.db")
	catalogPath := filepath.Join(tmp, "catalog_upstream.json")

	hits := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %s, want /v1/models", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Fatalf("missing bearer auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		Database: config.DatabaseConfig{Path: dbPath},
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: srv.URL, APIKey: "sk-test", Protocols: []string{"openai.chat"}},
		}},
	}

	client := provider.NewClient(cfg.Providers.Items, 2*time.Second, 0, 0)
	svc := catalog.NewService()

	if err := doCatalogRefresh(svc, cfg, client, 2*time.Second); err != nil {
		t.Fatalf("doCatalogRefresh error: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}

	// Verify snapshot content
	snap := svc.Snapshot()
	if len(snap.Providers) != 1 {
		t.Fatalf("providers count = %d, want 1", len(snap.Providers))
	}
	if snap.Providers[0].StatusCode != http.StatusOK {
		t.Fatalf("status_code = %d, want %d", snap.Providers[0].StatusCode, http.StatusOK)
	}
	if !strings.Contains(snap.Providers[0].Body, "gpt-4o") {
		t.Fatalf("body = %q, want contains gpt-4o", snap.Providers[0].Body)
	}
	if snap.GeneratedAt == "" {
		t.Fatal("generated_at should be set after successful refresh")
	}

	// Write to file to verify atomic write
	if err := svc.WriteToFile(catalogPath); err != nil {
		t.Fatalf("WriteToFile error: %v", err)
	}

	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatalf("read catalog file error: %v", err)
	}

	var got catalog.CatalogSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal catalog file error: %v", err)
	}
	if len(got.Providers) != 1 {
		t.Fatalf("file providers count = %d, want 1", len(got.Providers))
	}
}

// setModelsDevUnreachable 将 models.dev 拉取地址指向本地不可达端口，
// 避免 initCatalogService 的后台刷新循环真实请求外网。
func setModelsDevUnreachable(t *testing.T) {
	t.Helper()
	old := modelsDevAPIURL
	modelsDevAPIURL = "http://127.0.0.1:1"
	t.Cleanup(func() { modelsDevAPIURL = old })
}

func TestInitCatalogService_DoesNotBlockStartup(t *testing.T) {
	setModelsDevUnreachable(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "stats.db")

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		Database: config.DatabaseConfig{Path: dbPath},
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: srv.URL, APIKey: "sk-test", Protocols: []string{"openai.chat"}},
		}},
	}

	client := provider.NewClient(cfg.Providers.Items, 2*time.Second, 0, 0)
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	defer slog.SetDefault(oldLogger)

	start := time.Now()
	svc := initCatalogService(cfg, client, 2*time.Second)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		close(release)
		t.Fatalf("initCatalogService blocked for %v", elapsed)
	}

	// Verify not nil
	if svc == nil {
		t.Fatal("initCatalogService returned nil")
	}

	// Allow background probe to complete
	select {
	case <-started:
	case <-time.After(500 * time.Millisecond):
		close(release)
		t.Fatal("background probe did not start")
	}

	close(release)
	// Wait a bit for the loop to finish and write file
	deadline := time.Now().Add(3 * time.Second)
	waitOk := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(tmp, "catalog_upstream.json")); err == nil {
			waitOk = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !waitOk {
		t.Fatal("catalog file was not written by background refresh")
	}
}

func TestInitCatalogService_LoadsExistingSnapshot(t *testing.T) {
	setModelsDevUnreachable(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "stats.db")
	catalogPath := filepath.Join(tmp, "catalog_upstream.json")

	existingSnap := fmt.Sprintf(`{"generated_at":"%s","providers":[{"provider":"openai","protocol":"openai.chat","url":"https://api.openai.com/v1/models","body":"{\"data\":[{\"id\":\"gpt-4o\",\"context_length\":128000}]}","last_success_at":"%s"}]}`, time.Now().UTC().Add(-1*time.Hour).Format(time.RFC3339), time.Now().UTC().Add(-1*time.Hour).Format(time.RFC3339))
	if err := os.WriteFile(catalogPath, []byte(existingSnap), 0o644); err != nil {
		t.Fatalf("write existing catalog: %v", err)
	}

	hits := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Database: config.DatabaseConfig{Path: dbPath},
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: srv.URL, APIKey: "sk-test", Protocols: []string{"openai.chat"}},
		}},
	}

	client := provider.NewClient(cfg.Providers.Items, 2*time.Second, 0, 0)
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	defer slog.SetDefault(oldLogger)

	svc := initCatalogService(cfg, client, 2*time.Second)

	// Should have loaded the existing snapshot
	v, ok := svc.ContextLength("openai", "gpt-4o")
	if !ok || v != 128000 {
		t.Errorf("context_length = %d, ok=%v; want 128000", v, ok)
	}

	// 启动后立即触发首轮刷新（不再延迟），验证探测确实发生。
	time.Sleep(200 * time.Millisecond)

	if hits.Load() == 0 {
		t.Error("startup should trigger immediate catalog refresh, but got 0 hits")
	}
}

func TestDoCatalogRefresh_PartialFailurePreservesOldSuccess(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "stats.db")

	// openai succeeds, anthropic fails
	openaiHits := 0
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openaiHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"gpt-4o","context_length":128000}]}`))
	}))
	defer openaiSrv.Close()

	anthropicHits := 0
	anthropicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"server error"}`))
	}))
	defer anthropicSrv.Close()

	cfg := &config.Config{
		Database: config.DatabaseConfig{Path: dbPath},
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai":    {Endpoint: openaiSrv.URL, APIKey: "sk-test", Protocols: []string{"openai.chat"}},
			"anthropic": {Endpoint: anthropicSrv.URL, APIKey: "sk-test", Protocols: []string{"anthropic.messages"}},
		}},
	}

	client := provider.NewClient(cfg.Providers.Items, 2*time.Second, 0, 0)
	svc := catalog.NewService()

	// First refresh: both succeed
	firstCfg := &config.Config{
		Database: config.DatabaseConfig{Path: dbPath},
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai":    {Endpoint: openaiSrv.URL, APIKey: "sk-test", Protocols: []string{"openai.chat"}},
			"anthropic": {Endpoint: openaiSrv.URL, APIKey: "sk-test", Protocols: []string{"anthropic.messages"}},
		}},
	}
	firstClient := provider.NewClient(firstCfg.Providers.Items, 2*time.Second, 0, 0)
	if err := doCatalogRefresh(svc, firstCfg, firstClient, 2*time.Second); err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	// Verify initial context_length
	v, ok := svc.ContextLength("openai", "gpt-4o")
	if !ok || v != 128000 {
		t.Errorf("initial context_length = %d, want 128000", v)
	}

	// Second refresh: anthropic fails, openai succeeds
	if err := doCatalogRefresh(svc, cfg, client, 2*time.Second); err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	// openai should still be queryable
	v, ok = svc.ContextLength("openai", "gpt-4o")
	if !ok || v != 128000 {
		t.Errorf("context_length after partial refresh = %d, want 128000", v)
	}

	// anthropic should retain old success entry
	entries := svc.ProviderEntries("anthropic")
	if len(entries) != 1 {
		t.Fatalf("anthropic entries = %d, want 1", len(entries))
	}
	if entries[0].Error != "" {
		t.Error("anthropic should retain old success entry without error on partial failure")
	}
}

func TestDoCatalogRefresh_NeverSuccessfulProvider(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "stats.db")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"service unavailable"}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		Database: config.DatabaseConfig{Path: dbPath},
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"failing": {Endpoint: srv.URL, APIKey: "sk-test", Protocols: []string{"openai.chat"}},
		}},
	}

	client := provider.NewClient(cfg.Providers.Items, 2*time.Second, 0, 0)
	svc := catalog.NewService()

	if err := doCatalogRefresh(svc, cfg, client, 2*time.Second); err != nil {
		t.Fatalf("doCatalogRefresh: %v", err)
	}

	// Should have a failure entry, not empty
	entries := svc.ProviderEntries("failing")
	if len(entries) == 0 {
		t.Fatal("failing provider should have an entry even on failure")
	}
	if entries[0].Error == "" {
		t.Error("failing provider should have error set")
	}

	// Context length should be empty (no success)
	_, ok := svc.ContextLength("failing", "any-model")
	if ok {
		t.Error("context_length should be empty for never-successful provider")
	}

	// generated_at should be empty (no success at all)
	if svc.GeneratedAt() != "" {
		t.Errorf("generated_at should be empty for only-failures, got %q", svc.GeneratedAt())
	}

	// Next boot check: should be expired (no generated_at → expired)
	if !catalog.IsExpired(svc.GeneratedAt(), catalog.DefaultTTL, time.Now) {
		t.Error("snapshot with no generated_at should be expired")
	}
}

func TestProbeSingleProvider_UsesBuildCatalogURL(t *testing.T) {
	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
	}))
	defer srv.Close()

	client := provider.NewClient(map[string]config.ProviderConfig{
		"test": {Endpoint: srv.URL, APIKey: "key", Protocols: []string{"openai.chat"}},
	}, 2*time.Second, 0, 0)

	// Test that probeSingleProvider uses adaptor.BuildCatalogURL
	entry := probeSingleProvider(client, "test", config.ProviderConfig{
		Endpoint:  srv.URL,
		APIKey:    "key",
		Protocols: []string{"openai.chat"},
	}, 2*time.Second)

	if entry.Error != "" {
		t.Fatalf("unexpected error: %s", entry.Error)
	}
	if receivedPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", receivedPath)
	}
	if entry.StatusCode != http.StatusOK {
		t.Errorf("status_code = %d, want %d", entry.StatusCode, http.StatusOK)
	}
	if entry.LastSuccessAt == "" {
		t.Error("last_success_at should be set on success")
	}
}

func TestProbeSingleProvider_RemoteBridgeRequestsModels(t *testing.T) {
	// 设置隔离 HOME 并写入 bridge 独立 OAuth state（无 expires_in，避免触发刷新），
	// 使真实 bridge handler 可直接使用 access token 代理 xAI models，无需真实网络。
	realHome := os.Getenv("HOME")
	fakeHome := t.TempDir()
	if err := os.Setenv("HOME", fakeHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", realHome) })

	statePath, err := bridge.DefaultAuthStatePath()
	if err != nil {
		t.Fatalf("default state path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatalf("mkdir bridge state dir: %v", err)
	}
	// 不含 expires_in/last_refresh，使 needsRefresh 返回 false，直接使用 access token。
	stateJSON := `{"access_token":"test-xai-access","refresh_token":"test-xai-refresh","token_endpoint":"https://auth.x.ai/oauth2/token"}`
	if err := os.WriteFile(statePath, []byte(stateJSON), 0o600); err != nil {
		t.Fatalf("write bridge state: %v", err)
	}

	// mock xAI 模型列表上游：验证 bridge 真实 handler 会携带 OAuth token 请求并透传响应。
	var xaiHits int
	var xaiAuth, xaiAccept string
	xaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xaiHits++
		xaiAuth = r.Header.Get("Authorization")
		xaiAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"grok-3"}]}`))
	}))
	defer xaiSrv.Close()
	restoreURL := bridge.SetXaiModelsURLForTest(xaiSrv.URL + "/v1/models")
	defer restoreURL()

	// 用真实的独立 bridge handler（默认 XAIOAuthProvider + 已登录状态）作为 catalog probe 目标。
	// 真实 handler 会校验 bridge token 与 provider 头，并代理到 mock xAI。
	bridgeSrv := httptest.NewServer(bridge.NewBridgeMux(bridge.NewBridgeHandler("bridge-secret")))
	defer bridgeSrv.Close()

	client := provider.NewClient(map[string]config.ProviderConfig{
		"bridge-xai": {
			Endpoint:  bridgeSrv.URL,
			Protocols: []string{"openai.responses"},
			RemoteBridge: &config.RemoteBridgeConfig{
				Enabled:  true,
				Provider: "xai-oauth",
				Token:    "bridge-secret",
			},
		},
	}, 2*time.Second, 0, 0)

	entry := probeSingleProvider(client, "bridge-xai", config.ProviderConfig{
		Endpoint:  bridgeSrv.URL,
		Protocols: []string{"openai.responses"},
		RemoteBridge: &config.RemoteBridgeConfig{
			Enabled:  true,
			Provider: "xai-oauth",
			Token:    "bridge-secret",
		},
	}, 2*time.Second)

	if entry.Provider != "bridge-xai" {
		t.Errorf("provider = %q, want bridge-xai", entry.Provider)
	}
	// entry 成功即证明 bridge token 与 provider 头被真实 handler 接受（否则返回 401/400）。
	if entry.Error != "" {
		t.Errorf("remote bridge provider should succeed, got error %q", entry.Error)
	}
	// 真实 bridge handler 经 OAuth state 取得 access token 并代理到 mock xAI。
	if xaiHits != 1 {
		t.Fatalf("xai models upstream hits = %d, want 1", xaiHits)
	}
	if xaiAuth != "Bearer test-xai-access" {
		t.Errorf("xai upstream Authorization = %q, want Bearer test-xai-access", xaiAuth)
	}
	if xaiAccept != "application/json" {
		t.Errorf("xai upstream Accept = %q, want application/json", xaiAccept)
	}
	if entry.StatusCode != http.StatusOK {
		t.Errorf("status_code = %d, want 200", entry.StatusCode)
	}
	if !strings.Contains(entry.Body, "grok-3") {
		t.Errorf("body = %q, want contains grok-3", entry.Body)
	}
	if entry.LastSuccessAt == "" {
		t.Error("last_success_at should be set on success")
	}
}

// TestDoCatalogRefresh_NewProviderProbedWithUpdatedClient 验证修复：Apply 后更新 client，
// 下次循环时新增 provider 可以被正常探测。
func TestDoCatalogRefresh_NewProviderProbedWithUpdatedClient(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "stats.db")

	// provider A 的服务器
	providerAHits := 0
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerAHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
	}))
	defer srvA.Close()

	// provider B 的服务器
	providerBHits := 0
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerBHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"model-b"}]}`))
	}))
	defer srvB.Close()

	// 完整 config：包含 A 和 B 两个 provider（模拟 Apply 后的新 config）
	fullCfg := &config.Config{
		Database: config.DatabaseConfig{Path: dbPath},
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"provider-a": {Endpoint: srvA.URL, APIKey: "sk-a", Protocols: []string{"openai.chat"}},
			"provider-b": {Endpoint: srvB.URL, APIKey: "sk-b", Protocols: []string{"openai.chat"}},
		}},
	}

	// client 与 config 保持同步（Apply 后 updateCatalogClient 更新的结果）
	client := provider.NewClient(fullCfg.Providers.Items, 2*time.Second, 0, 0)

	svc := catalog.NewService()

	if err := doCatalogRefresh(svc, fullCfg, client, 2*time.Second); err != nil {
		t.Fatalf("doCatalogRefresh: %v", err)
	}

	// provider A 应该探测成功
	if providerAHits != 1 {
		t.Fatalf("provider A hits = %d, want 1", providerAHits)
	}
	entriesA := svc.ProviderEntries("provider-a")
	if len(entriesA) != 1 || entriesA[0].Error != "" {
		t.Fatalf("provider A should succeed, got error=%q", entriesA[0].Error)
	}

	// provider B 也应该探测成功 —— 验证 Apply 后更新 client 是有效的
	providerBEntries := svc.ProviderEntries("provider-b")
	if len(providerBEntries) != 1 {
		t.Fatalf("provider B should have 1 entry, got %d", len(providerBEntries))
	}
	if providerBEntries[0].Error != "" {
		t.Errorf("provider B should succeed, got error=%q", providerBEntries[0].Error)
	}
	if providerBHits != 1 {
		t.Errorf("provider B server hits = %d, want 1", providerBHits)
	}
}

func TestDoCatalogRefresh_ProbesRemoteBridgeProvider(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "stats.db")

	// openai 的正常服务器，会被请求
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
	}))
	defer openaiSrv.Close()

	// bridge 的服务器也会被 catalog probe 请求
	bridgeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Oh-My-API-Bridge-Provider") != "xai-oauth" {
			t.Errorf("bridge request missing provider header: %q", r.Header.Get("X-Oh-My-API-Bridge-Provider"))
		}
		if r.Header.Get("Authorization") != "Bearer bridge-secret" {
			t.Errorf("bridge request missing Authorization: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"grok-3"}]}`))
	}))
	defer bridgeSrv.Close()

	cfg := &config.Config{
		Database: config.DatabaseConfig{Path: dbPath},
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai":     {Endpoint: openaiSrv.URL, APIKey: "sk-test", Protocols: []string{"openai.chat"}},
			"bridge-xai": {Endpoint: bridgeSrv.URL, Protocols: []string{"openai.responses"}, RemoteBridge: &config.RemoteBridgeConfig{Enabled: true, Provider: "xai-oauth", Token: "bridge-secret"}},
		}},
	}

	client := provider.NewClient(cfg.Providers.Items, 2*time.Second, 0, 0)
	svc := catalog.NewService()

	if err := doCatalogRefresh(svc, cfg, client, 2*time.Second); err != nil {
		t.Fatalf("doCatalogRefresh error: %v", err)
	}

	snap := svc.Snapshot()
	if len(snap.Providers) != 2 {
		t.Fatalf("providers count = %d, want 2 (1 normal + 1 bridge)", len(snap.Providers))
	}

	for _, p := range snap.Providers {
		if p.Provider == "bridge-xai" {
			if p.Error != "" {
				t.Errorf("bridge provider should not have error, got %q", p.Error)
			}
			if p.StatusCode != http.StatusOK {
				t.Errorf("bridge provider status_code = %d, want 200", p.StatusCode)
			}
			if !strings.Contains(p.Body, "grok-3") {
				t.Errorf("bridge provider body = %q, want contains grok-3", p.Body)
			}
			if p.LastSuccessAt == "" {
				t.Errorf("bridge provider should have last_success_at, got empty")
			}
		}
	}
}

// TestRefreshModelsDev_FetchesAndWrites 验证：从 httptest server 拉取 api.json，
// 构建紧凑索引、写入 tsv、替换 Service 内存索引，且 /v1/models 查询路径可命中。
func TestRefreshModelsDev_FetchesAndWrites(t *testing.T) {
	tmp := t.TempDir()
	tsvPath := filepath.Join(tmp, "models_dev_ctx.tsv")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"openai": {"models": {"gpt-4o": {"limit": {"context": 128000}}}},
			"deepseek": {"models": {"deepseek-v4-flash": {"limit": {"context": 1000000}}}}
		}`))
	}))
	defer srv.Close()

	oldURL := modelsDevAPIURL
	modelsDevAPIURL = srv.URL
	t.Cleanup(func() { modelsDevAPIURL = oldURL })

	svc := catalog.NewService()
	refreshModelsDev(svc, tsvPath, 5*time.Second)

	// 内存索引已替换，查询路径（probe miss → models.dev fallback）可用
	v, ok := svc.ContextLength("tencent", "token-plan/deepseek-v4-flash-20160605")
	if !ok || v != 1000000 {
		t.Errorf("ContextLength fallback = %d, ok=%v; want 1000000", v, ok)
	}

	// tsv 已落盘，且可被 LoadFromFile 恢复（模拟重启）
	data, err := os.ReadFile(tsvPath)
	if err != nil {
		t.Fatalf("read tsv: %v", err)
	}
	if !strings.Contains(string(data), "deepseek-v4-flash\t1000000") {
		t.Errorf("tsv content missing entry: %q", string(data))
	}
	loaded, err := modelsdev.LoadFromFile(tsvPath)
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if v, ok := loaded.Lookup("openai/gpt-4o"); !ok || v != 128000 {
		t.Errorf("loaded gpt-4o ctx = %d, ok=%v; want 128000", v, ok)
	}
}

// TestRefreshModelsDev_FailureKeepsOldIndex 验证：拉取失败时保留上一份索引，不覆盖。
func TestRefreshModelsDev_FailureKeepsOldIndex(t *testing.T) {
	tmp := t.TempDir()
	tsvPath := filepath.Join(tmp, "models_dev_ctx.tsv")

	// 先成功写入一份索引
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"openai": {"models": {"gpt-4o": {"limit": {"context": 128000}}}}}`))
	}))
	oldURL := modelsDevAPIURL
	modelsDevAPIURL = srv.URL
	defer func() { modelsDevAPIURL = oldURL }()

	svc := catalog.NewService()
	refreshModelsDev(svc, tsvPath, 5*time.Second)
	if _, ok := svc.ContextLength("p", "gpt-4o"); !ok {
		t.Fatal("first refresh should populate index")
	}

	// 指向不可达地址后刷新失败：旧索引保留
	srv.Close()
	modelsDevAPIURL = "http://127.0.0.1:1"
	refreshModelsDev(svc, tsvPath, time.Second)

	if v, ok := svc.ContextLength("p", "gpt-4o"); !ok || v != 128000 {
		t.Errorf("old index should be kept, got %d, %v", v, ok)
	}
	if _, err := os.Stat(tsvPath); err != nil {
		t.Errorf("tsv should still exist after failed refresh: %v", err)
	}
}

func TestProbeSingleProvider_SkipsCascadeEnabled(t *testing.T) {
	client := provider.NewClient(map[string]config.ProviderConfig{}, 2*time.Second, 0, 0)
	entry := probeSingleProvider(client, "corp-dev", config.ProviderConfig{
		Cascade: &config.ProviderCascadeConfig{Enabled: true, Token: "secret"},
	}, 2*time.Second)

	if entry.Error == "" {
		t.Fatal("expected cascade provider to be skipped")
	}
	if !strings.Contains(entry.Error, "cascade") {
		t.Fatalf("error = %q, want cascade skip message", entry.Error)
	}
}

// TestRefreshModelsDev_Non2xxKeepsOldIndex 验证：上游返回非 2xx 时同样保留旧索引。
func TestRefreshModelsDev_Non2xxKeepsOldIndex(t *testing.T) {
	tmp := t.TempDir()
	tsvPath := filepath.Join(tmp, "models_dev_ctx.tsv")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	oldURL := modelsDevAPIURL
	modelsDevAPIURL = srv.URL
	t.Cleanup(func() { modelsDevAPIURL = oldURL })

	svc := catalog.NewService()
	svc.SetModelsDevIndex(modelsdev.NewIndex(map[string]int{"gpt-4o": 128000}))
	refreshModelsDev(svc, tsvPath, 5*time.Second)

	if v, ok := svc.ContextLength("p", "gpt-4o"); !ok || v != 128000 {
		t.Errorf("old index should be kept on non-2xx, got %d, %v", v, ok)
	}
}
