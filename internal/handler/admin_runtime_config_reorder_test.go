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

func TestAdminRuntimeConfigHandler_ReorderModelGroups(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.PUT("/admin/runtime-config/draft/model-groups/order", h.ReorderModelGroups)
	r.GET("/admin/runtime-config/draft", h.GetDraft)

	body, _ := json.Marshal(map[string]any{"names": []string{"gpt-4o", "gpt-4"}})
	req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/model-groups/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)

	req = httptest.NewRequest(http.MethodGet, "/admin/runtime-config/draft", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var resp DraftResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.ModelGroups, 2)
	assert.Equal(t, "gpt-4o", resp.ModelGroups[0].Name)
	assert.Equal(t, "gpt-4", resp.ModelGroups[1].Name)

	// 非置换
	body, _ = json.Marshal(map[string]any{"names": []string{"gpt-4o"}})
	req = httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/model-groups/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAdminRuntimeConfigHandler_ReorderRedirects(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	require.NoError(t, mgr.CreateRedirect(&runtimeconfig.RedirectInput{Source: "fast", Target: "gpt-4o"}))

	r := gin.New()
	r.PUT("/admin/runtime-config/draft/redirects/order", h.ReorderRedirects)
	r.GET("/admin/runtime-config/draft/redirects", h.GetRedirects)

	body, _ := json.Marshal(map[string]any{"sources": []string{"fast", "gpt4"}})
	req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/redirects/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)

	req = httptest.NewRequest(http.MethodGet, "/admin/runtime-config/draft/redirects", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var redirects []runtimeconfig.RedirectListOutput
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &redirects))
	require.Len(t, redirects, 2)
	assert.Equal(t, "fast", redirects[0].Source)
	assert.Equal(t, "gpt4", redirects[1].Source)
}
