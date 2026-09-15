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
	// max_output_tokens 映射：优先 MaxCompletionTokens（仅 >0 视为有效；显式 0 回退到 MaxTokens）
	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0 {
		out.MaxOutputTokens = *req.MaxCompletionTokens
	} else if req.MaxTokens > 0 {
		out.MaxOutputTokens = req.MaxTokens
	}
	// reasoning_effort 映射
	if req.ReasoningEffort != "" {
		out.Reasoning = &dto.ResponsesReasoning{Effort: req.ReasoningEffort}
	}
	// stream_options 透传
	if req.StreamOptions != nil {
		out.StreamOptions = req.StreamOptions
	}

	if err := fillInstructionsAndInput(req.Messages, out, NeedsDeepSeekCompat(upstreamModel)); err != nil {
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
func fillInstructionsAndInput(messages []dto.Message, out *dto.ResponsesRequest, replayReasoning bool) error {
	var systemTexts []string
	var items []dto.ResponsesInputItem
	legacyCalls := make(map[string][]string)
	legacyCallSeq := 0
	generatedCallSeq := 0
	var pendingCallIDs []string

	for _, msg := range messages {
		switch msg.Role {
		case "system", "developer":
			text, err := systemTextFromContent(msg.Content)
			if err != nil {
				return err
			}
			systemTexts = append(systemTexts, text)

		case "assistant":
			if replayReasoning && msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
				reasoningContent, err := json.Marshal([]map[string]any{
					{"type": "reasoning_text", "text": *msg.ReasoningContent},
				})
				if err != nil {
					return fmt.Errorf("marshal assistant reasoning content: %w", err)
				}
				items = append(items, dto.ResponsesInputItem{
					Type:    "reasoning",
					Content: json.RawMessage(reasoningContent),
				})
			}
			var err error
			var callIDs []string
			items, callIDs, generatedCallSeq, err = appendAssistantItems(items, msg, generatedCallSeq)
			if err != nil {
				return err
			}
			pendingCallIDs = append(pendingCallIDs, callIDs...)
			if msg.FunctionCall != nil {
				legacyCallSeq++
				callID := fmt.Sprintf("legacy_function_call_%d", legacyCallSeq)
				items = append(items, dto.ResponsesInputItem{
					Type:      "function_call",
					CallID:    callID,
					Name:      msg.FunctionCall.Name,
					Arguments: normalizeArguments(msg.FunctionCall.Arguments),
				})
				legacyCalls[msg.FunctionCall.Name] = append(legacyCalls[msg.FunctionCall.Name], callID)
			}

		case "tool":
			var err error
			callID := msg.ToolCallID
			if callID == "" && len(pendingCallIDs) > 0 {
				callID = pendingCallIDs[0]
				pendingCallIDs = pendingCallIDs[1:]
			} else if callID != "" {
				pendingCallIDs = removePendingCallID(pendingCallIDs, callID)
			}
			msg.ToolCallID = callID
			items, err = appendToolItems(items, msg)
			if err != nil {
				return err
			}

		case "function":
			var err error
			items, err = appendLegacyFunctionOutput(items, msg, legacyCalls)
			if err != nil {
				return err
			}

		default:
			var err error
			items, err = appendUserItems(items, msg)
			if err != nil {
				return err
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

// systemTextFromContent 提取 system/developer 文本；多模态时仅保留 text 部分（不失败）。
func systemTextFromContent(content any) (string, error) {
	text, err := extractStringContent(content)
	if err == nil || errors.Is(err, ErrMultimodalDetected) {
		// ErrMultimodalDetected 时 extractStringContent 仍返回已收集的文本
		return text, nil
	}
	return "", fmt.Errorf("system/developer message content: %w", err)
}

// appendAssistantItems 将 assistant 消息转为 Responses input items。
// content 与 msg.ToolCalls 解耦：无论 content 形态如何，末尾始终追加 ToolCalls。
func appendAssistantItems(items []dto.ResponsesInputItem, msg dto.Message, generatedCallSeq int) ([]dto.ResponsesInputItem, []string, int, error) {
	var err error
	switch content := msg.Content.(type) {
	case []dto.ContentBlock:
		items, err = appendAssistantContentBlocks(items, content)
	case []any:
		items, err = appendAssistantMapBlocks(items, content)
	default:
		items, err = appendAssistantPlainOrMultimodal(items, msg.Content)
	}
	if err != nil {
		return nil, nil, generatedCallSeq, err
	}
	items, callIDs, generatedCallSeq := appendFunctionCalls(items, msg.ToolCalls, generatedCallSeq)
	return items, callIDs, generatedCallSeq, nil
}

func appendAssistantContentBlocks(items []dto.ResponsesInputItem, blocks []dto.ContentBlock) ([]dto.ResponsesInputItem, error) {
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text == "" {
				continue
			}
			contentJSON, err := json.Marshal(block.Text)
			if err != nil {
				return nil, fmt.Errorf("marshal assistant text block: %w", err)
			}
			items = append(items, dto.ResponsesInputItem{
				Type:    "message",
				Role:    "assistant",
				Content: json.RawMessage(contentJSON),
			})
		case "tool_use":
			item, err := functionCallFromToolUse(block.ID, block.Name, block.Input)
			if err != nil {
				return nil, fmt.Errorf("marshal tool_use input: %w", err)
			}
			items = append(items, item)
		case "image", "document":
			// Responses API 不允许 assistant 消息携带 input_image/input_file 等 part
			//（assistant content 仅支持 output_text/output_audio），且 assistant 的图片
			// 并非给模型的输入，这里静默丢弃以保持请求合法（与 727e86a^ 行为一致）。
		}
	}
	return items, nil
}

// appendAssistantMapBlocks 处理 []any content。
// 纯文本路径保持 text/tool_use 交错顺序；多模态路径将 tool_use 提出为 function_call，
// 仅保留 text 合并为一条 output_text message（assistant 不允许 input_* part，多模态块丢弃）。
func appendAssistantMapBlocks(items []dto.ResponsesInputItem, mapBlocks []any) ([]dto.ResponsesInputItem, error) {
	if mapBlocksHaveMultimodal(mapBlocks) {
		var textBlocks []any
		for _, item := range mapBlocks {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := m["type"].(string)
			switch blockType {
			case "tool_use":
				fc, err := functionCallFromMapToolUse(m)
				if err != nil {
					return nil, err
				}
				items = append(items, fc)
			case "text":
				textBlocks = append(textBlocks, item)
			case "image_url", "input_audio", "file", "video_url":
				// Responses API 不允许 assistant 消息携带 input_image 等 part，
				// 与 assistant ContentBlock 路径一致：丢弃多模态块，仅保留文本。
			case "":
				return nil, fmt.Errorf("content part missing type field in assistant message")
			default:
				return nil, fmt.Errorf("unsupported content part type %q in assistant message", blockType)
			}
		}
		parts, err := convertContentToResponsePartsWithTextType(textBlocks, "output_text")
		if err != nil {
			return nil, fmt.Errorf("assistant message content: %w", err)
		}
		return appendMessageWithParts(items, "assistant", parts)
	}

	// 纯文本 + tool_use：按出现顺序交错输出（既有 wire format）
	for _, item := range mapBlocks {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := m["type"].(string)
		switch blockType {
		case "text":
			text, _ := m["text"].(string)
			if text == "" {
				continue
			}
			contentJSON, err := json.Marshal(text)
			if err != nil {
				return nil, fmt.Errorf("marshal assistant text block (map): %w", err)
			}
			items = append(items, dto.ResponsesInputItem{
				Type:    "message",
				Role:    "assistant",
				Content: json.RawMessage(contentJSON),
			})
		case "tool_use":
			fc, err := functionCallFromMapToolUse(m)
			if err != nil {
				return nil, err
			}
			items = append(items, fc)
		case "":
			return nil, fmt.Errorf("content part missing type field in assistant message")
		default:
			return nil, fmt.Errorf("unsupported content part type %q in assistant message", blockType)
		}
	}
	return items, nil
}

func appendAssistantPlainOrMultimodal(items []dto.ResponsesInputItem, content any) ([]dto.ResponsesInputItem, error) {
	text, err := extractStringContent(content)
	if err == nil {
		if text == "" {
			return items, nil
		}
		contentJSON, err := json.Marshal(text)
		if err != nil {
			return nil, fmt.Errorf("marshal assistant content: %w", err)
		}
		return append(items, dto.ResponsesInputItem{
			Type:    "message",
			Role:    "assistant",
			Content: json.RawMessage(contentJSON),
		}), nil
	}
	if !errors.Is(err, ErrMultimodalDetected) {
		return nil, fmt.Errorf("assistant message content: %w", err)
	}
	parts, err := convertContentToResponsePartsWithTextType(content, "output_text")
	if err != nil {
		return nil, fmt.Errorf("assistant message content: %w", err)
	}
	return appendMessageWithParts(items, "assistant", parts)
}

// appendToolItems 将 tool 消息转为 function_call_output。
// content 可能含多模态或结构化数据：string 直用，其余整体 JSON 序列化为字符串（与 new-api 对齐）。
// 缺少 call_id 时降级为 user 消息，避免上游校验失败。
func appendToolItems(items []dto.ResponsesInputItem, msg dto.Message) ([]dto.ResponsesInputItem, error) {
	output, err := stringifyToolOutput(msg.Content)
	if err != nil {
		return nil, err
	}

	if msg.ToolCallID == "" {
		fallbackJSON, err := json.Marshal("[tool_output_missing_call_id] " + output)
		if err != nil {
			return nil, fmt.Errorf("marshal tool output fallback: %w", err)
		}
		return append(items, dto.ResponsesInputItem{
			Type:    "message",
			Role:    "user",
			Content: json.RawMessage(fallbackJSON),
		}), nil
	}

	outputJSON, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("marshal tool output: %w", err)
	}
	return append(items, dto.ResponsesInputItem{
		Type:   "function_call_output",
		CallID: msg.ToolCallID,
		Output: json.RawMessage(outputJSON),
	}), nil
}

func appendLegacyFunctionOutput(items []dto.ResponsesInputItem, msg dto.Message, calls map[string][]string) ([]dto.ResponsesInputItem, error) {
	if msg.Name == "" {
		return nil, fmt.Errorf("legacy function output missing name")
	}
	callIDs := calls[msg.Name]
	if len(callIDs) == 0 {
		return nil, fmt.Errorf("legacy function output %q has no matching call", msg.Name)
	}
	if len(callIDs) > 1 {
		return nil, fmt.Errorf("legacy function output %q matches multiple pending calls", msg.Name)
	}

	output, err := stringifyToolOutput(msg.Content)
	if err != nil {
		return nil, err
	}
	delete(calls, msg.Name)
	outputJSON, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("marshal legacy function output: %w", err)
	}
	return append(items, dto.ResponsesInputItem{
		Type:   "function_call_output",
		CallID: callIDs[0],
		Output: json.RawMessage(outputJSON),
	}), nil
}

