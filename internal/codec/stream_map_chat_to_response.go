package codec

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

type chatToResponsesStreamMapper struct {
	responseID               string
	requestedModel           string
	model                    string
	responseCreated          bool
	textItemAdded            bool
	messageItemID            string
	textOutputIndex          int
	nextOutputIndex          int
	accumulatedText          string
	toolOutputIndexByTCIndex map[int]int
	toolCallIDByTCIndex      map[int]string
	toolNameByTCIndex        map[int]string
	accumulatedArgsByTCIndex map[int]string
	usage                    *dto.Usage
	pendingFinish            bool
	completedSent            bool
}

func newChatToResponsesStreamMapper(responseID, model string) *chatToResponsesStreamMapper {
	if responseID == "" {
		responseID = fmt.Sprintf("resp-%d", time.Now().UnixNano())
	}
	return &chatToResponsesStreamMapper{
		responseID:               responseID,
		requestedModel:           model,
		model:                    model,
		messageItemID:            fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		textOutputIndex:          -1,
		toolOutputIndexByTCIndex: map[int]int{},
		toolCallIDByTCIndex:      map[int]string{},
		toolNameByTCIndex:        map[int]string{},
		accumulatedArgsByTCIndex: map[int]string{},
	}
}

func mustRawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (m *chatToResponsesStreamMapper) ensureResponseCreated(chunk dto.ChatCompletionChunk, events *[]dto.ResponsesStreamEvent) {
	if m.responseCreated {
		return
	}
	if m.responseID == "" {
		if chunk.ID != "" {
			m.responseID = chunk.ID
		} else {
			m.responseID = fmt.Sprintf("resp-%d", time.Now().UnixNano())
		}
	}
	if m.requestedModel != "" {
		m.model = m.requestedModel
	} else if m.model == "" {
		m.model = chunk.Model
	}
	m.responseCreated = true
	*events = append(*events, dto.ResponsesStreamEvent{
		Type: "response.created",
		Response: mustRawJSON(map[string]any{
			"id":     m.responseID,
			"object": "response",
			"status": "in_progress",
			"model":  m.model,
		}),
	})
}

func (m *chatToResponsesStreamMapper) ensureTextItemAdded(events *[]dto.ResponsesStreamEvent) {
	if m.textItemAdded {
		return
	}
	m.textItemAdded = true
	m.textOutputIndex = m.nextOutputIndex
	m.nextOutputIndex++
	*events = append(*events,
		dto.ResponsesStreamEvent{
			Type:        "response.output_item.added",
			OutputIndex: intPtr(m.textOutputIndex),
			Item: mustRawJSON(map[string]any{
				"type":    "message",
				"id":      m.messageItemID,
				"status":  "in_progress",
				"role":    "assistant",
				"content": []any{},
			}),
		},
		dto.ResponsesStreamEvent{
			Type:         "response.content_part.added",
			ItemID:       m.messageItemID,
			OutputIndex:  intPtr(m.textOutputIndex),
			ContentIndex: intPtr(0),
			Item:         nil,
		},
	)
}

func intPtr(v int) *int          { return &v }
func stringPtr(v string) *string { return &v }

