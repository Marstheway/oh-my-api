package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/catalog"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/gorilla/websocket"
)

func metadataSourceConfig() *config.Config {
	public := config.ExposurePublic
	hidden := config.ExposureHidden
	internal := config.ExposureInternal
	return &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com", APIKey: "k", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "pub-group", Exposure: &public, Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
			{Name: "hid-group", Exposure: &hidden, Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
			{Name: "int-group", Exposure: &internal, Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
			{Name: "noctx-group", Models: config.ModelEntries{{Model: "openai/unknown-model"}}},
		},
		Redirect: config.RedirectConfigs{
			{Source: "hid-alias", Target: "hid-group", Exposure: &hidden},
			{Source: "int-alias", Target: "int-group", Exposure: &internal},
		},
	}
}

func TestSpokeMetadataSource_Snapshot(t *testing.T) {
	cfg := metadataSourceConfig()
	r, err := model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	lookup := func(provider, upstreamModel string) (int, bool) {
		if provider == "openai" && upstreamModel == "gpt-4" {
			return 8192, true
		}
		return 0, false
	}

	src := newSpokeMetadataSource(r, cfg, lookup)
	models := src.snapshot()

	got := make(map[string]int)
	for _, m := range models {
		cl := -1
		if m.ContextLength != nil {
			cl = *m.ContextLength
		}
		got[m.Model] = cl
	}

	if len(models) != 4 {
		t.Fatalf("snapshot len = %d, want 4 (public/hidden groups + hidden redirect + noctx, no internal)", len(models))
	}
	if got["pub-group"] != 8192 {
		t.Errorf("pub-group context_length = %d, want 8192 from lookup", got["pub-group"])
	}
	if got["hid-group"] != 8192 {
		t.Errorf("hid-group context_length = %d, want 8192", got["hid-group"])
	}
	if got["hid-alias"] != 8192 {
		t.Errorf("hid-alias context_length = %d, want 8192 via final group", got["hid-alias"])
	}
	// 缺失有效长度时只同步模型名。
	if got["noctx-group"] != -1 {
		t.Errorf("noctx-group context_length = %d, want omitted (miss lookup)", got["noctx-group"])
	}
	for _, name := range []string{"int-group", "int-alias"} {
		if _, ok := got[name]; ok {
			t.Errorf("internal entry %q must not be in snapshot", name)
		}
	}
}

func TestSpokeMetadataSource_SnapshotOmitsNonPositiveContextLength(t *testing.T) {
	public := config.ExposurePublic
	zero := 0
	neg := -5
	ok := 8192
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com", APIKey: "k", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name: "zero-group", Exposure: &public,
				Models:        config.ModelEntries{{Model: "openai/gpt-4"}},
				ModelMetadata: config.ModelMetadataConfig{ContextLength: &zero},
			},
			{
				Name: "neg-group", Exposure: &public,
				Models:        config.ModelEntries{{Model: "openai/gpt-4"}},
				ModelMetadata: config.ModelMetadataConfig{ContextLength: &neg},
			},
			{
				Name: "ok-group", Exposure: &public,
				Models:        config.ModelEntries{{Model: "openai/gpt-4"}},
				ModelMetadata: config.ModelMetadataConfig{ContextLength: &ok},
			},
		},
	}
	r, err := model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	src := newSpokeMetadataSource(r, cfg, func(string, string) (int, bool) {
		return 128000, true
	})
	models := src.snapshot()

	got := make(map[string]*int, len(models))
	for i := range models {
		got[models[i].Model] = models[i].ContextLength
	}
	if len(models) != 3 {
		t.Fatalf("snapshot len = %d, want 3 (all public groups still listed)", len(models))
	}
	if got["zero-group"] != nil {
		t.Errorf("zero-group context_length = %d, want omitted", *got["zero-group"])
	}
	if got["neg-group"] != nil {
		t.Errorf("neg-group context_length = %d, want omitted", *got["neg-group"])
	}
	if got["ok-group"] == nil || *got["ok-group"] != 8192 {
		t.Errorf("ok-group context_length = %v, want 8192", got["ok-group"])
	}
}

func TestSpokeMetadataSource_ReplaceIsAtomic(t *testing.T) {
	cfg := metadataSourceConfig()
	r, err := model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	src := newSpokeMetadataSource(r, cfg, func(string, string) (int, bool) { return 0, false })

	first := src.snapshot()
	if len(first) != 4 {
		t.Fatalf("initial snapshot len = %d, want 4", len(first))
	}

	// 替换 resolver/config 后，snapshot 反映新内容（原子替换，无锁竞争）。
	newCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com", APIKey: "k", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "only-group", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
		},
	}
	newR, err := model.NewResolver(newCfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	src.replace(newR, newCfg, nil)

	models := src.snapshot()
	if len(models) != 1 || models[0].Model != "only-group" {
		t.Fatalf("snapshot after replace = %+v, want [only-group]", models)
	}
}