func removePendingCallID(callIDs []string, target string) []string {
	for i, callID := range callIDs {
		if callID == target {
			return append(callIDs[:i], callIDs[i+1:]...)
		}
	}
	return callIDs
}

func stringifyToolOutput(content any) (string, error) {
	switch value := content.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	default:
		body, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("marshal tool output: %w", err)
		}
		return string(body), nil
	}
}

// appendUserItems 处理 user 及其他角色（含 tool_result / 多模态 ContentBlock）。
func appendUserItems(items []dto.ResponsesInputItem, msg dto.Message) ([]dto.ResponsesInputItem, error) {
	switch content := msg.Content.(type) {
	case []dto.ContentBlock:
		return appendUserContentBlocks(items, msg.Role, content)
	case []any:
		return appendUserMapBlocks(items, msg.Role, content)
	default:
		return appendUserPlainOrMultimodal(items, msg.Role, msg.Content)
	}
}

func appendUserContentBlocks(items []dto.ResponsesInputItem, role string, blocks []dto.ContentBlock) ([]dto.ResponsesInputItem, error) {
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text == "" {
				continue
			}
			contentJSON, err := json.Marshal(block.Text)
			if err != nil {
				return nil, fmt.Errorf("marshal user text block: %w", err)
			}
			items = append(items, dto.ResponsesInputItem{
				Type:    "message",
				Role:    role,
				Content: json.RawMessage(contentJSON),
			})
		case "tool_result":
			contentStr, _ := block.Content.(string)
			outputJSON, err := json.Marshal(contentStr)
			if err != nil {
				return nil, fmt.Errorf("marshal tool result output: %w", err)
			}
			items = append(items, dto.ResponsesInputItem{
				Type:   "function_call_output",
				CallID: block.ToolUseID,
				Output: json.RawMessage(outputJSON),
			})
		case "image":
			item, ok, err := messageItemFromImageContentBlock(role, block)
			if err != nil {
				return nil, err
			}
			if ok {
				items = append(items, item)
			}
		case "document":
			item, ok, err := messageItemFromDocumentContentBlock(role, block)
			if err != nil {
				return nil, err
			}
			if ok {
				items = append(items, item)
			}
		}
	}
	return items, nil
}

