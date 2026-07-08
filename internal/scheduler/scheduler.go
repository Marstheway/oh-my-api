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
	failoverStrat     *FailoverStrategy
	concurrentStrat   *ConcurrentStrategy
	lbStrat           *LoadBalanceStrategy
	adaptiveStrat     *AdaptiveStrategy
}

func New(rl *ratelimit.Manager, client *provider.Client, h *health.Checker, prefillTimeout time.Duration, streamIdleTimeout time.Duration) *Scheduler {
	failover := NewFailoverStrategy(client, rl, h, prefillTimeout, streamIdleTimeout)
	concurrent := NewConcurrentStrategy(client, rl, h, prefillTimeout, streamIdleTimeout)
	lb := NewLoadBalanceStrategy(client, rl, h, prefillTimeout, streamIdleTimeout)
	adaptive := NewAdaptiveStrategy(client, rl, h, prefillTimeout, streamIdleTimeout)

	s := &Scheduler{
		strategies:        make(map[string]Strategy),
		ratelimit:         rl,
		client:            client,
		health:            h,
		prefillTimeout:    prefillTimeout,
		streamIdleTimeout: streamIdleTimeout,
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

func (s *Scheduler) Do(ctx context.Context, providerName string, req *http.Request) (*http.Response, error) {
	healthKey := health.MakeHealthKey(providerName, "")
	if err := s.ratelimit.Wait(ctx, providerName, ""); err != nil {
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
	return s.ratelimit.Allow(providerName, "")
}

func IsRateLimitError(err error) bool {
	var rlErr *RateLimitError
	return errors.As(err, &rlErr)
}
