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

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, false, counter); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusCreated)
	}
	if w.Header().Get("X-Test") != "openai" {
		t.Fatalf("header not preserved")
	}
	if w.Body.String() != openAINonStreamBody {
		t.Fatalf("body mismatch")
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

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, counter); err != nil {
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

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, nil); err == nil {
		t.Fatalf("expected error for invalid json")
	}
}

func TestAnthropicMessagesCodec_WriteResponse_NonStream(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusAccepted, claudeNonStreamBody, map[string]string{"X-Test": "anthropic"})
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, counter); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	if w.Header().Get("X-Test") != "anthropic" {
		t.Fatalf("header not preserved")
	}
	if w.Body.String() != claudeNonStreamBody {
		t.Fatalf("body mismatch")
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

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, false, counter); err != nil {
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

	if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, false, nil); err == nil {
		t.Fatalf("expected error for invalid json")
	}
}

func TestOpenAIChatCodec_WriteResponse_Stream(t *testing.T) {
	codec := &OpenAIChatCodec{}

	t.Run("pass-through", func(t *testing.T) {
		resp := newResponse(http.StatusOK, openAIStreamBody, map[string]string{"Content-Type": "text/event-stream"})
		ctx, w := newTestContext()
		counter := token.NewStreamCounter(0)

		if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, true, counter); err != nil {
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

		if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, counter); err != nil {
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

	if err := c.WriteResponse(ctx, FormatOpenAIChat, resp, false, counter); err != nil {
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

func TestOpenAIResponseCodec_WriteResponse_FromOpenAIChat_NonStream_Tools(t *testing.T) {
	body := `{"id":"chatcmpl-2","object":"chat.completion","created":1234,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Beijing\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}}`

	c := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := c.WriteResponse(ctx, FormatOpenAIChat, resp, false, counter); err != nil {
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

	if err := c.WriteResponse(ctx, FormatOpenAIChat, resp, false, nil); err == nil {
		t.Fatalf("expected error for empty choices")
	}
}

func TestOpenAIChatCodec_WriteResponse_FromOpenAIResponse_NonStream_Text(t *testing.T) {
	body := `{"id":"resp-1","object":"realtime.response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

	codec := &OpenAIChatCodec{}
	resp := newResponse(http.StatusOK, body, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, counter); err != nil {
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

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, counter); err != nil {
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

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil); err == nil {
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

	if err := c.WriteResponse(ctx, FormatOpenAIChat, resp, true, counter); err != nil {
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

	if err := c.WriteResponse(ctx, FormatOpenAIChat, resp, true, counter); err != nil {
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

	if err := c.WriteResponse(ctx, FormatOpenAIChat, resp, true, counter); err != nil {
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

	if err := chatCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, counter); err != nil {
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

	if err := chatCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, counter); err != nil {
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

	err := chatCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, nil)
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

		if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, counter); err != nil {
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

		if err := codec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, nil); err != nil {
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

		if err := codec.WriteResponse(ctx, FormatOpenAIChat, resp, true, counter); err != nil {
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

	if err := responseCodec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, counter); err != nil {
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

	err := responseCodec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, nil)
	assertConversionError(t, err, "anthropic_to_response_via_chat", "response_read", FormatAnthropicMessages, FormatOpenAIResponse)
}

func TestOpenAIResponseCodec_WriteResponse_FromAnthropicMessages_NonStream_InvalidJSONWrapped(t *testing.T) {
	responseCodec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, "not-json", nil)
	ctx, _ := newTestContext()

	err := responseCodec.WriteResponse(ctx, FormatAnthropicMessages, resp, false, nil)
	assertConversionError(t, err, "anthropic_to_response_via_chat", "response_unmarshal", FormatAnthropicMessages, FormatOpenAIResponse)
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIResponse_NonStream_ViaChat(t *testing.T) {
	responsesBody := `{"id":"resp-1","object":"response","created_at":1234,"model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

	anthropicCodec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, responsesBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, counter); err != nil {
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

	err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil)
	assertConversionError(t, err, "response_to_chat", "response_read", FormatOpenAIResponse, FormatAnthropicMessages)
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIResponse_NonStream_InvalidJSONWrapped(t *testing.T) {
	anthropicCodec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, "not-json", nil)
	ctx, _ := newTestContext()

	err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil)
	assertConversionError(t, err, "response_to_chat", "response_unmarshal", FormatOpenAIResponse, FormatAnthropicMessages)
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIResponse_NonStream_ConversionErrorWrapped(t *testing.T) {
	responsesBody := `{"id":"resp-1","object":"response","created_at":1234,"model":"gpt-4o","status":"failed","output":[],"error":{"code":"server_error","message":"Internal error"},"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}`

	anthropicCodec := &AnthropicMessagesCodec{}
	resp := newResponse(http.StatusOK, responsesBody, nil)
	ctx, _ := newTestContext()

	err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil)
	assertConversionError(t, err, "response_to_chat", "response_conversion", FormatOpenAIResponse, FormatAnthropicMessages)
}

func TestOpenAIResponseCodec_WriteResponse_FromAnthropicMessages_Stream_ViaChat(t *testing.T) {
	responseCodec := &OpenAIResponseCodec{}
	resp := newResponse(http.StatusOK, claudeStreamBody, nil)
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := responseCodec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, counter); err != nil {
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

	if err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, counter); err != nil {
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

	err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, false, nil)
	if err == nil {
		t.Fatalf("expected error for unknown output item type 'reasoning', got nil")
	}
}

func TestAnthropicMessagesCodec_WriteResponse_FromOpenAIResponse_Stream_ReadErrorWrapped(t *testing.T) {
	anthropicCodec := &AnthropicMessagesCodec{}
	resp := &http.Response{StatusCode: http.StatusOK, Body: errReadCloser{}}
	ctx, _ := newTestContext()

	err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, nil)
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

	if err := codec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, counter); err != nil {
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
	mapper := newChatToClaudeStreamMapper()
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
	mapper := newChatToClaudeStreamMapper()

	toolChunk := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Object:  "chat.completion.chunk",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{ToolCalls: []dto.ToolCall{{
			Index: 0,
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
	mapper := newChatToClaudeStreamMapper()

	startChunk := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Object:  "chat.completion.chunk",
		Created: 1234,
		Model:   "gpt-4",
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{ToolCalls: []dto.ToolCall{{
			Index: 7,
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
			Index: 7,
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

	if err := responseCodec.WriteResponse(ctx, FormatAnthropicMessages, resp, true, counter); err != nil {
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

	if err := anthropicCodec.WriteResponse(ctx, FormatOpenAIResponse, resp, true, counter); err != nil {
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
