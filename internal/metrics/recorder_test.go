package metrics

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

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
