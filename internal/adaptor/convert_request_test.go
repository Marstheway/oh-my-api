package adaptor

import (
	"encoding/json"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

func TestStopReasonToFinishReason(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"end_turn", "stop"},
		{"stop_sequence", "stop"},
		{"max_tokens", "length"},
		{"tool_use", "tool_calls"},
		{"", "stop"},
		{"unknown_xyz", "stop"},
	}
	for _, c := range cases {
		got := stopReasonToFinishReason(c.in)
		if got != c.want {
			t.Errorf("stopReasonToFinishReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFinishReasonToStopReason(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"tool_calls", "tool_use"},
		{"", "end_turn"},
		{"unknown_xyz", "end_turn"},
	}
	for _, c := range cases {
		got := finishReasonToStopReason(c.in)
		if got != c.want {
			t.Errorf("finishReasonToStopReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestContentBlock_ToolUseJSON(t *testing.T) {
	block := dto.ContentBlock{
		Type:  "tool_use",
		ID:    "toolu_01",
		Name:  "get_weather",
		Input: map[string]any{"city": "Beijing"},
	}
	data, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["id"] != "toolu_01" {
		t.Errorf("id = %v, want toolu_01", m["id"])
	}
	if m["name"] != "get_weather" {
		t.Errorf("name = %v, want get_weather", m["name"])
	}
	if m["input"] == nil {
		t.Error("input should not be nil")
	}
}

func TestContentBlock_ToolResultJSON(t *testing.T) {
	block := dto.ContentBlock{
		Type:      "tool_result",
		ToolUseID: "toolu_01",
		Content:   "sunny",
	}
	data, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["tool_use_id"] != "toolu_01" {
		t.Errorf("tool_use_id = %v, want toolu_01", m["tool_use_id"])
	}
	if m["content"] != "sunny" {
		t.Errorf("content = %v, want sunny", m["content"])
	}
}

func TestConvertClaudeToOpenAI_StringContent(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
		},
	}
	got := ConvertClaudeToOpenAI(req, "gpt-4o")
	if got.Model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", got.Model)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(got.Messages))
	}
	if got.Messages[0].Content != "Hello" {
		t.Errorf("content = %v, want Hello", got.Messages[0].Content)
	}
}

func TestConvertClaudeToOpenAI_BlockContentMissingType(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Messages: []dto.ClaudeMessage{
			{
				Role: "user",
				Content: []any{
					map[string]any{"text": "Hello there"},       // type 缺失
					map[string]any{"type": "text", "text": "!"}, // 正常
				},
			},
		},
	}
	got := ConvertClaudeToOpenAI(req, "gpt-4o")
	if len(got.Messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(got.Messages))
	}
	blocks, ok := got.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("content should be []any, got %T", got.Messages[0].Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks count = %d, want 2", len(blocks))
	}
	first := blocks[0].(map[string]any)
	if first["type"] != "text" {
		t.Errorf("block[0].type = %v, want text (should be auto-filled)", first["type"])
	}
}

func TestConvertClaudeToOpenAI_SystemString(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		System:    "You are helpful.",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hi"},
		},
	}
	got := ConvertClaudeToOpenAI(req, "gpt-4o")
	if len(got.Messages) != 2 {
		t.Fatalf("messages count = %d, want 2 (system+user)", len(got.Messages))
	}
	if got.Messages[0].Role != "system" {
		t.Errorf("first message role = %q, want system", got.Messages[0].Role)
	}
	if got.Messages[0].Content != "You are helpful." {
		t.Errorf("system content = %v, want 'You are helpful.'", got.Messages[0].Content)
	}
}

func TestConvertClaudeToOpenAI_SystemBlockArray(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		System: []any{
			map[string]any{"type": "text", "text": "Be helpful."},
		},
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hi"},
		},
	}
	got := ConvertClaudeToOpenAI(req, "gpt-4o")
	if len(got.Messages) < 2 {
		t.Fatalf("messages count = %d, want >= 2", len(got.Messages))
	}
	if got.Messages[0].Role != "system" {
		t.Errorf("first message role = %q, want system", got.Messages[0].Role)
	}
}

