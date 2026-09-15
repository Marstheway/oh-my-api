package codec

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

// TestConvertOpenAIToOllamaChat_BasicText 测试基本文本消息转换
func TestConvertOpenAIToOllamaChat_BasicText(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
		},
		Stream: true,
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2:latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Model != "llama3.2:latest" {
		t.Errorf("model = %q, want llama3.2:latest", out.Model)
	}
	if !out.Stream {
		t.Errorf("stream = false, want true")
	}
	if len(out.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(out.Messages))
	}
	if out.Messages[0].Role != "system" || out.Messages[0].Content != "You are helpful." {
		t.Errorf("messages[0] = {%s, %s}, want {system, You are helpful.}", out.Messages[0].Role, out.Messages[0].Content)
	}
	if out.Messages[1].Role != "user" || out.Messages[1].Content != "Hello" {
		t.Errorf("messages[1] = {%s, %s}, want {user, Hello}", out.Messages[1].Role, out.Messages[1].Content)
	}
	if out.Messages[2].Role != "assistant" || out.Messages[2].Content != "Hi there!" {
		t.Errorf("messages[2] = {%s, %s}, want {assistant, Hi there!}", out.Messages[2].Role, out.Messages[2].Content)
	}
}

// TestConvertOpenAIToOllamaChat_ArrayContentAllText 测试数组格式的纯文本内容拼接
func TestConvertOpenAIToOllamaChat_ArrayContentAllText(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "Hello "},
					map[string]any{"type": "text", "text": "World"},
				},
			},
		},
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}
	if out.Messages[0].Content != "Hello World" {
		t.Errorf("content = %q, want 'Hello World'", out.Messages[0].Content)
	}
}

// TestConvertOpenAIToOllamaChat_ImageURLRejected 测试图片内容被拒绝
func TestConvertOpenAIToOllamaChat_ImageURLRejected(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "What is this?"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/img.png"}},
				},
			},
		},
	}

	_, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err == nil {
		t.Fatal("expected error for image_url content, got nil")
	}
}

// TestConvertOpenAIToOllamaChat_AudioRejected 测试音频内容被拒绝
func TestConvertOpenAIToOllamaChat_AudioRejected(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "base64data", "format": "wav"}},
				},
			},
		},
	}

	_, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err == nil {
		t.Fatal("expected error for input_audio content, got nil")
	}
}

// TestConvertOpenAIToOllamaChat_FileRejected 测试文件内容被拒绝
func TestConvertOpenAIToOllamaChat_FileRejected(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "file", "file": map[string]any{"file_data": "base64"}},
				},
			},
		},
	}

	_, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err == nil {
		t.Fatal("expected error for file content, got nil")
	}
}

// TestConvertOpenAIToOllamaChat_VideoURLRejected 测试视频内容被拒绝
func TestConvertOpenAIToOllamaChat_VideoURLRejected(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "video_url", "video_url": map[string]any{"url": "https://example.com/video.mp4"}},
				},
			},
		},
	}

	_, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err == nil {
		t.Fatal("expected error for video_url content, got nil")
	}
}

// TestConvertOpenAIToOllamaChat_Tools 测试工具定义转换
func TestConvertOpenAIToOllamaChat_Tools(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{Role: "user", Content: "What's the weather?"},
		},
		Tools: []dto.Tool{
			{
				Type: "function",
				Function: dto.ToolFunction{
					Name:        "get_weather",
					Description: "Get weather info",
					Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
				},
			},
		},
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(out.Tools))
	}
	if out.Tools[0].Function.Name != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", out.Tools[0].Function.Name)
	}
	if out.Tools[0].Function.Description != "Get weather info" {
		t.Errorf("tool description = %q, want 'Get weather info'", out.Tools[0].Function.Description)
	}
}

