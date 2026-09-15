package config

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/codec"
)

var requiredCascadeProtocols = map[codec.Format]struct{}{
	codec.FormatOpenAIChat:        {},
	codec.FormatOpenAIResponse:    {},
	codec.FormatAnthropicMessages: {},
}

// ValidateCascade 校验 cascade 配置合法性，供 Load 与 Apply 共用。
func ValidateCascade(cfg *Config) error {
	issues := make([]ValidationIssue, 0)

	var enabledProviders []string
	for name, p := range cfg.Providers.Items {
		if p.Cascade != nil && p.Cascade.Enabled {
			enabledProviders = append(enabledProviders, name)
			issues = append(issues, validateProviderCascade(name, p)...)
		}
	}

	if len(enabledProviders) > 1 {
		issues = append(issues, ValidationIssue{
			Path:    "providers",
			Message: fmt.Sprintf("at most one cascade.enabled provider allowed, got %d: %s", len(enabledProviders), strings.Join(enabledProviders, ", ")),
		})
	}

	hasHubCascade := len(enabledProviders) > 0
	hasSpokeCascade := cfg.Cascade != nil
	if hasHubCascade && hasSpokeCascade {
		issues = append(issues, ValidationIssue{
			Path:    "cascade",
			Message: "top-level spoke cascade config and hub cascade.enabled provider are mutually exclusive",
		})
	}

	if hasSpokeCascade {
		issues = append(issues, validateSpokeCascade(cfg)...)
	}

	if len(issues) > 0 {
		return ValidationError{Issues: issues}
	}
	return nil
}

func validateProviderCascade(name string, p ProviderConfig) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	prefix := fmt.Sprintf("providers.%s", name)

	if strings.TrimSpace(p.Cascade.Token) == "" {
		issues = append(issues, ValidationIssue{
			Path:    prefix + ".cascade.token",
			Message: "must not be empty when cascade is enabled",
		})
	}

	if p.RemoteBridge != nil && p.RemoteBridge.Enabled {
		issues = append(issues, ValidationIssue{
			Path:    prefix,
			Message: "remote_bridge.enabled and cascade.enabled are mutually exclusive on the same provider",
		})
	}

	issues = append(issues, validateCascadeProtocolSet(prefix, p)...)

	return issues
}

func validateCascadeProtocolSet(prefix string, p ProviderConfig) []ValidationIssue {
	reachable := p.reachableFormats()
	if len(reachable) != len(requiredCascadeProtocols) {
		got := formatSetStrings(reachable)
		want := formatSetStrings(requiredCascadeProtocols)
		return []ValidationIssue{{
			Path:    prefix + ".protocols",
			Message: fmt.Sprintf("cascade.enabled provider must declare exactly %v, got %v", want, got),
		}}
	}
	for f := range reachable {
		if _, ok := requiredCascadeProtocols[f]; !ok {
			got := formatSetStrings(reachable)
			want := formatSetStrings(requiredCascadeProtocols)
			return []ValidationIssue{{
				Path:    prefix + ".protocols",
				Message: fmt.Sprintf("cascade.enabled provider must declare exactly %v, got %v", want, got),
			}}
		}
	}
	return nil
}

func formatSetStrings(set map[codec.Format]struct{}) []string {
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, string(f))
	}
	// stable order for error messages
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func validateSpokeCascade(cfg *Config) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	spoke := cfg.Cascade

	if strings.TrimSpace(spoke.Token) == "" {
		issues = append(issues, ValidationIssue{
			Path:    "cascade.token",
			Message: "must not be empty",
		})
	}

	if err := validateSpokeHubOrigin(spoke.Hub); err != nil {
		issues = append(issues, ValidationIssue{
			Path:    "cascade.hub",
			Message: err.Error(),
		})
	}

	if strings.TrimSpace(spoke.Peer) == "" {
		issues = append(issues, ValidationIssue{
			Path:    "cascade.peer",
			Message: "must not be empty",
		})
	} else if _, ok := cfg.Providers.Items[spoke.Peer]; !ok {
		issues = append(issues, ValidationIssue{
			Path:    "cascade.peer",
			Message: fmt.Sprintf("provider %q not found", spoke.Peer),
		})
	}

	issues = append(issues, validateSpokeCallableEntries(cfg)...)

	return issues
}

