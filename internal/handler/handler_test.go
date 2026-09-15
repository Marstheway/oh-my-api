package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	errs "github.com/Marstheway/oh-my-api/internal/errors"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/Marstheway/oh-my-api/internal/token"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// exposurePtr 将 bool 映射为 *config.Exposure（true=public, false=internal），
// 旧 visible=false 等价于新 internal（不展示且不可外部直调）。
func exposurePtr(b bool) *config.Exposure {
	if b {
		v := config.ExposurePublic
		return &v
	}
	v := config.ExposureInternal
	return &v
}

func hiddenExposurePtr() *config.Exposure {
	v := config.ExposureHidden
	return &v
}

func internalExposurePtr() *config.Exposure {
	v := config.ExposureInternal
	return &v
}

func TestMain(m *testing.M) {
	// 在整个 handler 测试二进制启动时加载 deepseek tokenizer
	if err := token.Init(); err != nil {
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func init() {
	gin.SetMode(gin.TestMode)
}

type timedRecorder struct {
	*httptest.ResponseRecorder
	mu         sync.Mutex
	firstWrite time.Time
}

func newTimedRecorder() *timedRecorder {
	return &timedRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *timedRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	if r.firstWrite.IsZero() {
		r.firstWrite = time.Now()
	}
	r.mu.Unlock()
	return r.ResponseRecorder.Write(b)
}

func (r *timedRecorder) WriteString(s string) (int, error) {
	r.mu.Lock()
	if r.firstWrite.IsZero() {
		r.firstWrite = time.Now()
	}
	r.mu.Unlock()
	return r.ResponseRecorder.WriteString(s)
}

func (r *timedRecorder) FirstWriteTime() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.firstWrite
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
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: qpm},
		},
		"anthropic-provider": {
			Endpoint:  anthropicSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"anthropic.messages"},
			RateLimit: config.RateLimitConfig{QPM: qpm},
		},
	}

	ResetCatalogSource()

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
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

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

func TestChat_DeepSeekAutoCompat(t *testing.T) {
	var requestBody []byte
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer openaiSrv.Close()

	ResetCatalogSource()
	providers := map[string]config.ProviderConfig{
		"deepseek-provider": {
			Endpoint:  openaiSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai.chat"},
		},
	}
	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "deepseek-chat", Models: config.ModelEntries{{Model: "deepseek-provider/deepseek-chat", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	// 请求包含 assistant tool_calls 续轮，但客户端未传 reasoning_content
	body := dto.ChatCompletionRequest{
		Model: "deepseek-chat",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []dto.ToolCall{{
					ID:       "tool-1",
					Type:     "function",
					Function: dto.ToolCallFunc{Name: "read_file", Arguments: `{"path":"a"}`},
				}},
			},
			{Role: "tool", ToolCallID: "tool-1", Content: "ok"},
		},
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var upstreamReq dto.ChatCompletionRequest
	if err := json.Unmarshal(requestBody, &upstreamReq); err != nil {
		t.Fatalf("unmarshal upstream req: %v", err)
	}
	// 上游请求应自动补 reasoning_content 占位值
	if upstreamReq.Messages[1].ReasoningContent == nil || *upstreamReq.Messages[1].ReasoningContent != " " {
		t.Fatalf("reasoning_content = %#v, want single space", upstreamReq.Messages[1].ReasoningContent)
	}
	// 原始请求不应被修改
	if body.Messages[1].ReasoningContent != nil {
		t.Fatalf("original request should not be mutated")
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

func TestHandleUpstreamError_NoProviderAvailable(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	handleUpstreamError(c, errs.ProtocolOpenAI, scheduler.ErrNoProviderAvailable)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
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

func TestHandleUpstreamError_ClientCanceled(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	handleUpstreamError(c, errs.ProtocolOpenAI, context.Canceled)

	// gin buffers status until WriteHeaderNow (called by Engine in production);
	// assert via Writer.Status, not the raw recorder Code.
	if c.Writer.Status() != statusClientClosed {
		t.Errorf("status = %d, want %d", c.Writer.Status(), statusClientClosed)
	}
	if w.Body.Len() != 0 {
		t.Errorf("body should be empty on client cancel, got %q", w.Body.String())
	}
	// Must not masquerade as OpenAI-style upstream_error.
	if strings.Contains(w.Body.String(), "upstream connection failed") {
		t.Fatal("must not write upstream connection failed body on client cancel")
	}
	if !c.IsAborted() {
		t.Fatal("context should be aborted")
	}
}

func TestHandleUpstreamError_ClientCanceledWrapped(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	handleUpstreamError(c, errs.ProtocolOpenAI, fmt.Errorf("wrap: %w", context.Canceled))

	if c.Writer.Status() != statusClientClosed {
		t.Errorf("status = %d, want %d", c.Writer.Status(), statusClientClosed)
	}
	if w.Body.Len() != 0 {
		t.Errorf("body should be empty, got %q", w.Body.String())
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
			Protocols: []string{"openai.chat"},
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
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

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

func TestChat_StreamStartsBeforeUpstreamCompletes(t *testing.T) {
	// recordStats 依赖 stats recorder；本测试直接经过 Chat 的 key_name 统计路径，
	// 需自包含初始化，避免受其它测试执行顺序影响。
	testDBPath := filepath.Join(t.TempDir(), "handler-stream-stats.db")
	if err := stats.Init(testDBPath); err != nil {
		t.Fatalf("Init stats error: %v", err)
	}
	defer stats.Reset()

	// 因果握手：真实 HTTP 客户端首次读到首 chunk 时关闭 clientGotChunk；
	// 上游仅在收到该信号后才发送 DONE。于是"首 token 先于流结束"成为跨进程的
	// happens-before 顺序，可被 race 构建同样严格校验，而不依赖易失真的墙钟时延。
	clientGotChunk := make(chan struct{})
	upstreamTimedOut := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("upstream should support flusher")
		}

		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n"))
		flusher.Flush()

		// 流式实现应在客户端收到首 chunk 后被放行；若实现退化为缓冲，
		// 客户端直到 DONE 后才收到任何数据，等待将超时（回归）。
		select {
		case <-clientGotChunk:
		case <-time.After(5 * time.Second):
			close(upstreamTimedOut)
		}

		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"test-provider": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai.chat"},
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
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	metrics.ResetForTest()
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("key_name", "test-key")
		c.Next()
	})
	router.POST("/v1/chat/completions", Chat)

	// 经真实 HTTP 网关与客户端验证流式语义：httptest.ResponseRecorder 会缓冲
	// 全部响应，无法观测增量写入，故使用真实 TCP 流读取。
	gateway := httptest.NewServer(router)
	defer gateway.Close()

	body := `{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"Hello"}]}`
	req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("gateway request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		all, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body: %s", resp.StatusCode, all)
	}

	// 逐行读取 SSE：首行到达即认定首 token 已送达客户端。
	reader := bufio.NewReader(resp.Body)
	var received strings.Builder
	first := true
	for {
		line, readErr := reader.ReadString('\n')
		received.WriteString(line)
		if first {
			first = false
			select {
			case <-clientGotChunk:
			default:
				close(clientGotChunk)
			}
		}
		if strings.Contains(line, "[DONE]") {
			break
		}
		if readErr != nil {
			t.Fatalf("read stream: %v (received: %s)", readErr, received.String())
		}
	}
	requestDuration := time.Since(start)

	if !strings.Contains(received.String(), "data: [DONE]") {
		t.Fatalf("missing stream done marker, body: %s", received.String())
	}

	// 关键事件顺序（race 构建同样校验）：首 chunk 必须在流结束（DONE）之前送达客户端。
	select {
	case <-clientGotChunk:
	default:
		t.Fatal("first chunk was not delivered to the client")
	}
	select {
	case <-upstreamTimedOut:
		t.Fatal("upstream stream completed without the client receiving the first chunk: response was buffered instead of streamed")
	default:
	}

	// 时延 sanity：流式请求应在握手超时（5s）内完成；缓冲回归会等到超时。
	// 上限留足 race 检测器的执行放大余量，同时严格排除 5s 级缓冲回归。
	if requestDuration >= 2*time.Second {
		t.Fatalf("request took too long: %v (buffered responses must be rejected by the ordering check; sane streaming should finish well under 2s)", requestDuration)
	}

	// 流式解码时长指标应被记录（handler 在写回完成后记账，客户端读完可能存在
	// 极小窗口，轮询等待记账落盘）。原 0.15-0.5s 的固定窗口依赖固定的 200ms 上游
	// 停顿，与因果握手时序不兼容；此处保留"已记录"这一严格断言。
	deadline := time.Now().Add(2 * time.Second)
	decodeDuration := 0.0
	for time.Now().Before(deadline) {
		decodeDuration = testutil.ToFloat64(metrics.GetStreamDecodeDurationSecondsTotal().WithLabelValues(
			"test-provider", "gpt-4", "gpt-4", "test-key",
		))
		if decodeDuration > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if decodeDuration <= 0 {
		t.Fatalf("decode duration metric was not recorded, got %fs", decodeDuration)
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
			Protocols: []string{"openai.chat"}, // 默认协议
			RateLimit: config.RateLimitConfig{QPM: 0},
			Endpoints: []config.EndpointConfig{
				{URL: "https://default.openai.api.com", Protocols: []string{"openai.chat"}},
				{URL: anthropicSrv.URL, Protocols: []string{"anthropic.messages"}},
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
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

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
			Protocols: []string{"anthropic.messages"}, // 默认协议
			RateLimit: config.RateLimitConfig{QPM: 0},
			Endpoints: []config.EndpointConfig{
				{URL: "https://default.anthropic.api.com", Protocols: []string{"anthropic.messages"}},
				{URL: openaiSrv.URL, Protocols: []string{"openai.chat"}},
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
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

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
	inbound        string // 入方向协议: "openai" 或 "anthropic"
	providerConfig config.ProviderConfig
	expectedPath   string                  // 期望的上游请求路径
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
	testClient := provider.NewClient(providers, 120*time.Second, 0, 0)
	testRLManager := ratelimit.NewManager(providers)
	testHealthChecker := health.NewChecker(3, 30*time.Second)
	testSched := scheduler.New(testRLManager, testClient, testHealthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)
	router.POST("/v1/messages", Messages)

	var reqBody []byte
	var path string
	if tc.inbound == "openai.chat" {
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
		inbound: "openai.chat",
		providerConfig: config.ProviderConfig{
			Protocols: []string{"openai.chat"},
		},
		expectedPath: "/v1/chat/completions",
	})
}

// TestIntegration_OpenAI_SingleEndpoint_Anthropic 入方向OpenAI, 单endpoint(anthropic) → 出方向Anthropic
func TestIntegration_OpenAI_SingleEndpoint_Anthropic(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "OpenAI入方向-单endpoint-anthropic协议",
		inbound: "openai.chat",
		providerConfig: config.ProviderConfig{
			Protocols: []string{"anthropic.messages"},
		},
		expectedPath: "/v1/messages",
	})
}

// TestIntegration_Anthropic_SingleEndpoint_Anthropic 入方向Anthropic, 单endpoint(anthropic) → 出方向Anthropic
func TestIntegration_Anthropic_SingleEndpoint_Anthropic(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "Anthropic入方向-单endpoint-anthropic协议",
		inbound: "anthropic.messages",
		providerConfig: config.ProviderConfig{
			Protocols: []string{"anthropic.messages"},
		},
		expectedPath: "/v1/messages",
	})
}

// TestIntegration_Anthropic_SingleEndpoint_OpenAI 入方向Anthropic, 单endpoint(openai) → 出方向OpenAI
func TestIntegration_Anthropic_SingleEndpoint_OpenAI(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "Anthropic入方向-单endpoint-openai协议",
		inbound: "anthropic.messages",
		providerConfig: config.ProviderConfig{
			Protocols: []string{"openai.chat"},
		},
		expectedPath: "/v1/chat/completions",
	})
}

