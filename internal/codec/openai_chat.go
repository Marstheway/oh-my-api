package codec

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/gin-gonic/gin"
)

// NeedsDeepSeekCompat 判断 upstream model 是否需要 thinking/reasoning replay 兼容。
// 当 upstreamModel 大小写无关地包含 "deepseek" 时返回 true。
// 该函数被 codec 和 handler 包使用，用于自动判断是否启用兼容逻辑。
func NeedsDeepSeekCompat(upstreamModel string) bool {
	return strings.Contains(strings.ToLower(upstreamModel), "deepseek")
}

// hasAssistantToolCalls 判断消息是否为包含 tool_calls 的 assistant 消息。
func hasAssistantToolCalls(msg dto.Message) bool {
	return msg.Role == "assistant" && len(msg.ToolCalls) > 0
}

// cloneChatRequestWithDeepSeekCompat 为 DeepSeek 上游补齐缺失的 reasoning_content。
// 仅对包含 tool_calls 的 assistant 消息补 " " 占位值，已有 reasoning_content 的原样保留。
// 返回请求副本，不修改原始请求。
func cloneChatRequestWithDeepSeekCompat(req *dto.ChatCompletionRequest) *dto.ChatCompletionRequest {
	clone := *req
	if len(req.Messages) == 0 {
		return &clone
	}

	clone.Messages = make([]dto.Message, len(req.Messages))
	for i, msg := range req.Messages {
		clone.Messages[i] = msg
		if !hasAssistantToolCalls(msg) {
			continue
		}
		// 仅当 assistant tool_calls 消息缺少 reasoning_content 或为空字符串时补占位值
		if msg.ReasoningContent == nil || *msg.ReasoningContent == "" {
			pad := " "
			clone.Messages[i].ReasoningContent = &pad
		}
	}

	return &clone
}

type OpenAIChatCodec struct{}

func init() {
	register(FormatOpenAIChat, &OpenAIChatCodec{})
}

func (c *OpenAIChatCodec) Format() Format {
	return FormatOpenAIChat
}

func (c *OpenAIChatCodec) DecodeRequest(ctx *gin.Context) (any, error) {
	var req dto.ChatCompletionRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

func (c *OpenAIChatCodec) EncodeRequest(outbound Format, req any, upstreamModel string, needsDeepSeekCompat bool) ([]byte, error) {
	openaiReq, ok := req.(*dto.ChatCompletionRequest)
	if !ok {
		return nil, fmt.Errorf("invalid openai request type")
	}
	if needsDeepSeekCompat && outbound == FormatOpenAIChat {
		openaiReq = cloneChatRequestWithDeepSeekCompat(openaiReq)
	}

	switch outbound {
	case FormatOpenAIChat:
		clone := *openaiReq
		clone.Model = upstreamModel
		return json.Marshal(&clone)
	case FormatAnthropicMessages:
		claudeReq, err := convertOpenAIToAnthropicRequest(openaiReq, upstreamModel, needsDeepSeekCompat)
		if err != nil {
			return nil, err
		}
		return json.Marshal(claudeReq)
	case FormatOpenAIResponse:
		responseReq, err := convertChatToResponseRequest(openaiReq, upstreamModel)
		if err != nil {
			return nil, WrapConversionError("encode_request", "chat_to_response",
				FormatOpenAIChat, FormatOpenAIResponse, "request_conversion", err)
		}
		return json.Marshal(responseReq)
	case FormatOllamaChat:
		ollamaReq, err := convertOpenAIToOllamaChatRequest(openaiReq, upstreamModel)
		if err != nil {
			return nil, WrapConversionError("encode_request", "chat_to_ollama",
				FormatOpenAIChat, FormatOllamaChat, "request_conversion", err)
		}
		return json.Marshal(ollamaReq)
	default:
		return nil, fmt.Errorf("unsupported outbound format: %s", outbound)
	}
}

func (c *OpenAIChatCodec) WriteResponse(ctx *gin.Context, outbound Format, resp *http.Response, isStream bool, counter TokenCounter, rmc ResponseModelContext) error {
	switch outbound {
	case FormatOpenAIChat:
		return passThroughOpenAIResponse(ctx, resp, isStream, counter, rmc)
	case FormatAnthropicMessages:
		if isStream {
			return writeClaudeStreamAsOpenAI(ctx, resp, counter, rmc.RequestedModel)
		}
		return writeClaudeResponseAsOpenAI(ctx, resp, counter, rmc)
	case FormatOpenAIResponse:
		if isStream {
			if err := writeOpenAIResponseStreamAsChatStream(ctx, resp, counter, rmc.RequestedModel); err != nil {
				return WrapConversionError("write_response", "response_to_chat",
					FormatOpenAIResponse, FormatOpenAIChat, "stream_conversion", err)
			}
			return nil
		}
		if err := writeOpenAIResponseAsChatResponse(ctx, resp, counter, rmc); err != nil {
			return WrapConversionError("write_response", "response_to_chat",
				FormatOpenAIResponse, FormatOpenAIChat, "response_conversion", err)
		}
		return nil
	case FormatOllamaChat:
		if isStream {
			return writeOllamaChatStreamAsOpenAIStream(ctx, resp, counter, rmc)
		}
		return writeOllamaChatResponseAsOpenAIChat(ctx, resp, counter, rmc)
	default:
		return fmt.Errorf("unsupported outbound format: %s", outbound)
	}
}