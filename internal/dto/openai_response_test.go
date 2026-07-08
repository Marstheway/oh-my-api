package dto

import (
	"encoding/json"
	"testing"
)

func TestResponsesRequest_Unmarshal_Minimal(t *testing.T) {
	raw := `{
		"model": "gpt-4o",
		"instructions": "You are helpful",
		"input": [{"type":"message","role":"user","content":"Hello"}],
		"stream": false
	}`

	var req ResponsesRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if req.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", req.Model, "gpt-4o")
	}

	// instructions 是 json.RawMessage，验证可以再解析为字符串
	var instructions string
	if err := json.Unmarshal(req.Instructions, &instructions); err != nil {
		t.Fatalf("Instructions unmarshal error: %v", err)
	}
	if instructions != "You are helpful" {
		t.Errorf("Instructions = %q, want %q", instructions, "You are helpful")
	}

	// input 是 json.RawMessage，验证可以再解析为数组
	var items []ResponsesInputItem
	if err := json.Unmarshal(req.Input, &items); err != nil {
		t.Fatalf("Input unmarshal error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("Input items len = %d, want 1", len(items))
	}
	if items[0].Type != "message" {
		t.Errorf("items[0].Type = %q, want %q", items[0].Type, "message")
	}
	if items[0].Role != "user" {
		t.Errorf("items[0].Role = %q, want %q", items[0].Role, "user")
	}

	if req.Stream {
		t.Errorf("Stream = true, want false")
	}
}

func TestResponsesStreamEvent_Unmarshal_OutputTextDelta(t *testing.T) {
	raw := `{
		"type": "response.output_text.delta",
		"delta": {"type":"output_text","output_index":0,"content_index":0,"delta":"Hello"}
	}`

	var event ResponsesStreamEvent
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if event.Type != "response.output_text.delta" {
		t.Errorf("Type = %q, want %q", event.Type, "response.output_text.delta")
	}

	if event.Delta == nil {
		t.Fatalf("Delta is nil")
	}

	// 解析 delta 内容
	var delta struct {
		Type         string `json:"type"`
		OutputIndex  int    `json:"output_index"`
		ContentIndex int    `json:"content_index"`
		Delta        string `json:"delta"`
	}
	if err := json.Unmarshal(event.Delta, &delta); err != nil {
		t.Fatalf("Delta unmarshal error: %v", err)
	}
	if delta.Type != "output_text" {
		t.Errorf("delta.Type = %q, want %q", delta.Type, "output_text")
	}
	if delta.Delta != "Hello" {
		t.Errorf("delta.Delta = %q, want %q", delta.Delta, "Hello")
	}
	if delta.OutputIndex != 0 {
		t.Errorf("delta.OutputIndex = %d, want 0", delta.OutputIndex)
	}
	if delta.ContentIndex != 0 {
		t.Errorf("delta.ContentIndex = %d, want 0", delta.ContentIndex)
	}
}

