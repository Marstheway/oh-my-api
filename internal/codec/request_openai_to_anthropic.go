package codec

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

const (
	webSearchMaxUsesLow    = 1
	webSearchMaxUsesMedium = 5
	webSearchMaxUsesHigh   = 10
)

func convertOpenAIToAnthropicRequest(req *dto.ChatCompletionRequest, upstreamModel string, needsDeepSeekCompat bool) (*dto.ClaudeRequest, error) {
	out := &dto.ClaudeRequest{
		Model:       upstreamModel,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}

	// 1. max_tokens: max_completion_tokens 优先，两者都缺失时固定 4096
	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0 {
		out.MaxTokens = *req.MaxCompletionTokens
	} else if req.MaxTokens > 0 {
		out.MaxTokens = req.MaxTokens
	} else {
		out.MaxTokens = 4096
	}

	// 2. stop: StopParam 已是兼容 string/[]string 的 []string 类型，直接赋值
	if len(req.Stop) > 0 {
		out.StopSequences = req.Stop
	}

	// 3. tools: 转换为 Claude function tools
	if len(req.Tools) > 0 {
		claudeTools := make([]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			claudeTools = append(claudeTools, dto.ClaudeTool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: t.Function.Parameters,
			})
		}
		out.Tools = claudeTools
	}

	// 4. web search options → web_search tool
	if req.WebSearchOptions != nil {
		webSearchTool := buildWebSearchTool(req.WebSearchOptions)
		if out.Tools == nil {
			out.Tools = []any{webSearchTool}
		} else {
			out.Tools = append(out.Tools.([]any), webSearchTool)
		}
	}

	// 5. tool_choice + parallel_tool_calls
	if req.ToolChoice != nil || req.ParallelToolCalls != nil {
		out.ToolChoice = mapToolChoice(req.ToolChoice, req.ParallelToolCalls)
	}

	// 6. reasoning_effort / reasoning / THINKING
	if req.ReasoningEffort != "" {
		out.Thinking = handleReasoningEffort(req.ReasoningEffort)
	} else if len(req.Reasoning) > 0 {
		var reasoning struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.Unmarshal(req.Reasoning, &reasoning); err != nil {
			return nil, err
		}
		if reasoning.MaxTokens > 0 {
			budget := reasoning.MaxTokens
			out.Thinking = &dto.Thinking{
				Type:         "enabled",
				BudgetTokens: &budget,
			}
		}
	} else if len(req.THINKING) > 0 {
		var thinking dto.Thinking
		if err := json.Unmarshal(req.THINKING, &thinking); err == nil {
			out.Thinking = &thinking
		}
	}

	// 7. top_k
	if req.TopK != nil {
		out.TopK = req.TopK
	}

	// 8. service_tier
	if len(req.ServiceTier) > 0 {
		var tier string
		if err := json.Unmarshal(req.ServiceTier, &tier); err == nil {
			out.ServiceTier = tier
		}
	}

	// 9. metadata
	if len(req.Metadata) > 0 {
		out.Metadata = req.Metadata
	}

	// 10. messages: 预处理 + 组装
	formatMessages := preprocessOpenAIMessagesV2(req.Messages)

	var claudeMessages []dto.ClaudeMessage
	var systemMessages []map[string]any
	isFirstMessage := true

	for _, msg := range formatMessages {
		// system / developer 均归入 Anthropic system；多模态时仅保留 text 部分
		if msg.Role == "system" || msg.Role == "developer" {
			if msgIsStringContent(msg) {
				if text := msgStringContent(msg); text != "" {
					systemMessages = append(systemMessages, map[string]any{
						"type": "text",
						"text": text,
					})
				}
			} else {
				for _, part := range msgParseContent(msg) {
					if part["type"] == "text" && part["text"] != "" {
						systemMessages = append(systemMessages, map[string]any{
							"type": "text",
							"text": part["text"],
						})
					}
				}
			}
			continue
		}

		if isFirstMessage {
			isFirstMessage = false
			if msg.Role != "user" {
				claudeMessages = append(claudeMessages, dto.ClaudeMessage{
					Role: "user",
					Content: []dto.ContentBlock{
						{Type: "text", Text: "..."},
					},
				})
			}
		}

		var claudeMsg dto.ClaudeMessage
		var err error

		if msg.Role == "tool" {
			toolResult := dto.ContentBlock{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   normalizeToolResultContentForAnthropic(msg.Content),
			}
			if len(claudeMessages) > 0 && claudeMessages[len(claudeMessages)-1].Role == "user" {
				last := &claudeMessages[len(claudeMessages)-1]
				switch v := last.Content.(type) {
				case string:
					last.Content = []dto.ContentBlock{
						{Type: "text", Text: v},
						toolResult,
					}
				case []dto.ContentBlock:
					last.Content = append(v, toolResult)
				case []any:
					last.Content = append(v, toolResult)
				}
				continue
			}
			claudeMsg = dto.ClaudeMessage{
				Role:    "user",
				Content: []dto.ContentBlock{toolResult},
			}
		} else if msgIsStringContent(msg) && len(msg.ToolCalls) == 0 {
			text := msgStringContent(msg)
			if text == "" {
				text = "..."
			}
			claudeMsg = dto.ClaudeMessage{Role: msg.Role, Content: text}
		} else {
			claudeMsg, err = convertOpenAIMessageToAnthropicV2(msg, needsDeepSeekCompat)
			if err != nil {
				return nil, err
			}
		}

		claudeMessages = append(claudeMessages, claudeMsg)
	}

	// 设置累积的系统消息数组
	if len(systemMessages) > 0 {
		out.System = systemMessages
	}

	out.Messages = claudeMessages
	return out, nil
}

