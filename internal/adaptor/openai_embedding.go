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

type OpenAIEmbeddingResponse struct {
	Object string                       `json:"object"`
	Data   []OpenAIEmbeddingDataItem    `json:"data"`
	Model  string                       `json:"model"`
	Usage  OpenAIEmbeddingResponseUsage `json:"usage"`
}

type OpenAIEmbeddingDataItem struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

type OpenAIEmbeddingResponseUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type OpenAIEmbeddingDecodeResult struct {
	Embeddings   [][]float64
	PromptTokens int
	TotalTokens  int
}

func BuildOpenAIEmbeddingRequest(ctx context.Context, provider *config.ProviderConfig,
	upstreamModel string, req *dto.EmbeddingRequest, normalizedInput []string) (*http.Request, error) {

	endpoint := provider.GetEmbeddingEndpointByProtocol("openai.embeddings")
	if endpoint == "" {
		return nil, fmt.Errorf("embedding endpoint is empty")
	}

	u := strings.TrimRight(endpoint, "/")
	switch {
	case strings.HasSuffix(u, "/v1/embeddings"):
		// 已是完整路径，原样使用
	case strings.HasSuffix(u, "/v1"):
		// 与 chat 共用 .../v1 base：只补 /embeddings，避免拼成 /v1/v1/embeddings
		u += "/embeddings"
	default:
		u += "/v1/embeddings"
	}

	payload := map[string]any{
		"model": upstreamModel,
	}

	if len(normalizedInput) == 1 && isStringInput(req.Input) {
		payload["input"] = normalizedInput[0]
	} else {
		payload["input"] = normalizedInput
	}

	if req.EncodingFormat != "" {
		payload["encoding_format"] = req.EncodingFormat
	}

	if req.Dimensions != nil {
		payload["dimensions"] = *req.Dimensions
	}

	if req.User != "" {
		payload["user"] = req.User
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

func DecodeOpenAIEmbeddingResponse(body io.Reader, expectedCount int) (*OpenAIEmbeddingDecodeResult, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var resp OpenAIEmbeddingResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if resp.Data == nil {
		return nil, fmt.Errorf("data field missing or null in response")
	}

	if len(resp.Data) != expectedCount {
		return nil, fmt.Errorf("expected %d embeddings, got %d", expectedCount, len(resp.Data))
	}

	embeddings := make([][]float64, len(resp.Data))
	for i, item := range resp.Data {
		if item.Embedding == nil {
			return nil, fmt.Errorf("embedding at index %d is null", i)
		}
		embeddings[i] = item.Embedding
	}

	promptTokens := resp.Usage.PromptTokens
	totalTokens := resp.Usage.TotalTokens

	// 如果 total_tokens 缺失，回退为 prompt_tokens
	if totalTokens == 0 && promptTokens > 0 {
		totalTokens = promptTokens
	}

	return &OpenAIEmbeddingDecodeResult{
		Embeddings:   embeddings,
		PromptTokens: promptTokens,
		TotalTokens:  totalTokens,
	}, nil
}
