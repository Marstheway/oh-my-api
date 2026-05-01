package codec

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

// convertChatToResponseRequest 将 OpenAI Chat 请求转换为 OpenAI Responses API 请求。
func convertChatToResponseRequest(req *dto.ChatCompletionRequest, upstreamModel string) (*dto.ResponsesRequest, error) {
	out := &dto.ResponsesRequest{
		Model:       upstreamModel,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}
	if req.MaxTokens != 0 {
		out.MaxOutputTokens = req.MaxTokens
	}

	if err := fillInstructionsAndInput(req.Messages, out); err != nil {
		return nil, err
	}

	if len(req.Tools) > 0 {
		out.Tools = make([]dto.ResponsesTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			out.Tools = append(out.Tools, dto.ResponsesTool{
				Type:        "function",
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			})
		}
	}

	if req.ToolChoice != nil {
		out.ToolChoice = convertToolChoiceToResponse(req.ToolChoice)
	}

	return out, nil
}

// fillInstructionsAndInput 将消息列表分离为 instructions（system/developer）和 input（其余）。
func fillInstructionsAndInput(messages []dto.Message, out *dto.ResponsesRequest) error {
	var systemTexts []string
	var items []dto.ResponsesInputItem

	for _, msg := range messages {
		switch msg.Role {
		case "system", "developer":
			text, err := extractStringContent(msg.Content)
			if err != nil {
				return fmt.Errorf("system/developer message content: %w", err)
			}
			systemTexts = append(systemTexts, text)

		case "assistant":
			// 处理来自 anthropic→chat 转换的 ContentBlock 数组（包含 tool_use 块）
			if blocks, ok := msg.Content.([]dto.ContentBlock); ok {
				for _, block := range blocks {
					switch block.Type {
					case "text":
						if block.Text != "" {
							contentJSON, err := json.Marshal(block.Text)
							if err != nil {
								return fmt.Errorf("marshal assistant text block: %w", err)
							}
							items = append(items, dto.ResponsesInputItem{
								Type:    "message",
								Role:    "assistant",
								Content: json.RawMessage(contentJSON),
							})
						}
					case "tool_use":
						argsJSON, err := json.Marshal(block.Input)
						if err != nil {
							return fmt.Errorf("marshal tool_use input: %w", err)
						}
						items = append(items, dto.ResponsesInputItem{
							Type:      "function_call",
							CallID:    block.ID,
							Name:      block.Name,
							Arguments: string(argsJSON),
						})
					}
				}
			} else if mapBlocks, ok := msg.Content.([]any); ok {
				// 处理来自 JSON 反序列化的 []map[string]any blocks（tool_use 等）
				for _, item := range mapBlocks {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					blockType, _ := m["type"].(string)
					switch blockType {
					case "text":
						text, _ := m["text"].(string)
						if text != "" {
							contentJSON, err := json.Marshal(text)
							if err != nil {
								return fmt.Errorf("marshal assistant text block (map): %w", err)
							}
							items = append(items, dto.ResponsesInputItem{
								Type:    "message",
								Role:    "assistant",
								Content: json.RawMessage(contentJSON),
							})
						}
					case "tool_use":
						argsJSON, err := json.Marshal(m["input"])
						if err != nil {
							return fmt.Errorf("marshal tool_use input (map): %w", err)
						}
						id, _ := m["id"].(string)
						name, _ := m["name"].(string)
						items = append(items, dto.ResponsesInputItem{
							Type:      "function_call",
							CallID:    id,
							Name:      name,
							Arguments: string(argsJSON),
						})
					default:
						if blockType == "" {
							return fmt.Errorf("content part missing type field in assistant message")
						}
						return fmt.Errorf("unsupported content part type %q in assistant message", blockType)
					}
				}
			} else {
				// 先处理文本内容（即使同时有 tool_calls 也应保留）
				if text, err := extractStringContent(msg.Content); err != nil {
					return fmt.Errorf("assistant message content: %w", err)
				} else if text != "" {
					contentJSON, err := json.Marshal(text)
					if err != nil {
						return fmt.Errorf("marshal assistant content: %w", err)
					}
					items = append(items, dto.ResponsesInputItem{
						Type:    "message",
						Role:    "assistant",
						Content: json.RawMessage(contentJSON),
					})
				}
				// 处理工具调用（与文本内容平级）
				for _, tc := range msg.ToolCalls {
					items = append(items, dto.ResponsesInputItem{
						Type:      "function_call",
						CallID:    tc.ID,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					})
				}
			}

		case "tool":
			text, err := extractStringContent(msg.Content)
			if err != nil {
				return fmt.Errorf("tool message content: %w", err)
			}
			items = append(items, dto.ResponsesInputItem{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: text,
			})

		default:
			// 处理来自 anthropic→chat 转换的 ContentBlock 数组（包含 tool_result 块）
			if blocks, ok := msg.Content.([]dto.ContentBlock); ok {
				for _, block := range blocks {
					switch block.Type {
					case "text":
						if block.Text != "" {
							contentJSON, err := json.Marshal(block.Text)
							if err != nil {
								return fmt.Errorf("marshal user text block: %w", err)
							}
							items = append(items, dto.ResponsesInputItem{
								Type:    "message",
								Role:    msg.Role,
								Content: json.RawMessage(contentJSON),
							})
						}
					case "tool_result":
						contentStr, _ := block.Content.(string)
						items = append(items, dto.ResponsesInputItem{
							Type:   "function_call_output",
							CallID: block.ToolUseID,
							Output: contentStr,
						})
					}
				}
			} else if mapBlocks, ok := msg.Content.([]any); ok {
				// 处理来自 JSON 反序列化的 []map[string]any blocks（tool_result 等）
				for _, item := range mapBlocks {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					blockType, _ := m["type"].(string)
					switch blockType {
					case "text":
						text, _ := m["text"].(string)
						if text != "" {
							contentJSON, err := json.Marshal(text)
							if err != nil {
								return fmt.Errorf("marshal user text block (map): %w", err)
							}
							items = append(items, dto.ResponsesInputItem{
								Type:    "message",
								Role:    msg.Role,
								Content: json.RawMessage(contentJSON),
							})
						}
					case "tool_result":
						toolUseID, _ := m["tool_use_id"].(string)
						contentStr, _ := m["content"].(string)
						items = append(items, dto.ResponsesInputItem{
							Type:   "function_call_output",
							CallID: toolUseID,
							Output: contentStr,
						})
					default:
						if blockType == "" {
							return fmt.Errorf("content part missing type field in role=%s message", msg.Role)
						}
						return fmt.Errorf("unsupported content part type %q in role=%s message", blockType, msg.Role)
					}
				}
			} else {
				// user 及其他角色统一作为 message 类型
				text, err := extractStringContent(msg.Content)
				if err != nil {
					return fmt.Errorf("message (role=%s) content: %w", msg.Role, err)
				}
				contentJSON, err := json.Marshal(text)
				if err != nil {
					return fmt.Errorf("marshal message content: %w", err)
				}
				items = append(items, dto.ResponsesInputItem{
					Type:    "message",
					Role:    msg.Role,
					Content: json.RawMessage(contentJSON),
				})
			}
		}
	}

	if len(systemTexts) > 0 {
		joined := strings.Join(systemTexts, "\n")
		instrJSON, err := json.Marshal(joined)
		if err != nil {
			return fmt.Errorf("marshal instructions: %w", err)
		}
		out.Instructions = json.RawMessage(instrJSON)
	}

	if len(items) > 0 {
		inputJSON, err := json.Marshal(items)
		if err != nil {
			return fmt.Errorf("marshal input: %w", err)
		}
		out.Input = json.RawMessage(inputJSON)
	}

	return nil
}

