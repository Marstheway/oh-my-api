package scheduler

import (
	"context"
	"net/http"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/health"
)

// HealthAction 描述需要对 provider 健康状态采取的动作
type HealthAction string

const (
	// HealthActionNone 不改变健康状态
	HealthActionNone HealthAction = ""
	// HealthActionMarkUnhealthy 立即标记 provider 为不健康，使用自定义冷却时间
	HealthActionMarkUnhealthy HealthAction = "mark_unhealthy"
)

// HealthActionInfo 描述一次 attempt 后需要执行的健康状态动作
type HealthActionInfo struct {
	Action           HealthAction
	CooldownOverride time.Duration
}

type Task struct {
	ProviderName     string
	Provider         config.ProviderConfig
	UpstreamModel    string
	ModelGroup       string
	OutboundProtocol string
	Weight           int
	Request          *http.Request
}

type Result struct {
	Response         *http.Response
	Winner           string
	UpstreamModel    string
	Usage            *UsageInfo
	FailureKind      FailureKind
	FailureReason    string
	HealthActionInfo HealthActionInfo
	// StreamTTFT 流式探测中首次收到 SSE 事件的时间（自 probe 开始计时），
	// 仅流式响应且成功收到首 token 时有效；零值表示未捕获。
	StreamTTFT time.Duration
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

type UsageInfo struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	FinishReason     string
}

type Strategy interface {
	Execute(ctx context.Context, tasks []Task) (*Result, error)
}

// isProviderDisabledAt 返回 provider 在指定时间点是否处于禁用时段。
func isProviderDisabledAt(task Task, now time.Time) bool {
	return task.Provider.IsDisabledAt(now)
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
