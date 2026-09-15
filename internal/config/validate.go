package config

import (
	"fmt"
	"strings"
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

// ValidateForServe 仅做最基本的配置校验，保证网关能够启动且请求可被路由：
//  1. 至少配置 1 个 provider
//  2. 每个 provider 至少有一个可用 endpoint
//  3. 至少配置 1 个 model group
//  4. model group 中引用的 provider 必须存在
//  5. adaptive 模式的 models 只能是 provider/model 叶子条目（不支持嵌套引用）
//
// 其余字段格式、协议取值、混用限制、重复检测、api_key 非空、bridge/smart_route 形态等
// 一律交给运行时兜底，不做启动前拦截。
//
// ┌──────────────────────────────────────────────────────────────────────────┐
// │ 设计原则（请勿破坏）：                                                     │
// │ 本函数只拦截「不拦就会启动即崩 / 请求必然失败」的配置，不做任何洁癖式校验。  │
// │ 每新增一个功能，默认不应该往这里加校验——除非该功能在配置非法时会导致       │
// │ panic 或 100% 不可恢复的错误。格式错误、取值越界、协议混用等问题应由       │
// │ codec / resolver / scheduler / provider client 在运行时自行处理并返回错误。 │
// │ 启动前校验的唯一目标是「能起来」，而不是「配得对」。                       │
// └──────────────────────────────────────────────────────────────────────────┘
func ValidateForServe(cfg *Config) ([]ValidationWarning, error) {
	issues := make([]ValidationIssue, 0)
	warnings := make([]ValidationWarning, 0)

	// 1. providers 非空
	if len(cfg.Providers.Items) == 0 {
		issues = append(issues, ValidationIssue{Path: "providers", Message: "at least 1 provider required"})
	} else {
		// 2. 每个 provider 至少有一个可用 endpoint（cascade.enabled 除外）
		for name, p := range cfg.Providers.Items {
			if p.Cascade != nil && p.Cascade.Enabled {
				continue
			}
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
				issues = append(issues, ValidationIssue{Path: fmt.Sprintf("providers.%s.endpoint", name), Message: "must have at least one valid endpoint"})
			}
		}
	}

	// 3. model_groups 非空
	if len(cfg.ModelGroups) == 0 {
		issues = append(issues, ValidationIssue{Path: "model_groups", Message: "at least 1 model group required"})
	} else {
		for i, g := range cfg.ModelGroups {
			// 5. adaptive 模式只允许 provider/model 叶子条目
			if g.Mode == "adaptive" {
				issues = append(issues, validateAdaptiveLeafOnly(i, g.Models)...)
			}

			// 4. 引用的 provider 必须存在
			for j, entry := range g.Models {
				if !strings.Contains(entry.Model, "/") {
					continue // 内部引用，交给 model.NewResolver 校验
				}
				parts := strings.SplitN(entry.Model, "/", 2)
				providerName := parts[0]
				if _, ok := cfg.Providers.Items[providerName]; !ok {
					issues = append(issues, ValidationIssue{
						Path:    fmt.Sprintf("model_groups[%d].models[%d]", i, j),
						Message: fmt.Sprintf("provider %q not found", providerName),
					})
				}
			}
		}
	}

	if len(issues) > 0 {
		return warnings, ValidationError{Issues: issues}
	}
	return warnings, nil
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
