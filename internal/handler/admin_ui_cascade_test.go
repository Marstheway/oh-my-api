package handler

import (
	"fmt"
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

func TestAdminUI_Cascade(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewAdminUIHandler()
	r := gin.New()
	r.GET("/admin/cascade", h.Cascade)

	staticFS, err := fs.Sub(adminui.FS, "static")
	if err != nil {
		t.Fatal(err)
	}
	r.StaticFS("/admin/static", http.FS(staticFS))

	req := httptest.NewRequest(http.MethodGet, "/admin/cascade", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	for _, want := range []string{
		`<title>Cascade`,
		"Connect this gateway to another oh-my-api instance over WSS",
		"/admin/static/cascade.js",
		"cascadeModeWrap",
		"hubSection",
		"spokeSection",
		"publishedSection",
		"No pending changes",
		"Apply Changes",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}

	if strings.Contains(body, "Save Cascade") {
		t.Error("cascade page must not render a Save Cascade button")
	}

	reqJS := httptest.NewRequest(http.MethodGet, "/admin/static/cascade.js", nil)
	wJS := httptest.NewRecorder()
	r.ServeHTTP(wJS, reqJS)
	if wJS.Code != http.StatusOK {
		t.Fatalf("cascade.js status=%d", wJS.Code)
	}
	js := wJS.Body.String()
	for _, want := range []string{
		"function persistDraft",
		"function schedulePersist",
		"pagehide",
		"visibilitychange",
		"onBeforeApply",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("cascade.js missing %q", want)
		}
	}
	tmp := t.TempDir() + "/cascade.js"
	if err := os.WriteFile(tmp, wJS.Body.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", "--check", tmp).CombinedOutput(); err != nil {
		t.Fatalf("node --check failed: %v\n%s", err, out)
	}
}

func TestCascadeOriginValidation(t *testing.T) {
	js, err := adminui.FS.ReadFile("static/cascade.js")
	if err != nil {
		t.Fatal(err)
	}
	fn := extractJSFunction(string(js), "validateHubOrigin")
	if fn == "" {
		t.Fatal("validateHubOrigin not found in cascade.js")
	}

	script := fmt.Sprintf(`
const validateHubOrigin = (%s);
function assert(cond, msg) {
  if (!cond) { console.error('FAIL: ' + msg); process.exit(1); }
}
const a = validateHubOrigin('http://host:80');
assert(a.url === 'ws://host:80/cascade', 'http:80 -> ' + JSON.stringify(a));
const b = validateHubOrigin('https://host:443');
assert(b.url === 'wss://host:443/cascade', 'https:443 -> ' + JSON.stringify(b));
const c = validateHubOrigin('https://host/');
assert(c.error === 'Path is not allowed.', 'trailing slash -> ' + JSON.stringify(c));
const d = validateHubOrigin('wss://host');
assert(d.error === 'Scheme must be http or https (not ws/wss).', 'wss -> ' + JSON.stringify(d));
console.log('OK');
`, fn)

	tmp := t.TempDir() + "/cascade-origin-test.js"
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil {
		t.Fatalf("node origin test failed: %v\n%s", err, out)
	}
}

func extractJSFunction(src, name string) string {
	idx := strings.Index(src, "function "+name)
	if idx < 0 {
		return ""
	}
	brace := strings.IndexByte(src[idx:], '{')
	if brace < 0 {
		return ""
	}
	start := idx + brace
	depth := 0
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[idx : i+1]
			}
		}
	}
	return ""
}
