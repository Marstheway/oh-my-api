package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/codec"
	errs "github.com/Marstheway/oh-my-api/internal/errors"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/gin-gonic/gin"
)

// statusClientClosed is nginx's non-standard status for "client closed request".
// Used as a bare status (no body) so we never return 200 on a canceled turn, and
// never masquerade client cancel as a 502 upstream failure.
const statusClientClosed = 499

func inboundRequestContext(parent context.Context, r *http.Request, keyName string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	if keyName != "" {
		ctx = scheduler.WithKeyName(ctx, keyName)
	}
	return injectCascadeHop(ctx, r), cancel
}

func injectCascadeHop(ctx context.Context, r *http.Request) context.Context {
	if r == nil {
		return ctx
	}
	if hop := r.Header.Get(cascade.HopHeader); hop != "" {
		return cascade.WithCascadeHop(ctx, hop)
	}
	return ctx
}

func handleUpstreamError(c *gin.Context, inbound errs.Protocol, err error) {
	// Client disconnect / request context canceled: scheduling already stopped.
	// Do not synthesize a 502 {"upstream connection failed"} body — that
	// misattributes the failure and confuses downstream clients (e.g. Hermes).
	// Log once and end the handler without a response body.
	if errors.Is(err, context.Canceled) {
		slog.Info("client_canceled",
			"inbound", string(inbound),
			"error", err.Error(),
		)
		// Bare status only (no JSON body). WriteHeader before Abort so gin's
		// test/recorder path still records the code; avoid accidental 200.
		if !c.Writer.Written() {
			c.Status(statusClientClosed)
		}
		c.Abort()
		return
	}
	if scheduler.IsRateLimitError(err) {
		errs.WriteError(c, inbound, http.StatusTooManyRequests,
			errs.ErrRateLimitTimeout, "rate limit wait timeout, please retry later")
		return
	}
	if errors.Is(err, scheduler.ErrAllRateLimited) {
		errs.WriteError(c, inbound, http.StatusTooManyRequests,
			errs.ErrRateLimitTimeout, "all providers rate limited, please retry later")
		return
	}
	if errors.Is(err, scheduler.ErrNoProviderAvailable) {
		errs.WriteError(c, inbound, http.StatusServiceUnavailable,
			errs.ErrUpstreamError, "no provider available")
		return
	}
	if errors.Is(err, scheduler.ErrAllProvidersFailed) {
		errs.WriteError(c, inbound, http.StatusBadGateway,
			errs.ErrUpstreamError, "all providers failed")
		return
	}
	if errors.Is(err, scheduler.ErrNoTasks) {
		errs.WriteError(c, inbound, http.StatusInternalServerError,
			errs.ErrInternal, "no tasks to execute")
		return
	}
	if errors.Is(err, scheduler.ErrUnknownStrategy) {
		errs.WriteError(c, inbound, http.StatusInternalServerError,
			errs.ErrInternal, "unknown scheduling strategy")
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		errs.WriteError(c, inbound, http.StatusGatewayTimeout,
			errs.ErrUpstreamTimeout, "upstream request timeout")
		return
	}
	errs.WriteError(c, inbound, http.StatusBadGateway,
		errs.ErrUpstreamError, "upstream connection failed")
}

func handleUpstreamResponseError(c *gin.Context, inbound errs.Protocol, provider string, resp *http.Response) {
	body, _ := io.ReadAll(resp.Body)

	attrs := []any{
		"status", resp.StatusCode,
		"provider", provider,
		"body", string(body),
	}
	if resp.Request != nil {
		attrs = append(attrs, "url", resp.Request.URL.String())
		attrs = append(attrs, scheduler.RequestBodyDiagAttrs(resp.Request)...)
	}
	slog.Warn("upstream error response", attrs...)

	if len(body) > 0 {
		c.Data(resp.StatusCode, "application/json", body)
		return
	}

	errs.WriteError(c, inbound, resp.StatusCode,
		errs.ErrUpstreamError, "upstream error")
}

// handleCodecError 处理 codec 协议转换失败，统一映射为 502。
// phase: 转换阶段描述（如 "encode_request"、"write_response"），用于结构化日志。
func handleCodecError(c *gin.Context, inbound errs.Protocol, phase string, err error) {
	var convErr *codec.ConversionError
	if errors.As(err, &convErr) {
		slog.Warn("codec conversion error",
			"phase", convErr.Phase,
			"step", convErr.Step,
			"inbound_format", convErr.InboundFormat,
			"outbound_format", convErr.OutboundFormat,
			"reason", convErr.Reason,
			"error", convErr.Err,
		)
	} else {
		slog.Warn("codec conversion error",
			"phase", phase,
			"inbound", string(inbound),
			"error", err.Error(),
		)
	}
	errs.WriteError(c, inbound, http.StatusBadGateway,
		errs.ErrConversionError, "protocol conversion failed: "+err.Error())
}

// handleWriteResponseError 按来源处理响应写回阶段的错误。
// 返回值表示请求级指标的状态，以及是否应记录请求级指标。
func handleWriteResponseError(c *gin.Context, inbound errs.Protocol, err error, isStream bool, provider, upstreamModel, outboundProtocol string) (string, bool) {
	var convErr *codec.ConversionError
	if errors.As(err, &convErr) {
		handleCodecError(c, inbound, "write_response", err)
		return "conversion_error", false
	}

	reason := "upstream_read_error"
	log := slog.Warn
	if errors.Is(err, context.Canceled) {
		reason = "client_canceled"
		log = slog.Info
	} else if errors.Is(err, context.DeadlineExceeded) {
		reason = "upstream_timeout"
	} else if errors.Is(err, io.ErrUnexpectedEOF) {
		reason = "upstream_eof"
	} else if errors.Is(err, codec.ErrStreamTruncated) {
		reason = "upstream_truncated"
	}

	log("response interrupted",
		"reason", reason,
		"provider", provider,
		"upstream_model", upstreamModel,
		"inbound_protocol", string(inbound),
		"outbound_protocol", outboundProtocol,
		"stream", isStream,
		"error", err,
	)
	if isStream {
		metrics.RecordStreamInterrupted(provider, upstreamModel, outboundProtocol, reason)
	}

	// 客户端连接断开时写回通常返回 broken pipe，而不是 context.Canceled。
	// 仅在请求上下文仍有效时认定为上游未完整交付响应并上报健康检查。
	if !errors.Is(err, context.Canceled) && (c.Request == nil || c.Request.Context().Err() == nil) {
		sched.ReportStreamFailure(provider, outboundProtocol)
	}

	// Client cancel: end without a synthetic upstream 502 body (same policy as
	// handleUpstreamError). Partial stream may already have been written.
	if errors.Is(err, context.Canceled) {
		if !c.Writer.Written() {
			c.Status(statusClientClosed)
		}
		c.Abort()
		return reason, true
	}

	if !c.Writer.Written() {
		errs.WriteError(c, inbound, http.StatusBadGateway,
			errs.ErrUpstreamError, "upstream response interrupted")
	}
	return reason, true
}
