package dto

import (
	"encoding/json"
	"testing"
)

func TestOllamaChatDTO_RequestSerialization(t *testing.T) {
	temp := 0.7
	topP := 0.9
	topK := 40
	seed := int64(42)

	req := OllamaChatRequest{
		Model:  "llama3.2",
		Stream: true,
		Messages: []OllamaChatMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
		},
		Options: map[string]any{
			"temperature": temp,
			"top_p":       topP,
			"top_k":       topK,
			"seed":        seed,
			"num_predict": 512,
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal OllamaChatRequest: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if out["model"] != "llama3.2" {
		t.Errorf("model = %v, want llama3.2", out["model"])
	}
	if out["stream"] != true {
		t.Errorf("stream = %v, want true", out["stream"])
	}
	msgs, ok := out["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2", len(msgs))
	}
}

func TestOllamaChatDTO_ToolsAndToolCallsSerialization(t *testing.T) {
	argsJSON := json.RawMessage(`{"city":"beijing"}`)
	req := OllamaChatRequest{
		Model: "llama3.2",
		Messages: []OllamaChatMessage{
			{
				Role: "assistant",
				ToolCalls: []OllamaToolCall{
					{
						Function: OllamaToolCallFunction{
							Name:      "get_weather",
							Arguments: argsJSON,
						},
					},
				},
			},
			{
				Role:     "tool",
				Content:  "sunny",
				ToolName: "get_weather",
			},
		},
		Tools: []OllamaTool{
			{
				Type: "function",
				Function: OllamaToolFunction{
					Name:        "get_weather",
					Description: "Get weather",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	tools, ok := out["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}
	tool := tools[0].(map[string]any)
	fn := tool["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("tool name = %v, want get_weather", fn["name"])
	}

	msgs := out["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2", len(msgs))
	}
	assistantMsg := msgs[0].(map[string]any)
	toolCalls, ok := assistantMsg["tool_calls"].([]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(toolCalls))
	}
	tc := toolCalls[0].(map[string]any)
	tcFn := tc["function"].(map[string]any)
	if tcFn["name"] != "get_weather" {
		t.Errorf("tool_call name = %v, want get_weather", tcFn["name"])
	}

	toolMsg := msgs[1].(map[string]any)
	if toolMsg["role"] != "tool" {
		t.Errorf("tool msg role = %v, want tool", toolMsg["role"])
	}
	if toolMsg["tool_name"] != "get_weather" {
		t.Errorf("tool_name = %v, want get_weather", toolMsg["tool_name"])
	}
}

func TestOllamaChatDTO_FormatField(t *testing.T) {
	reqJSON := OllamaChatRequest{
		Model:    "llama3.2",
		Messages: []OllamaChatMessage{{Role: "user", Content: "hi"}},
		Format:   "json",
	}
	data, err := json.Marshal(reqJSON)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if out["format"] != "json" {
		t.Errorf("format = %v, want json", out["format"])
	}

	schema := map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}
	reqSchema := OllamaChatRequest{
		Model:    "llama3.2",
		Messages: []OllamaChatMessage{{Role: "user", Content: "hi"}},
		Format:   schema,
	}
	data2, err := json.Marshal(reqSchema)
	if err != nil {
		t.Fatalf("marshal schema error: %v", err)
	}
	var out2 map[string]any
	if err := json.Unmarshal(data2, &out2); err != nil {
		t.Fatalf("unmarshal schema error: %v", err)
	}
	fmtObj, ok := out2["format"].(map[string]any)
	if !ok {
		t.Fatalf("format is not object: %T", out2["format"])
	}
	if fmtObj["type"] != "object" {
		t.Errorf("format.type = %v, want object", fmtObj["type"])
	}
}

func TestOllamaChatDTO_ThinkField(t *testing.T) {
	req := OllamaChatRequest{
		Model:    "llama3.2",
		Messages: []OllamaChatMessage{{Role: "user", Content: "hi"}},
		Think:    json.RawMessage(`true`),
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if out["think"] != true {
		t.Errorf("think = %v, want true", out["think"])
	}
}

func TestOllamaChatDTO_ResponseDeserialization(t *testing.T) {
	raw := `{
		"model": "llama3.2",
		"created_at": "2024-01-01T00:00:00Z",
		"message": {
			"role": "assistant",
			"content": "Hello!",
			"thinking": "Let me think..."
		},
		"done": true,
		"done_reason": "stop",
		"prompt_eval_count": 10,
		"eval_count": 5
	}`

	var resp OllamaChatResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if resp.Model != "llama3.2" {
		t.Errorf("model = %q, want llama3.2", resp.Model)
	}
	if resp.Message.Role != "assistant" {
		t.Errorf("message.role = %q, want assistant", resp.Message.Role)
	}
	if resp.Message.Content != "Hello!" {
		t.Errorf("message.content = %q, want Hello!", resp.Message.Content)
	}
	if resp.Message.Thinking != "Let me think..." {
		t.Errorf("message.thinking = %q, want 'Let me think...'", resp.Message.Thinking)
	}
	if !resp.Done {
		t.Errorf("done = false, want true")
	}
	if resp.DoneReason != "stop" {
		t.Errorf("done_reason = %q, want stop", resp.DoneReason)
	}
	if resp.PromptEvalCount != 10 {
		t.Errorf("prompt_eval_count = %d, want 10", resp.PromptEvalCount)
	}
	if resp.EvalCount != 5 {
		t.Errorf("eval_count = %d, want 5", resp.EvalCount)
	}
}

func TestOllamaChatDTO_StreamChunkDeserialization(t *testing.T) {
	raw := `{
		"model": "llama3.2",
		"created_at": "2024-01-01T00:00:00Z",
		"message": {
			"role": "assistant",
			"content": "Hello"
		},
		"done": false
	}`

	var chunk OllamaChatStreamChunk
	if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if chunk.Model != "llama3.2" {
		t.Errorf("model = %q, want llama3.2", chunk.Model)
	}
	if chunk.Message.Content != "Hello" {
		t.Errorf("content = %q, want Hello", chunk.Message.Content)
	}
	if chunk.Done {
		t.Errorf("done = true, want false")
	}
}

func TestOllamaChatDTO_ErrorResponse(t *testing.T) {
	raw := `{"error": "model not found"}`
	var resp OllamaChatResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if resp.Error != "model not found" {
		t.Errorf("error = %q, want 'model not found'", resp.Error)
	}
}
