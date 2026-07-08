package codec

import (
	"encoding/json"
	"errors"
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
			outputJSON, err := json.Marshal(text)
			if err != nil {
				return fmt.Errorf("marshal tool output: %w", err)
			}
			items = append(items, dto.ResponsesInputItem{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: json.RawMessage(outputJSON),
			})

		default:
			// 处理来自 anthropic→chat 转换的 ContentBlock 数组（包含 tool_result 块、图片等多模态内容）
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
						outputJSON, err := json.Marshal(contentStr)
						if err != nil {
							return fmt.Errorf("marshal tool result output: %w", err)
						}
						items = append(items, dto.ResponsesInputItem{
							Type:   "function_call_output",
							CallID: block.ToolUseID,
							Output: json.RawMessage(outputJSON),
						})
					case "image":
						// 处理多模态图片内容
						if block.Source != nil {
							part := map[string]any{
								"type": "input_image",
							}
							if block.Source.Type == "base64" && block.Source.Data != "" {
								// base64 格式：转换为 data URL
								mediaType := block.Source.MediaType
								if mediaType == "" {
									mediaType = "image/jpeg"
								}
								part["image_url"] = fmt.Sprintf("data:%s;base64,%s", mediaType, block.Source.Data)
							} else if block.Source.Type == "url" && block.Source.Url != "" {
								// URL 格式
								part["image_url"] = block.Source.Url
							}
							parts := []map[string]any{part}
							contentJSON, err := json.Marshal(parts)
							if err != nil {
								return fmt.Errorf("marshal image content block: %w", err)
							}
							items = append(items, dto.ResponsesInputItem{
								Type:    "message",
								Role:    msg.Role,
								Content: json.RawMessage(contentJSON),
							})
						}
					case "document":
						// 处理文档内容
						if block.Source != nil {
							part := map[string]any{
								"type": "input_file",
							}
							if block.Source.Type == "base64" && block.Source.Data != "" {
								part["file_data"] = block.Source.Data
							}
							if block.Source.MediaType != "" {
								part["format"] = block.Source.MediaType
							}
							parts := []map[string]any{part}
							contentJSON, err := json.Marshal(parts)
							if err != nil {
								return fmt.Errorf("marshal document content block: %w", err)
							}
							items = append(items, dto.ResponsesInputItem{
								Type:    "message",
								Role:    msg.Role,
								Content: json.RawMessage(contentJSON),
							})
						}
					}
				}
			} else if mapBlocks, ok := msg.Content.([]any); ok {
				// 处理来自 JSON 反序列化的 []map[string]any blocks
				// 先检查是否包含多模态内容（image_url, input_audio, file, video_url）
				hasMultimodal := false
				for _, item := range mapBlocks {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					blockType, _ := m["type"].(string)
					if blockType == "image_url" || blockType == "input_audio" ||
						blockType == "file" || blockType == "video_url" {
						hasMultimodal = true
						break
					}
				}

				if hasMultimodal {
					// 使用多模态处理路径
					parts, err := convertContentToResponseParts(msg.Content)
					if err != nil {
						return fmt.Errorf("message (role=%s) content: %w", msg.Role, err)
					}
					if len(parts) > 0 {
						contentJSON, err := json.Marshal(parts)
						if err != nil {
							return fmt.Errorf("marshal multimodal content: %w", err)
						}
						items = append(items, dto.ResponsesInputItem{
							Type:    "message",
							Role:    msg.Role,
							Content: json.RawMessage(contentJSON),
						})
					}
				} else {
					// 处理 tool_result 等传统 blocks
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
							outputJSON, err := json.Marshal(contentStr)
							if err != nil {
								return fmt.Errorf("marshal map tool result output: %w", err)
							}
							items = append(items, dto.ResponsesInputItem{
								Type:   "function_call_output",
								CallID: toolUseID,
								Output: json.RawMessage(outputJSON),
							})
						default:
							if blockType == "" {
								return fmt.Errorf("content part missing type field in role=%s message", msg.Role)
							}
							return fmt.Errorf("unsupported content part type %q in role=%s message", blockType, msg.Role)
						}
					}
				}
			} else {
				// user 及其他角色统一作为 message 类型
				// 先尝试提取纯文本，如果包含多模态内容则使用多模态处理路径
				text, err := extractStringContent(msg.Content)
				if err == nil {
					// 纯文本内容
					contentJSON, err := json.Marshal(text)
					if err != nil {
						return fmt.Errorf("marshal message content: %w", err)
					}
					items = append(items, dto.ResponsesInputItem{
						Type:    "message",
						Role:    msg.Role,
						Content: json.RawMessage(contentJSON),
					})
				} else if errors.Is(err, ErrMultimodalDetected) {
					// 多模态内容，转换为 parts 数组
					parts, err := convertContentToResponseParts(msg.Content)
					if err != nil {
						return fmt.Errorf("message (role=%s) content: %w", msg.Role, err)
					}
					if len(parts) > 0 {
						contentJSON, err := json.Marshal(parts)
						if err != nil {
							return fmt.Errorf("marshal multimodal content: %w", err)
						}
						items = append(items, dto.ResponsesInputItem{
							Type:    "message",
							Role:    msg.Role,
							Content: json.RawMessage(contentJSON),
						})
					}
				} else {
					return fmt.Errorf("message (role=%s) content: %w", msg.Role, err)
				}
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
// 如果包含非文本 part（如 image_url），返回 error 以触发多模态处理路径。
func extractStringContent(content any) (string, error) {
	switch v := content.(type) {
	case string:
		return v, nil
	case nil:
		return "", nil
	case []any:
		var sb strings.Builder
		hasNonText := false
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
				hasNonText = true
			}
		}
		// 如果包含非文本内容，返回错误以触发多模态处理
		if hasNonText {
			return "", ErrMultimodalDetected
		}
		return sb.String(), nil
	default:
		return "", fmt.Errorf("unsupported content type: %T", content)
	}
}