// mapToolChoice 将 OpenAI tool_choice + parallel_tool_calls 映射为 Anthropic 格式。
func mapToolChoice(toolChoice any, parallelToolCalls *bool) map[string]any {
	var m map[string]any

	switch v := toolChoice.(type) {
	case string:
		switch v {
		case "auto":
			m = map[string]any{"type": "auto"}
		case "required":
			m = map[string]any{"type": "any"}
		case "none":
			m = map[string]any{"type": "none"}
		}
	case map[string]any:
		if fn, ok := v["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				m = map[string]any{
					"type": "tool",
					"name": name,
				}
			}
		}
	}

	if parallelToolCalls != nil {
		if m == nil {
			m = map[string]any{"type": "auto"}
		}
		if m["type"] != "none" {
			m["disable_parallel_tool_use"] = !*parallelToolCalls
		}
	}

	return m
}

func handleReasoningEffort(effort string) *dto.Thinking {
	var budget int
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "off", "disabled":
		return &dto.Thinking{Type: "disabled"}
	case "low":
		budget = 1280
	case "medium":
		budget = 2048
	case "high":
		budget = 4096
	default:
		return nil
	}
	return &dto.Thinking{
		Type:         "enabled",
		BudgetTokens: &budget,
	}
}

func buildWebSearchTool(opts *dto.WebSearchOptions) dto.ClaudeWebSearchTool {
	tool := dto.ClaudeWebSearchTool{
		Type: "web_search_20250305",
		Name: "web_search",
	}

	switch opts.SearchContextSize {
	case "low":
		tool.MaxUses = webSearchMaxUsesLow
	case "medium":
		tool.MaxUses = webSearchMaxUsesMedium
	case "high":
		tool.MaxUses = webSearchMaxUsesHigh
	}

	if len(opts.UserLocation) > 0 {
		var locMap map[string]any
		if err := json.Unmarshal(opts.UserLocation, &locMap); err == nil {
			if approx, ok := locMap["approximate"].(map[string]any); ok {
				loc := &dto.ClaudeWebSearchUserLocation{Type: "approximate"}
				if tz, _ := approx["timezone"].(string); tz != "" {
					loc.Timezone = tz
				}
				if country, _ := approx["country"].(string); country != "" {
					loc.Country = country
				}
				if region, _ := approx["region"].(string); region != "" {
					loc.Region = region
				}
				if city, _ := approx["city"].(string); city != "" {
					loc.City = city
				}
				tool.UserLocation = loc
			}
		}
	}

	return tool
}

