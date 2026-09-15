package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// mockKeyProvider 用于测试，实现 KeyProvider 接口
type mockKeyProvider struct {
	keys []config.KeyConfig
}

func (m *mockKeyProvider) ActiveKeys() []config.KeyConfig { return m.keys }

func TestAuth(t *testing.T) {
	kp := &mockKeyProvider{
		keys: []config.KeyConfig{
			{Name: "app1", Key: "sk-valid-1"},
			{Name: "app2", Key: "sk-valid-2"},
		},
	}

	tests := []struct {
		name       string
		path       string
		headers    map[string]string
		query      string
		wantStatus int
	}{
		{
			name:       "valid bearer token",
			path:       "/v1/chat/completions",
			headers:    map[string]string{"Authorization": "Bearer sk-valid-1"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid second key",
			path:       "/v1/chat/completions",
			headers:    map[string]string{"Authorization": "Bearer sk-valid-2"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid x-api-key header",
			path:       "/v1/messages",
			headers:    map[string]string{"x-api-key": "sk-valid-1"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "query API key is rejected",
			path:       "/v1/models",
			query:      "api_key=sk-valid-1",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "no auth",
			path:       "/v1/models",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid key",
			path:       "/v1/models",
			headers:    map[string]string{"Authorization": "Bearer sk-invalid"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "empty bearer",
			path:       "/v1/models",
			headers:    map[string]string{"Authorization": "Bearer "},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.Use(Auth(kp))
			r.Any("/v1/*path", func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			target := tt.path
			if tt.query != "" {
				target += "?" + tt.query
			}

			req := httptest.NewRequest("POST", target, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestAuth_ErrorFormat(t *testing.T) {
	kp := &mockKeyProvider{
		keys: []config.KeyConfig{
			{Name: "test", Key: "sk-valid"},
		},
	}

	r := gin.New()
	r.Use(Auth(kp))

	t.Run("OpenAI format for /v1/chat/completions", func(t *testing.T) {
		r.POST("/v1/chat/completions", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		body := w.Body.String()
		if !containsStr(body, `"error"`) {
			t.Errorf("expected OpenAI error format, got: %s", body)
		}
		if containsStr(body, `"type":"error"`) {
			t.Errorf("should not be Anthropic format, got: %s", body)
		}
	})

	t.Run("Anthropic format for /v1/messages", func(t *testing.T) {
		r.POST("/v1/messages", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest("POST", "/v1/messages", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		body := w.Body.String()
		if !containsStr(body, `"type":"error"`) {
			t.Errorf("expected Anthropic error format, got: %s", body)
		}
	})
}

// TestAuth_HotReload 验证 KeyProvider 返回的 keys 变更后立即生效（模拟 Apply 后场景）
func TestAuth_HotReload(t *testing.T) {
	kp := &mockKeyProvider{
		keys: []config.KeyConfig{{Name: "old", Key: "sk-old"}},
	}

	r := gin.New()
	r.Use(Auth(kp))
	r.POST("/v1/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// 旧 key 有效
	req := httptest.NewRequest("POST", "/v1/test", nil)
	req.Header.Set("Authorization", "Bearer sk-old")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("old key: status = %d, want 200", w.Code)
	}

	// 模拟 Apply：替换 keys
	kp.keys = []config.KeyConfig{{Name: "new", Key: "sk-new"}}

	// 旧 key 立即失效
	req = httptest.NewRequest("POST", "/v1/test", nil)
	req.Header.Set("Authorization", "Bearer sk-old")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("old key after reload: status = %d, want 401", w.Code)
	}

	// 新 key 立即生效
	req = httptest.NewRequest("POST", "/v1/test", nil)
	req.Header.Set("Authorization", "Bearer sk-new")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("new key after reload: status = %d, want 200", w.Code)
	}
}

func TestExtractAPIKey(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		query   string
		wantKey string
	}{
		{
			name:    "Authorization Bearer takes priority",
			headers: map[string]string{"Authorization": "Bearer sk-bearer", "x-api-key": "sk-xapi"},
			wantKey: "sk-bearer",
		},
		{
			name:    "x-api-key fallback",
			headers: map[string]string{"x-api-key": "sk-xapi"},
			wantKey: "sk-xapi",
		},
		{
			name:    "query API key is ignored",
			query:   "api_key=sk-query",
			wantKey: "",
		},
		{
			name:    "no key",
			wantKey: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			target := "/test"
			if tt.query != "" {
				target += "?" + tt.query
			}
			c.Request = httptest.NewRequest("GET", target, nil)
			for k, v := range tt.headers {
				c.Request.Header.Set(k, v)
			}

			got := extractAPIKey(c)
			if got != tt.wantKey {
				t.Errorf("key = %q, want %q", got, tt.wantKey)
			}
		})
	}
}

func TestDetectInboundProtocol(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/v1/messages", "anthropic"},
		{"/v1/chat/completions", "openai"},
		{"/v1/models", "openai"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", tt.path, nil)

			got := detectInboundProtocol(c)
			if string(got) != tt.want {
				t.Errorf("protocol = %q, want %q", got, tt.want)
			}
		})
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
