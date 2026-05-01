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
