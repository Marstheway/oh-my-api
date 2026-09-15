package rules

import (
	"reflect"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func iptr(v int) *int { return &v }

func TestEvaluate_QPMLastWriteWins(t *testing.T) {
	rules := []config.RuleConfig{
		{
			Match:  config.RuleMatch{UpstreamModel: cond("equals", "openai/gpt-4o")},
			Action: config.RuleAction{QPM: iptr(10)},
		},
		{
			Match:  config.RuleMatch{UpstreamModel: cond("startWith", "openai/")},
			Action: config.RuleAction{QPM: iptr(120)},
		},
	}

	got := Evaluate(rules, MatchContext{UpstreamModel: "openai/gpt-4o"})
	if got.QPM == nil {
		t.Fatal("QPM should be set")
	}
	if *got.QPM != 120 {
		t.Fatalf("QPM = %d, want 120 (later rule must override earlier qpm)", *got.QPM)
	}
}

func TestEvaluate_QPMLastWriteWinsCopiesValue(t *testing.T) {
	// 后写覆盖必须拷贝取值，合并结果不得与源 rule 共享底层 int。
	rules := []config.RuleConfig{
		{
			Match:  config.RuleMatch{UpstreamModel: cond("equals", "openai/gpt-4o")},
			Action: config.RuleAction{QPM: iptr(60)},
		},
	}
	got := Evaluate(rules, MatchContext{UpstreamModel: "openai/gpt-4o"})
	if got.QPM == nil || *got.QPM != 60 {
		t.Fatalf("QPM = %v, want 60", got.QPM)
	}

	// 修改源 rule 的 qpm 后合并结果不变。
	*rules[0].Action.QPM = 999
	if *got.QPM != 60 {
		t.Fatalf("merged QPM aliases source rule value: got %d, want 60", *got.QPM)
	}
}

func TestEvaluate_TimeRangeLastWriteWins(t *testing.T) {
	rules := []config.RuleConfig{
		{
			Match:  config.RuleMatch{UpstreamModel: cond("equals", "openai/gpt-4o")},
			Action: config.RuleAction{EnableTimeRange: []string{"09:00-12:00", "13:00-14:00"}},
		},
		{
			Match:  config.RuleMatch{UpstreamModel: cond("startWith", "openai/")},
			Action: config.RuleAction{EnableTimeRange: []string{"20:00-22:00"}},
		},
	}

	got := Evaluate(rules, MatchContext{UpstreamModel: "openai/gpt-4o"})
	want := []string{"20:00-22:00"}
	if !reflect.DeepEqual(got.EnableTimeRange, want) {
		t.Fatalf("EnableTimeRange = %v, want %v (later rule must replace whole range list)", got.EnableTimeRange, want)
	}
}

func TestEvaluate_EnableAndDisableBothKept(t *testing.T) {
	rules := []config.RuleConfig{
		{
			Match:  config.RuleMatch{UpstreamModel: cond("equals", "openai/gpt-4o")},
			Action: config.RuleAction{EnableTimeRange: []string{"09:00-18:00"}},
		},
		{
			Match:  config.RuleMatch{UpstreamModel: cond("startWith", "openai/")},
			Action: config.RuleAction{DisableTimeRange: []string{"23:00-02:00"}},
		},
	}

	got := Evaluate(rules, MatchContext{UpstreamModel: "openai/gpt-4o"})
	if !reflect.DeepEqual(got.EnableTimeRange, []string{"09:00-18:00"}) {
		t.Fatalf("EnableTimeRange = %v, want [09:00-18:00] (enable must not be cleared by disable rule)", got.EnableTimeRange)
	}
	if !reflect.DeepEqual(got.DisableTimeRange, []string{"23:00-02:00"}) {
		t.Fatalf("DisableTimeRange = %v, want [23:00-02:00]", got.DisableTimeRange)
	}
}

func TestEvaluate_SchedulingAndRewriteActionsMergeIndependently(t *testing.T) {
	rules := []config.RuleConfig{
		{
			Match:  config.RuleMatch{UpstreamModel: cond("equals", "openai/gpt-4o")},
			Action: config.RuleAction{QPM: iptr(60)},
		},
		{
			Match:  config.RuleMatch{UpstreamModel: cond("startWith", "openai/")},
			Action: config.RuleAction{Protocol: "anthropic.messages"},
		},
	}

	got := Evaluate(rules, MatchContext{UpstreamModel: "openai/gpt-4o"})
	if got.QPM == nil || *got.QPM != 60 {
		t.Fatalf("QPM = %v, want 60", got.QPM)
	}
	if got.Protocol != "anthropic.messages" {
		t.Fatalf("Protocol = %q, want anthropic.messages", got.Protocol)
	}
}

func TestEvaluate_RetriesLastWriteWinsCopiesValue(t *testing.T) {
	rules := []config.RuleConfig{
		{
			Match:  config.RuleMatch{UpstreamModel: cond("startWith", "openai/")},
			Action: config.RuleAction{Retries: iptr(2)},
		},
		{
			Match:  config.RuleMatch{UpstreamModel: cond("equals", "openai/gpt-4o")},
			Action: config.RuleAction{Retries: iptr(0)},
		},
	}

	got := Evaluate(rules, MatchContext{UpstreamModel: "openai/gpt-4o"})
	if got.Retries == nil {
		t.Fatal("Retries should be set")
	}
	if *got.Retries != 0 {
		t.Fatalf("Retries = %d, want 0 (later rule must override, including explicit 0)", *got.Retries)
	}

	*rules[1].Action.Retries = 2
	if *got.Retries != 0 {
		t.Fatalf("merged Retries aliases source rule value: got %d, want 0", *got.Retries)
	}
}

func TestEvaluate_SingleRuleMultipleActions(t *testing.T) {
	rules := []config.RuleConfig{
		{
			Match: config.RuleMatch{UpstreamModel: cond("equals", "openai/gpt-4o")},
			Action: config.RuleAction{
				Protocol:         "openai.chat",
				Thinking:         "off",
				QPM:              iptr(120),
				EnableTimeRange:  []string{"09:00-18:00"},
				DisableTimeRange: []string{"23:00-02:00"},
				Retries:          iptr(2),
			},
		},
	}

	got := Evaluate(rules, MatchContext{UpstreamModel: "openai/gpt-4o"})
	if got.Protocol != "openai.chat" {
		t.Fatalf("Protocol = %q, want openai.chat", got.Protocol)
	}
	if got.Thinking != "off" {
		t.Fatalf("Thinking = %q, want off", got.Thinking)
	}
	if got.QPM == nil || *got.QPM != 120 {
		t.Fatalf("QPM = %v, want 120", got.QPM)
	}
	if !reflect.DeepEqual(got.EnableTimeRange, []string{"09:00-18:00"}) {
		t.Fatalf("EnableTimeRange = %v, want [09:00-18:00]", got.EnableTimeRange)
	}
	if !reflect.DeepEqual(got.DisableTimeRange, []string{"23:00-02:00"}) {
		t.Fatalf("DisableTimeRange = %v, want [23:00-02:00]", got.DisableTimeRange)
	}
	if got.Retries == nil || *got.Retries != 2 {
		t.Fatalf("Retries = %v, want 2", got.Retries)
	}
}

func TestEvaluate_MaxTokensLastWriteWins(t *testing.T) {
	rules := []config.RuleConfig{
		{
			Match:  config.RuleMatch{UpstreamModel: cond("equals", "openai/gpt-4o")},
			Action: config.RuleAction{MaxTokens: iptr(1000)},
		},
		{
			Match:  config.RuleMatch{UpstreamModel: cond("startWith", "openai/")},
			Action: config.RuleAction{MaxTokens: iptr(2000)},
		},
	}

	got := Evaluate(rules, MatchContext{UpstreamModel: "openai/gpt-4o"})
	if got.MaxTokens == nil {
		t.Fatal("MaxTokens should be set")
	}
	if *got.MaxTokens != 2000 {
		t.Fatalf("MaxTokens = %d, want 2000 (later rule must override earlier max_tokens)", *got.MaxTokens)
	}
}

func TestEvaluate_MaxTokensCopiesValue(t *testing.T) {
	rules := []config.RuleConfig{
		{
			Match:  config.RuleMatch{UpstreamModel: cond("equals", "openai/gpt-4o")},
			Action: config.RuleAction{MaxTokens: iptr(800)},
		},
	}
	got := Evaluate(rules, MatchContext{UpstreamModel: "openai/gpt-4o"})
	if got.MaxTokens == nil || *got.MaxTokens != 800 {
		t.Fatalf("MaxTokens = %v, want 800", got.MaxTokens)
	}

	// 修改源 rule 后合并结果不变（拷贝取值）。
	*rules[0].Action.MaxTokens = 999
	if *got.MaxTokens != 800 {
		t.Fatalf("merged MaxTokens aliases source rule value: got %d, want 800", *got.MaxTokens)
	}
}

func TestEvaluate_TemperatureModeLastWriteWins(t *testing.T) {
	rules := []config.RuleConfig{
		{
			Match:  config.RuleMatch{UpstreamModel: cond("equals", "openai/gpt-4o")},
			Action: config.RuleAction{Thinking: "off"},
		},
		{
			Match:  config.RuleMatch{UpstreamModel: cond("startWith", "openai/")},
			Action: config.RuleAction{TemperatureMode: "strip"},
		},
	}

	got := Evaluate(rules, MatchContext{UpstreamModel: "openai/gpt-4o"})
	if got.TemperatureMode != "strip" {
		t.Fatalf("TemperatureMode = %q, want strip", got.TemperatureMode)
	}
	if got.Thinking != "off" {
		t.Fatalf("Thinking = %q, want off (different action class must stack)", got.Thinking)
	}
}
