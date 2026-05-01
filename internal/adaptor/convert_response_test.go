package adaptor

import (
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

func strPtr(s string) *string { return &s }

func TestConvertClaudeResponseToOpenAI_TextOnly(t *testing.T) {
	claudeResp := &dto.ClaudeResponse{
		ID:    "msg-123",
		Model: "claude-3-5-sonnet",
		Content: []dto.ContentBlock{
			{Type: "text", Text: "Hello, world!"},
		},
		StopReason: strPtr("end_turn"),
		Usage:      dto.ClaudeUsage{InputTokens: 10, OutputTokens: 5},
	}
	got := ConvertClaudeResponseToOpenAI(claudeResp)
	if got.ID != "msg-123" {
		t.Errorf("id = %q, want msg-123", got.ID)
	}
	if len(got.Choices) != 1 {
		t.Fatalf("choices count = %d, want 1", len(got.Choices))
	}
	if got.Choices[0].Message.Content != "Hello, world!" {
		t.Errorf("content = %q, want 'Hello, world!'", got.Choices[0].Message.Content)
	}
	if got.Choices[0].FinishReason == nil || *got.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %v, want stop", got.Choices[0].FinishReason)
	}
	if got.Usage.PromptTokens != 10 || got.Usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v, want input=10 output=5", got.Usage)
	}
}

func TestConvertClaudeResponseToOpenAI_ToolUse(t *testing.T) {
	input := map[string]any{"city": "Beijing"}
	claudeResp := &dto.ClaudeResponse{
		ID:    "msg-456",
		Model: "claude-3-5-sonnet",
		Content: []dto.ContentBlock{
			{Type: "tool_use", ID: "toolu_01", Name: "get_weather", Input: input},
		},
		StopReason: strPtr("tool_use"),
		Usage:      dto.ClaudeUsage{InputTokens: 20, OutputTokens: 10},
	}
	got := ConvertClaudeResponseToOpenAI(claudeResp)
	if len(got.Choices) != 1 {
		t.Fatalf("choices count = %d, want 1", len(got.Choices))
	}
	if got.Choices[0].FinishReason == nil || *got.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %v, want tool_calls", got.Choices[0].FinishReason)
	}
	msg := got.Choices[0].Message
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool_calls count = %d, want 1", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].ID != "toolu_01" {
		t.Errorf("tool_call id = %q, want toolu_01", msg.ToolCalls[0].ID)
	}
	if msg.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("tool_call name = %q, want get_weather", msg.ToolCalls[0].Function.Name)
	}
}

func TestConvertClaudeResponseToOpenAI_MaxTokens(t *testing.T) {
	claudeResp := &dto.ClaudeResponse{
		ID:         "msg-789",
		Content:    []dto.ContentBlock{{Type: "text", Text: "..."}},
		StopReason: strPtr("max_tokens"),
		Usage:      dto.ClaudeUsage{},
	}
	got := ConvertClaudeResponseToOpenAI(claudeResp)
	if got.Choices[0].FinishReason == nil || *got.Choices[0].FinishReason != "length" {
		t.Errorf("finish_reason = %v, want length", got.Choices[0].FinishReason)
	}
}

func TestConvertClaudeStreamEventToOpenAI_TextDelta(t *testing.T) {
	event := &dto.ClaudeStreamEvent{
		Type:  "content_block_delta",
		Index: 0,
		Delta: &dto.ClaudeDelta{
			Type: "text_delta",
			Text: "Hello",
		},
	}
	got := ConvertClaudeStreamEventToOpenAI("chatcmpl-123", "claude-3", event)
	if got == nil {
		t.Fatal("expected non-nil chunk for text_delta")
	}
	if len(got.Choices) != 1 {
		t.Fatalf("choices count = %d, want 1", len(got.Choices))
	}
	if got.Choices[0].Delta.Content != "Hello" {
		t.Errorf("delta content = %q, want Hello", got.Choices[0].Delta.Content)
	}
}

func TestConvertClaudeStreamEventToOpenAI_MessageDelta_StopReason(t *testing.T) {
	event := &dto.ClaudeStreamEvent{
		Type: "message_delta",
		Delta: &dto.ClaudeDelta{
			StopReason: "end_turn",
		},
	}
	got := ConvertClaudeStreamEventToOpenAI("chatcmpl-123", "claude-3", event)
	if got == nil {
		t.Fatal("expected non-nil chunk for message_delta")
	}
	if len(got.Choices) != 1 {
		t.Fatalf("choices count = %d, want 1", len(got.Choices))
	}
	if got.Choices[0].FinishReason == nil || *got.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %v, want stop", got.Choices[0].FinishReason)
	}
}

func TestConvertClaudeStreamEventToOpenAI_MessageStop_ReturnsNil(t *testing.T) {
	event := &dto.ClaudeStreamEvent{Type: "message_stop"}
	got := ConvertClaudeStreamEventToOpenAI("chatcmpl-123", "claude-3", event)
	if got != nil {
		t.Error("message_stop event should return nil (caller sends [DONE])")
	}
}

func TestConvertClaudeStreamEventToOpenAI_ContentBlockStart_ToolUse(t *testing.T) {
	event := &dto.ClaudeStreamEvent{
		Type:  "content_block_start",
		Index: 0,
		ContentBlock: &dto.ContentBlock{
			Type: "tool_use",
			ID:   "toolu_01",
			Name: "get_weather",
		},
	}
	got := ConvertClaudeStreamEventToOpenAI("chatcmpl-123", "claude-3", event)
	if got == nil {
		t.Fatal("expected non-nil chunk for content_block_start/tool_use")
	}
	if len(got.Choices[0].Delta.ToolCalls) != 1 {
		t.Fatalf("tool_calls count = %d, want 1", len(got.Choices[0].Delta.ToolCalls))
	}
	if got.Choices[0].Delta.ToolCalls[0].ID != "toolu_01" {
		t.Errorf("tool_call id = %q, want toolu_01", got.Choices[0].Delta.ToolCalls[0].ID)
	}
}

