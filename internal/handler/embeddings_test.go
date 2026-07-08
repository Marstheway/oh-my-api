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

	client := provider.NewClient(providers, 120*time.Second, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0)

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

	client := provider.NewClient(providers, 120*time.Second, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0)

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

	client := provider.NewClient(providers, 120*time.Second, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0)

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

	client := provider.NewClient(providers, 120*time.Second, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0)

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

	client := provider.NewClient(providers, 120*time.Second, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0)

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

	client := provider.NewClient(providers, 120*time.Second, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0)

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

	client := provider.NewClient(providers, 120*time.Second, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0)

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
				Name:    "embed-backend",
				Mode:    "failover",
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

	client := provider.NewClient(providers, 120*time.Second, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0)

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
				Name:    "embed-leaf",
				Exposure: exposurePtr(falseVal),
				Models:  config.ModelEntries{{Model: "ollama-provider/nomic-embed", Weight: 1}},
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

	client := provider.NewClient(providers, 120*time.Second, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0)

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
				Name:    "leaf-embed",
				Exposure: exposurePtr(falseVal),
				Models:  config.ModelEntries{{Model: "ollama-provider/nomic-embed", Weight: 1}},
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

	client := provider.NewClient(providers, 120*time.Second, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0)

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

	client := provider.NewClient(providers, 120*time.Second, 0)
	rlManager := ratelimit.NewManager(providers)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched = scheduler.New(rlManager, client, healthChecker, 500*time.Millisecond, 0)

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
