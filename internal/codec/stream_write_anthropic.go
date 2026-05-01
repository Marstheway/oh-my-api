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

func writeClaudeEvent(c *gin.Context, event dto.ClaudeStreamEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if event.Type != "" {
		if _, err := fmt.Fprintf(c.Writer, "event: %s\n", event.Type); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", data); err != nil {
		return err
	}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeResponsesStreamAsClaudeStream(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	mapper1 := newResponsesToChatStreamMapper("", "", 0)
	mapper2 := newChatToClaudeStreamMapper()

	err := scanSSEData(resp.Body, func(data string) error {
		var event dto.ResponsesStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return WrapConversionError("stream_event", "response_to_chat_stream", FormatOpenAIResponse, FormatAnthropicMessages, "invalid_stream_event", err)
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
				if err := writeClaudeEvent(c, out); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		var convErr *ConversionError
		if !errors.As(err, &convErr) {
			err = WrapConversionError("write_response", "response_to_chat", FormatOpenAIResponse, FormatAnthropicMessages, "stream_read", err)
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
