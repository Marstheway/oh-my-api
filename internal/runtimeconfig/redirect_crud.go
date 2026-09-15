package runtimeconfig

import (
	"strconv"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/config"
)

// RedirectCRUD 实现 redirect 的 CRUD 规则检查
type RedirectCRUD struct {
	draft *config.Config
}

func NewRedirectCRUD(draft *config.Config) *RedirectCRUD {
	return &RedirectCRUD{draft: draft}
}

// ValidateCreate 校验创建请求
func (c *RedirectCRUD) ValidateCreate(input *RedirectInput) *Error {
	// source 非空
	if strings.TrimSpace(input.Source) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "source", Message: "must not be empty"}
	}

	// target 非空
	if strings.TrimSpace(input.Target) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "target", Message: "must not be empty"}
	}

	// source 不能包含 '/'
	if strings.Contains(input.Source, "/") {
		return &Error{Code: ErrCodeBadRequest, Field: "source", Message: "must not contain '/'"}
	}

	// target 不能包含 '/'
	if strings.Contains(input.Target, "/") {
		return &Error{Code: ErrCodeBadRequest, Field: "target", Message: "must not contain '/'"}
	}

	// source 不能与现有 model_group 名冲突
	for _, g := range c.draft.ModelGroups {
		if g.Name == input.Source {
			return &Error{Code: ErrCodeConflict, Field: "source", Message: "conflicts with existing model_group name"}
		}
	}

	// source 不能重复
	if c.redirectExists(input.Source) {
		return &Error{Code: ErrCodeConflict, Field: "source", Message: "already exists"}
	}

	// 循环检测：新 source 不能形成环（必须在 targetExists 之前检查，因为自引用时 target 不存在）
	if c.wouldCreateCycle(input.Source, input.Target) {
		return &Error{Code: ErrCodeValidation, Field: "target", Message: "would create circular redirect"}
	}

	// target 必须存在（指向真实 group 或已有 source）
	// 注意：自引用已在循环检测中处理，这里检查其他情况
	if input.Source != input.Target && !c.targetExists(input.Target) {
		return &Error{Code: ErrCodeBadRequest, Field: "target", Message: "target must reference existing model_group or redirect source"}
	}

	// exposure 校验
	if input.Exposure != nil {
		if _, err := config.NormalizeExposure(*input.Exposure); err != nil {
			return &Error{Code: ErrCodeBadRequest, Field: "exposure", Message: err.Error()}
		}
	}

	return nil
}

// ValidateUpdate 校验更新请求（允许改名）
func (c *RedirectCRUD) ValidateUpdate(oldSource string, input *RedirectInput) *Error {
	// oldSource 必须存在
	if !c.redirectExists(oldSource) {
		return &Error{Code: ErrCodeNotFound, Field: "source", Message: "redirect not found"}
	}

	// source 非空
	if strings.TrimSpace(input.Source) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "source", Message: "must not be empty"}
	}

	// target 非空
	if strings.TrimSpace(input.Target) == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "target", Message: "must not be empty"}
	}

	// source 不能包含 '/'
	if strings.Contains(input.Source, "/") {
		return &Error{Code: ErrCodeBadRequest, Field: "source", Message: "must not contain '/'"}
	}

	// target 不能包含 '/'
	if strings.Contains(input.Target, "/") {
		return &Error{Code: ErrCodeBadRequest, Field: "target", Message: "must not contain '/'"}
	}

	// 改名时检查冲突
	if input.Source != oldSource {
		// 不能与现有 group 冲突
		for _, g := range c.draft.ModelGroups {
			if g.Name == input.Source {
				return &Error{Code: ErrCodeConflict, Field: "source", Message: "conflicts with existing model_group name"}
			}
		}
		// 不能与现有 source 冲突
		if c.redirectExists(input.Source) {
			return &Error{Code: ErrCodeConflict, Field: "source", Message: "already exists"}
		}
	}

	// target 必须存在
	if !c.targetExists(input.Target) {
		return &Error{Code: ErrCodeBadRequest, Field: "target", Message: "target must reference existing model_group or redirect source"}
	}

	// 循环检测（考虑改名场景）
	if c.wouldCreateCycleOnUpdate(oldSource, input.Source, input.Target) {
		return &Error{Code: ErrCodeValidation, Field: "target", Message: "would create circular redirect"}
	}

	// exposure 校验
	if input.Exposure != nil {
		if _, err := config.NormalizeExposure(*input.Exposure); err != nil {
			return &Error{Code: ErrCodeBadRequest, Field: "exposure", Message: err.Error()}
		}
	}

	return nil
}