// TestIntegration_OpenAI_MultiEndpoints_MatchOpenAI 入方向OpenAI, 多endpoints匹配openai → 出方向OpenAI
func TestIntegration_OpenAI_MultiEndpoints_MatchOpenAI(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "OpenAI入方向-多endpoints匹配openai",
		inbound: "openai.chat",
		providerConfig: config.ProviderConfig{
			Protocols: []string{"anthropic.messages"}, // 默认协议
			Endpoints: []config.EndpointConfig{
				{URL: "http://placeholder-anthropic", Protocols: []string{"anthropic.messages"}},
				{URL: "http://placeholder-openai", Protocols: []string{"openai.chat"}},
			},
		},
		expectedPath: "/v1/chat/completions",
	})
}

// TestIntegration_Anthropic_MultiEndpoints_MatchAnthropic 入方向Anthropic, 多endpoints匹配anthropic → 出方向Anthropic
func TestIntegration_Anthropic_MultiEndpoints_MatchAnthropic(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "Anthropic入方向-多endpoints匹配anthropic",
		inbound: "anthropic.messages",
		providerConfig: config.ProviderConfig{
			Protocols: []string{"openai.chat"}, // 默认协议
			Endpoints: []config.EndpointConfig{
				{URL: "http://placeholder-openai", Protocols: []string{"openai.chat"}},
				{URL: "http://placeholder-anthropic", Protocols: []string{"anthropic.messages"}},
			},
		},
		expectedPath: "/v1/messages",
	})
}

// TestIntegration_OpenAI_MultiEndpoints_NoMatchFallback 入方向OpenAI, 多endpoints无匹配 → fallback默认协议
func TestIntegration_OpenAI_MultiEndpoints_NoMatchFallback(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "OpenAI入方向-多endpoints无匹配-fallback默认协议",
		inbound: "openai.chat",
		providerConfig: config.ProviderConfig{
			Protocols: []string{"anthropic.messages"}, // 默认协议，因为没有匹配的endpoint所以用这个
			Endpoints: []config.EndpointConfig{
				{URL: "http://placeholder-other", Protocols: []string{"other"}},
			},
		},
		expectedPath: "/v1/messages", // 使用默认协议 anthropic
	})
}

// TestIntegration_Anthropic_MultiEndpoints_NoMatchFallback 入方向Anthropic, 多endpoints无匹配 → fallback默认协议
func TestIntegration_Anthropic_MultiEndpoints_NoMatchFallback(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "Anthropic入方向-多endpoints无匹配-fallback默认协议",
		inbound: "anthropic.messages",
		providerConfig: config.ProviderConfig{
			Protocols: []string{"openai.chat"}, // 默认协议，因为没有匹配的endpoint所以用这个
			Endpoints: []config.EndpointConfig{
				{URL: "http://placeholder-other", Protocols: []string{"other"}},
			},
		},
		expectedPath: "/v1/chat/completions", // 使用默认协议 openai
	})
}

// TestIntegration_OpenAI_EmptyEndpoints 入方向OpenAI, 空endpoints → 使用默认协议
func TestIntegration_OpenAI_EmptyEndpoints(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "OpenAI入方向-空endpoints数组",
		inbound: "openai.chat",
		providerConfig: config.ProviderConfig{
			Protocols: []string{"openai.chat"},
			Endpoints: []config.EndpointConfig{},
		},
		expectedPath: "/v1/chat/completions",
	})
}

// TestIntegration_Anthropic_NoEndpoints 入方向Anthropic, 无endpoints配置 → 使用默认协议
func TestIntegration_Anthropic_NoEndpoints(t *testing.T) {
	runIntegrationTest(t, integrationTestCase{
		name:    "Anthropic入方向-无endpoints配置",
		inbound: "anthropic.messages",
		providerConfig: config.ProviderConfig{
			Protocols: []string{"anthropic.messages"},
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
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "internal-backend",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}},
			},
		},
		Redirect: config.RedirectConfigs{
			{Source: "claude-4-6", Target: "internal-backend"},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

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

	// 直接调用 internal 模型应返回 404
	body = `{"model":"internal-backend","messages":[{"role":"user","content":"Hello"}]}`
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
			Endpoint:  "https://api.openai.com/v1",
			APIKey:    "test-key",
			Protocols: []string{"openai.chat"},
		},
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "internal-model",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}},
			},
			{
				Name:   "visible-model",
				Models: config.ModelEntries{{Model: "openai/gpt-4o", Weight: 1}},
			},
		},
		Redirect: config.RedirectConfigs{
			{Source: "alias-model", Target: "internal-model"},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

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

	// internal 模型不应该在列表中
	if modelSet["internal-model"] {
		t.Error("internal-model should not appear in model list")
	}
}

func TestIntegration_Visible_ModelInternal(t *testing.T) {
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
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "internal-model",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}},
			},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	// 直接调用 internal 模型应返回 404
	body := `{"model":"internal-model","messages":[{"role":"user","content":"Hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

// TestIntegration_Exposure_InternalAndHidden 验证三档 exposure 在 HTTP 入口的行为：
// - public：出现在 /v1/models 且可直调
// - hidden：不出现在 /v1/models，但可直调
// - internal：不出现在 /v1/models，且外部直调返回 404
func TestIntegration_Exposure_InternalAndHidden(t *testing.T) {
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
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "pub", Exposure: exposurePtr(true), Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "hid", Exposure: hiddenExposurePtr(), Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "int", Exposure: internalExposurePtr(), Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)
	router.GET("/v1/models", Models)

	// /v1/models 只列出 public
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for /v1/models, got %d", w.Code)
	}
	var listResp dto.ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal models: %v", err)
	}
	listed := make(map[string]bool)
	for _, m := range listResp.Data {
		listed[m.ID] = true
	}
	if !listed["pub"] {
		t.Error("expected public model in /v1/models")
	}
	if listed["hid"] {
		t.Error("hidden model should not appear in /v1/models")
	}
	if listed["int"] {
		t.Error("internal model should not appear in /v1/models")
	}

	chat := func(model string) int {
		body := `{"model":"` + model + `","messages":[{"role":"user","content":"Hi"}]}`
		r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		return rec.Code
	}

	if code := chat("pub"); code != http.StatusOK {
		t.Errorf("public model should be callable, got %d", code)
	}
	if code := chat("hid"); code != http.StatusOK {
		t.Errorf("hidden model should be callable, got %d", code)
	}
	if code := chat("int"); code != http.StatusNotFound {
		t.Errorf("internal model should not be callable (404), got %d", code)
	}

	// messages 和 responses 入口对 internal 模型也应返回 404
	router.POST("/v1/messages", Messages)
	router.POST("/v1/responses", Responses)

	// messages 入口
	msgBody := `{"model":"int","max_tokens":100,"messages":[{"role":"user","content":"Hi"}]}`
	req = httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(msgBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for internal model via messages, got %d", w.Code)
	}

	// responses 入口
	respBody := `{"model":"int","input":[{"type":"message","role":"user","content":"Hello"}]}`
	req = httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(respBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for internal model via responses, got %d", w.Code)
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
			Protocols: []string{"openai.chat"},
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
		Redirect: config.RedirectConfigs{
			{Source: "alias-1", Target: "alias-2"},
			{Source: "alias-2", Target: "alias-3"},
			{Source: "alias-3", Target: "backend"},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

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
			Protocols: []string{"openai.responses"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"openai-chat-provider": {
			Endpoint:  chatUpstream.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai.chat"},
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
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/responses", Responses)

	return router, responsesUpstream
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
	router, upstream := setupResponsesTestHandler("", "")
	defer upstream.Close()

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

// chat-only provider 需要 responses→chat 转换：reasoning input item 必须被跳过而不是 502。
func TestResponsesHandler_ToOpenAIChat_WithReasoningInput(t *testing.T) {
	router, upstream := setupResponsesTestHandler("", "")
	defer upstream.Close()

	reqBody := `{"model":"chat-model","input":[
		{"type":"message","role":"user","content":"Hello"},
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thinking..."}]},
		{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_1","output":"sunny"}
	]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
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
// Chat → openai.responses provider 集成测试
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
			Protocols: []string{"openai.responses"},
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
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)
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

	// Upstream must receive the request on /v1/responses (openai.responses protocol)
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
	// Upstream returns a Responses payload with only a reasoning output type.
	body := `{"id":"resp-test","object":"response","created_at":1234567890,"model":"test-model","status":"completed","output":[{"type":"reasoning","id":"rs-1","summary":[{"type":"summary_text","text":"thinking..."}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`
	router, _, _ := setupChatResponseProviderHandler(t, body)

	reqBody := `{"model":"test-model","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices len = %d, want 1", len(resp.Choices))
	}
	if resp.Choices[0].Message.ReasoningContent != "thinking..." {
		t.Fatalf("reasoning_content = %q, want %q", resp.Choices[0].Message.ReasoningContent, "thinking...")
	}
	if resp.Choices[0].Message.Content != "" {
		t.Fatalf("content = %q, want empty", resp.Choices[0].Message.Content)
	}
}

// ============================================================================
// Messages → openai.responses provider 集成测试
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
			Protocols: []string{"openai.responses"},
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
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)
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

func TestHandleWriteResponseError_ClassifiesStreamInterruptions(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		status   string
		wantCode int
		wantBody bool
	}{
		// Client cancel: bare 499, no synthetic upstream 502 JSON.
		{name: "client canceled", err: context.Canceled, status: "client_canceled", wantCode: statusClientClosed, wantBody: false},
		{name: "upstream timeout", err: context.DeadlineExceeded, status: "upstream_timeout", wantCode: http.StatusBadGateway, wantBody: true},
		{name: "upstream eof", err: io.ErrUnexpectedEOF, status: "upstream_eof", wantCode: http.StatusBadGateway, wantBody: true},
		{name: "upstream read error", err: errors.New("read failure"), status: "upstream_read_error", wantCode: http.StatusBadGateway, wantBody: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics.ResetForTest()
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/test", nil)

			status, recordMetrics := handleWriteResponseError(c, errs.ProtocolOpenAI, tt.err, true, "test-provider", "test-model", "openai.responses")
			if !recordMetrics {
				t.Fatal("recordMetrics = false, want true")
			}
			if status != tt.status {
				t.Errorf("status = %q, want %q", status, tt.status)
			}
			// gin buffers status until WriteHeaderNow; use Writer.Status for bare-status paths.
			gotCode := c.Writer.Status()
			if tt.wantBody {
				// WriteError flushes to the recorder immediately.
				gotCode = w.Code
			}
			if gotCode != tt.wantCode {
				t.Errorf("status code = %d, want %d", gotCode, tt.wantCode)
			}
			if tt.wantBody {
				if w.Body.Len() == 0 {
					t.Fatal("expected error body")
				}
			} else if w.Body.Len() != 0 {
				t.Errorf("body should be empty, got %q", w.Body.String())
			}

			count := testutil.ToFloat64(metrics.GetStreamInterruptedTotal().WithLabelValues(
				"test-provider", "test-model", "openai.responses", tt.status,
			))
			if count != 1 {
				t.Errorf("stream interruption count = %f, want 1", count)
			}
		})
	}
}

func TestHandleWriteResponseError_NonStreamDoesNotRecordInterruption(t *testing.T) {
	metrics.ResetForTest()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	status, recordMetrics := handleWriteResponseError(c, errs.ProtocolOpenAI, io.ErrUnexpectedEOF, false, "test-provider", "test-model", "openai.responses")
	if !recordMetrics {
		t.Fatal("recordMetrics = false, want true")
	}
	if status != "upstream_eof" {
		t.Errorf("status = %q, want upstream_eof", status)
	}
	if got := testutil.CollectAndCount(metrics.GetStreamInterruptedTotal()); got != 0 {
		t.Errorf("stream interruption metrics = %d, want 0", got)
	}
}

func TestHandleWriteResponseError_ConversionErrorUsesCodecPath(t *testing.T) {
	metrics.ResetForTest()
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

	status, recordMetrics := handleWriteResponseError(c, errs.ProtocolOpenAI, err, true, "test-provider", "test-model", "openai.responses")
	if recordMetrics {
		t.Fatal("recordMetrics = true, want false")
	}
	if status != "conversion_error" {
		t.Errorf("status = %q, want conversion_error", status)
	}
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
	if got := testutil.CollectAndCount(metrics.GetStreamInterruptedTotal()); got != 0 {
		t.Errorf("stream interruption metrics = %d, want 0", got)
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

// ============================================================================
// Task 1: 验证非流式成功响应中 model 字段回显请求中的别名
// ============================================================================

// setupAliasTestHandler 建立一个带别名重定向的测试环境
// alias -> group -> provider。upstream 返回的响应中 model 字段为上游真实模型名。
func setupAliasTestHandler(t *testing.T, upstreamPath string, upstreamBody string, router *gin.Engine) (*httptest.Server, func()) {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(upstreamBody))
	}))

	falseVal := false
	providers := map[string]config.ProviderConfig{
		"real-provider": {
			Endpoint:  upstream.URL,
			APIKey:    "test-key",
			Protocols: []string{upstreamPath},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "real-model-group",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "real-provider/upstream-model", Weight: 1}},
			},
		},
		Redirect: config.RedirectConfigs{
			{Source: "my-alias", Target: "real-model-group"},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)
	cfg = testCfg

	return upstream, func() { upstream.Close() }
}

// TestChat_SuccessEchoesRequestedAliasModel 验证 OpenAI Chat 入方向，非流式响应中 model 字段等于请求中的别名
func TestChat_SuccessEchoesRequestedAliasModel(t *testing.T) {
	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	// upstream 返回 openai chat 格式，model 字段是上游真实模型名
	upstreamBody := `{"id":"chatcmpl-x","object":"chat.completion","created":1234567890,"model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`
	_, cleanup := setupAliasTestHandler(t, "openai.chat", upstreamBody, router)
	defer cleanup()

	reqBody := `{"model":"my-alias","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "my-alias" {
		t.Errorf("model = %q, want %q (should echo requested alias)", out.Model, "my-alias")
	}
}

// ============================================================================
// Task 3: winner 变化回归测试 — 无论哪个 provider 胜出，客户端看到的 model 始终是别名
// ============================================================================

// setupFailoverAliasHandler 构建一个 failover 场景：
// - primary-provider 返回 500（失败）
// - secondary-provider 返回 200（成功）
// - alias "fast-model" → group "model-backend"（包含两个候选）
func setupFailoverAliasHandler(t *testing.T, router *gin.Engine, primaryProtocol, secondaryProtocol string, primaryBody, secondaryBody string) (primarySrv *httptest.Server, secondarySrv *httptest.Server, cleanup func()) {
	t.Helper()

	primarySrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"primary failed"}`))
	}))

	secondarySrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(secondaryBody))
	}))

	falseVal := false
	providers := map[string]config.ProviderConfig{
		"primary-provider": {
			Endpoint:  primarySrv.URL,
			APIKey:    "key",
			Protocols: []string{primaryProtocol},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"secondary-provider": {
			Endpoint:  secondarySrv.URL,
			APIKey:    "key",
			Protocols: []string{secondaryProtocol},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "model-backend",
				Mode:     "failover",
				Exposure: exposurePtr(falseVal),
				Models: config.ModelEntries{
					{Model: "primary-provider/primary-upstream", Weight: 1},
					{Model: "secondary-provider/secondary-upstream", Weight: 1},
				},
			},
		},
		Redirect: config.RedirectConfigs{
			{Source: "fast-model", Target: "model-backend"},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)
	cfg = testCfg

	return primarySrv, secondarySrv, func() {
		primarySrv.Close()
		secondarySrv.Close()
	}
}

