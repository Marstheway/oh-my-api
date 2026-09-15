package runtimeconfig

import (
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
)

// ErrorCode 表示操作失败的错误码
type ErrorCode string

const (
	ErrCodeBadRequest ErrorCode = "bad_request"
	ErrCodeNotFound   ErrorCode = "not_found"
	ErrCodeConflict   ErrorCode = "conflict"
	ErrCodeValidation ErrorCode = "validation_error"
	ErrCodeInternal   ErrorCode = "internal_error"
)

// Error 表示操作失败详情
type Error struct {
	Code    ErrorCode
	Message string
	Field   string // 可选：指向具体字段
}

func (e *Error) Error() string {
	if e.Field != "" {
		return e.Field + ": " + e.Message
	}
	return e.Message
}

// StickyInput 表示 sticky 配置输入
type StickyInput struct {
	Enabled     bool   `json:"enabled"`
	IdleTimeout string `json:"idle_timeout,omitempty"` // 空闲超时时间，默认 10m
}

// StickyOutput 表示 sticky 配置输出
type StickyOutput struct {
	Enabled     bool   `json:"enabled"`
	IdleTimeout string `json:"idle_timeout,omitempty"`
}

// ModelGroupInput 表示 API 输入的 model group 数据
// API 输入允许 model 或 models 二选一
type ModelGroupInput struct {
	Name          string              `json:"name"`
	Mode          string              `json:"mode,omitempty"`
	Model         string              `json:"model,omitempty"`  // 单模型输入
	Models        []ModelEntryInput   `json:"models,omitempty"` // 多模型输入
	Exposure      *string             `json:"exposure,omitempty"`
	ModelMetadata *ModelMetadataInput `json:"model_metadata,omitempty"`
	Sticky        *StickyInput        `json:"sticky,omitempty"` // sticky 配置（仅 load-balance 模式）
}

// ModelEntryInput 表示 models 数组条目输入
type ModelEntryInput struct {
	Model  string `json:"model"`
	Weight *int   `json:"weight,omitempty"`
}

// ModelMetadataInput 表示 model_metadata 输入
type ModelMetadataInput struct {
	ContextLength *int `json:"context_length,omitempty"`
}

// ModelGroupOutput 表示 API 输出的 model group 数据
// API 输出统一返回 models 数组形态
type ModelGroupOutput struct {
	Name          string               `json:"name"`
	Mode          string               `json:"mode,omitempty"`
	Models        []ModelEntryOutput   `json:"models"`
	Exposure      config.Exposure      `json:"exposure"`
	ModelMetadata *ModelMetadataOutput `json:"model_metadata,omitempty"`
	Sticky        *StickyOutput        `json:"sticky,omitempty"`
}

// ModelEntryOutput 表示 models 数组条目输出
type ModelEntryOutput struct {
	Model  string `json:"model"`
	Weight int    `json:"weight"`
}

// ModelMetadataOutput 表示 model_metadata 输出
type ModelMetadataOutput struct {
	ContextLength         *int `json:"context_length,omitempty"`
	ComputedContextLength *int `json:"computed_context_length,omitempty"` // 计算值（从 catalog/子 group 递归得出）
}

// NormalizeInput 将输入归一化为 config.ModelGroupConfig
// 若输入 model 非空，归一为单条 models（weight=1, priority=0）
func (input *ModelGroupInput) NormalizeInput() config.ModelGroupConfig {
	mode := input.Mode
	if mode == "" {
		mode = "failover"
	}
	cfg := config.ModelGroupConfig{
		Name:     input.Name,
		Mode:     mode,
		Exposure: normalizeExposurePtr(input.Exposure),
	}

	if input.ModelMetadata != nil {
		cfg.ModelMetadata = config.ModelMetadataConfig{
			ContextLength: input.ModelMetadata.ContextLength,
		}
	}

	// 归一化：model 或 models → models
	if input.Model != "" {
		cfg.Models = config.ModelEntries{{Model: input.Model, Weight: 1}}
	} else {
		cfg.Models = make(config.ModelEntries, len(input.Models))
		for i, e := range input.Models {
			weight := 1
			if e.Weight != nil {
				weight = *e.Weight
			}
			cfg.Models[i] = config.ModelEntry{
				Model:  e.Model,
				Weight: weight,
			}
		}
	}

	// 处理 sticky
	if input.Sticky != nil {
		cfg.Sticky = &config.StickyConfig{
			Enabled:     input.Sticky.Enabled,
			IdleTimeout: input.Sticky.IdleTimeout,
		}
	}

	return cfg
}

