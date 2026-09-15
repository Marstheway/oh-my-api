package codec

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

const DeepSeekResponsesToolChoiceSingleFunctionCompatRule = "deepseek_responses_tool_choice_single_function"
const DeepSeekResponsesReasoningReplayCompatRule = "deepseek_responses_reasoning_replay"

// applyDeepSeekOpenAIResponseCompat applies DeepSeek-specific request compat for Responses API.
// Today this includes:
//   - relaxing forced single-function tool_choice when thinking mode is active
//   - ensuring replayed reasoning items carry reasoning_text required by DeepSeek thinking mode
func applyDeepSeekOpenAIResponseCompat(req *dto.ResponsesRequest, enabled bool) *dto.ResponsesRequest {
	if req == nil {
		return nil
	}

	out := req
	cloned := false
	ensureClone := func() {
		if cloned {
			return
		}
		clone := *out
		out = &clone
		cloned = true
	}

	if enabled {
		if rewrittenInput, ok := normalizeDeepSeekResponsesReasoningInput(req.Input); ok {
			ensureClone()
			out.Input = rewrittenInput
			slog.Debug("deepseek responses reasoning compatibility applied",
				"compat_rule", DeepSeekResponsesReasoningReplayCompatRule,
			)
		}
	}

	if enabled && shouldDowngradeDeepSeekResponsesToolChoice(req) {
		ensureClone()
		out.ToolChoice = "auto"
		slog.Debug("deepseek responses tool_choice compatibility applied",
			"compat_rule", DeepSeekResponsesToolChoiceSingleFunctionCompatRule,
			"to", "auto",
		)
	}

	return out
}

func shouldDowngradeDeepSeekResponsesToolChoice(req *dto.ResponsesRequest) bool {
	if req == nil || req.ToolChoice == nil || len(req.Tools) != 1 {
		return false
	}
	tool := req.Tools[0]
	toolName := strings.TrimSpace(tool.Name)
	if strings.TrimSpace(tool.Type) != "function" || toolName == "" {
		return false
	}

	switch choice := req.ToolChoice.(type) {
	case string:
		return strings.TrimSpace(choice) == "required"
	case map[string]any:
		choiceType, _ := choice["type"].(string)
		if strings.TrimSpace(choiceType) != "function" {
			return false
		}
		name, _ := choice["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			if fn, ok := choice["function"].(map[string]any); ok {
				name, _ = fn["name"].(string)
				name = strings.TrimSpace(name)
			}
		}
		return name != "" && name == toolName
	default:
		return false
	}
}

func normalizeDeepSeekResponsesReasoningInput(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}

	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}

	rewritten := false
	for i, item := range items {
		if strings.TrimSpace(anyString(item["type"])) != "reasoning" {
			continue
		}
		content, ok := normalizeDeepSeekReasoningContent(item["content"])
		if !ok {
			continue
		}
		items[i]["content"] = content
		rewritten = true
	}

	if !rewritten {
		return nil, false
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(encoded), true
}

func normalizeDeepSeekReasoningContent(raw any) ([]any, bool) {
	parts, ok := raw.([]any)
	if !ok {
		return []any{map[string]any{"type": "reasoning_text", "text": " "}}, true
	}

	content := make([]any, 0, len(parts)+1)
	hasReasoningText := false
	fallbackText := ""

	for _, partRaw := range parts {
		part, ok := partRaw.(map[string]any)
		if !ok {
			content = append(content, partRaw)
			continue
		}
		clone := cloneMap(part)
		partType := strings.TrimSpace(anyString(clone["type"]))
		text, textOK := clone["text"].(string)
		switch partType {
		case "reasoning_text":
			if textOK && strings.TrimSpace(text) != "" {
				hasReasoningText = true
			}
		case "summary_text":
			if textOK && strings.TrimSpace(text) != "" && fallbackText == "" {
				fallbackText = text
			}
		}
		content = append(content, clone)
	}

	if hasReasoningText {
		return content, false
	}
	if fallbackText == "" {
		fallbackText = " "
	}
	content = append([]any{map[string]any{"type": "reasoning_text", "text": fallbackText}}, content...)
	return content, true
}

func anyString(v any) string {
	s, _ := v.(string)
	return s
}

// deepSeekResponsesThinkingEnabled reports whether DeepSeek thinking mode will be active.
// Official DeepSeek default is enabled; only an explicit disable turns it off.
func deepSeekResponsesThinkingEnabled(req *dto.ResponsesRequest, _ string) bool {
	if req == nil {
		return true
	}
	if req.Reasoning != nil && thinkingModeDisabled(req.Reasoning.Effort) {
		return false
	}
	if rawThinkingDisabled(req.EnableThinking) {
		return false
	}
	return true
}

// deepSeekChatThinkingEnabled reports whether DeepSeek thinking mode will be active.
// Official DeepSeek default is enabled; only an explicit disable turns it off.
// This matches production behavior for deepseek-v4-* where upstreams reject forced
// tool_choice even when the client omits thinking/reasoning fields.
func deepSeekChatThinkingEnabled(req *dto.ChatCompletionRequest, _ string) bool {
	if req == nil {
		return true
	}
	if thinkingModeDisabled(req.ReasoningEffort) {
		return false
	}
	if rawThinkingDisabled(req.Reasoning) || rawThinkingDisabled(req.EnableThinking) ||
		rawThinkingDisabled(req.Think) || rawThinkingDisabled(req.THINKING) || rawThinkingDisabled(req.Thinking) {
		return false
	}
	return true
}

// deepSeekClaudeThinkingEnabled reports whether DeepSeek thinking mode will be active.
// Official DeepSeek default is enabled; only an explicit disable turns it off.
func deepSeekClaudeThinkingEnabled(req *dto.ClaudeRequest, _ string) bool {
	if req != nil && req.Thinking != nil && thinkingModeDisabled(req.Thinking.Type) {
		return false
	}
	return true
}

func thinkingModeEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none", "off", "disabled", "false":
		return false
	default:
		return true
	}
}

func thinkingModeDisabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "off", "disabled", "false":
		return true
	default:
		return false
	}
}

func rawThinkingEnabled(raw []byte) bool {
	value, ok := parseRawThinkingValue(raw)
	if !ok {
		return false
	}
	switch value := value.(type) {
	case bool:
		return value
	case string:
		return thinkingModeEnabled(value)
	case map[string]any:
		if enabled, ok := value["enabled"].(bool); ok {
			return enabled
		}
		for _, key := range []string{"type", "mode", "effort"} {
			if mode, ok := value[key].(string); ok {
				return thinkingModeEnabled(mode)
			}
		}
	}
	return false
}

func rawThinkingDisabled(raw []byte) bool {
	value, ok := parseRawThinkingValue(raw)
	if !ok {
		return false
	}
	switch value := value.(type) {
	case bool:
		return !value
	case string:
		return thinkingModeDisabled(value)
	case map[string]any:
		if enabled, ok := value["enabled"].(bool); ok {
			return !enabled
		}
		for _, key := range []string{"type", "mode", "effort"} {
			if mode, ok := value[key].(string); ok {
				return thinkingModeDisabled(mode)
			}
		}
	}
	return false
}

func parseRawThinkingValue(raw []byte) (any, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	return value, true
}