// TestConvertOpenAIToOllamaChat_AssistantToolCalls 测试 assistant 的 tool_calls 转换
func TestConvertOpenAIToOllamaChat_AssistantToolCalls(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{Role: "user", Content: "What's the weather?"},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []dto.ToolCall{
					{
						ID:   "call-1",
						Type: "function",
						Function: dto.ToolCallFunc{
							Name:      "get_weather",
							Arguments: `{"city":"beijing"}`,
						},
					},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call-1",
				Content:    "sunny",
			},
		},
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(out.Messages))
	}

	assistantMsg := out.Messages[1]
	if assistantMsg.Role != "assistant" {
		t.Errorf("msg[1].role = %q, want assistant", assistantMsg.Role)
	}
	if len(assistantMsg.ToolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(assistantMsg.ToolCalls))
	}
	tc := assistantMsg.ToolCalls[0]
	if tc.Function.Name != "get_weather" {
		t.Errorf("tool_call name = %q, want get_weather", tc.Function.Name)
	}
	var args map[string]any
	if err := json.Unmarshal(tc.Function.Arguments, &args); err != nil {
		t.Fatalf("failed to unmarshal tool_call args: %v", err)
	}
	if args["city"] != "beijing" {
		t.Errorf("tool_call args.city = %v, want beijing", args["city"])
	}

	toolMsg := out.Messages[2]
	if toolMsg.Role != "tool" {
		t.Errorf("msg[2].role = %q, want tool", toolMsg.Role)
	}
	if toolMsg.Content != "sunny" {
		t.Errorf("msg[2].content = %q, want sunny", toolMsg.Content)
	}
	if toolMsg.ToolName != "get_weather" {
		t.Errorf("msg[2].tool_name = %q, want get_weather", toolMsg.ToolName)
	}
}

// TestConvertOpenAIToOllamaChat_ToolResultWithoutName 测试 tool result 消息中 name 字段缺失时使用 ToolCallID
func TestConvertOpenAIToOllamaChat_ToolResultWithoutName(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{
				Role:       "tool",
				ToolCallID: "call-abc",
				Content:    "result text",
			},
		},
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}
	toolMsg := out.Messages[0]
	if toolMsg.Role != "tool" {
		t.Errorf("role = %q, want tool", toolMsg.Role)
	}
	if toolMsg.Content != "result text" {
		t.Errorf("content = %q, want 'result text'", toolMsg.Content)
	}
	if toolMsg.ToolName != "call-abc" {
		t.Errorf("tool_name = %q, want 'call-abc'", toolMsg.ToolName)
	}
}

// TestConvertOpenAIToOllamaChat_ResponseFormatJSON 测试 json_object 格式映射
func TestConvertOpenAIToOllamaChat_ResponseFormatJSON(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{Role: "user", Content: "Return JSON"},
		},
		ResponseFormat: &dto.ResponseFormat{
			Type: "json_object",
		},
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Format != "json" {
		t.Errorf("format = %v, want json", out.Format)
	}
}

// TestConvertOpenAIToOllamaChat_ResponseFormatJSONMode 测试 json type 格式映射
func TestConvertOpenAIToOllamaChat_ResponseFormatJSONMode(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{Role: "user", Content: "Return JSON"},
		},
		ResponseFormat: &dto.ResponseFormat{
			Type: "json",
		},
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Format != "json" {
		t.Errorf("format = %v, want json", out.Format)
	}
}

// TestConvertOpenAIToOllamaChat_ResponseFormatJSONSchema 测试 json_schema 格式映射
func TestConvertOpenAIToOllamaChat_ResponseFormatJSONSchema(t *testing.T) {
	schemaJSON := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{Role: "user", Content: "Return JSON"},
		},
		ResponseFormat: &dto.ResponseFormat{
			Type:       "json_schema",
			JsonSchema: schemaJSON,
		},
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fmtObj, ok := out.Format.(map[string]any)
	if !ok {
		t.Fatalf("format is not object: %T", out.Format)
	}
	if fmtObj["type"] != "object" {
		t.Errorf("format.type = %v, want object", fmtObj["type"])
	}
}

