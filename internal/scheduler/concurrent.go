package scheduler

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

type ConcurrentStrategy struct {
	client            *provider.Client
	ratelimit         *ratelimit.Manager
	health            *health.Checker
	prefillTimeout    time.Duration
	streamIdleTimeout time.Duration
}

func NewConcurrentStrategy(client *provider.Client, rl *ratelimit.Manager, h *health.Checker, prefillTimeout time.Duration, streamIdleTimeout time.Duration) *ConcurrentStrategy {
	return &ConcurrentStrategy{
		client:            client,
		ratelimit:         rl,
		health:            h,
		prefillTimeout:    prefillTimeout,
		streamIdleTimeout: streamIdleTimeout,
	}
}

func (s *ConcurrentStrategy) Execute(ctx context.Context, tasks []Task) (*Result, error) {
	if len(tasks) == 0 {
		return nil, ErrNoTasks
	}

	now := time.Now().Local()

	if len(tasks) == 1 {
		t := tasks[0]
		if isProviderDisabledAt(t, now) {
			return nil, ErrNoProviderAvailable
		}
		if err := s.ratelimit.Wait(ctx, t.ProviderName, t.UpstreamModel); err != nil {
			s.health.ReportFailure(health.MakeHealthKey(t.ProviderName, t.OutboundProtocol))
			return nil, &RateLimitError{Provider: t.ProviderName, Err: err}
		}
		return s.executeTask(t)
	}

	return s.race(ctx, tasks, now)
}

func (s *ConcurrentStrategy) parseResponse(resp *http.Response, providerName, upstreamModel, protocol string) (*Result, error) {
	return parseResponse(resp, providerName, upstreamModel, protocol, s.prefillTimeout, s.streamIdleTimeout)
}

func isStreamResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	return strings.HasPrefix(contentType, "text/event-stream")
}

func (s *ConcurrentStrategy) executeTask(task Task) (*Result, error) {
	start := time.Now()
	resp, err := s.client.Do(task.ProviderName, task.Request)
	if err != nil {
		recordAttemptMetric("concurrent", task, nil, err, time.Since(start))
		s.health.ReportFailure(health.MakeHealthKey(task.ProviderName, task.OutboundProtocol))
		return nil, err
	}

	healthKey := health.MakeHealthKey(task.ProviderName, task.OutboundProtocol)
	result, err := s.parseResponse(resp, task.ProviderName, task.UpstreamModel, responseProtocol(task))
	if err != nil {
		recordAttemptMetric("concurrent", task, nil, err, time.Since(start))
		s.health.ReportFailure(healthKey)
		return nil, err
	}

	// 统一应用 TokenHub 错误码分类（仅对硬失败重分级）
	applyTokenHubClassification(result, resp, task.Request)

	recordAttemptMetric("concurrent", task, result, nil, time.Since(start))

	applyHealthAction(s.health, healthKey, result)
	if result.HealthActionInfo.Action != HealthActionNone {
		return result, nil
	}

	switch result.FailureKind {
	case FailureKindSuccess:
		s.health.ReportSuccess(healthKey)
	case FailureKindHard:
		if result.Response != nil && result.Response.StatusCode >= http.StatusInternalServerError {
			s.health.ReportFailure(healthKey)
		}
	}

	return result, nil
}

func closeResultBody(result *Result) {
	if result == nil || result.Response == nil || result.Response.Body == nil {
		return
	}
	result.Response.Body.Close()
}

func (s *ConcurrentStrategy) race(ctx context.Context, tasks []Task, now time.Time) (*Result, error) {
	// concurrent 不按健康状态过滤——即使 provider 被标记为 unhealthy 也应参与竞速。
	// 仅当全部候选均被禁用时段过滤，且尚未发起任何上游请求时，返回无可用 provider。
	if allProvidersDisabled(tasks, now) {
		return nil, ErrNoProviderAvailable
	}

	var available []Task
	for _, t := range tasks {
		if isProviderDisabledAt(t, now) {
			continue
		}
		if s.ratelimit.Allow(t.ProviderName, t.UpstreamModel) {
			available = append(available, t)
		}
	}
	if len(available) == 0 {
		return nil, ErrAllRateLimited
	}

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type outcome struct {
		result *Result
		err    error
	}
	ch := make(chan outcome, len(available))

	for _, t := range available {
		go func(task Task) {
			// Clone request and bind raceCtx to ensure cancel propagates to upstream request
			clonedReq := cloneRequest(task.Request, raceCtx)
			task.Request = clonedReq

			result, err := s.executeTask(task)
			// executeTask has already recorded attempt metric, no need to record again
			select {
			case ch <- outcome{result: result, err: err}:
			case <-raceCtx.Done():
				// raceCtx canceled but executeTask has already recorded the attempt
				// Close response body if needed
				if err == nil && result != nil {
					closeResultBody(result)
				}
			}
		}(t)
	}

	var lastHardResult *Result
	var lastHardErr error
	var lastSoftResult *Result
	remaining := len(available)

	for remaining > 0 {
		select {
		case o := <-ch:
			remaining--

			if o.err == nil && o.result != nil && o.result.FailureKind == FailureKindSuccess {
				cancel()
				closeResultBody(lastHardResult)
				closeResultBody(lastSoftResult)
				return o.result, nil
			}

			if o.err != nil {
				lastHardErr = o.err
				continue
			}
			if o.result == nil {
				continue
			}

			switch o.result.FailureKind {
			case FailureKindSoft:
				slog.Warn("content_filter_soft_failure",
					"provider", o.result.Winner,
					"upstream_identity", o.result.Winner+"/"+o.result.UpstreamModel,
					"reason", o.result.FailureReason,
				)
				closeResultBody(lastSoftResult)
				lastSoftResult = o.result
			default:
				closeResultBody(lastHardResult)
				lastHardResult = o.result
			}
		case <-ctx.Done():
			cancel()
			closeResultBody(lastHardResult)
			closeResultBody(lastSoftResult)
			if lastHardErr != nil {
				return nil, lastHardErr
			}
			return nil, ctx.Err()
		}
	}

	if lastHardResult != nil {
		closeResultBody(lastSoftResult)
		return lastHardResult, nil
	}
	if lastHardErr != nil {
		closeResultBody(lastSoftResult)
		return nil, lastHardErr
	}
	if lastSoftResult != nil {
		return lastSoftResult, nil
	}
	return nil, ErrAllProvidersFailed
}

// cloneRequest clones an HTTP request with a new context
func cloneRequest(req *http.Request, ctx context.Context) *http.Request {
	clonedReq := req.Clone(ctx)
	// Preserve the original body if it exists
	if req.Body != nil && req.GetBody != nil {
		body, err := req.GetBody()
		if err == nil {
			clonedReq.Body = body
		}
	}
	return clonedReq
}
