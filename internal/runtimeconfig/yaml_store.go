package runtimeconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Marstheway/oh-my-api/internal/config"
	"gopkg.in/yaml.v3"
)

// YamlStore 实现 YAML AST 定点写回
type YamlStore struct {
	configPath string
	rootNode   *yaml.Node
}

func NewYamlStore(configPath string) (*YamlStore, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}

	return &YamlStore{
		configPath: configPath,
		rootNode:   &root,
	}, nil
}

// findMappingNode 查找指定 key 的 mapping node
func findMappingNode(node *yaml.Node, key string) (*yaml.Node, bool) {
	if node.Kind != yaml.MappingNode {
		return nil, false
	}

	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1], true
		}
	}
	return nil, false
}

// findSequenceNode 查找指定 key 的 sequence node
func findSequenceNode(node *yaml.Node, key string) (*yaml.Node, bool) {
	return findMappingNode(node, key)
}

// UpdateModelGroup 更新或添加 model group
func (s *YamlStore) UpdateModelGroup(cfg config.ModelGroupConfig) error {
	// 确保 model_groups 存在
	modelGroupsNode, exists := findSequenceNode(s.rootNode.Content[0], "model_groups")
	if !exists {
		// 创建 model_groups sequence
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "model_groups"}
		seqNode := &yaml.Node{Kind: yaml.SequenceNode}
		s.rootNode.Content[0].Content = append(s.rootNode.Content[0].Content, keyNode, seqNode)
		modelGroupsNode = seqNode
	}

	// 查找是否已存在同名 group
	var existingIdx int = -1
	for i, item := range modelGroupsNode.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		nameNode, ok := findMappingNode(item, "name")
		if ok && nameNode.Value == cfg.Name {
			existingIdx = i
			break
		}
	}

	groupNode := s.modelGroupToYamlNode(cfg)

	if existingIdx >= 0 {
		// 替换现有
		modelGroupsNode.Content[existingIdx] = groupNode
	} else {
		// 添加新
		modelGroupsNode.Content = append(modelGroupsNode.Content, groupNode)
	}

	return nil
}

// DeleteModelGroup 删除 model group
func (s *YamlStore) DeleteModelGroup(name string) error {
	modelGroupsNode, exists := findSequenceNode(s.rootNode.Content[0], "model_groups")
	if !exists {
		return fmt.Errorf("model_groups not found")
	}

	for i, item := range modelGroupsNode.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		nameNode, ok := findMappingNode(item, "name")
		if ok && nameNode.Value == name {
			// 移除该元素
			modelGroupsNode.Content = append(modelGroupsNode.Content[:i], modelGroupsNode.Content[i+1:]...)
			return nil
		}
	}

	return fmt.Errorf("model group %q not found", name)
}

