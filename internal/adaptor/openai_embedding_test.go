package adaptor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
)

func TestOpenAIEmbedding_Build_EndpointWithoutPath(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:18080",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOpenAIEmbeddingRequest(ctx, provider, "text-embedding-3-small", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOpenAIEmbeddingRequest failed: %v", buildErr)
	}

	if httpReq == nil {
		t.Fatal("expected non-nil http.Request")
	}
	if !strings.HasSuffix(httpReq.URL.Path, "/v1/embeddings") {
		t.Errorf("expected URL to end with /v1/embeddings, got %s", httpReq.URL.Path)
	}
	if httpReq.Header.Get("Authorization") != "Bearer test-key" {
		t.Errorf("expected Authorization header, got %s", httpReq.Header.Get("Authorization"))
	}
	if httpReq.Header.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type: application/json, got %s", httpReq.Header.Get("Content-Type"))
	}
	assertReplayableBody(t, httpReq)
}

func TestOpenAIEmbedding_Build_EndpointAlreadyHasPath(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:18080/v1/embeddings",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOpenAIEmbeddingRequest(ctx, provider, "text-embedding-3-small", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOpenAIEmbeddingRequest failed: %v", buildErr)
	}

	if httpReq == nil {
		t.Fatal("expected non-nil http.Request")
	}
	if !strings.HasSuffix(httpReq.URL.Path, "/v1/embeddings") {
		t.Errorf("expected URL to end with /v1/embeddings, got %s", httpReq.URL.Path)
	}
	if strings.Count(httpReq.URL.Path, "/v1/embeddings") > 1 {
		t.Errorf("expected /v1/embeddings to appear once, but got path %s", httpReq.URL.Path)
	}
}

func TestOpenAIEmbedding_Build_EndpointEndsWithV1(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:18080/v1",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOpenAIEmbeddingRequest(ctx, provider, "text-embedding-3-small", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOpenAIEmbeddingRequest failed: %v", buildErr)
	}

	if httpReq == nil {
		t.Fatal("expected non-nil http.Request")
	}
	// endpoint 以 /v1 结尾时，应只补 /embeddings，不能拼成 /v1/v1/embeddings
	if httpReq.URL.Path != "/v1/embeddings" {
		t.Errorf("expected path /v1/embeddings, got %s", httpReq.URL.Path)
	}
}

func TestOpenAIEmbedding_Build_EndpointEndsWithV1Slash(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:18080/v1/",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOpenAIEmbeddingRequest(ctx, provider, "text-embedding-3-small", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOpenAIEmbeddingRequest failed: %v", buildErr)
	}

	if httpReq.URL.Path != "/v1/embeddings" {
		t.Errorf("expected path /v1/embeddings, got %s", httpReq.URL.Path)
	}
}

func TestOpenAIEmbedding_Build_EmptyEndpoint(t *testing.T) {
	provider := &config.ProviderConfig{}
	req := &dto.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	_, err := BuildOpenAIEmbeddingRequest(context.Background(), provider, "text-embedding-3-small", req, normalizedInput)
	if err == nil {
		t.Fatal("expected error for empty embedding endpoint")
	}
}

func TestOpenAIEmbedding_Build_EmptyAPIKey(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:18080",
		APIKey:   "",
	}
	req := &dto.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOpenAIEmbeddingRequest(ctx, provider, "text-embedding-3-small", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOpenAIEmbeddingRequest failed: %v", buildErr)
	}

	if httpReq == nil {
		t.Fatal("expected non-nil http.Request")
	}
	if httpReq.Header.Get("Authorization") != "" {
		t.Errorf("expected no Authorization header, got %s", httpReq.Header.Get("Authorization"))
	}
}

func TestOpenAIEmbedding_Build_SingleInputString(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:18080",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOpenAIEmbeddingRequest(ctx, provider, "text-embedding-3-small", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOpenAIEmbeddingRequest failed: %v", buildErr)
	}

	body, _ := io.ReadAll(httpReq.Body)
	var payload map[string]any
	err := json.Unmarshal(body, &payload)
	if err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}

	input := payload["input"]
	if str, ok := input.(string); ok {
		if str != "hello world" {
			t.Errorf("expected input to be 'hello world', got %s", str)
		}
	} else {
		t.Errorf("expected input to be string, got type %T", input)
	}
}

func TestOpenAIEmbedding_Build_MultipleInputsArray(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:18080",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: []any{"hello", "world"},
	}
	normalizedInput := []string{"hello", "world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOpenAIEmbeddingRequest(ctx, provider, "text-embedding-3-small", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOpenAIEmbeddingRequest failed: %v", buildErr)
	}

	body, _ := io.ReadAll(httpReq.Body)
	var payload map[string]any
	err := json.Unmarshal(body, &payload)
	if err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}

	input := payload["input"]
	arr, ok := input.([]any)
	if !ok {
		t.Fatalf("expected input to be array, got type %T", input)
	}
	if len(arr) != 2 {
		t.Errorf("expected 2 input items, got %d", len(arr))
	}
}

