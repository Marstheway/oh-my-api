package codec

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
)

func writeClaudeEvent(w http.ResponseWriter, event dto.ClaudeStreamEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if event.Type != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event.Type); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeResponsesStreamAsClaudeStream(w http.ResponseWriter, resp *http.Response, counter TokenCounter, requestedModel string) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("X-Accel-Buffering", "no")

	mapper1 := newResponsesToChatStreamMapper("", requestedModel, 0)
	mapper2 := newChatToClaudeStreamMapper(requestedModel)

	err := scanSSEData(resp.Body, func(data string) error {
		res, err := normalizeResponsesEventData([]byte(data))
		if err != nil {
			return WrapConversionError("stream_event", "response_to_chat_stream", FormatOpenAIResponse, FormatAnthropicMessages, "invalid_stream_event", err)
		}
		event := res.Event

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
		chunks, err := mapper1.Map(*event)
		if err != nil {
			return err
		}
		for _, chunk := range chunks {
			events, err := mapper2.Map(chunk)
			if err != nil {
				return err
			}
			for _, out := range events {
				if err := writeClaudeEvent(w, out); err != nil {
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

	// 流结束，调用 mapper2 Flush 发送剩余的 message_stop
	if err == nil {
		events, flushErr := mapper2.Flush()
		if flushErr != nil {
			return flushErr
		}
		for _, out := range events {
			if writeErr := writeClaudeEvent(w, out); writeErr != nil {
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
