package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/Marstheway/oh-my-api/internal/codec"
	errs "github.com/Marstheway/oh-my-api/internal/errors"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
)

func handleUpstreamError(c *gin.Context, inbound errs.Protocol, err error) {
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

	if resp.Request != nil {
		slog.Warn("upstream error response",
			"status", resp.StatusCode,
			"provider", provider,
			"url", resp.Request.URL.String(),
			"body", string(body),
		)
	} else {
		slog.Warn("upstream error response",
			"status", resp.StatusCode,
			"provider", provider,
			"body", string(body),
		)
	}

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

func shouldFallbackFromResponsesError(err error) bool {
	return err != nil
}

func shouldFallbackFromResponsesStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return false
	case http.StatusBadRequest:
		return false
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return true
	default:
		return statusCode >= 500
	}
}
