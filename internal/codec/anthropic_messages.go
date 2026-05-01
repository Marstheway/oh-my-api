package codec

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/gin-gonic/gin"
)

type AnthropicMessagesCodec struct{}

func init() {
	register(FormatAnthropicMessages, &AnthropicMessagesCodec{})
}

func (c *AnthropicMessagesCodec) Format() Format {
	return FormatAnthropicMessages
}

func (c *AnthropicMessagesCodec) DecodeRequest(ctx *gin.Context) (any, error) {
	var req dto.ClaudeRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

func (c *AnthropicMessagesCodec) EncodeRequest(outbound Format, req any, upstreamModel string) ([]byte, error) {
	claudeReq, ok := req.(*dto.ClaudeRequest)
	if !ok {
		return nil, fmt.Errorf("invalid anthropic request type")
	}

	switch outbound {
	case FormatAnthropicMessages:
		clone := *claudeReq
		clone.Model = upstreamModel
		return json.Marshal(&clone)
	case FormatOpenAIChat:
		openaiReq := convertAnthropicToOpenAIRequest(claudeReq, upstreamModel)
		return json.Marshal(openaiReq)
	case FormatOpenAIResponse:
		// 二次转换：anthropic -> chat -> response
		chatReq := convertAnthropicToOpenAIRequest(claudeReq, upstreamModel)
		responseReq, err := convertChatToResponseRequest(chatReq, upstreamModel)
		if err != nil {
			return nil, WrapConversionError("encode_request", "anthropic_to_response_via_chat",
				FormatAnthropicMessages, FormatOpenAIResponse, "request_conversion_second_hop", err)
		}
		return json.Marshal(responseReq)
	default:
		return nil, fmt.Errorf("unsupported outbound format: %s", outbound)
	}
}

func (c *AnthropicMessagesCodec) WriteResponse(ctx *gin.Context, outbound Format, resp *http.Response, isStream bool, counter TokenCounter) error {
	switch outbound {
	case FormatAnthropicMessages:
		return passThroughAnthropicResponse(ctx, resp, isStream, counter)
	case FormatOpenAIChat:
		if isStream {
			return writeOpenAIStreamAsAnthropic(ctx, resp, counter)
		}
		return writeOpenAIResponseAsAnthropic(ctx, resp, counter)
	case FormatOpenAIResponse:
		if isStream {
			// Event-by-event bridge: ResponsesEvent -> ChatChunk -> ClaudeEvent -> flush
			return writeResponsesStreamAsClaudeStream(ctx, resp, counter)
		}
		// 非流式：读取 response body → chat 对象 → claude 对象 → 写回
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return WrapConversionError("write_response", "response_to_chat",
				FormatOpenAIResponse, FormatAnthropicMessages, "response_read", err)
		}
		var responsesResp dto.ResponsesResponse
		if err := json.Unmarshal(body, &responsesResp); err != nil {
			return WrapConversionError("write_response", "response_to_chat",
				FormatOpenAIResponse, FormatAnthropicMessages, "response_unmarshal", err)
		}
		chatResp, err := convertOpenAIResponseToChat(&responsesResp)
		if err != nil {
			return WrapConversionError("write_response", "response_to_chat",
				FormatOpenAIResponse, FormatAnthropicMessages, "response_conversion", err)
		}
		claudeResp := convertOpenAIResponseToAnthropic(chatResp)
		if counter != nil {
			counter.SetLatency()
		}
		ctx.JSON(http.StatusOK, claudeResp)
		return nil
	default:
		return fmt.Errorf("unsupported outbound format: %s", outbound)
	}
}
