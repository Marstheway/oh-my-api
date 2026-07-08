package config

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
		if normalizeStringSlice(p.DefaultProtocols) {
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
		for _, um := range p.UpstreamModels {
			if normalizeStringSlice(um.AllowedProtocols) {
				changed = true
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
