package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/adaptor"
	"github.com/Marstheway/oh-my-api/internal/dto"
	errs "github.com/Marstheway/oh-my-api/internal/errors"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/gin-gonic/gin"
)

func Embeddings(c *gin.Context) {
	var req dto.EmbeddingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusBadRequest,
			errs.ErrInvalidRequest, "invalid request body")
		return
	}

	requestedModel := req.Model
	c.Set("model", requestedModel)

	// Validate encoding_format
	if !req.IsValidEncodingFormat() {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusBadRequest,
			errs.ErrInvalidRequest, "invalid encoding_format")
		return
	}

	// Parse and validate input
	normalizedInput, err := req.ParseInput()
	if err != nil {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusBadRequest,
			errs.ErrInvalidRequest, err.Error())
		return
	}

	// Resolve model to tasks
	result, err := resolver.Resolve(req.Model)
	if err != nil {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusNotFound,
			errs.ErrModelNotFound, fmt.Sprintf("model not found: %s", req.Model))
		return
	}

	// Embeddings 仅支持纯叶子 group（无子 group、无 fallback）
	if !model.IsEmbeddingsCompatiblePlan(result.Plan) {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusUnprocessableEntity,
			errs.ErrInvalidRequest, "embeddings does not support nested group or fallback plan")
		return
	}

	// Filter tasks to only those supporting embedding protocol
	var embeddingTasks []scheduler.Task
	for _, t := range result.Tasks {
		if !t.Provider.SupportsEmbeddingProtocol() {
			continue
		}

		// Build ollama embedding request
		upstreamReq, err := adaptor.BuildOllamaEmbeddingRequest(
			c.Request.Context(),
			&t.Provider,
			t.UpstreamModel,
			&req,
			normalizedInput,
		)
		if err != nil {
			errs.WriteError(c, errs.ProtocolOpenAI, http.StatusInternalServerError,
				errs.ErrInternal, "invalid ollama.embed provider configuration: "+err.Error())
			return
		}

		embeddingTasks = append(embeddingTasks, scheduler.Task{
			ProviderName:     t.ProviderName,
			Provider:         t.Provider,
			UpstreamModel:    t.UpstreamModel,
			ModelGroup:       result.ModelGroup,
			OutboundProtocol: "ollama.embed",
			Weight:           t.Weight,
			Request:          upstreamReq,
		})
	}

	// Check if any embedding-capable tasks exist
	if len(embeddingTasks) == 0 {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusInternalServerError,
			errs.ErrInternal, fmt.Sprintf("no ollama.embed provider available for model group: %s", result.ModelGroup))
		return
	}

	dests := make([]string, len(embeddingTasks))
	for i, t := range embeddingTasks {
		dests[i] = t.ProviderName + "/" + t.UpstreamModel
	}
	slog.Info("request",
		"key", c.GetString("key_name"),
		"protocol", "openai.embeddings",
		"model", req.Model,
		"scheduler", result.Mode,
		"dest", strings.Join(dests, " | "),
	)

	metrics.IncConcurrent()
	start := time.Now()

	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	resp, err := sched.Execute(ctx, result.Mode, embeddingTasks)
	metrics.DecConcurrent()
	if err != nil {
		recordEmbeddingMetrics(c, result.ModelGroup, "", "", "error", time.Since(start))
		handleUpstreamError(c, errs.ProtocolOpenAI, err)
		return
	}
	defer resp.Response.Body.Close()

	// Handle non-2xx upstream responses
	if resp.Response.StatusCode >= 400 {
		recordEmbeddingMetrics(c, result.ModelGroup, resp.Winner, resp.UpstreamModel, "error", time.Since(start))
		// Read body to extract error message
		bodyBytes, _ := io.ReadAll(resp.Response.Body)
		errorMsg := "upstream error"
		if len(bodyBytes) > 0 {
			var ollamaResp struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(bodyBytes, &ollamaResp); err == nil && ollamaResp.Error != "" {
				errorMsg = ollamaResp.Error
			}
		}
		errs.WriteError(c, errs.ProtocolOpenAI, resp.Response.StatusCode,
			errs.ErrUpstreamError, errorMsg)
		return
	}

	c.Set("provider", resp.Winner)
	latency := time.Since(start)

	// Decode Ollama embedding response
	ollamaResp, err := adaptor.DecodeOllamaEmbeddingResponse(resp.Response.Body, len(normalizedInput))
	if err != nil {
		recordEmbeddingMetrics(c, result.ModelGroup, resp.Winner, resp.UpstreamModel, "error", latency)
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusBadGateway,
			errs.ErrUpstreamError, "invalid upstream response: "+err.Error())
		return
	}

	slog.Info("response",
		"status", resp.Response.StatusCode,
		"protocol", "openai.embeddings",
		"latency", fmt.Sprintf("%.2fs", latency.Seconds()),
		"model", resp.Winner+"/"+resp.UpstreamModel,
		"embedding_count", len(ollamaResp.Embeddings),
	)

	// Build OpenAI-compatible response
	openaiResp := dto.EmbeddingResponse{
		Object: "list",
		Model:  requestedModel,
		Data:   make([]dto.EmbeddingResponseItem, len(ollamaResp.Embeddings)),
		Usage: dto.Usage{
			PromptTokens:     ollamaResp.PromptEvalCount,
			CompletionTokens: 0,
			TotalTokens:      ollamaResp.PromptEvalCount,
		},
	}

	for i, embedding := range ollamaResp.Embeddings {
		openaiResp.Data[i] = dto.EmbeddingResponseItem{
			Object:    "embedding",
			Index:     i,
			Embedding: embedding,
		}
	}

	recordEmbeddingMetrics(c, result.ModelGroup, resp.Winner, resp.UpstreamModel, "success", latency)
	recordEmbeddingStats(c, resp.Winner, resp.UpstreamModel, latency)

	c.JSON(http.StatusOK, openaiResp)
}

func recordEmbeddingStats(c *gin.Context, providerName, upstreamModel string, latency time.Duration) {
	keyName := c.GetString("key_name")
	if keyName == "" {
		return
	}

	if err := stats.GetRecorder().Record(
		keyName,
		providerName,
		upstreamModel,
		0, // input_tokens = 0 for embeddings
		0, // output_tokens = 0 for embeddings
		latency.Milliseconds(),
	); err != nil {
		slog.Warn("failed to record stats", "error", err)
	}
}

func recordEmbeddingMetrics(c *gin.Context, modelGroup, provider, upstreamModel, status string, latency time.Duration) {
	keyName := c.GetString("key_name")
	if keyName == "" {
		return
	}

	metrics.RecordRequest(context.Background(), metrics.RequestInfo{
		InboundProtocol:   "openai.embeddings",
		OutboundProtocol:  "ollama.embed",
		Provider:          provider,
		UpstreamModel:     upstreamModel,
		ModelGroup:        modelGroup,
		KeyName:           keyName,
		Status:            status,
		Duration:          latency.Seconds(),
		FirstTokenDuration: 0,
	})
}