// SyncFromConfig 从 config 同步所有 model_groups 和 redirect 到 AST
func (s *YamlStore) SyncFromConfig(cfg *config.Config) {
	// 同步 model_groups
	modelGroupsNode, exists := findSequenceNode(s.rootNode.Content[0], "model_groups")
	if !exists {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "model_groups"}
		seqNode := &yaml.Node{Kind: yaml.SequenceNode}
		s.rootNode.Content[0].Content = append(s.rootNode.Content[0].Content, keyNode, seqNode)
		modelGroupsNode = seqNode
	}

	// 清空并重建
	modelGroupsNode.Content = modelGroupsNode.Content[:0]
	for _, g := range cfg.ModelGroups {
		modelGroupsNode.Content = append(modelGroupsNode.Content, s.modelGroupToYamlNode(g))
	}

	// 同步 redirect
	redirectNode, exists := findMappingNode(s.rootNode.Content[0], "redirect")
	if !exists {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "redirect"}
		mapNode := &yaml.Node{Kind: yaml.SequenceNode}
		s.rootNode.Content[0].Content = append(s.rootNode.Content[0].Content, keyNode, mapNode)
		redirectNode = mapNode
	}

	// 确保使用新格式（sequence）重写 redirect
	redirectNode.Kind = yaml.SequenceNode
	redirectNode.Content = redirectNode.Content[:0]
	for _, rc := range cfg.Redirect {
		exposure := exposureOrDefault(rc.Exposure)
		itemNode := yaml.Node{
			Kind: yaml.MappingNode,
			Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Value: "source"},
				{Kind: yaml.ScalarNode, Value: rc.Source},
				{Kind: yaml.ScalarNode, Value: "target"},
				{Kind: yaml.ScalarNode, Value: rc.Target},
				{Kind: yaml.ScalarNode, Value: "exposure"},
				{Kind: yaml.ScalarNode, Value: string(exposure)},
			},
		}
		redirectNode.Content = append(redirectNode.Content, &itemNode)
	}

	// 同步 providers（inline map 结构）
	providersNode, exists := findMappingNode(s.rootNode.Content[0], "providers")
	if !exists {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "providers"}
		mapNode := &yaml.Node{Kind: yaml.MappingNode}
		s.rootNode.Content[0].Content = append(s.rootNode.Content[0].Content, keyNode, mapNode)
		providersNode = mapNode
	}

	// providers 是 inline map，需要清空并重建
	// providersNode 本身就是 mapping，Content 是成对的 key-value nodes
	providersNode.Kind = yaml.MappingNode
	providersNode.Content = providersNode.Content[:0]
	for name, pcfg := range cfg.Providers.Items {
		keyNode, valueNode := s.providerToYamlNodes(name, pcfg)
		providersNode.Content = append(providersNode.Content, keyNode, valueNode)
	}

	// 同步 inbound.auth.keys
	inboundNode, exists := findMappingNode(s.rootNode.Content[0], "inbound")
	if !exists {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "inbound"}
		mapNode := &yaml.Node{Kind: yaml.MappingNode}
		s.rootNode.Content[0].Content = append(s.rootNode.Content[0].Content, keyNode, mapNode)
		inboundNode = mapNode
	}

	authNode, exists := findMappingNode(inboundNode, "auth")
	if !exists {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "auth"}
		mapNode := &yaml.Node{Kind: yaml.MappingNode}
		inboundNode.Content = append(inboundNode.Content, keyNode, mapNode)
		authNode = mapNode
	}

	keysNode, exists := findSequenceNode(authNode, "keys")
	if !exists {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "keys"}
		seqNode := &yaml.Node{Kind: yaml.SequenceNode}
		authNode.Content = append(authNode.Content, keyNode, seqNode)
		keysNode = seqNode
	}

	// 清空并重建
	keysNode.Kind = yaml.SequenceNode
	keysNode.Content = keysNode.Content[:0]
	for _, k := range cfg.Inbound.Auth.Keys {
		itemNode := &yaml.Node{Kind: yaml.MappingNode}
		itemNode.Content = append(itemNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "name"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: k.Name},
			&yaml.Node{Kind: yaml.ScalarNode, Value: "key"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: k.Key},
		)
		keysNode.Content = append(keysNode.Content, itemNode)
	}
}

// Save 保存到临时文件并原子替换正式文件
func (s *YamlStore) Save() error {
	data, err := yaml.Marshal(s.rootNode)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}

	// 写入临时文件
	tmpPath := s.configPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	// 原子替换
	if err := os.Rename(tmpPath, s.configPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename to config file: %w", err)
	}

	return nil
}

// modelGroupToYamlNode 将 config.ModelGroupConfig 转换为 yaml.Node
// models 只有 1 项且 weight=1 且 priority=0 时写回 model，否则写回 models
func (s *YamlStore) modelGroupToYamlNode(cfg config.ModelGroupConfig) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}

	// name
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "name"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: cfg.Name},
	)

	// mode
	if cfg.Mode != "" {
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "mode"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: cfg.Mode},
		)
	}

	// models: 单模型写 model，多模型写 models
	if len(cfg.Models) == 1 && cfg.Models[0].Weight == 1 {
		// 单模型写 model
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "model"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: cfg.Models[0].Model},
		)
	} else if len(cfg.Models) > 0 {
		// 多模型写 models
		modelsNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, e := range cfg.Models {
			// 判断是否需要展开格式
			if e.Weight == 1 {
				// 简化格式：直接写字符串
				modelsNode.Content = append(modelsNode.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Value: e.Model},
				)
			} else {
				// 展开格式：写 mapping
				entryNode := &yaml.Node{Kind: yaml.MappingNode}
				entryNode.Content = append(entryNode.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Value: "model"},
					&yaml.Node{Kind: yaml.ScalarNode, Value: e.Model},
				)
				if e.Weight != 1 {
					entryNode.Content = append(entryNode.Content,
						&yaml.Node{Kind: yaml.ScalarNode, Value: "weight"},
						&yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("%d", e.Weight)},
					)
				}
				modelsNode.Content = append(modelsNode.Content, entryNode)
			}
		}
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "models"},
			modelsNode,
		)
	}

	// exposure（始终写出显式值，默认 public）
	exposure := exposureOrDefault(cfg.Exposure)
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "exposure"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: string(exposure)},
	)

	// model_metadata
	if cfg.ModelMetadata.ContextLength != nil {
		metadataNode := &yaml.Node{Kind: yaml.MappingNode}
		metadataNode.Content = append(metadataNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "context_length"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("%d", *cfg.ModelMetadata.ContextLength)},
		)
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "model_metadata"},
			metadataNode,
		)
	}

	return node
}

