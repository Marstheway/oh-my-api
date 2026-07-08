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

func passThroughOpenAIResponse(c *gin.Context, resp *http.Response, isStream bool, counter TokenCounter, rmc ResponseModelContext) error {
	if isStream {
		copyResponseHeaders(c.Writer.Header(), resp.Header)
		c.Writer.WriteHeader(resp.StatusCode)
		return passThroughOpenAIStream(c, resp, counter, rmc.RequestedModel)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var openAIResp dto.ChatCompletionResponse
	if err := json.Unmarshal(body, &openAIResp); err != nil {
		return err
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			text := token.ExtractTextFromOpenAIResponse(&openAIResp)
			sc.AddOutputText(text)
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

func passThroughOpenAIStream(c *gin.Context, resp *http.Response, counter TokenCounter, requestedModel string) error {
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

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			data = strings.TrimSuffix(data, "\n")
			if data == "[DONE]" {
				if counter != nil {
					if sc, ok2 := counter.(*token.StreamCounter); ok2 {
						sc.ComputeOutputTokens()
					}
				}
				_, _ = c.Writer.WriteString(line)
				flusher.Flush()
				break
			}
			var chunk dto.ChatCompletionChunk
			if jsonErr := json.Unmarshal([]byte(data), &chunk); jsonErr == nil {
				if counter != nil {
					if sc, ok2 := counter.(*token.StreamCounter); ok2 {
						sc.AddOutputText(token.ExtractTextFromOpenAIChunk(&chunk))
					}
				}
				if requestedModel != "" {
					if rewrittenData, rewriteErr := rewriteTopLevelModel([]byte(data), requestedModel); rewriteErr == nil {
						_, _ = fmt.Fprintf(c.Writer, "data: %s\n", rewrittenData)
						flusher.Flush()
						continue
					}
				}
			}
			_, _ = c.Writer.WriteString(line)
			flusher.Flush()
		} else {
			_, _ = c.Writer.WriteString(line)
			flusher.Flush()
		}

		if err == io.EOF {
			break
		}
	}

	return nil
}

func writeOpenAIResponseAsAnthropic(c *gin.Context, resp *http.Response, counter TokenCounter, rmc ResponseModelContext) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var openAIResp dto.ChatCompletionResponse
	if err := json.Unmarshal(body, &openAIResp); err != nil {
		return err
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			text := token.ExtractTextFromOpenAIResponse(&openAIResp)
			sc.AddOutputText(text)
			sc.ComputeOutputTokens()
		}
	}

	claudeResp := convertOpenAIResponseToAnthropic(&openAIResp)
	if rmc.RequestedModel != "" {
		claudeResp.Model = rmc.RequestedModel
	}
	c.JSON(http.StatusOK, claudeResp)
	return nil
}

func writeOpenAIStreamAsAnthropic(c *gin.Context, resp *http.Response, counter TokenCounter, requestedModel string) error {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	reader := bufio.NewReader(resp.Body)
	messageStarted := false
	textIndex := -1
	nextIndex := 0
	toolIndex := make(map[string]int)
	stopSent := false
	messageID := ""
	model := requestedModel

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		line = strings.TrimSuffix(line, "\n")
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}

		var chunk dto.ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if counter != nil {
			if sc, ok := counter.(*token.StreamCounter); ok {
				text := token.ExtractTextFromOpenAIChunk(&chunk)
				sc.AddOutputText(text)
			}
		}

		if !messageStarted {
			messageStarted = true
			if chunk.ID != "" {
				messageID = chunk.ID
			} else {
				messageID = fmt.Sprintf("msg-%d", time.Now().UnixNano())
			}
			if requestedModel == "" && chunk.Model != "" {
				model = chunk.Model
			}
			start := dto.ClaudeStreamEvent{
				Type: "message_start",
				Message: &dto.ClaudeMessageStart{
					ID:      messageID,
					Type:    "message",
					Role:    "assistant",
					Model:   model,
					Content: []dto.ContentBlock{},
					Usage:   dto.ClaudeUsage{},
				},
			}
			if err := writeAnthropicEvent(c.Writer, start); err != nil {
				return err
			}
			flusher.Flush()
		}

		if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil {
			continue
		}
		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			if textIndex == -1 {
				textIndex = nextIndex
				nextIndex++
				start := dto.ClaudeStreamEvent{
					Type:  "content_block_start",
					Index: textIndex,
					ContentBlock: &dto.ContentBlock{
						Type: "text",
						Text: "",
					},
				}
				if err := writeAnthropicEvent(c.Writer, start); err != nil {
					return err
				}
				flusher.Flush()
			}
			deltaEvent := dto.ClaudeStreamEvent{
				Type:  "content_block_delta",
				Index: textIndex,
				Delta: &dto.ClaudeDelta{Type: "text_delta", Text: delta.Content},
			}
			if err := writeAnthropicEvent(c.Writer, deltaEvent); err != nil {
				return err
			}
			flusher.Flush()
		}

		if len(delta.ToolCalls) > 0 {
			for _, tc := range delta.ToolCalls {
				idx, ok := toolIndex[tc.ID]
				if !ok {
					idx = nextIndex
					nextIndex++
					toolIndex[tc.ID] = idx
					start := dto.ClaudeStreamEvent{
						Type:  "content_block_start",
						Index: idx,
						ContentBlock: &dto.ContentBlock{
							Type:  "tool_use",
							ID:    tc.ID,
							Name:  tc.Function.Name,
							Input: map[string]any{},
						},
					}
					if err := writeAnthropicEvent(c.Writer, start); err != nil {
						return err
					}
					flusher.Flush()
				}
				if tc.Function.Arguments != "" {
					deltaEvent := dto.ClaudeStreamEvent{
						Type:  "content_block_delta",
						Index: idx,
						Delta: &dto.ClaudeDelta{Type: "input_json_delta", PartialJSON: &tc.Function.Arguments},
					}
					if err := writeAnthropicEvent(c.Writer, deltaEvent); err != nil {
						return err
					}
					flusher.Flush()
				}
			}
		}

		if chunk.Choices[0].FinishReason != nil {
			if textIndex != -1 {
				stop := dto.ClaudeStreamEvent{Type: "content_block_stop", Index: textIndex}
				if err := writeAnthropicEvent(c.Writer, stop); err != nil {
					return err
				}
				flusher.Flush()
			}
			for _, idx := range toolIndex {
				stop := dto.ClaudeStreamEvent{Type: "content_block_stop", Index: idx}
				if err := writeAnthropicEvent(c.Writer, stop); err != nil {
					return err
				}
				flusher.Flush()
			}

			stopReason := finishReasonToStopReason(*chunk.Choices[0].FinishReason)
			deltaEvent := dto.ClaudeStreamEvent{Type: "message_delta", Delta: &dto.ClaudeDelta{StopReason: stopReason}}
			if err := writeAnthropicEvent(c.Writer, deltaEvent); err != nil {
				return err
			}
			flusher.Flush()

			stopEvent := dto.ClaudeStreamEvent{Type: "message_stop"}
			if err := writeAnthropicEvent(c.Writer, stopEvent); err != nil {
				return err
			}
			flusher.Flush()
			stopSent = true
		}
	}

	if !stopSent {
		if textIndex != -1 {
			stop := dto.ClaudeStreamEvent{Type: "content_block_stop", Index: textIndex}
			if err := writeAnthropicEvent(c.Writer, stop); err != nil {
				return err
			}
		}
		for _, idx := range toolIndex {
			stop := dto.ClaudeStreamEvent{Type: "content_block_stop", Index: idx}
			if err := writeAnthropicEvent(c.Writer, stop); err != nil {
				return err
			}
		}
		stopEvent := dto.ClaudeStreamEvent{Type: "message_stop"}
		if err := writeAnthropicEvent(c.Writer, stopEvent); err != nil {
			return err
		}
		flusher.Flush()
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.ComputeOutputTokens()
		}
	}

	return nil
}

