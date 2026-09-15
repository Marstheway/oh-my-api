package codec

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
)

// scanOllamaChatStream 按行扫描 Ollama /api/chat 流式响应，每行是一个完整 JSON chunk。
// 空行跳过；非法 JSON 视为上游坏响应并返回 conversion error。
// 若 chunk.Error 非空，视为非法成功响应并返回 conversion error。
// 对每个有效 chunk 调用 onChunk 并传入标准化的 ChatCompletionChunk。
func scanOllamaChatStream(body io.Reader, onChunk func(dto.ChatCompletionChunk) error) error {
	reader := bufio.NewReader(body)
	// 分配稳定 index 给 tool call（按首次出现顺序）。
	// Ollama chunk 不提供可跨 chunk 复用的 tool_call 标识，因此每个出现的 tool call
	// 都视为一个新的完整调用，避免同名函数调用被错误合并。
	nextToolIndex := 0
	sawToolCalls := false

	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		if line == "" && err == io.EOF {
			return nil
		}

		line = strings.TrimSpace(line)
		if line == "" {
			if err == io.EOF {
				return nil
			}
			continue
		}

		var raw dto.OllamaChatStreamChunk
		if jsonErr := json.Unmarshal([]byte(line), &raw); jsonErr != nil {
			return WrapConversionError("write_response", "ollama_stream_scan",
				FormatOllamaChat, FormatOpenAIChat, "invalid_json_line", jsonErr)
		}

		if raw.Error != "" {
			return WrapConversionError("write_response", "ollama_stream_scan",
				FormatOllamaChat, FormatOpenAIChat, "upstream_error",
				fmt.Errorf("ollama error: %s", raw.Error))
		}

		// 追踪是否出现过 tool calls，用于终止 chunk 的 finish_reason 修正
		if !raw.Done && len(raw.Message.ToolCalls) > 0 {
			sawToolCalls = true
		}

		chunk := ollamaChunkToChatChunk(raw, &nextToolIndex)

		// 若终止 chunk 前出现过 tool calls，强制将 finish_reason 修正为 tool_calls
		if raw.Done && sawToolCalls && chunk.Choices[0].FinishReason != nil {
			fr := "tool_calls"
			chunk.Choices[0].FinishReason = &fr
		}

		if cbErr := onChunk(chunk); cbErr != nil {
			return cbErr
		}

		if err == io.EOF {
			return nil
		}
	}
}

// ollamaChunkToChatChunk 将单个 OllamaChatStreamChunk 转换为 ChatCompletionChunk。
func ollamaChunkToChatChunk(raw dto.OllamaChatStreamChunk, nextToolIndex *int) dto.ChatCompletionChunk {
	delta := &dto.Delta{
		Role:             raw.Message.Role,
		Content:          raw.Message.Content,
		ReasoningContent: raw.Message.Thinking,
	}

	for i, tc := range raw.Message.ToolCalls {
		idx := *nextToolIndex
		*nextToolIndex++
		args := ""
		if len(tc.Function.Arguments) > 0 {
			args = string(tc.Function.Arguments)
		}
		delta.ToolCalls = append(delta.ToolCalls, dto.ToolCall{
			Index: &idx,
			ID:    fmt.Sprintf("call_%08x_%d", rand.Uint32(), i),
			Type:  "function",
			Function: dto.ToolCallFunc{
				Name:      tc.Function.Name,
				Arguments: args,
			},
		})
	}

	chunk := dto.ChatCompletionChunk{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   raw.Model,
		Choices: []dto.ChunkChoice{
			{Index: 0, Delta: delta},
		},
	}

	if raw.Done {
		finishReason := "stop"
		if raw.DoneReason != "" {
			finishReason = raw.DoneReason
		}
		if len(delta.ToolCalls) > 0 {
			finishReason = "tool_calls"
		}
		chunk.Choices[0].FinishReason = &finishReason
		if raw.PromptEvalCount > 0 || raw.EvalCount > 0 {
			chunk.Usage = &dto.Usage{
				PromptTokens:     raw.PromptEvalCount,
				CompletionTokens: raw.EvalCount,
				TotalTokens:      raw.PromptEvalCount + raw.EvalCount,
			}
		}
	}

	return chunk
}

