package codec

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
)

func TestReadAnthropicStreamToObject_TextAndToolUse(t *testing.T) {
	body := strings.Join([]string{
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-3\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}",
		"",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}",
		"",
		"data: {\"type\":\"content_block_stop\",\"index\":0}",
		"",
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call-1\",\"name\":\"get_weather\",\"input\":{}}}",
		"",
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"beijing\\\"}\"}}",
		"",
		"data: {\"type\":\"content_block_stop\",\"index\":1}",
		"",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}",
		"",
		"data: {\"type\":\"message_stop\"}",
		"",
	}, "\n")

	out, err := readAnthropicStreamToObject(strings.NewReader(body), token.NewStreamCounter(0))
	if err != nil {
		t.Fatalf("readAnthropicStreamToObject error: %v", err)
	}
	if out.ID != "msg-1" {
		t.Fatalf("id = %q, want msg-1", out.ID)
	}
	if out.Model != "claude-3" {
		t.Fatalf("model = %q, want claude-3", out.Model)
	}
	if out.StopReason == nil || *out.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %v, want tool_use", out.StopReason)
	}
	if len(out.Content) != 2 {
		t.Fatalf("content len = %d, want 2", len(out.Content))
	}
	if out.Content[0].Type != "text" || out.Content[0].Text != "Hello" {
		t.Fatalf("content[0] = %+v, want text Hello", out.Content[0])
	}
	if out.Content[1].Type != "tool_use" || out.Content[1].ID != "call-1" {
		t.Fatalf("content[1] = %+v, want tool_use call-1", out.Content[1])
	}
	inputMap, ok := out.Content[1].Input.(map[string]any)
	if !ok || inputMap["city"] != "beijing" {
		t.Fatalf("tool input = %#v, want city=beijing", out.Content[1].Input)
	}
}

func TestReadResponsesStreamToObject_TextAndToolCall(t *testing.T) {
	body := strings.Join([]string{
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-4o\"}}",
		"",
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"call-1\",\"name\":\"get_weather\"}}",
		"",
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc-1\",\"delta\":\"{\\\"city\\\":\\\"beijing\\\"}\"}",
		"",
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}",
		"",
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-4o\",\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}",
		"",
	}, "\n")

	out, err := readResponsesStreamToObject(strings.NewReader(body), token.NewStreamCounter(0))
	if err != nil {
		t.Fatalf("readResponsesStreamToObject error: %v", err)
	}
	if out.ID != "resp-1" || out.Model != "gpt-4o" {
		t.Fatalf("id/model = %q/%q, want resp-1/gpt-4o", out.ID, out.Model)
	}
	if out.Usage.TotalTokens != 15 {
		t.Fatalf("usage.total_tokens = %d, want 15", out.Usage.TotalTokens)
	}
	if len(out.Output) != 2 {
		t.Fatalf("output len = %d, want 2", len(out.Output))
	}
	if out.Output[0].Type != "message" || len(out.Output[0].Content) == 0 || out.Output[0].Content[0].Text != "Hello" {
		t.Fatalf("output[0] = %+v, want message with Hello", out.Output[0])
	}
	if out.Output[1].Type != "function_call" || out.Output[1].CallID != "call-1" {
		t.Fatalf("output[1] = %+v, want function_call call-1", out.Output[1])
	}
}

func TestReadResponsesStreamToObject_Failed(t *testing.T) {
	body := "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\",\"message\":\"boom\"}}}\n\n"
	_, err := readResponsesStreamToObject(strings.NewReader(body), nil)
	if err == nil || !strings.Contains(err.Error(), "server_error") {
		t.Fatalf("error = %v, want contains server_error", err)
	}
}

func TestWriteClaudeObjectAsStream_WithEventNames(t *testing.T) {
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)
	stopReason := "tool_use"
	resp := &dto.ClaudeResponse{
		ID:        "msg-1",
		Type:      "message",
		Role:      "assistant",
		Model:     "claude-3",
		StopReason: &stopReason,
		Content: []dto.ContentBlock{
			{Type: "text", Text: "Hello"},
			{Type: "tool_use", ID: "call-1", Name: "get_weather", Input: map[string]any{"city": "beijing"}},
		},
	}

	if err := writeClaudeObjectAsStream(ctx, resp, counter); err != nil {
		t.Fatalf("writeClaudeObjectAsStream error: %v", err)
	}
	if got := w.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: message_start") {
		t.Fatalf("missing event: message_start")
	}
	if !strings.Contains(body, "event: content_block_delta") {
		t.Fatalf("missing event: content_block_delta")
	}
	if !strings.Contains(body, "event: message_stop") {
		t.Fatalf("missing event: message_stop")
	}
	if counter.GetOutputTokens() == 0 {
		t.Fatalf("output tokens should be > 0")
	}
}

func TestPassThroughResponsesResponse_NonStreamAndStream(t *testing.T) {
	t.Run("non-stream", func(t *testing.T) {
		ctx, w := newTestContext()
		counter := token.NewStreamCounter(0)
		body := `{"id":"resp-1","object":"response","created_at":1,"model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello"}]},{"type":"function_call","call_id":"call-1","name":"get_weather","arguments":"{\"city\":\"beijing\"}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}

		if err := passThroughResponsesResponse(ctx, resp, false, counter); err != nil {
			t.Fatalf("passThroughResponsesResponse non-stream error: %v", err)
		}
		if w.Body.String() != body {
			t.Fatalf("body mismatch")
		}
		if counter.GetOutputTokens() == 0 {
			t.Fatalf("output tokens should be > 0")
		}
	})

	t.Run("stream", func(t *testing.T) {
		ctx, w := newTestContext()
		counter := token.NewStreamCounter(0)
		streamBody := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n" +
			"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"city\\\":\\\"beijing\\\"}\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}\n\n"
		resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(streamBody))}

		if err := passThroughResponsesResponse(ctx, resp, true, counter); err != nil {
			t.Fatalf("passThroughResponsesResponse stream error: %v", err)
		}
		if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
			t.Fatalf("content-type = %q, want text/event-stream", got)
		}
		if !strings.Contains(w.Body.String(), "response.output_text.delta") {
			t.Fatalf("missing streamed delta event")
		}
		if counter.GetOutputTokens() == 0 {
			t.Fatalf("output tokens should be > 0")
		}
	})
}

