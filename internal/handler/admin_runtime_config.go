package handler

import (
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/gin-gonic/gin"
)

// AdminRuntimeConfigHandler 处理 /admin/runtime-config 端点
type AdminRuntimeConfigHandler struct {
	manager *runtimeconfig.Manager
}

// NewAdminRuntimeConfigHandler 创建 handler
func NewAdminRuntimeConfigHandler(manager *runtimeconfig.Manager) *AdminRuntimeConfigHandler {
	return &AdminRuntimeConfigHandler{
		manager: manager,
	}
}

// DraftResponse 表示完整 draft 视图（脱敏）
type DraftResponse struct {
	ModelGroups []runtimeconfig.ModelGroupOutput `json:"model_groups"`
	Redirect    []runtimeconfig.RedirectListOutput `json:"redirect,omitempty"`
	Providers   map[string]ProviderSummary         `json:"providers"`
	AuthKeys    []AuthKeySummary                   `json:"auth_keys"`
}

// ProviderSummary 表示 Provider 摘要（脱敏，移除 API Key）
type ProviderSummary struct {
	Endpoint  string   `json:"endpoint"`
	Protocols []string `json:"protocols,omitempty"`
}

// AuthKeySummary 表示 Auth Key 摘要（脱敏，只返回 name）
type AuthKeySummary struct {
	Name string `json:"name"`
}

// GetDraft 返回完整 draft 视图（脱敏）
func (h *AdminRuntimeConfigHandler) GetDraft(c *gin.Context) {
	cfg := h.manager.GetDraft()

	// 构建 draft resolver 用于计算 context_length
	draftResolver, err := h.manager.RebuildDraftResolver()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "internal_error",
				"message": "failed to build draft resolver: " + err.Error(),
			},
		})
		return
	}

	// 加载 draft 的 catalogIdx
	draftCatalogIdx := LoadCatalogContextIndex(cfg)

	groups := h.manager.ListModelGroups()
	output := make([]runtimeconfig.ModelGroupOutput, len(groups))
	for i, g := range groups {
		output[i] = runtimeconfig.ToOutput(g)
		// 计算 computed_context_length（仅当无配置值时）
		if output[i].ModelMetadata == nil || output[i].ModelMetadata.ContextLength == nil {
			computed := ResolveContextLength(g.Name, draftResolver, draftCatalogIdx)
			if computed != nil {
				if output[i].ModelMetadata == nil {
					output[i].ModelMetadata = &runtimeconfig.ModelMetadataOutput{}
				}
				output[i].ModelMetadata.ComputedContextLength = computed
			}
		}
	}

	// 计算 redirect 的 context_length（基于最终解析到的 group）
	redirects := h.manager.ListRedirects()
	for i := range redirects {
		if redirects[i].ResolvedGroup != "" {
			ctx := ResolveContextLength(redirects[i].ResolvedGroup, draftResolver, draftCatalogIdx)
			redirects[i].ContextLength = ctx
		}
	}

	// 构造 Providers 摘要（脱敏）
	providers := make(map[string]ProviderSummary)
	for name, p := range cfg.Providers.Items {
		providers[name] = ProviderSummary{
			Endpoint:  p.Endpoint,
			Protocols: p.Protocols,
		}
	}

	// 构造 Auth Keys 摘要（脱敏）
	authKeys := make([]AuthKeySummary, len(cfg.Inbound.Auth.Keys))
	for i, key := range cfg.Inbound.Auth.Keys {
		authKeys[i] = AuthKeySummary{
			Name: key.Name,
		}
	}

	resp := DraftResponse{
		ModelGroups: output,
		Redirect:    redirects,
		Providers:   providers,
		AuthKeys:    authKeys,
	}

	c.JSON(http.StatusOK, resp)
}

// GetModelGroup 返回单个 model group 详情
func (h *AdminRuntimeConfigHandler) GetModelGroup(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "missing model group name",
			},
		})
		return
	}

	group, found := h.manager.GetModelGroup(name)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "not_found",
				"message": "model group not found",
			},
		})
		return
	}

	c.JSON(http.StatusOK, runtimeconfig.ToOutput(group))
}

// CreateModelGroup 创建 model group
func (h *AdminRuntimeConfigHandler) CreateModelGroup(c *gin.Context) {
	var input runtimeconfig.ModelGroupInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid request body: " + err.Error(),
			},
		})
		return
	}

	group, err := h.manager.CreateModelGroup(&input)
	if err != nil {
		h.writeError(c, err)
		return
	}

	c.JSON(http.StatusCreated, runtimeconfig.ToOutput(group))
}

