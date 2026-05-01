package router

import (
	"github.com/gin-gonic/gin"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/handler"
	"github.com/Marstheway/oh-my-api/internal/middleware"
)

func Setup(r *gin.Engine, cfg *config.Config) {
	r.Use(middleware.Recovery())
	r.Use(middleware.Logger())

	// 静默丢弃未匹配路由（公网扫描/探测）
	r.NoRoute(func(c *gin.Context) {
		c.Status(404)
	})

	v1 := r.Group("/v1")
	v1.Use(middleware.Auth(cfg))

	v1.POST("/chat/completions", setProtocol("openai"), handler.Chat)
	v1.POST("/messages", setProtocol("anthropic"), handler.Messages)
	v1.POST("/responses", setProtocol("openai.response"), handler.Responses)
	v1.GET("/models", setProtocol("openai"), handler.Models)
}

func setProtocol(protocol string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("protocol", protocol)
		c.Next()
	}
}
