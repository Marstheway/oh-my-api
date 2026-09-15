package codec

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestBuildResponsesToolChoiceSingleFunctionFallback(t *testing.T) {
	body := []byte(`{
		"model":"test-model",
		"input":"hello",
		"stream":true,
		"unknown":{"kept":true},
		"tools":[
			{"type":"function","name":"read_file","parameters":{"type":"object"}},
			{"type":"function","name":"write_file","parameters":{"type":"object"}}
		],
		"tool_choice":{"type":"function","name":"write_file"}
	}`)
	original := append([]byte(nil), body...)

	fallback, changed, err := BuildResponsesToolChoiceSingleFunctionFallback(body)
	if err != nil {
		t.Fatalf("BuildResponsesToolChoiceSingleFunctionFallback error: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if !bytes.Equal(body, original) {
		t.Fatalf("input body was modified: %s", body)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(fallback, &got); err != nil {
		t.Fatalf("unmarshal fallback: %v", err)
	}
	if string(got["tool_choice"]) != `"required"` {
		t.Fatalf("tool_choice = %s, want required", got["tool_choice"])
	}
	var tools []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(got["tools"], &tools); err != nil {
		t.Fatalf("unmarshal tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Type != "function" || tools[0].Name != "write_file" {
		t.Fatalf("tools = %#v, want only write_file", tools)
	}
	if string(got["unknown"]) != `{"kept":true}` {
		t.Fatalf("unknown = %s, want preserved", got["unknown"])
	}
}

func TestBuildResponsesToolChoiceSingleFunctionFallback_NotApplicable(t *testing.T) {
	tests := []string{
		`{"tool_choice":"auto"}`,
		`{"tool_choice":{"type":"function"},"tools":[{"type":"function","name":"a"}]}`,
		`{"tool_choice":{"type":"function","name":"a"}}`,
		`{"tool_choice":{"type":"function","name":"missing"},"tools":[{"type":"function","name":"a"}]}`,
		`{"tool_choice":{"type":"function","name":"a"},"tools":[{"type":"function","name":"a"},{"type":"function","name":"a"}]}`,
		`{"tool_choice":{"type":"computer","name":"a"},"tools":[{"type":"function","name":"a"}]}`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			fallback, changed, err := BuildResponsesToolChoiceSingleFunctionFallback([]byte(body))
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if changed || fallback != nil {
				t.Fatalf("fallback = %s, changed = %v; want unchanged", fallback, changed)
			}
		})
	}
}

func TestBuildResponsesToolChoiceSingleFunctionFallback_InvalidJSON(t *testing.T) {
	_, changed, err := BuildResponsesToolChoiceSingleFunctionFallback([]byte(`{"tool_choice":`))
	if err == nil || changed {
		t.Fatalf("changed = %v, err = %v; want false and error", changed, err)
	}
}

func TestIsResponsesToolChoiceSingleFunctionCompatError(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   bool
	}{
		{http.StatusBadRequest, `{"message":"tool_choice.function is required when type is \\"function\\""}`, true},
		{http.StatusBadRequest, `{"message":"Missing required parameter: 'tool_choice.name'."}`, true},
		{http.StatusBadRequest, `{"error":{"message":"POST \\"https://example.invalid/responses\\": 400 Bad Request {\\n  \\"message\\": \\"Missing required parameter: 'tool_choice.name'.\\"\\n}"}}`, true},
		{http.StatusBadRequest, `{"message":"tool_choice.name is required when type is \\"function\\""}`, true},
		{http.StatusBadRequest, `{"error":{"message":"POST \\"https://taiji.example/responses\\": 400 Bad Request {\\"message\\":\\"tool_choice.name is required when type is \\\\\\"function\\\\\\"\\"}"}}`, true},
		{http.StatusBadRequest, "Failed to deserialize the JSON body into the target type: tool_choice: missing field `name` at line 1 column 1445", true},
		{http.StatusBadRequest, "{\"error\":{\"message\":\"POST \\\"https://tcodex.example/responses\\\": 400 Bad Request {\\\"message\\\":\\\"Failed to deserialize the JSON body into the target type: tool_choice: missing field `name` at line 1 column 1445\\\"}\"}}", true},
		{http.StatusBadRequest, `{"message":"tool_choice.function has invalid value"}`, false},
		{http.StatusBadRequest, `{"message":"tool_choice.function is invalid; required function is unavailable"}`, false},
		{http.StatusBadRequest, `{"message":"tool_choice: missing function name"}`, false},
		{http.StatusBadRequest, "Failed to deserialize the JSON body: tool_choice: missing field `type`", false},
		{http.StatusInternalServerError, `tool_choice.function is required`, false},
	}
	for _, tt := range tests {
		if got := IsResponsesToolChoiceSingleFunctionCompatError(tt.status, []byte(tt.body)); got != tt.want {
			t.Errorf("IsResponsesToolChoiceSingleFunctionCompatError(%d, %q) = %v, want %v", tt.status, tt.body, got, tt.want)
		}
	}
}
