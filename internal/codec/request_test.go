package codec

import (
	"encoding/json"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

func TestOpenAIChatCodec_EncodeRequest_ToOpenAIChat(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model:  "gpt-4",
		Stream: true,
		Messages: []dto.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
			{
				Role:    "assistant",
				Content: "Result",
				ToolCalls: []dto.ToolCall{
					{
						ID:   "tool-1",
						Type: "function",
						Function: dto.ToolCallFunc{
							Name:      "get_weather",
							Arguments: `{"city":"beijing"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "tool-1", Content: "sunny"},
		},
		Tools: []dto.Tool{
			{
				Type: "function",
				Function: dto.ToolFunction{
					Name:        "get_weather",
					Description: "Get weather",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		},
		ToolChoice: map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "get_weather",
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if out.Model != "gpt-4o" {
		t.Fatalf("model = %q, want %q", out.Model, "gpt-4o")
	}
	if !out.Stream {
		t.Fatalf("stream = %v, want true", out.Stream)
	}
	if len(out.Messages) < 2 || out.Messages[0].Role != "system" {
		t.Fatalf("system message not preserved")
	}
	if len(out.Tools) != 1 || out.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("tools not preserved")
	}
	choice, ok := out.ToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("tool_choice not preserved")
	}
	if choice["type"] != "function" {
		t.Fatalf("tool_choice type = %v, want function", choice["type"])
	}

}

func TestOpenAIChatCodec_EncodeRequest_ToOpenAIChat_EchoReasoningContent(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []dto.ToolCall{{
					ID:   "tool-1",
					Type: "function",
					Function: dto.ToolCallFunc{
						Name:      "get_weather",
						Arguments: `{"city":"beijing"}`,
					},
				}},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o", true)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if out.Messages[1].ReasoningContent == nil || *out.Messages[1].ReasoningContent != " " {
		t.Fatalf("reasoning_content = %#v, want single space", out.Messages[1].ReasoningContent)
	}
	if req.Messages[1].ReasoningContent != nil {
		t.Fatalf("original request should not be mutated")
	}
}

func TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model:  "gpt-4",
		Stream: true,
		Messages: []dto.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
			{
				Role:    "assistant",
				Content: "Result",
				ToolCalls: []dto.ToolCall{
					{
						ID:   "tool-1",
						Type: "function",
						Function: dto.ToolCallFunc{
							Name:      "get_weather",
							Arguments: `{"city":"beijing"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "tool-1", Content: "sunny"},
		},
		Tools: []dto.Tool{
			{
				Type: "function",
				Function: dto.ToolFunction{
					Name:        "get_weather",
					Description: "Get weather",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		},
		ToolChoice: map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "get_weather",
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if out.Model != "claude-3" {
		t.Fatalf("model = %q, want %q", out.Model, "claude-3")
	}
	if !out.Stream {
		t.Fatalf("stream = %v, want true", out.Stream)
	}
	if out.System == nil {
		t.Fatal("system should be array, not nil")
	}
	if tools, ok := out.Tools.([]any); !ok || len(tools) != 1 {
		t.Fatalf("tools not converted correctly: %v", out.Tools)
	}
	// tool_choice 现在使用新的 mapToolChoice 函数，type 变为 "tool"
	choice, ok := out.ToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("tool_choice not converted")
	}
	if choice["type"] != "tool" || choice["name"] != "get_weather" {
		t.Fatalf("tool_choice mismatch: %v", choice)
	}
	if len(out.Messages) == 0 {
		t.Fatalf("messages missing")
	}
}

func TestAnthropicMessagesCodec_EncodeRequest_ToAnthropicMessages(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	req := &dto.ClaudeRequest{
		Model:  "claude-fast",
		Stream: true,
		System: "You are helpful.",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
		},
		Tools: []dto.ClaudeTool{
			{
				Name:        "get_weather",
				Description: "Get weather",
				InputSchema: map[string]any{"type": "object"},
			},
		},
		ToolChoice: map[string]any{"type": "auto"},
	}

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if out.Model != "claude-3" {
		t.Fatalf("model = %q, want %q", out.Model, "claude-3")
	}
	if !out.Stream {
		t.Fatalf("stream = %v, want true", out.Stream)
	}
	if out.System != "You are helpful." {
		t.Fatalf("system = %v, want %q", out.System, "You are helpful.")
	}
	if len(out.Messages) != 1 || out.Messages[0].Role != "user" {
		t.Fatalf("messages not preserved")
	}
	if tools, ok := out.Tools.([]any); !ok || len(tools) != 1 {
		t.Fatalf("tools not preserved: %v", out.Tools)
	}
}

func TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_Text(t *testing.T) {
	codec := &OpenAIChatCodec{}
	maxTokens := 512
	temp := 0.7
	req := &dto.ChatCompletionRequest{
		Model:       "gpt-4",
		Stream:      true,
		MaxTokens:   maxTokens,
		Temperature: &temp,
		Messages: []dto.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if out.Model != "gpt-4o-mini" {
		t.Fatalf("model = %q, want %q", out.Model, "gpt-4o-mini")
	}
	if out.MaxOutputTokens != maxTokens {
		t.Fatalf("max_output_tokens = %d, want %d", out.MaxOutputTokens, maxTokens)
	}
	if !out.Stream {
		t.Fatalf("stream = false, want true")
	}

	var instructions string
	if err := json.Unmarshal(out.Instructions, &instructions); err != nil {
		t.Fatalf("failed to unmarshal instructions: %v", err)
	}
	if instructions != "You are helpful." {
		t.Fatalf("instructions = %q, want %q", instructions, "You are helpful.")
	}

	var items []map[string]any
	if err := json.Unmarshal(out.Input, &items); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("input length = %d, want 2", len(items))
	}

	userItem := items[0]
	if userItem["type"] != "message" {
		t.Fatalf("user item type = %v, want message", userItem["type"])
	}
	if userItem["role"] != "user" {
		t.Fatalf("user item role = %v, want user", userItem["role"])
	}
	var userContent string
	if err := json.Unmarshal([]byte(mustJSON(userItem["content"])), &userContent); err != nil {
		t.Fatalf("failed to parse user content: %v", err)
	}
	if userContent != "Hello" {
		t.Fatalf("user content = %q, want Hello", userContent)
	}

	assistantItem := items[1]
	if assistantItem["type"] != "message" {
		t.Fatalf("assistant item type = %v, want message", assistantItem["type"])
	}
	if assistantItem["role"] != "assistant" {
		t.Fatalf("assistant item role = %v, want assistant", assistantItem["role"])
	}
	var assistantContent string
	if err := json.Unmarshal([]byte(mustJSON(assistantItem["content"])), &assistantContent); err != nil {
		t.Fatalf("failed to parse assistant content: %v", err)
	}
	if assistantContent != "Hi there!" {
		t.Fatalf("assistant content = %q, want Hi there!", assistantContent)
	}
}

func TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_Tools(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model:  "gpt-4",
		Stream: false,
		Messages: []dto.Message{
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []dto.ToolCall{
					{
						ID:   "call-abc",
						Type: "function",
						Function: dto.ToolCallFunc{
							Name:      "get_weather",
							Arguments: `{"city":"beijing"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call-abc", Content: "sunny"},
		},
		Tools: []dto.Tool{
			{
				Type: "function",
				Function: dto.ToolFunction{
					Name:        "get_weather",
					Description: "Get weather",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		},
		ToolChoice: map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "get_weather",
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	var items []map[string]any
	if err := json.Unmarshal(out.Input, &items); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("input length = %d, want 2", len(items))
	}

	fcItem := items[0]
	if fcItem["type"] != "function_call" {
		t.Fatalf("function_call item type = %v, want function_call", fcItem["type"])
	}
	if fcItem["call_id"] != "call-abc" {
		t.Fatalf("call_id = %v, want call-abc", fcItem["call_id"])
	}
	if fcItem["name"] != "get_weather" {
		t.Fatalf("name = %v, want get_weather", fcItem["name"])
	}
	if fcItem["arguments"] != `{"city":"beijing"}` {
		t.Fatalf("arguments = %v, want {\"city\":\"beijing\"}", fcItem["arguments"])
	}

	fcoItem := items[1]
	if fcoItem["type"] != "function_call_output" {
		t.Fatalf("function_call_output item type = %v, want function_call_output", fcoItem["type"])
	}
	if fcoItem["call_id"] != "call-abc" {
		t.Fatalf("call_id = %v, want call-abc", fcoItem["call_id"])
	}
	if fcoItem["output"] != "sunny" {
		t.Fatalf("output = %v, want sunny", fcoItem["output"])
	}

	if len(out.Tools) != 1 || out.Tools[0].Name != "get_weather" {
		t.Fatalf("tools not converted correctly")
	}
	if out.Tools[0].Type != "function" {
		t.Fatalf("tools[0].type = %q, want function", out.Tools[0].Type)
	}

	choice, ok := out.ToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("tool_choice is not map: %T", out.ToolChoice)
	}
	if choice["type"] != "function" {
		t.Fatalf("tool_choice type = %v, want function", choice["type"])
	}
	if choice["name"] != "get_weather" {
		t.Fatalf("tool_choice name = %v, want get_weather", choice["name"])
	}
	if _, hasFunction := choice["function"]; hasFunction {
		t.Fatalf("tool_choice should not have nested function key")
	}
}

func TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_MultimodalImage(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "What is in this image?"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/img.png"}},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	var items []map[string]any
	if err := json.Unmarshal(out.Input, &items); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("input length = %d, want 1", len(items))
	}

	userItem := items[0]
	if userItem["type"] != "message" {
		t.Fatalf("user item type = %v, want message", userItem["type"])
	}
	if userItem["role"] != "user" {
		t.Fatalf("user item role = %v, want user", userItem["role"])
	}

	// 验证 content 是数组形式的多模态内容
	var content []map[string]any
	contentBytes, _ := json.Marshal(userItem["content"])
	if err := json.Unmarshal(contentBytes, &content); err != nil {
		t.Fatalf("failed to parse multimodal content: %v", err)
	}
	if len(content) != 2 {
		t.Fatalf("content length = %d, want 2", len(content))
	}

	// 验证 text part
	if content[0]["type"] != "input_text" {
		t.Fatalf("content[0].type = %v, want input_text", content[0]["type"])
	}
	if content[0]["text"] != "What is in this image?" {
		t.Fatalf("content[0].text = %v, want 'What is in this image?'", content[0]["text"])
	}

	// 验证 image_url part 被转换为 input_image
	if content[1]["type"] != "input_image" {
		t.Fatalf("content[1].type = %v, want input_image", content[1]["type"])
	}
	if content[1]["image_url"] != "https://example.com/img.png" {
		t.Fatalf("content[1].image_url = %v, want https://example.com/img.png", content[1]["image_url"])
	}
}

func TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_DeveloperMessage(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "developer", Content: "You are a code assistant."},
			{Role: "user", Content: "Write hello world."},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	var instructions string
	if err := json.Unmarshal(out.Instructions, &instructions); err != nil {
		t.Fatalf("failed to unmarshal instructions: %v", err)
	}
	if instructions != "You are a code assistant." {
		t.Fatalf("instructions = %q, want %q", instructions, "You are a code assistant.")
	}

	var items []map[string]any
	if err := json.Unmarshal(out.Input, &items); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("input length = %d, want 1 (developer excluded)", len(items))
	}
	if items[0]["role"] != "user" {
		t.Fatalf("first item role = %v, want user", items[0]["role"])
	}
}

func TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_MultipleSystemMessages(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "system", Content: "First system."},
			{Role: "system", Content: "Second system."},
			{Role: "user", Content: "Hello."},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	var instructions string
	if err := json.Unmarshal(out.Instructions, &instructions); err != nil {
		t.Fatalf("failed to unmarshal instructions: %v", err)
	}
	if instructions != "First system.\nSecond system." {
		t.Fatalf("instructions = %q, want %q", instructions, "First system.\nSecond system.")
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func TestOpenAIResponseCodec_EncodeRequest_ToAnthropicMessages_ViaChat(t *testing.T) {
	codec := &OpenAIResponseCodec{}

	instrJSON, _ := json.Marshal("You are helpful")
	userContentJSON, _ := json.Marshal("Hello")
	inputItems := []dto.ResponsesInputItem{
		{Type: "message", Role: "user", Content: json.RawMessage(userContentJSON)},
	}
	inputJSON, _ := json.Marshal(inputItems)

	req := &dto.ResponsesRequest{
		Model:        "gpt-4o",
		Instructions: json.RawMessage(instrJSON),
		Input:        json.RawMessage(inputJSON),
	}

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal ClaudeRequest: %v", err)
	}

	if out.Model != "claude-3" {
		t.Fatalf("model = %q, want %q", out.Model, "claude-3")
	}

	systemText := extractSystemText(out.System)
	if systemText != "You are helpful" {
		t.Fatalf("system = %q, want %q", systemText, "You are helpful")
	}

	if len(out.Messages) == 0 {
		t.Fatalf("messages is empty, want at least one message")
	}

	var userFound bool
	for _, msg := range out.Messages {
		if msg.Role == "user" {
			userFound = true
			break
		}
	}
	if !userFound {
		t.Fatalf("no user message found in messages")
	}
}

func TestAnthropicMessagesCodec_EncodeRequest_ToOpenAIResponse_ViaChat(t *testing.T) {
	codec := &AnthropicMessagesCodec{}

	req := &dto.ClaudeRequest{
		Model:  "claude-fast",
		System: "You are helpful",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal ResponsesRequest: %v", err)
	}

	if out.Model != "gpt-4o" {
		t.Fatalf("model = %q, want %q", out.Model, "gpt-4o")
	}

	var instructions string
	if err := json.Unmarshal(out.Instructions, &instructions); err != nil {
		t.Fatalf("failed to unmarshal instructions: %v", err)
	}
	if instructions != "You are helpful" {
		t.Fatalf("instructions = %q, want %q", instructions, "You are helpful")
	}

	var items []map[string]any
	if err := json.Unmarshal(out.Input, &items); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}

	var userFound bool
	for _, item := range items {
		if item["role"] == "user" {
			userFound = true
			break
		}
	}
	if !userFound {
		t.Fatalf("no user message found in input items")
	}
}

func TestOpenAIResponseCodec_EncodeRequest_ToAnthropicMessages_FirstHopFail(t *testing.T) {
	codec := &OpenAIResponseCodec{}

	req := &dto.ResponsesRequest{
		Model:        "gpt-4o",
		Instructions: json.RawMessage("123"),
		Input:        json.RawMessage(`[]`),
	}

	_, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err == nil {
		t.Fatalf("expected error for non-string instructions, got nil")
	}
}

func TestAnthropicMessagesCodec_EncodeRequest_ToOpenAIResponse_ViaChat_ToolRoundTrip(t *testing.T) {
	codec := &AnthropicMessagesCodec{}

	req := &dto.ClaudeRequest{
		Model: "claude-fast",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: []dto.ContentBlock{{Type: "text", Text: "Check weather"}}},
			{Role: "assistant", Content: []dto.ContentBlock{
				{
					Type:  "tool_use",
					ID:    "tool-1",
					Name:  "get_weather",
					Input: map[string]any{"city": "beijing"},
				},
			}},
			{Role: "user", Content: []dto.ContentBlock{
				{Type: "tool_result", ToolUseID: "tool-1", Content: "sunny"},
			}},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal ResponsesRequest: %v", err)
	}

	if out.Model != "gpt-4o" {
		t.Fatalf("model = %q, want %q", out.Model, "gpt-4o")
	}

	var items []map[string]any
	if err := json.Unmarshal(out.Input, &items); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}

	var fcFound, fcoFound bool
	for _, item := range items {
		switch item["type"] {
		case "function_call":
			if item["name"] == "get_weather" {
				fcFound = true
			}
		case "function_call_output":
			if item["call_id"] == "tool-1" {
				fcoFound = true
			}
		}
	}

	if !fcFound {
		t.Fatalf("function_call item not found in input, items = %v", items)
	}
	if !fcoFound {
		t.Fatalf("function_call_output item not found in input, items = %v", items)
	}
}

func TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_AssistantTextAndToolCalls(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{
				Role:    "assistant",
				Content: "I will check the weather.",
				ToolCalls: []dto.ToolCall{
					{
						ID:   "call-xyz",
						Type: "function",
						Function: dto.ToolCallFunc{
							Name:      "get_weather",
							Arguments: `{"city":"shanghai"}`,
						},
					},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	var items []map[string]any
	if err := json.Unmarshal(out.Input, &items); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}

	// 应产生 2 个 item：message（文本）+ function_call（工具调用）
	if len(items) != 2 {
		t.Fatalf("input length = %d, want 2 (message + function_call)", len(items))
	}

	msgItem := items[0]
	if msgItem["type"] != "message" {
		t.Fatalf("items[0].type = %v, want message", msgItem["type"])
	}
	if msgItem["role"] != "assistant" {
		t.Fatalf("items[0].role = %v, want assistant", msgItem["role"])
	}

	fcItem := items[1]
	if fcItem["type"] != "function_call" {
		t.Fatalf("items[1].type = %v, want function_call", fcItem["type"])
	}
	if fcItem["call_id"] != "call-xyz" {
		t.Fatalf("items[1].call_id = %v, want call-xyz", fcItem["call_id"])
	}
}

func TestOpenAIResponseCodec_EncodeRequest_ToOpenAIChat_Text(t *testing.T) {
	codec := &OpenAIResponseCodec{}
	temp := 0.8
	instrJSON, _ := json.Marshal("You are helpful")
	userContentJSON, _ := json.Marshal("Hello")
	assistantContentJSON, _ := json.Marshal("Hi there!")

	inputItems := []dto.ResponsesInputItem{
		{Type: "message", Role: "user", Content: json.RawMessage(userContentJSON)},
		{Type: "message", Role: "assistant", Content: json.RawMessage(assistantContentJSON)},
	}
	inputJSON, _ := json.Marshal(inputItems)

	req := &dto.ResponsesRequest{
		Model:           "gpt-4o",
		Stream:          true,
		Temperature:     &temp,
		MaxOutputTokens: 512,
		Instructions:    json.RawMessage(instrJSON),
		Input:           json.RawMessage(inputJSON),
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if out.Model != "gpt-4o-mini" {
		t.Fatalf("model = %q, want gpt-4o-mini", out.Model)
	}
	if !out.Stream {
		t.Fatalf("stream = false, want true")
	}
	if out.MaxTokens != 512 {
		t.Fatalf("max_tokens = %d, want 512", out.MaxTokens)
	}
	if out.Temperature == nil || *out.Temperature != temp {
		t.Fatalf("temperature not forwarded correctly")
	}
	if len(out.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(out.Messages))
	}
	if out.Messages[0].Role != "system" {
		t.Fatalf("messages[0].role = %q, want system", out.Messages[0].Role)
	}
	if out.Messages[0].Content != "You are helpful" {
		t.Fatalf("messages[0].content = %v, want 'You are helpful'", out.Messages[0].Content)
	}
	if out.Messages[1].Role != "user" {
		t.Fatalf("messages[1].role = %q, want user", out.Messages[1].Role)
	}
	if out.Messages[1].Content != "Hello" {
		t.Fatalf("messages[1].content = %v, want Hello", out.Messages[1].Content)
	}
	if out.Messages[2].Role != "assistant" {
		t.Fatalf("messages[2].role = %q, want assistant", out.Messages[2].Role)
	}
	if out.Messages[2].Content != "Hi there!" {
		t.Fatalf("messages[2].content = %v, want 'Hi there!'", out.Messages[2].Content)
	}
}

func TestOpenAIResponseCodec_EncodeRequest_ToOpenAIChat_InputString(t *testing.T) {
	codec := &OpenAIResponseCodec{}
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(`"hello from codex app"`),
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}
	if out.Messages[0].Role != "user" {
		t.Fatalf("messages[0].role = %q, want user", out.Messages[0].Role)
	}
	if out.Messages[0].Content != "hello from codex app" {
		t.Fatalf("messages[0].content = %v, want 'hello from codex app'", out.Messages[0].Content)
	}
}

func TestOpenAIResponseCodec_EncodeRequest_ToOpenAIChat_DeveloperRoleMappedToSystem(t *testing.T) {
	codec := &OpenAIResponseCodec{}
	inputItems := []dto.ResponsesInputItem{
		{Type: "message", Role: "user", Content: json.RawMessage(`"hello"`)},
		{Type: "message", Role: "developer", Content: json.RawMessage(`"be concise"`)},
	}
	inputJSON, _ := json.Marshal(inputItems)

	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(out.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(out.Messages))
	}
	if out.Messages[1].Role != "system" {
		t.Fatalf("messages[1].role = %q, want system", out.Messages[1].Role)
	}
	if out.Messages[1].Content != "be concise" {
		t.Fatalf("messages[1].content = %v, want 'be concise'", out.Messages[1].Content)
	}
}

func TestOpenAIResponseCodec_EncodeRequest_ToOpenAIChat_Tools(t *testing.T) {
	codec := &OpenAIResponseCodec{}
	inputItems := []dto.ResponsesInputItem{
		{
			Type:      "function_call",
			CallID:    "call-123",
			Name:      "get_weather",
			Arguments: `{"city":"beijing"}`,
		},
		{
			Type:   "function_call_output",
			CallID: "call-123",
			Output: json.RawMessage("\"sunny\""),
		},
	}
	inputJSON, _ := json.Marshal(inputItems)

	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
		Tools: []dto.ResponsesTool{
			{
				Type:        "function",
				Name:        "get_weather",
				Description: "Get weather info",
				Parameters:  map[string]any{"type": "object"},
			},
		},
		ToolChoice: map[string]any{
			"type": "function",
			"name": "get_weather",
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(out.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(out.Messages))
	}

	// function_call -> assistant message with ToolCalls
	assistantMsg := out.Messages[0]
	if assistantMsg.Role != "assistant" {
		t.Fatalf("messages[0].role = %q, want assistant", assistantMsg.Role)
	}
	if len(assistantMsg.ToolCalls) != 1 {
		t.Fatalf("messages[0].tool_calls len = %d, want 1", len(assistantMsg.ToolCalls))
	}
	tc := assistantMsg.ToolCalls[0]
	if tc.ID != "call-123" {
		t.Fatalf("tool_call.id = %q, want call-123", tc.ID)
	}
	// Type 字段在 request message 的 tool_calls 中不需要（会被上游某些 Provider 拒绝）
	// if tc.Type != "" {
	// 	t.Fatalf("tool_call.type = %q, want empty (omitempty)", tc.Type)
	// }
	if tc.Function.Name != "get_weather" {
		t.Fatalf("tool_call.function.name = %q, want get_weather", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"city":"beijing"}` {
		t.Fatalf("tool_call.function.arguments = %q", tc.Function.Arguments)
	}

	// function_call_output -> tool message
	toolMsg := out.Messages[1]
	if toolMsg.Role != "tool" {
		t.Fatalf("messages[1].role = %q, want tool", toolMsg.Role)
	}
	if toolMsg.ToolCallID != "call-123" {
		t.Fatalf("messages[1].tool_call_id = %q, want call-123", toolMsg.ToolCallID)
	}
	if toolMsg.Content != "sunny" {
		t.Fatalf("messages[1].content = %v, want sunny", toolMsg.Content)
	}

	// tools 转换
	if len(out.Tools) != 1 || out.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("tools not converted correctly")
	}
	if out.Tools[0].Type != "function" {
		t.Fatalf("tools[0].type = %q, want function", out.Tools[0].Type)
	}

	// tool_choice: {"type":"function","name":"X"} -> {"type":"function","function":{"name":"X"}}
	choice, ok := out.ToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("tool_choice is not map: %T", out.ToolChoice)
	}
	if choice["type"] != "function" {
		t.Fatalf("tool_choice.type = %v, want function", choice["type"])
	}
	fn, ok := choice["function"].(map[string]any)
	if !ok {
		t.Fatalf("tool_choice.function is not map: %T", choice["function"])
	}
	if fn["name"] != "get_weather" {
		t.Fatalf("tool_choice.function.name = %v, want get_weather", fn["name"])
	}
}

// TestOpenAIResponseCodec_EncodeRequest_ToOpenAIChat_EmptyArguments 测试空/无效 arguments 的标准化处理
func TestOpenAIResponseCodec_EncodeRequest_ToOpenAIChat_EmptyArguments(t *testing.T) {
	codec := &OpenAIResponseCodec{}

	tests := []struct {
		name        string
		arguments   string
		wantArgs    string
		description string
	}{
		{
			name:        "empty_string",
			arguments:   "",
			wantArgs:    "{}",
			description: "空字符串应转为空对象",
		},
		{
			name:        "invalid_json",
			arguments:   "not-json",
			wantArgs:    "{}",
			description: "无效 JSON 应转为空对象",
		},
		{
			name:        "valid_json_object",
			arguments:   `{"city":"beijing"}`,
			wantArgs:    `{"city":"beijing"}`,
			description: "有效 JSON 对象应保持不变",
		},
		{
			name:        "empty_json_object",
			arguments:   "{}",
			wantArgs:    "{}",
			description: "空 JSON 对象应保持不变",
		},
		{
			name:        "json_with_null",
			arguments:   `{"key":null}`,
			wantArgs:    `{"key":null}`,
			description: "包含 null 的 JSON 应保持不变",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputItems := []dto.ResponsesInputItem{
				{
					Type:      "function_call",
					CallID:    "call-test",
					Name:      "test_func",
					Arguments: tt.arguments,
				},
			}
			inputJSON, _ := json.Marshal(inputItems)

			req := &dto.ResponsesRequest{
				Model: "gpt-4o",
				Input: json.RawMessage(inputJSON),
			}

			payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini", false)
			if err != nil {
				t.Fatalf("%s: EncodeRequest error: %v", tt.description, err)
			}

			var out dto.ChatCompletionRequest
			if err := json.Unmarshal(payload, &out); err != nil {
				t.Fatalf("%s: failed to unmarshal: %v", tt.description, err)
			}

			if len(out.Messages) != 1 {
				t.Fatalf("%s: messages len = %d, want 1", tt.description, len(out.Messages))
			}

			msg := out.Messages[0]
			if msg.Role != "assistant" {
				t.Fatalf("%s: role = %q, want assistant", tt.description, msg.Role)
			}

			if len(msg.ToolCalls) != 1 {
				t.Fatalf("%s: tool_calls len = %d, want 1", tt.description, len(msg.ToolCalls))
			}

			tc := msg.ToolCalls[0]
			if tc.Function.Arguments != tt.wantArgs {
				t.Fatalf("%s: arguments = %q, want %q", tt.description, tc.Function.Arguments, tt.wantArgs)
			}
		})
	}
}

func TestOpenAIResponseCodec_EncodeRequest_ToOpenAIChat_InvalidInstructions(t *testing.T) {
	codec := &OpenAIResponseCodec{}
	req := &dto.ResponsesRequest{
		Model:        "gpt-4o",
		Instructions: json.RawMessage(`123`),
		Input:        json.RawMessage(`[]`),
	}

	_, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini", false)
	if err == nil {
		t.Fatalf("expected error for non-string instructions, got nil")
	}
}

func TestOpenAIResponseCodec_EncodeRequest_ToOpenAIChat_NonTextContent(t *testing.T) {
	codec := &OpenAIResponseCodec{}
	inputItems := []map[string]any{
		{
			"type":    "message",
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": "hello"}},
		},
	}
	inputJSON, _ := json.Marshal(inputItems)

	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}
	if out.Messages[0].Content != "hello" {
		t.Fatalf("messages[0].content = %v, want hello", out.Messages[0].Content)
	}
}

func TestOpenAIResponseCodec_EncodeRequest_ToOpenAIResponse_DirectPassthrough(t *testing.T) {
	codec := &OpenAIResponseCodec{}
	temp := 0.5
	instrJSON, _ := json.Marshal("Be concise")
	inputItems := []dto.ResponsesInputItem{
		{Type: "message", Role: "user", Content: json.RawMessage(`"hello"`)},
	}
	inputJSON, _ := json.Marshal(inputItems)

	req := &dto.ResponsesRequest{
		Model:           "gpt-4o",
		Stream:          true,
		Temperature:     &temp,
		MaxOutputTokens: 256,
		Instructions:    json.RawMessage(instrJSON),
		Input:           json.RawMessage(inputJSON),
	}

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if out.Model != "gpt-4o-mini" {
		t.Fatalf("model = %q, want gpt-4o-mini", out.Model)
	}
	if !out.Stream {
		t.Fatalf("stream = false, want true")
	}
	if out.MaxOutputTokens != 256 {
		t.Fatalf("max_output_tokens = %d, want 256", out.MaxOutputTokens)
	}

	var instrStr string
	if err := json.Unmarshal(out.Instructions, &instrStr); err != nil {
		t.Fatalf("failed to unmarshal instructions: %v", err)
	}
	if instrStr != "Be concise" {
		t.Fatalf("instructions = %q, want 'Be concise'", instrStr)
	}
}

func TestAnthropicMessagesCodec_EncodeRequest_ToOpenAIChat(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	req := &dto.ClaudeRequest{
		Model:  "claude-fast",
		Stream: true,
		System: "You are helpful.",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: []dto.ContentBlock{{Type: "text", Text: "Hello"}}},
			{Role: "assistant", Content: []dto.ContentBlock{{Type: "tool_use", ID: "tool-1", Name: "get_weather", Input: map[string]any{"city": "beijing"}}}},
			{Role: "user", Content: []dto.ContentBlock{{Type: "tool_result", ToolUseID: "tool-1", Content: "sunny"}}},
		},
		Tools: []dto.ClaudeTool{
			{
				Name:        "get_weather",
				Description: "Get weather",
				InputSchema: map[string]any{"type": "object"},
			},
		},
		ToolChoice: map[string]any{"type": "tool", "name": "get_weather"},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if out.Model != "gpt-4o" {
		t.Fatalf("model = %q, want %q", out.Model, "gpt-4o")
	}
	if !out.Stream {
		t.Fatalf("stream = %v, want true", out.Stream)
	}
	if len(out.Messages) == 0 || out.Messages[0].Role != "system" {
		t.Fatalf("system message missing")
	}
	if len(out.Tools) != 1 || out.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("tools not converted")
	}
	choice, ok := out.ToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("tool_choice not converted")
	}
	if choice["type"] != "function" {
		t.Fatalf("tool_choice type = %v, want function", choice["type"])
	}

	// Check for tool_use in ToolCalls (assistant message), not Content
	var toolUseFound bool
	for _, msg := range out.Messages {
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				if tc.ID == "tool-1" && tc.Function.Name == "get_weather" {
					toolUseFound = true
				}
			}
		}
	}
	if !toolUseFound {
		t.Fatalf("tool_use not found in assistant message ToolCalls")
	}

	// Check for tool_result as separate role="tool" message
	var toolResultFound bool
	for _, msg := range out.Messages {
		if msg.Role == "tool" && msg.ToolCallID == "tool-1" {
			toolResultFound = true
		}
	}
	if !toolResultFound {
		t.Fatalf("tool_result not found as separate role=tool message")
	}
}

