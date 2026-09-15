package codec

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

// TestClaudeToChatStreamMapper_ThinkingDelta tests thinking_delta event conversion
func TestClaudeToChatStreamMapper_ThinkingDelta(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("msg-1", "claude-3", 1234)

	// Test message_start
	events := []dto.ClaudeStreamEvent{
		{Type: "message_start", Message: &dto.ClaudeMessageStart{ID: "msg-1", Model: "claude-3"}},
	}

	for _, event := range events {
		chunks, err := mapper.Map(event)
		if err != nil {
			t.Fatalf("Map returned error: %v", err)
		}
		if len(chunks) == 0 {
			continue
		}
		// message_start should produce a role delta
		if chunks[0].Choices[0].Delta.Role != "assistant" {
			t.Fatalf("expected role 'assistant', got %q", chunks[0].Choices[0].Delta.Role)
		}
	}

	// Test thinking_delta
	thinking := "Let me think about this..."
	event := dto.ClaudeStreamEvent{
		Type:  "content_block_delta",
		Index: 0,
		Delta: &dto.ClaudeDelta{Type: "thinking_delta", Thinking: thinking},
	}

	chunks, err := mapper.Map(event)
	if err != nil {
		t.Fatalf("Map returned error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Choices[0].Delta.ReasoningContent != thinking {
		t.Fatalf("expected ReasoningContent %q, got %q", thinking, chunks[0].Choices[0].Delta.ReasoningContent)
	}
}

// TestClaudeToChatStreamMapper_PartialJSON tests input_json_delta with pointer PartialJSON
func TestClaudeToChatStreamMapper_PartialJSON(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("msg-1", "claude-3", 1234)

	// Test with non-nil PartialJSON
	partial := "{\"key\":"
	event := dto.ClaudeStreamEvent{
		Type:  "content_block_delta",
		Index: 0,
		Delta: &dto.ClaudeDelta{Type: "input_json_delta", PartialJSON: &partial},
	}

	chunks, err := mapper.Map(event)
	if err != nil {
		t.Fatalf("Map returned error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if len(chunks[0].Choices[0].Delta.ToolCalls) == 0 {
		t.Fatalf("expected ToolCalls in delta")
	}
	if chunks[0].Choices[0].Delta.ToolCalls[0].Function.Arguments != partial {
		t.Fatalf("expected Arguments %q, got %q", partial, chunks[0].Choices[0].Delta.ToolCalls[0].Function.Arguments)
	}

	// Test with nil PartialJSON
	eventNil := dto.ClaudeStreamEvent{
		Type:  "content_block_delta",
		Index: 0,
		Delta: &dto.ClaudeDelta{Type: "input_json_delta", PartialJSON: nil},
	}

	chunks, err = mapper.Map(eventNil)
	if err != nil {
		t.Fatalf("Map returned error: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for nil PartialJSON, got %d", len(chunks))
	}
}

func TestClaudeToChatStreamMapper_ToolUsePartialJSONPreservesIndexAndID(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("msg-1", "claude-3", 1234)

	startChunks, err := mapper.Map(dto.ClaudeStreamEvent{
		Type:  "content_block_start",
		Index: 2,
		ContentBlock: &dto.ContentBlock{
			Type: "tool_use",
			ID:   "call-1",
			Name: "exec_command",
		},
	})
	if err != nil {
		t.Fatalf("Map start error: %v", err)
	}
	if len(startChunks) != 1 {
		t.Fatalf("start len = %d, want 1", len(startChunks))
	}
	if got := startChunks[0].Choices[0].Delta.ToolCalls[0].GetIndex(); got != 2 {
		t.Fatalf("start index = %d, want 2", got)
	}
	if got := startChunks[0].Choices[0].Delta.ToolCalls[0].ID; got != "call-1" {
		t.Fatalf("start id = %q, want call-1", got)
	}

	partial1 := "{\"cmd\":\"git diff"
	chunks1, err := mapper.Map(dto.ClaudeStreamEvent{
		Type:  "content_block_delta",
		Index: 2,
		Delta: &dto.ClaudeDelta{Type: "input_json_delta", PartialJSON: &partial1},
	})
	if err != nil {
		t.Fatalf("Map partial1 error: %v", err)
	}
	if len(chunks1) != 1 {
		t.Fatalf("partial1 len = %d, want 1", len(chunks1))
	}
	if got := chunks1[0].Choices[0].Delta.ToolCalls[0].GetIndex(); got != 2 {
		t.Fatalf("partial1 index = %d, want 2", got)
	}
	if got := chunks1[0].Choices[0].Delta.ToolCalls[0].ID; got != "call-1" {
		t.Fatalf("partial1 id = %q, want call-1", got)
	}
	if got := chunks1[0].Choices[0].Delta.ToolCalls[0].Function.Arguments; got != partial1 {
		t.Fatalf("partial1 arguments = %q, want %q", got, partial1)
	}

	partial2 := " --stat\"}"
	chunks2, err := mapper.Map(dto.ClaudeStreamEvent{
		Type:  "content_block_delta",
		Index: 2,
		Delta: &dto.ClaudeDelta{Type: "input_json_delta", PartialJSON: &partial2},
	})
	if err != nil {
		t.Fatalf("Map partial2 error: %v", err)
	}
	if len(chunks2) != 1 {
		t.Fatalf("partial2 len = %d, want 1", len(chunks2))
	}
	if got := chunks2[0].Choices[0].Delta.ToolCalls[0].GetIndex(); got != 2 {
		t.Fatalf("partial2 index = %d, want 2", got)
	}
	if got := chunks2[0].Choices[0].Delta.ToolCalls[0].ID; got != "call-1" {
		t.Fatalf("partial2 id = %q, want call-1", got)
	}
	if got := chunks2[0].Choices[0].Delta.ToolCalls[0].Function.Arguments; got != partial2 {
		t.Fatalf("partial2 arguments = %q, want %q", got, partial2)
	}
}

// TestChatToClaudeStreamMapper_ReasoningContent tests ReasoningContent conversion to thinking_delta
func TestChatToClaudeStreamMapper_ReasoningContent(t *testing.T) {
	mapper := newChatToClaudeStreamMapper("")

	// First chunk with role
	chunk1 := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Model:   "gpt-4",
		Created: 1234,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Role: "assistant"}}},
	}

	events, err := mapper.Map(chunk1)
	if err != nil {
		t.Fatalf("Map returned error: %v", err)
	}
	if len(events) == 0 || events[0].Type != "message_start" {
		t.Fatalf("expected message_start event")
	}

	// Chunk with reasoning content (thinking)
	reasoning := "Let me analyze this problem..."
	chunk2 := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Model:   "gpt-4",
		Created: 1234,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{ReasoningContent: reasoning}}},
	}

	events, err = mapper.Map(chunk2)
	if err != nil {
		t.Fatalf("Map returned error: %v", err)
	}

	// Should produce a thinking block start and a thinking_delta
	foundThinkingBlockStart := false
	foundThinkingDelta := false
	for _, ev := range events {
		if ev.Type == "content_block_start" && ev.ContentBlock != nil && ev.ContentBlock.Type == "thinking" {
			foundThinkingBlockStart = true
		}
		if ev.Type == "content_block_delta" && ev.Delta != nil && ev.Delta.Type == "thinking_delta" {
			foundThinkingDelta = true
			if ev.Delta.Thinking != reasoning {
				t.Fatalf("expected Thinking %q, got %q", reasoning, ev.Delta.Thinking)
			}
		}
	}

	if !foundThinkingBlockStart {
		t.Fatal("expected thinking content_block_start event")
	}
	if !foundThinkingDelta {
		t.Fatal("expected thinking_delta event")
	}
}

