package router

import (
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/adminui"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/handler"
	"github.com/Marstheway/oh-my-api/internal/middleware"
	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/gin-gonic/gin"
)

func Setup(r *gin.Engine, keyProvider middleware.KeyProvider) {
	r.Use(middleware.Recovery())
	r.Use(middleware.Logger())

	// 静默丢弃未匹配路由（公网扫描/探测）
	r.NoRoute(func(c *gin.Context) {
		c.Status(404)
	})

	v1 := r.Group("/v1")
	v1.Use(middleware.Auth(keyProvider))

	v1.POST("/chat/completions", setProtocol("openai.chat"), handler.Chat)
	v1.POST("/messages", setProtocol("anthropic.messages"), handler.Messages)
	v1.POST("/responses", setProtocol("openai.responses"), handler.Responses)
	v1.POST("/embeddings", setProtocol("openai.embeddings"), handler.Embeddings)
	v1.GET("/models", setProtocol("openai.chat"), handler.Models)
}

func setProtocol(protocol string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("protocol", protocol)
		c.Next()
	}
}

// SetupAdmin 注册 /admin 路由组
// 登录页面始终可访问；管理功能仅在 password 非空时启用
func SetupAdmin(r *gin.Engine, cfg *config.Config, manager *runtimeconfig.Manager, querier stats.Querier) {
	hasPassword := cfg.Server.Admin.Password != ""

	if hasPassword {
		slog.Info("admin web UI enabled", "path", "/admin/")
	} else {
		slog.Info("admin web UI login page enabled (password not configured)", "path", "/admin/")
	}

	// 登录路由（无需认证）
	loginHandler := handler.NewAdminLoginHandler(cfg)
	r.GET("/admin/login", loginHandler.LoginPage)
	r.POST("/admin/login", loginHandler.Login)
	r.POST("/admin/logout", loginHandler.Logout)

	// 静态资源（从 adminui.FS 提取 static 子目录）
	staticFS, err := fs.Sub(adminui.FS, "static")
	if err != nil {
		panic("failed to extract static directory from adminui.FS: " + err.Error())
	}
	r.StaticFS("/admin/static", http.FS(staticFS))

	// 无密码时仅提供登录页面和静态资源，不注册管理功能路由
	if !hasPassword {
		return
	}

	// 根路径重定向到 admin（仅密码配置后注册）
	r.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/admin/")
	})

	// 获取 session secret（从 password 自动派生）
	sessionSecret := cfg.Server.Admin.GetSessionSecret()

	// 需要认证的管理路由
	admin := r.Group("/admin")
	admin.Use(middleware.AdminAuthOrSession(cfg.Server.Admin.Password, sessionSecret))

	// UI handlers
	uiHandler := handler.NewAdminUIHandler()
	admin.GET("/", uiHandler.Dashboard)
	admin.GET("/model-groups", uiHandler.ModelGroups)
	admin.GET("/redirects", uiHandler.Redirects)
	admin.GET("/providers", uiHandler.Providers)

	// Runtime config handlers
	h := handler.NewAdminRuntimeConfigHandler(manager)

	// Model group CRUD
	admin.GET("/runtime-config/draft", h.GetDraft)
	admin.GET("/runtime-config/draft/model-groups/:name", h.GetModelGroup)
	admin.POST("/runtime-config/draft/model-groups", h.CreateModelGroup)
	admin.PUT("/runtime-config/draft/model-groups/:name", h.UpdateModelGroup)
	admin.DELETE("/runtime-config/draft/model-groups/:name", h.DeleteModelGroup)

	// Redirect CRUD
	admin.GET("/runtime-config/draft/redirects", h.GetRedirects)
	admin.GET("/runtime-config/draft/redirects/:alias", h.GetRedirect)
	admin.POST("/runtime-config/draft/redirects", h.CreateRedirect)
	admin.PUT("/runtime-config/draft/redirects/:alias", h.UpdateRedirect)
	admin.DELETE("/runtime-config/draft/redirects/:alias", h.DeleteRedirect)

	// Provider CRUD
	admin.GET("/runtime-config/draft/providers", h.GetProviders)
	admin.GET("/runtime-config/draft/providers/:name", h.GetProvider)
	admin.POST("/runtime-config/draft/providers", h.CreateProvider)
	admin.PUT("/runtime-config/draft/providers/:name", h.UpdateProvider)
	admin.DELETE("/runtime-config/draft/providers/:name", h.DeleteProvider)

	// Auth Key CRUD
	admin.GET("/runtime-config/draft/auth-keys", h.GetAuthKeys)
	admin.GET("/runtime-config/draft/auth-keys/:name", h.GetAuthKey)
	admin.POST("/runtime-config/draft/auth-keys", h.CreateAuthKey)
	admin.PUT("/runtime-config/draft/auth-keys/:name", h.UpdateAuthKey)
	admin.DELETE("/runtime-config/draft/auth-keys/:name", h.DeleteAuthKey)

	admin.POST("/runtime-config/apply", h.Apply)

	// Stats API
	statsHandler := handler.NewAdminStatsHandler(querier)
	admin.GET("/stats", statsHandler.GetStats)
}