// TestConvertOpenAIToOllamaChat_ResponseFormatJSONSchema_Invalid 测试非法 schema 返回错误
func TestConvertOpenAIToOllamaChat_ResponseFormatJSONSchema_Invalid(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{Role: "user", Content: "Return JSON"},
		},
		ResponseFormat: &dto.ResponseFormat{
			Type:       "json_schema",
			JsonSchema: json.RawMessage(`invalid json`),
		},
	}

	_, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err == nil {
		t.Fatal("expected error for invalid json_schema, got nil")
	}
}

// TestConvertOpenAIToOllamaChat_ThinkNativeField 测试 Think 原生字段优先
func TestConvertOpenAIToOllamaChat_ThinkNativeField(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{Role: "user", Content: "Think hard"},
		},
		Think:          json.RawMessage(`true`),
		EnableThinking: json.RawMessage(`false`),
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(out.Think) != "true" {
		t.Errorf("think = %s, want true", string(out.Think))
	}
}

// TestConvertOpenAIToOllamaChat_ThinkFromEnableThinking 测试从 enable_thinking 透传
func TestConvertOpenAIToOllamaChat_ThinkFromEnableThinking(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{Role: "user", Content: "Think hard"},
		},
		EnableThinking: json.RawMessage(`{"budget_tokens":1000}`),
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(out.Think) != `{"budget_tokens":1000}` {
		t.Errorf("think = %s, want {\"budget_tokens\":1000}", string(out.Think))
	}
}

// TestConvertOpenAIToOllamaChat_ThinkAbsent 测试两者都空时不输出 think
func TestConvertOpenAIToOllamaChat_ThinkAbsent(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Think != nil {
		t.Errorf("think = %v, want nil", string(out.Think))
	}
}

// TestConvertOpenAIToOllamaChat_SamplingParams 测试采样参数映射到 Options
func TestConvertOpenAIToOllamaChat_SamplingParams(t *testing.T) {
	temp := 0.7
	topP := 0.9
	topK := 40
	freqPenalty := 0.5
	presPenalty := 0.3
	seed := int64(42)

	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
		},
		Temperature:      &temp,
		TopP:             &topP,
		TopK:             &topK,
		FrequencyPenalty: &freqPenalty,
		PresencePenalty:  &presPenalty,
		Seed:             &seed,
		MaxTokens:        512,
		Stop:             []string{"END", "STOP"},
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	opts := out.Options
	if opts == nil {
		t.Fatal("options is nil")
	}
	if opts["temperature"] != temp {
		t.Errorf("options.temperature = %v, want %v", opts["temperature"], temp)
	}
	if opts["top_p"] != topP {
		t.Errorf("options.top_p = %v, want %v", opts["top_p"], topP)
	}
	if opts["top_k"] != topK {
		t.Errorf("options.top_k = %v, want %v", opts["top_k"], topK)
	}
	if opts["frequency_penalty"] != freqPenalty {
		t.Errorf("options.frequency_penalty = %v, want %v", opts["frequency_penalty"], freqPenalty)
	}
	if opts["presence_penalty"] != presPenalty {
		t.Errorf("options.presence_penalty = %v, want %v", opts["presence_penalty"], presPenalty)
	}
	if opts["seed"] != seed {
		t.Errorf("options.seed = %v, want %v", opts["seed"], seed)
	}
	if opts["num_predict"] != 512 {
		t.Errorf("options.num_predict = %v, want 512", opts["num_predict"])
	}
	stop, ok := opts["stop"].([]string)
	if !ok {
		t.Fatalf("options.stop type = %T, want []string", opts["stop"])
	}
	if len(stop) != 2 || stop[0] != "END" || stop[1] != "STOP" {
		t.Errorf("options.stop = %v, want [END STOP]", stop)
	}
}

