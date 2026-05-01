package token

import (
	"testing"
)

func TestCountTokens(t *testing.T) {
	Init()

	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name:     "empty string",
			text:     "",
			expected: 0,
		},
		{
			name:     "simple english",
			text:     "Hello, world!",
			expected: 4,
		},
		{
			name:     "chinese text",
			text:     "你好世界",
			expected: 8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CountTokens(tt.text)
			if tt.expected > 0 {
				if result == 0 {
					t.Errorf("CountTokens(%q) = %d, expected > 0", tt.text, result)
				}
			} else {
				if result != tt.expected {
					t.Errorf("CountTokens(%q) = %d, expected %d", tt.text, result, tt.expected)
				}
			}
		})
	}
}

func TestStreamCounter(t *testing.T) {
	Init()

	counter := NewStreamCounter(100)

	if counter.GetInputTokens() != 100 {
		t.Errorf("GetInputTokens() = %d, expected 100", counter.GetInputTokens())
	}

	if counter.GetOutputTokens() != 0 {
		t.Errorf("GetOutputTokens() = %d, expected 0", counter.GetOutputTokens())
	}

	counter.AddOutputTokens("Hello, world!")

	if counter.GetOutputTokens() == 0 {
		t.Errorf("GetOutputTokens() should be > 0 after AddOutputTokens")
	}

	initial := counter.GetOutputTokens()
	counter.AddOutputTokens("")
	if counter.GetOutputTokens() != initial {
		t.Errorf("AddOutputTokens with empty string should not change count")
	}
}