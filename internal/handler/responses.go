package handler

import (
	"bytes"
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
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/Marstheway/oh-my-api/internal/token"
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

	c.Set("model", req.Model)

	inputTokens := token.CountRequestTokens(req)

	result, resolveErr := resolver.Resolve(req.Model)
	if resolveErr != nil {
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
		"protocol", "openai.response",
		"model", req.Model,
		"scheduler", result.Mode,
		"dest", strings.Join(dests, ","),
	)

	metrics.IncConcurrent()

	start := time.Now()

	tasks := make([]scheduler.Task, len(result.Tasks))
	for i, t := range result.Tasks {
		preferredOutbound, learned := responsesFallbackCache.GetPreferred(t.ProviderName, "openai.response")
		if !learned {
			preferredOutbound = t.Provider.GetOutboundProtocol("openai.response")
		}

		task, buildErr := buildTaskForOutboundFormat(c, inboundCodec, req, t, preferredOutbound)
		if buildErr != nil {
			handleCodecError(c, errs.ProtocolOpenAI, "encode_request", buildErr)
			return
		}
		tasks[i] = task
	}

	resp, schedErr := sched.Execute(c.Request.Context(), result.Mode, result.Timeout, tasks)
	metrics.DecConcurrent()
	if schedErr != nil {
		if len(tasks) == 1 && shouldFallbackFromResponsesError(schedErr) {
			fallbackTask, buildErr := buildTaskForOutboundFormat(c, inboundCodec, req, result.Tasks[0], "openai")
			if buildErr != nil {
				handleCodecError(c, errs.ProtocolOpenAI, "encode_request", buildErr)
				return
			}

			fallbackResp, fallbackErr := sched.Execute(c.Request.Context(), result.Mode, result.Timeout, []scheduler.Task{fallbackTask})
			if fallbackErr == nil && fallbackResp != nil && fallbackResp.Response != nil && fallbackResp.Response.StatusCode < 400 {
				responsesFallbackCache.MarkPreferred(fallbackTask.ProviderName, "openai.response", "openai")
				resp = fallbackResp
				schedErr = nil
			} else {
				slog.Warn("responses fallback failed",
					"provider", fallbackTask.ProviderName,
					"fallback_error", fallbackErr,
				)
			}
		}
	}
	if schedErr != nil {
		recordRequestMetrics(c, "openai.response", "openai.response", result.ModelGroup, "", "", "error", time.Since(start))
		handleUpstreamError(c, errs.ProtocolOpenAI, schedErr)
		return
	}
	defer resp.Response.Body.Close()

	if resp.Response.StatusCode >= 400 {
		if shouldFallbackFromResponsesStatus(resp.Response.StatusCode) && resp.Winner != "" {
			var winnerTask *scheduler.Task
			for i := range tasks {
				if tasks[i].ProviderName == resp.Winner {
					winnerTask = &tasks[i]
					break
				}
			}

			if winnerTask != nil {
				fallbackTask, buildErr := buildTaskForOutboundFormat(c, inboundCodec, req, scheduler.Task{
					ProviderName:  winnerTask.ProviderName,
					Provider:      winnerTask.Provider,
					UpstreamModel: winnerTask.UpstreamModel,
					Weight:        winnerTask.Weight,
					Priority:      winnerTask.Priority,
				}, "openai")
				if buildErr == nil {
					fallbackResp, fallbackErr := sched.Execute(c.Request.Context(), result.Mode, result.Timeout, []scheduler.Task{fallbackTask})
					if fallbackErr == nil && fallbackResp != nil && fallbackResp.Response != nil && fallbackResp.Response.StatusCode < 400 {
						responsesFallbackCache.MarkPreferred(fallbackTask.ProviderName, "openai.response", "openai")
						resp = fallbackResp
					} else if fallbackResp != nil && fallbackResp.Response != nil {
						slog.Warn("responses fallback after HTTP error failed",
							"provider", fallbackTask.ProviderName,
							"fallback_status", fallbackResp.Response.StatusCode,
							"fallback_error", fallbackErr,
						)
						defer fallbackResp.Response.Body.Close()
					} else if fallbackErr != nil {
						slog.Warn("responses fallback after HTTP error failed",
							"provider", fallbackTask.ProviderName,
							"fallback_error", fallbackErr,
						)
					}
				}
			}
		}
	}

	if resp.Response.StatusCode >= 400 {
		recordRequestMetrics(c, "openai.response", "openai.response", result.ModelGroup, resp.Winner, resp.UpstreamModel, "error", time.Since(start))
		handleUpstreamResponseError(c, errs.ProtocolOpenAI, resp.Winner, resp.Response)
		return
	}

	c.Set("provider", resp.Winner)

	latency := time.Since(start)
	slog.Info("response",
		"status", resp.Response.StatusCode,
		"protocol", "openai.response",
		"latency", fmt.Sprintf("%.2fs", latency.Seconds()),
		"model", resp.Winner+"/"+resp.UpstreamModel,
	)

	// 找到 winner provider 对应的 outbound format
	winnerOutbound := "openai.response"
	var winnerProvider config.ProviderConfig
	for _, t := range result.Tasks {
		if t.ProviderName == resp.Winner {
			winnerProvider = t.Provider
			for _, preparedTask := range tasks {
				if preparedTask.ProviderName == resp.Winner {
					winnerOutbound = preparedTask.OutboundProtocol
					break
				}
			}
			break
		}
	}
	outboundStr := winnerOutbound
	if outboundStr == "" {
		outboundStr = winnerProvider.GetOutboundProtocol("openai.response")
	}
	outboundFormat, _ := codec.SelectFormatForInbound(outboundStr, codec.FormatOpenAIResponse)

	counter := token.NewStreamCounter(inputTokens)
	counter.SetStartTime(start)

	if writeErr := inboundCodec.WriteResponse(c, outboundFormat, resp.Response, req.Stream, counter); writeErr != nil {
		handleCodecError(c, errs.ProtocolOpenAI, "write_response", writeErr)
		return
	}

	recordRequestMetrics(c, "openai.response", "openai.response", result.ModelGroup, resp.Winner, resp.UpstreamModel, "success", latency)
	recordStats(c, resp.Winner, resp.UpstreamModel, counter.GetInputTokens(), counter.GetOutputTokens(), counter.GetLatency())
}

func buildTaskForOutboundFormat(c *gin.Context, inboundCodec codec.Codec, req *dto.ResponsesRequest, t scheduler.Task, outboundStr string) (scheduler.Task, error) {
	outboundFormat, err := codec.SelectFormatForInbound(outboundStr, codec.FormatOpenAIResponse)
	if err != nil {
		outboundFormat, err = codec.SelectFormatForInbound(t.Provider.Protocol, codec.FormatOpenAIResponse)
		if err != nil {
			return scheduler.Task{}, err
		}
	}

	bodyBytes, encErr := inboundCodec.EncodeRequest(outboundFormat, req, t.UpstreamModel)
	if encErr != nil {
		return scheduler.Task{}, encErr
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
	return scheduler.Task{
		ProviderName:      t.ProviderName,
		Provider:          t.Provider,
		UpstreamModel:     t.UpstreamModel,
		OutboundProtocol:  string(outboundFormat),
		Weight:            t.Weight,
		Priority:          t.Priority,
		Request:           upstreamReq,
	}, nil
}