func TestOpenAIResponseCodec_EncodeRequest_ToOpenAIChat_TextParts(t *testing.T) {
	codec := &OpenAIResponseCodec{}

	// content as array of input_text parts
	contentJSON, _ := json.Marshal([]map[string]any{
		{"type": "input_text", "text": "hello"},
	})
	inputItems := []dto.ResponsesInputItem{
		{Type: "message", Role: "user", Content: json.RawMessage(contentJSON)},
	}
	inputJSON, _ := json.Marshal(inputItems)

	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}
	if out.Messages[0].Content != "hello" {
		t.Fatalf("messages[0].content = %v, want hello", out.Messages[0].Content)
	}
}


func TestAnthropicMessagesCodec_EncodeRequest_ToOpenAIResponse_ViaChat_JSONPath(t *testing.T) {
	// 验证通过完整 JSON 序列化/反序列化路径时（模拟真实 HTTP 请求），
	// []any (map[string]any) 格式的 ContentBlock 能被正确转换。
	claudeReqJSON := `{
		"model": "claude-3",
		"system": "You are helpful.",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "What is the weather?"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "call-1", "name": "get_weather", "input": {"city": "beijing"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call-1", "content": "sunny"}]}
		],
		"stream": false
	}`

	var claudeReq dto.ClaudeRequest
	if err := json.Unmarshal([]byte(claudeReqJSON), &claudeReq); err != nil {
		t.Fatalf("failed to unmarshal claude request: %v", err)
	}

	codec := &AnthropicMessagesCodec{}
	payload, err := codec.EncodeRequest(FormatOpenAIResponse, &claudeReq, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal response request: %v", err)
	}

	if out.Model != "gpt-4o" {
		t.Fatalf("model = %q, want gpt-4o", out.Model)
	}

	var instrStr string
	if err := json.Unmarshal(out.Instructions, &instrStr); err != nil {
		t.Fatalf("instructions unmarshal error: %v", err)
	}
	if instrStr != "You are helpful." {
		t.Fatalf("instructions = %q, want 'You are helpful.'", instrStr)
	}

	var items []map[string]any
	if err := json.Unmarshal(out.Input, &items); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}

	// 预期：user message + function_call + function_call_output
	var foundUserMsg, foundFunctionCall, foundFunctionOutput bool
	for _, item := range items {
		switch item["type"] {
		case "message":
			if item["role"] == "user" {
				foundUserMsg = true
			}
		case "function_call":
			if item["call_id"] == "call-1" && item["name"] == "get_weather" {
				foundFunctionCall = true
			}
		case "function_call_output":
			if item["call_id"] == "call-1" && item["output"] == "sunny" {
				foundFunctionOutput = true
			}
		}
	}
	if !foundUserMsg {
		t.Fatalf("user message item not found in input")
	}
	if !foundFunctionCall {
		t.Fatalf("function_call item not found in input")
	}
	if !foundFunctionOutput {
		t.Fatalf("function_call_output item not found in input")
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_TopK 测试 TopK 字段透传
func TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_TopK(t *testing.T) {
	codec := &OpenAIChatCodec{}
	topK := 50
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
		},
		TopK: &topK,
	}

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if out.TopK == nil || *out.TopK != 50 {
		t.Fatalf("top_k = %v, want 50", out.TopK)
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_Metadata 测试 Metadata 字段透传
func TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_Metadata(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
		},
		Metadata: json.RawMessage(`{"key": "value", "number": 123}`),
	}

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// 验证 metadata 内容，忽略空格差异
	var gotMap, wantMap map[string]any
	if err := json.Unmarshal(out.Metadata, &gotMap); err != nil {
		t.Fatalf("failed to unmarshal got metadata: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"key": "value", "number": 123}`), &wantMap); err != nil {
		t.Fatalf("failed to unmarshal want metadata: %v", err)
	}
	if gotMap["key"] != wantMap["key"] || gotMap["number"] != wantMap["number"] {
		t.Fatalf("metadata content mismatch: got %v, want %v", gotMap, wantMap)
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_Thinking 测试 Thinking 字段透传
func TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_Thinking(t *testing.T) {
	codec := &OpenAIChatCodec{}
	// OpenAI 格式使用 THINKING RawMessage 字段，值为 JSON 对象
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
		},
		THINKING: json.RawMessage(`{"type": "enabled", "budget_tokens": 1000}`),
	}

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if out.Thinking == nil {
		t.Fatalf("thinking is nil, want non-nil")
	}
	if out.Thinking.Type != "enabled" {
		t.Fatalf("thinking.type = %q, want enabled", out.Thinking.Type)
	}
	if out.Thinking.BudgetTokens == nil || *out.Thinking.BudgetTokens != 1000 {
		t.Fatalf("thinking.budget_tokens = %v, want 1000", out.Thinking.BudgetTokens)
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_ServiceTier 测试 ServiceTier 字段透传
func TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_ServiceTier(t *testing.T) {
	codec := &OpenAIChatCodec{}
	// OpenAI 格式使用 RawMessage，Claude 格式使用 string
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
		},
		ServiceTier: json.RawMessage(`"priority"`),
	}

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if out.ServiceTier != "priority" {
		t.Fatalf("service_tier = %q, want priority", out.ServiceTier)
	}
}

