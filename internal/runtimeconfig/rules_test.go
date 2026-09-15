package runtimeconfig

import (
	"strings"
	"testing"
)

func TestManagerListRules_InitialEmpty(t *testing.T) {
	cfg, configPath := createTestConfig(t)
	mgr, err := NewManager(cfg, configPath, &mockRebuilder{}, &mockReinit{})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	if rules := mgr.ListRules(); len(rules) != 0 {
		t.Fatalf("expected no rules initially, got %d", len(rules))
	}
}

func TestManagerReplaceRules(t *testing.T) {
	cfg, configPath := createTestConfig(t)
	mgr, err := NewManager(cfg, configPath, &mockRebuilder{}, &mockReinit{})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	input := []RuleInput{
		{
			Match: RuleMatchInput{
				ClientModel: &RuleConditionInput{Op: "equals", Value: "gpt-4"},
			},
			Action: RuleActionInput{Protocol: "openai"},
		},
		{
			Match: RuleMatchInput{
				ClientModel: &RuleConditionInput{Op: "equals", Value: "gpt-4"},
			},
			Action: RuleActionInput{Thinking: "off"},
		},
		{
			Action: RuleActionInput{Effort: []string{"high", "max"}},
		},
		{
			Match: RuleMatchInput{
				UpstreamModel: &RuleConditionInput{Op: "equals", Value: "tcodex-local/gpt-5.6-terra"},
			},
			Action: RuleActionInput{TemperatureMode: "strip"},
		},
	}

	if err := mgr.ReplaceRules(input); err != nil {
		t.Fatalf("replace rules: %v", err)
	}

	rules := mgr.ListRules()
	if len(rules) != 4 {
		t.Fatalf("rules len = %d, want 4", len(rules))
	}

	r0 := rules[0]
	if r0.Match.ClientModel == nil || r0.Match.ClientModel.Op != "equals" || r0.Match.ClientModel.Value != "gpt-4" {
		t.Errorf("rules[0].match.client_model = %+v, want equals gpt-4", r0.Match.ClientModel)
	}
	if r0.Match.Key != nil || r0.Match.UpstreamModel != nil {
		t.Errorf("rules[0].match = %+v, want only client_model", r0.Match)
	}
	if r0.Action.Protocol != "openai.chat" { // 协议别名应在 Replace 时规范化
		t.Errorf("rules[0].action.protocol = %q, want openai.chat", r0.Action.Protocol)
	}

	r1 := rules[1]
	if r1.Match.ClientModel == nil || r1.Match.ClientModel.Value != "gpt-4" {
		t.Errorf("rules[1].match = %+v, want client_model gpt-4", r1.Match)
	}
	if r1.Action.Thinking != "off" {
		t.Errorf("rules[1].action.thinking = %q, want off", r1.Action.Thinking)
	}

	r2 := rules[2]
	if r2.Match.ClientModel != nil || r2.Match.Key != nil || r2.Match.UpstreamModel != nil {
		t.Errorf("rules[2].match = %+v, want empty (global rule)", r2.Match)
	}
	if len(r2.Action.Effort) != 2 || r2.Action.Effort[0] != "high" || r2.Action.Effort[1] != "max" {
		t.Errorf("rules[2].action.effort = %+v, want [high max]", r2.Action.Effort)
	}

	r3 := rules[3]
	if r3.Action.TemperatureMode != "strip" {
		t.Errorf("rules[3].action.temperature_mode = %q, want strip", r3.Action.TemperatureMode)
	}
	if out := ToRuleOutput(rules[3]); out.Action.TemperatureMode != "strip" {
		t.Errorf("ToRuleOutput temperature_mode = %q, want strip", out.Action.TemperatureMode)
	}

	// active 不受影响
	if len(mgr.GetActive().Rules) != 0 {
		t.Errorf("active rules len = %d, want 0", len(mgr.GetActive().Rules))
	}
}

func TestManagerReplaceRulesClears(t *testing.T) {
	cfg, configPath := createTestConfig(t)
	mgr, err := NewManager(cfg, configPath, &mockRebuilder{}, &mockReinit{})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	if err := mgr.ReplaceRules([]RuleInput{{
		Match:  RuleMatchInput{ClientModel: &RuleConditionInput{Op: "equals", Value: "gpt-4"}},
		Action: RuleActionInput{Protocol: "openai.chat"},
	}}); err != nil {
		t.Fatalf("replace rules: %v", err)
	}

	// 空切片 = 清空
	if err := mgr.ReplaceRules([]RuleInput{}); err != nil {
		t.Fatalf("clear rules: %v", err)
	}
	if rules := mgr.ListRules(); len(rules) != 0 {
		t.Errorf("rules len = %d after clear, want 0", len(rules))
	}
}

func TestManagerReplaceRulesInputIsolation(t *testing.T) {
	cfg, configPath := createTestConfig(t)
	mgr, err := NewManager(cfg, configPath, &mockRebuilder{}, &mockReinit{})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	effort := []string{"high", "max"}
	input := []RuleInput{{
		Match:  RuleMatchInput{ClientModel: &RuleConditionInput{Op: "equals", Value: "gpt-4"}},
		Action: RuleActionInput{Effort: effort},
	}}
	if err := mgr.ReplaceRules(input); err != nil {
		t.Fatalf("replace rules: %v", err)
	}

	// 修改输入切片，draft 不应受影响（深拷贝写入）
	input[0].Match.ClientModel.Value = "mutated"
	input[0].Action.Effort[0] = "low"

	rules := mgr.ListRules()
	if rules[0].Match.ClientModel.Value != "gpt-4" {
		t.Errorf("rules[0].match value = %q, want gpt-4", rules[0].Match.ClientModel.Value)
	}
	if rules[0].Action.Effort[0] != "high" {
		t.Errorf("rules[0].effort[0] = %q, want high", rules[0].Action.Effort[0])
	}
}