func TestConvertClaudeStreamEventToOpenAI_FullFlow(t *testing.T) {
	events := []*dto.ClaudeStreamEvent{
		{Type: "message_start", Message: &dto.ClaudeMessageStart{ID: "msg-001", Model: "claude-3"}},
		{Type: "content_block_start", Index: 0, ContentBlock: &dto.ContentBlock{Type: "text"}},
		{Type: "content_block_delta", Index: 0, Delta: &dto.ClaudeDelta{Type: "text_delta", Text: "Hi"}},
		{Type: "content_block_stop"},
		{Type: "message_delta", Delta: &dto.ClaudeDelta{StopReason: "end_turn"}},
		{Type: "message_stop"},
	}

	responseID := "chatcmpl-test"
	var textChunks []string
	var finalFinishReason string

	for _, ev := range events {
		chunk := ConvertClaudeStreamEventToOpenAI(responseID, "claude-3", ev)
		if chunk == nil {
			continue
		}
		if len(chunk.Choices) > 0 {
			if chunk.Choices[0].Delta != nil && chunk.Choices[0].Delta.Content != "" {
				textChunks = append(textChunks, chunk.Choices[0].Delta.Content)
			}
			if chunk.Choices[0].FinishReason != nil {
				finalFinishReason = *chunk.Choices[0].FinishReason
			}
		}
	}

	if len(textChunks) != 1 || textChunks[0] != "Hi" {
		t.Errorf("textChunks = %v, want [Hi]", textChunks)
	}
	if finalFinishReason != "stop" {
		t.Errorf("finalFinishReason = %q, want stop", finalFinishReason)
	}
}

func TestConvertClaudeStreamEventToOpenAI_InputJsonDelta(t *testing.T) {
	event := &dto.ClaudeStreamEvent{
		Type:  "content_block_delta",
		Index: 1,
		Delta: &dto.ClaudeDelta{
			Type:        "input_json_delta",
			PartialJSON: `{"city":`,
		},
	}
	got := ConvertClaudeStreamEventToOpenAI("chatcmpl-123", "claude-3", event)
	if got == nil {
		t.Fatal("expected non-nil chunk for input_json_delta")
	}
	if got.Choices[0].Index != 1 {
		t.Errorf("choice index = %d, want 1", got.Choices[0].Index)
	}
	if len(got.Choices[0].Delta.ToolCalls) != 1 {
		t.Fatalf("tool_calls count = %d, want 1", len(got.Choices[0].Delta.ToolCalls))
	}
	if got.Choices[0].Delta.ToolCalls[0].Function.Arguments != `{"city":` {
		t.Errorf("arguments = %q, want {\"city\":", got.Choices[0].Delta.ToolCalls[0].Function.Arguments)
	}
}

func TestConvertClaudeStreamEventToOpenAI_ThinkingDelta(t *testing.T) {
	event := &dto.ClaudeStreamEvent{
		Type:  "content_block_delta",
		Index: 0,
		Delta: &dto.ClaudeDelta{
			Type:     "thinking_delta",
			Thinking: "Let me think...",
		},
	}
	got := ConvertClaudeStreamEventToOpenAI("chatcmpl-123", "claude-3", event)
	if got == nil {
		t.Fatal("expected non-nil chunk for thinking_delta")
	}
	if got.Choices[0].Delta.ReasoningContent != "Let me think..." {
		t.Errorf("reasoning_content = %q, want 'Let me think...'", got.Choices[0].Delta.ReasoningContent)
	}
}

func TestConvertClaudeStreamEventToOpenAI_ContentBlockStartText_ReturnsNil(t *testing.T) {
	event := &dto.ClaudeStreamEvent{
		Type:         "content_block_start",
		Index:        0,
		ContentBlock: &dto.ContentBlock{Type: "text"},
	}
	got := ConvertClaudeStreamEventToOpenAI("chatcmpl-123", "claude-3", event)
	if got != nil {
		t.Error("content_block_start with text type should return nil")
	}
}

func TestConvertClaudeStreamEventToOpenAI_Unknown_ReturnsNil(t *testing.T) {
	event := &dto.ClaudeStreamEvent{Type: "ping"}
	got := ConvertClaudeStreamEventToOpenAI("chatcmpl-123", "claude-3", event)
	if got != nil {
		t.Error("unknown event type should return nil")
	}
}

func TestConvertClaudeStreamEventToOpenAI_MessageStart_UpdatesIDAndModel(t *testing.T) {
	event := &dto.ClaudeStreamEvent{
		Type: "message_start",
		Message: &dto.ClaudeMessageStart{
			ID:    "msg-from-upstream",
			Model: "claude-3-5-sonnet",
		},
	}
	got := ConvertClaudeStreamEventToOpenAI("chatcmpl-initial", "unknown", event)
	if got == nil {
		t.Fatal("expected non-nil chunk for message_start")
	}
	if got.ID != "msg-from-upstream" {
		t.Errorf("chunk.ID = %q, want msg-from-upstream", got.ID)
	}
	if got.Model != "claude-3-5-sonnet" {
		t.Errorf("chunk.Model = %q, want claude-3-5-sonnet", got.Model)
	}
	if got.Choices[0].Delta.Role != "assistant" {
		t.Errorf("delta.role = %q, want assistant", got.Choices[0].Delta.Role)
	}
}
