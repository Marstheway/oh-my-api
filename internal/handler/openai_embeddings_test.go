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

func TestEmbeddings_OpenAIEmbeddings_Success(t *testing.T) {
	openaiEmbedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var req struct {
			Model string      `json:"model"`
			Input interface{} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"invalid request"}}`))
			return
		}

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

		data := make([]map[string]interface{}, inputCount)
		for i := 0; i < inputCount; i++ {
			data[i] = map[string]interface{}{
				"object":    "embedding",
				"index":     i,
				"embedding": make([]float64, 1536),
			}
		}

		resp := map[string]interface{}{
			"object": "list",
			"data":   data,
			"model":  req.Model,
			"usage": map[string]int{
				"prompt_tokens": 10,
				"total_tokens":  10,
			},
		}
		w.Write(mustMarshal(resp))
	}))
	defer openaiEmbedSrv.Close()

	tmpDir := filepath.Join(os.TempDir(), "oh-my-api-test-openai-embed-"+time.Now().Format("20060102150405"))
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)
	dbPath := filepath.Join(tmpDir, "test.db")

	providers := map[string]config.ProviderConfig{
		"openai-embed": {
			Endpoint: openaiEmbedSrv.URL,
			APIKey:   "test-key",
			Endpoints: []config.EndpointConfig{
				{
					URL:       openaiEmbedSrv.URL,
					Protocols: []string{"openai.embeddings"},
				},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "embedding-model", Models: config.ModelEntries{{Model: "openai-embed/text-embedding-3-small", Weight: 1}}},
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
	if resp.Usage.TotalTokens != 10 {
		t.Errorf("total_tokens = %d, want 10", resp.Usage.TotalTokens)
	}
}

func TestEmbeddings_MixedProtocols_Success(t *testing.T) {
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := struct {
			Embeddings      [][]float64 `json:"embeddings"`
			PromptEvalCount int         `json:"prompt_eval_count"`
		}{
			Embeddings:      [][]float64{make([]float64, 384)},
			PromptEvalCount: 5,
		}
		w.Write(mustMarshal(resp))
	}))
	defer ollamaSrv.Close()

	openaiEmbedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		data := []map[string]interface{}{
			{
				"object":    "embedding",
				"index":     0,
				"embedding": make([]float64, 1536),
			},
		}
		resp := map[string]interface{}{
			"object": "list",
			"data":   data,
			"model":  "text-embedding-3-small",
			"usage": map[string]int{
				"prompt_tokens": 15,
				"total_tokens":  15,
			},
		}
		w.Write(mustMarshal(resp))
	}))
	defer openaiEmbedSrv.Close()

	tmpDir := filepath.Join(os.TempDir(), "oh-my-api-test-mixed-embed-"+time.Now().Format("20060102150405"))
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)
	dbPath := filepath.Join(tmpDir, "test.db")

	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Endpoint: ollamaSrv.URL,
			APIKey:   "",
			Endpoints: []config.EndpointConfig{
				{
					URL:       ollamaSrv.URL,
					Protocols: []string{"ollama.embed"},
				},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"openai-provider": {
			Endpoint: openaiEmbedSrv.URL,
			APIKey:   "test-key",
			Endpoints: []config.EndpointConfig{
				{
					URL:       openaiEmbedSrv.URL,
					Protocols: []string{"openai.embeddings"},
				},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	falseVal := false
	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "mixed-embed-group",
				Mode:     "failover",
				Exposure: exposurePtr(falseVal),
				Models: config.ModelEntries{
					{Model: "ollama-provider/nomic-embed-text", Weight: 1},
					{Model: "openai-provider/text-embedding-3-small", Weight: 1},
				},
			},
		},
		Redirect: config.RedirectConfigs{
			{Source: "mixed-embed", Target: "mixed-embed-group"},
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
		Model: "mixed-embed",
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
	if resp.Model != "mixed-embed" {
		t.Errorf("model = %s, want mixed-embed", resp.Model)
	}
	if len(resp.Data) != 1 {
		t.Errorf("data count = %d, want 1", len(resp.Data))
	}
	// 验证走了 ollama 解码路径：ollama mock 返回 prompt_eval_count: 5，openai mock 返回 prompt_tokens: 15
	if resp.Usage.PromptTokens != 5 {
		t.Errorf("prompt_tokens = %d, want 5 (indicates ollama decoder path)", resp.Usage.PromptTokens)
	}
	if resp.Usage.TotalTokens != 5 {
		t.Errorf("total_tokens = %d, want 5 (indicates ollama decoder path)", resp.Usage.TotalTokens)
	}
}

func TestEmbeddings_OpenAIEmbeddings_UpstreamError(t *testing.T) {
	openaiEmbedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"invalid api_key"}}`))
	}))
	defer openaiEmbedSrv.Close()

	tmpDir := filepath.Join(os.TempDir(), "oh-my-api-test-openai-embed-err-"+time.Now().Format("20060102150405"))
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)
	dbPath := filepath.Join(tmpDir, "test.db")

	providers := map[string]config.ProviderConfig{
		"openai-embed": {
			Endpoint: openaiEmbedSrv.URL,
			APIKey:   "invalid-key",
			Endpoints: []config.EndpointConfig{
				{
					URL:       openaiEmbedSrv.URL,
					Protocols: []string{"openai.embeddings"},
				},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "embedding-model", Models: config.ModelEntries{{Model: "openai-embed/text-embedding-3-small", Weight: 1}}},
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

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	// 验证错误消息正确透传
	var errResp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error response: %v", err)
	}
	if errResp.Error.Message != "invalid api_key" {
		t.Errorf("error message = %q, want %q", errResp.Error.Message, "invalid api_key")
	}
}

