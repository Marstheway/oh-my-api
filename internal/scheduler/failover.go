package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

type FailoverStrategy struct {
	leafRuntime
}

type failoverDeferredTask struct {
	task             Task
	waitForRateLimit bool
}

func NewFailoverStrategy(client *provider.Client, rl *ratelimit.Manager, h *health.Checker, prefillTimeout, streamIdleTimeout, nonStreamTimeout time.Duration) *FailoverStrategy {
	return &FailoverStrategy{
		leafRuntime: newLeafRuntime("failover", client, rl, h, prefillTimeout, streamIdleTimeout, nonStreamTimeout),
	}
}

func (s *FailoverStrategy) Execute(ctx context.Context, tasks []Task) (*Result, error) {
	if len(tasks) == 0 {
		return nil, ErrNoTasks
	}
	if err := entryAbort(ctx, "failover"); err != nil {
		return nil, err
	}

	now := time.Now().Local()

	// 全部候选均被禁用时段过滤，且尚未发起上游请求 -> 无可用 provider
	if allProvidersDisabled(tasks, now) {
		return nil, ErrNoProviderAvailable
	}

	fallback := newSequentialFallback()
	deferred := make([]failoverDeferredTask, 0, len(tasks))

	for _, task := range tasks {
		if stop, err := stopSequential(ctx, "failover", fallback, nil); stop {
			return nil, err
		}

		if isProviderDisabledAt(task, now) {
			continue
		}
		if !cascadeLeafReady(s.client, task.ProviderName) {
			continue
		}

		healthKey := health.MakeHealthKey(task.ProviderName, task.OutboundProtocol)
		if !s.health.IsHealthy(healthKey) {
			deferred = append(deferred, failoverDeferredTask{task: task})
			continue
		}

		if !s.ratelimit.Allow(task.ProviderName, task.UpstreamModel, task.ModelQPM) {
			slog.Warn("provider rate limited, skipping",
				"provider", task.ProviderName,
				"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
			)
			deferred = append(deferred, failoverDeferredTask{task: task, waitForRateLimit: true})
			continue
		}

		result, err := s.executeTask(ctx, &task)
		if success, doneResult, doneErr := s.handleAttemptOutcome(ctx, fallback, result, err, task, false); success {
			return doneResult, doneErr
		}
	}

	if len(deferred) == 0 {
		return fallback.Final()
	}

	for _, deferredTask := range deferred {
		if stop, err := stopSequential(ctx, "failover", fallback, nil); stop {
			return nil, err
		}

		task := deferredTask.task
		if deferredTask.waitForRateLimit {
			if !s.ratelimit.Allow(task.ProviderName, task.UpstreamModel, task.ModelQPM) {
				if err := s.ratelimit.Wait(ctx, task.ProviderName, task.UpstreamModel, task.ModelQPM); err != nil {
					if stop, retErr := stopSequential(ctx, "failover", fallback, err,
						"provider", task.ProviderName,
						"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
					); stop {
						return nil, retErr
					}
					slog.Warn("provider rate limit wait failed",
						"provider", task.ProviderName,
						"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
						"error", err.Error(),
					)
					fallback.RecordRateLimit()
					continue
				}
			}
		} else if !s.ratelimit.Allow(task.ProviderName, task.UpstreamModel, task.ModelQPM) {
			slog.Warn("provider rate limited, skipping",
				"provider", task.ProviderName,
				"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
			)
			fallback.RecordRateLimit()
			continue
		}

		result, err := s.executeTask(ctx, &task)
		if success, doneResult, doneErr := s.handleAttemptOutcome(ctx, fallback, result, err, task, true); success {
			return doneResult, doneErr
		}
	}

	return fallback.Final()
}

func (s *FailoverStrategy) handleAttemptOutcome(ctx context.Context, fallback *sequentialFallback, result *Result, err error, task Task, forced bool) (bool, *Result, error) {
	if err != nil {
		if stop, retErr := stopSequential(ctx, "failover", fallback, err,
			"provider", task.ProviderName,
			"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
		); stop {
			return true, nil, retErr
		}
		slog.Warn(s.failoverFailureLogMessage(forced),
			"provider", task.ProviderName,
			"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
			"error", err.Error(),
		)
		fallback.RecordHardError(err)
		return false, nil, nil
	}
	if result == nil {
		return false, nil, nil
	}

	switch result.FailureKind {
	case FailureKindSuccess:
		fallback.DiscardSoftResult()
		return true, result, nil
	case FailureKindSoft:
		slog.Warn("content_filter_soft_failure",
			"provider", task.ProviderName,
			"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
			"reason", result.FailureReason,
			"forced", forced,
		)
		fallback.RecordSoftResult(result)
		return false, nil, nil
	default:
		logAttrs := []any{
			"provider", task.ProviderName,
			"upstream_identity", task.ProviderName + "/" + task.UpstreamModel,
			"status", result.Response.StatusCode,
			"reason", result.FailureReason,
		}
		slog.Warn(s.failoverFailureLogMessage(forced), upstreamErrorLogAttrs(logAttrs, result)...)
		fallback.RecordHardResult(result)
		return false, nil, nil
	}
}

func (s *FailoverStrategy) failoverFailureLogMessage(forced bool) string {
	if forced {
		return "provider failed (forced try), trying next"
	}
	return "provider failed, trying next"
}

type sequentialFallback struct {
	lastHardResult  *Result
	lastHardErr     error
	lastHardWasErr  bool
	lastSoftResult  *Result
	rateLimitedOnly bool
}

func newSequentialFallback() *sequentialFallback {
	return &sequentialFallback{rateLimitedOnly: true}
}

func (f *sequentialFallback) RecordRateLimit() {}

func (f *sequentialFallback) RecordHardResult(result *Result) {
	f.rateLimitedOnly = false
	f.DiscardSoftResult()
	f.lastHardResult = result
	f.lastHardErr = nil
	f.lastHardWasErr = false
}

func (f *sequentialFallback) RecordHardError(err error) {
	f.rateLimitedOnly = false
	f.DiscardSoftResult()
	f.lastHardErr = err
	f.lastHardResult = nil
	f.lastHardWasErr = true
}

func (f *sequentialFallback) RecordSoftResult(result *Result) {
	f.rateLimitedOnly = false
	if f.lastSoftResult != nil && f.lastSoftResult != result {
		closeResultBody(f.lastSoftResult)
	}
	f.lastSoftResult = result
}

func (f *sequentialFallback) DiscardSoftResult() {
	closeResultBody(f.lastSoftResult)
	f.lastSoftResult = nil
}

func (f *sequentialFallback) Final() (*Result, error) {
	if f.lastHardWasErr && f.lastHardErr != nil {
		f.DiscardSoftResult()
		return nil, f.lastHardErr
	}
	if !f.lastHardWasErr && f.lastHardResult != nil {
		f.DiscardSoftResult()
		return f.lastHardResult, nil
	}
	if f.lastSoftResult != nil {
		return f.lastSoftResult, nil
	}
	if f.rateLimitedOnly {
		return nil, ErrAllRateLimited
	}
	return nil, ErrAllProvidersFailed
}
