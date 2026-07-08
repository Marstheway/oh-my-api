package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/codec"
	"gopkg.in/yaml.v3"
)

// Exposure 表示模型（model_group / redirect）对外部的暴露级别。
//   - public：出现在 /v1/models，且允许外部请求直接调用。
//   - hidden：不出现在 /v1/models，但允许外部请求直接调用（兼容别名、灰度入口等）。
//   - internal：不出现在 /v1/models，且不允许外部请求直接调用，仅允许被其他 model group、redirect、smart route 等内部流程引用。
//
// 字段省略时统一按 public 处理。
type Exposure string

const (
	ExposurePublic   Exposure = "public"
	ExposureHidden   Exposure = "hidden"
	ExposureInternal Exposure = "internal"
)

// NormalizeExposure 将输入曝光值归一化；空字符串按 public 处理。
// 非法值返回错误。
func NormalizeExposure(raw string) (Exposure, error) {
	switch Exposure(raw) {
	case "", ExposurePublic:
		return ExposurePublic, nil
	case ExposureHidden:
		return ExposureHidden, nil
	case ExposureInternal:
		return ExposureInternal, nil
	default:
		return "", fmt.Errorf("invalid exposure %q: must be one of public, hidden, internal", raw)
	}
}

// UnmarshalYAML 实现 yaml.Unmarshaler，非法 exposure 在解析阶段失败。
func (e *Exposure) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	norm, err := NormalizeExposure(raw)
	if err != nil {
		return err
	}
	*e = norm
	return nil
}

// UnmarshalJSON 实现 json.Unmarshaler，非法 exposure 在解析阶段失败。
func (e *Exposure) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	norm, err := NormalizeExposure(raw)
	if err != nil {
		return err
	}
	*e = norm
	return nil
}

type Config struct {
	Server      ServerConfig       `yaml:"server"`
	Inbound     InboundConfig      `yaml:"inbound"`
	Providers   ProvidersConfig    `yaml:"providers"`
	ModelGroups []ModelGroupConfig `yaml:"model_groups"`
	Database    DatabaseConfig     `yaml:"database"`
	Redirect    RedirectConfigs    `yaml:"redirect"`
	SmartRoute  *SmartRouteConfig  `yaml:"smart_route"`
}

// RedirectConfig 定义 redirect 配置项，支持 exposure 字段
type RedirectConfig struct {
	Source   string    `yaml:"source"`             // 源名称（用户请求的模型名）
	Target   string    `yaml:"target"`             // 目标（指向的 group 或其他 redirect）
	Exposure *Exposure `yaml:"exposure,omitempty"` // 可选，默认 public
}

// RedirectConfigs 实现 YAML 双格式解析（旧 map 格式 + 新 slice 格式）
type RedirectConfigs []RedirectConfig

// UnmarshalYAML 实现 yaml.Unmarshaler 接口，支持 map 和 slice 两种格式
func (r *RedirectConfigs) UnmarshalYAML(value *yaml.Node) error {
	// 尝试解析为 slice 格式（新格式）
	var sliceFormat []RedirectConfig
	if err := value.Decode(&sliceFormat); err == nil {
		for _, item := range sliceFormat {
			if item.Source == "" {
				return fmt.Errorf("redirect item must have 'source' field")
			}
			if item.Target == "" {
				return fmt.Errorf("redirect item must have 'target' field")
			}
		}
		*r = sliceFormat
		return nil
	}

	// 尝试解析为 map 格式（旧格式）
	var mapFormat map[string]string
	if err := value.Decode(&mapFormat); err == nil {
		for source, target := range mapFormat {
			*r = append(*r, RedirectConfig{
				Source:   source,
				Target:   target,
				Exposure: nil, // 默认 public
			})
		}
		return nil
	}

	return fmt.Errorf("redirect must be either map or slice format")
}

// MarshalYAML 实现 yaml.Marshaler 接口，输出新格式（slice 格式）
func (r RedirectConfigs) MarshalYAML() (interface{}, error) {
	return []RedirectConfig(r), nil
}

