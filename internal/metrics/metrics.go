package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// 请求级指标
	requestTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "request_total",
			Help: "请求总数",
		},
		[]string{"inbound_protocol", "outbound_protocol", "provider", "upstream_model", "model_group", "key_name", "status"},
	)

	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "request_duration_seconds",
			Help:    "请求延迟",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
		},
		[]string{"provider", "upstream_model", "key_name", "status"},
	)

	requestFirstTokenSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "request_first_token_seconds",
			Help:    "端到端首个 SSE 事件延迟（仅流式请求）",
			Buckets: []float64{0.05, 0.1, 0.2, 0.3, 0.5, 0.75, 1, 1.5, 2, 3, 5, 8, 12, 20, 30},
		},
		[]string{"provider", "upstream_model", "key_name", "status"},
	)

	tokenInputTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "token_input_total",
			Help: "输入 token 消耗",
		},
		[]string{"provider", "upstream_model", "model_group", "key_name"},
	)

	tokenOutputTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "token_output_total",
			Help: "输出 token 消耗",
		},
		[]string{"provider", "upstream_model", "model_group", "key_name"},
	)

	streamDecodeOutputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stream_decode_output_tokens_total",
			Help: "成功流式请求从首个 SSE 事件至完成期间生成的输出 token 总数",
		},
		[]string{"provider", "upstream_model", "model_group", "key_name"},
	)

	streamDecodeDurationSecondsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stream_decode_duration_seconds_total",
			Help: "成功流式请求从首个 SSE 事件至完成的总耗时",
		},
		[]string{"provider", "upstream_model", "model_group", "key_name"},
	)

	// Provider 级指标
	providerRequestFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "provider_request_failures",
			Help: "Provider 请求失败数",
		},
		[]string{"provider", "error_type"},
	)

	providerHealthStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "provider_health_status",
			Help: "Provider 健康状态 (1=healthy, 0=unhealthy)",
		},
		[]string{"provider", "outbound_protocol"},
	)

	// Attempt 级指标
	providerAttemptTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "provider_attempt_total",
			Help: "Provider 尝试总数",
		},
		[]string{"scheduler", "provider", "upstream_model", "model_group", "outbound_protocol", "result", "failure_reason", "status_code"},
	)

	providerAttemptDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "provider_attempt_duration_seconds",
			Help:    "Provider 尝试延迟",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
		},
		[]string{"scheduler", "provider", "upstream_model", "model_group", "outbound_protocol", "result", "failure_reason", "status_code"},
	)

	streamInterruptedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stream_interrupted_total",
			Help: "流式响应中断次数",
		},
		[]string{"provider", "upstream_model", "outbound_protocol", "reason"},
	)

	// 限流指标
	ratelimitTriggeredTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ratelimit_triggered_total",
			Help: "限流触发次数",
		},
		[]string{"key_name"},
	)

	// 系统级指标
	concurrentRequests = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "concurrent_requests",
			Help: "当前并发请求数",
		},
	)

	// Smart route 指标
	smartRouteDecisionTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "smart_route_decision_total",
			Help: "Smart route 决策计数",
		},
		[]string{"decision_path"}, // rule_reason, rule_scout, need_judge, judge_reason, judge_scout, judge_fallback_to_reason
	)

	registered = false
)

// Init 初始化所有指标，返回 http.Handler 用于 /metrics 端点
func Init() http.Handler {
	if !registered {
		prometheus.MustRegister(
			requestTotal,
			requestDuration,
			requestFirstTokenSeconds,
			tokenInputTotal,
			tokenOutputTotal,
			streamDecodeOutputTokensTotal,
			streamDecodeDurationSecondsTotal,
			providerRequestFailures,
			providerHealthStatus,
			providerAttemptTotal,
			providerAttemptDuration,
			streamInterruptedTotal,
			ratelimitTriggeredTotal,
			concurrentRequests,
			smartRouteDecisionTotal,
		)
		registered = true
	}
	return promhttp.Handler()
}

// ResetForTest 重置所有指标用于测试
func ResetForTest() {
	requestTotal.Reset()
	requestDuration.Reset()
	requestFirstTokenSeconds.Reset()
	tokenInputTotal.Reset()
	tokenOutputTotal.Reset()
	streamDecodeOutputTokensTotal.Reset()
	streamDecodeDurationSecondsTotal.Reset()
	providerRequestFailures.Reset()
	providerHealthStatus.Reset()
	providerAttemptTotal.Reset()
	providerAttemptDuration.Reset()
	streamInterruptedTotal.Reset()
	ratelimitTriggeredTotal.Reset()
	concurrentRequests.Set(0)
	smartRouteDecisionTotal.Reset()
}

// GetProviderAttemptTotal 返回 providerAttemptTotal 指标用于测试
func GetProviderAttemptTotal() *prometheus.CounterVec {
	return providerAttemptTotal
}

// GetStreamInterruptedTotal 返回 streamInterruptedTotal 指标用于测试
func GetStreamInterruptedTotal() *prometheus.CounterVec {
	return streamInterruptedTotal
}

// GetStreamDecodeDurationSecondsTotal returns stream decode duration metrics for tests.
func GetStreamDecodeDurationSecondsTotal() *prometheus.CounterVec {
	return streamDecodeDurationSecondsTotal
}

// GetRequestTotal 返回 requestTotal 指标用于测试
func GetRequestTotal() *prometheus.CounterVec {
	return requestTotal
}

// RecordSmartRouteDecision 记录 smart route 决策
func RecordSmartRouteDecision(decisionPath string) {
	smartRouteDecisionTotal.WithLabelValues(decisionPath).Inc()
}

// GetSmartRouteDecisionTotal 返回 smartRouteDecisionTotal 指标用于测试
func GetSmartRouteDecisionTotal() *prometheus.CounterVec {
	return smartRouteDecisionTotal
}