// preprocessOpenAIMessagesV2 按 new-api 规则预处理消息：合并连续同角色文本消息、空内容补 "..."
func preprocessOpenAIMessagesV2(msgs []dto.Message) []dto.Message {
	result := make([]dto.Message, 0, len(msgs))
	lastRole := "tool" // 初始值不等于任何角色，确保第一条不会被合并

	for i := range msgs {
		msg := msgs[i]
		if msg.Role == "" {
			msg.Role = "user"
		}

		if lastRole == msg.Role && lastRole != "tool" {
			last := &result[len(result)-1]
			if msgIsStringContent(*last) && msgIsStringContent(msg) {
				merged := strings.TrimSpace(msgStringContent(*last) + " " + msgStringContent(msg))
				last.Content = merged
				lastRole = msg.Role
				continue
			}
		}

		if msg.Content == nil || (msgIsStringContent(msg) && msgStringContent(msg) == "") {
			msg.Content = "..."
		}

		result = append(result, msg)
		lastRole = msg.Role
	}
	return result
}

func convertOpenAIMessageToAnthropicV2(msg dto.Message, needsDeepSeekCompat bool) (dto.ClaudeMessage, error) {
	if msg.Role == "tool" {
		return dto.ClaudeMessage{
			Role: "user",
			Content: []dto.ContentBlock{
				{
					Type:      "tool_result",
					ToolUseID: msg.ToolCallID,
					Content:   normalizeToolResultContentForAnthropic(msg.Content),
				},
			},
		}, nil
	}

	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		blocks := make([]dto.ContentBlock, 0, len(msg.ToolCalls)+2)

		// 处理 reasoning_content -> thinking block
		if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
			blocks = append(blocks, dto.ContentBlock{
				Type:     "thinking",
				Thinking: msg.ReasoningContent,
			})
		} else if needsDeepSeekCompat {
			// DeepSeek 需要补占位 thinking block（仅当有 tool_calls 时）
			pad := " "
			blocks = append(blocks, dto.ContentBlock{
				Type:     "thinking",
				Thinking: &pad,
			})
		}

		// content 与 tool_calls 解耦：多模态数组完整转换，避免静默丢图
		var err error
		blocks, err = appendOpenAIContentAsAnthropicBlocks(blocks, msg.Content)
		if err != nil {
			return dto.ClaudeMessage{}, err
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
		return dto.ClaudeMessage{Role: "assistant", Content: blocks}, nil
	}

	// 处理多模态内容（数组格式）
	if _, ok := msg.Content.([]any); ok {
		blocks, err := appendOpenAIContentAsAnthropicBlocks(nil, msg.Content)
		if err != nil {
			return dto.ClaudeMessage{}, err
		}
		if len(blocks) == 0 {
			return dto.ClaudeMessage{Role: msg.Role, Content: "..."}, nil
		}
		return dto.ClaudeMessage{Role: msg.Role, Content: blocks}, nil
	}

	if textContent := extractTextContentV2(msg.Content); textContent != "" {
		return dto.ClaudeMessage{Role: msg.Role, Content: textContent}, nil
	}

	return dto.ClaudeMessage{Role: msg.Role, Content: "..."}, nil
}

// appendOpenAIContentAsAnthropicBlocks 将 Chat content 转为 Anthropic ContentBlock 并追加。
// []any 走媒体转换（text/image/file）；纯文本走 string 提取。
func appendOpenAIContentAsAnthropicBlocks(blocks []dto.ContentBlock, content any) ([]dto.ContentBlock, error) {
	if contentArray, ok := content.([]any); ok {
		for _, item := range contentArray {
			contentMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			block, err := convertOpenAIMediaContentToAnthropic(contentMap)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
		return blocks, nil
	}
	if textContent := extractTextContentV2(content); textContent != "" {
		blocks = append(blocks, dto.ContentBlock{Type: "text", Text: textContent})
	}
	return blocks, nil
}

// normalizeToolResultContentForAnthropic 将 tool 消息 content 规范为 Anthropic tool_result 可用的值。
// 纯字符串直用；多模态/结构化内容整体 JSON 序列化为字符串，避免 OpenAI image_url 块原样泄漏。
func normalizeToolResultContentForAnthropic(content any) any {
	switch c := content.(type) {
	case nil:
		return ""
	case string:
		return c
	default:
		b, err := json.Marshal(c)
		if err != nil {
			slog.Warn("failed to marshal tool result content, using empty string", "error", err)
			return ""
		}
		return string(b)
	}
}

func extractTextContentV2(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var text string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if m["type"] == "text" {
					if t, ok := m["text"].(string); ok {
						text += t
					}
				}
			}
		}
		return text
	default:
		return ""
	}
}

// =============================================================================
// Message helper functions
// =============================================================================

