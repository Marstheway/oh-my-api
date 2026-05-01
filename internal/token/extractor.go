package token

import (
	"encoding/json"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

func ExtractTextFromOpenAIRequest(req *dto.ChatCompletionRequest) string {
	var texts []string

	for _, msg := range req.Messages {
		texts = append(texts, extractContent(msg.Content))
		texts = append(texts, msg.Name)
		texts = append(texts, msg.ToolCallID)
		for _, tc := range msg.ToolCalls {
			texts = append(texts, tc.Function.Name)
			texts = append(texts, tc.Function.Arguments)
		}
	}

	for _, tool := range req.Tools {
		texts = append(texts, tool.Function.Name)
		texts = append(texts, tool.Function.Description)
		if params, err := json.Marshal(tool.Function.Parameters); err == nil {
			texts = append(texts, string(params))
		}
	}

	if req.ToolChoice != nil {
		if choice, err := json.Marshal(req.ToolChoice); err == nil {
			texts = append(texts, string(choice))
		}
	}

	return joinTexts(texts)
}

func ExtractTextFromClaudeRequest(req *dto.ClaudeRequest) string {
	var texts []string

	if req.System != nil {
		texts = append(texts, extractSystem(req.System))
	}

	for _, msg := range req.Messages {
		texts = append(texts, extractClaudeContent(msg.Content))
	}

	for _, tool := range req.Tools {
		texts = append(texts, tool.Name)
		texts = append(texts, tool.Description)
		if schema, err := json.Marshal(tool.InputSchema); err == nil {
			texts = append(texts, string(schema))
		}
	}

	if req.ToolChoice != nil {
		if choice, err := json.Marshal(req.ToolChoice); err == nil {
			texts = append(texts, string(choice))
		}
	}

	return joinTexts(texts)
}

func ExtractTextFromOpenAIResponse(resp *dto.ChatCompletionResponse) string {
	var texts []string

	for _, choice := range resp.Choices {
		if choice.Message != nil {
			texts = append(texts, choice.Message.Content)

			for _, tc := range choice.Message.ToolCalls {
				texts = append(texts, tc.Function.Name)
				texts = append(texts, tc.Function.Arguments)
			}
		}
	}

	return joinTexts(texts)
}

func ExtractTextFromClaudeResponse(resp *dto.ClaudeResponse) string {
	var texts []string

	for _, block := range resp.Content {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
		if block.Type == "tool_use" {
			texts = append(texts, block.Name)
			if input, err := json.Marshal(block.Input); err == nil {
				texts = append(texts, string(input))
			}
		}
	}

	return joinTexts(texts)
}

func ExtractTextFromResponsesRequest(req *dto.ResponsesRequest) string {
	var texts []string

	// instructions
	if len(req.Instructions) > 0 {
		var s string
		if err := json.Unmarshal(req.Instructions, &s); err == nil {
			texts = append(texts, s)
		}
	}

	// input items
	if len(req.Input) > 0 {
		// try as plain string first
		var rawText string
		if err := json.Unmarshal(req.Input, &rawText); err == nil {
			texts = append(texts, rawText)
		} else {
			// try as array of items
			var items []dto.ResponsesInputItem
			if err := json.Unmarshal(req.Input, &items); err == nil {
				for _, item := range items {
					texts = append(texts, item.Name)
					texts = append(texts, item.Arguments)
					texts = append(texts, item.Output)
					// extract message content
					if len(item.Content) > 0 {
						var contentStr string
						if err := json.Unmarshal(item.Content, &contentStr); err == nil {
							texts = append(texts, contentStr)
						} else {
							var parts []dto.ResponsesContentPart
							if err := json.Unmarshal(item.Content, &parts); err == nil {
								for _, p := range parts {
									texts = append(texts, p.Text)
								}
							}
						}
					}
				}
			}
		}
	}

	// tools
	for _, tool := range req.Tools {
		texts = append(texts, tool.Name)
		texts = append(texts, tool.Description)
		if tool.Parameters != nil {
			if params, err := json.Marshal(tool.Parameters); err == nil {
				texts = append(texts, string(params))
			}
		}
	}

	// tool_choice
	if req.ToolChoice != nil {
		if choice, err := json.Marshal(req.ToolChoice); err == nil {
			texts = append(texts, string(choice))
		}
	}

	return joinTexts(texts)
}

func CountRequestTokens(req any) int {
	switch r := req.(type) {
	case *dto.ChatCompletionRequest:
		return CountTokens(ExtractTextFromOpenAIRequest(r))
	case *dto.ClaudeRequest:
		return CountTokens(ExtractTextFromClaudeRequest(r))
	case *dto.ResponsesRequest:
		return CountTokens(ExtractTextFromResponsesRequest(r))
	default:
		return 0
	}
}

func CountResponseTokens(resp any) int {
	switch r := resp.(type) {
	case *dto.ChatCompletionResponse:
		return CountTokens(ExtractTextFromOpenAIResponse(r))
	case *dto.ClaudeResponse:
		return CountTokens(ExtractTextFromClaudeResponse(r))
	default:
		return 0
	}
}

func extractContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var textParts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if typ, ok := m["type"].(string); ok && typ == "text" {
					if text, ok := m["text"].(string); ok {
						textParts = append(textParts, text)
					}
				}
			}
		}
		return joinTexts(textParts)
	default:
		if bytes, err := json.Marshal(content); err == nil {
			return string(bytes)
		}
		return ""
	}
}