func buildOpenAIStreamUsageChunk(base dto.ChatCompletionChunk, usage dto.Usage) dto.ChatCompletionChunk {
	return dto.ChatCompletionChunk{
		ID:      base.ID,
		Object:  base.Object,
		Created: base.Created,
		Model:   base.Model,
		Choices: []dto.ChunkChoice{},
		Usage:   &usage,
	}
}

// writeOllamaChatStreamAsOpenAIStream 读取 Ollama 行流，转换为 OpenAI Chat SSE 流写回客户端。
func writeOllamaChatStreamAsOpenAIStream(w http.ResponseWriter, resp *http.Response, counter TokenCounter, rmc ResponseModelContext) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("X-Accel-Buffering", "no")

	err := scanOllamaChatStream(resp.Body, func(chunk dto.ChatCompletionChunk) error {
		if rmc.RequestedModel != "" {
			chunk.Model = rmc.RequestedModel
		} else if chunk.Model == "" && rmc.WinnerUpstreamModel != "" {
			chunk.Model = rmc.WinnerUpstreamModel
		}

		if counter != nil {
			if sc, ok := counter.(*token.StreamCounter); ok {
				sc.AddOutputText(token.ExtractTextFromOpenAIChunk(&chunk))
			}
		}

		var usageChunk *dto.ChatCompletionChunk
		if chunk.Usage != nil {
			usage := *chunk.Usage
			chunk.Usage = nil
			if rmc.IncludeUsage {
				extra := buildOpenAIStreamUsageChunk(chunk, usage)
				usageChunk = &extra
			}
		}

		data, marshalErr := json.Marshal(chunk)
		if marshalErr != nil {
			return marshalErr
		}
		if _, writeErr := fmt.Fprintf(w, "data: %s\n\n", data); writeErr != nil {
			return writeErr
		}
		if usageChunk != nil {
			usageData, usageMarshalErr := json.Marshal(usageChunk)
			if usageMarshalErr != nil {
				return usageMarshalErr
			}
			if _, writeErr := fmt.Fprintf(w, "data: %s\n\n", usageData); writeErr != nil {
				return writeErr
			}
		}
		flusher.Flush()
		return nil
	})
	if err != nil {
		return err
	}

	// 发送 [DONE] 终止标记
	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.ComputeOutputTokens()
		}
	}

	return nil
}

// writeOllamaChatStreamAsAnthropicStream 读取 Ollama 行流，转换为 Anthropic SSE 流写回客户端。
func writeOllamaChatStreamAsAnthropicStream(w http.ResponseWriter, resp *http.Response, counter TokenCounter, rmc ResponseModelContext) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("X-Accel-Buffering", "no")

	mapper := newChatToClaudeStreamMapper(rmc.RequestedModel)

	err := scanOllamaChatStream(resp.Body, func(chunk dto.ChatCompletionChunk) error {
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
			if writeErr := writeClaudeEvent(w, event); writeErr != nil {
				return writeErr
			}
		}
		return nil
	})

	// 流结束，调用 Flush 发送剩余的 message_stop
	if err == nil {
		events, flushErr := mapper.Flush()
		if flushErr != nil {
			return flushErr
		}
		for _, event := range events {
			if writeErr := writeClaudeEvent(w, event); writeErr != nil {
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

// writeOllamaChatStreamAsResponsesStream 读取 Ollama 行流，转换为 OpenAI Responses API SSE 流写回客户端。
func writeOllamaChatStreamAsResponsesStream(w http.ResponseWriter, resp *http.Response, counter TokenCounter, rmc ResponseModelContext) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	mapper := newChatToResponsesStreamMapper("", rmc.RequestedModel)
	writer := newResponsesStreamWriter(w)

	err := scanOllamaChatStream(resp.Body, func(chunk dto.ChatCompletionChunk) error {
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
			if writeErr := writer.writeEvent(event); writeErr != nil {
				return writeErr
			}
		}
		return nil
	})

	// 流结束，调用 Flush 发送剩余的 response.completed
	if err == nil {
		events, flushErr := mapper.Flush()
		if flushErr != nil {
			return flushErr
		}
		for _, event := range events {
			if writeErr := writer.writeEvent(event); writeErr != nil {
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
