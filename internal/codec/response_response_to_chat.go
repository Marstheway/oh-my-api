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

// convertOpenAIResponseToChat 将 Responses API 非流式响应转换为 Chat 格式。
func convertOpenAIResponseToChat(resp *dto.ResponsesResponse) (*dto.ChatCompletionResponse, error) {
	if resp.Status == "failed" || resp.Error != nil {
		msg := "response failed"
		if resp.Error != nil {
			msg = fmt.Sprintf("response error [%s]: %s", resp.Error.Code, resp.Error.Message)
		}
		return nil, fmt.Errorf("%s", msg)
	}

	msg := &dto.ResMessage{
		Role:    "assistant",
		Content: "",
	}

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					msg.Content += part.Text
				case "refusal":
					msg.Content += part.Text
				default:
					return nil, fmt.Errorf("unsupported output content part type: %s", part.Type)
				}
			}
		case "function_call":
			msg.ToolCalls = append(msg.ToolCalls, dto.ToolCall{
				ID:   item.CallID,
				Type: "function",
				Function: dto.ToolCallFunc{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			})
		default:
			return nil, fmt.Errorf("unsupported response output type: %s", item.Type)
		}
	}

	finishReason := "stop"
	switch resp.Status {
	case "completed":
		if len(msg.ToolCalls) > 0 {
			finishReason = "tool_calls"
		} else {
			finishReason = "stop"
		}
	case "incomplete":
		finishReason = "length"
	default:
		finishReason = "stop"
	}

	return &dto.ChatCompletionResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: resp.CreatedAt,
		Model:   resp.Model,
		Choices: []dto.Choice{{
			Index:        0,
			Message:      msg,
			FinishReason: &finishReason,
		}},
		Usage: dto.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}, nil
}

// writeOpenAIResponseAsChatResponse 读取 Responses API 格式的响应体，转换后以 Chat 格式写回客户端。
func writeOpenAIResponseAsChatResponse(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var responsesResp dto.ResponsesResponse
	if err := json.Unmarshal(body, &responsesResp); err != nil {
		return err
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			for _, item := range responsesResp.Output {
				switch item.Type {
				case "message":
					for _, part := range item.Content {
						if part.Type == "output_text" {
							sc.AddOutputText(part.Text)
						}
					}
				case "function_call":
					if item.Arguments != "" {
						sc.AddOutputText(item.Arguments)
					}
				}
			}
			sc.ComputeOutputTokens()
		}
	}

	chatResp, err := convertOpenAIResponseToChat(&responsesResp)
	if err != nil {
		return err
	}

	c.JSON(http.StatusOK, chatResp)
	if counter != nil {
		counter.SetLatency()
	}
	return nil
}

// writeOpenAIResponseStreamAsChatStream 读取 Responses API SSE 流，转换后以 Chat SSE 格式写回客户端。
func writeOpenAIResponseStreamAsChatStream(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	mapper := newResponsesToChatStreamMapper("", "", 0)
	err := scanSSEData(resp.Body, func(data string) error {
		var event dto.ResponsesStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return nil
		}
		if counter != nil {
			if sc, ok := counter.(*token.StreamCounter); ok {
				switch event.Type {
				case "response.output_text.delta", "response.function_call_arguments.delta":
					var delta string
					if len(event.Delta) > 0 {
						_ = json.Unmarshal(event.Delta, &delta)
					}
					if delta != "" {
						sc.AddOutputText(delta)
					}
				}
			}
		}
		chunks, err := mapper.Map(event)
		if err != nil {
			return err
		}
		for _, chunk := range chunks {
			payload, err := json.Marshal(chunk)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", payload); err != nil {
				return err
			}
			flusher.Flush()
		}
		if event.Type == "response.completed" {
			if _, err := fmt.Fprintf(c.Writer, "data: [DONE]\n\n"); err != nil {
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
