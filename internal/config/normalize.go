package config

import "strings"

// 协议简写映射表。历史原因，YAML 配置中可能使用 "openai"/"anthropic" 简写，
// 启动时通过此映射表一次性规范化并写回配置文件。
var protocolAliases = map[string]string{
	"openai":    "openai.chat",
	"anthropic": "anthropic.messages",
}

// ResolveProtocolAlias 将协议简写替换为全名，未知值原样返回。
func ResolveProtocolAlias(s string) string {
	if full, ok := protocolAliases[s]; ok {
		return full
	}
	return s
}

// NormalizeProviderProtocolsInConfig 对 cfg 中所有 provider 的协议字段做原地规范化。
// 返回 true 表示至少有一个值被替换。
func NormalizeProviderProtocolsInConfig(cfg *Config) bool {
	changed := false
	for name, p := range cfg.Providers.Items {
		if normalizeStringSlice(p.Protocols) {
			changed = true
		}
		for j, ep := range p.Endpoints {
			for k, proto := range ep.Protocols {
				if full := ResolveProtocolAlias(proto); full != proto {
					p.Endpoints[j].Protocols[k] = full
					changed = true
				}
			}
		}
		cfg.Providers.Items[name] = p
	}
	return changed
}

// normalizeStringSlice 对切片做原地规范化，返回是否有变化。
func normalizeStringSlice(slice []string) bool {
	changed := false
	for i, s := range slice {
		if full := ResolveProtocolAlias(s); full != s {
			slice[i] = full
			changed = true
		}
	}
	return changed
}

// normalizeReasoningEffortSlice 对 reasoning_effort 切片做原地规范化（trim + 小写 + 去重）。
// 返回是否有变化。
func normalizeReasoningEffortSlice(slice *[]string) bool {
	if len(*slice) == 0 {
		return false
	}

	seen := make(map[string]bool, len(*slice))
	var normalized []string
	changed := false

	for _, s := range *slice {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			changed = true // 空元素被删除
			continue
		}
		lower := strings.ToLower(trimmed)
		if seen[lower] {
			changed = true // 重复元素被删除
			continue
		}
		seen[lower] = true
		if lower != s {
			changed = true // 内容被修改
		}
		normalized = append(normalized, lower)
	}

	// 替换原切片
	*slice = normalized

	return changed
}