// ValidateDelete 校验删除请求（反向引用检查）
func (c *RedirectCRUD) ValidateDelete(source string) *Error {
	// source 必须存在
	if !c.redirectExists(source) {
		return &Error{Code: ErrCodeNotFound, Field: "source", Message: "redirect not found"}
	}

	// 检查是否有其他 redirect 引用此 source（反向引用）
	for _, rc := range c.draft.Redirect {
		if rc.Source != source && rc.Target == source {
			return &Error{Code: ErrCodeConflict, Field: "redirect." + rc.Source, Message: "references this redirect source"}
		}
	}

	// 检查是否有 model_group 的 models 引用此 source
	for _, g := range c.draft.ModelGroups {
		for i, e := range g.Models {
			if !strings.Contains(e.Model, "/") && e.Model == source {
				return &Error{Code: ErrCodeConflict, Field: "model_groups." + g.Name + ".models[" + strconv.Itoa(i) + "]", Message: "references this redirect source"}
			}
		}
	}

	return nil
}

// Create 执行创建操作
func (c *RedirectCRUD) Create(input *RedirectInput) error {
	if err := c.ValidateCreate(input); err != nil {
		return err
	}

	c.draft.Redirect = append(c.draft.Redirect, config.RedirectConfig{
		Source:   input.Source,
		Target:   input.Target,
		Exposure: normalizeExposurePtr(input.Exposure),
	})
	return nil
}

// Update 执行更新操作（允许改名）
func (c *RedirectCRUD) Update(oldSource string, input *RedirectInput) error {
	if err := c.ValidateUpdate(oldSource, input); err != nil {
		return err
	}

	// 查找并更新 redirect
	for i, rc := range c.draft.Redirect {
		if rc.Source == oldSource {
			c.draft.Redirect[i] = config.RedirectConfig{
				Source:   input.Source,
				Target:   input.Target,
				Exposure: normalizeExposurePtr(input.Exposure),
			}
			break
		}
	}
	return nil
}

// Delete 执行删除操作
func (c *RedirectCRUD) Delete(source string) error {
	if err := c.ValidateDelete(source); err != nil {
		return err
	}

	// 查找并删除 redirect
	for i, rc := range c.draft.Redirect {
		if rc.Source == source {
			c.draft.Redirect = append(c.draft.Redirect[:i], c.draft.Redirect[i+1:]...)
			break
		}
	}
	return nil
}

// Get 获取单个 redirect
func (c *RedirectCRUD) Get(source string) (RedirectOutput, bool) {
	for _, rc := range c.draft.Redirect {
		if rc.Source == source {
			return RedirectOutput{
				Source:   rc.Source,
				Target:   rc.Target,
				Exposure: exposureOrDefault(rc.Exposure),
			}, true
		}
	}
	return RedirectOutput{}, false
}

// List 获取所有 redirect（带解析信息）
func (c *RedirectCRUD) List() []RedirectListOutput {
	result := make([]RedirectListOutput, 0, len(c.draft.Redirect))

	for _, rc := range c.draft.Redirect {
		resolved, chainLength := c.resolveRedirect(rc.Source)
		result = append(result, RedirectListOutput{
			Source:        rc.Source,
			Target:        rc.Target,
			ResolvedGroup: resolved,
			ChainLength:   chainLength,
			Exposure:      exposureOrDefault(rc.Exposure),
		})
	}

	return result
}

// redirectExists 检查 redirect source 是否存在
func (c *RedirectCRUD) redirectExists(source string) bool {
	for _, rc := range c.draft.Redirect {
		if rc.Source == source {
			return true
		}
	}
	return false
}

// targetExists 检查 target 是否指向有效的 group 或 source
func (c *RedirectCRUD) targetExists(target string) bool {
	// 检查是否指向现有 group
	for _, g := range c.draft.ModelGroups {
		if g.Name == target {
			return true
		}
	}

	// 检查是否指向现有 source（允许 source-to-source）
	return c.redirectExists(target)
}

