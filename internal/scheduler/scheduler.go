package scheduler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

type RateLimitError struct {
	Provider string
	Err      error
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limit wait failed for provider %s: %v", e.Provider, e.Err)
}

func (e *RateLimitError) Unwrap() error {
	return e.Err
}

type Scheduler struct {
	strategies        map[string]Strategy
	ratelimit         *ratelimit.Manager
	client            *provider.Client
	health            *health.Checker
	prefillTimeout    time.Duration
	streamIdleTimeout time.Duration
	nonStreamTimeout  time.Duration
	failoverStrat     *FailoverStrategy
	concurrentStrat   *ConcurrentStrategy
	lbStrat           *LoadBalanceStrategy
	adaptiveStrat     *AdaptiveStrategy
}

func New(rl *ratelimit.Manager, client *provider.Client, h *health.Checker, prefillTimeout, streamIdleTimeout, nonStreamTimeout time.Duration) *Scheduler {
	failover := NewFailoverStrategy(client, rl, h, prefillTimeout, streamIdleTimeout, nonStreamTimeout)
	concurrent := NewConcurrentStrategy(client, rl, h, prefillTimeout, streamIdleTimeout, nonStreamTimeout)
	lb := NewLoadBalanceStrategy(client, rl, h, prefillTimeout, streamIdleTimeout, nonStreamTimeout)
	adaptive := NewAdaptiveStrategy(client, rl, h, prefillTimeout, streamIdleTimeout, nonStreamTimeout)

	s := &Scheduler{
		strategies:        make(map[string]Strategy),
		ratelimit:         rl,
		client:            client,
		health:            h,
		prefillTimeout:    prefillTimeout,
		streamIdleTimeout: streamIdleTimeout,
		nonStreamTimeout:  nonStreamTimeout,
		failoverStrat:     failover,
		concurrentStrat:   concurrent,
		lbStrat:           lb,
		adaptiveStrat:     adaptive,
	}
	s.strategies["concurrent"] = concurrent
	s.strategies["load-balance"] = lb
	s.strategies["failover"] = failover
	s.strategies["adaptive"] = adaptive
	return s
}

func (s *Scheduler) Execute(ctx context.Context, mode string, tasks []Task) (*Result, error) {
	strategy, ok := s.strategies[mode]
	if !ok {
		return nil, ErrUnknownStrategy
	}

	return strategy.Execute(ctx, tasks)
}

// ExecuteWithSticky 执行调度，支持 sticky session（仅 load-balance 模式）。
// groupName 和 sticky 仅在 mode 为 load-balance 时生效，其他模式忽略。
func (s *Scheduler) ExecuteWithSticky(ctx context.Context, mode string, groupName string, sticky *StickyMeta, tasks []Task) (*Result, error) {
	if mode != "load-balance" || sticky == nil || !sticky.Enabled {
		return s.Execute(ctx, mode, tasks)
	}
	return s.lbStrat.ExecuteSticky(ctx, groupName, sticky, tasks)
}

func (s *Scheduler) Do(ctx context.Context, providerName string, req *http.Request) (*http.Response, error) {
	healthKey := health.MakeHealthKey(providerName, "")
	if err := s.ratelimit.Wait(ctx, providerName, "", 0); err != nil {
		s.health.ReportFailure(healthKey)
		return nil, &RateLimitError{Provider: providerName, Err: err}
	}

	resp, err := s.client.Do(providerName, req)

	// 上报健康状态
	if err != nil {
		s.health.ReportFailure(healthKey)
	} else if resp.StatusCode >= 500 {
		s.health.ReportFailure(healthKey)
	} else if resp.StatusCode < 400 {
		s.health.ReportSuccess(healthKey)
	}

	return resp, err
}

func (s *Scheduler) Allow(providerName string) bool {
	return s.ratelimit.Allow(providerName, "", 0)
}

// ReportStreamFailure 上报流式响应中断失败（如上游中途断流、流空闲超时）。
// 连续失败达到 health.Checker 阈值后，对应 provider 会被摘除，后续请求自动换源。
func (s *Scheduler) ReportStreamFailure(providerName, outboundProtocol string) {
	if s == nil || s.health == nil {
		return
	}
	s.health.ReportFailure(health.MakeHealthKey(providerName, outboundProtocol))
}

func IsRateLimitError(err error) bool {
	var rlErr *RateLimitError
	return errors.As(err, &rlErr)
}