// TestEmbeddings_MixedProtocols_OpenAIWins 验证：failover 模式下 ollama 失败后切换到 openai，
// 能正确按 openai 协议解码（Usage.PromptTokens = 15 而非 5）。
func TestEmbeddings_MixedProtocols_OpenAIWins(t *testing.T) {
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"upstream error"}`))
	}))
	defer ollamaSrv.Close()

	openaiEmbedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		data := []map[string]interface{}{
			{
				"object":    "embedding",
				"index":     0,
				"embedding": make([]float64, 1536),
			},
		}
		resp := map[string]interface{}{
			"object": "list",
			"data":   data,
			"model":  "text-embedding-3-small",
			"usage": map[string]int{
				"prompt_tokens": 15,
				"total_tokens":  15,
			},
		}
		w.Write(mustMarshal(resp))
	}))
	defer openaiEmbedSrv.Close()

	tmpDir := filepath.Join(os.TempDir(), "oh-my-api-test-mixed-openai-wins-"+time.Now().Format("20060102150405"))
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)
	dbPath := filepath.Join(tmpDir, "test.db")

	providers := map[string]config.ProviderConfig{
		"ollama-provider": {
			Endpoint: ollamaSrv.URL,
			APIKey:   "",
			Endpoints: []config.EndpointConfig{
				{
					URL:       ollamaSrv.URL,
					Protocols: []string{"ollama.embed"},
				},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"openai-provider": {
			Endpoint: openaiEmbedSrv.URL,
			APIKey:   "test-key",
			Endpoints: []config.EndpointConfig{
				{
					URL:       openaiEmbedSrv.URL,
					Protocols: []string{"openai.embeddings"},
				},
			},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	falseVal := false
	cfg = &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "mixed-embed-openai-wins",
				Mode:     "failover",
				Exposure: exposurePtr(falseVal),
				Models: config.ModelEntries{
					{Model: "ollama-provider/nomic-embed-text", Weight: 1},
					{Model: "openai-provider/text-embedding-3-small", Weight: 1},
				},
			},
		},
		Redirect: config.RedirectConfigs{
			{Source: "mixed-openai-wins", Target: "mixed-embed-openai-wins"},
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
		Model: "mixed-openai-wins",
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
	if resp.Model != "mixed-openai-wins" {
		t.Errorf("model = %s, want mixed-openai-wins", resp.Model)
	}
	if len(resp.Data) != 1 {
		t.Errorf("data count = %d, want 1", len(resp.Data))
	}
	// 验证走了 openai 解码路径：openai mock 返回 prompt_tokens: 15，ollama mock 返回 prompt_eval_count: 5
	if resp.Usage.PromptTokens != 15 {
		t.Errorf("prompt_tokens = %d, want 15 (indicates openai decoder path)", resp.Usage.PromptTokens)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("total_tokens = %d, want 15 (indicates openai decoder path)", resp.Usage.TotalTokens)
	}
}