// SmartRouteConfig 定义 smart route 功能的全局配置。
// smart_route 仅对显式启用的 redirect alias 生效，不是全局默认行为。
type SmartRouteConfig struct {
	Cheap         string   `yaml:"cheap"`          // 判别器模型，仅用于 judge，不承载最终请求
	Scout         string   `yaml:"scout"`          // 探路模型，用于收集第一轮对话信息
	EnabledModels []string `yaml:"enabled_models"` // 显式启用 smart route 的入口模型名列表，可为 redirect alias 或 model group
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
	Listen            string            `yaml:"listen"`
	MetricsListen     string            `yaml:"metrics_listen"` // metrics 端口，留空则不启动
	LogLevel          string            `yaml:"log_level"`
	Timeout           string            `yaml:"timeout"`             // 全局请求超时，覆盖从发起到连接关闭的整个生命周期，默认 120s
	ConnectTimeout    string            `yaml:"connect_timeout"`     // TCP + TLS 连接建立超时，默认 10s
	PrefillTimeout    string            `yaml:"prefill_timeout"`     // 流式首 token 超时，默认 30s
	StreamIdleTimeout string            `yaml:"stream_idle_timeout"` // 流式传输空闲超时，默认 60s
	HealthCheck       HealthCheckConfig `yaml:"health_check"`
	Admin             AdminConfig       `yaml:"admin"`
}

type AdminConfig struct {
	Password string `yaml:"password"`
}

// GetSessionSecret 从 password 派生 session secret。
// 派生方式：hex(SHA-256(password))，生成 64 字符十六进制字符串。
func (a *AdminConfig) GetSessionSecret() string {
	if a.Password == "" {
		return ""
	}
	// 从 password 派生稳定的 32 字节密钥
	hash := sha256.Sum256([]byte(a.Password))
	return hex.EncodeToString(hash[:])
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
	Timeout string                    `yaml:"timeout"` // deprecated: removed, only kept for validation error hint
	Items   map[string]ProviderConfig `yaml:",inline"`
}

// DisabledTimeRange 表示一个每天重复的禁用时间段，以分钟数存储。
// 使用半开区间 [Start, End)。
// Start 和 End 均为 [0, 1440] 范围内的分钟数（1440 表示 24:00）。
// 当 Start >= End 时表示跨午夜区间，如 [1380, 120) 对应 23:00-02:00。
type DisabledTimeRange struct {
	Start int
	End   int
}

// ParseTimeRange 将 "HH:MM-HH:MM" 格式字符串解析为 DisabledTimeRange。
// 支持跨午夜区间，例如 "23:00-02:00"。
func ParseTimeRange(raw string) (DisabledTimeRange, error) {
	parts := strings.SplitN(raw, "-", 2)
	if len(parts) != 2 {
		return DisabledTimeRange{}, fmt.Errorf("invalid format: %q, expected HH:MM-HH:MM", raw)
	}

	start, err := parseMinutes(strings.TrimSpace(parts[0]))
	if err != nil {
		return DisabledTimeRange{}, fmt.Errorf("invalid start time in %q: %w", raw, err)
	}
	end, err := parseMinutes(strings.TrimSpace(parts[1]))
	if err != nil {
		return DisabledTimeRange{}, fmt.Errorf("invalid end time in %q: %w", raw, err)
	}

	if start == end {
		return DisabledTimeRange{}, fmt.Errorf("start and end time must not be equal in %q", raw)
	}

	return DisabledTimeRange{Start: start, End: end}, nil
}

// parseMinutes 将 "HH:MM" 格式字符串解析为分钟数 (0-1440)。
// 支持 "24:00" 作为 1440 分钟。
func parseMinutes(raw string) (int, error) {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid time format: %q, expected HH:MM", raw)
	}

	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid hour: %q", parts[0])
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid minute: %q", parts[1])
	}

	if h < 0 || h > 24 {
		return 0, fmt.Errorf("hour must be 0-24, got %d", h)
	}
	if m < 0 || m > 59 {
		return 0, fmt.Errorf("minute must be 0-59, got %d", m)
	}

	if h == 24 && m != 0 {
		return 0, fmt.Errorf("invalid time: 24:%02d", m)
	}

	return h*60 + m, nil
}

