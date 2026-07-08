package dto

import "encoding/json"

// StreamOptions 控制流式响应的行为选项。
type StreamOptions struct {
	IncludeUsage       bool `json:"include_usage,omitempty"`
	IncludeObfuscation bool `json:"include_obfuscation,omitempty"`
}

// ResponseFormat 定义响应的格式。
type ResponseFormat struct {
	Type       string          `json:"type,omitempty"`
	JsonSchema json.RawMessage `json:"json_schema,omitempty"`
}

// WebSearchOptions 定义网页搜索的选项。
type WebSearchOptions struct {
	SearchContextSize string          `json:"search_context_size,omitempty"`
	UserLocation      json.RawMessage `json:"user_location,omitempty"`
}

// Thinking 定义扩展思考的配置。
// 适用于 Claude Opus 4.7+ 的 adaptive thinking 功能。
type Thinking struct {
	Type         string `json:"type,omitempty"`
	BudgetTokens *int   `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}
