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
	"github.com/Marstheway/oh-my-api/internal/rules"
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

		// Get embedding protocol for this provider
		proto, err := t.Provider.GetEmbeddingProtocol()
		if err != nil {
			errs.WriteError(c, errs.ProtocolOpenAI, http.StatusInternalServerError,
				errs.ErrInternal, "invalid embedding provider configuration: "+err.Error())
			return
		}

		var upstreamReq *http.Request
		switch proto {
		case "ollama.embed":
			upstreamReq, err = adaptor.BuildOllamaEmbeddingRequest(
				c.Request.Context(),
				&t.Provider,
				t.UpstreamModel,
				&req,
				normalizedInput,
			)
		case "openai.embeddings":
			upstreamReq, err = adaptor.BuildOpenAIEmbeddingRequest(
				c.Request.Context(),
				&t.Provider,
				t.UpstreamModel,
				&req,
				normalizedInput,
			)
		default:
			err = fmt.Errorf("unsupported embedding protocol: %s", proto)
		}

		if err != nil {
			errs.WriteError(c, errs.ProtocolOpenAI, http.StatusInternalServerError,
				errs.ErrInternal, "failed to build embedding request: "+err.Error())
			return
		}

		// 调度层 action（qpm / 时段 / retries）对 embeddings 同样生效：match 用请求 model 作
		// client-model、key_name、叶子 provider/upstreamModel。protocol/effort/thinking
		// 不改变 embedding 请求，只消费 qpm、时段与 retries 字段。
		matchCtx := rules.MatchContext{
			ClientModel:   req.Model,
			KeyName:       c.GetString("key_name"),
			UpstreamModel: t.ProviderName + "/" + t.UpstreamModel,
		}
		merged := rules.Evaluate(cfg.Rules, matchCtx)

		embeddingTask := scheduler.Task{
			ProviderName:     t.ProviderName,
			Provider:         t.Provider,
			UpstreamModel:    t.UpstreamModel,
			ModelGroup:       result.ModelGroup,
			OutboundProtocol: proto,
			Weight:           t.Weight,
			Request:          upstreamReq,
		}
		if merged.QPM != nil {
			embeddingTask.ModelQPM = *merged.QPM
		}
		if merged.Retries != nil {
			embeddingTask.Retries = *merged.Retries
		}
		embeddingTask.EnableTimeRange = append([]string(nil), merged.EnableTimeRange...)
		embeddingTask.DisableTimeRange = append([]string(nil), merged.DisableTimeRange...)

		embeddingTasks = append(embeddingTasks, embeddingTask)
	}

	// Check if any embedding-capable tasks exist
	if len(embeddingTasks) == 0 {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusInternalServerError,
			errs.ErrInternal, fmt.Sprintf("no embedding provider available for model group: %s", result.ModelGroup))
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

	// 注入 key_name 到 context
	keyName := c.GetString("key_name")
	if keyName != "" {
		ctx = scheduler.WithKeyName(ctx, keyName)
	}

	// sticky 已由 resolver 解析；仅 load-balance 使用
	var stickyMeta *scheduler.StickyMeta
	if result.Mode == "load-balance" && result.Plan != nil {
		stickyMeta = result.Plan.Sticky
	}

	resp, err := sched.ExecuteWithSticky(ctx, result.Mode, result.ModelGroup, stickyMeta, embeddingTasks)
	metrics.DecConcurrent()
	if err != nil {
		recordEmbeddingMetrics(c, result.ModelGroup, "", "", "error", time.Since(start))
		handleUpstreamError(c, errs.ProtocolOpenAI, err)
		return
	}
	defer resp.Response.Body.Close()

	// Handle non-2xx upstream responses
	if resp.Response.StatusCode >= 400 {
		// Find winner's outbound protocol for error parsing
		var winnerProtocol string
		for _, t := range embeddingTasks {
			if t.ProviderName == resp.Winner && t.UpstreamModel == resp.UpstreamModel {
				winnerProtocol = t.OutboundProtocol
				break
			}
		}
		if winnerProtocol == "" {
			for _, t := range embeddingTasks {
				if t.ProviderName == resp.Winner {
					winnerProtocol = t.OutboundProtocol
					break
				}
			}
		}

		recordEmbeddingMetricsWithProtocol(c, result.ModelGroup, resp.Winner, resp.UpstreamModel, "error", time.Since(start), winnerProtocol)

		// Read body to extract error message
		bodyBytes, _ := io.ReadAll(resp.Response.Body)
		errorMsg := "upstream error"
		if len(bodyBytes) > 0 {
			// Try parsing error based on protocol
			switch winnerProtocol {
			case "openai.embeddings":
				var openaiErr struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(bodyBytes, &openaiErr); err == nil && openaiErr.Error.Message != "" {
					errorMsg = openaiErr.Error.Message
				}
			case "ollama.embed":
				var ollamaResp struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(bodyBytes, &ollamaResp); err == nil && ollamaResp.Error != "" {
					errorMsg = ollamaResp.Error
				}
			}
		}
		errs.WriteError(c, errs.ProtocolOpenAI, resp.Response.StatusCode,
			errs.ErrUpstreamError, errorMsg)
		return
	}

	c.Set("provider", resp.Winner)
	latency := time.Since(start)

	// Find winner's outbound protocol
	var winnerProtocol string
	for _, t := range embeddingTasks {
		if t.ProviderName == resp.Winner && t.UpstreamModel == resp.UpstreamModel {
			winnerProtocol = t.OutboundProtocol
			break
		}
	}
	// If no winner found (should not happen), use empty string for metrics
	if winnerProtocol == "" {
		for _, t := range embeddingTasks {
			if t.ProviderName == resp.Winner {
				winnerProtocol = t.OutboundProtocol
				break
			}
		}
	}

	// Decode response based on protocol
	var embeddings [][]float64
	var promptTokens int
	var totalTokens int

	switch winnerProtocol {
	case "openai.embeddings":
		openaiResp, err := adaptor.DecodeOpenAIEmbeddingResponse(resp.Response.Body, len(normalizedInput))
		if err != nil {
			recordEmbeddingMetricsWithProtocol(c, result.ModelGroup, resp.Winner, resp.UpstreamModel, "error", latency, winnerProtocol)
			errs.WriteError(c, errs.ProtocolOpenAI, http.StatusBadGateway,
				errs.ErrUpstreamError, "invalid upstream response: "+err.Error())
			return
		}
		embeddings = openaiResp.Embeddings
		promptTokens = openaiResp.PromptTokens
		totalTokens = openaiResp.TotalTokens
	case "ollama.embed":
		ollamaResp, err := adaptor.DecodeOllamaEmbeddingResponse(resp.Response.Body, len(normalizedInput))
		if err != nil {
			recordEmbeddingMetricsWithProtocol(c, result.ModelGroup, resp.Winner, resp.UpstreamModel, "error", latency, winnerProtocol)
			errs.WriteError(c, errs.ProtocolOpenAI, http.StatusBadGateway,
				errs.ErrUpstreamError, "invalid upstream response: "+err.Error())
			return
		}
		embeddings = ollamaResp.Embeddings
		promptTokens = ollamaResp.PromptEvalCount
		totalTokens = ollamaResp.PromptEvalCount
	default:
		recordEmbeddingMetricsWithProtocol(c, result.ModelGroup, resp.Winner, resp.UpstreamModel, "error", latency, winnerProtocol)
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusBadGateway,
			errs.ErrUpstreamError, fmt.Sprintf("unknown embedding protocol: %s", winnerProtocol))
		return
	}

	slog.Info("response",
		"status", resp.Response.StatusCode,
		"protocol", "openai.embeddings",
		"latency", fmt.Sprintf("%.2fs", latency.Seconds()),
		"model", resp.Winner+"/"+resp.UpstreamModel,
		"embedding_count", len(embeddings),
	)

	// Build OpenAI-compatible response
	openaiResp := dto.EmbeddingResponse{
		Object: "list",
		Model:  requestedModel,
		Data:   make([]dto.EmbeddingResponseItem, len(embeddings)),
		Usage: dto.Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: 0,
			TotalTokens:      totalTokens,
		},
	}

	for i, embedding := range embeddings {
		openaiResp.Data[i] = dto.EmbeddingResponseItem{
			Object:    "embedding",
			Index:     i,
			Embedding: embedding,
		}
	}

	recordEmbeddingMetricsWithProtocol(c, result.ModelGroup, resp.Winner, resp.UpstreamModel, "success", latency, winnerProtocol)
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

func recordEmbeddingMetricsWithProtocol(c *gin.Context, modelGroup, provider, upstreamModel, status string, latency time.Duration, outboundProtocol string) {
	keyName := c.GetString("key_name")
	if keyName == "" {
		return
	}

	metrics.RecordRequest(context.Background(), metrics.RequestInfo{
		InboundProtocol:    "openai.embeddings",
		OutboundProtocol:   outboundProtocol,
		Provider:           provider,
		UpstreamModel:      upstreamModel,
		ModelGroup:         modelGroup,
		KeyName:            keyName,
		Status:             status,
		Duration:           latency.Seconds(),
		FirstTokenDuration: 0,
	})
}

func recordEmbeddingMetrics(c *gin.Context, modelGroup, provider, upstreamModel, status string, latency time.Duration) {
	recordEmbeddingMetricsWithProtocol(c, modelGroup, provider, upstreamModel, status, latency, "")
}