// IsInDisabledTimeRange 判断给定时间是否命中任意一个禁用时间段。
// now 应为本地时间（即 time.Now().Local() 的返回值）。
// 时间段使用半开区间 [start, end)。
// 跨午夜区间（start >= end）按 [start, 24:00) 与 [00:00, end) 两段命中。
func IsInDisabledTimeRange(now time.Time, ranges []DisabledTimeRange) bool {
	if len(ranges) == 0 {
		return false
	}

	currentMinutes := now.Hour()*60 + now.Minute()

	for _, r := range ranges {
		if r.Start < r.End {
			// 普通区间 [start, end)
			if currentMinutes >= r.Start && currentMinutes < r.End {
				return true
			}
		} else {
			// 跨午夜区间 [start, 24:00) 或 [00:00, end)
			if currentMinutes >= r.Start || currentMinutes < r.End {
				return true
			}
		}
	}

	return false
}

type ProviderConfig struct {
	Endpoint           string                `yaml:"endpoint"`
	Endpoints          []EndpointConfig      `yaml:"endpoints"`
	APIKey             string                `yaml:"api_key"`
	Protocols          []string              `yaml:"protocols"`
	RateLimit          RateLimitConfig       `yaml:"rate_limit"`
	UpstreamModels     []UpstreamModelConfig `yaml:"upstream_model"`
	DefaultProtocols   []string              `yaml:"default_protocols"` // 上游模型默认协议，未显式设 allowed_protocols 的模型继承此值
	DisabledTimeRanges []string              `yaml:"disabled_time_ranges"`
}

type EndpointConfig struct {
	URL       string   `yaml:"url"`
	Protocols []string `yaml:"protocols,omitempty"`
}

type RateLimitConfig struct {
	QPM int `yaml:"qpm"`
}

type UpstreamModelConfig struct {
	Model            string   `yaml:"model"`
	QPM              int      `yaml:"qpm"`
	AllowedProtocols []string `yaml:"allowed_protocols"`
}

// GetEndpoint 根据入方向协议选择合适的 endpoint。
// 如果 endpoints 配置中找到匹配协议则返回对应 URL，否则 fallback 到 endpoint 字段。
func (p *ProviderConfig) GetEndpoint(inbound string) string {
	inboundFormat, err := codec.NormalizeProviderFormat(inbound)
	if err != nil {
		return p.fallbackEndpoint()
	}

	for _, ep := range p.Endpoints {
		for _, proto := range ep.Protocols {
			epFormat, parseErr := codec.NormalizeProviderFormat(proto)
			if parseErr != nil {
				continue
			}
			if epFormat == inboundFormat {
				return ep.URL
			}
		}
	}

	return p.fallbackEndpoint()
}

func (p *ProviderConfig) fallbackEndpoint() string {
	if len(p.Endpoints) > 0 {
		return p.Endpoints[0].URL
	}

	return p.Endpoint
}

func (p *ProviderConfig) SelectOutboundFormat(inbound codec.Format) (codec.Format, string, int, error) {
	return p.SelectOutboundFormatForModel(inbound, "")
}

func (p *ProviderConfig) SelectOutboundFormatForModel(inbound codec.Format, upstreamModel string) (codec.Format, string, int, error) {
	if allowed := p.allowedFormatsForModel(upstreamModel); len(allowed) > 0 {
		return codec.SelectBestFormat(allowed, inbound)
	}

	allFormats := make([]codec.Format, 0, len(p.Endpoints))

	for _, ep := range p.Endpoints {
		for _, proto := range ep.Protocols {
			format, err := codec.NormalizeProviderFormat(proto)
			if err != nil {
				continue
			}
			allFormats = append(allFormats, format)
			if format == inbound {
				return inbound, "passthrough", 0, nil
			}
		}
	}

	if len(allFormats) > 0 {
		return codec.SelectBestFormat(allFormats, inbound)
	}

	formats, err := codec.NormalizeProtocols(p.Protocols)
	if err != nil {
		return "", "", 0, err
	}
	return codec.SelectBestFormat(formats, inbound)
}

