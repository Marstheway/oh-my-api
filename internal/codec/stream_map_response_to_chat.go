package codec

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

type responsesToChatStreamMapper struct {
	responseID     string
	requestedModel string
	model          string
	created        int64
	roleSent       bool
	toolIndex      map[string]int
	nextTool       int
	itemIDToKey    map[string]string
	outputIndexToKey map[int]string
	pendingArgsByItemID    map[string]string
	pendingArgsByOutputIndex map[int]string
	sawToolCall              bool
	needsReasoningSummaryBreak bool
	hasSentReasoning         bool
}

func newResponsesToChatStreamMapper(responseID, model string, created int64) *responsesToChatStreamMapper {
	if responseID == "" {
		responseID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	return &responsesToChatStreamMapper{
		responseID:              responseID,
		requestedModel:          model,
		model:                   model,
		created:                 created,
		toolIndex:               map[string]int{},
		itemIDToKey:             map[string]string{},
		outputIndexToKey:        map[int]string{},
		pendingArgsByItemID:     map[string]string{},
		pendingArgsByOutputIndex: map[int]string{},
	}
}

func (m *responsesToChatStreamMapper) chunk(delta *dto.Delta, finishReason *string) dto.ChatCompletionChunk {
	return dto.ChatCompletionChunk{
		ID:      m.responseID,
		Object:  "chat.completion.chunk",
		Created: m.created,
		Model:   m.model,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: delta, FinishReason: finishReason}},
	}
}

func (m *responsesToChatStreamMapper) emitRoleIfNeeded() []dto.ChatCompletionChunk {
	if m.roleSent {
		return nil
	}
	m.roleSent = true
	return []dto.ChatCompletionChunk{m.chunk(&dto.Delta{Role: "assistant"}, nil)}
}

func (m *responsesToChatStreamMapper) keyForEvent(event dto.ResponsesStreamEvent) string {
	if event.OutputIndex != nil {
		return fmt.Sprintf("output:%d", *event.OutputIndex)
	}
	if itemID := strings.TrimSpace(event.ItemID); itemID != "" {
		return "item:" + itemID
	}
	return ""
}

func (m *responsesToChatStreamMapper) findTool(event dto.ResponsesStreamEvent) (string, int, bool) {
	if event.OutputIndex != nil {
		if key := m.outputIndexToKey[*event.OutputIndex]; key != "" {
			if idx, ok := m.toolIndex[key]; ok {
				return key, idx, true
			}
		}
	}
	if itemID := strings.TrimSpace(event.ItemID); itemID != "" {
		if key := m.itemIDToKey[itemID]; key != "" {
			if idx, ok := m.toolIndex[key]; ok {
				return key, idx, true
			}
		}
	}
	if event.OutputIndex != nil {
		key := fmt.Sprintf("output:%d", *event.OutputIndex)
		if idx, ok := m.toolIndex[key]; ok {
			return key, idx, true
		}
	}
	if itemID := strings.TrimSpace(event.ItemID); itemID != "" {
		key := "item:" + itemID
		if idx, ok := m.toolIndex[key]; ok {
			return key, idx, true
		}
	}
	return "", 0, false
}

func (m *responsesToChatStreamMapper) ensureTool(event dto.ResponsesStreamEvent, itemID, callID string) (string, int) {
	key := m.keyForEvent(event)
	if key == "" {
		if itemID != "" {
			key = "item:" + strings.TrimSpace(itemID)
		}
	}
	if key == "" {
		return "", 0
	}

	if idx, ok := m.toolIndex[key]; ok {
		return key, idx
	}

	idx := m.nextTool
	m.nextTool++
	m.toolIndex[key] = idx

	// 绑定各种索引
	if event.OutputIndex != nil {
		m.outputIndexToKey[*event.OutputIndex] = key
	}
	if itemID := strings.TrimSpace(itemID); itemID != "" {
		m.itemIDToKey[itemID] = key
	}

	return key, idx
}

func (m *responsesToChatStreamMapper) responsesFinishReasonFromCompletedEvent(event dto.ResponsesStreamEvent) string {
	if len(event.Response) > 0 {
		var responseObj struct {
			Status            string                 `json:"status"`
			IncompleteDetails *dto.IncompleteDetails `json:"incomplete_details,omitempty"`
		}
		if err := json.Unmarshal(event.Response, &responseObj); err == nil {
			resp := &dto.ResponsesResponse{
				Status:            strings.TrimSpace(responseObj.Status),
				IncompleteDetails: responseObj.IncompleteDetails,
			}
			if mappedReason, ok := responsesFinishReasonFromStatus(resp); ok {
				return mappedReason
			}
		}
	}
	if m.sawToolCall {
		return "tool_calls"
	}
	return "stop"
}

