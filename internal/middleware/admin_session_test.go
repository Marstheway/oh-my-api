package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestAdminSession_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)

	config := AdminSessionConfig{
		Secret:     "test-secret-key",
		CookieName: "admin_session",
		MaxAge:     24 * time.Hour,
		HttpOnly:   true,
	}

	// 生成有效的 session
	now := time.Now()
	sessionValue := GenerateSession(config, now)

	// 创建测试路由
	r := gin.New()
	r.Use(AdminSession(config))
	r.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 创建请求
	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(&http.Cookie{
		Name:  "admin_session",
		Value: sessionValue,
	})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	// 验证响应内容
	if w.Body.String() == "" {
		t.Error("Expected non-empty response body")
	}
}

func TestAdminSession_Expired(t *testing.T) {
	gin.SetMode(gin.TestMode)

	config := AdminSessionConfig{
		Secret:     "test-secret-key",
		CookieName: "admin_session",
		MaxAge:     24 * time.Hour,
	}

	// 生成过期的 session（使用过去的时间）
	pastTime := time.Now().Add(-25 * time.Hour)
	sessionValue := GenerateSession(config, pastTime)

	// 创建测试路由
	r := gin.New()
	r.Use(AdminSession(config))
	r.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 创建请求（Accept: application/json，应返回 401）
	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(&http.Cookie{
		Name:  "admin_session",
		Value: sessionValue,
	})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestAdminSession_InvalidSignature(t *testing.T) {
	gin.SetMode(gin.TestMode)

	config := AdminSessionConfig{
		Secret:     "test-secret-key",
		CookieName: "admin_session",
		MaxAge:     24 * time.Hour,
	}

	// 使用不同的 secret 生成 session
	wrongConfig := AdminSessionConfig{
		Secret: "wrong-secret",
	}
	now := time.Now()
	sessionValue := GenerateSession(wrongConfig, now)

	// 创建测试路由
	r := gin.New()
	r.Use(AdminSession(config))
	r.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 创建请求
	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(&http.Cookie{
		Name:  "admin_session",
		Value: sessionValue,
	})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestAdminSession_NoCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)

	config := AdminSessionConfig{
		Secret:     "test-secret-key",
		CookieName: "admin_session",
		MaxAge:     24 * time.Hour,
	}

	// 创建测试路由
	r := gin.New()
	r.Use(AdminSession(config))
	r.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 创建请求（不携带 cookie）
	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestAdminSession_HTMLRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)

	config := AdminSessionConfig{
		Secret:     "test-secret-key",
		CookieName: "admin_session",
		MaxAge:     24 * time.Hour,
	}

	// 创建测试路由
	r := gin.New()
	r.Use(AdminSession(config))
	r.GET("/admin/dashboard", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 创建 HTML 请求（不携带 cookie，Accept: text/html）
	req := httptest.NewRequest("GET", "/admin/dashboard", nil)
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("Expected status %d, got %d", http.StatusFound, w.Code)
	}

	// 验证重定向位置
	location := w.Header().Get("Location")
	if location != "/admin/login" {
		t.Errorf("Expected redirect to /admin/login, got %s", location)
	}
}

func TestAdminSession_APIPath_Returns401(t *testing.T) {
	gin.SetMode(gin.TestMode)

	config := AdminSessionConfig{
		Secret:     "test-secret-key",
		CookieName: "admin_session",
		MaxAge:     24 * time.Hour,
	}

	// 创建测试路由
	r := gin.New()
	r.Use(AdminSession(config))
	r.GET("/admin/stats", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 创建 API 请求（不携带 cookie，Accept: application/json）
	req := httptest.NewRequest("GET", "/admin/stats", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	// 应该返回 401 而不是重定向
	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestAdminSession_APIPath_NoAcceptHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	config := AdminSessionConfig{
		Secret:     "test-secret-key",
		CookieName: "admin_session",
		MaxAge:     24 * time.Hour,
	}

	// 创建测试路由
	r := gin.New()
	r.Use(AdminSession(config))
	r.GET("/admin/stats", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 创建请求（不携带 cookie，不设置 Accept header）
	req := httptest.NewRequest("GET", "/admin/stats", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	// 应该返回 401（因为没有 Accept: text/html）
	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestAdminSession_MalformedCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)

	config := AdminSessionConfig{
		Secret:     "test-secret-key",
		CookieName: "admin_session",
		MaxAge:     24 * time.Hour,
	}

	// 创建测试路由
	r := gin.New()
	r.Use(AdminSession(config))
	r.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	tests := []struct {
		name   string
		cookie string
	}{
		{
			name:   "missing separator",
			cookie: "invalidcookie",
		},
		{
			name:   "too many separators",
			cookie: "a.b.c",
		},
		{
			name:   "invalid base64",
			cookie: "!!!.!!!",
		},
		{
			name:   "empty value",
			cookie: ".",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/protected", nil)
			req.Header.Set("Accept", "application/json")
			req.AddCookie(&http.Cookie{
				Name:  "admin_session",
				Value: tt.cookie,
			})
			w := httptest.NewRecorder()

			r.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("Expected status %d, got %d", http.StatusUnauthorized, w.Code)
			}
		})
	}
}

func TestGenerateSession_Format(t *testing.T) {
	config := AdminSessionConfig{
		Secret: "test-secret",
		MaxAge: 24 * time.Hour,
	}

	now := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	sessionValue := GenerateSession(config, now)

	// 验证格式：应该包含一个点号
	dotCount := 0
	for _, ch := range sessionValue {
		if ch == '.' {
			dotCount++
		}
	}

	if dotCount != 1 {
		t.Errorf("Expected exactly one separator, got %d", dotCount)
	}

	// 验证各部分都是有效的 base64
	// 这里只验证不 panic，实际格式在集成测试中验证
	if sessionValue == "" {
		t.Error("Expected non-empty session value")
	}
}

func TestAdminSession_CustomConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	config := AdminSessionConfig{
		Secret:     "custom-secret",
		CookieName: "custom_session",
		MaxAge:     12 * time.Hour,
		Secure:     true,
		HttpOnly:   true,
		SameSite:   http.SameSiteStrictMode,
	}

	// 生成有效的 session
	now := time.Now()
	sessionValue := GenerateSession(config, now)

	// 创建测试路由
	r := gin.New()
	r.Use(AdminSession(config))
	r.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 创建请求
	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(&http.Cookie{
		Name:  "custom_session",
		Value: sessionValue,
	})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}
}