// TestRequestAnthropicToOpenAI_TopK 测试 TopK 字段透传
func TestRequestAnthropicToOpenAI_TopK(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	topK := 40
	req := &dto.ClaudeRequest{
		Model:  "claude-fast",
		Stream: false,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
		},
		TopK: &topK,
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if out.TopK == nil || *out.TopK != 40 {
		t.Fatalf("top_k = %v, want 40", out.TopK)
	}
}

// TestRequestAnthropicToOpenAI_ServiceTier 测试 ServiceTier 字段透传
func TestRequestAnthropicToOpenAI_ServiceTier(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	req := &dto.ClaudeRequest{
		Model:  "claude-fast",
		Stream: false,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
		},
		ServiceTier: "priority",
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// ServiceTier 是 RawMessage，需要解析后比较
	var tier string
	if err := json.Unmarshal(out.ServiceTier, &tier); err != nil {
		t.Fatalf("failed to unmarshal service_tier: %v", err)
	}
	if tier != "priority" {
		t.Fatalf("service_tier = %q, want priority", tier)
	}
}

// TestRequestAnthropicToOpenAI_Metadata 测试 Metadata 字段透传
func TestRequestAnthropicToOpenAI_Metadata(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	req := &dto.ClaudeRequest{
		Model:  "claude-fast",
		Stream: false,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
		},
		Metadata: json.RawMessage(`{"user_id": "12345", "session": "abc"}`),
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// 验证 metadata 内容，忽略空格差异
	var gotMap, wantMap map[string]any
	if err := json.Unmarshal(out.Metadata, &gotMap); err != nil {
		t.Fatalf("failed to unmarshal got metadata: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"user_id": "12345", "session": "abc"}`), &wantMap); err != nil {
		t.Fatalf("failed to unmarshal want metadata: %v", err)
	}
	if gotMap["user_id"] != wantMap["user_id"] || gotMap["session"] != wantMap["session"] {
		t.Fatalf("metadata content mismatch: got %v, want %v", gotMap, wantMap)
	}
}

// TestRequestAnthropicToOpenAI_ThinkingToMetadata 测试 Thinking 字段存入 Metadata
func TestRequestAnthropicToOpenAI_ThinkingToMetadata(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	budgetTokens := 2000
	req := &dto.ClaudeRequest{
		Model:  "claude-fast",
		Stream: false,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
		},
		Thinking: &dto.Thinking{
			Type:         "enabled",
			BudgetTokens: &budgetTokens,
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Thinking 应该存入 Metadata 的 _thinking 字段
	var meta map[string]any
	if err := json.Unmarshal(out.Metadata, &meta); err != nil {
		t.Fatalf("failed to unmarshal metadata: %v", err)
	}
	thinking, ok := meta["_thinking"].(map[string]any)
	if !ok {
		t.Fatalf("metadata._thinking not found or wrong type: %T", meta["_thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("metadata._thinking.type = %v, want enabled", thinking["type"])
	}
}

// TestRequestAnthropicToOpenAI_InferenceGeoToMetadata 测试 InferenceGeo 字段存入 Metadata
func TestRequestAnthropicToOpenAI_InferenceGeoToMetadata(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	req := &dto.ClaudeRequest{
		Model:  "claude-fast",
		Stream: false,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
		},
		InferenceGeo: "EU",
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// InferenceGeo 应该存入 Metadata 的 _inference_geo 字段
	var meta map[string]any
	if err := json.Unmarshal(out.Metadata, &meta); err != nil {
		t.Fatalf("failed to unmarshal metadata: %v", err)
	}
	if meta["_inference_geo"] != "EU" {
		t.Fatalf("metadata._inference_geo = %v, want EU", meta["_inference_geo"])
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_ImageURL_DataURI 测试 image_url (data URI) 转换为 Anthropic image block
func TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_ImageURL_DataURI(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "What is in this image?"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQ"}},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}

	// Content 应该是 []ContentBlock
	blocks, ok := out.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("content is not []any, got %T", out.Messages[0].Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("content blocks len = %d, want 2", len(blocks))
	}

	// Check text block
	textBlock, ok := blocks[0].(map[string]any)
	if !ok {
		t.Fatalf("block[0] is not map, got %T", blocks[0])
	}
	if textBlock["type"] != "text" {
		t.Fatalf("block[0].type = %v, want text", textBlock["type"])
	}

	// Check image block with base64 source
	imageBlock, ok := blocks[1].(map[string]any)
	if !ok {
		t.Fatalf("block[1] is not map, got %T", blocks[1])
	}
	if imageBlock["type"] != "image" {
		t.Fatalf("block[1].type = %v, want image", imageBlock["type"])
	}

	source, ok := imageBlock["source"].(map[string]any)
	if !ok {
		t.Fatalf("source is not map, got %T", imageBlock["source"])
	}
	if source["type"] != "base64" {
		t.Fatalf("source.type = %v, want base64", source["type"])
	}
	if source["media_type"] != "image/jpeg" {
		t.Fatalf("source.media_type = %v, want image/jpeg", source["media_type"])
	}
	if source["data"] != "/9j/4AAQSkZJRgABAQ" {
		t.Fatalf("source.data = %v, want /9j/4AAQSkZJRgABAQ", source["data"])
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_ImageURL_HTTPURL 测试 image_url (HTTP URL) 转换为 Anthropic image block
func TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_ImageURL_HTTPURL(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/image.png"}},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}

	blocks, ok := out.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("content is not []any, got %T", out.Messages[0].Content)
	}
	if len(blocks) != 1 {
		t.Fatalf("content blocks len = %d, want 1", len(blocks))
	}

	imageBlock, ok := blocks[0].(map[string]any)
	if !ok {
		t.Fatalf("block[0] is not map, got %T", blocks[0])
	}
	if imageBlock["type"] != "image" {
		t.Fatalf("block[0].type = %v, want image", imageBlock["type"])
	}

	source, ok := imageBlock["source"].(map[string]any)
	if !ok {
		t.Fatalf("source is not map, got %T", imageBlock["source"])
	}
	if source["type"] != "url" {
		t.Fatalf("source.type = %v, want url", source["type"])
	}
	if source["url"] != "https://example.com/image.png" {
		t.Fatalf("source.url = %v, want https://example.com/image.png", source["url"])
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_File 测试 file 转换为 Anthropic document block
func TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_File(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "Read this document"},
					map[string]any{"type": "file", "file": map[string]any{"filename": "doc.pdf", "file_data": "data:application/pdf;base64,JVBERi0xLjQK"}},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}

	blocks, ok := out.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("content is not []any, got %T", out.Messages[0].Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("content blocks len = %d, want 2", len(blocks))
	}

	// Check document block
	docBlock, ok := blocks[1].(map[string]any)
	if !ok {
		t.Fatalf("block[1] is not map, got %T", blocks[1])
	}
	if docBlock["type"] != "document" {
		t.Fatalf("block[1].type = %v, want document", docBlock["type"])
	}

	source, ok := docBlock["source"].(map[string]any)
	if !ok {
		t.Fatalf("source is not map, got %T", docBlock["source"])
	}
	if source["type"] != "base64" {
		t.Fatalf("source.type = %v, want base64", source["type"])
	}
	if source["media_type"] != "application/pdf" {
		t.Fatalf("source.media_type = %v, want application/pdf", source["media_type"])
	}
	if source["data"] != "JVBERi0xLjQK" {
		t.Fatalf("source.data = %v, want JVBERi0xLjQK", source["data"])
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_InputAudio_Error 测试 input_audio 返回错误
func TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_InputAudio_Error(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "What did I say?"},
					map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "audio_data", "format": "wav"}},
				},
			},
		},
	}

	_, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err == nil {
		t.Fatalf("expected error for input_audio content type, got nil")
	}
	if err.Error() != "input_audio content type is not supported by Anthropic Messages API" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_VideoUrl_Error 测试 video_url 返回错误
func TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_VideoUrl_Error(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "video_url", "video_url": map[string]any{"url": "https://example.com/video.mp4"}},
				},
			},
		},
	}

	_, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err == nil {
		t.Fatalf("expected error for video_url content type, got nil")
	}
	if err.Error() != "video_url content type is not supported by Anthropic Messages API" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_MultimodalMixed 测试混合多模态内容
