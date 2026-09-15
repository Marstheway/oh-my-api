package codec

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
	"github.com/gin-gonic/gin"
)

const (
	openAINonStreamBody = `{"id":"chatcmpl-1","object":"chat.completion","created":1234,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	openAIStreamBody    = "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"
	claudeNonStreamBody = `{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"Hello"}],"model":"claude-3","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`
	claudeStreamBody    = "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-3\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
)

func newTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", nil)
	return c, w
}

func newResponse(status int, body string, headers map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("read boom")
}

func (errReadCloser) Close() error {
	return nil
}

func assertConversionError(t *testing.T, err error, step, reason string, inbound, outbound Format) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected conversion error")
	}
	var convErr *ConversionError
	if !errors.As(err, &convErr) {
		t.Fatalf("expected ConversionError, got %T (%v)", err, err)
	}
	if convErr.Phase != "write_response" {
		t.Fatalf("phase = %q, want %q", convErr.Phase, "write_response")
	}
	if convErr.Step != step {
		t.Fatalf("step = %q, want %q", convErr.Step, step)
	}
	if convErr.Reason != reason {
		t.Fatalf("reason = %q, want %q", convErr.Reason, reason)
	}
	if convErr.InboundFormat != string(inbound) {
		t.Fatalf("inbound = %q, want %q", convErr.InboundFormat, inbound)
	}
	if convErr.OutboundFormat != string(outbound) {
		t.Fatalf("outbound = %q, want %q", convErr.OutboundFormat, outbound)
	}
}

func TestOpenAIChatCodec_WriteResponse_NonStream(t *testing.T) {
	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusCreated, openAINonStreamBody, map[string]string{"X-Test": "openai"})
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, false, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusCreated)
	}
	if w.Header().Get("X-Test") != "openai" {
		t.Fatalf("upstream header not forwarded to client")
	}
	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body parse error: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "gpt-4" {
		t.Fatalf("model = %q, want gpt-4", out.Model)
	}
	if len(out.Choices) == 0 || out.Choices[0].Message.Content != "Hello" {
		t.Fatalf("content not preserved")
	}
	if counter.GetOutputTokens() == 0 {
		t.Fatalf("output tokens should be > 0")
	}
}

func TestOpenAIChatCodec_WriteResponse_FromAnthropicMessages(t *testing.T) {
	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, claudeNonStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if out.Object != "chat.completion" {
		t.Fatalf("object = %q, want chat.completion", out.Object)
	}
	if len(out.Choices) == 0 || out.Choices[0].Message == nil || out.Choices[0].Message.Content != "Hello" {
		t.Fatalf("A->O conversion missing content")
	}
	if counter.GetOutputTokens() == 0 {
		t.Fatalf("output tokens should be > 0")
	}
}

func TestOpenAIChatCodec_WriteResponse_FromAnthropicMessages_InvalidJSON(t *testing.T) {
	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, "not-json", nil)
	ctx, _ := newTestContext()

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, nil, ResponseModelContext{}); err == nil {
		t.Fatalf("expected error for invalid json")
	}
}

func TestAnthropicMessagesCodec_WriteResponse_NonStream(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusAccepted, claudeNonStreamBody, map[string]string{"X-Test": "anthropic"})
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	if w.Header().Get("X-Test") != "anthropic" {
		t.Fatalf("upstream header not forwarded to client")
	}
	var out dto.ClaudeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body parse error: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "claude-3" {
		t.Fatalf("model = %q, want claude-3", out.Model)
	}
	if len(out.Content) == 0 || out.Content[0].Text != "Hello" {
		t.Fatalf("content not preserved")
	}
	if counter.GetOutputTokens() == 0 {
		t.Fatalf("output tokens should be > 0")
	}
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIChat(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, openAINonStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, false, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var out dto.ClaudeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if out.Type != "message" || out.Role != "assistant" {
		t.Fatalf("O->A response shape mismatch")
	}
	if len(out.Content) == 0 || out.Content[0].Text != "Hello" {
		t.Fatalf("O->A content not mapped")
	}
	if out.StopReason == nil || *out.StopReason != "end_turn" {
		t.Fatalf("stop_reason = %v, want end_turn", out.StopReason)
	}
	if counter.GetOutputTokens() == 0 {
		t.Fatalf("output tokens should be > 0")
	}
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIChat_InvalidJSON(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, "not-json", nil)
	ctx, _ := newTestContext()

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, false, nil, ResponseModelContext{}); err == nil {
		t.Fatalf("expected error for invalid json")
	}
}

func TestOpenAIChatCodec_WriteResponse_Stream(t *testing.T) {
	codec := &OpenAIChatCodec{}

	t.Run("pass-through", func(t *testing.T) {
		resp := newResponse(http.StatusOK, openAIStreamBody, map[string]string{"Content-Type": "text/event-stream"})
		ctx, w := newTestContext()
		counter := token.NewStreamCounter(0)

		if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, true, counter, ResponseModelContext{}); err != nil {
			t.Fatalf("WriteResponse error: %v", err)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		if w.Header().Get("Content-Type") != "text/event-stream" {
			t.Fatalf("content-type not preserved")
		}
		if !strings.Contains(w.Body.String(), "data: [DONE]") {
			t.Fatalf("body missing [DONE]")
		}
		if counter.GetOutputTokens() == 0 {
			t.Fatalf("output tokens should be > 0")
		}
	})

	t.Run("from anthropic", func(t *testing.T) {
		resp := newResponse(http.StatusOK, claudeStreamBody, nil)
		ctx, w := newTestContext()
		counter := token.NewStreamCounter(0)

		if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, counter, ResponseModelContext{}); err != nil {
			t.Fatalf("WriteResponse error: %v", err)
		}
		body := w.Body.String()
		if !strings.Contains(body, "chat.completion.chunk") {
			t.Fatalf("missing openai chunk output")
		}
		if !strings.Contains(body, "\"content\":\"Hello\"") {
			t.Fatalf("missing converted content")
		}
		if !strings.Contains(body, "data: [DONE]") {
			t.Fatalf("missing [DONE]")
		}
		if counter.GetOutputTokens() == 0 {
			t.Fatalf("output tokens should be > 0")
		}
	})
}

func TestOpenAIResponseCodec_WriteResponse_FromOpenAIChat_NonStream_Text(t *testing.T) {
	c := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, openAINonStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := c.WriteResponse(ctx, FormatOpenAIChat, resp, false, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if out.Object != "response" {
		t.Fatalf("object = %q, want response", out.Object)
	}
	if out.Status != "completed" {
		t.Fatalf("status = %q, want completed", out.Status)
	}
	if len(out.Output) == 0 {
		t.Fatalf("output should not be empty")
	}
	first := out.Output[0]
	if first.Type != "message" {
		t.Fatalf("output[0].type = %q, want message", first.Type)
	}
	if first.Role != "assistant" {
		t.Fatalf("output[0].role = %q, want assistant", first.Role)
	}
	if len(first.Content) == 0 {
		t.Fatalf("output[0].content should not be empty")
	}
	if first.Content[0].Type != "output_text" {
		t.Fatalf("content[0].type = %q, want output_text", first.Content[0].Type)
	}
	if first.Content[0].Text != "Hello" {
		t.Fatalf("content[0].text = %q, want Hello", first.Content[0].Text)
	}
	if out.Usage.InputTokens != 10 {
		t.Fatalf("usage.input_tokens = %d, want 10", out.Usage.InputTokens)
	}
	if out.Usage.OutputTokens != 5 {
		t.Fatalf("usage.output_tokens = %d, want 5", out.Usage.OutputTokens)
	}
	if out.Usage.TotalTokens != 15 {
		t.Fatalf("usage.total_tokens = %d, want 15", out.Usage.TotalTokens)
	}
	if out.CreatedAt != 1234 {
		t.Fatalf("created_at = %d, want 1234", out.CreatedAt)
	}
	if counter.GetOutputTokens() == 0 {
		t.Fatalf("counter output tokens should be > 0")
	}
}

func TestOpenAIResponseCodec_WriteResponse_FromOpenAIChat_NonStream_Reasoning(t *testing.T) {
	body := `{"id":"chatcmpl-r","object":"chat.completion","created":1234,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Answer","reasoning_content":"First think."},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

	c := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()

	if err := c.WriteResponse(ctx, FormatOpenAIChat, resp, false, nil, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(out.Output) < 2 {
		t.Fatalf("output len = %d, want >= 2", len(out.Output))
	}
	if out.Output[0].Type != "reasoning" {
		t.Fatalf("output[0].type = %q, want reasoning", out.Output[0].Type)
	}
	var summary []map[string]any
	if err := json.Unmarshal(out.Output[0].Summary, &summary); err != nil {
		t.Fatalf("summary parse error: %v", err)
	}
	if len(summary) != 1 || summary[0]["text"] != "First think." {
		t.Fatalf("unexpected reasoning summary: %+v", summary)
	}
	if out.Output[1].Type != "message" {
		t.Fatalf("output[1].type = %q, want message", out.Output[1].Type)
	}
}

func TestOpenAIResponseCodec_WriteResponse_FromOpenAIChat_NonStream_Tools(t *testing.T) {
	body := `{"id":"chatcmpl-2","object":"chat.completion","created":1234,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Beijing\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}}`

	c := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := c.WriteResponse(ctx, FormatOpenAIChat, resp, false, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	var funcCallItem *dto.ResponsesOutput
	for i := range out.Output {
		if out.Output[i].Type == "function_call" {
			funcCallItem = &out.Output[i]
			break
		}
	}
	if funcCallItem == nil {
		t.Fatalf("expected a function_call item in output, got: %+v", out.Output)
	}
	if funcCallItem.CallID != "call-1" {
		t.Fatalf("call_id = %q, want call-1", funcCallItem.CallID)
	}
	if funcCallItem.Name != "get_weather" {
		t.Fatalf("name = %q, want get_weather", funcCallItem.Name)
	}
	if funcCallItem.Arguments == "" {
		t.Fatalf("arguments should not be empty")
	}
}

func TestOpenAIResponseCodec_WriteResponse_FromOpenAIChat_NonStream_InvalidChoices(t *testing.T) {
	body := `{"id":"x","object":"chat.completion","created":1,"model":"gpt-4","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`

	c := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, _ := newTestContext()

	if err := c.WriteResponse(ctx, FormatOpenAIChat, resp, false, nil, ResponseModelContext{}); err == nil {
		t.Fatalf("expected error for empty choices")
	}
}

func TestOpenAIChatCodec_WriteResponse_FromOpenAIResponse_NonStream_Text(t *testing.T) {
	body := `{"id":"resp-1","object":"realtime.response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if out.Object != "chat.completion" {
		t.Fatalf("object = %q, want chat.completion", out.Object)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("len(choices) = %d, want 1", len(out.Choices))
	}
	if out.Choices[0].Message == nil || out.Choices[0].Message.Role != "assistant" {
		t.Fatalf("choices[0].message.role = %q, want assistant", out.Choices[0].Message.Role)
	}
	if out.Choices[0].Message.Content != "Hello" {
		t.Fatalf("choices[0].message.content = %q, want Hello", out.Choices[0].Message.Content)
	}
	if out.Usage.PromptTokens != 10 {
		t.Fatalf("usage.prompt_tokens = %d, want 10", out.Usage.PromptTokens)
	}
	if out.Usage.CompletionTokens != 5 {
		t.Fatalf("usage.completion_tokens = %d, want 5", out.Usage.CompletionTokens)
	}
	if out.Usage.TotalTokens != 15 {
		t.Fatalf("usage.total_tokens = %d, want 15", out.Usage.TotalTokens)
	}
	if out.Created != 1234 {
		t.Fatalf("created = %d, want 1234", out.Created)
	}
	if counter.GetOutputTokens() == 0 {
		t.Fatalf("counter output tokens should be > 0")
	}
}

func TestOpenAIChatCodec_WriteResponse_FromOpenAIResponse_NonStream_Tools(t *testing.T) {
	body := `{"id":"resp-2","object":"realtime.response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"function_call","call_id":"call-1","name":"get_weather","arguments":"{\"city\":\"Beijing\"}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("len(choices) = %d, want 1", len(out.Choices))
	}
	toolCalls := out.Choices[0].Message.ToolCalls
	if len(toolCalls) != 1 {
		t.Fatalf("len(tool_calls) = %d, want 1", len(toolCalls))
	}
	tc := toolCalls[0]
	if tc.ID != "call-1" {
		t.Fatalf("tool_call.id = %q, want call-1", tc.ID)
	}
	if tc.Type != "function" {
		t.Fatalf("tool_call.type = %q, want function", tc.Type)
	}
	if tc.Function.Name != "get_weather" {
		t.Fatalf("tool_call.function.name = %q, want get_weather", tc.Function.Name)
	}
	if out.Choices[0].FinishReason == nil || *out.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %v, want tool_calls", out.Choices[0].FinishReason)
	}
}

func TestOpenAIChatCodec_WriteResponse_FromOpenAIResponse_NonStream_StatusFailed(t *testing.T) {
	body := `{"id":"resp-3","object":"realtime.response","created_at":1234,"model":"gpt-4o","status":"failed","output":[],"error":{"code":"server_error","message":"Internal error"},"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`

	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, _ := newTestContext()

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{}); err == nil {
		t.Fatalf("expected error for failed status")
	}
}

