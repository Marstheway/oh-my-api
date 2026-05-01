package codec

import (
	"encoding/json"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

func convertAnthropicToOpenAIRequest(req *dto.ClaudeRequest, upstreamModel string) *dto.ChatCompletionRequest {
	out := &dto.ChatCompletionRequest{
		Model:       upstreamModel,
		MaxTokens:   req.MaxTokens,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSequences,
	}

	if req.System != nil {
		systemText := extractSystemText(req.System)
		if systemText != "" {
			out.Messages = append(out.Messages, dto.Message{
				Role:    "system",
				Content: systemText,
			})
		}
	}

	for _, msg := range req.Messages {
		out.Messages = append(out.Messages, convertClaudeMessageToOpenAI(msg)...)
	}

	if len(req.Tools) > 0 {
		for _, t := range req.Tools {
			out.Tools = append(out.Tools, dto.Tool{
				Type: "function",
				Function: dto.ToolFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.InputSchema,
				},
			})
		}
	}

	if req.ToolChoice != nil {
		out.ToolChoice = convertClaudeToolChoiceToOpenAI(req.ToolChoice)
	}

	return out
}

func convertClaudeMessageToOpenAI(msg dto.ClaudeMessage) []dto.Message {
	role := msg.Role
	if role == "" {
		role = "user"
	}

	if str, ok := msg.Content.(string); ok {
		return []dto.Message{{Role: role, Content: str}}
	}

	blocks, ok := msg.Content.([]any)
	if !ok {
		return []dto.Message{{Role: role, Content: msg.Content}}
	}

	textContent := ""
	toolCalls := make([]dto.ToolCall, 0)
	toolMessages := make([]dto.Message, 0)

	for _, item := range blocks {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}

		typ, _ := m["type"].(string)
		if typ == "" {
			if _, hasText := m["text"]; hasText {
				typ = "text"
			}
		}

		switch typ {
		case "text", "input_text":
			if t, _ := m["text"].(string); t != "" {
				textContent += t
			}
		case "tool_use":
			if role != "assistant" {
				continue
			}
			id, _ := m["id"].(string)
			name, _ := m["name"].(string)
			args := "{}"
			if rawInput, exists := m["input"]; exists {
				if b, err := json.Marshal(rawInput); err == nil {
					args = string(b)
				}
			}
			toolCalls = append(toolCalls, dto.ToolCall{
				ID:   id,
				Type: "function",
				Function: dto.ToolCallFunc{
					Name:      name,
					Arguments: args,
				},
			})
		case "tool_result":
			if role != "user" {
				continue
			}
			toolUseID, _ := m["tool_use_id"].(string)
			output := toolResultContentToString(m["content"])
			toolMessages = append(toolMessages, dto.Message{
				Role:       "tool",
				ToolCallID: toolUseID,
				Content:    output,
			})
		}
	}

	result := make([]dto.Message, 0, 1+len(toolMessages))
	if role == "assistant" {
		if textContent != "" || len(toolCalls) > 0 {
			result = append(result, dto.Message{
				Role:      "assistant",
				Content:   textContent,
				ToolCalls: toolCalls,
			})
		}
		return result
	}

	if textContent != "" {
		result = append(result, dto.Message{Role: role, Content: textContent})
	}
	result = append(result, toolMessages...)

	if len(result) == 0 {
		result = append(result, dto.Message{Role: role, Content: ""})
	}
	return result
}

func toolResultContentToString(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func normalizeClaudeContent(content any) any {
	blocks, ok := content.([]any)
	if !ok {
		return content
	}
	result := make([]any, len(blocks))
	for i, item := range blocks {
		m, ok := item.(map[string]any)
		if !ok {
			result[i] = item
			continue
		}
		clone := make(map[string]any, len(m)+1)
		for k, v := range m {
			clone[k] = v
		}
		if _, hasText := clone["text"]; hasText {
			if typ, hasType := clone["type"]; !hasType || typ == nil || typ == "" {
				clone["type"] = "text"
			}
		}
		result[i] = clone
	}
	return result
}

func convertClaudeToolChoiceToOpenAI(choice any) any {
	m, ok := choice.(map[string]any)
	if !ok {
		return choice
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "tool":
		name, _ := m["name"].(string)
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": name,
			},
		}
	default:
		return choice
	}
}