func TestOpenAIChatCodec_EncodeRequest_ToAnthropicMessages_MultimodalMixed(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "Describe this:"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,ABC123"}},
					map[string]any{"type": "file", "file": map[string]any{"file_data": "data:text/plain;base64,SGVsbG8="}},
					map[string]any{"type": "text", "text": "And this also."},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}

	blocks, ok := out.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("content is not []any, got %T", out.Messages[0].Content)
	}
	if len(blocks) != 4 {
		t.Fatalf("content blocks len = %d, want 4", len(blocks))
	}

	// Verify order: text, image, document, text
	if blocks[0].(map[string]any)["type"] != "text" {
		t.Fatalf("block[0].type = %v, want text", blocks[0].(map[string]any)["type"])
	}
	if blocks[1].(map[string]any)["type"] != "image" {
		t.Fatalf("block[1].type = %v, want image", blocks[1].(map[string]any)["type"])
	}
	if blocks[2].(map[string]any)["type"] != "document" {
		t.Fatalf("block[2].type = %v, want document", blocks[2].(map[string]any)["type"])
	}
	if blocks[3].(map[string]any)["type"] != "text" {
		t.Fatalf("block[3].type = %v, want text", blocks[3].(map[string]any)["type"])
	}
}

// TestAnthropicMessagesCodec_EncodeRequest_ToOpenAIChat_ImageBase64 测试 image block (base64) 转换为 OpenAI image_url
func TestAnthropicMessagesCodec_EncodeRequest_ToOpenAIChat_ImageBase64(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	req := &dto.ClaudeRequest{
		Model: "claude-fast",
		Messages: []dto.ClaudeMessage{
			{
				Role: "user",
				Content: []dto.ContentBlock{
					{Type: "text", Text: "What is in this image?"},
					{
						Type: "image",
						Source: &dto.MessageSource{
							Type:      "base64",
							MediaType: "image/jpeg",
							Data:      "/9j/4AAQSkZJRgABAQ",
						},
					},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}

	// Content 应该是 []MediaContent
	content, ok := out.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("content is not []any, got %T", out.Messages[0].Content)
	}
	if len(content) != 2 {
		t.Fatalf("content len = %d, want 2", len(content))
	}

	// Check text block
	textBlock, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] is not map, got %T", content[0])
	}
	if textBlock["type"] != "text" {
		t.Fatalf("content[0].type = %v, want text", textBlock["type"])
	}

	// Check image_url block
	imageBlock, ok := content[1].(map[string]any)
	if !ok {
		t.Fatalf("content[1] is not map, got %T", content[1])
	}
	if imageBlock["type"] != "image_url" {
		t.Fatalf("content[1].type = %v, want image_url", imageBlock["type"])
	}

	imageUrl, ok := imageBlock["image_url"].(map[string]any)
	if !ok {
		t.Fatalf("image_url is not map, got %T", imageBlock["image_url"])
	}
	expectedUrl := "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQ"
	if imageUrl["url"] != expectedUrl {
		t.Fatalf("image_url.url = %v, want %v", imageUrl["url"], expectedUrl)
	}
}