func TestOpenAIResponseCodec_WriteResponse_FromOpenAIChat_Stream_Text(t *testing.T) {
	chatStreamBody := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	c := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, chatStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := c.WriteResponse(ctx, FormatOpenAIChat, resp, true, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "response.created") {
		t.Fatalf("missing response.created, body=%s", body)
	}
	if !strings.Contains(body, "response.output_text.delta") {
		t.Fatalf("missing response.output_text.delta, body=%s", body)
	}
	if !strings.Contains(body, "Hello") {
		t.Fatalf("missing Hello text, body=%s", body)
	}
	if !strings.Contains(body, "response.completed") {
		t.Fatalf("missing response.completed, body=%s", body)
	}
	if counter.GetOutputTokens() == 0 {
		t.Fatalf("output tokens should be > 0")
	}
}

func TestOpenAIResponseCodec_WriteResponse_FromOpenAIChat_Stream_Tools(t *testing.T) {
	chatStreamBody := "data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"\",\"arguments\":\"{\\\"city\\\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"

	c := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, chatStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := c.WriteResponse(ctx, FormatOpenAIChat, resp, true, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "response.output_item.added") {
		t.Fatalf("missing response.output_item.added, body=%s", body)
	}
	if !strings.Contains(body, "function_call") {
		t.Fatalf("missing function_call item type, body=%s", body)
	}
	if !strings.Contains(body, "response.function_call_arguments.delta") {
		t.Fatalf("missing response.function_call_arguments.delta, body=%s", body)
	}
	if !strings.Contains(body, "response.completed") {
		t.Fatalf("missing response.completed, body=%s", body)
	}
}

// TestOpenAIResponseCodec_WriteResponse_FromOpenAIChat_Stream_Tools_RealFormat 模拟真实 OpenAI 流式工具调用：
// 第一个 chunk 携带 id，后续 arguments 分片 chunk id 为空，靠 index 字段关联。
func TestOpenAIResponseCodec_WriteResponse_FromOpenAIChat_Stream_Tools_RealFormat(t *testing.T) {
	chatStreamBody := "data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":null},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"\",\"type\":\"\",\"function\":{\"name\":\"\",\"arguments\":\"{\\\"city\\\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"\",\"type\":\"\",\"function\":{\"name\":\"\",\"arguments\":\": \\\"Beijing\\\"}\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"

	c := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, chatStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := c.WriteResponse(ctx, FormatOpenAIChat, resp, true, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "response.output_item.added") {
		t.Fatalf("missing response.output_item.added, body=%s", body)
	}
	if !strings.Contains(body, "function_call") {
		t.Fatalf("missing function_call item type, body=%s", body)
	}
	if !strings.Contains(body, "response.function_call_arguments.delta") {
		t.Fatalf("missing response.function_call_arguments.delta, body=%s", body)
	}
	if !strings.Contains(body, "city") {
		t.Fatalf("missing arguments content, body=%s", body)
	}
	if !strings.Contains(body, "response.completed") {
		t.Fatalf("missing response.completed, body=%s", body)
	}
}

func TestOpenAIChatCodec_WriteResponse_FromOpenAIResponse_Stream_Text(t *testing.T) {
	responsesStreamBody := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-4o\"}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"data: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hello\"}\n\n" +
		"data: {\"type\":\"response.output_text.done\",\"output_index\":0,\"content_index\":0,\"text\":\"Hello\"}\n\n" +
		"data: {\"type\":\"response.content_part.done\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"Hello\"}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\"}]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-4o\"}}\n\n"

	chatCodec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, responsesStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := chatCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "chat.completion.chunk") {
		t.Fatalf("missing chat.completion.chunk, body=%s", body)
	}
	if !strings.Contains(body, "Hello") {
		t.Fatalf("missing Hello text, body=%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("missing [DONE], body=%s", body)
	}
	if counter.GetOutputTokens() == 0 {
		t.Fatalf("output tokens should be > 0")
	}
}

func TestOpenAIChatCodec_WriteResponse_FromOpenAIResponse_Stream_Tools(t *testing.T) {
	responsesStreamBody := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-2\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-4o\"}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"call-1\",\"name\":\"get_weather\",\"arguments\":\"\"}}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"item_id\":\"fc-1\",\"delta\":\"{\\\"city\\\"\"}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"item_id\":\"fc-1\",\"arguments\":\"{\\\"city\\\":\\\"Beijing\\\"}\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"call-1\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"Beijing\\\"}\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-2\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-4o\"}}\n\n"

	chatCodec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, responsesStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := chatCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "tool_calls") {
		t.Fatalf("missing tool_calls, body=%s", body)
	}
}

func TestOpenAIChatCodec_WriteResponse_FromOpenAIResponse_Stream_InvalidEvent(t *testing.T) {
	responsesStreamBody := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-3\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-4o\"}}\n\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp-3\",\"object\":\"response\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"Internal error\"}}}\n\n"

	chatCodec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, responsesStreamBody, nil)
	ctx, _ := newTestContext()

	err := chatCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, nil, ResponseModelContext{})
	if err == nil {
		t.Fatalf("expected error for response.failed event")
	}
}

func TestAnthropicMessagesCodec_WriteResponse_Stream(t *testing.T) {
	codec := &AnthropicMessagesCodec{}

	t.Run("pass-through", func(t *testing.T) {
		resp := newResponse(http.StatusOK, claudeStreamBody, map[string]string{"Content-Type": "text/event-stream"})
		ctx, w := newTestContext()
		counter := token.NewStreamCounter(0)

		if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, counter, ResponseModelContext{}); err != nil {
			t.Fatalf("WriteResponse error: %v", err)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		if w.Header().Get("Content-Type") != "text/event-stream" {
			t.Fatalf("content-type not preserved")
		}
		if w.Body.String() != claudeStreamBody {
			t.Fatalf("body mismatch")
		}
		if counter.GetOutputTokens() == 0 {
			t.Fatalf("output tokens should be > 0")
		}
	})

	t.Run("pass-through nil counter", func(t *testing.T) {
		resp := newResponse(http.StatusOK, claudeStreamBody, map[string]string{"Content-Type": "text/event-stream"})
		ctx, w := newTestContext()

		if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, nil, ResponseModelContext{}); err != nil {
			t.Fatalf("WriteResponse error: %v", err)
		}
		if w.Body.String() != claudeStreamBody {
			t.Fatalf("body mismatch")
		}
	})

	t.Run("from openai", func(t *testing.T) {
		resp := newResponse(http.StatusOK, openAIStreamBody, nil)
		ctx, w := newTestContext()
		counter := token.NewStreamCounter(0)

		if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, true, counter, ResponseModelContext{}); err != nil {
			t.Fatalf("WriteResponse error: %v", err)
		}
		body := w.Body.String()
		if !strings.Contains(body, "\"type\":\"message_start\"") {
			t.Fatalf("missing message_start")
		}
		if !strings.Contains(body, "event: message_start") {
			t.Fatalf("missing SSE event name for message_start")
		}
		if !strings.Contains(body, "event: content_block_delta") {
			t.Fatalf("missing SSE event name for content_block_delta")
		}
		if !strings.Contains(body, "\"type\":\"content_block_delta\"") {
			t.Fatalf("missing content_block_delta")
		}
		if !strings.Contains(body, "\"text\":\"Hello\"") {
			t.Fatalf("missing converted text")
		}
		if !strings.Contains(body, "\"type\":\"message_stop\"") {
			t.Fatalf("missing message_stop")
		}
		if counter.GetOutputTokens() == 0 {
			t.Fatalf("output tokens should be > 0")
		}
		if got := w.Header().Get("X-Accel-Buffering"); got != "no" {
			t.Fatalf("X-Accel-Buffering = %q, want no", got)
		}
	})
}

// Task 9: 二次响应转换链路测试

func TestOpenAIResponseCodec_WriteResponse_FromAnthropicMessages_NonStream_ViaChat(t *testing.T) {
	responseCodec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, claudeNonStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := responseCodec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	body := w.Body.String()
	var out dto.ResponsesResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("response json unmarshal error: %v, body=%s", err, body)
	}
	if out.Object != "response" {
		t.Fatalf("object = %q, want %q", out.Object, "response")
	}
	if len(out.Output) == 0 {
		t.Fatalf("output should not be empty")
	}
	if out.Output[0].Type != "message" {
		t.Fatalf("output[0].type = %q, want %q", out.Output[0].Type, "message")
	}
}

func TestOpenAIResponseCodec_WriteResponse_FromAnthropicMessages_NonStream_ReadErrorWrapped(t *testing.T) {
	responseCodec := &OpenAIResponseCodec{}
	resp := &http.Response{StatusCode: http.StatusOK, Body: errReadCloser{}}
	ctx, _ := newTestContext()

	err := responseCodec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, nil, ResponseModelContext{})
	assertConversionError(t, err, "anthropic_to_response_via_chat", "response_read", FormatAnthropicMessages, FormatOpenAIResponse)
}

