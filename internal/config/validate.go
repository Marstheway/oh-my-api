package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/codec"
)

type ValidationIssue struct {
	Path    string
	Message string
}

type ValidationWarning struct {
	Path    string
	Message string
}

type ValidationError struct {
	Issues []ValidationIssue
}

func (e ValidationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "config validation failed (%d issues)", len(e.Issues))
	for _, issue := range e.Issues {
		fmt.Fprintf(&b, "\n- %s: %s", issue.Path, issue.Message)
	}
	return b.String()
}

func ValidateForServe(cfg *Config) ([]ValidationWarning, error) {
	issues := make([]ValidationIssue, 0)
	warnings := make([]ValidationWarning, 0)

	parseDuration := func(path, raw string) {
		if strings.TrimSpace(raw) == "" {
			return
		}
		if _, err := time.ParseDuration(raw); err != nil {
			issues = append(issues, ValidationIssue{Path: path, Message: "invalid duration: " + err.Error()})
		}
	}

	parseDuration("server.timeout", cfg.Server.Timeout)
	parseDuration("server.prefill_timeout", cfg.Server.PrefillTimeout)
	parseDuration("server.connect_timeout", cfg.Server.ConnectTimeout)
	parseDuration("server.stream_idle_timeout", cfg.Server.StreamIdleTimeout)
	parseDuration("server.health_check.cooldown", cfg.Server.HealthCheck.Cooldown)

	if strings.TrimSpace(cfg.Providers.Timeout) != "" {
		issues = append(issues, ValidationIssue{Path: "providers.timeout", Message: "field removed, use server.timeout instead"})
	}

	if cfg.Server.HealthCheck.FailureThreshold < 0 {
		issues = append(issues, ValidationIssue{Path: "server.health_check.failure_threshold", Message: "must be >= 0"})
	}

	// admin.password 校验：若配置则不能为空白字符
	if strings.TrimSpace(cfg.Server.Admin.Password) != "" && strings.TrimSpace(cfg.Server.Admin.Password) != cfg.Server.Admin.Password {
		issues = append(issues, ValidationIssue{Path: "server.admin.password", Message: "must not have leading/trailing whitespace"})
	}

	// inbound.auth.keys 校验
	if len(cfg.Inbound.Auth.Keys) == 0 {
		issues = append(issues, ValidationIssue{Path: "inbound.auth.keys", Message: "at least 1 key required"})
	} else {
		seenKeyNames := make(map[string]bool)
		for i, k := range cfg.Inbound.Auth.Keys {
			if k.Name == "" {
				issues = append(issues, ValidationIssue{Path: fmt.Sprintf("inbound.auth.keys[%d].name", i), Message: "must not be empty"})
			}
			if k.Key == "" {
				issues = append(issues, ValidationIssue{Path: fmt.Sprintf("inbound.auth.keys[%d].key", i), Message: "must not be empty"})
			}
			if k.Name != "" {
				if seenKeyNames[k.Name] {
					issues = append(issues, ValidationIssue{Path: fmt.Sprintf("inbound.auth.keys[%d].name", i), Message: "duplicate key name: " + k.Name})
				}
				seenKeyNames[k.Name] = true
			}
		}
	}

	// providers 校验
	if len(cfg.Providers.Items) == 0 {
		issues = append(issues, ValidationIssue{Path: "providers", Message: "at least 1 provider required"})
	} else {
		for name, p := range cfg.Providers.Items {
			isEmbedding := p.SupportsEmbeddingProtocol()
			isOllamaChat := p.SupportsOllamaChatProtocol()

			// ollama.chat 与 ollama.embed 同一 provider 混用：拒绝
			if isEmbedding && isOllamaChat {
				issues = append(issues, ValidationIssue{
					Path:    fmt.Sprintf("providers.%s.protocol", name),
					Message: "ollama.chat and ollama.embed cannot be mixed in the same provider; split into two provider entries",
				})
				continue
			}

			// ollama.embed provider 走独立校验路径
			if isEmbedding {
				embeddingIssues := validateEmbeddingProvider(name, p)
				issues = append(issues, embeddingIssues...)
				continue
			}

			// ollama.chat provider：允许空 key，但不能混用非 ollama.chat 协议
			if isOllamaChat {
				ollamaChatIssues := validateOllamaChatProvider(name, p)
				issues = append(issues, ollamaChatIssues...)
				continue
			}

			// API key 校验：非 ollama provider 必须非空
			if p.APIKey == "" {
				issues = append(issues, ValidationIssue{Path: fmt.Sprintf("providers.%s.api_key", name), Message: "must not be empty"})
			}

			// chat provider 协议校验
			if len(p.Protocols) > 0 {
				if _, err := codec.NormalizeProtocols(p.Protocols); err != nil {
					issues = append(issues, ValidationIssue{Path: fmt.Sprintf("providers.%s.protocols", name), Message: "invalid protocols: " + err.Error()})
				}
			}

			for i, ep := range p.Endpoints {
				for _, proto := range ep.Protocols {
					if _, err := codec.NormalizeProviderFormat(proto); err != nil {
						issues = append(issues, ValidationIssue{Path: fmt.Sprintf("providers.%s.endpoints[%d].protocols", name, i), Message: "invalid protocol: " + err.Error()})
					}
				}
			}

			reachableFormats := make(map[codec.Format]bool)
			for _, ep := range p.Endpoints {
				for _, proto := range ep.Protocols {
					format, err := codec.NormalizeProviderFormat(proto)
					if err != nil {
						continue
					}
					reachableFormats[format] = true
				}
			}
			if len(reachableFormats) == 0 && len(p.Protocols) > 0 {
				formats, err := codec.NormalizeProtocols(p.Protocols)
				if err == nil {
					for _, f := range formats {
						reachableFormats[f] = true
					}
				}
			}

			// default_protocols 校验
			for j, raw := range p.DefaultProtocols {
				format, err := codec.NormalizeProviderFormat(raw)
				if err != nil {
					issues = append(issues, ValidationIssue{
						Path:    fmt.Sprintf("providers.%s.default_protocols[%d]", name, j),
						Message: "invalid protocol: " + err.Error(),
					})
					continue
				}
				if !reachableFormats[format] {
					issues = append(issues, ValidationIssue{
						Path:    fmt.Sprintf("providers.%s.default_protocols[%d]", name, j),
						Message: fmt.Sprintf("protocol %q is not reachable by this provider endpoints", raw),
					})
				}
			}

			for i, modelCfg := range p.UpstreamModels {
				for j, raw := range modelCfg.AllowedProtocols {
					format, err := codec.NormalizeProviderFormat(raw)
					if err != nil {
						issues = append(issues, ValidationIssue{
							Path:    fmt.Sprintf("providers.%s.upstream_model[%d].allowed_protocols[%d]", name, i, j),
							Message: "invalid protocol: " + err.Error(),
						})
						continue
					}
					if !reachableFormats[format] {
						issues = append(issues, ValidationIssue{
							Path:    fmt.Sprintf("providers.%s.upstream_model[%d].allowed_protocols[%d]", name, i, j),
							Message: fmt.Sprintf("protocol %q is not reachable by this provider endpoints", raw),
						})
					}
				}
			}

			// disabled_time_ranges 校验
			for j, raw := range p.DisabledTimeRanges {
				if _, err := ParseTimeRange(raw); err != nil {
					issues = append(issues, ValidationIssue{
						Path:    fmt.Sprintf("providers.%s.disabled_time_ranges[%d]", name, j),
						Message: err.Error(),
					})
				}
			}

			// 必须有可用 endpoint
			hasEndpoint := p.Endpoint != ""
			if !hasEndpoint && len(p.Endpoints) > 0 {
				for _, ep := range p.Endpoints {
					if ep.URL != "" {
						hasEndpoint = true
						break
					}
				}
			}
			if !hasEndpoint {
				issues = append(issues, ValidationIssue{Path: fmt.Sprintf("providers.%s.endpoint", name), Message: "must have at least one valid endpoint"})
			}
		}
	}

	// model_groups 校验
	if len(cfg.ModelGroups) == 0 {
		issues = append(issues, ValidationIssue{Path: "model_groups", Message: "at least 1 model group required"})
	} else {
		seenGroupNames := make(map[string]bool)
		validModes := map[string]bool{
			"concurrent":   true,
			"load-balance": true,
			"loadbalance":  true,
			"failover":     true,
			"adaptive":     true,
		}

		for i, g := range cfg.ModelGroups {
			if strings.TrimSpace(g.Timeout) != "" {
				issues = append(issues, ValidationIssue{Path: fmt.Sprintf("model_groups[%d].timeout", i), Message: "field removed, use server.timeout instead"})
			}

			if g.Name == "" {
				issues = append(issues, ValidationIssue{Path: fmt.Sprintf("model_groups[%d].name", i), Message: "must not be empty"})
			} else {
				if seenGroupNames[g.Name] {
					issues = append(issues, ValidationIssue{Path: fmt.Sprintf("model_groups[%d].name", i), Message: "duplicate group name: " + g.Name})
				}
				seenGroupNames[g.Name] = true
			}

			if g.Mode != "" && !validModes[g.Mode] {
				issues = append(issues, ValidationIssue{Path: fmt.Sprintf("model_groups[%d].mode", i), Message: "must be one of: concurrent, load-balance, loadbalance, failover, adaptive"})
			}

			// adaptive 模式只允许 provider/model 叶子条目，拒绝任何内部引用
			if g.Mode == "adaptive" {
				issues = append(issues, validateAdaptiveLeafOnly(i, g.Models)...)
			}

			if g.ModelMetadata.ContextLength != nil && *g.ModelMetadata.ContextLength <= 0 {
				issues = append(issues, ValidationIssue{Path: fmt.Sprintf("model_groups[%d].model_metadata.context_length", i), Message: "must be greater than 0"})
			}


			validCount := 0
			for _, entry := range g.Models {
				if !strings.Contains(entry.Model, "/") {
					continue // 内部引用，不参与 provider 存在性统计
				}
				parts := strings.SplitN(entry.Model, "/", 2)
				providerName := parts[0]
				if _, ok := cfg.Providers.Items[providerName]; ok {
					validCount++
				}
			}

			// 第二遍：对含 / 的 models 条目检查 provider，内部引用跳过
			for j, entry := range g.Models {
				if !strings.Contains(entry.Model, "/") {
					continue // 内部引用，交给 model.NewResolver 校验
				}
				parts := strings.SplitN(entry.Model, "/", 2)
				providerName := parts[0]
				if _, ok := cfg.Providers.Items[providerName]; !ok {
					path := fmt.Sprintf("model_groups[%d].models[%d]", i, j)
					msg := fmt.Sprintf("provider %q not found", providerName)
					if validCount > 0 {
						warnings = append(warnings, ValidationWarning{Path: path, Message: msg})
					} else {
						issues = append(issues, ValidationIssue{Path: path, Message: msg})
					}
				}
			}
		}
	}

	// smart_route 校验：若未配置则跳过；若配置了则校验字段形状与 alias 存在性
	if cfg.SmartRoute != nil {
		sr := cfg.SmartRoute

		// cheap/scout 两个目标引用必须全部非空且不含 /
		for _, field := range []struct {
			name  string
			value string
			path  string
		}{
			{"cheap", sr.Cheap, "smart_route.cheap"},
			{"scout", sr.Scout, "smart_route.scout"},
		} {
			if strings.TrimSpace(field.value) == "" {
				issues = append(issues, ValidationIssue{
					Path:    field.path,
					Message: fmt.Sprintf("%s must not be empty when smart_route is configured", field.name),
				})
			} else if strings.Contains(field.value, "/") {
				issues = append(issues, ValidationIssue{
					Path:    field.path,
					Message: fmt.Sprintf("%s must not contain '/' (must be internal name, not provider/model)", field.name),
				})
			}
		}

		// enabled_models 中每一项都必须是内部引用名，不能是 provider/model
		for i, name := range sr.EnabledModels {
			if strings.TrimSpace(name) == "" {
				issues = append(issues, ValidationIssue{
					Path:    fmt.Sprintf("smart_route.enabled_models[%d]", i),
					Message: "must not be empty string",
				})
				continue
			}
			if strings.Contains(name, "/") {
				issues = append(issues, ValidationIssue{
					Path:    fmt.Sprintf("smart_route.enabled_models[%d]", i),
					Message: "must not contain '/' (must be internal name, not provider/model)",
				})
			}
		}
	}

	if len(issues) > 0 {
		return warnings, ValidationError{Issues: issues}
	}
	return warnings, nil
}

