package scheduler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

type FailoverStrategy struct {
	client    *provider.Client
	ratelimit *ratelimit.Manager
	health    *health.Checker
}

func NewFailoverStrategy(client *provider.Client, rl *ratelimit.Manager, h *health.Checker) *FailoverStrategy {
	return &FailoverStrategy{
		client:    client,
		ratelimit: rl,
		health:    h,
	}
}

func (s *FailoverStrategy) Execute(ctx context.Context, tasks []Task) (*Result, error) {
	if len(tasks) == 0 {
		return nil, ErrNoTasks
	}

	// 正常流程：按顺序尝试健康的 provider
	result, err := s.tryHealthyProviders(ctx, tasks)
	if err == nil && result != nil {
		return result, nil
	}

	// 全部不健康时，强制尝试所有 provider
	return s.forceTryAll(ctx, tasks)
}

// tryHealthyProviders 按顺序尝试健康的 provider
func (s *FailoverStrategy) tryHealthyProviders(ctx context.Context, tasks []Task) (*Result, error) {
	var lastErr error
	var lastResult *Result
	anySkipped := false

	for _, task := range tasks {
		// 检查超时
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// 先检查健康状态
		healthKey := health.MakeHealthKey(task.ProviderName, task.OutboundProtocol)
		if !s.health.IsHealthy(healthKey) {
			anySkipped = true
			continue
		}

		// 再检查限流
		if !s.ratelimit.Allow(task.ProviderName, task.UpstreamModel) {
			slog.Warn("provider rate limited, skipping",
				"provider", task.ProviderName,
			)
			anySkipped = true
			continue
		}

		// 执行请求
		result, err := s.executeTask(ctx, &task)
		if err == nil && result != nil && result.Response.StatusCode < 400 {
			return result, nil
		}

		// 记录失败
		if err != nil {
			slog.Warn("provider failed, trying next",
				"provider", task.ProviderName,
				"model", task.UpstreamModel,
				"error", err.Error(),
			)
			lastErr = err
		} else if result != nil {
			slog.Warn("provider failed, trying next",
				"provider", task.ProviderName,
				"model", task.UpstreamModel,
				"status", result.Response.StatusCode,
			)
			lastResult = result
		}
	}

	// 如果有跳过（说明有 provider 不健康或限流），返回 nil 让调用者决定是否强制尝试
	if anySkipped {
		return nil, nil
	}

	// 所有健康的 provider 都尝试过了，返回最后一个结果
	if lastResult != nil {
		return lastResult, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, nil
}

// forceTryAll 忽略健康状态，强制按顺序尝试所有 provider
func (s *FailoverStrategy) forceTryAll(ctx context.Context, tasks []Task) (*Result, error) {
	var lastErr error
	var lastResult *Result
	allRateLimited := true

	for _, task := range tasks {
		// 检查超时
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// 检查限流（强制尝试时也要尊重限流，但会等待）
		if !s.ratelimit.Allow(task.ProviderName, task.UpstreamModel) {
			// 等待限流
			if err := s.ratelimit.Wait(ctx, task.ProviderName, task.UpstreamModel); err != nil {
				slog.Warn("provider rate limit wait failed",
					"provider", task.ProviderName,
					"error", err.Error(),
				)
				continue
			}
		} else {
			allRateLimited = false
		}

		// 执行请求
		result, err := s.executeTask(ctx, &task)
		if err == nil && result != nil && result.Response.StatusCode < 400 {
			return result, nil
		}

		// 记录失败
		if err != nil {
			slog.Warn("provider failed (forced try), trying next",
				"provider", task.ProviderName,
				"model", task.UpstreamModel,
				"error", err.Error(),
			)
			lastErr = err
		} else if result != nil {
			slog.Warn("provider failed (forced try), trying next",
				"provider", task.ProviderName,
				"model", task.UpstreamModel,
				"status", result.Response.StatusCode,
			)
			lastResult = result
		}
	}

	// 全部限流且等待失败
	if allRateLimited && lastErr == nil && lastResult == nil {
		return nil, ErrAllRateLimited
	}

	if lastResult != nil {
		return lastResult, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrAllProvidersFailed
}

// executeTask 执行单个请求并上报健康状态
func (s *FailoverStrategy) executeTask(ctx context.Context, task *Task) (*Result, error) {
	resp, err := s.client.Do(task.ProviderName, task.Request)
	if err != nil {
		s.health.ReportFailure(health.MakeHealthKey(task.ProviderName, task.OutboundProtocol))
		return nil, err
	}

	// 上报健康状态
	if resp.StatusCode >= 500 {
		s.health.ReportFailure(health.MakeHealthKey(task.ProviderName, task.OutboundProtocol))
	} else if resp.StatusCode < 400 {
		s.health.ReportSuccess(health.MakeHealthKey(task.ProviderName, task.OutboundProtocol))
	}

	return s.parseResponse(resp, task.ProviderName, task.UpstreamModel, task.Provider.Protocol)
}

func (s *FailoverStrategy) parseResponse(resp *http.Response, providerName, upstreamModel, protocol string) (*Result, error) {
	return parseResponse(resp, providerName, upstreamModel, protocol)
}
