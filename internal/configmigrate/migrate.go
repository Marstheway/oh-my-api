package configmigrate

import (
	"fmt"
	"os"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/yamlutil"
	"gopkg.in/yaml.v3"
)

// Result describes the outcome of a config migration.
type Result struct {
	Output  []byte
	Changed bool
	Message string
}

// Migrate converts deprecated provider protocol/effort fields into top-level rules.
// Input must be parsed independently of config.Load so old keys are not silently dropped.
func Migrate(data []byte) (Result, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Result{}, fmt.Errorf("parse config yaml: %w", err)
	}

	doc := yamlutil.DocumentMapping(&root)
	if doc == nil {
		return Result{}, fmt.Errorf("config yaml root must be a mapping")
	}

	providersNode, ok := yamlutil.MappingValue(doc, "providers")
	if !ok || providersNode.Kind != yaml.MappingNode {
		return Result{
			Message: "no providers section; config unchanged",
		}, nil
	}

	// 先扫不可迁移的 provider 字段：disabled_time_ranges 与 upstream_model[].qpm。
	// 必须在 hasDeprecatedProviderFields 的 unchanged 短路之前完成，否则会误报
	// "no deprecated provider fields found" 而静默丢掉配额/时段。
	if err := rejectNonMigratableProviderFields(providersNode); err != nil {
		return Result{}, err
	}

	if !hasDeprecatedProviderFields(providersNode) {
		return Result{
			Message: "no deprecated provider fields found; config unchanged",
		}, nil
	}

	newRules, err := buildRulesFromProviders(providersNode)
	if err != nil {
		return Result{}, err
	}

	if err := stripDeprecatedProviderFields(providersNode); err != nil {
		return Result{}, err
	}
	if err := appendRules(doc, newRules); err != nil {
		return Result{}, err
	}

	out, err := encodeDocument(&root)
	if err != nil {
		return Result{}, err
	}
	if err := validateOutput(out); err != nil {
		return Result{}, fmt.Errorf("validate migrated config: %w", err)
	}

	msg := "stripped deprecated provider fields"
	if len(newRules) > 0 {
		msg = fmt.Sprintf("migrated %d rule(s) from deprecated provider fields", len(newRules))
	}

	return Result{
		Output:  out,
		Changed: true,
		Message: msg,
	}, nil
}

// MigrateFile migrates the config file at path. When write is true, overwrites the file.
func MigrateFile(path string, write bool) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("read config file: %w", err)
	}

	result, err := Migrate(data)
	if err != nil {
		return Result{}, err
	}
	if !result.Changed || !write {
		return result, nil
	}

	if err := os.WriteFile(path, result.Output, 0o644); err != nil {
		return Result{}, fmt.Errorf("write config file: %w", err)
	}
	return result, nil
}

func hasDeprecatedProviderFields(providersNode *yaml.Node) bool {
	if providersNode == nil || providersNode.Kind != yaml.MappingNode {
		return false
	}

	for i := 0; i+1 < len(providersNode.Content); i += 2 {
		providerNode := providersNode.Content[i+1]
		if providerNode.Kind != yaml.MappingNode {
			continue
		}
		if hasDeprecatedKey(providerNode) {
			return true
		}

		upstreamNode, ok := yamlutil.MappingEntry(providerNode, "upstream_model")
		if !ok || upstreamNode.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range upstreamNode.Content {
			if item.Kind == yaml.MappingNode && hasDeprecatedKey(item) {
				return true
			}
		}
	}
	return false
}

// rejectNonMigratableProviderFields 拒绝 migrate-rules 不能转换的字段：
// providers.*.disabled_time_ranges 与 providers.*.upstream_model[].qpm。
// 它们必须手工改写成 top-level rules；静默丢弃或误报 unchanged 都会丢配额/时段。
func rejectNonMigratableProviderFields(providersNode *yaml.Node) error {
	if providersNode == nil || providersNode.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(providersNode.Content); i += 2 {
		providerName := providersNode.Content[i].Value
		providerNode := providersNode.Content[i+1]
		if providerNode.Kind != yaml.MappingNode {
			continue
		}
		if _, ok := yamlutil.MappingEntry(providerNode, "disabled_time_ranges"); ok {
			return fmt.Errorf("providers.%s.disabled_time_ranges: time ranges must be configured via top-level rules (enable_time_range / disable_time_range); migrate-rules cannot convert this field", providerName)
		}

		upstreamNode, ok := yamlutil.MappingEntry(providerNode, "upstream_model")
		if !ok || upstreamNode.Kind != yaml.SequenceNode {
			continue
		}
		for idx, item := range upstreamNode.Content {
			if item.Kind != yaml.MappingNode {
				continue
			}
			if _, ok := yamlutil.MappingEntry(item, "qpm"); ok {
				return fmt.Errorf("providers.%s.upstream_model[%d].qpm: per-model qpm must be configured via top-level rules (action qpm with upstream-model match); migrate-rules cannot convert this field", providerName, idx)
			}
		}
	}
	return nil
}

