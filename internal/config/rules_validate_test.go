package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRules_RejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		rules   []RuleConfig
		wantErr string
	}{
		{
			name: "unknown op",
			rules: []RuleConfig{{
				Match: RuleMatch{Key: &RuleCondition{Op: "regex", Value: "x"}},
			}},
			wantErr: "invalid op",
		},
		{
			name: "invalid thinking",
			rules: []RuleConfig{{
				Action: RuleAction{Thinking: "maybe"},
			}},
			wantErr: "invalid thinking",
		},
		{
			name: "effort and effort_mode in same action",
			rules: []RuleConfig{{
				Action: RuleAction{
					Effort:     []string{"high"},
					EffortMode: "strip",
				},
			}},
			wantErr: "effort and effort_mode cannot be set on the same rule",
		},
		{
			name: "invalid effort value",
			rules: []RuleConfig{{
				Action: RuleAction{Effort: []string{"ultra"}},
			}},
			wantErr: "invalid effort value",
		},
		{
			name: "none mixed with other effort",
			rules: []RuleConfig{{
				Action: RuleAction{Effort: []string{"none", "high"}},
			}},
			wantErr: "must be exclusive",
		},
		{
			name: "invalid effort_mode",
			rules: []RuleConfig{{
				Action: RuleAction{EffortMode: "clamp"},
			}},
			wantErr: "invalid effort_mode",
		},
		{
			name: "invalid temperature_mode",
			rules: []RuleConfig{{
				Action: RuleAction{TemperatureMode: "clamp"},
			}},
			wantErr: "invalid temperature_mode",
		},
		{
			name: "invalid protocol",
			rules: []RuleConfig{{
				Action: RuleAction{Protocol: "anthropic.message"},
			}},
			wantErr: "invalid protocol",
		},
		{
			name: "empty action",
			rules: []RuleConfig{{
				Match: RuleMatch{Key: &RuleCondition{Op: "equals", Value: "x"}},
			}},
			wantErr: "max_tokens",
		},
		{
			name: "two match fields",
			rules: []RuleConfig{{
				Match: RuleMatch{
					ClientModel: &RuleCondition{Op: "equals", Value: "claude-fast"},
					Key:         &RuleCondition{Op: "equals", Value: "cc-"},
				},
				Action: RuleAction{Thinking: "off"},
			}},
			wantErr: "at most one condition field",
		},
		{
			name: "three match fields",
			rules: []RuleConfig{{
				Match: RuleMatch{
					ClientModel:   &RuleCondition{Op: "equals", Value: "claude-fast"},
					Key:           &RuleCondition{Op: "equals", Value: "cc-"},
					UpstreamModel: &RuleCondition{Op: "equals", Value: "p/m"},
				},
				Action: RuleAction{Thinking: "off"},
			}},
			wantErr: "at most one condition field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRules(tt.rules)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestValidateRules_RejectsCompoundPaths 验证双 match / 空 action / effort+effort_mode 的错误路径定位到 rules[i]。
func TestValidateRules_RejectsCompoundPaths(t *testing.T) {
	tests := []struct {
		name     string
		rules    []RuleConfig
		wantPath string
	}{
		{
			name: "double match path",
			rules: []RuleConfig{{
				Match: RuleMatch{
					ClientModel: &RuleCondition{Op: "equals", Value: "a"},
					Key:         &RuleCondition{Op: "equals", Value: "b"},
				},
				Action: RuleAction{Thinking: "off"},
			}},
			wantPath: "rules[0].match",
		},
		{
			name: "effort and effort_mode path",
			rules: []RuleConfig{{
				Action: RuleAction{Effort: []string{"high"}, EffortMode: "strip"},
			}},
			wantPath: "rules[0].action",
		},
		{
			name: "empty action path",
			rules: []RuleConfig{{
				Match: RuleMatch{Key: &RuleCondition{Op: "equals", Value: "x"}},
			}},
			wantPath: "rules[0].action",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRules(tt.rules)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			verr, ok := err.(ValidationError)
			if !ok {
				t.Fatalf("expected ValidationError, got %T", err)
			}
			if len(verr.Issues) == 0 {
				t.Fatal("expected at least one issue")
			}
			// match 基数/action 基数 issue 必须落在期望路径（在 op 等其它 issue 之前）。
			if verr.Issues[0].Path != tt.wantPath {
				t.Fatalf("first issue path = %q, want %q (issues=%+v)", verr.Issues[0].Path, tt.wantPath, verr.Issues)
			}
		})
	}
}

func TestValidateRules_AcceptsValidConfig(t *testing.T) {
	// 每条至多一个 match 字段；action 可单类或多类；空 match = GLOBAL。
	rules := []RuleConfig{
		{
			Match: RuleMatch{
				ClientModel: &RuleCondition{Op: "equals", Value: "claude-fast"},
			},
			Action: RuleAction{Protocol: "anthropic"},
		},
		{
			Match: RuleMatch{
				Key: &RuleCondition{Op: "startWith", Value: "cc-"},
			},
			Action: RuleAction{Thinking: "off"},
		},
		{
			Match: RuleMatch{
				UpstreamModel: &RuleCondition{Op: "include", Value: "deepseek/"},
			},
			Action: RuleAction{Effort: []string{"high", "max"}},
		},
		{
			Match: RuleMatch{
				UpstreamModel: &RuleCondition{Op: "equals", Value: "openai/simple-model"},
			},
			Action: RuleAction{EffortMode: "strip"},
		},
		{
			Match: RuleMatch{
				UpstreamModel: &RuleCondition{Op: "equals", Value: "tcodex-local/gpt-5.6-terra"},
			},
			Action: RuleAction{TemperatureMode: "strip"},
		},
		{
			Match:  RuleMatch{},
			Action: RuleAction{Effort: []string{"none"}},
		},
		{
			Match: RuleMatch{
				UpstreamModel: &RuleCondition{Op: "equals", Value: "openai/gpt-4o"},
			},
			Action: RuleAction{
				Protocol:         "openai.chat",
				Thinking:         "off",
				QPM:              iptr(120),
				EnableTimeRange:  []string{"09:00-18:00"},
				DisableTimeRange: []string{"23:00-02:00"},
				Retries:          iptr(1),
			},
		},
		{
			Match:  RuleMatch{},
			Action: RuleAction{Retries: iptr(0)},
		},
		{
			Match: RuleMatch{
				UpstreamModel: &RuleCondition{Op: "startWith", Value: "token-hub/deepseek/"},
			},
			Action: RuleAction{MaxTokens: iptr(2000)},
		},
	}

	if err := ValidateRules(rules); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestValidateRules_SchedulingActions 验证三类调度 action 的合法性：
// qpm 只允许 upstream-model match（GLOBAL/client-model/key 均拒绝），显式 0/负数拒绝并落在
// rules[i].action.qpm；时段空表拒绝（类计数为 0 落到 rules[i].action）；非法 HH:MM-HH:MM 落到
// rules[i].action.enable_time_range[j] / disable_time_range[j]；09:00-09:00 非法。
func TestValidateRules_SchedulingActions(t *testing.T) {
	tests := []struct {
		name     string
		rules    []RuleConfig
		wantPath string
		wantMsg  string
	}{
		{
			name: "qpm with global match rejected",
			rules: []RuleConfig{{
				Action: RuleAction{QPM: iptr(100)},
			}},
			wantPath: "rules[0].action.qpm",
			wantMsg:  "only allowed with upstream-model match",
		},
		{
			name: "qpm with client-model match rejected",
			rules: []RuleConfig{{
				Match:  RuleMatch{ClientModel: &RuleCondition{Op: "equals", Value: "gpt-4"}},
				Action: RuleAction{QPM: iptr(100)},
			}},
			wantPath: "rules[0].action.qpm",
			wantMsg:  "only allowed with upstream-model match",
		},
		{
			name: "qpm with key match rejected",
			rules: []RuleConfig{{
				Match:  RuleMatch{Key: &RuleCondition{Op: "equals", Value: "sk-1"}},
				Action: RuleAction{QPM: iptr(100)},
			}},
			wantPath: "rules[0].action.qpm",
			wantMsg:  "only allowed with upstream-model match",
		},
		{
			name: "explicit qpm zero rejected",
			rules: []RuleConfig{{
				Match:  RuleMatch{UpstreamModel: &RuleCondition{Op: "equals", Value: "openai/gpt-4o"}},
				Action: RuleAction{QPM: iptr(0)},
			}},
			wantPath: "rules[0].action.qpm",
			wantMsg:  "must be a positive integer",
		},
		{
			name: "negative qpm rejected",
			rules: []RuleConfig{{
				Match:  RuleMatch{UpstreamModel: &RuleCondition{Op: "equals", Value: "openai/gpt-4o"}},
				Action: RuleAction{QPM: iptr(-5)},
			}},
			wantPath: "rules[0].action.qpm",
			wantMsg:  "must be a positive integer",
		},
		{
			name: "empty enable_time_range is empty action",
			rules: []RuleConfig{{
				Match:  RuleMatch{UpstreamModel: &RuleCondition{Op: "equals", Value: "openai/gpt-4o"}},
				Action: RuleAction{EnableTimeRange: []string{}},
			}},
			wantPath: "rules[0].action",
			wantMsg:  "at least one action class",
		},
		{
			name: "invalid enable_time_range entry",
			rules: []RuleConfig{{
				Match:  RuleMatch{UpstreamModel: &RuleCondition{Op: "equals", Value: "openai/gpt-4o"}},
				Action: RuleAction{EnableTimeRange: []string{"9am-5pm"}},
			}},
			wantPath: "rules[0].action.enable_time_range[0]",
			wantMsg:  "invalid",
		},
		{
			name: "invalid disable_time_range entry",
			rules: []RuleConfig{{
				Match:  RuleMatch{UpstreamModel: &RuleCondition{Op: "equals", Value: "openai/gpt-4o"}},
				Action: RuleAction{DisableTimeRange: []string{"25:00-26:00"}},
			}},
			wantPath: "rules[0].action.disable_time_range[0]",
			wantMsg:  "invalid",
		},
		{
			name: "zero length time range rejected",
			rules: []RuleConfig{{
				Match:  RuleMatch{UpstreamModel: &RuleCondition{Op: "equals", Value: "openai/gpt-4o"}},
				Action: RuleAction{EnableTimeRange: []string{"09:00-09:00"}},
			}},
			wantPath: "rules[0].action.enable_time_range[0]",
			wantMsg:  "must not be equal",
		},
		{
			name: "negative retries rejected",
			rules: []RuleConfig{{
				Action: RuleAction{Retries: iptr(-1)},
			}},
			wantPath: "rules[0].action.retries",
			wantMsg:  "must be an integer in [0, 2]",
		},
		{
			name: "retries above max rejected",
			rules: []RuleConfig{{
				Match:  RuleMatch{UpstreamModel: &RuleCondition{Op: "equals", Value: "openai/gpt-4o"}},
				Action: RuleAction{Retries: iptr(3)},
			}},
			wantPath: "rules[0].action.retries",
			wantMsg:  "must be an integer in [0, 2]",
		},
		{
			name: "explicit max_tokens zero rejected",
			rules: []RuleConfig{{
				Action: RuleAction{MaxTokens: iptr(0)},
			}},
			wantPath: "rules[0].action.max_tokens",
			wantMsg:  "must be a positive integer",
		},
		{
			name: "negative max_tokens rejected",
			rules: []RuleConfig{{
				Match:  RuleMatch{UpstreamModel: &RuleCondition{Op: "equals", Value: "openai/gpt-4o"}},
				Action: RuleAction{MaxTokens: iptr(-1)},
			}},
			wantPath: "rules[0].action.max_tokens",
			wantMsg:  "must be a positive integer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRules(tt.rules)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantPath) {
				t.Fatalf("error = %q, want path %q", err.Error(), tt.wantPath)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error = %q, want message substring %q", err.Error(), tt.wantMsg)
			}
		})
	}
}

