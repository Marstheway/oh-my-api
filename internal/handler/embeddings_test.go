package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/gin-gonic/gin"
)

func setupEmbeddingsTestHandler(qpm int) (*gin.Engine, *httptest.Server, string) {
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string      `json:"model"`
			Input interface{} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid request"}`))
			return
		}

		// Determine input count
		var inputCount int
		switch v := req.Input.(type) {
		case string:
			inputCount = 1
		case []interface{}:
			inputCount = len(v)
		default:
			inputCount = 1
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := struct {
			Embeddings      [][]float64 `json:"embeddings"`
			PromptEvalCount int         `json:"prompt_eval_count"`
		}{
			Embeddings:      make([][]float64, inputCount),
			PromptEvalCount: 10,
		}
		for i := 0; i < inputCount; i++ {
			resp.Embeddings[i] = make([]float64, 384)
		}
		w.Write(mustMarshal(resp))
	}))

	tmpDir := filepath.Join(os.TempDir(), "oh-my-api-test-"+time.Now().Format("20060102150405"))
	os.MkdirAll(tmpDir, 0755)
	dbPath := filepath.Join(tmpDir, "test.db")

	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Endpoint: ollamaSrv.URL,
			APIKey:   "test-key",
			Endpoints: []config.EndpointConfig{
				{
					URL:       ollamaSrv.URL,
					Protocols: []string{"ollama.embed"},
				},
			},
			RateLimit: config.RateLimitConfig{QPM: qpm},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "embedding-model", Models: config.ModelEntries{{Model: "ollama-provider/embedding-model", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		panic(err)
	}

	if err := stats.Init(dbPath); err != nil {
		panic(err)
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/embeddings", Embeddings)

	return router, ollamaSrv, tmpDir
}

// Helper for compact JSON marshaling
func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestEmbeddings_Success(t *testing.T) {
	router, ollamaSrv, tmpDir := setupEmbeddingsTestHandler(0)
	defer ollamaSrv.Close()
	defer os.RemoveAll(tmpDir)

	body := dto.EmbeddingRequest{
		Model: "embedding-model",
		Input: "Hello, world!",
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp dto.EmbeddingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.Object != "list" {
		t.Errorf("object = %s, want list", resp.Object)
	}
	if resp.Model != "embedding-model" {
		t.Errorf("model = %s, want embedding-model", resp.Model)
	}
	if len(resp.Data) != 1 {
		t.Errorf("data count = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].Index != 0 {
		t.Errorf("index = %d, want 0", resp.Data[0].Index)
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("prompt_tokens = %d, want 10", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 0 {
		t.Errorf("completion_tokens = %d, want 0", resp.Usage.CompletionTokens)
	}
}

func TestEmbeddings_InvalidEncodingFormat(t *testing.T) {
	router, ollamaSrv, tmpDir := setupEmbeddingsTestHandler(0)
	defer ollamaSrv.Close()
	defer os.RemoveAll(tmpDir)

	body := dto.EmbeddingRequest{
		Model:          "embedding-model",
		Input:          "Hello, world!",
		EncodingFormat: "invalid",
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestEmbeddings_InvalidInput(t *testing.T) {
	router, ollamaSrv, tmpDir := setupEmbeddingsTestHandler(0)
	defer ollamaSrv.Close()
	defer os.RemoveAll(tmpDir)

	body := dto.EmbeddingRequest{
		Model: "embedding-model",
		Input: 123, // Invalid type
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestEmbeddings_EmptyInput(t *testing.T) {
	router, ollamaSrv, tmpDir := setupEmbeddingsTestHandler(0)
	defer ollamaSrv.Close()
	defer os.RemoveAll(tmpDir)

	body := dto.EmbeddingRequest{
		Model: "embedding-model",
		Input: []string{}, // Empty array
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestEmbeddings_ProviderDoesNotSupportEmbedding(t *testing.T) {
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer openaiSrv.Close()

	tmpDir := filepath.Join(os.TempDir(), "oh-my-api-test-"+time.Now().Format("20060102150405"))
	os.MkdirAll(tmpDir, 0755)
	dbPath := filepath.Join(tmpDir, "test.db")

	providers := map[string]config.ProviderConfig{
		"openai-provider": {
			Endpoint:  openaiSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai-provider/gpt-4", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		panic(err)
	}

	if err := stats.Init(dbPath); err != nil {
		panic(err)
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/embeddings", Embeddings)

	body := dto.EmbeddingRequest{
		Model: "gpt-4",
		Input: "Hello, world!",
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}

	defer os.RemoveAll(tmpDir)
}

func TestEmbeddings_InvalidEmbeddingEndpointConfiguration(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "oh-my-api-test-"+time.Now().Format("20060102150405"))
	os.MkdirAll(tmpDir, 0755)
	dbPath := filepath.Join(tmpDir, "test.db")

	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Protocols: []string{"ollama.embed"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "embedding-model", Models: config.ModelEntries{{Model: "ollama-provider/embedding-model", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		panic(err)
	}

	if err := stats.Init(dbPath); err != nil {
		panic(err)
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/embeddings", Embeddings)

	body := dto.EmbeddingRequest{
		Model: "embedding-model",
		Input: "Hello, world!",
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}

	defer os.RemoveAll(tmpDir)
}

func TestEmbeddings_UpstreamErrorResponse(t *testing.T) {
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"upstream error message"}`))
	}))
	defer ollamaSrv.Close()

	tmpDir := filepath.Join(os.TempDir(), "oh-my-api-test-"+time.Now().Format("20060102150405"))
	os.MkdirAll(tmpDir, 0755)
	dbPath := filepath.Join(tmpDir, "test.db")

	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Endpoint: ollamaSrv.URL,
			APIKey:   "test-key",
			Endpoints: []config.EndpointConfig{
				{
					URL:       ollamaSrv.URL,
					Protocols: []string{"ollama.embed"},
				},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "embedding-model", Models: config.ModelEntries{{Model: "ollama-provider/embedding-model", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		panic(err)
	}

	if err := stats.Init(dbPath); err != nil {
		panic(err)
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/embeddings", Embeddings)

	body := dto.EmbeddingRequest{
		Model: "embedding-model",
		Input: "Hello, world!",
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}

	defer os.RemoveAll(tmpDir)
}

func TestEmbeddings_MismatchedEmbeddingCount(t *testing.T) {
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Return wrong number of embeddings
		resp := struct {
			Embeddings      [][]float64 `json:"embeddings"`
			PromptEvalCount int         `json:"prompt_eval_count"`
		}{
			Embeddings:      [][]float64{{1.0}, {2.0}}, // 2 embeddings
			PromptEvalCount: 10,
		}
		w.Write(mustMarshal(resp))
	}))
	defer ollamaSrv.Close()

	tmpDir := filepath.Join(os.TempDir(), "oh-my-api-test-"+time.Now().Format("20060102150405"))
	os.MkdirAll(tmpDir, 0755)
	dbPath := filepath.Join(tmpDir, "test.db")

	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Endpoint: ollamaSrv.URL,
			APIKey:   "test-key",
			Endpoints: []config.EndpointConfig{
				{
					URL:       ollamaSrv.URL,
					Protocols: []string{"ollama.embed"},
				},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "embedding-model", Models: config.ModelEntries{{Model: "ollama-provider/embedding-model", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		panic(err)
	}

	if err := stats.Init(dbPath); err != nil {
		panic(err)
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/embeddings", Embeddings)

	body := dto.EmbeddingRequest{
		Model: "embedding-model",
		Input: "Hello, world!", // 1 input but 2 embeddings
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}

	defer os.RemoveAll(tmpDir)
}

func TestEmbeddings_NullEmbeddingItem(t *testing.T) {
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"embeddings":[null],"prompt_eval_count":10}`))
	}))
	defer ollamaSrv.Close()

	tmpDir := filepath.Join(os.TempDir(), "oh-my-api-test-"+time.Now().Format("20060102150405"))
	os.MkdirAll(tmpDir, 0755)
	dbPath := filepath.Join(tmpDir, "test.db")

	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Endpoint: ollamaSrv.URL,
			APIKey:   "test-key",
			Endpoints: []config.EndpointConfig{
				{
					URL:       ollamaSrv.URL,
					Protocols: []string{"ollama.embed"},
				},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "embedding-model", Models: config.ModelEntries{{Model: "ollama-provider/embedding-model", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		panic(err)
	}

	if err := stats.Init(dbPath); err != nil {
		panic(err)
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/embeddings", Embeddings)

	body := dto.EmbeddingRequest{
		Model: "embedding-model",
		Input: "Hello, world!",
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}

	defer os.RemoveAll(tmpDir)
}

func TestEmbeddings_UsesRequestedAliasModel(t *testing.T) {
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := struct {
			Embeddings      [][]float64 `json:"embeddings"`
			PromptEvalCount int         `json:"prompt_eval_count"`
		}{
			Embeddings:      [][]float64{make([]float64, 4)},
			PromptEvalCount: 5,
		}
		w.Write(mustMarshal(resp))
	}))
	defer ollamaSrv.Close()

	tmpDir := filepath.Join(os.TempDir(), "oh-my-api-test-"+time.Now().Format("20060102150405.000"))
	os.MkdirAll(tmpDir, 0755)
	dbPath := filepath.Join(tmpDir, "test.db")
	defer os.RemoveAll(tmpDir)

	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Endpoint: ollamaSrv.URL,
			APIKey:   "test-key",
			Endpoints: []config.EndpointConfig{
				{
					URL:       ollamaSrv.URL,
					Protocols: []string{"ollama.embed"},
				},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "embedding-model", Models: config.ModelEntries{{Model: "ollama-provider/embedding-model", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{
			{Source: "embed-alias", Target: "embedding-model"},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("failed to create resolver: %v", err)
	}

	if err := stats.Init(dbPath); err != nil {
		t.Fatalf("failed to init stats: %v", err)
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/embeddings", Embeddings)

	body := dto.EmbeddingRequest{
		Model: "embed-alias",
		Input: "hello",
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp dto.EmbeddingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.Model != "embed-alias" {
		t.Errorf("model = %q, want %q (response should echo the requested alias, not the upstream model)", resp.Model, "embed-alias")
	}
}

func TestEmbeddings_MultipleInputs(t *testing.T) {
	router, ollamaSrv, tmpDir := setupEmbeddingsTestHandler(0)
	defer ollamaSrv.Close()
	defer os.RemoveAll(tmpDir)

	body := dto.EmbeddingRequest{
		Model: "embedding-model",
		Input: []string{"Hello", "World"},
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp dto.EmbeddingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(resp.Data) != 2 {
		t.Errorf("data count = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].Index != 0 {
		t.Errorf("first index = %d, want 0", resp.Data[0].Index)
	}
	if resp.Data[1].Index != 1 {
		t.Errorf("second index = %d, want 1", resp.Data[1].Index)
	}
}

// TestEmbeddings_RulesQPMApplies 验证 embeddings 走同一套调度层规则：
// 命中 upstream-model 的 qpm rule 后，Task 带上 model qpm；model 桶被第一次请求耗尽后，
// 第二次请求在 Wait 期限内拿不到令牌 → 429 all providers rate limited。
func TestEmbeddings_RulesQPMApplies(t *testing.T) {
	router, ollamaSrv, tmpDir := setupEmbeddingsTestHandler(0)
	defer ollamaSrv.Close()
	defer os.RemoveAll(tmpDir)

	qpm := 1
	cfg.Rules = []config.RuleConfig{{
		Match: config.RuleMatch{
			UpstreamModel: &config.RuleCondition{Op: "equals", Value: "ollama-provider/embedding-model"},
		},
		Action: config.RuleAction{QPM: &qpm},
	}}
	defer func() { cfg.Rules = nil }()

	// 缩短请求超时：model 桶 qpm=1 的 refill 周期为 60s，Wait 必然超时后走 429。
	oldTimeout := timeout
	timeout = 2 * time.Second
	defer func() { timeout = oldTimeout }()

	body := dto.EmbeddingRequest{Model: "embedding-model", Input: "hello"}
	jsonBody, _ := json.Marshal(body)

	// 第一次：model 桶建立并消费唯一 token，成功。
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first embedding status = %d, want 200, body: %s", w.Code, w.Body.String())
	}

	// 第二次：model 桶耗尽，Wait 超时 → 429。
	w = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second embedding status = %d, want 429 (model qpm bucket exhausted)", w.Code)
	}
}

// TestEmbeddings_RulesDisableTimeRangeSkips 验证 embeddings 命中 disable_time_range rule 时
// 叶子被跳过 → 全灭返回 503 no provider available。
func TestEmbeddings_RulesDisableTimeRangeSkips(t *testing.T) {
	router, ollamaSrv, tmpDir := setupEmbeddingsTestHandler(0)
	defer ollamaSrv.Close()
	defer os.RemoveAll(tmpDir)

	cfg.Rules = []config.RuleConfig{{
		Match: config.RuleMatch{
			UpstreamModel: &config.RuleCondition{Op: "equals", Value: "ollama-provider/embedding-model"},
		},
		Action: config.RuleAction{DisableTimeRange: []string{"00:00-24:00"}},
	}}
	defer func() { cfg.Rules = nil }()

	body := dto.EmbeddingRequest{Model: "embedding-model", Input: "hello"}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("embedding with all-disabled leaf status = %d, want 503, body: %s", w.Code, w.Body.String())
	}
}

// TestEmbeddings_RulesEnableTimeRangeSkipsOutsideWindow 验证 embeddings 命中 enable_time_range
// 但当前时间不在窗口内时同样 503。
func TestEmbeddings_RulesEnableTimeRangeSkipsOutsideWindow(t *testing.T) {
	router, ollamaSrv, tmpDir := setupEmbeddingsTestHandler(0)
	defer ollamaSrv.Close()
	defer os.RemoveAll(tmpDir)

	now := time.Now()
	// 未来窗口 [now+1h, now+2h)：请求时当前时间必然不在窗口内。
	enable := now.Add(1*time.Hour).Format("15:04") + "-" + now.Add(2*time.Hour).Format("15:04")
	cfg.Rules = []config.RuleConfig{{
		Match: config.RuleMatch{
			UpstreamModel: &config.RuleCondition{Op: "equals", Value: "ollama-provider/embedding-model"},
		},
		Action: config.RuleAction{EnableTimeRange: []string{enable}},
	}}
	defer func() { cfg.Rules = nil }()

	body := dto.EmbeddingRequest{Model: "embedding-model", Input: "hello"}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("embedding outside enable window status = %d, want 503, body: %s", w.Code, w.Body.String())
	}
}

// TestEmbeddings_RulesThinkingIgnored 验证 protocol/effort/thinking 不改变 embedding 请求：
// 命中 thinking rule 的 embedding 请求照常成功（embeddings 只消费调度层 action）。
func TestEmbeddings_RulesThinkingIgnored(t *testing.T) {
	router, ollamaSrv, tmpDir := setupEmbeddingsTestHandler(0)
	defer ollamaSrv.Close()
	defer os.RemoveAll(tmpDir)

	cfg.Rules = []config.RuleConfig{{
		Match: config.RuleMatch{
			UpstreamModel: &config.RuleCondition{Op: "equals", Value: "ollama-provider/embedding-model"},
		},
		Action: config.RuleAction{Thinking: "off"},
	}}
	defer func() { cfg.Rules = nil }()

	body := dto.EmbeddingRequest{Model: "embedding-model", Input: "hello"}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("embedding with thinking rule status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
}

// TestEmbeddings_RequestedAliasSurvivesWinnerChange 验证：当 embedding 请求通过别名路由时，
// 即使 failover 切换到不同 provider/upstream_model，响应中的 model 字段仍为别名。
func TestEmbeddings_RequestedAliasSurvivesWinnerChange(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"fail"}`))
	}))
	defer failSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := struct {
			Embeddings      [][]float64 `json:"embeddings"`
			PromptEvalCount int         `json:"prompt_eval_count"`
		}{
			Embeddings:      [][]float64{make([]float64, 4)},
			PromptEvalCount: 5,
		}
		w.Write(mustMarshal(resp))
	}))
	defer successSrv.Close()

	tmpDir := filepath.Join(os.TempDir(), "oh-my-api-test-embed-failover-"+time.Now().Format("20060102150405.000"))
	os.MkdirAll(tmpDir, 0755)
	dbPath := filepath.Join(tmpDir, "test.db")
	defer os.RemoveAll(tmpDir)

	falseVal := false
	providers := map[string]config.ProviderConfig{
		"primary-ollama": {
			Endpoint: failSrv.URL,
			APIKey:   "k",
			Endpoints: []config.EndpointConfig{
				{URL: failSrv.URL, Protocols: []string{"ollama.embed"}},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"secondary-ollama": {
			Endpoint: successSrv.URL,
			APIKey:   "k",
			Endpoints: []config.EndpointConfig{
				{URL: successSrv.URL, Protocols: []string{"ollama.embed"}},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "embed-backend",
				Mode:     "failover",
				Exposure: exposurePtr(falseVal),
				Models: config.ModelEntries{
					{Model: "primary-ollama/nomic-embed-primary", Weight: 1},
					{Model: "secondary-ollama/nomic-embed-secondary", Weight: 1},
				},
			},
		},
		Redirect: config.RedirectConfigs{
			{Source: "embed-alias-failover", Target: "embed-backend"},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("failed to create resolver: %v", err)
	}

	if err := stats.Init(dbPath); err != nil {
		t.Fatalf("failed to init stats: %v", err)
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/embeddings", Embeddings)

	body := dto.EmbeddingRequest{
		Model: "embed-alias-failover",
		Input: "hello world",
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var respOut dto.EmbeddingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &respOut); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if respOut.Model != "embed-alias-failover" {
		t.Errorf("model = %q, want %q (winner change must not leak upstream model)", respOut.Model, "embed-alias-failover")
	}
}

// ============================================================================
// Task 3: Embeddings 严格拒绝测试
// ============================================================================

// setupEmbeddingOllamaSrv 建立一个简单的 ollama embed server，始终返回一个 embedding。
func setupEmbeddingOllamaSrv(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := struct {
			Embeddings      [][]float64 `json:"embeddings"`
			PromptEvalCount int         `json:"prompt_eval_count"`
		}{
			Embeddings:      [][]float64{make([]float64, 4)},
			PromptEvalCount: 3,
		}
		w.Write(mustMarshal(resp))
	}))
}

// ============================================================================
// openai.embeddings 协议集成测试
// ============================================================================

// setupOpenAIEmbeddingHandler 建立 OpenAI 格式的 /v1/embeddings 上游，返回 router 与记录请求路径的 server。
func setupOpenAIEmbeddingHandler(t *testing.T, endpoint string, endpointPath *string) *gin.Engine {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if endpointPath != nil {
			*endpointPath = r.URL.Path
		}
		var req struct {
			Model string          `json:"model"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"invalid request"}}`))
			return
		}
		var inputCount int
		if len(req.Input) > 0 && req.Input[0] == '"' {
			inputCount = 1
		} else {
			var arr []json.RawMessage
			_ = json.Unmarshal(req.Input, &arr)
			inputCount = len(arr)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		data := make([]map[string]any, inputCount)
		for i := 0; i < inputCount; i++ {
			data[i] = map[string]any{
				"object":    "embedding",
				"index":     i,
				"embedding": make([]float64, 4),
			}
		}
		resp := map[string]any{
			"object": "list",
			"data":   data,
			"model":  req.Model,
			"usage": map[string]int{
				"prompt_tokens":     7,
				"completion_tokens": 0,
				"total_tokens":      7,
			},
		}
		w.Write(mustMarshal(resp))
	}))
	t.Cleanup(srv.Close)

	providerName := "openai-embed-provider"
	providers := map[string]config.ProviderConfig{
		providerName: {
			// endpoint 参数（"" / "/v1" / "/v1/embeddings"）作为路径后缀拼到 server URL 上
			Endpoint:  srv.URL + endpoint,
			APIKey:    "test-key",
			Protocols: []string{"openai.embeddings"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "embedding-model", Models: config.ModelEntries{{Model: providerName + "/embedding-model", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	if err := stats.Init(dbPath); err != nil {
		t.Fatalf("stats.Init: %v", err)
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/embeddings", Embeddings)
	return router
}

func postEmbeddingRequest(router *gin.Engine, body dto.EmbeddingRequest) *httptest.ResponseRecorder {
	jsonBody, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

func TestEmbeddings_OpenAIProtocol_Success(t *testing.T) {
	var upstreamPath string
	router := setupOpenAIEmbeddingHandler(t, "", &upstreamPath)

	w := postEmbeddingRequest(router, dto.EmbeddingRequest{
		Model: "embedding-model",
		Input: "Hello, world!",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if upstreamPath != "/v1/embeddings" {
		t.Errorf("upstream path = %q, want /v1/embeddings", upstreamPath)
	}

	var resp dto.EmbeddingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp.Object != "list" {
		t.Errorf("object = %s, want list", resp.Object)
	}
	if resp.Model != "embedding-model" {
		t.Errorf("model = %s, want embedding-model", resp.Model)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data count = %d, want 1", len(resp.Data))
	}
	if resp.Usage.PromptTokens != 7 || resp.Usage.TotalTokens != 7 {
		t.Errorf("usage = %+v, want prompt/total 7", resp.Usage)
	}
}

func TestEmbeddings_OpenAIProtocol_EndpointWithV1Base(t *testing.T) {
	// endpoint 以 /v1 结尾（与 chat 共用 base）时，应只补 /embeddings，不产生 /v1/v1/embeddings
	var upstreamPath string
	router := setupOpenAIEmbeddingHandler(t, "/v1", &upstreamPath)

	w := postEmbeddingRequest(router, dto.EmbeddingRequest{
		Model: "embedding-model",
		Input: []any{"a", "b"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if upstreamPath != "/v1/embeddings" {
		t.Errorf("upstream path = %q, want /v1/embeddings (not /v1/v1/embeddings)", upstreamPath)
	}
	var resp dto.EmbeddingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Errorf("data count = %d, want 2", len(resp.Data))
	}
}

func TestEmbeddings_OpenAIProtocol_EndpointFullPath(t *testing.T) {
	// endpoint 已是完整 /v1/embeddings，原样使用
	var upstreamPath string
	router := setupOpenAIEmbeddingHandler(t, "/v1/embeddings", &upstreamPath)

	w := postEmbeddingRequest(router, dto.EmbeddingRequest{
		Model: "embedding-model",
		Input: "hi",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if upstreamPath != "/v1/embeddings" {
		t.Errorf("upstream path = %q, want /v1/embeddings", upstreamPath)
	}
}

// TestEmbeddings_NestedChildGroupRejected 验证：embedding group 引用了子 group → 应返回错误。
func TestEmbeddings_NestedChildGroupRejected(t *testing.T) {
	ollamaSrv := setupEmbeddingOllamaSrv(t)
	defer ollamaSrv.Close()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	falseVal := false
	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Endpoint: ollamaSrv.URL,
			APIKey:   "k",
			Endpoints: []config.EndpointConfig{
				{URL: ollamaSrv.URL, Protocols: []string{"ollama.embed"}},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "embed-leaf",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "ollama-provider/nomic-embed", Weight: 1}},
			},
			{
				// 顶层 group 引用子 group，不是纯叶子
				Name: "embed-nested",
				Mode: "failover",
				Models: config.ModelEntries{
					{Model: "embed-leaf", Weight: 1},
				},
			},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	if err := stats.Init(dbPath); err != nil {
		t.Fatalf("failed to init stats: %v", err)
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/embeddings", Embeddings)

	body := dto.EmbeddingRequest{
		Model: "embed-nested",
		Input: "hello",
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// nested group 不支持，应返回 422 UnprocessableEntity
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 UnprocessableEntity (nested group embedding should be rejected)", w.Code)
	}
}

// TestEmbeddings_AliasToLeafGroupSucceeds 验证：alias 指向纯叶子 group → 应成功（不因为是 alias 就被拒绝）。
func TestEmbeddings_AliasToLeafGroupSucceeds(t *testing.T) {
	ollamaSrv := setupEmbeddingOllamaSrv(t)
	defer ollamaSrv.Close()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	falseVal := false
	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Endpoint: ollamaSrv.URL,
			APIKey:   "k",
			Endpoints: []config.EndpointConfig{
				{URL: ollamaSrv.URL, Protocols: []string{"ollama.embed"}},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "leaf-embed",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "ollama-provider/nomic-embed", Weight: 1}},
			},
		},
		Redirect: config.RedirectConfigs{
			{Source: "embed-alias-leaf", Target: "leaf-embed"},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver error: %v", err)
	}
	if err := stats.Init(dbPath); err != nil {
		t.Fatalf("failed to init stats: %v", err)
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/embeddings", Embeddings)

	body := dto.EmbeddingRequest{
		Model: "embed-alias-leaf",
		Input: "hello",
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// alias 指向纯叶子 group 应成功
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s (alias to leaf group must succeed)", w.Code, http.StatusOK, w.Body.String())
	}

	var respOut dto.EmbeddingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &respOut); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	// model 应回显别名
	if respOut.Model != "embed-alias-leaf" {
		t.Errorf("model = %q, want %q", respOut.Model, "embed-alias-leaf")
	}
}

func TestEmbeddings_InternalModelReturns404(t *testing.T) {
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ollamaSrv.Close()

	tmpDir := filepath.Join(os.TempDir(), "oh-my-api-test-emb-"+time.Now().Format("20060102150405"))
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)
	dbPath := filepath.Join(tmpDir, "test.db")

	internalExposure := config.ExposureInternal

	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Endpoint: ollamaSrv.URL,
			APIKey:   "test-key",
			Endpoints: []config.EndpointConfig{
				{
					URL:       ollamaSrv.URL,
					Protocols: []string{"ollama.embed"},
				},
			},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "internal-emb", Exposure: &internalExposure, Models: config.ModelEntries{{Model: "ollama-provider/internal-emb", Weight: 1}}},
		},
	}

	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := stats.Init(dbPath); err != nil {
		t.Fatalf("stats init: %v", err)
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.POST("/v1/embeddings", Embeddings)

	body := dto.EmbeddingRequest{
		Model: "internal-emb",
		Input: "hello",
	}
	jsonBody, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for internal model via embeddings, got %d: %s", w.Code, w.Body.String())
	}
}
