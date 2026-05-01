package handler

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/Marstheway/oh-my-api/internal/adaptor"
	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	errs "github.com/Marstheway/oh-my-api/internal/errors"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/Marstheway/oh-my-api/internal/token"
)

var (
	cfg      *config.Config
	resolver *model.Resolver
	sched    *scheduler.Scheduler
)

func Init(c *config.Config, r *model.Resolver, s *scheduler.Scheduler) {
	cfg = c
	resolver = r
	sched = s
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

	c.Set("model", req.Model)

	inputTokens := token.CountRequestTokens(req)

	result, err := resolver.Resolve(req.Model)
	if err != nil {
		errs.WriteError(c, errs.ProtocolOpenAI, http.StatusNotFound,
			errs.ErrModelNotFound, fmt.Sprintf("model not found: %s", req.Model))
		return
	}

	dests := make([]string, len(result.Tasks))
	for i, t := range result.Tasks {
		dests[i] = t.ProviderName + "/" + t.UpstreamModel
	}
	slog.Info("request",
		"key", c.GetString("key_name"),
		"protocol", "openai",
		"model", req.Model,
		"scheduler", result.Mode,
		"dest", strings.Join(dests, ","),
	)

	metrics.IncConcurrent()

	start := time.Now()

	tasks := make([]scheduler.Task, len(result.Tasks))
	for i, t := range result.Tasks {
		outboundStr := t.Provider.GetOutboundProtocol("openai")
		outboundFormat, fmtErr := codec.SelectFormatForInbound(outboundStr, codec.FormatOpenAIChat)
		if fmtErr != nil {
			outboundFormat, fmtErr = codec.SelectFormatForInbound(t.Provider.Protocol, codec.FormatOpenAIChat)
		}
		if fmtErr != nil {
			errs.WriteError(c, errs.ProtocolOpenAI, http.StatusInternalServerError,
				errs.ErrInternal, "unknown outbound format: "+outboundStr)
			return
		}

		bodyBytes, encErr := inboundCodec.EncodeRequest(outboundFormat, req, t.UpstreamModel)
		if encErr != nil {
			handleCodecError(c, errs.ProtocolOpenAI, "encode_request", encErr)
			return
		}

		var ada adaptor.Adaptor
		var adaProtocol adaptor.Protocol
		switch outboundFormat {
		case codec.FormatAnthropicMessages:
			ada = adaptor.GetAdaptor("anthropic")
			adaProtocol = adaptor.ProtocolAnthropic
		case codec.FormatOpenAIResponse:
			ada = adaptor.GetAdaptor("openai")
			adaProtocol = adaptor.ProtocolOpenAIResponse
		default:
			ada = adaptor.GetAdaptor("openai")
			adaProtocol = adaptor.ProtocolOpenAI
		}

		upstreamReq := ada.BuildRequest(c.Request.Context(), &t.Provider, t.UpstreamModel, bytes.NewReader(bodyBytes), adaProtocol)
		tasks[i] = scheduler.Task{
			ProviderName:  t.ProviderName,
			Provider:      t.Provider,
			UpstreamModel: t.UpstreamModel,
			OutboundProtocol: string(outboundFormat),
			Weight:        t.Weight,
			Priority:      t.Priority,
			Request:       upstreamReq,
		}
	}

	resp, err := sched.Execute(c.Request.Context(), result.Mode, result.Timeout, tasks)
	metrics.DecConcurrent()
	if err != nil {
		recordRequestMetrics(c, "openai.chat", "openai", result.ModelGroup, "", "", "error", time.Since(start))
		handleUpstreamError(c, errs.ProtocolOpenAI, err)
		return
	}
	defer resp.Response.Body.Close()

	if resp.Response.StatusCode >= 400 {
		recordRequestMetrics(c, "openai.chat", "openai", result.ModelGroup, resp.Winner, resp.UpstreamModel, "error", time.Since(start))
		handleUpstreamResponseError(c, errs.ProtocolOpenAI, resp.Winner, resp.Response)
		return
	}

	c.Set("provider", resp.Winner)

	latency := time.Since(start)

	slog.Info("response",
		"status", resp.Response.StatusCode,
		"protocol", "openai",
		"latency", fmt.Sprintf("%.2fs", latency.Seconds()),
		"model", resp.Winner+"/"+resp.UpstreamModel,
	)

	var winnerProvider config.ProviderConfig
	for _, t := range result.Tasks {
		if t.ProviderName == resp.Winner {
			winnerProvider = t.Provider
			break
		}
	}
	outboundStr := winnerProvider.GetOutboundProtocol("openai")
	outboundFormat, fmtErr := codec.SelectFormatForInbound(outboundStr, codec.FormatOpenAIChat)
	if fmtErr != nil {
		outboundFormat, _ = codec.SelectFormatForInbound(winnerProvider.Protocol, codec.FormatOpenAIChat)
	}

	counter := token.NewStreamCounter(inputTokens)
	counter.SetStartTime(start)

	if writeErr := inboundCodec.WriteResponse(c, outboundFormat, resp.Response, req.Stream, counter); writeErr != nil {
		handleCodecError(c, errs.ProtocolOpenAI, "write_response", writeErr)
		return
	}

	recordRequestMetrics(c, "openai.chat", "openai", result.ModelGroup, resp.Winner, resp.UpstreamModel, "success", latency)
	recordStats(c, resp.Winner, resp.UpstreamModel, counter.GetInputTokens(), counter.GetOutputTokens(), counter.GetLatency())
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
func recordRequestMetrics(c *gin.Context, metricInboundProtocol, routingInboundProtocol, modelGroup, provider, upstreamModel, status string, latency time.Duration) {
	keyName := c.GetString("key_name")
	if keyName == "" {
		return
	}

	var outboundProtocol string
	if provider != "" {
		if prov, exists := cfg.Providers.Items[provider]; exists {
			outboundProtocol = prov.GetOutboundProtocol(routingInboundProtocol)
		}
	}

	metrics.RecordRequest(context.Background(), metrics.RequestInfo{
		InboundProtocol:  metricInboundProtocol,
		OutboundProtocol: outboundProtocol,
		Provider:         provider,
		UpstreamModel:    upstreamModel,
		ModelGroup:       modelGroup,
		KeyName:          keyName,
		Status:           status,
		Duration:         latency.Seconds(),
	})
}
