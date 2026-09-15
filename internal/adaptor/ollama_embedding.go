package adaptor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
)

type OllamaEmbeddingResponse struct {
	Embeddings      [][]float64 `json:"embeddings"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	Error           string      `json:"error"`
}

func BuildOllamaEmbeddingRequest(ctx context.Context, provider *config.ProviderConfig,
	upstreamModel string, req *dto.EmbeddingRequest, normalizedInput []string) (*http.Request, error) {

	endpoint := provider.GetEmbeddingEndpointByProtocol("ollama.embed")
	if endpoint == "" {
		return nil, fmt.Errorf("embedding endpoint is empty")
	}

	u := strings.TrimRight(endpoint, "/")
	if !strings.HasSuffix(u, "/api/embed") {
		u += "/api/embed"
	}

	payload := map[string]any{
		"model": upstreamModel,
	}

	if len(normalizedInput) == 1 && isStringInput(req.Input) {
		payload["input"] = normalizedInput[0]
	} else {
		payload["input"] = normalizedInput
	}

	if req.Dimensions != nil {
		payload["dimensions"] = *req.Dimensions
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build embedding request: %w", err)
	}
	attachReplayableBody(httpReq, body)
	httpReq.Header.Set("Content-Type", "application/json")

	if provider.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}

	return httpReq, nil
}

func DecodeOllamaEmbeddingResponse(body io.Reader, expectedCount int) (*OllamaEmbeddingResponse, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var resp OllamaEmbeddingResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("upstream error: %s", resp.Error)
	}

	if resp.Embeddings == nil {
		return nil, fmt.Errorf("embeddings field missing or null in response")
	}

	if len(resp.Embeddings) != expectedCount {
		return nil, fmt.Errorf("expected %d embeddings, got %d", expectedCount, len(resp.Embeddings))
	}

	for i, embedding := range resp.Embeddings {
		if embedding == nil {
			return nil, fmt.Errorf("embedding at index %d is null", i)
		}
	}

	return &resp, nil
}

func isStringInput(input any) bool {
	_, ok := input.(string)
	return ok
}

func attachReplayableBody(req *http.Request, body []byte) {
	if req == nil {
		return
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	req.Body, _ = req.GetBody()
	req.ContentLength = int64(len(body))
}