func TestOpenAIEmbedding_Build_WithOptionalFields(t *testing.T) {
	dimensions := 1024
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:18080",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model:          "text-embedding-3-small",
		Input:          "hello world",
		Dimensions:     &dimensions,
		EncodingFormat: "float",
		User:           "test-user",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOpenAIEmbeddingRequest(ctx, provider, "text-embedding-3-small", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOpenAIEmbeddingRequest failed: %v", buildErr)
	}

	body, _ := io.ReadAll(httpReq.Body)
	var payload map[string]any
	err := json.Unmarshal(body, &payload)
	if err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}

	if payload["dimensions"] != float64(dimensions) {
		t.Errorf("expected dimensions %d, got %v", dimensions, payload["dimensions"])
	}
	if payload["encoding_format"] != "float" {
		t.Errorf("expected encoding_format float, got %v", payload["encoding_format"])
	}
	if payload["user"] != "test-user" {
		t.Errorf("expected user 'test-user', got %v", payload["user"])
	}
}

func TestOpenAIEmbedding_Build_Model(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:18080",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOpenAIEmbeddingRequest(ctx, provider, "upstream-model", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOpenAIEmbeddingRequest failed: %v", buildErr)
	}

	body, _ := io.ReadAll(httpReq.Body)
	var payload map[string]any
	err := json.Unmarshal(body, &payload)
	if err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}

	model := payload["model"]
	if model != "upstream-model" {
		t.Errorf("expected model to be 'upstream-model', got %s", model)
	}
}

func TestOpenAIEmbedding_Decode_Success(t *testing.T) {
	responseBody := map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}},
			{"object": "embedding", "index": 1, "embedding": []float64{0.4, 0.5, 0.6}},
		},
		"model": "text-embedding-3-small",
		"usage": map[string]int{
			"prompt_tokens": 10,
			"total_tokens":  10,
		},
	}
	body, _ := json.Marshal(responseBody)
	reader := bytes.NewReader(body)

	result, err := DecodeOpenAIEmbeddingResponse(reader, 2)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Embeddings) != 2 {
		t.Errorf("expected 2 embeddings, got %d", len(result.Embeddings))
	}
	if result.PromptTokens != 10 {
		t.Errorf("expected prompt_tokens 10, got %d", result.PromptTokens)
	}
	if result.TotalTokens != 10 {
		t.Errorf("expected total_tokens 10, got %d", result.TotalTokens)
	}
}

func TestOpenAIEmbedding_Decode_WrongCount(t *testing.T) {
	responseBody := map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}},
			{"object": "embedding", "index": 1, "embedding": []float64{0.4, 0.5, 0.6}},
		},
		"model": "text-embedding-3-small",
		"usage": map[string]int{
			"prompt_tokens": 10,
			"total_tokens":  10,
		},
	}
	body, _ := json.Marshal(responseBody)
	reader := bytes.NewReader(body)

	result, err := DecodeOpenAIEmbeddingResponse(reader, 3)
	if err == nil {
		t.Fatal("expected error for mismatched embedding count")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %v", result)
	}
}

func TestOpenAIEmbedding_Decode_InvalidJSON(t *testing.T) {
	reader := bytes.NewReader([]byte("invalid json"))

	result, err := DecodeOpenAIEmbeddingResponse(reader, 1)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %v", result)
	}
}

func TestOpenAIEmbedding_Decode_MissingData(t *testing.T) {
	responseBody := map[string]any{
		"object": "list",
		"model":  "text-embedding-3-small",
		"usage": map[string]int{
			"prompt_tokens": 10,
			"total_tokens":  10,
		},
	}
	body, _ := json.Marshal(responseBody)
	reader := bytes.NewReader(body)

	result, err := DecodeOpenAIEmbeddingResponse(reader, 1)
	if err == nil {
		t.Fatal("expected error for missing data")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %v", result)
	}
}

func TestOpenAIEmbedding_Decode_NullEmbeddingItem(t *testing.T) {
	responseBody := map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"object": "embedding", "index": 0, "embedding": nil},
		},
		"model": "text-embedding-3-small",
		"usage": map[string]int{
			"prompt_tokens": 10,
			"total_tokens":  10,
		},
	}
	body, _ := json.Marshal(responseBody)
	reader := bytes.NewReader(body)

	result, err := DecodeOpenAIEmbeddingResponse(reader, 1)
	if err == nil {
		t.Fatal("expected error when embedding item is null")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %v", result)
	}
}

func TestOpenAIEmbedding_Decode_MissingTotalTokens(t *testing.T) {
	responseBody := map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}},
		},
		"model": "text-embedding-3-small",
		"usage": map[string]int{
			"prompt_tokens": 10,
		},
	}
	body, _ := json.Marshal(responseBody)
	reader := bytes.NewReader(body)

	result, err := DecodeOpenAIEmbeddingResponse(reader, 1)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// total_tokens 缺失时，应回退为 prompt_tokens
	if result.TotalTokens != 10 {
		t.Errorf("expected total_tokens to fallback to prompt_tokens 10, got %d", result.TotalTokens)
	}
}

func TestOpenAIEmbedding_Build_MethodIsPost(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:18080",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOpenAIEmbeddingRequest(ctx, provider, "text-embedding-3-small", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOpenAIEmbeddingRequest failed: %v", buildErr)
	}

	if httpReq.Method != http.MethodPost {
		t.Errorf("expected POST method, got %s", httpReq.Method)
	}
}