// validateOllamaChatProvider 对 ollama.chat provider 进行校验。
// 规则：允许空 api_key，但不能混用非 ollama.chat 协议。
func validateOllamaChatProvider(name string, p ProviderConfig) []ValidationIssue {
	issues := make([]ValidationIssue, 0)

	validateProtocolSpec := func(path string, protocols []string) {
		for _, raw := range protocols {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			switch strings.ToLower(raw) {
			case "ollama.chat":
				continue
			case "ollama.embed":
				issues = append(issues, ValidationIssue{
					Path:    path,
					Message: "ollama.chat provider must not mix with other protocols; split into separate provider entries",
				})
			default:
				if _, err := codec.NormalizeProviderFormat(raw); err == nil {
					issues = append(issues, ValidationIssue{
						Path:    path,
						Message: "ollama.chat provider must not mix with other protocols; split into separate provider entries",
					})
					continue
				}
				issues = append(issues, ValidationIssue{
					Path:    path,
					Message: "invalid protocol: unknown provider protocol: " + raw,
				})
			}
		}
	}

	if len(p.Protocols) > 0 {
		validateProtocolSpec(fmt.Sprintf("providers.%s.protocols", name), p.Protocols)
	}

	for i, ep := range p.Endpoints {
		if len(ep.Protocols) > 0 {
			validateProtocolSpec(fmt.Sprintf("providers.%s.endpoints[%d].protocols", name, i), ep.Protocols)
		}
	}

	// 必须有可用 endpoint
	hasEndpoint := p.Endpoint != ""
	if !hasEndpoint {
		for _, ep := range p.Endpoints {
			if ep.URL != "" {
				hasEndpoint = true
				break
			}
		}
	}
	if !hasEndpoint {
		issues = append(issues, ValidationIssue{
			Path:    fmt.Sprintf("providers.%s.endpoint", name),
			Message: "must have at least one valid endpoint",
		})
	}

	// disabled_time_ranges 校验
	for j, raw := range p.DisabledTimeRanges {
		if _, err := ParseTimeRange(raw); err != nil {
			issues = append(issues, ValidationIssue{
				Path:    fmt.Sprintf("providers.%s.disabled_time_ranges[%d]", name, j),
				Message: err.Error(),
			})
		}
	}

	return issues
}

