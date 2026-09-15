package codec

import (
	"fmt"
	"time"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

// claudeToChatStreamMapper maps individual Anthropic SSE events to Chat completion chunks.
type claudeToChatStreamMapper struct {
	responseID      string
	requestedModel  string
	model           string
	created         int64
	roleSent        bool
	finishSent      bool
	toolCallByIndex map[int]dto.ToolCall
	usage           *dto.Usage
	// usageFromDelta 仅在观察到 message_delta.usage 后为 true。
	// message_start 只会缓存 prompt 侧 token，不能单独当作最终 usage 发出。
	usageFromDelta bool
}

func newClaudeToChatStreamMapper(responseID, model string, created int64) *claudeToChatStreamMapper {
	if responseID == "" {
		responseID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	return &claudeToChatStreamMapper{
		responseID:      responseID,
		requestedModel:  model,
		model:           model,
		created:         created,
		toolCallByIndex: map[int]dto.ToolCall{},
	}
}

func (m *claudeToChatStreamMapper) chunkWithUsage(delta *dto.Delta, finishReason *string, usage *dto.Usage) dto.ChatCompletionChunk {
	return dto.ChatCompletionChunk{
		ID:      m.responseID,
		Object:  "chat.completion.chunk",
		Created: m.created,
		Model:   m.model,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: delta, FinishReason: finishReason}},
		Usage:   usage,
	}
}

// mergeClaudeUsageIntoChat 将 Claude 流式 usage 合并到 Chat Usage。
// fromDelta=false 仅写入 message_start 的 prompt 侧字段；fromDelta=true 写入 output 并标记可发射。
func mergeClaudeUsageIntoChat(dst *dto.Usage, src *dto.ClaudeUsage, fromDelta bool) {
	if dst == nil || src == nil {
		return
	}
	if fromDelta {
		dst.CompletionTokens = src.OutputTokens
		if src.InputTokens > 0 {
			dst.PromptTokens = src.InputTokens
		}
	} else {
		dst.PromptTokens = src.InputTokens
	}
	// cache_read 可表达为 PromptTokensDetails.CachedTokens；cache_creation 等无对等字段则丢弃。
	if src.CacheReadInputTokens > 0 {
		if dst.PromptTokensDetails == nil {
			dst.PromptTokensDetails = &dto.UsageDetails{}
		}
		dst.PromptTokensDetails.CachedTokens = src.CacheReadInputTokens
	}
	dst.TotalTokens = dst.PromptTokens + dst.CompletionTokens
}

func (m *claudeToChatStreamMapper) readyUsageCopy() *dto.Usage {
	if !m.usageFromDelta || m.usage == nil {
		return nil
	}
	usage := *m.usage
	if m.usage.PromptTokensDetails != nil {
		d := *m.usage.PromptTokensDetails
		usage.PromptTokensDetails = &d
	}
	if m.usage.CompletionTokensDetails != nil {
		d := *m.usage.CompletionTokensDetails
		usage.CompletionTokensDetails = &d
	}
	return &usage
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
			// 仅缓存 prompt 侧；不把 m.usage 指针非空当作“已齐”
			m.usage = &dto.Usage{}
			mergeClaudeUsageIntoChat(m.usage, &event.Message.Usage, false)
		}
		m.roleSent = true
		return []dto.ChatCompletionChunk{m.chunkWithUsage(&dto.Delta{Role: "assistant"}, nil, nil)}, nil

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
			return []dto.ChatCompletionChunk{m.chunkWithUsage(&dto.Delta{ToolCalls: []dto.ToolCall{toolCall}}, nil, nil)}, nil
		}
		return nil, nil

	case "content_block_delta":
		if event.Delta == nil {
			return nil, nil
		}
		switch event.Delta.Type {
		case "text_delta":
			return []dto.ChatCompletionChunk{m.chunkWithUsage(&dto.Delta{Content: event.Delta.Text}, nil, nil)}, nil
		case "input_json_delta":
			if event.Delta.PartialJSON != nil {
				toolCall, ok := m.toolCallByIndex[event.Index]
				if !ok {
					toolCall = dto.ToolCall{Index: &event.Index, Type: "function"}
				}
				toolCall.Function.Arguments = *event.Delta.PartialJSON
				return []dto.ChatCompletionChunk{m.chunkWithUsage(&dto.Delta{ToolCalls: []dto.ToolCall{toolCall}}, nil, nil)}, nil
			}
			return nil, nil
		case "thinking_delta":
			return []dto.ChatCompletionChunk{m.chunkWithUsage(&dto.Delta{ReasoningContent: event.Delta.Thinking}, nil, nil)}, nil
		}
		return nil, nil

	case "content_block_stop":
		return nil, nil

	case "message_delta":
		// 先处理 usage（事件顶层）；仅 message_delta.usage 才标记已齐（含 completion_tokens==0）
		if event.Usage != nil {
			if m.usage == nil {
				m.usage = &dto.Usage{}
			}
			mergeClaudeUsageIntoChat(m.usage, event.Usage, true)
			m.usageFromDelta = true
		}

		// 再处理 stop_reason；仅 usageFromDelta 时挂 usage
		if event.Delta != nil && event.Delta.StopReason != "" {
			fr := stopReasonToFinishReason(event.Delta.StopReason)
			m.finishSent = true
			return []dto.ChatCompletionChunk{m.chunkWithUsage(&dto.Delta{}, &fr, m.readyUsageCopy())}, nil
		}

		// 纯 usage-only chunk（无 stop_reason）
		if event.Usage != nil {
			if usage := m.readyUsageCopy(); usage != nil {
				return []dto.ChatCompletionChunk{{
					ID:      m.responseID,
					Object:  "chat.completion.chunk",
					Created: m.created,
					Model:   m.model,
					Choices: []dto.ChunkChoice{},
					Usage:   usage,
				}}, nil
			}
		}
		return nil, nil

	case "message_stop":
		if m.finishSent {
			return nil, nil
		}
		fr := "stop"
		m.finishSent = true
		// 与 message_delta+stop_reason 对称：已齐的 usage 挂到 finish
		return []dto.ChatCompletionChunk{m.chunkWithUsage(&dto.Delta{}, &fr, m.readyUsageCopy())}, nil

	default:
		return nil, nil
	}
}
