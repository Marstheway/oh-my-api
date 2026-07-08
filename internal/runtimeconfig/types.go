package runtimeconfig

import (
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
)

// ErrorCode 表示操作失败的错误码
type ErrorCode string

const (
	ErrCodeBadRequest   ErrorCode = "bad_request"
	ErrCodeNotFound     ErrorCode = "not_found"
	ErrCodeConflict     ErrorCode = "conflict"
	ErrCodeValidation   ErrorCode = "validation_error"
	ErrCodeInternal     ErrorCode = "internal_error"
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

// ModelGroupInput 表示 API 输入的 model group 数据
// API 输入允许 model 或 models 二选一
type ModelGroupInput struct {
	Name          string              `json:"name"`
	Mode          string              `json:"mode,omitempty"`
	Model         string              `json:"model,omitempty"`  // 单模型输入
	Models        []ModelEntryInput   `json:"models,omitempty"` // 多模型输入
	Exposure      *string             `json:"exposure,omitempty"`
	ModelMetadata *ModelMetadataInput `json:"model_metadata,omitempty"`
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
}

// ModelEntryOutput 表示 models 数组条目输出
type ModelEntryOutput struct {
	Model  string `json:"model"`
	Weight int    `json:"weight"`
}

// ModelMetadataOutput 表示 model_metadata 输出
type ModelMetadataOutput struct {
	ContextLength        *int `json:"context_length,omitempty"`
	ComputedContextLength *int `json:"computed_context_length,omitempty"` // 计算值（从 catalog/子 group 递归得出）
}

// NormalizeInput 将输入归一化为 config.ModelGroupConfig
// 若输入 model 非空，归一为单条 models（weight=1, priority=0）
func (input *ModelGroupInput) NormalizeInput() config.ModelGroupConfig {
	cfg := config.ModelGroupConfig{
		Name:    input.Name,
		Mode:    input.Mode,
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
	Source   string           `json:"source"`   // 源名称
	Target   string           `json:"target"`   // 目标
	Exposure config.Exposure  `json:"exposure"` // 暴露级别（显式值）
}

// RedirectListOutput 表示列表输出的 redirect（带解析信息）
type RedirectListOutput struct {
	Source        string          `json:"source"`         // 源名称
	Target        string          `json:"target"`         // 配置的直接目标
	ResolvedGroup string          `json:"resolved_group"` // 最终解析到的 group
	ChainLength   int             `json:"chain_length"`   // 链路长度（0=直达 group）
	Exposure      config.Exposure `json:"exposure"`        // 暴露级别（显式值）
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

// ProviderInput 表示 API 输入的 provider 数据
type ProviderInput struct {
	Name             string                     `json:"name"`
	Endpoint         string                     `json:"endpoint,omitempty"`
	Endpoints        []EndpointInput            `json:"endpoints,omitempty"`
	APIKey           string                     `json:"api_key,omitempty"`
	Protocols        []string                   `json:"protocols,omitempty"`
	RateLimit        RateLimitInput             `json:"rate_limit,omitempty"`
	UpstreamModels   []UpstreamModelInput       `json:"upstream_models,omitempty"`
	DefaultProtocols []string                   `json:"default_protocols,omitempty"`
	DisabledTimeRanges []string                 `json:"disabled_time_ranges,omitempty"`
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

// UpstreamModelInput 表示 upstream_models 数组条目输入
type UpstreamModelInput struct {
	Model            string   `json:"model"`
	QPM              int      `json:"qpm,omitempty"`
	AllowedProtocols []string `json:"allowed_protocols,omitempty"`
}

// ProviderOutput 表示 API 输出的 provider 数据（完整配置，含 API Key）
type ProviderOutput struct {
	Name             string                     `json:"name"`
	Endpoints        []EndpointOutput           `json:"endpoints"`
	APIKey           string                     `json:"api_key,omitempty"`
	Protocols        []string                   `json:"protocols,omitempty"`
	RateLimit        RateLimitOutput            `json:"rate_limit,omitempty"`
	UpstreamModels   []UpstreamModelOutput      `json:"upstream_models,omitempty"`
	DefaultProtocols []string                   `json:"default_protocols,omitempty"`
	DisabledTimeRanges []string                 `json:"disabled_time_ranges,omitempty"`
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

// UpstreamModelOutput 表示 upstream_models 数组条目输出
type UpstreamModelOutput struct {
	Model            string   `json:"model"`
	QPM              int      `json:"qpm,omitempty"`
	AllowedProtocols []string `json:"allowed_protocols,omitempty"`
}

// ToConfig 将 ProviderInput 转换为 config.ProviderConfig
func (input *ProviderInput) ToConfig() config.ProviderConfig {
	cfg := config.ProviderConfig{
		Endpoint:           input.Endpoint,
		APIKey:             input.APIKey,
		Protocols:          input.Protocols,
		DefaultProtocols:   input.DefaultProtocols,
		DisabledTimeRanges: input.DisabledTimeRanges,
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

	// upstream_models
	cfg.UpstreamModels = make([]config.UpstreamModelConfig, len(input.UpstreamModels))
	for i, um := range input.UpstreamModels {
		cfg.UpstreamModels[i] = config.UpstreamModelConfig{
			Model:            um.Model,
			QPM:              um.QPM,
			AllowedProtocols: um.AllowedProtocols,
		}
	}

	return cfg
}

// ToProviderOutput 将 config.ProviderConfig 转换为输出格式
func ToProviderOutput(name string, cfg config.ProviderConfig) ProviderOutput {
	output := ProviderOutput{
		Name:               name,
		APIKey:             cfg.APIKey,
		DefaultProtocols:   cfg.DefaultProtocols,
		DisabledTimeRanges: cfg.DisabledTimeRanges,
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

	// upstream_models
	output.UpstreamModels = make([]UpstreamModelOutput, len(cfg.UpstreamModels))
	for i, um := range cfg.UpstreamModels {
		output.UpstreamModels[i] = UpstreamModelOutput{
			Model:            um.Model,
			QPM:              um.QPM,
			AllowedProtocols: um.AllowedProtocols,
		}
	}

	return output
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
