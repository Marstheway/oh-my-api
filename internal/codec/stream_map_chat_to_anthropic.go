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
	usage              *dto.ClaudeUsage
	pendingStopReason  string
	stopSent           bool
}

func newChatToClaudeStreamMapper(requestedModel string) *chatToClaudeStreamMapper {
	return &chatToClaudeStreamMapper{
		messageID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		requestedModel:   requestedModel,
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

// closeOpenContentBlocks emits content_block_stop for any open blocks and clears open flags.
func (m *chatToClaudeStreamMapper) closeOpenContentBlocks() []dto.ClaudeStreamEvent {
	var events []dto.ClaudeStreamEvent
	if m.thinkingBlockStart {
		events = append(events, dto.ClaudeStreamEvent{Type: "content_block_stop", Index: m.thinkingIndex})
		m.thinkingBlockStart = false
	}
	if m.textBlockStart {
		events = append(events, dto.ClaudeStreamEvent{Type: "content_block_stop", Index: m.textIndex})
		m.textBlockStart = false
	}
	toolIndices := make([]int, 0, len(m.toolIndexByChunk))
	for _, idx := range m.toolIndexByChunk {
		toolIndices = append(toolIndices, idx)
	}
	sort.Ints(toolIndices)
	for _, idx := range toolIndices {
		events = append(events, dto.ClaudeStreamEvent{Type: "content_block_stop", Index: idx})
	}
	// Prevent double-close on a subsequent finish/flush.
	m.toolIndexByChunk = map[int]int{}
	return events
}

func (m *chatToClaudeStreamMapper) Map(chunk dto.ChatCompletionChunk) ([]dto.ClaudeStreamEvent, error) {
	var events []dto.ClaudeStreamEvent

	if m.requestedModel != "" {
		m.model = m.requestedModel
	} else if m.model == "" && chunk.Model != "" {
		m.model = chunk.Model
	}

	// 必须先处理 usage（即使 Choices 为空）
	if chunk.Usage != nil {
		if m.usage == nil {
			m.usage = &dto.ClaudeUsage{}
		}
		m.usage.InputTokens = chunk.Usage.PromptTokens
		m.usage.OutputTokens = chunk.Usage.CompletionTokens
		// usage-only：已有 pending finish 则立即收尾
		if m.pendingStopReason != "" && !m.stopSent {
			m.stopSent = true
			events = append(events, m.emitStopWithUsage()...)
			return events, nil
		}
	}

	// 纯 usage-only（无 choices）
	if len(chunk.Choices) == 0 {
		return events, nil
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	// delta 可为 nil（部分上游 finish 省略 delta）；仅在非 nil 时处理内容
	if delta != nil {
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
			mediaType, data, isDataURI := parseDataURI(delta.Content)
			if isDataURI {
				idx := m.nextIndex
				m.nextIndex++
				if isImageMediaType(mediaType) {
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
				events = append(events, dto.ClaudeStreamEvent{
					Type:  "content_block_stop",
					Index: idx,
				})
			} else {
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
	}

	// finish_reason 不依赖 delta 是否存在
	if choice.FinishReason != nil && m.pendingStopReason == "" && !m.stopSent {
		m.ensureMessageStart(chunk, &events)
		events = append(events, m.closeOpenContentBlocks()...)
		m.pendingStopReason = finishReasonToStopReason(*choice.FinishReason)
		// usage 已齐则立即收尾；否则 pending，等 usage-only 或 Flush
		if m.usage != nil {
			m.stopSent = true
			events = append(events, m.emitStopWithUsage()...)
		}
	}

	return events, nil
}

func (m *chatToClaudeStreamMapper) emitStopWithUsage() []dto.ClaudeStreamEvent {
	var events []dto.ClaudeStreamEvent
	delta := &dto.ClaudeDelta{StopReason: m.pendingStopReason}
	if m.usage != nil {
		events = append(events, dto.ClaudeStreamEvent{
			Type:  "message_delta",
			Delta: delta,
			Usage: &dto.ClaudeUsage{
				InputTokens:  m.usage.InputTokens,
				OutputTokens: m.usage.OutputTokens,
			},
		})
	} else {
		events = append(events, dto.ClaudeStreamEvent{
			Type:  "message_delta",
			Delta: delta,
		})
	}
	events = append(events, dto.ClaudeStreamEvent{Type: "message_stop"})
	return events
}

// Flush 在流结束时调用：仅在已 pending finish 时补发终态；
// 若 message 已开始但从未 finish，先关闭打开中的 content_block 再收尾；
// 空流（从未 message_start）不产出事件。幂等。
func (m *chatToClaudeStreamMapper) Flush() ([]dto.ClaudeStreamEvent, error) {
	if m.stopSent {
		return nil, nil
	}
	if m.pendingStopReason != "" {
		m.stopSent = true
		return m.emitStopWithUsage(), nil
	}
	if !m.messageStarted {
		return nil, nil
	}
	// 上游中断：补齐 content_block_stop，再发默认 end_turn 收尾
	var events []dto.ClaudeStreamEvent
	events = append(events, m.closeOpenContentBlocks()...)
	m.pendingStopReason = "end_turn"
	m.stopSent = true
	events = append(events, m.emitStopWithUsage()...)
	return events, nil
}
