package codec

import (
	"encoding/json"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

func functionToolChoice(name string) map[string]any {
	return map[string]any{
		"type":     "function",
		"function": map[string]any{"name": name},
	}
}

func singleSessionTitleTool() []dto.Tool {
	return []dto.Tool{
		{Type: "function", Function: dto.ToolFunction{Name: "session_title"}},
	}
}

func TestDeepSeekChatThinkingEnabled_DefaultOn(t *testing.T) {
	tests := []struct {
		name string
		req  *dto.ChatCompletionRequest
		want bool
	}{
		{name: "nil request defaults on", req: nil, want: true},
		{name: "empty request defaults on", req: &dto.ChatCompletionRequest{}, want: true},
		{
			name: "reasoning_effort none disables",
			req:  &dto.ChatCompletionRequest{ReasoningEffort: "none"},
			want: false,
		},
		{
			name: "thinking type disabled",
			req:  &dto.ChatCompletionRequest{Thinking: json.RawMessage(`{"type":"disabled"}`)},
			want: false,
		},
		{
			name: "enable_thinking false",
			req:  &dto.ChatCompletionRequest{EnableThinking: json.RawMessage(`false`)},
			want: false,
		},
		{
			name: "thinking type enabled stays on",
			req:  &dto.ChatCompletionRequest{Thinking: json.RawMessage(`{"type":"enabled"}`)},
			want: true,
		},
		{
			name: "reasoning_effort high stays on",
			req:  &dto.ChatCompletionRequest{ReasoningEffort: "high"},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deepSeekChatThinkingEnabled(tt.req, "deepseek-v4-flash"); got != tt.want {
				t.Errorf("deepSeekChatThinkingEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldDowngradeDeepSeekToolChoice(t *testing.T) {
	tests := []struct {
		name string
		req  *dto.ChatCompletionRequest
		want bool
	}{
		{
			name: "single function object match",
			req: &dto.ChatCompletionRequest{
				Tools:      singleSessionTitleTool(),
				ToolChoice: functionToolChoice("session_title"),
			},
			want: true,
		},
		{
			name: "single tool required string",
			req: &dto.ChatCompletionRequest{
				Tools:      singleSessionTitleTool(),
				ToolChoice: "required",
			},
			want: true,
		},
		{
			name: "single tool auto string",
			req: &dto.ChatCompletionRequest{
				Tools:      singleSessionTitleTool(),
				ToolChoice: "auto",
			},
			want: false,
		},
		{
			name: "single tool none string",
			req: &dto.ChatCompletionRequest{
				Tools:      singleSessionTitleTool(),
				ToolChoice: "none",
			},
			want: false,
		},
		{
			name: "multi tool function object",
			req: &dto.ChatCompletionRequest{
				Tools: []dto.Tool{
					{Type: "function", Function: dto.ToolFunction{Name: "read_file"}},
					{Type: "function", Function: dto.ToolFunction{Name: "session_title"}},
				},
				ToolChoice: functionToolChoice("session_title"),
			},
			want: false,
		},
		{
			name: "multi tool required string",
			req: &dto.ChatCompletionRequest{
				Tools: []dto.Tool{
					{Type: "function", Function: dto.ToolFunction{Name: "read_file"}},
					{Type: "function", Function: dto.ToolFunction{Name: "session_title"}},
				},
				ToolChoice: "required",
			},
			want: false,
		},
		{
			name: "function name mismatch",
			req: &dto.ChatCompletionRequest{
				Tools: []dto.Tool{
					{Type: "function", Function: dto.ToolFunction{Name: "other_tool"}},
				},
				ToolChoice: functionToolChoice("session_title"),
			},
			want: false,
		},
		{
			name: "no tools",
			req: &dto.ChatCompletionRequest{
				ToolChoice: functionToolChoice("session_title"),
			},
			want: false,
		},
		{
			name: "nil tool choice",
			req: &dto.ChatCompletionRequest{
				Tools: singleSessionTitleTool(),
			},
			want: false,
		},
		{
			name: "empty function name",
			req: &dto.ChatCompletionRequest{
				Tools: singleSessionTitleTool(),
				ToolChoice: map[string]any{
					"type":     "function",
					"function": map[string]any{"name": "  "},
				},
			},
			want: false,
		},
		{
			name: "nil request",
			req:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldDowngradeDeepSeekToolChoice(tt.req); got != tt.want {
				t.Errorf("shouldDowngradeDeepSeekToolChoice() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyDeepSeekOpenAIChatRequestCompat_ToolChoice(t *testing.T) {
	tests := []struct {
		name            string
		toolChoice      any
		tools           []dto.Tool
		thinkingEnabled bool
		wantToolChoice  any
		wantRewritten   bool
	}{
		{
			name:            "function object downgraded",
			toolChoice:      functionToolChoice("session_title"),
			tools:           singleSessionTitleTool(),
			thinkingEnabled: true,
			wantToolChoice:  "auto",
			wantRewritten:   true,
		},
		{
			name:            "required downgraded",
			toolChoice:      "required",
			tools:           singleSessionTitleTool(),
			thinkingEnabled: true,
			wantToolChoice:  "auto",
			wantRewritten:   true,
		},
		{
			name:       "multi tool preserved",
			toolChoice: functionToolChoice("session_title"),
			tools: []dto.Tool{
				{Type: "function", Function: dto.ToolFunction{Name: "read_file"}},
				{Type: "function", Function: dto.ToolFunction{Name: "session_title"}},
			},
			wantToolChoice: functionToolChoice("session_title"),
			wantRewritten:  false,
		},
		{
			name:           "thinking disabled preserves required",
			toolChoice:     "required",
			tools:          singleSessionTitleTool(),
			wantToolChoice: "required",
			wantRewritten:  false,
		},
		{
			name:            "none preserved",
			toolChoice:      "none",
			tools:           singleSessionTitleTool(),
			thinkingEnabled: true,
			wantToolChoice:  "none",
			wantRewritten:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &dto.ChatCompletionRequest{
				Model:      "test",
				Tools:      tt.tools,
				ToolChoice: tt.toolChoice,
			}
			out := applyDeepSeekOpenAIChatRequestCompat(req, tt.thinkingEnabled)
			if out == nil {
				t.Fatal("apply returned nil")
			}
			if tt.wantRewritten && out == req {
				t.Fatal("rewritten request must return a clone, not the original pointer")
			}
			// original must never be mutated
			if !toolChoiceEqual(req.ToolChoice, tt.toolChoice) {
				t.Errorf("original ToolChoice mutated: got %#v, want %#v", req.ToolChoice, tt.toolChoice)
			}
			if !toolChoiceEqual(out.ToolChoice, tt.wantToolChoice) {
				t.Errorf("out.ToolChoice = %#v, want %#v", out.ToolChoice, tt.wantToolChoice)
			}
			rewritten := !toolChoiceEqual(out.ToolChoice, tt.toolChoice)
			if rewritten != tt.wantRewritten {
				t.Errorf("rewritten = %v, want %v (out=%#v)", rewritten, tt.wantRewritten, out.ToolChoice)
			}
		})
	}
}

func TestEncodeRequest_DeepSeekToolChoicePaths(t *testing.T) {
	functionChoice := functionToolChoice("session_title")
	tools := singleSessionTitleTool()

	t.Run("chat to chat deepseek function object", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model: "user-model",
			Messages: []dto.Message{
				{Role: "user", Content: "hello"},
			},
			Tools:      tools,
			ToolChoice: functionChoice,
		}
		body, err := (&OpenAIChatCodec{}).EncodeRequest(FormatOpenAIChat, req, "deepseek-reasoner", true)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var encoded dto.ChatCompletionRequest
		if err := json.Unmarshal(body, &encoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if encoded.ToolChoice != "auto" {
			t.Errorf("encoded.ToolChoice = %#v, want auto", encoded.ToolChoice)
		}
		if !toolChoiceEqual(req.ToolChoice, functionChoice) {
			t.Errorf("original mutated: %#v", req.ToolChoice)
		}
	})

	t.Run("chat to chat deepseek-v4-flash default thinking downgrades session_title", func(t *testing.T) {
		// Mirrors grok-build title requests: no thinking fields, single forced tool.
		req := &dto.ChatCompletionRequest{
			Model: "user-model",
			Messages: []dto.Message{
				{Role: "system", Content: "generate title"},
				{Role: "user", Content: "hello"},
			},
			Tools:      tools,
			ToolChoice: functionChoice,
		}
		body, err := (&OpenAIChatCodec{}).EncodeRequest(FormatOpenAIChat, req, "deepseek/deepseek-v4-flash-0731", true)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var encoded dto.ChatCompletionRequest
		if err := json.Unmarshal(body, &encoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if encoded.ToolChoice != "auto" {
			t.Errorf("encoded.ToolChoice = %#v, want auto (DeepSeek default thinking on)", encoded.ToolChoice)
		}
	})

	t.Run("chat to chat deepseek explicit thinking disabled preserves required", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model: "user-model",
			Messages: []dto.Message{
				{Role: "user", Content: "hello"},
			},
			Tools:           tools,
			ToolChoice:      "required",
			Thinking:        json.RawMessage(`{"type":"disabled"}`),
			ReasoningEffort: "none",
		}
		body, err := (&OpenAIChatCodec{}).EncodeRequest(FormatOpenAIChat, req, "deepseek-v4-flash", true)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var encoded dto.ChatCompletionRequest
		if err := json.Unmarshal(body, &encoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if encoded.ToolChoice != "required" {
			t.Errorf("encoded.ToolChoice = %#v, want required when thinking explicitly disabled", encoded.ToolChoice)
		}
	})

	t.Run("chat to chat deepseek required", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model: "user-model",
			Messages: []dto.Message{
				{Role: "user", Content: "hello"},
			},
			Tools:      tools,
			ToolChoice: "required",
		}
		body, err := (&OpenAIChatCodec{}).EncodeRequest(FormatOpenAIChat, req, "deepseek-reasoner", true)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var encoded dto.ChatCompletionRequest
		if err := json.Unmarshal(body, &encoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if encoded.ToolChoice != "auto" {
			t.Errorf("encoded.ToolChoice = %#v, want auto", encoded.ToolChoice)
		}
		if req.ToolChoice != "required" {
			t.Errorf("original mutated: %#v", req.ToolChoice)
		}
	})

	t.Run("chat to chat non-deepseek preserved", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model: "user-model",
			Messages: []dto.Message{
				{Role: "user", Content: "hello"},
			},
			Tools:      tools,
			ToolChoice: functionChoice,
		}
		body, err := (&OpenAIChatCodec{}).EncodeRequest(FormatOpenAIChat, req, "gpt-4o", false)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var encoded dto.ChatCompletionRequest
		if err := json.Unmarshal(body, &encoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		encChoice, ok := encoded.ToolChoice.(map[string]any)
		if !ok || encChoice["type"] != "function" {
			t.Errorf("encoded.ToolChoice = %#v, want preserved function choice", encoded.ToolChoice)
		}
	})

	t.Run("response to chat deepseek function object", func(t *testing.T) {
		req := &dto.ResponsesRequest{
			Model: "user-model",
			Input: json.RawMessage(`"hello"`),
			Tools: []dto.ResponsesTool{
				{Type: "function", Name: "session_title"},
			},
			ToolChoice: map[string]any{
				"type": "function",
				"name": "session_title",
			},
		}
		body, err := (&OpenAIResponseCodec{}).EncodeRequest(FormatOpenAIChat, req, "deepseek-reasoner", true)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var encoded dto.ChatCompletionRequest
		if err := json.Unmarshal(body, &encoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if encoded.ToolChoice != "auto" {
			t.Errorf("encoded.ToolChoice = %#v, want auto", encoded.ToolChoice)
		}
		// original Responses request must keep function object shape
		orig, ok := req.ToolChoice.(map[string]any)
		if !ok || orig["type"] != "function" || orig["name"] != "session_title" {
			t.Errorf("original Responses ToolChoice mutated: %#v", req.ToolChoice)
		}
	})

	t.Run("anthropic to chat deepseek function tool", func(t *testing.T) {
		req := &dto.ClaudeRequest{
			Model: "user-model",
			Messages: []dto.ClaudeMessage{
				{Role: "user", Content: "hello"},
			},
			Tools: []dto.ClaudeTool{
				{Name: "session_title", InputSchema: map[string]any{"type": "object"}},
			},
			ToolChoice: map[string]any{
				"type": "tool",
				"name": "session_title",
			},
		}
		body, err := (&AnthropicMessagesCodec{}).EncodeRequest(FormatOpenAIChat, req, "deepseek-reasoner", true)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var encoded dto.ChatCompletionRequest
		if err := json.Unmarshal(body, &encoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if encoded.ToolChoice != "auto" {
			t.Errorf("encoded.ToolChoice = %#v, want auto", encoded.ToolChoice)
		}
		orig, ok := req.ToolChoice.(map[string]any)
		if !ok || orig["type"] != "tool" {
			t.Errorf("original Anthropic ToolChoice mutated: %#v", req.ToolChoice)
		}
	})

	t.Run("chat to responses deepseek reasoning downgraded", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model:           "user-model",
			Messages:        []dto.Message{{Role: "user", Content: "hello"}},
			ReasoningEffort: "low",
			Tools:           singleSessionTitleTool(),
			ToolChoice:      "required",
		}
		body, err := (&OpenAIChatCodec{}).EncodeRequest(FormatOpenAIResponse, req, "deepseek-reasoner", true)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var encoded dto.ResponsesRequest
		if err := json.Unmarshal(body, &encoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if encoded.ToolChoice != "auto" {
			t.Errorf("encoded.ToolChoice = %#v, want auto", encoded.ToolChoice)
		}
		if req.ToolChoice != "required" {
			t.Errorf("original mutated: %#v", req.ToolChoice)
		}
	})

	t.Run("anthropic to responses deepseek thinking downgraded", func(t *testing.T) {
		req := &dto.ClaudeRequest{
			Model:    "user-model",
			Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
			Tools: []dto.ClaudeTool{
				{Name: "session_title", InputSchema: map[string]any{"type": "object"}},
			},
			ToolChoice: map[string]any{"type": "tool", "name": "session_title"},
			Thinking:   &dto.Thinking{Type: "enabled"},
		}
		body, err := (&AnthropicMessagesCodec{}).EncodeRequest(FormatOpenAIResponse, req, "deepseek-reasoner", true)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var encoded dto.ResponsesRequest
		if err := json.Unmarshal(body, &encoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if encoded.ToolChoice != "auto" {
			t.Errorf("encoded.ToolChoice = %#v, want auto", encoded.ToolChoice)
		}
	})
}

func toolChoiceEqual(a, b any) bool {
	aj, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bj, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(aj) == string(bj)
}

func responsesFunctionToolChoice(name string) map[string]any {
	return map[string]any{
		"type": "function",
		"name": name,
	}
}

func singleResponsesSessionTitleTool() []dto.ResponsesTool {
	return []dto.ResponsesTool{
		{Type: "function", Name: "session_title"},
	}
}

func TestShouldDowngradeDeepSeekResponsesToolChoice(t *testing.T) {
	tests := []struct {
		name string
		req  *dto.ResponsesRequest
		want bool
	}{
		{
			name: "single function top-level name match",
			req: &dto.ResponsesRequest{
				Tools:      singleResponsesSessionTitleTool(),
				ToolChoice: responsesFunctionToolChoice("session_title"),
			},
			want: true,
		},
		{
			name: "single function nested function name match",
			req: &dto.ResponsesRequest{
				Tools:      singleResponsesSessionTitleTool(),
				ToolChoice: functionToolChoice("session_title"),
			},
			want: true,
		},
		{
			name: "single tool required string",
			req: &dto.ResponsesRequest{
				Tools:      singleResponsesSessionTitleTool(),
				ToolChoice: "required",
			},
			want: true,
		},
		{
			name: "single tool auto string",
			req: &dto.ResponsesRequest{
				Tools:      singleResponsesSessionTitleTool(),
				ToolChoice: "auto",
			},
			want: false,
		},
		{
			name: "single tool none string",
			req: &dto.ResponsesRequest{
				Tools:      singleResponsesSessionTitleTool(),
				ToolChoice: "none",
			},
			want: false,
		},
		{
			name: "multi tool function object",
			req: &dto.ResponsesRequest{
				Tools: []dto.ResponsesTool{
					{Type: "function", Name: "read_file"},
					{Type: "function", Name: "session_title"},
				},
				ToolChoice: responsesFunctionToolChoice("session_title"),
			},
			want: false,
		},
		{
			name: "function name mismatch",
			req: &dto.ResponsesRequest{
				Tools: []dto.ResponsesTool{
					{Type: "function", Name: "other_tool"},
				},
				ToolChoice: responsesFunctionToolChoice("session_title"),
			},
			want: false,
		},
		{
			name: "no tools",
			req: &dto.ResponsesRequest{
				ToolChoice: responsesFunctionToolChoice("session_title"),
			},
			want: false,
		},
		{
			name: "nil tool choice",
			req: &dto.ResponsesRequest{
				Tools: singleResponsesSessionTitleTool(),
			},
			want: false,
		},
		{
			name: "empty function name",
			req: &dto.ResponsesRequest{
				Tools: singleResponsesSessionTitleTool(),
				ToolChoice: map[string]any{
					"type": "function",
					"name": "  ",
				},
			},
			want: false,
		},
		{
			name: "nil request",
			req:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldDowngradeDeepSeekResponsesToolChoice(tt.req); got != tt.want {
				t.Errorf("shouldDowngradeDeepSeekResponsesToolChoice() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyDeepSeekOpenAIResponseCompat_ToolChoice(t *testing.T) {
	tests := []struct {
		name           string
		toolChoice     any
		tools          []dto.ResponsesTool
		wantToolChoice any
		wantRewritten  bool
	}{
		{
			name:           "top-level name function object downgraded",
			toolChoice:     responsesFunctionToolChoice("session_title"),
			tools:          singleResponsesSessionTitleTool(),
			wantToolChoice: "auto",
			wantRewritten:  true,
		},
		{
			name:           "required downgraded",
			toolChoice:     "required",
			tools:          singleResponsesSessionTitleTool(),
			wantToolChoice: "auto",
			wantRewritten:  true,
		},
		{
			name:       "multi tool preserved",
			toolChoice: responsesFunctionToolChoice("session_title"),
			tools: []dto.ResponsesTool{
				{Type: "function", Name: "read_file"},
				{Type: "function", Name: "session_title"},
			},
			wantToolChoice: responsesFunctionToolChoice("session_title"),
			wantRewritten:  false,
		},
		{
			name:           "none preserved",
			toolChoice:     "none",
			tools:          singleResponsesSessionTitleTool(),
			wantToolChoice: "none",
			wantRewritten:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &dto.ResponsesRequest{
				Model:      "test",
				Tools:      tt.tools,
				ToolChoice: tt.toolChoice,
			}
			out := applyDeepSeekOpenAIResponseCompat(req, true)
			if out == nil {
				t.Fatal("apply returned nil")
			}
			if tt.wantRewritten && out == req {
				t.Fatal("rewritten request must return a clone, not the original pointer")
			}
			// original must never be mutated
			if !toolChoiceEqual(req.ToolChoice, tt.toolChoice) {
				t.Errorf("original ToolChoice mutated: got %#v, want %#v", req.ToolChoice, tt.toolChoice)
			}
			if !toolChoiceEqual(out.ToolChoice, tt.wantToolChoice) {
				t.Errorf("out.ToolChoice = %#v, want %#v", out.ToolChoice, tt.wantToolChoice)
			}
			rewritten := !toolChoiceEqual(out.ToolChoice, tt.toolChoice)
			if rewritten != tt.wantRewritten {
				t.Errorf("rewritten = %v, want %v (out=%#v)", rewritten, tt.wantRewritten, out.ToolChoice)
			}
		})
	}
}

func TestApplyDeepSeekOpenAIResponseCompat_ReasoningReplay(t *testing.T) {
	t.Run("summary_text is promoted to reasoning_text for deepseek", func(t *testing.T) {
		req := &dto.ResponsesRequest{
			Model: "user-model",
			Input: json.RawMessage(`[
				{"type":"reasoning","content":[{"type":"summary_text","text":"step by step"}]},
				{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}
			]`),
		}
		out := applyDeepSeekOpenAIResponseCompat(req, true)
		if out == nil {
			t.Fatal("apply returned nil")
		}
		if out == req {
			t.Fatal("expected clone when rewriting reasoning replay")
		}
		if string(req.Input) == string(out.Input) {
			t.Fatalf("expected rewritten input, got unchanged: %s", string(out.Input))
		}
		var items []map[string]any
		if err := json.Unmarshal(out.Input, &items); err != nil {
			t.Fatalf("unmarshal output input: %v", err)
		}
		content, _ := items[0]["content"].([]any)
		if len(content) < 2 {
			t.Fatalf("content = %#v, want promoted reasoning_text plus original summary_text", content)
		}
		first, _ := content[0].(map[string]any)
		if first["type"] != "reasoning_text" || first["text"] != "step by step" {
			t.Fatalf("promoted reasoning part = %#v, want reasoning_text/step by step", first)
		}
	})

	t.Run("existing reasoning_text is preserved", func(t *testing.T) {
		req := &dto.ResponsesRequest{
			Model: "user-model",
			Input: json.RawMessage(`[
				{"type":"reasoning","content":[{"type":"reasoning_text","text":"kept"},{"type":"summary_text","text":"summary"}]}
			]`),
		}
		out := applyDeepSeekOpenAIResponseCompat(req, true)
		if out != req {
			t.Fatalf("request should not be cloned when reasoning_text already exists")
		}
	})

	t.Run("empty reasoning content falls back to blank placeholder", func(t *testing.T) {
		req := &dto.ResponsesRequest{
			Model: "user-model",
			Input: json.RawMessage(`[
				{"type":"reasoning","content":[{"type":"summary_text","text":"   "}]}
			]`),
		}
		out := applyDeepSeekOpenAIResponseCompat(req, true)
		var items []map[string]any
		if err := json.Unmarshal(out.Input, &items); err != nil {
			t.Fatalf("unmarshal output input: %v", err)
		}
		content, _ := items[0]["content"].([]any)
		first, _ := content[0].(map[string]any)
		if first["type"] != "reasoning_text" || first["text"] != " " {
			t.Fatalf("placeholder reasoning part = %#v, want single-space reasoning_text", first)
		}
	})

	t.Run("empty reasoning_text is not considered valid", func(t *testing.T) {
		req := &dto.ResponsesRequest{
			Model: "user-model",
			Input: json.RawMessage(`[
				{"type":"reasoning","content":[
					{"type":"reasoning_text","text":"   "},
					{"type":"summary_text","text":"usable summary"}
				]}
			]`),
		}
		out := applyDeepSeekOpenAIResponseCompat(req, true)
		var items []map[string]any
		if err := json.Unmarshal(out.Input, &items); err != nil {
			t.Fatalf("unmarshal output input: %v", err)
		}
		content, _ := items[0]["content"].([]any)
		first, _ := content[0].(map[string]any)
		if first["type"] != "reasoning_text" || first["text"] != "usable summary" {
			t.Fatalf("promoted reasoning part = %#v, want usable summary", first)
		}
	})

	for _, tt := range []struct {
		name  string
		input string
	}{
		{
			name:  "missing content",
			input: `[{"type":"reasoning"}]`,
		},
		{
			name:  "null content",
			input: `[{"type":"reasoning","content":null}]`,
		},
		{
			name:  "non-array content",
			input: `[{"type":"reasoning","content":{"type":"summary_text","text":"summary"}}]`,
		},
		{
			name:  "empty content array",
			input: `[{"type":"reasoning","content":[]}]`,
		},
		{
			name:  "reasoning_text with non-string text",
			input: `[{"type":"reasoning","content":[{"type":"reasoning_text","text":123}]}]`,
		},
	} {
		t.Run(tt.name+" gets blank placeholder", func(t *testing.T) {
			req := &dto.ResponsesRequest{
				Model: "user-model",
				Input: json.RawMessage(tt.input),
			}
			out := applyDeepSeekOpenAIResponseCompat(req, true)
			var items []map[string]any
			if err := json.Unmarshal(out.Input, &items); err != nil {
				t.Fatalf("unmarshal output input: %v", err)
			}
			content, ok := items[0]["content"].([]any)
			if !ok || len(content) == 0 {
				t.Fatalf("content = %#v, want non-empty array", items[0]["content"])
			}
			first, _ := content[0].(map[string]any)
			if first["type"] != "reasoning_text" || first["text"] != " " {
				t.Fatalf("placeholder reasoning part = %#v, want single-space reasoning_text", first)
			}
		})
	}

	t.Run("compat disabled leaves malformed reasoning unchanged", func(t *testing.T) {
		req := &dto.ResponsesRequest{
			Model: "user-model",
			Input: json.RawMessage(`[{"type":"reasoning","content":null}]`),
		}
		out := applyDeepSeekOpenAIResponseCompat(req, false)
		if out != req {
			t.Fatal("disabled compat must return original request")
		}
		if string(out.Input) != string(req.Input) {
			t.Fatalf("disabled compat changed input: %s", out.Input)
		}
	})
}

func TestEncodeRequest_DeepSeekResponsesToolChoicePaths(t *testing.T) {
	tools := singleResponsesSessionTitleTool()

	t.Run("responses to responses deepseek without reasoning defaults thinking on and downgrades", func(t *testing.T) {
		req := &dto.ResponsesRequest{
			Model:      "user-model",
			Input:      json.RawMessage(`"hello"`),
			Tools:      tools,
			ToolChoice: responsesFunctionToolChoice("session_title"),
		}
		body, err := (&OpenAIResponseCodec{}).EncodeRequest(FormatOpenAIResponse, req, "deepseek-chat", true)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var encoded dto.ResponsesRequest
		if err := json.Unmarshal(body, &encoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if encoded.ToolChoice != "auto" {
			t.Errorf("encoded.ToolChoice = %#v, want auto (DeepSeek default thinking on)", encoded.ToolChoice)
		}
	})

	t.Run("responses to responses deepseek explicit reasoning none preserves function object", func(t *testing.T) {
		req := &dto.ResponsesRequest{
			Model:      "user-model",
			Input:      json.RawMessage(`"hello"`),
			Reasoning:  &dto.ResponsesReasoning{Effort: "none"},
			Tools:      tools,
			ToolChoice: responsesFunctionToolChoice("session_title"),
		}
		body, err := (&OpenAIResponseCodec{}).EncodeRequest(FormatOpenAIResponse, req, "deepseek-chat", true)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var encoded dto.ResponsesRequest
		if err := json.Unmarshal(body, &encoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !toolChoiceEqual(encoded.ToolChoice, req.ToolChoice) {
			t.Errorf("encoded.ToolChoice = %#v, want %#v", encoded.ToolChoice, req.ToolChoice)
		}
	})

	t.Run("responses to responses deepseek function object", func(t *testing.T) {
		req := &dto.ResponsesRequest{
			Model:      "user-model",
			Input:      json.RawMessage(`"hello"`),
			Reasoning:  &dto.ResponsesReasoning{Effort: "low"},
			Tools:      tools,
			ToolChoice: responsesFunctionToolChoice("session_title"),
		}
		body, err := (&OpenAIResponseCodec{}).EncodeRequest(FormatOpenAIResponse, req, "deepseek-reasoner", true)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var encoded dto.ResponsesRequest
		if err := json.Unmarshal(body, &encoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if encoded.ToolChoice != "auto" {
			t.Errorf("encoded.ToolChoice = %#v, want auto", encoded.ToolChoice)
		}
		if encoded.Model != "deepseek-reasoner" {
			t.Errorf("encoded.Model = %q, want deepseek-reasoner", encoded.Model)
		}
		if !toolChoiceEqual(req.ToolChoice, responsesFunctionToolChoice("session_title")) {
			t.Errorf("original mutated: %#v", req.ToolChoice)
		}
	})

	t.Run("responses to responses non-deepseek preserved", func(t *testing.T) {
		req := &dto.ResponsesRequest{
			Model:      "user-model",
			Input:      json.RawMessage(`"hello"`),
			Tools:      tools,
			ToolChoice: responsesFunctionToolChoice("session_title"),
		}
		body, err := (&OpenAIResponseCodec{}).EncodeRequest(FormatOpenAIResponse, req, "hy3-ioa", false)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var encoded dto.ResponsesRequest
		if err := json.Unmarshal(body, &encoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		encChoice, ok := encoded.ToolChoice.(map[string]any)
		if !ok || encChoice["type"] != "function" || encChoice["name"] != "session_title" {
			t.Errorf("encoded.ToolChoice = %#v, want preserved function choice", encoded.ToolChoice)
		}
	})
}
