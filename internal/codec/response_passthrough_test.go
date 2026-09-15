package codec

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

func TestPassThroughResponsesStream_DropsKeepaliveEvent(t *testing.T) {
	resp := newResponse(http.StatusOK, strings.Join([]string{
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1234,"model":"upstream-model","status":"in_progress","output":[]}}`,
		"",
		`data: {"type":"keepalive","sequence_number":1}`,
		"",
		`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","object":"response","created_at":1234,"model":"upstream-model","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		"",
	}, "\n"), map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesStream(w, resp, nil, ResponseModelContext{}); err != nil {
		t.Fatalf("passThroughResponsesStream error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, `"type":"keepalive"`) {
		t.Fatalf("stream body should not contain keepalive event, got %q", body)
	}
	if !strings.Contains(body, `"type":"response.created"`) {
		t.Fatalf("stream body should contain response.created, got %q", body)
	}
	if !strings.Contains(body, `"type":"response.completed"`) {
		t.Fatalf("stream body should contain response.completed, got %q", body)
	}
	if strings.Contains(body, "\n\n\n") {
		t.Fatalf("stream body should not contain empty dropped event separator, got %q", body)
	}
	if strings.Count(body, "data: ") != 2 {
		t.Fatalf("stream body should contain 2 SSE data frames after dropping keepalive, got %q", body)
	}
}

func TestPassThroughResponsesStream_DropsKeepaliveEventWithHeaders(t *testing.T) {
	resp := newResponse(http.StatusOK, strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1234,"model":"upstream-model","status":"in_progress","output":[]}}`,
		"",
		`id: keepalive-1`,
		`event: keepalive`,
		`retry: 1000`,
		`data: {"type":"keepalive","sequence_number":1}`,
		"",
		`event: response.completed`,
		`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","object":"response","created_at":1234,"model":"upstream-model","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		"",
	}, "\n"), map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesStream(w, resp, nil, ResponseModelContext{}); err != nil {
		t.Fatalf("passThroughResponsesStream error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, `event: keepalive`) {
		t.Fatalf("stream body should not contain keepalive event header, got %q", body)
	}
	if strings.Contains(body, `id: keepalive-1`) {
		t.Fatalf("stream body should not contain dropped keepalive id header, got %q", body)
	}
	if strings.Contains(body, `retry: 1000`) {
		t.Fatalf("stream body should not contain dropped keepalive retry header, got %q", body)
	}
	if strings.Contains(body, `"type":"keepalive"`) {
		t.Fatalf("stream body should not contain keepalive event data, got %q", body)
	}
	if strings.Count(body, "event: ") != 2 {
		t.Fatalf("stream body should contain 2 SSE events after dropping keepalive, got %q", body)
	}
	if strings.Count(body, "data: ") != 2 {
		t.Fatalf("stream body should contain 2 SSE data frames after dropping keepalive, got %q", body)
	}
}

func TestPassThroughResponsesStream_DropsKeepaliveEventAtEOF(t *testing.T) {
	resp := newResponse(http.StatusOK, strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1234,"model":"upstream-model","status":"in_progress","output":[]}}`,
		"",
		`event: keepalive`,
		`data: {"type":"keepalive","sequence_number":1}`,
		"",
		`event: response.completed`,
		`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","object":"response","created_at":1234,"model":"upstream-model","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	}, "\n"), map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesStream(w, resp, nil, ResponseModelContext{}); err != nil {
		t.Fatalf("passThroughResponsesStream error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, `event: keepalive`) {
		t.Fatalf("stream body should not contain keepalive event header at EOF, got %q", body)
	}
	if strings.Contains(body, `"type":"keepalive"`) {
		t.Fatalf("stream body should not contain keepalive event data at EOF, got %q", body)
	}
	if !strings.Contains(body, `event: response.created`) {
		t.Fatalf("stream body should contain response.created event, got %q", body)
	}
}

func TestPassThroughResponsesStream_NormalizesMultilineDataAsSingleEvent(t *testing.T) {
	resp := newResponse(http.StatusOK, strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created",`,
		`data: "response":{"id":"resp_1","object":"response","created_at":1234,"model":"upstream-model","status":"in_progress","output":[]}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1234,"model":"upstream-model","status":"completed","output":[]}}`,
		"",
	}, "\n"), map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesStream(w, resp, nil, ResponseModelContext{RequestedModel: "requested-model"}); err != nil {
		t.Fatalf("passThroughResponsesStream error: %v", err)
	}

	body := w.Body.String()
	if got := strings.Count(body, "data: "); got != 2 {
		t.Fatalf("stream body should contain two data lines (merged created + completed), got %d: %q", got, body)
	}
	if !strings.Contains(body, "event: response.created\n") {
		t.Fatalf("stream body should preserve event header, got %q", body)
	}

	var data string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		t.Fatalf("rewritten event data is not valid JSON: %q: %v", data, err)
	}
	if got := int(event["sequence_number"].(float64)); got != 0 {
		t.Fatalf("sequence_number = %d, want 0", got)
	}
	response := event["response"].(map[string]any)
	if got := response["model"]; got != "requested-model" {
		t.Fatalf("response.model = %v, want requested-model", got)
	}
}

