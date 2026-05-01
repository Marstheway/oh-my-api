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

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o")
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

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3")
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
	if len(out.Tools) != 1 || out.Tools[0].Name != "get_weather" {
		t.Fatalf("tools not converted")
	}
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

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3")
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
	if len(out.Tools) != 1 || out.Tools[0].Name != "get_weather" {
		t.Fatalf("tools not preserved")
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

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini")
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

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini")
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

func TestOpenAIChatCodec_EncodeRequest_ToOpenAIResponse_MultimodalUnsupported(t *testing.T) {
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

	_, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini")
	if err == nil {
		t.Fatalf("expected error for image_url content part, got nil")
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

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini")
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

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini")
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

	payload, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3")
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

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o")
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

	_, err := codec.EncodeRequest(FormatAnthropicMessages, req, "claude-3")
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

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o")
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

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini")
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

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini")
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

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini")
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

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini")
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
			Output: "sunny",
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

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini")
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
	if tc.Type != "function" {
		t.Fatalf("tool_call.type = %q, want function", tc.Type)
	}
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

func TestOpenAIResponseCodec_EncodeRequest_ToOpenAIChat_InvalidInstructions(t *testing.T) {
	codec := &OpenAIResponseCodec{}
	req := &dto.ResponsesRequest{
		Model:        "gpt-4o",
		Instructions: json.RawMessage(`123`),
		Input:        json.RawMessage(`[]`),
	}

	_, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini")
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

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini")
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

	payload, err := codec.EncodeRequest(FormatOpenAIResponse, req, "gpt-4o-mini")
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

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o")
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

	var toolUseFound bool
	var toolResultFound bool
	for _, msg := range out.Messages {
		switch msg.Role {
		case "assistant":
			switch blocks := msg.Content.(type) {
			case []dto.ContentBlock:
				for _, block := range blocks {
					if block.Type == "tool_use" && block.ID == "tool-1" && block.Name == "get_weather" {
						toolUseFound = true
					}
				}
			case []any:
				for _, item := range blocks {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					if m["type"] == "tool_use" && m["id"] == "tool-1" && m["name"] == "get_weather" {
						toolUseFound = true
					}
				}
			}
		case "user":
			switch blocks := msg.Content.(type) {
			case []dto.ContentBlock:
				for _, block := range blocks {
					if block.Type == "tool_result" && block.ToolUseID == "tool-1" {
						toolResultFound = true
					}
				}
			case []any:
				for _, item := range blocks {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					if m["type"] == "tool_result" && m["tool_use_id"] == "tool-1" {
						toolResultFound = true
					}
				}
			}
		}
	}
	if !toolUseFound {
		t.Fatalf("tool_use block not preserved in messages")
	}
	if !toolResultFound {
		t.Fatalf("tool_result block not preserved in messages")
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

	payload, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini")
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

func TestOpenAIResponseCodec_EncodeRequest_ToOpenAIChat_UnsupportedPart(t *testing.T) {
	codec := &OpenAIResponseCodec{}

	contentJSON, _ := json.Marshal([]map[string]any{
		{"type": "input_image", "image_url": "https://example.com/img.png"},
	})
	inputItems := []dto.ResponsesInputItem{
		{Type: "message", Role: "user", Content: json.RawMessage(contentJSON)},
	}
	inputJSON, _ := json.Marshal(inputItems)

	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	_, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o-mini")
	if err == nil {
		t.Fatalf("expected error for unsupported input_image part, got nil")
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
	payload, err := codec.EncodeRequest(FormatOpenAIResponse, &claudeReq, "gpt-4o")
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
