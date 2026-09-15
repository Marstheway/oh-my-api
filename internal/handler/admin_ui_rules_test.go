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

func TestAdminUI_Rules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewAdminUIHandler()
	r := gin.New()
	r.GET("/admin/rules", h.Rules)
	r.GET("/admin/", h.Dashboard)

	staticFS, err := fs.Sub(adminui.FS, "static")
	if err != nil {
		t.Fatal(err)
	}
	r.StaticFS("/admin/static", http.FS(staticFS))

	req := httptest.NewRequest(http.MethodGet, "/admin/rules", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("expected Content-Type 'text/html; charset=utf-8', got '%s'", ct)
	}

	body := w.Body.String()
	for _, want := range []string{
		"<title>Rules | Admin</title>",
		"/admin/static/shared.js",
		"/admin/static/rules.js",
		"rulesTableBody",
		"rulesMatchTips",
		"All matching rules run in order",
		"later ones override the same action type",
		"put broad defaults first",
		// action type 自定义 dropdown 宽度（select 隐藏后由 .dropdown.w-action-type 占位）
		".dropdown.w-action-type",
	} {
		if !contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}

	// 侧栏 Rules 位于 Redirects 与 Providers 之间
	redirectsIdx := strings.Index(body, `href="/admin/redirects"`)
	rulesIdx := strings.Index(body, `href="/admin/rules"`)
	providersIdx := strings.Index(body, `href="/admin/providers"`)
	if redirectsIdx < 0 || rulesIdx < 0 || providersIdx < 0 {
		t.Fatalf("sidebar links missing: redirects=%d rules=%d providers=%d", redirectsIdx, rulesIdx, providersIdx)
	}
	if !(redirectsIdx < rulesIdx && rulesIdx < providersIdx) {
		t.Errorf("sidebar order: rules must sit between redirects and providers (redirects=%d rules=%d providers=%d)", redirectsIdx, rulesIdx, providersIdx)
	}

	// Serve JS and syntax-check with node
	reqJS := httptest.NewRequest(http.MethodGet, "/admin/static/rules.js", nil)
	wJS := httptest.NewRecorder()
	r.ServeHTTP(wJS, reqJS)
	if wJS.Code != http.StatusOK {
		t.Fatalf("rules.js status=%d", wJS.Code)
	}
	tmp := t.TempDir() + "/rules.js"
	if err := os.WriteFile(tmp, wJS.Body.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	jsBody := wJS.Body.String()
	if out, err := exec.Command("node", "--check", tmp).CombinedOutput(); err != nil {
		t.Fatalf("node --check failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"ruleMatchField",
		"One condition per rule",
		"Add one row per action type",
		"effort and effort_mode are mutually exclusive",
		"ruleActionEditor",
		"ACTION_DEFS",
		"addActionRow",
		"removeActionRow",
		"enable_time_range",
		"disable_time_range",
		"qpm action requires an upstream_model match",
		"retries must be 0, 1, or 2",
		"type: 'retries'",
		"type: 'max_tokens'",
		"type: 'qpm'",
		"type: 'temperature_mode'",
		"eachPresentAction",
		"action[parsed.type]",
		"temperature_mode",
		"strip removes the temperature parameter from the request.",
		"max_tokens must be a positive integer.",
		"effort and effort_mode cannot be set on the same rule",
		// action type 下拉需走自定义 dropdown，与 Match 字段一致（勿回退原生 select）
		`data-field="type" data-dropdown`,
		"refreshDropdown",
	} {
		if !strings.Contains(jsBody, want) {
			t.Errorf("rules.js missing scheduling-action copy %q", want)
		}
	}

	reqLayout := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	wLayout := httptest.NewRecorder()
	r.ServeHTTP(wLayout, reqLayout)
	if wLayout.Code != http.StatusOK {
		t.Fatalf("layout/dashboard status=%d", wLayout.Code)
	}
	layoutBody := wLayout.Body.String()
	for _, want := range []string{
		"function refreshDropdown",
		"dropdown-option.disabled",
		"buildDropdownMenu",
	} {
		if !strings.Contains(layoutBody, want) {
			t.Errorf("layout missing dropdown helper %q", want)
		}
	}
	// summaries / 回填 / 收集必须扫 ACTION_DEFS（type 即字段名），禁止再平铺 a.qpm 一类分支。
	for _, fn := range []string{"actionSummary", "ruleSummaryText", "actionRowsFromAction"} {
		start := strings.Index(jsBody, "function "+fn)
		if start < 0 {
			t.Errorf("rules.js missing function %s", fn)
			continue
		}
		seg := jsBody[start:]
		if end := strings.Index(seg, "\n}\n"); end > 0 {
			seg = seg[:end]
		}
		if !strings.Contains(seg, "eachPresentAction") && !strings.Contains(seg, "ACTION_DEFS") {
			t.Errorf("%s must walk ACTION_DEFS / eachPresentAction", fn)
		}
		for _, key := range []string{"a.qpm", "a.retries", "a.enable_time_range", "a.disable_time_range", "a.temperature_mode"} {
			if strings.Contains(seg, key) {
				t.Errorf("%s still hardcodes %s; drive it from ACTION_DEFS", fn, key)
			}
		}
	}
	collectStart := strings.Index(jsBody, "function collectAction")
	if collectStart < 0 {
		t.Error("rules.js missing function collectAction")
	} else {
		seg := jsBody[collectStart:]
		if end := strings.Index(seg, "\n}\n"); end > 0 {
			seg = seg[:end]
		}
		if !strings.Contains(seg, "action[parsed.type]") {
			t.Error("collectAction must assign action[parsed.type] (type is the JSON field)")
		}
		if strings.Contains(seg, "action.qpm") || strings.Contains(seg, "parsed.type === 'qpm'") {
			t.Error("collectAction still maps fields by name; use action[parsed.type]")
		}
	}
	for _, banned := range []string{
		"All filled conditions must match (AND)",
		"All filled conditions must match",
	} {
		if strings.Contains(jsBody, banned) {
			t.Errorf("rules.js still contains compound AND copy %q", banned)
		}
	}

	// Regression: dashboard still renders after sidebar change
	req = httptest.NewRequest(http.MethodGet, "/admin/", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard status=%d body=%s", w.Code, w.Body.String())
	}
}
