package rules

import (
	"github.com/Marstheway/oh-my-api/internal/config"
)

// MergedAction is the last-write-wins snapshot of rule actions.
type MergedAction = config.RuleAction

// Evaluate 按配置顺序评估所有规则，合并命中规则的 action。
func Evaluate(rules []config.RuleConfig, ctx MatchContext) MergedAction {
	var merged MergedAction
	for _, rule := range rules {
		if !MatchRule(rule.Match, ctx) {
			continue
		}
		mergeAction(&merged, rule.Action)
	}
	return merged
}

func mergeAction(merged *MergedAction, action config.RuleAction) {
	if action.Protocol != "" {
		merged.Protocol = action.Protocol
	}

	if len(action.Effort) > 0 {
		merged.Effort = append([]string(nil), action.Effort...)
		merged.EffortMode = ""
	}
	if action.EffortMode != "" {
		merged.EffortMode = action.EffortMode
		merged.Effort = nil
	}

	if action.TemperatureMode != "" {
		merged.TemperatureMode = action.TemperatureMode
	}

	if action.Thinking != "" {
		merged.Thinking = action.Thinking
	}

	if action.MaxTokens != nil {
		n := *action.MaxTokens
		merged.MaxTokens = &n
	}

	// 调度类：qpm 后写覆盖（拷贝取值，避免共享底层 int）；
	// enable_time_range / disable_time_range 后写整表替换；两类互不清空。
	if action.QPM != nil {
		q := *action.QPM
		merged.QPM = &q
	}
	if len(action.EnableTimeRange) > 0 {
		merged.EnableTimeRange = append([]string(nil), action.EnableTimeRange...)
	}
	if len(action.DisableTimeRange) > 0 {
		merged.DisableTimeRange = append([]string(nil), action.DisableTimeRange...)
	}
	if action.Retries != nil {
		n := *action.Retries
		merged.Retries = &n
	}
}
