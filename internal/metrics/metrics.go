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
)

// Init 初始化所有指标，返回 http.Handler 用于 /metrics 端点
func Init() http.Handler {
	prometheus.MustRegister(
		requestTotal,
		requestDuration,
		tokenInputTotal,
		tokenOutputTotal,
		providerRequestFailures,
		providerHealthStatus,
		ratelimitTriggeredTotal,
		concurrentRequests,
	)
	return promhttp.Handler()
}
