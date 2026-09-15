package runtimeconfig

import (
	"strconv"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/config"
)

// ProviderCRUD 实现 provider 的 CRUD 规则检查
type ProviderCRUD struct {
	draft *config.Config
}

func NewProviderCRUD(draft *config.Config) *ProviderCRUD {
	return &ProviderCRUD{draft: draft}
}

// ValidateCreate 校验创建请求
func (c *ProviderCRUD) ValidateCreate(input *ProviderInput) *Error {
	// name 非空
	if strings.TrimSpace(input.Name) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "name", Message: "must not be empty"}
	}

	// 检查 name 在 providers.Items 中唯一
	if _, exists := c.draft.Providers.Items[input.Name]; exists {
		return &Error{Code: ErrCodeConflict, Field: "name", Message: "already exists"}
	}

	// endpoint 非空（至少需要 endpoint 或 endpoints；cascade.enabled 除外）
	if !isCascadeEnabled(input) && strings.TrimSpace(input.Endpoint) == "" && len(input.Endpoints) == 0 {
		return &Error{Code: ErrCodeBadRequest, Field: "endpoint", Message: "must provide endpoint or endpoints"}
	}

	// protocols 格式校验
	for i, protocol := range input.Protocols {
		if strings.TrimSpace(protocol) == "" {
			return &Error{Code: ErrCodeBadRequest, Field: "protocols[" + strconv.Itoa(i) + "]", Message: "must not be empty"}
		}
	}

	// endpoints 校验
	for i, ep := range input.Endpoints {
		if strings.TrimSpace(ep.URL) == "" {
			return &Error{Code: ErrCodeBadRequest, Field: "endpoints[" + strconv.Itoa(i) + "].url", Message: "must not be empty"}
		}
		if len(ep.Protocols) == 0 {
			return &Error{Code: ErrCodeBadRequest, Field: "endpoints[" + strconv.Itoa(i) + "].protocols", Message: "must not be empty"}
		}
	}

	// rate_limit.qpm 校验
	if input.RateLimit.QPM < 0 {
		return &Error{Code: ErrCodeBadRequest, Field: "rate_limit.qpm", Message: "must be >= 0"}
	}

	// remote_bridge 校验
	if err := validateRemoteBridgeInput(input); err != nil {
		return err
	}

	// cascade 校验
	if err := validateCascadeInput(input); err != nil {
		return err
	}

	return nil
}

// Create 执行创建操作
func (c *ProviderCRUD) Create(input *ProviderInput) error {
	if err := c.ValidateCreate(input); err != nil {
		return err
	}

	cfg := input.ToConfig()
	c.draft.Providers.Items[input.Name] = cfg
	return nil
}

// ValidateUpdate 校验更新请求（含改名传播）
func (c *ProviderCRUD) ValidateUpdate(oldName string, input *ProviderInput) *Error {
	// name 非空
	if strings.TrimSpace(input.Name) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "name", Message: "must not be empty"}
	}

	// 查找目标 provider
	if _, exists := c.draft.Providers.Items[oldName]; !exists {
		return &Error{Code: ErrCodeNotFound, Field: "name", Message: "provider not found"}
	}

	// 改名时检查冲突
	if input.Name != oldName {
		if _, exists := c.draft.Providers.Items[input.Name]; exists {
			return &Error{Code: ErrCodeConflict, Field: "name", Message: "already exists"}
		}
	}

	// endpoint 非空（至少需要 endpoint 或 endpoints；cascade.enabled 除外）
	if !c.allowsEmptyEndpoint(oldName, input) && strings.TrimSpace(input.Endpoint) == "" && len(input.Endpoints) == 0 {
		return &Error{Code: ErrCodeBadRequest, Field: "endpoint", Message: "must provide endpoint or endpoints"}
	}

	// protocols 格式校验
	for i, protocol := range input.Protocols {
		if strings.TrimSpace(protocol) == "" {
			return &Error{Code: ErrCodeBadRequest, Field: "protocols[" + strconv.Itoa(i) + "]", Message: "must not be empty"}
		}
	}

	// endpoints 校验
	for i, ep := range input.Endpoints {
		if strings.TrimSpace(ep.URL) == "" {
			return &Error{Code: ErrCodeBadRequest, Field: "endpoints[" + strconv.Itoa(i) + "].url", Message: "must not be empty"}
		}
		if len(ep.Protocols) == 0 {
			return &Error{Code: ErrCodeBadRequest, Field: "endpoints[" + strconv.Itoa(i) + "].protocols", Message: "must not be empty"}
		}
	}

	// rate_limit.qpm 校验
	if input.RateLimit.QPM < 0 {
		return &Error{Code: ErrCodeBadRequest, Field: "rate_limit.qpm", Message: "must be >= 0"}
	}

	// remote_bridge 校验
	if err := validateRemoteBridgeInput(input); err != nil {
		return err
	}

	// cascade 校验
	if err := validateCascadeInput(input); err != nil {
		return err
	}

	return nil
}