func extractSystem(system any) string {
	switch v := system.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return joinTexts(parts)
	default:
		if bytes, err := json.Marshal(system); err == nil {
			return string(bytes)
		}
		return ""
	}
}

func extractClaudeContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []dto.ContentBlock:
		var parts []string
		for _, block := range v {
			if block.Type == "text" {
				parts = append(parts, block.Text)
			}
		}
		return joinTexts(parts)
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if typ, ok := m["type"].(string); ok && typ == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return joinTexts(parts)
	default:
		if bytes, err := json.Marshal(content); err == nil {
			return string(bytes)
		}
		return ""
	}
}

func joinTexts(texts []string) string {
	result := ""
	for _, t := range texts {
		if t != "" {
			result += t + "\n"
		}
	}
	return result
}

func ExtractTextFromOpenAIChunk(chunk *dto.ChatCompletionChunk) string {
	var texts []string
	for _, choice := range chunk.Choices {
		if choice.Delta != nil {
			texts = append(texts, choice.Delta.Content)
			texts = append(texts, choice.Delta.ReasoningContent)
			for _, tc := range choice.Delta.ToolCalls {
				texts = append(texts, tc.Function.Name)
				texts = append(texts, tc.Function.Arguments)
			}
		}
	}
	return joinTexts(texts)
}

func ExtractTextFromClaudeStreamEvent(event *dto.ClaudeStreamEvent) string {
	switch event.Type {
	case "content_block_delta":
		var texts []string
		if event.Delta != nil {
			if event.Delta.Text != "" {
				texts = append(texts, event.Delta.Text)
			}
			if event.Delta.Thinking != "" {
				texts = append(texts, event.Delta.Thinking)
			}
			if event.Delta.PartialJSON != "" {
				texts = append(texts, event.Delta.PartialJSON)
			}
		}
		return joinTexts(texts)
	case "content_block_start":
		if event.ContentBlock != nil {
			if event.ContentBlock.Type == "text" {
				return event.ContentBlock.Text
			}
			if event.ContentBlock.Type == "tool_use" {
				texts := []string{event.ContentBlock.Name}
				if input, err := json.Marshal(event.ContentBlock.Input); err == nil {
					texts = append(texts, string(input))
				}
				return joinTexts(texts)
			}
		}
	}
	return ""
}
