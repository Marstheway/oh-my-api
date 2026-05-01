package errors

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

type Protocol string

const (
	ProtocolOpenAI    Protocol = "openai"
	ProtocolAnthropic Protocol = "anthropic"
)

func WriteError(c *gin.Context, inbound Protocol, statusCode int, code ErrorCode, message string) {
	if inbound == ProtocolAnthropic {
		writeAnthropicError(c, statusCode, code, message)
	} else {
		writeOpenAIError(c, statusCode, code, message)
	}
}

func writeOpenAIError(c *gin.Context, statusCode int, code ErrorCode, message string) {
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"message": message,
			"type":    "invalid_request_error",
			"code":    code,
		},
	})
}

func writeAnthropicError(c *gin.Context, statusCode int, code ErrorCode, message string) {
	errorType := mapErrorType(code)
	c.JSON(statusCode, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errorType,
			"message": message,
		},
	})
}

func mapErrorType(code ErrorCode) string {
	switch code {
	case ErrInvalidAPIKey:
		return "authentication_error"
	case ErrModelNotFound:
		return "not_found_error"
	case ErrInvalidRequest:
		return "invalid_request_error"
	default:
		return "api_error"
	}
}

func WriteStreamError(c *gin.Context, inbound Protocol, message string) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	if inbound == ProtocolAnthropic {
		event := map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "api_error",
				"message": message,
			},
		}
		data, _ := json.Marshal(event)
		fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", data)
	} else {
		chunk := map[string]any{
			"id":      "chatcmpl-error",
			"object":  "chat.completion.chunk",
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "error"}},
			"error": map[string]any{
				"message": message,
				"type":    "api_error",
			},
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(c.Writer, "data: %s\n\n", data)
		fmt.Fprint(c.Writer, "data: [DONE]\n\n")
	}

	flusher.Flush()
}
