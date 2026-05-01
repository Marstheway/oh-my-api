package codec

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/gin-gonic/gin"
)


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

func (c *OpenAIChatCodec) EncodeRequest(outbound Format, req any, upstreamModel string) ([]byte, error) {
	openaiReq, ok := req.(*dto.ChatCompletionRequest)
	if !ok {
		return nil, fmt.Errorf("invalid openai request type")
	}

	switch outbound {
	case FormatOpenAIChat:
		clone := *openaiReq
		clone.Model = upstreamModel
		return json.Marshal(&clone)
	case FormatAnthropicMessages:
		claudeReq := convertOpenAIToAnthropicRequest(openaiReq, upstreamModel)
		return json.Marshal(claudeReq)
	case FormatOpenAIResponse:
		responseReq, err := convertChatToResponseRequest(openaiReq, upstreamModel)
		if err != nil {
			return nil, WrapConversionError("encode_request", "chat_to_response",
				FormatOpenAIChat, FormatOpenAIResponse, "request_conversion", err)
		}
		return json.Marshal(responseReq)
	default:
		return nil, fmt.Errorf("unsupported outbound format: %s", outbound)
	}
}

func (c *OpenAIChatCodec) WriteResponse(ctx *gin.Context, outbound Format, resp *http.Response, isStream bool, counter TokenCounter) error {
	switch outbound {
	case FormatOpenAIChat:
		return passThroughOpenAIResponse(ctx, resp, isStream, counter)
	case FormatAnthropicMessages:
		if isStream {
			return writeClaudeStreamAsOpenAI(ctx, resp, counter)
		}
		return writeClaudeResponseAsOpenAI(ctx, resp, counter)
	case FormatOpenAIResponse:
		if isStream {
			if err := writeOpenAIResponseStreamAsChatStream(ctx, resp, counter); err != nil {
				return WrapConversionError("write_response", "response_to_chat",
					FormatOpenAIResponse, FormatOpenAIChat, "stream_conversion", err)
			}
			return nil
		}
		if err := writeOpenAIResponseAsChatResponse(ctx, resp, counter); err != nil {
			return WrapConversionError("write_response", "response_to_chat",
				FormatOpenAIResponse, FormatOpenAIChat, "response_conversion", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported outbound format: %s", outbound)
	}
}
