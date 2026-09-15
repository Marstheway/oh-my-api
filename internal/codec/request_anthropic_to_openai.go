package codec

import (
	"encoding/json"
	"fmt"

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
		TopK:        req.TopK,
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

	switch tools := req.Tools.(type) {
	case []any:
		for _, t := range tools {
			switch tool := t.(type) {
			case dto.ClaudeTool:
				out.Tools = append(out.Tools, dto.Tool{
					Type: "function",
					Function: dto.ToolFunction{
						Name:        tool.Name,
						Description: tool.Description,
						Parameters:  tool.InputSchema,
					},
				})
			case *dto.ClaudeTool:
				out.Tools = append(out.Tools, dto.Tool{
					Type: "function",
					Function: dto.ToolFunction{
						Name:        tool.Name,
						Description: tool.Description,
						Parameters:  tool.InputSchema,
					},
				})
			case map[string]any:
				// JSON-unmarshaled form
				name, _ := tool["name"].(string)
				desc, _ := tool["description"].(string)
				if name != "" && tool["type"] != "web_search_20250305" {
					out.Tools = append(out.Tools, dto.Tool{
						Type: "function",
						Function: dto.ToolFunction{
							Name:        name,
							Description: desc,
							Parameters:  tool["input_schema"],
						},
					})
				}
			}
		}
	case []dto.ClaudeTool:
		for _, tool := range tools {
			out.Tools = append(out.Tools, dto.Tool{
				Type: "function",
				Function: dto.ToolFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
				},
			})
		}
	}

	if req.ToolChoice != nil {
		out.ToolChoice = convertClaudeToolChoiceToOpenAI(req.ToolChoice)
	}

	// ServiceTier: string -> json.RawMessage
	if req.ServiceTier != "" {
		out.ServiceTier = json.RawMessage(`"` + req.ServiceTier + `"`)
	}

	// Metadata 和 Claude 特有字段处理
	out.Metadata = buildOpenAIMetadataFromClaude(req)

	return out
}

