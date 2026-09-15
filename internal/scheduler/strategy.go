package scheduler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
)

// HealthAction 描述需要对 provider 健康状态采取的动作
type HealthAction string

const (
	// HealthActionNone 不改变健康状态
	HealthActionNone HealthAction = ""
	// HealthActionMarkUnhealthy 立即标记 provider 为不健康，使用 CooldownOverride 作为固定冷却时间
	HealthActionMarkUnhealthy HealthAction = "mark_unhealthy"
	// HealthActionMarkUnhealthyQuota 额度类错误：按调度层额度策略做指数退避摘除
	// （base/max 由 applyHealthAction 持有，不经由 CooldownOverride）
	HealthActionMarkUnhealthyQuota HealthAction = "mark_unhealthy_quota"
)

// HealthActionInfo 描述一次 attempt 后需要执行的健康状态动作
type HealthActionInfo struct {
	Action HealthAction
	// CooldownOverride 仅用于 HealthActionMarkUnhealthy 的固定冷却；
	// HealthActionMarkUnhealthyQuota 忽略该字段。
	CooldownOverride time.Duration
}

type Task struct {
	ProviderName     string
	Provider         config.ProviderConfig
	UpstreamModel    string
	ModelGroup       string
	OutboundProtocol string
	Weight           int
	// ModelQPM 是该 Task 命中的 qpm rule 合并值（per-model 限流）；0 = 未设置，无 model 层。
	ModelQPM int
	// EnableTimeRange / DisableTimeRange 是该 Task 命中的时段 rule 合并值。
	// 未命中为 nil；disable 命中任一间区 → 不可调度；enable 非空且当前不在任一窗口 → 不可调度。
	EnableTimeRange  []string
	DisableTimeRange []string
	// Retries 是同一叶子在瞬时失败后的额外尝试次数（0-2）。0 = 不额外重试。
	Retries int
	// Stream 标记该 attempt 是否为流式请求。决定使用 prefill_timeout 还是 non_stream_timeout。
	Stream  bool
	Request *http.Request
}

type Result struct {
	Response         *http.Response
	Winner           string
	UpstreamModel    string
	OutboundProtocol string
	Usage            *UsageInfo
	FailureKind      FailureKind
	FailureReason    string
	HealthActionInfo HealthActionInfo
	// StreamTTFT 流式探测中首次收到 SSE 事件的时间（自单次上游尝试开始计时），
	// 仅流式响应且成功收到首 SSE 事件时有效；零值表示未捕获。
	StreamTTFT time.Duration
	// StreamStartedAt 是首次收到 SSE 事件的绝对时间，用于计算从首事件到流结束的耗时。
	// 零值表示未捕获。
	StreamStartedAt time.Time
}

func responseProtocol(task Task) string {
	if task.OutboundProtocol != "" {
		return task.OutboundProtocol
	}
	if len(task.Provider.Protocols) > 0 {
		return task.Provider.Protocols[0]
	}
	return ""
}

func requestMeta(task Task) provider.RequestMeta {
	return provider.RequestMeta{
		UpstreamModel:    task.UpstreamModel,
		OutboundProtocol: responseProtocol(task),
	}
}

type UsageInfo struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	FinishReason     string
}

type Strategy interface {
	Execute(ctx context.Context, tasks []Task) (*Result, error)
}

// isProviderDisabledAt 返回 task 在当前时间点是否被时段 rule 排除。
// disable_time_range 命中任一间区 → 排除；enable_time_range 非空且当前不在任一窗口 → 排除。
// 任一时段字符串解析失败 → fail-closed 排除（该 leaf 不可调度），禁止当作全天可用。
func isProviderDisabledAt(task Task, now time.Time) bool {
	if len(task.DisableTimeRange) > 0 {
		ranges, err := parseTaskTimeRanges(task.DisableTimeRange)
		if err != nil {
			slog.Error("disable_time_range parse failed; leaf treated as unschedulable",
				"provider", task.ProviderName,
				"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
				"error", err.Error(),
			)
			return true
		}
		if config.IsInDisabledTimeRange(now, ranges) {
			return true
		}
	}
	if len(task.EnableTimeRange) > 0 {
		ranges, err := parseTaskTimeRanges(task.EnableTimeRange)
		if err != nil {
			slog.Error("enable_time_range parse failed; leaf treated as unschedulable",
				"provider", task.ProviderName,
				"upstream_identity", task.ProviderName+"/"+task.UpstreamModel,
				"error", err.Error(),
			)
			return true
		}
		if !config.IsInDisabledTimeRange(now, ranges) {
			return true
		}
	}
	return false
}

func parseTaskTimeRanges(raws []string) ([]config.DisabledTimeRange, error) {
	ranges := make([]config.DisabledTimeRange, 0, len(raws))
	for _, raw := range raws {
		r, err := config.ParseTimeRange(raw)
		if err != nil {
			return nil, err
		}
		ranges = append(ranges, r)
	}
	return ranges, nil
}

// allProvidersDisabledOrUnhealthy 返回所有候选是否均不可用（禁用时段 + 健康过滤），
// 且尚未发起任何上游请求。
func allProvidersDisabledOrUnhealthy(tasks []Task, h *health.Checker, now time.Time) bool {
	if len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		if isProviderDisabledAt(t, now) {
			continue
		}
		healthKey := health.MakeHealthKey(t.ProviderName, t.OutboundProtocol)
		if !h.IsHealthy(healthKey) {
			continue
		}
		return false // 至少有一个可用候选
	}
	return true
}

// allProvidersDisabled 返回所有候选是否均处于当前禁用时段。
// 与 allProvidersDisabledOrUnhealthy 不同，此函数不检查健康状态，
// 适用于 failover 和 concurrent 策略（两者都会尝试不健康的 provider）。
func allProvidersDisabled(tasks []Task, now time.Time) bool {
	if len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		if !isProviderDisabledAt(t, now) {
			return false
		}
	}
	return true
}
