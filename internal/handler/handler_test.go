package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	errs "github.com/Marstheway/oh-my-api/internal/errors"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func setupTestHandler(qpm int) (*gin.Engine, *httptest.Server, *httptest.Server) {
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))

	anthropicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg-test","type":"message","role":"assistant","content":[{"type":"text","text":"Hello!"}],"model":"claude-3","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))

	providers := map[string]config.ProviderConfig{
		"openai-provider": {
			Endpoint:  openaiSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: qpm},
		},
		"anthropic-provider": {
			Endpoint:  anthropicSrv.URL,
			APIKey:    "test-key",
			Protocol:  "anthropic",
			RateLimit: config.RateLimitConfig{QPM: qpm},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai-provider/gpt-4", Weight: 1}}},
			{Name: "claude-3", Models: config.ModelEntries{{Model: "anthropic-provider/claude-3", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		panic(err)
	}
	client := provider.NewClient(providers, "")
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker)

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)
	router.POST("/v1/messages", Messages)
	router.GET("/v1/models", Models)

	return router, openaiSrv, anthropicSrv
}

func TestChat_Success(t *testing.T) {
	router, openaiSrv, _ := setupTestHandler(0)
	defer openaiSrv.Close()

	body := dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
		},
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestChat_InvalidRequest(t *testing.T) {
	router, openaiSrv, _ := setupTestHandler(0)
	defer openaiSrv.Close()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestChat_ModelNotFound(t *testing.T) {
	router, openaiSrv, _ := setupTestHandler(0)
	defer openaiSrv.Close()

	body := dto.ChatCompletionRequest{
		Model: "unknown-model",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
		},
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestMessages_Success(t *testing.T) {
	router, _, anthropicSrv := setupTestHandler(0)
	defer anthropicSrv.Close()

	body := dto.ClaudeRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
		},
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestMessages_InvalidRequest(t *testing.T) {
	router, _, anthropicSrv := setupTestHandler(0)
	defer anthropicSrv.Close()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestModels_Success(t *testing.T) {
	router, openaiSrv, anthropicSrv := setupTestHandler(0)
	defer openaiSrv.Close()
	defer anthropicSrv.Close()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp dto.ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(resp.Data) != 2 {
		t.Errorf("models count = %d, want 2", len(resp.Data))
	}

	modelSet := make(map[string]bool)
	for _, m := range resp.Data {
		modelSet[m.ID] = true
	}
	if !modelSet["gpt-4"] || !modelSet["claude-3"] {
		t.Errorf("expected models gpt-4 and claude-3, got %v", resp.Data)
	}
}

func TestHandleUpstreamError_RateLimitTimeout(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	rlErr := &scheduler.RateLimitError{Provider: "test", Err: context.DeadlineExceeded}
	handleUpstreamError(c, errs.ProtocolOpenAI, rlErr)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
}

func TestHandleUpstreamError_AllRateLimited(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	handleUpstreamError(c, errs.ProtocolOpenAI, scheduler.ErrAllRateLimited)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
}

func TestHandleUpstreamError_AllProvidersFailed(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	handleUpstreamError(c, errs.ProtocolOpenAI, scheduler.ErrAllProvidersFailed)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}

func TestHandleUpstreamError_UpstreamTimeout(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	handleUpstreamError(c, errs.ProtocolOpenAI, context.DeadlineExceeded)

	if w.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want %d", w.Code, http.StatusGatewayTimeout)
	}
}

func TestHandleUpstreamError_GenericError(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	handleUpstreamError(c, errs.ProtocolOpenAI, http.ErrHandlerTimeout)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}

func TestHandleUpstreamResponseError_WithBody(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"internal error"}}`))
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("failed to get response: %v", err)
	}
	defer resp.Body.Close()

	handleUpstreamResponseError(c, errs.ProtocolOpenAI, "test-provider", resp)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestChat_RateLimitWait(t *testing.T) {
	router, openaiSrv, _ := setupTestHandler(60)
	defer openaiSrv.Close()

	body := dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestChat_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"test-provider": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "test-provider/gpt-4", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		panic(err)
	}
	client := provider.NewClient(providers, "")
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker)

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	body := dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
		},
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(jsonBody)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Error("expected non-200 status for canceled context")
	}
}

// TestMessages_WithEndpointsProtocol 测试使用 endpoints 配置时能正确选择 adaptor
// 这是对 bug 的回归测试：当 provider 配置了 endpoints，入方向 anthropic 应该使用对应的 endpoint 的 protocol
func TestMessages_WithEndpointsProtocol(t *testing.T) {
	// 创建一个模拟的 anthropic API 服务器，验证请求路径是否正确
	var receivedPath string
	anthropicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg-test","type":"message","role":"assistant","content":[{"type":"text","text":"Hello!"}],"model":"claude-3","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer anthropicSrv.Close()

	// provider 默认协议是 openai，但 endpoints 配置了 anthropic
	providers := map[string]config.ProviderConfig{
		"multi-protocol-provider": {
			Endpoint:  "https://default.openai.api.com",
			APIKey:    "test-key",
			Protocol:  "openai", // 默认协议
			RateLimit: config.RateLimitConfig{QPM: 0},
			Endpoints: []config.EndpointConfig{
				{URL: "https://default.openai.api.com", Protocol: "openai"},
				{URL: anthropicSrv.URL, Protocol: "anthropic"},
			},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "claude-3", Models: config.ModelEntries{{Model: "multi-protocol-provider/claude-3", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		panic(err)
	}
	client := provider.NewClient(providers, "")
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker)

	router := gin.New()
	router.POST("/v1/messages", Messages)

	body := dto.ClaudeRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
		},
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// 验证请求路径是 /v1/messages 而不是 /chat/completions
	if receivedPath != "/v1/messages" {
		t.Errorf("request path = %q, want %q (should use anthropic adaptor, not openai adaptor)", receivedPath, "/v1/messages")
	}
}

// TestChat_WithEndpointsProtocol 测试 OpenAI 入方向使用 endpoints 配置
func TestChat_WithEndpointsProtocol(t *testing.T) {
	var receivedPath string
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer openaiSrv.Close()

	// provider 默认协议是 anthropic，但 endpoints 配置了 openai
	providers := map[string]config.ProviderConfig{
		"multi-protocol-provider": {
			Endpoint:  "https://default.anthropic.api.com",
			APIKey:    "test-key",
			Protocol:  "anthropic", // 默认协议
			RateLimit: config.RateLimitConfig{QPM: 0},
			Endpoints: []config.EndpointConfig{
				{URL: "https://default.anthropic.api.com", Protocol: "anthropic"},
				{URL: openaiSrv.URL, Protocol: "openai"},
			},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "multi-protocol-provider/gpt-4", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		panic(err)
	}
	client := provider.NewClient(providers, "")
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker)

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	body := dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
		},
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// 验证请求路径是 /v1/chat/completions
	if receivedPath != "/v1/chat/completions" {
		t.Errorf("request path = %q, want %q", receivedPath, "/v1/chat/completions")
	}
}

// ============================================================================
// 集成测试：入方向协议 × Provider 配置 × 出方向协议
// ============================================================================

// integrationTestCase 封装集成测试用例
type integrationTestCase struct {
	name           string
	inbound        string              // 入方向协议: "openai" 或 "anthropic"
	providerConfig config.ProviderConfig
	expectedPath   string              // 期望的上游请求路径
	setupUpstream  func() *httptest.Server // 设置上游服务器
}

// runIntegrationTest 执行集成测试
func runIntegrationTest(t *testing.T, tc integrationTestCase) {
	var receivedPath string
	var receivedHeaders http.Header
	var receivedBody []byte

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedHeaders = r.Header
		body, _ := io.ReadAll(r.Body)
		receivedBody = body

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// 根据请求路径返回对应格式的响应
		if r.URL.Path == "/v1/messages" {
			w.Write([]byte(`{"id":"msg-test","type":"message","role":"assistant","content":[{"type":"text","text":"Hello!"}],"model":"test-model","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`))
		} else {
			w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
		}
	}))
	defer upstream.Close()

	// 如果 provider 配置中没有 endpoints，设置默认 endpoint
	if tc.providerConfig.Endpoint == "" {
		tc.providerConfig.Endpoint = upstream.URL
	}
	// 更新 endpoints 中的 URL 为测试服务器
	for i := range tc.providerConfig.Endpoints {
		tc.providerConfig.Endpoints[i].URL = upstream.URL
	}
	tc.providerConfig.APIKey = "test-api-key"
	tc.providerConfig.RateLimit = config.RateLimitConfig{QPM: 0}

	providers := map[string]config.ProviderConfig{
		"test-provider": tc.providerConfig,
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "test-model", Models: config.ModelEntries{{Model: "test-provider/test-model", Weight: 1}}},
		},
	}

	testResolver, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	testClient := provider.NewClient(providers, "")
	testRLManager := ratelimit.NewManager(providers)
	testHealthChecker := health.NewChecker(3, 30*time.Second)
		testSched := scheduler.New(testRLManager, testClient, testHealthChecker)

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)
	router.POST("/v1/messages", Messages)

	var reqBody []byte
	var path string
	if tc.inbound == "openai" {
		path = "/v1/chat/completions"
		reqBody, _ = json.Marshal(dto.ChatCompletionRequest{
			Model:    "test-model",
			Messages: []dto.Message{{Role: "user", Content: "Hello"}},
		})
	} else {
		path = "/v1/messages"
		reqBody, _ = json.Marshal(dto.ClaudeRequest{
			Model:     "test-model",
			MaxTokens: 100,
			Messages:  []dto.ClaudeMessage{{Role: "user", Content: "Hello"}},
		})
	}

	// 临时替换全局变量
	oldCfg, oldResolver, oldSched := cfg, resolver, sched
	cfg, resolver, sched = testCfg, testResolver, testSched
	defer func() { cfg, resolver, sched = oldCfg, oldResolver, oldSched }()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
		return
	}

	if receivedPath != tc.expectedPath {
		t.Errorf("upstream path = %q, want %q", receivedPath, tc.expectedPath)
	}

	// 验证请求体格式
	if len(receivedBody) > 0 {
		var parsed map[string]any
		if err := json.Unmarshal(receivedBody, &parsed); err != nil {
			t.Errorf("failed to parse request body: %v", err)
		}
		// 验证 model 字段存在
		if _, ok := parsed["model"]; !ok {
			t.Errorf("request body missing 'model' field: %s", string(receivedBody))
		}
	}

	// 验证认证头
	if tc.expectedPath == "/v1/messages" {
		if receivedHeaders.Get("x-api-key") != "test-api-key" {
			t.Errorf("missing x-api-key header for anthropic protocol")
		}
	} else {
		auth := receivedHeaders.Get("Authorization")
		if auth != "Bearer test-api-key" {
			t.Errorf("Authorization header = %q, want %q", auth, "Bearer test-api-key")
		}
	}
}

