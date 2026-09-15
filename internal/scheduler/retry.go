package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

// maxLeafRetries 是同一叶子额外重试次数上限（总尝试 = 1 + retries ≤ 3）。
const maxLeafRetries = 2

// leafRuntime 是四种调度策略共用的叶子执行依赖（上游 client / 健康 / 限流 / 超时）。
type leafRuntime struct {
	client            *provider.Client
	health            *health.Checker
	ratelimit         *ratelimit.Manager
	schedulerName     string
	prefillTimeout    time.Duration
	streamIdleTimeout time.Duration
	nonStreamTimeout  time.Duration
}

func newLeafRuntime(name string, client *provider.Client, rl *ratelimit.Manager, h *health.Checker, prefill, streamIdle, nonStream time.Duration) leafRuntime {
	return leafRuntime{
		client:            client,
		health:            h,
		ratelimit:         rl,
		schedulerName:     name,
		prefillTimeout:    prefill,
		streamIdleTimeout: streamIdle,
		nonStreamTimeout:  nonStream,
	}
}

func leafRetryBudget(task *Task) int {
	if task == nil || task.Retries <= 0 {
		return 0
	}
	if task.Retries > maxLeafRetries {
		return maxLeafRetries
	}
	return task.Retries
}

// isRetryableAttempt 判定该次叶子结果是否允许同一叶子再打一次。
// 白名单：传输错误（非 timeout）、HTTP ≥500（含 529）。
// 排除：父 ctx 中止、attempt/prefill timeout、net timeout、soft/content_filter、额度、其它 4xx（含 429）、未知 error。
func isRetryableAttempt(ctx context.Context, result *Result, err error) bool {
	if ShouldStopScheduling(ctx, err) {
		return false
	}
	if err != nil {
		if errors.Is(err, ErrPrefillTimeout) || errors.Is(err, ErrAttemptTimeout) {
			return false
		}
		return isRetryableTransportError(err)
	}
	if result == nil {
		return false
	}
	if result.FailureKind == FailureKindSuccess || result.FailureKind == FailureKindSoft {
		return false
	}
	if result.FailureReason == failureReasonQuotaExceeded {
		return false
	}
	if result.Response != nil && result.Response.StatusCode >= http.StatusInternalServerError {
		return true
	}
	return false
}

func isRetryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return !netErr.Timeout()
	}
	return false
}

func executeOnce(ctx context.Context, rt leafRuntime, task *Task) (*Result, error) {
	start := time.Now()
	resp, attemptStart, err := doWithAttemptBudget(ctx, rt.client, task, rt.prefillTimeout, rt.nonStreamTimeout)
	if err != nil {
		recordAttemptMetric(rt.schedulerName, *task, nil, err, time.Since(start))
		return nil, err
	}

	result, err := parseResponse(resp, task.ProviderName, task.UpstreamModel, responseProtocol(*task), rt.prefillTimeout, rt.streamIdleTimeout, attemptStart)
	if err != nil {
		recordAttemptMetric(rt.schedulerName, *task, nil, err, time.Since(start))
		return nil, err
	}

	applyProviderErrorClassification(result, resp, task.Request)
	recordAttemptMetric(rt.schedulerName, *task, result, nil, time.Since(start))
	return result, nil
}

func applyAttemptHealth(h *health.Checker, task *Task, result *Result, err error) {
	if h == nil || task == nil {
		return
	}
	healthKey := health.MakeHealthKey(task.ProviderName, task.OutboundProtocol)
	if err != nil {
		reportFailureUnlessAbort(h, healthKey, err)
		return
	}
	if result == nil {
		return
	}

	applyHealthAction(h, healthKey, result)
	if result.HealthActionInfo.Action != HealthActionNone {
		return
	}

	switch result.FailureKind {
	case FailureKindSuccess:
		h.ReportSuccess(healthKey)
	case FailureKindHard:
		if result.Response != nil && result.Response.StatusCode >= http.StatusInternalServerError {
			h.ReportFailure(healthKey)
		}
	}
}

func (rt leafRuntime) executeTask(ctx context.Context, task *Task) (*Result, error) {
	if task == nil {
		return nil, ErrNoTasks
	}

	extra := leafRetryBudget(task)
	if extra > 0 && !requestIsReplayable(task) {
		slog.Warn("upstream retry disabled, request is not replayable",
			"scheduler", rt.schedulerName,
			"provider", task.ProviderName,
			"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
		)
		extra = 0
	}

	var lastResult *Result
	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if stop, abortErr := stopSequential(ctx, rt.schedulerName+"-retry", nil, nil,
				"provider", task.ProviderName,
				"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
			); stop {
				closeResultBody(lastResult)
				return nil, abortErr
			}
			if rt.ratelimit != nil && !rt.ratelimit.Allow(task.ProviderName, task.UpstreamModel, task.ModelQPM) {
				slog.Warn("upstream retry skipped, rate limited",
					"scheduler", rt.schedulerName,
					"provider", task.ProviderName,
					"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
				)
				applyAttemptHealth(rt.health, task, lastResult, lastErr)
				return lastResult, lastErr
			}
			closeResultBody(lastResult)
			lastResult = nil
		}

		result, err := executeOnce(ctx, rt, task)
		if err == nil && result != nil && result.FailureKind == FailureKindSuccess {
			applyAttemptHealth(rt.health, task, result, nil)
			return result, nil
		}

		lastResult, lastErr = result, err
		if !isRetryableAttempt(ctx, result, err) || attempt >= extra {
			applyAttemptHealth(rt.health, task, result, err)
			return result, err
		}

		logRetry(rt.schedulerName, task, result, err, attempt+1, extra-attempt)
	}
}

func logRetry(schedulerName string, task *Task, result *Result, err error, attempt, retriesLeft int) {
	attrs := []any{
		"scheduler", schedulerName,
		"provider", task.ProviderName,
		"upstream_identity", task.ProviderName + "/" + task.UpstreamModel,
		"attempt", attempt,
		"retries_left", retriesLeft,
	}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	} else if result != nil && result.Response != nil {
		attrs = append(attrs, "status", result.Response.StatusCode, "reason", result.FailureReason)
	}
	slog.Warn("upstream attempt failed, retrying", attrs...)
}

func requestIsReplayable(task *Task) bool {
	if task == nil || task.Request == nil {
		return true
	}
	req := task.Request
	if req.GetBody != nil {
		return true
	}
	return req.Body == nil || req.Body == http.NoBody
}
