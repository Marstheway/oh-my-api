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
	strategies map[string]Strategy
	ratelimit  *ratelimit.Manager
	client     *provider.Client
	health     *health.Checker
}

func New(rl *ratelimit.Manager, client *provider.Client, h *health.Checker) *Scheduler {
	s := &Scheduler{
		strategies: make(map[string]Strategy),
		ratelimit:  rl,
		client:     client,
		health:     h,
	}
	s.strategies["concurrent"] = NewConcurrentStrategy(client, rl, h)
	s.strategies["load-balance"] = NewLoadBalanceStrategy(client, rl, h)
	s.strategies["failover"] = NewFailoverStrategy(client, rl, h)
	return s
}

func (s *Scheduler) Execute(ctx context.Context, mode string, timeout time.Duration, tasks []Task) (*Result, error) {
	strategy, ok := s.strategies[mode]
	if !ok {
		return nil, ErrUnknownStrategy
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

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