func TestOpenAIResponseCodec_WriteResponse_FromAnthropicMessages_NonStream_InvalidJSONWrapped(t *testing.T) {
	responseCodec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, "not-json", nil)
	ctx, _ := newTestContext()

	err := responseCodec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, nil, ResponseModelContext{})
	assertConversionError(t, err, "anthropic_to_response_via_chat", "response_unmarshal", FormatAnthropicMessages, FormatOpenAIResponse)
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIResponse_NonStream_ViaChat(t *testing.T) {
	responsesBody := `{"id":"resp-1","object":"response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

	anthropicCodec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, responsesBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	body := w.Body.String()
	var out dto.ClaudeResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("claude json unmarshal error: %v, body=%s", err, body)
	}
	if out.Type != "message" {
		t.Fatalf("type = %q, want %q", out.Type, "message")
	}
	if len(out.Content) == 0 {
		t.Fatalf("content should not be empty")
	}
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIResponse_NonStream_ReadErrorWrapped(t *testing.T) {
	anthropicCodec := &AnthropicMessagesCodec{}
	resp := &http.Response{StatusCode: http.StatusOK, Body: errReadCloser{}}
	ctx, _ := newTestContext()

	err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{})
	assertConversionError(t, err, "response_to_chat", "response_read", FormatOpenAIResponse, FormatAnthropicMessages)
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIResponse_NonStream_InvalidJSONWrapped(t *testing.T) {
	anthropicCodec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, "not-json", nil)
	ctx, _ := newTestContext()

	err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{})
	assertConversionError(t, err, "response_to_chat", "response_read", FormatOpenAIResponse, FormatAnthropicMessages)
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIResponse_NonStream_ConversionErrorWrapped(t *testing.T) {
	responsesBody := `{"id":"resp-1","object":"response","created_at":1234,"model":"gpt-4o","status":"failed","output":[],"error":{"code":"server_error","message":"Internal error"},"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}`

	anthropicCodec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, responsesBody, nil)
	ctx, _ := newTestContext()

	err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{})
	assertConversionError(t, err, "response_to_chat", "response_conversion", FormatOpenAIResponse, FormatAnthropicMessages)
}

func TestOpenAIResponseCodec_WriteResponse_FromAnthropicMessages_Stream_ViaChat(t *testing.T) {
	responseCodec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, claudeStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := responseCodec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "response.created") {
		t.Fatalf("missing response.created, body=%s", body)
	}
	if !strings.Contains(body, "Hello") {
		t.Fatalf("missing Hello text, body=%s", body)
	}
	if !strings.Contains(body, "response.completed") {
		t.Fatalf("missing response.completed, body=%s", body)
	}
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIResponse_Stream_ViaChat(t *testing.T) {
	responsesStreamBody := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-4o\"}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"data: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hello\"}\n\n" +
		"data: {\"type\":\"response.output_text.done\",\"output_index\":0,\"content_index\":0,\"text\":\"Hello\"}\n\n" +
		"data: {\"type\":\"response.content_part.done\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"Hello\"}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\"}]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-4o\"}}\n\n"

	anthropicCodec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, responsesStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "message_start") {
		t.Fatalf("missing message_start, body=%s", body)
	}
	if !strings.Contains(body, "Hello") {
		t.Fatalf("missing Hello text, body=%s", body)
	}
	if !strings.Contains(body, "message_stop") {
		t.Fatalf("missing message_stop, body=%s", body)
	}
}

func TestOpenAIChatCodec_WriteResponse_FromOpenAIResponse_UnknownOutputItem(t *testing.T) {
	body := `{"id":"resp-4","object":"response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"reasoning","id":"rs-1","content":[]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, _ := newTestContext()

	err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{})
	if err != nil {
		t.Fatalf("expected reasoning output to be supported, got error: %v", err)
	}
}

func TestOpenAIChatCodec_WriteResponse_FromOpenAIResponse_NonStream_Reasoning(t *testing.T) {
	body := `{"id":"resp-4","object":"response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"reasoning","id":"rs-1","summary":[{"type":"summary_text","text":"thinking..."}]},{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()

	err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{})
	if err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if out.Choices[0].Message == nil {
		t.Fatal("message should not be nil")
	}
	if out.Choices[0].Message.ReasoningContent != "thinking..." {
		t.Fatalf("reasoning_content = %q, want %q", out.Choices[0].Message.ReasoningContent, "thinking...")
	}
	if out.Choices[0].Message.Content != "Hello" {
		t.Fatalf("content = %q, want Hello", out.Choices[0].Message.Content)
	}
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIResponse_Stream_ReadErrorWrapped(t *testing.T) {
	anthropicCodec := &AnthropicMessagesCodec{}
	resp := &http.Response{StatusCode: http.StatusOK, Body: errReadCloser{}}
	ctx, _ := newTestContext()

	err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, nil, ResponseModelContext{})
	assertConversionError(t, err, "response_to_chat", "stream_read", FormatOpenAIResponse, FormatAnthropicMessages)
}

func TestOpenAIResponseCodec_WriteResponse_OpenAIResponse_PassthroughStream(t *testing.T) {
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-4o\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hello\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-4o\"}}\n\n"

	codec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	result := w.Body.String()
	if !strings.Contains(result, "response.output_text.delta") {
		t.Fatalf("body should contain response.output_text.delta, got: %s", result)
	}
	if counter.GetOutputTokens() == 0 {
		t.Fatalf("output tokens should be > 0, got %d", counter.GetOutputTokens())
	}
}

// --- Task 8: Anthropic<->Chat stream mapper tests ---

func TestMapClaudeEventToChatChunks_TextDelta(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("chatcmpl-1", "claude-3", 1234)
	// First: message_start to get role chunk
	startEvent := dto.ClaudeStreamEvent{
		Type: "message_start",
		Message: &dto.ClaudeMessageStart{
			ID:    "msg-1",
			Type:  "message",
			Role:  "assistant",
			Model: "claude-3",
		},
	}
	chunks, err := mapper.Map(startEvent)
	if err != nil {
		t.Fatalf("Map message_start error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("message_start: len(chunks) = %d, want 1", len(chunks))
	}
	if chunks[0].Choices[0].Delta.Role != "assistant" {
		t.Fatalf("role = %q, want assistant", chunks[0].Choices[0].Delta.Role)
	}

	// Then: content_block_delta text_delta
	textEvent := dto.ClaudeStreamEvent{
		Type:  "content_block_delta",
		Index: 0,
		Delta: &dto.ClaudeDelta{Type: "text_delta", Text: "Hello"},
	}
	chunks, err = mapper.Map(textEvent)
	if err != nil {
		t.Fatalf("Map text_delta error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("text_delta: len(chunks) = %d, want 1", len(chunks))
	}
	if chunks[0].Choices[0].Delta.Content != "Hello" {
		t.Fatalf("content = %q, want Hello", chunks[0].Choices[0].Delta.Content)
	}
}

func TestMapClaudeEventToChatChunks_MessageStopEmitsFinish(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("chatcmpl-1", "claude-3", 1234)

	chunks, err := mapper.Map(dto.ClaudeStreamEvent{Type: "message_stop"})
	if err != nil {
		t.Fatalf("Map message_stop error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("message_stop: len(chunks) = %d, want 1", len(chunks))
	}
	if chunks[0].Choices[0].FinishReason == nil || *chunks[0].Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason = %v, want stop", chunks[0].Choices[0].FinishReason)
	}
}

func TestMapChatChunkToClaudeEvents_TextDelta(t *testing.T) {
	mapper := newChatToClaudeStreamMapper("")
	chunk := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Object:  "chat.completion.chunk",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Content: "Hello"}}},
	}

	events, err := mapper.Map(chunk)
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	// Should include message_start (lazily), content_block_start, and content_block_delta
	foundDelta := false
	for _, ev := range events {
		if ev.Type == "content_block_delta" && ev.Delta != nil && ev.Delta.Type == "text_delta" && ev.Delta.Text == "Hello" {
			foundDelta = true
		}
	}
	if !foundDelta {
		t.Fatalf("expected content_block_delta with text_delta Hello, got %+v", events)
	}
}

func TestMapChatChunkToClaudeEvents_TextAfterToolUsesAllocatedTextIndex(t *testing.T) {
	mapper := newChatToClaudeStreamMapper("")
	tcIdx0 := 0

	toolChunk := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Object:  "chat.completion.chunk",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{ToolCalls: []dto.ToolCall{{
			Index: &tcIdx0,
			ID:    "call-1",
			Type:  "function",
			Function: dto.ToolCallFunc{
				Name:      "get_weather",
				Arguments: "",
			},
		}}}}},
	}
	if _, err := mapper.Map(toolChunk); err != nil {
		t.Fatalf("Map tool chunk error: %v", err)
	}

	textChunk := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Object:  "chat.completion.chunk",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Content: "Hello"}}},
	}
	events, err := mapper.Map(textChunk)
	if err != nil {
		t.Fatalf("Map text chunk error: %v", err)
	}

	found := false
	for _, ev := range events {
		if ev.Type == "content_block_delta" && ev.Delta != nil && ev.Delta.Type == "text_delta" {
			found = true
			if ev.Index != 1 {
				t.Fatalf("text delta index = %d, want 1", ev.Index)
			}
		}
	}
	if !found {
		t.Fatalf("expected text content_block_delta, got %+v", events)
	}
}

func TestMapChatChunkToClaudeEvents_ToolArgumentContinuationUsesChunkIndex(t *testing.T) {
	tcIdx7 := 7
	mapper := newChatToClaudeStreamMapper("")

	startChunk := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Object:  "chat.completion.chunk",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{ToolCalls: []dto.ToolCall{{
			Index: &tcIdx7,
			ID:    "call-1",
			Type:  "function",
			Function: dto.ToolCallFunc{
				Name:      "get_weather",
				Arguments: "",
			},
		}}}}},
	}
	if _, err := mapper.Map(startChunk); err != nil {
		t.Fatalf("Map tool start chunk error: %v", err)
	}

	contChunk := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Object:  "chat.completion.chunk",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{ToolCalls: []dto.ToolCall{{
			Index:    &tcIdx7,
			Function: dto.ToolCallFunc{Arguments: "{\"city\":\"Beijing\"}"},
		}}}}},
	}
	events, err := mapper.Map(contChunk)
	if err != nil {
		t.Fatalf("Map tool continuation chunk error: %v", err)
	}

	found := false
	for _, ev := range events {
		if ev.Type == "content_block_delta" && ev.Delta != nil && ev.Delta.Type == "input_json_delta" {
			found = true
			if ev.Index != 0 {
				t.Fatalf("tool delta index = %d, want 0", ev.Index)
			}
		}
	}
	if !found {
		t.Fatalf("expected tool input_json_delta, got %+v", events)
	}
}

// --- Task 9: event-by-event bridge tests ---

func waitUntilContains(t *testing.T, w *httptest.ResponseRecorder, substr string) {
	t.Helper()
	// In test context the writes are synchronous, so just check immediately.
	body := w.Body.String()
	if !strings.Contains(body, substr) {
		t.Fatalf("expected body to contain %q, got:\n%s", substr, body)
	}
}

