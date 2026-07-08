package dto

import (
	"encoding/json"
	"testing"
)

func TestClaudeRequestNewFields(t *testing.T) {
	// Test Thinking field with shared struct
	t.Run("Thinking field", func(t *testing.T) {
		budgetTokens := 10000
		req := ClaudeRequest{
			Model:     "claude-opus-4-7-20250514",
			MaxTokens: 1024,
			Messages: []ClaudeMessage{
				{Role: "user", Content: "Hello"},
			},
			Thinking: &Thinking{
				Type:         "enabled",
				BudgetTokens: &budgetTokens,
				Display:      "minimal",
			},
		}

		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ClaudeRequest
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.Thinking == nil {
			t.Fatal("Thinking should not be nil")
		}
		if unmarshaled.Thinking.Type != "enabled" {
			t.Errorf("Expected Thinking.Type = enabled, got %s", unmarshaled.Thinking.Type)
		}
		if unmarshaled.Thinking.BudgetTokens == nil || *unmarshaled.Thinking.BudgetTokens != 10000 {
			t.Errorf("Expected Thinking.BudgetTokens = 10000, got %v", unmarshaled.Thinking.BudgetTokens)
		}
	})

	// Test RawMessage type fields
	t.Run("RawMessage fields", func(t *testing.T) {
		req := ClaudeRequest{
			Model:        "claude-opus-4-7-20250514",
			MaxTokens:    1024,
			Messages:     []ClaudeMessage{{Role: "user", Content: "Hello"}},
			CacheControl: json.RawMessage(`{"type": "ephemeral"}`),
			Metadata:     json.RawMessage(`{"user_id": "123"}`),
			InferenceGeo: "eu",
			ServiceTier:  "priority",
		}

		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ClaudeRequest
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		// Verify CacheControl
		if unmarshaled.CacheControl == nil {
			t.Fatal("CacheControl should not be nil")
		}
		if string(unmarshaled.CacheControl) != `{"type":"ephemeral"}` {
			t.Errorf("Expected CacheControl = %s, got %s", `{"type":"ephemeral"}`, string(unmarshaled.CacheControl))
		}

		// Verify Metadata
		if unmarshaled.Metadata == nil {
			t.Fatal("Metadata should not be nil")
		}
		if string(unmarshaled.Metadata) != `{"user_id":"123"}` {
			t.Errorf("Expected Metadata = %s, got %s", `{"user_id":"123"}`, string(unmarshaled.Metadata))
		}

		// Verify simple string fields
		if unmarshaled.InferenceGeo != "eu" {
			t.Errorf("Expected InferenceGeo = eu, got %s", unmarshaled.InferenceGeo)
		}
		if unmarshaled.ServiceTier != "priority" {
			t.Errorf("Expected ServiceTier = priority, got %s", unmarshaled.ServiceTier)
		}
	})

	// Test pointer type fields
	t.Run("Pointer type fields", func(t *testing.T) {
		topK := 50
		maxTokensToSample := 1024

		req := ClaudeRequest{
			Model:             "claude-opus-4-7-20250514",
			MaxTokens:         2048,
			Messages:          []ClaudeMessage{{Role: "user", Content: "Hello"}},
			TopK:              &topK,
			MaxTokensToSample: &maxTokensToSample,
		}

		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ClaudeRequest
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.TopK == nil || *unmarshaled.TopK != 50 {
			t.Errorf("Expected TopK = 50, got %v", unmarshaled.TopK)
		}
		if unmarshaled.MaxTokensToSample == nil || *unmarshaled.MaxTokensToSample != 1024 {
			t.Errorf("Expected MaxTokensToSample = 1024, got %v", unmarshaled.MaxTokensToSample)
		}
	})

	// Test Prompt (legacy API)
	t.Run("Prompt field for legacy API", func(t *testing.T) {
		req := ClaudeRequest{
			Model:  "claude-opus-4-7-20250514",
			Prompt: "\n\nHuman: Hello\n\nAssistant:",
		}

		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ClaudeRequest
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.Prompt != "\n\nHuman: Hello\n\nAssistant:" {
			t.Errorf("Expected Prompt to be preserved, got %s", unmarshaled.Prompt)
		}
	})

	// Test complex RawMessage fields
	t.Run("Complex RawMessage fields", func(t *testing.T) {
		req := ClaudeRequest{
			Model:             "claude-opus-4-7-20250514",
			MaxTokens:         1024,
			Messages:          []ClaudeMessage{{Role: "user", Content: "Hello"}},
			ContextManagement: json.RawMessage(`{"enabled": true}`),
			OutputConfig:      json.RawMessage(`{"effort": "high"}`),
			OutputFormat:      json.RawMessage(`{"type": "json_object"}`),
			Container:         json.RawMessage(`{"type": "code_execution"}`),
			McpServers:        json.RawMessage(`[{"name": "server1"}]`),
			Speed:             json.RawMessage(`{"mode": "fast"}`),
		}

		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ClaudeRequest
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		// Verify all RawMessage fields are preserved
		if string(unmarshaled.ContextManagement) != `{"enabled":true}` {
			t.Errorf("ContextManagement mismatch: %s", string(unmarshaled.ContextManagement))
		}
		if string(unmarshaled.OutputConfig) != `{"effort":"high"}` {
			t.Errorf("OutputConfig mismatch: %s", string(unmarshaled.OutputConfig))
		}
		if string(unmarshaled.OutputFormat) != `{"type":"json_object"}` {
			t.Errorf("OutputFormat mismatch: %s", string(unmarshaled.OutputFormat))
		}
		if string(unmarshaled.Container) != `{"type":"code_execution"}` {
			t.Errorf("Container mismatch: %s", string(unmarshaled.Container))
		}
		if string(unmarshaled.McpServers) != `[{"name":"server1"}]` {
			t.Errorf("McpServers mismatch: %s", string(unmarshaled.McpServers))
		}
		if string(unmarshaled.Speed) != `{"mode":"fast"}` {
			t.Errorf("Speed mismatch: %s", string(unmarshaled.Speed))
		}
	})

	// Test nil pointer vs zero value distinction
	t.Run("Nil pointer distinction", func(t *testing.T) {
		jsonData := `{"model": "claude-opus-4-7-20250514", "max_tokens": 1024, "messages": [{"role": "user", "content": "Hello"}]}`

		var req ClaudeRequest
		if err := json.Unmarshal([]byte(jsonData), &req); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		// Pointer fields should be nil when not present in JSON
		if req.TopK != nil {
			t.Errorf("TopK should be nil, got %d", *req.TopK)
		}
		if req.MaxTokensToSample != nil {
			t.Errorf("MaxTokensToSample should be nil, got %d", *req.MaxTokensToSample)
		}
		if req.Thinking != nil {
			t.Error("Thinking should be nil")
		}
	})
}

