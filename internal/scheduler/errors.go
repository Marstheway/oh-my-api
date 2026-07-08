package scheduler

import "errors"

var (
	ErrAllRateLimited      = errors.New("all providers rate limited")
	ErrAllProvidersFailed  = errors.New("all providers failed")
	ErrNoTasks             = errors.New("no tasks to execute")
	ErrUnknownStrategy     = errors.New("unknown scheduling strategy")
	ErrNoProviderAvailable = errors.New("no provider available")
	ErrPrefillTimeout      = errors.New("prefill timeout: no response within deadline")
)