// TestChatToClaudeStreamMapper_TextAndReasoning tests interleaved text and reasoning content
func TestChatToClaudeStreamMapper_TextAndReasoning(t *testing.T) {
	mapper := newChatToClaudeStreamMapper("")

	// Initialize with role
	chunk0 := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Model:   "gpt-4",
		Created: 1234,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Role: "assistant"}}},
	}
	mapper.Map(chunk0)

	// Reasoning chunk
	chunk1 := dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{ReasoningContent: "thinking..."}}},
	}
	events, _ := mapper.Map(chunk1)

	// Verify thinking block index
	var thinkingIndex int
	for _, ev := range events {
		if ev.Type == "content_block_start" && ev.ContentBlock != nil && ev.ContentBlock.Type == "thinking" {
			thinkingIndex = ev.Index
		}
	}

	// Text chunk should use a different block
	chunk2 := dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Content: "Hello"}}},
	}
	events, _ = mapper.Map(chunk2)

	var textIndex int
	for _, ev := range events {
		if ev.Type == "content_block_start" && ev.ContentBlock != nil && ev.ContentBlock.Type == "text" {
			textIndex = ev.Index
		}
	}

	if thinkingIndex == textIndex {
		t.Fatal("thinking and text should use different block indices")
	}
}

// TestClaudeToChatStreamMapper_ImageBlock tests image content block handling in streaming
func TestClaudeToChatStreamMapper_ImageBlock(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("msg-1", "claude-3", 1234)

	// Initialize with message_start
	mapper.Map(dto.ClaudeStreamEvent{
		Type:    "message_start",
		Message: &dto.ClaudeMessageStart{ID: "msg-1", Model: "claude-3"},
	})

	// Image content block start - should be handled gracefully (skipped in output)
	event := dto.ClaudeStreamEvent{
		Type:  "content_block_start",
		Index: 0,
		ContentBlock: &dto.ContentBlock{
			Type: "image",
			Source: &dto.MessageSource{
				Type:      "base64",
				MediaType: "image/jpeg",
				Data:      "base64encodeddata",
			},
		},
	}

	chunks, err := mapper.Map(event)
	if err != nil {
		t.Fatalf("Map returned error: %v", err)
	}
	// Image blocks should be skipped (return nil, nil)
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for image block (not supported in OpenAI Chat), got %d", len(chunks))
	}
}

// TestClaudeToChatStreamMapper_DocumentBlock tests document content block handling in streaming
func TestClaudeToChatStreamMapper_DocumentBlock(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("msg-1", "claude-3", 1234)

	// Initialize with message_start
	mapper.Map(dto.ClaudeStreamEvent{
		Type:    "message_start",
		Message: &dto.ClaudeMessageStart{ID: "msg-1", Model: "claude-3"},
	})

	// Document content block start - should be handled gracefully (skipped in output)
	event := dto.ClaudeStreamEvent{
		Type:  "content_block_start",
		Index: 0,
		ContentBlock: &dto.ContentBlock{
			Type: "document",
			Source: &dto.MessageSource{
				Type:      "base64",
				MediaType: "application/pdf",
				Data:      "base64encodedpdf",
			},
		},
	}

	chunks, err := mapper.Map(event)
	if err != nil {
		t.Fatalf("Map returned error: %v", err)
	}
	// Document blocks should be skipped (return nil, nil)
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for document block (not supported in OpenAI Chat), got %d", len(chunks))
	}
}

// TestChatToClaudeStreamMapper_ImageDataURI tests Data URI image in content conversion
func TestChatToClaudeStreamMapper_ImageDataURI(t *testing.T) {
	mapper := newChatToClaudeStreamMapper("")

	// Initialize with role
	chunk0 := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Model:   "gpt-4",
		Created: 1234,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Role: "assistant"}}},
	}
	events, _ := mapper.Map(chunk0)

	// Image data URI in content
	imageDataURI := "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQ="
	chunk1 := dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Content: imageDataURI}}},
	}

	events, err := mapper.Map(chunk1)
	if err != nil {
		t.Fatalf("Map returned error: %v", err)
	}

	// Should convert to image content block instead of text block
	// And should emit both content_block_start and content_block_stop
	var foundImageBlock bool
	var foundImageBlockStop bool
	var imageBlockIndex int
	for _, ev := range events {
		if ev.Type == "content_block_start" && ev.ContentBlock != nil && ev.ContentBlock.Type == "image" {
			foundImageBlock = true
			imageBlockIndex = ev.Index
			if ev.ContentBlock.Source == nil {
				t.Fatal("expected Source in image content block")
			}
			if ev.ContentBlock.Source.Type != "base64" {
				t.Fatalf("expected Source.Type 'base64', got %q", ev.ContentBlock.Source.Type)
			}
			if ev.ContentBlock.Source.MediaType != "image/jpeg" {
				t.Fatalf("expected MediaType 'image/jpeg', got %q", ev.ContentBlock.Source.MediaType)
			}
			if ev.ContentBlock.Source.Data != "/9j/4AAQSkZJRgABAQ=" {
				t.Fatalf("expected Data '/9j/4AAQSkZJRgABAQ=', got %q", ev.ContentBlock.Source.Data)
			}
		}
		if ev.Type == "content_block_stop" {
			foundImageBlockStop = true
			if ev.Index != imageBlockIndex {
				t.Fatalf("expected content_block_stop index %d to match content_block_start index %d", ev.Index, imageBlockIndex)
			}
		}
	}

	if !foundImageBlock {
		t.Fatal("expected image content_block_start event for Data URI content")
	}
	if !foundImageBlockStop {
		t.Fatal("expected content_block_stop event after image content_block_start")
	}
}

// TestChatToClaudeStreamMapper_DocumentDataURI tests Data URI document in content conversion
func TestChatToClaudeStreamMapper_DocumentDataURI(t *testing.T) {
	mapper := newChatToClaudeStreamMapper("")

	// Initialize with role
	chunk0 := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Model:   "gpt-4",
		Created: 1234,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Role: "assistant"}}},
	}
	mapper.Map(chunk0)

	// Document data URI in content
	docDataURI := "data:application/pdf;base64,JVBERi0xLjQKJ"
	chunk1 := dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Content: docDataURI}}},
	}

	events, err := mapper.Map(chunk1)
	if err != nil {
		t.Fatalf("Map returned error: %v", err)
	}

	// Should convert to document content block instead of text block
	// And should emit both content_block_start and content_block_stop
	var foundDocumentBlock bool
	var foundDocumentBlockStop bool
	var docBlockIndex int
	for _, ev := range events {
		if ev.Type == "content_block_start" && ev.ContentBlock != nil && ev.ContentBlock.Type == "document" {
			foundDocumentBlock = true
			docBlockIndex = ev.Index
			if ev.ContentBlock.Source == nil {
				t.Fatal("expected Source in document content block")
			}
			if ev.ContentBlock.Source.Type != "base64" {
				t.Fatalf("expected Source.Type 'base64', got %q", ev.ContentBlock.Source.Type)
			}
			if ev.ContentBlock.Source.MediaType != "application/pdf" {
				t.Fatalf("expected MediaType 'application/pdf', got %q", ev.ContentBlock.Source.MediaType)
			}
			if ev.ContentBlock.Source.Data != "JVBERi0xLjQKJ" {
				t.Fatalf("expected Data 'JVBERi0xLjQKJ', got %q", ev.ContentBlock.Source.Data)
			}
		}
		if ev.Type == "content_block_stop" {
			foundDocumentBlockStop = true
			if ev.Index != docBlockIndex {
				t.Fatalf("expected content_block_stop index %d to match content_block_start index %d", ev.Index, docBlockIndex)
			}
		}
	}

	if !foundDocumentBlock {
		t.Fatal("expected document content_block_start event for Data URI content")
	}
	if !foundDocumentBlockStop {
		t.Fatal("expected content_block_stop event after document content_block_start")
	}
}

