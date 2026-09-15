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

// AdminSessionConfig 定义 Session Cookie 认证配置
type AdminSessionConfig struct {
	Secret     string        // HMAC 签名密钥
	CookieName string        // Cookie 名称，默认 "admin_session"
	MaxAge     time.Duration // Session 有效期，默认 24h
	Secure     bool          // 是否设置 Secure 属性
	HttpOnly   bool          // 是否设置 HttpOnly 属性，默认 true
	SameSite   http.SameSite // SameSite 属性，默认 Lax
}

// AdminSession 返回一个 Gin 中间件，用于验证 Session Cookie
// 验证成功：设置 c.Set("admin_authenticated", true) 并继续
// 验证失败：HTML 请求重定向到 /admin/login，API 请求返回 401
func AdminSession(config AdminSessionConfig) gin.HandlerFunc {
	// 设置默认值
	if config.CookieName == "" {
		config.CookieName = "admin_session"
	}
	if config.MaxAge == 0 {
		config.MaxAge = 24 * time.Hour
	}
	if config.SameSite == 0 {
		config.SameSite = http.SameSiteLaxMode
	}

	return func(c *gin.Context) {
		// 1. 从请求中读取 cookie
		cookieValue, err := c.Cookie(config.CookieName)
		if err != nil || cookieValue == "" {
			handleUnauthorized(c)
			return
		}

		// 2. 解析 cookie 值，分离 expires_at 和 signature
		parts := strings.Split(cookieValue, ".")
		if len(parts) != 2 {
			handleUnauthorized(c)
			return
		}

		expiresAtB64 := parts[0]
		signatureB64 := parts[1]

		// 3. base64 解码（使用 URL 安全编码）
		expiresAtBytes, err := base64.RawURLEncoding.DecodeString(expiresAtB64)
		if err != nil {
			handleUnauthorized(c)
			return
		}

		signature, err := base64.RawURLEncoding.DecodeString(signatureB64)
		if err != nil {
			handleUnauthorized(c)
			return
		}

		// 4. 使用 secret 重新计算签名，与提交的签名比较（常量时间比较）
		mac := hmac.New(sha256.New, []byte(config.Secret))
		mac.Write(expiresAtBytes)
		expectedSignature := mac.Sum(nil)

		if subtle.ConstantTimeCompare(signature, expectedSignature) != 1 {
			handleUnauthorized(c)
			return
		}

		// 5. 检查 expires_at > time.Now().Unix()（未过期）
		expiresAt, err := strconv.ParseInt(string(expiresAtBytes), 10, 64)
		if err != nil {
			handleUnauthorized(c)
			return
		}

		if expiresAt <= time.Now().Unix() {
			handleUnauthorized(c)
			return
		}

		// 6. 有效：设置 c.Set("admin_authenticated", true) 并继续
		c.Set("admin_authenticated", true)
		c.Next()
	}
}

// GenerateSession 生成 Session Cookie 值
// 格式: base64(expires_at) + "." + base64(signature)
// expires_at = now + MaxAge
// signature = HMAC-SHA256(secret, expires_at)
func GenerateSession(config AdminSessionConfig, now time.Time) string {
	// 设置默认值
	if config.MaxAge == 0 {
		config.MaxAge = 24 * time.Hour
	}

	// 计算 expires_at
	expiresAt := now.Add(config.MaxAge).Unix()

	// 将 expires_at 转换为字节（数字字符串）
	expiresAtBytes := []byte(strconv.FormatInt(expiresAt, 10))

	// 计算签名
	mac := hmac.New(sha256.New, []byte(config.Secret))
	mac.Write(expiresAtBytes)
	signature := mac.Sum(nil)

	// base64 编码（使用 URL 安全编码，无填充）
	expiresAtB64 := base64.RawURLEncoding.EncodeToString(expiresAtBytes)
	signatureB64 := base64.RawURLEncoding.EncodeToString(signature)

	return expiresAtB64 + "." + signatureB64
}

// handleUnauthorized 处理未授权请求
// HTML 请求重定向到 /admin/login，API 请求返回 401
func handleUnauthorized(c *gin.Context) {
	// 判断是否为 HTML 请求（仅通过 Accept header 判断）
	accept := c.GetHeader("Accept")
	isHTMLRequest := strings.Contains(accept, "text/html")

	if isHTMLRequest {
		c.Redirect(http.StatusFound, "/admin/login")
		c.Abort()
		return
	}

	// API 请求返回 401
	c.JSON(http.StatusUnauthorized, gin.H{
		"error": gin.H{
			"code":    "unauthorized",
			"message": "invalid or expired session",
		},
	})
	c.Abort()
}