func TestOpenAIResponseCodec_WriteResponse_FromAnthropicMessages_Stream_FirstDeltaForwarded(t *testing.T) {
	// Anthropic stream -> Response SSE: each event should be forwarded immediately (event-by-event).
	responseCodec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, claudeStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := responseCodec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	body := w.Body.String()
	// Must contain response.created
	if !strings.Contains(body, "response.created") {
		t.Fatalf("missing response.created, body=%s", body)
	}
	// Must contain the text delta forwarded
	if !strings.Contains(body, "response.output_text.delta") {
		t.Fatalf("missing response.output_text.delta, body=%s", body)
	}
	if !strings.Contains(body, "Hello") {
		t.Fatalf("missing Hello text, body=%s", body)
	}
	// Must have response.completed
	if !strings.Contains(body, "response.completed") {
		t.Fatalf("missing response.completed, body=%s", body)
	}
	if counter.GetOutputTokens() == 0 {
		t.Fatalf("output tokens should be > 0")
	}
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIResponse_Stream_FirstDeltaForwarded(t *testing.T) {
	// Response SSE -> Anthropic stream: each event should be forwarded immediately (event-by-event).
	responsesStreamBody := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-4o\"}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"data: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hello\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-4o\"}}\n\n"

	anthropicCodec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, responsesStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, counter, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	body := w.Body.String()
	// Must contain message_start
	if !strings.Contains(body, "message_start") {
		t.Fatalf("missing message_start, body=%s", body)
	}
	// Must contain text delta
	if !strings.Contains(body, "content_block_delta") {
		t.Fatalf("missing content_block_delta, body=%s", body)
	}
	if !strings.Contains(body, "Hello") {
		t.Fatalf("missing Hello text, body=%s", body)
	}
	// Must contain message_stop
	if !strings.Contains(body, "message_stop") {
		t.Fatalf("missing message_stop, body=%s", body)
	}
	if counter.GetOutputTokens() == 0 {
		t.Fatalf("output tokens should be > 0")
	}
}

func TestMapResponsesEventToChatChunks_TextDelta(t *testing.T) {
	mapper := newResponsesToChatStreamMapper("resp-1", "gpt-4o", 1234)
	event := dto.ResponsesStreamEvent{Type: "response.output_text.delta", Delta: json.RawMessage(`"Hello"`)}

	chunks, err := mapper.Map(event)
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	if chunks[0].Choices[0].Delta == nil || chunks[0].Choices[0].Delta.Content != "Hello" {
		t.Fatalf("content = %q, want Hello", chunks[0].Choices[0].Delta.Content)
	}
}

func TestMapChatChunkToResponsesEvents_TextDelta(t *testing.T) {
	mapper := newChatToResponsesStreamMapper("resp-1", "gpt-4o")
	chunk := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Object:  "chat.completion.chunk",
		Created: 1234,
		Model:   "gpt-4o",
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Content: "Hello"}}},
	}

	events, err := mapper.Map(chunk)
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type == "response.output_text.delta" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected response.output_text.delta, got %#v", events)
	}
}

// Task 11: OpenAI -> Anthropic Usage fields passthrough tests

func TestConvertOpenAIResponseToAnthropic_UsagePassthrough_CachedTokens(t *testing.T) {
	// Test: OpenAI PromptTokensDetails.CachedTokens -> Claude CacheReadInputTokens
	openAIResp := &dto.ChatCompletionResponse{
		ID:      "chatcmpl-1",
		Object:  "chat.completion",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.Choice{{
			Index: 0,
			Message: &dto.ResMessage{
				Role:    "assistant",
				Content: "Hello",
			},
			FinishReason: ptr("stop"),
		}},
		Usage: dto.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			PromptTokensDetails: &dto.UsageDetails{
				CachedTokens: 80,
			},
		},
	}

	claudeResp := convertOpenAIResponseToAnthropic(openAIResp)

	if claudeResp.Usage.InputTokens != 100 {
		t.Fatalf("InputTokens = %d, want 100", claudeResp.Usage.InputTokens)
	}
	if claudeResp.Usage.OutputTokens != 50 {
		t.Fatalf("OutputTokens = %d, want 50", claudeResp.Usage.OutputTokens)
	}
	if claudeResp.Usage.CacheReadInputTokens != 80 {
		t.Fatalf("CacheReadInputTokens = %d, want 80", claudeResp.Usage.CacheReadInputTokens)
	}
}

func TestConvertOpenAIResponseToAnthropic_UsagePassthrough_NoCachedTokens(t *testing.T) {
	// Test: When PromptTokensDetails is nil or CachedTokens is 0, CacheReadInputTokens should be 0
	openAIResp := &dto.ChatCompletionResponse{
		ID:      "chatcmpl-2",
		Object:  "chat.completion",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.Choice{{
			Index: 0,
			Message: &dto.ResMessage{
				Role:    "assistant",
				Content: "Hello",
			},
			FinishReason: ptr("stop"),
		}},
		Usage: dto.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	claudeResp := convertOpenAIResponseToAnthropic(openAIResp)

	if claudeResp.Usage.CacheReadInputTokens != 0 {
		t.Fatalf("CacheReadInputTokens = %d, want 0", claudeResp.Usage.CacheReadInputTokens)
	}
}

func TestConvertOpenAIResponseToAnthropic_UsagePassthrough_ReasoningTokensIgnored(t *testing.T) {
	// Test: CompletionTokensDetails.ReasoningTokens should be ignored (not mapped)
	openAIResp := &dto.ChatCompletionResponse{
		ID:      "chatcmpl-3",
		Object:  "chat.completion",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.Choice{{
			Index: 0,
			Message: &dto.ResMessage{
				Role:    "assistant",
				Content: "Hello",
			},
			FinishReason: ptr("stop"),
		}},
		Usage: dto.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			CompletionTokensDetails: &dto.UsageDetails{
				ReasoningTokens: 30,
			},
		},
	}

	claudeResp := convertOpenAIResponseToAnthropic(openAIResp)

	// ReasoningTokens should NOT be mapped to any Claude field
	// Just verify basic usage is correct
	if claudeResp.Usage.InputTokens != 100 {
		t.Fatalf("InputTokens = %d, want 100", claudeResp.Usage.InputTokens)
	}
	if claudeResp.Usage.OutputTokens != 50 {
		t.Fatalf("OutputTokens = %d, want 50", claudeResp.Usage.OutputTokens)
	}
}

func TestConvertOpenAIResponseToAnthropic_UsagePassthrough_BothDetails(t *testing.T) {
	// Test: Both PromptTokensDetails and CompletionTokensDetails present
	openAIResp := &dto.ChatCompletionResponse{
		ID:      "chatcmpl-4",
		Object:  "chat.completion",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.Choice{{
			Index: 0,
			Message: &dto.ResMessage{
				Role:    "assistant",
				Content: "Hello",
			},
			FinishReason: ptr("stop"),
		}},
		Usage: dto.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			PromptTokensDetails: &dto.UsageDetails{
				CachedTokens: 80,
			},
			CompletionTokensDetails: &dto.UsageDetails{
				ReasoningTokens: 30,
			},
		},
	}

	claudeResp := convertOpenAIResponseToAnthropic(openAIResp)

	if claudeResp.Usage.CacheReadInputTokens != 80 {
		t.Fatalf("CacheReadInputTokens = %d, want 80", claudeResp.Usage.CacheReadInputTokens)
	}
}

func ptr(s string) *string { return &s }

// ============================================================================
// Task 1: 验证非流式响应中 model 字段使用 RequestedModel
// ============================================================================

func TestWriteClaudeResponseAsOpenAI_UsesRequestedModel(t *testing.T) {
	// Claude 上游响应中 model 是 upstream-model，但请求中的别名是 my-alias
	body := `{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"Hi"}],"model":"upstream-model","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`
	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse error: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "my-alias" {
		t.Errorf("model = %q, want %q", out.Model, "my-alias")
	}
}

func TestWriteOpenAIResponseAsAnthropic_UsesRequestedModel(t *testing.T) {
	// OpenAI 上游响应中 model 是 upstream-model，但请求别名是 my-alias
	body := `{"id":"chatcmpl-1","object":"chat.completion","created":1234,"model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`
	codec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, false, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out dto.ClaudeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse error: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "my-alias" {
		t.Errorf("model = %q, want %q", out.Model, "my-alias")
	}
}

func TestWriteOpenAIChatResponseAsResponses_UsesRequestedModel(t *testing.T) {
	// OpenAI Chat 上游响应转换为 Responses 格式，model 应该是请求别名
	body := `{"id":"chatcmpl-1","object":"chat.completion","created":1234,"model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`
	codec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, false, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse error: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "my-alias" {
		t.Errorf("model = %q, want %q", out.Model, "my-alias")
	}
}

func TestWriteOpenAIResponseAsChatResponse_UsesRequestedModel(t *testing.T) {
	// Responses 上游响应转换为 Chat 格式，model 应该是请求别名
	body := `{"id":"resp-1","object":"response","created_at":1234,"model":"upstream-model","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`
	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse error: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "my-alias" {
		t.Errorf("model = %q, want %q", out.Model, "my-alias")
	}
}

func TestPassThroughResponsesResponse_UsesRequestedModel(t *testing.T) {
	// Responses 直通（passthrough），model 应该是请求别名
	body := `{"id":"resp-1","object":"response","created_at":1234,"model":"upstream-model","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`
	codec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse error: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "my-alias" {
		t.Errorf("model = %q, want %q", out.Model, "my-alias")
	}
}

func TestPassThroughOpenAIResponse_UsesRequestedModel(t *testing.T) {
	// OpenAI Chat 直通，model 应该是请求别名
	body := `{"id":"chatcmpl-1","object":"chat.completion","created":1234,"model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`
	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, false, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse error: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "my-alias" {
		t.Errorf("model = %q, want %q", out.Model, "my-alias")
	}
}

func TestPassThroughAnthropicResponse_UsesRequestedModel(t *testing.T) {
	// Anthropic 直通，model 应该是请求别名
	body := `{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"Hi"}],"model":"upstream-model","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`
	codec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out dto.ClaudeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse error: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "my-alias" {
		t.Errorf("model = %q, want %q", out.Model, "my-alias")
	}
}

// Task 12: Anthropic -> OpenAI Usage fields passthrough tests

func TestConvertClaudeResponseToOpenAI_UsagePassthrough_CacheReadTokens(t *testing.T) {
	// Test: Claude CacheReadInputTokens -> OpenAI PromptTokensDetails.CachedTokens
	claudeResp := &dto.ClaudeResponse{
		ID:   "msg-1",
		Type: "message",
		Role: "assistant",
		Content: []dto.ContentBlock{{
			Type: "text",
			Text: "Hello",
		}},
		Model:      "claude-3",
		StopReason: ptr("end_turn"),
		Usage: dto.ClaudeUsage{
			InputTokens:              100,
			OutputTokens:             50,
			CacheReadInputTokens:     80,
			CacheCreationInputTokens: 10,
		},
	}

	openAIResp := convertClaudeResponseToOpenAI(claudeResp)

	if openAIResp.Usage.PromptTokens != 100 {
		t.Fatalf("PromptTokens = %d, want 100", openAIResp.Usage.PromptTokens)
	}
	if openAIResp.Usage.CompletionTokens != 50 {
		t.Fatalf("CompletionTokens = %d, want 50", openAIResp.Usage.CompletionTokens)
	}
	if openAIResp.Usage.TotalTokens != 150 {
		t.Fatalf("TotalTokens = %d, want 150", openAIResp.Usage.TotalTokens)
	}
	if openAIResp.Usage.PromptTokensDetails == nil {
		t.Fatalf("PromptTokensDetails should not be nil")
	}
	if openAIResp.Usage.PromptTokensDetails.CachedTokens != 80 {
		t.Fatalf("PromptTokensDetails.CachedTokens = %d, want 80", openAIResp.Usage.PromptTokensDetails.CachedTokens)
	}
}