// providerToYamlNode 将 ProviderConfig 转换为 YAML node（inline map 格式）
// providers 在 YAML 中是 inline map，每个 provider 是一个 key-value pair
// 返回两个节点：key node (name) 和 value node (config)
func (s *YamlStore) providerToYamlNodes(name string, cfg config.ProviderConfig) (*yaml.Node, *yaml.Node) {
	// key node: provider name
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: name}

	// value node: provider config (mapping)
	valueNode := &yaml.Node{Kind: yaml.MappingNode}

	// endpoint
	if cfg.Endpoint != "" {
		valueNode.Content = append(valueNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "endpoint"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: cfg.Endpoint},
		)
	}

	// endpoints
	if len(cfg.Endpoints) > 0 {
		endpointsNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, ep := range cfg.Endpoints {
			epNode := &yaml.Node{Kind: yaml.MappingNode}
			epNode.Content = append(epNode.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: "url"},
				&yaml.Node{Kind: yaml.ScalarNode, Value: ep.URL},
			)
			if len(ep.Protocols) > 0 {
				protocolsNode := &yaml.Node{Kind: yaml.SequenceNode}
				for _, proto := range ep.Protocols {
					protocolsNode.Content = append(protocolsNode.Content,
						&yaml.Node{Kind: yaml.ScalarNode, Value: proto},
					)
				}
				epNode.Content = append(epNode.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Value: "protocols"},
					protocolsNode,
				)
			}
			endpointsNode.Content = append(endpointsNode.Content, epNode)
		}
		valueNode.Content = append(valueNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "endpoints"},
			endpointsNode,
		)
	}

	// api_key
	if cfg.APIKey != "" {
		valueNode.Content = append(valueNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "api_key"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: cfg.APIKey},
		)
	}

	// protocols
	if len(cfg.Protocols) > 0 {
		protocolsNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, p := range cfg.Protocols {
			protocolsNode.Content = append(protocolsNode.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: p},
			)
		}
		valueNode.Content = append(valueNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "protocols"},
			protocolsNode,
		)
	}

	// rate_limit
	if cfg.RateLimit.QPM > 0 {
		rateLimitNode := &yaml.Node{Kind: yaml.MappingNode}
		rateLimitNode.Content = append(rateLimitNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "qpm"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("%d", cfg.RateLimit.QPM)},
		)
		valueNode.Content = append(valueNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "rate_limit"},
			rateLimitNode,
		)
	}

	// upstream_models
	if len(cfg.UpstreamModels) > 0 {
		upstreamModelsNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, um := range cfg.UpstreamModels {
			umNode := &yaml.Node{Kind: yaml.MappingNode}
			umNode.Content = append(umNode.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: "model"},
				&yaml.Node{Kind: yaml.ScalarNode, Value: um.Model},
			)
			if um.QPM > 0 {
				umNode.Content = append(umNode.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Value: "qpm"},
					&yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("%d", um.QPM)},
				)
			}
			if len(um.AllowedProtocols) > 0 {
				allowedNode := &yaml.Node{Kind: yaml.SequenceNode}
				for _, ap := range um.AllowedProtocols {
					allowedNode.Content = append(allowedNode.Content,
						&yaml.Node{Kind: yaml.ScalarNode, Value: ap},
					)
				}
				umNode.Content = append(umNode.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Value: "allowed_protocols"},
					allowedNode,
				)
			}
			upstreamModelsNode.Content = append(upstreamModelsNode.Content, umNode)
		}
		valueNode.Content = append(valueNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "upstream_model"},
			upstreamModelsNode,
		)
	}

	// default_protocols
	if len(cfg.DefaultProtocols) > 0 {
		defaultProtocolsNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, dp := range cfg.DefaultProtocols {
			defaultProtocolsNode.Content = append(defaultProtocolsNode.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: dp},
			)
		}
		valueNode.Content = append(valueNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "default_protocols"},
			defaultProtocolsNode,
		)
	}

	// disabled_time_ranges
	if len(cfg.DisabledTimeRanges) > 0 {
		disabledNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, dr := range cfg.DisabledTimeRanges {
			disabledNode.Content = append(disabledNode.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: dr},
			)
		}
		valueNode.Content = append(valueNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "disabled_time_ranges"},
			disabledNode,
		)
	}

	return keyNode, valueNode
}

