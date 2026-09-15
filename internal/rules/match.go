package rules

import (
	"strings"

	"github.com/Marstheway/oh-my-api/internal/config"
)

// MatchRule 判断单条规则的 match 是否命中；未写出的 match 字段不参与匹配。
// 原子合同下 match 至多一个条件字段（ValidateRules 强制），此处对非 nil 字段逐个判断。
func MatchRule(match config.RuleMatch, ctx MatchContext) bool {
	if match.ClientModel != nil && !matchCondition(*match.ClientModel, ctx.ClientModel) {
		return false
	}
	if match.Key != nil && !matchCondition(*match.Key, ctx.KeyName) {
		return false
	}
	if match.UpstreamModel != nil && !matchCondition(*match.UpstreamModel, ctx.UpstreamModel) {
		return false
	}
	return true
}

func matchCondition(cond config.RuleCondition, actual string) bool {
	expected := strings.TrimSpace(cond.Value)
	if expected == "" {
		return false
	}

	actual = strings.TrimSpace(actual)
	switch cond.Op {
	case "equals":
		return actual == expected
	case "startWith":
		return strings.HasPrefix(actual, expected)
	case "include":
		return strings.Contains(actual, expected)
	default:
		return false
	}
}
