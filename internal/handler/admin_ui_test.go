package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAdminUI_Dashboard(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewAdminUIHandler()

	r := gin.New()
	r.GET("/admin/", h.Dashboard)

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
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

	if !contains(body, "<title>Dashboard") {
		t.Error("expected HTML to contain dashboard title")
	}

	// 检查关键内容
	if !contains(body, "Requests") {
		t.Error("expected HTML to contain 'Requests'")
	}

	if !contains(body, "Provider Breakdown") {
		t.Error("expected HTML to contain 'Provider Breakdown'")
	}

	if !contains(body, "By Key") {
		t.Error("expected HTML to contain 'By Key'")
	}

	if !contains(body, "By Upstream Model") {
		t.Error("expected HTML to contain 'By Upstream Model'")
	}

	// 验证 JavaScript 引用
	if !contains(body, "/admin/static/shared.js") {
		t.Error("expected HTML to reference shared.js")
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 &&
		(len(s) >= len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
