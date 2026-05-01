package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestNewLimiter_Noop(t *testing.T) {
	l := NewLimiter(0)
	if _, ok := l.(*noopLimiter); !ok {
		t.Fatal("QPM <= 0 should return noopLimiter")
	}
}

func TestNewLimiter_TokenBucket(t *testing.T) {
	l := NewLimiter(60)
	if _, ok := l.(*tokenBucketLimiter); !ok {
		t.Fatal("QPM > 0 should return tokenBucketLimiter")
	}
}

func TestNoopLimiter_Allow(t *testing.T) {
	l := NewLimiter(0)
	for i := 0; i < 100; i++ {
		if !l.Allow() {
			t.Fatal("noopLimiter should always allow")
		}
	}
}

func TestNoopLimiter_Wait(t *testing.T) {
	l := NewLimiter(0)
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("noopLimiter.Wait should not block: %v", err)
	}
}

func TestTokenBucketLimiter_Allow(t *testing.T) {
	// QPM=60 → rate=1/s, burst=1
	l := NewLimiter(60)
	if !l.Allow() {
		t.Fatal("first Allow should succeed")
	}
	// 令牌耗尽，第二次应失败
	if l.Allow() {
		t.Fatal("second Allow should fail (no tokens)")
	}
}

func TestTokenBucketLimiter_Wait(t *testing.T) {
	// QPM=60 → rate=1/s
	l := NewLimiter(60)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 消耗令牌
	l.Allow()

	// Wait 应在约 1 秒后获得令牌
	start := time.Now()
	if err := l.Wait(ctx); err != nil {
		t.Fatalf("Wait should succeed: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 500*time.Millisecond {
		t.Fatalf("Wait returned too fast: %v", elapsed)
	}
}

func TestTokenBucketLimiter_Wait_Cancel(t *testing.T) {
	l := NewLimiter(60)
	l.Allow() // 消耗令牌

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := l.Wait(ctx); err == nil {
		t.Fatal("Wait with cancelled context should return error")
	}
}

func TestTokenBucketLimiter_LowQPM(t *testing.T) {
	// QPM=30 → rate=0.5/s, burst=1
	l := NewLimiter(30)
	if !l.Allow() {
		t.Fatal("first Allow should succeed with low QPM")
	}
	if l.Allow() {
		t.Fatal("second Allow should fail with low QPM")
	}
}