// TestIntegration_OpenAI_SingleEndpoint_OpenAI 入方向OpenAI, 单endpoint(openai) → 出方向OpenAI
func TestIntegration_OpenAI_SingleEndpoint_OpenAI(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "OpenAI入方向-单endpoint-openai协议",
		inbound: "openai",
		providerConfig: config.ProviderConfig{
			Protocol: "openai",
		},
		expectedPath: "/v1/chat/completions",
	})
}

// TestIntegration_OpenAI_SingleEndpoint_Anthropic 入方向OpenAI, 单endpoint(anthropic) → 出方向Anthropic
func TestIntegration_OpenAI_SingleEndpoint_Anthropic(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "OpenAI入方向-单endpoint-anthropic协议",
		inbound: "openai",
		providerConfig: config.ProviderConfig{
			Protocol: "anthropic",
		},
		expectedPath: "/v1/messages",
	})
}

// TestIntegration_Anthropic_SingleEndpoint_Anthropic 入方向Anthropic, 单endpoint(anthropic) → 出方向Anthropic
func TestIntegration_Anthropic_SingleEndpoint_Anthropic(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "Anthropic入方向-单endpoint-anthropic协议",
		inbound: "anthropic",
		providerConfig: config.ProviderConfig{
			Protocol: "anthropic",
		},
		expectedPath: "/v1/messages",
	})
}

