package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/handler"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/provider"
)

type cascadeRuntime struct {
	mu          sync.Mutex
	registry    *cascade.HubRegistry
	hub         *cascade.Hub
	spoke       *cascade.Spoke
	spokeCancel context.CancelFunc
	health      *health.Checker
	metaSrc     *spokeMetadataSource
}

func newCascadeRuntime(registry *cascade.HubRegistry, checker *health.Checker) *cascadeRuntime {
	if registry == nil {
		registry = cascade.NewHubRegistry()
	}
	return &cascadeRuntime{
		registry: registry,
		health:   checker,
	}
}

// installHub creates the initial hub on process start (no teardown).
func (cr *cascadeRuntime) installHub(cfg *config.Config) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cr.hub = cr.buildHubLocked(cfg)
	cr.registry.Set(cr.hub)
}

// swapHub closes the previous hub and installs a new one after a successful Apply.
func (cr *cascadeRuntime) swapHub(cfg *config.Config) {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	old := cr.hub
	cr.hub = cr.buildHubLocked(cfg)
	cr.registry.Set(cr.hub)
	if old != nil {
		old.Close()
	}
}

// afterApply runs hub swap and spoke restart after handler reinit on a successful Apply.
func (cr *cascadeRuntime) afterApply(cfg *config.Config) {
	cr.swapHub(cfg)
	cr.startSpoke(cfg)
}

func (cr *cascadeRuntime) buildHubLocked(cfg *config.Config) *cascade.Hub {
	if cfg == nil {
		return nil
	}
	if name, cc := findHubCascadeProvider(cfg); cc != nil {
		if hub, ok := cascade.HubFromProvider(name, cc.Token); ok {
			if cr.health != nil {
				hub.SetHealthChecker(cr.health)
			}
			slog.Info("cascade hub enabled", "provider", name)
			return hub
		}
	}
	return nil
}

func (cr *cascadeRuntime) wireClient(client *provider.Client) {
	if client == nil {
		return
	}
	client.SetCascadeHubRegistry(cr.registry)
}

func (cr *cascadeRuntime) shutdown() {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cr.stopSpokeLocked()
	if cr.hub != nil {
		cr.hub.Close()
		cr.hub = nil
	}
	cr.registry.Set(nil)
}

func (cr *cascadeRuntime) stopSpokeLocked() {
	if cr.spokeCancel != nil {
		cr.spokeCancel()
		cr.spokeCancel = nil
	}
	if cr.spoke != nil {
		_ = cr.spoke.Close()
		cr.spoke = nil
	}
}

func (cr *cascadeRuntime) startSpoke(cfg *config.Config) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cr.startSpokeLocked(cfg)
}

func (cr *cascadeRuntime) startSpokeLocked(cfg *config.Config) {
	cr.stopSpokeLocked()
	if cfg == nil || cfg.Cascade == nil {
		return
	}
	if cfg.Cascade.Hub == "" || cfg.Cascade.Token == "" {
		return
	}

	wsURL, err := cascade.HubWSURL(cfg.Cascade.Hub)
	if err != nil {
		slog.Warn("cascade spoke hub origin invalid", "error", err)
		return
	}

	spoke := cascade.NewSpoke(cascade.SpokeConfig{
		URL:              wsURL,
		Token:            cfg.Cascade.Token,
		MetadataProvider: cr.metadataSnapshotProviderLocked(),
	}, handler.ExecuteCascadeJob)
	ctx, cancel := context.WithCancel(context.Background())
	cr.spoke = spoke
	cr.spokeCancel = cancel

	slog.Info("cascade spoke connecting", "hub", cfg.Cascade.Hub, "ws", wsURL)
	go func() {
		if err := spoke.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("cascade spoke run ended", "error", err)
		}
	}()
}

func findHubCascadeProvider(cfg *config.Config) (string, *config.ProviderCascadeConfig) {
	if cfg == nil {
		return "", nil
	}
	for name, p := range cfg.Providers.Items {
		if p.Cascade != nil && p.Cascade.Enabled {
			return name, p.Cascade
		}
	}
	return "", nil
}

func newWiredProviderClient(
	cr *cascadeRuntime,
	cfg *config.Config,
	timeout, connectTimeout, responseHeaderTimeout time.Duration,
) *provider.Client {
	client := newProviderClient(cfg, timeout, connectTimeout, responseHeaderTimeout)
	if cr != nil {
		cr.wireClient(client)
	}
	return client
}