func hasDeprecatedKey(node *yaml.Node) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if config.IsDeprecatedConfigKey(node.Content[i].Value) {
			return true
		}
	}
	return false
}

func buildRulesFromProviders(providersNode *yaml.Node) ([]config.RuleConfig, error) {
	var rules []config.RuleConfig

	for i := 0; i+1 < len(providersNode.Content); i += 2 {
		providerName := providersNode.Content[i].Value
		providerNode := providersNode.Content[i+1]
		if providerNode.Kind != yaml.MappingNode {
			continue
		}

		defaultNode, hasDefault := yamlutil.MappingEntry(providerNode, "default_protocols")
		if hasDefault {
			protocols, err := yamlutil.DecodeStringSlice(defaultNode)
			if err != nil {
				return nil, fmt.Errorf("providers.%s.default_protocols: %w", providerName, err)
			}
			switch len(protocols) {
			case 0:
				// empty list: strip only, no rule
			case 1:
				rules = append(rules, config.RuleConfig{
					Match: config.RuleMatch{
						UpstreamModel: &config.RuleCondition{
							Op:    "startWith",
							Value: providerName + "/",
						},
					},
					Action: config.RuleAction{
						Protocol: config.ResolveProtocolAlias(strings.TrimSpace(protocols[0])),
					},
				})
			default:
				return nil, fmt.Errorf("providers.%s.default_protocols: expected exactly one protocol, got %d", providerName, len(protocols))
			}
		}

		upstreamNode, hasUpstream := yamlutil.MappingEntry(providerNode, "upstream_model")
		if !hasUpstream || upstreamNode.Kind != yaml.SequenceNode {
			continue
		}

		for idx, item := range upstreamNode.Content {
			if item.Kind != yaml.MappingNode {
				continue
			}
			modelName, _ := yamlutil.ScalarValue(item, "model")
			modelName = strings.TrimSpace(modelName)

			pathPrefix := fmt.Sprintf("providers.%s.upstream_model[%d]", providerName, idx)
			var protocol string
			var effort []string
			var mode string
			var err error
			itemChanged := false

			if allowedNode, ok := yamlutil.MappingEntry(item, "allowed_protocols"); ok {
				decoded, err := yamlutil.DecodeStringSlice(allowedNode)
				if err != nil {
					return nil, fmt.Errorf("%s.allowed_protocols: %w", pathPrefix, err)
				}
				switch len(decoded) {
				case 0:
					// empty list: strip only, no rule
				case 1:
					protocol = config.ResolveProtocolAlias(strings.TrimSpace(decoded[0]))
					itemChanged = true
				default:
					return nil, fmt.Errorf("%s.allowed_protocols: expected exactly one protocol, got %d", pathPrefix, len(decoded))
				}
			}

			if effortNode, ok := yamlutil.MappingEntry(item, "reasoning_effort"); ok {
				effort, err = yamlutil.DecodeStringSlice(effortNode)
				if err != nil {
					return nil, fmt.Errorf("%s.reasoning_effort: %w", pathPrefix, err)
				}
				if len(effort) > 0 {
					itemChanged = true
				}
			}

			if modeNode, ok := yamlutil.MappingEntry(item, "reasoning_effort_mode"); ok {
				mode, err = yamlutil.DecodeScalar(modeNode)
				if err != nil {
					return nil, fmt.Errorf("%s.reasoning_effort_mode: %w", pathPrefix, err)
				}
				mode = strings.ToLower(strings.TrimSpace(mode))
				if mode != "" {
					if mode != "strip" {
						return nil, fmt.Errorf("%s.reasoning_effort_mode: unsupported value %q (only strip is supported)", pathPrefix, mode)
					}
					itemChanged = true
				}
			}

			if !itemChanged {
				continue
			}
			if modelName == "" {
				return nil, fmt.Errorf("%s: deprecated fields require model name to migrate into rules", pathPrefix)
			}

			// migrate 产出按类拆条，保持顺序可预期；现网也允许同条多类 action。
			// 同一 upstream 的多类 action 按固定顺序各打一条（protocol → effort → effort_mode），
			// 每条使用独立的 upstream-model equals match（同值，不共享指针）；
			// 同类后写覆盖依赖该稳定次序。
			upstreamEquals := providerName + "/" + modelName
			newUpstreamMatch := func() config.RuleMatch {
				return config.RuleMatch{
					UpstreamModel: &config.RuleCondition{Op: "equals", Value: upstreamEquals},
				}
			}
			if protocol != "" {
				rules = append(rules, config.RuleConfig{
					Match:  newUpstreamMatch(),
					Action: config.RuleAction{Protocol: protocol},
				})
			}
			if len(effort) > 0 {
				rules = append(rules, config.RuleConfig{
					Match:  newUpstreamMatch(),
					Action: config.RuleAction{Effort: append([]string(nil), effort...)},
				})
			}
			if mode != "" {
				rules = append(rules, config.RuleConfig{
					Match:  newUpstreamMatch(),
					Action: config.RuleAction{EffortMode: mode},
				})
			}
		}
	}

	return rules, nil
}