func TestCascadeRuntime_MetadataSourceWiring(t *testing.T) {
	cr := newCascadeRuntime(nil, nil)
	cfg := metadataSourceConfig()
	r, err := model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	if cr.metaSrc != nil {
		t.Fatal("metaSrc must be nil before source init")
	}

	cr.setMetadataSource(r, cfg, func(string, string) (int, bool) { return 0, false })
	if cr.metaSrc == nil {
		t.Fatal("metaSrc must be non-nil after source init")
	}
	models := cr.metaSrc.snapshot()
	if len(models) != 4 {
		t.Fatalf("source snapshot len = %d, want 4", len(models))
	}

	// notify 在无 spoke 时安全。
	cr.notifyMetadataChanged()
}

// runtimeHubStub 是 runtime wiring 测试的最小 hub 桩：接受注册并收集 spoke 发来的
// metadata snapshot 帧。
type runtimeHubStub struct {
	server *httptest.Server
	frames chan cascade.Frame
}

func newRuntimeHubStub(t *testing.T) *runtimeHubStub {
	t.Helper()
	stub := &runtimeHubStub{frames: make(chan cascade.Frame, 64)}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		frame, err := cascade.DecodeFrame(data)
		if err != nil || frame.Type != cascade.FrameRegister {
			return
		}
		ack, _ := cascade.EncodeFrame(cascade.Frame{Type: cascade.FrameRegisterAck, Provider: "corp-dev"})
		_ = conn.WriteMessage(websocket.TextMessage, ack)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			frame, err := cascade.DecodeFrame(data)
			if err != nil {
				continue
			}
			switch frame.Type {
			case cascade.FrameMetadataSnapshot:
				stub.frames <- frame
			case cascade.FramePing:
				payload, _ := cascade.EncodeFrame(cascade.Frame{Type: cascade.FramePong})
				_ = conn.WriteMessage(websocket.TextMessage, payload)
			}
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *runtimeHubStub) nextSnapshot(t *testing.T, timeout time.Duration) []cascade.MetadataModel {
	t.Helper()
	select {
	case frame := <-s.frames:
		if frame.Type != cascade.FrameMetadataSnapshot {
			t.Fatalf("frame type = %q, want metadata_snapshot", frame.Type)
		}
		var models []cascade.MetadataModel
		if err := json.Unmarshal(frame.Body, &models); err != nil {
			t.Fatalf("unmarshal snapshot: %v", err)
		}
		return models
	case <-time.After(timeout):
		t.Fatal("timeout waiting for metadata snapshot")
		return nil
	}
}

func (s *runtimeHubStub) assertNoSnapshot(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case frame := <-s.frames:
		t.Fatalf("unexpected frame %q (snapshot should be deduplicated)", frame.Type)
	case <-time.After(timeout):
	}
}

// wiringConfig 构建启用顶层 Spoke Cascade 的配置；provider 指向 providerEndpoint，
// 供 catalog 探测复用。
func wiringConfig(hubURL, providerEndpoint string, groupNames ...string) *config.Config {
	groups := make([]config.ModelGroupConfig, len(groupNames))
	for i, name := range groupNames {
		groups[i] = config.ModelGroupConfig{
			Name:   name,
			Models: config.ModelEntries{{Model: "openai/" + name + "-model"}},
		}
	}
	return &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: providerEndpoint, APIKey: "k", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: groups,
		Cascade: &config.SpokeCascadeConfig{
			Hub:   hubURL,
			Token: "hub-secret",
			Peer:  "openai",
		},
	}
}