// TestChatToClaudeStreamMapper_MalformedDataURI tests malformed Data URI handling
func TestChatToClaudeStreamMapper_MalformedDataURI(t *testing.T) {
	mapper := newChatToClaudeStreamMapper("")

	// Initialize with role
	chunk0 := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Model:   "gpt-4",
		Created: 1234,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Role: "assistant"}}},
	}
	mapper.Map(chunk0)

	// Malformed data URI with empty data after comma
	malformedDataURI := "data:image/png;base64,"
	chunk1 := dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Content: malformedDataURI}}},
	}

	events, err := mapper.Map(chunk1)
	if err != nil {
		t.Fatalf("Map returned error: %v", err)
	}

	// Should convert to image content block with empty data
	var foundImageBlock bool
	var foundImageBlockStop bool
	var imageBlockIndex int
	for _, ev := range events {
		if ev.Type == "content_block_start" && ev.ContentBlock != nil && ev.ContentBlock.Type == "image" {
			foundImageBlock = true
			imageBlockIndex = ev.Index
			if ev.ContentBlock.Source == nil {
				t.Fatal("expected Source in image content block")
			}
			if ev.ContentBlock.Source.Type != "base64" {
				t.Fatalf("expected Source.Type 'base64', got %q", ev.ContentBlock.Source.Type)
			}
			if ev.ContentBlock.Source.MediaType != "image/png" {
				t.Fatalf("expected MediaType 'image/png', got %q", ev.ContentBlock.Source.MediaType)
			}
			// Data should be empty
			if ev.ContentBlock.Source.Data != "" {
				t.Fatalf("expected empty Data for malformed URI, got %q", ev.ContentBlock.Source.Data)
			}
		}
		if ev.Type == "content_block_stop" {
			foundImageBlockStop = true
			if ev.Index != imageBlockIndex {
				t.Fatalf("expected content_block_stop index %d to match content_block_start index %d", ev.Index, imageBlockIndex)
			}
		}
	}

	if !foundImageBlock {
		t.Fatal("expected image content_block_start event for malformed Data URI")
	}
	if !foundImageBlockStop {
		t.Fatal("expected content_block_stop event after image content_block_start")
	}
}

// TestChatToClaudeStreamMapper_RegularTextNotAffected tests that regular text still works
func TestChatToClaudeStreamMapper_RegularTextNotAffected(t *testing.T) {
	mapper := newChatToClaudeStreamMapper("")

	// Initialize with role
	chunk0 := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Model:   "gpt-4",
		Created: 1234,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Role: "assistant"}}},
	}
	mapper.Map(chunk0)

	// Regular text content (not a Data URI)
	chunk1 := dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Content: "Hello world"}}},
	}

	events, err := mapper.Map(chunk1)
	if err != nil {
		t.Fatalf("Map returned error: %v", err)
	}

	// Should convert to text content block
	var foundTextBlock bool
	var foundTextDelta bool
	for _, ev := range events {
		if ev.Type == "content_block_start" && ev.ContentBlock != nil && ev.ContentBlock.Type == "text" {
			foundTextBlock = true
		}
		if ev.Type == "content_block_delta" && ev.Delta != nil && ev.Delta.Type == "text_delta" {
			foundTextDelta = true
			if ev.Delta.Text != "Hello world" {
				t.Fatalf("expected Text 'Hello world', got %q", ev.Delta.Text)
			}
		}
	}

	if !foundTextBlock {
		t.Fatal("expected text content_block_start event")
	}
	if !foundTextDelta {
		t.Fatal("expected text_delta event")
	}
}

// TestChatToClaudeStreamMapper_ToolCallWithPartialJSON tests tool call handling with partial JSON
func TestChatToClaudeStreamMapper_ToolCallWithPartialJSON(t *testing.T) {
	mapper := newChatToClaudeStreamMapper("")
	intZero := 0

	// Initialize
	chunk0 := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Model:   "gpt-4",
		Created: 1234,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Role: "assistant"}}},
	}
	mapper.Map(chunk0)

	// Tool call start
	chunk1 := dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{
			ToolCalls: []dto.ToolCall{{Index: &intZero, ID: "call-1", Type: "function", Function: dto.ToolCallFunc{Name: "get_weather"}}},
		}}},
	}
	events, err := mapper.Map(chunk1)
	if err != nil {
		t.Fatalf("Map returned error: %v", err)
	}

	// Should have content_block_start for tool_use
	var foundToolStart bool
	for _, ev := range events {
		if ev.Type == "content_block_start" && ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
			foundToolStart = true
			if ev.ContentBlock.Name != "get_weather" {
				t.Fatalf("expected tool name 'get_weather', got %q", ev.ContentBlock.Name)
			}
		}
	}
	if !foundToolStart {
		t.Fatal("expected tool_use content_block_start")
	}

	// Tool call argument delta
	chunk2 := dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{
			ToolCalls: []dto.ToolCall{{Index: &intZero, Function: dto.ToolCallFunc{Arguments: "{\"loc"}}},
		}}},
	}
	events, err = mapper.Map(chunk2)
	if err != nil {
		t.Fatalf("Map returned error: %v", err)
	}

	// Should have input_json_delta
	var foundInputJSONDelta bool
	for _, ev := range events {
		if ev.Type == "content_block_delta" && ev.Delta != nil && ev.Delta.Type == "input_json_delta" {
			foundInputJSONDelta = true
			if ev.Delta.PartialJSON == nil || *ev.Delta.PartialJSON != "{\"loc" {
				t.Fatalf("expected PartialJSON %q, got %v", "{\"loc", ev.Delta.PartialJSON)
			}
		}
	}
	if !foundInputJSONDelta {
		t.Fatal("expected input_json_delta event")
	}
}

// ============================================================================
// Task 2: Stream mapper RequestedModel tests
// ============================================================================

// TestStreamMapChatToClaude_UsesRequestedModel verifies that chatToClaudeStreamMapper
// uses the constructor-seeded requestedModel rather than the upstream model from chunks.
func TestStreamMapChatToClaude_UsesRequestedModel(t *testing.T) {
	mapper := newChatToClaudeStreamMapper("")
	mapper.requestedModel = "my-alias"

	chunk := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Model:   "upstream-model",
		Created: 1234,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Role: "assistant"}}},
	}

	events, err := mapper.Map(chunk)
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}

	for _, ev := range events {
		if ev.Type == "message_start" && ev.Message != nil {
			if ev.Message.Model != "my-alias" {
				t.Errorf("message_start.model = %q, want %q", ev.Message.Model, "my-alias")
			}
		}
	}
}

