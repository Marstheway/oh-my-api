package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAdminUI_ModelGroups(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewAdminUIHandler()

	r := gin.New()
	r.GET("/admin/model-groups", h.ModelGroups)

	req := httptest.NewRequest(http.MethodGet, "/admin/model-groups", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	// 验证返回 HTML
	contentType := w.Header().Get("Content-Type")
	if contentType != "text/html; charset=utf-8" {
		t.Errorf("expected Content-Type 'text/html; charset=utf-8', got '%s'", contentType)
	}

	// 验证 HTML 包含关键元素
	body := w.Body.String()

	if !contains(body, "<title>Model Groups") {
		t.Error("expected HTML to contain model groups title")
	}

	// 检查关键内容
	if !contains(body, "Model Groups") {
		t.Error("expected HTML to contain 'Model Groups'")
	}

	if !contains(body, "No pending changes") {
		t.Error("expected HTML to contain 'No pending changes'")
	}

	if !contains(body, "Apply Changes") {
		t.Error("expected HTML to contain 'Apply Changes'")
	}

	if !contains(body, "New Group") {
		t.Error("expected HTML to contain 'New Group'")
	}

	// 验证 JavaScript 引用
	if !contains(body, "/admin/static/shared.js") {
		t.Error("expected HTML to reference shared.js")
	}
}
