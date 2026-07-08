package codec

import (
	"encoding/json"
	"fmt"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

// convertOpenAIToOllamaChatRequest 将标准 OpenAI Chat 请求转换为 OllamaChatRequest。
// 所有入方向（OpenAI Chat、Anthropic、OpenAI Response）均必须先规范化到 OpenAI Chat 语义，
// 再通过此唯一 helper 完成转换，不得绕过或复制。
func convertOpenAIToOllamaChatRequest(req *dto.ChatCompletionRequest, upstreamModel string) (*dto.OllamaChatRequest, error) {
	out := &dto.OllamaChatRequest{
		Model:  upstreamModel,
		Stream: req.Stream,
	}

	// 预扫描 assistant 消息，建立 callID -> toolName 映射，用于 tool result 消息
	callIDToName := buildCallIDToNameMap(req.Messages)

	// 消息转换，保持输入顺序
	out.Messages = make([]dto.OllamaChatMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		ollamaMsg, err := convertOpenAIMessageToOllama(msg, callIDToName)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, ollamaMsg)
	}

	// response_format 映射
	if req.ResponseFormat != nil {
		format, err := convertResponseFormatToOllama(req.ResponseFormat)
		if err != nil {
			return nil, err
		}
		out.Format = format
	}

	// tools 转换
	if len(req.Tools) > 0 {
		out.Tools = make([]dto.OllamaTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			out.Tools = append(out.Tools, dto.OllamaTool{
				Type: t.Type,
				Function: dto.OllamaToolFunction{
					Name:        t.Function.Name,
					Description: t.Function.Description,
					Parameters:  t.Function.Parameters,
				},
			})
		}
	}

	// think 字段优先级：Think > EnableThinking > 省略
	if len(req.Think) > 0 {
		out.Think = req.Think
	} else if len(req.EnableThinking) > 0 {
		out.Think = req.EnableThinking
	}

	// 采样参数映射到 Options
	opts := make(map[string]any)
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		opts["top_p"] = *req.TopP
	}
	if req.TopK != nil {
		opts["top_k"] = *req.TopK
	}
	if req.FrequencyPenalty != nil {
		opts["frequency_penalty"] = *req.FrequencyPenalty
	}
	if req.PresencePenalty != nil {
		opts["presence_penalty"] = *req.PresencePenalty
	}
	if req.Seed != nil {
		opts["seed"] = *req.Seed
	}
	if len(req.Stop) > 0 {
		opts["stop"] = []string(req.Stop)
	}
	// max_tokens / max_completion_tokens 均映射到 num_predict，max_tokens 优先
	if req.MaxTokens > 0 {
		opts["num_predict"] = req.MaxTokens
	} else if req.MaxCompletionTokens != nil {
		opts["num_predict"] = *req.MaxCompletionTokens
	}

	if len(opts) > 0 {
		out.Options = opts
	}

	return out, nil
}

// convertOpenAIMessageToOllama 将单条 OpenAI Message 转为 OllamaChatMessage。
// 遇到图像、音频、文件等非文本内容立即返回错误。
func convertOpenAIMessageToOllama(msg dto.Message, callIDToName map[string]string) (dto.OllamaChatMessage, error) {
	out := dto.OllamaChatMessage{
		Role: msg.Role,
	}

	// tool result 消息
	if msg.Role == "tool" {
		content, err := extractOllamaTextContent(msg.Content)
		if err != nil {
			return dto.OllamaChatMessage{}, err
		}
		out.Content = content
		// ToolName 优先使用 Name 字段，其次从 callIDToName 映射查找，最后使用 ToolCallID
		if msg.Name != "" {
			out.ToolName = msg.Name
		} else if name, ok := callIDToName[msg.ToolCallID]; ok {
			out.ToolName = name
		} else if msg.ToolCallID != "" {
			out.ToolName = msg.ToolCallID
		}
		return out, nil
	}

	// assistant 消息含 tool_calls
	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		content, err := extractOllamaTextContent(msg.Content)
		if err != nil {
			return dto.OllamaChatMessage{}, err
		}
		out.Content = content
		out.ToolCalls = make([]dto.OllamaToolCall, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			args := json.RawMessage(tc.Function.Arguments)
			out.ToolCalls = append(out.ToolCalls, dto.OllamaToolCall{
				Function: dto.OllamaToolCallFunction{
					Name:      tc.Function.Name,
					Arguments: args,
				},
			})
		}
		return out, nil
	}

	// 普通消息
	content, err := extractOllamaTextContent(msg.Content)
	if err != nil {
		return dto.OllamaChatMessage{}, err
	}
	out.Content = content
	return out, nil
}

// extractOllamaTextContent 从 OpenAI message content 中提取纯文本。
// 若 content 是字符串，直接返回；若是数组，要求每个 block 都是 text 类型，并按顺序拼接。
// 遇到 image_url、input_audio、file、video_url 或未知类型立即返回错误。
func extractOllamaTextContent(content any) (string, error) {
	if content == nil {
		return "", nil
	}
	if s, ok := content.(string); ok {
		return s, nil
	}
	blocks, ok := content.([]any)
	if !ok {
		return "", fmt.Errorf("unsupported content type: %T", content)
	}

	var result string
	for _, item := range blocks {
		m, ok := item.(map[string]any)
		if !ok {
			return "", fmt.Errorf("unsupported content block type: %T", item)
		}
		blockType, _ := m["type"].(string)
		switch blockType {
		case dto.ContentTypeText:
			text, _ := m["text"].(string)
			result += text
		case dto.ContentTypeImageURL:
			return "", fmt.Errorf("image_url content is not supported by Ollama chat")
		case dto.ContentTypeInputAudio:
			return "", fmt.Errorf("input_audio content is not supported by Ollama chat")
		case dto.ContentTypeFile:
			return "", fmt.Errorf("file content is not supported by Ollama chat")
		case dto.ContentTypeVideoURL:
			return "", fmt.Errorf("video_url content is not supported by Ollama chat")
		default:
			return "", fmt.Errorf("unsupported content block type for Ollama: %q", blockType)
		}
	}
	return result, nil
}

// convertResponseFormatToOllama 将 OpenAI ResponseFormat 转换为 Ollama format 字段。
// json / json_object -> "json"
// json_schema -> 解析后的 schema 对象
func convertResponseFormatToOllama(rf *dto.ResponseFormat) (any, error) {
	switch rf.Type {
	case "json", "json_object":
		return "json", nil
	case "json_schema":
		if len(rf.JsonSchema) == 0 {
			return nil, fmt.Errorf("json_schema type requires non-empty schema")
		}
		var schema map[string]any
		if err := json.Unmarshal(rf.JsonSchema, &schema); err != nil {
			return nil, fmt.Errorf("invalid json_schema: %w", err)
		}
		return schema, nil
	default:
		return nil, nil
	}
}

// buildCallIDToNameMap 预扫描消息列表，建立 tool call ID -> function name 映射，
// 用于在转换 tool result 消息时查找工具名。
func buildCallIDToNameMap(msgs []dto.Message) map[string]string {
	m := make(map[string]string)
	for _, msg := range msgs {
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" && tc.Function.Name != "" {
					m[tc.ID] = tc.Function.Name
				}
			}
		}
	}
	return m
}
