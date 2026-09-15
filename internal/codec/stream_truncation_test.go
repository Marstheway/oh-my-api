package codec

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// 三个直通路径（responses / chat / anthropic）在中途断流时应返回 ErrStreamTruncated，
// 正常收到终止事件（response.completed / [DONE]+finish_reason / message_stop）时应返回 nil。

func TestPassThroughResponsesStream_Truncated(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","model":"upstream-model","status":"in_progress","output":[]}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		"",
	}, "\n")
	resp := newResponse(http.StatusOK, body, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	err := passThroughResponsesStream(w, resp, nil, ResponseModelContext{})
	if !errors.Is(err, ErrStreamTruncated) {
		t.Fatalf("error = %v, want ErrStreamTruncated", err)
	}
}

func TestPassThroughResponsesStream_Completed(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","model":"upstream-model","status":"in_progress","output":[]}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_1","model":"upstream-model","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		"",
	}, "\n")
	resp := newResponse(http.StatusOK, body, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesStream(w, resp, nil, ResponseModelContext{}); err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}

func TestPassThroughOpenAIStream_Truncated(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		"",
	}, "\n")
	resp := newResponse(http.StatusOK, body, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	err := passThroughOpenAIStream(w, resp, nil, "")
	if !errors.Is(err, ErrStreamTruncated) {
		t.Fatalf("error = %v, want ErrStreamTruncated", err)
	}
}

func TestPassThroughOpenAIStream_Finished(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		"",
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	resp := newResponse(http.StatusOK, body, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughOpenAIStream(w, resp, nil, ""); err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}

func TestPassThroughOpenAIStream_FinalTerminalFrameWithoutNewline(t *testing.T) {
	body := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	resp := newResponse(http.StatusOK, body, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughOpenAIStream(w, resp, nil, ""); err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if !strings.Contains(w.Body.String(), `"finish_reason":"stop"`) {
		t.Fatalf("final terminal frame was not forwarded: %q", w.Body.String())
	}
}

func TestPassThroughAnthropicStream_Truncated(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3","content":[]}}`,
		"",
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`,
		"",
	}, "\n")
	resp := newResponse(http.StatusOK, body, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	err := passThroughAnthropicStream(w, resp, nil, "")
	if !errors.Is(err, ErrStreamTruncated) {
		t.Fatalf("error = %v, want ErrStreamTruncated", err)
	}
}

func TestPassThroughAnthropicStream_Finished(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3","content":[]}}`,
		"",
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`,
		"",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	resp := newResponse(http.StatusOK, body, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughAnthropicStream(w, resp, nil, ""); err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}

func TestPassThroughAnthropicStream_ErrorEventIsTerminal(t *testing.T) {
	body := "data: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"upstream failure\"}}\n\n"
	resp := newResponse(http.StatusOK, body, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughAnthropicStream(w, resp, nil, ""); err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}
