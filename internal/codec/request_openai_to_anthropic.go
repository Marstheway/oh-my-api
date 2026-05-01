package codec

import (
	"encoding/json"
	"log/slog"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

func convertOpenAIToAnthropicRequest(req *dto.ChatCompletionRequest, upstreamModel string) *dto.ClaudeRequest {
	out := &dto.ClaudeRequest{
		Model:       upstreamModel,
		MaxTokens:   req.MaxTokens,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}
	if out.MaxTokens == 0 {
		out.MaxTokens = 4096
	}
	if len(req.Stop) > 0 {
		out.StopSequences = req.Stop
	}

	if len(req.Tools) > 0 {
		for _, t := range req.Tools {
			out.Tools = append(out.Tools, dto.ClaudeTool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: t.Function.Parameters,
			})
		}
	}
	if req.ToolChoice != nil {
		out.ToolChoice = convertOpenAIToolChoiceToAnthropic(req.ToolChoice)
	}

	fmtMessages := preprocessOpenAIMessages(req.Messages)
	for _, msg := range fmtMessages {
		if msg.Role == "system" {
			systemText := extractSystemText(msg.Content)
			if systemText != "" {
				out.System = systemText
			}
			continue
		}
		if msg.Role == "tool" {
			out.Messages = appendToolResultMessage(out.Messages, msg.ToolCallID, extractTextContent(msg.Content))
			continue
		}
		claudeMsg := convertOpenAIMessageToAnthropic(msg)
		out.Messages = append(out.Messages, claudeMsg)
	}

	if len(out.Messages) > 0 && out.Messages[0].Role != "user" {
		placeholder := dto.ClaudeMessage{
			Role:    "user",
			Content: []dto.ContentBlock{{Type: "text", Text: "..."}},
		}
		out.Messages = append([]dto.ClaudeMessage{placeholder}, out.Messages...)
	}

	return out
}

func extractSystemText(system any) string {
	switch v := system.(type) {
	case string:
		return v
	case []any:
		var text string
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := m["text"].(string); ok {
				text += t
			}
		}
		return text
	default:
		b, _ := json.Marshal(v)
		slog.Debug("unknown system type, marshalling to string")
		return string(b)
	}
}

func extractTextContent(content any) string {
	if strContent, ok := content.(string); ok {
		return strContent
	}
	if blocks, ok := content.([]any); ok {
		var text string
		for _, b := range blocks {
			m, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "text" {
				if t, ok := m["text"].(string); ok {
					text += t
				}
			}
		}
		return text
	}
	return ""
}

func appendToolResultMessage(messages []dto.ClaudeMessage, toolUseID, content string) []dto.ClaudeMessage {
	toolResult := dto.ContentBlock{
		Type:      "tool_result",
		ToolUseID: toolUseID,
		Content:   content,
	}

	if len(messages) > 0 {
		last := &messages[len(messages)-1]
		if last.Role == "user" {
			switch v := last.Content.(type) {
			case string:
				blocks := []dto.ContentBlock{{Type: "text", Text: v}, toolResult}
				last.Content = blocks
				return messages
			case []dto.ContentBlock:
				last.Content = append(v, toolResult)
				return messages
			}
		}
	}

	return append(messages, dto.ClaudeMessage{
		Role: "user",
		Content: []dto.ContentBlock{
			toolResult,
		},
	})
}

func preprocessOpenAIMessages(msgs []dto.Message) []dto.Message {
	result := make([]dto.Message, 0, len(msgs))
	for _, msg := range msgs {
		if msg.Role == "" {
			msg.Role = "user"
		}
		if len(result) > 0 &&
			result[len(result)-1].Role == msg.Role &&
			msg.Role != "tool" {
			last := &result[len(result)-1]
			prevStr, prevIsStr := last.Content.(string)
			curStr, curIsStr := msg.Content.(string)
			if prevIsStr && curIsStr {
				last.Content = prevStr + " " + curStr
				continue
			}
		}
		result = append(result, msg)
	}
	return result
}

func convertOpenAIMessageToAnthropic(msg dto.Message) dto.ClaudeMessage {
	if msg.Role == "tool" {
		contentStr, _ := msg.Content.(string)
		return dto.ClaudeMessage{
			Role: "user",
			Content: []dto.ContentBlock{
				{
					Type:      "tool_result",
					ToolUseID: msg.ToolCallID,
					Content:   contentStr,
				},
			},
		}
	}

	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		blocks := make([]dto.ContentBlock, 0, len(msg.ToolCalls)+1)
		if textContent := extractTextContent(msg.Content); textContent != "" {
			blocks = append(blocks, dto.ContentBlock{Type: "text", Text: textContent})
		}
		for _, tc := range msg.ToolCalls {
			var input map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
				slog.Debug("failed to parse tool_call arguments", "error", err)
				input = map[string]any{}
			}
			blocks = append(blocks, dto.ContentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: input,
			})
		}
		return dto.ClaudeMessage{Role: "assistant", Content: blocks}
	}

	if textContent := extractTextContent(msg.Content); textContent != "" {
		return dto.ClaudeMessage{Role: msg.Role, Content: textContent}
	}

	return dto.ClaudeMessage{Role: msg.Role, Content: ""}
}

func convertOpenAIToolChoiceToAnthropic(choice any) any {
	switch v := choice.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]any{"type": "auto"}
		case "none":
			return map[string]any{"type": "auto"}
		case "required":
			return map[string]any{"type": "any"}
		}
	case map[string]any:
		if fn, ok := v["function"].(map[string]any); ok {
			return map[string]any{
				"type": "tool",
				"name": fn["name"],
			}
		}
	}
	return map[string]any{"type": "auto"}
}
