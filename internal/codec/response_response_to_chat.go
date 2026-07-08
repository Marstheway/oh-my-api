package codec

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
	"github.com/gin-gonic/gin"
)

// convertOpenAIResponseToChat 将 Responses API 非流式响应转换为 Chat 格式。
func convertOpenAIResponseToChat(resp *dto.ResponsesResponse) (*dto.ChatCompletionResponse, error) {
	if resp.Status == "failed" || resp.Error != nil {
		msg := "response failed"
		if resp.Error != nil {
			msg = fmt.Sprintf("response error [%s]: %s", resp.Error.Code, resp.Error.Message)
		}
		return nil, fmt.Errorf("%s", msg)
	}

	msg := &dto.ResMessage{
		Role:    "assistant",
		Content: "",
	}

	// 两段文本提取：优先 assistant message，否则 fallback 到全部 output text
	extractedText := extractOutputTextFromResponses(resp)

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			// message 文本已在 extractOutputTextFromResponses 中处理；这里仅校验已支持类型。
			for _, part := range item.Content {
				switch part.Type {
				case "output_text", "refusal":
					continue
				default:
					return nil, fmt.Errorf("unsupported output content part type: %s", part.Type)
				}
			}
		case "function_call":
			name := strings.TrimSpace(item.Name)
			if name == "" {
				continue
			}
			callID := strings.TrimSpace(item.CallID)
			if callID == "" {
				callID = strings.TrimSpace(item.ID)
			}
			msg.ToolCalls = append(msg.ToolCalls, dto.ToolCall{
				ID:   callID,
				Type: "function",
				Function: dto.ToolCallFunc{
					Name:      name,
					Arguments: item.Arguments,
				},
			})
		case "reasoning":
			// 提取 reasoning content（详细推理过程）
			var reasoningText string
			for _, c := range item.Content {
				if c.Type == "reasoning_text" && c.Text != "" {
					reasoningText += c.Text
				}
			}
			// 提取 summary（摘要）
			var summaryItems []map[string]any
			if len(item.Summary) > 0 {
				if err := json.Unmarshal(item.Summary, &summaryItems); err == nil {
					for _, s := range summaryItems {
						if s["type"] == "summary_text" {
							if text, ok := s["text"].(string); ok && strings.TrimSpace(text) != "" {
								msg.ReasoningContent += text
							}
						}
					}
				}
			}
			// 将详细推理内容前置拼接到 reasoning_content（先详细推理，后摘要）
			if reasoningText != "" {
				msg.ReasoningContent = reasoningText + msg.ReasoningContent
			}
		default:
			// 未知类型优雅跳过，记录 Debug 日志
			slog.Debug("skipping unknown response output type", "type", item.Type, "id", item.ID)
			continue
		}
	}

	msg.Content = extractedText

	// Usage 映射：对齐参考实现的 UsageFromResponsesUsage
	usage := usageFromResponsesUsage(&resp.Usage)

	finishReason := "stop"
	if mappedReason, ok := responsesFinishReasonFromStatus(resp); ok {
		finishReason = mappedReason
	} else if len(msg.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}

	return &dto.ChatCompletionResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: resp.CreatedAt,
		Model:   resp.Model,
		Choices: []dto.Choice{{
			Index:        0,
			Message:      msg,
			FinishReason: &finishReason,
		}},
		Usage: usage,
	}, nil
}

// extractOutputTextFromResponses 对齐参考实现的 ExtractOutputTextFromResponses：
// 第一段：只取 output[].type=="message" 且 role 为空或 assistant 的文本；
// 如果第一段没有拿到任何文本，第二段 fallback 遍历全部 output 的 content[].text。
func extractOutputTextFromResponses(resp *dto.ResponsesResponse) string {
	if resp == nil || len(resp.Output) == 0 {
		return ""
	}
	var sb strings.Builder

	// 第一段：优先取 assistant message 的文本
	for _, out := range resp.Output {
		if out.Type != "message" {
			continue
		}
		if out.Role != "" && out.Role != "assistant" {
			continue
		}
		for _, c := range out.Content {
			if c.Type == "output_text" && c.Text != "" {
				sb.WriteString(c.Text)
			}
		}
	}
	if sb.Len() > 0 {
		return sb.String()
	}

	// 第二段 fallback：遍历所有 output content text
	for _, out := range resp.Output {
		for _, c := range out.Content {
			if c.Text != "" {
				sb.WriteString(c.Text)
			}
		}
	}
	return sb.String()
}