func TestConvertClaudeResponseToOpenAI_UsagePassthrough_NoCacheTokens(t *testing.T) {
	// Test: When CacheReadInputTokens is 0, PromptTokensDetails should not be set (or CachedTokens should be 0)
	claudeResp := &dto.ClaudeResponse{
		ID:   "msg-2",
		Type: "message",
		Role: "assistant",
		Content: []dto.ContentBlock{{
			Type: "text",
			Text: "Hello",
		}},
		Model:      "claude-3",
		StopReason: ptr("end_turn"),
		Usage: dto.ClaudeUsage{
			InputTokens:  100,
			OutputTokens: 50,
		},
	}

	openAIResp := convertClaudeResponseToOpenAI(claudeResp)

	if openAIResp.Usage.PromptTokens != 100 {
		t.Fatalf("PromptTokens = %d, want 100", openAIResp.Usage.PromptTokens)
	}
	// When no cache tokens, PromptTokensDetails should be nil or have 0 cached tokens
	if openAIResp.Usage.PromptTokensDetails != nil && openAIResp.Usage.PromptTokensDetails.CachedTokens != 0 {
		t.Fatalf("PromptTokensDetails.CachedTokens = %d, want 0 or nil", openAIResp.Usage.PromptTokensDetails.CachedTokens)
	}
}

func TestConvertClaudeResponseToOpenAI_UsagePassthrough_CacheCreationIgnored(t *testing.T) {
	// Test: CacheCreationInputTokens should be ignored (not mapped to OpenAI)
	claudeResp := &dto.ClaudeResponse{
		ID:   "msg-3",
		Type: "message",
		Role: "assistant",
		Content: []dto.ContentBlock{{
			Type: "text",
			Text: "Hello",
		}},
		Model:      "claude-3",
		StopReason: ptr("end_turn"),
		Usage: dto.ClaudeUsage{
			InputTokens:              100,
			OutputTokens:             50,
			CacheCreationInputTokens: 20,
		},
	}

	openAIResp := convertClaudeResponseToOpenAI(claudeResp)

	// CacheCreationInputTokens is not mapped to OpenAI
	// Just verify it doesn't affect other fields
	if openAIResp.Usage.PromptTokens != 100 {
		t.Fatalf("PromptTokens = %d, want 100", openAIResp.Usage.PromptTokens)
	}
}

// --- Task 8: Anthropic -> OpenAI Multimodal Response Conversion Tests ---

func TestConvertClaudeResponseToOpenAI_ImageBlock_Base64(t *testing.T) {
	// Test: Claude image block with base64 source -> OpenAI image_url
	claudeResp := &dto.ClaudeResponse{
		ID:   "msg-img-1",
		Type: "message",
		Role: "assistant",
		Content: []dto.ContentBlock{{
			Type: "image",
			Source: &dto.MessageSource{
				Type:      "base64",
				MediaType: "image/jpeg",
				Data:      "base64encodeddata",
			},
		}},
		Model:      "claude-3",
		StopReason: ptr("end_turn"),
		Usage: dto.ClaudeUsage{
			InputTokens:  100,
			OutputTokens: 50,
		},
	}

	openAIResp := convertClaudeResponseToOpenAI(claudeResp)

	if len(openAIResp.Choices) != 1 {
		t.Fatalf("len(choices) = %d, want 1", len(openAIResp.Choices))
	}

	// Content should be empty string for multimodal responses
	if openAIResp.Choices[0].Message.Content != "" {
		t.Fatalf("content = %q, want empty string", openAIResp.Choices[0].Message.Content)
	}
}

func TestConvertClaudeResponseToOpenAI_ImageBlock_URL(t *testing.T) {
	// Test: Claude image block with URL source -> OpenAI image_url
	claudeResp := &dto.ClaudeResponse{
		ID:   "msg-img-2",
		Type: "message",
		Role: "assistant",
		Content: []dto.ContentBlock{{
			Type: "image",
			Source: &dto.MessageSource{
				Type: "url",
				Url:  "https://example.com/image.jpg",
			},
		}},
		Model:      "claude-3",
		StopReason: ptr("end_turn"),
		Usage: dto.ClaudeUsage{
			InputTokens:  100,
			OutputTokens: 50,
		},
	}

	openAIResp := convertClaudeResponseToOpenAI(claudeResp)

	if len(openAIResp.Choices) != 1 {
		t.Fatalf("len(choices) = %d, want 1", len(openAIResp.Choices))
	}

	// Content should be empty string for multimodal responses
	if openAIResp.Choices[0].Message.Content != "" {
		t.Fatalf("content = %q, want empty string", openAIResp.Choices[0].Message.Content)
	}
}

func TestConvertClaudeResponseToOpenAI_DocumentBlock_Base64(t *testing.T) {
	// Test: Claude document block with base64 source -> OpenAI file
	claudeResp := &dto.ClaudeResponse{
		ID:   "msg-doc-1",
		Type: "message",
		Role: "assistant",
		Content: []dto.ContentBlock{{
			Type: "document",
			Source: &dto.MessageSource{
				Type:      "base64",
				MediaType: "application/pdf",
				Data:      "base64pdfdata",
			},
		}},
		Model:      "claude-3",
		StopReason: ptr("end_turn"),
		Usage: dto.ClaudeUsage{
			InputTokens:  100,
			OutputTokens: 50,
		},
	}

	openAIResp := convertClaudeResponseToOpenAI(claudeResp)

	if len(openAIResp.Choices) != 1 {
		t.Fatalf("len(choices) = %d, want 1", len(openAIResp.Choices))
	}

	// Content should be empty string for multimodal responses
	if openAIResp.Choices[0].Message.Content != "" {
		t.Fatalf("content = %q, want empty string", openAIResp.Choices[0].Message.Content)
	}
}

func TestConvertClaudeResponseToOpenAI_MixedContent(t *testing.T) {
	// Test: Mixed content - text + image + document
	claudeResp := &dto.ClaudeResponse{
		ID:   "msg-mixed-1",
		Type: "message",
		Role: "assistant",
		Content: []dto.ContentBlock{
			{
				Type: "text",
				Text: "Here is an image and a document:",
			},
			{
				Type: "image",
				Source: &dto.MessageSource{
					Type:      "base64",
					MediaType: "image/png",
					Data:      "pngdata",
				},
			},
			{
				Type: "document",
				Source: &dto.MessageSource{
					Type:      "base64",
					MediaType: "application/pdf",
					Data:      "pdfdata",
				},
			},
		},
		Model:      "claude-3",
		StopReason: ptr("end_turn"),
		Usage: dto.ClaudeUsage{
			InputTokens:  200,
			OutputTokens: 100,
		},
	}

	openAIResp := convertClaudeResponseToOpenAI(claudeResp)

	if len(openAIResp.Choices) != 1 {
		t.Fatalf("len(choices) = %d, want 1", len(openAIResp.Choices))
	}

	// Text content should contain only the text block
	if openAIResp.Choices[0].Message.Content != "Here is an image and a document:" {
		t.Fatalf("content = %q, want 'Here is an image and a document:'", openAIResp.Choices[0].Message.Content)
	}
}

// --- Task 9: OpenAI → Anthropic Multimodal Response Conversion Tests ---

func TestConvertOpenAIResponseToAnthropic_ImageContent_DataURI(t *testing.T) {
	// Test: OpenAI response with Data URI image in content -> Anthropic image block
	openAIResp := &dto.ChatCompletionResponse{
		ID:      "chatcmpl-img-1",
		Object:  "chat.completion",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.Choice{{
			Index: 0,
			Message: &dto.ResMessage{
				Role:    "assistant",
				Content: "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEASABIAAD",
			},
			FinishReason: ptr("stop"),
		}},
		Usage: dto.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	claudeResp := convertOpenAIResponseToAnthropic(openAIResp)

	if claudeResp.Type != "message" {
		t.Fatalf("type = %q, want message", claudeResp.Type)
	}
	if len(claudeResp.Content) != 1 {
		t.Fatalf("len(content) = %d, want 1", len(claudeResp.Content))
	}
	block := claudeResp.Content[0]
	if block.Type != "image" {
		t.Fatalf("content[0].type = %q, want image", block.Type)
	}
	if block.Source == nil {
		t.Fatalf("content[0].source should not be nil")
	}
	if block.Source.Type != "base64" {
		t.Fatalf("source.type = %q, want base64", block.Source.Type)
	}
	if block.Source.MediaType != "image/jpeg" {
		t.Fatalf("source.media_type = %q, want image/jpeg", block.Source.MediaType)
	}
	if block.Source.Data != "/9j/4AAQSkZJRgABAQEASABIAAD" {
		t.Fatalf("source.data = %q, want /9j/4AAQSkZJRgABAQEASABIAAD", block.Source.Data)
	}
}

func TestConvertOpenAIResponseToAnthropic_FileContent_DataURI(t *testing.T) {
	// Test: OpenAI response with Data URI file (PDF) in content -> Anthropic document block
	openAIResp := &dto.ChatCompletionResponse{
		ID:      "chatcmpl-file-1",
		Object:  "chat.completion",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.Choice{{
			Index: 0,
			Message: &dto.ResMessage{
				Role:    "assistant",
				Content: "data:application/pdf;base64,JVBERi0xLjQKJcOkw7zDtsOWw5PDgMO",
			},
			FinishReason: ptr("stop"),
		}},
		Usage: dto.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	claudeResp := convertOpenAIResponseToAnthropic(openAIResp)

	if len(claudeResp.Content) != 1 {
		t.Fatalf("len(content) = %d, want 1", len(claudeResp.Content))
	}
	block := claudeResp.Content[0]
	if block.Type != "document" {
		t.Fatalf("content[0].type = %q, want document", block.Type)
	}
	if block.Source == nil {
		t.Fatalf("content[0].source should not be nil")
	}
	if block.Source.Type != "base64" {
		t.Fatalf("source.type = %q, want base64", block.Source.Type)
	}
	if block.Source.MediaType != "application/pdf" {
		t.Fatalf("source.media_type = %q, want application/pdf", block.Source.MediaType)
	}
	if block.Source.Data != "JVBERi0xLjQKJcOkw7zDtsOWw5PDgMO" {
		t.Fatalf("source.data = %q, want JVBERi0xLjQKJcOkw7zDtsOWw5PDgMO", block.Source.Data)
	}
}

func TestConvertOpenAIResponseToAnthropic_TextContent(t *testing.T) {
	// Test: Normal text content (not Data URI) -> Anthropic text block
	openAIResp := &dto.ChatCompletionResponse{
		ID:      "chatcmpl-text-1",
		Object:  "chat.completion",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.Choice{{
			Index: 0,
			Message: &dto.ResMessage{
				Role:    "assistant",
				Content: "Hello, this is a normal text response",
			},
			FinishReason: ptr("stop"),
		}},
		Usage: dto.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	claudeResp := convertOpenAIResponseToAnthropic(openAIResp)

	if len(claudeResp.Content) != 1 {
		t.Fatalf("len(content) = %d, want 1", len(claudeResp.Content))
	}
	block := claudeResp.Content[0]
	if block.Type != "text" {
		t.Fatalf("content[0].type = %q, want text", block.Type)
	}
	if block.Text != "Hello, this is a normal text response" {
		t.Fatalf("content[0].text = %q, want 'Hello, this is a normal text response'", block.Text)
	}
}

func TestConvertOpenAIResponseToAnthropic_URLContent(t *testing.T) {
	// Test: HTTP URL in content (not Data URI) -> Anthropic text block (URL as text)
	openAIResp := &dto.ChatCompletionResponse{
		ID:      "chatcmpl-url-1",
		Object:  "chat.completion",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.Choice{{
			Index: 0,
			Message: &dto.ResMessage{
				Role:    "assistant",
				Content: "https://example.com/image.jpg",
			},
			FinishReason: ptr("stop"),
		}},
		Usage: dto.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	claudeResp := convertOpenAIResponseToAnthropic(openAIResp)

	if len(claudeResp.Content) != 1 {
		t.Fatalf("len(content) = %d, want 1", len(claudeResp.Content))
	}
	block := claudeResp.Content[0]
	if block.Type != "text" {
		t.Fatalf("content[0].type = %q, want text", block.Type)
	}
	if block.Text != "https://example.com/image.jpg" {
		t.Fatalf("content[0].text = %q, want 'https://example.com/image.jpg'", block.Text)
	}
}

// ============================================================================
// P1-1: Invalid JSON error path tests for passthrough functions
// ============================================================================

// TestPassThroughOpenAIResponse_InvalidJSON verifies that passThroughOpenAIResponse
// returns an error when the upstream 2xx body is not valid JSON, so the client
// does not receive the unparseable raw body.
func TestPassThroughOpenAIResponse_InvalidJSON(t *testing.T) {
	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, "not-valid-json", map[string]string{"X-Test": "openai"})
	ctx, w := newTestContext()

	err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, false, nil, ResponseModelContext{})
	if err == nil {
		t.Fatalf("expected error for invalid JSON body, got nil")
	}
	// No response body must have been written before the error was returned,
	// so the caller can still write a proper error response.
	if w.Body.String() != "" {
		t.Fatalf("expected empty response body before error, got: %s", w.Body.String())
	}
}

// TestPassThroughAnthropicResponse_InvalidJSON verifies that passThroughAnthropicResponse
// returns an error when the upstream 2xx body is not valid JSON, so the client
// does not receive the unparseable raw body.
func TestPassThroughAnthropicResponse_InvalidJSON(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, "not-valid-json", map[string]string{"X-Test": "anthropic"})
	ctx, w := newTestContext()

	err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, nil, ResponseModelContext{})
	if err == nil {
		t.Fatalf("expected error for invalid JSON body, got nil")
	}
	// No response body must have been written before the error was returned,
	// so the caller can still write a proper error response.
	if w.Body.String() != "" {
		t.Fatalf("expected empty response body before error, got: %s", w.Body.String())
	}
}

