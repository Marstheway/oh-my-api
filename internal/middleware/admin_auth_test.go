package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestAdminAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		password   string
		header     string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "missing password header",
			password:   "secret",
			header:     "",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "unauthorized",
		},
		{
			name:       "wrong password",
			password:   "secret",
			header:     "wrong",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "unauthorized",
		},
		{
			name:       "correct password",
			password:   "secret",
			header:     "secret",
			wantStatus: http.StatusOK,
			wantCode:   "",
		},
		{
			name:       "empty config password allows no access",
			password:   "",
			header:     "anything",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "unauthorized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.Use(AdminAuth(tt.password))
			r.GET("/test", func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			req := httptest.NewRequest("GET", "/test", nil)
			if tt.header != "" {
				req.Header.Set(HeaderAdminPassword, tt.header)
			}
			w := httptest.NewRecorder()

			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantCode != "" {
				assert.Contains(t, w.Body.String(), tt.wantCode)
			}
		})
	}
}

func TestAdminAuthOrSession(t *testing.T) {
	gin.SetMode(gin.TestMode)

	password := "admin-secret"
	sessionSecret := "session-secret"

	t.Run("header auth success", func(t *testing.T) {
		r := gin.New()
		r.Use(AdminAuthOrSession(password, sessionSecret))
		r.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set(HeaderAdminPassword, password)
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("header auth failure returns 401", func(t *testing.T) {
		r := gin.New()
		r.Use(AdminAuthOrSession(password, sessionSecret))
		r.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set(HeaderAdminPassword, "wrong")
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "invalid admin password")
	})

	t.Run("missing credentials with HTML request redirects to login", func(t *testing.T) {
		r := gin.New()
		r.Use(AdminAuthOrSession(password, sessionSecret))
		r.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusFound, w.Code)
		assert.Equal(t, "/admin/login", w.Header().Get("Location"))
	})

	t.Run("missing credentials with API request returns 401", func(t *testing.T) {
		r := gin.New()
		r.Use(AdminAuthOrSession(password, sessionSecret))
		r.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "missing admin password or session")
	})

	t.Run("missing credentials without Accept header returns 401", func(t *testing.T) {
		r := gin.New()
		r.Use(AdminAuthOrSession(password, sessionSecret))
		r.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "missing admin password or session")
	})

	t.Run("valid session cookie success", func(t *testing.T) {
		r := gin.New()
		r.Use(AdminAuthOrSession(password, sessionSecret))
		r.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		// 生成有效的 session cookie
		config := AdminSessionConfig{
			Secret:     sessionSecret,
			CookieName: "admin_session",
			MaxAge:     24 * 3600 * time.Second,
		}
		sessionValue := GenerateSession(config, time.Now())

		req := httptest.NewRequest("GET", "/test", nil)
		req.AddCookie(&http.Cookie{
			Name:  "admin_session",
			Value: sessionValue,
		})
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("expired session cookie with HTML request redirects", func(t *testing.T) {
		r := gin.New()
		r.Use(AdminAuthOrSession(password, sessionSecret))
		r.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		// 生成过期的 session cookie
		config := AdminSessionConfig{
			Secret:     sessionSecret,
			CookieName: "admin_session",
			MaxAge:     24 * 3600 * time.Second,
		}
		// 使用过去的时间生成已过期的 session
		sessionValue := GenerateSession(config, time.Now().Add(-48*time.Hour))

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Accept", "text/html")
		req.AddCookie(&http.Cookie{
			Name:  "admin_session",
			Value: sessionValue,
		})
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusFound, w.Code)
		assert.Equal(t, "/admin/login", w.Header().Get("Location"))
	})
}
