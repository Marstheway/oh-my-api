package codec

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/gin-gonic/gin"
)

// DeepSeekToolChoiceSingleFunctionCompatRule 标识 DeepSeek thinking 模式下
// 强制 tool_choice 的静态降级规则（单工具 function object / "required" → "auto"）。
const DeepSeekToolChoiceSingleFunctionCompatRule = "deepseek_tool_choice_single_function"

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

// applyDeepSeekOpenAIChatRequestCompat 是「出站 OpenAI Chat + DeepSeek」的唯一兼容入口。
// 一次 clone 完成 reasoning_content 补齐与 tool_choice 降级，不修改原始请求。
// thinkingEnabled 表示上游将处于 thinking 模式（DeepSeek 默认开启；仅显式关闭时为 false）。
func applyDeepSeekOpenAIChatRequestCompat(req *dto.ChatCompletionRequest, thinkingEnabled bool) *dto.ChatCompletionRequest {
	if req == nil {
		return req
	}
	out := cloneChatRequestWithDeepSeekCompat(req)
	if !thinkingEnabled || !shouldDowngradeDeepSeekToolChoice(out) {
		return out
	}
	out.ToolChoice = "auto"
	slog.Debug("deepseek tool_choice compatibility applied",
		"compat_rule", DeepSeekToolChoiceSingleFunctionCompatRule,
		"to", "auto",
	)
	return out
}

// shouldDowngradeDeepSeekToolChoice 判断是否应将强制 tool_choice 静态降级为 "auto"。
//
// DeepSeek thinking 模式不接受 tool_choice 为 function object 或 "required"
// （多家上游均报 400）。单工具场景下 "auto" 与强制指定语义近似等价
// （模型只有这一个工具可选），因此直接静态改写，无需探测重试。
// 多工具场景不降级，避免改写调用语义。
func shouldDowngradeDeepSeekToolChoice(req *dto.ChatCompletionRequest) bool {
	if req == nil || req.ToolChoice == nil || len(req.Tools) != 1 {
		return false
	}
	tool := req.Tools[0]
	toolName := strings.TrimSpace(tool.Function.Name)
	if tool.Type != "function" || toolName == "" {
		return false
	}

	switch choice := req.ToolChoice.(type) {
	case string:
		// "required" 强制调用某个工具；单工具时 auto 是 thinking 模式可接受的最近近似。
		// "auto"/"none" 本身合法，不改写。
		return strings.TrimSpace(choice) == "required"
	case map[string]any:
		if choice["type"] != "function" {
			return false
		}
		fn, ok := choice["function"].(map[string]any)
		if !ok {
			return false
		}
		name, _ := fn["name"].(string)
		name = strings.TrimSpace(name)
		// 强制指定的函数名必须与唯一工具一致，避免误降级名字不匹配的脏请求。
		return name != "" && name == toolName
	default:
		return false
	}
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
	if needsDeepSeekCompat {
		switch outbound {
		case FormatOpenAIChat:
			openaiReq = applyDeepSeekOpenAIChatRequestCompat(openaiReq, deepSeekChatThinkingEnabled(openaiReq, upstreamModel))
		case FormatOpenAIResponse:
			openaiReq = cloneChatRequestWithDeepSeekCompat(openaiReq)
		}
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
		if needsDeepSeekCompat {
			responseReq = applyDeepSeekOpenAIResponseCompat(responseReq, deepSeekChatThinkingEnabled(openaiReq, upstreamModel))
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
	return c.WriteResponseTo(ctx.Writer, outbound, resp, isStream, counter, rmc)
}

func (c *OpenAIChatCodec) WriteResponseTo(w http.ResponseWriter, outbound Format, resp *http.Response, isStream bool, counter TokenCounter, rmc ResponseModelContext) error {
	switch outbound {
	case FormatOpenAIChat:
		return passThroughOpenAIResponse(w, resp, isStream, counter, rmc)
	case FormatAnthropicMessages:
		if isStream {
			return writeClaudeStreamAsOpenAI(w, resp, counter, rmc)
		}
		return writeClaudeResponseAsOpenAI(w, resp, counter, rmc)
	case FormatOpenAIResponse:
		if isStream {
			if err := writeOpenAIResponseStreamAsChatStream(w, resp, counter, rmc); err != nil {
				return WrapConversionError("write_response", "response_to_chat",
					FormatOpenAIResponse, FormatOpenAIChat, "stream_conversion", err)
			}
			return nil
		}
		if err := writeOpenAIResponseAsChatResponse(w, resp, counter, rmc); err != nil {
			return WrapConversionError("write_response", "response_to_chat",
				FormatOpenAIResponse, FormatOpenAIChat, "response_conversion", err)
		}
		return nil
	case FormatOllamaChat:
		if isStream {
			return writeOllamaChatStreamAsOpenAIStream(w, resp, counter, rmc)
		}
		return writeOllamaChatResponseAsOpenAIChat(w, resp, counter, rmc)
	default:
		return fmt.Errorf("unsupported outbound format: %s", outbound)
	}
}
