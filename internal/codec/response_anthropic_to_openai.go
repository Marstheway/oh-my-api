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
)

func passThroughAnthropicResponse(w http.ResponseWriter, resp *http.Response, isStream bool, counter TokenCounter, rmc ResponseModelContext) error {
	if isStream {
		copyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		return passThroughAnthropicStream(w, resp, counter, rmc.RequestedModel)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var claudeResp dto.ClaudeResponse
	if err := json.Unmarshal(body, &claudeResp); err != nil {
		return err
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			text := token.ExtractTextFromClaudeResponse(&claudeResp)
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

func passThroughAnthropicStream(w http.ResponseWriter, resp *http.Response, counter TokenCounter, requestedModel string) (retErr error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	sawTerminal := false
	defer func() {
		// 流已读到 EOF 但未收到 message_stop 或 error：上游中途断流。
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
		if line == "" && err == io.EOF {
			break
		}

		trimmed := strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(trimmed, "data: ") {
			data := strings.TrimPrefix(trimmed, "data: ")
			if data != "" && data != "[DONE]" {
				var event dto.ClaudeStreamEvent
				if jsonErr := json.Unmarshal([]byte(data), &event); jsonErr == nil {
					if event.Type == "message_stop" || event.Type == "error" {
						sawTerminal = true
					}
					if counter != nil {
						if sc, ok2 := counter.(*token.StreamCounter); ok2 {
							sc.AddOutputText(token.ExtractTextFromClaudeStreamEvent(&event))
						}
					}
					if requestedModel != "" && event.Type == "message_start" && event.Message != nil {
						if rewrittenData, rewriteErr := rewriteNestedModel([]byte(data), "message", requestedModel); rewriteErr == nil {
							_, _ = fmt.Fprintf(w, "data: %s\n", rewrittenData)
							flusher.Flush()
							if err == io.EOF {
								break
							}
							continue
						}
					}
				}
			}
		}

		_, _ = io.WriteString(w, line)
		flusher.Flush()

		if err == io.EOF {
			break
		}
	}

	if sc, ok := counter.(*token.StreamCounter); ok {
		sc.ComputeOutputTokens()
	}

	return nil
}

func writeClaudeResponseAsOpenAI(w http.ResponseWriter, resp *http.Response, counter TokenCounter, rmc ResponseModelContext) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var claudeResp dto.ClaudeResponse
	if err := json.Unmarshal(body, &claudeResp); err != nil {
		return err
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			text := token.ExtractTextFromClaudeResponse(&claudeResp)
			sc.AddOutputText(text)
			sc.ComputeOutputTokens()
		}
	}

	openAIResp := convertClaudeResponseToOpenAI(&claudeResp)
	if rmc.RequestedModel != "" {
		openAIResp.Model = rmc.RequestedModel
	}
	if err := writeJSON(w, http.StatusOK, openAIResp); err != nil {
		return err
	}
	return nil
}

func writeClaudeStreamAsOpenAI(w http.ResponseWriter, resp *http.Response, counter TokenCounter, rmc ResponseModelContext) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	mapper := newClaudeToChatStreamMapper("", rmc.RequestedModel, time.Now().Unix())
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

		var event dto.ClaudeStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			// Unmarshal 不会返回 io.EOF；坏帧跳过继续读
			continue
		}

		if counter != nil {
			if sc, ok2 := counter.(*token.StreamCounter); ok2 {
				sc.AddOutputText(token.ExtractTextFromClaudeStreamEvent(&event))
			}
		}

		chunks, mapErr := mapper.Map(event)
		if mapErr != nil {
			return mapErr
		}

		for _, chunk := range chunks {
			// 按 Design Rules「Chat 写回层」处理 usage
			var usageToSend *dto.Usage
			if chunk.Usage != nil {
				usageToSend = chunk.Usage
				chunk.Usage = nil
			}

			// 判断 base chunk 是否有可写内容
			if len(chunk.Choices) > 0 {
				chunkData, marshalErr := json.Marshal(chunk)
				if marshalErr != nil {
					return marshalErr
				}
				if _, writeErr := fmt.Fprintf(w, "data: %s\n\n", chunkData); writeErr != nil {
					return writeErr
				}
				flusher.Flush()
			}

			// 仅当 IncludeUsage==true 时发送 usage-only chunk
			if usageToSend != nil && rmc.IncludeUsage {
				usageChunk := buildOpenAIStreamUsageChunk(chunk, *usageToSend)
				usageData, marshalErr := json.Marshal(usageChunk)
				if marshalErr != nil {
					return marshalErr
				}
				if _, writeErr := fmt.Fprintf(w, "data: %s\n\n", usageData); writeErr != nil {
					return writeErr
				}
				flusher.Flush()
			}
		}

		if err == io.EOF {
			break
		}
	}

	// 流结束写一次 [DONE]
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.ComputeOutputTokens()
		}
	}

	return nil
}

func convertClaudeResponseToOpenAI(resp *dto.ClaudeResponse) *dto.ChatCompletionResponse {
	finishReason := "stop"
	if resp.StopReason != nil {
		finishReason = stopReasonToFinishReason(*resp.StopReason)
	}

	msg := &dto.ResMessage{
		Role:    "assistant",
		Content: "",
	}

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			msg.Content += block.Text
		case "tool_use":
			args, _ := json.Marshal(block.Input)
			msg.ToolCalls = append(msg.ToolCalls, dto.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: dto.ToolCallFunc{
					Name:      block.Name,
					Arguments: string(args),
				},
			})
		case "image":
			// Image blocks in Claude response are skipped because OpenAI Chat format
			// doesn't support multimodal content in the assistant's response message.
			// The image data from upstream cannot be represented in the OpenAI Chat response format.
		case "document":
			// Document blocks in Claude response are skipped because OpenAI Chat format
			// doesn't support file attachments in the assistant's response message.
			// The document data from upstream cannot be represented in the OpenAI Chat response format.
		}
	}

	usage := dto.Usage{
		PromptTokens:     resp.Usage.InputTokens,
		CompletionTokens: resp.Usage.OutputTokens,
		TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
	}

	// Map CacheReadInputTokens to PromptTokensDetails.CachedTokens
	if resp.Usage.CacheReadInputTokens > 0 {
		usage.PromptTokensDetails = &dto.UsageDetails{
			CachedTokens: resp.Usage.CacheReadInputTokens,
		}
	}

	return &dto.ChatCompletionResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []dto.Choice{{
			Index:        0,
			Message:      msg,
			FinishReason: &finishReason,
		}},
		Usage: usage,
	}
}

func stopReasonToFinishReason(stopReason string) string {
	switch stopReason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}
