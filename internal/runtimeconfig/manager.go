package runtimeconfig

import (
	"fmt"
	"sync"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
)

// Manager 管理运行时配置的 draft/active 状态、CRUD 操作和 apply 流程
type Manager struct {
	mu       sync.RWMutex
	active   *config.Config
	draft    *config.Config
	rebuilder RuntimeRebuilder
	reinit   ReinitHandler
	store    *YamlStore

	// CRUD 服务
	modelGroupCRUD *ModelGroupCRUD
	redirectCRUD   *RedirectCRUD
	providerCRUD   *ProviderCRUD
	authKeyCRUD    *AuthKeyCRUD
}

// NewManager 创建配置管理器
func NewManager(initialCfg *config.Config, configPath string, rebuilder RuntimeRebuilder, reinit ReinitHandler) (*Manager, error) {
	store, err := NewYamlStore(configPath)
	if err != nil {
		return nil, fmt.Errorf("create yaml store: %w", err)
	}

	m := &Manager{
		active:   initialCfg,
		draft:    deepCopyConfig(initialCfg),
		rebuilder: rebuilder,
		reinit:    reinit,
		store:     store,
	}

	m.modelGroupCRUD = NewModelGroupCRUD(m.draft)
	m.redirectCRUD = NewRedirectCRUD(m.draft)
	m.providerCRUD = NewProviderCRUD(m.draft)
	m.authKeyCRUD = NewAuthKeyCRUD(m.draft)
	return m, nil
}

// GetDraft 获取 draft 配置（只读）
func (m *Manager) GetDraft() *config.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.draft
}

// GetActive 获取 active 配置（只读）
func (m *Manager) GetActive() *config.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active
}

// ActiveKeys 返回当前生效的 auth keys（实现 middleware.KeyProvider 接口）
// 每次 Apply 后 active 被替换为新 deepCopyConfig 结果，旧切片不会被原地修改
func (m *Manager) ActiveKeys() []config.KeyConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active.Inbound.Auth.Keys
}

// GetDraftModelGroupCRUD 获取 draft 的 model group CRUD 服务
func (m *Manager) GetDraftModelGroupCRUD() *ModelGroupCRUD {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.modelGroupCRUD
}

// ModelGroup CRUD 操作（代理到 ModelGroupCRUD）

// CreateModelGroup 创建 model group
func (m *Manager) CreateModelGroup(input *ModelGroupInput) (config.ModelGroupConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := m.modelGroupCRUD.Create(input)
	if err != nil {
		return config.ModelGroupConfig{}, err
	}

	return cfg, nil
}

// UpdateModelGroup 更新 model group
func (m *Manager) UpdateModelGroup(oldName string, input *ModelGroupInput) (config.ModelGroupConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := m.modelGroupCRUD.Update(oldName, input)
	if err != nil {
		return config.ModelGroupConfig{}, err
	}

	return cfg, nil
}

// DeleteModelGroup 删除 model group
func (m *Manager) DeleteModelGroup(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.modelGroupCRUD.Delete(name)
}

// GetModelGroup 获取单个 model group
func (m *Manager) GetModelGroup(name string) (config.ModelGroupConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.modelGroupCRUD.Get(name)
}

// ListModelGroups 获取所有 model groups
func (m *Manager) ListModelGroups() []config.ModelGroupConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.modelGroupCRUD.List()
}

// Redirect CRUD 操作（代理到 RedirectCRUD）

// CreateRedirect 创建 redirect
func (m *Manager) CreateRedirect(input *RedirectInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.redirectCRUD.Create(input)
}

// UpdateRedirect 更新 redirect
func (m *Manager) UpdateRedirect(oldAlias string, input *RedirectInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.redirectCRUD.Update(oldAlias, input)
}

// DeleteRedirect 删除 redirect
func (m *Manager) DeleteRedirect(alias string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.redirectCRUD.Delete(alias)
}

// GetRedirect 获取单个 redirect
func (m *Manager) GetRedirect(alias string) (RedirectOutput, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.redirectCRUD.Get(alias)
}

// ListRedirects 获取所有 redirects
func (m *Manager) ListRedirects() []RedirectListOutput {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.redirectCRUD.List()
}