// TestStreamMapClaudeToChat_UsesRequestedModel verifies that claudeToChatStreamMapper
// uses the constructor-seeded requestedModel rather than the upstream model from events.
func TestStreamMapClaudeToChat_UsesRequestedModel(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("chatcmpl-1", "my-alias", 1234)

	event := dto.ClaudeStreamEvent{
		Type: "message_start",
		Message: &dto.ClaudeMessageStart{
			ID:    "msg-1",
			Model: "upstream-model",
		},
	}

	chunks, err := mapper.Map(event)
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk from message_start")
	}
	if chunks[0].Model != "my-alias" {
		t.Errorf("chunk.Model = %q, want %q", chunks[0].Model, "my-alias")
	}
}

// TestStreamMapChatToResponses_UsesRequestedModel verifies that chatToResponsesStreamMapper
// uses the constructor-seeded requestedModel rather than the upstream model from chunks.
func TestStreamMapChatToResponses_UsesRequestedModel(t *testing.T) {
	mapper := newChatToResponsesStreamMapper("resp-1", "my-alias")

	chunk := dto.ChatCompletionChunk{
		ID:      "chatcmpl-1",
		Model:   "upstream-model",
		Created: 1234,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Role: "assistant", Content: ""}}},
	}

	events, err := mapper.Map(chunk)
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}

	for _, ev := range events {
		if ev.Type == "response.created" && len(ev.Response) > 0 {
			var r struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(ev.Response, &r); err != nil {
				t.Fatalf("unmarshal response.created: %v", err)
			}
			if r.Model != "my-alias" {
				t.Errorf("response.created.model = %q, want %q", r.Model, "my-alias")
			}
		}
	}
}

// TestStreamMapResponsesToChat_UsesRequestedModel verifies that responsesToChatStreamMapper
// uses the constructor-seeded requestedModel rather than the upstream model from events.
func TestStreamMapResponsesToChat_UsesRequestedModel(t *testing.T) {
	mapper := newResponsesToChatStreamMapper("resp-1", "my-alias", 1234)

	event := dto.ResponsesStreamEvent{
		Type:     "response.created",
		Response: json.RawMessage(`{"id":"resp-1","model":"upstream-model","created_at":1234}`),
	}

	chunks, err := mapper.Map(event)
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	for _, ch := range chunks {
		if ch.Model != "my-alias" {
			t.Errorf("chunk.Model = %q, want %q", ch.Model, "my-alias")
		}
	}
}

func TestStreamMapChatToResponses_OutputTextDoneIncludesText(t *testing.T) {
	mapper := newChatToResponsesStreamMapper("resp-1", "my-alias")

	contentEvents, err := mapper.Map(dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{
			Index: 0,
			Delta: &dto.Delta{Content: "Hello"},
		}},
	})
	if err != nil {
		t.Fatalf("Map content error: %v", err)
	}
	if len(contentEvents) == 0 {
		t.Fatal("expected content events")
	}

	finishReason := "stop"
	finishEvents, err := mapper.Map(dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{
			Index:        0,
			Delta:        &dto.Delta{},
			FinishReason: &finishReason,
		}},
	})
	if err != nil {
		t.Fatalf("Map finish error: %v", err)
	}

	for _, event := range finishEvents {
		if event.Type != "response.output_text.done" {
			continue
		}
		if event.Text == nil || *event.Text != "Hello" {
			t.Fatalf("output_text.done text = %#v, want Hello", event.Text)
		}
		return
	}
	t.Fatal("missing response.output_text.done event")
}

// ============================================================================
// 新增 Response -> Chat 流式对齐测试（test-first）
// ============================================================================

// TestResponsesToChatStreamMapper_EmitsReasoningSummaryDelta 验证
// response.reasoning_summary_text.delta 输出到 ReasoningContent
// response.reasoning_summary_text.done 只更新状态不输出 chunk
func TestResponsesToChatStreamMapper_EmitsReasoningSummaryDelta(t *testing.T) {
	mapper := newResponsesToChatStreamMapper("resp-1", "gpt-4o", 1234)

	// reasoning_summary_text.delta
	event := dto.ResponsesStreamEvent{
		Type:  "response.reasoning_summary_text.delta",
		Delta: json.RawMessage(`"summary thinking"`),
	}

	chunks, err := mapper.Map(event)
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}

	// 应该至少有一个 chunk，包含 ReasoningContent
	found := false
	for _, ch := range chunks {
		if ch.Choices[0].Delta.ReasoningContent == "summary thinking" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected ReasoningContent 'summary thinking' in chunks, got: %+v", chunks)
	}

	// reasoning_summary_text.done 不应该输出 chunk
	doneEvent := dto.ResponsesStreamEvent{
		Type: "response.reasoning_summary_text.done",
	}
	chunks, err = mapper.Map(doneEvent)
	if err != nil {
		t.Fatalf("Map done error: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("done event should produce no chunks, got %d", len(chunks))
	}
}

// TestResponsesToChatStreamMapper_ReusesToolIndexAcrossPendingArguments 验证
// arguments delta 先到、output_item.added 后到的乱序场景：delta 暂存，added 到来时冲刷
func TestResponsesToChatStreamMapper_ReusesToolIndexAcrossPendingArguments(t *testing.T) {
	mapper := newResponsesToChatStreamMapper("resp-2", "gpt-4o", 1234)

	// 先发 arguments delta（乱序），此时 tool 还不存在
	argsEvent1 := dto.ResponsesStreamEvent{
		Type:   "response.function_call_arguments.delta",
		ItemID: "fc-1",
		Delta:  json.RawMessage(`"{\"city\""`),
	}
	chunks, err := mapper.Map(argsEvent1)
	if err != nil {
		t.Fatalf("Map args delta error: %v", err)
	}
	// 此时应该没有输出 chunk（因为 tool 不存在，delta 暂存）
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for pending args delta, got %d", len(chunks))
	}

	// 再发第二个 arguments delta
	argsEvent2 := dto.ResponsesStreamEvent{
		Type:   "response.function_call_arguments.delta",
		ItemID: "fc-1",
		Delta:  json.RawMessage(`": \"Beijing\"}"`),
	}
	chunks, err = mapper.Map(argsEvent2)
	if err != nil {
		t.Fatalf("Map args delta2 error: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for pending args delta2, got %d", len(chunks))
	}

	// 现在发 output_item.added，应该冲刷所有暂存的 args
	itemEvent := dto.ResponsesStreamEvent{
		Type: "response.output_item.added",
		Item: json.RawMessage(`{"type":"function_call","id":"fc-1","call_id":"call-1","name":"get_weather"}`),
	}
	chunks, err = mapper.Map(itemEvent)
	if err != nil {
		t.Fatalf("Map item added error: %v", err)
	}

	// 验证有 tool call chunk 输出，且包含所有暂存的 arguments
	foundTool := false
	for _, ch := range chunks {
		for _, tc := range ch.Choices[0].Delta.ToolCalls {
			if tc.Function.Name == "get_weather" {
				foundTool = true
				if tc.Function.Arguments != `{"city": "Beijing"}` {
					t.Errorf("tool arguments = %q, want {\"city\": \"Beijing\"}", tc.Function.Arguments)
				}
				if tc.ID != "call-1" {
					t.Errorf("tool call id = %q, want call-1", tc.ID)
				}
			}
		}
	}
	if !foundTool {
		t.Fatalf("expected tool call for get_weather, got chunks: %+v", chunks)
	}
}

