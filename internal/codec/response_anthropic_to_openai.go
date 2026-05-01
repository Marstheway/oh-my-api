package codec

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
	"github.com/gin-gonic/gin"
)

func passThroughAnthropicResponse(c *gin.Context, resp *http.Response, isStream bool, counter TokenCounter) error {
	for k, v := range resp.Header {
		c.Writer.Header()[k] = v
	}
	c.Writer.WriteHeader(resp.StatusCode)

	if isStream {
		err := passThroughAnthropicStream(c, resp, counter)
		if counter != nil {
			counter.SetLatency()
		}
		return err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			var claudeResp dto.ClaudeResponse
			if err := json.Unmarshal(body, &claudeResp); err == nil {
				text := token.ExtractTextFromClaudeResponse(&claudeResp)
				sc.AddOutputText(text)
				sc.ComputeOutputTokens()
			}
		}
	}

	c.Data(resp.StatusCode, "application/json", body)
	if counter != nil {
		counter.SetLatency()
	}
	return nil
}

func passThroughAnthropicStream(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		if line == "" && err == io.EOF {
			break
		}

		_, _ = c.Writer.WriteString(line)
		flusher.Flush()

		line = strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(line, "data: ") && counter != nil {
			data := strings.TrimPrefix(line, "data: ")
			if data != "" && data != "[DONE]" {
				if sc, ok := counter.(*token.StreamCounter); ok {
					var event dto.ClaudeStreamEvent
					if err := json.Unmarshal([]byte(data), &event); err == nil {
						text := token.ExtractTextFromClaudeStreamEvent(&event)
						sc.AddOutputText(text)
					}
				}
			}
		}

		if err == io.EOF {
			break
		}
	}

	if sc, ok := counter.(*token.StreamCounter); ok {
		sc.ComputeOutputTokens()
	}

	return nil
}

func writeClaudeResponseAsOpenAI(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var claudeResp dto.ClaudeResponse
	if err := json.Unmarshal(body, &claudeResp); err != nil {
		return err
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			text := token.ExtractTextFromClaudeResponse(&claudeResp)
			sc.AddOutputText(text)
			sc.ComputeOutputTokens()
		}
	}

	openAIResp := convertClaudeResponseToOpenAI(&claudeResp)
	c.JSON(http.StatusOK, openAIResp)
	if counter != nil {
		counter.SetLatency()
	}
	return nil
}

func writeClaudeStreamAsOpenAI(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	responseID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	model := ""
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		if line == "" && err == io.EOF {
			break
		}
		line = strings.TrimSuffix(line, "\n")
		if !strings.HasPrefix(line, "data: ") {
			if err == io.EOF {
				break
			}
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "" {
			if err == io.EOF {
				break
			}
			continue
		}

		if counter != nil {
			if sc, ok := counter.(*token.StreamCounter); ok {
				var streamEvent dto.ClaudeStreamEvent
				if err := json.Unmarshal([]byte(data), &streamEvent); err == nil {
					text := token.ExtractTextFromClaudeStreamEvent(&streamEvent)
					sc.AddOutputText(text)
				}
			}
		}

		var event dto.ClaudeStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			if err == io.EOF {
				break
			}
			continue
		}

		if event.Type == "message_start" && event.Message != nil {
			if event.Message.ID != "" {
				responseID = event.Message.ID
			}
			if event.Message.Model != "" {
				model = event.Message.Model
			}
		}

		chunk := convertClaudeStreamEventToOpenAI(responseID, model, &event)
		if chunk == nil {
			if event.Type == "message_stop" {
				_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
				flusher.Flush()
			}
			if err == io.EOF {
				break
			}
			continue
		}

		chunkData, err := json.Marshal(chunk)
		if err != nil {
			if err == io.EOF {
				break
			}
			continue
		}
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", chunkData)
		flusher.Flush()

		if err == io.EOF {
			break
		}
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.ComputeOutputTokens()
		}
		counter.SetLatency()
	}

	return nil
}

func convertClaudeResponseToOpenAI(resp *dto.ClaudeResponse) *dto.ChatCompletionResponse {
	finishReason := "stop"
	if resp.StopReason != nil {
		finishReason = stopReasonToFinishReason(*resp.StopReason)
	}

	msg := &dto.ResMessage{
		Role:    "assistant",
		Content: "",
	}

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			msg.Content += block.Text
		case "tool_use":
			args, _ := json.Marshal(block.Input)
			msg.ToolCalls = append(msg.ToolCalls, dto.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: dto.ToolCallFunc{
					Name:      block.Name,
					Arguments: string(args),
				},
			})
		}
	}

	return &dto.ChatCompletionResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []dto.Choice{{
			Index:        0,
			Message:      msg,
			FinishReason: &finishReason,
		}},
		Usage: dto.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}
}

func convertClaudeStreamEventToOpenAI(responseID, model string, event *dto.ClaudeStreamEvent) *dto.ChatCompletionChunk {
	chunk := &dto.ChatCompletionChunk{
		ID:      responseID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: &dto.Delta{}}},
	}

	switch event.Type {
	case "message_start":
		if event.Message != nil {
			if event.Message.ID != "" {
				chunk.ID = event.Message.ID
			}
			if event.Message.Model != "" {
				chunk.Model = event.Message.Model
			}
		}
		chunk.Choices[0].Delta.Role = "assistant"
		return chunk

	case "content_block_start":
		if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
			chunk.Choices[0].Index = event.Index
			chunk.Choices[0].Delta.ToolCalls = []dto.ToolCall{{
				ID:   event.ContentBlock.ID,
				Type: "function",
				Function: dto.ToolCallFunc{
					Name:      event.ContentBlock.Name,
					Arguments: "",
				},
			}}
			return chunk
		}
		return nil

	case "content_block_delta":
		if event.Delta == nil {
			return nil
		}
		chunk.Choices[0].Index = event.Index
		switch event.Delta.Type {
		case "text_delta":
			chunk.Choices[0].Delta.Content = event.Delta.Text
			return chunk
		case "input_json_delta":
			chunk.Choices[0].Index = event.Index
			chunk.Choices[0].Delta.ToolCalls = []dto.ToolCall{{
				Function: dto.ToolCallFunc{Arguments: event.Delta.PartialJSON},
			}}
			return chunk
		case "thinking_delta":
			chunk.Choices[0].Delta.ReasoningContent = event.Delta.Thinking
			return chunk
		}
		return nil

	case "content_block_stop":
		return nil

	case "message_delta":
		if event.Delta != nil && event.Delta.StopReason != "" {
			fr := stopReasonToFinishReason(event.Delta.StopReason)
			chunk.Choices[0].FinishReason = &fr
			chunk.Choices[0].Delta = &dto.Delta{}
			return chunk
		}
		return nil

	case "message_stop":
		return nil

	default:
		return nil
	}
}

func stopReasonToFinishReason(stopReason string) string {
	switch stopReason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}