// extractStringContent 从消息 content 提取纯文本。
// 仅支持 string 和只含 text part 的 []any（JSON 反序列化后）。
// 遇到非文本 part（如 image_url）立即返回错误。
func extractStringContent(content any) (string, error) {
	switch v := content.(type) {
	case string:
		return v, nil
	case nil:
		return "", nil
	case []any:
		var sb strings.Builder
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return "", fmt.Errorf("unexpected content block type: %T", item)
			}
			partType, _ := m["type"].(string)
			if partType == "" {
				return "", fmt.Errorf("content part missing type field")
			}
			switch partType {
			case "text":
				text, _ := m["text"].(string)
				sb.WriteString(text)
			default:
				return "", fmt.Errorf("unsupported content part type: %s", partType)
			}
		}
		return sb.String(), nil
	default:
		return "", fmt.Errorf("unsupported content type: %T", content)
	}
}

// convertToolChoiceToResponse 将 OpenAI Chat 的 tool_choice 格式转换为 Responses API 格式。
// {"type":"function","function":{"name":"X"}} -> {"type":"function","name":"X"}
// 字符串 "none"/"auto"/"required" 直接透传。
func convertToolChoiceToResponse(choice any) any {
	switch v := choice.(type) {
	case string:
		return v
	case map[string]any:
		if v["type"] == "function" {
			if fn, ok := v["function"].(map[string]any); ok {
				return map[string]any{
					"type": "function",
					"name": fn["name"],
				}
			}
		}
		return v
	default:
		return choice
	}
}
