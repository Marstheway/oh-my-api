package codec

import (
	"fmt"
	"sort"
	"time"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

// chatToClaudeStreamMapper maps individual Chat completion chunks to Anthropic SSE events.
type chatToClaudeStreamMapper struct {
	messageID          string
	requestedModel     string
	model              string
	messageStarted     bool
	textBlockStart     bool
	thinkingBlockStart bool
	textIndex          int
	thinkingIndex      int
	nextIndex          int
	toolIndexByChunk   map[int]int
}

func newChatToClaudeStreamMapper() *chatToClaudeStreamMapper {
	return &chatToClaudeStreamMapper{
		messageID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		textIndex:        -1,
		thinkingIndex:    -1,
		toolIndexByChunk: map[int]int{},
	}
}

func (m *chatToClaudeStreamMapper) ensureMessageStart(chunk dto.ChatCompletionChunk, events *[]dto.ClaudeStreamEvent) {
	if m.messageStarted {
		return
	}
	m.messageStarted = true
	if chunk.ID != "" {
		m.messageID = chunk.ID
	}
	if m.requestedModel != "" {
		m.model = m.requestedModel
	} else if chunk.Model != "" {
		m.model = chunk.Model
	}
	*events = append(*events, dto.ClaudeStreamEvent{
		Type: "message_start",
		Message: &dto.ClaudeMessageStart{
			ID:      m.messageID,
			Type:    "message",
			Role:    "assistant",
			Model:   m.model,
			Content: []dto.ContentBlock{},
			Usage:   dto.ClaudeUsage{},
		},
	})
}

func (m *chatToClaudeStreamMapper) ensureTextBlockStart(events *[]dto.ClaudeStreamEvent) {
	if m.textBlockStart {
		return
	}
	m.textBlockStart = true
	m.textIndex = m.nextIndex
	m.nextIndex++
	*events = append(*events, dto.ClaudeStreamEvent{
		Type:         "content_block_start",
		Index:        m.textIndex,
		ContentBlock: &dto.ContentBlock{Type: "text", Text: ""},
	})
}

func (m *chatToClaudeStreamMapper) ensureThinkingBlockStart(events *[]dto.ClaudeStreamEvent) {
	if m.thinkingBlockStart {
		return
	}
	m.thinkingBlockStart = true
	m.thinkingIndex = m.nextIndex
	m.nextIndex++
	*events = append(*events, dto.ClaudeStreamEvent{
		Type:         "content_block_start",
		Index:        m.thinkingIndex,
		ContentBlock: &dto.ContentBlock{Type: "thinking"},
	})
}

func (m *chatToClaudeStreamMapper) Map(chunk dto.ChatCompletionChunk) ([]dto.ClaudeStreamEvent, error) {
	var events []dto.ClaudeStreamEvent

	if m.requestedModel != "" {
		m.model = m.requestedModel
	} else if m.model == "" && chunk.Model != "" {
		m.model = chunk.Model
	}

	if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil {
		m.ensureMessageStart(chunk, &events)
		return events, nil
	}

	delta := chunk.Choices[0].Delta

	// Handle role delta (usually first chunk)
	if delta.Role != "" {
		m.ensureMessageStart(chunk, &events)
	}

	if delta.ReasoningContent != "" {
		m.ensureMessageStart(chunk, &events)
		m.ensureThinkingBlockStart(&events)
		events = append(events, dto.ClaudeStreamEvent{
			Type:  "content_block_delta",
			Index: m.thinkingIndex,
			Delta: &dto.ClaudeDelta{Type: "thinking_delta", Thinking: delta.ReasoningContent},
		})
	}

	if delta.Content != "" {
		m.ensureMessageStart(chunk, &events)
		// 检查是否为 Data URI 格式的多模态内容
		mediaType, data, isDataURI := parseDataURI(delta.Content)
		if isDataURI {
			// 多模态内容：作为完整的 content_block_start 发送，不累积后续 delta
			idx := m.nextIndex
			m.nextIndex++
			if isImageMediaType(mediaType) {
				// 图片类型
				events = append(events, dto.ClaudeStreamEvent{
					Type:  "content_block_start",
					Index: idx,
					ContentBlock: &dto.ContentBlock{
						Type: "image",
						Source: &dto.MessageSource{
							Type:      "base64",
							MediaType: mediaType,
							Data:      data,
						},
					},
				})
			} else {
				// 文档类型
				events = append(events, dto.ClaudeStreamEvent{
					Type:  "content_block_start",
					Index: idx,
					ContentBlock: &dto.ContentBlock{
						Type: "document",
						Source: &dto.MessageSource{
							Type:      "base64",
							MediaType: mediaType,
							Data:      data,
						},
					},
				})
			}
			// 多模态内容需要 content_block_stop
			events = append(events, dto.ClaudeStreamEvent{
				Type:  "content_block_stop",
				Index: idx,
			})
		} else {
			// 普通文本内容
			m.ensureTextBlockStart(&events)
			events = append(events, dto.ClaudeStreamEvent{
				Type:  "content_block_delta",
				Index: m.textIndex,
				Delta: &dto.ClaudeDelta{Type: "text_delta", Text: delta.Content},
			})
		}
	}

	for _, tc := range delta.ToolCalls {
		m.ensureMessageStart(chunk, &events)
		idx, exists := m.toolIndexByChunk[tc.GetIndex()]
		if !exists && tc.ID != "" {
			idx = m.nextIndex
			m.nextIndex++
			m.toolIndexByChunk[tc.GetIndex()] = idx
			exists = true
			events = append(events, dto.ClaudeStreamEvent{
				Type:  "content_block_start",
				Index: idx,
				ContentBlock: &dto.ContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: map[string]any{},
				},
			})
		}
		if tc.Function.Arguments != "" {
			if !exists {
				continue
			}
			events = append(events, dto.ClaudeStreamEvent{
				Type:  "content_block_delta",
				Index: idx,
				Delta: &dto.ClaudeDelta{Type: "input_json_delta", PartialJSON: &tc.Function.Arguments},
			})
		}
	}

	if chunk.Choices[0].FinishReason != nil {
		m.ensureMessageStart(chunk, &events)
		// content_block_stop for thinking
		if m.thinkingBlockStart {
			events = append(events, dto.ClaudeStreamEvent{Type: "content_block_stop", Index: m.thinkingIndex})
		}
		// content_block_stop for text
		if m.textBlockStart {
			events = append(events, dto.ClaudeStreamEvent{Type: "content_block_stop", Index: m.textIndex})
		}
		// content_block_stop for tools
		toolIndices := make([]int, 0, len(m.toolIndexByChunk))
		for _, idx := range m.toolIndexByChunk {
			toolIndices = append(toolIndices, idx)
		}
		sort.Ints(toolIndices)
		for _, idx := range toolIndices {
			events = append(events, dto.ClaudeStreamEvent{Type: "content_block_stop", Index: idx})
		}

		stopReason := finishReasonToStopReason(*chunk.Choices[0].FinishReason)
		events = append(events, dto.ClaudeStreamEvent{
			Type:  "message_delta",
			Delta: &dto.ClaudeDelta{StopReason: stopReason},
		})
		events = append(events, dto.ClaudeStreamEvent{Type: "message_stop"})
	}

	return events, nil
}