func (p *ProviderConfig) allowedFormatsForModel(upstreamModel string) []codec.Format {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return nil
	}

	reachable := p.reachableFormats()
	if len(reachable) == 0 {
		return nil
	}

	for _, modelCfg := range p.UpstreamModels {
		if strings.TrimSpace(modelCfg.Model) != upstreamModel {
			continue
		}
		rawProtocols := modelCfg.AllowedProtocols
		if len(rawProtocols) == 0 {
			if len(p.DefaultProtocols) == 0 {
				return nil
			}
			rawProtocols = p.DefaultProtocols
		}

		formats := make([]codec.Format, 0, len(rawProtocols))
		seen := make(map[codec.Format]struct{}, len(rawProtocols))
		for _, raw := range rawProtocols {
			format, err := codec.NormalizeProviderFormat(raw)
			if err != nil {
				continue
			}
			if _, ok := reachable[format]; !ok {
				continue
			}
			if _, ok := seen[format]; ok {
				continue
			}
			seen[format] = struct{}{}
			formats = append(formats, format)
		}
		return formats
	}

	return nil
}

func (p *ProviderConfig) reachableFormats() map[codec.Format]struct{} {
	formats := make(map[codec.Format]struct{})

	for _, ep := range p.Endpoints {
		for _, proto := range ep.Protocols {
			format, err := codec.NormalizeProviderFormat(proto)
			if err != nil {
				continue
			}
			formats[format] = struct{}{}
		}
	}

	if len(formats) > 0 {
		return formats
	}

	providerFormats, err := codec.NormalizeProtocols(p.Protocols)
	if err != nil {
		return nil
	}
	for _, f := range providerFormats {
		formats[f] = struct{}{}
	}
	return formats
}

// GetOutboundProtocol 返回与历史实现兼容的字符串协议。
// 新代码应优先使用 SelectOutboundFormat。
func (p *ProviderConfig) GetOutboundProtocol(inbound string) string {
	inboundFormat, err := codec.NormalizeProviderFormat(inbound)
	if err != nil {
		if len(p.Protocols) > 0 {
			return p.Protocols[0]
		}
		return ""
	}

	outbound, _, _, err := p.SelectOutboundFormat(inboundFormat)
	if err != nil {
		if len(p.Protocols) > 0 {
			return p.Protocols[0]
		}
		return ""
	}
	return string(outbound)
}

func protocolsContains(protocols []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, p := range protocols {
		if strings.ToLower(strings.TrimSpace(p)) == target {
			return true
		}
	}
	return false
}

func protocolContains(raw, target string) bool {
	return strings.EqualFold(strings.TrimSpace(raw), target)
}

// SupportsOllamaChatProtocol 返回该 provider 是否声明了 ollama.chat 协议
// （在顶层 protocols 或任意 endpoint 中）。
func (p *ProviderConfig) SupportsOllamaChatProtocol() bool {
	if protocolsContains(p.Protocols, "ollama.chat") {
		return true
	}
	for _, ep := range p.Endpoints {
		for _, proto := range ep.Protocols {
			if protocolContains(proto, "ollama.chat") {
				return true
			}
		}
	}
	return false
}

// SupportsEmbeddingProtocol 返回该 provider 是否为 embedding-only provider。
// 返回 true 表示该 provider 支持 ollama.embed 协议（在顶层 protocols 或任意 endpoint 中）。
func (p *ProviderConfig) SupportsEmbeddingProtocol() bool {
	if protocolsContains(p.Protocols, "ollama.embed") {
		return true
	}
	for _, ep := range p.Endpoints {
		for _, proto := range ep.Protocols {
			if protocolContains(proto, "ollama.embed") {
				return true
			}
		}
	}
	return false
}