// TestChat_RequestedAliasSurvivesWinnerChange 验证：failover 时第二个 provider 胜出，
// 但 OpenAI Chat 响应的 model 字段仍为请求中的别名。
func TestChat_RequestedAliasSurvivesWinnerChange(t *testing.T) {
	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	secondaryBody := `{"id":"chatcmpl-x","object":"chat.completion","created":1234567890,"model":"secondary-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`

	_, _, cleanup := setupFailoverAliasHandler(t, router, "openai.chat", "openai.chat", "", secondaryBody)
	defer cleanup()

	reqBody := `{"model":"fast-model","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "fast-model" {
		t.Errorf("model = %q, want %q (winner change must not leak upstream model)", out.Model, "fast-model")
	}
}

// TestMessages_RequestedAliasSurvivesWinnerChange 验证：failover 时第二个 provider 胜出，
// 但 Anthropic Messages 响应的 model 字段仍为请求中的别名。
func TestMessages_RequestedAliasSurvivesWinnerChange(t *testing.T) {
	router := gin.New()
	router.POST("/v1/messages", Messages)

	secondaryBody := `{"id":"msg-x","type":"message","role":"assistant","content":[{"type":"text","text":"Hi"}],"model":"secondary-upstream","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`

	_, _, cleanup := setupFailoverAliasHandler(t, router, "anthropic.messages", "anthropic.messages", "", secondaryBody)
	defer cleanup()

	reqBody := `{"model":"fast-model","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out dto.ClaudeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "fast-model" {
		t.Errorf("model = %q, want %q (winner change must not leak upstream model)", out.Model, "fast-model")
	}
}

// TestResponses_RequestedAliasSurvivesWinnerChange 验证：failover 时第二个 provider 胜出，
// 但 OpenAI Responses 响应的 model 字段仍为请求中的别名。
func TestResponses_RequestedAliasSurvivesWinnerChange(t *testing.T) {
	router := gin.New()
	router.POST("/v1/responses", Responses)

	secondaryBody := `{"id":"resp-x","object":"response","created_at":1234567890,"model":"secondary-upstream","status":"completed","output":[{"type":"message","id":"msg-x","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`

	_, _, cleanup := setupFailoverAliasHandler(t, router, "openai.responses", "openai.responses", "", secondaryBody)
	defer cleanup()

	reqBody := `{"model":"fast-model","input":[{"type":"message","role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "fast-model" {
		t.Errorf("model = %q, want %q (winner change must not leak upstream model)", out.Model, "fast-model")
	}
}

// TestMessages_SuccessEchoesRequestedAliasModel 验证 Anthropic Messages 入方向，非流式响应中 model 字段等于请求中的别名
func TestMessages_SuccessEchoesRequestedAliasModel(t *testing.T) {
	router := gin.New()
	router.POST("/v1/messages", Messages)

	// upstream 返回 anthropic 格式，model 字段是上游真实模型名
	upstreamBody := `{"id":"msg-x","type":"message","role":"assistant","content":[{"type":"text","text":"Hi"}],"model":"upstream-model","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`
	_, cleanup := setupAliasTestHandler(t, "anthropic.messages", upstreamBody, router)
	defer cleanup()

	reqBody := `{"model":"my-alias","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out dto.ClaudeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "my-alias" {
		t.Errorf("model = %q, want %q (should echo requested alias)", out.Model, "my-alias")
	}
}

// TestResponses_SuccessEchoesRequestedAliasModel 验证 OpenAI Responses 入方向，非流式响应中 model 字段等于请求中的别名
func TestResponses_SuccessEchoesRequestedAliasModel(t *testing.T) {
	router := gin.New()
	router.POST("/v1/responses", Responses)

	// upstream 返回 openai.responses 格式，model 字段是上游真实模型名
	upstreamBody := `{"id":"resp-x","object":"response","created_at":1234567890,"model":"upstream-model","status":"completed","output":[{"type":"message","id":"msg-x","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`
	_, cleanup := setupAliasTestHandler(t, "openai.responses", upstreamBody, router)
	defer cleanup()

	reqBody := `{"model":"my-alias","input":[{"type":"message","role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "my-alias" {
		t.Errorf("model = %q, want %q (should echo requested alias)", out.Model, "my-alias")
	}
}

// ============================================================================
// Task 4: ollama.chat 端到端集成测试
// ============================================================================

// ollamaChatResponse 构建一个标准的 Ollama /api/chat 非流式响应体
func ollamaChatResponse(model string) string {
	return `{"model":"` + model + `","created_at":"2024-01-01T00:00:00Z","message":{"role":"assistant","content":"Hi there!"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`
}

// setupOllamaProviderHandler 建立一个 ollama.chat provider 的测试环境，
// 返回 router、receivedPath 指针和 receivedHeaders 指针。
func setupOllamaProviderHandler(t *testing.T, inboundPath string, handler gin.HandlerFunc, apiKey string) (*gin.Engine, *httptest.Server, *string, *http.Header) {
	t.Helper()

	var receivedPath string
	var receivedHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(ollamaChatResponse("llama3")))
	}))

	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Endpoint:  upstream.URL,
			APIKey:    apiKey,
			Protocols: []string{"ollama.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "llama3", Models: config.ModelEntries{{Model: "ollama-provider/llama3", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)
	cfg = testCfg

	router := gin.New()
	router.POST(inboundPath, handler)

	t.Cleanup(func() { upstream.Close() })
	return router, upstream, &receivedPath, &receivedHeaders
}

// TestHandler_ToOllamaChat_ViaChat 验证 /v1/chat/completions 入方向打到 /api/chat
func TestHandler_ToOllamaChat_ViaChat(t *testing.T) {
	router, _, receivedPath, _ := setupOllamaProviderHandler(t, "/v1/chat/completions", Chat, "test-key")

	reqBody := `{"model":"llama3","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if *receivedPath != "/api/chat" {
		t.Errorf("upstream path = %q, want %q", *receivedPath, "/api/chat")
	}
}

// TestHandler_ToOllamaChat_ViaMessages 验证 /v1/messages 入方向打到 /api/chat
func TestHandler_ToOllamaChat_ViaMessages(t *testing.T) {
	router, _, receivedPath, _ := setupOllamaProviderHandler(t, "/v1/messages", Messages, "test-key")

	reqBody := `{"model":"llama3","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if *receivedPath != "/api/chat" {
		t.Errorf("upstream path = %q, want %q", *receivedPath, "/api/chat")
	}
}

// TestHandler_ToOllamaChat_ViaResponses 验证 /v1/responses 入方向打到 /api/chat
func TestHandler_ToOllamaChat_ViaResponses(t *testing.T) {
	router, _, receivedPath, _ := setupOllamaProviderHandler(t, "/v1/responses", Responses, "test-key")

	reqBody := `{"model":"llama3","input":[{"type":"message","role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if *receivedPath != "/api/chat" {
		t.Errorf("upstream path = %q, want %q", *receivedPath, "/api/chat")
	}
}

// TestOllamaChat_EmptyAPIKey_NoAuthHeader 验证 api_key 为空时不发送 Authorization 头
func TestOllamaChat_EmptyAPIKey_NoAuthHeader(t *testing.T) {
	router, _, receivedPath, receivedHeaders := setupOllamaProviderHandler(t, "/v1/chat/completions", Chat, "")

	reqBody := `{"model":"llama3","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if *receivedPath != "/api/chat" {
		t.Errorf("upstream path = %q, want %q", *receivedPath, "/api/chat")
	}
	if (*receivedHeaders).Get("Authorization") != "" {
		t.Errorf("Authorization header should be empty when api_key is empty, got: %q", (*receivedHeaders).Get("Authorization"))
	}
}

// TestOllamaChat_AliasModelEchoedInResponse 验证 alias model 在成功响应里保持客户端请求名
func TestOllamaChat_AliasModelEchoedInResponse(t *testing.T) {
	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	// 建立 alias -> group -> ollama provider 的配置
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// upstream 响应中的 model 字段是上游真实模型名，不是别名
		w.Write([]byte(ollamaChatResponse("llama3")))
	}))
	defer upstream.Close()

	falseVal := false
	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Endpoint:  upstream.URL,
			APIKey:    "key",
			Protocols: []string{"ollama.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "llama3-backend",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "ollama-provider/llama3", Weight: 1}},
			},
		},
		Redirect: config.RedirectConfigs{
			{Source: "my-llama", Target: "llama3-backend"},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)
	cfg = testCfg

	reqBody := `{"model":"my-llama","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "my-llama" {
		t.Errorf("model = %q, want %q (alias must not leak upstream model)", out.Model, "my-llama")
	}
}

// TestOllamaChat_MultimodalConversionFailure 验证多模态请求在 encode 阶段失败并返回 error，
// 且上游不应该收到成功的调用。
func TestOllamaChat_MultimodalConversionFailure(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(ollamaChatResponse("llama3")))
	}))
	defer upstream.Close()

	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Endpoint:  upstream.URL,
			APIKey:    "key",
			Protocols: []string{"ollama.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "llama3", Models: config.ModelEntries{{Model: "ollama-provider/llama3", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)
	cfg = testCfg

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	// 多模态请求（包含 image_url）在转换为 ollama.chat 时应失败
	reqBody := `{"model":"llama3","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// encode 失败应返回 502 BadGateway
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d, body: %s", w.Code, w.Body.String())
	}
	// 上游不应该被调用
	if upstreamCalled {
		t.Error("upstream should not be called when encode fails")
	}
}

// ============================================================================
// Task 3: 验证从 handler 发起的"请求成功但内部 attempt 失败"场景在指标上可见
// ============================================================================

// TestChat_RequestSuccessButAttemptFailed 验证 failover 场景下请求成功但首个 provider attempt 失败的指标可见性
func TestChat_RequestSuccessButAttemptFailed(t *testing.T) {
	// 重置 metrics
	metrics.ResetForTest()
	stats.Reset()

	// 首个 provider 返回 402 (hard failure)
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"error":{"message":"payment required","type":"payment_error","code":"payment_required"}}`))
	}))
	defer primarySrv.Close()

	// 第二个 provider 返回 200 (success)
	secondarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer secondarySrv.Close()

	providers := map[string]config.ProviderConfig{
		"primary-provider": {
			Endpoint:  primarySrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"secondary-provider": {
			Endpoint:  secondarySrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name: "test-model",
				Mode: "failover",
				Models: config.ModelEntries{
					{Model: "primary-provider/gpt-4", Weight: 1},
					{Model: "secondary-provider/gpt-4", Weight: 1},
				},
			},
		},
	}

	testResolver, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	testClient := provider.NewClient(providers, 120*time.Second, 0, 0)
	testRLManager := ratelimit.NewManager(providers)
	testHealthChecker := health.NewChecker(3, 30*time.Second)
	testSched := scheduler.New(testRLManager, testClient, testHealthChecker, 500*time.Millisecond, 0, 0)

	// 初始化 stats recorder，使用临时文件让 migration 和当前连接共享同一数据库
	testDBPath := filepath.Join(t.TempDir(), "handler-stats.db")
	if err := stats.Init(testDBPath); err != nil {
		t.Fatalf("Init stats error: %v", err)
	}
	defer stats.Reset()

	// 临时替换全局变量
	oldCfg, oldResolver, oldSched := cfg, resolver, sched
	cfg, resolver, sched = testCfg, testResolver, testSched
	defer func() { cfg, resolver, sched = oldCfg, oldResolver, oldSched }()

	router := gin.New()
	// 注入 key_name 的 middleware
	router.Use(func(c *gin.Context) {
		c.Set("key_name", "test-key-123")
		c.Next()
	})
	router.POST("/v1/chat/completions", Chat)

	reqBody := `{"model":"test-model","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// 验证客户端最终响应成功
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// 验证 request_total{status="success"} 增加
	requestTotalCounter := metrics.GetRequestTotal()
	if requestTotalCounter == nil {
		t.Fatal("requestTotal counter is nil")
	}

	// 验证请求级指标：成功
	err = testutil.CollectAndCompare(requestTotalCounter, strings.NewReader(`
		# HELP request_total 请求总数
		# TYPE request_total counter
		request_total{inbound_protocol="openai.chat",key_name="test-key-123",model_group="test-model",outbound_protocol="openai.chat",provider="secondary-provider",status="success",upstream_model="gpt-4"} 1
	`), "request_total")
	if err != nil {
		t.Errorf("request_total metric mismatch: %v", err)
	}

	// 验证 attempt 级指标：首个 provider 402 硬失败
	providerAttemptCounter := metrics.GetProviderAttemptTotal()
	if providerAttemptCounter == nil {
		t.Fatal("providerAttemptTotal counter is nil")
	}

	// 验证首个 provider 的 402 硬失败 attempt 指标
	failCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"failover", "primary-provider", "gpt-4", "test-model", "openai.chat", "hard_failure", "quota_exceeded", "402",
	))
	if failCount != 1 {
		t.Errorf("expected 1 hard_failure attempt for primary-provider, got %f", failCount)
	}

	// 验证第二个 provider 的成功 attempt 指标
	successCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"failover", "secondary-provider", "gpt-4", "test-model", "openai.chat", "success", "", "",
	))
	if successCount != 1 {
		t.Errorf("expected 1 success attempt for secondary-provider, got %f", successCount)
	}
}

// ============================================================================
// Task 3: Model Group Soft Fallback 端到端测试
// ============================================================================

// TestChat_SoftFailureTriggersFallback 验证主链路 soft failure 触发 fallback，fallback provider 使用不同协议返回成功
func TestChat_UsesModelLevelAllowedProtocol(t *testing.T) {
	metrics.ResetForTest()
	stats.Reset()

	var openAIHits int
	openAISrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openAIHits++
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("openai path = %q, want %q", r.URL.Path, "/v1/chat/completions")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"chatcmpl-glm",
			"object":"chat.completion",
			"created":1234567890,
			"model":"glm-5.2",
			"choices":[{"index":0,"message":{"role":"assistant","content":"openai route"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
		}`))
	}))
	defer openAISrv.Close()

	anthropicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected anthropic request path=%q", r.URL.Path)
	}))
	defer anthropicSrv.Close()

	providers := map[string]config.ProviderConfig{
		"mixed-provider": {
			Endpoint: openAISrv.URL,
			Endpoints: []config.EndpointConfig{
				{URL: anthropicSrv.URL, Protocols: []string{"anthropic.messages"}},
				{URL: openAISrv.URL, Protocols: []string{"openai.chat"}},
			},
			APIKey:    "test-key",
			Protocols: []string{"openai.chat", "anthropic.messages"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		Rules: []config.RuleConfig{{
			Match: config.RuleMatch{
				UpstreamModel: &config.RuleCondition{
					Op:    "equals",
					Value: "mixed-provider/glm-5.2",
				},
			},
			Action: config.RuleAction{Protocol: "openai.chat"},
		}},
		ModelGroups: []config.ModelGroupConfig{{
			Name:   "glm-5.2",
			Models: config.ModelEntries{{Model: "mixed-provider/glm-5.2", Weight: 1}},
		}},
	}

	testResolver, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	testClient := provider.NewClient(providers, 120*time.Second, 0, 0)
	testRLManager := ratelimit.NewManager(providers)
	testHealthChecker := health.NewChecker(3, 30*time.Second)
	testSched := scheduler.New(testRLManager, testClient, testHealthChecker, 500*time.Millisecond, 0, 0)

	testDBPath := filepath.Join(t.TempDir(), "handler-model-protocol-stats.db")
	if err := stats.Init(testDBPath); err != nil {
		t.Fatalf("Init stats error: %v", err)
	}
	defer stats.Reset()

	oldCfg, oldResolver, oldSched := cfg, resolver, sched
	cfg, resolver, sched = testCfg, testResolver, testSched
	defer func() { cfg, resolver, sched = oldCfg, oldResolver, oldSched }()

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("key_name", "test-key-123")
		c.Next()
	})
	router.POST("/v1/messages", Messages)

	reqBody := `{"model":"glm-5.2","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"Hello"}]}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if openAIHits != 1 {
		t.Fatalf("openai hits = %d, want 1", openAIHits)
	}

	err = testutil.CollectAndCompare(metrics.GetRequestTotal(), strings.NewReader(`
		# HELP request_total 请求总数
		# TYPE request_total counter
		request_total{inbound_protocol="anthropic.messages",key_name="test-key-123",model_group="glm-5.2",outbound_protocol="openai.chat",provider="mixed-provider",status="success",upstream_model="glm-5.2"} 1
	`), "request_total")
	if err != nil {
		t.Errorf("request_total metric mismatch: %v", err)
	}
}

// ============================================================================
// Task 3: Nested Group 端到端测试
// ============================================================================

// TestChat_NestedGroupScheduling 验证：顶层 failover + 子 group concurrent，
// 第一个子组所有 provider 失败，最终命中第二个子组的 provider。
// 响应 model 字段仍应回显请求中的模型名。
func TestChat_NestedGroupScheduling(t *testing.T) {
	// 子组 A 的 provider 返回 500（硬失败）
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"fail","type":"server_error"}}`))
	}))
	defer failSrv.Close()

	// 子组 B 的 provider 返回 200
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-ok","object":"chat.completion","created":1234567890,"model":"child-b-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"Hi from B"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer successSrv.Close()

	falseVal := false
	providers := map[string]config.ProviderConfig{
		"provider-a": {
			Endpoint:  failSrv.URL,
			APIKey:    "key",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"provider-b": {
			Endpoint:  successSrv.URL,
			APIKey:    "key",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "child-a",
				Mode:     "concurrent",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "provider-a/model-a", Weight: 1}},
			},
			{
				Name:     "child-b",
				Mode:     "concurrent",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "provider-b/model-b", Weight: 1}},
			},
			{
				Name: "nested-top",
				Mode: "failover",
				Models: config.ModelEntries{
					{Model: "child-a", Weight: 1},
					{Model: "child-b", Weight: 1},
				},
			},
		},
	}

	testResolver, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	testClient := provider.NewClient(providers, 120*time.Second, 0, 0)
	testRLManager := ratelimit.NewManager(providers)
	testHealthChecker := health.NewChecker(3, 30*time.Second)
	testSched := scheduler.New(testRLManager, testClient, testHealthChecker, 500*time.Millisecond, 0, 0)

	oldCfg, oldResolver, oldSched := cfg, resolver, sched
	cfg, resolver, sched = testCfg, testResolver, testSched
	defer func() { cfg, resolver, sched = oldCfg, oldResolver, oldSched }()

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	reqBody := `{"model":"nested-top","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}

	// model 必须回显请求中的模型名
	if out.Model != "nested-top" {
		t.Errorf("model = %q, want %q (nested group must not leak upstream model)", out.Model, "nested-top")
	}

	// 内容应来自子组 B
	if len(out.Choices) == 0 || out.Choices[0].Message.Content != "Hi from B" {
		t.Errorf("content = %q, want %q", func() string {
			if len(out.Choices) > 0 {
				return out.Choices[0].Message.Content
			}
			return ""
		}(), "Hi from B")
	}
}

// TestMessages_NestedGroupScheduling 验证：Messages 入方向，顶层 failover + 子 group concurrent，
// 第一个子组所有 provider 失败，最终命中第二个子组的 provider。
// 响应 model 字段仍应回显请求中的模型名。
func TestMessages_NestedGroupScheduling(t *testing.T) {
	// 子组 A 的 provider 返回 500（硬失败）
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"type":"error","error":{"type":"server_error","message":"fail"}}`))
	}))
	defer failSrv.Close()

	// 子组 B 的 provider 返回 200
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg-ok","type":"message","role":"assistant","content":[{"type":"text","text":"Hi from B"}],"model":"child-b-upstream","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`))
	}))
	defer successSrv.Close()

	falseVal := false
	providers := map[string]config.ProviderConfig{
		"provider-a": {
			Endpoint:  failSrv.URL,
			APIKey:    "key",
			Protocols: []string{"anthropic.messages"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"provider-b": {
			Endpoint:  successSrv.URL,
			APIKey:    "key",
			Protocols: []string{"anthropic.messages"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "child-a",
				Mode:     "concurrent",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "provider-a/model-a", Weight: 1}},
			},
			{
				Name:     "child-b",
				Mode:     "concurrent",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "provider-b/model-b", Weight: 1}},
			},
			{
				Name: "nested-top",
				Mode: "failover",
				Models: config.ModelEntries{
					{Model: "child-a", Weight: 1},
					{Model: "child-b", Weight: 1},
				},
			},
		},
	}

	testResolver, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	testClient := provider.NewClient(providers, 120*time.Second, 0, 0)
	testRLManager := ratelimit.NewManager(providers)
	testHealthChecker := health.NewChecker(3, 30*time.Second)
	testSched := scheduler.New(testRLManager, testClient, testHealthChecker, 500*time.Millisecond, 0, 0)

	oldCfg, oldResolver, oldSched := cfg, resolver, sched
	cfg, resolver, sched = testCfg, testResolver, testSched
	defer func() { cfg, resolver, sched = oldCfg, oldResolver, oldSched }()

	router := gin.New()
	router.POST("/v1/messages", Messages)

	reqBody := `{"model":"nested-top","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out dto.ClaudeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}

	// model 必须回显请求中的模型名
	if out.Model != "nested-top" {
		t.Errorf("model = %q, want %q (nested group must not leak upstream model)", out.Model, "nested-top")
	}

	// 内容应来自子组 B
	if len(out.Content) == 0 || out.Content[0].Text != "Hi from B" {
		t.Errorf("content = %q, want %q", func() string {
			if len(out.Content) > 0 {
				return out.Content[0].Text
			}
			return ""
		}(), "Hi from B")
	}
}