// TestValidateRules_AcceptsSchedulingActions 验证合法调度 action 通过校验：
// qpm>0 + upstream-model（equals / startWith）、跨午夜 enable_time_range、disable_time_range。
func TestValidateRules_AcceptsSchedulingActions(t *testing.T) {
	rules := []RuleConfig{
		{
			Match:  RuleMatch{UpstreamModel: &RuleCondition{Op: "equals", Value: "openai/gpt-4o"}},
			Action: RuleAction{QPM: iptr(120)},
		},
		{
			Match:  RuleMatch{UpstreamModel: &RuleCondition{Op: "startWith", Value: "openrouter/"}},
			Action: RuleAction{QPM: iptr(60)},
		},
		{
			Match:  RuleMatch{UpstreamModel: &RuleCondition{Op: "equals", Value: "anthropic/claude"}},
			Action: RuleAction{EnableTimeRange: []string{"09:00-18:00"}},
		},
		{
			Match:  RuleMatch{UpstreamModel: &RuleCondition{Op: "equals", Value: "anthropic/claude"}},
			Action: RuleAction{DisableTimeRange: []string{"23:00-02:00"}},
		},
		{
			Match:  RuleMatch{UpstreamModel: &RuleCondition{Op: "equals", Value: "zhipu/glm"}},
			Action: RuleAction{EnableTimeRange: []string{"09:00-12:00", "13:00-14:00"}},
		},
	}

	if err := ValidateRules(rules); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func iptr(v int) *int { return &v }

func TestNormalizeRulesInConfig_ResolvesProtocolAlias(t *testing.T) {
	cfg := &Config{
		Rules: []RuleConfig{
			{
				Action: RuleAction{Protocol: "anthropic"},
			},
			{
				Action: RuleAction{Effort: []string{" HIGH ", "Max"}},
			},
			{
				Action: RuleAction{Thinking: " OFF "},
			},
			{
				Action: RuleAction{TemperatureMode: " STRIP "},
			},
		},
	}

	if !NormalizeRulesInConfig(cfg) {
		t.Fatal("expected normalization changes")
	}

	if got := cfg.Rules[0].Action.Protocol; got != "anthropic.messages" {
		t.Errorf("rule[0].protocol = %q, want anthropic.messages", got)
	}
	if len(cfg.Rules[1].Action.Effort) != 2 || cfg.Rules[1].Action.Effort[0] != "high" || cfg.Rules[1].Action.Effort[1] != "max" {
		t.Errorf("rule[1].effort = %v, want [high max]", cfg.Rules[1].Action.Effort)
	}
	if got := cfg.Rules[2].Action.Thinking; got != "off" {
		t.Errorf("rule[2].thinking = %q, want off", got)
	}
	if got := cfg.Rules[3].Action.TemperatureMode; got != "strip" {
		t.Errorf("rule[3].temperature_mode = %q, want strip", got)
	}
}

func TestLoad_WithRules(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
inbound:
  auth:
    keys:
      - name: "test"
        key: "sk-test"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "gpt-4"
    models:
      - "openai/gpt-4"
rules:
  - match:
      client-model: { op: equals, value: "claude-fast" }
    action:
      protocol: anthropic
  - match:
      upstream-model: { op: equals, value: "zhipu/glm-5.2" }
    action:
      effort: [high, max]
  - action:
      thinking: off
  - match:
      upstream-model: { op: equals, value: "tcodex-local/gpt-5.6-terra" }
    action:
      temperature_mode: strip
`
	cfg := loadTestConfig(t, yaml)

	if len(cfg.Rules) != 4 {
		t.Fatalf("len(Rules) = %d, want 4", len(cfg.Rules))
	}
	if cfg.Rules[0].Action.Protocol != "anthropic.messages" {
		t.Errorf("rule[0] protocol = %q, want anthropic.messages", cfg.Rules[0].Action.Protocol)
	}
	if cfg.Rules[1].Action.Effort[0] != "high" || cfg.Rules[1].Action.Effort[1] != "max" {
		t.Errorf("rule[1] effort = %v, want [high max]", cfg.Rules[1].Action.Effort)
	}
	if cfg.Rules[2].Action.Thinking != "off" {
		t.Errorf("rule[2] thinking = %q, want off", cfg.Rules[2].Action.Thinking)
	}
	if cfg.Rules[3].Action.TemperatureMode != "strip" {
		t.Errorf("rule[3] temperature_mode = %q, want strip", cfg.Rules[3].Action.TemperatureMode)
	}
}

func TestLoad_RejectsInvalidRules(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
inbound:
  auth:
    keys:
      - name: "test"
        key: "sk-test"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "gpt-4"
    models:
      - "openai/gpt-4"
rules:
  - match:
      key: { op: unknown, value: "x" }
    action:
      thinking: off
`
	if _, err := loadTestConfigErr(t, yaml); err == nil {
		t.Fatal("expected load error for invalid rules")
	} else if !strings.Contains(err.Error(), "validate rules") {
		t.Fatalf("error = %q, want validate rules failure", err.Error())
	}
}

func TestLoad_RejectsInvalidRuleProtocol(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
inbound:
  auth:
    keys:
      - name: "test"
        key: "sk-test"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "gpt-4"
    models:
      - "openai/gpt-4"
rules:
  - action:
      protocol: anthropic.message
`
	if _, err := loadTestConfigErr(t, yaml); err == nil {
		t.Fatal("expected load error for invalid protocol")
	} else if !strings.Contains(err.Error(), "invalid protocol") {
		t.Fatalf("error = %q, want invalid protocol failure", err.Error())
	}
}

func TestLoad_RejectsEmptyAction(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
inbound:
  auth:
    keys:
      - name: "test"
        key: "sk-test"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "gpt-4"
    models:
      - "openai/gpt-4"
rules:
  - match:
      key: { op: equals, value: "x" }
    action: {}
`
	if _, err := loadTestConfigErr(t, yaml); err == nil {
		t.Fatal("expected load error for empty action")
	} else if !strings.Contains(err.Error(), "rules[0].action") {
		t.Fatalf("error = %q, want rules[0].action path", err.Error())
	}
}

func TestLoad_RejectsCompoundRules(t *testing.T) {
	tests := []struct {
		name      string
		rulesYAML string
		wantPath  string
	}{
		{
			name: "two match fields",
			rulesYAML: `  - match:
      client-model: { op: equals, value: "claude-fast" }
      key: { op: startWith, value: "cc-" }
    action:
      thinking: off
`,
			wantPath: "rules[0].match",
		},
		{
			name: "effort and effort_mode",
			rulesYAML: `  - match:
      key: { op: equals, value: "x" }
    action:
      effort: [high]
      effort_mode: strip
`,
			wantPath: "rules[0].action",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := `
server:
  listen: ":18000"
inbound:
  auth:
    keys:
      - name: "test"
        key: "sk-test"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "gpt-4"
    models:
      - "openai/gpt-4"
rules:
` + tt.rulesYAML
			if _, err := loadTestConfigErr(t, yaml); err == nil {
				t.Fatal("expected load error for compound rule")
			} else if !strings.Contains(err.Error(), tt.wantPath) {
				t.Fatalf("error = %q, want path %q", err.Error(), tt.wantPath)
			}
		})
	}
}