// buildOpenAIMetadataFromClaude 构建包含 Claude 特有字段的 Metadata
func buildOpenAIMetadataFromClaude(req *dto.ClaudeRequest) json.RawMessage {
	meta := make(map[string]any)

	// 如果原始请求已有 Metadata，先解析到 meta
	if len(req.Metadata) > 0 {
		if err := json.Unmarshal(req.Metadata, &meta); err != nil {
			// 解析失败时忽略，重新开始
			meta = make(map[string]any)
		}
	}

	// Thinking 存入 _thinking
	if req.Thinking != nil {
		thinkingMap := map[string]any{
			"type": req.Thinking.Type,
		}
		if req.Thinking.BudgetTokens != nil {
			thinkingMap["budget_tokens"] = *req.Thinking.BudgetTokens
		}
		if req.Thinking.Display != "" {
			thinkingMap["display"] = req.Thinking.Display
		}
		meta["_thinking"] = thinkingMap
	}

	// InferenceGeo 存入 _inference_geo
	if req.InferenceGeo != "" {
		meta["_inference_geo"] = req.InferenceGeo
	}

	// 如果 meta 为空，返回 nil
	if len(meta) == 0 {
		return nil
	}

	data, err := json.Marshal(meta)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

func convertClaudeMessageToOpenAI(msg dto.ClaudeMessage) []dto.Message {
	role := msg.Role
	if role == "" {
		role = "user"
	}

	if str, ok := msg.Content.(string); ok {
		return []dto.Message{{Role: role, Content: str}}
	}

	// 统一转换为 []dto.ContentBlock 处理
	var blocks []dto.ContentBlock
	switch v := msg.Content.(type) {
	case []dto.ContentBlock:
		blocks = v
	case []any:
		blocks = convertMapSliceToContentBlocks(v)
	default:
		return []dto.Message{{Role: role, Content: msg.Content}}
	}

	return convertClaudeContentBlocksToOpenAI(role, blocks)
}

// convertMapSliceToContentBlocks 将 []any (map[string]any) 转换为 []dto.ContentBlock
func convertMapSliceToContentBlocks(items []any) []dto.ContentBlock {
	blocks := make([]dto.ContentBlock, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		block := dto.ContentBlock{}
		if typ, _ := m["type"].(string); typ != "" {
			block.Type = typ
		} else if _, hasText := m["text"]; hasText {
			block.Type = "text"
		}
		block.Text, _ = m["text"].(string)
		block.ID, _ = m["id"].(string)
		block.Name, _ = m["name"].(string)
		if input, ok := m["input"]; ok {
			block.Input = input
		}
		block.ToolUseID, _ = m["tool_use_id"].(string)
		if content, ok := m["content"]; ok {
			block.Content = content
		}
		if source, ok := m["source"].(map[string]any); ok {
			block.Source = convertMapToMessageSource(source)
		}
		// Handle thinking block fields
		if block.Type == "thinking" {
			if thinking, ok := m["thinking"].(string); ok {
				block.Thinking = &thinking
			}
			if sig, ok := m["signature"].(string); ok {
				block.Signature = sig
			}
		}
		blocks = append(blocks, block)
	}
	return blocks
}

// convertMapToMessageSource 将 map[string]any 转换为 dto.MessageSource。
// source 字段是可选组合（base64 有 media_type/data，url 型只有 url），
// 必须用 comma-ok，缺字段不能裸断言，否则 nil interface 会 panic。
func convertMapToMessageSource(m map[string]any) *dto.MessageSource {
	if m == nil {
		return nil
	}
	src := &dto.MessageSource{}
	src.Type, _ = m["type"].(string)
	src.MediaType, _ = m["media_type"].(string)
	src.Data, _ = m["data"].(string)
	src.Url, _ = m["url"].(string)
	return src
}

// convertClaudeContentBlocksToOpenAI converts []dto.ContentBlock to OpenAI format
func convertClaudeContentBlocksToOpenAI(role string, blocks []dto.ContentBlock) []dto.Message {
	var state messageConversionState

	for _, block := range blocks {
		switch block.Type {
		case "thinking":
			// Accumulate thinking content into reasoning_content
			if block.Thinking != nil && *block.Thinking != "" {
				state.reasoningContent += *block.Thinking
			}
		case "text", "input_text":
			if block.Text != "" {
				state.contentParts = append(state.contentParts, block)
				state.textContent += block.Text
			}
		case "image":
			if media := convertClaudeImageContentBlockToOpenAI(block); media != nil {
				state.contentParts = append(state.contentParts, media)
				state.hasMediaContent = true
			}
		case "document":
			if media := convertClaudeDocumentContentBlockToOpenAI(block); media != nil {
				state.contentParts = append(state.contentParts, media)
				state.hasMediaContent = true
			}
		case "tool_use":
			if role == "assistant" {
				state.toolCalls = append(state.toolCalls, dto.ToolCall{
					ID:   block.ID,
					Type: "function",
					Function: dto.ToolCallFunc{
						Name:      block.Name,
						Arguments: marshalInput(block.Input),
					},
				})
			}
		case "tool_result":
			if role == "user" {
				state.toolMessages = append(state.toolMessages, dto.Message{
					Role:       "tool",
					ToolCallID: block.ToolUseID,
					Content:    toolResultContentToString(block.Content),
				})
			}
		}
	}

	return state.buildMessages(role)
}

// messageConversionState 收集消息转换过程中的状态
type messageConversionState struct {
	textContent      string
	reasoningContent string
	toolCalls        []dto.ToolCall
	toolMessages     []dto.Message
	contentParts     []any
	hasMediaContent  bool
}

// buildMessages 根据收集的状态构建最终的消息列表
func (s *messageConversionState) buildMessages(role string) []dto.Message {
	result := make([]dto.Message, 0, 1+len(s.toolMessages))

	if role == "assistant" {
		if s.textContent != "" || len(s.toolCalls) > 0 || s.hasMediaContent || s.reasoningContent != "" {
			var content any = s.textContent
			if s.hasMediaContent {
				content = s.contentParts
			}
			msg := dto.Message{
				Role:      "assistant",
				Content:   content,
				ToolCalls: s.toolCalls,
			}
			// Preserve reasoning_content if present
			if s.reasoningContent != "" {
				msg.ReasoningContent = &s.reasoningContent
			}
			result = append(result, msg)
		}
		return result
	}

	// User role
	if s.hasMediaContent || len(s.toolMessages) > 0 || len(s.contentParts) > 1 {
		result = append(result, dto.Message{Role: role, Content: s.contentParts})
	} else if s.textContent != "" {
		result = append(result, dto.Message{Role: role, Content: s.textContent})
	}
	result = append(result, s.toolMessages...)

	if len(result) == 0 {
		result = append(result, dto.Message{Role: role, Content: ""})
	}
	return result
}

// marshalInput 将 input 序列化为 JSON 字符串
func marshalInput(input any) string {
	if input == nil {
		return "{}"
	}
	b, err := json.Marshal(input)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// convertClaudeImageContentBlockToOpenAI converts dto.ContentBlock image to MediaContent
func convertClaudeImageContentBlockToOpenAI(block dto.ContentBlock) *dto.MediaContent {
	if block.Source == nil {
		return nil
	}

	var imageUrl string
	switch block.Source.Type {
	case "base64":
		if block.Source.MediaType != "" && block.Source.Data != "" {
			imageUrl = fmt.Sprintf("data:%s;base64,%s", block.Source.MediaType, block.Source.Data)
		}
	case "url":
		imageUrl = block.Source.Url
	}

	if imageUrl == "" {
		return nil
	}

	return &dto.MediaContent{
		Type:     "image_url",
		ImageUrl: dto.MessageImageUrl{Url: imageUrl},
	}
}

// convertClaudeDocumentContentBlockToOpenAI converts dto.ContentBlock document to MediaContent
func convertClaudeDocumentContentBlockToOpenAI(block dto.ContentBlock) *dto.MediaContent {
	if block.Source == nil || block.Source.Type != "base64" {
		return nil
	}

	if block.Source.MediaType == "" || block.Source.Data == "" {
		return nil
	}

	fileData := fmt.Sprintf("data:%s;base64,%s", block.Source.MediaType, block.Source.Data)
	return &dto.MediaContent{
		Type: "file",
		File: dto.MessageFile{FileData: fileData},
	}
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