// TestAnthropicMessagesCodec_EncodeRequest_ToOpenAIChat_ImageUrl 测试 image block (url) 转换为 OpenAI image_url
func TestAnthropicMessagesCodec_EncodeRequest_ToOpenAIChat_ImageUrl(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	req := &dto.ClaudeRequest{
		Model: "claude-fast",
		Messages: []dto.ClaudeMessage{
			{
				Role: "user",
				Content: []dto.ContentBlock{
					{
						Type: "image",
						Source: &dto.MessageSource{
							Type: "url",
							Url:  "https://example.com/image.png",
						},
					},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}

	content, ok := out.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("content is not []any, got %T", out.Messages[0].Content)
	}
	if len(content) != 1 {
		t.Fatalf("content len = %d, want 1", len(content))
	}

	imageBlock, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] is not map, got %T", content[0])
	}
	if imageBlock["type"] != "image_url" {
		t.Fatalf("content[0].type = %v, want image_url", imageBlock["type"])
	}

	imageUrl, ok := imageBlock["image_url"].(map[string]any)
	if !ok {
		t.Fatalf("image_url is not map, got %T", imageBlock["image_url"])
	}
	if imageUrl["url"] != "https://example.com/image.png" {
		t.Fatalf("image_url.url = %v, want https://example.com/image.png", imageUrl["url"])
	}
}