// ToOutput 将 config.ModelGroupConfig 转换为输出格式
// 输出统一返回 models 数组形态
func ToOutput(cfg config.ModelGroupConfig) ModelGroupOutput {
	models := make([]ModelEntryOutput, len(cfg.Models))
	for i, e := range cfg.Models {
		models[i] = ModelEntryOutput{
			Model:  e.Model,
			Weight: e.Weight,
		}
	}

	output := ModelGroupOutput{
		Name:   cfg.Name,
		Mode:   cfg.Mode,
		Models: models,
	}

	output.Exposure = exposureOrDefault(cfg.Exposure)

	if cfg.ModelMetadata.ContextLength != nil {
		output.ModelMetadata = &ModelMetadataOutput{
			ContextLength: cfg.ModelMetadata.ContextLength,
		}
	}

	// 处理 sticky
	if cfg.Sticky != nil {
		output.Sticky = &StickyOutput{
			Enabled:     cfg.Sticky.Enabled,
			IdleTimeout: cfg.Sticky.IdleTimeout,
		}
	}

	return output
}

// RedirectInput 表示 API 输入的 redirect 数据
type RedirectInput struct {
	Source   string  `json:"source"`             // 源名称（用户请求的模型名）
	Target   string  `json:"target"`             // 目标 group 或其他 redirect
	Exposure *string `json:"exposure,omitempty"` // 暴露级别（可选，默认 public）
}

// RedirectOutput 表示 API 输出的 redirect 数据
type RedirectOutput struct {
	Source   string          `json:"source"`   // 源名称
	Target   string          `json:"target"`   // 目标
	Exposure config.Exposure `json:"exposure"` // 暴露级别（显式值）
}

// RedirectListOutput 表示列表输出的 redirect（带解析信息）
type RedirectListOutput struct {
	Source        string          `json:"source"`                   // 源名称
	Target        string          `json:"target"`                   // 配置的直接目标
	ResolvedGroup string          `json:"resolved_group"`           // 最终解析到的 group
	ChainLength   int             `json:"chain_length"`             // 链路长度（0=直达 group）
	Exposure      config.Exposure `json:"exposure"`                 // 暴露级别（显式值）
	ContextLength *int            `json:"context_length,omitempty"` // 最终 group 的 context_length
}

// exposureOrDefault 将 *config.Exposure 归一为显式值，nil 按 public 处理。
func exposureOrDefault(e *config.Exposure) config.Exposure {
	if e == nil {
		return config.ExposurePublic
	}
	return *e
}

// normalizeExposurePtr 将输入的 exposure（可能 nil=省略）归一为 *config.Exposure。
// 调用前必须由 CRUD 的 ValidateCreate/ValidateUpdate 先行校验；若非法值绕过了校验走到这里，
// 说明内部不变量被破坏，直接 panic 暴露问题，而不是静默回退为 public。
func normalizeExposurePtr(raw *string) *config.Exposure {
	if raw == nil {
		return nil
	}
	norm, err := config.NormalizeExposure(*raw)
	if err != nil {
		panic("normalizeExposurePtr: invalid exposure reached normalization without prior validation: " + err.Error())
	}
	return &norm
}

// AuthKeyInput 表示 API 输入的 auth key 数据
type AuthKeyInput struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// AuthKeyOutput 表示 API 输出的 auth key 数据（完整 key，不脱敏）
type AuthKeyOutput struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// RemoteBridgeInput 表示 API 输入的 remote bridge 配置
type RemoteBridgeInput struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider,omitempty"`
	Token    string `json:"token,omitempty"`
	Local    bool   `json:"local,omitempty"`
}

// CascadeInput 表示 API 输入的 cascade 配置
type CascadeInput struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token,omitempty"`
}

