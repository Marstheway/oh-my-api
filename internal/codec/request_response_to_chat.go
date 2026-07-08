package codec

import (
	"encoding/json"
	"fmt"
	"log/slog"
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

			// 先建立 call_id -> tool_call_output 的映射（用于腾讯云等需要交错排列的 API）
			outputByCallID := make(map[string]dto.ResponsesInputItem)
			for _, item := range items {
				if isToolCallOutput(item.Type) {
					if item.CallID != "" {
						outputByCallID[item.CallID] = item
					}
				}
			}

			for _, item := range items {
				// tool_call_output 类型的 item 已在对应的 tool_call 后面处理，跳过
				if isToolCallOutput(item.Type) {
					continue
				}

				msg, err := convertResponseInputItemToMessage(item)
				if err != nil {
					return nil, err
				}
				if msg.Role != "" {
					messages = append(messages, msg)

					// 如果是 tool_call，紧接着插入对应的 tool_call_output（交错排列）
					if isToolCall(item.Type) && item.CallID != "" {
						if outputItem, ok := outputByCallID[item.CallID]; ok {
							outputMsg, err := convertResponseInputItemToMessage(outputItem)
							if err != nil {
								return nil, err
							}
							if outputMsg.Role != "" {
								messages = append(messages, outputMsg)
							}
							delete(outputByCallID, item.CallID) // 防止重复插入
						}
					}
				}
			}

			// 将未被 tool_call 消费的 output item 追加到末尾（仅 output 无 call 的场景）
			for _, outputItem := range outputByCallID {
				outputMsg, err := convertResponseInputItemToMessage(outputItem)
				if err != nil {
					return nil, err
				}
				if outputMsg.Role != "" {
					messages = append(messages, outputMsg)
				}
			}
		}
	}

	out.Messages = messages

	// 转换 Tools
	if len(req.Tools) > 0 {
		out.Tools = make([]dto.Tool, 0, len(req.Tools))
		for _, t := range req.Tools {
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
				return nil, fmt.Errorf("tools[%q].name is required for function tool", name)
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

	// 透传 parallel_tool_calls
	if len(req.ParallelToolCalls) > 0 {
		var ptc bool
		if err := json.Unmarshal(req.ParallelToolCalls, &ptc); err == nil {
			out.ParallelToolCalls = &ptc
		}
	}

	// 静默忽略 reasoning.effort（Chat API 无对应字段）

	return out, nil
}

// responseMessageContentToChatContent 将 Responses API message item 的 content 字段转换为 Chat 格式。
// content 可以是 JSON 字符串或 []dto.ResponsesContentPart 数组。
// 返回 any 类型：纯文本返回 string，含图片返回 []dto.MediaContent 数组。
func responseMessageContentToChatContent(raw json.RawMessage, role string) (any, error) {
	// 优先尝试解析为字符串
	var contentStr string
	if err := json.Unmarshal(raw, &contentStr); err == nil {
		return contentStr, nil
	}

	// 尝试解析为 content parts 数组
	var parts []dto.ResponsesContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("message item content must be a string or array of content parts (role=%s): %w", role, err)
	}

	// 检查是否包含图片
	hasImage := false
	for _, part := range parts {
		if part.Type == "input_image" {
			hasImage = true
			break
		}
	}

	// 无图片，拼接为纯文本
	if !hasImage {
		var text string
		for _, part := range parts {
			switch part.Type {
			case "input_text", "output_text", "text":
				text += part.Text
			default:
				return nil, fmt.Errorf("unsupported content part type %q in message (role=%s)", part.Type, role)
			}
		}
		return text, nil
	}

	// 包含图片，转换为 []dto.MediaContent 数组
	var mediaContents []dto.MediaContent
	for _, part := range parts {
		switch part.Type {
		case "input_text", "output_text", "text":
			mediaContents = append(mediaContents, dto.MediaContent{
				Type: "text",
				Text: part.Text,
			})
		case "input_image":
			// 提取图片 URL（支持字符串或对象两种格式）
			url := extractImageURL(part.ImageURL)
			if url == "" {
				slog.Warn("failed to extract image URL, skipping input_image part", "role", role)
				continue
			}
			mediaContents = append(mediaContents, dto.MediaContent{
				Type: "image_url",
				ImageUrl: &dto.MessageImageUrl{
					Url: url,
				},
			})
		default:
			return nil, fmt.Errorf("unsupported content part type %q in message (role=%s)", part.Type, role)
		}
	}
	return mediaContents, nil
}

