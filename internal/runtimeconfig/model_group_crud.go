package runtimeconfig

import (
	"strconv"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/config"
)

// ModelGroupCRUD 实现 model group 的 CRUD 规则检查
type ModelGroupCRUD struct {
	draft *config.Config
}

func NewModelGroupCRUD(draft *config.Config) *ModelGroupCRUD {
	return &ModelGroupCRUD{draft: draft}
}

// ValidateCreate 校验创建请求
func (c *ModelGroupCRUD) ValidateCreate(input *ModelGroupInput) *Error {
	// name 非空
	if strings.TrimSpace(input.Name) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "name", Message: "must not be empty"}
	}

	// 检查 name 在 model_groups 中唯一
	for _, g := range c.draft.ModelGroups {
		if g.Name == input.Name {
			return &Error{Code: ErrCodeConflict, Field: "name", Message: "already exists"}
		}
	}

	// 检查 name 不与 redirect source 冲突
	for _, rc := range c.draft.Redirect {
		if rc.Source == input.Name {
			return &Error{Code: ErrCodeConflict, Field: "name", Message: "conflicts with redirect source"}
		}
	}

	// mode 校验
	if input.Mode != "" {
		switch input.Mode {
		case "concurrent", "load-balance", "loadbalance", "failover", "adaptive":
			// valid
		default:
			return &Error{Code: ErrCodeBadRequest, Field: "mode", Message: "must be one of concurrent, load-balance, loadbalance, failover, adaptive"}
		}
	}

	// model/models 二选一校验
	if input.Model == "" && len(input.Models) == 0 {
		return &Error{Code: ErrCodeBadRequest, Field: "models", Message: "must provide model or models"}
	}
	if input.Model != "" && len(input.Models) > 0 {
		return &Error{Code: ErrCodeBadRequest, Field: "models", Message: "cannot specify both model and models"}
	}

	// adaptive 模式只允许 provider/model 叶子条目，拒绝内部引用
	if input.Mode == "adaptive" {
		if input.Model != "" && !strings.Contains(input.Model, "/") {
			return &Error{Code: ErrCodeBadRequest, Field: "model", Message: "adaptive mode requires direct provider/model entry (must contain '/')"}
		}
		for i, e := range input.Models {
			if !strings.Contains(e.Model, "/") {
				return &Error{Code: ErrCodeBadRequest, Field: "models[" + strconv.Itoa(i) + "].model", Message: "adaptive mode requires direct provider/model entry (must contain '/')"}
			}
		}
	}

	// models[] 条目校验
	// 注意：Validate 检查显式提供的值是否有效（nil 表示未提供，跳过检查）
	// NormalizeInput 会在之后补齐默认值（weight=1）
	for i, e := range input.Models {
		if strings.TrimSpace(e.Model) == "" {
			return &Error{Code: ErrCodeBadRequest, Field: "models[" + strconv.Itoa(i) + "].model", Message: "must not be empty"}
		}
		if e.Weight != nil && *e.Weight <= 0 {
			return &Error{Code: ErrCodeBadRequest, Field: "models[" + strconv.Itoa(i) + "].weight", Message: "must be > 0"}
		}
	}


	// model_metadata.context_length 校验
	if input.ModelMetadata != nil && input.ModelMetadata.ContextLength != nil {
		if *input.ModelMetadata.ContextLength <= 0 {
			return &Error{Code: ErrCodeBadRequest, Field: "model_metadata.context_length", Message: "must be > 0"}
		}
	}

	// exposure 校验
	if input.Exposure != nil {
		if _, err := config.NormalizeExposure(*input.Exposure); err != nil {
			return &Error{Code: ErrCodeBadRequest, Field: "exposure", Message: err.Error()}
		}
	}

	return nil
}

// Create 执行创建操作
func (c *ModelGroupCRUD) Create(input *ModelGroupInput) (config.ModelGroupConfig, error) {
	if err := c.ValidateCreate(input); err != nil {
		return config.ModelGroupConfig{}, err
	}

	cfg := input.NormalizeInput()
	c.draft.ModelGroups = append(c.draft.ModelGroups, cfg)
	return cfg, nil
}