// ProviderInput 表示 API 输入的 provider 数据
type ProviderInput struct {
	Name         string             `json:"name"`
	Endpoint     string             `json:"endpoint,omitempty"`
	Endpoints    []EndpointInput    `json:"endpoints,omitempty"`
	APIKey       string             `json:"api_key,omitempty"`
	Protocols    []string           `json:"protocols,omitempty"`
	RateLimit    RateLimitInput     `json:"rate_limit,omitempty"`
	RemoteBridge *RemoteBridgeInput `json:"remote_bridge,omitempty"`
	Cascade      *CascadeInput      `json:"cascade,omitempty"`
}

// EndpointInput 表示 endpoints 数组条目输入
type EndpointInput struct {
	URL       string   `json:"url"`
	Protocols []string `json:"protocols,omitempty"`
}

// RateLimitInput 表示 rate_limit 输入
type RateLimitInput struct {
	QPM int `json:"qpm,omitempty"`
}

// RemoteBridgeOutput 表示 API 输出的 remote bridge 配置
type RemoteBridgeOutput struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider,omitempty"`
	Token    string `json:"token,omitempty"`
	Local    bool   `json:"local,omitempty"`
}

// CascadeOutput 表示 API 输出的 cascade 配置
type CascadeOutput struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token,omitempty"`
}

// ProviderOutput 表示 API 输出的 provider 数据（完整配置，含 API Key）
type ProviderOutput struct {
	Name         string              `json:"name"`
	Endpoints    []EndpointOutput    `json:"endpoints"`
	APIKey       string              `json:"api_key,omitempty"`
	Protocols    []string            `json:"protocols,omitempty"`
	RateLimit    RateLimitOutput     `json:"rate_limit,omitempty"`
	RemoteBridge *RemoteBridgeOutput `json:"remote_bridge,omitempty"`
	Cascade      *CascadeOutput      `json:"cascade,omitempty"`
}

// EndpointOutput 表示 endpoints 数组条目输出
type EndpointOutput struct {
	URL       string   `json:"url"`
	Protocols []string `json:"protocols,omitempty"`
}

// RateLimitOutput 表示 rate_limit 输出
type RateLimitOutput struct {
	QPM int `json:"qpm,omitempty"`
}

// ToConfig 将 ProviderInput 转换为 config.ProviderConfig
func (input *ProviderInput) ToConfig() config.ProviderConfig {
	cfg := config.ProviderConfig{
		Endpoint:  input.Endpoint,
		APIKey:    input.APIKey,
		Protocols: input.Protocols,
	}

	// endpoints
	cfg.Endpoints = make([]config.EndpointConfig, len(input.Endpoints))
	for i, ep := range input.Endpoints {
		cfg.Endpoints[i] = config.EndpointConfig{
			URL:       ep.URL,
			Protocols: ep.Protocols,
		}
	}

	// rate_limit
	cfg.RateLimit = config.RateLimitConfig{
		QPM: input.RateLimit.QPM,
	}

	// remote_bridge
	if input.RemoteBridge != nil {
		cfg.RemoteBridge = &config.RemoteBridgeConfig{
			Enabled:  input.RemoteBridge.Enabled,
			Provider: input.RemoteBridge.Provider,
			Token:    input.RemoteBridge.Token,
			Local:    input.RemoteBridge.Local,
		}
	}

	// cascade
	if input.Cascade != nil {
		cfg.Cascade = &config.ProviderCascadeConfig{
			Enabled: input.Cascade.Enabled,
			Token:   input.Cascade.Token,
		}
	}

	return cfg
}

