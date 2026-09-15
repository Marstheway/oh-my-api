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
	Server      ServerConfig        `yaml:"server"`
	Inbound     InboundConfig       `yaml:"inbound"`
	Providers   ProvidersConfig     `yaml:"providers"`
	ModelGroups []ModelGroupConfig  `yaml:"model_groups"`
	Database    DatabaseConfig      `yaml:"database"`
	Redirect    RedirectConfigs     `yaml:"redirect"`
	SmartRoute  *SmartRouteConfig   `yaml:"smart_route"`
	Rules       []RuleConfig        `yaml:"rules"`
	Cascade     *SpokeCascadeConfig `yaml:"cascade,omitempty"`
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
	PrefillTimeout    string            `yaml:"prefill_timeout"`     // 流式单 attempt 预算（等 header + 首 token），默认 30s
	NonStreamTimeout  string            `yaml:"non_stream_timeout"`  // 非流式单 attempt 预算（等 header/生成），默认 120s
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
	Endpoint     string                 `yaml:"endpoint"`
	Endpoints    []EndpointConfig       `yaml:"endpoints"`
	APIKey       string                 `yaml:"api_key"`
	Protocols    []string               `yaml:"protocols"`
	RateLimit    RateLimitConfig        `yaml:"rate_limit"`
	RemoteBridge *RemoteBridgeConfig    `yaml:"remote_bridge,omitempty"`
	Cascade      *ProviderCascadeConfig `yaml:"cascade,omitempty"`
}

// ProviderCascadeConfig 表示 hub 侧 cascade provider 配置。
// 当 enabled 为 true 时，该 provider 通过 WSS 会话接收 job，本地不拨号。
type ProviderCascadeConfig struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token"`
}

// HTTPDialable reports whether this provider has a dialable HTTP endpoint.
// cascade.enabled providers are session-backed and must not be probed or dialed.
func (p ProviderConfig) HTTPDialable() bool {
	return p.Cascade == nil || !p.Cascade.Enabled
}

// SpokeCascadeConfig 表示 spoke 侧顶层 cascade 出站配置。
// 历史 `offer` 白名单已移除（由 public/hidden exposure 元数据同步取代），
// 通过 RejectDeprecatedCascadeKeys 在加载与 Apply 时按路径硬拒绝。
type SpokeCascadeConfig struct {
	Hub   string `yaml:"hub"`
	Token string `yaml:"token"`
	Peer  string `yaml:"peer"`
}

// 元数据快照协议资源上限。启用顶层 Spoke Cascade 时，public/hidden 可调用入口
// 集合（名称去重）必须满足同样限制，否则配置可能在元数据同步阶段失败；
// 该上限同时用于 metadata frame 的全帧语义校验（见 internal/cascade）。
// 只限入口数与名称字节长度，不限制 WebSocket 消息大小，避免影响既有
// job/result/cancel frame 的大小语义。
const (
	MaxMetadataSnapshotEntries = 1000
	MaxMetadataModelNameBytes  = 256
)

// RemoteBridgeConfig 表示远程桥接 provider 配置。
// 当 enabled 为 true 时，该 provider 通过远程 bridge 服务代理请求，
// 本地不持有上游的真实 API 凭证。
type RemoteBridgeConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Provider string `yaml:"provider"` // bridge 本地 provider 类型，第一版仅允许 xai-oauth
	Token    string `yaml:"token"`    // bridge 认证 token
	Local    bool   `yaml:"local"`    // 同容器 loopback bridge，显式跳过 bearer 鉴权
}

type EndpointConfig struct {
	URL       string   `yaml:"url"`
	Protocols []string `yaml:"protocols,omitempty"`
}

