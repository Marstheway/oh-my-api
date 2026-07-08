package codec

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/gin-gonic/gin"
)

// OpenAIResponseCodec 处理 /v1/responses 接口（OpenAI Responses API）。
type OpenAIResponseCodec struct{}

func init() {
	register(FormatOpenAIResponse, &OpenAIResponseCodec{})
}

func (c *OpenAIResponseCodec) Format() Format {
	return FormatOpenAIResponse
}

func (c *OpenAIResponseCodec) DecodeRequest(ctx *gin.Context) (any, error) {
	var req dto.ResponsesRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

func (c *OpenAIResponseCodec) EncodeRequest(outbound Format, req any, upstreamModel string, needsDeepSeekCompat bool) ([]byte, error) {
	responseReq, ok := req.(*dto.ResponsesRequest)
	if !ok {
		return nil, fmt.Errorf("invalid request type: %T", req)
	}
	switch outbound {
	case FormatOpenAIResponse:
		clone := *responseReq
		clone.Model = upstreamModel
		return json.Marshal(&clone)
	case FormatOpenAIChat:
		chatReq, err := convertResponseRequestToChatRequest(responseReq, upstreamModel)
		if err != nil {
			return nil, WrapConversionError("encode_request", "response_to_chat",
				FormatOpenAIResponse, FormatOpenAIChat, "request_conversion", err)
		}
		// DeepSeek 兼容：为 OpenAI chat 格式补 reasoning_content
		if needsDeepSeekCompat {
			chatReq = cloneChatRequestWithDeepSeekCompat(chatReq)
		}
		return json.Marshal(chatReq)
	case FormatAnthropicMessages:
		// 二次转换：response -> chat -> anthropic
		chatReq, err := convertResponseRequestToChatRequest(responseReq, upstreamModel)
		if err != nil {
			return nil, WrapConversionError("encode_request", "response_to_chat",
				FormatOpenAIResponse, FormatAnthropicMessages, "request_conversion_first_hop", err)
		}
		anthropicReq, err := convertOpenAIToAnthropicRequest(chatReq, upstreamModel, needsDeepSeekCompat)
		if err != nil {
			return nil, err
		}
		return json.Marshal(anthropicReq)
	case FormatOllamaChat:
		// 二次转换：response -> chat -> ollama
		chatReq, err := convertResponseRequestToChatRequest(responseReq, upstreamModel)
		if err != nil {
			return nil, WrapConversionError("encode_request", "response_to_ollama_via_chat",
				FormatOpenAIResponse, FormatOllamaChat, "request_conversion_first_hop", err)
		}
		ollamaReq, err := convertOpenAIToOllamaChatRequest(chatReq, upstreamModel)
		if err != nil {
			return nil, WrapConversionError("encode_request", "response_to_ollama_via_chat",
				FormatOpenAIResponse, FormatOllamaChat, "request_conversion", err)
		}
		return json.Marshal(ollamaReq)
	default:
		return nil, fmt.Errorf("unsupported outbound format: %s", outbound)
	}
}

func (c *OpenAIResponseCodec) WriteResponse(ctx *gin.Context, outbound Format, resp *http.Response, isStream bool, counter TokenCounter, rmc ResponseModelContext) error {
	switch outbound {
	case FormatOpenAIResponse:
		// 直通：直接把上游 Responses API 响应写回客户端，同时统计 token/latency。
		return passThroughResponsesResponse(ctx, resp, isStream, counter, rmc)
	case FormatOpenAIChat:
		if isStream {
			if err := writeOpenAIChatStreamAsResponses(ctx, resp, counter, rmc.RequestedModel); err != nil {
				return WrapConversionError("write_response", "chat_to_response",
					FormatOpenAIChat, FormatOpenAIResponse, "stream_conversion", err)
			}
			return nil
		}
		if err := writeOpenAIChatResponseAsResponses(ctx, resp, counter, rmc); err != nil {
			return WrapConversionError("write_response", "chat_to_response",
				FormatOpenAIChat, FormatOpenAIResponse, "response_conversion", err)
		}
		return nil
	case FormatAnthropicMessages:
		if isStream {
			// Event-by-event bridge: ClaudeEvent -> ChatChunk -> ResponsesEvent -> flush
			return writeAnthropicStreamAsResponsesStream(ctx, resp, counter, rmc.RequestedModel)
		}
		// 非流式：读取 claude body → chat 对象 → responses 对象 → 写回
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return WrapConversionError("write_response", "anthropic_to_response_via_chat",
				FormatAnthropicMessages, FormatOpenAIResponse, "response_read", err)
		}
		var claudeResp dto.ClaudeResponse
		if err := json.Unmarshal(body, &claudeResp); err != nil {
			return WrapConversionError("write_response", "anthropic_to_response_via_chat",
				FormatAnthropicMessages, FormatOpenAIResponse, "response_unmarshal", err)
		}
		chatResp := convertClaudeResponseToOpenAI(&claudeResp)
		responsesResp, err := convertOpenAIChatResponseToResponses(chatResp)
		if err != nil {
			return WrapConversionError("write_response", "chat_to_response",
				FormatAnthropicMessages, FormatOpenAIResponse, "response_conversion", err)
		}
		if rmc.RequestedModel != "" {
			responsesResp.Model = rmc.RequestedModel
		}
		ctx.JSON(http.StatusOK, responsesResp)
		return nil
	case FormatOllamaChat:
		if isStream {
			return writeOllamaChatStreamAsResponsesStream(ctx, resp, counter, rmc)
		}
		return writeOllamaChatResponseAsResponses(ctx, resp, counter, rmc)
	default:
		return fmt.Errorf("unsupported outbound format: %s", outbound)
	}
}
