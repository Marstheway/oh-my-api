package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/errors"
)

func Auth(cfg *config.Config) gin.HandlerFunc {
	validKeys := cfg.Inbound.Auth.Keys

	return func(c *gin.Context) {
		key := extractAPIKey(c)
		if key == "" {
			unauthorized(c)
			return
		}

		name := findKeyName(key, validKeys)
		if name == "" {
			unauthorized(c)
			return
		}

		c.Set("key_name", name)
		c.Next()
	}
}

func findKeyName(key string, validKeys []config.KeyConfig) string {
	for _, vk := range validKeys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(vk.Key)) == 1 {
			return vk.Name
		}
	}
	return ""
}

func unauthorized(c *gin.Context) {
	inbound := detectInboundProtocol(c)
	errors.WriteError(c, inbound, http.StatusUnauthorized,
		errors.ErrInvalidAPIKey, "invalid api key")
	c.Abort()
}

func extractAPIKey(c *gin.Context) string {
	if auth := c.GetHeader("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimSpace(auth[7:])
		}
		return auth
	}

	if key := c.GetHeader("x-api-key"); key != "" {
		return key
	}

	return c.Query("api_key")
}

func detectInboundProtocol(c *gin.Context) errors.Protocol {
	if strings.HasPrefix(c.Request.URL.Path, "/v1/messages") {
		return errors.ProtocolAnthropic
	}
	return errors.ProtocolOpenAI
}
