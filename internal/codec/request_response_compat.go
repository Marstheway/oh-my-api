package codec

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const ResponsesToolChoiceSingleFunctionCompatRule = "responses_tool_choice_single_function"

// BuildResponsesToolChoiceSingleFunctionFallback returns a conservative
// Responses-compatible fallback for a request that explicitly requires one
// declared function. It leaves every other request unchanged.
func BuildResponsesToolChoiceSingleFunctionFallback(body []byte) ([]byte, bool, error) {
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, false, fmt.Errorf("parse responses request: %w", err)
	}

	toolChoiceRaw, ok := request["tool_choice"]
	if !ok {
		return nil, false, nil
	}
	var toolChoice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(toolChoiceRaw, &toolChoice); err != nil ||
		strings.TrimSpace(toolChoice.Type) != "function" ||
		strings.TrimSpace(toolChoice.Name) == "" {
		return nil, false, nil
	}

	toolsRaw, ok := request["tools"]
	if !ok {
		return nil, false, nil
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(toolsRaw, &tools); err != nil {
		return nil, false, nil
	}

	var selected json.RawMessage
	matches := 0
	for _, rawTool := range tools {
		var tool struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(rawTool, &tool); err != nil {
			continue
		}
		if strings.TrimSpace(tool.Type) == "function" && strings.TrimSpace(tool.Name) == strings.TrimSpace(toolChoice.Name) {
			selected = rawTool
			matches++
		}
	}
	if matches != 1 {
		return nil, false, nil
	}

	selectedTools, err := json.Marshal([]json.RawMessage{selected})
	if err != nil {
		return nil, false, fmt.Errorf("marshal selected responses tool: %w", err)
	}
	request["tools"] = selectedTools
	request["tool_choice"] = json.RawMessage(`"required"`)

	fallback, err := json.Marshal(request)
	if err != nil {
		return nil, false, fmt.Errorf("marshal responses fallback request: %w", err)
	}
	return fallback, true, nil
}

// IsResponsesToolChoiceSingleFunctionCompatError identifies the known
// Responses-to-Chat conversion failure without matching unrelated 400 errors.
func IsResponsesToolChoiceSingleFunctionCompatError(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(string(body)))
	message = strings.ReplaceAll(message, `\`, "")
	message = strings.ReplaceAll(message, `"`, "")
	// 各上游对 Responses 单函数 tool_choice 的报错措辞不同，统一识别：
	// - 常规 tcodex：Missing required parameter: 'tool_choice.name'.
	// - taiji：tool_choice.name is required when type is "function"
	// - tcodex serde 反序列化：tool_choice: missing field `name`
	return strings.Contains(message, "tool_choice.function is required when type is function") ||
		strings.Contains(message, "missing required parameter: 'tool_choice.name'") ||
		strings.Contains(message, "tool_choice.name is required when type is function") ||
		strings.Contains(message, "tool_choice: missing field `name`")
}
