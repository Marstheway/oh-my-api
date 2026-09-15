package config

import (
	"testing"
)

func TestApplyReasoningEffort(t *testing.T) {
	tests := []struct {
		name           string
		value          string
		allowed        []string
		expectedResult string
		expectedAction ReasoningEffortAction
	}{
		// 未配置：透传
		{
			name:           "empty allowed should passthrough any value",
			value:          "medium",
			allowed:        nil,
			expectedResult: "medium",
			expectedAction: ActionPassthrough,
		},
		{
			name:           "empty allowed should passthrough empty value",
			value:          "",
			allowed:        nil,
			expectedResult: "",
			expectedAction: ActionPassthrough,
		},
		// 允许集模式：钳制
		{
			name:           "allowed set should keep exact match",
			value:          "high",
			allowed:        []string{"high", "max"},
			expectedResult: "high",
			expectedAction: ActionReplace,
		},
		{
			name:           "allowed set should clamp medium to high",
			value:          "medium",
			allowed:        []string{"high", "max"},
			expectedResult: "high",
			expectedAction: ActionReplace,
		},
		{
			name:           "allowed set should clamp xhigh to max",
			value:          "xhigh",
			allowed:        []string{"high", "max"},
			expectedResult: "max",
			expectedAction: ActionReplace,
		},
		{
			name:           "allowed set should clamp unknown to max",
			value:          "ultra max",
			allowed:        []string{"high", "max"},
			expectedResult: "max",
			expectedAction: ActionReplace,
		},
		{
			name:           "allowed set should clamp high to max when equal distance",
			value:          "high",
			allowed:        []string{"low", "max"},
			expectedResult: "max", // high 与 low/max 等距，取更高的 max
			expectedAction: ActionReplace,
		},
		{
			name:           "allowed set should passthrough empty value",
			value:          "",
			allowed:        []string{"high", "max"},
			expectedResult: "",
			expectedAction: ActionPassthrough,
		},
		{
			name:           "allowed set should normalize and clamp",
			value:          " MEDIUM ",
			allowed:        []string{"low", "high"},
			expectedResult: "high",
			expectedAction: ActionReplace,
		},
		// none 档位（思考关闭）
		{
			name:           "none in allowed set should keep exact match",
			value:          "none",
			allowed:        []string{"none"},
			expectedResult: "none",
			expectedAction: ActionReplace,
		},
		{
			name:           "allowed set with only none should clamp any effort to none",
			value:          "high",
			allowed:        []string{"none"},
			expectedResult: "none",
			expectedAction: ActionReplace,
		},
		{
			name:           "none should clamp to lowest tier in mixed allowed set",
			value:          "none",
			allowed:        []string{"low", "high"},
			expectedResult: "low",
			expectedAction: ActionReplace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, action := ApplyReasoningEffort(tt.value, tt.allowed)
			if result != tt.expectedResult {
				t.Errorf("result = %q, want %q", result, tt.expectedResult)
			}
			if action != tt.expectedAction {
				t.Errorf("action = %v, want %v", action, tt.expectedAction)
			}
		})
	}
}
