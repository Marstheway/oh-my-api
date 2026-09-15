package handler

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/adminui"
	"github.com/gin-gonic/gin"
)

func TestAdminUI_Providers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewAdminUIHandler()
	r := gin.New()
	r.GET("/admin/providers", h.Providers)
	// Other pages must still render with default empty page_head
	r.GET("/admin/", h.Dashboard)
	r.GET("/admin/model-groups", h.ModelGroups)

	staticFS, err := fs.Sub(adminui.FS, "static")
	if err != nil {
		t.Fatal(err)
	}
	r.StaticFS("/admin/static", http.FS(staticFS))

	req := httptest.NewRequest(http.MethodGet, "/admin/providers", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"/admin/static/shared.js",
		"/admin/static/providers.js",
		`href="/admin/static/providers.css"`,
		"providerGrid",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, `@import url("/admin/static/providers.css")`) {
		t.Error("should not use @import for providers.css")
	}

	// link must appear after </style> so page rules win the cascade over layout
	linkIdx := strings.Index(body, `href="/admin/static/providers.css"`)
	styleEndIdx := strings.Index(body, "</style>")
	if linkIdx < 0 || styleEndIdx < 0 || linkIdx < styleEndIdx {
		t.Errorf("providers.css link should appear after </style> (link=%d styleEnd=%d)", linkIdx, styleEndIdx)
	}

	// Serve JS and syntax-check with node
	reqJS := httptest.NewRequest(http.MethodGet, "/admin/static/providers.js", nil)
	wJS := httptest.NewRecorder()
	r.ServeHTTP(wJS, reqJS)
	if wJS.Code != http.StatusOK {
		t.Fatalf("providers.js status=%d", wJS.Code)
	}
	tmp := t.TempDir() + "/providers.js"
	if err := os.WriteFile(tmp, wJS.Body.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", "--check", tmp).CombinedOutput(); err != nil {
		t.Fatalf("node --check failed: %v\n%s", err, out)
	}

	jsBody := wJS.Body.String()
	// 时段与 per-model qpm 已迁到 rules：providers.js 不再处理 disabled_time_ranges / timeRangesInput，
	// 也不再保存 upstream_models；provider 级 QPM（账号 tier）仍保留。
	for _, banned := range []string{
		"disabled_time_ranges",
		"timeRangesInput",
		"validateTimeRange",
		"timeRangesArea",
		"upstream_models",
		"time restrictions",
	} {
		if strings.Contains(jsBody, banned) {
			t.Errorf("providers.js must not contain %q", banned)
		}
	}
	for _, want := range []string{
		"pvQpm",
		"rate_limit",
		"qpm",
	} {
		if !strings.Contains(jsBody, want) {
			t.Errorf("providers.js missing %q", want)
		}
	}

	reqCSS := httptest.NewRequest(http.MethodGet, "/admin/static/providers.css", nil)
	wCSS := httptest.NewRecorder()
	r.ServeHTTP(wCSS, reqCSS)
	if wCSS.Code != http.StatusOK || wCSS.Body.Len() < 100 {
		t.Fatalf("providers.css status=%d len=%d", wCSS.Code, wCSS.Body.Len())
	}

	// Regression: other pages still render after page_head was introduced
	for _, path := range []string{"/admin/", "/admin/model-groups"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s status=%d body=%s", path, w.Code, w.Body.String()[:min(200, w.Body.Len())])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