// TestIntegration_Anthropic_SingleEndpoint_OpenAI 入方向Anthropic, 单endpoint(openai) → 出方向OpenAI
func TestIntegration_Anthropic_SingleEndpoint_OpenAI(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "Anthropic入方向-单endpoint-openai协议",
		inbound: "anthropic",
		providerConfig: config.ProviderConfig{
			Protocol: "openai",
		},
		expectedPath: "/v1/chat/completions",
	})
}

// TestIntegration_OpenAI_MultiEndpoints_MatchOpenAI 入方向OpenAI, 多endpoints匹配openai → 出方向OpenAI
func TestIntegration_OpenAI_MultiEndpoints_MatchOpenAI(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "OpenAI入方向-多endpoints匹配openai",
		inbound: "openai",
		providerConfig: config.ProviderConfig{
			Protocol: "anthropic", // 默认协议
			Endpoints: []config.EndpointConfig{
				{URL: "http://placeholder-anthropic", Protocol: "anthropic"},
				{URL: "http://placeholder-openai", Protocol: "openai"},
			},
		},
		expectedPath: "/v1/chat/completions",
	})
}

// TestIntegration_Anthropic_MultiEndpoints_MatchAnthropic 入方向Anthropic, 多endpoints匹配anthropic → 出方向Anthropic
func TestIntegration_Anthropic_MultiEndpoints_MatchAnthropic(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "Anthropic入方向-多endpoints匹配anthropic",
		inbound: "anthropic",
		providerConfig: config.ProviderConfig{
			Protocol: "openai", // 默认协议
			Endpoints: []config.EndpointConfig{
				{URL: "http://placeholder-openai", Protocol: "openai"},
				{URL: "http://placeholder-anthropic", Protocol: "anthropic"},
			},
		},
		expectedPath: "/v1/messages",
	})
}

