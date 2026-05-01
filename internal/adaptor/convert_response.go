package adaptor

import (
	"encoding/json"
	"time"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

// ConvertClaudeResponseToOpenAI 将 Anthropic 非流式响应转换为 OpenAI 格式
func ConvertClaudeResponseToOpenAI(resp *dto.ClaudeResponse) *dto.ChatCompletionResponse {
	finishReason := "stop"
	if resp.StopReason != nil {
		finishReason = stopReasonToFinishReason(*resp.StopReason)
	}

	msg := &dto.ResMessage{
		Role:    "assistant",
		Content: "",
	}

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			msg.Content += block.Text
		case "tool_use":
			args, _ := json.Marshal(block.Input)
			msg.ToolCalls = append(msg.ToolCalls, dto.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: dto.ToolCallFunc{
					Name:      block.Name,
					Arguments: string(args),
				},
			})
		}
	}

	return &dto.ChatCompletionResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []dto.Choice{
			{
				Index:        0,
				Message:      msg,
				FinishReason: &finishReason,
			},
		},
		Usage: dto.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}
}

// ConvertClaudeStreamEventToOpenAI 将单个 Anthropic SSE 事件转换为 OpenAI chunk
// 返回 nil 表示该事件不需要输出（如 message_stop、content_block_stop）
func ConvertClaudeStreamEventToOpenAI(responseID, model string, event *dto.ClaudeStreamEvent) *dto.ChatCompletionChunk {
	chunk := &dto.ChatCompletionChunk{
		ID:      responseID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{}}},
	}

	switch event.Type {
	case "message_start":
		if event.Message != nil {
			if event.Message.ID != "" {
				chunk.ID = event.Message.ID
			}
			if event.Message.Model != "" {
				chunk.Model = event.Message.Model
			}
		}
		chunk.Choices[0].Delta.Role = "assistant"
		return chunk

	case "content_block_start":
		if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
			chunk.Choices[0].Index = event.Index
			chunk.Choices[0].Delta.ToolCalls = []dto.ToolCall{
				{
					ID:   event.ContentBlock.ID,
					Type: "function",
					Function: dto.ToolCallFunc{
						Name:      event.ContentBlock.Name,
						Arguments: "",
					},
				},
			}
			return chunk
		}
		return nil

	case "content_block_delta":
		if event.Delta == nil {
			return nil
		}
		chunk.Choices[0].Index = event.Index
		switch event.Delta.Type {
		case "text_delta":
			chunk.Choices[0].Delta.Content = event.Delta.Text
			return chunk
		case "input_json_delta":
			chunk.Choices[0].Index = event.Index
			chunk.Choices[0].Delta.ToolCalls = []dto.ToolCall{
				{Function: dto.ToolCallFunc{Arguments: event.Delta.PartialJSON}},
			}
			return chunk
		case "thinking_delta":
			chunk.Choices[0].Delta.ReasoningContent = event.Delta.Thinking
			return chunk
		}
		return nil

	case "content_block_stop":
		return nil

	case "message_delta":
		if event.Delta != nil && event.Delta.StopReason != "" {
			fr := stopReasonToFinishReason(event.Delta.StopReason)
			chunk.Choices[0].FinishReason = &fr
			chunk.Choices[0].Delta = &dto.Delta{}
			return chunk
		}
		return nil

	case "message_stop":
		return nil

	default:
		return nil
	}
}
