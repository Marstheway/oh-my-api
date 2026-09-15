package config

import (
	"strings"
)

// ReasoningEffortAction 表示对 reasoning_effort 的处理动作
type ReasoningEffortAction int

const (
	// ActionPassthrough 不做修改，保持原值
	ActionPassthrough ReasoningEffortAction = iota
	// ActionReplace 替换为钳制后的值
	ActionReplace
)

// effortRank 定义 effort 档位的全序排名
// 低 rank 表示低档位，高 rank 表示高档位
// "none" 表示思考关闭，rank 最低（低于所有思考档位）
var effortRank = map[string]int{
	"none":   -1,
	"low":    0,
	"medium": 1,
	"high":   2,
	"xhigh":  3,
	"max":    4,
}

// ApplyReasoningEffort 根据 allowed 配置对 value 进行钳制。
// 参数：
//   - value: client 传入的 effort 值（可能含空格、大小写混合）
//   - allowed: 配置的允许集（已规范化为小写、去重）
//
// 返回：
//   - result: 处理后的结果值
//   - action: 处理动作（Passthrough/Replace）
//
// 行为规则：
//   - allowed 为空：Passthrough，不做任何修改
//   - allowed 为档位集：value 非空时 Replace 为钳制结果，value 为空时 Passthrough
func ApplyReasoningEffort(value string, allowed []string) (result string, action ReasoningEffortAction) {
	// 未配置：透传
	if len(allowed) == 0 {
		return value, ActionPassthrough
	}

	// 规范化 client 传入的值
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		// client 未传：不注入
		return "", ActionPassthrough
	}
	normalized := strings.ToLower(trimmed)

	// 允许集模式：钳制
	// 检查是否在允许集中
	for _, a := range allowed {
		if a == normalized {
			// 精确匹配：保持规范小写
			return normalized, ActionReplace
		}
	}

	// 不在允许集中：钳制
	clamped := clampEffort(normalized, allowed)
	return clamped, ActionReplace
}

// clampEffort 将 value 钳制到 allowed 中最近的档位。
// 钳制规则：
//   - 计算每个 allowed 档位与 value 的 rank 距离
//   - 选择距离最小的档位
//   - 并列时选择 rank 更高的档位
//   - 未知字符串的 rank = 5（max+1），保证未知串被钳制到允许集的最高档
func clampEffort(value string, allowed []string) string {
	valueRank, valueKnown := effortRank[value]
	if !valueKnown {
		// 未知字符串 rank = max+1
		valueRank = 5
	}

	var bestEffort string
	bestDistance := 100 // 足够大的初始值
	bestRank := -1

	for _, a := range allowed {
		allowedRank, ok := effortRank[a]
		if !ok {
			// allowed 中不应出现未知值（已由校验保证）
			continue
		}

		distance := abs(valueRank - allowedRank)

		// 选择距离更小的，或距离相等但 rank 更高的
		if distance < bestDistance || (distance == bestDistance && allowedRank > bestRank) {
			bestDistance = distance
			bestRank = allowedRank
			bestEffort = a
		}
	}

	if bestEffort == "" {
		// 兜底：返回最后一个允许的档位（理论上不会走到这里）
		return allowed[len(allowed)-1]
	}

	return bestEffort
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