// TestCascadeRuntime_Wiring_ConnectedSpoke 端到端验证已连接 Spoke 下的发布触发面：
// 注册、Apply source 替换、provider catalog 更新、models.dev 更新与无变化通知；
// 仅当规范化快照内容实际变化时才发送新 frame。
func TestCascadeRuntime_Wiring_ConnectedSpoke(t *testing.T) {
	stub := newRuntimeHubStub(t)
	svc := catalog.NewService()

	// 保存并恢复 catalog/models.dev 通知钩子，避免跨测试干扰。
	oldFn := cascadeMetadataNotifyFn.Load()
	t.Cleanup(func() {
		if oldFn != nil {
			cascadeMetadataNotifyFn.Store(oldFn)
		} else {
			cascadeMetadataNotifyFn.Store(nil)
		}
	})

	cfg1 := wiringConfig(stub.server.URL, "http://unused", "alpha")
	r1, err := model.NewResolver(cfg1)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	cr := newCascadeRuntime(nil, nil)
	setCascadeMetadataNotifyHook(func() { cr.notifyMetadataChanged() })
	cr.setMetadataSource(r1, cfg1, svc.ContextLength)
	cr.startSpoke(cfg1)
	defer cr.shutdown()

	// 1. 注册后发布初始快照（仅 alpha，无 context_length）。
	models := stub.nextSnapshot(t, 3*time.Second)
	if len(models) != 1 || models[0].Model != "alpha" {
		t.Fatalf("initial snapshot = %+v, want [alpha]", models)
	}

	// 2. Apply source 替换：新增 beta 入口 → 内容变化 → 新 frame。
	cfg2 := wiringConfig(stub.server.URL, "http://unused", "alpha", "beta")
	r2, err := model.NewResolver(cfg2)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	cr.setMetadataSource(r2, cfg2, svc.ContextLength)
	models = stub.nextSnapshot(t, 3*time.Second)
	if len(models) != 2 || models[0].Model != "alpha" || models[1].Model != "beta" {
		t.Fatalf("snapshot after apply = %+v, want [alpha beta]", models)
	}

	// 3. 无变化通知：不发送新 frame。
	cr.notifyMetadataChanged()
	stub.assertNoSnapshot(t, 150*time.Millisecond)

	// 4. provider catalog 更新：alpha-model 获得 context_length → 内容变化 → 新 frame。
	//    复刻 runCatalogRefreshLoop 的序列：doCatalogRefresh + cascadeMetadataNotify。
	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"alpha-model","context_length":128000}]}`))
	}))
	defer catalogSrv.Close()

	probeCfg := &config.Config{Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
		"openai": {Endpoint: catalogSrv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
	}}}
	client := provider.NewClient(probeCfg.Providers.Items, 2*time.Second, 0, 0)
	if err := doCatalogRefresh(svc, probeCfg, client, 2*time.Second); err != nil {
		t.Fatalf("doCatalogRefresh: %v", err)
	}
	cascadeMetadataNotify()
	models = stub.nextSnapshot(t, 3*time.Second)
	if len(models) != 2 || models[0].Model != "alpha" || models[0].ContextLength == nil || *models[0].ContextLength != 128000 {
		t.Fatalf("snapshot after catalog update = %+v, want alpha with context_length 128000", models)
	}

	// 5. models.dev 索引更新：beta-model 获得 context_length → 内容变化 → 新 frame。
	mdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"openai":{"models":{"beta-model":{"limit":{"context":200000}}}}}`))
	}))
	defer mdSrv.Close()

	oldModelsDevURL := modelsDevAPIURL
	modelsDevAPIURL = mdSrv.URL
	t.Cleanup(func() { modelsDevAPIURL = oldModelsDevURL })

	tsvPath := filepath.Join(t.TempDir(), "models_dev_ctx.tsv")
	refreshModelsDev(svc, tsvPath, 5*time.Second)
	models = stub.nextSnapshot(t, 3*time.Second)
	if len(models) != 2 || models[1].Model != "beta" || models[1].ContextLength == nil || *models[1].ContextLength != 200000 {
		t.Fatalf("snapshot after models.dev update = %+v, want beta with context_length 200000", models)
	}

	// 6. 再触发一次无变化通知：不发送新 frame。
	cr.notifyMetadataChanged()
	stub.assertNoSnapshot(t, 150*time.Millisecond)
}

