package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/adaptor"
	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/catalog"
	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/gin-gonic/gin"
)

const (
	httpTestTimeout      = 15 * time.Second
	cascadeTestTimeout   = 45 * time.Second
	cascadeTestMaxTokens = 32
	cascadeTestProtocol  = "openai.chat"
	cascadeTestDummyURL  = "http://cascade.local/v1/chat/completions"
)

// AdminRuntimeConfigHandler 处理 /admin/runtime-config 端点
type AdminRuntimeConfigHandler struct {
	manager     *runtimeconfig.Manager
	cascadeHubs *cascade.HubRegistry
}

// NewAdminRuntimeConfigHandler 创建 handler
func NewAdminRuntimeConfigHandler(manager *runtimeconfig.Manager) *AdminRuntimeConfigHandler {
	return &AdminRuntimeConfigHandler{
		manager: manager,
	}
}

// SetCascadeHubs wires the live hub registry used by cascade leaf tests.
func (h *AdminRuntimeConfigHandler) SetCascadeHubs(hubs *cascade.HubRegistry) {
	h.cascadeHubs = hubs
}

// DraftResponse 表示完整 draft 视图（脱敏）
type DraftResponse struct {
	ModelGroups []runtimeconfig.ModelGroupOutput   `json:"model_groups"`
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

	// 与 /v1/models 相同：优先共享 CatalogSource（含 probe + models.dev fallback），
	// 避免 Admin 只读 catalog_upstream.json 而丢掉 models.dev 回退。
	lookup := contextLengthLookup()

	groups := h.manager.ListModelGroups()
	output := make([]runtimeconfig.ModelGroupOutput, len(groups))
	for i, g := range groups {
		output[i] = runtimeconfig.ToOutput(g)
		// 计算 computed_context_length（仅当无配置值时）
		if output[i].ModelMetadata == nil || output[i].ModelMetadata.ContextLength == nil {
			computed := ResolveContextLength(g.Name, draftResolver, lookup)
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
			ctx := ResolveContextLength(redirects[i].ResolvedGroup, draftResolver, lookup)
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

// GetCascade 返回 draft cascade 配置（含明文 shared token，供 Admin UI 回显编辑）。
// GET /admin/runtime-config/draft/cascade
func (h *AdminRuntimeConfigHandler) GetCascade(c *gin.Context) {
	c.JSON(http.StatusOK, h.manager.GetCascade())
}

// UpdateCascade 将请求的角色应用到 draft cascade 配置。
// PUT /admin/runtime-config/draft/cascade
func (h *AdminRuntimeConfigHandler) UpdateCascade(c *gin.Context) {
	var input runtimeconfig.CascadeConfigInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid request body: " + err.Error(),
			},
		})
		return
	}

	if err := h.manager.UpdateCascade(&input); err != nil {
		h.writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, h.manager.GetCascade())
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


// ReorderModelGroups PUT /admin/runtime-config/draft/model-groups/order
func (h *AdminRuntimeConfigHandler) ReorderModelGroups(c *gin.Context) {
	var body struct {
		Names []string `json:"names"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid request body: " + err.Error(),
			},
		})
		return
	}
	if err := h.manager.ReorderModelGroups(body.Names); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
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

// GetCatalog 返回脱敏、规范化的上游模型目录视图（只读，供 Model Group 弹窗自动补全）。
// GET /admin/runtime-config/catalog
// 无 catalog source 时返回空且 stale 的目录响应，不序列化原始 ProviderEntry。
func (h *AdminRuntimeConfigHandler) GetCatalog(c *gin.Context) {
	if catalogSrc == nil {
		c.JSON(http.StatusOK, catalog.EmptyCatalogView())
		return
	}
	c.JSON(http.StatusOK, catalogSrc.CatalogView())
}


// ReorderRedirects PUT /admin/runtime-config/draft/redirects/order
func (h *AdminRuntimeConfigHandler) ReorderRedirects(c *gin.Context) {
	var body struct {
		Sources []string `json:"sources"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid request body: " + err.Error(),
			},
		})
		return
	}
	if err := h.manager.ReorderRedirects(body.Sources); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
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