// spokeMetadataSource 原子持有 Spoke 元数据快照构造所需的不可变组合：
// 当前成功 Apply 的 resolver 与 active 配置，以及共享 catalog context lookup。
// 后台 publisher 只经此 source 读取，不触碰 handler 包级状态。
type spokeMetadataSource struct {
	mu       sync.RWMutex
	resolver *model.Resolver
	cfg      *config.Config
	lookup   func(provider, upstreamModel string) (int, bool)
}

func newSpokeMetadataSource(r *model.Resolver, cfg *config.Config, lookup func(provider, upstreamModel string) (int, bool)) *spokeMetadataSource {
	return &spokeMetadataSource{resolver: r, cfg: cfg, lookup: lookup}
}

// replace 原子替换 source 内容。resolver 必须是 Apply 实际构建并交给 handler 的实例，
// 不允许在此重新构建。
func (src *spokeMetadataSource) replace(r *model.Resolver, cfg *config.Config, lookup func(provider, upstreamModel string) (int, bool)) {
	src.mu.Lock()
	src.resolver = r
	src.cfg = cfg
	if lookup != nil {
		src.lookup = lookup
	}
	src.mu.Unlock()
}

// snapshot 基于当前 source 构建完整元数据快照（public/hidden 入口 + 可选 context_length）。
// 使用与 Spoke 自身 /v1/models 相同的 ResolveContextLength 规则，
// 使配置覆盖、嵌套 group 最小值、普通 catalog 与 models.dev 回退保持一致。
// 仅携带正数长度：YAML 覆盖为 0/负数时省略该字段，避免 Hub 全帧拒绝拖垮其余模型。
func (src *spokeMetadataSource) snapshot() []cascade.MetadataModel {
	src.mu.RLock()
	defer src.mu.RUnlock()
	if src.resolver == nil || src.cfg == nil {
		return nil
	}
	entries := src.resolver.ListCallableEntries()
	models := make([]cascade.MetadataModel, 0, len(entries))
	for _, e := range entries {
		m := cascade.MetadataModel{Model: e.Name}
		if cl := handler.ResolveContextLength(e.Name, src.resolver, src.lookup); cl != nil && *cl > 0 {
			m.ContextLength = cl
		}
		models = append(models, m)
	}
	return models
}

// setMetadataSource 初始化或替换 Spoke 元数据 source（启动时与 Apply 提交后调用）。
func (cr *cascadeRuntime) setMetadataSource(r *model.Resolver, cfg *config.Config, lookup func(provider, upstreamModel string) (int, bool)) {
	cr.mu.Lock()
	if cr.metaSrc == nil {
		cr.metaSrc = newSpokeMetadataSource(r, cfg, lookup)
	} else {
		cr.metaSrc.replace(r, cfg, lookup)
	}
	cr.mu.Unlock()
	cr.notifyMetadataChanged()
}

// metadataSnapshotProviderLocked 返回 Spoke 元数据快照提供者。
// 必须在持有 cr.mu 时调用（startSpokeLocked 内）；未初始化 source 时返回 nil。
func (cr *cascadeRuntime) metadataSnapshotProviderLocked() func() []cascade.MetadataModel {
	if cr.metaSrc == nil {
		return nil
	}
	return cr.metaSrc.snapshot
}

// notifyMetadataChanged 通知当前 Spoke publisher 重算并去重发布元数据快照。
func (cr *cascadeRuntime) notifyMetadataChanged() {
	cr.mu.Lock()
	spoke := cr.spoke
	cr.mu.Unlock()
	if spoke != nil {
		spoke.NotifyMetadataChanged()
	}
}

// cascadeMetadataNotifyFn 由 catalog / models.dev 索引成功替换后调用，
// 驱动 Spoke publisher 重算并去重发布。通过原子指针持有，避免后台刷新
// 循环与 serve 装配之间对函数变量的数据竞争。
var cascadeMetadataNotifyFn atomic.Pointer[func()]

func cascadeMetadataNotify() {
	if fn := cascadeMetadataNotifyFn.Load(); fn != nil {
		(*fn)()
	}
}

// setCascadeMetadataNotifyHook 设置 catalog/models.dev 刷新后的元数据通知钩子。
// 仅在 serve 装配阶段调用一次；测试可替换。
func setCascadeMetadataNotifyHook(fn func()) {
	cascadeMetadataNotifyFn.Store(&fn)
}