// TestPassThroughResponsesResponse_InvalidJSON verifies that passThroughResponsesResponse
// returns an error when the upstream 2xx body is not valid JSON, so the client
// does not receive the unparseable raw body.
func TestPassThroughResponsesResponse_InvalidJSON(t *testing.T) {
	codec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, "not-valid-json", nil)
	ctx, w := newTestContext()

	err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{})
	if err == nil {
		t.Fatalf("expected error for invalid JSON body, got nil")
	}
	if strings.Contains(w.Body.String(), "not-valid-json") {
		t.Fatalf("raw unparseable body must not be forwarded to client, got: %s", w.Body.String())
	}
}

func TestPassThroughResponsesResponse_NonStreamSSEBody(t *testing.T) {
	codec := &OpenAIResponseCodec{}
	sseBody := "" +
		"event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-sse\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-hy3\"}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-sse\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n"
	resp := newResponse(http.StatusOK, sseBody, map[string]string{"Content-Type": "text/event-stream"})
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, counter, ResponseModelContext{RequestedModel: "hy3-ioa"})
	if err != nil {
		t.Fatalf("expected SSE non-stream body to be aggregated, got error: %v", err)
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "hy3-ioa" {
		t.Fatalf("model = %q, want %q", out.Model, "hy3-ioa")
	}
	if got := extractOutputTextFromResponses(&out); got != "ok" {
		t.Fatalf("output text = %q, want %q", got, "ok")
	}
}

func TestWriteOpenAIResponseAsChatResponse_NonStreamSSEBody(t *testing.T) {
	codec := &OpenAIChatCodec{}
	sseBody := "" +
		"event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-sse\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-hy3\"}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-sse\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n"
	resp := newResponse(http.StatusOK, sseBody, map[string]string{"Content-Type": "text/event-stream"})
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, counter, ResponseModelContext{RequestedModel: "hy3-ioa"})
	if err != nil {
		t.Fatalf("expected SSE non-stream body to be aggregated before response->chat conversion, got error: %v", err)
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "hy3-ioa" {
		t.Fatalf("model = %q, want %q", out.Model, "hy3-ioa")
	}
	if len(out.Choices) != 1 || out.Choices[0].Message == nil || out.Choices[0].Message.Content != "ok" {
		t.Fatalf("unexpected chat response: %+v", out)
	}
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIResponse_NonStreamSSEBody(t *testing.T) {
	anthropicCodec := &AnthropicMessagesCodec{}
	sseBody := "" +
		"event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-sse\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-hy3\"}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-sse\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n"
	resp := newResponse(http.StatusOK, sseBody, map[string]string{"Content-Type": "text/event-stream"})
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, counter, ResponseModelContext{RequestedModel: "hy3-ioa"})
	if err != nil {
		t.Fatalf("expected SSE non-stream body to be aggregated before response->anthropic conversion, got error: %v", err)
	}

	var out dto.ClaudeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "hy3-ioa" {
		t.Fatalf("model = %q, want %q", out.Model, "hy3-ioa")
	}
	if out.Type != "message" {
		t.Fatalf("type = %q, want %q", out.Type, "message")
	}
	if len(out.Content) == 0 {
		t.Fatalf("content should not be empty")
	}
}

func TestPassThroughResponsesResponse_NonStreamSSEBody_TokenCount(t *testing.T) {
	codec := &OpenAIResponseCodec{}
	sseBody := "" +
		"event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-sse\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-hy3\"}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello world token counting\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-sse\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n"
	resp := newResponse(http.StatusOK, sseBody, map[string]string{"Content-Type": "text/event-stream"})
	ctx, _ := newTestContext()
	counter := token.NewStreamCounter(0)

	err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, counter, ResponseModelContext{RequestedModel: "hy3-ioa"})
	if err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	if got := counter.GetOutputTokens(); got == 0 {
		t.Fatalf("output tokens should not be 0 after SSE non-stream aggregation")
	}
}

// ============================================================================
// P1-2: Header preservation behavior for passthrough functions
// ============================================================================

// TestPassThroughResponsesResponse_PreservesUpstreamHeaders verifies that
// passthrough Responses responses still forward upstream custom headers after
// rewriting the model field.
func TestPassThroughResponsesResponse_PreservesUpstreamHeaders(t *testing.T) {
	body := `{"id":"resp-1","object":"response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`
	codec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, body, map[string]string{"X-Upstream-Custom": "value"})
	ctx, w := newTestContext()

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	if got := w.Header().Get("X-Upstream-Custom"); got != "value" {
		t.Fatalf("upstream header = %q, want %q", got, "value")
	}
}

func TestPassThroughOpenAIResponse_PreservesUnknownFields(t *testing.T) {
	body := `{"id":"chatcmpl-1","object":"chat.completion","created":1234,"model":"upstream-model","system_fingerprint":"fp_123","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`
	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, false, nil, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse error: %v, body=%s", err, w.Body.String())
	}
	if out["model"] != "my-alias" {
		t.Fatalf("model = %v, want my-alias", out["model"])
	}
	if out["system_fingerprint"] != "fp_123" {
		t.Fatalf("system_fingerprint = %v, want fp_123", out["system_fingerprint"])
	}
}

func TestPassThroughAnthropicResponse_PreservesUnknownFields(t *testing.T) {
	body := `{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"Hi"}],"model":"upstream-model","extra":"keep-me","usage":{"input_tokens":5,"output_tokens":3}}`
	codec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, nil, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse error: %v, body=%s", err, w.Body.String())
	}
	if out["model"] != "my-alias" {
		t.Fatalf("model = %v, want my-alias", out["model"])
	}
	if out["extra"] != "keep-me" {
		t.Fatalf("extra = %v, want keep-me", out["extra"])
	}
}

func TestPassThroughOpenAIStream_PreservesUnknownFields(t *testing.T) {
	streamBody := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"upstream-model\",\"system_fingerprint\":\"fp_123\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n" +
		"data: [DONE]\n\n"
	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, streamBody, nil)
	ctx, w := newTestContext()

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, true, nil, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"system_fingerprint":"fp_123"`) {
		t.Fatalf("missing preserved system_fingerprint, body=%s", body)
	}
	if strings.Contains(body, "upstream-model") {
		t.Fatalf("body should not contain upstream model, body=%s", body)
	}
}

func TestPassThroughAnthropicStream_PreservesUnknownFields(t *testing.T) {
	streamBody := "data: {\"type\":\"message_start\",\"extra\":\"keep-me\",\"message\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"upstream-model\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	codec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, streamBody, nil)
	ctx, w := newTestContext()

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, nil, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"extra":"keep-me"`) {
		t.Fatalf("missing preserved extra field, body=%s", body)
	}
	if strings.Contains(body, "upstream-model") {
		t.Fatalf("body should not contain upstream model, body=%s", body)
	}
}

func TestPassThroughResponsesResponse_AcceptsReasoningSummaryArray(t *testing.T) {
	body := `{"id":"resp-1","object":"response","created_at":1234,"model":"upstream-model","status":"completed","output":[{"type":"reasoning","id":"rs-1","summary":[{"type":"summary_text","text":"thinking..."}]}],"extra":"keep-me","usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`
	codec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse error: %v, body=%s", err, w.Body.String())
	}
	var model string
	if err := json.Unmarshal(out["model"], &model); err != nil {
		t.Fatalf("model parse error: %v", err)
	}
	if model != "my-alias" {
		t.Fatalf("model = %q, want my-alias", model)
	}
	var extra string
	if err := json.Unmarshal(out["extra"], &extra); err != nil {
		t.Fatalf("extra parse error: %v", err)
	}
	if extra != "keep-me" {
		t.Fatalf("extra = %q, want keep-me", extra)
	}
	var rawOutput []map[string]json.RawMessage
	if err := json.Unmarshal(out["output"], &rawOutput); err != nil {
		t.Fatalf("output parse error: %v", err)
	}
	if len(rawOutput) != 1 {
		t.Fatalf("len(output) = %d, want 1", len(rawOutput))
	}
	var summary []map[string]any
	if err := json.Unmarshal(rawOutput[0]["summary"], &summary); err != nil {
		t.Fatalf("summary parse error: %v", err)
	}
	if len(summary) != 1 {
		t.Fatalf("len(summary) = %d, want 1", len(summary))
	}
}

// ============================================================================
// Task 2: Stream paths must emit RequestedModel
// ============================================================================

// TestWriteClaudeStreamAsOpenAI_UsesRequestedModel verifies that a Claude (Anthropic)
// upstream stream is converted to OpenAI Chat chunks in which every model field equals
// the RequestedModel alias, not the upstream model carried in message_start.
func TestWriteClaudeStreamAsOpenAI_UsesRequestedModel(t *testing.T) {
	streamBody := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"upstream-claude\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, streamBody, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	// Every data chunk must have model == "my-alias" and NOT "upstream-claude"
	if strings.Contains(body, "upstream-claude") {
		t.Errorf("stream output must not contain upstream model 'upstream-claude', body=%s", body)
	}
	if !strings.Contains(body, "my-alias") {
		t.Errorf("stream output must contain requested model 'my-alias', body=%s", body)
	}
}

