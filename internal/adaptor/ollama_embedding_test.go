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

func TestOllamaEmbedding_Build_EndpointWithoutPath(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOllamaEmbeddingRequest(ctx, provider, "nomic-embed-text", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOllamaEmbeddingRequest failed: %v", buildErr)
	}

	if httpReq == nil {
		t.Fatal("expected non-nil http.Request")
	}
	if !strings.HasSuffix(httpReq.URL.Path, "/api/embed") {
		t.Errorf("expected URL to end with /api/embed, got %s", httpReq.URL.Path)
	}
	if httpReq.Header.Get("Authorization") != "Bearer test-key" {
		t.Errorf("expected Authorization header, got %s", httpReq.Header.Get("Authorization"))
	}
	if httpReq.Header.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type: application/json, got %s", httpReq.Header.Get("Content-Type"))
	}
	assertReplayableBody(t, httpReq)
}

func TestOllamaEmbedding_Build_EndpointAlreadyHasPath(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:11434/api/embed",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOllamaEmbeddingRequest(ctx, provider, "nomic-embed-text", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOllamaEmbeddingRequest failed: %v", buildErr)
	}

	if httpReq == nil {
		t.Fatal("expected non-nil http.Request")
	}
	if !strings.HasSuffix(httpReq.URL.Path, "/api/embed") {
		t.Errorf("expected URL to end with /api/embed, got %s", httpReq.URL.Path)
	}
	if strings.Count(httpReq.URL.Path, "/api/embed") > 1 {
		t.Errorf("expected /api/embed to appear once, but got path %s", httpReq.URL.Path)
	}
}

func TestOllamaEmbedding_Build_EmptyEndpoint(t *testing.T) {
	provider := &config.ProviderConfig{}
	req := &dto.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	_, err := BuildOllamaEmbeddingRequest(context.Background(), provider, "nomic-embed-text", req, normalizedInput)
	if err == nil {
		t.Fatal("expected error for empty embedding endpoint")
	}
}

func TestOllamaEmbedding_Build_EmptyAPIKey(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "",
	}
	req := &dto.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOllamaEmbeddingRequest(ctx, provider, "nomic-embed-text", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOllamaEmbeddingRequest failed: %v", buildErr)
	}

	if httpReq == nil {
		t.Fatal("expected non-nil http.Request")
	}
	if httpReq.Header.Get("Authorization") != "" {
		t.Errorf("expected no Authorization header, got %s", httpReq.Header.Get("Authorization"))
	}
}

func TestOllamaEmbedding_Build_SingleInputString(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOllamaEmbeddingRequest(ctx, provider, "nomic-embed-text", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOllamaEmbeddingRequest failed: %v", buildErr)
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

func TestOllamaEmbedding_Build_MultipleInputsArray(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: []any{"hello", "world"},
	}
	normalizedInput := []string{"hello", "world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOllamaEmbeddingRequest(ctx, provider, "nomic-embed-text", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOllamaEmbeddingRequest failed: %v", buildErr)
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

func TestOllamaEmbedding_Build_WithDimensions(t *testing.T) {
	dimensions := 1024
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model:      "nomic-embed-text",
		Input:      "hello world",
		Dimensions: &dimensions,
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOllamaEmbeddingRequest(ctx, provider, "nomic-embed-text", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOllamaEmbeddingRequest failed: %v", buildErr)
	}

	body, _ := io.ReadAll(httpReq.Body)
	var payload map[string]any
	err := json.Unmarshal(body, &payload)
	if err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}

	dims, ok := payload["dimensions"]
	if !ok {
		t.Error("expected dimensions field in request body")
	}
	if float64(dimensions) != dims {
		t.Errorf("expected dimensions %d, got %v", dimensions, dims)
	}
}

func TestOllamaEmbedding_Build_WithoutDimensions(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOllamaEmbeddingRequest(ctx, provider, "nomic-embed-text", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOllamaEmbeddingRequest failed: %v", buildErr)
	}

	body, _ := io.ReadAll(httpReq.Body)
	var payload map[string]any
	err := json.Unmarshal(body, &payload)
	if err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}

	if _, ok := payload["dimensions"]; ok {
		t.Error("expected dimensions field to not be present")
	}
}

