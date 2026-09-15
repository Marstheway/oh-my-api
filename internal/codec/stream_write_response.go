package codec

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
)

type responsesStreamSequencer struct {
	next int64
}

func (s *responsesStreamSequencer) rewrite(data []byte) ([]byte, error) {
	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, err
	}
	event["sequence_number"] = s.next
	s.next++
	return json.Marshal(event)
}

type responsesStreamWriter struct {
	rw        http.ResponseWriter
	sequencer responsesStreamSequencer
}

func newResponsesStreamWriter(rw http.ResponseWriter) *responsesStreamWriter {
	return &responsesStreamWriter{rw: rw}
}

func (w *responsesStreamWriter) writeEvent(event any) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data, err = w.sequencer.rewrite(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w.rw, "data: %s\n\n", data); err != nil {
		return err
	}
	if flusher, ok := w.rw.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeAnthropicStreamAsResponsesStream(w http.ResponseWriter, resp *http.Response, counter TokenCounter, requestedModel string) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	mapper1 := newClaudeToChatStreamMapper("", requestedModel, 0)
	mapper2 := newChatToResponsesStreamMapper("", requestedModel)
	writer := newResponsesStreamWriter(w)

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
				if err := writer.writeEvent(out); err != nil {
					return err
				}
			}
		}
		return nil
	})

	// 仅成功读完后 Flush，补发 pending 的 response.completed
	if err == nil {
		flushEvents, flushErr := mapper2.Flush()
		if flushErr != nil {
			err = flushErr
		} else {
			for _, out := range flushEvents {
				if writeErr := writer.writeEvent(out); writeErr != nil {
					err = writeErr
					break
				}
			}
		}
	}

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
	}
	return err
}
