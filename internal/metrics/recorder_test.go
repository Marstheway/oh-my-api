package metrics

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestRecordRequest_PreservesProviderUpstreamDimensions 验证 RecordRequest 保存独立的 provider
// 和 upstream_model 维度，不合并为单一字段。
func TestRecordRequest_PreservesProviderUpstreamDimensions(t *testing.T) {
	requestTotal.Reset()
	requestDuration.Reset()

	info := RequestInfo{
		InboundProtocol:  "openai",
		OutboundProtocol: "anthropic",
		Provider:         "my-provider",
		UpstreamModel:    "my-upstream",
		ModelGroup:       "alias",
		KeyName:          "key1",
		Status:           "success",
		Duration:         0.5,
	}

	RecordRequest(context.Background(), info)

	// 指标维度中 provider 和 upstream_model 独立存在
	count := testutil.ToFloat64(requestTotal.WithLabelValues(
		"openai", "anthropic", "my-provider", "my-upstream", "alias", "key1", "success",
	))
	if count != 1 {
		t.Errorf("expected count 1 for provider/upstream_model dimensions, got %f", count)
	}

	// 验证不会把 alias 写入 upstream_model 维度
	aliasCount := testutil.ToFloat64(requestTotal.WithLabelValues(
		"openai", "anthropic", "my-provider", "alias", "alias", "key1", "success",
	))
	if aliasCount != 0 {
		t.Errorf("alias should not appear in upstream_model dimension, got %f", aliasCount)
	}
}

func TestRecordRequest(t *testing.T) {
	requestTotal.Reset()
	requestDuration.Reset()

	info := RequestInfo{
		InboundProtocol:  "openai",
		OutboundProtocol: "anthropic",
		Provider:         "test-provider",
		UpstreamModel:    "gpt-4",
		ModelGroup:       "test-group",
		KeyName:          "test-key",
		Status:           "success",
		Duration:         1.5,
	}

	RecordRequest(context.Background(), info)

	count := testutil.ToFloat64(requestTotal.WithLabelValues(
		"openai", "anthropic", "test-provider", "gpt-4", "test-group", "test-key", "success",
	))
	if count != 1 {
		t.Errorf("expected request_total count 1, got %f", count)
	}

	if got := testutil.CollectAndCount(requestDuration); got == 0 {
		t.Errorf("expected request_duration_seconds to have collected metrics")
	}
}

func TestRecordToken(t *testing.T) {
	tokenInputTotal.Reset()
	tokenOutputTotal.Reset()

	RecordToken("test-provider", "gpt-4", "test-group", "test-key", 100, 50)

	input := testutil.ToFloat64(tokenInputTotal.WithLabelValues("test-provider", "gpt-4", "test-group", "test-key"))
	if input != 100 {
		t.Errorf("expected token_input_total 100, got %f", input)
	}

	output := testutil.ToFloat64(tokenOutputTotal.WithLabelValues("test-provider", "gpt-4", "test-group", "test-key"))
	if output != 50 {
		t.Errorf("expected token_output_total 50, got %f", output)
	}
}

func TestRecordStreamDecode(t *testing.T) {
	streamDecodeOutputTokensTotal.Reset()
	streamDecodeDurationSecondsTotal.Reset()

	RecordStreamDecode("test-provider", "gpt-4", "test-group", "test-key", 50, 2.5)
	RecordStreamDecode("test-provider", "gpt-4", "test-group", "test-key", 0, 2.5)
	RecordStreamDecode("test-provider", "gpt-4", "test-group", "test-key", 50, 0)

	output := testutil.ToFloat64(streamDecodeOutputTokensTotal.WithLabelValues("test-provider", "gpt-4", "test-group", "test-key"))
	if output != 50 {
		t.Errorf("expected stream_decode_output_tokens_total 50, got %f", output)
	}

	duration := testutil.ToFloat64(streamDecodeDurationSecondsTotal.WithLabelValues("test-provider", "gpt-4", "test-group", "test-key"))
	if duration != 2.5 {
		t.Errorf("expected stream_decode_duration_seconds_total 2.5, got %f", duration)
	}
}

