package codec

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

// convertResponseRequestToChatRequest 将 OpenAI Responses API 请求转换为 Chat 请求。
func convertResponseRequestToChatRequest(req *dto.ResponsesRequest, upstreamModel string) (*dto.ChatCompletionRequest, error) {
	out := &dto.ChatCompletionRequest{
		Model:       upstreamModel,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}
	if req.MaxOutputTokens != 0 {
		out.MaxTokens = req.MaxOutputTokens
	}

	var messages []dto.Message

	// 解析 Instructions 为 system message
	if len(req.Instructions) > 0 {
		var instrStr string
		if err := json.Unmarshal(req.Instructions, &instrStr); err != nil {
			return nil, fmt.Errorf("instructions must be a string: %w", err)
		}
		messages = append(messages, dto.Message{
			Role:    "system",
			Content: instrStr,
		})
	}

	// 解析 Input，支持字符串或数组两种格式。
	if len(req.Input) > 0 {
		var inputStr string
		if err := json.Unmarshal(req.Input, &inputStr); err == nil {
			messages = append(messages, dto.Message{
				Role:    "user",
				Content: inputStr,
			})
		} else {
			var items []dto.ResponsesInputItem
			if err := json.Unmarshal(req.Input, &items); err != nil {
				return nil, fmt.Errorf("failed to parse input: %w", err)
			}
			for _, item := range items {
				msg, err := convertResponseInputItemToMessage(item)
				if err != nil {
					return nil, err
				}
				messages = append(messages, msg)
			}
		}
	}

	out.Messages = messages

	// 转换 Tools
	if len(req.Tools) > 0 {
		out.Tools = make([]dto.Tool, 0, len(req.Tools))
		for i, t := range req.Tools {
			toolType := strings.TrimSpace(t.Type)
			if toolType == "" {
				toolType = "function"
			}
			// Chat Completions 仅支持 function 工具，其他 Responses 内建工具在该协议下跳过。
			if toolType != "function" {
				continue
			}

			name := strings.TrimSpace(t.Name)
			if name == "" {
				return nil, fmt.Errorf("tools[%d].name is required for function tool", i)
			}

			out.Tools = append(out.Tools, dto.Tool{
				Type: "function",
				Function: dto.ToolFunction{
					Name:        name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			})
		}
	}

	// 转换 ToolChoice
	if req.ToolChoice != nil {
		out.ToolChoice = convertToolChoiceToChat(req.ToolChoice)
	}

	return out, nil
}

// responseMessageContentToChatContent 将 Responses API message item 的 content 字段转换为 Chat 格式的纯文本。
// content 可以是 JSON 字符串或 []dto.ResponsesContentPart 数组。
// 对于数组形式，仅接受 input_text、output_text、text 三种 part 类型，其余类型返回错误。
func responseMessageContentToChatContent(raw json.RawMessage, role string) (string, error) {
	// 优先尝试解析为字符串
	var contentStr string
	if err := json.Unmarshal(raw, &contentStr); err == nil {
		return contentStr, nil
	}

	// 尝试解析为 content parts 数组
	var parts []dto.ResponsesContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("message item content must be a string or array of content parts (role=%s): %w", role, err)
	}

	var text string
	for _, part := range parts {
		switch part.Type {
		case "input_text", "output_text", "text":
			text += part.Text
		default:
			return "", fmt.Errorf("unsupported content part type %q in message (role=%s)", part.Type, role)
		}
	}
	return text, nil
}

// convertResponseInputItemToMessage 将单个 ResponsesInputItem 转换为 Chat Message。
func convertResponseInputItemToMessage(item dto.ResponsesInputItem) (dto.Message, error) {
	switch item.Type {
	case "message":
		role, err := normalizeResponseMessageRoleForChat(item.Role)
		if err != nil {
			return dto.Message{}, err
		}
		contentStr, err := responseMessageContentToChatContent(item.Content, item.Role)
		if err != nil {
			return dto.Message{}, err
		}
		return dto.Message{
			Role:    role,
			Content: contentStr,
		}, nil

	case "function_call":
		return dto.Message{
			Role: "assistant",
			ToolCalls: []dto.ToolCall{
				{
					ID:   item.CallID,
					Type: "function",
					Function: dto.ToolCallFunc{
						Name:      item.Name,
						Arguments: item.Arguments,
					},
				},
			},
		}, nil

	case "function_call_output":
		return dto.Message{
			Role:       "tool",
			ToolCallID: item.CallID,
			Content:    item.Output,
		}, nil

	default:
		return dto.Message{}, fmt.Errorf("unsupported input item type: %s", item.Type)
	}
}

func normalizeResponseMessageRoleForChat(role string) (string, error) {
	switch role {
	case "system", "user", "assistant", "tool":
		return role, nil
	case "developer":
		// Chat Completions 不支持 developer，语义最接近 system。
		return "system", nil
	default:
		return "", fmt.Errorf("unsupported message role for chat: %s", role)
	}
}

// convertToolChoiceToChat 将 Responses API 的 tool_choice 格式转换为 Chat API 格式。
// {"type":"function","name":"X"} -> {"type":"function","function":{"name":"X"}}
// 字符串直接透传。
func convertToolChoiceToChat(choice any) any {
	switch v := choice.(type) {
	case string:
		s := strings.TrimSpace(v)
		switch s {
		case "", "auto", "none", "required":
			if s == "" {
				return "auto"
			}
			return s
		default:
			return "auto"
		}
	case map[string]any:
		if v["type"] == "function" {
			if name, ok := v["name"]; ok && strings.TrimSpace(fmt.Sprint(name)) != "" {
				return map[string]any{
					"type": "function",
					"function": map[string]any{
						"name": name,
					},
				}
			}
			return "auto"
		}
		if typ, ok := v["type"].(string); ok {
			switch strings.TrimSpace(typ) {
			case "auto", "none", "required":
				return strings.TrimSpace(typ)
			}
		}
		return "auto"
	default:
		return "auto"
	}
}
