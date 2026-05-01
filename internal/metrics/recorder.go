package metrics

import (
	"context"
	"strings"
)

// RecordRequest 记录请求埋点
func RecordRequest(ctx context.Context, info RequestInfo) {
	requestTotal.WithLabelValues(
		info.InboundProtocol,
		info.OutboundProtocol,
		info.Provider,
		info.UpstreamModel,
		info.ModelGroup,
		info.KeyName,
		info.Status,
	).Inc()

	requestDuration.WithLabelValues(
		info.Provider,
		info.UpstreamModel,
		info.KeyName,
		info.Status,
	).Observe(info.Duration)
}

// RecordToken 记录 token 消耗
func RecordToken(provider, model, modelGroup, keyName string, input, output int) {
	if input > 0 {
		tokenInputTotal.WithLabelValues(provider, model, modelGroup, keyName).Add(float64(input))
	}
	if output > 0 {
		tokenOutputTotal.WithLabelValues(provider, model, modelGroup, keyName).Add(float64(output))
	}
}

// SetProviderHealth 设置 Provider 健康状态
func SetProviderHealth(provider string, healthy bool) {
	value := float64(0)
	if healthy {
		value = 1
	}
	// Parse health key: provider or provider|outbound_protocol.
	parts := strings.SplitN(provider, "|", 2)
	providerName := parts[0]
	outboundProtocol := ""
	if len(parts) == 2 {
		outboundProtocol = parts[1]
	}

	providerHealthStatus.WithLabelValues(providerName, outboundProtocol).Set(value)
}

// RecordProviderFailure 记录 Provider 失败
func RecordProviderFailure(provider, errorType string) {
	providerRequestFailures.WithLabelValues(provider, errorType).Inc()
}

// RecordRatelimitTriggered 记录限流触发
func RecordRatelimitTriggered(keyName string) {
	ratelimitTriggeredTotal.WithLabelValues(keyName).Inc()
}

// IncConcurrent 增加并发请求计数
func IncConcurrent() {
	concurrentRequests.Inc()
}

// DecConcurrent 减少并发请求计数
func DecConcurrent() {
	concurrentRequests.Dec()
}
