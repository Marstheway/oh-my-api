package middleware

import (
	"github.com/gin-gonic/gin"
)

func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		// 静默丢弃 404 请求（公网扫描/探测）
		if c.Writer.Status() == 404 {
			return
		}
	}
}