// appendUserMapBlocks 处理 user 侧 []any content。
// 纯文本路径保持 text/tool_result 交错顺序；多模态路径合并为 input_* parts。
func appendUserMapBlocks(items []dto.ResponsesInputItem, role string, mapBlocks []any) ([]dto.ResponsesInputItem, error) {
	if mapBlocksHaveMultimodal(mapBlocks) {
		parts, err := convertContentToResponsePartsWithTextType(mapBlocks, "input_text")
		if err != nil {
			return nil, fmt.Errorf("message (role=%s) content: %w", role, err)
		}
		return appendMessageWithParts(items, role, parts)
	}

	for _, item := range mapBlocks {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := m["type"].(string)
		switch blockType {
		case "text":
			text, _ := m["text"].(string)
			if text == "" {
				continue
			}
			contentJSON, err := json.Marshal(text)
			if err != nil {
				return nil, fmt.Errorf("marshal user text block (map): %w", err)
			}
			items = append(items, dto.ResponsesInputItem{
				Type:    "message",
				Role:    role,
				Content: json.RawMessage(contentJSON),
			})
		case "tool_result":
			toolUseID, _ := m["tool_use_id"].(string)
			contentStr, _ := m["content"].(string)
			outputJSON, err := json.Marshal(contentStr)
			if err != nil {
				return nil, fmt.Errorf("marshal map tool result output: %w", err)
			}
			items = append(items, dto.ResponsesInputItem{
				Type:   "function_call_output",
				CallID: toolUseID,
				Output: json.RawMessage(outputJSON),
			})
		case "":
			return nil, fmt.Errorf("content part missing type field in role=%s message", role)
		default:
			return nil, fmt.Errorf("unsupported content part type %q in role=%s message", blockType, role)
		}
	}
	return items, nil
}