func TestConvertClaudeToOpenAI_EmptyRoleFixed(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Messages: []dto.ClaudeMessage{
			{Role: "", Content: "Hello"},
		},
	}
	got := ConvertClaudeToOpenAI(req, "gpt-4o")
	if len(got.Messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(got.Messages))
	}
	if got.Messages[0].Role == "" {
		t.Error("empty role should be fixed to 'user'")
	}
}

func TestConvertOpenAIToClaudeV2_StringContent(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "system", Content: "Be helpful."},
			{Role: "user", Content: "Hello"},
		},
		MaxTokens: 100,
	}
	got := ConvertOpenAIToClaudeV2(req, "claude-3-5-sonnet")
	if got.Model != "claude-3-5-sonnet" {
		t.Errorf("model = %q, want claude-3-5-sonnet", got.Model)
	}
	if got.System != "Be helpful." {
		t.Errorf("system = %v, want 'Be helpful.'", got.System)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages count = %d, want 1 (system extracted)", len(got.Messages))
	}
	if got.Messages[0].Content != "Hello" {
		t.Errorf("user content = %v, want Hello", got.Messages[0].Content)
	}
}

func TestConvertOpenAIToClaudeV2_DefaultMaxTokens(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "Hi"}},
	}
	got := ConvertOpenAIToClaudeV2(req, "claude-3-5-sonnet")
	if got.MaxTokens != 4096 {
		t.Errorf("max_tokens = %d, want 4096", got.MaxTokens)
	}
}

func TestConvertOpenAIToClaudeV2_ToolCalls(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "What's the weather?"},
			{
				Role:    "assistant",
				Content: nil,
				ToolCalls: []dto.ToolCall{
					{
						ID:   "call_abc",
						Type: "function",
						Function: dto.ToolCallFunc{
							Name:      "get_weather",
							Arguments: `{"city":"Beijing"}`,
						},
					},
				},
			},
			{
				Role:       "tool",
				Content:    "Sunny, 25°C",
				ToolCallID: "call_abc",
			},
		},
	}
	got := ConvertOpenAIToClaudeV2(req, "claude-3-5-sonnet")

	if len(got.Messages) != 3 {
		t.Fatalf("messages count = %d, want 3, msgs: %+v", len(got.Messages), got.Messages)
	}

	assistantMsg := got.Messages[1]
	if assistantMsg.Role != "assistant" {
		t.Errorf("msg[1].role = %q, want assistant", assistantMsg.Role)
	}
	assistantBlocks, ok := assistantMsg.Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("assistant content type = %T, want []dto.ContentBlock", assistantMsg.Content)
	}
	if len(assistantBlocks) != 1 || assistantBlocks[0].Type != "tool_use" {
		t.Errorf("assistant blocks = %+v, want 1 tool_use block", assistantBlocks)
	}
	if assistantBlocks[0].ID != "call_abc" {
		t.Errorf("tool_use id = %q, want call_abc", assistantBlocks[0].ID)
	}

	toolResultMsg := got.Messages[2]
	if toolResultMsg.Role != "user" {
		t.Errorf("msg[2].role = %q, want user", toolResultMsg.Role)
	}
	toolBlocks, ok := toolResultMsg.Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("tool result content type = %T, want []dto.ContentBlock", toolResultMsg.Content)
	}
	if len(toolBlocks) != 1 || toolBlocks[0].Type != "tool_result" {
		t.Errorf("tool result blocks = %+v, want 1 tool_result block", toolBlocks)
	}
	if toolBlocks[0].ToolUseID != "call_abc" {
		t.Errorf("tool_use_id = %q, want call_abc", toolBlocks[0].ToolUseID)
	}
}

