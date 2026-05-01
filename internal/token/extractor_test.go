package token

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

func TestExtractTextFromOpenAIRequest(t *testing.T) {
	Init()

	req := &dto.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{
				Role:       "assistant",
				Content:    "正在调用工具",
				Name:       "assistant_name",
				ToolCallID: "call_123",
				ToolCalls: []dto.ToolCall{
					{
						Function: dto.ToolCallFunc{
							Name:      "search_web",
							Arguments: `{"q":"hello"}`,
						},
					},
				},
			},
			{Role: "user", Content: "Hello!"},
		},
		ToolChoice: map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "search_web",
			},
		},
	}

	text := ExtractTextFromOpenAIRequest(req)
	if text == "" {
		t.Error("ExtractTextFromOpenAIRequest returned empty string")
	}

	for _, expected := range []string{"assistant_name", "call_123", "search_web", `{"q":"hello"}`, `"type":"function"`} {
		if !strings.Contains(text, expected) {
			t.Errorf("expected extracted text to contain %q, got %q", expected, text)
		}
	}

	tokens := CountTokens(text)
	if tokens == 0 {
		t.Error("CountTokens for extracted text should be > 0")
	}
}

func TestExtractTextFromClaudeRequest(t *testing.T) {
	Init()

	req := &dto.ClaudeRequest{
		Model:  "claude-3-opus",
		System: "You are a helpful assistant.",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello!"},
		},
		ToolChoice: map[string]any{
			"type": "tool",
			"name": "search_web",
		},
	}

	text := ExtractTextFromClaudeRequest(req)
	if text == "" {
		t.Error("ExtractTextFromClaudeRequest returned empty string")
	}

	if !strings.Contains(text, `"name":"search_web"`) {
		t.Errorf("expected extracted text to contain tool_choice, got %q", text)
	}

	tokens := CountTokens(text)
	if tokens == 0 {
		t.Error("CountTokens for extracted text should be > 0")
	}
}

func TestExtractTextFromOpenAIResponse(t *testing.T) {
	Init()

	resp := &dto.ChatCompletionResponse{
		Choices: []dto.Choice{
			{
				Message: &dto.ResMessage{
					Role:    "assistant",
					Content: "Hello! How can I help you?",
				},
			},
		},
	}

	text := ExtractTextFromOpenAIResponse(resp)
	if text == "" {
		t.Error("ExtractTextFromOpenAIResponse returned empty string")
	}

	tokens := CountTokens(text)
	if tokens == 0 {
		t.Error("CountTokens for extracted text should be > 0")
	}
}

func TestExtractTextFromClaudeResponse(t *testing.T) {
	Init()

	resp := &dto.ClaudeResponse{
		Content: []dto.ContentBlock{
			{Type: "text", Text: "Hello! How can I help you?"},
			{Type: "tool_use", Name: "search_web", Input: map[string]any{"q": "weather"}},
		},
	}

	text := ExtractTextFromClaudeResponse(resp)
	if text == "" {
		t.Error("ExtractTextFromClaudeResponse returned empty string")
	}

	for _, expected := range []string{"search_web", `"q":"weather"`} {
		if !strings.Contains(text, expected) {
			t.Errorf("expected extracted text to contain %q, got %q", expected, text)
		}
	}

	tokens := CountTokens(text)
	if tokens == 0 {
		t.Error("CountTokens for extracted text should be > 0")
	}
}

func TestExtractTextFromClaudeStreamEvent(t *testing.T) {
	t.Run("tool_use start", func(t *testing.T) {
		event := &dto.ClaudeStreamEvent{
			Type: "content_block_start",
			ContentBlock: &dto.ContentBlock{
				Type:  "tool_use",
				Name:  "search_web",
				Input: map[string]any{"q": "hello"},
			},
		}

		text := ExtractTextFromClaudeStreamEvent(event)
		for _, expected := range []string{"search_web", `"q":"hello"`} {
			if !strings.Contains(text, expected) {
				t.Errorf("expected extracted text to contain %q, got %q", expected, text)
			}
		}
	})

	t.Run("tool_use delta partial_json", func(t *testing.T) {
		event := &dto.ClaudeStreamEvent{
			Type: "content_block_delta",
			Delta: &dto.ClaudeDelta{
				PartialJSON: `{"loc":"beijing"}`,
			},
		}

		text := ExtractTextFromClaudeStreamEvent(event)
		if !strings.Contains(text, `{"loc":"beijing"}`) {
			t.Errorf("expected extracted text to contain partial_json, got %q", text)
		}
	})
}

func TestCountRequestTokens(t *testing.T) {
	Init()

	t.Run("OpenAI request", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model: "gpt-4",
			Messages: []dto.Message{
				{Role: "user", Content: "Hello!"},
			},
		}

		tokens := CountRequestTokens(req)
		if tokens == 0 {
			t.Error("CountRequestTokens for OpenAI request should be > 0")
		}
	})

	t.Run("Claude request", func(t *testing.T) {
		req := &dto.ClaudeRequest{
			Model: "claude-3-opus",
			Messages: []dto.ClaudeMessage{
				{Role: "user", Content: "Hello!"},
			},
		}

		tokens := CountRequestTokens(req)
		if tokens == 0 {
			t.Error("CountRequestTokens for Claude request should be > 0")
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		tokens := CountRequestTokens("unknown")
		if tokens != 0 {
			t.Errorf("CountRequestTokens for unknown type should be 0, got %d", tokens)
		}
	})
}

func TestCountRequestTokens_ResponsesRequest(t *testing.T) {
	Init()

	req := &dto.ResponsesRequest{
		Model:        "gpt-4o",
		Instructions: json.RawMessage(`"be concise"`),
		Input: json.RawMessage(`[
			{"type":"message","role":"user","content":"hello"},
			{"type":"function_call","name":"get_weather","arguments":"{\"city\":\"beijing\"}"},
			{"type":"function_call_output","output":"sunny"}
		]`),
		Tools: []dto.ResponsesTool{
			{
				Type:        "function",
				Name:        "get_weather",
				Description: "Get current weather",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"city": map[string]any{"type": "string"},
					},
				},
			},
		},
		ToolChoice: map[string]any{"type": "function", "name": "get_weather"},
	}

	tokens := CountRequestTokens(req)
	if tokens <= 0 {
		t.Fatalf("CountRequestTokens for ResponsesRequest should be > 0, got %d", tokens)
	}
}

func TestCountResponseTokens(t *testing.T) {
	Init()

	t.Run("OpenAI response", func(t *testing.T) {
		resp := &dto.ChatCompletionResponse{
			Choices: []dto.Choice{
				{
					Message: &dto.ResMessage{
						Content: "Hello!",
					},
				},
			},
		}

		tokens := CountResponseTokens(resp)
		if tokens == 0 {
			t.Error("CountResponseTokens for OpenAI response should be > 0")
		}
	})

	t.Run("Claude response", func(t *testing.T) {
		resp := &dto.ClaudeResponse{
			Content: []dto.ContentBlock{
				{Type: "text", Text: "Hello!"},
			},
		}

		tokens := CountResponseTokens(resp)
		if tokens == 0 {
			t.Error("CountResponseTokens for Claude response should be > 0")
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		tokens := CountResponseTokens("unknown")
		if tokens != 0 {
			t.Errorf("CountResponseTokens for unknown type should be 0, got %d", tokens)
		}
	})
}
