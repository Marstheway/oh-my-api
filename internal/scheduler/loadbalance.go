package scheduler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

type LoadBalanceStrategy struct {
	client    *provider.Client
	ratelimit *ratelimit.Manager
	health    *health.Checker
}

func NewLoadBalanceStrategy(client *provider.Client, rl *ratelimit.Manager, h *health.Checker) *LoadBalanceStrategy {
	return &LoadBalanceStrategy{
		client:    client,
		ratelimit: rl,
		health:    h,
	}
}

func (s *LoadBalanceStrategy) Execute(ctx context.Context, tasks []Task) (*Result, error) {
	if len(tasks) == 0 {
		return nil, ErrNoTasks
	}

	// 过滤不健康的 provider
	healthyTasks := s.filterHealthy(tasks)
	if len(healthyTasks) == 0 {
		return nil, ErrNoHealthyProvider
	}

	selector := NewWeightedSelector(healthyTasks)
	var lastErr error
	var lastResp *http.Response
	var lastTask *Task

	for !selector.IsEmpty() {
		task := selector.Select()
		if task == nil {
			break
		}

		slog.Debug("load-balance selected candidate",
			"provider", task.ProviderName,
			"model", task.UpstreamModel,
			"weight", task.Weight,
			"remaining_candidates", selector.Len(),
		)

		resp, err := s.executeTask(ctx, task)
		if err == nil && resp.StatusCode < 400 {
			slog.Debug("load-balance request succeeded",
				"provider", task.ProviderName,
				"model", task.UpstreamModel,
				"status", resp.StatusCode,
			)
			return s.parseResponse(resp, task.ProviderName, task.UpstreamModel, task.Provider.Protocol)
		}

		// 失败，移除该 provider 并尝试下一个
		failureReason := ""
		if resp != nil && resp.StatusCode >= 400 {
			failureReason = summarizeUpstreamError(resp, 120)
		}

		slog.Debug("load-balance request failed, removing candidate",
			"provider", task.ProviderName,
			"model", task.UpstreamModel,
			"error", err,
			"status", func() int {
				if resp != nil {
					return resp.StatusCode
				}
				return 0
			}(),
			"reason", failureReason,
		)
		selector.Remove(task.ProviderName)
		lastErr = err
		lastResp = resp
		lastTask = task

		// 检查是否还有健康 provider
		healthyTasks = s.filterHealthy(tasks)
		if len(healthyTasks) == 0 {
			break
		}
	}

	// 返回最后一个错误响应
	if lastResp != nil && lastTask != nil {
		return s.parseResponse(lastResp, lastTask.ProviderName, lastTask.UpstreamModel, lastTask.Provider.Protocol)
	}

	if lastErr != nil && IsRateLimitError(lastErr) {
		return nil, ErrAllRateLimited
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrNoHealthyProvider
}

func (s *LoadBalanceStrategy) filterHealthy(tasks []Task) []Task {
	var healthy []Task
	for _, t := range tasks {
		// 注意：这里不能调用 Allow()，否则会为未选中的 provider 也消耗令牌
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

func (s *LoadBalanceStrategy) executeTask(ctx context.Context, task *Task) (*http.Response, error) {
	// 真正选中后才消耗令牌；限流时直接尝试下一个，避免单个 provider 阻塞整次请求
	if !s.ratelimit.Allow(task.ProviderName, task.UpstreamModel) {
		slog.Warn("provider rate limited, trying next",
			"provider", task.ProviderName,
			"model", task.UpstreamModel,
		)
		return nil, &RateLimitError{Provider: task.ProviderName, Err: ErrAllRateLimited}
	}

	resp, err := s.client.Do(task.ProviderName, task.Request)

	// 上报健康状态
	if err != nil {
		s.health.ReportFailure(health.MakeHealthKey(task.ProviderName, task.OutboundProtocol))
		return nil, err
	}
	if resp.StatusCode >= 500 {
		s.health.ReportFailure(health.MakeHealthKey(task.ProviderName, task.OutboundProtocol))
	} else if resp.StatusCode < 400 {
		s.health.ReportSuccess(health.MakeHealthKey(task.ProviderName, task.OutboundProtocol))
	}

	return resp, err
}

func (s *LoadBalanceStrategy) parseResponse(resp *http.Response, providerName, upstreamModel, protocol string) (*Result, error) {
	return parseResponse(resp, providerName, upstreamModel, protocol)
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
