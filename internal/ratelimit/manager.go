package ratelimit

import (
	"context"
	"log/slog"
	"sync"

	"github.com/Marstheway/oh-my-api/internal/config"
)

// Manager 持有两层限流桶：
//   - provider 桶：启动 / Apply 时按 providers.<name>.rate_limit.qpm（账号 tier）建立，全家共用。
//   - model 桶：按请求携带的 rule qpm（Action.QPM 合并结果）按 identity "provider/upstreamModel" 惰性 get-or-create；
//     同一 identity 已有桶则复用。Apply 重建 Manager 后旧桶全部丢弃。
type Manager struct {
	limiters      map[string]Limiter // key: providerName
	mu            sync.Mutex
	modelLimiters map[string]Limiter // key: "providerName/upstreamModel"
}

func NewManager(providers map[string]config.ProviderConfig) *Manager {
	limiters := make(map[string]Limiter, len(providers))
	for name, cfg := range providers {
		limiters[name] = NewLimiter(cfg.RateLimit.QPM)
	}
	return &Manager{
		limiters:      limiters,
		modelLimiters: make(map[string]Limiter),
	}
}

// Allow 先扣 provider 桶，再扣 model 桶（modelQPM<=0 表示未命中 qpm rule，无 model 层）。
func (m *Manager) Allow(providerName, upstreamModel string, modelQPM int) bool {
	l, ok := m.limiters[providerName]
	if !ok {
		slog.Debug("ratelimit allow bypassed: provider limiter not found",
			"provider", providerName,
			"upstream_identity", providerName+"/"+upstreamModel,
		)
		return true
	}
	if !l.Allow() {
		slog.Debug("ratelimit blocked by provider limiter",
			"provider", providerName,
			"upstream_identity", providerName+"/"+upstreamModel,
		)
		return false
	}
	if modelQPM > 0 {
		if ml := m.modelLimiter(providerName+"/"+upstreamModel, modelQPM); !ml.Allow() {
			slog.Debug("ratelimit blocked by model limiter",
				"provider", providerName,
				"upstream_identity", providerName+"/"+upstreamModel,
			)
			return false
		}
	}
	return true
}

// Wait 等待 provider 桶，再等待 model 桶（modelQPM<=0 跳过 model 层）。
func (m *Manager) Wait(ctx context.Context, providerName, upstreamModel string, modelQPM int) error {
	l, ok := m.limiters[providerName]
	if !ok {
		return nil
	}
	if err := l.Wait(ctx); err != nil {
		return err
	}
	if modelQPM > 0 {
		return m.modelLimiter(providerName+"/"+upstreamModel, modelQPM).Wait(ctx)
	}
	return nil
}

// modelLimiter 按 identity get-or-create model 桶。qpm 为该 identity 首次请求携带的 rule qpm；
// 同一 identity 的合并 qpm 由静态 ruleset 决定，因此首个创建的速率即该 identity 的稳定速率。
func (m *Manager) modelLimiter(key string, qpm int) Limiter {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ml, ok := m.modelLimiters[key]; ok {
		return ml
	}
	ml := NewLimiter(qpm)
	m.modelLimiters[key] = ml
	return ml
}
