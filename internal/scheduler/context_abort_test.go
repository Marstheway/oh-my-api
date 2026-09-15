package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/health"
)

func TestIsContextAbort(t *testing.T) {
	if IsContextAbort(nil) {
		t.Fatal("nil should not be abort")
	}
	if !IsContextAbort(context.Canceled) {
		t.Fatal("Canceled should be abort")
	}
	if !IsContextAbort(context.DeadlineExceeded) {
		t.Fatal("DeadlineExceeded should be abort")
	}
	if !IsContextAbort(fmt.Errorf("wrap: %w", context.Canceled)) {
		t.Fatal("wrapped Canceled should be abort")
	}
	if IsContextAbort(errors.New("upstream 502")) {
		t.Fatal("generic error should not be abort")
	}
}

func TestShouldStopScheduling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !ShouldStopScheduling(ctx, nil) {
		t.Fatal("done ctx should stop")
	}
	if !ShouldStopScheduling(context.Background(), context.Canceled) {
		t.Fatal("Canceled err should stop")
	}

	dctx, dcancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer dcancel()
	time.Sleep(2 * time.Millisecond)
	if !ShouldStopScheduling(dctx, nil) {
		t.Fatal("deadline ctx should stop")
	}

	if ShouldStopScheduling(context.Background(), errors.New("502 bad gateway")) {
		t.Fatal("retryable err should not stop")
	}
	if ShouldStopScheduling(context.Background(), nil) {
		t.Fatal("live ctx + nil err should not stop")
	}
}

func TestAbortReason(t *testing.T) {
	if AbortReason(context.Background(), context.Canceled) != "client_canceled" {
		t.Fatalf("got %q", AbortReason(context.Background(), context.Canceled))
	}
	if AbortReason(context.Background(), context.DeadlineExceeded) != "request_deadline" {
		t.Fatalf("got %q", AbortReason(context.Background(), context.DeadlineExceeded))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if AbortReason(ctx, nil) != "client_canceled" {
		t.Fatalf("got %q", AbortReason(ctx, nil))
	}
}

func TestEntryAbort(t *testing.T) {
	if err := entryAbort(context.Background(), "test"); err != nil {
		t.Fatalf("live ctx: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := entryAbort(ctx, "test")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("dead ctx expected Canceled, got %v", err)
	}
}

func TestStopSequential(t *testing.T) {
	// live + nil
	stop, err := stopSequential(context.Background(), "test", nil, nil)
	if stop || err != nil {
		t.Fatalf("live: stop=%v err=%v", stop, err)
	}

	// dead ctx, nil fallback
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stop, err = stopSequential(ctx, "test", nil, nil)
	if !stop || !errors.Is(err, context.Canceled) {
		t.Fatalf("dead nil-fallback: stop=%v err=%v", stop, err)
	}

	// dead ctx discards soft body
	bodyClosed := false
	fallback := newSequentialFallback()
	fallback.RecordSoftResult(&Result{
		FailureKind: FailureKindSoft,
		Response: &http.Response{
			Body: &closeProbe{onClose: func() { bodyClosed = true }},
		},
	})
	stop, err = stopSequential(ctx, "test", fallback, context.Canceled)
	if !stop || !errors.Is(err, context.Canceled) {
		t.Fatalf("soft discard: stop=%v err=%v", stop, err)
	}
	if !bodyClosed {
		t.Fatal("expected soft result body closed")
	}
	if fallback.lastSoftResult != nil {
		t.Fatal("soft result should be cleared")
	}
}

func TestAbortErrorPrefersParentAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := abortError(ctx, errors.New("upstream 502"))
	if !errors.Is(got, context.Canceled) {
		t.Fatalf("parent abort should win over hard err, got %v", got)
	}
	if !errors.Is(abortError(context.Background(), context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("concrete abort err should be kept")
	}
}

func TestReportFailureUnlessAbort(t *testing.T) {
	// nil checker is safe
	reportFailureUnlessAbort(nil, "k", errors.New("x"))
	reportFailureUnlessAbort(nil, "k", context.Canceled)

	// real checker: abort errors must not mark unhealthy
	h := health.NewChecker(1, 30*time.Second) // threshold=1 so one failure would flip
	reportFailureUnlessAbort(h, "abort-key", context.Canceled)
	if !h.IsHealthy("abort-key") {
		t.Fatal("context.Canceled must not mark unhealthy")
	}
	reportFailureUnlessAbort(h, "abort-key", context.DeadlineExceeded)
	if !h.IsHealthy("abort-key") {
		t.Fatal("context.DeadlineExceeded must not mark unhealthy")
	}

	// real checker: non-abort errors must be reported
	reportFailureUnlessAbort(h, "fail-key", errors.New("connection refused"))
	if h.IsHealthy("fail-key") {
		t.Fatal("non-abort error must mark unhealthy")
	}
}

func TestFinishRaceAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	canceled := false
	cancelFn := func() { canceled = true; cancel() }

	bodyClosed := false
	res := &Result{
		Response: &http.Response{
			Body: &closeProbe{onClose: func() { bodyClosed = true }},
		},
	}
	err := finishRaceAbort(cancelFn, ctx, context.Canceled, res)
	if !canceled {
		t.Fatal("cancel must be invoked")
	}
	if !bodyClosed {
		t.Fatal("body must be closed")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

type closeProbe struct {
	onClose func()
	closed  bool
}

func (c *closeProbe) Read(p []byte) (int, error) { return 0, io.EOF }
func (c *closeProbe) Close() error {
	if !c.closed {
		c.closed = true
		if c.onClose != nil {
			c.onClose()
		}
	}
	return nil
}
