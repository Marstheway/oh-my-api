package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/token"
)

var allowedCascadeJobProtocols = map[string]codec.Format{
	"openai.chat":        codec.FormatOpenAIChat,
	"openai.responses":   codec.FormatOpenAIResponse,
	"anthropic.messages": codec.FormatAnthropicMessages,
}

// cascadeStatsKey 是 Spoke 为 Hub 下发的 job 使用的统计 key。
// Hub 的真实客户端 key 不向 Spoke 暴露，统一记为 peer 维度的合成名。
func cascadeStatsKey(peer string) string {
	if peer == "" {
		return "cascade"
	}
	return "cascade:" + peer
}

// ExecuteCascadeJob validates, schedules, and maps a spoke-side cascade job back to the hub protocol.
func ExecuteCascadeJob(ctx context.Context, job cascade.Frame, sink cascade.JobResultSink) error {
	start := time.Now()
	if sink == nil {
		return fmt.Errorf("nil cascade job result sink")
	}
	if cfg == nil || cfg.Cascade == nil {
		return sendCascadeJobError(sink, "cascade spoke not configured")
	}
	if resolver == nil || sched == nil {
		return sendCascadeJobError(sink, "handler not initialized")
	}

	inboundFormat, ok := allowedCascadeJobProtocols[job.Protocol]
	if !ok {
		return sendCascadeJobError(sink, fmt.Sprintf("unsupported cascade job protocol %q", job.Protocol))
	}
	if job.Model == "" {
		return sendCascadeJobError(sink, "cascade job model must not be empty")
	}

	inboundCodec, err := codec.Get(inboundFormat)
	if err != nil {
		return sendCascadeJobError(sink, "job codec not available")
	}

	rawReq, err := decodeCascadeJobBody(inboundFormat, job.Body)
	if err != nil {
		return sendCascadeJobError(sink, "invalid cascade job body")
	}
	overwriteCascadeJobModel(rawReq, job.Model)

	stream := job.Stream != nil && *job.Stream

	plan, modelGroup, err := resolveCascadeJobPlan(job.Model)
	if err != nil {
		return sendCascadeJobError(sink, fmt.Sprintf("resolve job model: %v", err))
	}
	statsKey := cascadeStatsKey(cfg.Cascade.Peer)

	ctx = cascade.WithCascadeOrigin(ctx)
	hop := job.Hop
	if hop == "" {
		hop = cfg.Cascade.Token
	}
	ctx = cascade.WithCascadeHop(ctx, hop)

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rootNode, matErr := materializePlan(reqCtx, plan, materializeInput{
		InboundFormat: inboundFormat,
		InboundCodec:  inboundCodec,
		RawReq:        rawReq,
		ModelGroup:    modelGroup,
		ClientModel:   job.Model,
		Rules:         cfg.Rules,
		CascadePeer:   cfg.Cascade.Peer,
		CascadeHop:    cfg.Cascade.Token,
	})
	if matErr != nil {
		return sendCascadeJobError(sink, fmt.Sprintf("materialize job: %v", matErr))
	}

	metrics.IncConcurrent()
	resp, schedErr := sched.ExecuteNode(reqCtx, rootNode)
	metrics.DecConcurrent()
	if schedErr != nil {
		recordRequestMetricsForKey(statsKey, job.Protocol, modelGroup, "", "", "", "error", time.Since(start), 0)
		return sendCascadeJobError(sink, fmt.Sprintf("execute job: %v", schedErr))
	}
	if resp == nil || resp.Response == nil {
		recordRequestMetricsForKey(statsKey, job.Protocol, modelGroup, "", "", "", "error", time.Since(start), 0)
		return sendCascadeJobError(sink, "execute job: empty upstream response")
	}
	defer resp.Response.Body.Close()

	statusCode := resp.Response.StatusCode
	if statusCode >= 400 {
		recordRequestMetricsForKey(statsKey, job.Protocol, modelGroup, resp.Winner, resp.UpstreamModel, resp.OutboundProtocol, "error", time.Since(start), 0)
		upstreamBody, readErr := io.ReadAll(resp.Response.Body)
		if readErr != nil {
			return sendCascadeJobError(sink, fmt.Sprintf("read upstream response: %v", readErr))
		}
		return writeCascadeJobUpstreamErrorBody(sink, false, statusCode, upstreamBody)
	}

	outboundFormat, _, _ := winnerOutboundFormat(resp, inboundFormat)
	counter := token.NewStreamCounterFor(resp.UpstreamModel, countCascadeJobInputTokens(resp.UpstreamModel, rawReq))
	rmc := codec.ResponseModelContext{
		RequestedModel:      job.Model,
		ModelGroup:          modelGroup,
		WinnerProvider:      resp.Winner,
		WinnerUpstreamModel: resp.UpstreamModel,
		IncludeUsage:        cascadeJobIncludeUsage(rawReq),
	}

	writer := newCascadeJobResponseWriter(sink, stream)
	if writeErr := inboundCodec.WriteResponseTo(writer, outboundFormat, resp.Response, stream, counter, rmc); writeErr != nil {
		slog.Warn("cascade job response mapping failed",
			"job_id", job.ID,
			"protocol", job.Protocol,
			"model", job.Model,
			"provider", resp.Winner,
			"upstream_model", resp.UpstreamModel,
			"outbound_protocol", string(outboundFormat),
			"stream", stream,
			"error", writeErr,
		)
		recordRequestMetricsForKey(statsKey, job.Protocol, modelGroup, resp.Winner, resp.UpstreamModel, resp.OutboundProtocol, "error", time.Since(start), resp.StreamTTFT)
		return sendCascadeJobError(sink, fmt.Sprintf("map job response: %v", writeErr))
	}
	if err := writer.finish(stream); err != nil {
		slog.Warn("cascade job response finish failed",
			"job_id", job.ID,
			"protocol", job.Protocol,
			"model", job.Model,
			"provider", resp.Winner,
			"upstream_model", resp.UpstreamModel,
			"outbound_protocol", string(outboundFormat),
			"stream", stream,
			"error", err,
		)
		recordRequestMetricsForKey(statsKey, job.Protocol, modelGroup, resp.Winner, resp.UpstreamModel, resp.OutboundProtocol, "error", time.Since(start), resp.StreamTTFT)
		return err
	}

	latency := time.Since(start)
	recordRequestMetricsForKey(statsKey, job.Protocol, modelGroup, resp.Winner, resp.UpstreamModel, resp.OutboundProtocol, "success", latency, resp.StreamTTFT)
	recordStatsForKey(statsKey, job.Model, resp.Winner, resp.UpstreamModel, counter.GetInputTokens(), counter.GetOutputTokens(), latency)
	return nil
}