// IsDisabledAt 返回 provider 在指定时间是否处于禁用时段。
// now 应为本地时间（如 time.Now().Local()）。
func (p *ProviderConfig) IsDisabledAt(now time.Time) bool {
	if len(p.DisabledTimeRanges) == 0 {
		return false
	}
	ranges := make([]DisabledTimeRange, 0, len(p.DisabledTimeRanges))
	for _, raw := range p.DisabledTimeRanges {
		r, err := ParseTimeRange(raw)
		if err != nil {
			continue // 已在启动校验阶段拦截非法值
		}
		ranges = append(ranges, r)
	}
	return IsInDisabledTimeRange(now, ranges)
}

// GetEmbeddingEndpoint 返回 embedding 请求应使用的 endpoint URL。
// 选择顺序：第一个 protocols 包含 "ollama.embed" 的 endpoint URL → fallback 到 provider.endpoint。
// 如果都没有，返回空字符串。
func (p *ProviderConfig) GetEmbeddingEndpoint() string {
	for _, ep := range p.Endpoints {
		for _, proto := range ep.Protocols {
			if proto == "ollama.embed" && ep.URL != "" {
				return ep.URL
			}
		}
	}
	return p.Endpoint
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
	Model  string `yaml:"model"`
	Weight int    `yaml:"weight"`
}

type ModelEntries []ModelEntry

func (e *ModelEntries) UnmarshalYAML(value *yaml.Node) error {
	var single string
	if err := value.Decode(&single); err == nil {
		slog.Warn("DEPRECATED: models: \"string\" format is deprecated, use model: \"string\" instead")
		*e = []ModelEntry{{Model: single, Weight: 1}}
		return nil
	}

	var multiRaw []yaml.Node
	if err := value.Decode(&multiRaw); err != nil {
		return fmt.Errorf("expected string or array of model entries")
	}

	for _, node := range multiRaw {
		var entryStr string
		if err := node.Decode(&entryStr); err == nil {
			*e = append(*e, ModelEntry{Model: entryStr, Weight: 1})
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

// ModelMetadataConfig 保存 model group 的可选元数据覆盖值。
type ModelMetadataConfig struct {
	ContextLength *int `yaml:"context_length"` // 若设置则覆盖 catalog 查询结果
}

type ModelGroupConfig struct {
	Name          string              `yaml:"name"`
	Mode          string              `yaml:"mode"`
	Timeout       string              `yaml:"timeout"`            // deprecated: removed, only kept for validation error hint
	Model         string              `yaml:"model"`              // 单模型配置
	Models        ModelEntries        `yaml:"models"`             // 多模型配置（向后兼容）
	Exposure      *Exposure           `yaml:"exposure,omitempty"` // 可选，默认 public
	ModelMetadata ModelMetadataConfig `yaml:"model_metadata"`     // 可选元数据覆盖
}

func (c *ModelGroupConfig) UnmarshalYAML(value *yaml.Node) error {
	type plain ModelGroupConfig
	if err := value.Decode((*plain)(c)); err != nil {
		return err
	}

	// 仅当原始配置同时包含 model 和 models 键时告警
	hasModel := false
	hasModels := false
	for i := 0; i+1 < len(value.Content); i += 2 {
		switch value.Content[i].Value {
		case "model":
			hasModel = true
		case "models":
			hasModels = true
		}
	}

	if hasModel && hasModels {
		slog.Warn("both 'model' and 'models' specified, 'models' takes precedence",
			"name", c.Name)
	}

	// 如果 model 非空且 models 为空，将 model 转换为 models
	if c.Model != "" && len(c.Models) == 0 {
		c.Models = ModelEntries{{Model: c.Model, Weight: 1}}
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

	// 内存级规范化协议简写（"openai" → "openai.chat" 等）。
	// 配置文件的持久化升级由 serve.go 启动时的 AST 写回负责，
	// 这里仅保证 cfg 本身不再持有简写值，避免运行时各路径出现简写。
	NormalizeProviderProtocolsInConfig(&cfg)

	return &cfg, nil
}