// UpdateModelGroup 更新 model group（支持改名）
func (h *AdminRuntimeConfigHandler) UpdateModelGroup(c *gin.Context) {
	oldName := c.Param("name")
	if oldName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "missing model group name",
			},
		})
		return
	}

	var input runtimeconfig.ModelGroupInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid request body: " + err.Error(),
			},
		})
		return
	}

	group, err := h.manager.UpdateModelGroup(oldName, &input)
	if err != nil {
		h.writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, runtimeconfig.ToOutput(group))
}

// DeleteModelGroup 删除 model group
func (h *AdminRuntimeConfigHandler) DeleteModelGroup(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "missing model group name",
			},
		})
		return
	}

	err := h.manager.DeleteModelGroup(name)
	if err != nil {
		h.writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// ApplyRequest apply 请求
type ApplyRequest struct {
	Confirm bool `json:"confirm"`
}

// ApplyResponse apply 响应
type ApplyResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// Apply 执行配置生效
func (h *AdminRuntimeConfigHandler) Apply(c *gin.Context) {
	result := h.manager.Apply()

	if !result.Success {
		c.JSON(http.StatusUnprocessableEntity, ApplyResponse{
			Success: false,
			Message: result.Message,
		})
		return
	}

	c.JSON(http.StatusOK, ApplyResponse{Success: true})
}

// GetRedirects 返回所有 redirect 列表
func (h *AdminRuntimeConfigHandler) GetRedirects(c *gin.Context) {
	redirects := h.manager.ListRedirects()
	c.JSON(http.StatusOK, redirects)
}

// GetRedirect 返回单个 redirect 详情
func (h *AdminRuntimeConfigHandler) GetRedirect(c *gin.Context) {
	alias := c.Param("alias")
	if alias == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "missing redirect alias",
			},
		})
		return
	}

	redirect, found := h.manager.GetRedirect(alias)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "not_found",
				"message": "redirect not found",
			},
		})
		return
	}

	c.JSON(http.StatusOK, redirect)
}

// CreateRedirect 创建 redirect
func (h *AdminRuntimeConfigHandler) CreateRedirect(c *gin.Context) {
	var input runtimeconfig.RedirectInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid request body: " + err.Error(),
			},
		})
		return
	}

	if err := h.manager.CreateRedirect(&input); err != nil {
		h.writeError(c, err)
		return
	}

	redirect, _ := h.manager.GetRedirect(input.Source)
	c.JSON(http.StatusCreated, redirect)
}

// UpdateRedirect 更新 redirect（支持改名）
func (h *AdminRuntimeConfigHandler) UpdateRedirect(c *gin.Context) {
	oldSource := c.Param("alias")
	if oldSource == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "missing redirect source",
			},
		})
		return
	}

	var input runtimeconfig.RedirectInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid request body: " + err.Error(),
			},
		})
		return
	}

	if err := h.manager.UpdateRedirect(oldSource, &input); err != nil {
		h.writeError(c, err)
		return
	}

	redirect, _ := h.manager.GetRedirect(input.Source)
	c.JSON(http.StatusOK, redirect)
}

// DeleteRedirect 删除 redirect
func (h *AdminRuntimeConfigHandler) DeleteRedirect(c *gin.Context) {
	source := c.Param("alias")
	if source == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "missing redirect source",
			},
		})
		return
	}

	if err := h.manager.DeleteRedirect(source); err != nil {
		h.writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// GetProviders 返回所有 provider 列表（完整配置，含 API Key）
func (h *AdminRuntimeConfigHandler) GetProviders(c *gin.Context) {
	providers := h.manager.ListProviders()
	c.JSON(http.StatusOK, providers)
}

// GetProvider 返回单个 provider 详情（完整配置，含 API Key）
func (h *AdminRuntimeConfigHandler) GetProvider(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "missing provider name",
			},
		})
		return
	}

	provider, found := h.manager.GetProvider(name)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "not_found",
				"message": "provider not found",
			},
		})
		return
	}

	c.JSON(http.StatusOK, provider)
}

