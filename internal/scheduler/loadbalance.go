package scheduler

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

type LoadBalanceStrategy struct {
	leafRuntime
	randIntn func(int) int
}

func NewLoadBalanceStrategy(client *provider.Client, rl *ratelimit.Manager, h *health.Checker, prefillTimeout, streamIdleTimeout, nonStreamTimeout time.Duration) *LoadBalanceStrategy {
	return &LoadBalanceStrategy{
		leafRuntime: newLeafRuntime("loadbalance", client, rl, h, prefillTimeout, streamIdleTimeout, nonStreamTimeout),
	}
}

func (s *LoadBalanceStrategy) Execute(ctx context.Context, tasks []Task) (*Result, error) {
	return s.execute(ctx, "", nil, tasks)
}

// ExecuteSticky 在 sticky 启用时用 sticky/deficit 选路；否则与 Execute 相同。
// 限流只在 executeTask 内 Allow 一次。
func (s *LoadBalanceStrategy) ExecuteSticky(ctx context.Context, groupName string, sticky *StickyMeta, tasks []Task) (*Result, error) {
	if sticky == nil || !sticky.Enabled {
		return s.execute(ctx, "", nil, tasks)
	}
	return s.execute(ctx, groupName, sticky, tasks)
}

func (s *LoadBalanceStrategy) execute(ctx context.Context, groupName string, sticky *StickyMeta, tasks []Task) (*Result, error) {
	if len(tasks) == 0 {
		return nil, ErrNoTasks
	}
	if err := entryAbort(ctx, "load-balance"); err != nil {
		return nil, err
	}

	now := time.Now().Local()

	if allProvidersDisabledOrUnhealthy(tasks, s.health, now) {
		return nil, ErrNoProviderAvailable
	}

	healthyTasks := s.filterHealthy(tasks, now)
	if len(healthyTasks) == 0 {
		return nil, ErrNoProviderAvailable
	}

	if sticky != nil && sticky.Enabled {
		return s.executeStickyLoop(ctx, groupName, sticky, healthyTasks, now)
	}

	randFn := s.randIntn
	if randFn == nil {
		randFn = rand.Intn
	}
	selector := newWeightedSelector(healthyTasks, randFn)
	fallback := newSequentialFallback()

	for !selector.IsEmpty() {
		if stop, err := stopSequential(ctx, "load-balance", fallback, nil); stop {
			return nil, err
		}

		task := selector.Select()
		if task == nil {
			break
		}
		selectedTask := *task

		slog.Debug("load-balance selected candidate",
			"provider", selectedTask.ProviderName,
			"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
			"weight", selectedTask.Weight,
			"remaining_candidates", selector.Len(),
		)

		result, err := s.executeTask(ctx, &selectedTask)
		if err == nil && result != nil && result.FailureKind == FailureKindSuccess {
			slog.Debug("load-balance request succeeded",
				"provider", selectedTask.ProviderName,
				"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
				"status", result.Response.StatusCode,
			)
			fallback.DiscardSoftResult()
			return result, nil
		}

		selector.RemoveTask(task)
		s.removeUnhealthySiblingTasks(selector, &selectedTask)

		if IsRateLimitError(err) {
			fallback.RecordRateLimit()
			continue
		}

		if err != nil {
			if stop, retErr := stopSequential(ctx, "load-balance", fallback, err,
				"provider", selectedTask.ProviderName,
				"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
			); stop {
				return nil, retErr
			}
			slog.Debug("load-balance request failed, removing candidate",
				"provider", selectedTask.ProviderName,
				"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
				"error", err,
			)
			fallback.RecordHardError(err)
			continue
		}
		if result == nil {
			continue
		}

		if result.FailureKind == FailureKindSoft {
			slog.Debug("content_filter_soft_failure",
				"provider", selectedTask.ProviderName,
				"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
				"reason", result.FailureReason,
			)
			fallback.RecordSoftResult(result)
			continue
		}

		logAttrs := []any{
			"provider", selectedTask.ProviderName,
			"upstream_identity", selectedTask.ProviderName + "/" + selectedTask.UpstreamModel,
			"status", func() int {
				if result.Response != nil {
					return result.Response.StatusCode
				}
				return 0
			}(),
			"reason", result.FailureReason,
		}
		slog.Debug("load-balance request failed, removing candidate", upstreamErrorLogAttrs(logAttrs, result)...)
		fallback.RecordHardResult(result)
	}

	return fallback.Final()
}

