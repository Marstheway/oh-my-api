package ratelimit

import (
	"context"

	"golang.org/x/time/rate"
)

type Limiter interface {
	Allow() bool
	Wait(ctx context.Context) error
}

type tokenBucketLimiter struct {
	limiter *rate.Limiter
}

func NewLimiter(qpm int) Limiter {
	if qpm <= 0 {
		return &noopLimiter{}
	}
	r := rate.Limit(float64(qpm) / 60.0)
	burst := max(1, int(r))
	return &tokenBucketLimiter{
		limiter: rate.NewLimiter(r, burst),
	}
}

func (l *tokenBucketLimiter) Allow() bool {
	return l.limiter.Allow()
}

func (l *tokenBucketLimiter) Wait(ctx context.Context) error {
	return l.limiter.Wait(ctx)
}

type noopLimiter struct{}

func (n *noopLimiter) Allow() bool { return true }

func (n *noopLimiter) Wait(_ context.Context) error { return nil }