// Provider CRUD 操作（代理到 ProviderCRUD）

// CreateProvider 创建 provider
func (m *Manager) CreateProvider(input *ProviderInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.providerCRUD.Create(input)
}

// UpdateProvider 更新 provider
func (m *Manager) UpdateProvider(oldName string, input *ProviderInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.providerCRUD.Update(oldName, input)
}

// DeleteProvider 删除 provider
func (m *Manager) DeleteProvider(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.providerCRUD.Delete(name)
}

// GetProvider 获取单个 provider
func (m *Manager) GetProvider(name string) (ProviderOutput, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.providerCRUD.Get(name)
}

// ListProviders 获取所有 providers
func (m *Manager) ListProviders() []ProviderOutput {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.providerCRUD.List()
}

// AuthKey CRUD 操作（代理到 AuthKeyCRUD）

// CreateAuthKey 创建 auth key
func (m *Manager) CreateAuthKey(input *AuthKeyInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.authKeyCRUD.Create(input)
}

// UpdateAuthKey 更新 auth key（允许改名）
func (m *Manager) UpdateAuthKey(oldName string, input *AuthKeyInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.authKeyCRUD.Update(oldName, input)
}

// DeleteAuthKey 删除 auth key
func (m *Manager) DeleteAuthKey(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.authKeyCRUD.Delete(name)
}

// GetAuthKey 获取单个 auth key（返回完整 key）
func (m *Manager) GetAuthKey(name string) (AuthKeyOutput, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.authKeyCRUD.Get(name)
}

// ListAuthKeys 获取所有 auth keys（返回完整 key）
func (m *Manager) ListAuthKeys() []AuthKeyOutput {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.authKeyCRUD.List()
}

// RebuildDraftResolver 基于 draft 配置临时构建 resolver（用于计算 context_length）
func (m *Manager) RebuildDraftResolver() (*model.Resolver, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rebuilder.RebuildResolver(m.draft)
}

// Apply 执行配置生效流程
// 流程：校验 draft → 构建新 resolver/scheduler → 临时文件写入 → 原子替换 → 更新 handler → 提交 active
func (m *Manager) Apply() ApplyResult {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. 校验 draft
	warnings, err := config.ValidateForServe(m.draft)
	if err != nil {
		return ApplyResult{
			Success: false,
			Message: fmt.Sprintf("validation failed: %v (warnings: %d)", err, len(warnings)),
		}
	}

	// 2. 构建新 resolver/scheduler
	newResolver, newScheduler, err := m.rebuilder.Rebuild(m.draft)
	if err != nil {
		return ApplyResult{
			Success: false,
			Message: fmt.Sprintf("rebuild failed: %v", err),
		}
	}

	// 3. 同步到 YAML AST 并保存
	m.store.SyncFromConfig(m.draft)
	if err := m.store.Save(); err != nil {
		// Save 失败后恢复 AST 状态，避免下一次 Apply 基于已污染的 AST 操作
		if reloadErr := m.store.Reload(); reloadErr != nil {
			// 记录重载失败，但返回原始 Save 错误
			return ApplyResult{
				Success: false,
				Message: fmt.Sprintf("save config failed: %v (reload AST also failed: %v)", err, reloadErr),
			}
		}
		return ApplyResult{
			Success: false,
			Message: fmt.Sprintf("save config failed: %v", err),
		}
	}

	// 4. 更新 handler 依赖
	m.reinit.Reinit(m.draft, newResolver, newScheduler)

	// 5. 提交 active = draft
	m.active = deepCopyConfig(m.draft)

	return ApplyResult{Success: true}
}

// DiscardDraft 放弃 draft，重置为 active
func (m *Manager) DiscardDraft() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.draft = deepCopyConfig(m.active)
	m.modelGroupCRUD = NewModelGroupCRUD(m.draft)
	m.redirectCRUD = NewRedirectCRUD(m.draft)
	m.providerCRUD = NewProviderCRUD(m.draft)
	m.authKeyCRUD = NewAuthKeyCRUD(m.draft)
}

// ResetDraftToActive 重置 draft 为当前 active（alias of DiscardDraft）
func (m *Manager) ResetDraftToActive() {
	m.DiscardDraft()
}

