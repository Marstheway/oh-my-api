package handler

import (
	"crypto/subtle"
	"html/template"
	"net/http"
	"time"

	"github.com/Marstheway/oh-my-api/internal/adminui"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/middleware"
	"github.com/gin-gonic/gin"
)

// AdminLoginHandler 处理登录页面和登录请求
type AdminLoginHandler struct {
	password      string
	sessionConfig middleware.AdminSessionConfig
	tmpl          *template.Template
}

// NewAdminLoginHandler 创建登录处理器
func NewAdminLoginHandler(cfg *config.Config) *AdminLoginHandler {
	// 读取模板文件
	layoutContent, err := adminui.FS.ReadFile("templates/layout.html")
	if err != nil {
		panic("failed to read layout template: " + err.Error())
	}

	loginContent, err := adminui.FS.ReadFile("templates/login.html")
	if err != nil {
		panic("failed to read login template: " + err.Error())
	}

	// 解析模板
	tmpl, err := template.New("layout.html").Parse(string(layoutContent))
	if err != nil {
		panic("failed to parse layout template: " + err.Error())
	}

	_, err = tmpl.Parse(string(loginContent))
	if err != nil {
		panic("failed to parse login template: " + err.Error())
	}

	var sessionConfig middleware.AdminSessionConfig
	if cfg.Server.Admin.Password != "" {
		sessionConfig = middleware.AdminSessionConfig{
			Secret:     cfg.Server.Admin.GetSessionSecret(),
			CookieName: "admin_session",
			MaxAge:     24 * time.Hour,
			HttpOnly:   true,
			SameSite:   http.SameSiteLaxMode,
		}
	}

	return &AdminLoginHandler{
		password:      cfg.Server.Admin.Password,
		sessionConfig: sessionConfig,
		tmpl:          tmpl,
	}
}

// LoginPage 渲染登录页面
// GET /admin/login
func (h *AdminLoginHandler) LoginPage(c *gin.Context) {
	// 检查是否已有有效 session
	if _, exists := c.Get("admin_authenticated"); exists {
		// 已登录，重定向到管理页面
		c.Redirect(http.StatusFound, "/admin/")
		c.Abort()
		return
	}

	// 渲染登录页面
	c.Header("Content-Type", "text/html; charset=utf-8")
	data := gin.H{
		"Authenticated": false,
		"HasPassword":   h.password != "",
	}
	if err := h.tmpl.Execute(c.Writer, data); err != nil {
		c.String(http.StatusInternalServerError, "Failed to render login page: %v", err)
	}
}

// Login 处理登录请求
// POST /admin/login
func (h *AdminLoginHandler) Login(c *gin.Context) {
	var req struct {
		Password string `json:"password"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "invalid_request",
				"message": "invalid request body",
			},
		})
		return
	}

	// 无密码配置时拒绝登录
	if h.password == "" {
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"code":    "password_not_configured",
				"message": "admin password not configured, please set server.admin.password in config.yaml",
			},
		})
		return
	}

	// 使用常量时间比较密码
	if subtle.ConstantTimeCompare([]byte(req.Password), []byte(h.password)) != 1 {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{
				"code":    "unauthorized",
				"message": "invalid password",
			},
		})
		return
	}

	// 生成 session cookie
	sessionValue := middleware.GenerateSession(h.sessionConfig, time.Now())

	// 设置 cookie
	c.SetSameSite(h.sessionConfig.SameSite)
	c.SetCookie(
		h.sessionConfig.CookieName,
		sessionValue,
		int(h.sessionConfig.MaxAge.Seconds()),
		"/",
		"",
		h.sessionConfig.Secure,
		h.sessionConfig.HttpOnly,
	)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
	})
}

// Logout 处理登出请求
// POST /admin/logout
// 清除 session cookie。无需认证（session 过期也能清除残留 cookie）。
func (h *AdminLoginHandler) Logout(c *gin.Context) {
	// MaxAge=-1 立即删除 cookie；path 必须与登录时一致（"/"）
	c.SetSameSite(h.sessionConfig.SameSite)
	c.SetCookie(
		h.sessionConfig.CookieName,
		"",
		-1,
		"/",
		"",
		h.sessionConfig.Secure,
		h.sessionConfig.HttpOnly,
	)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
	})
}
