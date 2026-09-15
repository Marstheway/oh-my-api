package rules

import (
	"reflect"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func cond(op, value string) *config.RuleCondition {
	return &config.RuleCondition{Op: op, Value: value}
}

func TestMatchRule_GlobalHit(t *testing.T) {
	match := config.RuleMatch{}
	ctx := MatchContext{ClientModel: "anything", KeyName: "k", UpstreamModel: "p/m"}
	if !MatchRule(match, ctx) {
		t.Fatal("empty match should global hit")
	}
}

func TestMatchRule_Operators(t *testing.T) {
	tests := []struct {
		name  string
		match config.RuleMatch
		ctx   MatchContext
		want  bool
	}{
		{
			name:  "equals case sensitive",
			match: config.RuleMatch{ClientModel: cond("equals", "GPT-4")},
			ctx:   MatchContext{ClientModel: "gpt-4"},
			want:  false,
		},
		{
			name:  "equals trims value",
			match: config.RuleMatch{ClientModel: cond("equals", " gpt-4 ")},
			ctx:   MatchContext{ClientModel: "gpt-4"},
			want:  true,
		},
		{
			name:  "equals trims actual",
			match: config.RuleMatch{ClientModel: cond("equals", "gpt-4")},
			ctx:   MatchContext{ClientModel: " gpt-4 "},
			want:  true,
		},
		{
			name:  "startWith",
			match: config.RuleMatch{UpstreamModel: cond("startWith", "deepseek/")},
			ctx:   MatchContext{UpstreamModel: "deepseek/chat"},
			want:  true,
		},
		{
			name:  "include",
			match: config.RuleMatch{UpstreamModel: cond("include", "deepseek/")},
			ctx:   MatchContext{UpstreamModel: "openrouter/deepseek/chat"},
			want:  true,
		},
		{
			name:  "empty value after trim fails",
			match: config.RuleMatch{Key: cond("equals", "   ")},
			ctx:   MatchContext{KeyName: "anything"},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchRule(tt.match, tt.ctx); got != tt.want {
				t.Fatalf("MatchRule() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluate_MergesAtomicRulesInOrder 验证原子规则叠层：同 match 拆成相邻多行后，
// 合并结果与旧复合多 action 同条在后写覆盖语义下等价（同类 action 后者覆盖前者，
// 不同类 action 相互叠加）。
func TestEvaluate_MergesAtomicRulesInOrder(t *testing.T) {
	rules := []config.RuleConfig{
		{
			Match:  config.RuleMatch{UpstreamModel: cond("startWith", "zhipu/")},
			Action: config.RuleAction{Protocol: "anthropic.messages"},
		},
		{
			Match:  config.RuleMatch{UpstreamModel: cond("startWith", "zhipu/")},
			Action: config.RuleAction{Thinking: "off"},
		},
		{
			Match:  config.RuleMatch{UpstreamModel: cond("equals", "zhipu/glm-5.2")},
			Action: config.RuleAction{Effort: []string{"high", "max"}},
		},
		{
			Match:  config.RuleMatch{ClientModel: cond("equals", "claude-fast")},
			Action: config.RuleAction{Protocol: "openai.chat"},
		},
		{
			Match:  config.RuleMatch{ClientModel: cond("equals", "claude-fast")},
			Action: config.RuleAction{Thinking: "on"},
		},
	}

	ctx := MatchContext{
		ClientModel:   "claude-fast",
		KeyName:       "cc-main",
		UpstreamModel: "zhipu/glm-5.2",
	}

	got := Evaluate(rules, ctx)
	want := MergedAction{
		Protocol: "openai.chat",
		Effort:   []string{"high", "max"},
		Thinking: "on",
	}

	if got.Protocol != want.Protocol {
		t.Errorf("Protocol = %q, want %q (protocol row must be overridden by later protocol row)", got.Protocol, want.Protocol)
	}
	if !reflect.DeepEqual(got.Effort, want.Effort) {
		t.Errorf("Effort = %v, want %v", got.Effort, want.Effort)
	}
	if got.Thinking != want.Thinking {
		t.Errorf("Thinking = %q, want %q (thinking off row must be overridden by later thinking on row)", got.Thinking, want.Thinking)
	}
}

func TestEvaluate_EmptyRulesReturnsZeroValue(t *testing.T) {
	got := Evaluate(nil, MatchContext{ClientModel: "x"})
	var want MergedAction
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Evaluate(nil) = %+v, want zero value", got)
	}
}

func TestEvaluate_ThinkingOnOverridesOff(t *testing.T) {
	rules := []config.RuleConfig{
		{
			Match:  config.RuleMatch{},
			Action: config.RuleAction{Thinking: "off"},
		},
		{
			Match:  config.RuleMatch{},
			Action: config.RuleAction{Effort: []string{"high", "max"}},
		},
		{
			Match:  config.RuleMatch{Key: cond("equals", "vip")},
			Action: config.RuleAction{Thinking: "on"},
		},
	}

	got := Evaluate(rules, MatchContext{KeyName: "vip"})
	if got.Thinking != "on" {
		t.Fatalf("Thinking = %q, want on", got.Thinking)
	}
	if !reflect.DeepEqual(got.Effort, []string{"high", "max"}) {
		t.Fatalf("Effort = %v, want [high max]", got.Effort)
	}
}

func TestEvaluate_EffortModeAndEffortOverwrite(t *testing.T) {
	rules := []config.RuleConfig{
		{
			Match:  config.RuleMatch{},
			Action: config.RuleAction{Effort: []string{"high", "max"}},
		},
		{
			Match:  config.RuleMatch{Key: cond("equals", "strip-key")},
			Action: config.RuleAction{EffortMode: "strip"},
		},
		{
			Match:  config.RuleMatch{Key: cond("equals", "effort-key")},
			Action: config.RuleAction{Effort: []string{"low", "medium"}},
		},
	}

	stripGot := Evaluate(rules, MatchContext{KeyName: "strip-key"})
	if stripGot.EffortMode != "strip" {
		t.Fatalf("EffortMode = %q, want strip", stripGot.EffortMode)
	}
	if stripGot.Effort != nil {
		t.Fatalf("Effort = %v, want nil after strip", stripGot.Effort)
	}

	effortGot := Evaluate(rules, MatchContext{KeyName: "effort-key"})
	if effortGot.EffortMode != "" {
		t.Fatalf("EffortMode = %q, want empty", effortGot.EffortMode)
	}
	if !reflect.DeepEqual(effortGot.Effort, []string{"low", "medium"}) {
		t.Fatalf("Effort = %v, want [low medium]", effortGot.Effort)
	}
}

func TestEvaluate_SkipsNonMatchingRules(t *testing.T) {
	rules := []config.RuleConfig{
		{
			Match:  config.RuleMatch{ClientModel: cond("equals", "other")},
			Action: config.RuleAction{Protocol: "anthropic.messages"},
		},
		{
			Match:  config.RuleMatch{ClientModel: cond("equals", "target")},
			Action: config.RuleAction{Protocol: "openai.chat"},
		},
	}

	got := Evaluate(rules, MatchContext{ClientModel: "target"})
	if got.Protocol != "openai.chat" {
		t.Fatalf("Protocol = %q, want openai.chat", got.Protocol)
	}
}
