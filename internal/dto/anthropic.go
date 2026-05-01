package dto

type ClaudeRequest struct {
	Model         string          `json:"model"`
	Messages      []ClaudeMessage `json:"messages"`
	System        any             `json:"system,omitempty"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	Tools         []ClaudeTool    `json:"tools,omitempty"`
	ToolChoice    any             `json:"tool_choice,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
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
}

type ClaudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type ClaudeStreamEvent struct {
	Type         string              `json:"type"`
	Index        int                 `json:"index,omitempty"`
	Delta        *ClaudeDelta        `json:"delta,omitempty"`
	ContentBlock *ContentBlock       `json:"content_block,omitempty"`
	Message      *ClaudeMessageStart `json:"message,omitempty"`
}

type ClaudeDelta struct {
	Type         string `json:"type,omitempty"`
	Text         string `json:"text,omitempty"`
	Thinking     string `json:"thinking,omitempty"`
	PartialJSON  string `json:"partial_json,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	StopSequence string `json:"stop_sequence,omitempty"`
}

type ClaudeMessageStart struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Role    string         `json:"role"`
	Model   string         `json:"model"`
	Content []ContentBlock `json:"content"`
	Usage   ClaudeUsage    `json:"usage"`
}
