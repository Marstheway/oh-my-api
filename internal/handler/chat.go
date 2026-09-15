package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/catalog"
	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	errs "github.com/Marstheway/oh-my-api/internal/errors"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/Marstheway/oh-my-api/internal/token"
	"github.com/gin-gonic/gin"
)

var (
	cfg         *config.Config
	resolver    *model.Resolver
	sched       *scheduler.Scheduler
	timeout     = 120 * time.Second
	catalogIdx  catalogContextIndex
	cascadeHubs *cascade.HubRegistry
)

// CatalogSource 定义了 handler 读取上游模型目录的接口。
// 由 serve.go 在 Init 之前通过 SetCatalogSource 注入。
type CatalogSource interface {
	ContextLength(provider, upstreamModel string) (int, bool)
	// CatalogView 返回脱敏、规范化的目录视图，供 Model Group 弹窗自动补全使用。
	// 实现不应返回原始快照中的 url/body/error 等敏感信息。
	CatalogView() catalog.CatalogView
}

var catalogSrc CatalogSource

// SetCatalogSource 设置共享 catalog 信息源。
func SetCatalogSource(src CatalogSource) {
	catalogSrc = src
}

// ResetCatalogSource 清除 catalog source（测试辅助函数）。
func ResetCatalogSource() {
	catalogSrc = nil
}

// SetCascadeHubs 注入 cascade hub registry，供 context_length 推导查询
// 活跃 Cascade session 的元数据快照。
func SetCascadeHubs(hubs *cascade.HubRegistry) {
	cascadeHubs = hubs
}

// ResetCascadeHubs 清除 cascade hub registry（测试辅助函数）。
func ResetCascadeHubs() {
	cascadeHubs = nil
}

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
	keyName := c.GetString("key_name")
	slog.Info("request",
		"key", keyName,
		"protocol", "openai.chat",
		"model", req.Model,
		"scheduler", result.Mode,
		"dest", strings.Join(dests, " | "),
	)

	metrics.IncConcurrent()
	start := time.Now()

	ctx, cancel := inboundRequestContext(c.Request.Context(), c.Request, keyName)
	defer cancel()

	rootNode, matErr := materializePlan(ctx, result.Plan, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
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
		recordRequestMetrics(c, "openai.chat", result.ModelGroup, "", "", "", "error", time.Since(start), 0)
		handleUpstreamError(c, errs.ProtocolOpenAI, schedErr)
		return
	}
	defer resp.Response.Body.Close()

	if resp.Response.StatusCode >= 400 {
		recordRequestMetrics(c, "openai.chat", result.ModelGroup, resp.Winner, resp.UpstreamModel, resp.OutboundProtocol, "error", time.Since(start), 0)
		handleUpstreamResponseError(c, errs.ProtocolOpenAI, resp.Winner, resp.Response)
		return
	}

	c.Set("provider", resp.Winner)

	latency := time.Since(start)

	// 使用 winner 物化时的 outbound 协议写响应
	outboundFormat, reason, cost := winnerOutboundFormat(resp, codec.FormatOpenAIChat)

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
		if status, recordMetrics := handleWriteResponseError(c, errs.ProtocolOpenAI, writeErr, req.Stream, resp.Winner, resp.UpstreamModel, string(outboundFormat)); recordMetrics {
			recordRequestMetrics(c, "openai.chat", result.ModelGroup, resp.Winner, resp.UpstreamModel, resp.OutboundProtocol, status, time.Since(start), resp.StreamTTFT)
		}
		return
	}

	latency = time.Since(start)
	recordRequestMetrics(c, "openai.chat", result.ModelGroup, resp.Winner, resp.UpstreamModel, resp.OutboundProtocol, "success", latency, resp.StreamTTFT)
	recordStats(c, req.Model, resp.Winner, resp.UpstreamModel, counter.GetInputTokens(), counter.GetOutputTokens(), latency)
	recordStreamDecodeMetrics(c, req.Stream, result.ModelGroup, resp.Winner, resp.UpstreamModel, counter.GetOutputTokens(), resp.StreamStartedAt)
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
		dests = append(dests, child.GroupName+"["+child.Mode+": "+strings.Join(childDests, ", ")+"]")
	}
	return dests
}
