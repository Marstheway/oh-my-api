package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

// AdaptiveStrategy 根据 TTFT 延迟统计排序候选，按顺序尝试执行。
type AdaptiveStrategy struct {
	client            *provider.Client
	ratelimit         *ratelimit.Manager
	health            *health.Checker
	prefillTimeout    time.Duration
	streamIdleTimeout time.Duration
}

// NewAdaptiveStrategy 创建新的 AdaptiveStrategy。
func NewAdaptiveStrategy(client *provider.Client, rl *ratelimit.Manager, h *health.Checker, prefillTimeout time.Duration, streamIdleTimeout time.Duration) *AdaptiveStrategy {
	return &AdaptiveStrategy{
		client:            client,
		ratelimit:         rl,
		health:            h,
		prefillTimeout:    prefillTimeout,
		streamIdleTimeout: streamIdleTimeout,
	}
}

// Execute 实现 Strategy 接口。按 TTFT score 排序候选，顺序尝试。
func (s *AdaptiveStrategy) Execute(ctx context.Context, tasks []Task) (*Result, error) {
	if len(tasks) == 0 {
		return nil, ErrNoTasks
	}

	now := time.Now().Local()

	if allProvidersDisabledOrUnhealthy(tasks, s.health, now) {
		return nil, ErrNoProviderAvailable
	}

	healthyTasks := s.filterHealthy(tasks, now)
	if len(healthyTasks) == 0 {
		return nil, ErrNoProviderAvailable
	}

	// 只剩一个候选，直接执行
	if len(healthyTasks) == 1 {
		task := &healthyTasks[0]
		key := adaptiveCandidateKey(*task)
		GetLatencyTracker().RecordSelection(key, now)
		result, err := s.executeTask(ctx, task)
		if err == nil && result != nil && result.FailureKind == FailureKindSuccess {
			return result, nil
		}
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	// 读取统计并排序
	keys := make([]string, len(healthyTasks))
	for i, t := range healthyTasks {
		keys[i] = adaptiveCandidateKey(t)
	}
	stats := GetLatencyTracker().GetStats(keys, now)
	scores := RankCandidates(stats, now)

	// 记录强制纠偏日志
	logForcedCorrection(scores)

	// 按 score 顺序映射回 task
	orderedTasks := s.reorderByScores(healthyTasks, scores)

	// 顺序尝试（索引循环，支持 removeUnhealthyFromRemaining 在迭代中收缩 slice）
	fallback := newSequentialFallback()
	for i := 0; i < len(orderedTasks); i++ {
		task := orderedTasks[i]

		if ctx.Err() != nil {
			fallback.DiscardSoftResult()
			return nil, ctx.Err()
		}

		key := adaptiveCandidateKey(task)
		GetLatencyTracker().RecordSelection(key, time.Now())

		sc := findScoreByKey(scores, key)
		logAdaptiveSelected(task, sc, i, len(orderedTasks))

		result, err := s.executeTask(ctx, &task)
		if err == nil && result != nil && result.FailureKind == FailureKindSuccess {
			slog.Debug("adaptive request succeeded",
				"upstream_model", upstreamModelLabel(task),
				"model_group", task.ModelGroup,
				"rank_position", fmt.Sprintf("%d/%d", i+1, len(orderedTasks)),
				"status", result.Response.StatusCode,
			)
			fallback.DiscardSoftResult()
			return result, nil
		}

		// 移除同 health key 的不健康兄弟候选（当前 task 自身会保留，
		// 已处理完毕不影响后续迭代）
		s.removeUnhealthyFromRemaining(&orderedTasks, &task, key)

		if IsRateLimitError(err) {
			fallback.RecordRateLimit()
			continue
		}

		if err != nil {
			slog.Debug("adaptive request failed, trying next",
				"upstream_model", upstreamModelLabel(task),
				"model_group", task.ModelGroup,
				"rank_position", fmt.Sprintf("%d/%d", i+1, len(orderedTasks)),
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
				"upstream_model", upstreamModelLabel(task),
				"model_group", task.ModelGroup,
				"reason", result.FailureReason,
			)
			fallback.RecordSoftResult(result)
			continue
		}

		failureReason := result.FailureReason
		if failureReason == "" && result.Response != nil && result.Response.StatusCode >= http.StatusBadRequest {
			failureReason = summarizeUpstreamError(result.Response, 120)
		}

		slog.Debug("adaptive request failed, trying next",
			"upstream_model", upstreamModelLabel(task),
			"model_group", task.ModelGroup,
			"rank_position", fmt.Sprintf("%d/%d", i+1, len(orderedTasks)),
			"status", func() int {
				if result.Response != nil {
					return result.Response.StatusCode
				}
				return 0
			}(),
			"reason", failureReason,
		)
		fallback.RecordHardResult(result)
	}

	return fallback.Final()
}

// filterHealthy 复用与 LoadBalanceStrategy 相同的健康/禁用过滤规则。
func (s *AdaptiveStrategy) filterHealthy(tasks []Task, now time.Time) []Task {
	var healthy []Task
	for _, t := range tasks {
		if isProviderDisabledAt(t, now) {
			continue
		}
		healthKey := health.MakeHealthKey(t.ProviderName, t.OutboundProtocol)
		if s.health.IsHealthy(healthKey) {
			healthy = append(healthy, t)
		}
	}

	if len(healthy) != len(tasks) {
		slog.Debug("adaptive filtered unhealthy providers",
			"total", len(tasks),
			"healthy", len(healthy),
			"model_group", func() string {
				if len(tasks) > 0 {
					return tasks[0].ModelGroup
				}
				return ""
			}(),
		)
	}

	return healthy
}

// reorderByScores 将 healthyTasks 按 scores 的顺序重新排列。
func (s *AdaptiveStrategy) reorderByScores(healthyTasks []Task, scores []CandidateScore) []Task {
	scoreMap := make(map[string]int, len(scores))
	for i, sc := range scores {
		scoreMap[sc.Key] = i
	}

	result := make([]Task, 0, len(healthyTasks))
	used := make(map[string]bool, len(healthyTasks))

	// 先按 score 顺序加入
	for _, sc := range scores {
		for _, t := range healthyTasks {
			key := adaptiveCandidateKey(t)
			if used[key] {
				continue
			}
			if key == sc.Key {
				result = append(result, t)
				used[key] = true
				break
			}
		}
	}

	// 兜底：加入所有未被 score 覆盖的 healthy task（保留原顺序）
	for _, t := range healthyTasks {
		key := adaptiveCandidateKey(t)
		if !used[key] {
			result = append(result, t)
		}
	}

	return result
}

// removeUnhealthyFromRemaining 若当前 task 在执行后被标记为 unhealthy，
// 则从后续候选中移除同一 health key 的兄弟候选（不含当前 task 自身）。
func (s *AdaptiveStrategy) removeUnhealthyFromRemaining(tasks *[]Task, current *Task, currentKey string) {
	if tasks == nil || current == nil {
		return
	}
	healthKey := health.MakeHealthKey(current.ProviderName, current.OutboundProtocol)
	if s.health.IsHealthy(healthKey) {
		return
	}

	filtered := (*tasks)[:0]
	for _, t := range *tasks {
		key := adaptiveCandidateKey(t)
		if key == currentKey {
			filtered = append(filtered, t)
			continue // 当前 task 保留，只移除兄弟
		}
		candidateHealthKey := health.MakeHealthKey(t.ProviderName, t.OutboundProtocol)
		if candidateHealthKey == healthKey {
			continue // 同 health key 的兄弟移除
		}
		filtered = append(filtered, t)
	}
	*tasks = filtered
}

// executeTask 执行单个请求，复用与 LoadBalanceStrategy 一致的限流/健康/指标上报逻辑。
func (s *AdaptiveStrategy) executeTask(ctx context.Context, task *Task) (*Result, error) {
	start := time.Now()
	if !s.ratelimit.Allow(task.ProviderName, task.UpstreamModel) {
		slog.Warn("provider rate limited, trying next",
			"upstream_model", upstreamModelLabel(*task),
			"model_group", task.ModelGroup,
		)
		return nil, &RateLimitError{Provider: task.ProviderName, Err: ErrAllRateLimited}
	}

	resp, err := s.client.Do(task.ProviderName, task.Request)
	if err != nil {
		recordAttemptMetric("adaptive", *task, nil, err, time.Since(start))
		s.health.ReportFailure(health.MakeHealthKey(task.ProviderName, task.OutboundProtocol))
		return nil, err
	}

	healthKey := health.MakeHealthKey(task.ProviderName, task.OutboundProtocol)
	result, err := s.parseResponse(resp, task.ProviderName, task.UpstreamModel, responseProtocol(*task), s.prefillTimeout, s.streamIdleTimeout)
	if err != nil {
		recordAttemptMetric("adaptive", *task, nil, err, time.Since(start))
		s.health.ReportFailure(healthKey)
		return nil, err
	}

	// 统一应用 TokenHub 错误码分类（仅对硬失败重分级）
	applyTokenHubClassification(result, resp, task.Request)

	recordAttemptMetric("adaptive", *task, result, nil, time.Since(start))

	applyHealthAction(s.health, healthKey, result)
	if result.HealthActionInfo.Action != HealthActionNone {
		return result, nil
	}

	switch result.FailureKind {
	case FailureKindSuccess:
		s.health.ReportSuccess(healthKey)
	case FailureKindHard:
		if result.Response != nil && result.Response.StatusCode >= http.StatusInternalServerError {
			s.health.ReportFailure(healthKey)
		}
	}

	return result, nil
}

func (s *AdaptiveStrategy) parseResponse(resp *http.Response, providerName, upstreamModel, protocol string, prefillTimeout time.Duration, streamIdleTimeout time.Duration) (*Result, error) {
	return parseResponse(resp, providerName, upstreamModel, protocol, prefillTimeout, streamIdleTimeout)
}

// upstreamModelLabel 返回 provider/upstream_model 统一格式标签。
func upstreamModelLabel(task Task) string {
	return task.ProviderName + "/" + task.UpstreamModel
}

// findScoreByKey 在排序结果中查找指定 key 的 score 信息。
func findScoreByKey(scores []CandidateScore, key string) *CandidateScore {
	for i := range scores {
		if scores[i].Key == key {
			return &scores[i]
		}
	}
	return nil
}

// logAdaptiveSelected 记录候选选中日志，包含 score 分解。
func logAdaptiveSelected(task Task, sc *CandidateScore, rank, total int) {
	attrs := []any{
		"upstream_model", upstreamModelLabel(task),
		"model_group", task.ModelGroup,
		"rank_position", fmt.Sprintf("%d/%d", rank+1, total),
	}
	if sc != nil {
		attrs = append(attrs,
			"score", sc.Score,
			"mean_ttft", sc.Stats.MeanTTFT,
			"sample_count", sc.Stats.SampleCount,
		)
		// 计算 sample_penalty 和 stale_penalty
		n := sc.Stats.SampleCount
		if n > 0 {
			samplePenalty := 300.0 / math.Min(float64(n), 5)
			attrs = append(attrs, "sample_penalty", samplePenalty)
		}
		if !sc.Stats.LastSuccessAt.IsZero() {
			staleMinutes := time.Since(sc.Stats.LastSuccessAt).Minutes()
			stalePenalty := math.Min(300, 15*math.Floor(staleMinutes))
			if stalePenalty > 0 {
				attrs = append(attrs, "stale_penalty", stalePenalty)
			}
		}
	}
	slog.Debug("adaptive selected candidate", attrs...)
}

// logForcedCorrection 若当前排序命中强制纠偏，记录信息日志。
func logForcedCorrection(scores []CandidateScore) {
	if len(scores) == 0 {
		return
	}
	first := scores[0]
	// 强制纠偏的判定条件：该候选 LastSelectedAt 距今超过阈值
	// 注意：零值 LastSelectedAt 也视为纠偏（从未被调度）
	now := time.Now()
	if first.Stats.LastSelectedAt.IsZero() {
		slog.Info("adaptive force-corrected: never selected",
			"upstream_model", first.Key,
			"sample_count", first.Stats.SampleCount,
		)
		return
	}
	if now.Sub(first.Stats.LastSelectedAt) >= 30*time.Minute {
		slog.Info("adaptive force-corrected: long-idle candidate promoted",
			"upstream_model", first.Key,
			"since_last_selected", now.Sub(first.Stats.LastSelectedAt).Round(time.Minute),
			"sample_count", first.Stats.SampleCount,
		)
	}
}