// TestWriteOpenAIStreamAsAnthropic_UsesRequestedModel verifies that an OpenAI Chat upstream
// stream converted to Anthropic SSE uses RequestedModel in the message_start event.
func TestWriteOpenAIStreamAsAnthropic_UsesRequestedModel(t *testing.T) {
	streamBody := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"upstream-gpt\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"upstream-gpt\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"upstream-gpt\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	codec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, streamBody, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, true, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, "upstream-gpt") {
		t.Errorf("stream output must not contain upstream model 'upstream-gpt', body=%s", body)
	}
	if !strings.Contains(body, "my-alias") {
		t.Errorf("stream output must contain requested model 'my-alias', body=%s", body)
	}
}

// TestPassThroughResponsesStream_UsesRequestedModel verifies that a direct Responses API
// stream passthrough rewrites model in response.created and response.completed events.
func TestPassThroughResponsesStream_UsesRequestedModel(t *testing.T) {
	streamBody := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-gpt\",\"extra\":\"keep-me\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Hi\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-gpt\",\"extra\":\"keep-me\"}}\n\n"

	codec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, streamBody, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, "upstream-gpt") {
		t.Errorf("stream output must not contain upstream model 'upstream-gpt', body=%s", body)
	}
	if !strings.Contains(body, "my-alias") {
		t.Errorf("stream output must contain requested model 'my-alias', body=%s", body)
	}
	if !strings.Contains(body, `"extra":"keep-me"`) {
		t.Errorf("stream output must preserve nested extra fields, body=%s", body)
	}
}

// TestWriteOpenAIChatStreamAsResponses_UsesRequestedModel verifies that an OpenAI Chat
// upstream stream converted to Responses SSE uses RequestedModel in response.created and
// response.completed events.
func TestWriteOpenAIChatStreamAsResponses_UsesRequestedModel(t *testing.T) {
	chatStreamBody := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"upstream-gpt\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"upstream-gpt\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"upstream-gpt\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	codec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, chatStreamBody, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, true, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, "upstream-gpt") {
		t.Errorf("stream output must not contain upstream model 'upstream-gpt', body=%s", body)
	}
	if !strings.Contains(body, "my-alias") {
		t.Errorf("stream output must contain requested model 'my-alias', body=%s", body)
	}
}

// TestWriteOpenAIResponseStreamAsChatStream_UsesRequestedModel verifies that a Responses API
// upstream stream converted to OpenAI Chat SSE uses RequestedModel in every chunk.
func TestWriteOpenAIResponseStreamAsChatStream_UsesRequestedModel(t *testing.T) {
	responsesStreamBody := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-gpt\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hi\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-gpt\"}}\n\n"

	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, responsesStreamBody, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, "upstream-gpt") {
		t.Errorf("stream output must not contain upstream model 'upstream-gpt', body=%s", body)
	}
	if !strings.Contains(body, "my-alias") {
		t.Errorf("stream output must contain requested model 'my-alias', body=%s", body)
	}
}

// TestWriteAnthropicStreamAsResponsesStream_UsesRequestedModel verifies that an Anthropic
// upstream stream converted to Responses SSE uses RequestedModel in response.created and
// response.completed events.
func TestWriteAnthropicStreamAsResponsesStream_UsesRequestedModel(t *testing.T) {
	streamBody := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"upstream-claude\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	codec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, streamBody, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, "upstream-claude") {
		t.Errorf("stream output must not contain upstream model 'upstream-claude', body=%s", body)
	}
	if !strings.Contains(body, "my-alias") {
		t.Errorf("stream output must contain requested model 'my-alias', body=%s", body)
	}
}

// TestWriteResponsesStreamAsClaudeStream_UsesRequestedModel verifies that a Responses API
// upstream stream converted to Anthropic SSE uses RequestedModel in the message_start event.
func TestWriteResponsesStreamAsClaudeStream_UsesRequestedModel(t *testing.T) {
	responsesStreamBody := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-gpt\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hi\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-gpt\"}}\n\n"

	codec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, responsesStreamBody, nil)
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, "upstream-gpt") {
		t.Errorf("stream output must not contain upstream model 'upstream-gpt', body=%s", body)
	}
	if !strings.Contains(body, "my-alias") {
		t.Errorf("stream output must contain requested model 'my-alias', body=%s", body)
	}
}

// TestPassThroughOpenAIStream_UsesRequestedModel verifies that a direct OpenAI Chat stream
// passthrough rewrites model in each chunk.
func TestPassThroughOpenAIStream_UsesRequestedModel(t *testing.T) {
	streamBody := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"upstream-gpt\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"upstream-gpt\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n" +
		"data: [DONE]\n\n"

	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, true, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, "upstream-gpt") {
		t.Errorf("stream output must not contain upstream model 'upstream-gpt', body=%s", body)
	}
	if !strings.Contains(body, "my-alias") {
		t.Errorf("stream output must contain requested model 'my-alias', body=%s", body)
	}
}

// TestPassThroughOpenAIStream_ConvertsOpenCodeInferenceCost verifies that OpenCode's
// private inference-cost trailer is not forwarded, and is rewritten as a standard
// usage chunk that strict clients can deserialize.
func TestPassThroughOpenAIStream_ConvertsOpenCodeInferenceCost(t *testing.T) {
	streamBody := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"upstream-gpt\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[],\"cost\":\"0.00009884\",\"model\":\"upstream-gpt\",\"normalizedUsage\":{\"inputTokens\":584,\"outputTokens\":61,\"reasoningTokens\":17,\"cacheReadTokens\":3,\"cacheWrite5mTokens\":0,\"cacheWrite1hTokens\":0},\"x-opencode-type\":\"inference-cost\"}\n\n" +
		"data: [DONE]\n\n"

	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "coding-low"}

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, true, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, "x-opencode-type") || strings.Contains(body, "normalizedUsage") || strings.Contains(body, `"cost"`) {
		t.Fatalf("private opencode fields must not be forwarded, body=%s", body)
	}
	if !strings.Contains(body, `"prompt_tokens":584`) {
		t.Fatalf("expected standard prompt_tokens from normalizedUsage, body=%s", body)
	}
	if !strings.Contains(body, `"completion_tokens":61`) {
		t.Fatalf("expected standard completion_tokens from normalizedUsage, body=%s", body)
	}
	if !strings.Contains(body, `"total_tokens":645`) {
		t.Fatalf("expected total_tokens = input+output, body=%s", body)
	}
	if !strings.Contains(body, `"cached_tokens":3`) {
		t.Fatalf("expected prompt_tokens_details.cached_tokens, body=%s", body)
	}
	if !strings.Contains(body, `"reasoning_tokens":17`) {
		t.Fatalf("expected completion_tokens_details.reasoning_tokens, body=%s", body)
	}
	if !strings.Contains(body, `"id":"chatcmpl-1"`) {
		t.Fatalf("usage chunk must reuse last chunk id, body=%s", body)
	}
	if !strings.Contains(body, `"model":"coding-low"`) {
		t.Fatalf("usage chunk must use requested model, body=%s", body)
	}

	// Ensure the converted usage line is itself a valid ChatCompletionChunk with required id.
	var sawUsage bool
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" || data == "" {
			continue
		}
		var chunk dto.ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("client-facing chunk must unmarshal: %v data=%s", err, data)
		}
		if chunk.ID == "" {
			t.Fatalf("client-facing chunk missing required id: %s", data)
		}
		if chunk.Usage != nil {
			sawUsage = true
		}
	}
	if !sawUsage {
		t.Fatalf("expected a usage chunk in stream, body=%s", body)
	}
}

// TestPassThroughOpenAIStream_DropsOpenCodeInferenceCostWithoutBaseChunk drops the
// private event when there is no prior chunk to supply required id/object fields.
func TestPassThroughOpenAIStream_DropsOpenCodeInferenceCostWithoutBaseChunk(t *testing.T) {
	streamBody := "data: {\"choices\":[],\"cost\":\"0.01\",\"model\":\"upstream\",\"normalizedUsage\":{\"inputTokens\":1,\"outputTokens\":1,\"reasoningTokens\":0,\"cacheReadTokens\":0},\"x-opencode-type\":\"inference-cost\"}\n\n" +
		"data: [DONE]\n\n"

	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	ctx, w := newTestContext()

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, true, nil, ResponseModelContext{RequestedModel: "coding-low"}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, "x-opencode-type") || strings.Contains(body, "normalizedUsage") {
		t.Fatalf("private event must be dropped, body=%s", body)
	}
	if strings.Contains(body, "prompt_tokens") {
		t.Fatalf("should not invent usage without base chunk id, body=%s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream must still end with [DONE], body=%s", body)
	}
}

// TestPassThroughAnthropicStream_UsesRequestedModel verifies that a direct Anthropic stream
// passthrough rewrites model in the message_start event.
func TestPassThroughAnthropicStream_UsesRequestedModel(t *testing.T) {
	streamBody := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"upstream-claude\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	codec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	ctx, w := newTestContext()
	rmc := ResponseModelContext{RequestedModel: "my-alias"}

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, nil, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, "upstream-claude") {
		t.Errorf("stream output must not contain upstream model 'upstream-claude', body=%s", body)
	}
	if !strings.Contains(body, "my-alias") {
		t.Errorf("stream output must contain requested model 'my-alias', body=%s", body)
	}
}

// ============================================================================
// 新增 Response -> Chat 对齐测试（test-first）
// ============================================================================

// TestConvertOpenAIResponseToChat_MapsUsageDetails 验证 usage details 映射：
// input_tokens_details.cached_tokens/image_tokens/audio_tokens -> PromptTokensDetails
// completion_tokens_details.reasoning_tokens -> CompletionTokensDetails.ReasoningTokens
func TestConvertOpenAIResponseToChat_MapsUsageDetails(t *testing.T) {
	body := `{"id":"resp-usage","object":"response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":100,"output_tokens":50,"total_tokens":0,"input_tokens_details":{"cached_tokens":30,"image_tokens":10,"audio_tokens":5},"completion_tokens_details":{"reasoning_tokens":20}}}`

	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse error: %v, body=%s", err, w.Body.String())
	}

	// 基础 usage 映射
	if out.Usage.PromptTokens != 100 {
		t.Fatalf("PromptTokens = %d, want 100", out.Usage.PromptTokens)
	}
	if out.Usage.CompletionTokens != 50 {
		t.Fatalf("CompletionTokens = %d, want 50", out.Usage.CompletionTokens)
	}
	// TotalTokens 为 0 时采用总和兜底
	if out.Usage.TotalTokens != 150 {
		t.Fatalf("TotalTokens = %d, want 150", out.Usage.TotalTokens)
	}

	// Usage details 映射
	if out.Usage.PromptTokensDetails == nil {
		t.Fatal("PromptTokensDetails should not be nil")
	}
	if out.Usage.PromptTokensDetails.CachedTokens != 30 {
		t.Fatalf("PromptTokensDetails.CachedTokens = %d, want 30", out.Usage.PromptTokensDetails.CachedTokens)
	}
	if out.Usage.PromptTokensDetails.ImageTokens != 10 {
		t.Fatalf("PromptTokensDetails.ImageTokens = %d, want 10", out.Usage.PromptTokensDetails.ImageTokens)
	}
	if out.Usage.PromptTokensDetails.AudioTokens != 5 {
		t.Fatalf("PromptTokensDetails.AudioTokens = %d, want 5", out.Usage.PromptTokensDetails.AudioTokens)
	}

	if out.Usage.CompletionTokensDetails == nil {
		t.Fatal("CompletionTokensDetails should not be nil")
	}
	if out.Usage.CompletionTokensDetails.ReasoningTokens != 20 {
		t.Fatalf("CompletionTokensDetails.ReasoningTokens = %d, want 20", out.Usage.CompletionTokensDetails.ReasoningTokens)
	}
}

