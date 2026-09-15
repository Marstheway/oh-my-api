package codec

import (
	"encoding/json"
	"fmt"
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

// cloneClaudeRequestWithDeepSeekCompat 为 DeepSeek 上游补齐缺失的 thinking block。
// 仅对包含 tool_use 的 assistant 消息补 thinking 占位值，已有 thinking block 的原样保留。
// 返回请求副本，不修改原始请求。
func cloneClaudeRequestWithDeepSeekCompat(req *dto.ClaudeRequest) *dto.ClaudeRequest {
	clone := *req
	if len(req.Messages) == 0 {
		return &clone
	}

	clone.Messages = make([]dto.ClaudeMessage, len(req.Messages))
	for i, msg := range req.Messages {
		clone.Messages[i] = msg
		if msg.Role != "assistant" {
			continue
		}

		// 根据内容形态分别处理
		switch content := msg.Content.(type) {
		case []dto.ContentBlock:
			// []dto.ContentBlock 形态的处理逻辑
			blocks := content

			// 检查是否有 tool_use 和 thinking block 的状态
			hasToolUse := false
			var emptyThinkingIndex int = -1 // 记录空 thinking block 的位置
			for idx, b := range blocks {
				if b.Type == "tool_use" {
					hasToolUse = true
				}
				if b.Type == "thinking" {
					// 如果 thinking 内容为空或缺失，记录位置以便填充
					if b.Thinking == nil || *b.Thinking == "" {
						emptyThinkingIndex = idx
					}
				}
			}

			// 没有 tool_use，不需要补位
			if !hasToolUse {
				continue
			}

			// 已有非空 thinking，无需处理
			hasNonEmptyThinking := false
			for _, b := range blocks {
				if b.Type == "thinking" && b.Thinking != nil && *b.Thinking != "" {
					hasNonEmptyThinking = true
					break
				}
			}
			if hasNonEmptyThinking {
				continue
			}

			// 情况1：有空 thinking block，填充内容并保留 signature
			if emptyThinkingIndex >= 0 {
				newBlocks := make([]dto.ContentBlock, len(blocks))
				for idx, b := range blocks {
					newBlocks[idx] = b
					if idx == emptyThinkingIndex {
						pad := " "
						newBlocks[idx].Thinking = &pad
						// Signature 字段已在复制时保留，无需额外处理
					}
				}
				clone.Messages[i].Content = newBlocks
				continue
			}

			// 情况2：没有 thinking block，插入新的
			insertIdx := len(blocks)
			for idx, b := range blocks {
				if b.Type == "tool_use" {
					insertIdx = idx
					break
				}
			}
			newBlocks := make([]dto.ContentBlock, len(blocks)+1)
			copy(newBlocks, blocks[:insertIdx])
			pad := " "
			newBlocks[insertIdx] = dto.ContentBlock{Type: "thinking", Thinking: &pad}
			copy(newBlocks[insertIdx+1:], blocks[insertIdx:])
			clone.Messages[i].Content = newBlocks

		case []any:
			// []any 形态：直接在 map 上操作，避免转换丢失字段
			items := content

			// 检查是否有 tool_use 和 thinking
			hasToolUse := false
			var emptyThinkingIdx int = -1
			for idx, item := range items {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				typ, _ := m["type"].(string)
				if typ == "tool_use" {
					hasToolUse = true
				}
				if typ == "thinking" {
					thinking, hasThinking := m["thinking"]
					if !hasThinking || thinking == "" {
						emptyThinkingIdx = idx
					}
				}
			}

			// 没有 tool_use，不需要补位
			if !hasToolUse {
				continue
			}

			// 检查是否有非空 thinking
			hasNonEmptyThinking := false
			for _, item := range items {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if typ, _ := m["type"].(string); typ == "thinking" {
					if thinking, ok := m["thinking"].(string); ok && thinking != "" {
						hasNonEmptyThinking = true
						break
					}
				}
			}
			if hasNonEmptyThinking {
				continue
			}

			// 深拷贝整个切片
			newItems := make([]any, len(items))
			for idx, item := range items {
				if m, ok := item.(map[string]any); ok {
					newMap := make(map[string]any, len(m))
					for k, v := range m {
						newMap[k] = v
					}
					newItems[idx] = newMap
				} else {
					newItems[idx] = item
				}
			}

			// 情况1：有空 thinking，填充内容
			if emptyThinkingIdx >= 0 {
				if m, ok := newItems[emptyThinkingIdx].(map[string]any); ok {
					m["thinking"] = " "
				}
				clone.Messages[i].Content = newItems
				continue
			}

			// 情况2：没有 thinking，插入新的
			insertIdx := len(newItems)
			for idx, item := range newItems {
				if m, ok := item.(map[string]any); ok {
					if typ, _ := m["type"].(string); typ == "tool_use" {
						insertIdx = idx
						break
					}
				}
			}
			newBlock := map[string]any{"type": "thinking", "thinking": " "}
			newItems = append(newItems, nil)
			copy(newItems[insertIdx+1:], newItems[insertIdx:])
			newItems[insertIdx] = newBlock
			clone.Messages[i].Content = newItems

		default:
			continue
		}
	}

	return &clone
}

func (c *AnthropicMessagesCodec) EncodeRequest(outbound Format, req any, upstreamModel string, needsDeepSeekCompat bool) ([]byte, error) {
	claudeReq, ok := req.(*dto.ClaudeRequest)
	if !ok {
		return nil, fmt.Errorf("invalid anthropic request type")
	}

	switch outbound {
	case FormatAnthropicMessages:
		// DeepSeek passthrough: 补齐缺失的 thinking block
		if needsDeepSeekCompat {
			claudeReq = cloneClaudeRequestWithDeepSeekCompat(claudeReq)
		}
		clone := *claudeReq
		clone.Model = upstreamModel
		return json.Marshal(&clone)
	case FormatOpenAIChat:
		openaiReq := convertAnthropicToOpenAIRequest(claudeReq, upstreamModel)
		// DeepSeek 转换：补齐 reasoning_content，并仅在推理模式下降级强制工具选择。
		if needsDeepSeekCompat {
			openaiReq = applyDeepSeekOpenAIChatRequestCompat(openaiReq, deepSeekClaudeThinkingEnabled(claudeReq, upstreamModel))
		}
		return json.Marshal(openaiReq)
	case FormatOpenAIResponse:
		// 二次转换：anthropic -> chat -> response
		chatReq := convertAnthropicToOpenAIRequest(claudeReq, upstreamModel)
		responseReq, err := convertChatToResponseRequest(chatReq, upstreamModel)
		if err != nil {
			return nil, WrapConversionError("encode_request", "anthropic_to_response_via_chat",
				FormatAnthropicMessages, FormatOpenAIResponse, "request_conversion_second_hop", err)
		}
		if needsDeepSeekCompat {
			responseReq = applyDeepSeekOpenAIResponseCompat(responseReq, deepSeekClaudeThinkingEnabled(claudeReq, upstreamModel))
		}
		return json.Marshal(responseReq)
	case FormatOllamaChat:
		// 二次转换：anthropic -> chat -> ollama
		chatReq := convertAnthropicToOpenAIRequest(claudeReq, upstreamModel)
		ollamaReq, err := convertOpenAIToOllamaChatRequest(chatReq, upstreamModel)
		if err != nil {
			return nil, WrapConversionError("encode_request", "anthropic_to_ollama_via_chat",
				FormatAnthropicMessages, FormatOllamaChat, "request_conversion", err)
		}
		return json.Marshal(ollamaReq)
	default:
		return nil, fmt.Errorf("unsupported outbound format: %s", outbound)
	}
}

func (c *AnthropicMessagesCodec) WriteResponse(ctx *gin.Context, outbound Format, resp *http.Response, isStream bool, counter TokenCounter, rmc ResponseModelContext) error {
	return c.WriteResponseTo(ctx.Writer, outbound, resp, isStream, counter, rmc)
}

func (c *AnthropicMessagesCodec) WriteResponseTo(w http.ResponseWriter, outbound Format, resp *http.Response, isStream bool, counter TokenCounter, rmc ResponseModelContext) error {
	switch outbound {
	case FormatAnthropicMessages:
		return passThroughAnthropicResponse(w, resp, isStream, counter, rmc)
	case FormatOpenAIChat:
		if isStream {
			return writeOpenAIStreamAsAnthropic(w, resp, counter, rmc.RequestedModel)
		}
		return writeOpenAIResponseAsAnthropic(w, resp, counter, rmc)
	case FormatOpenAIResponse:
		if isStream {
			// Event-by-event bridge: ResponsesEvent -> ChatChunk -> ClaudeEvent -> flush
			return writeResponsesStreamAsClaudeStream(w, resp, counter, rmc.RequestedModel)
		}
		// 非流式：读取 response body -> chat 对象 -> claude 对象 -> 写回
		// 复用 readResponsesResponseForNonStream 以兼容上游返回 SSE 的场景（如本地代理）。
		responsesResp, err := readResponsesResponseForNonStream(resp, counter)
		if err != nil {
			return WrapConversionError("write_response", "response_to_chat",
				FormatOpenAIResponse, FormatAnthropicMessages, "response_read", err)
		}
		chatResp, err := convertOpenAIResponseToChat(responsesResp)
		if err != nil {
			return WrapConversionError("write_response", "response_to_chat",
				FormatOpenAIResponse, FormatAnthropicMessages, "response_conversion", err)
		}
		claudeResp := convertOpenAIResponseToAnthropic(chatResp)
		if rmc.RequestedModel != "" {
			claudeResp.Model = rmc.RequestedModel
		}
		return writeJSON(w, http.StatusOK, claudeResp)
	case FormatOllamaChat:
		if isStream {
			return writeOllamaChatStreamAsAnthropicStream(w, resp, counter, rmc)
		}
		return writeOllamaChatResponseAsAnthropic(w, resp, counter, rmc)
	default:
		return fmt.Errorf("unsupported outbound format: %s", outbound)
	}
}