// CreateProvider 创建 provider
func (h *AdminRuntimeConfigHandler) CreateProvider(c *gin.Context) {
	var input runtimeconfig.ProviderInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid request body: " + err.Error(),
			},
		})
		return
	}

	if err := h.manager.CreateProvider(&input); err != nil {
		h.writeError(c, err)
		return
	}

	provider, _ := h.manager.GetProvider(input.Name)
	c.JSON(http.StatusCreated, provider)
}

// UpdateProvider 更新 provider（支持改名）
func (h *AdminRuntimeConfigHandler) UpdateProvider(c *gin.Context) {
	oldName := c.Param("name")
	if oldName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "missing provider name",
			},
		})
		return
	}

	var input runtimeconfig.ProviderInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid request body: " + err.Error(),
			},
		})
		return
	}

	if err := h.manager.UpdateProvider(oldName, &input); err != nil {
		h.writeError(c, err)
		return
	}

	provider, _ := h.manager.GetProvider(input.Name)
	c.JSON(http.StatusOK, provider)
}

// DeleteProvider 删除 provider
func (h *AdminRuntimeConfigHandler) DeleteProvider(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "missing provider name",
			},
		})
		return
	}

	if err := h.manager.DeleteProvider(name); err != nil {
		h.writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// GetAuthKeys 返回所有 auth key 列表（完整 key）
func (h *AdminRuntimeConfigHandler) GetAuthKeys(c *gin.Context) {
	keys := h.manager.ListAuthKeys()
	c.JSON(http.StatusOK, keys)
}

// GetAuthKey 返回单个 auth key 详情（完整 key）
func (h *AdminRuntimeConfigHandler) GetAuthKey(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "missing auth key name",
			},
		})
		return
	}

	key, found := h.manager.GetAuthKey(name)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "not_found",
				"message": "auth key not found",
			},
		})
		return
	}

	c.JSON(http.StatusOK, key)
}

// CreateAuthKey 创建 auth key
func (h *AdminRuntimeConfigHandler) CreateAuthKey(c *gin.Context) {
	var input runtimeconfig.AuthKeyInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid request body: " + err.Error(),
			},
		})
		return
	}

	if err := h.manager.CreateAuthKey(&input); err != nil {
		h.writeError(c, err)
		return
	}

	key, _ := h.manager.GetAuthKey(input.Name)
	c.JSON(http.StatusCreated, key)
}

// UpdateAuthKey 更新 auth key（允许改名）
func (h *AdminRuntimeConfigHandler) UpdateAuthKey(c *gin.Context) {
	oldName := c.Param("name")
	if oldName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "missing auth key name",
			},
		})
		return
	}

	var input runtimeconfig.AuthKeyInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid request body: " + err.Error(),
			},
		})
		return
	}

	if err := h.manager.UpdateAuthKey(oldName, &input); err != nil {
		h.writeError(c, err)
		return
	}

	key, _ := h.manager.GetAuthKey(input.Name)
	c.JSON(http.StatusOK, key)
}

// DeleteAuthKey 删除 auth key
func (h *AdminRuntimeConfigHandler) DeleteAuthKey(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "missing auth key name",
			},
		})
		return
	}

	if err := h.manager.DeleteAuthKey(name); err != nil {
		h.writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// writeError 根据 runtimeconfig.Error 映射 HTTP 状态码
func (h *AdminRuntimeConfigHandler) writeError(c *gin.Context, err error) {
	if rerr, ok := err.(*runtimeconfig.Error); ok {
		switch rerr.Code {
		case runtimeconfig.ErrCodeBadRequest:
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"code":    string(rerr.Code),
					"message": rerr.Message,
					"field":   rerr.Field,
				},
			})
		case runtimeconfig.ErrCodeNotFound:
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"code":    string(rerr.Code),
					"message": rerr.Message,
				},
			})
		case runtimeconfig.ErrCodeConflict:
			c.JSON(http.StatusConflict, gin.H{
				"error": gin.H{
					"code":    string(rerr.Code),
					"message": rerr.Message,
					"field":   rerr.Field,
				},
			})
		case runtimeconfig.ErrCodeValidation:
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": gin.H{
					"code":    string(rerr.Code),
					"message": rerr.Message,
					"field":   rerr.Field,
				},
			})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": gin.H{
					"code":    "internal_error",
					"message": rerr.Message,
				},
			})
		}
		return
	}

	// 未知错误
	c.JSON(http.StatusInternalServerError, gin.H{
		"error": gin.H{
			"code":    "internal_error",
			"message": err.Error(),
		},
	})
}