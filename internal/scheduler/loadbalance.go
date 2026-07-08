package scheduler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

type LoadBalanceStrategy struct {
	client            *provider.Client
	ratelimit         *ratelimit.Manager
	health            *health.Checker
	prefillTimeout    time.Duration
	streamIdleTimeout time.Duration
}

func NewLoadBalanceStrategy(client *provider.Client, rl *ratelimit.Manager, h *health.Checker, prefillTimeout time.Duration, streamIdleTimeout time.Duration) *LoadBalanceStrategy {
	return &LoadBalanceStrategy{
		client:            client,
		ratelimit:         rl,
		health:            h,
		prefillTimeout:    prefillTimeout,
		streamIdleTimeout: streamIdleTimeout,
	}
}

func (s *LoadBalanceStrategy) Execute(ctx context.Context, tasks []Task) (*Result, error) {
	if len(tasks) == 0 {
		return nil, ErrNoTasks
	}

	now := time.Now().Local()

	if allProvidersDisabledOrUnhealthy(tasks, s.health, now) {
		return nil, ErrNoProviderAvailable
	}

	healthyTasks := s.filterHealthy(tasks, now)
	if len(healthyTasks) == 0 {
		return nil, ErrNoProviderAvailable
	}

	selector := NewWeightedSelector(healthyTasks)
	fallback := newSequentialFallback()

	for !selector.IsEmpty() {
		task := selector.Select()
		if task == nil {
			break
		}
		selectedTask := *task

		slog.Debug("load-balance selected candidate",
			"provider", selectedTask.ProviderName,
			"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
			"weight", selectedTask.Weight,
			"remaining_candidates", selector.Len(),
		)

		result, err := s.executeTask(ctx, &selectedTask)
		if err == nil && result != nil && result.FailureKind == FailureKindSuccess {
			slog.Debug("load-balance request succeeded",
				"provider", selectedTask.ProviderName,
				"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
				"status", result.Response.StatusCode,
			)
			fallback.DiscardSoftResult()
			return result, nil
		}

		selector.RemoveTask(task)
		s.removeUnhealthySiblingTasks(selector, &selectedTask)

		if IsRateLimitError(err) {
			fallback.RecordRateLimit()
			continue
		}

		if err != nil {
			slog.Debug("load-balance request failed, removing candidate",
				"provider", selectedTask.ProviderName,
				"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
				"error", err,
			)
			fallback.RecordHardError(err)
			continue
		}
		if result == nil {
			continue
		}

		if result.FailureKind == FailureKindSoft {
			slog.Debug("content_filter_soft_failure",
				"provider", selectedTask.ProviderName,
				"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
				"reason", result.FailureReason,
			)
			fallback.RecordSoftResult(result)
			continue
		}

		failureReason := result.FailureReason
		if failureReason == "" && result.Response != nil && result.Response.StatusCode >= http.StatusBadRequest {
			failureReason = summarizeUpstreamError(result.Response, 120)
		}

		slog.Debug("load-balance request failed, removing candidate",
			"provider", selectedTask.ProviderName,
			"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
			"status", func() int {
				if result.Response != nil {
					return result.Response.StatusCode
				}
				return 0
			}(),
			"reason", failureReason,
		)
		fallback.RecordHardResult(result)
	}

	return fallback.Final()
}

func (s *LoadBalanceStrategy) filterHealthy(tasks []Task, now time.Time) []Task {
	var healthy []Task
	for _, t := range tasks {
		// 注意：这里不能调用 Allow()，否则会为未选中的 provider 也消耗令牌
		if isProviderDisabledAt(t, now) {
			continue
		}
		healthKey := health.MakeHealthKey(t.ProviderName, t.OutboundProtocol)
		if s.health.IsHealthy(healthKey) {
			healthy = append(healthy, t)
		}
	}

	if len(healthy) != len(tasks) {
		slog.Debug("load-balance filtered unhealthy providers",
			"total", len(tasks),
			"healthy", len(healthy),
		)
	}

	return healthy
}

func (s *LoadBalanceStrategy) removeUnhealthySiblingTasks(selector *WeightedSelector, task *Task) {
	if selector == nil || task == nil {
		return
	}

	healthKey := health.MakeHealthKey(task.ProviderName, task.OutboundProtocol)
	if s.health.IsHealthy(healthKey) {
		return
	}

	selector.RemoveByHealthKey(healthKey)
}

func (s *LoadBalanceStrategy) executeTask(ctx context.Context, task *Task) (*Result, error) {
	start := time.Now()
	// 真正选中后才消耗令牌；限流时直接尝试下一个，避免单个 provider 阻塞整次请求
	if !s.ratelimit.Allow(task.ProviderName, task.UpstreamModel) {
		slog.Warn("provider rate limited, trying next",
			"provider", task.ProviderName,
			"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
		)
		return nil, &RateLimitError{Provider: task.ProviderName, Err: ErrAllRateLimited}
	}

	resp, err := s.client.Do(task.ProviderName, task.Request)
	if err != nil {
		recordAttemptMetric("loadbalance", *task, nil, err, time.Since(start))
		s.health.ReportFailure(health.MakeHealthKey(task.ProviderName, task.OutboundProtocol))
		return nil, err
	}

	healthKey := health.MakeHealthKey(task.ProviderName, task.OutboundProtocol)
	result, err := s.parseResponse(resp, task.ProviderName, task.UpstreamModel, responseProtocol(*task), s.prefillTimeout, s.streamIdleTimeout)
	if err != nil {
		recordAttemptMetric("loadbalance", *task, nil, err, time.Since(start))
		s.health.ReportFailure(healthKey)
		return nil, err
	}

	// 统一应用 TokenHub 错误码分类（仅对硬失败重分级）
	applyTokenHubClassification(result, resp, task.Request)

	recordAttemptMetric("loadbalance", *task, result, nil, time.Since(start))

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

func (s *LoadBalanceStrategy) parseResponse(resp *http.Response, providerName, upstreamModel, protocol string, prefillTimeout time.Duration, streamIdleTimeout time.Duration) (*Result, error) {
	return parseResponse(resp, providerName, upstreamModel, protocol, prefillTimeout, streamIdleTimeout)
}

func summarizeUpstreamError(resp *http.Response, maxRunes int) string {
	if resp == nil || resp.Body == nil {
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "read error body failed"
	}
	resp.Body.Close()
	resp.Body = io.NopCloser(strings.NewReader(string(body)))

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}

	runes := []rune(trimmed)
	if maxRunes > 0 && len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return trimmed
}