// ============================================================================
// Task 2: /v1/models context_length 测试
// ============================================================================

// setTestCatalogSource 注入一个基于 map 的测试 catalog source。
func setTestCatalogSource(entries catalogContextIndex) {
	src := entries // catalogContextIndex 实现了 CatalogSource 接口
	SetCatalogSource(src)
}

// setupModelsContextRouter 构建只含 /v1/models 路由的测试 router，同时接受外部注入的
// catalog source，用于覆盖包级全局状态。
func setupModelsContextRouter(t *testing.T, testCfg *config.Config) (*gin.Engine, func()) {
	t.Helper()

	testResolver, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}

	oldCfg, oldResolver := cfg, resolver
	oldCatalogSrc := catalogSrc

	cfg = testCfg
	resolver = testResolver

	router := gin.New()
	router.GET("/v1/models", Models)

	cleanup := func() {
		cfg, resolver = oldCfg, oldResolver
		catalogSrc = oldCatalogSrc
	}
	return router, cleanup
}

// TestModels_ContextLength_ConfigOverride 验证：group 配置中 context_length 覆盖 catalog。
func TestModels_ContextLength_ConfigOverride(t *testing.T) {
	cl := 32768
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com", APIKey: "k", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:   "gpt-4",
				Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}},
				ModelMetadata: config.ModelMetadataConfig{
					ContextLength: &cl,
				},
			},
		},
	}

	router, cleanup := setupModelsContextRouter(t, testCfg)
	defer cleanup()

	// catalog 中有一个不同的值，应被配置覆盖忽略
	setTestCatalogSource(catalogContextIndex{"openai/gpt-4": 8192})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp dto.ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("models count = %d, want 1", len(resp.Data))
	}
	m := resp.Data[0]
	if m.ID != "gpt-4" {
		t.Errorf("id = %q, want %q", m.ID, "gpt-4")
	}
	if m.ContextLength == nil {
		t.Fatal("context_length is nil, want 32768")
	}
	if *m.ContextLength != 32768 {
		t.Errorf("context_length = %d, want 32768", *m.ContextLength)
	}
}