// exposureDirectCallable 表示该暴露级别是否允许外部请求直接调用（public 与 hidden）。
// 与 model.Resolver 的外部直调权限规则一致，保证配置校验与发布端名称集合不分叉。
func exposureDirectCallable(e Exposure) bool {
	return e == ExposurePublic || e == ExposureHidden
}

// ListDirectCallableNames 返回纯配置的 public/hidden 可外部直调名称集合：
// model group 名与 redirect source 名，排除 internal，名称去重且稳定排序。
// 不依赖 resolver，供 Cascade 配置校验与 model.Resolver.ListCallableEntries 共同复用。
func ListDirectCallableNames(cfg *Config) []string {
	seen := make(map[string]struct{}, len(cfg.ModelGroups)+len(cfg.Redirect))
	names := make([]string, 0, len(cfg.ModelGroups)+len(cfg.Redirect))
	add := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, g := range cfg.ModelGroups {
		if exposureDirectCallable(exposureOrPublic(g.Exposure)) {
			add(g.Name)
		}
	}
	for _, rc := range cfg.Redirect {
		if exposureDirectCallable(exposureOrPublic(rc.Exposure)) {
			add(rc.Source)
		}
	}
	sort.Strings(names)
	return names
}

// exposureOrPublic 返回配置暴露级别；字段省略时按 public 处理。
func exposureOrPublic(e *Exposure) Exposure {
	if e == nil {
		return ExposurePublic
	}
	return *e
}

// validateSpokeCallableEntries 校验启用 Spoke Cascade 时的 public/hidden 可调用
// 入口集合满足元数据快照资源上限：最多 MaxMetadataSnapshotEntries 项、每个名称
// 最多 MaxMetadataModelNameBytes UTF-8 字节。边界值允许；超限拒绝配置，保证任一
// 成功 Apply 的集合都能完整编码为元数据快照，而非在发布端才失败。
func validateSpokeCallableEntries(cfg *Config) []ValidationIssue {
	names := ListDirectCallableNames(cfg)
	if len(names) > MaxMetadataSnapshotEntries {
		return []ValidationIssue{{
			Path:    "cascade",
			Message: fmt.Sprintf("spoke cascade has %d public/hidden callable entries, max %d", len(names), MaxMetadataSnapshotEntries),
		}}
	}
	for _, name := range names {
		if n := len([]byte(name)); n > MaxMetadataModelNameBytes {
			return []ValidationIssue{{
				Path:    "cascade",
				Message: fmt.Sprintf("spoke cascade callable entry name %q is %d bytes, max %d", name, n, MaxMetadataModelNameBytes),
			}}
		}
	}
	return nil
}

func validateSpokeHubOrigin(hub string) error {
	hub = strings.TrimSpace(hub)
	if hub == "" {
		return fmt.Errorf("must not be empty")
	}

	u, err := url.Parse(hub)
	if err != nil {
		return fmt.Errorf("invalid URL: %v", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("must not contain userinfo")
	}
	if u.Host == "" {
		return fmt.Errorf("must have a host")
	}
	if u.Path != "" {
		return fmt.Errorf("must not contain a path")
	}
	if u.RawQuery != "" {
		return fmt.Errorf("must not contain query")
	}
	if u.Fragment != "" {
		return fmt.Errorf("must not contain fragment")
	}
	return nil
}

// ValidateSpokeHubOrigin exposes the spoke hub origin check so runtimeconfig's
// cascade draft endpoint can return field-level errors without duplicating logic.
func ValidateSpokeHubOrigin(hub string) error {
	return validateSpokeHubOrigin(hub)
}