func TestOllamaEmbedding_Decode_Success(t *testing.T) {
	responseBody := map[string]any{
		"embeddings":        [][]float64{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}},
		"prompt_eval_count": 10,
	}
	body, _ := json.Marshal(responseBody)
	reader := bytes.NewReader(body)

	result, err := DecodeOllamaEmbeddingResponse(reader, 2)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Embeddings) != 2 {
		t.Errorf("expected 2 embeddings, got %d", len(result.Embeddings))
	}
	if result.PromptEvalCount != 10 {
		t.Errorf("expected prompt_eval_count 10, got %d", result.PromptEvalCount)
	}
}

func TestOllamaEmbedding_Decode_WrongCount(t *testing.T) {
	responseBody := map[string]any{
		"embeddings":        [][]float64{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}},
		"prompt_eval_count": 10,
	}
	body, _ := json.Marshal(responseBody)
	reader := bytes.NewReader(body)

	result, err := DecodeOllamaEmbeddingResponse(reader, 3)
	if err == nil {
		t.Fatal("expected error for mismatched embedding count")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %v", result)
	}
}

func TestOllamaEmbedding_Decode_InvalidJSON(t *testing.T) {
	reader := bytes.NewReader([]byte("invalid json"))

	result, err := DecodeOllamaEmbeddingResponse(reader, 1)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %v", result)
	}
}

func TestOllamaEmbedding_Decode_MissingEmbeddings(t *testing.T) {
	responseBody := map[string]any{
		"prompt_eval_count": 10,
	}
	body, _ := json.Marshal(responseBody)
	reader := bytes.NewReader(body)

	result, err := DecodeOllamaEmbeddingResponse(reader, 1)
	if err == nil {
		t.Fatal("expected error for missing embeddings")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %v", result)
	}
}

func TestOllamaEmbedding_Decode_NullEmbeddingItem(t *testing.T) {
	reader := bytes.NewReader([]byte(`{"embeddings":[null],"prompt_eval_count":10}`))

	result, err := DecodeOllamaEmbeddingResponse(reader, 1)
	if err == nil {
		t.Fatal("expected error when embedding item is null")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %v", result)
	}
}

func TestOllamaEmbedding_Decode_ErrorField(t *testing.T) {
	responseBody := map[string]any{
		"embeddings": [][]float64{{0.1, 0.2, 0.3}},
		"error":      "model not found",
	}
	body, _ := json.Marshal(responseBody)
	reader := bytes.NewReader(body)

	result, err := DecodeOllamaEmbeddingResponse(reader, 1)
	if err == nil {
		t.Fatal("expected error when error field is non-empty")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %v", result)
	}
}

func TestOllamaEmbedding_Decode_MissingPromptEvalCount(t *testing.T) {
	responseBody := map[string]any{
		"embeddings": [][]float64{{0.1, 0.2, 0.3}},
	}
	body, _ := json.Marshal(responseBody)
	reader := bytes.NewReader(body)

	result, err := DecodeOllamaEmbeddingResponse(reader, 1)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.PromptEvalCount != 0 {
		t.Errorf("expected prompt_eval_count 0 when missing, got %d", result.PromptEvalCount)
	}
}

func TestOllamaEmbedding_Build_MethodIsPost(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOllamaEmbeddingRequest(ctx, provider, "nomic-embed-text", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOllamaEmbeddingRequest failed: %v", buildErr)
	}

	if httpReq.Method != http.MethodPost {
		t.Errorf("expected POST method, got %s", httpReq.Method)
	}
}

func TestOllamaEmbedding_Build_Model(t *testing.T) {
	provider := &config.ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "test-key",
	}
	req := &dto.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello world",
	}
	normalizedInput := []string{"hello world"}

	ctx := context.Background()
	httpReq, buildErr := BuildOllamaEmbeddingRequest(ctx, provider, "some-upstream-model", req, normalizedInput)
	if buildErr != nil {
		t.Fatalf("BuildOllamaEmbeddingRequest failed: %v", buildErr)
	}

	body, _ := io.ReadAll(httpReq.Body)
	var payload map[string]any
	err := json.Unmarshal(body, &payload)
	if err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}

	model := payload["model"]
	if model != "some-upstream-model" {
		t.Errorf("expected model to be 'some-upstream-model', got %s", model)
	}
}

func assertReplayableBody(t *testing.T, req *http.Request) {
	t.Helper()
	if req.GetBody == nil {
		t.Fatal("expected GetBody so the request can be retried")
	}
	first, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	rc, err := req.GetBody()
	if err != nil {
		t.Fatalf("GetBody: %v", err)
	}
	defer rc.Close()
	second, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read replayed body: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("replayed body %q != original %q", second, first)
	}
}