func TestPassThroughResponsesStream_NormalizesFailedResponseRequiredFields(t *testing.T) {
	resp := newResponse(http.StatusOK, strings.Join([]string{
		`data: {"type":"response.failed","response":{"id":"resp_1","object":"response","status":"failed","error":{"code":"server_error","message":"upstream failure"}}}`,
		"",
	}, "\n"), map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesStream(w, resp, nil, ResponseModelContext{RequestedModel: "requested-model"}); err != nil {
		t.Fatalf("passThroughResponsesStream error: %v", err)
	}

	var event map[string]any
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatalf("unmarshal stream event: %v", err)
			}
		}
	}
	response, ok := event["response"].(map[string]any)
	if !ok {
		t.Fatalf("response = %T, want object", event["response"])
	}
	if got := response["model"]; got != "requested-model" {
		t.Fatalf("response.model = %v, want requested-model", got)
	}
	if output, ok := response["output"].([]any); !ok || len(output) != 0 {
		t.Fatalf("response.output = %#v, want empty array", response["output"])
	}
}

func TestPassThroughResponsesStream_DoesNotSplitMultilineDataIntoMultipleEvents(t *testing.T) {
	firstLine := `data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"upstream-model","output":[]}}`
	secondLine := `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"upstream-model","output":[]}}`
	resp := newResponse(http.StatusOK, strings.Join([]string{
		firstLine,
		secondLine,
		"",
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"upstream-model","output":[]}}`,
		"",
	}, "\n"), map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesStream(w, resp, nil, ResponseModelContext{}); err != nil {
		t.Fatalf("passThroughResponsesStream error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, firstLine+"\n"+secondLine+"\n") {
		t.Fatalf("unparseable multiline event should be preserved, got %q", body)
	}
	if got := strings.Count(body, `"sequence_number":`); got != 2 {
		t.Fatalf("stream should only sequence the two following valid events, got %d sequence numbers: %q", got, body)
	}

	var lastEvent map[string]any
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") || !strings.Contains(line, `response.output_text.delta`) {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &lastEvent); err != nil {
			t.Fatalf("unmarshal final event: %v", err)
		}
	}
	if got := int(lastEvent["sequence_number"].(float64)); got != 0 {
		t.Fatalf("final event sequence_number = %d, want 0", got)
	}
}

func TestResponsesCompatLogTracker_DeduplicatesAndSummarizes(t *testing.T) {
	tracker := newResponsesCompatLogTracker("model")
	tracker.record(4, "response.reasoning_summary_text.delta", []string{"summary_index"})
	tracker.record(5, "response.reasoning_summary_text.delta", []string{"summary_index"})
	tracker.record(6, "response.reasoning_summary_part.added", []string{"summary_index", "part.text"})

	if tracker.total != 3 {
		t.Fatalf("total = %d, want 3", tracker.total)
	}
	if len(tracker.counts) != 2 {
		t.Fatalf("rule count = %d, want 2", len(tracker.counts))
	}
	want := []string{
		"response.reasoning_summary_part.added[summary_index,part.text]=1",
		"response.reasoning_summary_text.delta[summary_index]=2",
	}
	if got := tracker.summaryRules(); !reflect.DeepEqual(got, want) {
		t.Fatalf("summaryRules = %v, want %v", got, want)
	}
}

func TestExtractResponsesError_NestedResponseError(t *testing.T) {
	event := dto.ResponsesStreamEvent{
		Type:     "response.failed",
		Response: json.RawMessage(`{"id":"resp_1","status":"failed","error":{"code":"server_error","message":"upstream failure"}}`),
	}
	code, message := extractResponsesError(event)
	if code != "server_error" {
		t.Fatalf("code = %q, want server_error", code)
	}
	if message != "upstream failure" {
		t.Fatalf("message = %q, want upstream failure", message)
	}
}

func TestExtractResponsesError_TopLevelError(t *testing.T) {
	event := dto.ResponsesStreamEvent{
		Type:  "response.failed",
		Error: json.RawMessage(`{"code":"rate_limit_exceeded","message":"slow down"}`),
	}
	code, message := extractResponsesError(event)
	if code != "rate_limit_exceeded" {
		t.Fatalf("code = %q, want rate_limit_exceeded", code)
	}
	if message != "slow down" {
		t.Fatalf("message = %q, want slow down", message)
	}
}

func TestExtractResponsesError_NoError(t *testing.T) {
	event := dto.ResponsesStreamEvent{Type: "response.failed"}
	code, message := extractResponsesError(event)
	if code != "" || message != "" {
		t.Fatalf("code=%q message=%q, want both empty", code, message)
	}
}