func (m *chatToResponsesStreamMapper) Map(chunk dto.ChatCompletionChunk) ([]dto.ResponsesStreamEvent, error) {
	var events []dto.ResponsesStreamEvent
	if m.responseID == "" && chunk.ID != "" {
		m.responseID = chunk.ID
	}
	if m.requestedModel != "" {
		m.model = m.requestedModel
	} else if m.model == "" && chunk.Model != "" {
		m.model = chunk.Model
	}

	// 必须先处理 usage（即使 Choices 为空）
	if chunk.Usage != nil {
		m.usage = chunk.Usage
		if m.pendingFinish && !m.completedSent {
			m.completedSent = true
			events = append(events, m.emitCompleted()...)
		}
	}

	// 纯 usage-only（无 choices）
	if len(chunk.Choices) == 0 {
		return events, nil
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	// delta 可为 nil；仅非 nil 时处理内容
	if delta != nil {
		if delta.Content != "" {
			m.ensureResponseCreated(chunk, &events)
			m.ensureTextItemAdded(&events)
			m.accumulatedText += delta.Content
			events = append(events, dto.ResponsesStreamEvent{
				Type:         "response.output_text.delta",
				ItemID:       m.messageItemID,
				OutputIndex:  intPtr(m.textOutputIndex),
				ContentIndex: intPtr(0),
				Delta:        mustRawJSON(delta.Content),
			})
		}

		for _, tc := range delta.ToolCalls {
			m.ensureResponseCreated(chunk, &events)
			if _, exists := m.toolOutputIndexByTCIndex[tc.GetIndex()]; !exists && tc.ID != "" {
				outputIdx := m.nextOutputIndex
				m.nextOutputIndex++
				m.toolOutputIndexByTCIndex[tc.GetIndex()] = outputIdx
				m.toolCallIDByTCIndex[tc.GetIndex()] = tc.ID
				m.toolNameByTCIndex[tc.GetIndex()] = tc.Function.Name
				m.accumulatedArgsByTCIndex[tc.GetIndex()] = ""
				events = append(events, dto.ResponsesStreamEvent{
					Type:        "response.output_item.added",
					OutputIndex: intPtr(outputIdx),
					Item: mustRawJSON(map[string]any{
						"type":      "function_call",
						"id":        fmt.Sprintf("fc-%s", tc.ID),
						"call_id":   tc.ID,
						"name":      tc.Function.Name,
						"arguments": "",
					}),
				})
			}
			if tc.Function.Arguments != "" {
				outputIdx, exists := m.toolOutputIndexByTCIndex[tc.GetIndex()]
				if !exists {
					continue
				}
				m.accumulatedArgsByTCIndex[tc.GetIndex()] += tc.Function.Arguments
				callID := m.toolCallIDByTCIndex[tc.GetIndex()]
				events = append(events, dto.ResponsesStreamEvent{
					Type:        "response.function_call_arguments.delta",
					ItemID:      fmt.Sprintf("fc-%s", callID),
					OutputIndex: intPtr(outputIdx),
					Delta:       mustRawJSON(tc.Function.Arguments),
				})
			}
		}
	}

	// finish_reason 不依赖 delta 是否存在
	if choice.FinishReason != nil && !m.pendingFinish && !m.completedSent {
		m.ensureResponseCreated(chunk, &events)
		if m.textItemAdded {
			events = append(events,
				dto.ResponsesStreamEvent{
					Type:         "response.output_text.done",
					Text:         stringPtr(m.accumulatedText),
					ItemID:       m.messageItemID,
					OutputIndex:  intPtr(m.textOutputIndex),
					ContentIndex: intPtr(0),
				},
				dto.ResponsesStreamEvent{
					Type:        "response.output_item.done",
					OutputIndex: intPtr(m.textOutputIndex),
					Item: mustRawJSON(map[string]any{
						"type":   "message",
						"id":     m.messageItemID,
						"status": "completed",
						"role":   "assistant",
						"content": []any{
							map[string]any{"type": "output_text", "text": m.accumulatedText},
						},
					}),
				},
			)
		}
		for tcIndex, outputIdx := range m.toolOutputIndexByTCIndex {
			callID := m.toolCallIDByTCIndex[tcIndex]
			name := m.toolNameByTCIndex[tcIndex]
			args := m.accumulatedArgsByTCIndex[tcIndex]
			events = append(events,
				dto.ResponsesStreamEvent{
					Type:        "response.function_call_arguments.done",
					ItemID:      fmt.Sprintf("fc-%s", callID),
					OutputIndex: intPtr(outputIdx),
					Item:        nil,
					Response:    nil,
					Delta:       nil,
				},
				dto.ResponsesStreamEvent{
					Type:        "response.output_item.done",
					OutputIndex: intPtr(outputIdx),
					Item: mustRawJSON(map[string]any{
						"type":      "function_call",
						"id":        fmt.Sprintf("fc-%s", callID),
						"call_id":   callID,
						"name":      name,
						"arguments": args,
					}),
				},
			)
		}
		m.pendingFinish = true
		if m.usage != nil {
			m.completedSent = true
			events = append(events, m.emitCompleted()...)
		}
	}

	return events, nil
}

// chatUsageToResponsesMap 将 Chat Usage 转为 Responses usage 对象（含细节字段）。
// 与 usageFromResponsesUsage 对称：prompt 细节 → input_tokens_details，reasoning → completion_tokens_details。
func chatUsageToResponsesMap(u *dto.Usage) map[string]any {
	if u == nil {
		return nil
	}
	total := u.TotalTokens
	if total == 0 {
		total = u.PromptTokens + u.CompletionTokens
	}
	out := map[string]any{
		"input_tokens":  u.PromptTokens,
		"output_tokens": u.CompletionTokens,
		"total_tokens":  total,
	}
	if d := u.PromptTokensDetails; d != nil {
		details := map[string]any{}
		if d.CachedTokens != 0 {
			details["cached_tokens"] = d.CachedTokens
		}
		if d.ImageTokens != 0 {
			details["image_tokens"] = d.ImageTokens
		}
		if d.AudioTokens != 0 {
			details["audio_tokens"] = d.AudioTokens
		}
		if len(details) > 0 {
			out["input_tokens_details"] = details
		}
	}
	if d := u.CompletionTokensDetails; d != nil && d.ReasoningTokens != 0 {
		out["completion_tokens_details"] = map[string]any{
			"reasoning_tokens": d.ReasoningTokens,
		}
	}
	return out
}

func (m *chatToResponsesStreamMapper) emitCompleted() []dto.ResponsesStreamEvent {
	resp := map[string]any{
		"id":     m.responseID,
		"object": "response",
		"status": "completed",
		"model":  m.model,
	}
	if usage := chatUsageToResponsesMap(m.usage); usage != nil {
		resp["usage"] = usage
	}
	return []dto.ResponsesStreamEvent{{
		Type:     "response.completed",
		Response: mustRawJSON(resp),
	}}
}

// Flush 仅在已收到 finish_reason（pendingFinish）且尚未 completed 时补发 response.completed。
// 无 finish_reason 时不合成 completed（与 Claude 路径不同：Claude Flush 会在 messageStarted 时补 end_turn）。
// 空流或未 finish 时不产出事件。幂等。
func (m *chatToResponsesStreamMapper) Flush() ([]dto.ResponsesStreamEvent, error) {
	if m.completedSent || !m.pendingFinish {
		return nil, nil
	}
	m.completedSent = true
	return m.emitCompleted(), nil
}