func TestSetProviderHealth(t *testing.T) {
	providerHealthStatus.Reset()

	SetProviderHealth("test-provider", true)
	val := testutil.ToFloat64(providerHealthStatus.WithLabelValues("test-provider", ""))
	if val != 1 {
		t.Errorf("expected provider_health_status 1, got %f", val)
	}

	SetProviderHealth("test-provider", false)
	val = testutil.ToFloat64(providerHealthStatus.WithLabelValues("test-provider", ""))
	if val != 0 {
		t.Errorf("expected provider_health_status 0, got %f", val)
	}

	SetProviderHealth("test-provider|openai.response", true)
	val = testutil.ToFloat64(providerHealthStatus.WithLabelValues("test-provider", "openai.response"))
	if val != 1 {
		t.Errorf("expected provider_health_status 1 for protocol key, got %f", val)
	}
}

func TestRecordProviderFailure(t *testing.T) {
	providerRequestFailures.Reset()

	RecordProviderFailure("test-provider", "timeout")

	count := testutil.ToFloat64(providerRequestFailures.WithLabelValues("test-provider", "timeout"))
	if count != 1 {
		t.Errorf("expected provider_request_failures count 1, got %f", count)
	}
}

func TestRecordStreamInterrupted(t *testing.T) {
	streamInterruptedTotal.Reset()

	RecordStreamInterrupted("test-provider", "gpt-4", "openai.responses", "upstream_timeout")

	count := testutil.ToFloat64(streamInterruptedTotal.WithLabelValues(
		"test-provider", "gpt-4", "openai.responses", "upstream_timeout",
	))
	if count != 1 {
		t.Errorf("expected stream_interrupted_total count 1, got %f", count)
	}
}

func TestRecordRatelimitTriggered(t *testing.T) {
	ratelimitTriggeredTotal.Reset()

	RecordRatelimitTriggered("test-key")

	count := testutil.ToFloat64(ratelimitTriggeredTotal.WithLabelValues("test-key"))
	if count != 1 {
		t.Errorf("expected ratelimit_triggered_total count 1, got %f", count)
	}
}

func TestConcurrentRequests(t *testing.T) {
	concurrentRequests.Set(0)

	IncConcurrent()
	val := testutil.ToFloat64(concurrentRequests)
	if val != 1 {
		t.Errorf("expected concurrent_requests 1, got %f", val)
	}

	IncConcurrent()
	val = testutil.ToFloat64(concurrentRequests)
	if val != 2 {
		t.Errorf("expected concurrent_requests 2, got %f", val)
	}

	DecConcurrent()
	val = testutil.ToFloat64(concurrentRequests)
	if val != 1 {
		t.Errorf("expected concurrent_requests 1, got %f", val)
	}
}

// TestRecordProviderAttempt_Success 验证成功场景的 attempt 指标记录
func TestRecordProviderAttempt_Success(t *testing.T) {
	providerAttemptTotal.Reset()

	info := ProviderAttemptInfo{
		Scheduler:        "failover",
		Provider:         "test-provider",
		UpstreamModel:    "gpt-4",
		ModelGroup:       "group-a",
		OutboundProtocol: "openai",
		Result:           "success",
		FailureReason:    "",
		StatusCode:       "",
		Duration:         0.25,
	}

	RecordProviderAttempt(info)

	count := testutil.ToFloat64(providerAttemptTotal.WithLabelValues(
		"failover", "test-provider", "gpt-4", "group-a", "openai", "success", "", "",
	))
	if count != 1 {
		t.Errorf("expected provider_attempt_total count 1 for success, got %f", count)
	}
}

// TestRecordProviderAttempt_HardFailure 验证硬失败场景的 attempt 指标记录
func TestRecordProviderAttempt_HardFailure(t *testing.T) {
	providerAttemptTotal.Reset()

	info := ProviderAttemptInfo{
		Scheduler:        "failover",
		Provider:         "test-provider",
		UpstreamModel:    "gpt-4",
		ModelGroup:       "group-b",
		OutboundProtocol: "openai",
		Result:           "hard_failure",
		FailureReason:    "http_status",
		StatusCode:       "402",
		Duration:         1.5,
	}

	RecordProviderAttempt(info)

	count := testutil.ToFloat64(providerAttemptTotal.WithLabelValues(
		"failover", "test-provider", "gpt-4", "group-b", "openai", "hard_failure", "http_status", "402",
	))
	if count != 1 {
		t.Errorf("expected provider_attempt_total count 1 for hard_failure, got %f", count)
	}
}