func TestManagerReplaceRulesMaxTokensIsolation(t *testing.T) {
	cfg, configPath := createTestConfig(t)
	mgr, err := NewManager(cfg, configPath, &mockRebuilder{}, &mockReinit{})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	n := 2000
	if err := mgr.ReplaceRules([]RuleInput{{
		Match:  RuleMatchInput{UpstreamModel: &RuleConditionInput{Op: "equals", Value: "p/m"}},
		Action: RuleActionInput{MaxTokens: &n},
	}}); err != nil {
		t.Fatalf("replace rules: %v", err)
	}

	n = 1
	rules := mgr.ListRules()
	if len(rules) != 1 {
		t.Fatalf("rules len = %d, want 1", len(rules))
	}
	if rules[0].Action.MaxTokens == nil || *rules[0].Action.MaxTokens != 2000 {
		t.Fatalf("MaxTokens = %v, want 2000 after mutating input", rules[0].Action.MaxTokens)
	}
}

func TestManagerReplaceRulesValidationFailureKeepsDraft(t *testing.T) {
	cfg, configPath := createTestConfig(t)
	mgr, err := NewManager(cfg, configPath, &mockRebuilder{}, &mockReinit{})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 先写入一条合法规则
	valid := []RuleInput{{
		Match:  RuleMatchInput{ClientModel: &RuleConditionInput{Op: "equals", Value: "gpt-4"}},
		Action: RuleActionInput{Protocol: "openai.chat"},
	}}
	if err := mgr.ReplaceRules(valid); err != nil {
		t.Fatalf("replace rules: %v", err)
	}
	before := mgr.ListRules()

	// 非法 op
	err = mgr.ReplaceRules([]RuleInput{{
		Match: RuleMatchInput{Key: &RuleConditionInput{Op: "regex", Value: "x"}},
	}})
	if err == nil {
		t.Fatal("expected validation error for invalid op")
	}
	rerr, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *runtimeconfig.Error, got %T", err)
	}
	if rerr.Code != ErrCodeValidation {
		t.Errorf("error code = %q, want %q", rerr.Code, ErrCodeValidation)
	}
	if !strings.Contains(rerr.Message, "rules[0].match.key.op") {
		t.Errorf("error message %q must contain rules[0].match.key.op path", rerr.Message)
	}
	if rerr.Field != "rules[0].match.key.op" {
		t.Errorf("error field = %q, want rules[0].match.key.op", rerr.Field)
	}

	// draft 不变
	after := mgr.ListRules()
	if len(after) != len(before) {
		t.Fatalf("rules len changed after failed replace: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i].Action.Protocol != after[i].Action.Protocol {
			t.Errorf("rules[%d] changed after failed replace", i)
		}
	}

	// effort 与 effort_mode 同条（多类 action）→ 拒绝
	err = mgr.ReplaceRules([]RuleInput{{
		Action: RuleActionInput{Effort: []string{"high"}, EffortMode: "strip"},
	}})
	if err == nil {
		t.Fatal("expected validation error for effort + effort_mode in same rule")
	}
	if len(mgr.ListRules()) != len(before) {
		t.Error("draft changed after failed replace")
	}

	// effort none 与其它档位混用
	err = mgr.ReplaceRules([]RuleInput{{
		Action: RuleActionInput{Effort: []string{"none", "high"}},
	}})
	if err == nil {
		t.Fatal("expected validation error for effort [none, high]")
	}
	if len(mgr.ListRules()) != len(before) {
		t.Error("draft changed after failed replace")
	}

	// 空 action → 拒绝，错误路径落在 rules[0].action
	err = mgr.ReplaceRules([]RuleInput{{
		Match: RuleMatchInput{Key: &RuleConditionInput{Op: "equals", Value: "x"}},
	}})
	if err == nil {
		t.Fatal("expected validation error for empty action")
	}
	if !strings.Contains(err.Error(), "rules[0].action") {
		t.Errorf("error %q must contain rules[0].action path", err.Error())
	}
	if len(mgr.ListRules()) != len(before) {
		t.Error("draft changed after failed replace")
	}

	// 同条双 match 字段 → 拒绝，错误路径落在 rules[0].match
	err = mgr.ReplaceRules([]RuleInput{{
		Match: RuleMatchInput{
			ClientModel: &RuleConditionInput{Op: "equals", Value: "gpt-4"},
			Key:         &RuleConditionInput{Op: "equals", Value: "cc-"},
		},
		Action: RuleActionInput{Thinking: "off"},
	}})
	if err == nil {
		t.Fatal("expected validation error for two match fields")
	}
	if !strings.Contains(err.Error(), "rules[0].match") {
		t.Errorf("error %q must contain rules[0].match path", err.Error())
	}
	if len(mgr.ListRules()) != len(before) {
		t.Error("draft changed after failed replace")
	}
}