func writeCascadeJobUpstreamErrorBody(sink cascade.JobResultSink, stream bool, statusCode int, body []byte) error {
	writer := newCascadeJobResponseWriter(sink, stream)
	writer.WriteHeader(statusCode)
	if len(body) > 0 {
		if _, err := writer.Write(body); err != nil {
			return err
		}
	}
	return writer.finish(stream)
}

func sendCascadeJobError(sink cascade.JobResultSink, message string) error {
	return sink.SendError(message)
}

func decodeCascadeJobBody(inboundFormat codec.Format, body []byte) (any, error) {
	switch inboundFormat {
	case codec.FormatOpenAIChat:
		var req dto.ChatCompletionRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return &req, nil
	case codec.FormatAnthropicMessages:
		var req dto.ClaudeRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return &req, nil
	case codec.FormatOpenAIResponse:
		var req dto.ResponsesRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return &req, nil
	default:
		return nil, fmt.Errorf("unsupported cascade job protocol %q", inboundFormat)
	}
}

func overwriteCascadeJobModel(rawReq any, jobModel string) {
	switch r := rawReq.(type) {
	case *dto.ChatCompletionRequest:
		r.Model = jobModel
	case *dto.ClaudeRequest:
		r.Model = jobModel
	case *dto.ResponsesRequest:
		r.Model = jobModel
	}
}

func resolveCascadeJobPlan(jobModel string) (*model.PlanNode, string, error) {
	// Hub 发起的 job 属于 Spoke 的外部调用，必须遵循 public/hidden 直调权限，
	// 不允许通过 cascade 绕过 internal exposure。
	result, err := resolver.Resolve(jobModel)
	if err != nil {
		return nil, "", err
	}
	return result.Plan, result.ModelGroup, nil
}

func countCascadeJobInputTokens(upstreamModel string, rawReq any) int {
	switch r := rawReq.(type) {
	case *dto.ChatCompletionRequest:
		return token.CountRequestTokensFor(upstreamModel, r)
	case *dto.ClaudeRequest:
		return token.CountRequestTokensFor(upstreamModel, r)
	case *dto.ResponsesRequest:
		return token.CountRequestTokensFor(upstreamModel, r)
	default:
		return 0
	}
}

func cascadeJobIncludeUsage(rawReq any) bool {
	switch r := rawReq.(type) {
	case *dto.ChatCompletionRequest:
		return r.StreamOptions != nil && r.StreamOptions.IncludeUsage
	default:
		return false
	}
}

type cascadeJobResponseWriter struct {
	rec        *httptest.ResponseRecorder
	sink       cascade.JobResultSink
	stream     bool
	buf        bytes.Buffer
	statusCode int
	err        error
}

func newCascadeJobResponseWriter(sink cascade.JobResultSink, stream bool) *cascadeJobResponseWriter {
	return &cascadeJobResponseWriter{
		rec:        httptest.NewRecorder(),
		sink:       sink,
		stream:     stream,
		statusCode: http.StatusOK,
	}
}

func (w *cascadeJobResponseWriter) Header() http.Header {
	return w.rec.Header()
}

func (w *cascadeJobResponseWriter) Write(b []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.stream {
		return w.buf.Write(b)
	}
	return w.rec.Write(b)
}

func (w *cascadeJobResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.rec.WriteHeader(statusCode)
}

func (w *cascadeJobResponseWriter) Flush() {
	if !w.stream || w.buf.Len() == 0 || w.err != nil {
		return
	}
	chunk := w.buf.String()
	w.buf.Reset()
	if err := w.sink.SendResult(chunk, false, 0); err != nil {
		w.err = err
	}
}

func (w *cascadeJobResponseWriter) finish(stream bool) error {
	if w.err != nil {
		return w.err
	}
	if stream {
		if w.buf.Len() > 0 {
			if err := w.sink.SendResult(w.buf.String(), false, 0); err != nil {
				return err
			}
			w.buf.Reset()
		}
		return w.sink.SendResult("", true, w.statusCode)
	}
	return w.sink.SendResult(w.rec.Body.String(), true, w.statusCode)
}

// Ensure cascadeJobResponseWriter implements http.Flusher for streaming codec paths.
var _ http.Flusher = (*cascadeJobResponseWriter)(nil)
