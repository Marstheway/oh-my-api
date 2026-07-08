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

	// endpoint 非空（至少需要 endpoint 或 endpoints）
	if strings.TrimSpace(input.Endpoint) == "" && len(input.Endpoints) == 0 {
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

	// upstream_models 校验
	for i, um := range input.UpstreamModels {
		if strings.TrimSpace(um.Model) == "" {
			return &Error{Code: ErrCodeBadRequest, Field: "upstream_models[" + strconv.Itoa(i) + "].model", Message: "must not be empty"}
		}
		if um.QPM < 0 {
			return &Error{Code: ErrCodeBadRequest, Field: "upstream_models[" + strconv.Itoa(i) + "].qpm", Message: "must be >= 0"}
		}
	}

	// rate_limit.qpm 校验
	if input.RateLimit.QPM < 0 {
		return &Error{Code: ErrCodeBadRequest, Field: "rate_limit.qpm", Message: "must be >= 0"}
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

	// endpoint 非空（至少需要 endpoint 或 endpoints）
	if strings.TrimSpace(input.Endpoint) == "" && len(input.Endpoints) == 0 {
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

	// upstream_models 校验
	for i, um := range input.UpstreamModels {
		if strings.TrimSpace(um.Model) == "" {
			return &Error{Code: ErrCodeBadRequest, Field: "upstream_models[" + strconv.Itoa(i) + "].model", Message: "must not be empty"}
		}
		if um.QPM < 0 {
			return &Error{Code: ErrCodeBadRequest, Field: "upstream_models[" + strconv.Itoa(i) + "].qpm", Message: "must be >= 0"}
		}
	}

	// rate_limit.qpm 校验
	if input.RateLimit.QPM < 0 {
		return &Error{Code: ErrCodeBadRequest, Field: "rate_limit.qpm", Message: "must be >= 0"}
	}

	return nil
}

// Update 执行更新操作（含改名传播）
func (c *ProviderCRUD) Update(oldName string, input *ProviderInput) error {
	if err := c.ValidateUpdate(oldName, input); err != nil {
		return err
	}

	cfg := input.ToConfig()

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