// ToProviderOutput 将 config.ProviderConfig 转换为输出格式
func ToProviderOutput(name string, cfg config.ProviderConfig) ProviderOutput {
	output := ProviderOutput{
		Name:   name,
		APIKey: cfg.APIKey,
	}

	// 统一转换为 endpoints 数组
	if len(cfg.Endpoints) > 0 {
		output.Endpoints = make([]EndpointOutput, len(cfg.Endpoints))
		for i, ep := range cfg.Endpoints {
			output.Endpoints[i] = EndpointOutput{
				URL:       ep.URL,
				Protocols: ep.Protocols,
			}
		}
	} else if cfg.Endpoint != "" {
		output.Endpoints = []EndpointOutput{{URL: cfg.Endpoint, Protocols: cfg.Protocols}}
	}

	// protocols: 优先使用显式配置，否则从 endpoints 提取
	if len(cfg.Protocols) > 0 {
		output.Protocols = cfg.Protocols
	} else {
		protocols := make([]string, 0)
		seen := make(map[string]bool)
		for _, ep := range output.Endpoints {
			for _, proto := range ep.Protocols {
				if proto != "" && !seen[proto] {
					protocols = append(protocols, proto)
					seen[proto] = true
				}
			}
		}
		if len(protocols) > 0 {
			output.Protocols = protocols
		}
	}

	// rate_limit
	if cfg.RateLimit.QPM > 0 {
		output.RateLimit = RateLimitOutput{QPM: cfg.RateLimit.QPM}
	}

	// remote_bridge
	if cfg.RemoteBridge != nil {
		output.RemoteBridge = &RemoteBridgeOutput{
			Enabled:  cfg.RemoteBridge.Enabled,
			Provider: cfg.RemoteBridge.Provider,
			Token:    cfg.RemoteBridge.Token,
			Local:    cfg.RemoteBridge.Local,
		}
	}

	// cascade
	if cfg.Cascade != nil {
		output.Cascade = &CascadeOutput{
			Enabled: cfg.Cascade.Enabled,
			Token:   cfg.Cascade.Token,
		}
	}

	return output
}

// RulesInput 表示整表替换 rules 的请求体。
// Rules 为 nil 表示请求缺少 rules 键或值为 null（非法）；空切片表示清空。
type RulesInput struct {
	Rules []RuleInput `json:"rules"`
}

// RuleInput 表示 API 输入的规则条目（snake_case JSON，与 YAML 的 kebab-case 字段名不同）。
type RuleInput struct {
	Match  RuleMatchInput  `json:"match"`
	Action RuleActionInput `json:"action"`
}

// RuleMatchInput 表示 API 输入的规则 match；省略的字段表示未设置（不参与匹配）。
type RuleMatchInput struct {
	ClientModel   *RuleConditionInput `json:"client_model,omitempty"`
	Key           *RuleConditionInput `json:"key,omitempty"`
	UpstreamModel *RuleConditionInput `json:"upstream_model,omitempty"`
}

// RuleConditionInput 表示 API 输入的单个 match 条件。
type RuleConditionInput struct {
	Op    string `json:"op"`
	Value string `json:"value"`
}

// RuleActionInput 表示 API 输入的规则 action；省略的字段表示未设置。
type RuleActionInput struct {
	Protocol         string   `json:"protocol,omitempty"`
	Effort           []string `json:"effort,omitempty"`
	EffortMode       string   `json:"effort_mode,omitempty"`
	TemperatureMode  string   `json:"temperature_mode,omitempty"`
	Thinking         string   `json:"thinking,omitempty"`
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	QPM              *int     `json:"qpm,omitempty"`
	EnableTimeRange  []string `json:"enable_time_range,omitempty"`
	DisableTimeRange []string `json:"disable_time_range,omitempty"`
	Retries          *int     `json:"retries,omitempty"`
}

// ToConfig 将 RuleInput 转换为 config.RuleConfig。
func (input *RuleInput) ToConfig() config.RuleConfig {
	return config.RuleConfig{
		Match: config.RuleMatch{
			ClientModel:   ruleConditionInputToConfig(input.Match.ClientModel),
			Key:           ruleConditionInputToConfig(input.Match.Key),
			UpstreamModel: ruleConditionInputToConfig(input.Match.UpstreamModel),
		},
		Action: config.RuleAction{
			Protocol:         input.Action.Protocol,
			Effort:           input.Action.Effort,
			EffortMode:       input.Action.EffortMode,
			TemperatureMode:  input.Action.TemperatureMode,
			Thinking:         input.Action.Thinking,
			MaxTokens:        input.Action.MaxTokens,
			QPM:              input.Action.QPM,
			EnableTimeRange:  input.Action.EnableTimeRange,
			DisableTimeRange: input.Action.DisableTimeRange,
			Retries:          input.Action.Retries,
		},
	}
}

func ruleConditionInputToConfig(cond *RuleConditionInput) *config.RuleCondition {
	if cond == nil {
		return nil
	}
	return &config.RuleCondition{Op: cond.Op, Value: cond.Value}
}