// ValidateUpdate 校验更新请求（含改名传播）
func (c *ModelGroupCRUD) ValidateUpdate(oldName string, input *ModelGroupInput) *Error {
	// name 非空
	if strings.TrimSpace(input.Name) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "name", Message: "must not be empty"}
	}

	// 查找目标 group
	var found bool
	for _, g := range c.draft.ModelGroups {
		if g.Name == oldName {
			found = true
			break
		}
	}
	if !found {
		return &Error{Code: ErrCodeNotFound, Field: "name", Message: "model group not found"}
	}

	// 改名时检查冲突
	if input.Name != oldName {
		for _, g := range c.draft.ModelGroups {
			if g.Name == input.Name {
				return &Error{Code: ErrCodeConflict, Field: "name", Message: "already exists"}
			}
		}
			for _, rc := range c.draft.Redirect {
				if rc.Source == input.Name {
					return &Error{Code: ErrCodeConflict, Field: "name", Message: "conflicts with redirect source"}
				}
			}
	}

	// mode 校验
	if input.Mode != "" {
		switch input.Mode {
		case "concurrent", "load-balance", "loadbalance", "failover", "adaptive":
			// valid
		default:
			return &Error{Code: ErrCodeBadRequest, Field: "mode", Message: "must be one of concurrent, load-balance, loadbalance, failover, adaptive"}
		}
	}

	// model/models 二选一校验
	if input.Model == "" && len(input.Models) == 0 {
		return &Error{Code: ErrCodeBadRequest, Field: "models", Message: "must provide model or models"}
	}
	if input.Model != "" && len(input.Models) > 0 {
		return &Error{Code: ErrCodeBadRequest, Field: "models", Message: "cannot specify both model and models"}
	}

	// adaptive 模式只允许 provider/model 叶子条目，拒绝内部引用
	if input.Mode == "adaptive" {
		if input.Model != "" && !strings.Contains(input.Model, "/") {
			return &Error{Code: ErrCodeBadRequest, Field: "model", Message: "adaptive mode requires direct provider/model entry (must contain '/')"}
		}
		for i, e := range input.Models {
			if !strings.Contains(e.Model, "/") {
				return &Error{Code: ErrCodeBadRequest, Field: "models[" + strconv.Itoa(i) + "].model", Message: "adaptive mode requires direct provider/model entry (must contain '/')"}
			}
		}
	}

	// models[] 条目校验
	// 注意：Validate 检查显式提供的值是否有效（nil 表示未提供，跳过检查）
	// NormalizeInput 会在之后补齐默认值（weight=1）
	for i, e := range input.Models {
		if strings.TrimSpace(e.Model) == "" {
			return &Error{Code: ErrCodeBadRequest, Field: "models[" + strconv.Itoa(i) + "].model", Message: "must not be empty"}
		}
		if e.Weight != nil && *e.Weight <= 0 {
			return &Error{Code: ErrCodeBadRequest, Field: "models[" + strconv.Itoa(i) + "].weight", Message: "must be > 0"}
		}
	}

	// model_metadata.context_length 校验
	if input.ModelMetadata != nil && input.ModelMetadata.ContextLength != nil {
		if *input.ModelMetadata.ContextLength <= 0 {
			return &Error{Code: ErrCodeBadRequest, Field: "model_metadata.context_length", Message: "must be > 0"}
		}
	}

	// exposure 校验
	if input.Exposure != nil {
		if _, err := config.NormalizeExposure(*input.Exposure); err != nil {
			return &Error{Code: ErrCodeBadRequest, Field: "exposure", Message: err.Error()}
		}
	}

	return nil
}

// Update 执行更新操作（含改名传播）
func (c *ModelGroupCRUD) Update(oldName string, input *ModelGroupInput) (config.ModelGroupConfig, error) {
	if err := c.ValidateUpdate(oldName, input); err != nil {
		return config.ModelGroupConfig{}, err
	}

	cfg := input.NormalizeInput()

	// 找到并替换
	for i, g := range c.draft.ModelGroups {
		if g.Name == oldName {
			c.draft.ModelGroups[i] = cfg
			break
		}
	}

	// 改名传播
	if input.Name != oldName {
		c.propagateRename(oldName, input.Name)
	}

	return cfg, nil
}

// propagateRename 执行改名传播：redirect value、其他组 models[]、其他组 fallback[]
func (c *ModelGroupCRUD) propagateRename(oldName, newName string) {
	// 更新 redirect value
	// 更新 redirect target
	for i, rc := range c.draft.Redirect {
		if rc.Target == oldName {
			c.draft.Redirect[i].Target = newName
		}
	}

	// 更新其他 group 的 models[] 内部引用
	for i, g := range c.draft.ModelGroups {
		if g.Name == newName {
			continue // 跳过自身
		}
		for j, e := range g.Models {
			if !strings.Contains(e.Model, "/") && e.Model == oldName {
				c.draft.ModelGroups[i].Models[j].Model = newName
			}
		}
	}
}

// ValidateDelete 校验删除请求（反向引用检查）
func (c *ModelGroupCRUD) ValidateDelete(name string) *Error {
	// 检查是否存在
	var found bool
	for _, g := range c.draft.ModelGroups {
		if g.Name == name {
			found = true
			break
		}
	}
	if !found {
		return &Error{Code: ErrCodeNotFound, Field: "name", Message: "model group not found"}
	}

	// 检查 redirect value 反向引用
	// 检查 redirect target 反向引用
	for _, rc := range c.draft.Redirect {
		if rc.Target == name {
			return &Error{Code: ErrCodeConflict, Field: "redirect." + rc.Source, Message: "references this model group"}
		}
	}

	// 检查其他 group 的 models[] 内部引用
	for _, g := range c.draft.ModelGroups {
		if g.Name == name {
			continue
		}
		for _, e := range g.Models {
			if !strings.Contains(e.Model, "/") && e.Model == name {
				return &Error{Code: ErrCodeConflict, Field: "model_groups." + g.Name + ".models", Message: "references this model group"}
			}
		}
	}

	return nil
}

// Delete 执行删除操作
func (c *ModelGroupCRUD) Delete(name string) error {
	if err := c.ValidateDelete(name); err != nil {
		return err
	}

	// 从切片中移除
	for i, g := range c.draft.ModelGroups {
		if g.Name == name {
			c.draft.ModelGroups = append(c.draft.ModelGroups[:i], c.draft.ModelGroups[i+1:]...)
			break
		}
	}

	return nil
}

// Get 获取单个 model group
func (c *ModelGroupCRUD) Get(name string) (config.ModelGroupConfig, bool) {
	for _, g := range c.draft.ModelGroups {
		if g.Name == name {
			return g, true
		}
	}
	return config.ModelGroupConfig{}, false
}

// List 获取所有 model groups
func (c *ModelGroupCRUD) List() []config.ModelGroupConfig {
	return c.draft.ModelGroups
}