func TestContentBlockNewFields(t *testing.T) {
	// Test Thinking and Signature fields
	t.Run("Thinking and Signature fields", func(t *testing.T) {
		thinkingContent := "Let me think about this..."
		block := ContentBlock{
			Type:      "thinking",
			Thinking:  &thinkingContent,
			Signature: "abc123",
		}

		data, err := json.Marshal(block)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ContentBlock
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.Thinking == nil || *unmarshaled.Thinking != "Let me think about this..." {
			t.Errorf("Expected Thinking content, got %v", unmarshaled.Thinking)
		}
		if unmarshaled.Signature != "abc123" {
			t.Errorf("Expected Signature = abc123, got %s", unmarshaled.Signature)
		}
	})

	// Test CacheControl field
	t.Run("CacheControl field", func(t *testing.T) {
		block := ContentBlock{
			Type:         "text",
			Text:         "Hello",
			CacheControl: json.RawMessage(`{"type": "ephemeral"}`),
		}

		data, err := json.Marshal(block)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ContentBlock
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if string(unmarshaled.CacheControl) != `{"type":"ephemeral"}` {
			t.Errorf("Expected CacheControl = %s, got %s", `{"type":"ephemeral"}`, string(unmarshaled.CacheControl))
		}
	})

	// Test Delta field for streaming
	t.Run("Delta field", func(t *testing.T) {
		block := ContentBlock{
			Type:  "text_delta",
			Delta: "partial text",
		}

		data, err := json.Marshal(block)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ContentBlock
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.Delta != "partial text" {
			t.Errorf("Expected Delta = 'partial text', got %s", unmarshaled.Delta)
		}
	})

	// Test Role and Model fields for message_start
	t.Run("Role and Model fields", func(t *testing.T) {
		block := ContentBlock{
			Type:  "message_start",
			Role:  "assistant",
			Model: "claude-opus-4-7-20250514",
		}

		data, err := json.Marshal(block)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ContentBlock
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.Role != "assistant" {
			t.Errorf("Expected Role = assistant, got %s", unmarshaled.Role)
		}
		if unmarshaled.Model != "claude-opus-4-7-20250514" {
			t.Errorf("Expected Model = claude-opus-4-7-20250514, got %s", unmarshaled.Model)
		}
	})

	// Test Usage field
	t.Run("Usage field", func(t *testing.T) {
		usage := ClaudeUsage{InputTokens: 100, OutputTokens: 50}
		block := ContentBlock{
			Type:  "message_start",
			Usage: &usage,
		}

		data, err := json.Marshal(block)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ContentBlock
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.Usage == nil {
			t.Fatal("Usage should not be nil")
		}
		if unmarshaled.Usage.InputTokens != 100 || unmarshaled.Usage.OutputTokens != 50 {
			t.Errorf("Usage mismatch: %+v", unmarshaled.Usage)
		}
	})

	// Test StopReason field
	t.Run("StopReason field", func(t *testing.T) {
		stopReason := "end_turn"
		block := ContentBlock{
			Type:       "message_stop",
			StopReason: &stopReason,
		}

		data, err := json.Marshal(block)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ContentBlock
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.StopReason == nil || *unmarshaled.StopReason != "end_turn" {
			t.Errorf("Expected StopReason = end_turn, got %v", unmarshaled.StopReason)
		}
	})

	// Test nil pointer distinction
	t.Run("Nil pointer distinction", func(t *testing.T) {
		jsonData := `{"type": "text", "text": "Hello"}`

		var block ContentBlock
		if err := json.Unmarshal([]byte(jsonData), &block); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if block.Thinking != nil {
			t.Error("Thinking should be nil")
		}
		if block.Usage != nil {
			t.Error("Usage should be nil")
		}
		if block.StopReason != nil {
			t.Error("StopReason should be nil")
		}
	})
}