// embedding provider 不能混合 ollama.embed 和任何 chat 协议。
func validateEmbeddingProvider(name string, p ProviderConfig) []ValidationIssue {
	issues := make([]ValidationIssue, 0)

	hasOllamaEmbed := false
	hasChatProtocol := false

	for _, proto := range p.Protocols {
		switch proto {
		case "ollama.embed":
			hasOllamaEmbed = true
		default:
			if _, err := codec.NormalizeProviderFormat(proto); err == nil {
				hasChatProtocol = true
			} else {
				issues = append(issues, ValidationIssue{
					Path:    fmt.Sprintf("providers.%s.protocols", name),
					Message: "invalid protocol: " + err.Error(),
				})
			}
		}
	}

	for i, ep := range p.Endpoints {
		for _, proto := range ep.Protocols {
			if proto == "" {
				continue
			}

			switch proto {
			case "ollama.embed":
				hasOllamaEmbed = true
			default:
				if _, err := codec.NormalizeProviderFormat(proto); err == nil {
					hasChatProtocol = true
				} else {
					issues = append(issues, ValidationIssue{
						Path:    fmt.Sprintf("providers.%s.endpoints[%d].protocols", name, i),
						Message: "invalid protocol: " + err.Error(),
					})
				}
			}
		}
	}

	if hasOllamaEmbed && hasChatProtocol {
		issues = append(issues, ValidationIssue{
			Path:    fmt.Sprintf("providers.%s.protocols", name),
			Message: "ollama.embed cannot be mixed with chat protocols (openai, anthropic, etc.)",
		})
	}

	if p.GetEmbeddingEndpoint() == "" {
		issues = append(issues, ValidationIssue{
			Path:    fmt.Sprintf("providers.%s.endpoint", name),
			Message: "must have at least one valid embedding endpoint",
		})
	}

	// disabled_time_ranges 校验
	for j, raw := range p.DisabledTimeRanges {
		if _, err := ParseTimeRange(raw); err != nil {
			issues = append(issues, ValidationIssue{
				Path:    fmt.Sprintf("providers.%s.disabled_time_ranges[%d]", name, j),
				Message: err.Error(),
			})
		}
	}

	return issues
}

// validateAdaptiveLeafOnly 校验 adaptive 模式的 models[] 只包含 provider/model 叶子条目，
// 拒绝任何不含 "/" 的内部引用（无论最终解析成 group 还是 redirect）。
func validateAdaptiveLeafOnly(groupIndex int, models ModelEntries) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	for j, entry := range models {
		if !strings.Contains(entry.Model, "/") {
			issues = append(issues, ValidationIssue{
				Path:    fmt.Sprintf("model_groups[%d].models[%d]", groupIndex, j),
				Message: "adaptive mode requires direct provider/model entries only (must contain '/'), internal references are not allowed",
			})
		}
	}
	return issues
}