// Update 执行更新操作（含改名传播）
func (c *ProviderCRUD) Update(oldName string, input *ProviderInput) error {
	if err := c.ValidateUpdate(oldName, input); err != nil {
		return err
	}

	cfg := input.ToConfig()

	// 更新时省略 cascade 字段则保留 draft 已有块
	if input.Cascade == nil {
		if existing, ok := c.draft.Providers.Items[oldName]; ok && existing.Cascade != nil {
			cascadeCopy := *existing.Cascade
			cfg.Cascade = &cascadeCopy
		}
	}

	// 删除旧条目（如果改名）
	if input.Name != oldName {
		delete(c.draft.Providers.Items, oldName)
		// 改名传播到 model_groups[].models[]
		c.propagateRename(oldName, input.Name)
	}

	// 更新/创建新条目
	c.draft.Providers.Items[input.Name] = cfg
	return nil
}

// propagateRename 执行改名传播：将 model_groups[].models[] 中引用 oldProvider/model 的条目改为 newProvider/model
func (c *ProviderCRUD) propagateRename(oldName, newName string) {
	for i, g := range c.draft.ModelGroups {
		for j, e := range g.Models {
			// 匹配 oldProvider/model 格式
			if strings.HasPrefix(e.Model, oldName+"/") {
				// 替换前缀为 newName
				remaining := strings.TrimPrefix(e.Model, oldName+"/")
				c.draft.ModelGroups[i].Models[j].Model = newName + "/" + remaining
			}
		}
	}
}

// ValidateDelete 校验删除请求（反向引用检查）
func (c *ProviderCRUD) ValidateDelete(name string) *Error {
	// 检查是否存在
	if _, exists := c.draft.Providers.Items[name]; !exists {
		return &Error{Code: ErrCodeNotFound, Field: "name", Message: "provider not found"}
	}

	// 检查 model_groups[].models[] 中是否有引用该 provider 的条目
	for _, g := range c.draft.ModelGroups {
		for _, e := range g.Models {
			// 检查是否以 provider/ 格式开头
			if strings.HasPrefix(e.Model, name+"/") {
				return &Error{Code: ErrCodeConflict, Field: "model_groups." + g.Name + ".models", Message: "references this provider"}
			}
		}
	}

	return nil
}

// Delete 执行删除操作
func (c *ProviderCRUD) Delete(name string) error {
	if err := c.ValidateDelete(name); err != nil {
		return err
	}

	delete(c.draft.Providers.Items, name)
	return nil
}

// Get 获取单个 provider
func (c *ProviderCRUD) Get(name string) (ProviderOutput, bool) {
	cfg, exists := c.draft.Providers.Items[name]
	if !exists {
		return ProviderOutput{}, false
	}
	return ToProviderOutput(name, cfg), true
}

// List 获取所有 providers
func (c *ProviderCRUD) List() []ProviderOutput {
	result := make([]ProviderOutput, 0, len(c.draft.Providers.Items))
	for name, cfg := range c.draft.Providers.Items {
		result = append(result, ToProviderOutput(name, cfg))
	}
	return result
}

// validateRemoteBridgeInput 校验 remote_bridge 输入字段。
// 仅当 input.RemoteBridge 非空且 enabled 为 true 时才执行校验。
// 规则与 serve 校验保持一致：provider 仅允许 xai-oauth；local 模式不使用 token。
func validateRemoteBridgeInput(input *ProviderInput) *Error {
	if input.RemoteBridge == nil || !input.RemoteBridge.Enabled {
		return nil
	}

	b := input.RemoteBridge

	if b.Local {
		if strings.TrimSpace(b.Token) != "" {
			return &Error{Code: ErrCodeBadRequest, Field: "remote_bridge.token", Message: "must be empty when OAuth bridge local mode is enabled"}
		}
	} else if strings.TrimSpace(b.Token) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "remote_bridge.token", Message: "must not be empty when OAuth bridge is enabled"}
	}

	// provider 非空
	if strings.TrimSpace(b.Provider) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "remote_bridge.provider", Message: "must not be empty when OAuth bridge is enabled"}
	}

	// 仅允许 xai-oauth
	if b.Provider != "xai-oauth" {
		return &Error{Code: ErrCodeBadRequest, Field: "remote_bridge.provider", Message: "unsupported OAuth bridge provider type, only xai-oauth is allowed"}
	}

	return nil
}

func isCascadeEnabled(input *ProviderInput) bool {
	return input.Cascade != nil && input.Cascade.Enabled
}

func (c *ProviderCRUD) allowsEmptyEndpoint(providerName string, input *ProviderInput) bool {
	if isCascadeEnabled(input) {
		return true
	}
	if input.Cascade != nil {
		return false
	}
	if existing, ok := c.draft.Providers.Items[providerName]; ok {
		return existing.Cascade != nil && existing.Cascade.Enabled
	}
	return false
}

// validateCascadeInput 校验 cascade 输入字段。
// 仅当 input.Cascade 非空且 enabled 为 true 时才执行 token 校验。
func validateCascadeInput(input *ProviderInput) *Error {
	if input.Cascade == nil || !input.Cascade.Enabled {
		return nil
	}

	if strings.TrimSpace(input.Cascade.Token) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "cascade.token", Message: "must not be empty when cascade is enabled"}
	}

	return nil
}