// extractImageURL 从 image_url 字段提取 URL，支持字符串或对象两种格式
func extractImageURL(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// 先尝试解析为对象 {"url": "...", "detail": "..."}
	var obj struct {
		URL    string `json:"url"`
		Detail string `json:"detail,omitempty"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.URL != "" {
		return obj.URL
	}

	// 再尝试解析为纯字符串
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str
	}

	return ""
}

// extractToolOutputContent 从工具 output 字段提取内容
// 支持纯字符串和 {"content": [{"type": "text", "text": "..."}]} 对象格式
func extractToolOutputContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// 先尝试解析为字符串
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// 再尝试解析为对象 {"content": [...]}
	var obj struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"is_error"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		var texts []string
		for _, c := range obj.Content {
			if c.Type == "text" && c.Text != "" {
				texts = append(texts, c.Text)
			}
		}
		return strings.Join(texts, "")
	}

	// 都失败，返回空字符串（不阻断流程）
	slog.Warn("failed to parse tool output content, returning empty string", "raw", string(raw))
	return ""
}

// convertResponseInputItemToMessage 将单个 ResponsesInputItem 转换为 Chat Message。
func convertResponseInputItemToMessage(item dto.ResponsesInputItem) (dto.Message, error) {
	switch item.Type {
	case "message":
		role, err := normalizeResponseMessageRoleForChat(item.Role)
		if err != nil {
			return dto.Message{}, err
		}
		content, err := responseMessageContentToChatContent(item.Content, item.Role)
		if err != nil {
			return dto.Message{}, err
		}
		return dto.Message{
			Role:    role,
			Content: content,
		}, nil

	case "function_call", "mcp_tool_call", "custom_tool_call":
		// 确保 arguments 是有效的 JSON 对象字符串，避免上游 Provider 校验失败
		args := normalizeArguments(item.Arguments)
		return dto.Message{
			Role:    "assistant",
			Content: "", // 显式设置空字符串，避免序列化为 null（部分 Provider 不接受 content: null）
			ToolCalls: []dto.ToolCall{
				{
					ID:   item.CallID,
					Type: "function",
					Function: dto.ToolCallFunc{
						Name:      item.Name,
						Arguments: args,
					},
				},
			},
		}, nil

	case "function_call_output":
		contentStr := extractToolOutputContent(item.Output)
		return dto.Message{
			Role:       "tool",
			ToolCallID: item.CallID,
			Content:    contentStr,
		}, nil

	case "mcp_tool_call_output":
		contentStr := extractToolOutputContent(item.Output)
		return dto.Message{
			Role:       "tool",
			ToolCallID: item.CallID,
			Content:    contentStr,
		}, nil

	case "custom_tool_call_output":
		contentStr := extractToolOutputContent(item.Output)
		return dto.Message{
			Role:       "tool",
			ToolCallID: item.CallID,
			Content:    contentStr,
		}, nil

	case "additional_tools":
		// Chat API 不支持动态添加工具，忽略该 item
		return dto.Message{}, nil

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

// normalizeArguments 确保 arguments 是有效的 JSON 对象字符串。
// OpenAI Chat API 要求 tool_calls[].function.arguments 必须是有效 JSON，
// 部分 Provider（如阿里云 DashScope）会严格校验，空字符串或无效 JSON 会报错：
// "Invalid parameter: 'tool_calls' type error"
func normalizeArguments(args string) string {
	if args == "" {
		return "{}"
	}

	// 验证是否为有效 JSON
	var obj map[string]any
	if err := json.Unmarshal([]byte(args), &obj); err != nil {
		// 不是有效 JSON，返回空对象避免上游校验失败
		return "{}"
	}

	return args
}

// isToolCall 判断 input item 是否是 tool_call 类型。
func isToolCall(typ string) bool {
	switch typ {
	case "function_call", "mcp_tool_call", "custom_tool_call":
		return true
	default:
		return false
	}
}

// isToolCallOutput 判断 input item 是否是 tool_call_output 类型。
func isToolCallOutput(typ string) bool {
	switch typ {
	case "function_call_output", "mcp_tool_call_output", "custom_tool_call_output":
		return true
	default:
		return false
	}
}
