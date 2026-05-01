package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server      ServerConfig       `yaml:"server"`
	Inbound     InboundConfig      `yaml:"inbound"`
	Providers   ProvidersConfig    `yaml:"providers"`
	ModelGroups []ModelGroupConfig `yaml:"model_groups"`
	Database    DatabaseConfig     `yaml:"database"`
	Redirect    map[string]string  `yaml:"redirect"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// HealthCheckConfig 健康检查配置
type HealthCheckConfig struct {
	FailureThreshold int    `yaml:"failure_threshold"` // 连续失败阈值，默认 3
	Cooldown         string `yaml:"cooldown"`          // 冷却时间，默认 30s
}

type ServerConfig struct {
	Listen        string            `yaml:"listen"`
	MetricsListen string            `yaml:"metrics_listen"` // metrics 端口，留空则不启动
	LogLevel      string            `yaml:"log_level"`
	Timeout       string            `yaml:"timeout"`
	StreamTimeout string            `yaml:"stream_timeout"`
	HealthCheck   HealthCheckConfig `yaml:"health_check"`
}

type InboundConfig struct {
	Auth AuthConfig `yaml:"auth"`
}

type AuthConfig struct {
	Keys []KeyConfig `yaml:"keys"`
}

type KeyConfig struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

type ProvidersConfig struct {
	Timeout string                  `yaml:"timeout"`
	Items   map[string]ProviderConfig `yaml:",inline"`
}

type ProviderConfig struct {
	Endpoint       string                `yaml:"endpoint"`
	Endpoints      []EndpointConfig      `yaml:"endpoints"`
	APIKey         string                `yaml:"api_key"`
	Protocol       string                `yaml:"protocol"`
	RateLimit      RateLimitConfig       `yaml:"rate_limit"`
	UpstreamModels []UpstreamModelConfig `yaml:"upstream_model"`
}

type EndpointConfig struct {
	URL      string `yaml:"url"`
	Protocol string `yaml:"protocol"`
}

type RateLimitConfig struct {
	QPM int `yaml:"qpm"`
}

type UpstreamModelConfig struct {
	Model string `yaml:"model"`
	QPM   int    `yaml:"qpm"`
}

// parseProtocols 解析多协议字符串
// 格式：单协议 "openai" 或多协议 "openai/anthropic"
func parseProtocols(s string) []string {
	if strings.Contains(s, "/") {
		parts := strings.Split(s, "/")
		result := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				result = append(result, p)
			}
		}
		return result
	}
	return []string{s}
}

// GetEndpoint 根据入方向协议选择合适的 endpoint。
// 如果 endpoints 配置中找到匹配协议则返回对应 URL，否则 fallback 到 endpoint 字段。
func (p *ProviderConfig) GetEndpoint(inbound string) string {
	for _, ep := range p.Endpoints {
		if ep.Protocol == inbound {
			return ep.URL
		}
	}

	// 若配置了 endpoints 但未命中协议，则优先回退到第一个 endpoint，
	// 使多-endpoints 场景可独立于顶层 endpoint/protocol 工作。
	if len(p.Endpoints) > 0 {
		return p.Endpoints[0].URL
	}

	return p.Endpoint
}

// GetOutboundProtocol 根据入方向协议获取出方向协议。
// 如果 endpoints 配置中找到匹配协议则返回该 endpoint 的 protocol。
// 如果 protocol 字段是多协议格式（如 "openai/anthropic"），则根据入向协议选择。
// 否则返回 provider 默认 protocol。
func (p *ProviderConfig) GetOutboundProtocol(inbound string) string {
	// 优先按 endpoints 数组匹配
	for _, ep := range p.Endpoints {
		if ep.Protocol == inbound {
			return ep.Protocol
		}
	}

	// 若配置了 endpoints 但未命中协议，则优先回退到第一个 endpoint 的 protocol。
	if len(p.Endpoints) > 0 && p.Endpoints[0].Protocol != "" {
		return p.Endpoints[0].Protocol
	}

	// 检查 protocol 字段是否支持入向协议（多协议格式：openai/anthropic）
	protocols := parseProtocols(p.Protocol)
	for _, proto := range protocols {
		if proto == inbound {
			return inbound
		}
	}

	// fallback 到默认 protocol
	return p.Protocol
}

type StringSlice []string

func (s *StringSlice) UnmarshalYAML(value *yaml.Node) error {
	var single string
	if err := value.Decode(&single); err == nil {
		*s = []string{single}
		return nil
	}

	var multi []string
	if err := value.Decode(&multi); err != nil {
		return fmt.Errorf("expected string or array of strings")
	}
	*s = multi
	return nil
}

type ModelEntry struct {
	Model    string `yaml:"model"`
	Weight   int    `yaml:"weight"`
	Priority int    `yaml:"priority"`
}

type ModelEntries []ModelEntry

func (e *ModelEntries) UnmarshalYAML(value *yaml.Node) error {
	var single string
	if err := value.Decode(&single); err == nil {
		slog.Warn("DEPRECATED: models: \"string\" format is deprecated, use model: \"string\" instead")
		*e = []ModelEntry{{Model: single, Weight: 1, Priority: 0}}
		return nil
	}

	var multiRaw []yaml.Node
	if err := value.Decode(&multiRaw); err != nil {
		return fmt.Errorf("expected string or array of model entries")
	}

	for _, node := range multiRaw {
		var entryStr string
		if err := node.Decode(&entryStr); err == nil {
			*e = append(*e, ModelEntry{Model: entryStr, Weight: 1, Priority: 0})
			continue
		}

		var entry ModelEntry
		if err := node.Decode(&entry); err != nil {
			return fmt.Errorf("invalid model entry format")
		}
		*e = append(*e, entry)
	}

	return nil
}

type ModelGroupConfig struct {
	Name    string       `yaml:"name"`
	Mode    string       `yaml:"mode"`
	Timeout string       `yaml:"timeout"`
	Model   string       `yaml:"model"`              // 单模型配置
	Models  ModelEntries `yaml:"models"`             // 多模型配置（向后兼容）
	Visible *bool        `yaml:"visible"`
}

func (c *ModelGroupConfig) UnmarshalYAML(value *yaml.Node) error {
	type plain ModelGroupConfig
	if err := value.Decode((*plain)(c)); err != nil {
		return err
	}

	// 如果 model 非空且 models 为空，将 model 转换为 models
	if c.Model != "" && len(c.Models) == 0 {
		c.Models = ModelEntries{{Model: c.Model, Weight: 1, Priority: 0}}
	}

	// 如果两者都配置，输出警告
	if c.Model != "" && len(c.Models) > 0 {
		slog.Warn("both 'model' and 'models' specified, 'models' takes precedence",
			"name", c.Name)
	}

	return nil
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return &cfg, nil
}