// convertContentToResponseParts 将 Chat Completion 的多模态 content 转换为 Responses API 格式。
// 返回 input_text, input_image, input_file, input_audio, input_video 等类型的 parts。
func convertContentToResponseParts(content any) ([]map[string]any, error) {
	switch v := content.(type) {
	case string:
		if v == "" {
			return nil, nil
		}
		return []map[string]any{{"type": "input_text", "text": v}}, nil
	case nil:
		return nil, nil
	case []any:
		var parts []map[string]any
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("unexpected content block type: %T", item)
			}
			partType, _ := m["type"].(string)
			if partType == "" {
				return nil, fmt.Errorf("content part missing type field")
			}

			switch partType {
			case "text":
				text, _ := m["text"].(string)
				if text != "" {
					parts = append(parts, map[string]any{
						"type": "input_text",
						"text": text,
					})
				}
			case "image_url":
				if imageUrlObj, ok := m["image_url"].(map[string]any); ok {
					if url, ok := imageUrlObj["url"].(string); ok && url != "" {
						part := map[string]any{
							"type":      "input_image",
							"image_url": url,
						}
						if detail, ok := imageUrlObj["detail"]; ok && detail != nil {
							part["detail"] = detail
						}
						parts = append(parts, part)
					}
				}
			case "input_audio":
				if audioObj, ok := m["input_audio"].(map[string]any); ok {
					part := map[string]any{
						"type": "input_audio",
					}
					if data, ok := audioObj["data"].(string); ok {
						part["audio_data"] = data
					}
					if format, ok := audioObj["format"].(string); ok {
						part["format"] = format
					}
					parts = append(parts, part)
				}
			case "file":
				if fileObj, ok := m["file"].(map[string]any); ok {
					part := map[string]any{
						"type": "input_file",
					}
					if filename, ok := fileObj["filename"].(string); ok {
						part["filename"] = filename
					}
					if fileData, ok := fileObj["file_data"].(string); ok {
						part["file_data"] = fileData
					}
					if url, ok := fileObj["url"].(string); ok {
						part["url"] = url
					}
					parts = append(parts, part)
				}
			case "video_url":
				if videoObj, ok := m["video_url"].(map[string]any); ok {
					if url, ok := videoObj["url"].(string); ok && url != "" {
						parts = append(parts, map[string]any{
							"type":      "input_video",
							"video_url": url,
						})
					}
				}
			default:
				return nil, fmt.Errorf("unsupported content part type: %s", partType)
			}
		}
		return parts, nil
	default:
		return nil, fmt.Errorf("unsupported content type: %T", content)
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