// GetRules 返回 draft 中的 rules（顺序即配置顺序）。
// GET /admin/runtime-config/draft/rules
func (h *AdminRuntimeConfigHandler) GetRules(c *gin.Context) {
	rules := h.manager.ListRules()
	output := make([]runtimeconfig.RuleOutput, len(rules))
	for i, rule := range rules {
		output[i] = runtimeconfig.ToRuleOutput(rule)
	}
	c.JSON(http.StatusOK, gin.H{"rules": output})
}

// ReplaceRules 整表替换 draft rules。
// PUT /admin/runtime-config/draft/rules
// body 必须含 rules 键：[] 表示清空；缺 rules 键或 rules 为 null 返回 400，draft 不变。
func (h *AdminRuntimeConfigHandler) ReplaceRules(c *gin.Context) {
	var req runtimeconfig.RulesInput
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid request body: " + err.Error(),
			},
		})
		return
	}

	// 显式 nil 检查：{"rules":[]} 是合法清空，binding:"required" 会误拒绝
	if req.Rules == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "missing rules key: body must contain a rules array (use [] to clear)",
			},
		})
		return
	}

	if err := h.manager.ReplaceRules(req.Rules); err != nil {
		h.writeError(c, err)
		return
	}

	rules := h.manager.ListRules()
	output := make([]runtimeconfig.RuleOutput, len(rules))
	for i, rule := range rules {
		output[i] = runtimeconfig.ToRuleOutput(rule)
	}
	c.JSON(http.StatusOK, gin.H{"rules": output})
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
					"field":   rerr.Field,
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