// TestConvertOpenAIResponseToChat_FiltersMessageRoleAndFallsBackToAnyText 验证两段文本提取：
// 第一段：只取 output[].type=="message" 且 role 为空或 assistant 的文本；
// 第一段没拿到任何文本时，第二段 fallback 遍历全部 output 的 content[].text。
func TestConvertOpenAIResponseToChat_FiltersMessageRoleAndFallsBackToAnyText(t *testing.T) {
	t.Run("message role filter excludes non-assistant roles", func(t *testing.T) {
		// role="system" 的 message 应该被过滤掉
		body := `{"id":"resp-1","object":"response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"system","content":[{"type":"output_text","text":"system text"}]},{"type":"message","id":"msg-2","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

		codec := &OpenAIChatCodec{}
		resp := newResponse(http.StatusOK, body, nil)
		ctx, w := newTestContext()

		if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{}); err != nil {
			t.Fatalf("WriteResponse error: %v", err)
		}

		var out dto.ChatCompletionResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("parse error: %v", err)
		}
		// 应该只包含 assistant message 的文本，不包含 system message 的文本
		if out.Choices[0].Message.Content != "Hello" {
			t.Fatalf("content = %q, want Hello", out.Choices[0].Message.Content)
		}
	})

	t.Run("fallback to any output text when no assistant message found", func(t *testing.T) {
		// 没有 assistant message，应该 fallback 到所有 output 的 text
		body := `{"id":"resp-2","object":"response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"reasoning","id":"rs-1","content":[{"type":"output_text","text":"think step"}]},{"type":"message","id":"msg-1","status":"completed","role":"system","content":[{"type":"output_text","text":"system text"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

		codec := &OpenAIChatCodec{}
		resp := newResponse(http.StatusOK, body, nil)
		ctx, w := newTestContext()

		if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{}); err != nil {
			t.Fatalf("WriteResponse error: %v", err)
		}

		var out dto.ChatCompletionResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("parse error: %v", err)
		}
		// fallback 应该包含所有 output content 的 text
		if out.Choices[0].Message.Content != "think stepsystem text" {
			t.Fatalf("fallback content = %q, want think stepsystem text", out.Choices[0].Message.Content)
		}
	})
}

// TestConvertOpenAIResponseToChat_UsesToolCallsFinishReasonEvenWithText 验证：
// 1. 文本和 tool call 共存时 finish_reason 仍然是 "tool_calls"
// 2. 空 call_id 回退到 item.id
// 3. 空 name 的 tool call 跳过
func TestConvertOpenAIResponseToChat_UsesToolCallsFinishReasonEvenWithText(t *testing.T) {
	t.Run("tool calls finish reason wins over text", func(t *testing.T) {
		// 有 message 文本 + function_call，finish_reason 应该为 tool_calls
		body := `{"id":"resp-3","object":"response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Let me check the weather."}]},{"type":"function_call","id":"fc-1","call_id":"call-1","name":"get_weather","arguments":"{\"city\":\"Beijing\"}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

		codec := &OpenAIChatCodec{}
		resp := newResponse(http.StatusOK, body, nil)
		ctx, w := newTestContext()

		if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{}); err != nil {
			t.Fatalf("WriteResponse error: %v", err)
		}

		var out dto.ChatCompletionResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("parse error: %v", err)
		}

		// 文本应该存在
		if out.Choices[0].Message.Content != "Let me check the weather." {
			t.Fatalf("content = %q, want Let me check the weather.", out.Choices[0].Message.Content)
		}
		// 但 finish_reason 应该为 tool_calls（因为存在 tool call）
		if out.Choices[0].FinishReason == nil || *out.Choices[0].FinishReason != "tool_calls" {
			t.Fatalf("finish_reason = %v, want tool_calls", out.Choices[0].FinishReason)
		}

		// tool call 存在
		if len(out.Choices[0].Message.ToolCalls) != 1 {
			t.Fatalf("len(tool_calls) = %d, want 1", len(out.Choices[0].Message.ToolCalls))
		}
		if out.Choices[0].Message.ToolCalls[0].Function.Name != "get_weather" {
			t.Fatalf("tool_call name = %q, want get_weather", out.Choices[0].Message.ToolCalls[0].Function.Name)
		}
	})

	t.Run("empty call_id falls back to item id", func(t *testing.T) {
		// call_id 为空，应该用 item.id 作为 tool call id
		body := `{"id":"resp-4","object":"response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"function_call","id":"fc-1","call_id":"","name":"get_weather","arguments":"{\"city\":\"Beijing\"}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

		codec := &OpenAIChatCodec{}
		resp := newResponse(http.StatusOK, body, nil)
		ctx, w := newTestContext()

		if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{}); err != nil {
			t.Fatalf("WriteResponse error: %v", err)
		}

		var out dto.ChatCompletionResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("parse error: %v", err)
		}

		if len(out.Choices[0].Message.ToolCalls) != 1 {
			t.Fatalf("len(tool_calls) = %d, want 1", len(out.Choices[0].Message.ToolCalls))
		}
		// call_id 为空时回退到 item.id: "fc-1"
		if out.Choices[0].Message.ToolCalls[0].ID != "fc-1" {
			t.Fatalf("tool_call id = %q, want fc-1 (fallback to item id)", out.Choices[0].Message.ToolCalls[0].ID)
		}
	})

	t.Run("empty name tool call is skipped", func(t *testing.T) {
		// name 为空的 function_call 应该跳过
		body := `{"id":"resp-5","object":"response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"","arguments":"{\"city\":\"Beijing\"}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

		codec := &OpenAIChatCodec{}
		resp := newResponse(http.StatusOK, body, nil)
		ctx, w := newTestContext()

		if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{}); err != nil {
			t.Fatalf("WriteResponse error: %v", err)
		}

		var out dto.ChatCompletionResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("parse error: %v", err)
		}

		// 空 name 的 tool call 应该被跳过，没有 tool calls
		if len(out.Choices[0].Message.ToolCalls) != 0 {
			t.Fatalf("len(tool_calls) = %d, want 0 (empty name should be skipped)", len(out.Choices[0].Message.ToolCalls))
		}
		// finish_reason 应该为 stop（因为没有有效 tool call）
		if out.Choices[0].FinishReason == nil || *out.Choices[0].FinishReason != "stop" {
			t.Fatalf("finish_reason = %v, want stop", out.Choices[0].FinishReason)
		}
	})
}

func TestConvertOpenAIResponseToChat_PreservesRefusalWhenNoOutputText(t *testing.T) {
	body := `{"id":"resp-refusal","object":"response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"refusal","text":"cannot comply"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil, ResponseModelContext{}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if out.Choices[0].Message.Content != "cannot comply" {
		t.Fatalf("content = %q, want cannot comply", out.Choices[0].Message.Content)
	}
}

// Anthropic 流带 message_start.usage + message_delta.usage，验证 Chat 写回 IncludeUsage 门闩
const claudeStreamWithUsageBody = "" +
	"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-3\",\"content\":[],\"usage\":{\"input_tokens\":100,\"output_tokens\":0}}}\n\n" +
	"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
	"data: {\"type\":\"message_stop\"}\n\n"

func TestOpenAIChatCodec_WriteResponse_FromAnthropic_IncludeUsageTrue(t *testing.T) {
	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, claudeStreamWithUsageBody, nil)
	ctx, w := newTestContext()

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, nil, ResponseModelContext{
		RequestedModel: "alias-model",
		IncludeUsage:   true,
	}); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"prompt_tokens":100`) {
		t.Fatalf("IncludeUsage=true should emit usage, body=%s", body)
	}
	if !strings.Contains(body, `"completion_tokens":3`) {
		t.Fatalf("missing completion_tokens, body=%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("missing [DONE], body=%s", body)
	}
	// base finish chunk 不应残留 usage 字段混在 choices 上之后仍合理；usage-only 的 choices 为空
	if !strings.Contains(body, `"choices":[]`) {
		t.Fatalf("expected usage-only chunk with empty choices, body=%s", body)
	}
}

func TestOpenAIChatCodec_WriteResponse_FromAnthropic_IncludeUsageFalse(t *testing.T) {
	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, claudeStreamWithUsageBody, nil)
	ctx, w := newTestContext()

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, nil, ResponseModelContext{
		RequestedModel: "alias-model",
		IncludeUsage:   false,
	}); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	body := w.Body.String()
	if strings.Contains(body, `"prompt_tokens"`) || strings.Contains(body, `"completion_tokens"`) {
		t.Fatalf("IncludeUsage=false must not emit usage fields, body=%s", body)
	}
	if strings.Contains(body, `"choices":[]`) {
		t.Fatalf("IncludeUsage=false must not emit empty choices usage-only chunk, body=%s", body)
	}
	if !strings.Contains(body, "Hi") {
		t.Fatalf("content should still stream, body=%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("missing [DONE], body=%s", body)
	}
}

// Responses 流带 response.completed.usage，验证 Chat 写回 IncludeUsage 门闩
const responsesStreamWithUsageBody = "" +
	"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-u\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-4o\"}}\n\n" +
	"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg-u\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
	"data: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hi\"}\n\n" +
	"data: {\"type\":\"response.output_text.done\",\"output_index\":0,\"content_index\":0,\"text\":\"Hi\"}\n\n" +
	"data: {\"type\":\"response.content_part.done\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"Hi\"}}\n\n" +
	"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg-u\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hi\"}]}}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-u\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-4o\",\"usage\":{\"input_tokens\":80,\"output_tokens\":4,\"total_tokens\":84,\"input_tokens_details\":{\"cached_tokens\":10},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}}\n\n"

func TestOpenAIChatCodec_WriteResponse_FromResponses_IncludeUsageTrue(t *testing.T) {
	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, responsesStreamWithUsageBody, nil)
	ctx, w := newTestContext()

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, nil, ResponseModelContext{
		RequestedModel: "alias-model",
		IncludeUsage:   true,
	}); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"prompt_tokens":80`) {
		t.Fatalf("IncludeUsage=true should emit usage, body=%s", body)
	}
	if !strings.Contains(body, `"completion_tokens":4`) {
		t.Fatalf("missing completion_tokens, body=%s", body)
	}
	if !strings.Contains(body, `"choices":[]`) {
		t.Fatalf("expected usage-only chunk with empty choices, body=%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("missing [DONE], body=%s", body)
	}
}

func TestOpenAIChatCodec_WriteResponse_FromResponses_IncludeUsageFalse(t *testing.T) {
	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, responsesStreamWithUsageBody, nil)
	ctx, w := newTestContext()

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, nil, ResponseModelContext{
		RequestedModel: "alias-model",
		IncludeUsage:   false,
	}); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	body := w.Body.String()
	if strings.Contains(body, `"prompt_tokens"`) || strings.Contains(body, `"completion_tokens"`) {
		t.Fatalf("IncludeUsage=false must not emit usage fields, body=%s", body)
	}
	if strings.Contains(body, `"choices":[]`) {
		t.Fatalf("IncludeUsage=false must not emit empty choices usage-only chunk, body=%s", body)
	}
	if !strings.Contains(body, "Hi") {
		t.Fatalf("content should still stream, body=%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("missing [DONE], body=%s", body)
	}
}