// TestConvertOpenAIToOllamaChat_StopSingleString 测试 stop 为单字符串
func TestConvertOpenAIToOllamaChat_StopSingleString(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model:    "llama3.2",
		Messages: []dto.Message{{Role: "user", Content: "Hello"}},
		Stop:     []string{"END"},
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stop, ok := out.Options["stop"].([]string)
	if !ok {
		t.Fatalf("options.stop type = %T, want []string", out.Options["stop"])
	}
	if len(stop) != 1 || stop[0] != "END" {
		t.Errorf("options.stop = %v, want [END]", stop)
	}
}

// TestConvertOpenAIToOllamaChat_MaxCompletionTokens 测试 max_completion_tokens 映射到 num_predict
func TestConvertOpenAIToOllamaChat_MaxCompletionTokens(t *testing.T) {
	maxComp := 200
	req := &dto.ChatCompletionRequest{
		Model:               "llama3.2",
		Messages:            []dto.Message{{Role: "user", Content: "Hello"}},
		MaxCompletionTokens: &maxComp,
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Options["num_predict"] != maxComp {
		t.Errorf("options.num_predict = %v, want %d", out.Options["num_predict"], maxComp)
	}
}

// TestConvertOpenAIToOllamaChat_EmptyTextContent 测试空文本内容
func TestConvertOpenAIToOllamaChat_EmptyTextContent(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{Role: "user", Content: ""},
		},
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}
	if out.Messages[0].Content != "" {
		t.Errorf("content = %q, want empty string", out.Messages[0].Content)
	}
}

// TestEncodeRequestToOllamaChat_OpenAIChat 测试 OpenAI Chat inbound 编码为 ollama.chat
func TestEncodeRequestToOllamaChat_OpenAIChat(t *testing.T) {
	c := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
		},
		Stream: false,
	}

	body, err := c.EncodeRequest(FormatOllamaChat, req, "llama3.2:latest", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out dto.OllamaChatRequest
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	if out.Model != "llama3.2:latest" {
		t.Errorf("model = %q, want llama3.2:latest", out.Model)
	}
	if len(out.Messages) != 1 || out.Messages[0].Role != "user" || out.Messages[0].Content != "Hello" {
		t.Errorf("messages mismatch: %+v", out.Messages)
	}
}

// TestEncodeRequestToOllamaChat_AnthropicMessages 测试 Anthropic Messages inbound 编码为 ollama.chat
func TestEncodeRequestToOllamaChat_AnthropicMessages(t *testing.T) {
	c := &AnthropicMessagesCodec{}
	req := &dto.ClaudeRequest{
		Model: "claude-3-5-sonnet",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
		},
		Stream: false,
	}

	body, err := c.EncodeRequest(FormatOllamaChat, req, "llama3.2:latest", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out dto.OllamaChatRequest
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	if out.Model != "llama3.2:latest" {
		t.Errorf("model = %q, want llama3.2:latest", out.Model)
	}
	if len(out.Messages) < 1 {
		t.Fatalf("messages is empty")
	}
	last := out.Messages[len(out.Messages)-1]
	if last.Role != "user" || last.Content != "Hello" {
		t.Errorf("last message = {%s, %s}, want {user, Hello}", last.Role, last.Content)
	}
}

