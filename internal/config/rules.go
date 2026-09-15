package config

import (
	"fmt"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/codec"
)

// RuleConfig 定义一条有序请求规则。
// match 至多一个条件字段（空 match = GLOBAL）；action 至少一类、可多类并存，由 ValidateRules 强制。
// effort 与 effort_mode 同条互斥（合并时也会互相清空），需分两条规则表达。
type RuleConfig struct {
	Match  RuleMatch  `yaml:"match"`
	Action RuleAction `yaml:"action"`
}

// RuleMatch 定义规则的匹配条件；至多一个字段可设置，省略的字段不参与匹配。
type RuleMatch struct {
	ClientModel   *RuleCondition `yaml:"client-model,omitempty"`
	Key           *RuleCondition `yaml:"key,omitempty"`
	UpstreamModel *RuleCondition `yaml:"upstream-model,omitempty"`
}

// RuleCondition 定义单个 match 字段的比较条件。
type RuleCondition struct {
	Op    string `yaml:"op"`
	Value string `yaml:"value"`
}

// RuleAction 定义规则命中后的 action；至少一类、可多类并存，同类 action 由后续命中规则覆盖。
// 请求改写类：protocol / effort / effort_mode / temperature_mode / thinking / max_tokens，在物化时修改请求。
// 调度类：qpm（per-model 限流）/ enable_time_range / disable_time_range / retries，在调度执行时生效。
// effort 与 effort_mode 同条互斥；qpm 为 *int：nil=未设置该类；非 nil 必须 >0（显式 0 视为非法，而不是未设置）。
// retries 为 *int：nil=未设置该类（默认不额外重试）；非 nil 必须在 [0, 2]（0 可覆盖更宽规则）。
// max_tokens 为 *int：nil=未设置该类；非 nil 必须 >0，作为请求输出预算下限（floor）。
type RuleAction struct {
	Protocol   string   `yaml:"protocol,omitempty"`
	Effort     []string `yaml:"effort,omitempty"`
	EffortMode string   `yaml:"effort_mode,omitempty"`
	// TemperatureMode 目前仅支持 strip：剔除发给上游的 temperature。
	TemperatureMode string `yaml:"temperature_mode,omitempty"`
	Thinking        string `yaml:"thinking,omitempty"`
	// MaxTokens 是请求 max_tokens 的下限（floor）钳制：请求未带或小于该值时补/提到配置值，
	// 请求已带且大于该值时保持不动。nil=未设置该类。
	MaxTokens        *int     `yaml:"max_tokens,omitempty"`
	QPM              *int     `yaml:"qpm,omitempty"`
	EnableTimeRange  []string `yaml:"enable_time_range,omitempty"`
	DisableTimeRange []string `yaml:"disable_time_range,omitempty"`
	// Retries 是同一叶子在瞬时失败后的额外尝试次数（不含首次）；总尝试 = 1 + retries，最多 3 次。
	Retries *int `yaml:"retries,omitempty"`
}

var validRuleOps = map[string]struct{}{
	"equals":    {},
	"startWith": {},
	"include":   {},
}

// NormalizeRulesInConfig 对 cfg.Rules 做原地规范化，返回是否有变化。
func NormalizeRulesInConfig(cfg *Config) bool {
	if cfg == nil || len(cfg.Rules) == 0 {
		return false
	}

	changed := false
	for i := range cfg.Rules {
		if normalizeRuleAction(&cfg.Rules[i].Action) {
			changed = true
		}
	}
	return changed
}

func normalizeRuleAction(action *RuleAction) bool {
	changed := false

	if action.Protocol != "" {
		if full := ResolveProtocolAlias(action.Protocol); full != action.Protocol {
			action.Protocol = full
			changed = true
		}
	}

	if normalizeReasoningEffortSlice(&action.Effort) {
		changed = true
	}

	mode := strings.ToLower(strings.TrimSpace(action.EffortMode))
	if mode != action.EffortMode {
		action.EffortMode = mode
		changed = true
	}

	tempMode := strings.ToLower(strings.TrimSpace(action.TemperatureMode))
	if tempMode != action.TemperatureMode {
		action.TemperatureMode = tempMode
		changed = true
	}

	thinking := strings.ToLower(strings.TrimSpace(action.Thinking))
	if thinking != action.Thinking {
		action.Thinking = thinking
		changed = true
	}

	return changed
}

// ValidateRules 校验 rules 配置合法性，供 Load 与 Apply 共用。
// 合同：单条 rule 至多一个 match 字段（0 个 = GLOBAL）；action 至少一类、可多类并存；
// effort 与 effort_mode 同条互斥。校验顺序固定为先 match（条件合法性/基数）再 action（基数与枚举），
// 保证非法 op 夹具的错误路径落在 rules[i].match.*.op，空 action 落在 rules[i].action。
func ValidateRules(rules []RuleConfig) error {
	issues := make([]ValidationIssue, 0)

	for i, rule := range rules {
		prefix := fmt.Sprintf("rules[%d]", i)
		issues = append(issues, validateRuleMatch(prefix+".match", rule.Match)...)
		issues = append(issues, validateRuleAction(prefix+".action", rule.Match, rule.Action)...)
	}

	if len(issues) > 0 {
		return ValidationError{Issues: issues}
	}
	return nil
}

func validateRuleMatch(path string, match RuleMatch) []ValidationIssue {
	issues := make([]ValidationIssue, 0)

	conds := []struct {
		name string
		cond *RuleCondition
	}{
		{"client-model", match.ClientModel},
		{"key", match.Key},
		{"upstream-model", match.UpstreamModel},
	}

	count := 0
	for _, c := range conds {
		if c.cond != nil {
			count++
		}
	}
	if count > 1 {
		issues = append(issues, ValidationIssue{
			Path:    path,
			Message: fmt.Sprintf("match must have at most one condition field, got %d", count),
		})
	}

	for _, c := range conds {
		if c.cond != nil {
			issues = append(issues, validateRuleCondition(path+"."+c.name, c.cond)...)
		}
	}

	return issues
}