// TestResponsesToChatStreamMapper_FallsBackCallIDToItemID 验证
// output_item.added 中 call_id 为空时回退到 item.id
func TestResponsesToChatStreamMapper_FallsBackCallIDToItemID(t *testing.T) {
	mapper := newResponsesToChatStreamMapper("resp-3", "gpt-4o", 1234)

	// output_item.added with empty call_id
	event := dto.ResponsesStreamEvent{
		Type: "response.output_item.added",
		Item: json.RawMessage(`{"type":"function_call","id":"fc-1","call_id":"","name":"get_weather"}`),
	}

	chunks, err := mapper.Map(event)
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}

	// 应该有 tool call chunk，call_id 回退到 item.id "fc-1"
	foundTool := false
	for _, ch := range chunks {
		for _, tc := range ch.Choices[0].Delta.ToolCalls {
			if tc.Function.Name == "get_weather" {
				foundTool = true
				if tc.ID != "fc-1" {
					t.Errorf("tool call id = %q, want fc-1 (fallback to item id)", tc.ID)
				}
			}
		}
	}
	if !foundTool {
		t.Fatalf("expected tool call for get_weather, got chunks: %+v", chunks)
	}

	// 再发 response.completed，由于有 tool call，finish_reason 应为 "tool_calls"
	completedEvent := dto.ResponsesStreamEvent{
		Type: "response.completed",
	}
	chunks, err = mapper.Map(completedEvent)
	if err != nil {
		t.Fatalf("Map completed error: %v", err)
	}

	foundFinish := false
	for _, ch := range chunks {
		if ch.Choices[0].FinishReason != nil {
			foundFinish = true
			if *ch.Choices[0].FinishReason != "tool_calls" {
				t.Errorf("finish_reason = %q, want tool_calls", *ch.Choices[0].FinishReason)
			}
		}
	}
	if !foundFinish {
		t.Fatalf("expected finish_reason chunk, got: %+v", chunks)
	}
}

func TestResponsesToChatStreamMapper_GeneratesCallIDWhenMissing(t *testing.T) {
	mapper := newResponsesToChatStreamMapper("resp-4", "gpt-4o", 1234)
	outputIndex := 3
	event := dto.ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: &outputIndex,
		Item:        json.RawMessage(`{"type":"function_call","name":"get_weather"}`),
	}

	chunks, err := mapper.Map(event)
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	if len(chunks) == 0 || len(chunks[len(chunks)-1].Choices[0].Delta.ToolCalls) != 1 {
		t.Fatalf("expected tool call chunk, got %+v", chunks)
	}
	call := chunks[len(chunks)-1].Choices[0].Delta.ToolCalls[0]
	if call.ID != "generated_tool_call_3" {
		t.Fatalf("tool call id = %q, want generated_tool_call_3", call.ID)
	}
}

func TestResponsesToChatStreamMapper_AssociatesAnonymousArgumentsWithSingleTool(t *testing.T) {
	mapper := newResponsesToChatStreamMapper("resp-4", "gpt-4o", 1234)
	chunks, err := mapper.Map(dto.ResponsesStreamEvent{
		Type: "response.output_item.added",
		Item: json.RawMessage(`{"type":"function_call","name":"get_weather"}`),
	})
	if err != nil {
		t.Fatalf("Map added error: %v", err)
	}
	call := chunks[len(chunks)-1].Choices[0].Delta.ToolCalls[0]

	chunks, err = mapper.Map(dto.ResponsesStreamEvent{
		Type:  "response.function_call_arguments.delta",
		Delta: json.RawMessage(`"{\"city\":\"Beijing\"}"`),
	})
	if err != nil {
		t.Fatalf("Map delta error: %v", err)
	}
	if len(chunks) != 1 || len(chunks[0].Choices[0].Delta.ToolCalls) != 1 {
		t.Fatalf("expected one arguments chunk, got %+v", chunks)
	}
	got := chunks[0].Choices[0].Delta.ToolCalls[0]
	if got.GetIndex() != call.GetIndex() || got.Function.Arguments != `{"city":"Beijing"}` {
		t.Fatalf("arguments chunk = %+v, want index %d with arguments", got, call.GetIndex())
	}
}

