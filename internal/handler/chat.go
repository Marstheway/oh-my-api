package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	errs "github.com/Marstheway/oh-my-api/internal/errors"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/Marstheway/oh-my-api/internal/token"
	"github.com/gin-gonic/gin"
)

var (
	cfg        *config.Config
	resolver   *model.Resolver
	sched      *scheduler.Scheduler
	timeout    = 120 * time.Second
	catalogIdx catalogContextIndex
)

func Init(c *config.Config, r *model.Resolver, s *scheduler.Scheduler) {
	cfg = c
	resolver = r
	sched = s

	// 默认 120 秒
	timeout = 120 * time.Second
	if c.Server.Timeout != "" {
		if d, err := time.ParseDuration(c.Server.Timeout); err == nil {
			timeout = d
		}
	}

	catalogIdx = LoadCatalogContextIndex(c)
}

func Chat(c *gin.Context) {
	inboundCodec, err := codec.Get(codec.FormatOpenAIChat)
	if err != nil {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusInternalServerError,
			errs.ErrInternal, "chat codec not available")
		return
	}

	rawReq, err := inboundCodec.DecodeRequest(c)
	if err != nil {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusBadRequest,
			errs.ErrInvalidRequest, "invalid request body")
		return
	}

	req, ok := rawReq.(*dto.ChatCompletionRequest)
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
		obs := ObserveTurnOpenAIChat(req)
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

	result, err := resolver.Resolve(effectiveModel)
	if err != nil {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusNotFound,
			errs.ErrModelNotFound, fmt.Sprintf("model not found: %s", effectiveModel))
		return
	}

	// 记录日志：从 plan tree 收集叶子 dest 信息
	dests := collectPlanDests(result.Plan)
	slog.Info("request",
		"key", c.GetString("key_name"),
		"protocol", "openai.chat",
		"model", req.Model,
		"scheduler", result.Mode,
		"dest", strings.Join(dests, " | "),
	)

	metrics.IncConcurrent()
	start := time.Now()

	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	rootNode, matErr := materializePlan(ctx, result.Plan, codec.FormatOpenAIChat, inboundCodec, req, result.ModelGroup)
	if matErr != nil {
		metrics.DecConcurrent()
		handleCodecError(c, errs.ProtocolOpenAI, "encode_request", matErr)
		return
	}

	resp, schedErr := sched.ExecuteNode(ctx, rootNode)
	metrics.DecConcurrent()
	if schedErr != nil {
		recordRequestMetrics(c, "openai.chat", "openai.chat", result.ModelGroup, "", "", "error", time.Since(start), 0)
		handleUpstreamError(c, errs.ProtocolOpenAI, schedErr)
		return
	}
	defer resp.Response.Body.Close()

	if resp.Response.StatusCode >= 400 {
		recordRequestMetrics(c, "openai.chat", "openai.chat", result.ModelGroup, resp.Winner, resp.UpstreamModel, "error", time.Since(start), 0)
		handleUpstreamResponseError(c, errs.ProtocolOpenAI, resp.Winner, resp.Response)
		return
	}

	c.Set("provider", resp.Winner)

	latency := time.Since(start)

	// 从 plan tree 中找到 winner 对应的 outbound format
	outboundFormat, reason, cost := findWinnerOutbound(result.Plan, resp.Winner, resp.UpstreamModel, codec.FormatOpenAIChat)

	slog.Info("response",
		"status", resp.Response.StatusCode,
		"protocol", "openai.chat",
		"stream", req.Stream,
		"latency", fmt.Sprintf("%.2fs", latency.Seconds()),
		"model", resp.Winner+"/"+resp.UpstreamModel,
		"inbound_format", string(codec.FormatOpenAIChat),
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
		IncludeUsage:        req.StreamOptions != nil && req.StreamOptions.IncludeUsage,
	}

	if writeErr := inboundCodec.WriteResponse(c, outboundFormat, resp.Response, req.Stream, counter, rmc); writeErr != nil {
		handleCodecError(c, errs.ProtocolOpenAI, "write_response", writeErr)
		return
	}

	recordRequestMetrics(c, "openai.chat", "openai.chat", result.ModelGroup, resp.Winner, resp.UpstreamModel, "success", latency, resp.StreamTTFT)
	recordStats(c, resp.Winner, resp.UpstreamModel, counter.GetInputTokens(), counter.GetOutputTokens(), latency)
}

