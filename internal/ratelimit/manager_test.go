package ratelimit

import (
	"context"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func TestNewManager(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"openai":    {RateLimit: config.RateLimitConfig{QPM: 60}},
		"anthropic": {RateLimit: config.RateLimitConfig{QPM: 0}},
	}
	m := NewManager(providers)
	if m == nil {
		t.Fatal("NewManager should not return nil")
	}
}

func TestManager_Allow(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"openai": {RateLimit: config.RateLimitConfig{QPM: 60}},
	}
	m := NewManager(providers)

	if !m.Allow("openai", "gpt-4o") {
		t.Fatal("first Allow should succeed")
	}
	if m.Allow("openai", "gpt-4o") {
		t.Fatal("second Allow should fail (tokens exhausted)")
	}
}

func TestManager_Allow_UnknownProvider(t *testing.T) {
	m := NewManager(map[string]config.ProviderConfig{})
	if !m.Allow("unknown", "gpt-4o") {
		t.Fatal("Allow for unknown provider should return true (no limit)")
	}
}

func TestManager_Allow_NoLimitProvider(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"anthropic": {RateLimit: config.RateLimitConfig{QPM: 0}},
	}
	m := NewManager(providers)
	if !m.Allow("anthropic", "claude-3-5-sonnet") {
		t.Fatal("Allow for provider with QPM=0 should return true")
	}
}

func TestManager_Wait(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"openai": {RateLimit: config.RateLimitConfig{QPM: 60}},
	}
	m := NewManager(providers)

	if err := m.Wait(context.Background(), "openai", "gpt-4o"); err != nil {
		t.Fatalf("Wait should succeed: %v", err)
	}
}

func TestManager_Wait_UnknownProvider(t *testing.T) {
	m := NewManager(map[string]config.ProviderConfig{})
	if err := m.Wait(context.Background(), "unknown", "gpt-4o"); err != nil {
		t.Fatalf("Wait for unknown provider should return nil: %v", err)
	}
}

func TestManager_Allow_ModelLimit(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"openrouter": {
			RateLimit: config.RateLimitConfig{QPM: 200},
			UpstreamModels: []config.UpstreamModelConfig{
				{Model: "openai/gpt-4o", QPM: 1},
			},
		},
	}
	m := NewManager(providers)

	// provider 级有 token，model 级 QPM=1 burst=1，首次可通过
	if !m.Allow("openrouter", "openai/gpt-4o") {
		t.Fatal("first Allow should succeed")
	}
	// model 级 token 耗尽
	if m.Allow("openrouter", "openai/gpt-4o") {
		t.Fatal("second Allow should fail (model token exhausted)")
	}
	// 其他 model 不受 gpt-4o model 限速影响
	if !m.Allow("openrouter", "other-model") {
		t.Fatal("Allow for other model should succeed")
	}
}

func TestManager_Allow_ProviderLimitBlocks(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"openrouter": {
			RateLimit: config.RateLimitConfig{QPM: 1},
			UpstreamModels: []config.UpstreamModelConfig{
				{Model: "openai/gpt-4o", QPM: 200},
			},
		},
	}
	m := NewManager(providers)

	// 消耗 provider 级 token
	m.Allow("openrouter", "openai/gpt-4o")
	// provider 级耗尽后，即使 model 级有余量也应返回 false
	if m.Allow("openrouter", "openai/gpt-4o") {
		t.Fatal("Allow should fail when provider-level token exhausted")
	}
}