// TestRecordProviderAttempt_SoftFailure 验证软失败场景的 attempt 指标记录
func TestRecordProviderAttempt_SoftFailure(t *testing.T) {
	providerAttemptTotal.Reset()

	info := ProviderAttemptInfo{
		Scheduler:        "failover",
		Provider:         "test-provider",
		UpstreamModel:    "gpt-4",
		ModelGroup:       "group-c",
		OutboundProtocol: "openai",
		Result:           "soft_failure",
		FailureReason:    "content_filter_finish_reason",
		StatusCode:       "200",
		Duration:         0.75,
	}

	RecordProviderAttempt(info)

	count := testutil.ToFloat64(providerAttemptTotal.WithLabelValues(
		"failover", "test-provider", "gpt-4", "group-c", "openai", "soft_failure", "content_filter_finish_reason", "200",
	))
	if count != 1 {
		t.Errorf("expected provider_attempt_total count 1 for soft_failure, got %f", count)
	}
}

// TestRecordProviderAttempt_AdaptiveScheduler 验证 adaptive scheduler 标签可正常记录
func TestRecordProviderAttempt_AdaptiveScheduler(t *testing.T) {
	providerAttemptTotal.Reset()

	info := ProviderAttemptInfo{
		Scheduler:        "adaptive",
		Provider:         "adaptive-provider",
		UpstreamModel:    "claude-sonnet",
		ModelGroup:       "adaptive-group",
		OutboundProtocol: "anthropic",
		Result:           "success",
		FailureReason:    "",
		StatusCode:       "",
		Duration:         0.45,
	}

	RecordProviderAttempt(info)

	count := testutil.ToFloat64(providerAttemptTotal.WithLabelValues(
		"adaptive", "adaptive-provider", "claude-sonnet", "adaptive-group", "anthropic", "success", "", "",
	))
	if count != 1 {
		t.Errorf("expected provider_attempt_total count 1 for adaptive scheduler, got %f", count)
	}
}

// TestRecordSmartRouteDecision 验证 smart route 决策计数
func TestRecordSmartRouteDecision(t *testing.T) {
	smartRouteDecisionTotal.Reset()

	RecordSmartRouteDecision("rule_reason")
	RecordSmartRouteDecision("rule_reason")
	RecordSmartRouteDecision("rule_scout")
	RecordSmartRouteDecision("judge_reason")
	RecordSmartRouteDecision("judge_scout")
	RecordSmartRouteDecision("judge_fallback_to_reason")

	// 验证 rule_reason 计数
	reasonCount := testutil.ToFloat64(smartRouteDecisionTotal.WithLabelValues("rule_reason"))
	if reasonCount != 2 {
		t.Errorf("expected smart_route_decision_total count 2 for rule_reason, got %f", reasonCount)
	}

	// 验证 rule_scout 计数
	scoutCount := testutil.ToFloat64(smartRouteDecisionTotal.WithLabelValues("rule_scout"))
	if scoutCount != 1 {
		t.Errorf("expected smart_route_decision_total count 1 for rule_scout, got %f", scoutCount)
	}

	// 验证 judge_reason 计数
	judgeReasonCount := testutil.ToFloat64(smartRouteDecisionTotal.WithLabelValues("judge_reason"))
	if judgeReasonCount != 1 {
		t.Errorf("expected smart_route_decision_total count 1 for judge_reason, got %f", judgeReasonCount)
	}

	// 验证 judge_scout 计数
	judgeScoutCount := testutil.ToFloat64(smartRouteDecisionTotal.WithLabelValues("judge_scout"))
	if judgeScoutCount != 1 {
		t.Errorf("expected smart_route_decision_total count 1 for judge_scout, got %f", judgeScoutCount)
	}

	// 验证 judge_fallback_to_reason 计数
	fallbackCount := testutil.ToFloat64(smartRouteDecisionTotal.WithLabelValues("judge_fallback_to_reason"))
	if fallbackCount != 1 {
		t.Errorf("expected smart_route_decision_total count 1 for judge_fallback_to_reason, got %f", fallbackCount)
	}
}