func TestConvertOpenAIToClaudeV2_AssistantTextAndToolResultPreserved(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Question"},
			{
				Role:    "assistant",
				Content: "Let me call tool",
				ToolCalls: []dto.ToolCall{
					{
						ID:   "call_abc",
						Type: "function",
						Function: dto.ToolCallFunc{
							Name:      "get_weather",
							Arguments: `{"city":"Beijing"}`,
						},
					},
				},
			},
			{Role: "tool", Content: "Sunny", ToolCallID: "call_abc"},
		},
	}

	got := ConvertOpenAIToClaudeV2(req, "claude-3-5-sonnet")
	if len(got.Messages) != 3 {
		t.Fatalf("messages count = %d, want 3", len(got.Messages))
	}

	assistantBlocks, ok := got.Messages[1].Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("assistant content type = %T, want []dto.ContentBlock", got.Messages[1].Content)
	}
	if len(assistantBlocks) != 2 {
		t.Fatalf("assistant blocks = %d, want 2(text + tool_use)", len(assistantBlocks))
	}
	if assistantBlocks[0].Type != "text" || assistantBlocks[0].Text != "Let me call tool" {
		t.Errorf("assistant first block = %+v, want text block", assistantBlocks[0])
	}

	toolResultMsg := got.Messages[2]
	if toolResultMsg.Role != "user" {
		t.Fatalf("tool result role = %q, want user", toolResultMsg.Role)
	}
	userBlocks, ok := toolResultMsg.Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("tool result content type = %T, want []dto.ContentBlock", toolResultMsg.Content)
	}
	if len(userBlocks) != 1 {
		t.Fatalf("tool result blocks = %d, want 1(tool_result)", len(userBlocks))
	}
	if userBlocks[0].Type != "tool_result" || userBlocks[0].ToolUseID != "call_abc" {
		t.Errorf("tool result block = %+v, want tool_result/call_abc", userBlocks[0])
	}
}

func TestConvertOpenAIToClaudeV2_ConsecutiveToolMessagesMerged(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Question"},
			{
				Role: "assistant",
				ToolCalls: []dto.ToolCall{
					{ID: "call_1", Type: "function", Function: dto.ToolCallFunc{Name: "a", Arguments: `{}`}},
					{ID: "call_2", Type: "function", Function: dto.ToolCallFunc{Name: "b", Arguments: `{}`}},
				},
			},
			{Role: "tool", Content: "result1", ToolCallID: "call_1"},
			{Role: "tool", Content: "result2", ToolCallID: "call_2"},
		},
	}

	got := ConvertOpenAIToClaudeV2(req, "claude-3-5-sonnet")
	if len(got.Messages) != 3 {
		t.Fatalf("messages count = %d, want 3(user, assistant, merged tool-results)", len(got.Messages))
	}
	merged := got.Messages[2]
	blocks, ok := merged.Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("merged tool content type = %T, want []dto.ContentBlock", merged.Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("merged tool blocks = %d, want 2", len(blocks))
	}
	if blocks[0].ToolUseID != "call_1" || blocks[1].ToolUseID != "call_2" {
		t.Errorf("merged tool_use_id sequence = [%s,%s], want [call_1,call_2]", blocks[0].ToolUseID, blocks[1].ToolUseID)
	}
}

func TestConvertOpenAIToClaudeV2_FirstMessageNotUser(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "assistant", Content: "Hello, I'm ready."},
		},
	}
	got := ConvertOpenAIToClaudeV2(req, "claude-3-5-sonnet")
	if len(got.Messages) < 2 {
		t.Fatalf("messages count = %d, want >= 2 (placeholder + assistant)", len(got.Messages))
	}
	if got.Messages[0].Role != "user" {
		t.Errorf("first message role = %q, want user (placeholder)", got.Messages[0].Role)
	}
}

func TestConvertOpenAIToClaudeV2_ConsecutiveSameRole(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
			{Role: "user", Content: "World"},
		},
	}
	got := ConvertOpenAIToClaudeV2(req, "claude-3-5-sonnet")
	if len(got.Messages) != 1 {
		t.Fatalf("messages count = %d, want 1 (merged)", len(got.Messages))
	}
}

func TestConvertOpenAIToClaudeV2_StopConversion(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "Hi"}},
		Stop:     []string{"STOP", "END"},
	}
	got := ConvertOpenAIToClaudeV2(req, "claude-3-5-sonnet")
	if len(got.StopSequences) != 2 {
		t.Errorf("stop_sequences = %v, want [STOP END]", got.StopSequences)
	}
}