func appendUserPlainOrMultimodal(items []dto.ResponsesInputItem, role string, content any) ([]dto.ResponsesInputItem, error) {
	text, err := extractStringContent(content)
	if err == nil {
		contentJSON, err := json.Marshal(text)
		if err != nil {
			return nil, fmt.Errorf("marshal message content: %w", err)
		}
		return append(items, dto.ResponsesInputItem{
			Type:    "message",
			Role:    role,
			Content: json.RawMessage(contentJSON),
		}), nil
	}
	if !errors.Is(err, ErrMultimodalDetected) {
		return nil, fmt.Errorf("message (role=%s) content: %w", role, err)
	}
	parts, err := convertContentToResponsePartsWithTextType(content, "input_text")
	if err != nil {
		return nil, fmt.Errorf("message (role=%s) content: %w", role, err)
	}
	return appendMessageWithParts(items, role, parts)
}

func appendMessageWithParts(items []dto.ResponsesInputItem, role string, parts []map[string]any) ([]dto.ResponsesInputItem, error) {
	if len(parts) == 0 {
		return items, nil
	}
	contentJSON, err := json.Marshal(parts)
	if err != nil {
		return nil, fmt.Errorf("marshal multimodal content: %w", err)
	}
	return append(items, dto.ResponsesInputItem{
		Type:    "message",
		Role:    role,
		Content: json.RawMessage(contentJSON),
	}), nil
}

func appendFunctionCalls(items []dto.ResponsesInputItem, toolCalls []dto.ToolCall, generatedCallSeq int) ([]dto.ResponsesInputItem, []string, int) {
	callIDs := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		callID := tc.ID
		if callID == "" {
			generatedCallSeq++
			callID = fmt.Sprintf("generated_tool_call_%d", generatedCallSeq)
		}
		items = append(items, dto.ResponsesInputItem{
			Type:      "function_call",
			CallID:    callID,
			Name:      tc.Function.Name,
			Arguments: normalizeArguments(tc.Function.Arguments),
		})
		callIDs = append(callIDs, callID)
	}
	return items, callIDs, generatedCallSeq
}

