package ratelimit

import (
	"context"
	"log/slog"

	"github.com/Marstheway/oh-my-api/internal/config"
)

type Manager struct {
	limiters      map[string]Limiter // key: providerName
	modelLimiters map[string]Limiter // key: "providerName/upstreamModel"
}

func NewManager(providers map[string]config.ProviderConfig) *Manager {
	limiters := make(map[string]Limiter, len(providers))
	modelLimiters := make(map[string]Limiter)
	for name, cfg := range providers {
		limiters[name] = NewLimiter(cfg.RateLimit.QPM)
		for _, m := range cfg.UpstreamModels {
			if m.QPM > 0 {
				modelLimiters[name+"/"+m.Model] = NewLimiter(m.QPM)
			}
		}
	}
	return &Manager{limiters: limiters, modelLimiters: modelLimiters}
}

func (m *Manager) Allow(providerName, upstreamModel string) bool {
	l, ok := m.limiters[providerName]
	if !ok {
		slog.Debug("ratelimit allow bypassed: provider limiter not found",
			"provider", providerName,
			"model", upstreamModel,
		)
		return true
	}
	if !l.Allow() {
		slog.Debug("ratelimit blocked by provider limiter",
			"provider", providerName,
			"model", upstreamModel,
		)
		return false
	}
	if ml, ok := m.modelLimiters[providerName+"/"+upstreamModel]; ok {
		allowed := ml.Allow()
		if !allowed {
			slog.Debug("ratelimit blocked by model limiter",
				"provider", providerName,
				"model", upstreamModel,
			)
		}
		return allowed
	}


	return true
}

func (m *Manager) Wait(ctx context.Context, providerName, upstreamModel string) error {
	l, ok := m.limiters[providerName]
	if !ok {
		return nil
	}
	if err := l.Wait(ctx); err != nil {
		return err
	}
	if ml, ok := m.modelLimiters[providerName+"/"+upstreamModel]; ok {
		return ml.Wait(ctx)
	}
	return nil
}
