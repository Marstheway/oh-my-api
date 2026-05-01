package adaptor

import (
	"encoding/json"
	"log/slog"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

// ConvertClaudeToOpenAI 将 Anthropic 格式请求转换为 OpenAI 格式
func ConvertClaudeToOpenAI(req *dto.ClaudeRequest, upstreamModel string) *dto.ChatCompletionRequest {
	out := &dto.ChatCompletionRequest{
		Model:       upstreamModel,
		MaxTokens:   req.MaxTokens,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSequences,
	}

	// system 处理：string | []block -> OpenAI system message
	if req.System != nil {
		systemText := extractSystemText(req.System)
		if systemText != "" {
			out.Messages = append(out.Messages, dto.Message{
				Role:    "system",
				Content: systemText,
			})
		}
	}

	// messages 转换
	for _, msg := range req.Messages {
		role := msg.Role
		if role == "" {
			role = "user"
		}
		out.Messages = append(out.Messages, dto.Message{
			Role:    role,
			Content: normalizeClaudeContent(msg.Content),
		})
	}

	// tools 转换
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

// extractSystemText 从 system 字段（string 或 []block）提取纯文本
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

// normalizeClaudeContent 标准化 Anthropic content 字段
// - string 原样返回
// - []any (block 数组)：补齐缺失的 type 字段
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
		// text 存在但 type 缺失时补 "text"
		if _, hasText := clone["text"]; hasText {
			if typ, hasType := clone["type"]; !hasType || typ == nil || typ == "" {
				clone["type"] = "text"
			}
		}
		result[i] = clone
	}
	return result
}

// convertClaudeToolChoiceToOpenAI 转换 Anthropic tool_choice 到 OpenAI 格式
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

// ConvertOpenAIToClaudeV2 将 OpenAI 格式请求转换为 Anthropic 格式
func ConvertOpenAIToClaudeV2(req *dto.ChatCompletionRequest, upstreamModel string) *dto.ClaudeRequest {
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
		out.ToolChoice = convertOpenAIToolChoiceToClaude(req.ToolChoice)
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
		claudeMsg := convertOpenAIMessageToClaude(msg)
		out.Messages = append(out.Messages, claudeMsg)
	}

	// 首消息非 user 时插入占位
	if len(out.Messages) > 0 && out.Messages[0].Role != "user" {
		placeholder := dto.ClaudeMessage{
			Role:    "user",
			Content: []dto.ContentBlock{{Type: "text", Text: "..."}},
		}
		out.Messages = append([]dto.ClaudeMessage{placeholder}, out.Messages...)
	}

	return out
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

// preprocessOpenAIMessages 合并连续同 role 消息、修复空 role
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

// convertOpenAIMessageToClaude 将单条 OpenAI Message 转为 ClaudeMessage
func convertOpenAIMessageToClaude(msg dto.Message) dto.ClaudeMessage {
	// tool role -> user 包裹的 tool_result
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

	// assistant 含 tool_calls -> tool_use blocks
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

	// 普通消息：string content
	if textContent := extractTextContent(msg.Content); textContent != "" {
		return dto.ClaudeMessage{Role: msg.Role, Content: textContent}
	}

	return dto.ClaudeMessage{Role: msg.Role, Content: ""}
}

// convertOpenAIToolChoiceToClaude 转换 OpenAI tool_choice 到 Anthropic 格式
func convertOpenAIToolChoiceToClaude(choice any) any {
	switch v := choice.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]any{"type": "auto"}
		case "none":
			return map[string]any{"type": "auto"} // Claude 不支持 none，降级为 auto
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
