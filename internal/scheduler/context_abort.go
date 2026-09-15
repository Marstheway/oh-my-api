package scheduler

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Marstheway/oh-my-api/internal/health"
)

type ctxKey int

const keyNameKey ctxKey = iota

// WithKeyName 将 key_name 存入 context，供 scheduler 读取。
func WithKeyName(ctx context.Context, keyName string) context.Context {
	return context.WithValue(ctx, keyNameKey, keyName)
}

// GetKeyName 从 context 中读取 key_name，若不存在返回空字符串。
func GetKeyName(ctx context.Context) string {
	if v := ctx.Value(keyNameKey); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// IsContextAbort reports whether err indicates the request context is no longer
// usable for further upstream attempts: client disconnect (Canceled) or total
// request budget exhausted (DeadlineExceeded).
func IsContextAbort(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// AbortReason returns a stable log/metric reason for a dead context or abort err.
// Prefer err when set; otherwise inspect ctx.Err().
func AbortReason(ctx context.Context, err error) string {
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "request_deadline"
		}
		if errors.Is(err, context.Canceled) {
			return "client_canceled"
		}
	}
	if ctx != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return "request_deadline"
		case errors.Is(ctx.Err(), context.Canceled):
			return "client_canceled"
		}
	}
	return "context_abort"
}

// ShouldStopScheduling reports whether any scheduler mode must not start or
// continue further candidate work (failover, load-balance, adaptive sequential
// loops, or concurrent race aggregation after parent abort).
//
// Stop when:
//   - err is Canceled or DeadlineExceeded, or
//   - ctx is already done (Canceled or DeadlineExceeded).
//
// With the current request-binding model every leaf uses the same parent ctx, so
// continuing after ctx is dead only produces more cancel/deadline noise.
func ShouldStopScheduling(ctx context.Context, err error) bool {
	if IsContextAbort(err) {
		return true
	}
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	return false
}

// abortError prefers a concrete abort err, else ctx.Err().
// When parent is dead, incidental non-abort hard errors lose to the abort.
// All callers guard with ShouldStopScheduling/entryAbort, so the default return
// is unreachable in practice; context.Canceled is a safe fallback.
func abortError(ctx context.Context, err error) error {
	if IsContextAbort(err) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	return context.Canceled
}

// entryAbort gates mode/public entry: if ctx is already dead, log once and
// return the abort error; otherwise return nil.
func entryAbort(ctx context.Context, stage string, extra ...any) error {
	if !ShouldStopScheduling(ctx, nil) {
		return nil
	}
	logSchedulerAbort(stage, ctx, ctx.Err(), extra...)
	return abortError(ctx, ctx.Err())
}

// stopSequential is the single control point for sequential attempt loops
// (failover / load-balance / adaptive, including deferred and rate-limit wait).
//
// When scheduling must stop: discard soft fallback (if any), log once, return
// (true, abortErr). Otherwise return (false, nil).
// fallback may be nil.
func stopSequential(ctx context.Context, stage string, fallback *sequentialFallback, err error, extra ...any) (stop bool, retErr error) {
	if !ShouldStopScheduling(ctx, err) {
		return false, nil
	}
	if fallback != nil {
		fallback.DiscardSoftResult()
	}
	logSchedulerAbort(stage, ctx, err, extra...)
	return true, abortError(ctx, err)
}

// reportFailureUnlessAbort reports provider health failure unless err is a
// context abort (client cancel, request deadline, or race-loser cancel).
func reportFailureUnlessAbort(h *health.Checker, key string, err error) {
	if h == nil || IsContextAbort(err) {
		return
	}
	h.ReportFailure(key)
}

// finishRaceAbort cancels sibling racers, closes any held response bodies,
// logs once, and returns the abort error. Used by concurrent aggregation only.
func finishRaceAbort(cancel context.CancelFunc, ctx context.Context, err error, bodies ...*Result) error {
	if cancel != nil {
		cancel()
	}
	for _, b := range bodies {
		closeResultBody(b)
	}
	logSchedulerAbort("concurrent", ctx, err)
	return abortError(ctx, err)
}

// logSchedulerAbort emits a single structured abort line (not "trying next").
func logSchedulerAbort(stage string, ctx context.Context, err error, extra ...any) {
	attrs := []any{
		"stage", stage,
		"reason", AbortReason(ctx, err),
	}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	} else if ctx != nil && ctx.Err() != nil {
		attrs = append(attrs, "error", ctx.Err().Error())
	}
	attrs = append(attrs, extra...)
	slog.Info("scheduler abort", attrs...)
}
