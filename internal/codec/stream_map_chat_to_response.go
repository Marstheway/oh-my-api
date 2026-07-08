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

func intPtr(v int) *int { return &v }

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
	if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil {
		m.ensureResponseCreated(chunk, &events)
		return events, nil
	}

	delta := chunk.Choices[0].Delta

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

	if chunk.Choices[0].FinishReason != nil {
		m.ensureResponseCreated(chunk, &events)
		if m.textItemAdded {
			events = append(events,
				dto.ResponsesStreamEvent{
					Type:         "response.output_text.done",
					ItemID:       m.messageItemID,
					OutputIndex:  intPtr(m.textOutputIndex),
					ContentIndex: intPtr(0),
					Delta:        nil,
					Item:         nil,
					Response:     nil,
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
		events = append(events, dto.ResponsesStreamEvent{
			Type: "response.completed",
			Response: mustRawJSON(map[string]any{
				"id":     m.responseID,
				"object": "response",
				"status": "completed",
				"model":  m.model,
			}),
		})
	}

	return events, nil
}