func TestResponsesToChatStreamMapper_DoesNotGuessBetweenAnonymousTools(t *testing.T) {
	mapper := newResponsesToChatStreamMapper("resp-4", "gpt-4o", 1234)
	for _, name := range []string{"first", "second"} {
		if _, err := mapper.Map(dto.ResponsesStreamEvent{
			Type: "response.output_item.added",
			Item: json.RawMessage(fmt.Sprintf(`{"type":"function_call","name":%q}`, name)),
		}); err != nil {
			t.Fatalf("Map added error: %v", err)
		}
	}

	chunks, err := mapper.Map(dto.ResponsesStreamEvent{
		Type:  "response.function_call_arguments.delta",
		Delta: json.RawMessage(`"{}"`),
	})
	if err != nil {
		t.Fatalf("Map delta error: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("ambiguous arguments must not be assigned, got %+v", chunks)
	}
}

func TestResponsesToChatStreamMapper_ReasoningDeltaEmitsRoleFirst(t *testing.T) {
	mapper := newResponsesToChatStreamMapper("resp-role", "gpt-4o", 1234)

	chunks, err := mapper.Map(dto.ResponsesStreamEvent{
		Type:  "response.reasoning_summary_text.delta",
		Delta: json.RawMessage(`"thinking"`),
	})
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	if chunks[0].Choices[0].Delta.Role != "assistant" {
		t.Fatalf("first delta role = %q, want assistant", chunks[0].Choices[0].Delta.Role)
	}
	if chunks[1].Choices[0].Delta.ReasoningContent != "thinking" {
		t.Fatalf("reasoning_content = %q, want thinking", chunks[1].Choices[0].Delta.ReasoningContent)
	}
}

func TestResponsesToChatStreamMapper_IncompleteMapsToLength(t *testing.T) {
	mapper := newResponsesToChatStreamMapper("resp-incomplete", "gpt-4o", 1234)

	chunks, err := mapper.Map(dto.ResponsesStreamEvent{
		Type:     "response.incomplete",
		Response: json.RawMessage(`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`),
	})
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	if chunks[0].Choices[0].FinishReason == nil || *chunks[0].Choices[0].FinishReason != "length" {
		t.Fatalf("finish_reason = %v, want length", chunks[0].Choices[0].FinishReason)
	}
}

func TestResponsesToChatStreamMapper_CustomToolCallInputDelta(t *testing.T) {
	mapper := newResponsesToChatStreamMapper("resp-ctc", "gpt-4o", 1234)

	// 先发 arguments delta（乱序），此时 tool 还不存在
	argsEvent := dto.ResponsesStreamEvent{
		Type:   "response.custom_tool_call_input.delta",
		ItemID: "ctc-1",
		Delta:  json.RawMessage(`"{\"query\""`),
	}
	chunks, err := mapper.Map(argsEvent)
	if err != nil {
		t.Fatalf("Map custom_tool_call_input.delta error: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for pending custom_tool_call_input.delta, got %d", len(chunks))
	}

	// 发第二个 delta
	argsEvent2 := dto.ResponsesStreamEvent{
		Type:   "response.custom_tool_call_input.delta",
		ItemID: "ctc-1",
		Delta:  json.RawMessage(`": \"hello\"}"`),
	}
	chunks, err = mapper.Map(argsEvent2)
	if err != nil {
		t.Fatalf("Map custom_tool_call_input.delta2 error: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for pending custom_tool_call_input.delta2, got %d", len(chunks))
	}

	// 发 output_item.added (custom_tool_call)，冲刷暂存的 args
	itemEvent := dto.ResponsesStreamEvent{
		Type: "response.output_item.added",
		Item: json.RawMessage(`{"type":"custom_tool_call","id":"ctc-1","call_id":"call-ctc-1","name":"search"}`),
	}
	chunks, err = mapper.Map(itemEvent)
	if err != nil {
		t.Fatalf("Map output_item.added (custom_tool_call) error: %v", err)
	}

	foundTool := false
	for _, ch := range chunks {
		for _, tc := range ch.Choices[0].Delta.ToolCalls {
			if tc.Function.Name == "search" {
				foundTool = true
				if tc.Function.Arguments != `{"query": "hello"}` {
					t.Errorf("tool arguments = %q, want {\"query\": \"hello\"}", tc.Function.Arguments)
				}
				if tc.ID != "call-ctc-1" {
					t.Errorf("tool call id = %q, want call-ctc-1", tc.ID)
				}
			}
		}
	}
	if !foundTool {
		t.Fatalf("expected tool call for search, got chunks: %+v", chunks)
	}
}

// TestStreamUsage 测试 Anthropic 流式 usage 映射到 Chat
func TestStreamUsage(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("", "claude-3", 1234)

	// message_start 带 input_tokens
	startChunks, err := mapper.Map(dto.ClaudeStreamEvent{
		Type: "message_start",
		Message: &dto.ClaudeMessageStart{
			ID:    "msg-1",
			Type:  "message",
			Role:  "assistant",
			Model: "claude-3",
			Usage: dto.ClaudeUsage{InputTokens: 100},
		},
	})
	if err != nil {
		t.Fatalf("Map message_start error: %v", err)
	}
	if len(startChunks) != 1 {
		t.Fatalf("len(startChunks) = %d, want 1", len(startChunks))
	}

	// message_delta 带 stop_reason 和 usage
	deltaChunks, err := mapper.Map(dto.ClaudeStreamEvent{
		Type:  "message_delta",
		Delta: &dto.ClaudeDelta{StopReason: "end_turn"},
		Usage: &dto.ClaudeUsage{OutputTokens: 50},
	})
	if err != nil {
		t.Fatalf("Map message_delta error: %v", err)
	}

	// 应该有 finish_reason 和 usage
	foundFinish := false
	foundUsage := false
	for _, ch := range deltaChunks {
		if ch.Choices[0].FinishReason != nil && *ch.Choices[0].FinishReason == "stop" {
			foundFinish = true
		}
		if ch.Usage != nil && ch.Usage.PromptTokens == 100 && ch.Usage.CompletionTokens == 50 {
			foundUsage = true
		}
	}

	if !foundFinish {
		t.Fatalf("expected finish_reason=stop in chunks")
	}
	if !foundUsage {
		t.Fatalf("expected usage with prompt_tokens=100, completion_tokens=50 in chunks")
	}
}

// TestClaudeToChat_FinishWithUsageOnChunk 验证 finish 同包带 Usage（mapper 中间表示）
func TestClaudeToChat_FinishWithUsageOnChunk(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("", "claude-3", 1234)

	deltaChunks, err := mapper.Map(dto.ClaudeStreamEvent{
		Type:  "message_delta",
		Delta: &dto.ClaudeDelta{StopReason: "end_turn"},
		Usage: &dto.ClaudeUsage{InputTokens: 100, OutputTokens: 50},
	})
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}

	hasFinishWithUsage := false
	for _, ch := range deltaChunks {
		if len(ch.Choices) > 0 && ch.Choices[0].FinishReason != nil && ch.Usage != nil {
			hasFinishWithUsage = true
		}
	}
	if !hasFinishWithUsage {
		t.Fatalf("expected finish_reason chunk with usage attached")
	}
}

// TestClaudeToChat_ZeroCompletionTokensStillAttachesUsage 合法 0 completion 也应挂 usage
func TestClaudeToChat_ZeroCompletionTokensStillAttachesUsage(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("", "claude-3", 1234)
	_, _ = mapper.Map(dto.ClaudeStreamEvent{
		Type: "message_start",
		Message: &dto.ClaudeMessageStart{
			ID: "msg-1", Model: "claude-3",
			Usage: dto.ClaudeUsage{InputTokens: 12},
		},
	})
	chunks, err := mapper.Map(dto.ClaudeStreamEvent{
		Type:  "message_delta",
		Delta: &dto.ClaudeDelta{StopReason: "end_turn"},
		Usage: &dto.ClaudeUsage{OutputTokens: 0},
	})
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	if len(chunks) != 1 || chunks[0].Usage == nil {
		t.Fatalf("expected usage on finish chunk, got %+v", chunks)
	}
	if chunks[0].Usage.PromptTokens != 12 || chunks[0].Usage.CompletionTokens != 0 {
		t.Fatalf("usage = %+v, want prompt=12 completion=0", chunks[0].Usage)
	}
}

// TestClaudeToChat_StopReasonWithoutDeltaUsage_DoesNotEmitStartOnlyUsage
// message_start 不能把 usage 视为已齐；仅 stop_reason 时不应回写 completion_tokens=0。
func TestClaudeToChat_StopReasonWithoutDeltaUsage_DoesNotEmitStartOnlyUsage(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("", "claude-3", 1234)
	_, _ = mapper.Map(dto.ClaudeStreamEvent{
		Type: "message_start",
		Message: &dto.ClaudeMessageStart{
			ID: "msg-1", Model: "claude-3",
			Usage: dto.ClaudeUsage{InputTokens: 99},
		},
	})
	chunks, err := mapper.Map(dto.ClaudeStreamEvent{
		Type:  "message_delta",
		Delta: &dto.ClaudeDelta{StopReason: "end_turn"},
	})
	if err != nil {
		t.Fatalf("Map error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks)=%d, want 1", len(chunks))
	}
	if chunks[0].Usage != nil {
		t.Fatalf("start-only usage must not attach on stop_reason, got %+v", chunks[0].Usage)
	}
}

