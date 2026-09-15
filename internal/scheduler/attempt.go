package scheduler

import (
	"context"
	"net/http"
	"time"

	"github.com/Marstheway/oh-my-api/internal/provider"
)

// doWithAttemptBudget 发起单次上游请求，并在「单 attempt 预算」内强制失败，避免一家 hang 拖死整个 failover。
//
// 预算选择：
//   - Stream=true  → prefillTimeout（header + 首 token 共用，Do 阶段只负责 header）
//   - Stream=false → nonStreamTimeout（等 header/生成）
//
// 超时错误刻意映射为 ErrPrefillTimeout / ErrAttemptTimeout，而不是 context.DeadlineExceeded/Canceled，
// 否则 ShouldStopScheduling 会把整单调度停掉，后续候选永远轮不到。
//
// 流式成功路径不会 cancel 请求 context：body 仍要继续读给客户端。
func doWithAttemptBudget(
	ctx context.Context,
	client *provider.Client,
	task *Task,
	prefillTimeout, nonStreamTimeout time.Duration,
) (*http.Response, time.Time, error) {
	attemptStart := time.Now()
	if task == nil {
		return nil, attemptStart, ErrNoTasks
	}
	if client == nil {
		return nil, attemptStart, ErrNoProviderAvailable
	}

	if task.Stream {
		return doStreamAttempt(ctx, client, task, prefillTimeout, attemptStart)
	}
	return doNonStreamAttempt(ctx, client, task, nonStreamTimeout, attemptStart)
}

func doNonStreamAttempt(
	ctx context.Context,
	client *provider.Client,
	task *Task,
	nonStreamTimeout time.Duration,
	attemptStart time.Time,
) (*http.Response, time.Time, error) {
	reqCtx := ctx
	var cancel context.CancelFunc
	if nonStreamTimeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, nonStreamTimeout)
		defer cancel()
	}
	if task.Request != nil {
		task.Request = cloneRequest(task.Request, reqCtx)
	}

	resp, err := client.DoWithMeta(task.ProviderName, requestMeta(*task), task.Request)
	if err != nil {
		// attempt 级超时：父 ctx 仍可用时映射为 ErrAttemptTimeout，让 failover 继续下一家
		if cancel != nil && reqCtx.Err() != nil && ctx.Err() == nil {
			if resp != nil {
				_ = resp.Body.Close()
			}
			return nil, attemptStart, ErrAttemptTimeout
		}
		return nil, attemptStart, err
	}
	return resp, attemptStart, nil
}

func doStreamAttempt(
	ctx context.Context,
	client *provider.Client,
	task *Task,
	prefillTimeout time.Duration,
	attemptStart time.Time,
) (*http.Response, time.Time, error) {
	// prefill<=0：不在 Do 阶段做 header 预算，只依赖后续 probe / 全局 timeout
	if prefillTimeout <= 0 {
		if task.Request != nil {
			task.Request = cloneRequest(task.Request, ctx)
		}
		resp, err := client.DoWithMeta(task.ProviderName, requestMeta(*task), task.Request)
		return resp, attemptStart, err
	}

	// 独立 cancel：仅用于掐断 hang 的 Do；成功拿到 header 后不得 cancel，否则会干掉 SSE body。
	reqCtx, cancel := context.WithCancel(ctx)
	if task.Request != nil {
		task.Request = cloneRequest(task.Request, reqCtx)
	}

	type outcome struct {
		resp *http.Response
		err  error
	}
	ch := make(chan outcome, 1)
	go func() {
		resp, err := client.DoWithMeta(task.ProviderName, requestMeta(*task), task.Request)
		ch <- outcome{resp: resp, err: err}
	}()

	timer := time.NewTimer(prefillTimeout)
	defer timer.Stop()

	select {
	case o := <-ch:
		if o.err != nil {
			cancel()
			return nil, attemptStart, o.err
		}
		// 成功：保留 reqCtx 直至父 ctx 结束（handler 读完 body / 整单超时）
		return o.resp, attemptStart, nil
	case <-timer.C:
		cancel()
		o := <-ch
		if o.resp != nil {
			_ = o.resp.Body.Close()
		}
		if ctx.Err() != nil {
			return nil, attemptStart, ctx.Err()
		}
		return nil, attemptStart, ErrPrefillTimeout
	case <-ctx.Done():
		cancel()
		o := <-ch
		if o.resp != nil {
			_ = o.resp.Body.Close()
		}
		return nil, attemptStart, ctx.Err()
	}
}
