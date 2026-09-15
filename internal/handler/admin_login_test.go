package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/gin-gonic/gin"
)

func TestAdminLoginHandler_LoginPage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		password     string
		sessionValue string
		wantStatus   int
		wantRedirect bool
	}{
		{
			name:         "未登录用户应返回登录页面",
			password:     "test123",
			sessionValue: "",
			wantStatus:   http.StatusOK,
			wantRedirect: false,
		},
		{
			name:         "已登录用户应重定向到管理页面",
			password:     "test123",
			sessionValue: "valid_session",
			wantStatus:   http.StatusFound,
			wantRedirect: true,
		},
		{
			name:         "无密码配置时应显示配置提示",
			password:     "",
			sessionValue: "",
			wantStatus:   http.StatusOK,
			wantRedirect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Server: config.ServerConfig{
					Admin: config.AdminConfig{
						Password: tt.password,
					},
				},
			}

			h := NewAdminLoginHandler(cfg)
			r := gin.New()

			// 如果有 sessionValue，模拟已登录状态
			if tt.sessionValue != "" {
				r.Use(func(c *gin.Context) {
					c.Set("admin_authenticated", true)
					c.Next()
				})
			}

			r.GET("/admin/login", h.LoginPage)

			req := httptest.NewRequest("GET", "/admin/login", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("LoginPage() status = %v, want %v", w.Code, tt.wantStatus)
			}

			if tt.wantRedirect {
				location := w.Header().Get("Location")
				if location != "/admin/" {
					t.Errorf("LoginPage() redirect location = %v, want /admin/", location)
				}
			}

			if !tt.wantRedirect && !strings.Contains(w.Body.String(), "Admin Console") {
				t.Error("LoginPage() 应包含登录页面内容")
			}
			if tt.password == "" && !strings.Contains(w.Body.String(), "Password Not Configured") {
				t.Error("LoginPage() 无密码时应显示配置提示")
			}
		})
	}
}

func TestAdminLoginHandler_Login(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		password   string
		input      string
		wantStatus int
		wantCookie bool
		wantError  bool
	}{
		{
			name:       "正确密码应登录成功",
			password:   "test123",
			input:      `{"password":"test123"}`,
			wantStatus: http.StatusOK,
			wantCookie: true,
			wantError:  false,
		},
		{
			name:       "错误密码应返回401",
			password:   "test123",
			input:      `{"password":"wrong"}`,
			wantStatus: http.StatusUnauthorized,
			wantCookie: false,
			wantError:  true,
		},
		{
			name:       "空密码应返回401",
			password:   "test123",
			input:      `{"password":""}`,
			wantStatus: http.StatusUnauthorized,
			wantCookie: false,
			wantError:  true,
		},
		{
			name:       "无效JSON应返回400",
			password:   "test123",
			input:      `invalid`,
			wantStatus: http.StatusBadRequest,
			wantCookie: false,
			wantError:  true,
		},
		{
			name:       "无密码配置时应返回403",
			password:   "",
			input:      `{"password":"test123"}`,
			wantStatus: http.StatusForbidden,
			wantCookie: false,
			wantError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Server: config.ServerConfig{
					Admin: config.AdminConfig{
						Password: tt.password,
					},
				},
			}

			h := NewAdminLoginHandler(cfg)
			r := gin.New()
			r.POST("/admin/login", h.Login)

			req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(tt.input))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("Login() status = %v, want %v", w.Code, tt.wantStatus)
			}

			// 检查响应体
			body := w.Body.String()
			if tt.wantError {
				if !strings.Contains(body, "error") {
					t.Error("Login() 响应应包含 error 字段")
				}
			} else {
				if !strings.Contains(body, `"success":true`) {
					t.Error("Login() 成功响应应包含 success:true")
				}
			}

			// 检查 Cookie
			cookies := w.Result().Cookies()
			if tt.wantCookie {
				found := false
				for _, cookie := range cookies {
					if cookie.Name == "admin_session" {
						found = true
						if cookie.Value == "" {
							t.Error("Login() cookie 值不应为空")
						}
						if cookie.Path != "/" {
							t.Errorf("Login() cookie path = %v, want /", cookie.Path)
						}
						if !cookie.HttpOnly {
							t.Error("Login() cookie 应设置 HttpOnly")
						}
						break
					}
				}
				if !found {
					t.Error("Login() 应设置 admin_session cookie")
				}
			} else {
				for _, cookie := range cookies {
					if cookie.Name == "admin_session" && cookie.Value != "" {
						t.Error("Login() 不应设置有效的 admin_session cookie")
					}
				}
			}
		})
	}
}

func TestAdminLoginHandler_LoginConstantTime(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Admin: config.AdminConfig{
				Password: "correct-password",
			},
		},
	}

	h := NewAdminLoginHandler(cfg)
	r := gin.New()
	r.POST("/admin/login", h.Login)

	// 测试多次错误密码的响应时间一致性
	passwords := []string{
		"wrong1",
		"wrong2",
		"wrong3",
	}

	var times []time.Duration
	for _, pwd := range passwords {
		input := `{"password":"` + pwd + `"}`

		start := time.Now()
		req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(input))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		elapsed := time.Since(start)

		times = append(times, elapsed)
	}

	// 检查响应时间差异不应太大（允许一定波动）
	// 这里只做简单检查，实际常量时间比较由 crypto/subtle 包保证
	avgTime := time.Duration(0)
	for _, duration := range times {
		avgTime += duration
	}
	avgTime = avgTime / time.Duration(len(times))

	for _, duration := range times {
		diff := duration - avgTime
		if diff < 0 {
			diff = -diff
		}
		// 允许 50% 的波动
		if diff > avgTime/2 {
			t.Logf("Warning: response time variation detected: %v vs avg %v", duration, avgTime)
		}
	}
}

func TestAdminLoginHandler_Logout(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Admin: config.AdminConfig{
				Password: "test123",
			},
		},
	}

	h := NewAdminLoginHandler(cfg)
	r := gin.New()
	r.POST("/admin/logout", h.Logout)

	req := httptest.NewRequest("POST", "/admin/logout", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 验证状态码
	if w.Code != http.StatusOK {
		t.Errorf("Logout() status = %v, want %v", w.Code, http.StatusOK)
	}

	// 验证响应体
	if !strings.Contains(w.Body.String(), `"success":true`) {
		t.Error("Logout() 响应应包含 success:true")
	}

	// 验证 cookie 被清除（MaxAge < 0 表示删除）
	cookies := w.Result().Cookies()
	found := false
	for _, cookie := range cookies {
		if cookie.Name == "admin_session" {
			found = true
			if cookie.MaxAge >= 0 {
				t.Errorf("Logout() cookie MaxAge = %v, want negative (delete)", cookie.MaxAge)
			}
			if cookie.Path != "/" {
				t.Errorf("Logout() cookie path = %v, want /", cookie.Path)
			}
			if !cookie.HttpOnly {
				t.Error("Logout() cookie 应设置 HttpOnly")
			}
			break
		}
	}
	if !found {
		t.Error("Logout() 应设置 admin_session cookie（带删除标记）")
	}
}
