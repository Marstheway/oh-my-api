package dto

import (
	"encoding/json"
	"testing"
)

func TestEmbeddingRequestParseInput_SingleString(t *testing.T) {
	req := &EmbeddingRequest{
		Model: "test-model",
		Input: "hello world",
	}

	result, err := req.ParseInput()
	if err != nil {
		t.Fatalf("ParseInput failed: %v", err)
	}

	if len(result) != 1 {
		t.Errorf("Expected 1 result, got %d", len(result))
	}
	if result[0] != "hello world" {
		t.Errorf("Expected 'hello world', got %q", result[0])
	}
}

func TestEmbeddingRequestParseInput_StringArray(t *testing.T) {
	req := &EmbeddingRequest{
		Model: "test-model",
		Input: []interface{}{"hello", "world", "test"},
	}

	result, err := req.ParseInput()
	if err != nil {
		t.Fatalf("ParseInput failed: %v", err)
	}

	if len(result) != 3 {
		t.Errorf("Expected 3 results, got %d", len(result))
	}
	if result[0] != "hello" || result[1] != "world" || result[2] != "test" {
		t.Errorf("Got unexpected values: %v", result)
	}
}

func TestEmbeddingRequestParseInput_EmptyString(t *testing.T) {
	req := &EmbeddingRequest{
		Model: "test-model",
		Input: "",
	}

	result, err := req.ParseInput()
	if err != nil {
		t.Fatalf("ParseInput failed: %v", err)
	}

	if len(result) != 1 {
		t.Errorf("Expected 1 result, got %d", len(result))
	}
	if result[0] != "" {
		t.Errorf("Expected empty string, got %q", result[0])
	}
}

func TestEmbeddingRequestParseInput_EmptyArray(t *testing.T) {
	req := &EmbeddingRequest{
		Model: "test-model",
		Input: []interface{}{},
	}

	_, err := req.ParseInput()
	if err == nil {
		t.Error("Expected error for empty array, got nil")
	}
}

func TestEmbeddingRequestParseInput_MixedTypeArray(t *testing.T) {
	req := &EmbeddingRequest{
		Model: "test-model",
		Input: []interface{}{"hello", 123, "world"},
	}

	_, err := req.ParseInput()
	if err == nil {
		t.Error("Expected error for mixed type array, got nil")
	}
}

func TestEmbeddingRequestParseInput_NonStringArrayElement(t *testing.T) {
	req := &EmbeddingRequest{
		Model: "test-model",
		Input: []interface{}{"hello", nil, "world"},
	}

	_, err := req.ParseInput()
	if err == nil {
		t.Error("Expected error for non-string array element, got nil")
	}
}

func TestEmbeddingRequestParseInput_InvalidInput(t *testing.T) {
	req := &EmbeddingRequest{
		Model: "test-model",
		Input: 123,
	}

	result, err := req.ParseInput()
	if err == nil {
		t.Error("Expected error for numeric input, got nil")
	}
	if result != nil {
		t.Errorf("Expected nil result, got %v", result)
	}
}

func TestEmbeddingRequestParseInput_MapInput(t *testing.T) {
	req := &EmbeddingRequest{
		Model: "test-model",
		Input: map[string]interface{}{"key": "value"},
	}

	_, err := req.ParseInput()
	if err == nil {
		t.Error("Expected error for map input, got nil")
	}
}

func TestEmbeddingRequestEncodingFormatValidation(t *testing.T) {
	tests := []struct {
		name      string
		format    string
		isValid   bool
	}{
		{"empty string", "", true},
		{"float", "float", true},
		{"base64", "base64", false},
		{"invalid", "invalid", false},
		{"uppercase FLOAT", "FLOAT", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &EmbeddingRequest{
				Model:          "test-model",
				Input:          "test",
				EncodingFormat: tt.format,
			}

			valid := req.IsValidEncodingFormat()
			if valid != tt.isValid {
				t.Errorf("IsValidEncodingFormat(%q): got %v, want %v", tt.format, valid, tt.isValid)
			}
		})
	}
}

func TestEmbeddingResponseItem(t *testing.T) {
	item := &EmbeddingResponseItem{
		Object:    "embedding",
		Index:     0,
		Embedding: []float64{0.1, 0.2, 0.3},
	}

	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var got EmbeddingResponseItem
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if got.Object != "embedding" {
		t.Errorf("Object: got %q, want 'embedding'", got.Object)
	}
	if got.Index != 0 {
		t.Errorf("Index: got %d, want 0", got.Index)
	}
	if len(got.Embedding) != 3 {
		t.Errorf("Embedding length: got %d, want 3", len(got.Embedding))
	}
}

func TestEmbeddingResponse(t *testing.T) {
	usage := Usage{
		PromptTokens:     10,
		CompletionTokens: 0,
		TotalTokens:      10,
	}

	response := &EmbeddingResponse{
		Object: "list",
		Data: []EmbeddingResponseItem{
			{
				Object:    "embedding",
				Index:     0,
				Embedding: []float64{0.1, 0.2},
			},
			{
				Object:    "embedding",
				Index:     1,
				Embedding: []float64{0.3, 0.4},
			},
		},
		Model: "test-model",
		Usage: usage,
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var got EmbeddingResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if got.Object != "list" {
		t.Errorf("Object: got %q, want 'list'", got.Object)
	}
	if got.Model != "test-model" {
		t.Errorf("Model: got %q, want 'test-model'", got.Model)
	}
	if len(got.Data) != 2 {
		t.Errorf("Data length: got %d, want 2", len(got.Data))
	}
	if got.Usage.TotalTokens != 10 {
		t.Errorf("Usage.TotalTokens: got %d, want 10", got.Usage.TotalTokens)
	}
}

func TestEmbeddingResponseJSON(t *testing.T) {
	jsonStr := `{
		"object": "list",
		"data": [
			{
				"object": "embedding",
				"index": 0,
				"embedding": [0.1, 0.2, 0.3]
			}
		],
		"model": "test-model",
		"usage": {
			"prompt_tokens": 5,
			"completion_tokens": 0,
			"total_tokens": 5
		}
	}`

	var response EmbeddingResponse
	if err := json.Unmarshal([]byte(jsonStr), &response); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if response.Object != "list" {
		t.Errorf("Object: got %q, want 'list'", response.Object)
	}
	if len(response.Data) != 1 {
		t.Errorf("Data length: got %d, want 1", len(response.Data))
	}
	if response.Data[0].Index != 0 {
		t.Errorf("First item index: got %d, want 0", response.Data[0].Index)
	}
	if len(response.Data[0].Embedding) != 3 {
		t.Errorf("Embedding size: got %d, want 3", len(response.Data[0].Embedding))
	}
}

func TestEmbeddingRequestDimensions(t *testing.T) {
	dim := 1536
	req := &EmbeddingRequest{
		Model:      "test-model",
		Input:      "test",
		Dimensions: &dim,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var got EmbeddingRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if got.Dimensions == nil {
		t.Error("Dimensions: got nil, want non-nil")
	} else if *got.Dimensions != 1536 {
		t.Errorf("Dimensions: got %d, want 1536", *got.Dimensions)
	}
}

func TestEmbeddingRequestUser(t *testing.T) {
	req := &EmbeddingRequest{
		Model: "test-model",
		Input: "test",
		User:  "user123",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var got EmbeddingRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if got.User != "user123" {
		t.Errorf("User: got %q, want 'user123'", got.User)
	}
}