// NormalizeProviderProtocols 在 AST 上遍历所有 provider 的协议字段，调用 resolve 替换简写为全名。
// 仅修改标量节点值，不改变 AST 结构，保留注释和格式。
// 返回 true 表示至少有一个值被替换。
func (s *YamlStore) NormalizeProviderProtocols(resolve func(string) string) bool {
	providersNode, exists := findMappingNode(s.rootNode.Content[0], "providers")
	if !exists || providersNode.Kind != yaml.MappingNode {
		return false
	}

	changed := false
	// providers 是 mapping: 每对 key-value 是一个 provider
	for i := 0; i+1 < len(providersNode.Content); i += 2 {
		providerValue := providersNode.Content[i+1]
		if providerValue.Kind != yaml.MappingNode {
			continue
		}

		// protocols
		if node, ok := findMappingNode(providerValue, "protocols"); ok && node.Kind == yaml.SequenceNode {
			for _, item := range node.Content {
				if item.Kind == yaml.ScalarNode {
					if full := resolve(item.Value); full != item.Value {
						item.Value = full
						changed = true
					}
				}
			}
		}

		// default_protocols
		if node, ok := findMappingNode(providerValue, "default_protocols"); ok && node.Kind == yaml.SequenceNode {
			for _, item := range node.Content {
				if item.Kind == yaml.ScalarNode {
					if full := resolve(item.Value); full != item.Value {
						item.Value = full
						changed = true
					}
				}
			}
		}

		// endpoints[].protocol
		if node, ok := findMappingNode(providerValue, "endpoints"); ok && node.Kind == yaml.SequenceNode {
			for _, epNode := range node.Content {
				if epNode.Kind != yaml.MappingNode {
					continue
				}
				if protoNode, ok := findMappingNode(epNode, "protocol"); ok && protoNode.Kind == yaml.ScalarNode {
					if full := resolve(protoNode.Value); full != protoNode.Value {
						protoNode.Value = full
						changed = true
					}
				}
			}
		}

		// upstream_model[].allowed_protocols
		if node, ok := findMappingNode(providerValue, "upstream_model"); ok && node.Kind == yaml.SequenceNode {
			for _, umNode := range node.Content {
				if umNode.Kind != yaml.MappingNode {
					continue
				}
				if apNode, ok := findMappingNode(umNode, "allowed_protocols"); ok && apNode.Kind == yaml.SequenceNode {
					for _, item := range apNode.Content {
						if item.Kind == yaml.ScalarNode {
							if full := resolve(item.Value); full != item.Value {
								item.Value = full
								changed = true
							}
						}
					}
				}
			}
		}
	}

	return changed
}

// GetConfigPath 返回配置文件路径
func (s *YamlStore) GetConfigPath() string {
	return s.configPath
}

// Backup 创建备份文件
func (s *YamlStore) Backup() (string, error) {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return "", fmt.Errorf("read config file: %w", err)
	}

	backupPath := s.configPath + ".bak"
	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		return "", fmt.Errorf("write backup: %w", err)
	}

	return backupPath, nil
}

// Restore 从备份恢复
func (s *YamlStore) Restore(backupPath string) error {
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("read backup file: %w", err)
	}

	if err := os.WriteFile(s.configPath, data, 0644); err != nil {
		return fmt.Errorf("restore config: %w", err)
	}

	return nil
}

// Reload 重新加载配置文件
func (s *YamlStore) Reload() error {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse config yaml: %w", err)
	}

	s.rootNode = &root
	return nil
}

// Ensure YamlStore implements the interface for Manager
var _ interface {
	SyncFromConfig(cfg *config.Config)
	Save() error
	GetConfigPath() string
} = (*YamlStore)(nil)

// LoadConfig 从文件加载 config
func LoadConfig(path string) (*config.Config, error) {
	return config.Load(path)
}

// SaveConfig 保存 config 到文件
func SaveConfig(path string, cfg *config.Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// 确保目录存在
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	// 写入临时文件并原子替换
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename to config file: %w", err)
	}

	return nil
}