// RuleOutput 表示 API 输出的规则条目。
type RuleOutput struct {
	Match  RuleMatchOutput  `json:"match"`
	Action RuleActionOutput `json:"action"`
}

// RuleMatchOutput 表示 API 输出的规则 match。
type RuleMatchOutput struct {
	ClientModel   *RuleConditionOutput `json:"client_model,omitempty"`
	Key           *RuleConditionOutput `json:"key,omitempty"`
	UpstreamModel *RuleConditionOutput `json:"upstream_model,omitempty"`
}

// RuleConditionOutput 表示 API 输出的单个 match 条件。
type RuleConditionOutput struct {
	Op    string `json:"op"`
	Value string `json:"value"`
}

// RuleActionOutput 表示 API 输出的规则 action。
type RuleActionOutput struct {
	Protocol         string   `json:"protocol,omitempty"`
	Effort           []string `json:"effort,omitempty"`
	EffortMode       string   `json:"effort_mode,omitempty"`
	TemperatureMode  string   `json:"temperature_mode,omitempty"`
	Thinking         string   `json:"thinking,omitempty"`
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	QPM              *int     `json:"qpm,omitempty"`
	EnableTimeRange  []string `json:"enable_time_range,omitempty"`
	DisableTimeRange []string `json:"disable_time_range,omitempty"`
	Retries          *int     `json:"retries,omitempty"`
}

// ToRuleOutput 将 config.RuleConfig 转换为 API 输出格式。
func ToRuleOutput(rule config.RuleConfig) RuleOutput {
	var effort []string
	if len(rule.Action.Effort) > 0 {
		effort = append([]string(nil), rule.Action.Effort...)
	}
	var enable []string
	if len(rule.Action.EnableTimeRange) > 0 {
		enable = append([]string(nil), rule.Action.EnableTimeRange...)
	}
	var disable []string
	if len(rule.Action.DisableTimeRange) > 0 {
		disable = append([]string(nil), rule.Action.DisableTimeRange...)
	}
	var qpm *int
	if rule.Action.QPM != nil {
		q := *rule.Action.QPM
		qpm = &q
	}
	var retries *int
	if rule.Action.Retries != nil {
		n := *rule.Action.Retries
		retries = &n
	}
	var maxTokens *int
	if rule.Action.MaxTokens != nil {
		n := *rule.Action.MaxTokens
		maxTokens = &n
	}
	return RuleOutput{
		Match: RuleMatchOutput{
			ClientModel:   ruleConditionToOutput(rule.Match.ClientModel),
			Key:           ruleConditionToOutput(rule.Match.Key),
			UpstreamModel: ruleConditionToOutput(rule.Match.UpstreamModel),
		},
		Action: RuleActionOutput{
			Protocol:         rule.Action.Protocol,
			Effort:           effort,
			EffortMode:       rule.Action.EffortMode,
			TemperatureMode:  rule.Action.TemperatureMode,
			Thinking:         rule.Action.Thinking,
			MaxTokens:        maxTokens,
			QPM:              qpm,
			EnableTimeRange:  enable,
			DisableTimeRange: disable,
			Retries:          retries,
		},
	}
}

func ruleConditionToOutput(cond *config.RuleCondition) *RuleConditionOutput {
	if cond == nil {
		return nil
	}
	return &RuleConditionOutput{Op: cond.Op, Value: cond.Value}
}

// ApplyResult 表示 apply 操作结果
type ApplyResult struct {
	Success bool
	Message string // 失败时的错误信息
}

// RuntimeRebuilder 基于 draft 配置重建运行时依赖
type RuntimeRebuilder interface {
	// Rebuild 基于 draft 配置返回新的 resolver 和 scheduler
	Rebuild(cfg *config.Config) (*model.Resolver, *scheduler.Scheduler, error)
	// RebuildResolver 只构建 resolver（用于计算 context_length）
	RebuildResolver(cfg *config.Config) (*model.Resolver, error)
}

// ReinitHandler 重新初始化 handler 依赖
type ReinitHandler interface {
	// Reinit 更新 handler 的运行时依赖
	Reinit(cfg *config.Config, resolver *model.Resolver, sched *scheduler.Scheduler)
}