type RateLimitConfig struct {
	QPM int `yaml:"qpm"`
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

// ProtocolReachable 判断 rawProtocol 规范化后是否落在 provider 可达协议内。
func (p *ProviderConfig) ProtocolReachable(rawProtocol string) bool {
	return len(p.filterReachableFormats([]string{rawProtocol})) > 0
}

// filterReachableFormats 将原始协议字符串规范化并与 provider 可达协议求交。
func (p *ProviderConfig) filterReachableFormats(rawProtocols []string) []codec.Format {
	reachable := p.reachableFormats()
	if len(reachable) == 0 {
		return nil
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

// SupportsEmbeddingProtocol 返回该 provider 是否支持 embedding 协议。
// 返回 true 表示该 provider 支持 ollama.embed 或 openai.embeddings 协议（在顶层 protocols 或任意 endpoint 中）。
func (p *ProviderConfig) SupportsEmbeddingProtocol() bool {
	if protocolsContains(p.Protocols, "ollama.embed") || protocolsContains(p.Protocols, "openai.embeddings") {
		return true
	}
	for _, ep := range p.Endpoints {
		for _, proto := range ep.Protocols {
			if protocolContains(proto, "ollama.embed") || protocolContains(proto, "openai.embeddings") {
				return true
			}
		}
	}
	return false
}

// GetEmbeddingProtocol 返回该 provider 唯一的 embedding 协议（规范化小写字符串）。
// 若同时存在两种 embedding 协议、或一种都没有，返回 error。
func (p *ProviderConfig) GetEmbeddingProtocol() (string, error) {
	var found []string

	// 检查顶层 protocols
	for _, proto := range p.Protocols {
		proto = strings.ToLower(strings.TrimSpace(proto))
		if proto == "ollama.embed" || proto == "openai.embeddings" {
			found = append(found, proto)
		}
	}

	// 检查 endpoints
	for _, ep := range p.Endpoints {
		for _, proto := range ep.Protocols {
			proto = strings.ToLower(strings.TrimSpace(proto))
			if proto == "ollama.embed" || proto == "openai.embeddings" {
				found = append(found, proto)
			}
		}
	}

	// 去重
	unique := make(map[string]bool)
	var result []string
	for _, p := range found {
		if !unique[p] {
			unique[p] = true
			result = append(result, p)
		}
	}

	if len(result) == 0 {
		return "", fmt.Errorf("no embedding protocol found")
	}
	if len(result) > 1 {
		return "", fmt.Errorf("multiple embedding protocols found: %v", result)
	}
	return result[0], nil
}

// GetEmbeddingEndpointByProtocol 选择第一个 endpoints[].protocols 含该 protocol 的 endpoint URL（匹配大小写不敏感）。
// 若无匹配则 fallback 顶层 endpoint；无可用 URL 时返回空串。
func (p *ProviderConfig) GetEmbeddingEndpointByProtocol(protocol string) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))

	// 先在 endpoints 中查找
	for _, ep := range p.Endpoints {
		for _, proto := range ep.Protocols {
			if strings.ToLower(strings.TrimSpace(proto)) == protocol && ep.URL != "" {
				return ep.URL
			}
		}
	}

	// fallback 到顶层 endpoint
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

// StickyConfig 定义 load-balance 模式的可选 sticky session 配置。
type StickyConfig struct {
	Enabled     bool   `yaml:"enabled"`                // 是否启用 sticky session
	IdleTimeout string `yaml:"idle_timeout,omitempty"` // 空闲超时时间，默认 10m
}

// ModelGroupConfig 定义模型组的完整配置。
// 新增字段时需同步更新以下位置，遗漏会导致功能不完整：
//   - runtimeconfig/yaml_store.go: modelGroupToYamlNode (YAML 序列化)
//   - runtimeconfig/types.go: NormalizeInput / ToOutput (API 输入/输出转换)
//   - runtimeconfig/model_group_crud.go: ValidateCreate / ValidateUpdate (校验)
//   - runtimeconfig/yaml_store_test.go: TestModelGroupToYamlNode_ExhaustiveKeys (穷举测试)
type ModelGroupConfig struct {
	Name          string              `yaml:"name"`
	Mode          string              `yaml:"mode"`
	Timeout       string              `yaml:"timeout"`            // deprecated: removed, only kept for validation error hint
	Model         string              `yaml:"model"`              // 单模型配置
	Models        ModelEntries        `yaml:"models"`             // 多模型配置（向后兼容）
	Exposure      *Exposure           `yaml:"exposure,omitempty"` // 可选，默认 public
	ModelMetadata ModelMetadataConfig `yaml:"model_metadata"`     // 可选元数据覆盖
	Sticky        *StickyConfig       `yaml:"sticky,omitempty"`   // 可选 sticky session 配置（仅 load-balance 模式）
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

	if err := RejectDeprecatedConfigYAML(data); err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// 内存级规范化协议简写（"openai" → "openai.chat" 等）。
	// 配置文件的持久化升级由 serve.go 启动时的 AST 写回负责，
	// 这里仅保证 cfg 本身不再持有简写值，避免运行时各路径出现简写。
	NormalizeProviderProtocolsInConfig(&cfg)
	NormalizeRulesInConfig(&cfg)

	if err := ValidateRules(cfg.Rules); err != nil {
		return nil, fmt.Errorf("validate rules: %w", err)
	}

	if err := ValidateCascade(&cfg); err != nil {
		return nil, fmt.Errorf("validate cascade: %w", err)
	}

	return &cfg, nil
}
