package codec

import (
	"fmt"
	"time"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

// claudeToChatStreamMapper maps individual Anthropic SSE events to Chat completion chunks.
type claudeToChatStreamMapper struct {
	responseID     string
	requestedModel string
	model          string
	created        int64
	roleSent       bool
	finishSent     bool
	toolCallByIndex map[int]dto.ToolCall
}

func newClaudeToChatStreamMapper(responseID, model string, created int64) *claudeToChatStreamMapper {
	if responseID == "" {
		responseID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	return &claudeToChatStreamMapper{
		responseID:     responseID,
		requestedModel: model,
		model:          model,
		created:        created,
		toolCallByIndex: map[int]dto.ToolCall{},
	}
}

func (m *claudeToChatStreamMapper) chunk(delta *dto.Delta, finishReason *string) dto.ChatCompletionChunk {
	return dto.ChatCompletionChunk{
		ID:      m.responseID,
		Object:  "chat.completion.chunk",
		Created: m.created,
		Model:   m.model,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: delta, FinishReason: finishReason}},
	}
}

func (m *claudeToChatStreamMapper) Map(event dto.ClaudeStreamEvent) ([]dto.ChatCompletionChunk, error) {
	switch event.Type {
	case "message_start":
		if event.Message != nil {
			if event.Message.ID != "" {
				m.responseID = event.Message.ID
			}
			if m.requestedModel != "" {
				m.model = m.requestedModel
			} else if event.Message.Model != "" {
				m.model = event.Message.Model
			}
		}
		m.roleSent = true
		return []dto.ChatCompletionChunk{m.chunk(&dto.Delta{Role: "assistant"}, nil)}, nil

	case "content_block_start":
		if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
			toolCall := dto.ToolCall{
				Index: &event.Index,
				ID:    event.ContentBlock.ID,
				Type:  "function",
				Function: dto.ToolCallFunc{
					Name:      event.ContentBlock.Name,
					Arguments: "",
				},
			}
			m.toolCallByIndex[event.Index] = toolCall
			return []dto.ChatCompletionChunk{m.chunk(&dto.Delta{ToolCalls: []dto.ToolCall{toolCall}}, nil)}, nil
		}
		return nil, nil

	case "content_block_delta":
		if event.Delta == nil {
			return nil, nil
		}
		switch event.Delta.Type {
		case "text_delta":
			return []dto.ChatCompletionChunk{m.chunk(&dto.Delta{Content: event.Delta.Text}, nil)}, nil
		case "input_json_delta":
			if event.Delta.PartialJSON != nil {
				toolCall, ok := m.toolCallByIndex[event.Index]
				if !ok {
					toolCall = dto.ToolCall{Index: &event.Index, Type: "function"}
				}
				toolCall.Function.Arguments = *event.Delta.PartialJSON
				return []dto.ChatCompletionChunk{m.chunk(&dto.Delta{ToolCalls: []dto.ToolCall{toolCall}}, nil)}, nil
			}
			return nil, nil
		case "thinking_delta":
			return []dto.ChatCompletionChunk{m.chunk(&dto.Delta{ReasoningContent: event.Delta.Thinking}, nil)}, nil
		}
		return nil, nil

	case "content_block_stop":
		return nil, nil

	case "message_delta":
		if event.Delta != nil && event.Delta.StopReason != "" {
			fr := stopReasonToFinishReason(event.Delta.StopReason)
			m.finishSent = true
			return []dto.ChatCompletionChunk{m.chunk(&dto.Delta{}, &fr)}, nil
		}
		return nil, nil

	case "message_stop":
		if m.finishSent {
			return nil, nil
		}
		fr := "stop"
		m.finishSent = true
		return []dto.ChatCompletionChunk{m.chunk(&dto.Delta{}, &fr)}, nil

	default:
		return nil, nil
	}
}