// TestClaudeToChat_MessageStopAttachesReadyUsage 覆盖 message_delta.usage 后靠 message_stop 收尾
func TestClaudeToChat_MessageStopAttachesReadyUsage(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("", "claude-3", 1234)
	_, _ = mapper.Map(dto.ClaudeStreamEvent{
		Type: "message_start",
		Message: &dto.ClaudeMessageStart{
			ID: "msg-1", Model: "claude-3",
			Usage: dto.ClaudeUsage{InputTokens: 40, CacheReadInputTokens: 15},
		},
	})
	// 仅 usage、无 stop_reason
	usageOnly, err := mapper.Map(dto.ClaudeStreamEvent{
		Type:  "message_delta",
		Usage: &dto.ClaudeUsage{OutputTokens: 7},
	})
	if err != nil {
		t.Fatalf("usage-only map: %v", err)
	}
	if len(usageOnly) != 1 || usageOnly[0].Usage == nil {
		t.Fatalf("expected usage-only chunk, got %+v", usageOnly)
	}
	if usageOnly[0].Usage.PromptTokens != 40 || usageOnly[0].Usage.CompletionTokens != 7 {
		t.Fatalf("usage-only = %+v, want prompt=40 completion=7", usageOnly[0].Usage)
	}
	if usageOnly[0].Usage.PromptTokensDetails == nil || usageOnly[0].Usage.PromptTokensDetails.CachedTokens != 15 {
		t.Fatalf("cache_read not mapped: %+v", usageOnly[0].Usage.PromptTokensDetails)
	}

	// message_stop 应挂已齐 usage
	stopChunks, err := mapper.Map(dto.ClaudeStreamEvent{Type: "message_stop"})
	if err != nil {
		t.Fatalf("message_stop: %v", err)
	}
	if len(stopChunks) != 1 {
		t.Fatalf("len(stopChunks)=%d, want 1", len(stopChunks))
	}
	if stopChunks[0].Usage == nil {
		t.Fatalf("message_stop should attach ready usage")
	}
	if stopChunks[0].Usage.PromptTokens != 40 || stopChunks[0].Usage.CompletionTokens != 7 {
		t.Fatalf("stop usage = %+v", stopChunks[0].Usage)
	}
	if stopChunks[0].Choices[0].FinishReason == nil || *stopChunks[0].Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason = %v, want stop", stopChunks[0].Choices[0].FinishReason)
	}
}

// TestClaudeToChat_MessageStopWithoutDeltaUsage_NoUsage 仅 message_start + message_stop 不挂伪 usage
func TestClaudeToChat_MessageStopWithoutDeltaUsage_NoUsage(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("", "claude-3", 1234)
	_, _ = mapper.Map(dto.ClaudeStreamEvent{
		Type: "message_start",
		Message: &dto.ClaudeMessageStart{
			ID: "msg-1", Model: "claude-3",
			Usage: dto.ClaudeUsage{InputTokens: 5},
		},
	})
	chunks, err := mapper.Map(dto.ClaudeStreamEvent{Type: "message_stop"})
	if err != nil {
		t.Fatalf("message_stop: %v", err)
	}
	if len(chunks) != 1 || chunks[0].Usage != nil {
		t.Fatalf("expected finish without usage, got %+v", chunks)
	}
}

// TestChatToResponses_EmitCompleted_UsageDetails 验证 cached/reasoning 细节不丢
func TestChatToResponses_EmitCompleted_UsageDetails(t *testing.T) {
	mapper := newChatToResponsesStreamMapper("resp-d", "m")
	stop := "stop"
	_, _ = mapper.Map(dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Content: "x"}}},
	})
	events, err := mapper.Map(dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{}, FinishReason: &stop}},
		Usage: &dto.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			PromptTokensDetails: &dto.UsageDetails{
				CachedTokens: 30,
				ImageTokens:  2,
			},
			CompletionTokensDetails: &dto.UsageDetails{
				ReasoningTokens: 12,
			},
		},
	})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	var completed *dto.ResponsesStreamEvent
	for i := range events {
		if events[i].Type == "response.completed" {
			completed = &events[i]
			break
		}
	}
	if completed == nil {
		t.Fatalf("expected response.completed, events=%+v", events)
	}
	var resp map[string]any
	if err := json.Unmarshal(completed.Response, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	usage, _ := resp["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("missing usage: %v", resp)
	}
	inDetails, _ := usage["input_tokens_details"].(map[string]any)
	if inDetails == nil || inDetails["cached_tokens"] != float64(30) || inDetails["image_tokens"] != float64(2) {
		t.Fatalf("input_tokens_details = %v", inDetails)
	}
	outDetails, _ := usage["completion_tokens_details"].(map[string]any)
	if outDetails == nil || outDetails["reasoning_tokens"] != float64(12) {
		t.Fatalf("completion_tokens_details = %v", outDetails)
	}
}

// TestChatToClaude_FinishNilDeltaThenUsage 覆盖 finish_reason + delta:null 再 usage-only
func TestChatToClaude_FinishNilDeltaThenUsage(t *testing.T) {
	mapper := newChatToClaudeStreamMapper("claude-alias")
	stop := "stop"

	// 文本
	if _, err := mapper.Map(dto.ChatCompletionChunk{
		ID: "chatcmpl-1", Model: "gpt-4",
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Role: "assistant", Content: "Hi"}}},
	}); err != nil {
		t.Fatalf("content map: %v", err)
	}

	// finish：Delta == nil
	finishEvents, err := mapper.Map(dto.ChatCompletionChunk{
		ID: "chatcmpl-1", Model: "gpt-4",
		Choices: []dto.ChunkChoice{{Index: 0, Delta: nil, FinishReason: &stop}},
	})
	if err != nil {
		t.Fatalf("finish map: %v", err)
	}
	foundBlockStop := false
	foundMessageStop := false
	for _, ev := range finishEvents {
		if ev.Type == "content_block_stop" {
			foundBlockStop = true
		}
		if ev.Type == "message_stop" {
			foundMessageStop = true
		}
	}
	if !foundBlockStop {
		t.Fatalf("expected content_block_stop on finish with nil delta, got %+v", finishEvents)
	}
	if foundMessageStop {
		t.Fatalf("should not message_stop before usage, got %+v", finishEvents)
	}

	// usage-only
	usageEvents, err := mapper.Map(dto.ChatCompletionChunk{
		ID: "chatcmpl-1", Model: "gpt-4",
		Choices: []dto.ChunkChoice{},
		Usage:   &dto.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	})
	if err != nil {
		t.Fatalf("usage map: %v", err)
	}
	foundDeltaUsage := false
	foundStop := false
	for _, ev := range usageEvents {
		if ev.Type == "message_delta" && ev.Usage != nil &&
			ev.Usage.InputTokens == 10 && ev.Usage.OutputTokens == 2 {
			foundDeltaUsage = true
		}
		if ev.Type == "message_stop" {
			foundStop = true
		}
	}
	if !foundDeltaUsage || !foundStop {
		t.Fatalf("expected message_delta.usage + message_stop, got %+v", usageEvents)
	}

	// Flush 幂等
	flushEvents, err := mapper.Flush()
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(flushEvents) != 0 {
		t.Fatalf("Flush after stop should be empty, got %+v", flushEvents)
	}
}

// TestChatToClaude_FlushEmptyAndPending 空流不产出；pending 无 usage 时 Flush 收尾
func TestChatToClaude_FlushEmptyAndPending(t *testing.T) {
	empty := newChatToClaudeStreamMapper("")
	ev, err := empty.Flush()
	if err != nil {
		t.Fatalf("empty Flush: %v", err)
	}
	if len(ev) != 0 {
		t.Fatalf("empty Flush should be nil, got %+v", ev)
	}

	mapper := newChatToClaudeStreamMapper("")
	stop := "stop"
	_, _ = mapper.Map(dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Content: "x"}}},
	})
	_, _ = mapper.Map(dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{}, FinishReason: &stop}},
	})
	ev, err = mapper.Flush()
	if err != nil {
		t.Fatalf("pending Flush: %v", err)
	}
	foundStop := false
	for _, e := range ev {
		if e.Type == "message_stop" {
			foundStop = true
		}
	}
	if !foundStop {
		t.Fatalf("pending Flush should emit message_stop, got %+v", ev)
	}
}