func stripDeprecatedProviderFields(providersNode *yaml.Node) error {
	for i := 0; i+1 < len(providersNode.Content); i += 2 {
		providerNode := providersNode.Content[i+1]
		if providerNode.Kind != yaml.MappingNode {
			continue
		}
		yamlutil.RemoveMappingKey(providerNode, "default_protocols")
		// upstream_model 整段删除：protocol/effort 已迁成 rules，qpm 已被入口扫描拒绝，
		// 该列表不再有任何合法职责。
		yamlutil.RemoveMappingKey(providerNode, "upstream_model")
	}
	return nil
}

func appendRules(doc *yaml.Node, newRules []config.RuleConfig) error {
	if len(newRules) == 0 {
		return nil
	}

	rulesNode, ok := yamlutil.MappingEntry(doc, "rules")
	if !ok {
		seqNode, err := rulesToSequenceNode(newRules)
		if err != nil {
			return err
		}
		doc.Content = append(doc.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "rules"},
			seqNode,
		)
		return nil
	}

	if rulesNode.Kind != yaml.SequenceNode {
		return fmt.Errorf("rules must be a sequence")
	}

	for _, rule := range newRules {
		node, err := ruleToNode(rule)
		if err != nil {
			return err
		}
		rulesNode.Content = append(rulesNode.Content, node)
	}
	return nil
}

func rulesToSequenceNode(rules []config.RuleConfig) (*yaml.Node, error) {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, rule := range rules {
		node, err := ruleToNode(rule)
		if err != nil {
			return nil, err
		}
		seq.Content = append(seq.Content, node)
	}
	return seq, nil
}

func ruleToNode(rule config.RuleConfig) (*yaml.Node, error) {
	var encoded yaml.Node
	if err := encoded.Encode(rule); err != nil {
		return nil, fmt.Errorf("encode rule: %w", err)
	}
	switch encoded.Kind {
	case yaml.DocumentNode:
		if len(encoded.Content) == 0 {
			return nil, fmt.Errorf("encode rule: empty document")
		}
		return encoded.Content[0], nil
	case yaml.MappingNode:
		node := encoded
		return &node, nil
	default:
		return nil, fmt.Errorf("encode rule: unexpected yaml node kind %v", encoded.Kind)
	}
}

func validateOutput(data []byte) error {
	if err := config.RejectDeprecatedConfigYAML(data); err != nil {
		return err
	}

	path, err := writeTempConfig(data)
	if err != nil {
		return err
	}
	defer os.Remove(path)

	if _, err := config.Load(path); err != nil {
		return err
	}
	return nil
}

func writeTempConfig(data []byte) (string, error) {
	f, err := os.CreateTemp("", "oh-my-api-migrate-*.yaml")
	if err != nil {
		return "", fmt.Errorf("create temp config: %w", err)
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return "", fmt.Errorf("write temp config: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close temp config: %w", err)
	}
	return path, nil
}

func encodeDocument(root *yaml.Node) ([]byte, error) {
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("encode config yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close yaml encoder: %w", err)
	}
	return []byte(buf.String()), nil
}