func TestLoad_AcceptsMultiActionRule(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
inbound:
  auth:
    keys:
      - name: "test"
        key: "sk-test"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "gpt-4"
    models:
      - "openai/gpt-4"
rules:
  - match:
      upstream-model: { op: equals, value: "openai/gpt-4o" }
    action:
      protocol: openai.chat
      thinking: off
      qpm: 120
      enable_time_range: ["09:00-18:00"]
`
	cfg, err := loadTestConfigErr(t, yaml)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("rules len = %d, want 1", len(cfg.Rules))
	}
	got := cfg.Rules[0].Action
	if got.Protocol != "openai.chat" || got.Thinking != "off" {
		t.Fatalf("action = %+v, want protocol+thinking", got)
	}
	if got.QPM == nil || *got.QPM != 120 {
		t.Fatalf("qpm = %v, want 120", got.QPM)
	}
	if len(got.EnableTimeRange) != 1 || got.EnableTimeRange[0] != "09:00-18:00" {
		t.Fatalf("enable_time_range = %v, want [09:00-18:00]", got.EnableTimeRange)
	}
}

// TestLoad_RejectsInvalidSchedulingActions 验证调度类 action 的非法形态在 Load 阶段即失败：
// qpm 配 GLOBAL、显式 qpm: 0、非法时段字符串、同条 effort+effort_mode。
func TestLoad_RejectsInvalidSchedulingActions(t *testing.T) {
	tests := []struct {
		name      string
		rulesYAML string
		wantPath  string
		wantMsg   string
	}{
		{
			name: "qpm with global match",
			rulesYAML: `  - action:
      qpm: 100
`,
			wantPath: "rules[0].action.qpm",
			wantMsg:  "only allowed with upstream-model match",
		},
		{
			name: "qpm zero",
			rulesYAML: `  - match:
      upstream-model: { op: equals, value: "openai/gpt-4o" }
    action:
      qpm: 0
`,
			wantPath: "rules[0].action.qpm",
			wantMsg:  "positive integer",
		},
		{
			name: "invalid enable_time_range",
			rulesYAML: `  - match:
      upstream-model: { op: equals, value: "openai/gpt-4o" }
    action:
      enable_time_range: ["not-a-range"]
`,
			wantPath: "rules[0].action.enable_time_range[0]",
			wantMsg:  "invalid",
		},
		{
			name: "effort and effort_mode on same rule",
			rulesYAML: `  - match:
      upstream-model: { op: equals, value: "openai/gpt-4o" }
    action:
      effort: [high]
      effort_mode: strip
`,
			wantPath: "rules[0].action",
			wantMsg:  "effort and effort_mode cannot be set on the same rule",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := `
server:
  listen: ":18000"
inbound:
  auth:
    keys:
      - name: "test"
        key: "sk-test"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "gpt-4"
    models:
      - "openai/gpt-4"
rules:
` + tt.rulesYAML
			_, err := loadTestConfigErr(t, yaml)
			if err == nil {
				t.Fatal("expected load error for invalid scheduling action")
			}
			if !strings.Contains(err.Error(), tt.wantPath) {
				t.Fatalf("error = %q, want path %q", err.Error(), tt.wantPath)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error = %q, want message substring %q", err.Error(), tt.wantMsg)
			}
		})
	}
}

func loadTestConfig(t *testing.T, yamlContent string) *Config {
	t.Helper()
	cfg, err := loadTestConfigErr(t, yamlContent)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	return cfg
}

func loadTestConfigErr(t *testing.T, yamlContent string) (*Config, error) {
	t.Helper()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(path, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	return Load(path)
}
