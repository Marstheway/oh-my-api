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

func TestExtractTextFromClaudeRequest_JSONUnmarshaledTools(t *testing.T) {
	Init()

	req := &dto.ClaudeRequest{
		Model: "claude-3-opus",
		Tools: []any{
			map[string]any{
				"type":         "function",
				"name":         "get_weather",
				"description":  "Get current weather",
				"input_schema": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
			},
			map[string]any{
				"type": "web_search_20250305",
				"name": "web_search",
				"user_location": map[string]any{
					"type":     "approximate",
					"country":  "CN",
					"timezone": "Asia/Shanghai",
				},
			},
		},
	}

	text := ExtractTextFromClaudeRequest(req)
	for _, expected := range []string{"get_weather", "Get current weather", `"city"`, "web_search", `"country":"CN"`} {
		if !strings.Contains(text, expected) {
			t.Errorf("expected extracted text to contain %q, got %q", expected, text)
		}
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
		partialJSON := `{"loc":"beijing"}`
		event := &dto.ClaudeStreamEvent{
			Type: "content_block_delta",
			Delta: &dto.ClaudeDelta{
				PartialJSON: &partialJSON,
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

func TestCountRequestTokensFor_ModelSelection(t *testing.T) {
	// 在不加载 deepseek tokenizer 时，所有模型都走 default tokenizer
	for _, upstreamModel := range []string{"gpt-4", "claude-3", "deepseek-chat", "DEEPSEEK-V3"} {
		t.Run("no_deepseek_loaded_"+upstreamModel, func(t *testing.T) {
			// 确保 deepseek tokenizer 未加载
			prev := deepseekTk
			deepseekTk = nil
			defer func() { deepseekTk = prev }()

			if defaultTk == nil {
				defaultTk = &tiktokenTokenizer{encoding: EncodingCL100K}
			}

			tk := Pick(upstreamModel)
			if tk != defaultTk {
				t.Errorf("Pick(%q) = %T, want defaultTk when deepseek not loaded", upstreamModel, tk)
			}
		})
	}

	// 加载 deepseek tokenizer 后再验证
	loadAndInitDeepseek(t)

	t.Run("deepseek_model_selects_deepseek_tokenizer", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model: "deepseek-chat",
			Messages: []dto.Message{
				{Role: "user", Content: "你好，世界"},
			},
		}

		text := ExtractTextFromOpenAIRequest(req)
		dsExpected := deepseekTk.CountTokens(text)
		defExpected := defaultTk.CountTokens(text)

		dsTokens := CountRequestTokensFor("deepseek-chat", req)
		if dsTokens != dsExpected {
			t.Errorf("CountRequestTokensFor(deepseek-chat) = %d, deepseekTk.CountTokens = %d (should match deepseek tokenizer)", dsTokens, dsExpected)
		}
		if defExpected != dsExpected {
			t.Logf("deepseek count=%d, default count=%d (different as expected)", dsExpected, defExpected)
		}

		defTokens := CountRequestTokensFor("gpt-4", req)
		if defTokens != defExpected {
			t.Errorf("CountRequestTokensFor(gpt-4) = %d, defaultTk.CountTokens = %d (should match default tokenizer)", defTokens, defExpected)
		}
	})

	t.Run("case_insensitive_match", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model: "DEEPSEEK-V3",
			Messages: []dto.Message{
				{Role: "user", Content: "Hello!"},
			},
		}

		tk1 := Pick("deepseek-chat")
		tk2 := Pick("DEEPSEEK-V3")
		if tk1 != tk2 {
			t.Error("Pick should be case-insensitive for deepseek model names")
		}

		dsTokens := CountRequestTokensFor("DEEPSEEK-V3", req)
		defTokens := CountRequestTokensFor("gpt-4o", req)
		if dsTokens == 0 || defTokens == 0 {
			t.Fatal("both should produce non-zero counts")
		}
		if dsTokens == defTokens {
			t.Log("deepseek and default tokenizer produced same count for 'Hello!'")
		}
	})

	t.Run("claude_request_with_deepseek_model", func(t *testing.T) {
		req := &dto.ClaudeRequest{
			Model:  "deepseek-chat",
			System: "Be concise.",
			Messages: []dto.ClaudeMessage{
				{Role: "user", Content: "你好，世界"},
			},
		}

		dsTokens := CountRequestTokensFor("deepseek-chat", req)
		defTokens := CountRequestTokensFor("claude-3", req)

		if dsTokens == 0 {
			t.Fatal("CountRequestTokensFor should return > 0 for Claude request with deepseek model")
		}

		text := ExtractTextFromClaudeRequest(req)
		dsExpected := deepseekTk.CountTokens(text)
		if dsTokens != dsExpected {
			t.Errorf("CountRequestTokensFor(deepseek-chat) = %d, deepseekTk.CountTokens = %d", dsTokens, dsExpected)
		}
		_ = defTokens
	})
}