// executeStickyLoop 纯叶子 sticky/deficit 选路；与加权路径共用 executeTask（单次 Allow）。
func (s *LoadBalanceStrategy) executeStickyLoop(ctx context.Context, groupName string, sticky *StickyMeta, healthyTasks []Task, now time.Time) (*Result, error) {
	keyName := GetKeyName(ctx)
	store := GetStickyStore()

	remaining := make([]string, 0, len(healthyTasks))
	weights := make(map[string]int, len(healthyTasks))
	taskByKey := make(map[string]Task, len(healthyTasks))
	for _, t := range healthyTasks {
		key := adaptiveCandidateKey(t)
		// 同键后者覆盖：与既有候选身份约定一致
		if _, exists := taskByKey[key]; !exists {
			remaining = append(remaining, key)
		}
		taskByKey[key] = t
		weights[key] = t.Weight
	}

	fallback := newSequentialFallback()

	for len(remaining) > 0 {
		if stop, err := stopSequential(ctx, "load-balance", fallback, nil); stop {
			return nil, err
		}

		// Lookup 用请求开始时刻；成功 Remember 用成功时刻
		picked := pickStickyOrDeficit(store, groupName, keyName, sticky, now, remaining, weights)
		if picked == "" {
			break
		}
		selectedTask, ok := taskByKey[picked]
		if !ok {
			remaining = removeString(remaining, picked)
			continue
		}

		slog.Debug("load-balance sticky/deficit selected candidate",
			"group", groupName,
			"key_name", keyName,
			"provider", selectedTask.ProviderName,
			"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
			"weight", selectedTask.Weight,
			"remaining_candidates", len(remaining),
		)

		result, err := s.executeTask(ctx, &selectedTask)
		if err == nil && result != nil && result.FailureKind == FailureKindSuccess {
			slog.Debug("load-balance request succeeded",
				"provider", selectedTask.ProviderName,
				"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
				"status", result.Response.StatusCode,
			)
			fallback.DiscardSoftResult()
			recordStickySuccess(store, groupName, keyName, picked, sticky.IdleTimeout, time.Now().Local())
			return result, nil
		}

		remaining = removeString(remaining, picked)
		remaining = removeUnhealthySiblingKeys(remaining, taskByKey, s.health, &selectedTask)

		if IsRateLimitError(err) {
			fallback.RecordRateLimit()
			continue
		}

		if err != nil {
			if stop, retErr := stopSequential(ctx, "load-balance", fallback, err,
				"provider", selectedTask.ProviderName,
				"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
			); stop {
				return nil, retErr
			}
			slog.Debug("load-balance request failed, removing candidate",
				"provider", selectedTask.ProviderName,
				"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
				"error", err,
			)
			fallback.RecordHardError(err)
			continue
		}
		if result == nil {
			continue
		}

		if result.FailureKind == FailureKindSoft {
			slog.Debug("content_filter_soft_failure",
				"provider", selectedTask.ProviderName,
				"upstream_identity", selectedTask.ProviderName+"/"+selectedTask.UpstreamModel,
				"reason", result.FailureReason,
			)
			fallback.RecordSoftResult(result)
			continue
		}

		logAttrs := []any{
			"provider", selectedTask.ProviderName,
			"upstream_identity", selectedTask.ProviderName + "/" + selectedTask.UpstreamModel,
			"status", func() int {
				if result.Response != nil {
					return result.Response.StatusCode
				}
				return 0
			}(),
			"reason", result.FailureReason,
		}
		slog.Debug("load-balance request failed, removing candidate", upstreamErrorLogAttrs(logAttrs, result)...)
		fallback.RecordHardResult(result)
	}

	return fallback.Final()
}

func removeString(keys []string, target string) []string {
	out := keys[:0]
	for _, k := range keys {
		if k != target {
			out = append(out, k)
		}
	}
	return out
}

func removeUnhealthySiblingKeys(keys []string, taskByKey map[string]Task, h *health.Checker, task *Task) []string {
	if h == nil || task == nil {
		return keys
	}
	healthKey := health.MakeHealthKey(task.ProviderName, task.OutboundProtocol)
	if h.IsHealthy(healthKey) {
		return keys
	}
	out := keys[:0]
	for _, k := range keys {
		t, ok := taskByKey[k]
		if !ok {
			continue
		}
		if health.MakeHealthKey(t.ProviderName, t.OutboundProtocol) == healthKey {
			continue
		}
		out = append(out, k)
	}
	return out
}

func (s *LoadBalanceStrategy) filterHealthy(tasks []Task, now time.Time) []Task {
	var healthy []Task
	for _, t := range tasks {
		// 注意：这里不能调用 Allow()，否则会为未选中的 provider 也消耗令牌
		if isProviderDisabledAt(t, now) {
			continue
		}
		if !cascadeLeafReady(s.client, t.ProviderName) {
			continue
		}
		healthKey := health.MakeHealthKey(t.ProviderName, t.OutboundProtocol)
		if s.health.IsHealthy(healthKey) {
			healthy = append(healthy, t)
		}
	}

	if len(healthy) != len(tasks) {
		slog.Debug("load-balance filtered unhealthy providers",
			"total", len(tasks),
			"healthy", len(healthy),
		)
	}

	return healthy
}

func (s *LoadBalanceStrategy) removeUnhealthySiblingTasks(selector *WeightedSelector, task *Task) {
	if selector == nil || task == nil {
		return
	}

	healthKey := health.MakeHealthKey(task.ProviderName, task.OutboundProtocol)
	if s.health.IsHealthy(healthKey) {
		return
	}

	selector.RemoveByHealthKey(healthKey)
}

func (s *LoadBalanceStrategy) executeTask(ctx context.Context, task *Task) (*Result, error) {
	// 真正选中后才消耗令牌；限流时直接尝试下一个，避免单个 provider 阻塞整次请求
	if !s.ratelimit.Allow(task.ProviderName, task.UpstreamModel, task.ModelQPM) {
		slog.Warn("provider rate limited, trying next",
			"provider", task.ProviderName,
			"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
		)
		return nil, &RateLimitError{Provider: task.ProviderName, Err: ErrAllRateLimited}
	}

	return s.leafRuntime.executeTask(ctx, task)
}
