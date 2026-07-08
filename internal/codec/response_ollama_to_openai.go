package codec

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"time"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
	"github.com/gin-gonic/gin"
)

// convertOllamaChatResponseToOpenAIChat 将 Ollama /api/chat 非流式响应转换为标准 OpenAI Chat 响应。
// 若 resp.Error 非空则视为错误响应并返回 conversion error。
func convertOllamaChatResponseToOpenAIChat(resp *dto.OllamaChatResponse) (*dto.ChatCompletionResponse, error) {
	if resp.Error != "" {
		return nil, fmt.Errorf("ollama upstream error: %s", resp.Error)
	}

	finishReason := "stop"
	if resp.DoneReason != "" {
		finishReason = resp.DoneReason
	}
	// 若存在 tool calls，将 finish_reason 改为 tool_calls
	if len(resp.Message.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}

	msg := &dto.ResMessage{
		Role:    "assistant",
		Content: resp.Message.Content,
	}
	if resp.Message.Thinking != "" {
		msg.ReasoningContent = resp.Message.Thinking
	}
	for i, tc := range resp.Message.ToolCalls {
		args := ""
		if len(tc.Function.Arguments) > 0 {
			args = string(tc.Function.Arguments)
		}
		msg.ToolCalls = append(msg.ToolCalls, dto.ToolCall{
			Index: &i,
			ID:    fmt.Sprintf("call_%08x_%d", rand.Uint32(), i),
			Type:  "function",
			Function: dto.ToolCallFunc{
				Name:      tc.Function.Name,
				Arguments: args,
			},
		})
	}

	out := &dto.ChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []dto.Choice{
			{
				Index:        0,
				Message:      msg,
				FinishReason: &finishReason,
			},
		},
		Usage: dto.Usage{
			PromptTokens:     resp.PromptEvalCount,
			CompletionTokens: resp.EvalCount,
			TotalTokens:      resp.PromptEvalCount + resp.EvalCount,
		},
	}
	return out, nil
}

// writeOllamaChatResponseAsOpenAIChat 读取 Ollama 非流式 body，转换为 OpenAI Chat 格式写回客户端。
func writeOllamaChatResponseAsOpenAIChat(c *gin.Context, resp *http.Response, counter TokenCounter, rmc ResponseModelContext) error {
	var ollamaResp dto.OllamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return WrapConversionError("write_response", "ollama_to_chat",
			FormatOllamaChat, FormatOpenAIChat, "response_unmarshal", err)
	}

	chatResp, err := convertOllamaChatResponseToOpenAIChat(&ollamaResp)
	if err != nil {
		return WrapConversionError("write_response", "ollama_to_chat",
			FormatOllamaChat, FormatOpenAIChat, "response_conversion", err)
	}

	if rmc.RequestedModel != "" {
		chatResp.Model = rmc.RequestedModel
	} else if chatResp.Model == "" && rmc.WinnerUpstreamModel != "" {
		chatResp.Model = rmc.WinnerUpstreamModel
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.AddOutputText(token.ExtractTextFromOpenAIResponse(chatResp))
			sc.ComputeOutputTokens()
		}
	}

	c.JSON(http.StatusOK, chatResp)
	return nil
}

// writeOllamaChatResponseAsAnthropic 读取 Ollama 非流式 body，转换为 Anthropic 格式写回客户端。
func writeOllamaChatResponseAsAnthropic(c *gin.Context, resp *http.Response, counter TokenCounter, rmc ResponseModelContext) error {
	var ollamaResp dto.OllamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return WrapConversionError("write_response", "ollama_to_anthropic",
			FormatOllamaChat, FormatAnthropicMessages, "response_unmarshal", err)
	}

	chatResp, err := convertOllamaChatResponseToOpenAIChat(&ollamaResp)
	if err != nil {
		return WrapConversionError("write_response", "ollama_to_anthropic",
			FormatOllamaChat, FormatAnthropicMessages, "response_conversion", err)
	}

	claudeResp := convertOpenAIResponseToAnthropic(chatResp)
	if rmc.RequestedModel != "" {
		claudeResp.Model = rmc.RequestedModel
	} else if claudeResp.Model == "" && rmc.WinnerUpstreamModel != "" {
		claudeResp.Model = rmc.WinnerUpstreamModel
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.AddOutputText(token.ExtractTextFromOpenAIResponse(chatResp))
			sc.ComputeOutputTokens()
		}
	}

	c.JSON(http.StatusOK, claudeResp)
	return nil
}

// writeOllamaChatResponseAsResponses 读取 Ollama 非流式 body，转换为 OpenAI Responses API 格式写回客户端。
func writeOllamaChatResponseAsResponses(c *gin.Context, resp *http.Response, counter TokenCounter, rmc ResponseModelContext) error {
	var ollamaResp dto.OllamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return WrapConversionError("write_response", "ollama_to_responses",
			FormatOllamaChat, FormatOpenAIResponse, "response_unmarshal", err)
	}

	chatResp, err := convertOllamaChatResponseToOpenAIChat(&ollamaResp)
	if err != nil {
		return WrapConversionError("write_response", "ollama_to_responses",
			FormatOllamaChat, FormatOpenAIResponse, "response_conversion", err)
	}

	responsesResp, err := convertOpenAIChatResponseToResponses(chatResp)
	if err != nil {
		return WrapConversionError("write_response", "ollama_to_responses",
			FormatOllamaChat, FormatOpenAIResponse, "chat_to_responses_conversion", err)
	}

	if rmc.RequestedModel != "" {
		responsesResp.Model = rmc.RequestedModel
	} else if responsesResp.Model == "" && rmc.WinnerUpstreamModel != "" {
		responsesResp.Model = rmc.WinnerUpstreamModel
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.AddOutputText(token.ExtractTextFromOpenAIResponse(chatResp))
			sc.ComputeOutputTokens()
		}
	}

	c.JSON(http.StatusOK, responsesResp)
	return nil
}