// TestModels_ContextLength_CatalogHit 验证：无配置覆盖时从 catalog 返回。
func TestModels_ContextLength_CatalogHit(t *testing.T) {
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"anthropic": {Endpoint: "https://api.anthropic.com", APIKey: "k", Protocols: []string{"anthropic.messages"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:   "claude-3",
				Models: config.ModelEntries{{Model: "anthropic/claude-3-opus-20240229", Weight: 1}},
			},
		},
	}

	router, cleanup := setupModelsContextRouter(t, testCfg)
	defer cleanup()

	setTestCatalogSource(catalogContextIndex{"anthropic/claude-3-opus-20240229": 200000})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp dto.ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("models count = %d, want 1", len(resp.Data))
	}
	m := resp.Data[0]
	if m.ContextLength == nil {
		t.Fatal("context_length is nil, want 200000")
	}
	if *m.ContextLength != 200000 {
		t.Errorf("context_length = %d, want 200000", *m.ContextLength)
	}
}

// TestModels_ContextLength_MinAcrossLeaves 验证：多个叶子候选时取最小值。
func TestModels_ContextLength_MinAcrossLeaves(t *testing.T) {
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"provider-a": {Endpoint: "https://a.com", APIKey: "k", Protocols: []string{"openai.chat"}},
			"provider-b": {Endpoint: "https://b.com", APIKey: "k", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name: "fast-model",
				Models: config.ModelEntries{
					{Model: "provider-a/model-x", Weight: 1},
					{Model: "provider-b/model-x", Weight: 1},
				},
			},
		},
	}

	router, cleanup := setupModelsContextRouter(t, testCfg)
	defer cleanup()

	// provider-a 报告 128000，provider-b 报告 64000，应取最小值 64000
	setTestCatalogSource(catalogContextIndex{
		"provider-a/model-x": 128000,
		"provider-b/model-x": 64000,
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp dto.ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("models count = %d, want 1", len(resp.Data))
	}
	m := resp.Data[0]
	if m.ContextLength == nil {
		t.Fatal("context_length is nil, want 64000")
	}
	if *m.ContextLength != 64000 {
		t.Errorf("context_length = %d, want 64000 (minimum across leaves)", *m.ContextLength)
	}
}

// TestModels_ContextLength_AliasInheritsFinalGroup 验证：alias 继承最终 group 的配置与叶子，
// 响应中 id 仍为 alias 自身。
func TestModels_ContextLength_AliasInheritsFinalGroup(t *testing.T) {
	falseVal := false
	cl := 16384
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com", APIKey: "k", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "gpt-4-backend",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "openai/gpt-4-turbo", Weight: 1}},
				ModelMetadata: config.ModelMetadataConfig{
					ContextLength: &cl,
				},
			},
		},
		Redirect: config.RedirectConfigs{
			{Source: "my-gpt4", Target: "gpt-4-backend"},
		},
	}

	router, cleanup := setupModelsContextRouter(t, testCfg)
	defer cleanup()

	// catalog 有不同值，但配置覆盖应生效（继承自 gpt-4-backend group）
	setTestCatalogSource(catalogContextIndex{"openai/gpt-4-turbo": 128000})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp dto.ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	// 只有 alias 可见，gpt-4-backend 不可见
	if len(resp.Data) != 1 {
		t.Fatalf("models count = %d, want 1 (only alias visible)", len(resp.Data))
	}
	m := resp.Data[0]
	if m.ID != "my-gpt4" {
		t.Errorf("id = %q, want %q (alias id must be preserved)", m.ID, "my-gpt4")
	}
	if m.ContextLength == nil {
		t.Fatal("context_length is nil, want 16384 (inherited from final group config)")
	}
	if *m.ContextLength != 16384 {
		t.Errorf("context_length = %d, want 16384 (inherited from gpt-4-backend config override)", *m.ContextLength)
	}
}