// TestAnthropicMessagesCodec_EncodeRequest_ToOpenAIChat_Document 测试 document block 转换为 OpenAI file
func TestAnthropicMessagesCodec_EncodeRequest_ToOpenAIChat_Document(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	req := &dto.ClaudeRequest{
		Model: "claude-fast",
		Messages: []dto.ClaudeMessage{
			{
				Role: "user",
				Content: []dto.ContentBlock{
					{Type: "text", Text: "Read this document:"},
					{
						Type: "document",
						Source: &dto.MessageSource{
							Type:      "base64",
							MediaType: "application/pdf",
							Data:      "JVBERi0xLjQK",
						},
					},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}

	content, ok := out.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("content is not []any, got %T", out.Messages[0].Content)
	}
	if len(content) != 2 {
		t.Fatalf("content len = %d, want 2", len(content))
	}

	// Check file block
	fileBlock, ok := content[1].(map[string]any)
	if !ok {
		t.Fatalf("content[1] is not map, got %T", content[1])
	}
	if fileBlock["type"] != "file" {
		t.Fatalf("content[1].type = %v, want file", fileBlock["type"])
	}

	fileData, ok := fileBlock["file"].(map[string]any)
	if !ok {
		t.Fatalf("file is not map, got %T", fileBlock["file"])
	}
	expectedFileData := "data:application/pdf;base64,JVBERi0xLjQK"
	if fileData["file_data"] != expectedFileData {
		t.Fatalf("file.file_data = %v, want %v", fileData["file_data"], expectedFileData)
	}
}

// TestAnthropicMessagesCodec_EncodeRequest_ToOpenAIChat_MultimodalMixed 测试混合多模态内容转换
func TestAnthropicMessagesCodec_EncodeRequest_ToOpenAIChat_MultimodalMixed(t *testing.T) {
	codec := &AnthropicMessagesCodec{}
	req := &dto.ClaudeRequest{
		Model: "claude-fast",
		Messages: []dto.ClaudeMessage{
			{
				Role: "user",
				Content: []dto.ContentBlock{
					{Type: "text", Text: "Describe these:"},
					{
						Type: "image",
						Source: &dto.MessageSource{
							Type:      "base64",
							MediaType: "image/png",
							Data:      "ABC123",
						},
					},
					{
						Type: "document",
						Source: &dto.MessageSource{
							Type:      "base64",
							MediaType: "text/plain",
							Data:      "SGVsbG8=",
						},
					},
					{Type: "text", Text: "Thanks!"},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ChatCompletionRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(out.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out.Messages))
	}

	content, ok := out.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("content is not []any, got %T", out.Messages[0].Content)
	}
	if len(content) != 4 {
		t.Fatalf("content len = %d, want 4", len(content))
	}

	// Verify order: text, image_url, file, text
	if content[0].(map[string]any)["type"] != "text" {
		t.Fatalf("content[0].type = %v, want text", content[0].(map[string]any)["type"])
	}
	if content[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("content[1].type = %v, want image_url", content[1].(map[string]any)["type"])
	}
	if content[2].(map[string]any)["type"] != "file" {
		t.Fatalf("content[2].type = %v, want file", content[2].(map[string]any)["type"])
	}
	if content[3].(map[string]any)["type"] != "text" {
		t.Fatalf("content[3].type = %v, want text", content[3].(map[string]any)["type"])
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_MultimodalInputAudio 测试 input_audio 转换为 Responses API 格式
func TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_MultimodalInputAudio(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "What did I say?"},
					map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "audio_data_base64", "format": "wav"}},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	var items []map[string]any
	if err := json.Unmarshal(out.Input, &items); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("input length = %d, want 1", len(items))
	}

	var content []map[string]any
	contentBytes, _ := json.Marshal(items[0]["content"])
	if err := json.Unmarshal(contentBytes, &content); err != nil {
		t.Fatalf("failed to parse multimodal content: %v", err)
	}
	if len(content) != 2 {
		t.Fatalf("content length = %d, want 2", len(content))
	}

	if content[0]["type"] != "input_text" {
		t.Fatalf("content[0].type = %v, want input_text", content[0]["type"])
	}
	if content[1]["type"] != "input_audio" {
		t.Fatalf("content[1].type = %v, want input_audio", content[1]["type"])
	}
	if content[1]["audio_data"] != "audio_data_base64" {
		t.Fatalf("content[1].audio_data = %v, want audio_data_base64", content[1]["audio_data"])
	}
	if content[1]["format"] != "wav" {
		t.Fatalf("content[1].format = %v, want wav", content[1]["format"])
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_MultimodalFile 测试 file 转换为 Responses API 格式
func TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_MultimodalFile(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "Read this document:"},
					map[string]any{"type": "file", "file": map[string]any{"filename": "doc.pdf", "file_data": "data:application/pdf;base64,JVBERi0xLjQK"}},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	var items []map[string]any
	if err := json.Unmarshal(out.Input, &items); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}

	var content []map[string]any
	contentBytes, _ := json.Marshal(items[0]["content"])
	if err := json.Unmarshal(contentBytes, &content); err != nil {
		t.Fatalf("failed to parse multimodal content: %v", err)
	}
	if len(content) != 2 {
		t.Fatalf("content length = %d, want 2", len(content))
	}

	if content[0]["type"] != "input_text" {
		t.Fatalf("content[0].type = %v, want input_text", content[0]["type"])
	}
	if content[1]["type"] != "input_file" {
		t.Fatalf("content[1].type = %v, want input_file", content[1]["type"])
	}
	if content[1]["filename"] != "doc.pdf" {
		t.Fatalf("content[1].filename = %v, want doc.pdf", content[1]["filename"])
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_MultimodalVideo 测试 video_url 转换为 Responses API 格式
func TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_MultimodalVideo(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "Describe this video:"},
					map[string]any{"type": "video_url", "video_url": map[string]any{"url": "https://example.com/video.mp4"}},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	var items []map[string]any
	if err := json.Unmarshal(out.Input, &items); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}

	var content []map[string]any
	contentBytes, _ := json.Marshal(items[0]["content"])
	if err := json.Unmarshal(contentBytes, &content); err != nil {
		t.Fatalf("failed to parse multimodal content: %v", err)
	}
	if len(content) != 2 {
		t.Fatalf("content length = %d, want 2", len(content))
	}

	if content[0]["type"] != "input_text" {
		t.Fatalf("content[0].type = %v, want input_text", content[0]["type"])
	}
	if content[1]["type"] != "input_video" {
		t.Fatalf("content[1].type = %v, want input_video", content[1]["type"])
	}
	if content[1]["video_url"] != "https://example.com/video.mp4" {
		t.Fatalf("content[1].video_url = %v, want https://example.com/video.mp4", content[1]["video_url"])
	}
}

// TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_MultimodalMixed 测试混合多模态内容转换
func TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_MultimodalMixed(t *testing.T) {
	codec := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "Analyze these:"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,ABC123"}},
					map[string]any{"type": "file", "file": map[string]any{"file_data": "data:text/plain;base64,SGVsbG8="}},
					map[string]any{"type": "text", "text": "Thanks!"},
				},
			},
		},
	}

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var out dto.ResponsesRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	var items []map[string]any
	if err := json.Unmarshal(out.Input, &items); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}

	var content []map[string]any
	contentBytes, _ := json.Marshal(items[0]["content"])
	if err := json.Unmarshal(contentBytes, &content); err != nil {
		t.Fatalf("failed to parse multimodal content: %v", err)
	}
	if len(content) != 4 {
		t.Fatalf("content length = %d, want 4", len(content))
	}

	// 验证顺序: input_text, input_image, input_file, input_text
	if content[0]["type"] != "input_text" {
		t.Fatalf("content[0].type = %v, want input_text", content[0]["type"])
	}
	if content[1]["type"] != "input_image" {
		t.Fatalf("content[1].type = %v, want input_image", content[1]["type"])
	}
	if content[2]["type"] != "input_file" {
		t.Fatalf("content[2].type = %v, want input_file", content[2]["type"])
	}
	if content[3]["type"] != "input_text" {
		t.Fatalf("content[3].type = %v, want input_text", content[3]["type"])
	}
}

