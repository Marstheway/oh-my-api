package dto

import "encoding/json"

// ResponsesRequest 是 /v1/responses 接口的请求体。
// Input 和 Instructions 使用 json.RawMessage 以支持字符串或数组两种形式。
type ResponsesRequest struct {
	Model           string          `json:"model"`
	Input           json.RawMessage `json:"input,omitempty"`
	Instructions    json.RawMessage `json:"instructions,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	Tools           []ResponsesTool `json:"tools,omitempty"`
	ToolChoice      any             `json:"tool_choice,omitempty"`
}

// ResponsesTool 是 Responses API 的工具定义。
type ResponsesTool struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// ResponsesResponse 是 /v1/responses 接口的非流式响应体。
type ResponsesResponse struct {
	ID                string             `json:"id"`
	Object            string             `json:"object"`
	CreatedAt         int64              `json:"created_at"`
	Model             string             `json:"model"`
	Status            string             `json:"status"`
	Output            []ResponsesOutput  `json:"output"`
	Usage             ResponsesUsage     `json:"usage"`
	IncompleteDetails *IncompleteDetails `json:"incomplete_details,omitempty"`
	Error             *ResponsesError    `json:"error,omitempty"`
}

// ResponsesUsage 记录 token 用量。
type ResponsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// ResponsesOutput 是响应中的一个输出 item。
// Type 可以是 "message"、"function_call" 或 "reasoning"。
type ResponsesOutput struct {
	Type    string                   `json:"type"`
	ID      string                   `json:"id,omitempty"`
	Status  string                   `json:"status,omitempty"`
	Role    string                   `json:"role,omitempty"`
	Content []ResponsesOutputContent `json:"content,omitempty"`
	// function_call 专用字段
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ResponsesOutputContent 是输出 message 的 content part。
type ResponsesOutputContent struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Annotations []any  `json:"annotations,omitempty"`
}

// ResponsesInputItem 用于将 input 数组解析为具体条目，供 response->chat 转换时使用。
type ResponsesInputItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Output    string          `json:"output,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
}

// ResponsesContentPart 用于解析多模态 input content 中的单个 part。
type ResponsesContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL json.RawMessage `json:"image_url,omitempty"`
	FileData json.RawMessage `json:"file,omitempty"`
}

// ResponsesStreamEvent 是 /v1/responses 流式接口的单个 SSE 事件。
// OutputIndex 和 ContentIndex 使用指针以区分"字段不存在"和"值为 0"。
type ResponsesStreamEvent struct {
	Type         string          `json:"type"`
	Delta        json.RawMessage `json:"delta,omitempty"`
	ItemID       string          `json:"item_id,omitempty"`
	OutputIndex  *int            `json:"output_index,omitempty"`
	ContentIndex *int            `json:"content_index,omitempty"`
	Response     json.RawMessage `json:"response,omitempty"`
	Item         json.RawMessage `json:"item,omitempty"`
}

// IncompleteDetails 说明响应未完成的原因。
type IncompleteDetails struct {
	Reason string `json:"reason,omitempty"`
}

// ResponsesError 是 Responses API 响应中的错误结构。
type ResponsesError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