// TestIntegration_OpenAI_MultiEndpoints_NoMatchFallback 入方向OpenAI, 多endpoints无匹配 → fallback默认协议
func TestIntegration_OpenAI_MultiEndpoints_NoMatchFallback(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "OpenAI入方向-多endpoints无匹配-fallback默认协议",
		inbound: "openai",
		providerConfig: config.ProviderConfig{
			Protocol: "anthropic", // 默认协议，因为没有匹配的endpoint所以用这个
			Endpoints: []config.EndpointConfig{
				{URL: "http://placeholder-other", Protocol: "other"},
			},
		},
		expectedPath: "/v1/messages", // 使用默认协议 anthropic
	})
}

// TestIntegration_Anthropic_MultiEndpoints_NoMatchFallback 入方向Anthropic, 多endpoints无匹配 → fallback默认协议
func TestIntegration_Anthropic_MultiEndpoints_NoMatchFallback(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "Anthropic入方向-多endpoints无匹配-fallback默认协议",
		inbound: "anthropic",
		providerConfig: config.ProviderConfig{
			Protocol: "openai", // 默认协议，因为没有匹配的endpoint所以用这个
			Endpoints: []config.EndpointConfig{
				{URL: "http://placeholder-other", Protocol: "other"},
			},
		},
		expectedPath: "/v1/chat/completions", // 使用默认协议 openai
	})
}

// TestIntegration_OpenAI_EmptyEndpoints 入方向OpenAI, 空endpoints → 使用默认协议
func TestIntegration_OpenAI_EmptyEndpoints(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "OpenAI入方向-空endpoints数组",
		inbound: "openai",
		providerConfig: config.ProviderConfig{
			Protocol:  "openai",
			Endpoints: []config.EndpointConfig{},
		},
		expectedPath: "/v1/chat/completions",
	})
}

// TestIntegration_Anthropic_NoEndpoints 入方向Anthropic, 无endpoints配置 → 使用默认协议
func TestIntegration_Anthropic_NoEndpoints(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "Anthropic入方向-无endpoints配置",
		inbound: "anthropic",
		providerConfig: config.ProviderConfig{
			Protocol: "anthropic",
		},
		expectedPath: "/v1/messages",
	})
}

// ========== Redirect 集成测试 ==========

