package config

import (
	"testing"
)

func TestNormalizeReasoningEffortSlice(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
		changed  bool
	}{
		{
			name:     "empty slice",
			input:    []string{},
			expected: []string{},
			changed:  false,
		},
		{
			name:     "nil slice",
			input:    nil,
			expected: nil,
			changed:  false,
		},
		{
			name:     "already normalized",
			input:    []string{"low", "high"},
			expected: []string{"low", "high"},
			changed:  false,
		},
		{
			name:     "uppercase to lowercase",
			input:    []string{"LOW", "HIGH"},
			expected: []string{"low", "high"},
			changed:  true,
		},
		{
			name:     "trim spaces",
			input:    []string{" low ", " high "},
			expected: []string{"low", "high"},
			changed:  true,
		},
		{
			name:     "remove duplicates",
			input:    []string{"low", "low", "high"},
			expected: []string{"low", "high"},
			changed:  true,
		},
		{
			name:     "remove empty elements",
			input:    []string{"low", "", "high", "  "},
			expected: []string{"low", "high"},
			changed:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 复制一份，避免修改测试数据
			var slice []string
			if tt.input != nil {
				slice = make([]string, len(tt.input))
				copy(slice, tt.input)
			}

			changed := normalizeReasoningEffortSlice(&slice)

			if changed != tt.changed {
				t.Errorf("changed = %v, want %v", changed, tt.changed)
			}

			if len(slice) != len(tt.expected) {
				t.Errorf("len(slice) = %d, want %d", len(slice), len(tt.expected))
				return
			}

			for i := range slice {
				if slice[i] != tt.expected[i] {
					t.Errorf("slice[%d] = %q, want %q", i, slice[i], tt.expected[i])
				}
			}
		})
	}
}