func validateRuleCondition(path string, cond *RuleCondition) []ValidationIssue {
	if cond == nil {
		return nil
	}

	if _, ok := validRuleOps[cond.Op]; !ok {
		return []ValidationIssue{{
			Path:    path + ".op",
			Message: fmt.Sprintf("invalid op %q: must be one of equals, startWith, include", cond.Op),
		}}
	}
	return nil
}

func validateRuleAction(path string, match RuleMatch, action RuleAction) []ValidationIssue {
	issues := make([]ValidationIssue, 0)

	classCount := 0
	if action.Protocol != "" {
		classCount++
	}
	if len(action.Effort) > 0 {
		classCount++
	}
	if action.EffortMode != "" {
		classCount++
	}
	if action.TemperatureMode != "" {
		classCount++
	}
	if action.Thinking != "" {
		classCount++
	}
	if action.MaxTokens != nil {
		classCount++
	}
	if action.QPM != nil {
		classCount++
	}
	if len(action.EnableTimeRange) > 0 {
		classCount++
	}
	if len(action.DisableTimeRange) > 0 {
		classCount++
	}
	if action.Retries != nil {
		classCount++
	}
	if classCount == 0 {
		issues = append(issues, ValidationIssue{
			Path:    path,
			Message: "action must have at least one action class (protocol, effort, effort_mode, temperature_mode, thinking, max_tokens, qpm, enable_time_range, disable_time_range, retries), got 0",
		})
	}
	if len(action.Effort) > 0 && action.EffortMode != "" {
		issues = append(issues, ValidationIssue{
			Path:    path,
			Message: "effort and effort_mode cannot be set on the same rule (mutually exclusive; split into separate rules)",
		})
	}

	if action.Protocol != "" {
		full := ResolveProtocolAlias(strings.TrimSpace(action.Protocol))
		if _, err := codec.NormalizeProviderFormat(full); err != nil {
			issues = append(issues, ValidationIssue{
				Path:    path + ".protocol",
				Message: fmt.Sprintf("invalid protocol %q: must be a known provider protocol (aliases openai/anthropic are accepted)", action.Protocol),
			})
		}
	}

	if len(action.Effort) > 0 {
		if err := validateEffortAllowList(action.Effort); err != nil {
			issues = append(issues, ValidationIssue{
				Path:    path + ".effort",
				Message: err.Error(),
			})
		}
	}

	if action.EffortMode != "" && action.EffortMode != "strip" {
		issues = append(issues, ValidationIssue{
			Path:    path + ".effort_mode",
			Message: fmt.Sprintf("invalid effort_mode %q: must be strip", action.EffortMode),
		})
	}

	if action.TemperatureMode != "" && action.TemperatureMode != "strip" {
		issues = append(issues, ValidationIssue{
			Path:    path + ".temperature_mode",
			Message: fmt.Sprintf("invalid temperature_mode %q: must be strip", action.TemperatureMode),
		})
	}

	if action.Thinking != "" && action.Thinking != "on" && action.Thinking != "off" {
		issues = append(issues, ValidationIssue{
			Path:    path + ".thinking",
			Message: fmt.Sprintf("invalid thinking %q: must be on or off", action.Thinking),
		})
	}

	if action.MaxTokens != nil {
		if *action.MaxTokens <= 0 {
			issues = append(issues, ValidationIssue{
				Path:    path + ".max_tokens",
				Message: fmt.Sprintf("invalid max_tokens %d: must be a positive integer", *action.MaxTokens),
			})
		}
	}

	if action.QPM != nil {
		if *action.QPM <= 0 {
			issues = append(issues, ValidationIssue{
				Path:    path + ".qpm",
				Message: fmt.Sprintf("invalid qpm %d: must be a positive integer", *action.QPM),
			})
		}
		if match.UpstreamModel == nil {
			issues = append(issues, ValidationIssue{
				Path:    path + ".qpm",
				Message: "qpm action is only allowed with upstream-model match (rate limits protect the upstream identity)",
			})
		}
	}

	for i, raw := range action.EnableTimeRange {
		if _, err := ParseTimeRange(raw); err != nil {
			issues = append(issues, ValidationIssue{
				Path:    fmt.Sprintf("%s.enable_time_range[%d]", path, i),
				Message: err.Error(),
			})
		}
	}
	for i, raw := range action.DisableTimeRange {
		if _, err := ParseTimeRange(raw); err != nil {
			issues = append(issues, ValidationIssue{
				Path:    fmt.Sprintf("%s.disable_time_range[%d]", path, i),
				Message: err.Error(),
			})
		}
	}

	if action.Retries != nil {
		if *action.Retries < 0 || *action.Retries > 2 {
			issues = append(issues, ValidationIssue{
				Path:    path + ".retries",
				Message: fmt.Sprintf("invalid retries %d: must be an integer in [0, 2]", *action.Retries),
			})
		}
	}

	return issues
}

func validateEffortAllowList(effort []string) error {
	if len(effort) == 0 {
		return nil
	}

	hasNone := false
	for _, e := range effort {
		if e == "none" {
			hasNone = true
			continue
		}
		if _, ok := effortRank[e]; !ok {
			return fmt.Errorf("invalid effort value %q: must be one of low, medium, high, xhigh, max, or exactly [none]", e)
		}
	}

	if hasNone && len(effort) != 1 {
		return fmt.Errorf("effort [none] must be exclusive")
	}
	return nil
}