func (m *responsesToChatStreamMapper) Map(event dto.ResponsesStreamEvent) ([]dto.ChatCompletionChunk, error) {
	switch event.Type {
	case "response.created":
		var responseObj struct {
			ID        string `json:"id"`
			Model     string `json:"model"`
			CreatedAt int64  `json:"created_at"`
		}
		if len(event.Response) > 0 {
			if err := json.Unmarshal(event.Response, &responseObj); err == nil {
				if responseObj.ID != "" {
					m.responseID = responseObj.ID
				}
				if m.requestedModel != "" {
					m.model = m.requestedModel
				} else if responseObj.Model != "" {
					m.model = responseObj.Model
				}
				if responseObj.CreatedAt != 0 {
					m.created = responseObj.CreatedAt
				}
			}
		}
		return m.emitRoleIfNeeded(), nil

		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			var delta string
			if len(event.Delta) > 0 {
				if err := json.Unmarshal(event.Delta, &delta); err != nil {
					return nil, err
			}
		}
		if delta == "" {
			return nil, nil
		}
			if m.needsReasoningSummaryBreak {
			if strings.HasPrefix(delta, "\n\n") {
				m.needsReasoningSummaryBreak = false
			} else if strings.HasPrefix(delta, "\n") {
				delta = "\n" + delta
				m.needsReasoningSummaryBreak = false
			} else {
				delta = "\n\n" + delta
				m.needsReasoningSummaryBreak = false
			}
			}
			m.hasSentReasoning = true
			chunks := m.emitRoleIfNeeded()
			chunks = append(chunks, m.chunk(&dto.Delta{ReasoningContent: delta}, nil))
			return chunks, nil

	case "response.reasoning_summary_text.done", "response.reasoning_text.done":
		if m.hasSentReasoning {
			m.needsReasoningSummaryBreak = true
		}
		return nil, nil

	case "response.output_item.added":
		var item struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
		}
		if len(event.Item) > 0 {
			if err := json.Unmarshal(event.Item, &item); err != nil {
				return nil, err
			}
		}
		if item.Type != "function_call" && item.Type != "custom_tool_call" {
			return nil, nil
		}

		name := strings.TrimSpace(item.Name)
		if name == "" {
			return nil, nil
		}
		callID := strings.TrimSpace(item.CallID)
		if callID == "" {
			callID = strings.TrimSpace(item.ID)
		}

			key, idx := m.ensureTool(event, item.ID, callID)
			if key == "" {
				return nil, nil
			}

		m.sawToolCall = true

		// 组合 arguments：先检查是否有暂存的 args
		var initialArgs string
		if event.OutputIndex != nil {
			initialArgs = m.pendingArgsByOutputIndex[*event.OutputIndex]
			delete(m.pendingArgsByOutputIndex, *event.OutputIndex)
		}
		if initialArgs == "" {
			if itID := strings.TrimSpace(item.ID); itID != "" {
				initialArgs = m.pendingArgsByItemID[itID]
				delete(m.pendingArgsByItemID, itID)
			}
		}

		chunks := m.emitRoleIfNeeded()
		chunks = append(chunks, m.chunk(&dto.Delta{ToolCalls: []dto.ToolCall{{
			Index: &idx,
			ID:    callID,
			Type:  "function",
			Function: dto.ToolCallFunc{
				Name:      name,
				Arguments: initialArgs,
			},
		}}}, nil))
		return chunks, nil

	case "response.output_text.delta":
		var delta string
		if len(event.Delta) > 0 {
			if err := json.Unmarshal(event.Delta, &delta); err != nil {
				return nil, err
			}
		}
		if delta == "" {
			return nil, nil
		}
		return []dto.ChatCompletionChunk{m.chunk(&dto.Delta{Content: delta}, nil)}, nil

	case "response.function_call_arguments.delta":
		var argsDelta string
		if len(event.Delta) > 0 {
			if err := json.Unmarshal(event.Delta, &argsDelta); err != nil {
				return nil, err
			}
		}
		if argsDelta == "" {
			return nil, nil
		}

		if _, idx, ok := m.findTool(event); ok {
			return []dto.ChatCompletionChunk{m.chunk(&dto.Delta{ToolCalls: []dto.ToolCall{{
				Index: &idx,
				Type:  "function",
				Function: dto.ToolCallFunc{
					Arguments: argsDelta,
				},
			}}}, nil)}, nil
		}

		// Tool 还不存在，暂存 delta
		if event.OutputIndex != nil {
			m.pendingArgsByOutputIndex[*event.OutputIndex] += argsDelta
		}
		if itemID := strings.TrimSpace(event.ItemID); itemID != "" {
			m.pendingArgsByItemID[itemID] += argsDelta
		}
		return nil, nil

		case "response.custom_tool_call_input.delta":
			var argsDelta string
			if len(event.Delta) > 0 {
				if err := json.Unmarshal(event.Delta, &argsDelta); err != nil {
					return nil, err
				}
			}
			if argsDelta == "" {
				return nil, nil
			}

			if _, idx, ok := m.findTool(event); ok {
				return []dto.ChatCompletionChunk{m.chunk(&dto.Delta{ToolCalls: []dto.ToolCall{{
					Index: &idx,
					Type:  "function",
					Function: dto.ToolCallFunc{
						Arguments: argsDelta,
					},
				}}}, nil)}, nil
			}

			// Tool 还不存在，暂存 delta
			if event.OutputIndex != nil {
				m.pendingArgsByOutputIndex[*event.OutputIndex] += argsDelta
			}
			if itemID := strings.TrimSpace(event.ItemID); itemID != "" {
				m.pendingArgsByItemID[itemID] += argsDelta
			}
			return nil, nil

		case "response.completed", "response.incomplete", "response.done":
			finishReason := m.responsesFinishReasonFromCompletedEvent(event)
			return []dto.ChatCompletionChunk{m.chunk(&dto.Delta{}, &finishReason)}, nil

	case "response.failed":
		var responseObj struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if len(event.Response) > 0 {
			_ = json.Unmarshal(event.Response, &responseObj)
		}
		if responseObj.Error != nil {
			return nil, fmt.Errorf("response failed [%s]: %s", responseObj.Error.Code, responseObj.Error.Message)
		}
		return nil, fmt.Errorf("response failed")
	default:
		return nil, nil
	}
}
