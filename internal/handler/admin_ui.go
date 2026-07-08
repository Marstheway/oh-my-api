package handler

import (
	"html/template"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/adminui"
	"github.com/gin-gonic/gin"
)

// AdminUIHandler 处理 Admin Web UI 页面渲染
type AdminUIHandler struct {
	tmpl *template.Template
}

// NewAdminUIHandler 创建 Admin UI handler
func NewAdminUIHandler() *AdminUIHandler {
	// 读取布局模板
	layoutContent, err := adminui.FS.ReadFile("templates/layout.html")
	if err != nil {
		panic("failed to read layout template: " + err.Error())
	}

	// 解析模板
	tmpl, err := template.New("layout.html").Parse(string(layoutContent))
	if err != nil {
		panic("failed to parse layout template: " + err.Error())
	}

	// 解析共享组件到基础模板
	draftBarContent, err := adminui.FS.ReadFile("templates/components/draft_bar.html")
	if err != nil {
		panic("failed to read draft_bar component: " + err.Error())
	}
	tmpl, err = tmpl.Parse(string(draftBarContent))
	if err != nil {
		panic("failed to parse draft_bar component: " + err.Error())
	}

	return &AdminUIHandler{
		tmpl: tmpl,
	}
}

// Dashboard 渲染 Dashboard 页面
// GET /admin/
func (h *AdminUIHandler) Dashboard(c *gin.Context) {
	// 重新解析仅包含 dashboard 的模板集
	tmpl, err := h.tmpl.Clone()
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to clone template: %v", err)
		return
	}

	// 解析 dashboard 模板
	dashboardContent, err := adminui.FS.ReadFile("templates/dashboard.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to read dashboard template: %v", err)
		return
	}

	tmpl, err = tmpl.Parse(string(dashboardContent))
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to parse dashboard template: %v", err)
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	data := gin.H{
		"Authenticated": true,
		"Page":          "dashboard",
	}
	if err := tmpl.Execute(c.Writer, data); err != nil {
		c.String(http.StatusInternalServerError, "Failed to render dashboard page: %v", err)
	}
}

// ModelGroups 渲染 Model Groups 页面
// GET /admin/model-groups
func (h *AdminUIHandler) ModelGroups(c *gin.Context) {
	// 重新解析仅包含 model-groups 的模板集
	tmpl, err := h.tmpl.Clone()
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to clone template: %v", err)
		return
	}

	// 解析 model-groups 模板
	modelGroupsContent, err := adminui.FS.ReadFile("templates/model-groups.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to read model-groups template: %v", err)
		return
	}

	tmpl, err = tmpl.Parse(string(modelGroupsContent))
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to parse model-groups template: %v", err)
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	data := gin.H{
		"Authenticated": true,
		"Page":          "model-groups",
	}
	if err := tmpl.Execute(c.Writer, data); err != nil {
		c.String(http.StatusInternalServerError, "Failed to render model-groups page: %v", err)
	}
}

// Redirects 渲染 Redirects 页面
// GET /admin/redirects
func (h *AdminUIHandler) Redirects(c *gin.Context) {
	// 重新解析仅包含 redirects 的模板集
	tmpl, err := h.tmpl.Clone()
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to clone template: %v", err)
		return
	}

	// 解析 redirects 模板
	redirectsContent, err := adminui.FS.ReadFile("templates/redirects.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to read redirects template: %v", err)
		return
	}

	tmpl, err = tmpl.Parse(string(redirectsContent))
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to parse redirects template: %v", err)
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	data := gin.H{
		"Authenticated": true,
		"Page":          "redirects",
	}
	if err := tmpl.Execute(c.Writer, data); err != nil {
		c.String(http.StatusInternalServerError, "Failed to render redirects page: %v", err)
	}
}

// Providers 渲染 Providers 页面
// GET /admin/providers
func (h *AdminUIHandler) Providers(c *gin.Context) {
	// 重新解析仅包含 providers 的模板集
	tmpl, err := h.tmpl.Clone()
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to clone template: %v", err)
		return
	}

	// 解析 providers 模板
	providersContent, err := adminui.FS.ReadFile("templates/providers.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to read providers template: %v", err)
		return
	}

	tmpl, err = tmpl.Parse(string(providersContent))
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to parse providers template: %v", err)
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	data := gin.H{
		"Authenticated": true,
		"Page":          "providers",
	}
	if err := tmpl.Execute(c.Writer, data); err != nil {
		c.String(http.StatusInternalServerError, "Failed to render providers page: %v", err)
	}
}
