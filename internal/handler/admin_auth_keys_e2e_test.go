package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/middleware"
	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuthKeysE2E 端到端验证：通过 admin CRUD API 管理 inbound auth key，
// apply 后立即反映到网关入口认证（middleware.Auth 使用的 KeyProvider）。
// 流程：创建 key → apply → 新 key 可访问 /v1 → 删除 key → apply → 新 key 失效。
func TestAuthKeysE2E(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	// 同时挂载 admin 路由与网关入口路由，模拟真实服务拓扑
	r := gin.New()
	r.Use(middleware.Recovery())

	// 网关入口：受 middleware.Auth(keyProvider) 保护
	v1 := r.Group("/v1")
	v1.Use(middleware.Auth(mgr))
	v1.GET("/models", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// admin 运行时配置路由（含 auth-keys CRUD + apply）
	admin := r.Group("/admin")
	admin.Use(middleware.AdminAuthOrSession(cfg.Server.Admin.Password, cfg.Server.Admin.GetSessionSecret()))
	admin.POST("/runtime-config/draft/auth-keys", h.CreateAuthKey)
	admin.PUT("/runtime-config/draft/auth-keys/:name", h.UpdateAuthKey)
	admin.DELETE("/runtime-config/draft/auth-keys/:name", h.DeleteAuthKey)
	admin.POST("/runtime-config/apply", h.Apply)

	const newKey = "sk-e2e-new-key"
	const newName = "e2e-key"

	doGateway := func(key string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// 1. 初始状态：新 key 尚未创建，网关应拒绝 401
	require.Equal(t, http.StatusUnauthorized, doGateway(newKey), "新 key 创建前不应通过网关认证")

	adminReq := func(method, path string, payload []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Admin-Password", cfg.Server.Admin.Password)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 2. 通过 admin API 创建 key
	body, _ := json.Marshal(runtimeconfig.AuthKeyInput{Name: newName, Key: newKey})
	w := adminReq(http.MethodPost, "/admin/runtime-config/draft/auth-keys", body)
	require.Equal(t, http.StatusCreated, w.Code, "创建 auth key 应返回 201")

	// 3. 创建后尚未 apply：网关仍使用旧 active，新 key 应被拒绝
	require.Equal(t, http.StatusUnauthorized, doGateway(newKey), "apply 前新 key 不应生效")

	// 4. apply 热生效
	w = adminReq(http.MethodPost, "/admin/runtime-config/apply", nil)
	require.Equal(t, http.StatusOK, w.Code, "apply 应成功")

	// 5. apply 后：新 key 立即通过网关认证
	require.Equal(t, http.StatusOK, doGateway(newKey), "apply 后新 key 应通过网关认证")

	// 6. 原有的 test/sk-test key 仍然有效
	require.Equal(t, http.StatusOK, doGateway("sk-test"), "原 key 在 apply 后应保持有效")

	// 7. 删除新 key
	w = adminReq(http.MethodDelete, "/admin/runtime-config/draft/auth-keys/"+newName, nil)
	require.Equal(t, http.StatusNoContent, w.Code, "删除 auth key 应返回 204")

	// 8. 删除后尚未 apply：网关仍用旧 active，新 key 短暂仍有效
	require.Equal(t, http.StatusOK, doGateway(newKey), "apply 前删除不应影响已生效的网关")

	// 9. 再次 apply 提交删除
	w = adminReq(http.MethodPost, "/admin/runtime-config/apply", nil)
	require.Equal(t, http.StatusOK, w.Code, "apply 应成功")

	// 10. apply 后：新 key 已失效，恢复 401
	require.Equal(t, http.StatusUnauthorized, doGateway(newKey), "apply 后已删除的 key 应失效")

	// 11. 校验 ActiveKeys() 接口反映最终状态（热生效的数据源）
	var found bool
	for _, k := range mgr.ActiveKeys() {
		if k.Name == newName {
			found = true
		}
	}
	assert.False(t, found, "apply 后 active 配置不应再包含已删除的 key")

	// 12. 更新 key 值也应经 apply 生效
	const rotatedKey = "sk-e2e-rotated"
	body, _ = json.Marshal(runtimeconfig.AuthKeyInput{Name: newName, Key: rotatedKey})
	w = adminReq(http.MethodPost, "/admin/runtime-config/draft/auth-keys", body)
	require.Equal(t, http.StatusCreated, w.Code)

	w = adminReq(http.MethodPost, "/admin/runtime-config/apply", nil)
	require.Equal(t, http.StatusOK, w.Code)

	require.Equal(t, http.StatusOK, doGateway(rotatedKey), "apply 后轮换的新 key 应通过网关认证")
	require.Equal(t, http.StatusUnauthorized, doGateway(newKey), "旧的 key 值应已失效")
}
