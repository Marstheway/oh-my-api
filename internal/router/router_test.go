package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/middleware"
	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestSetupAdminRoutesConditionalRegistration(t *testing.T) {
	// Test: no routes when password empty
	cfgEmpty := &config.Config{
		Server: config.ServerConfig{
			Admin: config.AdminConfig{Password: ""},
		},
	}
	r := gin.New()
	r.NoRoute(func(c *gin.Context) { c.Status(404) })
	SetupAdmin(r, cfgEmpty, nil, nil)

	req := httptest.NewRequest("GET", "/admin/runtime-config/draft", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// Test: routes registered when password non-empty (session_secret auto-derived)
	cfgWithPassword := &config.Config{
		Server: config.ServerConfig{
			Admin: config.AdminConfig{
				Password: "secret123",
				// SessionSecret 留空，自动派生
			},
		},
	}
	mgr := &runtimeconfig.Manager{} // dummy manager (not used for this routing test)
	var querier stats.Querier       // nil is ok for routing test

	r2 := gin.New()
	SetupAdmin(r2, cfgWithPassword, mgr, querier)

	req = httptest.NewRequest("GET", "/admin/runtime-config/draft", nil)
	w = httptest.NewRecorder()
	r2.ServeHTTP(w, req)
	// Should require authentication - returns 401 without header
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestSetupAdminRoutesWithAuth(t *testing.T) {
	cfgWithPassword := &config.Config{
		Server: config.ServerConfig{
			Admin: config.AdminConfig{
				Password: "secret123",
			},
		},
	}
	// We can't test the full handler flow without a real manager,
	// but we can verify auth middleware works
	// So we'll just skip calling the actual handler

	// Test: auth middleware passes for correct password
	r := gin.New()
	admin := r.Group("/admin")
	admin.Use(middleware.AdminAuth(cfgWithPassword.Server.Admin.Password))
	admin.GET("/runtime-config/draft", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	req := httptest.NewRequest("GET", "/admin/runtime-config/draft", nil)
	req.Header.Set("X-Admin-Password", "secret123")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Test: auth middleware rejects wrong password
	req = httptest.NewRequest("GET", "/admin/runtime-config/draft", nil)
	req.Header.Set("X-Admin-Password", "wrongpassword")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestSetupAdminAutoDeriveSessionSecret(t *testing.T) {
	// Test: session_secret auto-derived from password when not set
	cfg := &config.Config{
		Server: config.ServerConfig{
			Admin: config.AdminConfig{
				Password: "secret123",
			},
		},
	}

	r := gin.New()
	SetupAdmin(r, cfg, nil, nil)

	// 验证路由已注册（返回 401 需要认证，而非 404）
	req := httptest.NewRequest("GET", "/admin/runtime-config/draft", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestSetupAdminRootRedirect(t *testing.T) {
	// Test: root path redirects to /admin/ when admin enabled
	cfg := &config.Config{
		Server: config.ServerConfig{
			Admin: config.AdminConfig{
				Password: "secret123",
			},
		},
	}

	r := gin.New()
	SetupAdmin(r, cfg, nil, nil)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Should redirect to /admin/
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/admin/", w.Header().Get("Location"))

	// Test: no redirect when admin disabled (password empty)
	cfgEmpty := &config.Config{
		Server: config.ServerConfig{
			Admin: config.AdminConfig{Password: ""},
		},
	}

	r2 := gin.New()
	r2.NoRoute(func(c *gin.Context) { c.Status(404) })
	SetupAdmin(r2, cfgEmpty, nil, nil)

	req2 := httptest.NewRequest("GET", "/", nil)
	w2 := httptest.NewRecorder()
	r2.ServeHTTP(w2, req2)

	// Should return 404 (no route registered)
	assert.Equal(t, http.StatusNotFound, w2.Code)
}

func TestSetupAdminCatalogRouteConditionalRegistration(t *testing.T) {
	// Test: catalog route not registered when password empty
	cfgEmpty := &config.Config{
		Server: config.ServerConfig{
			Admin: config.AdminConfig{Password: ""},
		},
	}
	r := gin.New()
	r.NoRoute(func(c *gin.Context) { c.Status(404) })
	SetupAdmin(r, cfgEmpty, nil, nil)

	req := httptest.NewRequest("GET", "/admin/runtime-config/catalog", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// Test: catalog route registered when password non-empty, requires auth
	cfgWithPassword := &config.Config{
		Server: config.ServerConfig{
			Admin: config.AdminConfig{Password: "secret123"},
		},
	}
	mgr := &runtimeconfig.Manager{}
	var querier stats.Querier

	r2 := gin.New()
	SetupAdmin(r2, cfgWithPassword, mgr, querier)

	// Unauthenticated: 401
	req = httptest.NewRequest("GET", "/admin/runtime-config/catalog", nil)
	w = httptest.NewRecorder()
	r2.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// Authenticated via X-Admin-Password header: 200
	req = httptest.NewRequest("GET", "/admin/runtime-config/catalog", nil)
	req.Header.Set("X-Admin-Password", "secret123")
	w = httptest.NewRecorder()
	r2.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}
