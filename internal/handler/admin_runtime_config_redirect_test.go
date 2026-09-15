package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminRuntimeConfigHandler_GetRedirects(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.GET("/admin/runtime-config/draft/redirects", h.GetRedirects)

	req := httptest.NewRequest("GET", "/admin/runtime-config/draft/redirects", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var redirects []runtimeconfig.RedirectListOutput
	err := json.Unmarshal(w.Body.Bytes(), &redirects)
	require.NoError(t, err)
	assert.Len(t, redirects, 1)
	assert.Equal(t, "gpt4", redirects[0].Source)
	assert.Equal(t, "gpt-4", redirects[0].Target)
	assert.Equal(t, "gpt-4", redirects[0].ResolvedGroup)
	assert.Equal(t, 0, redirects[0].ChainLength)
}

func TestAdminRuntimeConfigHandler_GetRedirect(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.GET("/admin/runtime-config/draft/redirects/:alias", h.GetRedirect)

	// Found
	req := httptest.NewRequest("GET", "/admin/runtime-config/draft/redirects/gpt4", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var redirect runtimeconfig.RedirectOutput
	err := json.Unmarshal(w.Body.Bytes(), &redirect)
	require.NoError(t, err)
	assert.Equal(t, "gpt4", redirect.Source)
	assert.Equal(t, "gpt-4", redirect.Target)

	// Not found
	req = httptest.NewRequest("GET", "/admin/runtime-config/draft/redirects/unknown", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAdminRuntimeConfigHandler_CreateRedirect(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.POST("/admin/runtime-config/draft/redirects", h.CreateRedirect)

	// Success - valid redirect
	input := runtimeconfig.RedirectInput{
		Source: "fast-model",
		Target: "gpt-4o",
	}
	body, _ := json.Marshal(input)
	req := httptest.NewRequest("POST", "/admin/runtime-config/draft/redirects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	var redirect runtimeconfig.RedirectOutput
	err := json.Unmarshal(w.Body.Bytes(), &redirect)
	require.NoError(t, err)
	assert.Equal(t, "fast-model", redirect.Source)
	assert.Equal(t, "gpt-4o", redirect.Target)

	// Conflict - alias already exists
	input = runtimeconfig.RedirectInput{
		Source: "gpt4", // already in config
		Target: "gpt-4",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/redirects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)

	// Bad request - empty alias
	input = runtimeconfig.RedirectInput{
		Source: "",
		Target: "gpt-4",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/redirects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Bad request - target not exist
	input = runtimeconfig.RedirectInput{
		Source: "test-alias",
		Target: "not-exist",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/redirects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Validation error - circular redirect
	input = runtimeconfig.RedirectInput{
		Source: "self-redirect",
		Target: "self-redirect",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/redirects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)

	// Success - alias-to-alias redirect
	input = runtimeconfig.RedirectInput{
		Source: "alias-chain",
		Target: "gpt4",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/redirects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestAdminRuntimeConfigHandler_UpdateRedirect(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.POST("/admin/runtime-config/draft/redirects", h.CreateRedirect)
	r.PUT("/admin/runtime-config/draft/redirects/:alias", h.UpdateRedirect)

	// 先创建一个额外的 redirect 用于后续测试
	inputCreate := runtimeconfig.RedirectInput{
		Source: "fast-model",
		Target: "gpt-4o",
	}
	body, _ := json.Marshal(inputCreate)
	req := httptest.NewRequest("POST", "/admin/runtime-config/draft/redirects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	// Success - update target
	input := runtimeconfig.RedirectInput{
		Source: "gpt4",
		Target: "gpt-4o",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("PUT", "/admin/runtime-config/draft/redirects/gpt4", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var redirect runtimeconfig.RedirectOutput
	err := json.Unmarshal(w.Body.Bytes(), &redirect)
	require.NoError(t, err)
	assert.Equal(t, "gpt4", redirect.Source)
	assert.Equal(t, "gpt-4o", redirect.Target)

	// Success - rename alias
	input = runtimeconfig.RedirectInput{
		Source: "gpt4-new",
		Target: "gpt-4",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("PUT", "/admin/runtime-config/draft/redirects/gpt4", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	err = json.Unmarshal(w.Body.Bytes(), &redirect)
	require.NoError(t, err)
	assert.Equal(t, "gpt4-new", redirect.Source)

	// Not found
	input = runtimeconfig.RedirectInput{
		Source: "unknown",
		Target: "gpt-4",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("PUT", "/admin/runtime-config/draft/redirects/unknown", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// Conflict - rename to existing alias
	input = runtimeconfig.RedirectInput{
		Source: "gpt4-new", // already exists from previous test
		Target: "gpt-4",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("PUT", "/admin/runtime-config/draft/redirects/fast-model", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestAdminRuntimeConfigHandler_DeleteRedirect(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.DELETE("/admin/runtime-config/draft/redirects/:alias", h.DeleteRedirect)

	// Success - delete unused redirect
	req := httptest.NewRequest("DELETE", "/admin/runtime-config/draft/redirects/gpt4", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)

	// Not found
	req = httptest.NewRequest("DELETE", "/admin/runtime-config/draft/redirects/unknown", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAdminRuntimeConfigHandler_RedirectCRUDWithApply(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.POST("/admin/runtime-config/draft/redirects", h.CreateRedirect)
	r.GET("/admin/runtime-config/draft/redirects/:alias", h.GetRedirect)
	r.PUT("/admin/runtime-config/draft/redirects/:alias", h.UpdateRedirect)
	r.DELETE("/admin/runtime-config/draft/redirects/:alias", h.DeleteRedirect)
	r.POST("/admin/runtime-config/apply", h.Apply)

	// 1. 创建 redirect
	input := runtimeconfig.RedirectInput{
		Source: "test-alias",
		Target: "gpt-4",
	}
	body, _ := json.Marshal(input)
	req := httptest.NewRequest("POST", "/admin/runtime-config/draft/redirects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	// 2. 验证创建成功
	req = httptest.NewRequest("GET", "/admin/runtime-config/draft/redirects/test-alias", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// 3. Apply 配置
	req = httptest.NewRequest("POST", "/admin/runtime-config/apply", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var applyResp ApplyResponse
	err := json.Unmarshal(w.Body.Bytes(), &applyResp)
	require.NoError(t, err)
	assert.True(t, applyResp.Success)

	// 4. 更新 redirect
	input = runtimeconfig.RedirectInput{
		Source: "test-alias",
		Target: "gpt-4o",
	}
	body, _ = json.Marshal(input)
	req = httptest.NewRequest("PUT", "/admin/runtime-config/draft/redirects/test-alias", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// 5. 再次 Apply
	req = httptest.NewRequest("POST", "/admin/runtime-config/apply", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	err = json.Unmarshal(w.Body.Bytes(), &applyResp)
	require.NoError(t, err)
	assert.True(t, applyResp.Success)

	// 6. 删除 redirect
	req = httptest.NewRequest("DELETE", "/admin/runtime-config/draft/redirects/test-alias", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)

	// 7. 最终 Apply
	req = httptest.NewRequest("POST", "/admin/runtime-config/apply", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	err = json.Unmarshal(w.Body.Bytes(), &applyResp)
	require.NoError(t, err)
	assert.True(t, applyResp.Success)
}

func TestAdminRuntimeConfigHandler_RedirectChainWithApply(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.POST("/admin/runtime-config/draft/redirects", h.CreateRedirect)
	r.GET("/admin/runtime-config/draft/redirects", h.GetRedirects)
	r.POST("/admin/runtime-config/apply", h.Apply)

	// 1. 创建 alias-to-alias chain: alias1 -> alias2 -> gpt-4
	input1 := runtimeconfig.RedirectInput{
		Source: "alias2",
		Target: "gpt-4",
	}
	body, _ := json.Marshal(input1)
	req := httptest.NewRequest("POST", "/admin/runtime-config/draft/redirects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	input2 := runtimeconfig.RedirectInput{
		Source: "alias1",
		Target: "alias2",
	}
	body, _ = json.Marshal(input2)
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/redirects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	// 2. 查看 redirects 列表，验证 chain length
	req = httptest.NewRequest("GET", "/admin/runtime-config/draft/redirects", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var redirects []runtimeconfig.RedirectListOutput
	err := json.Unmarshal(w.Body.Bytes(), &redirects)
	require.NoError(t, err)

	// 找到 alias1 和 alias2
	var alias1Entry, alias2Entry *runtimeconfig.RedirectListOutput
	for i := range redirects {
		if redirects[i].Source == "alias1" {
			alias1Entry = &redirects[i]
		}
		if redirects[i].Source == "alias2" {
			alias2Entry = &redirects[i]
		}
	}

	require.NotNil(t, alias1Entry)
	require.NotNil(t, alias2Entry)

	// alias2 直达 gpt-4，chainLength = 0
	assert.Equal(t, "gpt-4", alias2Entry.ResolvedGroup)
	assert.Equal(t, 0, alias2Entry.ChainLength)

	// alias1 经过 alias2 到 gpt-4，chainLength = 1
	assert.Equal(t, "gpt-4", alias1Entry.ResolvedGroup)
	assert.Equal(t, 1, alias1Entry.ChainLength)

	// 3. Apply 配置
	req = httptest.NewRequest("POST", "/admin/runtime-config/apply", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var applyResp ApplyResponse
	err = json.Unmarshal(w.Body.Bytes(), &applyResp)
	require.NoError(t, err)
	assert.True(t, applyResp.Success)
}

func TestAdminRuntimeConfigHandler_CircularRedirectPrevention(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.POST("/admin/runtime-config/draft/redirects", h.CreateRedirect)
	r.PUT("/admin/runtime-config/draft/redirects/:alias", h.UpdateRedirect)

	// 1. 创建第一个 redirect
	input1 := runtimeconfig.RedirectInput{
		Source: "alias-a",
		Target: "gpt-4",
	}
	body, _ := json.Marshal(input1)
	req := httptest.NewRequest("POST", "/admin/runtime-config/draft/redirects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	// 2. 创建第二个 redirect
	input2 := runtimeconfig.RedirectInput{
		Source: "alias-b",
		Target: "alias-a",
	}
	body, _ = json.Marshal(input2)
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/redirects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	// 3. 尝试创建循环 redirect - 应该失败
	input3 := runtimeconfig.RedirectInput{
		Source: "alias-c",
		Target: "alias-b", // alias-b -> alias-a -> gpt-4
	}
	body, _ = json.Marshal(input3)
	req = httptest.NewRequest("POST", "/admin/runtime-config/draft/redirects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	// 4. 尝试更新形成循环 - 将 alias-a 指向 alias-c
	input4 := runtimeconfig.RedirectInput{
		Source: "alias-a",
		Target: "alias-c", // alias-a -> alias-c -> alias-b -> alias-a (循环!)
	}
	body, _ = json.Marshal(input4)
	req = httptest.NewRequest("PUT", "/admin/runtime-config/draft/redirects/alias-a", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}
