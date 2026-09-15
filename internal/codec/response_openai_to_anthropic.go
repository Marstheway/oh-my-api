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
)

func passThroughOpenAIResponse(w http.ResponseWriter, resp *http.Response, isStream bool, counter TokenCounter, rmc ResponseModelContext) error {
	if isStream {
		copyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		return passThroughOpenAIStream(w, resp, counter, rmc.RequestedModel)
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

	copyResponseHeaders(w.Header(), resp.Header)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	return writeBody(w, resp.StatusCode, contentType, outBody)
}

func passThroughOpenAIStream(w http.ResponseWriter, resp *http.Response, counter TokenCounter, requestedModel string) (retErr error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	var lastChunk dto.ChatCompletionChunk
	sawTerminal := false
	defer func() {
		// 流已读到 EOF 但从未收到 [DONE] 或 finish_reason：上游中途断流。
		if retErr == nil && !sawTerminal {
			retErr = ErrStreamTruncated
		}
	}()
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}

		if line != "" && strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			data = strings.TrimSuffix(data, "\n")
			if data == "[DONE]" {
				sawTerminal = true
				if counter != nil {
					if sc, ok2 := counter.(*token.StreamCounter); ok2 {
						sc.ComputeOutputTokens()
					}
				}
				_, _ = io.WriteString(w, line)
				flusher.Flush()
				break
			}

			// OpenCode 私有计费事件：不透传，尽量归一为标准 usage chunk。
			if isOpenCodeInferenceCostData(data) {
				if usage, ok := openCodeInferenceCostToUsage(data); ok && lastChunk.ID != "" {
					base := lastChunk
					if requestedModel != "" {
						base.Model = requestedModel
					}
					if base.Object == "" {
						base.Object = "chat.completion.chunk"
					}
					out, marshalErr := json.Marshal(buildOpenAIStreamUsageChunk(base, usage))
					if marshalErr == nil {
						_, _ = fmt.Fprintf(w, "data: %s\n", out)
						flusher.Flush()
					}
				}
				continue
			}

			var chunk dto.ChatCompletionChunk
			if jsonErr := json.Unmarshal([]byte(data), &chunk); jsonErr == nil {
				if chunk.ID != "" {
					lastChunk = chunk
				}
				for _, choice := range chunk.Choices {
					if choice.FinishReason != nil && *choice.FinishReason != "" {
						sawTerminal = true
					}
				}
				if counter != nil {
					if sc, ok2 := counter.(*token.StreamCounter); ok2 {
						sc.AddOutputText(token.ExtractTextFromOpenAIChunk(&chunk))
					}
				}
				if requestedModel != "" {
					if rewrittenData, rewriteErr := rewriteTopLevelModel([]byte(data), requestedModel); rewriteErr == nil {
						_, _ = fmt.Fprintf(w, "data: %s\n", rewrittenData)
						flusher.Flush()
						continue
					}
				}
			}
			_, _ = io.WriteString(w, line)
			flusher.Flush()
		} else if line != "" {
			_, _ = io.WriteString(w, line)
			flusher.Flush()
		}

		if err == io.EOF {
			break
		}
	}

	return nil
}

// isOpenCodeInferenceCostData 识别 OpenCode 流末尾的私有计费事件。
// 形如: {"choices":[],"cost":"...","normalizedUsage":{...},"x-opencode-type":"inference-cost"}
func isOpenCodeInferenceCostData(data string) bool {
	if !strings.Contains(data, "x-opencode-type") {
		return false
	}
	var probe struct {
		Type string `json:"x-opencode-type"`
	}
	if err := json.Unmarshal([]byte(data), &probe); err != nil {
		return false
	}
	return probe.Type == "inference-cost"
}

// openCodeInferenceCostToUsage 将 OpenCode normalizedUsage 映射为标准 Chat usage。
func openCodeInferenceCostToUsage(data string) (dto.Usage, bool) {
	var event struct {
		NormalizedUsage *struct {
			InputTokens     int `json:"inputTokens"`
			OutputTokens    int `json:"outputTokens"`
			ReasoningTokens int `json:"reasoningTokens"`
			CacheReadTokens int `json:"cacheReadTokens"`
		} `json:"normalizedUsage"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil || event.NormalizedUsage == nil {
		return dto.Usage{}, false
	}
	nu := event.NormalizedUsage
	usage := dto.Usage{
		PromptTokens:     nu.InputTokens,
		CompletionTokens: nu.OutputTokens,
		TotalTokens:      nu.InputTokens + nu.OutputTokens,
	}
	if nu.CacheReadTokens > 0 {
		usage.PromptTokensDetails = &dto.UsageDetails{CachedTokens: nu.CacheReadTokens}
	}
	if nu.ReasoningTokens > 0 {
		usage.CompletionTokensDetails = &dto.UsageDetails{ReasoningTokens: nu.ReasoningTokens}
	}
	return usage, true
}

func writeOpenAIResponseAsAnthropic(w http.ResponseWriter, resp *http.Response, counter TokenCounter, rmc ResponseModelContext) error {
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
	if err := writeJSON(w, http.StatusOK, claudeResp); err != nil {
		return err
	}
	return nil
}

func writeOpenAIStreamAsAnthropic(w http.ResponseWriter, resp *http.Response, counter TokenCounter, requestedModel string) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	mapper := newChatToClaudeStreamMapper(requestedModel)
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
		if !strings.HasPrefix(trimmed, "data: ") {
			if err == io.EOF {
				break
			}
			continue
		}

		data := strings.TrimPrefix(trimmed, "data: ")
		if data == "" {
			if err == io.EOF {
				break
			}
			continue
		}

		if data == "[DONE]" {
			break
		}

		var chunk dto.ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Unmarshal 不会返回 io.EOF；坏帧跳过继续读
			continue
		}

		if counter != nil {
			if sc, ok := counter.(*token.StreamCounter); ok {
				sc.AddOutputText(token.ExtractTextFromOpenAIChunk(&chunk))
			}
		}

		events, mapErr := mapper.Map(chunk)
		if mapErr != nil {
			return mapErr
		}

		for _, event := range events {
			if writeErr := writeAnthropicEvent(w, event); writeErr != nil {
				return writeErr
			}
			flusher.Flush()
		}

		if err == io.EOF {
			break
		}
	}

	// 流结束，调用 Flush 发送剩余的 message_stop
	events, flushErr := mapper.Flush()
	if flushErr != nil {
		return flushErr
	}
	for _, event := range events {
		if writeErr := writeAnthropicEvent(w, event); writeErr != nil {
			return writeErr
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
