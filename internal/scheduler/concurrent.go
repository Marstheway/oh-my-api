package scheduler

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

type ConcurrentStrategy struct {
	leafRuntime
}

func NewConcurrentStrategy(client *provider.Client, rl *ratelimit.Manager, h *health.Checker, prefillTimeout, streamIdleTimeout, nonStreamTimeout time.Duration) *ConcurrentStrategy {
	return &ConcurrentStrategy{
		leafRuntime: newLeafRuntime("concurrent", client, rl, h, prefillTimeout, streamIdleTimeout, nonStreamTimeout),
	}
}

func (s *ConcurrentStrategy) Execute(ctx context.Context, tasks []Task) (*Result, error) {
	if len(tasks) == 0 {
		return nil, ErrNoTasks
	}
	if err := entryAbort(ctx, "concurrent"); err != nil {
		return nil, err
	}

	now := time.Now().Local()

	if len(tasks) == 1 {
		t := tasks[0]
		if isProviderDisabledAt(t, now) {
			return nil, ErrNoProviderAvailable
		}
		if err := s.ratelimit.Wait(ctx, t.ProviderName, t.UpstreamModel, t.ModelQPM); err != nil {
			if stop, retErr := stopSequential(ctx, "concurrent", nil, err); stop {
				return nil, retErr
			}
			s.health.ReportFailure(health.MakeHealthKey(t.ProviderName, t.OutboundProtocol))
			return nil, &RateLimitError{Provider: t.ProviderName, Err: err}
		}
		return s.executeTask(ctx, &t)
	}

	return s.race(ctx, tasks, now)
}

func isStreamResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	return strings.HasPrefix(contentType, "text/event-stream")
}

func closeResultBody(result *Result) {
	if result == nil || result.Response == nil || result.Response.Body == nil {
		return
	}
	result.Response.Body.Close()
}

// raceStops 管理竞速里每个候选的独立 attempt cancel。
// 胜出口径：只 cancel 落败者；胜者的 attemptCtx 必须活到父 ctx 结束，否则 SSE body 会被掐断。
type raceStops struct {
	stops []context.CancelFunc
	keep  int
}

func newRaceStops() raceStops {
	return raceStops{keep: -1}
}

func (r *raceStops) add(stop context.CancelFunc) int {
	r.stops = append(r.stops, stop)
	return len(r.stops) - 1
}

func (r *raceStops) keepIndex(i int) {
	r.keep = i
}

func (r *raceStops) cancelLosers() {
	for i, stop := range r.stops {
		if stop == nil || i == r.keep {
			continue
		}
		stop()
	}
}

func (s *ConcurrentStrategy) race(ctx context.Context, tasks []Task, now time.Time) (*Result, error) {
	// concurrent 不按健康状态过滤——即使 provider 被标记为 unhealthy 也应参与竞速。
	// 仅当全部候选均被禁用时段过滤，且尚未发起任何上游请求时，返回无可用 provider。
	if allProvidersDisabled(tasks, now) {
		return nil, ErrNoProviderAvailable
	}

	var available []Task
	for _, t := range tasks {
		if isProviderDisabledAt(t, now) {
			continue
		}
		if s.ratelimit.Allow(t.ProviderName, t.UpstreamModel, t.ModelQPM) {
			available = append(available, t)
		}
	}
	if len(available) == 0 {
		return nil, ErrAllRateLimited
	}
	if len(available) == 1 {
		t := available[0]
		return s.executeTask(ctx, &t)
	}

	// lost 只用来让落败 goroutine 丢掉多余 body，不能当上游 request 的 parent。
	// 每个候选用独立 attemptCtx（挂在请求 ctx 下）；胜出后只 cancel 落败者。
	lost, markLost := context.WithCancel(ctx)
	defer markLost()
	stops := newRaceStops()
	defer stops.cancelLosers()

	type outcome struct {
		result *Result
		err    error
		idx    int
	}
	ch := make(chan outcome, len(available))

	for _, t := range available {
		attemptCtx, stopAttempt := context.WithCancel(ctx)
		idx := stops.add(stopAttempt)
		go func(task Task, attemptCtx context.Context, idx int) {
			result, err := s.executeTask(attemptCtx, &task)
			// executeTask has already recorded attempt metric, no need to record again
			select {
			case ch <- outcome{result: result, err: err, idx: idx}:
			case <-lost.Done():
				closeResultBody(result)
			}
		}(t, attemptCtx, idx)
	}

	var lastHardResult *Result
	var lastHardErr error
	var lastSoftResult *Result
	lastHardIdx, lastSoftIdx := -1, -1
	remaining := len(available)

	for remaining > 0 {
		select {
		case o := <-ch:
			remaining--

			if o.err == nil && o.result != nil && o.result.FailureKind == FailureKindSuccess {
				stops.keepIndex(o.idx)
				markLost()
				closeResultBody(lastHardResult)
				closeResultBody(lastSoftResult)
				return o.result, nil
			}

			if o.err != nil {
				// Per-worker cancel after a sibling won is not parent abort.
				lastHardErr = o.err
				continue
			}
			if o.result == nil {
				continue
			}

			switch o.result.FailureKind {
			case FailureKindSoft:
				slog.Warn("content_filter_soft_failure",
					"provider", o.result.Winner,
					"upstream_identity", o.result.Winner+"/"+o.result.UpstreamModel,
					"reason", o.result.FailureReason,
				)
				closeResultBody(lastSoftResult)
				lastSoftResult = o.result
				lastSoftIdx = o.idx
			default:
				logAttrs := []any{
					"provider", o.result.Winner,
					"upstream_identity", o.result.Winner + "/" + o.result.UpstreamModel,
					"reason", o.result.FailureReason,
				}
				if o.result.Response != nil {
					logAttrs = append(logAttrs, "status", o.result.Response.StatusCode)
				}
				slog.Warn("concurrent request failed", upstreamErrorLogAttrs(logAttrs, o.result)...)
				closeResultBody(lastHardResult)
				lastHardResult = o.result
				lastHardIdx = o.idx
			}
		case <-ctx.Done():
			return nil, finishRaceAbort(markLost, ctx, lastHardErr, lastHardResult, lastSoftResult)
		}
	}

	if lastHardResult != nil {
		stops.keepIndex(lastHardIdx)
		closeResultBody(lastSoftResult)
		return lastHardResult, nil
	}
	if lastHardErr != nil {
		closeResultBody(lastSoftResult)
		return nil, lastHardErr
	}
	if lastSoftResult != nil {
		stops.keepIndex(lastSoftIdx)
		return lastSoftResult, nil
	}
	return nil, ErrAllProvidersFailed
}

// cloneRequest clones an HTTP request with a new context
func cloneRequest(req *http.Request, ctx context.Context) *http.Request {
	clonedReq := req.Clone(ctx)
	// Preserve the original body if it exists
	if req.Body != nil && req.GetBody != nil {
		body, err := req.GetBody()
		if err == nil {
			clonedReq.Body = body
		}
	}
	return clonedReq
}