func functionCallFromToolUse(id, name string, input any) (dto.ResponsesInputItem, error) {
	argsJSON, err := json.Marshal(input)
	if err != nil {
		return dto.ResponsesInputItem{}, err
	}
	return dto.ResponsesInputItem{
		Type:      "function_call",
		CallID:    id,
		Name:      name,
		Arguments: string(argsJSON),
	}, nil
}

func functionCallFromMapToolUse(m map[string]any) (dto.ResponsesInputItem, error) {
	id, _ := m["id"].(string)
	name, _ := m["name"].(string)
	item, err := functionCallFromToolUse(id, name, m["input"])
	if err != nil {
		return dto.ResponsesInputItem{}, fmt.Errorf("marshal tool_use input (map): %w", err)
	}
	return item, nil
}

// messageItemFromImageContentBlock 将 Anthropic 风格 image ContentBlock 转为 Responses message item。
// 第二个返回值表示是否生成了有效 item（无 Source 时为 false）。
func messageItemFromImageContentBlock(role string, block dto.ContentBlock) (dto.ResponsesInputItem, bool, error) {
	if block.Source == nil {
		return dto.ResponsesInputItem{}, false, nil
	}
	part := map[string]any{"type": "input_image"}
	if block.Source.Type == "base64" && block.Source.Data != "" {
		mediaType := block.Source.MediaType
		if mediaType == "" {
			mediaType = "image/jpeg"
		}
		part["image_url"] = fmt.Sprintf("data:%s;base64,%s", mediaType, block.Source.Data)
	} else if block.Source.Type == "url" && block.Source.Url != "" {
		part["image_url"] = block.Source.Url
	} else {
		return dto.ResponsesInputItem{}, false, nil
	}
	contentJSON, err := json.Marshal([]map[string]any{part})
	if err != nil {
		return dto.ResponsesInputItem{}, false, fmt.Errorf("marshal image content block: %w", err)
	}
	return dto.ResponsesInputItem{
		Type:    "message",
		Role:    role,
		Content: json.RawMessage(contentJSON),
	}, true, nil
}

func messageItemFromDocumentContentBlock(role string, block dto.ContentBlock) (dto.ResponsesInputItem, bool, error) {
	if block.Source == nil {
		return dto.ResponsesInputItem{}, false, nil
	}
	part := map[string]any{"type": "input_file"}
	if block.Source.Type == "base64" && block.Source.Data != "" {
		part["file_data"] = block.Source.Data
	}
	if block.Source.MediaType != "" {
		part["format"] = block.Source.MediaType
	}
	contentJSON, err := json.Marshal([]map[string]any{part})
	if err != nil {
		return dto.ResponsesInputItem{}, false, fmt.Errorf("marshal document content block: %w", err)
	}
	return dto.ResponsesInputItem{
		Type:    "message",
		Role:    role,
		Content: json.RawMessage(contentJSON),
	}, true, nil
}

func mapBlocksHaveMultimodal(blocks []any) bool {
	for _, item := range blocks {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := m["type"].(string)
		if isMultimodalPartType(blockType) {
			return true
		}
	}
	return false
}

func isMultimodalPartType(blockType string) bool {
	switch blockType {
	case "image_url", "input_audio", "file", "video_url":
		return true
	default:
		return false
	}
}

// extractStringContent 从消息 content 提取纯文本。
// 仅支持 string 和 []any（JSON 反序列化后）。
// 若包含非文本 part（如 image_url），返回已收集的文本 + ErrMultimodalDetected，
// 便于 system 降级直接使用文本，其它路径再走多模态转换。
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
		if hasNonText {
			return sb.String(), ErrMultimodalDetected
		}
		return sb.String(), nil
	default:
		return "", fmt.Errorf("unsupported content type: %T", content)
	}
}

// convertContentToResponseParts 将 Chat Completion 的多模态 content 转换为 Responses API 格式。
// 返回 input_text, input_image, input_file, input_audio, input_video 等类型的 parts。
func convertContentToResponseParts(content any) ([]map[string]any, error) {
	return convertContentToResponsePartsWithTextType(content, "input_text")
}

func convertContentToResponsePartsWithTextType(content any, textType string) ([]map[string]any, error) {
	switch v := content.(type) {
	case string:
		if v == "" {
			return nil, nil
		}
		return []map[string]any{{"type": textType, "text": v}}, nil
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
						"type": textType,
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