// TestModels_ContextLength_CompositeGroup 验证：复合 group（含子 group）的 context_length 计算。
// 规则（覆盖语义）：顶层无配置时，递归收集子节点配置 + 叶子 catalog，取最小。
func TestModels_ContextLength_CompositeGroup(t *testing.T) {
	cheapCL := 100000
	scoutCL := 50000
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai":    {Endpoint: "https://api.openai.com", APIKey: "k", Protocols: []string{"openai.chat"}},
			"deepseek":  {Endpoint: "https://api.deepseek.com", APIKey: "k", Protocols: []string{"openai.chat"}},
			"anthropic": {Endpoint: "https://api.anthropic.com", APIKey: "k", Protocols: []string{"anthropic.messages"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				// 顶层复合 group，无配置，引用两个子 group
				Name:   "auto",
				Mode:   "failover",
				Models: config.ModelEntries{{Model: "cheap-group"}, {Model: "scout-group"}},
			},
			{
				// 子 group 有配置，不展开叶子
				Name:   "cheap-group",
				Models: config.ModelEntries{{Model: "openai/gpt-4o-mini"}},
				ModelMetadata: config.ModelMetadataConfig{
					ContextLength: &cheapCL,
				},
			},
			{
				// 子 group 有配置，不展开叶子
				Name:   "scout-group",
				Models: config.ModelEntries{{Model: "deepseek/deepseek-chat"}},
				ModelMetadata: config.ModelMetadataConfig{
					ContextLength: &scoutCL,
				},
			},
		},
	}

	router, cleanup := setupModelsContextRouter(t, testCfg)
	defer cleanup()

	// catalog 中有叶子值，但子 group 有配置应覆盖（不查叶子）
	setTestCatalogSource(catalogContextIndex{
		"openai/gpt-4o-mini":     128000,
		"deepseek/deepseek-chat": 64000,
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp dto.ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	// 找到 auto model
	var autoModel *dto.ModelInfo
	for i := range resp.Data {
		if resp.Data[i].ID == "auto" {
			autoModel = &resp.Data[i]
			break
		}
	}
	if autoModel == nil {
		t.Fatal("auto model not found in response")
	}

	// auto 无配置，子 group cheap-group=100000, scout-group=50000，取最小 50000
	// catalog 叶子值被配置覆盖，不参与计算
	if autoModel.ContextLength == nil {
		t.Fatal("context_length is nil, want 50000")
	}
	if *autoModel.ContextLength != 50000 {
		t.Errorf("context_length = %d, want 50000 (min of sub-group configs, catalog ignored)", *autoModel.ContextLength)
	}
}

// TestModels_ContextLength_CompositeGroupPartialConfig 验证：部分子 group 有配置，部分无配置。
// 有配置的用配置值，无配置的展开查叶子 catalog，整体取最小。
func TestModels_ContextLength_CompositeGroupPartialConfig(t *testing.T) {
	cheapCL := 100000
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai":   {Endpoint: "https://api.openai.com", APIKey: "k", Protocols: []string{"openai.chat"}},
			"deepseek": {Endpoint: "https://api.deepseek.com", APIKey: "k", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:   "auto",
				Mode:   "failover",
				Models: config.ModelEntries{{Model: "cheap-group"}, {Model: "scout-group"}},
			},
			{
				// 子 group 有配置，不展开
				Name:   "cheap-group",
				Models: config.ModelEntries{{Model: "openai/gpt-4o-mini"}},
				ModelMetadata: config.ModelMetadataConfig{
					ContextLength: &cheapCL,
				},
			},
			{
				// 子 group 无配置，展开查叶子 catalog
				Name:   "scout-group",
				Models: config.ModelEntries{{Model: "deepseek/deepseek-chat"}},
			},
		},
	}

	router, cleanup := setupModelsContextRouter(t, testCfg)
	defer cleanup()

	// catalog 叶子值
	setTestCatalogSource(catalogContextIndex{
		"openai/gpt-4o-mini":     200000, // 被 cheap-group 配置覆盖，不参与
		"deepseek/deepseek-chat": 64000,  // scout-group 无配置，参与计算
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp dto.ModelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	var autoModel *dto.ModelInfo
	for i := range resp.Data {
		if resp.Data[i].ID == "auto" {
			autoModel = &resp.Data[i]
			break
		}
	}
	if autoModel == nil {
		t.Fatal("auto model not found in response")
	}

	// auto 无配置
	// cheap-group 有配置 100000
	// scout-group 无配置 → 叶子 catalog 64000
	// 取最小 64000
	if autoModel.ContextLength == nil {
		t.Fatal("context_length is nil, want 64000")
	}
	if *autoModel.ContextLength != 64000 {
		t.Errorf("context_length = %d, want 64000 (min of config 100000 and catalog 64000)", *autoModel.ContextLength)
	}
}

// TestResponses_NestedGroupScheduling 验证：Responses 入方向，顶层 failover + 子 group concurrent，
// 第一个子组所有 provider 失败，最终命中第二个子组的 provider。
// 响应 model 字段仍应回显请求中的模型名。
func TestResponses_NestedGroupScheduling(t *testing.T) {
	// 子组 A 的 provider 返回 500（硬失败）
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"fail","type":"server_error"}}`))
	}))
	defer failSrv.Close()

	// 子组 B 的 provider 返回 200
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp-ok","object":"response","created_at":1234567890,"model":"child-b-upstream","status":"completed","output":[{"type":"message","id":"msg-ok","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hi from B"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`))
	}))
	defer successSrv.Close()

	falseVal := false
	providers := map[string]config.ProviderConfig{
		"provider-a": {
			Endpoint:  failSrv.URL,
			APIKey:    "key",
			Protocols: []string{"openai.responses"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"provider-b": {
			Endpoint:  successSrv.URL,
			APIKey:    "key",
			Protocols: []string{"openai.responses"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "child-a",
				Mode:     "concurrent",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "provider-a/model-a", Weight: 1}},
			},
			{
				Name:     "child-b",
				Mode:     "concurrent",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "provider-b/model-b", Weight: 1}},
			},
			{
				Name: "nested-top",
				Mode: "failover",
				Models: config.ModelEntries{
					{Model: "child-a", Weight: 1},
					{Model: "child-b", Weight: 1},
				},
			},
		},
	}

	testResolver, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	testClient := provider.NewClient(providers, 120*time.Second, 0, 0)
	testRLManager := ratelimit.NewManager(providers)
	testHealthChecker := health.NewChecker(3, 30*time.Second)
	testSched := scheduler.New(testRLManager, testClient, testHealthChecker, 500*time.Millisecond, 0, 0)

	oldCfg, oldResolver, oldSched := cfg, resolver, sched
	cfg, resolver, sched = testCfg, testResolver, testSched
	defer func() { cfg, resolver, sched = oldCfg, oldResolver, oldSched }()

	router := gin.New()
	router.POST("/v1/responses", Responses)

	reqBody := `{"model":"nested-top","input":[{"type":"message","role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}

	// model 必须回显请求中的模型名
	if out.Model != "nested-top" {
		t.Errorf("model = %q, want %q (nested group must not leak upstream model)", out.Model, "nested-top")
	}

	// 内容应来自子组 B
	if len(out.Output) == 0 {
		t.Errorf("output is empty, want message from child-b")
	}
}

// ============================================================================
// Task 2: DeepSeek tokenizer handler 集成验证
// ============================================================================

// findProjectRoot 从当前测试文件位置向上走两级到项目根。
func findProjectRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

// TestChat_DeepseekModelTokenizerIntegration 验证 chat handler 在加载
// deepseek tokenizer 后，对含 "deepseek" upstream model 的请求正常处理。
func TestChat_DeepseekModelTokenizerIntegration(t *testing.T) {
	// tokenizer 已由 TestMain 从嵌入数据加载

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-ds","object":"chat.completion","created":1234567890,"model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"来自 DeepSeek 的回复"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer upstream.Close()

	providers := map[string]config.ProviderConfig{
		"ds-provider": {
			Endpoint:  upstream.URL,
			APIKey:    "ds-key",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "deepseek-group", Models: config.ModelEntries{{Model: "ds-provider/deepseek-chat", Weight: 1}}},
		},
	}

	testResolver, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	testClient := provider.NewClient(providers, 120*time.Second, 0, 0)
	testRLManager := ratelimit.NewManager(providers)
	testHealthChecker := health.NewChecker(3, 30*time.Second)
	testSched := scheduler.New(testRLManager, testClient, testHealthChecker, 500*time.Millisecond, 0, 0)

	oldCfg, oldResolver, oldSched := cfg, resolver, sched
	cfg, resolver, sched = testCfg, testResolver, testSched
	defer func() { cfg, resolver, sched = oldCfg, oldResolver, oldSched }()

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	reqBody := `{"model":"deepseek-group","messages":[{"role":"user","content":"你好"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse response: %v, body=%s", err, w.Body.String())
	}

	if out.Model != "deepseek-group" {
		t.Errorf("model = %q, want %q (should echo requested model)", out.Model, "deepseek-group")
	}

	if len(out.Choices) == 0 || out.Choices[0].Message.Content == "" {
		t.Error("response content should not be empty")
	}

	// 验证 deepseek tokenizer 确实被选中而非默认 tokenizer。
	// 对于中文字符串，deepseek tokenizer 和 cl100k_base 的计数应有明显差异。
	var parsedReq dto.ChatCompletionRequest
	if err := json.Unmarshal([]byte(reqBody), &parsedReq); err != nil {
		t.Fatalf("parse request body: %v", err)
	}
	dsTokens := token.CountRequestTokensFor("deepseek-chat", &parsedReq)
	defTokens := token.CountRequestTokensFor("gpt-4o", &parsedReq)
	t.Logf("tokenizer assertion: deepseek_tokens=%d, default_tokens=%d", dsTokens, defTokens)
	if dsTokens == 0 {
		t.Error("deepseek tokenizer count should be > 0 for non-empty input")
	}
	if dsTokens == defTokens {
		t.Errorf("deepseek tokenizer not active: dsTokens=%d defTokens=%d (expected different counts for Chinese text)", dsTokens, defTokens)
	}
}

// ============================================================================
// Task 2: Smart Route 集成测试
// ============================================================================

// setupSmartRouteTestHandler 创建一个启用 smart route 的测试环境
// alias -> reason group (backend), 同时配置 smart_route
func setupSmartRouteTestHandler(t *testing.T, upstreamHandler http.HandlerFunc) (*gin.Engine, *httptest.Server, func()) {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(upstreamHandler))

	falseVal := false
	trueVal := true
	providers := map[string]config.ProviderConfig{
		"upstream-provider": {
			Endpoint:  upstream.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "reason-backend",
				Exposure: exposurePtr(trueVal), // 可见，方便测试 disabled 场景
				Models:   config.ModelEntries{{Model: "upstream-provider/gpt-4", Weight: 1}},
			},
			{
				Name:     "scout-backend",
				Exposure: exposurePtr(trueVal), // 可见，方便测试 scout 场景
				Models:   config.ModelEntries{{Model: "upstream-provider/gpt-3.5", Weight: 1}},
			},
			{
				Name:     "cheap-backend",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "upstream-provider/gpt-3.5-turbo", Weight: 1}},
			},
		},
		Redirect: config.RedirectConfigs{
			{Source: "smart-alias", Target: "reason-backend"},
		},
		SmartRoute: &config.SmartRouteConfig{
			Cheap:         "cheap-backend",
			Scout:         "scout-backend",
			EnabledModels: []string{"smart-alias"},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)
	cfg = testCfg

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)
	router.POST("/v1/messages", Messages)
	router.POST("/v1/responses", Responses)

	return router, upstream, func() {
		upstream.Close()
	}
}

func setupSmartRouteStaticTestHandler(t *testing.T, upstreamBody string) (*gin.Engine, *httptest.Server, func()) {
	t.Helper()
	return setupSmartRouteTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(upstreamBody))
	})
}

