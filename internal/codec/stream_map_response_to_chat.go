package codec

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

type responsesToChatStreamMapper struct {
	responseID    string
	model         string
	created       int64
	roleSent      bool
	toolIndex     map[string]int
	nextTool      int
	finishReason  string
}

func newResponsesToChatStreamMapper(responseID, model string, created int64) *responsesToChatStreamMapper {
	if responseID == "" {
		responseID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	return &responsesToChatStreamMapper{
		responseID:   responseID,
		model:        model,
		created:      created,
		toolIndex:    map[string]int{},
		finishReason: "stop",
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
				if responseObj.Model != "" {
					m.model = responseObj.Model
				}
				if responseObj.CreatedAt != 0 {
					m.created = responseObj.CreatedAt
				}
			}
		}
		return m.emitRoleIfNeeded(), nil
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
		if item.Type != "function_call" {
			return nil, nil
		}
		if item.CallID == "" {
			item.CallID = item.ID
		}
		m.finishReason = "tool_calls"
		idx := m.nextTool
		m.nextTool++
		m.toolIndex[item.ID] = idx
		chunks := m.emitRoleIfNeeded()
		chunks = append(chunks, m.chunk(&dto.Delta{ToolCalls: []dto.ToolCall{{
			Index: idx,
			ID:    item.CallID,
			Type:  "function",
			Function: dto.ToolCallFunc{
				Name:      item.Name,
				Arguments: "",
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
		idx, exists := m.toolIndex[event.ItemID]
		if !exists {
			idx = m.nextTool - 1
		}
		return []dto.ChatCompletionChunk{m.chunk(&dto.Delta{ToolCalls: []dto.ToolCall{{
			Index: idx,
			ID:    event.ItemID,
			Type:  "function",
			Function: dto.ToolCallFunc{
				Arguments: argsDelta,
			},
		}}}, nil)}, nil
	case "response.completed":
		finishReason := m.finishReason
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
