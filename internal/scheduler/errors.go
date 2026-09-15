package scheduler

import "errors"

var (
	ErrAllRateLimited      = errors.New("all providers rate limited")
	ErrAllProvidersFailed  = errors.New("all providers failed")
	ErrNoTasks             = errors.New("no tasks to execute")
	ErrUnknownStrategy     = errors.New("unknown scheduling strategy")
	ErrNoProviderAvailable = errors.New("no provider available")
	ErrPrefillTimeout      = errors.New("prefill timeout: no response within deadline")
	// ErrAttemptTimeout 非流式单 attempt 预算耗尽（等响应头/生成超时）。
	// 刻意不是 context.DeadlineExceeded，避免 ShouldStopScheduling 把整单 failover 停掉。
	ErrAttemptTimeout = errors.New("attempt timeout: upstream did not respond within deadline")
)
