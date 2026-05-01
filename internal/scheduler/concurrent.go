package scheduler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

type ConcurrentStrategy struct {
	client    *provider.Client
	ratelimit *ratelimit.Manager
	health    *health.Checker
}

func NewConcurrentStrategy(client *provider.Client, rl *ratelimit.Manager, h *health.Checker) *ConcurrentStrategy {
	return &ConcurrentStrategy{
		client:    client,
		ratelimit: rl,
		health:    h,
	}
}

func (s *ConcurrentStrategy) Execute(ctx context.Context, tasks []Task) (*Result, error) {
	if len(tasks) == 0 {
		return nil, ErrNoTasks
	}

	if len(tasks) == 1 {
		t := tasks[0]
		if err := s.ratelimit.Wait(ctx, t.ProviderName, t.UpstreamModel); err != nil {
			s.health.ReportFailure(health.MakeHealthKey(t.ProviderName, t.OutboundProtocol))
			return nil, &RateLimitError{Provider: t.ProviderName, Err: err}
		}
		resp, err := s.client.Do(t.ProviderName, t.Request)
		if err != nil {
			s.health.ReportFailure(health.MakeHealthKey(t.ProviderName, t.OutboundProtocol))
			return nil, err
		}
		// 上报健康状态
		if resp.StatusCode >= 500 {
			s.health.ReportFailure(health.MakeHealthKey(t.ProviderName, t.OutboundProtocol))
		} else if resp.StatusCode < 400 {
			s.health.ReportSuccess(health.MakeHealthKey(t.ProviderName, t.OutboundProtocol))
		}
		return s.parseResponse(resp, t.ProviderName, t.UpstreamModel, t.Provider.Protocol)
	}

	return s.race(ctx, tasks)
}

func (s *ConcurrentStrategy) parseResponse(resp *http.Response, providerName, upstreamModel, protocol string) (*Result, error) {
	return parseResponse(resp, providerName, upstreamModel, protocol)
}

// parseResponse 解析 HTTP 响应，提取 usage 信息（包级函数，供其他策略复用）
func parseResponse(resp *http.Response, providerName, upstreamModel, protocol string) (*Result, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	resp.Body.Close()

	resp.Body = io.NopCloser(strings.NewReader(string(body)))

	result := &Result{
		Response:      resp,
		Winner:        providerName,
		UpstreamModel: upstreamModel,
	}

	if resp.StatusCode >= 400 {
		return result, nil
	}

	var usage *UsageInfo

	if strings.Contains(protocol, "anthropic") {
		var claudeResp dto.ClaudeResponse
		if err := json.Unmarshal(body, &claudeResp); err == nil {
			usage = &UsageInfo{
				PromptTokens:     claudeResp.Usage.InputTokens,
				CompletionTokens: claudeResp.Usage.OutputTokens,
				TotalTokens:      claudeResp.Usage.InputTokens + claudeResp.Usage.OutputTokens,
			}
			if claudeResp.StopReason != nil {
				usage.FinishReason = *claudeResp.StopReason
			}
		}
	} else {
		var openaiResp dto.ChatCompletionResponse
		if err := json.Unmarshal(body, &openaiResp); err == nil {
			usage = &UsageInfo{
				PromptTokens:     openaiResp.Usage.PromptTokens,
				CompletionTokens: openaiResp.Usage.CompletionTokens,
				TotalTokens:      openaiResp.Usage.TotalTokens,
			}
			if len(openaiResp.Choices) > 0 && openaiResp.Choices[0].FinishReason != nil {
				usage.FinishReason = *openaiResp.Choices[0].FinishReason
			}
		}
	}

	result.Usage = usage
	return result, nil
}

func (s *ConcurrentStrategy) race(ctx context.Context, tasks []Task) (*Result, error) {
	var available []Task
	for _, t := range tasks {
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
		resp    *http.Response
		err     error
		taskIdx int
	}
	ch := make(chan outcome, len(available))

	for i, t := range available {
		go func(idx int, task Task) {
			resp, err := s.client.Do(task.ProviderName, task.Request)
			select {
			case ch <- outcome{resp: resp, err: err, taskIdx: idx}:
			case <-raceCtx.Done():
				if resp != nil {
					resp.Body.Close()
				}
			}
		}(i, t)
	}

	var firstErr error
	var firstErrResp *http.Response
	var firstErrTaskIdx int
	remaining := len(available)

	for remaining > 0 {
		select {
		case o := <-ch:
			remaining--
			task := available[o.taskIdx]
			// 上报健康状态
			healthKey := health.MakeHealthKey(task.ProviderName, task.OutboundProtocol)
			if o.err != nil {
				s.health.ReportFailure(healthKey)
			} else if o.resp.StatusCode >= 500 {
				s.health.ReportFailure(healthKey)
			} else if o.resp.StatusCode < 400 {
				s.health.ReportSuccess(healthKey)
			}

			if o.err == nil && o.resp.StatusCode < 400 {
				cancel()
				return s.parseResponse(o.resp, task.ProviderName, task.UpstreamModel, task.Provider.Protocol)
			}
			if o.err != nil {
				firstErr = o.err
			}
			if o.resp != nil {
				if firstErrResp == nil {
					firstErrResp = o.resp
					firstErrTaskIdx = o.taskIdx
				} else {
					o.resp.Body.Close()
				}
			}
		case <-ctx.Done():
			cancel()
			if firstErr != nil {
				return nil, firstErr
			}
			return nil, ctx.Err()
		}
	}

	if firstErrResp != nil {
		task := available[firstErrTaskIdx]
		return s.parseResponse(firstErrResp, "", task.UpstreamModel, task.Provider.Protocol)
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, ErrAllProvidersFailed
}