func TestIntegration_Redirect_VisibleTarget(t *testing.T) {
	falseVal := false

	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer openaiSrv.Close()

	providers := map[string]config.ProviderConfig{
		"openai": {
			Endpoint:  openaiSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:    "hidden-backend",
				Visible: &falseVal,
				Models:  config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}},
			},
		},
		Redirect: map[string]string{
			"claude-4-6": "hidden-backend",
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, "")
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker)

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	// 通过别名调用
	body := `{"model":"claude-4-6","messages":[{"role":"user","content":"Hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// 直接调用不可见模型应返回 404
	body = `{"model":"hidden-backend","messages":[{"role":"user","content":"Hello"}]}`
	req = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404 for hidden model, got %d", w.Code)
	}
}

func TestIntegration_Redirect_ModelsEndpoint(t *testing.T) {
	falseVal := false

	providers := map[string]config.ProviderConfig{
		"openai": {
			Endpoint: "https://api.openai.com/v1",
			APIKey:   "test-key",
			Protocol: "openai",
		},
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:    "hidden-model",
				Visible: &falseVal,
				Models:  config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}},
			},
			{
				Name:    "visible-model",
				Models:  config.ModelEntries{{Model: "openai/gpt-4o", Weight: 1}},
			},
		},
		Redirect: map[string]string{
			"alias-model": "hidden-model",
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, "")
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker)

	router := gin.New()
	router.GET("/v1/models", Models)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp dto.ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	modelSet := make(map[string]bool)
	for _, m := range resp.Data {
		modelSet[m.ID] = true
	}

	// 别名和可见模型应该在列表中
	if !modelSet["alias-model"] {
		t.Error("expected alias-model in model list")
	}
	if !modelSet["visible-model"] {
		t.Error("expected visible-model in model list")
	}

	// 不可见模型不应该在列表中
	if modelSet["hidden-model"] {
		t.Error("hidden-model should not appear in model list")
	}
}

func TestIntegration_Visible_ModelHidden(t *testing.T) {
	falseVal := false

	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer openaiSrv.Close()

	providers := map[string]config.ProviderConfig{
		"openai": {
			Endpoint:  openaiSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:    "hidden-model",
				Visible: &falseVal,
				Models:  config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}},
			},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, "")
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker)

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	// 直接调用不可见模型应返回 404
	body := `{"model":"hidden-model","messages":[{"role":"user","content":"Hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestIntegration_Redirect_ChainedRedirect(t *testing.T) {
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer openaiSrv.Close()

	providers := map[string]config.ProviderConfig{
		"openai": {
			Endpoint:  openaiSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:   "backend",
				Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}},
			},
		},
		Redirect: map[string]string{
			"alias-1": "alias-2",
			"alias-2": "alias-3",
			"alias-3": "backend",
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, "")
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker)

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	// 通过链式别名调用应成功
	body := `{"model":"alias-1","messages":[{"role":"user","content":"Hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ============================================================================
// /v1/responses handler 集成测试
// ============================================================================

func setupResponsesTestHandler(responsesBody string, chatBody string) (*gin.Engine, *httptest.Server) {
	responsesFallbackCache.Reset()

	responsesUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if responsesBody != "" {
			w.Write([]byte(responsesBody))
		} else {
			w.Write([]byte(`{"id":"resp-test","object":"response","created_at":1234567890,"model":"test-model","status":"completed","output":[{"type":"message","id":"msg-test","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello!"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`))
		}
	}))

	chatUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if chatBody != "" {
			w.Write([]byte(chatBody))
		} else {
			w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
		}
	}))

	providers := map[string]config.ProviderConfig{
		"openai-response-provider": {
			Endpoint:  responsesUpstream.URL,
			APIKey:    "test-key",
			Protocol:  "openai.response",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"openai-chat-provider": {
			Endpoint:  chatUpstream.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "resp-model", Models: config.ModelEntries{{Model: "openai-response-provider/resp-model", Weight: 1}}},
			{Name: "chat-model", Models: config.ModelEntries{{Model: "openai-chat-provider/chat-model", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		panic(err)
	}
	client := provider.NewClient(providers, "")
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker)

	router := gin.New()
	router.POST("/v1/responses", Responses)

	return router, responsesUpstream
}

func TestFallbackCache_Basic(t *testing.T) {
	cache := newProtocolFallbackCache()

	if _, ok := cache.GetPreferred("p1", "openai.response"); ok {
		t.Fatal("expected cache miss")
	}

	cache.MarkPreferred("p1", "openai.response", "openai")
	outbound, ok := cache.GetPreferred("p1", "openai.response")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if outbound != "openai" {
		t.Fatalf("outbound = %q, want %q", outbound, "openai")
	}
}

func TestResponsesHandler_FallbackToChatAndLearn(t *testing.T) {
	responsesHits := 0
	chatHits := 0

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/responses":
			responsesHits++
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"type":"server_error","message":"responses not ready"}}`))
		case "/v1/chat/completions":
			chatHits++
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello from chat"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	providers := map[string]config.ProviderConfig{
		"token-hub": {
			Endpoint:  upstream.URL,
			APIKey:    "test-key",
			Protocol:  "openai.response/openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "resp-model", Models: config.ModelEntries{{Model: "token-hub/resp-model", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, "")
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker)
	cfg = testCfg

	router := gin.New()
	router.POST("/v1/responses", Responses)

	body := `{"model":"resp-model","input":[{"type":"message","role":"user","content":"Hello"}]}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if responsesHits == 0 || chatHits == 0 {
		t.Fatalf("expected fallback path hit both responses and chat, got responses=%d chat=%d", responsesHits, chatHits)
	}

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(body))
	req2.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second request status = %d, want %d, body: %s", w2.Code, http.StatusOK, w2.Body.String())
	}

	if responsesHits != 1 {
		t.Fatalf("expected second request bypass responses after learning, responsesHits=%d", responsesHits)
	}
	if chatHits != 2 {
		t.Fatalf("expected second request go directly chat, chatHits=%d", chatHits)
	}
}

func TestResponsesHandler_NoFallbackOnUnauthorized(t *testing.T) {
	responsesFallbackCache.Reset()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/responses" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"Should not fallback"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer upstream.Close()

	providers := map[string]config.ProviderConfig{
		"token-hub": {
			Endpoint:  upstream.URL,
			APIKey:    "test-key",
			Protocol:  "openai.response/openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "resp-model", Models: config.ModelEntries{{Model: "token-hub/resp-model", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, "")
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker)
	cfg = testCfg

	router := gin.New()
	router.POST("/v1/responses", Responses)

	body := `{"model":"resp-model","input":[{"type":"message","role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusUnauthorized, w.Body.String())
	}

	if _, ok := responsesFallbackCache.GetPreferred("token-hub", "openai.response"); ok {
		t.Fatal("should not learn fallback for unauthorized errors")
	}
}

func TestResponsesHandler_ToOpenAIResponse(t *testing.T) {
	router, upstream := setupResponsesTestHandler("", "")
	defer upstream.Close()

	reqBody := `{"model":"resp-model","input":[{"type":"message","role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
		return
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Object != "response" {
		t.Errorf("object = %q, want %q", out.Object, "response")
	}
}

func TestResponsesHandler_ToOpenAIChat(t *testing.T) {
	router, _ := setupResponsesTestHandler("", "")
	defer func() {}()

	reqBody := `{"model":"chat-model","input":[{"type":"message","role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
		return
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Object != "response" {
		t.Errorf("object = %q, want %q", out.Object, "response")
	}
}

func TestResponsesHandler_InvalidRequest(t *testing.T) {
	router, upstream := setupResponsesTestHandler("", "")
	defer upstream.Close()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString("invalid json"))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// ============================================================================
// Chat → openai.response provider 集成测试
// ============================================================================

func setupChatResponseProviderHandler(t *testing.T, upstreamBody string) (*gin.Engine, *httptest.Server, *string) {
	t.Helper()

	var receivedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(upstreamBody))
	}))

	providers := map[string]config.ProviderConfig{
		"resp-provider": {
			Endpoint:  upstream.URL,
			APIKey:    "test-key",
			Protocol:  "openai.response",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "test-model", Models: config.ModelEntries{{Model: "resp-provider/test-model", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, "")
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker)
	cfg = testCfg

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	t.Cleanup(func() { upstream.Close() })
	return router, upstream, &receivedPath
}

func TestChatHandler_ToOpenAIResponse(t *testing.T) {
	body := `{"id":"resp-test","object":"response","created_at":1234567890,"model":"test-model","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello!"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`
	router, _, receivedPath := setupChatResponseProviderHandler(t, body)

	reqBody := `{"model":"test-model","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// Upstream must receive the request on /v1/responses (openai.response protocol)
	if *receivedPath != "/v1/responses" {
		t.Errorf("upstream path = %q, want %q", *receivedPath, "/v1/responses")
	}

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out["object"] != "chat.completion" {
		t.Errorf("object = %q, want %q", out["object"], "chat.completion")
	}
}

func TestChatHandler_ConversionFailure502(t *testing.T) {
	// Upstream returns a Responses payload with only an unsupported "reasoning" output type.
	body := `{"id":"resp-test","object":"response","created_at":1234567890,"model":"test-model","status":"completed","output":[{"type":"reasoning","id":"rs-1","summary":[{"type":"summary_text","text":"thinking..."}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`
	router, _, _ := setupChatResponseProviderHandler(t, body)

	reqBody := `{"model":"test-model","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusBadGateway, w.Body.String())
	}
}

// ============================================================================
// Messages → openai.response provider 集成测试
// ============================================================================

func setupMessagesResponseProviderHandler(t *testing.T, upstreamBody string) (*gin.Engine, *httptest.Server, *string) {
	t.Helper()

	var receivedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(upstreamBody))
	}))

	providers := map[string]config.ProviderConfig{
		"resp-provider": {
			Endpoint:  upstream.URL,
			APIKey:    "test-key",
			Protocol:  "openai.response",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "test-model", Models: config.ModelEntries{{Model: "resp-provider/test-model", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, "")
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker)
	cfg = testCfg

	router := gin.New()
	router.POST("/v1/messages", Messages)

	t.Cleanup(func() { upstream.Close() })
	return router, upstream, &receivedPath
}

func TestMessagesHandler_ToOpenAIResponse(t *testing.T) {
	body := `{"id":"resp-test","object":"response","created_at":1234567890,"model":"test-model","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello!"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`
	router, _, receivedPath := setupMessagesResponseProviderHandler(t, body)

	reqBody := `{"model":"test-model","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if *receivedPath != "/v1/responses" {
		t.Errorf("upstream path = %q, want %q", *receivedPath, "/v1/responses")
	}

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out["type"] != "message" {
		t.Errorf("type = %q, want %q", out["type"], "message")
	}
}

func TestMessagesHandler_ConversionFailure502(t *testing.T) {
	// Upstream returns a Responses payload with status=failed.
	body := `{"id":"resp-test","object":"response","created_at":1234567890,"model":"test-model","status":"failed","error":{"code":"server_error","message":"upstream failure"},"output":[],"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}`
	router, _, _ := setupMessagesResponseProviderHandler(t, body)

	reqBody := `{"model":"test-model","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusBadGateway, w.Body.String())
	}
}

func TestHandleCodecError_ConversionErrorUses502(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	err := &codec.ConversionError{
		Phase:          "write_response",
		Step:           "response_to_chat",
		InboundFormat:  string(codec.FormatOpenAIResponse),
		OutboundFormat: string(codec.FormatOpenAIChat),
		Reason:         "unsupported_output_item",
		Err:            errors.New("unsupported output item: reasoning"),
	}

	handleCodecError(c, errs.ProtocolOpenAI, "write_response", err)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
	if !strings.Contains(w.Body.String(), "conversion") {
		t.Errorf("body should contain 'conversion', got: %s", w.Body.String())
	}
}

func TestHandleCodecError_ConversionErrorNilErrDoesNotPanic(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	err := &codec.ConversionError{
		Phase:          "write_response",
		Step:           "response_to_chat",
		InboundFormat:  string(codec.FormatOpenAIResponse),
		OutboundFormat: string(codec.FormatOpenAIChat),
		Reason:         "response_conversion",
	}

	handleCodecError(c, errs.ProtocolOpenAI, "write_response", err)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
	if !strings.Contains(w.Body.String(), "conversion") {
		t.Errorf("body should contain 'conversion', got: %s", w.Body.String())
	}
}

func TestResponsesHandler_ModelNotFound(t *testing.T) {
	router, upstream := setupResponsesTestHandler("", "")
	defer upstream.Close()

	reqBody := `{"model":"unknown-model","input":[{"type":"message","role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}