// =============================================================================
// 对齐 new-api 规则的测试
// =============================================================================

// TestOpenAIChatToAnthropic_MaxCompletionTokens 测试 max_completion_tokens 优先于 max_tokens
func TestOpenAIChatToAnthropic_MaxCompletionTokens(t *testing.T) {
	c := &OpenAIChatCodec{}
	mct := 1000
	req := &dto.ChatCompletionRequest{
		Model:               "gpt-4",
		Messages:            []dto.Message{{Role: "user", Content: "Hi"}},
		MaxTokens:           500,
		MaxCompletionTokens: &mct,
	}

	payload, err := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}
	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if out.MaxTokens != 1000 {
		t.Fatalf("max_tokens = %d, want 1000 (max_completion_tokens wins)", out.MaxTokens)
	}
}

// TestOpenAIChatToAnthropic_MaxTokensDefault 测试 max_tokens 缺失时固定回落 4096
func TestOpenAIChatToAnthropic_MaxTokensDefault(t *testing.T) {
	c := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "Hi"}},
	}

	payload, err := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}
	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if out.MaxTokens != 4096 {
		t.Fatalf("max_tokens = %d, want 4096 (default fallback)", out.MaxTokens)
	}
}

// TestOpenAIChatToAnthropic_StopStringAndArray 测试 stop 支持单字符串和字符串数组
func TestOpenAIChatToAnthropic_StopStringAndArray(t *testing.T) {
	c := &OpenAIChatCodec{}

	// 字符串数组
	var req dto.ChatCompletionRequest
	if err := json.Unmarshal([]byte(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "Hi"}],
		"stop": ["END", "STOP"]
	}`), &req); err != nil {
		t.Fatalf("unmarshal array: %v", err)
	}
	payload, _ := c.EncodeRequest(FormatAnthropicMessages, &req, "claude-3", false)
	var out dto.ClaudeRequest
	json.Unmarshal(payload, &out)
	if len(out.StopSequences) != 2 || out.StopSequences[0] != "END" || out.StopSequences[1] != "STOP" {
		t.Fatalf("stop_sequences = %v, want [END STOP]", out.StopSequences)
	}

	// 单字符串
	var req2 dto.ChatCompletionRequest
	if err := json.Unmarshal([]byte(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "Hi"}],
		"stop": "STOP"
	}`), &req2); err != nil {
		t.Fatalf("unmarshal string: %v", err)
	}
	payload2, _ := c.EncodeRequest(FormatAnthropicMessages, &req2, "claude-3", false)
	var out2 dto.ClaudeRequest
	json.Unmarshal(payload2, &out2)
	if len(out2.StopSequences) != 1 || out2.StopSequences[0] != "STOP" {
		t.Fatalf("stop_sequences = %v, want [STOP]", out2.StopSequences)
	}
}

