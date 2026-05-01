package adaptor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
)

type OpenAIAdaptor struct{}

func (a *OpenAIAdaptor) BuildRequest(ctx context.Context, provider *config.ProviderConfig,
	upstreamModel string, body io.Reader, inbound Protocol) *http.Request {

	endpoint := provider.GetEndpoint(string(inbound))
	url := BuildURL(endpoint, inbound)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	return req
}

func (a *OpenAIAdaptor) WriteResponse(c *gin.Context, inbound Protocol,
	resp *http.Response, isStream bool, counter TokenCounter) error {

	for k, v := range resp.Header {
		c.Writer.Header()[k] = v
	}

	c.Writer.WriteHeader(resp.StatusCode)

	if isStream {
		err := a.writeStreamResponse(c, resp, counter)
		if counter != nil {
			counter.SetLatency()
		}
		return err
	}
	err := a.writeNonStreamResponse(c, resp, counter)
	if counter != nil {
		counter.SetLatency()
	}
	return err
}

func (a *OpenAIAdaptor) writeStreamResponse(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		c.Writer.WriteString(line)
		flusher.Flush()

		if strings.HasPrefix(line, "data: ") && counter != nil {
			data := strings.TrimPrefix(line, "data: ")
			data = strings.TrimSuffix(data, "\n")
			if data != "[DONE]" {
				if sc, ok := counter.(*token.StreamCounter); ok {
					var chunk dto.ChatCompletionChunk
					if err := json.Unmarshal([]byte(data), &chunk); err == nil {
						text := token.ExtractTextFromOpenAIChunk(&chunk)
						sc.AddOutputText(text)
					}
				}
			}
		}

		if strings.Contains(line, "[DONE]") {
			if sc, ok := counter.(*token.StreamCounter); ok {
				sc.ComputeOutputTokens()
			}
			break
		}
	}

	return nil
}

func (a *OpenAIAdaptor) writeNonStreamResponse(c *gin.Context, resp *http.Response, counter TokenCounter) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			var openAIResp dto.ChatCompletionResponse
			if err := json.Unmarshal(body, &openAIResp); err == nil {
				text := token.ExtractTextFromOpenAIResponse(&openAIResp)
				sc.AddOutputText(text)
				sc.ComputeOutputTokens()
			}
		}
	}

	c.Data(resp.StatusCode, "application/json", body)
	return nil
}