func msgIsStringContent(msg dto.Message) bool {
	_, ok := msg.Content.(string)
	return ok
}

func msgStringContent(msg dto.Message) string {
	s, _ := msg.Content.(string)
	return s
}

func msgParseContent(msg dto.Message) []map[string]any {
	contentArray, ok := msg.Content.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(contentArray))
	for _, item := range contentArray {
		if m, ok := item.(map[string]any); ok {
			result = append(result, m)
		}
	}
	return result
}

// extractSystemText 从 system 字段（string 或 []block）提取纯文本。
// 保留用于 request_anthropic_to_openai.go 等其它文件的兼容。
func extractSystemText(system any) string {
	switch v := system.(type) {
	case string:
		return v
	case []any:
		var text string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					text += t
				}
			}
		}
		return text
	default:
		b, _ := json.Marshal(v)
		slog.Debug("unknown system type, marshalling to string")
		return string(b)
	}
}

// =============================================================================
// 以下函数保持与旧版兼容（多模态内容转换等）
// =============================================================================

// parseDataURI 解析 data URI，返回 media type 和 base64 数据
// 格式：data:[<mediatype>][;base64],<data>
func parseDataURI(dataURI string) (mediaType string, data string, isDataURI bool) {
	const prefix = "data:"
	if !strings.HasPrefix(dataURI, prefix) {
		return "", dataURI, false
	}

	// 去掉 data: 前缀
	content := dataURI[len(prefix):]

	// 查找逗号分隔符
	commaIdx := strings.Index(content, ",")
	if commaIdx == -1 {
		return "", dataURI, false
	}

	// 提取 metadata 部分
	metadata := content[:commaIdx]
	data = content[commaIdx+1:]

	// 检查是否包含 base64
	if strings.HasSuffix(metadata, ";base64") {
		mediaType = strings.TrimSuffix(metadata, ";base64")
	} else {
		mediaType = metadata
	}

	return mediaType, data, true
}

// convertOpenAIMediaContentToAnthropic 将 OpenAI 多模态内容转换为 Anthropic ContentBlock
func convertOpenAIMediaContentToAnthropic(content map[string]any) (dto.ContentBlock, error) {
	contentType, _ := content["type"].(string)

	switch contentType {
	case dto.ContentTypeText:
		text, _ := content["text"].(string)
		return dto.ContentBlock{Type: "text", Text: text}, nil

	case dto.ContentTypeImageURL:
		imageUrlData, ok := content["image_url"].(map[string]any)
		if !ok {
			return dto.ContentBlock{}, errors.New("invalid image_url format")
		}
		url, _ := imageUrlData["url"].(string)

		// 解析 data URI
		mediaType, data, isDataURI := parseDataURI(url)
		if isDataURI {
			return dto.ContentBlock{
				Type: "image",
				Source: &dto.MessageSource{
					Type:      "base64",
					MediaType: mediaType,
					Data:      data,
				},
			}, nil
		}

		// 普通 URL
		return dto.ContentBlock{
			Type: "image",
			Source: &dto.MessageSource{
				Type: "url",
				Url:  url,
			},
		}, nil

	case dto.ContentTypeInputAudio:
		return dto.ContentBlock{}, errors.New("input_audio content type is not supported by Anthropic Messages API")

	case dto.ContentTypeVideoURL:
		return dto.ContentBlock{}, errors.New("video_url content type is not supported by Anthropic Messages API")

	case dto.ContentTypeFile:
		fileData, ok := content["file"].(map[string]any)
		if !ok {
			return dto.ContentBlock{}, errors.New("invalid file format")
		}
		fileDataStr, _ := fileData["file_data"].(string)

		// 解析 data URI 提取 base64 数据
		mediaType, data, isDataURI := parseDataURI(fileDataStr)
		if isDataURI {
			return dto.ContentBlock{
				Type: "document",
				Source: &dto.MessageSource{
					Type:      "base64",
					MediaType: mediaType,
					Data:      data,
				},
			}, nil
		}

		// 如果没有 data URI，尝试直接使用 file_data 作为 base64 数据
		return dto.ContentBlock{
			Type: "document",
			Source: &dto.MessageSource{
				Type: "base64",
				Data: fileDataStr,
			},
		}, nil

	default:
		return dto.ContentBlock{}, errors.New("unknown content type: " + contentType)
	}
}
