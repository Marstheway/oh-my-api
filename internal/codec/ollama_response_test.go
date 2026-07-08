package codec

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
)

// ============================================================================
// convertOllamaChatResponseToOpenAIChat — 非流式 JSON 转换
// ============================================================================

func TestOllamaChatResponse_TextOnly(t *testing.T) {
	input := dto.OllamaChatResponse{
		Model:     "llama3",
		Done:      true,
		DoneReason: "stop",
		Message: dto.OllamaChatResponseMessage{
			Role:    "assistant",
			Content: "Hello, world!",
		},
		PromptEvalCount: 10,
		EvalCount:       5,
	}

	resp, err := convertOllamaChatResponseToOpenAIChat(&input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Model != "llama3" {
		t.Errorf("model = %q, want llama3", resp.Model)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices len = %d, want 1", len(resp.Choices))
	}
	if resp.Choices[0].Message == nil {
		t.Fatal("message is nil")
	}
	if resp.Choices[0].Message.Content != "Hello, world!" {
		t.Errorf("content = %q, want Hello, world!", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].FinishReason == nil || *resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %v, want stop", resp.Choices[0].FinishReason)
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("prompt_tokens = %d, want 10", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 5 {
		t.Errorf("completion_tokens = %d, want 5", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("total_tokens = %d, want 15", resp.Usage.TotalTokens)
	}
}

func TestOllamaChatResponse_Thinking(t *testing.T) {
	input := dto.OllamaChatResponse{
		Model: "deepseek-r1",
		Done:  true,
		Message: dto.OllamaChatResponseMessage{
			Role:     "assistant",
			Content:  "The answer is 42.",
			Thinking: "Let me think about this carefully...",
		},
		PromptEvalCount: 20,
		EvalCount:       8,
	}

	resp, err := convertOllamaChatResponseToOpenAIChat(&input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message == nil {
		t.Fatal("no choice or message")
	}
	if resp.Choices[0].Message.Content != "The answer is 42." {
		t.Errorf("content = %q, want 'The answer is 42.'", resp.Choices[0].Message.Content)
	}
	// thinking 字段应存在
	if resp.Choices[0].Message.ReasoningContent != "Let me think about this carefully..." {
		t.Errorf("reasoning_content = %q, want 'Let me think about this carefully...'", resp.Choices[0].Message.ReasoningContent)
	}
}

func TestOllamaChatResponse_ToolCallsOnly(t *testing.T) {
	args := json.RawMessage(`{"city":"beijing"}`)
	input := dto.OllamaChatResponse{
		Model:      "llama3",
		Done:       true,
		DoneReason: "stop",
		Message: dto.OllamaChatResponseMessage{
			Role:    "assistant",
			Content: "",
			ToolCalls: []dto.OllamaToolCall{
				{Function: dto.OllamaToolCallFunction{Name: "get_weather", Arguments: args}},
			},
		},
		PromptEvalCount: 15,
		EvalCount:       3,
	}

	resp, err := convertOllamaChatResponseToOpenAIChat(&input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message == nil {
		t.Fatal("no choice or message")
	}
	msg := resp.Choices[0].Message
	if msg.Content != "" {
		t.Errorf("content = %q, want empty", msg.Content)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.Function.Name != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"city":"beijing"}` {
		t.Errorf("tool arguments = %q, want {\"city\":\"beijing\"}", tc.Function.Arguments)
	}
	if tc.Type != "function" {
		t.Errorf("tool type = %q, want function", tc.Type)
	}
	// finish_reason 应为 tool_calls
	if resp.Choices[0].FinishReason == nil || *resp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %v, want tool_calls", resp.Choices[0].FinishReason)
	}
}

func TestOllamaChatResponse_ErrorField(t *testing.T) {
	input := dto.OllamaChatResponse{
		Model: "llama3",
		Done:  true,
		Error: "model not found",
	}
	_, err := convertOllamaChatResponseToOpenAIChat(&input)
	if err == nil {
		t.Fatal("expected error for non-empty Error field")
	}
}

func TestOllamaChatResponse_DefaultDoneReason(t *testing.T) {
	input := dto.OllamaChatResponse{
		Model: "llama3",
		Done:  true,
		Message: dto.OllamaChatResponseMessage{
			Role:    "assistant",
			Content: "hi",
		},
	}
	resp, err := convertOllamaChatResponseToOpenAIChat(&input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].FinishReason == nil || *resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %v, want stop (default)", resp.Choices[0].FinishReason)
	}
}

func TestOllamaChatResponse_MissingModel_FallbackToWinner(t *testing.T) {
	input := dto.OllamaChatResponse{
		Model: "",
		Done:  true,
		Message: dto.OllamaChatResponseMessage{
			Role:    "assistant",
			Content: "hi",
		},
	}
	resp, err := convertOllamaChatResponseToOpenAIChat(&input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 当 model 为空时，返回空字符串（调用方再处理 rmc）
	if resp.Model != "" {
		t.Errorf("model = %q, want empty when upstream omits it", resp.Model)
	}
}

// ============================================================================
// scanOllamaChatStream — 流式逐行 JSON 解析
// ============================================================================

func TestOllamaChatStream_TextDelta(t *testing.T) {
	lines := strings.Join([]string{
		`{"model":"llama3","message":{"role":"assistant","content":"Hello"},"done":false}`,
		`{"model":"llama3","message":{"role":"assistant","content":" world"},"done":false}`,
		`{"model":"llama3","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`,
	}, "\n")

	var chunks []dto.ChatCompletionChunk
	err := scanOllamaChatStream(strings.NewReader(lines), func(chunk dto.ChatCompletionChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	// 第一个 chunk：文本增量
	if len(chunks[0].Choices) == 0 || chunks[0].Choices[0].Delta == nil {
		t.Fatal("first chunk has no delta")
	}
	if chunks[0].Choices[0].Delta.Content != "Hello" {
		t.Errorf("chunk[0].content = %q, want Hello", chunks[0].Choices[0].Delta.Content)
	}
	if chunks[0].Choices[0].FinishReason != nil {
		t.Errorf("chunk[0] should not have finish_reason")
	}
	// 第三个 chunk：完成信号
	last := chunks[2]
	if last.Choices[0].FinishReason == nil || *last.Choices[0].FinishReason != "stop" {
		t.Errorf("last chunk finish_reason = %v, want stop", last.Choices[0].FinishReason)
	}
	// 最后 chunk 带 usage
	if last.Usage == nil {
		t.Error("last chunk should have usage")
	} else {
		if last.Usage.PromptTokens != 10 || last.Usage.CompletionTokens != 5 {
			t.Errorf("usage = %+v, want prompt=10 completion=5", last.Usage)
		}
	}
}

func TestOllamaChatStream_Thinking(t *testing.T) {
	lines := strings.Join([]string{
		`{"model":"deepseek","message":{"role":"assistant","content":"","thinking":"Thinking..."},"done":false}`,
		`{"model":"deepseek","message":{"role":"assistant","content":"Answer"},"done":true,"done_reason":"stop"}`,
	}, "\n")

	var chunks []dto.ChatCompletionChunk
	err := scanOllamaChatStream(strings.NewReader(lines), func(chunk dto.ChatCompletionChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 1 {
		t.Fatal("no chunks")
	}
	if chunks[0].Choices[0].Delta.ReasoningContent != "Thinking..." {
		t.Errorf("reasoning_content = %q, want 'Thinking...'", chunks[0].Choices[0].Delta.ReasoningContent)
	}
}

func TestOllamaChatStream_ToolCalls(t *testing.T) {
	args := `{"city":"beijing"}`
	lines := strings.Join([]string{
		`{"model":"llama3","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"get_weather","arguments":` + args + `}}]},"done":false}`,
		`{"model":"llama3","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`,
	}, "\n")

	var chunks []dto.ChatCompletionChunk
	err := scanOllamaChatStream(strings.NewReader(lines), func(chunk dto.ChatCompletionChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 1 {
		t.Fatal("no chunks")
	}
	delta := chunks[0].Choices[0].Delta
	if len(delta.ToolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(delta.ToolCalls))
	}
	tc := delta.ToolCalls[0]
	if tc.Function.Name != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", tc.Function.Name)
	}
	if tc.Function.Arguments != args {
		t.Errorf("tool arguments = %q, want %q", tc.Function.Arguments, args)
	}
	if *tc.Index != 0 {
		t.Errorf("tool index = %d, want 0 (stable first occurrence)", *tc.Index)
	}
}

func TestOllamaChatStream_ToolCalls_SameNameUseDistinctIndexes(t *testing.T) {
	lines := strings.Join([]string{
		`{"model":"llama3","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"get_weather","arguments":{"city":"beijing"}}}]},"done":false}`,
		`{"model":"llama3","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"get_weather","arguments":{"city":"shanghai"}}}]},"done":false}`,
		`{"model":"llama3","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`,
	}, "\n")

	var chunks []dto.ChatCompletionChunk
	err := scanOllamaChatStream(strings.NewReader(lines), func(chunk dto.ChatCompletionChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	first := chunks[0].Choices[0].Delta.ToolCalls
	second := chunks[1].Choices[0].Delta.ToolCalls
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("unexpected tool call counts: first=%d second=%d", len(first), len(second))
	}
	if *first[0].Index != 0 {
		t.Fatalf("first tool index = %d, want 0", *first[0].Index)
	}
	if *second[0].Index != 1 {
		t.Fatalf("second tool index = %d, want 1", *second[0].Index)
	}
}

func TestOllamaChatStream_InvalidJSON(t *testing.T) {
	lines := "not valid json\n"
	err := scanOllamaChatStream(strings.NewReader(lines), func(_ dto.ChatCompletionChunk) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON line")
	}
}

func TestOllamaChatStream_EmptyLinesSkipped(t *testing.T) {
	lines := "\n\n" + `{"model":"llama3","message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}` + "\n"
	var count int
	err := scanOllamaChatStream(strings.NewReader(lines), func(_ dto.ChatCompletionChunk) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("chunk count = %d, want 1", count)
	}
}

func TestOllamaChatStream_ErrorField(t *testing.T) {
	lines := `{"model":"llama3","error":"model not loaded","done":true}` + "\n"
	err := scanOllamaChatStream(strings.NewReader(lines), func(_ dto.ChatCompletionChunk) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error for Error field in chunk")
	}
}

// ============================================================================
// WriteResponse — OpenAIChatCodec + outbound=ollama.chat
// ============================================================================

func TestWriteResponse_OpenAIChat_OllamaOutbound_NonStream(t *testing.T) {
	ollamaBody := `{"model":"llama3","message":{"role":"assistant","content":"Hello"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(ollamaBody)),
	}
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)
	c := &OpenAIChatCodec{}

	if err := c.WriteResponse(ctx, FormatOllamaChat, resp, false, counter, ResponseModelContext{RequestedModel: "my-llama"}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	var out dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body parse error: %v, body=%s", err, w.Body.String())
	}
	if out.Model != "my-llama" {
		t.Errorf("model = %q, want my-llama", out.Model)
	}
	if len(out.Choices) == 0 || out.Choices[0].Message == nil {
		t.Fatal("no choices")
	}
	if out.Choices[0].Message.Content != "Hello" {
		t.Errorf("content = %q, want Hello", out.Choices[0].Message.Content)
	}
	if out.Usage.PromptTokens != 10 || out.Usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v", out.Usage)
	}
}

func TestWriteResponse_OpenAIChat_OllamaOutbound_Stream(t *testing.T) {
	lines := strings.Join([]string{
		`{"model":"llama3","message":{"role":"assistant","content":"Hi"},"done":false}`,
		`{"model":"llama3","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(lines)),
	}
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)
	c := &OpenAIChatCodec{}

	if err := c.WriteResponse(ctx, FormatOllamaChat, resp, true, counter, ResponseModelContext{RequestedModel: "my-llama"}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Errorf("expected SSE data lines, got: %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("expected [DONE] sentinel")
	}
	if !strings.Contains(body, "Hi") {
		t.Errorf("expected content 'Hi' in stream")
	}
}

// ============================================================================
// WriteResponse — AnthropicMessagesCodec + outbound=ollama.chat
// ============================================================================

func TestWriteResponse_Anthropic_OllamaOutbound_NonStream(t *testing.T) {
	ollamaBody := `{"model":"llama3","message":{"role":"assistant","content":"Hello"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(ollamaBody)),
	}
	ctx, _ := newTestContext()
	counter := token.NewStreamCounter(0)
	c := &AnthropicMessagesCodec{}

	if err := c.WriteResponse(ctx, FormatOllamaChat, resp, false, counter, ResponseModelContext{RequestedModel: "my-llama"}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
}

func TestWriteResponse_Anthropic_OllamaOutbound_Stream(t *testing.T) {
	lines := strings.Join([]string{
		`{"model":"llama3","message":{"role":"assistant","content":"Hi"},"done":false}`,
		`{"model":"llama3","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(lines)),
	}
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)
	c := &AnthropicMessagesCodec{}

	if err := c.WriteResponse(ctx, FormatOllamaChat, resp, true, counter, ResponseModelContext{RequestedModel: "my-llama"}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "message_start") {
		t.Errorf("expected message_start event, got: %s", body)
	}
	if !strings.Contains(body, "message_stop") {
		t.Errorf("expected message_stop event")
	}
	if !strings.Contains(body, "Hi") {
		t.Errorf("expected content 'Hi' in stream")
	}
}

// ============================================================================
// WriteResponse — OpenAIResponseCodec + outbound=ollama.chat
// ============================================================================

func TestWriteResponse_OpenAIResponse_OllamaOutbound_NonStream(t *testing.T) {
	ollamaBody := `{"model":"llama3","message":{"role":"assistant","content":"Hello"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(ollamaBody)),
	}
	ctx, _ := newTestContext()
	counter := token.NewStreamCounter(0)
	c := &OpenAIResponseCodec{}

	if err := c.WriteResponse(ctx, FormatOllamaChat, resp, false, counter, ResponseModelContext{RequestedModel: "my-llama"}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}
}

func TestWriteResponse_OpenAIResponse_OllamaOutbound_Stream(t *testing.T) {
	lines := strings.Join([]string{
		`{"model":"llama3","message":{"role":"assistant","content":"Hi"},"done":false}`,
		`{"model":"llama3","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(lines)),
	}
	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)
	c := &OpenAIResponseCodec{}

	if err := c.WriteResponse(ctx, FormatOllamaChat, resp, true, counter, ResponseModelContext{RequestedModel: "my-llama"}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "response.created") {
		t.Errorf("expected response.created event, got: %s", body)
	}
	if !strings.Contains(body, "Hi") {
		t.Errorf("expected content 'Hi' in stream")
	}
}

// ============================================================================
// 失败路径：非法 JSON body（非流式）
// ============================================================================

func TestWriteResponse_OpenAIChat_OllamaOutbound_InvalidBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("not json")),
	}
	ctx, _ := newTestContext()
	c := &OpenAIChatCodec{}
	err := c.WriteResponse(ctx, FormatOllamaChat, resp, false, nil, ResponseModelContext{})
	if err == nil {
		t.Fatal("expected error for invalid JSON body")
	}
}

// ============================================================================
// OpenAIChat 流式写回默认不发 usage chunk（无 stream_options.include_usage）
// ============================================================================

func TestWriteResponse_OpenAIChat_OllamaOutbound_Stream_NoUsageByDefault(t *testing.T) {
	lines := strings.Join([]string{
		`{"model":"llama3","message":{"role":"assistant","content":"Hi"},"done":false}`,
		`{"model":"llama3","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(lines)),
	}

	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)
	c := &OpenAIChatCodec{}

	// 未传 include_usage，不应出现带 usage 字段的 chunk
	if err := c.WriteResponse(ctx, FormatOllamaChat, resp, true, counter, ResponseModelContext{RequestedModel: "my-llama"}); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("expected [DONE] sentinel")
	}

	// 解析每个 SSE data 行，断言没有 chunk 携带非 null 的 usage
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk dto.ChatCompletionChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("chunk parse error: %v, payload=%s", err, payload)
		}
		if chunk.Usage != nil {
			t.Errorf("expected no usage in chunk when include_usage is false, got: %+v", chunk.Usage)
		}
	}
}

func TestWriteResponse_OpenAIChat_OllamaOutbound_Stream_WithUsage(t *testing.T) {
	lines := strings.Join([]string{
		`{"model":"llama3","message":{"role":"assistant","content":"Hi"},"done":false}`,
		`{"model":"llama3","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(lines)),
	}

	ctx, w := newTestContext()
	counter := token.NewStreamCounter(0)
	c := &OpenAIChatCodec{}

	// 传入 include_usage=true，应在 stop chunk 之后额外发送一个 usage chunk。
	rmc := ResponseModelContext{RequestedModel: "my-llama", IncludeUsage: true}
	if err := c.WriteResponse(ctx, FormatOllamaChat, resp, true, counter, rmc); err != nil {
		t.Fatalf("WriteResponse error: %v", err)
	}

	body := w.Body.String()
	var chunks []dto.ChatCompletionChunk
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk dto.ChatCompletionChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("chunk parse error: %v", err)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks (content, stop, usage), got %d", len(chunks))
	}

	stopChunk := chunks[1]
	if stopChunk.Choices[0].FinishReason == nil || *stopChunk.Choices[0].FinishReason != "stop" {
		t.Fatalf("stop chunk finish_reason = %v, want stop", stopChunk.Choices[0].FinishReason)
	}
	if stopChunk.Usage != nil {
		t.Fatalf("stop chunk should not carry usage, got %+v", stopChunk.Usage)
	}

	usageChunk := chunks[2]
	if len(usageChunk.Choices) != 0 {
		t.Fatalf("usage chunk choices len = %d, want 0", len(usageChunk.Choices))
	}
	if usageChunk.Usage == nil {
		t.Fatal("expected dedicated usage chunk, got nil usage")
	}
	if usageChunk.Usage.PromptTokens != 10 || usageChunk.Usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v, want prompt=10 completion=5", usageChunk.Usage)
	}
}

// ============================================================================
// 流式 tool_calls finish_reason 验证
// ============================================================================

func TestOllamaChatStream_ToolCalls_FinishReason(t *testing.T) {
	args := `{"city":"beijing"}`
	lines := strings.Join([]string{
		`{"model":"llama3","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"get_weather","arguments":` + args + `}}]},"done":false}`,
		`{"model":"llama3","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`,
	}, "\n")

	var chunks []dto.ChatCompletionChunk
	err := scanOllamaChatStream(strings.NewReader(lines), func(chunk dto.ChatCompletionChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}

	// 终止 chunk 的 finish_reason 应为 tool_calls，即使 Ollama 给的是 stop
	last := chunks[1]
	if last.Choices[0].FinishReason == nil || *last.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %v, want tool_calls", last.Choices[0].FinishReason)
	}
}
