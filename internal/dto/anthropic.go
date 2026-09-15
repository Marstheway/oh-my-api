package dto

import "encoding/json"

type ClaudeRequest struct {
	Model         string          `json:"model"`
	Messages      []ClaudeMessage `json:"messages"`
	System        any             `json:"system,omitempty"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	Tools         any             `json:"tools,omitempty"`
	ToolChoice    any             `json:"tool_choice,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`

	// 控制类字段
	Prompt            string `json:"prompt,omitempty"`               // 旧版 API 兼容
	MaxTokensToSample *int   `json:"max_tokens_to_sample,omitempty"` // 旧版 API 兼容
	TopK              *int   `json:"top_k,omitempty"`                // Top-K 采样

	// 配置类字段
	CacheControl      json.RawMessage `json:"cache_control,omitempty"`      // 请求级缓存控制
	InferenceGeo      string          `json:"inference_geo,omitempty"`      // 数据驻留区域
	ContextManagement json.RawMessage `json:"context_management,omitempty"` // 上下文管理
	OutputConfig      json.RawMessage `json:"output_config,omitempty"`      // 输出配置（含 effort）
	OutputFormat      json.RawMessage `json:"output_format,omitempty"`      // 输出格式
	Container         json.RawMessage `json:"container,omitempty"`          // 容器配置
	Thinking          *Thinking       `json:"thinking,omitempty"`           // 思考配置
	McpServers        json.RawMessage `json:"mcp_servers,omitempty"`        // MCP 服务器配置
	Speed             json.RawMessage `json:"speed,omitempty"`              // 推理速度模式
	ServiceTier       string          `json:"service_tier,omitempty"`       // 服务层级

	// 元数据类字段
	Metadata json.RawMessage `json:"metadata,omitempty"` // 元数据
}

type ClaudeMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type ClaudeTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

// ClaudeWebSearchTool represents Anthropic's web search tool definition.
// https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/web-search-tool
type ClaudeWebSearchTool struct {
	Type         string                       `json:"type"`
	Name         string                       `json:"name"`
	MaxUses      int                          `json:"max_uses,omitempty"`
	UserLocation *ClaudeWebSearchUserLocation `json:"user_location,omitempty"`
}

// ClaudeWebSearchUserLocation represents approximate user location for web search.
type ClaudeWebSearchUserLocation struct {
	Type     string `json:"type"`
	Timezone string `json:"timezone,omitempty"`
	Country  string `json:"country,omitempty"`
	Region   string `json:"region,omitempty"`
	City     string `json:"city,omitempty"`
}

type ClaudeResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Content      []ContentBlock `json:"content"`
	Model        string         `json:"model"`
	StopReason   *string        `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        ClaudeUsage    `json:"usage"`
}

type ContentBlock struct {
	Type string `json:"type"`
	// text block
	Text string `json:"text,omitempty"`
	// tool_use block
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
	// tool_result block
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   any    `json:"content,omitempty"` // string 或 []ContentBlock
	// thinking block
	Thinking  *string `json:"thinking,omitempty"`
	Signature string  `json:"signature,omitempty"`
	// content block level cache control
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
	// streaming delta
	Delta string `json:"delta,omitempty"`
	// message_start fields
	Role  string       `json:"role,omitempty"`
	Usage *ClaudeUsage `json:"usage,omitempty"`
	// message_stop fields
	StopReason *string `json:"stop_reason,omitempty"`
	Model      string  `json:"model,omitempty"`
	// multimodal source (for image/document)
	Source *MessageSource `json:"source,omitempty"`
}

// MessageSource represents a source for multimodal content (image/document)
type MessageSource struct {
	Type      string `json:"type"`                 // "base64" or "url"
	MediaType string `json:"media_type,omitempty"` // MIME type (image/jpeg, audio/wav, etc.)
	Data      string `json:"data,omitempty"`       // base64 data when Type="base64"
	Url       string `json:"url,omitempty"`        // URL when Type="url"
}

type ClaudeUsage struct {
	InputTokens              int                       `json:"input_tokens"`
	OutputTokens             int                       `json:"output_tokens"`
	CacheCreationInputTokens int                       `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int                       `json:"cache_read_input_tokens,omitempty"`
	CacheCreation            *ClaudeCacheCreationUsage `json:"cache_creation,omitempty"`
	ServerToolUse            *ClaudeServerToolUse      `json:"server_tool_use,omitempty"`
}

// ClaudeCacheCreationUsage detailed cache creation statistics
type ClaudeCacheCreationUsage struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens,omitempty"`
}

// ClaudeServerToolUse server tool use statistics
type ClaudeServerToolUse struct {
	WebSearchRequests int `json:"web_search_requests,omitempty"`
}

type ClaudeStreamEvent struct {
	Type         string              `json:"type"`
	Index        int                 `json:"index,omitempty"`
	Delta        *ClaudeDelta        `json:"delta,omitempty"`
	ContentBlock *ContentBlock       `json:"content_block,omitempty"`
	Message      *ClaudeMessageStart `json:"message,omitempty"`
	Usage        *ClaudeUsage        `json:"usage,omitempty"`
}

type ClaudeDelta struct {
	Type         string  `json:"type,omitempty"`
	Text         string  `json:"text,omitempty"`
	Thinking     string  `json:"thinking,omitempty"`
	PartialJSON  *string `json:"partial_json,omitempty"`
	StopReason   string  `json:"stop_reason,omitempty"`
	StopSequence string  `json:"stop_sequence,omitempty"`
}

type ClaudeMessageStart struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Role    string         `json:"role"`
	Model   string         `json:"model"`
	Content []ContentBlock `json:"content"`
	Usage   ClaudeUsage    `json:"usage"`
}