func TestResponsesRequest_NewFields(t *testing.T) {
	// 测试 ResponsesRequest 的新增字段
	t.Run("reasoning field", func(t *testing.T) {
		raw := `{
			"model": "gpt-4o",
			"reasoning": {"effort": "medium", "summary": "auto"}
		}`

		var req ResponsesRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}

		if req.Reasoning == nil {
			t.Fatalf("Reasoning is nil")
		}
		if req.Reasoning.Effort != "medium" {
			t.Errorf("Reasoning.Effort = %q, want %q", req.Reasoning.Effort, "medium")
		}
		if req.Reasoning.Summary != "auto" {
			t.Errorf("Reasoning.Summary = %q, want %q", req.Reasoning.Summary, "auto")
		}
	})

	t.Run("stream_options field", func(t *testing.T) {
		raw := `{
			"model": "gpt-4o",
			"stream": true,
			"stream_options": {"include_usage": true, "include_obfuscation": false}
		}`

		var req ResponsesRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}

		if req.StreamOptions == nil {
			t.Fatalf("StreamOptions is nil")
		}
		if !req.StreamOptions.IncludeUsage {
			t.Errorf("StreamOptions.IncludeUsage = false, want true")
		}
		if req.StreamOptions.IncludeObfuscation {
			t.Errorf("StreamOptions.IncludeObfuscation = true, want false")
		}
	})

	t.Run("raw_message fields", func(t *testing.T) {
		raw := `{
			"model": "gpt-4o",
			"include": ["file_search"],
			"conversation": {"type": "previous"},
			"context_management": {"max_turns": 10},
			"metadata": {"key": "value"},
			"parallel_tool_calls": true,
			"previous_response_id": "resp_123",
			"service_tier": "auto",
			"text": {"format": {"type": "json_object"}},
			"truncation": "auto",
			"user": "user_abc",
			"prompt": "test prompt"
		}`

		var req ResponsesRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}

		// 验证 RawMessage 字段可正常解析
		if len(req.Include) == 0 {
			t.Errorf("Include is empty")
		}
		if len(req.Conversation) == 0 {
			t.Errorf("Conversation is empty")
		}
		if len(req.ContextManagement) == 0 {
			t.Errorf("ContextManagement is empty")
		}
		if len(req.Metadata) == 0 {
			t.Errorf("Metadata is empty")
		}
		if len(req.ParallelToolCalls) == 0 {
			t.Errorf("ParallelToolCalls is empty")
		}
		if req.PreviousResponseID != "resp_123" {
			t.Errorf("PreviousResponseID = %q, want %q", req.PreviousResponseID, "resp_123")
		}
		if req.ServiceTier != "auto" {
			t.Errorf("ServiceTier = %q, want %q", req.ServiceTier, "auto")
		}
		if len(req.Text) == 0 {
			t.Errorf("Text is empty")
		}
		if len(req.Truncation) == 0 {
			t.Errorf("Truncation is empty")
		}
		if len(req.User) == 0 {
			t.Errorf("User is empty")
		}
		if len(req.Prompt) == 0 {
			t.Errorf("Prompt is empty")
		}
	})

	t.Run("int pointer fields", func(t *testing.T) {
		topLogProbs := 5
		maxToolCalls := 10

		raw := `{
			"model": "gpt-4o",
			"top_logprobs": 5,
			"max_tool_calls": 10
		}`

		var req ResponsesRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}

		if req.TopLogProbs == nil || *req.TopLogProbs != topLogProbs {
			t.Errorf("TopLogProbs = %v, want %d", req.TopLogProbs, topLogProbs)
		}
		if req.MaxToolCalls == nil || *req.MaxToolCalls != maxToolCalls {
			t.Errorf("MaxToolCalls = %v, want %d", req.MaxToolCalls, maxToolCalls)
		}
	})

	t.Run("provider specific fields", func(t *testing.T) {
		raw := `{
			"model": "gpt-4o",
			"enable_thinking": true,
			"preset": "default"
		}`

		var req ResponsesRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}

		if len(req.EnableThinking) == 0 {
			t.Errorf("EnableThinking is empty")
		}
		if len(req.Preset) == 0 {
			t.Errorf("Preset is empty")
		}
	})
}

func TestResponsesResponse_NewFields(t *testing.T) {
	raw := `{
		"id": "resp_abc123",
		"object": "response",
		"created_at": 1699000000,
		"model": "gpt-4o",
		"status": "completed",
		"previous_response_id": "resp_prev",
		"output": [{"type": "message", "summary": "Brief summary"}],
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"total_tokens": 150,
			"reasoning_tokens": 20,
			"cached_tokens": 30
		}
	}`

	var resp ResponsesResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	// 验证 ResponsesResponse 新增字段
	if resp.PreviousResponseID != "resp_prev" {
		t.Errorf("PreviousResponseID = %q, want %q", resp.PreviousResponseID, "resp_prev")
	}

	// 验证 ResponsesUsage 新增字段
	if resp.Usage.ReasoningTokens != 20 {
		t.Errorf("Usage.ReasoningTokens = %d, want 20", resp.Usage.ReasoningTokens)
	}
	if resp.Usage.CachedTokens != 30 {
		t.Errorf("Usage.CachedTokens = %d, want 30", resp.Usage.CachedTokens)
	}

	// 验证 ResponsesOutput 新增字段
	if len(resp.Output) == 0 {
		t.Fatalf("Output is empty")
	}
	var summary string
	if err := json.Unmarshal(resp.Output[0].Summary, &summary); err != nil {
		t.Fatalf("Output[0].Summary unmarshal error: %v", err)
	}
	if summary != "Brief summary" {
		t.Errorf("Output[0].Summary = %q, want %q", summary, "Brief summary")
	}
}
