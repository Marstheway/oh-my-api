package adaptor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
)

type AnthropicAdaptor struct{}

func (a *AnthropicAdaptor) BuildRequest(ctx context.Context, provider *config.ProviderConfig,
	upstreamModel string, body io.Reader, inbound Protocol) *http.Request {

	endpoint := provider.GetEndpoint(string(inbound))
	url := BuildURL(endpoint, ProtocolAnthropic)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", provider.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	return req
}

func (a *AnthropicAdaptor) WriteResponse(c *gin.Context, inbound Protocol,
	resp *http.Response, isStream bool, counter TokenCounter) error {

	if inbound == ProtocolAnthropic {
		return a.passThrough(c, resp, isStream, counter)
	}

	if isStream {
		err := a.convertClaudeStreamToOpenAI(c, resp, counter)
		if counter != nil {
			counter.SetLatency()
		}
		return err
	}
	return a.convertClaudeResponseToOpenAI(c, resp, counter)
}

func (a *AnthropicAdaptor) passThrough(c *gin.Context, resp *http.Response, isStream bool, counter TokenCounter) error {
	for k, v := range resp.Header {
		c.Writer.Header()[k] = v
	}
	c.Writer.WriteHeader(resp.StatusCode)

	if isStream && counter != nil {
		err := a.passThroughStream(c, resp, counter)
		counter.SetLatency()
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

func (a *AnthropicAdaptor) passThroughStream(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		c.Writer.WriteString(line + "\n")
		flusher.Flush()

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
	}

	if sc, ok := counter.(*token.StreamCounter); ok {
		sc.ComputeOutputTokens()
	}

	return scanner.Err()
}

// ConvertOpenAIToClaude converts OpenAI request to Anthropic format.
// Deprecated: use ConvertOpenAIToClaudeV2 directly.
func ConvertOpenAIToClaude(req *dto.ChatCompletionRequest, model string) *dto.ClaudeRequest {
	return ConvertOpenAIToClaudeV2(req, model)
}

func (a *AnthropicAdaptor) convertClaudeResponseToOpenAI(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var claudeResp dto.ClaudeResponse
	if err := json.Unmarshal(body, &claudeResp); err != nil {
		c.Data(resp.StatusCode, "application/json", body)
		if counter != nil {
			counter.SetLatency()
		}
		return nil
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			text := token.ExtractTextFromClaudeResponse(&claudeResp)
			sc.AddOutputText(text)
			sc.ComputeOutputTokens()
		}
	}

	openAIResp := ConvertClaudeResponseToOpenAI(&claudeResp)
	c.JSON(http.StatusOK, openAIResp)
	if counter != nil {
		counter.SetLatency()
	}
	return nil
}

func (a *AnthropicAdaptor) convertClaudeStreamToOpenAI(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	responseID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	model := ""
	scanner := bufio.NewScanner(resp.Body)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "" {
			continue
		}

		// token 统计
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

		chunk := ConvertClaudeStreamEventToOpenAI(responseID, model, &event)
		if chunk == nil {
			if event.Type == "message_stop" {
				fmt.Fprint(c.Writer, "data: [DONE]\n\n")
				flusher.Flush()
			}
			continue
		}

		chunkData, err := json.Marshal(chunk)
		if err != nil {
			continue
		}
		fmt.Fprintf(c.Writer, "data: %s\n\n", chunkData)
		flusher.Flush()
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.ComputeOutputTokens()
		}
	}

	return scanner.Err()
}