func TestClaudeUsageCacheFields(t *testing.T) {
	// Test cache token fields
	t.Run("Cache token fields", func(t *testing.T) {
		usage := ClaudeUsage{
			InputTokens:              100,
			OutputTokens:             50,
			CacheCreationInputTokens: 500,
			CacheReadInputTokens:     200,
		}

		data, err := json.Marshal(usage)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ClaudeUsage
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.InputTokens != 100 {
			t.Errorf("Expected InputTokens = 100, got %d", unmarshaled.InputTokens)
		}
		if unmarshaled.OutputTokens != 50 {
			t.Errorf("Expected OutputTokens = 50, got %d", unmarshaled.OutputTokens)
		}
		if unmarshaled.CacheCreationInputTokens != 500 {
			t.Errorf("Expected CacheCreationInputTokens = 500, got %d", unmarshaled.CacheCreationInputTokens)
		}
		if unmarshaled.CacheReadInputTokens != 200 {
			t.Errorf("Expected CacheReadInputTokens = 200, got %d", unmarshaled.CacheReadInputTokens)
		}
	})

	// Test CacheCreation nested struct
	t.Run("CacheCreation nested struct", func(t *testing.T) {
		usage := ClaudeUsage{
			InputTokens:  100,
			OutputTokens: 50,
			CacheCreation: &ClaudeCacheCreationUsage{
				Ephemeral5mInputTokens: 300,
				Ephemeral1hInputTokens: 200,
			},
		}

		data, err := json.Marshal(usage)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ClaudeUsage
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.CacheCreation == nil {
			t.Fatal("CacheCreation should not be nil")
		}
		if unmarshaled.CacheCreation.Ephemeral5mInputTokens != 300 {
			t.Errorf("Expected Ephemeral5mInputTokens = 300, got %d", unmarshaled.CacheCreation.Ephemeral5mInputTokens)
		}
		if unmarshaled.CacheCreation.Ephemeral1hInputTokens != 200 {
			t.Errorf("Expected Ephemeral1hInputTokens = 200, got %d", unmarshaled.CacheCreation.Ephemeral1hInputTokens)
		}
	})

	// Test ServerToolUse nested struct
	t.Run("ServerToolUse nested struct", func(t *testing.T) {
		usage := ClaudeUsage{
			InputTokens:  100,
			OutputTokens: 50,
			ServerToolUse: &ClaudeServerToolUse{
				WebSearchRequests: 5,
			},
		}

		data, err := json.Marshal(usage)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ClaudeUsage
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.ServerToolUse == nil {
			t.Fatal("ServerToolUse should not be nil")
		}
		if unmarshaled.ServerToolUse.WebSearchRequests != 5 {
			t.Errorf("Expected WebSearchRequests = 5, got %d", unmarshaled.ServerToolUse.WebSearchRequests)
		}
	})

	// Test all fields together
	t.Run("All fields together", func(t *testing.T) {
		usage := ClaudeUsage{
			InputTokens:              100,
			OutputTokens:             50,
			CacheCreationInputTokens: 500,
			CacheReadInputTokens:     200,
			CacheCreation: &ClaudeCacheCreationUsage{
				Ephemeral5mInputTokens: 300,
				Ephemeral1hInputTokens: 200,
			},
			ServerToolUse: &ClaudeServerToolUse{
				WebSearchRequests: 5,
			},
		}

		data, err := json.Marshal(usage)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ClaudeUsage
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		// Verify all fields
		if unmarshaled.InputTokens != 100 {
			t.Errorf("InputTokens mismatch")
		}
		if unmarshaled.OutputTokens != 50 {
			t.Errorf("OutputTokens mismatch")
		}
		if unmarshaled.CacheCreationInputTokens != 500 {
			t.Errorf("CacheCreationInputTokens mismatch")
		}
		if unmarshaled.CacheReadInputTokens != 200 {
			t.Errorf("CacheReadInputTokens mismatch")
		}
		if unmarshaled.CacheCreation == nil || unmarshaled.CacheCreation.Ephemeral5mInputTokens != 300 {
			t.Errorf("CacheCreation mismatch")
		}
		if unmarshaled.ServerToolUse == nil || unmarshaled.ServerToolUse.WebSearchRequests != 5 {
			t.Errorf("ServerToolUse mismatch")
		}
	})

	// Test nil pointer distinction
	t.Run("Nil pointer distinction", func(t *testing.T) {
		jsonData := `{"input_tokens": 100, "output_tokens": 50}`

		var usage ClaudeUsage
		if err := json.Unmarshal([]byte(jsonData), &usage); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if usage.InputTokens != 100 || usage.OutputTokens != 50 {
			t.Errorf("Basic tokens mismatch")
		}
		if usage.CacheCreation != nil {
			t.Error("CacheCreation should be nil when not present")
		}
		if usage.ServerToolUse != nil {
			t.Error("ServerToolUse should be nil when not present")
		}
	})

	// Test JSON field names (snake_case)
	t.Run("JSON field names", func(t *testing.T) {
		usage := ClaudeUsage{
			InputTokens:              100,
			OutputTokens:             50,
			CacheCreationInputTokens: 500,
			CacheReadInputTokens:     200,
			CacheCreation: &ClaudeCacheCreationUsage{
				Ephemeral5mInputTokens: 300,
				Ephemeral1hInputTokens: 200,
			},
			ServerToolUse: &ClaudeServerToolUse{
				WebSearchRequests: 5,
			},
		}

		data, err := json.Marshal(usage)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		// Verify snake_case field names in JSON
		jsonStr := string(data)
		expectedFields := []string{
			`"input_tokens"`,
			`"output_tokens"`,
			`"cache_creation_input_tokens"`,
			`"cache_read_input_tokens"`,
			`"cache_creation"`,
			`"ephemeral_5m_input_tokens"`,
			`"ephemeral_1h_input_tokens"`,
			`"server_tool_use"`,
			`"web_search_requests"`,
		}
		for _, field := range expectedFields {
			if !contains(jsonStr, field) {
				t.Errorf("JSON should contain field %s, got: %s", field, jsonStr)
			}
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestClaudeDeltaPartialJSONPointer(t *testing.T) {
	// Test PartialJSON as pointer type - non-nil value
	t.Run("PartialJSON with value", func(t *testing.T) {
		partialJSON := `{"key": "val`
		delta := ClaudeDelta{
			Type:        "input_json_delta",
			PartialJSON: &partialJSON,
		}

		data, err := json.Marshal(delta)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ClaudeDelta
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.PartialJSON == nil {
			t.Fatal("PartialJSON should not be nil")
		}
		if *unmarshaled.PartialJSON != `{"key": "val` {
			t.Errorf("Expected PartialJSON = %s, got %s", `{"key": "val`, *unmarshaled.PartialJSON)
		}
	})

	// Test PartialJSON as pointer type - nil value
	t.Run("PartialJSON nil", func(t *testing.T) {
		delta := ClaudeDelta{
			Type:        "text_delta",
			Text:        "Hello",
			PartialJSON: nil,
		}

		data, err := json.Marshal(delta)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ClaudeDelta
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.PartialJSON != nil {
			t.Errorf("PartialJSON should be nil, got %s", *unmarshaled.PartialJSON)
		}
	})

	// Test PartialJSON from JSON without the field
	t.Run("PartialJSON missing in JSON", func(t *testing.T) {
		jsonData := `{"type": "text_delta", "text": "Hello"}`

		var delta ClaudeDelta
		if err := json.Unmarshal([]byte(jsonData), &delta); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if delta.PartialJSON != nil {
			t.Errorf("PartialJSON should be nil when not present in JSON, got %s", *delta.PartialJSON)
		}
	})

	// Test empty string PartialJSON (should be distinguishable from nil)
	t.Run("PartialJSON empty string", func(t *testing.T) {
		emptyJSON := ""
		delta := ClaudeDelta{
			Type:        "input_json_delta",
			PartialJSON: &emptyJSON,
		}

		data, err := json.Marshal(delta)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ClaudeDelta
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		// Empty string should still be non-nil pointer
		if unmarshaled.PartialJSON == nil {
			t.Error("PartialJSON should not be nil for empty string")
		}
		if *unmarshaled.PartialJSON != "" {
			t.Errorf("Expected empty string, got %s", *unmarshaled.PartialJSON)
		}
	})
}

func TestMessageSource(t *testing.T) {
	// Test base64 image source serialization/deserialization
	t.Run("base64 image source", func(t *testing.T) {
		source := MessageSource{
			Type:      "base64",
			MediaType: "image/jpeg",
			Data:      "base64encodeddata",
		}

		data, err := json.Marshal(source)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled MessageSource
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.Type != "base64" {
			t.Errorf("Expected Type = base64, got %s", unmarshaled.Type)
		}
		if unmarshaled.MediaType != "image/jpeg" {
			t.Errorf("Expected MediaType = image/jpeg, got %s", unmarshaled.MediaType)
		}
		if unmarshaled.Data != "base64encodeddata" {
			t.Errorf("Expected Data = base64encodeddata, got %s", unmarshaled.Data)
		}
	})

	// Test URL image source serialization
	t.Run("URL image source", func(t *testing.T) {
		source := MessageSource{
			Type:      "url",
			MediaType: "image/png",
			Url:       "https://example.com/image.png",
		}

		data, err := json.Marshal(source)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled MessageSource
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.Type != "url" {
			t.Errorf("Expected Type = url, got %s", unmarshaled.Type)
		}
		if unmarshaled.MediaType != "image/png" {
			t.Errorf("Expected MediaType = image/png, got %s", unmarshaled.MediaType)
		}
		if unmarshaled.Url != "https://example.com/image.png" {
			t.Errorf("Expected Url = https://example.com/image.png, got %s", unmarshaled.Url)
		}
	})

	// Test document content serialization
	t.Run("document content with source", func(t *testing.T) {
		block := ContentBlock{
			Type: "document",
			Source: &MessageSource{
				Type:      "base64",
				MediaType: "application/pdf",
				Data:      "pdfbase64data",
			},
		}

		data, err := json.Marshal(block)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ContentBlock
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.Type != "document" {
			t.Errorf("Expected Type = document, got %s", unmarshaled.Type)
		}
		if unmarshaled.Source == nil {
			t.Fatal("Source should not be nil")
		}
		if unmarshaled.Source.Type != "base64" {
			t.Errorf("Expected Source.Type = base64, got %s", unmarshaled.Source.Type)
		}
		if unmarshaled.Source.MediaType != "application/pdf" {
			t.Errorf("Expected Source.MediaType = application/pdf, got %s", unmarshaled.Source.MediaType)
		}
		if unmarshaled.Source.Data != "pdfbase64data" {
			t.Errorf("Expected Source.Data = pdfbase64data, got %s", unmarshaled.Source.Data)
		}
	})

	// Test image content with source
	t.Run("image content with source", func(t *testing.T) {
		block := ContentBlock{
			Type: "image",
			Source: &MessageSource{
				Type:      "base64",
				MediaType: "image/webp",
				Data:      "webpbase64data",
			},
		}

		data, err := json.Marshal(block)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ContentBlock
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.Type != "image" {
			t.Errorf("Expected Type = image, got %s", unmarshaled.Type)
		}
		if unmarshaled.Source == nil {
			t.Fatal("Source should not be nil")
		}
		if unmarshaled.Source.MediaType != "image/webp" {
			t.Errorf("Expected Source.MediaType = image/webp, got %s", unmarshaled.Source.MediaType)
		}
	})

	// Test ContentBlock without source (backward compatibility)
	t.Run("ContentBlock without source", func(t *testing.T) {
		block := ContentBlock{
			Type: "text",
			Text: "Hello world",
		}

		data, err := json.Marshal(block)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled ContentBlock
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.Type != "text" {
			t.Errorf("Expected Type = text, got %s", unmarshaled.Type)
		}
		if unmarshaled.Text != "Hello world" {
			t.Errorf("Expected Text = 'Hello world', got %s", unmarshaled.Text)
		}
		if unmarshaled.Source != nil {
			t.Error("Source should be nil for text block")
		}
	})

	// Test audio source
	t.Run("audio source", func(t *testing.T) {
		source := MessageSource{
			Type:      "base64",
			MediaType: "audio/wav",
			Data:      "audiobase64data",
		}

		data, err := json.Marshal(source)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var unmarshaled MessageSource
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if unmarshaled.MediaType != "audio/wav" {
			t.Errorf("Expected MediaType = audio/wav, got %s", unmarshaled.MediaType)
		}
	})
}