// TestEncodeRequestToOllamaChat_OpenAIResponse 测试 OpenAI Response inbound 编码为 ollama.chat
func TestEncodeRequestToOllamaChat_OpenAIResponse(t *testing.T) {
	c := &OpenAIResponseCodec{}
	req := &dto.ResponsesRequest{
		Model:  "gpt-4o",
		Input:  json.RawMessage(`"Hello"`),
		Stream: false,
	}

	body, err := c.EncodeRequest(FormatOllamaChat, req, "llama3.2:latest", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out dto.OllamaChatRequest
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	if out.Model != "llama3.2:latest" {
		t.Errorf("model = %q, want llama3.2:latest", out.Model)
	}
	if len(out.Messages) < 1 {
		t.Fatalf("messages is empty")
	}
	last := out.Messages[len(out.Messages)-1]
	if last.Role != "user" || last.Content != "Hello" {
		t.Errorf("last message = {%s, %s}, want {user, Hello}", last.Role, last.Content)
	}
}

// TestRejectsMultimodalForOllamaChat_OpenAIChat 测试 OpenAI Chat 多模态请求在 encode 阶段失败并包装为 ConversionError
func TestRejectsMultimodalForOllamaChat_OpenAIChat(t *testing.T) {
	c := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "Look at this"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/img.png"}},
				},
			},
		},
	}

	_, err := c.EncodeRequest(FormatOllamaChat, req, "llama3.2:latest", false)
	if err == nil {
		t.Fatal("expected error for multimodal content, got nil")
	}
	var convErr *ConversionError
	if !errors.As(err, &convErr) {
		t.Errorf("expected *ConversionError, got %T: %v", err, err)
	}
}

// TestRejectsMultimodalForOllamaChat_AnthropicMessages 测试 Anthropic 图片请求在 encode 阶段失败并包装为 ConversionError
func TestRejectsMultimodalForOllamaChat_AnthropicMessages(t *testing.T) {
	c := &AnthropicMessagesCodec{}
	req := &dto.ClaudeRequest{
		Model: "claude-3-5-sonnet",
		Messages: []dto.ClaudeMessage{
			{
				Role: "user",
				Content: []any{
					map[string]any{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": "image/jpeg",
							"data":       "base64data",
							"url":        "",
						},
					},
				},
			},
		},
	}

	_, err := c.EncodeRequest(FormatOllamaChat, req, "llama3.2:latest", false)
	if err == nil {
		t.Fatal("expected error for image content, got nil")
	}
	var convErr *ConversionError
	if !errors.As(err, &convErr) {
		t.Errorf("expected *ConversionError, got %T: %v", err, err)
	}
}

// TestRejectsMultimodalForOllamaChat_OpenAIResponse 测试 Responses API 多模态 input_image
// 经 Response→Chat 映射后，在 Chat→Ollama 阶段被拒绝。
func TestRejectsMultimodalForOllamaChat_OpenAIResponse(t *testing.T) {
	c := &OpenAIResponseCodec{}
	// 使用 Responses 标准 input_image part（image_url 为字符串）
	inputJSON := json.RawMessage(`[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.com/img.png"}]}]`)
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: inputJSON,
	}

	_, err := c.EncodeRequest(FormatOllamaChat, req, "llama3.2:latest", false)
	if err == nil {
		t.Fatal("expected error for multimodal content to ollama, got nil")
	}
	var convErr *ConversionError
	if !errors.As(err, &convErr) {
		t.Errorf("expected *ConversionError, got %T: %v", err, err)
	}
}

// TestConvertOpenAIToOllamaChat_MessageOrder 测试消息顺序保持不变
func TestConvertOpenAIToOllamaChat_MessageOrder(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "llama3.2",
		Messages: []dto.Message{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "second"},
			{Role: "user", Content: "third"},
			{Role: "assistant", Content: "fourth"},
		},
	}

	out, err := convertOpenAIToOllamaChatRequest(req, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.Messages) != 4 {
		t.Fatalf("messages len = %d, want 4", len(out.Messages))
	}
	expected := []struct{ role, content string }{
		{"user", "first"},
		{"assistant", "second"},
		{"user", "third"},
		{"assistant", "fourth"},
	}
	for i, e := range expected {
		if out.Messages[i].Role != e.role || out.Messages[i].Content != e.content {
			t.Errorf("messages[%d] = {%s, %s}, want {%s, %s}", i, out.Messages[i].Role, out.Messages[i].Content, e.role, e.content)
		}
	}
}
