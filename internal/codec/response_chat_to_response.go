package codec

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
	"github.com/gin-gonic/gin"
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
func writeOpenAIChatResponseAsResponses(c *gin.Context, resp *http.Response, counter TokenCounter) error {
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

	c.JSON(http.StatusOK, responsesResp)
	if counter != nil {
		counter.SetLatency()
	}
	return nil
}

// writeOpenAIChatStreamAsResponses 读取 Chat 格式的 SSE 流，转换后以 Responses API SSE 格式写回客户端。
func writeOpenAIChatStreamAsResponses(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	mapper := newChatToResponsesStreamMapper("", "")
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
			payload, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", payload); err != nil {
				return err
			}
			flusher.Flush()
		}
		return nil
	})

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.ComputeOutputTokens()
		}
		counter.SetLatency()
	}

	return err
}