// usageFromResponsesUsage 对齐参考实现的 UsageFromResponsesUsage：
// 映射 input_tokens_details 和 completion_tokens_details 到 Chat Usage 细节。
func usageFromResponsesUsage(src *dto.ResponsesUsage) dto.Usage {
	usage := dto.Usage{
		PromptTokens:     src.InputTokens,
		CompletionTokens: src.OutputTokens,
		TotalTokens:      src.TotalTokens,
	}
	// TotalTokens 为零时采用总和兜底
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}

	// 映射 input_tokens_details -> PromptTokensDetails
	if src.InputTokensDetails != nil {
		if usage.PromptTokensDetails == nil {
			usage.PromptTokensDetails = &dto.UsageDetails{}
		}
		usage.PromptTokensDetails.CachedTokens = src.InputTokensDetails.CachedTokens
		usage.PromptTokensDetails.ImageTokens = src.InputTokensDetails.ImageTokens
		usage.PromptTokensDetails.AudioTokens = src.InputTokensDetails.AudioTokens
	}

	// 映射 completion_tokens_details.reasoning_tokens -> CompletionTokensDetails.ReasoningTokens
	if src.CompletionTokensDetails != nil && src.CompletionTokensDetails.ReasoningTokens != 0 {
		if usage.CompletionTokensDetails == nil {
			usage.CompletionTokensDetails = &dto.UsageDetails{}
		}
		usage.CompletionTokensDetails.ReasoningTokens = src.CompletionTokensDetails.ReasoningTokens
	}

	return usage
}

// responsesFinishReasonFromStatus 对齐参考实现的 ResponsesFinishReasonFromStatus：
// status 为 "incomplete" 时，根据 incomplete_details.reason 映射 finish_reason；
// 返回空字符串和 false 表示不应使用状态映射。
func responsesFinishReasonFromStatus(resp *dto.ResponsesResponse) (string, bool) {
	if resp == nil || resp.Status != "incomplete" {
		return "", false
	}
	reason := ""
	if resp.IncompleteDetails != nil {
		reason = strings.TrimSpace(resp.IncompleteDetails.Reason)
	}
	if reason == "content_filter" {
		return "content_filter", true
	}
	return "length", true
}

// writeOpenAIResponseAsChatResponse 读取 Responses API 格式的响应体，转换后以 Chat 格式写回客户端。
func writeOpenAIResponseAsChatResponse(c *gin.Context, resp *http.Response, counter TokenCounter, rmc ResponseModelContext) error {
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
				switch item.Type {
				case "message":
					for _, part := range item.Content {
						if part.Type == "output_text" {
							sc.AddOutputText(part.Text)
						}
					}
				case "function_call":
					if item.Arguments != "" {
						sc.AddOutputText(item.Arguments)
					}
				}
			}
			sc.ComputeOutputTokens()
		}
	}

	chatResp, err := convertOpenAIResponseToChat(&responsesResp)
	if err != nil {
		return err
	}

	if rmc.RequestedModel != "" {
		chatResp.Model = rmc.RequestedModel
	}

	c.JSON(http.StatusOK, chatResp)
	return nil
}

// writeOpenAIResponseStreamAsChatStream 读取 Responses API SSE 流，转换后以 Chat SSE 格式写回客户端。
func writeOpenAIResponseStreamAsChatStream(c *gin.Context, resp *http.Response, counter TokenCounter, requestedModel string) error {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	mapper := newResponsesToChatStreamMapper("", requestedModel, 0)
	err := scanSSEData(resp.Body, func(data string) error {
		var event dto.ResponsesStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return nil
		}
		if counter != nil {
			if sc, ok := counter.(*token.StreamCounter); ok {
				switch event.Type {
				case "response.output_text.delta", "response.function_call_arguments.delta":
					var delta string
					if len(event.Delta) > 0 {
						_ = json.Unmarshal(event.Delta, &delta)
					}
					if delta != "" {
						sc.AddOutputText(delta)
					}
				}
			}
		}
		chunks, err := mapper.Map(event)
		if err != nil {
			return err
		}
		for _, chunk := range chunks {
			payload, err := json.Marshal(chunk)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", payload); err != nil {
				return err
			}
			flusher.Flush()
		}
		if event.Type == "response.completed" {
			if _, err := fmt.Fprintf(c.Writer, "data: [DONE]\n\n"); err != nil {
				return err
			}
			flusher.Flush()
		}
		return nil
	})

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.ComputeOutputTokens()
		}
	}

	return err
}
