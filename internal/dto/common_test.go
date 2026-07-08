package dto

import (
	"encoding/json"
	"testing"
)

func TestCommonStreamOptions(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected StreamOptions
	}{
		{
			name:     "empty object",
			input:    `{}`,
			expected: StreamOptions{},
		},
		{
			name:     "include_usage true",
			input:    `{"include_usage": true}`,
			expected: StreamOptions{IncludeUsage: true},
		},
		{
			name:     "include_obfuscation true",
			input:    `{"include_obfuscation": true}`,
			expected: StreamOptions{IncludeObfuscation: true},
		},
		{
			name:     "both fields set",
			input:    `{"include_usage": true, "include_obfuscation": false}`,
			expected: StreamOptions{IncludeUsage: true, IncludeObfuscation: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got StreamOptions
			if err := json.Unmarshal([]byte(tt.input), &got); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if got != tt.expected {
				t.Errorf("Unmarshal: got %+v, want %+v", got, tt.expected)
			}

			// Test marshaling back
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("Failed to marshal: %v", err)
			}
			var got2 StreamOptions
			if err := json.Unmarshal(data, &got2); err != nil {
				t.Fatalf("Failed to unmarshal marshaled data: %v", err)
			}
			if got2 != tt.expected {
				t.Errorf("Round-trip: got %+v, want %+v", got2, tt.expected)
			}
		})
	}
}

func TestCommonResponseFormat(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected ResponseFormat
	}{
		{
			name:     "empty object",
			input:    `{}`,
			expected: ResponseFormat{},
		},
		{
			name:     "text type",
			input:    `{"type": "text"}`,
			expected: ResponseFormat{Type: "text"},
		},
		{
			name:     "json_object type",
			input:    `{"type": "json_object"}`,
			expected: ResponseFormat{Type: "json_object"},
		},
		{
			name:  "json_schema with raw message",
			input: `{"type": "json_schema", "json_schema": {"name": "test", "strict": true}}`,
			expected: ResponseFormat{
				Type:       "json_schema",
				JsonSchema: json.RawMessage(`{"name": "test", "strict": true}`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got ResponseFormat
			if err := json.Unmarshal([]byte(tt.input), &got); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if got.Type != tt.expected.Type {
				t.Errorf("Type: got %q, want %q", got.Type, tt.expected.Type)
			}
			if tt.expected.JsonSchema != nil {
				if got.JsonSchema == nil {
					t.Error("JsonSchema: got nil, want non-nil")
				} else if string(got.JsonSchema) != string(tt.expected.JsonSchema) {
					t.Errorf("JsonSchema: got %s, want %s", got.JsonSchema, tt.expected.JsonSchema)
				}
			}
		})
	}
}

func TestCommonWebSearchOptions(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected WebSearchOptions
	}{
		{
			name:     "empty object",
			input:    `{}`,
			expected: WebSearchOptions{},
		},
		{
			name:     "search_context_size only",
			input:    `{"search_context_size": "medium"}`,
			expected: WebSearchOptions{SearchContextSize: "medium"},
		},
		{
			name:  "user_location as raw message",
			input: `{"search_context_size": "high", "user_location": {"type": "approximate", "city": "Tokyo"}}`,
			expected: WebSearchOptions{
				SearchContextSize: "high",
				UserLocation:      json.RawMessage(`{"type": "approximate", "city": "Tokyo"}`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got WebSearchOptions
			if err := json.Unmarshal([]byte(tt.input), &got); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if got.SearchContextSize != tt.expected.SearchContextSize {
				t.Errorf("SearchContextSize: got %q, want %q", got.SearchContextSize, tt.expected.SearchContextSize)
			}
			if tt.expected.UserLocation != nil {
				if got.UserLocation == nil {
					t.Error("UserLocation: got nil, want non-nil")
				} else if string(got.UserLocation) != string(tt.expected.UserLocation) {
					t.Errorf("UserLocation: got %s, want %s", got.UserLocation, tt.expected.UserLocation)
				}
			}
		})
	}
}

func TestCommonThinking(t *testing.T) {
	budgetTokens := 10000

	tests := []struct {
		name     string
		input    string
		expected Thinking
	}{
		{
			name:     "empty object",
			input:    `{}`,
			expected: Thinking{},
		},
		{
			name:  "enabled with budget_tokens",
			input: `{"type": "enabled", "budget_tokens": 10000}`,
			expected: Thinking{
				Type:         "enabled",
				BudgetTokens: &budgetTokens,
			},
		},
		{
			name:     "disabled type",
			input:    `{"type": "disabled"}`,
			expected: Thinking{Type: "disabled"},
		},
		{
			name:  "with display field",
			input: `{"type": "enabled", "budget_tokens": 5000, "display": "summarized"}`,
			expected: Thinking{
				Type:         "enabled",
				BudgetTokens: intPtr(5000),
				Display:      "summarized",
			},
		},
		{
			name:  "display omitted",
			input: `{"type": "enabled", "budget_tokens": 8000, "display": "omitted"}`,
			expected: Thinking{
				Type:         "enabled",
				BudgetTokens: intPtr(8000),
				Display:      "omitted",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Thinking
			if err := json.Unmarshal([]byte(tt.input), &got); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if got.Type != tt.expected.Type {
				t.Errorf("Type: got %q, want %q", got.Type, tt.expected.Type)
			}
			if tt.expected.BudgetTokens != nil {
				if got.BudgetTokens == nil {
					t.Error("BudgetTokens: got nil, want non-nil")
				} else if *got.BudgetTokens != *tt.expected.BudgetTokens {
					t.Errorf("BudgetTokens: got %d, want %d", *got.BudgetTokens, *tt.expected.BudgetTokens)
				}
			} else if got.BudgetTokens != nil {
				t.Errorf("BudgetTokens: got %d, want nil", *got.BudgetTokens)
			}
			if got.Display != tt.expected.Display {
				t.Errorf("Display: got %q, want %q", got.Display, tt.expected.Display)
			}
		})
	}
}

// Helper function
func intPtr(i int) *int {
	return &i
}