func recordStats(c *gin.Context, providerName, upstreamModel string, inputTokens, outputTokens int, latency time.Duration) {
	keyName := c.GetString("key_name")
	if keyName == "" {
		return
	}

	if err := stats.GetRecorder().Record(
		keyName,
		providerName,
		upstreamModel,
		inputTokens,
		outputTokens,
		latency.Milliseconds(),
	); err != nil {
		slog.Warn("failed to record stats", "error", err)
	}

	metrics.RecordToken(providerName, upstreamModel, "", keyName, inputTokens, outputTokens)
}

// recordRequestMetrics 记录请求级 metrics
func recordRequestMetrics(c *gin.Context, metricInboundProtocol, routingInboundProtocol, modelGroup, provider, upstreamModel, status string, latency, ttft time.Duration) {
	keyName := c.GetString("key_name")
	if keyName == "" {
		return
	}

	var outboundProtocol string
	if provider != "" {
		if prov, exists := cfg.Providers.Items[provider]; exists {
			inboundFormat, err := codec.NormalizeProviderFormat(routingInboundProtocol)
			if err == nil {
				if outboundFormat, _, _, selErr := prov.SelectOutboundFormatForModel(inboundFormat, upstreamModel); selErr == nil {
					outboundProtocol = string(outboundFormat)
				}
			}
			if outboundProtocol == "" {
				outboundProtocol = prov.GetOutboundProtocol(routingInboundProtocol)
			}
		}
	}

	metrics.RecordRequest(context.Background(), metrics.RequestInfo{
		InboundProtocol:   metricInboundProtocol,
		OutboundProtocol:  outboundProtocol,
		Provider:          provider,
		UpstreamModel:     upstreamModel,
		ModelGroup:        modelGroup,
		KeyName:           keyName,
		Status:            status,
		Duration:          latency.Seconds(),
		FirstTokenDuration: ttft.Seconds(),
	})
}

// collectPlanDests 从 plan tree 收集所有叶子的 provider/model 字符串（用于日志）。
func collectPlanDests(node *model.PlanNode) []string {
	if node == nil {
		return nil
	}
	var dests []string
	for _, leaf := range node.Leaves {
		dests = append(dests, leaf.ProviderName+"/"+leaf.UpstreamModel)
	}
	for _, child := range node.Children {
		childDests := collectPlanDests(child)
		dests = append(dests, child.GroupName+"["+child.Mode+": "+strings.Join(childDests,", ")+"]")
	}
	return dests
}

// findWinnerOutbound 从 plan tree 中找 winner 对应的 outbound format。
// 若找不到，返回 inboundFormat 作为 fallback。
func findWinnerOutbound(node *model.PlanNode, winner, upstreamModel string, inboundFormat codec.Format) (codec.Format, string, int) {
	if node == nil {
		return inboundFormat, "unknown", 0
	}
	for _, leaf := range node.Leaves {
		if leaf.ProviderName == winner && leaf.UpstreamModel == upstreamModel {
			f, reason, cost, _ := leaf.Provider.SelectOutboundFormatForModel(inboundFormat, leaf.UpstreamModel)
			return f, reason, cost
		}
	}
	for _, child := range node.Children {
		if f, reason, cost := findWinnerOutbound(child, winner, upstreamModel, inboundFormat); f != inboundFormat || reason != "unknown" {
			return f, reason, cost
		}
	}
	return inboundFormat, "unknown", 0
}
