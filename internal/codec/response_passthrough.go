package codec

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
	"github.com/gin-gonic/gin"
)

// passThroughResponsesResponse forwards an upstream Responses API response back
// to the client, extracting token counts and setting latency along the way.
func passThroughResponsesResponse(c *gin.Context, resp *http.Response, isStream bool, counter TokenCounter) error {
	if isStream {
		err := passThroughResponsesStream(c, resp, counter)
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
			var responsesResp dto.ResponsesResponse
			if err := json.Unmarshal(body, &responsesResp); err == nil {
				for _, item := range responsesResp.Output {
					if item.Type == "function_call" {
						sc.AddOutputText(item.Arguments)
					}
					for _, part := range item.Content {
						if part.Text != "" {
							sc.AddOutputText(part.Text)
						}
					}
				}
				sc.ComputeOutputTokens()
			}
		}
	}

	ctx := c
	ctx.Data(resp.StatusCode, "application/json", body)
	if counter != nil {
		counter.SetLatency()
	}
	return nil
}

// passThroughResponsesStream forwards raw SSE chunks from an upstream Responses
// API stream, extracting text for token counting.
func passThroughResponsesStream(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

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

		trimmed := strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(trimmed, "data: ") && counter != nil {
			data := strings.TrimPrefix(trimmed, "data: ")
			if data != "" {
				if sc, ok := counter.(*token.StreamCounter); ok {
					var event dto.ResponsesStreamEvent
					if jsonErr := json.Unmarshal([]byte(data), &event); jsonErr == nil {
						switch event.Type {
						case "response.output_text.delta":
							var textDelta string
							if len(event.Delta) > 0 {
								_ = json.Unmarshal(event.Delta, &textDelta)
							}
							if textDelta != "" {
								sc.AddOutputText(textDelta)
							}
						case "response.function_call_arguments.delta":
							var argsDelta string
							if len(event.Delta) > 0 {
								_ = json.Unmarshal(event.Delta, &argsDelta)
							}
							if argsDelta != "" {
								sc.AddOutputText(argsDelta)
							}
						case "response.completed":
							sc.ComputeOutputTokens()
						}
					}
				}
			}
		}

		if err == io.EOF {
			break
		}
	}

	// Ensure tokens are computed even if no response.completed event was received
	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.ComputeOutputTokens()
		}
	}

	return nil
}