// wouldCreateCycle 检查新增 source 是否会形成环
func (c *RedirectCRUD) wouldCreateCycle(source, target string) bool {
	// 构建临时 redirect slice（包含新增项）
	tempRedirect := make([]config.RedirectConfig, 0, len(c.draft.Redirect)+1)
	tempRedirect = append(tempRedirect, c.draft.Redirect...)
	tempRedirect = append(tempRedirect, config.RedirectConfig{
		Source: source,
		Target: target,
	})

	// 检测环：从 source 开始追踪
	visited := make(map[string]bool)
	current := source

	for {
		if visited[current] {
			return true // 发现环
		}
		visited[current] = true

		// 检查是否到达真实 group
		foundGroup := false
		for _, g := range c.draft.ModelGroups {
			if g.Name == current {
				foundGroup = true
				break
			}
		}
		if foundGroup {
			return false // 成功到达 group，无环
		}

		// 继续追踪
		next := ""
		for _, rc := range tempRedirect {
			if rc.Source == current {
				next = rc.Target
				break
			}
		}
		if next == "" {
			return false // 链中断（但这在 targetExists 检查中已拦截）
		}
		current = next
	}
}

// wouldCreateCycleOnUpdate 检查更新是否形成环（考虑改名）
func (c *RedirectCRUD) wouldCreateCycleOnUpdate(oldSource, newSource, newTarget string) bool {
	// 构建临时 slice：移除 oldSource，添加 newSource
	tempRedirect := make([]config.RedirectConfig, 0, len(c.draft.Redirect))
	for _, rc := range c.draft.Redirect {
		if rc.Source != oldSource {
			tempRedirect = append(tempRedirect, rc)
		}
	}
	tempRedirect = append(tempRedirect, config.RedirectConfig{
		Source: newSource,
		Target: newTarget,
	})

	// 从 newSource 开始检测环
	visited := make(map[string]bool)
	current := newSource

	for {
		if visited[current] {
			return true
		}
		visited[current] = true

		foundGroup := false
		for _, g := range c.draft.ModelGroups {
			if g.Name == current {
				foundGroup = true
				break
			}
		}
		if foundGroup {
			return false
		}

		next := ""
		for _, rc := range tempRedirect {
			if rc.Source == current {
				next = rc.Target
				break
			}
		}
		if next == "" {
			return false
		}
		current = next
	}
}

// resolveRedirect 解析 redirect 到最终的 group，返回 (最终 group, 链路长度)
func (c *RedirectCRUD) resolveRedirect(source string) (string, int) {
	visited := make(map[string]bool)
	current := source
	chainLength := 0

	for {
		if visited[current] {
			// 理论上不会发生，因为 CRUD 时已做循环检测
			return "", chainLength
		}
		visited[current] = true

		// 检查当前节点是否是 group（在移动之前检查）
		for _, g := range c.draft.ModelGroups {
			if g.Name == current {
				return current, chainLength
			}
		}

		// 当前节点不是 group，尝试追踪 redirect
		next := ""
		for _, rc := range c.draft.Redirect {
			if rc.Source == current {
				next = rc.Target
				break
			}
		}
		if next == "" {
			// 链中断，返回空
			return "", chainLength
		}

		// 检查 next 是否是 group
		isGroup := false
		for _, g := range c.draft.ModelGroups {
			if g.Name == next {
				isGroup = true
				break
			}
		}

		// 如果 next 是 group，不增加 chainLength（直达）
		// 如果 next 不是 group，说明是中间 redirect，增加 chainLength
		if !isGroup {
			chainLength++
		}

		// 移动到 next
		current = next
	}
}

// Reorder 按 sources 置换 redirect 切片。必须是现有 source 的排列。
func (c *RedirectCRUD) Reorder(sources []string) error {
	if sources == nil {
		return &Error{Code: ErrCodeBadRequest, Field: "sources", Message: "must not be null"}
	}
	current := c.draft.Redirect
	if len(sources) != len(current) {
		return &Error{Code: ErrCodeBadRequest, Field: "sources", Message: "must be a permutation of existing redirect sources"}
	}
	bySource := make(map[string]config.RedirectConfig, len(current))
	for _, rc := range current {
		bySource[rc.Source] = rc
	}
	seen := make(map[string]struct{}, len(sources))
	next := make(config.RedirectConfigs, 0, len(sources))
	for _, s := range sources {
		if s == "" {
			return &Error{Code: ErrCodeBadRequest, Field: "sources", Message: "must not contain empty source"}
		}
		if _, dup := seen[s]; dup {
			return &Error{Code: ErrCodeBadRequest, Field: "sources", Message: "duplicate source: " + s}
		}
		seen[s] = struct{}{}
		rc, ok := bySource[s]
		if !ok {
			return &Error{Code: ErrCodeBadRequest, Field: "sources", Message: "unknown redirect: " + s}
		}
		next = append(next, rc)
	}
	c.draft.Redirect = next
	return nil
}