// TestSmartRoute_RuleReason 验证规则命中走 reason
func TestSmartRoute_RuleReason(t *testing.T) {
	metrics.ResetForTest()

	upstreamBody := `{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	router, _, cleanup := setupSmartRouteStaticTestHandler(t, upstreamBody)
	defer cleanup()

	// 普通请求（无工具历史），应该走 reason
	reqBody := `{"model":"smart-alias","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// 验证 smart_route_decision_total{decision_path="rule_reason"} 增加
	count := testutil.ToFloat64(metrics.GetSmartRouteDecisionTotal().WithLabelValues("rule_reason"))
	if count != 1 {
		t.Errorf("expected smart_route_decision_total{decision_path=\"rule_reason\"} = 1, got %f", count)
	}
}

// TestSmartRoute_DisabledAlias 验证未启用 alias 的旁路行为
func TestSmartRoute_DisabledAlias(t *testing.T) {
	metrics.ResetForTest()

	upstreamBody := `{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	router, _, cleanup := setupSmartRouteStaticTestHandler(t, upstreamBody)
	defer cleanup()

	// 直接请求 backend group（非 smart route alias）
	reqBody := `{"model":"reason-backend","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// 验证 smart_route_decision_total{decision_path="disabled"} 增加
	count := testutil.ToFloat64(metrics.GetSmartRouteDecisionTotal().WithLabelValues("disabled"))
	if count != 1 {
		t.Errorf("expected smart_route_decision_total{decision_path=\"disabled\"} = 1, got %f", count)
	}
}

// TestSmartRoute_RuleScout 验证规则命中走 scout（有工具历史且继续搜索）
func TestSmartRoute_RuleScout(t *testing.T) {
	metrics.ResetForTest()

	upstreamBody := `{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Found data"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	router, _, cleanup := setupSmartRouteStaticTestHandler(t, upstreamBody)
	defer cleanup()

	// 请求有工具历史且最新是 tool result，且包含继续搜索信号（无执行型信号）
	// 注意：避免使用执行型信号关键词：write, implement, create, modify, fix, debug, refactor, optimize, final, summary, conclude, answer, result, output, generate, build
	reqBody := `{"model":"smart-alias","messages":[
		{"role":"user","content":"Find Go tutorials"},
		{"role":"assistant","content":"","tool_calls":[{"id":"tool-1","type":"function","function":{"name":"web_search","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"tool-1","content":"Search results:\nTitle: Go tutorials\nURL: https://go.dev\nSnippet: official tutorial index. Please continue searching for more specific topics."}
	]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// 验证 smart_route_decision_total{decision_path="rule_scout"} 增加
	count := testutil.ToFloat64(metrics.GetSmartRouteDecisionTotal().WithLabelValues("rule_scout"))
	if count != 1 {
		t.Errorf("expected smart_route_decision_total{decision_path=\"rule_scout\"} = 1, got %f", count)
	}
}

// TestSmartRoute_MessagesHandler 验证 Anthropic Messages handler 的 smart route
func TestSmartRoute_MessagesHandler(t *testing.T) {
	metrics.ResetForTest()

	upstreamBody := `{"id":"msg-test","type":"message","role":"assistant","content":[{"type":"text","text":"Hi"}],"model":"gpt-4","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`
	router, _, cleanup := setupSmartRouteStaticTestHandler(t, upstreamBody)
	defer cleanup()

	// 使用 Anthropic Messages 入方向
	reqBody := `{"model":"smart-alias","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// 验证 smart_route_decision_total{decision_path="rule_reason"} 增加
	count := testutil.ToFloat64(metrics.GetSmartRouteDecisionTotal().WithLabelValues("rule_reason"))
	if count != 1 {
		t.Errorf("expected smart_route_decision_total{decision_path=\"rule_reason\"} = 1, got %f", count)
	}
}

// TestSmartRoute_ResponsesHandler 验证 OpenAI Responses handler 的 smart route
func TestSmartRoute_ResponsesHandler(t *testing.T) {
	metrics.ResetForTest()

	// 使用 chat 格式的响应，因为 test provider 使用 openai 协议
	upstreamBody := `{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	router, _, cleanup := setupSmartRouteStaticTestHandler(t, upstreamBody)
	defer cleanup()

	// 使用 OpenAI Responses 入方向
	reqBody := `{"model":"smart-alias","input":[{"type":"message","role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// 验证 smart_route_decision_total{decision_path="rule_reason"} 增加
	count := testutil.ToFloat64(metrics.GetSmartRouteDecisionTotal().WithLabelValues("rule_reason"))
	if count != 1 {
		t.Errorf("expected smart_route_decision_total{decision_path=\"rule_reason\"} = 1, got %f", count)
	}
}

func TestSmartRoute_CheapJudgeOnlyRunsOnNeedJudge(t *testing.T) {
	metrics.ResetForTest()

	var mu sync.Mutex
	requestModels := make([]string, 0, 2)
	router, _, cleanup := setupSmartRouteTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var req dto.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}

		mu.Lock()
		requestModels = append(requestModels, req.Model)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if req.Model == "gpt-3.5-turbo" {
			_, _ = w.Write([]byte(`{"id":"chatcmpl-judge","object":"chat.completion","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"message":{"role":"assistant","content":"SCOUT"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-main","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	})
	defer cleanup()

	reqBody := `{"model":"smart-alias","messages":[
		{"role":"user","content":"Find docs"},
		{"role":"assistant","content":"","tool_calls":[{"id":"tool-1","type":"function","function":{"name":"web_search","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"tool-1","content":"Search results:\nTitle: Go docs\nURL: https://go.dev\nSnippet: package docs\nPlease search more official details. Based on this, what did you find?"}
	]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestModels) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(requestModels))
	}
	if requestModels[0] != "gpt-3.5-turbo" {
		t.Fatalf("first upstream model = %q, want cheap judge model", requestModels[0])
	}
	if requestModels[1] != "gpt-3.5" {
		t.Fatalf("second upstream model = %q, want scout model", requestModels[1])
	}

	count := testutil.ToFloat64(metrics.GetSmartRouteDecisionTotal().WithLabelValues("judge_scout"))
	if count != 1 {
		t.Errorf("expected smart_route_decision_total{decision_path=\"judge_scout\"} = 1, got %f", count)
	}
}

func TestSmartRoute_RuleScoutSkipsCheapJudge(t *testing.T) {
	metrics.ResetForTest()

	var mu sync.Mutex
	requestModels := make([]string, 0, 1)
	router, _, cleanup := setupSmartRouteTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var req dto.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}

		mu.Lock()
		requestModels = append(requestModels, req.Model)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-main","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	})
	defer cleanup()

	reqBody := `{"model":"smart-alias","messages":[
		{"role":"user","content":"Find docs"},
		{"role":"assistant","content":"","tool_calls":[{"id":"tool-1","type":"function","function":{"name":"web_search","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"tool-1","content":"Search results:\nTitle: Go docs\nURL: https://go.dev\nSnippet: package docs\nPlease search for more official details."}
	]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestModels) != 1 {
		t.Fatalf("upstream request count = %d, want 1", len(requestModels))
	}
	if requestModels[0] != "gpt-3.5" {
		t.Fatalf("upstream model = %q, want scout model", requestModels[0])
	}

	count := testutil.ToFloat64(metrics.GetSmartRouteDecisionTotal().WithLabelValues("rule_scout"))
	if count != 1 {
		t.Errorf("expected smart_route_decision_total{decision_path=\"rule_scout\"} = 1, got %f", count)
	}
}

func TestSmartRoute_CodeGatheringLoopUsesCheapJudge(t *testing.T) {
	metrics.ResetForTest()

	var mu sync.Mutex
	requestModels := make([]string, 0, 2)
	router, _, cleanup := setupSmartRouteTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var req dto.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}

		mu.Lock()
		requestModels = append(requestModels, req.Model)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if req.Model == "gpt-3.5-turbo" {
			_, _ = w.Write([]byte(`{"id":"chatcmpl-judge","object":"chat.completion","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"message":{"role":"assistant","content":"SCOUT"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-main","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	})
	defer cleanup()

	reqBody := `{"model":"smart-alias","messages":[
		{"role":"user","content":"排查 smart route 规则"},
		{"role":"assistant","content":"","tool_calls":[{"id":"tool-1","type":"function","function":{"name":"grep","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"tool-1","content":"path: internal/handler/smart_route.go\nline 247\nfunc ApplySmartRouteRules(obs *TurnObservation, aliasInfo *model.AliasSmartRouteInfo) *SmartRouteResult {\npath: internal/handler/chat.go\nline 74\n// Smart route：解码请求后、resolver.Resolve 之前"}
	]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestModels) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(requestModels))
	}
	if requestModels[0] != "gpt-3.5-turbo" {
		t.Fatalf("first upstream model = %q, want cheap judge model", requestModels[0])
	}
	if requestModels[1] != "gpt-3.5" {
		t.Fatalf("second upstream model = %q, want scout model", requestModels[1])
	}

	count := testutil.ToFloat64(metrics.GetSmartRouteDecisionTotal().WithLabelValues("judge_scout"))
	if count != 1 {
		t.Errorf("expected smart_route_decision_total{decision_path=\"judge_scout\"} = 1, got %f", count)
	}
}

// ============================================================================
// Adaptive Mode 集成测试：验证 adaptive group 在真实 handler 主路径下工作
// ============================================================================

// setupAdaptiveAliasHandler 建立一个 adaptive mode group 的测试环境：
// - alias "adaptive-model" → 内部 group "adaptive-backend"（mode: adaptive）
// - 包含两个候选 provider，首选 first 返回 firstStatus，次选 second 返回 200
// 验证 adaptive 首选失败后会顺延到第二候选，且对外 alias 行为不变。
func setupAdaptiveAliasHandler(t *testing.T, router *gin.Engine, firstStatus int, firstBody, secondBody string) (*httptest.Server, *httptest.Server, func()) {
	t.Helper()

	firstSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(firstStatus)
		w.Write([]byte(firstBody))
	}))

	secondSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(secondBody))
	}))

	falseVal := false
	providers := map[string]config.ProviderConfig{
		"first-provider": {
			Endpoint:  firstSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"second-provider": {
			Endpoint:  secondSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "adaptive-backend",
				Mode:     "adaptive",
				Exposure: exposurePtr(falseVal),
				Models: config.ModelEntries{
					{Model: "first-provider/first-model", Weight: 1},
					{Model: "second-provider/second-model", Weight: 1},
				},
			},
		},
		Redirect: config.RedirectConfigs{
			{Source: "adaptive-model", Target: "adaptive-backend"},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)
	cfg = testCfg

	return firstSrv, secondSrv, func() {
		firstSrv.Close()
		secondSrv.Close()
	}
}

// TestChat_AdaptiveMode_FirstFailsSecondWins 验证 adaptive group 在 handler 主路径下：
// 首选 (first) 失败后会自动顺延到次选 (second)，且客户端看到的 model 字段仍是请求中的别名。
func TestChat_AdaptiveMode_FirstFailsSecondWins(t *testing.T) {
	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	firstBody := `{"error":"internal server error"}`
	secondBody := `{"id":"chatcmpl-x","object":"chat.completion","created":1234567890,"model":"second-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello from second!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

	_, _, cleanup := setupAdaptiveAliasHandler(t, router, http.StatusInternalServerError, firstBody, secondBody)
	defer cleanup()

	reqBody := `{"model":"adaptive-model","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}

	// 验证对外 alias 行为不变：model 字段等于请求中的别名
	if out.Model != "adaptive-model" {
		t.Errorf("model = %q, want %q (should echo requested alias)", out.Model, "adaptive-model")
	}

	// 验证内容来自 second（次选）
	if out.Choices[0].Message.Content != "Hello from second!" {
		t.Errorf("content = %q, want %q", out.Choices[0].Message.Content, "Hello from second!")
	}
}

// TestChat_AdaptiveMode_AllSucceed 验证 adaptive group 首选直接成功时正常返回。
func TestChat_AdaptiveMode_AllSucceed(t *testing.T) {
	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	firstBody := `{"id":"chatcmpl-x","object":"chat.completion","created":1234567890,"model":"first-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello from first!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	secondBody := `{"id":"chatcmpl-y","object":"chat.completion","created":1234567891,"model":"second-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello from second!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}}`

	_, _, cleanup := setupAdaptiveAliasHandler(t, router, http.StatusOK, firstBody, secondBody)
	defer cleanup()

	reqBody := `{"model":"adaptive-model","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}

	if out.Model != "adaptive-model" {
		t.Errorf("model = %q, want %q (should echo requested alias)", out.Model, "adaptive-model")
	}
}

// ============================================================================
// Remote Bridge Provider 集成测试
// ============================================================================

// standardBridgeResponse 返回标准的 OpenAI Responses 格式响应体。
func standardBridgeResponse() string {
	return `{"id":"resp-bridge-1","object":"response","created_at":1234567890,"model":"grok-4","status":"completed","output":[{"type":"message","id":"msg-bridge-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello from bridge!"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`
}

// setupBridgeTestHandler 构建一个带 mock bridge server 的测试环境，
// 用于验证 remote bridge provider 从 /v1/responses 入口到 bridge 转发的完整链路。
// bridgeHandler 由调用方提供，模拟真实 bridge 行为。
func setupBridgeTestHandler(t *testing.T, bridgeHandler http.HandlerFunc) (*gin.Engine, *httptest.Server, func()) {
	t.Helper()

	bridgeSrv := httptest.NewServer(http.HandlerFunc(bridgeHandler))

	providers := map[string]config.ProviderConfig{
		"xai-oauth-bridge": {
			Endpoint:  bridgeSrv.URL,
			APIKey:    "", // bridge provider 的 api_key 留空，鉴权 token 通过 RemoteBridge.Token 提供
			Protocols: []string{"openai.responses"},
			RateLimit: config.RateLimitConfig{QPM: 0},
			RemoteBridge: &config.RemoteBridgeConfig{
				Enabled:  true,
				Provider: "xai-oauth",
				Token:    "test-bridge-token",
			},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "grok-model", Models: config.ModelEntries{{Model: "xai-oauth-bridge/grok-4", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)
	cfg = testCfg

	router := gin.New()
	router.POST("/v1/responses", Responses)

	return router, bridgeSrv, func() {
		bridgeSrv.Close()
	}
}

// TestBridgeResponses_Success 验证 remote bridge provider 非流式成功路径：
// mock bridge 收到正确的路径 /v1/responses、Authorization 头为 bridge token、
// X-Oh-My-API-Bridge-Provider 头为 xai-oauth、body 是原始 Responses 请求 JSON；
// 客户端看到的是标准 Responses 输出（200 且响应结构正确）。
func TestBridgeResponses_Success(t *testing.T) {
	var (
		receivedPath           string
		receivedAuth           string
		receivedBridgeProvider string
		receivedBody           []byte
	)

	router, _, cleanup := setupBridgeTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		receivedBridgeProvider = r.Header.Get("X-Oh-My-API-Bridge-Provider")
		receivedBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(standardBridgeResponse()))
	})
	defer cleanup()

	reqBody := `{"model":"grok-model","input":[{"type":"message","role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// 验证 oh-my-api 返回 200
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// 验证 mock bridge 收到的请求路径
	if receivedPath != "/v1/responses" {
		t.Errorf("bridge path = %q, want %q", receivedPath, "/v1/responses")
	}

	// 验证 bridge 鉴权头
	if receivedAuth != "Bearer test-bridge-token" {
		t.Errorf("Authorization = %q, want %q", receivedAuth, "Bearer test-bridge-token")
	}

	// 验证 provider 类型头
	if receivedBridgeProvider != "xai-oauth" {
		t.Errorf("X-Oh-My-API-Bridge-Provider = %q, want %q", receivedBridgeProvider, "xai-oauth")
	}

	// 验证请求体是有效的 Responses JSON
	var upstreamReq dto.ResponsesRequest
	if err := json.Unmarshal(receivedBody, &upstreamReq); err != nil {
		t.Errorf("bridge received invalid Responses JSON: %v, body=%s", err, string(receivedBody))
	}
	if upstreamReq.Model != "grok-4" {
		t.Errorf("upstream model = %q, want %q", upstreamReq.Model, "grok-4")
	}

	// 验证客户端看到的是标准 Responses 响应格式
	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Object != "response" {
		t.Errorf("object = %q, want %q", out.Object, "response")
	}
	if out.Status != "completed" {
		t.Errorf("status = %q, want %q", out.Status, "completed")
	}
}

// TestBridgeResponses_Bridge401 验证 bridge 返回 401 时 oh-my-api 正确透传错误状态码，
// 不会把 bridge 本地错误误包装成其它协议错误（如 502）。
func TestBridgeResponses_Bridge401(t *testing.T) {
	router, _, cleanup := setupBridgeTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid bridge token","type":"authentication_error","code":"unauthorized"}}`))
	})
	defer cleanup()

	reqBody := `{"model":"grok-model","input":[{"type":"message","role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// oh-my-api 应透传 bridge 的 401，不包装成 502
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusUnauthorized, w.Body.String())
	}
}

// TestBridgeResponses_Bridge502 验证 bridge 返回 502 时 oh-my-api 正确透传错误状态码，
// 不会把 bridge 本地错误误包装成其它协议错误。
func TestBridgeResponses_Bridge502(t *testing.T) {
	router, _, cleanup := setupBridgeTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error":{"message":"upstream unreachable","type":"server_error","code":"bad_gateway"}}`))
	})
	defer cleanup()

	reqBody := `{"model":"grok-model","input":[{"type":"message","role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// oh-my-api 应透传 bridge 返回的错误状态码
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusBadGateway, w.Body.String())
	}
}

// TestBridgeResponses_Stream 验证 stream=true 的透传场景，
// mock bridge 返回标准 Responses SSE 流，oh-my-api 不破坏事件流和 [DONE] 终止行为。
func TestBridgeResponses_Stream(t *testing.T) {
	var (
		receivedAuth           string
		receivedBridgeProvider string
	)

	router, _, cleanup := setupBridgeTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedBridgeProvider = r.Header.Get("X-Oh-My-API-Bridge-Provider")

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("bridge server should support Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// 模拟标准 Responses SSE 流事件序列
		w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-stream-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"grok-4\",\"output\":[]}}\n\n"))
		flusher.Flush()

		w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]},\"output_index\":0}\n\n"))
		flusher.Flush()

		w.Write([]byte("data: {\"type\":\"response.content_part.added\",\"item_id\":\"msg-1\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n"))
		flusher.Flush()

		w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg-1\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hello from bridge stream!\"}\n\n"))
		flusher.Flush()

		w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-stream-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"grok-4\",\"output\":[]}}\n\n"))
		flusher.Flush()

		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	defer cleanup()

	reqBody := `{"model":"grok-model","input":[{"type":"message","role":"user","content":"Hello"}],"stream":true}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// 验证 bridge 鉴权头
	if receivedAuth != "Bearer test-bridge-token" {
		t.Errorf("Authorization = %q, want %q", receivedAuth, "Bearer test-bridge-token")
	}
	if receivedBridgeProvider != "xai-oauth" {
		t.Errorf("X-Oh-My-API-Bridge-Provider = %q, want %q", receivedBridgeProvider, "xai-oauth")
	}

	// 验证 SSE 内容类型
	contentType := w.Header().Get("Content-Type")
	if contentType != "text/event-stream" {
		t.Errorf("Content-Type = %q, want %q", contentType, "text/event-stream")
	}

	body := w.Body.String()
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("stream missing [DONE] marker, body: %s", body)
	}
	if !strings.Contains(body, "response.output_text.delta") {
		t.Errorf("stream missing output text delta, body: %s", body)
	}
	if !strings.Contains(body, "Hello from bridge stream!") {
		t.Errorf("stream missing expected text content, body: %s", body)
	}
}

// setupBridgeChatTestHandler 构建一个带 mock bridge server 的测试环境，
// 用于验证 remote bridge provider 从 /v1/chat/completions 入口到 bridge 转发的完整链路。
func setupBridgeChatTestHandler(t *testing.T, bridgeHandler http.HandlerFunc) (*gin.Engine, *httptest.Server, func()) {
	t.Helper()

	bridgeSrv := httptest.NewServer(http.HandlerFunc(bridgeHandler))

	providers := map[string]config.ProviderConfig{
		"xai-oauth-bridge": {
			Endpoint:  bridgeSrv.URL,
			APIKey:    "",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
			RemoteBridge: &config.RemoteBridgeConfig{
				Enabled:  true,
				Provider: "xai-oauth",
				Token:    "test-bridge-token",
			},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "grok-model", Models: config.ModelEntries{{Model: "xai-oauth-bridge/grok-4", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)
	cfg = testCfg

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	return router, bridgeSrv, func() {
		bridgeSrv.Close()
	}
}

// TestBridgeChatCompletions_Success 验证 remote bridge provider Chat 非流式成功路径：
// mock bridge 收到 /v1/chat/completions、bridge 鉴权头与 provider 头、OpenAI Chat 请求体；
// 客户端看到标准 Chat 响应。
func TestBridgeChatCompletions_Success(t *testing.T) {
	var (
		receivedPath           string
		receivedAuth           string
		receivedBridgeProvider string
		receivedBody           []byte
	)

	router, _, cleanup := setupBridgeChatTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		receivedBridgeProvider = r.Header.Get("X-Oh-My-API-Bridge-Provider")
		receivedBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-bridge-1","object":"chat.completion","created":1234567890,"model":"grok-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello from bridge!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	})
	defer cleanup()

	reqBody := `{"model":"grok-model","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if receivedPath != "/v1/chat/completions" {
		t.Errorf("bridge path = %q, want %q", receivedPath, "/v1/chat/completions")
	}
	if receivedAuth != "Bearer test-bridge-token" {
		t.Errorf("Authorization = %q, want %q", receivedAuth, "Bearer test-bridge-token")
	}
	if receivedBridgeProvider != "xai-oauth" {
		t.Errorf("X-Oh-My-API-Bridge-Provider = %q, want %q", receivedBridgeProvider, "xai-oauth")
	}

	var upstreamReq dto.ChatCompletionRequest
	if err := json.Unmarshal(receivedBody, &upstreamReq); err != nil {
		t.Errorf("bridge received invalid Chat JSON: %v, body=%s", err, string(receivedBody))
	}
	if upstreamReq.Model != "grok-4" {
		t.Errorf("upstream model = %q, want %q", upstreamReq.Model, "grok-4")
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Object != "chat.completion" {
		t.Errorf("object = %q, want %q", out.Object, "chat.completion")
	}
}