func writeAnthropicEvent(w io.Writer, event dto.ClaudeStreamEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if event.Type != "" {
		if _, err = fmt.Fprintf(w, "event: %s\n", event.Type); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

func convertOpenAIResponseToAnthropic(resp *dto.ChatCompletionResponse) *dto.ClaudeResponse {
	out := &dto.ClaudeResponse{
		ID:    resp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: resp.Model,
	}

	if len(resp.Choices) > 0 && resp.Choices[0].Message != nil {
		msg := resp.Choices[0].Message
		// 尝试将 Content 解析为多模态内容
		contentBlocks := convertOpenAIContentToAnthropicBlocks(msg.Content)
		out.Content = append(out.Content, contentBlocks...)

		for _, tc := range msg.ToolCalls {
			input := map[string]any{}
			if tc.Function.Arguments != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
			}
			out.Content = append(out.Content, dto.ContentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: input,
			})
		}
	}

	if len(resp.Choices) > 0 && resp.Choices[0].FinishReason != nil {
		stop := finishReasonToStopReason(*resp.Choices[0].FinishReason)
		out.StopReason = &stop
	}

	out.Usage = dto.ClaudeUsage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}

	// 透传 Usage 缓存 token 统计字段
	if resp.Usage.PromptTokensDetails != nil && resp.Usage.PromptTokensDetails.CachedTokens > 0 {
		out.Usage.CacheReadInputTokens = resp.Usage.PromptTokensDetails.CachedTokens
	}

	return out
}

// convertOpenAIContentToAnthropicBlocks 将 OpenAI 响应的内容转换为 Anthropic ContentBlock 数组
// 支持检测 Data URI 格式的图片和文件并转换为对应的 block 类型
func convertOpenAIContentToAnthropicBlocks(content string) []dto.ContentBlock {
	if content == "" {
		return nil
	}

	// 解析 Data URI
	mediaType, data, isDataURI := parseDataURI(content)
	if !isDataURI {
		// 非 Data URI，作为普通文本
		return []dto.ContentBlock{{Type: "text", Text: content}}
	}

	// 根据 media type 判断类型
	if isImageMediaType(mediaType) {
		return []dto.ContentBlock{{
			Type: "image",
			Source: &dto.MessageSource{
				Type:      "base64",
				MediaType: mediaType,
				Data:      data,
			},
		}}
	}

	// 其他类型统一作为 document
	return []dto.ContentBlock{{
		Type: "document",
		Source: &dto.MessageSource{
			Type:      "base64",
			MediaType: mediaType,
			Data:      data,
		},
	}}
}

// isImageMediaType 判断 media type 是否为图片类型
func isImageMediaType(mediaType string) bool {
	return strings.HasPrefix(mediaType, "image/")
}

func finishReasonToStopReason(finishReason string) string {
	switch finishReason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return "end_turn"
	}
}