func TestConvertOpenAIToClaudeV2_SystemBlockArray(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{
				Role: "system",
				Content: []any{
					map[string]any{"type": "text", "text": "Be helpful."},
				},
			},
			{Role: "user", Content: "Hi"},
		},
	}
	got := ConvertOpenAIToClaudeV2(req, "claude-3-5-sonnet")
	if got.System != "Be helpful." {
		t.Errorf("system = %v, want 'Be helpful.'", got.System)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages count = %d, want 1 (system extracted)", len(got.Messages))
	}
}

func TestConvertClaudeToOpenAI_WithTools(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Messages:  []dto.ClaudeMessage{{Role: "user", Content: "What's the weather?"}},
		Tools: []dto.ClaudeTool{
			{
				Name:        "get_weather",
				Description: "Get current weather",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"city": map[string]any{"type": "string"},
					},
					"required": []string{"city"},
				},
			},
		},
	}
	got := ConvertClaudeToOpenAI(req, "gpt-4o")
	if len(got.Tools) != 1 {
		t.Fatalf("tools count = %d, want 1", len(got.Tools))
	}
	if got.Tools[0].Type != "function" {
		t.Errorf("tool type = %q, want function", got.Tools[0].Type)
	}
	if got.Tools[0].Function.Name != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", got.Tools[0].Function.Name)
	}
	if got.Tools[0].Function.Description != "Get current weather" {
		t.Errorf("tool description = %q, want 'Get current weather'", got.Tools[0].Function.Description)
	}
}

func TestConvertClaudeToOpenAI_ToolChoice_Auto(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model:      "claude-3",
		MaxTokens:  100,
		Messages:   []dto.ClaudeMessage{{Role: "user", Content: "Hi"}},
		ToolChoice: map[string]any{"type": "auto"},
	}
	got := ConvertClaudeToOpenAI(req, "gpt-4o")
	if got.ToolChoice != "auto" {
		t.Errorf("tool_choice = %v, want 'auto'", got.ToolChoice)
	}
}

func TestConvertClaudeToOpenAI_ToolChoice_Any(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model:      "claude-3",
		MaxTokens:  100,
		Messages:   []dto.ClaudeMessage{{Role: "user", Content: "Hi"}},
		ToolChoice: map[string]any{"type": "any"},
	}
	got := ConvertClaudeToOpenAI(req, "gpt-4o")
	if got.ToolChoice != "required" {
		t.Errorf("tool_choice = %v, want 'required'", got.ToolChoice)
	}
}

func TestConvertClaudeToOpenAI_ToolChoice_SpecificTool(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Messages:  []dto.ClaudeMessage{{Role: "user", Content: "Hi"}},
		ToolChoice: map[string]any{
			"type": "tool",
			"name": "get_weather",
		},
	}
	got := ConvertClaudeToOpenAI(req, "gpt-4o")
	m, ok := got.ToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("tool_choice type = %T, want map[string]any", got.ToolChoice)
	}
	if m["type"] != "function" {
		t.Errorf("tool_choice.type = %v, want function", m["type"])
	}
	fn, ok := m["function"].(map[string]any)
	if !ok {
		t.Fatalf("tool_choice.function type = %T, want map[string]any", m["function"])
	}
	if fn["name"] != "get_weather" {
		t.Errorf("tool_choice.function.name = %v, want get_weather", fn["name"])
	}
}

func TestConvertOpenAIToClaudeV2_WithTools(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "What's the weather?"},
		},
		Tools: []dto.Tool{
			{
				Type: "function",
				Function: dto.ToolFunction{
					Name:        "get_weather",
					Description: "Get weather",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"city": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	}
	got := ConvertOpenAIToClaudeV2(req, "claude-3-5-sonnet")
	if len(got.Tools) != 1 {
		t.Fatalf("tools count = %d, want 1", len(got.Tools))
	}
	if got.Tools[0].Name != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", got.Tools[0].Name)
	}
	if got.Tools[0].Description != "Get weather" {
		t.Errorf("tool description = %q, want 'Get weather'", got.Tools[0].Description)
	}
}
