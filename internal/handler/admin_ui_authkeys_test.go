package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAdminUI_AuthKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewAdminUIHandler()

	r := gin.New()
	r.GET("/admin/auth-keys", h.AuthKeys)

	req := httptest.NewRequest(http.MethodGet, "/admin/auth-keys", nil)
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

	body := w.Body.String()

	if !contains(body, "<title>Auth Keys") {
		t.Error("expected HTML to contain auth-keys title")
	}

	// 验证关键内容
	if !contains(body, "New Auth Key") {
		t.Error("expected HTML to contain 'New Auth Key'")
	}

	if !contains(body, "authKeyTableBody") {
		t.Error("expected HTML to contain auth key table body")
	}

	// 验证 JavaScript 引用
	if !contains(body, "/admin/static/shared.js") {
		t.Error("expected HTML to reference shared.js")
	}
}
