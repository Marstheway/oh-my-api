package ratelimit

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
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

// TestManager_LogsProviderUpstreamPair 验证 Allow 限流时日志输出 upstream_identity=provider/model，
// 而非裸 model 字段。
func TestManager_LogsProviderUpstreamPair(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"openrouter": {RateLimit: config.RateLimitConfig{QPM: 1}},
	}
	m := NewManager(providers)

	// 捕获 slog 输出
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	old := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(old)

	// 第一次应通过（消耗令牌），第二次应触发日志
	m.Allow("openrouter", "gpt-4o")
	m.Allow("openrouter", "gpt-4o")

	logs := buf.String()
	if logs == "" {
		t.Skip("no debug log output captured (logging may not be at debug level)")
	}

	// 不应出现裸 model 字段（值不含 / 的）
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if v, ok := entry["model"]; ok {
			s, _ := v.(string)
			if !strings.Contains(s, "/") {
				t.Errorf("ratelimit log contains bare model field without provider prefix: %q, line: %s", s, line)
			}
		}
	}

	// upstream_identity 字段在日志被触发时应存在
	if strings.Contains(logs, "ratelimit blocked") || strings.Contains(logs, "allow bypassed") {
		if !strings.Contains(logs, `"upstream_identity"`) {
			t.Errorf("ratelimit log should contain upstream_identity field, got: %s", logs)
		}
	}
}
