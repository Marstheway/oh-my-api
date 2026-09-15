package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

var deprecatedConfigKeys = map[string]struct{}{
	"default_protocols":     {},
	"allowed_protocols":     {},
	"reasoning_effort":      {},
	"reasoning_effort_mode": {},
}

// IsDeprecatedConfigKey reports whether key is a removed provider-level field.
func IsDeprecatedConfigKey(key string) bool {
	_, ok := deprecatedConfigKeys[key]
	return ok
}

// deprecatedProviderKeys 是已移除的 provider 级字段；它们迁移到 top-level rules，
// 但 migrate-rules 不负责转换（需要手工改写），因此错误文案与旧四字段区分。
var deprecatedProviderKeys = map[string]struct{}{
	"disabled_time_ranges": {},
	"upstream_model":       {},
}

// IsDeprecatedProviderKey reports whether key is a removed provider-level field
// that must move to top-level rules (manual migration).
func IsDeprecatedProviderKey(key string) bool {
	_, ok := deprecatedProviderKeys[key]
	return ok
}

// RejectDeprecatedProviderKeys scans a YAML AST and fails if any removed
// provider-level field (disabled_time_ranges / upstream_model) is present
// under providers.<name>. These two keys are hard-rejected by path instead of
// the global key scan so that rules match keys (e.g. upstream-model) and
// rate_limit.qpm are never affected.
func RejectDeprecatedProviderKeys(root *yaml.Node) error {
	if root == nil {
		return nil
	}

	var paths []string
	collectDeprecatedProviderKeys(root, &paths)
	if len(paths) == 0 {
		return nil
	}

	return fmt.Errorf("deprecated provider fields are no longer supported (configure via top-level rules): %s", strings.Join(paths, ", "))
}

// collectDeprecatedProviderKeys 遍历 YAML AST，在 providers 映射下的每个 provider
// 配置中查找已移除的 provider 级字段。
func collectDeprecatedProviderKeys(node *yaml.Node, paths *[]string) {
	if node == nil {
		return
	}

	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			collectDeprecatedProviderKeys(child, paths)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]
			if keyNode.Value == "providers" && valueNode.Kind == yaml.MappingNode {
				collectProviderConfigKeys(valueNode, "providers", paths)
				continue
			}
			collectDeprecatedProviderKeys(valueNode, paths)
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			collectDeprecatedProviderKeys(child, paths)
		}
	}
}

// collectProviderConfigKeys 遍历 providers 映射的每个 provider 配置，收集命中的旧键路径。
func collectProviderConfigKeys(providersNode *yaml.Node, path string, paths *[]string) {
	for i := 0; i+1 < len(providersNode.Content); i += 2 {
		providerName := providersNode.Content[i].Value
		providerNode := providersNode.Content[i+1]
		if providerNode.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(providerNode.Content); j += 2 {
			key := providerNode.Content[j].Value
			if IsDeprecatedProviderKey(key) {
				*paths = append(*paths, path+"."+providerName+"."+key)
			}
		}
	}
}

// RejectDeprecatedConfigKeys scans a YAML AST and fails if any removed provider
// fields are still present anywhere in the document.
func RejectDeprecatedConfigKeys(root *yaml.Node) error {
	if root == nil {
		return nil
	}

	var paths []string
	collectDeprecatedConfigKeys(root, "", &paths)
	if len(paths) == 0 {
		return nil
	}

	return fmt.Errorf("deprecated config fields are no longer supported (use top-level rules instead; run: oh-my-api migrate-rules --write): %s", strings.Join(paths, ", "))
}

// RejectDeprecatedConfigYAML scans raw YAML bytes before typed unmarshaling.
func RejectDeprecatedConfigYAML(data []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse config yaml: %w", err)
	}
	if err := RejectDeprecatedConfigKeys(&root); err != nil {
		return err
	}
	if err := RejectDeprecatedProviderKeys(&root); err != nil {
		return err
	}
	return RejectDeprecatedCascadeKeys(&root)
}

func collectDeprecatedConfigKeys(node *yaml.Node, path string, paths *[]string) {
	if node == nil {
		return
	}

	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			collectDeprecatedConfigKeys(child, path, paths)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]
			key := keyNode.Value
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if IsDeprecatedConfigKey(key) {
				*paths = append(*paths, childPath)
			}
			collectDeprecatedConfigKeys(valueNode, childPath, paths)
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			collectDeprecatedConfigKeys(child, fmt.Sprintf("%s[%d]", path, i), paths)
		}
	}
}

// RejectDeprecatedCascadeKeys scans a YAML AST and fails if the removed
// top-level spoke field cascade.offer is present anywhere. It is hard-rejected
// by path (not a silent ignore) so historical YAML must be migrated by hand:
// hub-visible spoke entries must become public/hidden model groups or redirects.
func RejectDeprecatedCascadeKeys(root *yaml.Node) error {
	if root == nil {
		return nil
	}

	var paths []string
	collectDeprecatedCascadeKeys(root, &paths)
	if len(paths) == 0 {
		return nil
	}

	return fmt.Errorf("deprecated cascade field is no longer supported (hub callable entries are now governed by public/hidden exposure): %s", strings.Join(paths, ", "))
}

// collectDeprecatedCascadeKeys 遍历 YAML AST，在任意 cascade 映射中查找已移除的 offer 字段。
func collectDeprecatedCascadeKeys(node *yaml.Node, paths *[]string) {
	if node == nil {
		return
	}

	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			collectDeprecatedCascadeKeys(child, paths)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]
			if keyNode.Value == "cascade" && valueNode.Kind == yaml.MappingNode {
				for j := 0; j+1 < len(valueNode.Content); j += 2 {
					if valueNode.Content[j].Value == "offer" {
						*paths = append(*paths, "cascade.offer")
					}
				}
				continue
			}
			collectDeprecatedCascadeKeys(valueNode, paths)
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			collectDeprecatedCascadeKeys(child, paths)
		}
	}
}