// deepCopyConfig 深拷贝配置
func deepCopyConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}

	result := &config.Config{
		Server:    cfg.Server,
		Inbound:   cfg.Inbound,
		Database:  cfg.Database,
		Redirect:  make(config.RedirectConfigs, 0),
	}

	// 深拷贝 Inbound.Auth.Keys（切片底层数组共享，需独立拷贝）
	if len(cfg.Inbound.Auth.Keys) > 0 {
		result.Inbound.Auth.Keys = make([]config.KeyConfig, len(cfg.Inbound.Auth.Keys))
		copy(result.Inbound.Auth.Keys, cfg.Inbound.Auth.Keys)
	}

	// 深拷贝 Providers
	result.Providers = cfg.Providers
	result.Providers.Items = make(map[string]config.ProviderConfig)
	for k, v := range cfg.Providers.Items {
		// 深拷贝每个 ProviderConfig 的切片字段
		pc := v
		pc.Endpoints = make([]config.EndpointConfig, len(v.Endpoints))
		copy(pc.Endpoints, v.Endpoints)
		pc.Protocols = make([]string, len(v.Protocols))
		copy(pc.Protocols, v.Protocols)
		pc.UpstreamModels = make([]config.UpstreamModelConfig, len(v.UpstreamModels))
		for i, um := range v.UpstreamModels {
			pc.UpstreamModels[i] = um
			pc.UpstreamModels[i].AllowedProtocols = make([]string, len(um.AllowedProtocols))
			copy(pc.UpstreamModels[i].AllowedProtocols, um.AllowedProtocols)
		}
		pc.DefaultProtocols = make([]string, len(v.DefaultProtocols))
		copy(pc.DefaultProtocols, v.DefaultProtocols)
		pc.DisabledTimeRanges = make([]string, len(v.DisabledTimeRanges))
		copy(pc.DisabledTimeRanges, v.DisabledTimeRanges)
		result.Providers.Items[k] = pc
	}

	// 深拷贝 ModelGroups
	result.ModelGroups = make([]config.ModelGroupConfig, len(cfg.ModelGroups))
	for i, g := range cfg.ModelGroups {
		result.ModelGroups[i] = g
		result.ModelGroups[i].Models = make(config.ModelEntries, len(g.Models))
		copy(result.ModelGroups[i].Models, g.Models)
	}

	// 深拷贝 Redirect
	if len(cfg.Redirect) > 0 {
		result.Redirect = make(config.RedirectConfigs, len(cfg.Redirect))
		copy(result.Redirect, cfg.Redirect)
	}

	// 深拷贝 SmartRoute
	if cfg.SmartRoute != nil {
		sr := *cfg.SmartRoute
		sr.EnabledModels = make([]string, len(cfg.SmartRoute.EnabledModels))
		copy(sr.EnabledModels, cfg.SmartRoute.EnabledModels)
		result.SmartRoute = &sr
	}

	return result
}

// DefaultRuntimeRebuilder 默认的运行时重建器
type DefaultRuntimeRebuilder struct {
	NewResolver  func(*config.Config) (*model.Resolver, error)
	NewScheduler func(*config.Config, *model.Resolver) (*scheduler.Scheduler, error)
}

func (r *DefaultRuntimeRebuilder) Rebuild(cfg *config.Config) (*model.Resolver, *scheduler.Scheduler, error) {
	resolver, err := r.NewResolver(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("create resolver: %w", err)
	}

	sched, err := r.NewScheduler(cfg, resolver)
	if err != nil {
		return nil, nil, fmt.Errorf("create scheduler: %w", err)
	}

	return resolver, sched, nil
}

func (r *DefaultRuntimeRebuilder) RebuildResolver(cfg *config.Config) (*model.Resolver, error) {
	return r.NewResolver(cfg)
}

// DefaultReinitHandler 默认的重新初始化处理器
type DefaultReinitHandler struct {
	InitFunc func(*config.Config, *model.Resolver, *scheduler.Scheduler)
}

func (h *DefaultReinitHandler) Reinit(cfg *config.Config, resolver *model.Resolver, sched *scheduler.Scheduler) {
	if h.InitFunc != nil {
		h.InitFunc(cfg, resolver, sched)
	}
}