// TestCascadeRuntime_Wiring_UncommittedDraftIsolation 是真实生命周期回归测试：
// 成功 Apply 后编辑未 Apply 的 model group 与 redirect，触发 catalog/models.dev
// 元数据通知，断言同步的 snapshot 仍严格等于已提交集合，绝不混入 draft 的未提交
// 入口。同时并发修改 draft + 通知，供 race 检测该路径。
//
// 同步范围遵循 README 语义：public 与 hidden 的 group/redirect 都会进入 snapshot
// （internal 除外），因此 hidden redirect（gamma-alias）也会被同步。
func TestCascadeRuntime_Wiring_UncommittedDraftIsolation(t *testing.T) {
	stub := newRuntimeHubStub(t)
	svc := catalog.NewService()

	oldFn := cascadeMetadataNotifyFn.Load()
	t.Cleanup(func() {
		if oldFn != nil {
			cascadeMetadataNotifyFn.Store(oldFn)
		} else {
			cascadeMetadataNotifyFn.Store(nil)
		}
	})

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	initial := wiringConfig(stub.server.URL, "http://unused", "alpha", "beta")
	if err := runtimeconfig.SaveConfig(configPath, initial); err != nil {
		t.Fatalf("write initial config: %v", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	cr := newCascadeRuntime(nil, nil)
	setCascadeMetadataNotifyHook(func() { cr.notifyMetadataChanged() })

	rebuilder := &runtimeconfig.DefaultRuntimeRebuilder{
		NewResolver: func(c *config.Config) (*model.Resolver, error) {
			return model.NewResolver(c)
		},
		NewScheduler: func(c *config.Config, r *model.Resolver) (*scheduler.Scheduler, error) {
			return scheduler.New(
				ratelimit.NewManager(c.Providers.Items),
				provider.NewClient(c.Providers.Items, 2*time.Second, 0, 0),
				health.NewChecker(3, 30*time.Second),
				500*time.Millisecond, 0, 0,
			), nil
		},
	}
	reinit := &runtimeconfig.DefaultReinitHandler{
		InitFunc: func(*config.Config, *model.Resolver, *scheduler.Scheduler) {},
	}
	mgr, err := runtimeconfig.NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	// 与 serve.go 一致：Apply 提交后把实际构建的 resolver 与 active 配置交给 runtime source。
	mgr.OnCommitted = func(newCfg *config.Config, newResolver *model.Resolver) {
		cr.setMetadataSource(newResolver, newCfg, svc.ContextLength)
	}

	// 首次 Apply：提交 {alpha, beta}，并启动 spoke。
	res := mgr.Apply()
	if !res.Success {
		t.Fatalf("initial apply failed: %s", res.Message)
	}
	cr.startSpoke(mgr.GetActive())
	defer cr.shutdown()

	models := stub.nextSnapshot(t, 3*time.Second)
	if len(models) != 2 || models[0].Model != "alpha" || models[1].Model != "beta" {
		t.Fatalf("initial committed snapshot = %+v, want [alpha beta]", models)
	}

	// 编辑 draft（未 Apply）：新增 public group gamma 与 hidden redirect gamma-alias。
	if _, err := mgr.CreateModelGroup(&runtimeconfig.ModelGroupInput{
		Name:  "gamma",
		Model: "openai/gamma-model",
	}); err != nil {
		t.Fatalf("create draft group: %v", err)
	}
	hidden := "hidden"
	if err := mgr.CreateRedirect(&runtimeconfig.RedirectInput{
		Source:   "gamma-alias",
		Target:   "gamma",
		Exposure: &hidden,
	}); err != nil {
		t.Fatalf("create draft redirect: %v", err)
	}

	// 触发 catalog/models.dev 元数据通知：snapshot 必须仍严格等于已提交集合，不发新 frame。
	cascadeMetadataNotify()
	stub.assertNoSnapshot(t, 300*time.Millisecond)

	// 并发修改 draft + 通知：publisher 读取的必须是 resolver 自有不可变数据，无竞态、无 frame。
	burstDone := make(chan struct{})
	go func() {
		defer close(burstDone)
		for i := 0; i < 200; i++ {
			name := fmt.Sprintf("scratch-%d", i)
			if _, err := mgr.CreateModelGroup(&runtimeconfig.ModelGroupInput{
				Name:  name,
				Model: "openai/gamma-model",
			}); err != nil {
				return
			}
			if err := mgr.DeleteModelGroup(name); err != nil {
				return
			}
		}
	}()
	for i := 0; i < 20; i++ {
		cascadeMetadataNotify()
		time.Sleep(10 * time.Millisecond)
	}
	<-burstDone
	stub.assertNoSnapshot(t, 300*time.Millisecond)

	// 真实 Apply 提交 gamma 与 gamma-alias 后，publisher 才发送包含它们的新快照。
	// 同步范围含 public group（gamma）与 hidden redirect（gamma-alias），二者按
	// ListCallableEntries 的字典序排列；draft 未提交的 scratch-* 入口绝不出现。
	res = mgr.Apply()
	if !res.Success {
		t.Fatalf("second apply failed: %s", res.Message)
	}
	models = stub.nextSnapshot(t, 3*time.Second)
	want := []string{"alpha", "beta", "gamma", "gamma-alias"}
	if len(models) != len(want) {
		t.Fatalf("snapshot after committed apply = %+v, want %v", models, want)
	}
	for i, name := range want {
		if models[i].Model != name {
			t.Fatalf("snapshot after committed apply = %+v, want %v", models, want)
		}
	}
}
