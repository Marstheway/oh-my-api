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

func copyResponseHeaders(dst, src http.Header) {
	for k, v := range src {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		dst[k] = append([]string(nil), v...)
	}
}

func rewriteTopLevelModel(body []byte, requestedModel string) ([]byte, error) {
	if requestedModel == "" {
		return body, nil
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}

	modelJSON, err := json.Marshal(requestedModel)
	if err != nil {
		return nil, err
	}
	obj["model"] = modelJSON
	return json.Marshal(obj)
}

func rewriteNestedModel(body []byte, field, requestedModel string) ([]byte, error) {
	if requestedModel == "" {
		return body, nil
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}

	nestedBody, ok := obj[field]
	if !ok || len(nestedBody) == 0 {
		return body, nil
	}

	var nested map[string]json.RawMessage
	if err := json.Unmarshal(nestedBody, &nested); err != nil {
		return body, nil
	}

	modelJSON, err := json.Marshal(requestedModel)
	if err != nil {
		return nil, err
	}
	nested["model"] = modelJSON

	rewrittenNested, err := json.Marshal(nested)
	if err != nil {
		return nil, err
	}
	obj[field] = rewrittenNested
	return json.Marshal(obj)
}

// passThroughResponsesResponse forwards an upstream Responses API response back
// to the client, extracting token counts and setting latency along the way.
func passThroughResponsesResponse(c *gin.Context, resp *http.Response, isStream bool, counter TokenCounter, rmc ResponseModelContext) error {
	if isStream {
		return passThroughResponsesStream(c, resp, counter, rmc.RequestedModel)
	}

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

	outBody, err := rewriteTopLevelModel(body, rmc.RequestedModel)
	if err != nil {
		return err
	}

	copyResponseHeaders(c.Writer.Header(), resp.Header)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(resp.StatusCode, contentType, outBody)
	return nil
}

// passThroughResponsesStream forwards raw SSE chunks from an upstream Responses
// API stream, extracting text for token counting.
func passThroughResponsesStream(c *gin.Context, resp *http.Response, counter TokenCounter, requestedModel string) error {
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

		trimmed := strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(trimmed, "data: ") {
			data := strings.TrimPrefix(trimmed, "data: ")
			if data != "" {
				var event dto.ResponsesStreamEvent
				if jsonErr := json.Unmarshal([]byte(data), &event); jsonErr == nil {
					if counter != nil {
						if sc, ok2 := counter.(*token.StreamCounter); ok2 {
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
					if event.Type == "response.created" || event.Type == "response.completed" {
						rewrittenData := []byte(data)
						if requestedModel != "" {
							if out, rewriteErr := rewriteNestedModel([]byte(data), "response", requestedModel); rewriteErr == nil {
								rewrittenData = out
							}
						}
						_, _ = fmt.Fprintf(c.Writer, "data: %s\n", rewrittenData)
						flusher.Flush()
						if err == io.EOF {
							break
						}
						continue
					}
				}
			}
		}

		_, _ = c.Writer.WriteString(line)
		flusher.Flush()

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
