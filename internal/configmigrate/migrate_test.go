package configmigrate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

const testBaseConfig = `
server:
  listen: ":18000"
inbound:
  auth:
    keys:
      - name: "test"
        key: "sk-test"
providers:
  openrouter:
    endpoint: "https://openrouter.ai/api/v1"
    api_key: "sk-or-test"
    protocols: ["openai.chat", "anthropic.messages"]
model_groups:
  - name: "test"
    models:
      - "openrouter/gpt-4o"
`

func TestMigrate_NoOldFields(t *testing.T) {
	result, err := Migrate([]byte(testBaseConfig))
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if result.Changed {
		t.Fatalf("expected no changes, got changed output")
	}
	if !strings.Contains(result.Message, "no deprecated") {
		t.Fatalf("message = %q, want no deprecated fields hint", result.Message)
	}
}

func TestMigrate_NoProvidersSection(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
inbound:
  auth:
    keys:
      - name: "test"
        key: "sk-test"
model_groups:
  - name: "test"
    models:
      - "openrouter/gpt-4o"
`
	result, err := Migrate([]byte(yaml))
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if result.Changed {
		t.Fatal("expected unchanged config without providers")
	}
	if !strings.Contains(result.Message, "no providers section") {
		t.Fatalf("message = %q, want no providers section hint", result.Message)
	}
}

func TestMigrate_DefaultProtocols(t *testing.T) {
	yaml := injectProviderField(testBaseConfig, `    default_protocols: ["openai.chat"]`)

	result, err := Migrate([]byte(yaml))
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if !result.Changed {
		t.Fatal("expected changes")
	}

	out := string(result.Output)
	if strings.Contains(out, "default_protocols") {
		t.Fatalf("output still contains default_protocols:\n%s", out)
	}
	if !strings.Contains(out, "op: startWith") || !strings.Contains(out, "value: openrouter/") {
		t.Fatalf("missing startWith rule for provider default:\n%s", out)
	}
	if !strings.Contains(out, "protocol: openai.chat") {
		t.Fatalf("missing protocol action:\n%s", out)
	}

	assertOutputLoads(t, result.Output)
}

func TestMigrate_AllowedProtocolsAndEffort(t *testing.T) {
	yaml := injectProviderField(testBaseConfig, `
    default_protocols: ["openai.chat"]
    upstream_model:
      - model: "deepseek/deepseek-reasoner"
        allowed_protocols: ["anthropic.messages"]
        reasoning_effort: ["high", "max"]
      - model: "simple-model"
        reasoning_effort_mode: strip`)

	result, err := Migrate([]byte(yaml))
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	out := string(result.Output)
	for _, deprecated := range []string{"default_protocols", "allowed_protocols", "reasoning_effort", "reasoning_effort_mode"} {
		if strings.Contains(out, deprecated) {
			t.Fatalf("output still contains %s:\n%s", deprecated, out)
		}
	}
	if !strings.Contains(out, "effort_mode: strip") {
		t.Fatalf("missing effort_mode strip:\n%s", out)
	}
	if strings.Contains(out, "thinking:") {
		t.Fatalf("strip must not generate thinking:\n%s", out)
	}

	// 原子规则：同一 upstream 的 protocol + effort 拆成两条原子 rule，
	// 顺序固定为 protocol → effort；产物必须能被硬切后的 ValidateRules 接受。
	rules := loadOutputRules(t, result.Output)
	want := []config.RuleConfig{
		{
			Match:  config.RuleMatch{UpstreamModel: &config.RuleCondition{Op: "startWith", Value: "openrouter/"}},
			Action: config.RuleAction{Protocol: "openai.chat"},
		},
		{
			Match:  config.RuleMatch{UpstreamModel: &config.RuleCondition{Op: "equals", Value: "openrouter/deepseek/deepseek-reasoner"}},
			Action: config.RuleAction{Protocol: "anthropic.messages"},
		},
		{
			Match:  config.RuleMatch{UpstreamModel: &config.RuleCondition{Op: "equals", Value: "openrouter/deepseek/deepseek-reasoner"}},
			Action: config.RuleAction{Effort: []string{"high", "max"}},
		},
		{
			Match:  config.RuleMatch{UpstreamModel: &config.RuleCondition{Op: "equals", Value: "openrouter/simple-model"}},
			Action: config.RuleAction{EffortMode: "strip"},
		},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules = %+v, want %+v", rules, want)
	}

	assertOutputLoads(t, result.Output)
}

func TestMigrate_MultipleAllowedProtocolsFails(t *testing.T) {
	yaml := injectProviderField(testBaseConfig, `
    upstream_model:
      - model: "gpt-4o"
        allowed_protocols: ["openai.chat", "anthropic.messages"]`)

	_, err := Migrate([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for multi-protocol allowed_protocols")
	}
	if !strings.Contains(err.Error(), "allowed_protocols") {
		t.Fatalf("error = %q, want allowed_protocols", err.Error())
	}
	if !strings.Contains(err.Error(), "upstream_model[0]") {
		t.Fatalf("error = %q, want path upstream_model[0]", err.Error())
	}
}

func TestMigrate_MultipleDefaultProtocolsFails(t *testing.T) {
	yaml := injectProviderField(testBaseConfig, `    default_protocols: ["openai.chat", "anthropic.messages"]`)

	_, err := Migrate([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for multi-protocol default_protocols")
	}
	if !strings.Contains(err.Error(), "default_protocols") {
		t.Fatalf("error = %q, want default_protocols", err.Error())
	}
}

func TestMigrate_EmptyDeprecatedKeysStripped(t *testing.T) {
	yaml := injectProviderField(testBaseConfig, `
    default_protocols: []
    upstream_model:
      - model: "gpt-4o"
        allowed_protocols: []
        reasoning_effort: []`)

	result, err := Migrate([]byte(yaml))
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if !result.Changed {
		t.Fatal("expected changes when deprecated keys are present")
	}
	if strings.Contains(result.Message, "no deprecated") {
		t.Fatalf("must not report no-op: %q", result.Message)
	}

	out := string(result.Output)
	for _, deprecated := range []string{"default_protocols", "allowed_protocols", "reasoning_effort"} {
		if strings.Contains(out, deprecated) {
			t.Fatalf("output still contains %s:\n%s", deprecated, out)
		}
	}
	if strings.Contains(out, "rules:") {
		t.Fatalf("empty deprecated values must not create rules:\n%s", out)
	}

	assertOutputLoads(t, result.Output)
}

func TestMigrate_UpstreamModelWithoutModelStripsEmptyDeprecatedKeys(t *testing.T) {
	yaml := injectProviderField(testBaseConfig, `
    upstream_model:
      - reasoning_effort: []`)

	result, err := Migrate([]byte(yaml))
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if !result.Changed {
		t.Fatal("expected changes")
	}
	if strings.Contains(string(result.Output), "reasoning_effort") {
		t.Fatalf("reasoning_effort key must be stripped:\n%s", result.Output)
	}
	assertOutputLoads(t, result.Output)
}

func TestMigrate_PreservesExistingRules(t *testing.T) {
	yaml := strings.Replace(testBaseConfig, "model_groups:", `rules:
  - match:
      client-model: { op: equals, value: "keep-me" }
    action:
      thinking: off
model_groups:`, 1)
	yaml = injectProviderField(yaml, `    default_protocols: ["openai.chat"]`)

	result, err := Migrate([]byte(yaml))
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	out := string(result.Output)
	if !strings.Contains(out, `value: "keep-me"`) && !strings.Contains(out, "value: keep-me") {
		t.Fatalf("existing rule not preserved:\n%s", out)
	}
	if !strings.Contains(out, "value: openrouter/") {
		t.Fatalf("migrated rule missing:\n%s", out)
	}
	keepIdx := strings.Index(out, "keep-me")
	startIdx := strings.Index(out, "value: openrouter/")
	if keepIdx < 0 || startIdx < 0 || keepIdx > startIdx {
		t.Fatalf("existing rules must precede migrated rules:\n%s", out)
	}

	assertOutputLoads(t, result.Output)
}

func TestMigrateFile_Write(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := injectProviderField(testBaseConfig, `    default_protocols: ["openai.chat"]`)
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	result, err := MigrateFile(path, false)
	if err != nil {
		t.Fatalf("MigrateFile() error = %v", err)
	}
	if !result.Changed {
		t.Fatal("expected changes")
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(onDisk), "default_protocols") {
		t.Fatalf("file should be unchanged without --write:\n%s", onDisk)
	}

	result, err = MigrateFile(path, true)
	if err != nil {
		t.Fatalf("MigrateFile(write) error = %v", err)
	}
	onDisk, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(onDisk), "default_protocols") {
		t.Fatalf("file still has default_protocols after --write:\n%s", onDisk)
	}
	if !strings.Contains(string(onDisk), "rules:") {
		t.Fatalf("file missing rules after --write:\n%s", onDisk)
	}
}

// TestMigrate_DisabledTimeRangesFails 验证仅含 providers.*.disabled_time_ranges 的输入
// migrate-rules 必须失败并提示改 rules，不得报 unchanged。
func TestMigrate_DisabledTimeRangesFails(t *testing.T) {
	yaml := injectProviderField(testBaseConfig, `    disabled_time_ranges: ["09:00-12:00"]`)

	_, err := Migrate([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for disabled_time_ranges")
	}
	if !strings.Contains(err.Error(), "providers.openrouter.disabled_time_ranges") {
		t.Fatalf("error = %q, want providers.openrouter.disabled_time_ranges path", err.Error())
	}
	if strings.Contains(err.Error(), "no deprecated provider fields") {
		t.Fatalf("error = %q, must not report unchanged", err.Error())
	}
}

// TestMigrate_UpstreamModelQPMPresentFails 验证仅含 upstream_model[].qpm 的输入
// migrate-rules 必须失败（在 unchanged 短路之前拦截），不得静默丢配额。
func TestMigrate_UpstreamModelQPMPresentFails(t *testing.T) {
	yaml := injectProviderField(testBaseConfig, `
    upstream_model:
      - model: "gpt-4o"
        qpm: 10`)

	_, err := Migrate([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for upstream_model[].qpm")
	}
	if !strings.Contains(err.Error(), "upstream_model[0].qpm") {
		t.Fatalf("error = %q, want path upstream_model[0].qpm", err.Error())
	}
	if strings.Contains(err.Error(), "no deprecated provider fields") {
		t.Fatalf("error = %q, must not report unchanged", err.Error())
	}
}

// TestMigrate_UpstreamModelWithQPMAndLegacyFieldsFails 验证 upstream_model 同时含旧四字段与 qpm 时，
// 也必须在任何规则生成/写盘之前失败（不得生成规则后静默丢掉 qpm）。
func TestMigrate_UpstreamModelWithQPMAndLegacyFieldsFails(t *testing.T) {
	yaml := injectProviderField(testBaseConfig, `
    default_protocols: ["openai.chat"]
    upstream_model:
      - model: "gpt-4o"
        allowed_protocols: ["openai.chat"]
        qpm: 10`)

	_, err := Migrate([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for upstream_model[].qpm")
	}
	if !strings.Contains(err.Error(), "upstream_model[0].qpm") {
		t.Fatalf("error = %q, want path upstream_model[0].qpm", err.Error())
	}
}

// TestMigrate_ProtocolOnlyDeletesEntireUpstreamModelKey 验证仅协议旧字段迁移时
// 整个 upstream_model 键被删除（不允许残留），产物可 Load。
func TestMigrate_ProtocolOnlyDeletesEntireUpstreamModelKey(t *testing.T) {
	yaml := injectProviderField(testBaseConfig, `
    upstream_model:
      - model: "gpt-4o"
        allowed_protocols: ["openai.chat"]
      - model: "gpt-4o-mini"`)

	result, err := Migrate([]byte(yaml))
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if !result.Changed {
		t.Fatal("expected changes")
	}

	out := string(result.Output)
	if strings.Contains(out, "upstream_model") {
		t.Fatalf("output still contains upstream_model key:\n%s", out)
	}
	if !strings.Contains(out, "protocol: openai.chat") {
		t.Fatalf("missing migrated protocol rule:\n%s", out)
	}

	rules := loadOutputRules(t, result.Output)
	if len(rules) != 1 {
		t.Fatalf("rules len = %d, want 1 (model without deprecated fields must not generate a rule)", len(rules))
	}
	if rules[0].Match.UpstreamModel == nil || rules[0].Match.UpstreamModel.Value != "openrouter/gpt-4o" {
		t.Fatalf("rule match = %+v, want upstream-model equals openrouter/gpt-4o", rules[0].Match)
	}

	assertOutputLoads(t, result.Output)
}

func injectProviderField(base, inject string) string {
	return strings.Replace(base, `    protocols: ["openai.chat", "anthropic.messages"]`, "    protocols: [\"openai.chat\", \"anthropic.messages\"]\n"+inject, 1)
}

// loadOutputRules 将迁移产物写盘并 Load，返回解析后的 rules。
func loadOutputRules(t *testing.T, data []byte) []config.RuleConfig {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg.Rules
}

func assertOutputLoads(t *testing.T, data []byte) {
	t.Helper()

	if err := config.RejectDeprecatedConfigYAML(data); err != nil {
		t.Fatalf("RejectDeprecatedConfigYAML: %v", err)
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	if _, err := config.Load(path); err != nil {
		t.Fatalf("config.Load: %v", err)
	}
}
