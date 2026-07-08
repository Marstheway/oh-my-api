package dto

import "encoding/json"

// OllamaChatRequest 是发往 Ollama /api/chat 的请求体。
type OllamaChatRequest struct {
	Model    string             `json:"model"`
	Messages []OllamaChatMessage `json:"messages"`
	Stream   bool               `json:"stream"`
	// Format 支持字符串 "json" 或解析后的 schema 对象，omitempty 通过 interface{} 实现
	Format  any          `json:"format,omitempty"`
	Tools   []OllamaTool `json:"tools,omitempty"`
	Options map[string]any `json:"options,omitempty"`
	// Think 保留原始 JSON 值，不做字符串化
	Think json.RawMessage `json:"think,omitempty"`
}

// OllamaChatMessage 是 Ollama chat 的单条消息。
type OllamaChatMessage struct {
	Role      string          `json:"role"`
	Content   string          `json:"content,omitempty"`
	ToolCalls []OllamaToolCall `json:"tool_calls,omitempty"`
	// ToolName 仅 role=tool 时使用
	ToolName string `json:"tool_name,omitempty"`
}

// OllamaToolCall 是 assistant 消息中的工具调用。
type OllamaToolCall struct {
	Function OllamaToolCallFunction `json:"function"`
}

// OllamaToolCallFunction 是工具调用的函数部分。
type OllamaToolCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// OllamaTool 是工具定义。
type OllamaTool struct {
	Type     string             `json:"type"`
	Function OllamaToolFunction `json:"function"`
}

// OllamaToolFunction 是工具函数的描述。
type OllamaToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// OllamaChatMessage 中的消息体，用于响应。
type OllamaChatResponseMessage struct {
	Role      string          `json:"role"`
	Content   string          `json:"content,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ToolCalls []OllamaToolCall `json:"tool_calls,omitempty"`
}

// OllamaChatResponse 是 Ollama /api/chat 的非流式响应体。
type OllamaChatResponse struct {
	Model           string                     `json:"model"`
	CreatedAt       string                     `json:"created_at,omitempty"`
	Message         OllamaChatResponseMessage  `json:"message"`
	Done            bool                       `json:"done"`
	DoneReason      string                     `json:"done_reason,omitempty"`
	PromptEvalCount int                        `json:"prompt_eval_count,omitempty"`
	EvalCount       int                        `json:"eval_count,omitempty"`
	Error           string                     `json:"error,omitempty"`
}

// OllamaChatStreamChunk 是 Ollama 流式响应的单个数据块。
type OllamaChatStreamChunk struct {
	Model           string                     `json:"model"`
	CreatedAt       string                     `json:"created_at,omitempty"`
	Message         OllamaChatResponseMessage  `json:"message"`
	Done            bool                       `json:"done"`
	DoneReason      string                     `json:"done_reason,omitempty"`
	PromptEvalCount int                        `json:"prompt_eval_count,omitempty"`
	EvalCount       int                        `json:"eval_count,omitempty"`
	Error           string                     `json:"error,omitempty"`
}
