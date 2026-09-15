package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/dto"
	errs "github.com/Marstheway/oh-my-api/internal/errors"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/token"
	"github.com/gin-gonic/gin"
)

// Responses handles POST /v1/responses requests (OpenAI Responses API format).
func Responses(c *gin.Context) {
	inboundCodec, err := codec.Get(codec.FormatOpenAIResponse)
	if err != nil {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusInternalServerError,
			errs.ErrInternal, "responses codec not available")
		return
	}

	rawReq, err := inboundCodec.DecodeRequest(c)
	if err != nil {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusBadRequest,
			errs.ErrInvalidRequest, "invalid request body")
		return
	}

	req, ok := rawReq.(*dto.ResponsesRequest)
	if !ok {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusInternalServerError,
			errs.ErrInternal, "unexpected request type")
		return
	}

	originalModel := req.Model
	c.Set("model", originalModel)

	// Smart route：解码请求后、resolver.Resolve 之前
	effectiveModel := originalModel
	if resolver.SmartRouteEnabled() {
		sri := resolver.GetSmartRouteIndex()
		obs := ObserveTurnOpenAIResponse(req)
		srResult := SmartRoute(c.Request.Context(), originalModel, obs, sri)
		effectiveModel = srResult.EffectiveModel

		// 记录 smart route 日志
		slog.Info("smart route",
			"enabled", true,
			"original_model", originalModel,
			"decision", srResult.Decision,
			"decision_path", srResult.DecisionPath,
			"effective_model", effectiveModel,
			"fallback_reason", srResult.FallbackReason,
		)
	}

	result, resolveErr := resolver.Resolve(effectiveModel)
	if resolveErr != nil {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusNotFound,
			errs.ErrModelNotFound, fmt.Sprintf("model not found: %s", effectiveModel))
		return
	}

	dests := collectPlanDests(result.Plan)
	slog.Info("request",
		"key", c.GetString("key_name"),
		"protocol", "openai.responses",
		"model", req.Model,
		"scheduler", result.Mode,
		"dest", strings.Join(dests, " | "),
	)

	metrics.IncConcurrent()
	start := time.Now()

	keyName := c.GetString("key_name")
	ctx, cancel := inboundRequestContext(c.Request.Context(), c.Request, keyName)
	defer cancel()

	rootNode, matErr := materializePlan(ctx, result.Plan, materializeInput{
		InboundFormat: codec.FormatOpenAIResponse,
		InboundCodec:  inboundCodec,
		RawReq:        req,
		ModelGroup:    result.ModelGroup,
		ClientModel:   originalModel,
		KeyName:       keyName,
		Rules:         cfg.Rules,
	})
	if matErr != nil {
		metrics.DecConcurrent()
		handleCodecError(c, errs.ProtocolOpenAI, "encode_request", matErr)
		return
	}

	resp, schedErr := sched.ExecuteNode(ctx, rootNode)
	metrics.DecConcurrent()
	if schedErr != nil {
		recordRequestMetrics(c, "openai.responses", result.ModelGroup, "", "", "", "error", time.Since(start), 0)
		handleUpstreamError(c, errs.ProtocolOpenAI, schedErr)
		return
	}
	defer resp.Response.Body.Close()

	if resp.Response.StatusCode >= 400 {
		recordRequestMetrics(c, "openai.responses", result.ModelGroup, resp.Winner, resp.UpstreamModel, resp.OutboundProtocol, "error", time.Since(start), 0)
		handleUpstreamResponseError(c, errs.ProtocolOpenAI, resp.Winner, resp.Response)
		return
	}

	c.Set("provider", resp.Winner)

	latency := time.Since(start)
	outboundFormat, reason, cost := winnerOutboundFormat(resp, codec.FormatOpenAIResponse)

	slog.Info("response",
		"status", resp.Response.StatusCode,
		"protocol", "openai.responses",
		"stream", req.Stream,
		"latency", fmt.Sprintf("%.2fs", latency.Seconds()),
		"model", resp.Winner+"/"+resp.UpstreamModel,
		"inbound_format", string(codec.FormatOpenAIResponse),
		"outbound_selected", string(outboundFormat),
		"selection_reason", reason,
		"conversion_cost", cost,
		"candidate_count", func() int {
			if result.Plan == nil {
				return 0
			}
			return len(result.Plan.Leaves) + len(result.Plan.Children)
		}(),
	)

	counter := token.NewStreamCounterFor(resp.UpstreamModel, token.CountRequestTokensFor(resp.UpstreamModel, req))

	rmc := codec.ResponseModelContext{
		RequestedModel:      req.Model,
		ModelGroup:          result.ModelGroup,
		WinnerProvider:      resp.Winner,
		WinnerUpstreamModel: resp.UpstreamModel,
	}

	if writeErr := inboundCodec.WriteResponse(c, outboundFormat, resp.Response, req.Stream, counter, rmc); writeErr != nil {
		if status, recordMetrics := handleWriteResponseError(c, errs.ProtocolOpenAI, writeErr, req.Stream, resp.Winner, resp.UpstreamModel, string(outboundFormat)); recordMetrics {
			recordRequestMetrics(c, "openai.responses", result.ModelGroup, resp.Winner, resp.UpstreamModel, resp.OutboundProtocol, status, time.Since(start), resp.StreamTTFT)
		}
		return
	}

	latency = time.Since(start)
	recordRequestMetrics(c, "openai.responses", result.ModelGroup, resp.Winner, resp.UpstreamModel, resp.OutboundProtocol, "success", latency, resp.StreamTTFT)
	recordStats(c, req.Model, resp.Winner, resp.UpstreamModel, counter.GetInputTokens(), counter.GetOutputTokens(), latency)
	recordStreamDecodeMetrics(c, req.Stream, result.ModelGroup, resp.Winner, resp.UpstreamModel, counter.GetOutputTokens(), resp.StreamStartedAt)
}
