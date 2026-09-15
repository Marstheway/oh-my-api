package handler

import (
	"context"
	"log/slog"
	"time"

	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/gin-gonic/gin"
)

// recordStreamDecodeMetrics records output token and elapsed-time metrics from the first SSE event to stream completion.
func recordStreamDecodeMetrics(c *gin.Context, stream bool, modelGroup, provider, upstreamModel string, outputTokens int, streamStartedAt time.Time) {
	if !stream || streamStartedAt.IsZero() {
		return
	}

	keyName := c.GetString("key_name")
	if keyName == "" {
		return
	}

	metrics.RecordStreamDecode(provider, upstreamModel, modelGroup, keyName, outputTokens, time.Since(streamStartedAt).Seconds())
}

func recordStats(c *gin.Context, userModel, providerName, upstreamModel string, inputTokens, outputTokens int, latency time.Duration) {
	recordStatsForKey(c.GetString("key_name"), userModel, providerName, upstreamModel, inputTokens, outputTokens, latency)
}

// recordStatsForKey 与 recordStats 相同，但 key 名由调用方给出：cascade job 在 Spoke 侧
// 没有 gin.Context，使用 peer 维度的合成 key。
func recordStatsForKey(keyName, userModel, providerName, upstreamModel string, inputTokens, outputTokens int, latency time.Duration) {
	if keyName == "" {
		return
	}

	if err := stats.GetRecorder().Record(
		keyName,
		providerName,
		upstreamModel,
		inputTokens,
		outputTokens,
		latency.Milliseconds(),
	); err != nil {
		slog.Warn("failed to record stats", "error", err)
	}

	if userModel != "" {
		if err := stats.GetRecorder().RecordUserModel(userModel, inputTokens, outputTokens, latency.Milliseconds()); err != nil {
			slog.Warn("failed to record user model stats", "error", err)
		}
	}

	metrics.RecordToken(providerName, upstreamModel, "", keyName, inputTokens, outputTokens)
}

// recordRequestMetrics records request-level metrics.
// outboundProtocol 应使用 winner 物化时的出站协议；空字符串时不写入 outbound 标签。
func recordRequestMetrics(c *gin.Context, inboundProtocol, modelGroup, provider, upstreamModel, outboundProtocol, status string, latency, ttft time.Duration) {
	recordRequestMetricsForKey(c.GetString("key_name"), inboundProtocol, modelGroup, provider, upstreamModel, outboundProtocol, status, latency, ttft)
}

// recordRequestMetricsForKey 与 recordRequestMetrics 相同，但 key 名由调用方给出。
// outboundProtocol 应使用 winner 物化时的出站协议；空字符串时不写入 outbound 标签。
func recordRequestMetricsForKey(keyName, inboundProtocol, modelGroup, provider, upstreamModel, outboundProtocol, status string, latency, ttft time.Duration) {
	if keyName == "" {
		return
	}

	metrics.RecordRequest(context.Background(), metrics.RequestInfo{
		InboundProtocol:    inboundProtocol,
		OutboundProtocol:   outboundProtocol,
		Provider:           provider,
		UpstreamModel:      upstreamModel,
		ModelGroup:         modelGroup,
		KeyName:            keyName,
		Status:             status,
		Duration:           latency.Seconds(),
		FirstTokenDuration: ttft.Seconds(),
	})
}
