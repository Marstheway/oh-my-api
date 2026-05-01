package codec

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
	"github.com/gin-gonic/gin"
)

func writeResponsesEvent(c *gin.Context, event dto.ResponsesStreamEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", data); err != nil {
		return err
	}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeAnthropicStreamAsResponsesStream(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	mapper1 := newClaudeToChatStreamMapper("", "", 0)
	mapper2 := newChatToResponsesStreamMapper("", "")

	err := scanSSEData(resp.Body, func(data string) error {
		var event dto.ClaudeStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return WrapConversionError("stream_event", "anthropic_to_chat_stream", FormatAnthropicMessages, FormatOpenAIResponse, "invalid_stream_event", err)
		}
		if counter != nil {
			if sc, ok := counter.(*token.StreamCounter); ok {
				sc.AddOutputText(token.ExtractTextFromClaudeStreamEvent(&event))
			}
		}
		chunks, err := mapper1.Map(event)
		if err != nil {
			return err
		}
		for _, chunk := range chunks {
			events, err := mapper2.Map(chunk)
			if err != nil {
				return err
			}
			for _, out := range events {
				if err := writeResponsesEvent(c, out); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		var convErr *ConversionError
		if !errors.As(err, &convErr) {
			err = WrapConversionError("write_response", "anthropic_to_response_via_chat", FormatAnthropicMessages, FormatOpenAIResponse, "stream_read", err)
		}
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.ComputeOutputTokens()
		}
		counter.SetLatency()
	}
	return err
}