// TestChatToResponses_FinishNilDeltaThenUsage 延迟 completed + total_tokens 回退
func TestChatToResponses_FinishNilDeltaThenUsage(t *testing.T) {
	mapper := newChatToResponsesStreamMapper("resp-1", "gpt-4o")
	stop := "stop"

	_, _ = mapper.Map(dto.ChatCompletionChunk{
		ID: "chatcmpl-1", Model: "gpt-4o",
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Content: "Hi"}}},
	})

	// finish with nil delta，无 usage → pending，不 completed
	finishEvents, err := mapper.Map(dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: nil, FinishReason: &stop}},
	})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	for _, e := range finishEvents {
		if e.Type == "response.completed" {
			t.Fatalf("should not complete before usage, got %+v", finishEvents)
		}
	}

	// usage-only：TotalTokens=0 时应相加
	usageEvents, err := mapper.Map(dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{},
		Usage:   &dto.Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 0},
	})
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	found := false
	for _, e := range usageEvents {
		if e.Type != "response.completed" {
			continue
		}
		var resp map[string]any
		if err := json.Unmarshal(e.Response, &resp); err != nil {
			t.Fatalf("unmarshal completed: %v", err)
		}
		usage, _ := resp["usage"].(map[string]any)
		if usage == nil {
			t.Fatalf("completed missing usage: %v", resp)
		}
		// JSON numbers are float64
		if usage["input_tokens"] != float64(7) || usage["output_tokens"] != float64(3) || usage["total_tokens"] != float64(10) {
			t.Fatalf("usage = %v, want input=7 output=3 total=10", usage)
		}
		found = true
	}
	if !found {
		t.Fatalf("expected response.completed with usage, got %+v", usageEvents)
	}

	flush, err := mapper.Flush()
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(flush) != 0 {
		t.Fatalf("Flush after completed should be empty")
	}
}

// TestChatToResponses_FlushEmptyAndNoPending 空流 / 仅 delta 未 finish 不 completed
func TestChatToResponses_FlushEmptyAndNoPending(t *testing.T) {
	empty := newChatToResponsesStreamMapper("", "m")
	ev, err := empty.Flush()
	if err != nil || len(ev) != 0 {
		t.Fatalf("empty Flush = %v err=%v", ev, err)
	}

	mapper := newChatToResponsesStreamMapper("resp-1", "m")
	_, _ = mapper.Map(dto.ChatCompletionChunk{
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{Content: "partial"}}},
	})
	ev, err = mapper.Flush()
	if err != nil || len(ev) != 0 {
		t.Fatalf("no-pending Flush should be empty, got %v err=%v", ev, err)
	}
}

// TestBridgeClaudeToResponses_Usage 桥接：Anthropic usage → Chat → Responses completed.usage
func TestBridgeClaudeToResponses_Usage(t *testing.T) {
	m1 := newClaudeToChatStreamMapper("", "alias", 1)
	m2 := newChatToResponsesStreamMapper("resp-bridge", "alias")

	feed := []dto.ClaudeStreamEvent{
		{
			Type: "message_start",
			Message: &dto.ClaudeMessageStart{
				ID: "msg-1", Type: "message", Role: "assistant", Model: "claude",
				Usage: dto.ClaudeUsage{InputTokens: 20},
			},
		},
		{
			Type:         "content_block_start",
			Index:        0,
			ContentBlock: &dto.ContentBlock{Type: "text", Text: ""},
		},
		{
			Type:  "content_block_delta",
			Index: 0,
			Delta: &dto.ClaudeDelta{Type: "text_delta", Text: "ok"},
		},
		{Type: "content_block_stop", Index: 0},
		{
			Type:  "message_delta",
			Delta: &dto.ClaudeDelta{StopReason: "end_turn"},
			Usage: &dto.ClaudeUsage{OutputTokens: 5},
		},
		{Type: "message_stop"},
	}

	var all []dto.ResponsesStreamEvent
	for _, e := range feed {
		chunks, err := m1.Map(e)
		if err != nil {
			t.Fatalf("claude→chat: %v", err)
		}
		for _, ch := range chunks {
			out, err := m2.Map(ch)
			if err != nil {
				t.Fatalf("chat→responses: %v", err)
			}
			all = append(all, out...)
		}
	}
	// 若 finish 同包已带 usage，completed 应已发出；否则 Flush
	flush, err := m2.Flush()
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	all = append(all, flush...)

	found := false
	for _, e := range all {
		if e.Type != "response.completed" {
			continue
		}
		var resp map[string]any
		if err := json.Unmarshal(e.Response, &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		usage, _ := resp["usage"].(map[string]any)
		if usage == nil {
			t.Fatalf("completed without usage: %v", resp)
		}
		if usage["input_tokens"] != float64(20) || usage["output_tokens"] != float64(5) {
			t.Fatalf("usage = %v, want input=20 output=5", usage)
		}
		found = true
	}
	if !found {
		t.Fatalf("expected completed with usage in bridge, events=%+v", all)
	}
}

// TestToolCallIndex 测试 Anthropic tool_call 流式 index/id 完整性
func TestToolCallIndex(t *testing.T) {
	mapper := newClaudeToChatStreamMapper("", "claude-3", 1234)

	// content_block_start for tool_use
	startChunks, err := mapper.Map(dto.ClaudeStreamEvent{
		Type:  "content_block_start",
		Index: 0,
		ContentBlock: &dto.ContentBlock{
			Type:  "tool_use",
			ID:    "toolu_123",
			Name:  "get_weather",
			Input: map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("Map content_block_start error: %v", err)
	}
	if len(startChunks) != 1 {
		t.Fatalf("len(startChunks) = %d, want 1", len(startChunks))
	}

	tc := startChunks[0].Choices[0].Delta.ToolCalls[0]
	if tc.Index == nil || *tc.Index != 0 {
		t.Fatalf("tool_call.index = %v, want 0", tc.Index)
	}
	if tc.ID != "toolu_123" {
		t.Fatalf("tool_call.id = %q, want toolu_123", tc.ID)
	}

	// input_json_delta - 必须保留 index 和 id
	deltaChunks, err := mapper.Map(dto.ClaudeStreamEvent{
		Type:  "content_block_delta",
		Index: 0,
		Delta: &dto.ClaudeDelta{
			Type:        "input_json_delta",
			PartialJSON: ptr(`{"city": "Beijing"}`),
		},
	})
	if err != nil {
		t.Fatalf("Map content_block_delta error: %v", err)
	}
	if len(deltaChunks) != 1 {
		t.Fatalf("len(deltaChunks) = %d, want 1", len(deltaChunks))
	}

	tc2 := deltaChunks[0].Choices[0].Delta.ToolCalls[0]
	if tc2.Index == nil || *tc2.Index != 0 {
		t.Fatalf("delta tool_call.index = %v, want 0", tc2.Index)
	}
	// ID 应该从 mapper 状态中恢复（已有 tool 的 ID）
	if tc2.ID == "" {
		t.Fatalf("delta tool_call.id is empty, want toolu_123")
	}
}
