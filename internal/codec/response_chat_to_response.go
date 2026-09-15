package codec

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
)

// convertOpenAIChatResponseToResponses 将 OpenAI Chat 非流式响应转换为 Responses API 格式。
func convertOpenAIChatResponseToResponses(resp *dto.ChatCompletionResponse) (*dto.ResponsesResponse, error) {
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("chat response has no choices")
	}

	choice := resp.Choices[0]

	status := "completed"
	var incompleteDetails *dto.IncompleteDetails
	if choice.FinishReason != nil && *choice.FinishReason == "length" {
		status = "incomplete"
		incompleteDetails = &dto.IncompleteDetails{Reason: "max_output_tokens"}
	}

	var output []dto.ResponsesOutput
	if choice.Message != nil {
		msg := choice.Message
		if msg.ReasoningContent != "" {
			output = append(output, dto.ResponsesOutput{
				Type: "reasoning",
				Summary: mustRawJSON([]map[string]any{
					{"type": "summary_text", "text": msg.ReasoningContent},
				}),
			})
		}
		if msg.Content != "" {
			output = append(output, dto.ResponsesOutput{
				Type:   "message",
				ID:     resp.ID,
				Status: "completed",
				Role:   "assistant",
				Content: []dto.ResponsesOutputContent{
					{Type: "output_text", Text: msg.Content},
				},
			})
		}
		for _, tc := range msg.ToolCalls {
			output = append(output, dto.ResponsesOutput{
				Type:      "function_call",
				CallID:    tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
	}

	out := &dto.ResponsesResponse{
		ID:        resp.ID,
		Object:    "response",
		CreatedAt: resp.Created,
		Model:     resp.Model,
		Status:    status,
		Output:    output,
		Usage: dto.ResponsesUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		},
		IncompleteDetails: incompleteDetails,
	}

	return out, nil
}

// writeOpenAIChatResponseAsResponses 读取 Chat 格式的响应体，转换后以 Responses API 格式写回客户端。
func writeOpenAIChatResponseAsResponses(w http.ResponseWriter, resp *http.Response, counter TokenCounter, rmc ResponseModelContext) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var chatResp dto.ChatCompletionResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return err
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			text := token.ExtractTextFromOpenAIResponse(&chatResp)
			sc.AddOutputText(text)
			sc.ComputeOutputTokens()
		}
	}

	responsesResp, err := convertOpenAIChatResponseToResponses(&chatResp)
	if err != nil {
		return err
	}

	if rmc.RequestedModel != "" {
		responsesResp.Model = rmc.RequestedModel
	}

	if err := writeJSON(w, http.StatusOK, responsesResp); err != nil {
		return err
	}
	return nil
}

// writeOpenAIChatStreamAsResponses 读取 Chat 格式的 SSE 流，转换后以 Responses API SSE 格式写回客户端。
func writeOpenAIChatStreamAsResponses(w http.ResponseWriter, resp *http.Response, counter TokenCounter, requestedModel string) error {
	if _, ok := w.(http.Flusher); !ok {
		return fmt.Errorf("streaming not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	mapper := newChatToResponsesStreamMapper("", requestedModel)
	writer := newResponsesStreamWriter(w)
	err := scanSSEData(resp.Body, func(data string) error {
		var chunk dto.ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil
		}
		if counter != nil {
			if sc, ok := counter.(*token.StreamCounter); ok {
				sc.AddOutputText(token.ExtractTextFromOpenAIChunk(&chunk))
			}
		}
		events, err := mapper.Map(chunk)
		if err != nil {
			return err
		}
		for _, event := range events {
			if err := writer.writeEvent(event); err != nil {
				return err
			}
		}
		return nil
	})

	// 流结束，调用 Flush 发送剩余的 response.completed
	if err == nil {
		events, flushErr := mapper.Flush()
		if flushErr != nil {
			return flushErr
		}
		for _, event := range events {
			if writeErr := writer.writeEvent(event); writeErr != nil {
				return writeErr
			}
		}
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.ComputeOutputTokens()
		}
	}

	return err
}