// TestOpenAIChatToAnthropic_ToolChoice 测试 tool_choice 各映射规则
func TestOpenAIChatToAnthropic_ToolChoice(t *testing.T) {
	c := &OpenAIChatCodec{}

	tests := []struct {
		name          string
		toolChoice    any
		parallelCalls *bool
		expectType    string
		expectDisable *bool
	}{
		{"auto", "auto", nil, "auto", nil},
		{"none", "none", nil, "none", nil},
		{"required", "required", nil, "any", nil},
		{"tool_name", map[string]any{"function": map[string]any{"name": "f"}}, nil, "tool", nil},
		{"auto_parallel_false", "auto", boolPtr(false), "auto", boolPtr(true)},
		{"auto_parallel_true", "auto", boolPtr(true), "auto", boolPtr(false)},
		{"none_parallel_false", "none", boolPtr(false), "none", nil},
		{"parallel_only", nil, boolPtr(false), "auto", boolPtr(true)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &dto.ChatCompletionRequest{
				Model:             "gpt-4",
				Messages:          []dto.Message{{Role: "user", Content: "Hi"}},
				Tools:             []dto.Tool{{Type: "function", Function: dto.ToolFunction{Name: "f"}}},
				ToolChoice:        tt.toolChoice,
				ParallelToolCalls: tt.parallelCalls,
			}
			payload, err := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
			if err != nil {
				t.Fatalf("EncodeRequest error: %v", err)
			}
			var out dto.ClaudeRequest
			if err := json.Unmarshal(payload, &out); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			choice, ok := out.ToolChoice.(map[string]any)
			if !ok {
				t.Fatalf("tool_choice is not map: %T", out.ToolChoice)
			}
			if choice["type"] != tt.expectType {
				t.Fatalf("tool_choice.type = %v, want %v", choice["type"], tt.expectType)
			}
			if tt.expectDisable != nil {
				actual := choice["disable_parallel_tool_use"]
				if actual != *tt.expectDisable {
					t.Fatalf("disable_parallel_tool_use = %v, want %v", actual, *tt.expectDisable)
				}
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

// TestOpenAIChatToAnthropic_ReasoningEffort 测试 reasoning_effort 映射
func TestOpenAIChatToAnthropic_ReasoningEffort(t *testing.T) {
	c := &OpenAIChatCodec{}

	tests := []struct {
		effort       string
		expectType   string
		expectBudget int
	}{
		{"low", "enabled", 1280},
		{"medium", "enabled", 2048},
		{"high", "enabled", 4096},
		{"invalid", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.effort, func(t *testing.T) {
			req := &dto.ChatCompletionRequest{
				Model:           "gpt-4",
				Messages:        []dto.Message{{Role: "user", Content: "Hi"}},
				ReasoningEffort: tt.effort,
			}
			payload, err := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
			if err != nil {
				t.Fatalf("EncodeRequest error: %v", err)
			}
			var out dto.ClaudeRequest
			if err := json.Unmarshal(payload, &out); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			if tt.expectType == "" {
				if out.Thinking != nil {
					t.Fatalf("thinking should be nil for invalid effort, got %+v", out.Thinking)
				}
				return
			}
			if out.Thinking == nil {
				t.Fatal("thinking should not be nil")
			}
			if out.Thinking.Type != tt.expectType {
				t.Fatalf("thinking.type = %v, want %v", out.Thinking.Type, tt.expectType)
			}
			if out.Thinking.BudgetTokens == nil || *out.Thinking.BudgetTokens != tt.expectBudget {
				t.Fatalf("thinking.budget_tokens = %v, want %d", out.Thinking.BudgetTokens, tt.expectBudget)
			}
		})
	}
}

// TestOpenAIChatToAnthropic_Reasoning 测试 reasoning 字段映射
func TestOpenAIChatToAnthropic_Reasoning(t *testing.T) {
	c := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model:     "gpt-4",
		Messages:  []dto.Message{{Role: "user", Content: "Hi"}},
		Reasoning: json.RawMessage(`{"max_tokens": 3000}`),
	}

	payload, err := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}
	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if out.Thinking == nil || out.Thinking.Type != "enabled" {
		t.Fatal("thinking should be enabled")
	}
	if out.Thinking.BudgetTokens == nil || *out.Thinking.BudgetTokens != 3000 {
		t.Fatalf("thinking.budget_tokens = %v, want 3000", out.Thinking.BudgetTokens)
	}
}

// TestOpenAIChatToAnthropic_ReasoningOverridesTHINKING 测试 reasoning 优先于 THINKING
func TestOpenAIChatToAnthropic_ReasoningOverridesTHINKING(t *testing.T) {
	c := &OpenAIChatCodec{}

	req := &dto.ChatCompletionRequest{
		Model:           "gpt-4",
		Messages:        []dto.Message{{Role: "user", Content: "Hi"}},
		ReasoningEffort: "low",
		THINKING:        json.RawMessage(`{"type": "enabled", "budget_tokens": 9999}`),
	}
	payload, _ := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	var out dto.ClaudeRequest
	json.Unmarshal(payload, &out)
	if out.Thinking.BudgetTokens == nil || *out.Thinking.BudgetTokens != 1280 {
		t.Fatalf("thinking.budget_tokens = %v, want 1280 (reasoning_effort wins)", out.Thinking.BudgetTokens)
	}

	req2 := &dto.ChatCompletionRequest{
		Model:     "gpt-4",
		Messages:  []dto.Message{{Role: "user", Content: "Hi"}},
		Reasoning: json.RawMessage(`{"max_tokens": 2000}`),
		THINKING:  json.RawMessage(`{"type": "enabled", "budget_tokens": 9999}`),
	}
	payload2, _ := c.EncodeRequest(FormatAnthropicMessages, req2, "claude-3", false)
	var out2 dto.ClaudeRequest
	json.Unmarshal(payload2, &out2)
	if out2.Thinking.BudgetTokens == nil || *out2.Thinking.BudgetTokens != 2000 {
		t.Fatalf("thinking.budget_tokens = %v, want 2000 (reasoning wins)", out2.Thinking.BudgetTokens)
	}
}

// TestOpenAIChatToAnthropic_WebSearchOptions 测试 web_search_options 映射
func TestOpenAIChatToAnthropic_WebSearchOptions(t *testing.T) {
	c := &OpenAIChatCodec{}

	userLoc := `{"approximate":{"timezone":"Asia/Shanghai","country":"CN","region":"Shanghai","city":"Shanghai"}}`

	req := &dto.ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "Hi"}},
		WebSearchOptions: &dto.WebSearchOptions{
			SearchContextSize: "high",
			UserLocation:      json.RawMessage(userLoc),
		},
	}

	payload, err := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}
	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	tools, ok := out.Tools.([]any)
	if !ok {
		t.Fatalf("tools is not []any: %T", out.Tools)
	}
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}
	wsTool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool is not map: %T", tools[0])
	}
	if wsTool["type"] != "web_search_20250305" {
		t.Fatalf("type = %v, want web_search_20250305", wsTool["type"])
	}
	if wsTool["name"] != "web_search" {
		t.Fatalf("name = %v, want web_search", wsTool["name"])
	}
	if int(wsTool["max_uses"].(float64)) != 10 {
		t.Fatalf("max_uses = %v, want 10", wsTool["max_uses"])
	}

	loc, ok := wsTool["user_location"].(map[string]any)
	if !ok {
		t.Fatal("user_location not found")
	}
	if loc["type"] != "approximate" {
		t.Fatalf("user_location.type = %v, want approximate", loc["type"])
	}
	if loc["timezone"] != "Asia/Shanghai" {
		t.Fatalf("timezone = %v, want Asia/Shanghai", loc["timezone"])
	}
	if loc["country"] != "CN" {
		t.Fatalf("country = %v, want CN", loc["country"])
	}
}

// TestOpenAIChatToAnthropic_SystemArray 测试 system 聚合为数组
func TestOpenAIChatToAnthropic_SystemArray(t *testing.T) {
	c := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "system", Content: "First rule."},
			{Role: "system", Content: "Second rule."},
			{Role: "user", Content: "Hello"},
		},
	}

	payload, err := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}
	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	sysArr, ok := out.System.([]any)
	if !ok {
		t.Fatalf("system is not []any: %T", out.System)
	}
	if len(sysArr) != 1 {
		t.Fatalf("system array len = %d, want 1 (consecutive system messages merged)", len(sysArr))
	}
	s0, _ := sysArr[0].(map[string]any)
	if s0["type"] != "text" || s0["text"] != "First rule. Second rule." {
		t.Fatalf("system[0] = %v, want {type:text, text:First rule. Second rule.}", sysArr[0])
	}
}

// TestOpenAIChatToAnthropic_EmptyContent 测试空内容补 "..."
func TestOpenAIChatToAnthropic_EmptyContent(t *testing.T) {
	c := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: ""},
			{Role: "assistant", Content: nil},
		},
	}

	payload, err := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}
	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(out.Messages))
	}
	if out.Messages[0].Content != "..." {
		t.Fatalf("first msg content = %v, want ...", out.Messages[0].Content)
	}
	if out.Messages[1].Content != "..." {
		t.Fatalf("second msg content = %v, want ...", out.Messages[1].Content)
	}
}

// TestOpenAIChatToAnthropic_FirstNonUserPlaceholder 测试首条非 user 自动补 user 占位
func TestOpenAIChatToAnthropic_FirstNonUserPlaceholder(t *testing.T) {
	c := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "assistant", Content: "I am the assistant."},
		},
	}

	payload, err := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}
	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(out.Messages))
	}
	if out.Messages[0].Role != "user" {
		t.Fatalf("messages[0].role = %v, want user (placeholder)", out.Messages[0].Role)
	}
}

// TestOpenAIChatToAnthropic_ToolMergeIntoUser 测试 tool result 并入前一条 user 消息
func TestOpenAIChatToAnthropic_ToolMergeIntoUser(t *testing.T) {
	c := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Check weather"},
			{Role: "assistant", Content: "Will do", ToolCalls: []dto.ToolCall{{
				ID: "tool-1", Type: "function", Function: dto.ToolCallFunc{Name: "get_weather", Arguments: "{}"},
			}}},
			{Role: "tool", ToolCallID: "tool-1", Content: "sunny"},
			{Role: "user", Content: "Good"},
		},
	}

	payload, err := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}
	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(out.Messages) != 4 {
		t.Fatalf("messages len = %d, want 4 (user, assistant, user[tool_result], user)", len(out.Messages))
	}
}

// TestOpenAIChatToAnthropic_ToolNewUser 测试 tool result 在前一条非 user 时新建 user 消息
func TestOpenAIChatToAnthropic_ToolNewUser(t *testing.T) {
	c := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "assistant", Content: "Will do", ToolCalls: []dto.ToolCall{{
				ID: "tool-1", Type: "function", Function: dto.ToolCallFunc{Name: "get_weather", Arguments: "{}"},
			}}},
			{Role: "tool", ToolCallID: "tool-1", Content: "sunny"},
		},
	}

	payload, err := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}
	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(out.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(out.Messages))
	}
	if out.Messages[2].Role != "user" {
		t.Fatalf("messages[2].role = %v, want user", out.Messages[2].Role)
	}
}

// TestOpenAIChatToAnthropic_WebSearchWithOtherTools 测试 web_search 与其他 function tool 共存
func TestOpenAIChatToAnthropic_WebSearchWithOtherTools(t *testing.T) {
	c := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "Hi"}},
		Tools: []dto.Tool{
			{Type: "function", Function: dto.ToolFunction{Name: "my_func"}},
		},
		WebSearchOptions: &dto.WebSearchOptions{
			SearchContextSize: "medium",
		},
	}

	payload, err := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}
	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	tools, ok := out.Tools.([]any)
	if !ok {
		t.Fatalf("tools is not []any: %T", out.Tools)
	}
	if len(tools) != 2 {
		t.Fatalf("tools len = %d, want 2 (function + web_search)", len(tools))
	}
}

// TestOpenAIChatToAnthropic_ConsecutiveSameRoleTextMerge 测试连续同角色文本消息合并
func TestOpenAIChatToAnthropic_ConsecutiveSameRoleTextMerge(t *testing.T) {
	c := &OpenAIChatCodec{}
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
			{Role: "user", Content: "world"},
			{Role: "assistant", Content: "Hi"},
		},
	}

	payload, err := c.EncodeRequest(FormatAnthropicMessages, req, "claude-3", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}
	var out dto.ClaudeRequest
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2 (merged user + assistant)", len(out.Messages))
	}
	if out.Messages[0].Content != "Hello world" {
		t.Fatalf("merged user content = %v, want 'Hello world'", out.Messages[0].Content)
	}
}
