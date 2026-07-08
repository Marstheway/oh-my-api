package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	HeaderAdminPassword = "X-Admin-Password"
)

// AdminAuth 创建一个中间件，仅支持 header 认证（用于 API）
// 这是原始的认证方式，保持向后兼容
func AdminAuth(password string) gin.HandlerFunc {
	return func(c *gin.Context) {
		passed := c.GetHeader(HeaderAdminPassword)
		if passed == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"code":    "unauthorized",
					"message": "missing admin password",
				},
			})
			c.Abort()
			return
		}

		if subtle.ConstantTimeCompare([]byte(passed), []byte(password)) == 1 {
			c.Next()
			return
		}

		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{
				"code":    "unauthorized",
				"message": "invalid admin password",
			},
		})
		c.Abort()
	}
}

// AdminAuthOrSession 创建一个中间件，支持 header 或 session cookie 两种认证方式
// 优先尝试 header 认证（用于 API），如果失败则尝试 session cookie（用于 Web UI）
func AdminAuthOrSession(password string, sessionSecret string) gin.HandlerFunc {
	sessionConfig := AdminSessionConfig{
		Secret:     sessionSecret,
		CookieName: "admin_session",
		MaxAge:     24 * time.Hour,
	}

	return func(c *gin.Context) {
		// 1. 优先尝试 header 认证
		passed := c.GetHeader(HeaderAdminPassword)
		if passed != "" {
			if subtle.ConstantTimeCompare([]byte(passed), []byte(password)) == 1 {
				c.Next()
				return
			}
			// header 认证失败，返回 401（不尝试 session）
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"code":    "unauthorized",
					"message": "invalid admin password",
				},
			})
			c.Abort()
			return
		}

		// 2. 尝试 session cookie 认证
		cookieValue, err := c.Cookie(sessionConfig.CookieName)
		if err != nil || cookieValue == "" {
			handleAdminAuthUnauthorized(c)
			return
		}

		// 验证 session cookie
		if !validateSession(cookieValue, sessionConfig) {
			handleAdminAuthUnauthorized(c)
			return
		}

		c.Set("admin_authenticated", true)
		c.Next()
	}
}

// handleAdminAuthUnauthorized 处理未授权请求
// HTML 请求重定向到 /admin/login（携带 redirect 参数），API 请求返回 401
func handleAdminAuthUnauthorized(c *gin.Context) {
	// 判断是否为 HTML 请求（仅通过 Accept header 判断）
	accept := c.GetHeader("Accept")
	isHTMLRequest := strings.Contains(accept, "text/html")

	if isHTMLRequest {
		// 重定向到登录页，携带当前路径作为 redirect 参数
		currentPath := c.Request.URL.Path
		// 安全检查：只允许 /admin/ 开头的路径，防止开放重定向漏洞
		if strings.HasPrefix(currentPath, "/admin/") {
			c.Redirect(http.StatusFound, "/admin/login?redirect="+currentPath)
		} else {
			c.Redirect(http.StatusFound, "/admin/login")
		}
		c.Abort()
		return
	}

	// API 请求返回 401
	c.JSON(http.StatusUnauthorized, gin.H{
		"error": gin.H{
			"code":    "unauthorized",
			"message": "missing admin password or session",
		},
	})
	c.Abort()
}

// validateSession 验证 session cookie 值
// 格式: base64(expires_at) + "." + base64(signature)
func validateSession(cookieValue string, config AdminSessionConfig) bool {
	parts := strings.SplitN(cookieValue, ".", 2)
	if len(parts) != 2 {
		return false
	}

	expiresAtB64 := parts[0]
	signatureB64 := parts[1]

	// base64 解码（使用 URL 安全编码）
	expiresAtBytes, err := base64.RawURLEncoding.DecodeString(expiresAtB64)
	if err != nil {
		return false
	}

	signature, err := base64.RawURLEncoding.DecodeString(signatureB64)
	if err != nil {
		return false
	}

	// 使用 secret 重新计算签名，与提交的签名比较（常量时间比较）
	mac := hmac.New(sha256.New, []byte(config.Secret))
	mac.Write(expiresAtBytes)
	expectedSignature := mac.Sum(nil)

	if subtle.ConstantTimeCompare(signature, expectedSignature) != 1 {
		return false
	}

	// 检查 expires_at > time.Now().Unix()（未过期）
	expiresAt, err := strconv.ParseInt(string(expiresAtBytes), 10, 64)
	if err != nil {
		return false
	}

	if expiresAt <= time.Now().Unix() {
		return false
	}

	return true
}