// testModelRequest 表示模型测试请求
type testModelRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// testModelResponse 表示模型测试结果
type testModelResponse struct {
	Success    bool   `json:"success"`
	LatencyMs  int64  `json:"latency_ms"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// TestModel 测试模型连通性
// POST /admin/runtime-config/test-model
func (h *AdminRuntimeConfigHandler) TestModel(c *gin.Context) {
	var req testModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "bad_request", "message": "invalid request: " + err.Error()},
		})
		return
	}

	if req.Provider == "" || req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "bad_request", "message": "provider and model are required"},
		})
		return
	}

	cfg := h.manager.GetDraft()
	providerCfg, ok := cfg.Providers.Items[req.Provider]
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{"code": "not_found", "message": fmt.Sprintf("provider '%s' not found", req.Provider)},
		})
		return
	}

	if providerCfg.Cascade != nil && providerCfg.Cascade.Enabled {
		c.JSON(http.StatusOK, h.testCascadeModel(req.Provider, providerCfg, req.Model))
		return
	}

	// 探测该 provider 全部可达 endpoint（不评 rules；无 client-model/key 上下文）
	endpoints := collectTestEndpoints(providerCfg)
	if len(endpoints) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "bad_request", "message": "no endpoint configured for this provider"},
		})
		return
	}

	// 构造最小测试请求，body 按协议转换在 doTestEndpoint 内完成
	testReq := &dto.ChatCompletionRequest{
		Model: req.Model,
		Messages: []dto.Message{
			{Role: "user", Content: "hi"},
		},
		MaxTokens: 64,
	}

	timeout := httpTestTimeout
	client := provider.NewClient(map[string]config.ProviderConfig{
		req.Provider: providerCfg,
	}, 0, 0, 0)

	testModel := req.Provider + "/" + req.Model
	slog.Info("testing model connectivity",
		"test_model", testModel,
		"endpoints", len(endpoints),
	)

	// 按顺序测试所有 endpoint，遇到第一个成功就返回
	var lastResult testModelResponse
	for i, ep := range endpoints {
		epLog := slog.With("test_model", testModel, "endpoint_idx", i+1, "total", len(endpoints))

		result := doTestEndpoint(client, req.Provider, ep.url, ep.protocol, providerCfg.APIKey, testReq, timeout)

		if result.Success {
			epLog.Info("model test success",
				"url", ep.url,
				"protocol", ep.protocol,
				"latency_ms", result.LatencyMs,
				"status_code", result.StatusCode,
			)
			c.JSON(http.StatusOK, result)
			return
		}

		epLog.Warn("model test failed",
			"url", ep.url,
			"protocol", ep.protocol,
			"latency_ms", result.LatencyMs,
			"status_code", result.StatusCode,
			"error", result.Error,
		)
		lastResult = result
	}

	// 所有 endpoint 都失败
	c.JSON(http.StatusOK, lastResult)
}

// testEndpoint 表示一个待测试的 endpoint
type testEndpoint struct {
	url      string
	protocol string
}

// collectTestEndpoints 收集所有需要测试的 endpoint。
func collectTestEndpoints(providerCfg config.ProviderConfig) []testEndpoint {
	var rawEndpoints []struct {
		url       string
		protocols []string
	}

	if len(providerCfg.Endpoints) > 0 {
		for _, ep := range providerCfg.Endpoints {
			protos := ep.Protocols
			if len(protos) == 0 {
				protos = providerCfg.Protocols
			}
			rawEndpoints = append(rawEndpoints, struct {
				url       string
				protocols []string
			}{url: ep.URL, protocols: protos})
		}
	} else if providerCfg.Endpoint != "" {
		rawEndpoints = append(rawEndpoints, struct {
			url       string
			protocols []string
		}{url: providerCfg.Endpoint, protocols: providerCfg.Protocols})
	}

	var result []testEndpoint
	for _, raw := range rawEndpoints {
		if len(raw.protocols) == 0 {
			result = append(result, testEndpoint{url: raw.url, protocol: ""})
			continue
		}
		for _, proto := range raw.protocols {
			result = append(result, testEndpoint{url: raw.url, protocol: strings.TrimSpace(proto)})
		}
	}

	return result
}

func (h *AdminRuntimeConfigHandler) testCascadeModel(providerName string, providerCfg config.ProviderConfig, model string) testModelResponse {
	if h.cascadeHubs == nil {
		return testModelResponse{Success: false, Error: "cascade hub not configured"}
	}
	hub := h.cascadeHubs.Get()
	if hub == nil || !hub.Configured() {
		return testModelResponse{Success: false, Error: "cascade hub not configured"}
	}
	if _, ok := hub.Session(); !ok {
		return testModelResponse{Success: false, Error: "cascade spoke not connected"}
	}

	client := provider.NewClient(map[string]config.ProviderConfig{
		providerName: providerCfg,
	}, 0, 0, 0)
	client.SetCascadeHubRegistry(h.cascadeHubs)

	testReq := &dto.ChatCompletionRequest{
		Model:  model,
		Stream: true,
		Messages: []dto.Message{
			{Role: "user", Content: "hi"},
		},
		MaxTokens: cascadeTestMaxTokens,
	}

	chatCodec, err := codec.Get(codec.FormatOpenAIChat)
	if err != nil {
		return testModelResponse{Success: false, Error: err.Error()}
	}
	bodyBytes, err := chatCodec.EncodeRequest(codec.FormatOpenAIChat, testReq, testReq.Model, false)
	if err != nil {
		return testModelResponse{Success: false, Error: fmt.Sprintf("encode request: %s", err)}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cascadeTestTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cascadeTestDummyURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return testModelResponse{Success: false, Error: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bodyBytes)), nil
	}

	testModel := providerName + "/" + model
	slog.Info("testing cascade model connectivity",
		"test_model", testModel,
		"protocol", cascadeTestProtocol,
	)

	start := time.Now()
	resp, err := client.DoWithMeta(providerName, provider.RequestMeta{
		UpstreamModel:    model,
		OutboundProtocol: cascadeTestProtocol,
	}, httpReq)
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		slog.Warn("cascade model test failed",
			"test_model", testModel,
			"latency_ms", latencyMs,
			"error", err.Error(),
		)
		return testModelResponse{Success: false, LatencyMs: latencyMs, Error: err.Error()}
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if readErr != nil && len(body) == 0 {
		return testModelResponse{
			Success:    false,
			LatencyMs:  latencyMs,
			StatusCode: resp.StatusCode,
			Error:      fmt.Sprintf("HTTP %d: read body failed: %s", resp.StatusCode, readErr),
		}
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		slog.Info("cascade model test success",
			"test_model", testModel,
			"latency_ms", latencyMs,
			"status_code", resp.StatusCode,
		)
		return testModelResponse{Success: true, LatencyMs: latencyMs, StatusCode: resp.StatusCode}
	}

	errMsg := strings.TrimSpace(string(body))
	runes := []rune(errMsg)
	if len(runes) > 500 {
		errMsg = string(runes[:500])
	}
	slog.Warn("cascade model test failed",
		"test_model", testModel,
		"latency_ms", latencyMs,
		"status_code", resp.StatusCode,
		"error", errMsg,
	)
	return testModelResponse{
		Success:    false,
		LatencyMs:  latencyMs,
		StatusCode: resp.StatusCode,
		Error:      fmt.Sprintf("HTTP %d: %s", resp.StatusCode, errMsg),
	}
}

// doTestEndpoint 向单个 endpoint 发送测试请求
func doTestEndpoint(client *provider.Client, providerName, endpointURL, protocol, apiKey string, testReq *dto.ChatCompletionRequest, timeout time.Duration) testModelResponse {
	// protocol 为空时按 openai.chat 处理，与 BuildURL 的 default 分支一致
	outboundFormat := codec.FormatOpenAIChat
	if protocol != "" {
		f, err := codec.NormalizeProviderFormat(protocol)
		if err != nil {
			return testModelResponse{Success: false, Error: fmt.Sprintf("invalid protocol %q: %s", protocol, err)}
		}
		outboundFormat = f
	}

	chatCodec, err := codec.Get(codec.FormatOpenAIChat)
	if err != nil {
		return testModelResponse{Success: false, Error: err.Error()}
	}

	bodyBytes, err := chatCodec.EncodeRequest(outboundFormat, testReq, testReq.Model, false)
	if err != nil {
		return testModelResponse{Success: false, Error: fmt.Sprintf("encode request: %s", err)}
	}

	requestURL := adaptor.BuildURL(endpointURL, adaptor.Protocol(protocol))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return testModelResponse{Success: false, Error: err.Error()}
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if adaptor.Protocol(protocol) == adaptor.ProtocolAnthropic {
		httpReq.Header.Set("x-api-key", apiKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	start := time.Now()
	resp, err := client.Do(providerName, httpReq)
	latencyMs := time.Since(start).Milliseconds()

	if err != nil {
		return testModelResponse{Success: false, LatencyMs: latencyMs, Error: err.Error()}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return testModelResponse{
			Success:    false,
			LatencyMs:  latencyMs,
			StatusCode: resp.StatusCode,
			Error:      fmt.Sprintf("HTTP %d: read body failed: %s", resp.StatusCode, err),
		}
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return testModelResponse{Success: true, LatencyMs: latencyMs, StatusCode: resp.StatusCode}
	}

	errMsg := strings.TrimSpace(string(body))
	runes := []rune(errMsg)
	if len(runes) > 500 {
		errMsg = string(runes[:500])
	}
	return testModelResponse{
		Success:    false,
		LatencyMs:  latencyMs,
		StatusCode: resp.StatusCode,
		Error:      fmt.Sprintf("HTTP %d: %s", resp.StatusCode, errMsg),
	}
}
