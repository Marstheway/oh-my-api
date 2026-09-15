package dto

import "encoding/json"

// StopParam supports both string and []string JSON values for the "stop" field.
type StopParam []string

func (s *StopParam) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '[' {
		return json.Unmarshal(data, (*[]string)(s))
	}
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	*s = []string{str}
	return nil
}

type ChatCompletionRequest struct {
	Model       string    `json:"model" binding:"required"`
	Messages    []Message `json:"messages" binding:"required"`
	Stream      bool      `json:"stream,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	Tools       []Tool    `json:"tools,omitempty"`
	ToolChoice  any       `json:"tool_choice,omitempty"`
	Stop        StopParam `json:"stop,omitempty"`
	N           int       `json:"n,omitempty"`

	// 控制类字段
	MaxCompletionTokens  *int              `json:"max_completion_tokens,omitempty"`
	TopK                 *int              `json:"top_k,omitempty"`
	StreamOptions        *StreamOptions    `json:"stream_options,omitempty"`
	ReasoningEffort      string            `json:"reasoning_effort,omitempty"`
	ResponseFormat       *ResponseFormat   `json:"response_format,omitempty"`
	FrequencyPenalty     *float64          `json:"frequency_penalty,omitempty"`
	PresencePenalty      *float64          `json:"presence_penalty,omitempty"`
	Seed                 *int64            `json:"seed,omitempty"`
	LogProbs             *bool             `json:"logprobs,omitempty"`
	TopLogProbs          *int              `json:"top_logprobs,omitempty"`
	LogitBias            json.RawMessage   `json:"logit_bias,omitempty"`
	User                 json.RawMessage   `json:"user,omitempty"`
	Prediction           json.RawMessage   `json:"prediction,omitempty"`
	Store                json.RawMessage   `json:"store,omitempty"`
	SafetyIdentifier     json.RawMessage   `json:"safety_identifier,omitempty"`
	PromptCacheKey       string            `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention json.RawMessage   `json:"prompt_cache_retention,omitempty"`
	ParallelToolCalls    *bool             `json:"parallel_tool_calls,omitempty"`
	ServiceTier          json.RawMessage   `json:"service_tier,omitempty"`
	Metadata             json.RawMessage   `json:"metadata,omitempty"`
	WebSearchOptions     *WebSearchOptions `json:"web_search_options,omitempty"`

	// Provider 特有字段 (透传)
	Reasoning              json.RawMessage `json:"reasoning,omitempty"`
	Usage                  json.RawMessage `json:"usage,omitempty"`
	Thinking               json.RawMessage `json:"thinking,omitempty"` // DeepSeek: {"type":"enabled|disabled"}
	EnableThinking         json.RawMessage `json:"enable_thinking,omitempty"`
	Think                  json.RawMessage `json:"think,omitempty"`
	THINKING               json.RawMessage `json:"THINKING,omitempty"`
	EnableSearch           json.RawMessage `json:"enable_search,omitempty"`
	SearchParameters       json.RawMessage `json:"search_parameters,omitempty"`
	SearchDomainFilter     json.RawMessage `json:"search_domain_filter,omitempty"`
	SearchRecencyFilter    json.RawMessage `json:"search_recency_filter,omitempty"`
	SearchMode             json.RawMessage `json:"search_mode,omitempty"`
	ReturnRelatedQuestions *bool           `json:"return_related_questions,omitempty"`
	ReasoningSplit         json.RawMessage `json:"reasoning_split,omitempty"`
}

type Message struct {
	Role             string        `json:"role" binding:"required"`
	Content          any           `json:"content"`
	Name             string        `json:"name,omitempty"`
	FunctionCall     *ToolCallFunc `json:"function_call,omitempty"`
	ToolCalls        []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	ReasoningContent *string       `json:"reasoning_content,omitempty"`
	Reasoning        *string       `json:"reasoning,omitempty"`
	Prefix           *bool         `json:"prefix,omitempty"`
}

type MediaContent struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ImageUrl     any             `json:"image_url,omitempty"`
	InputAudio   any             `json:"input_audio,omitempty"`
	File         any             `json:"file,omitempty"`
	VideoUrl     any             `json:"video_url,omitempty"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

// MessageImageUrl represents an image URL content block
// Can be used with image_url type in MediaContent
type MessageImageUrl struct {
	Url    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// MessageInputAudio represents audio input content block
// Used for audio inputs in multimodal messages
type MessageInputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

// MessageFile represents a file content block
// Used for file attachments in messages
type MessageFile struct {
	FileName string `json:"filename,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileId   string `json:"file_id,omitempty"`
}

// MessageVideoUrl represents a video URL content block
// Used for video URL references in multimodal messages
type MessageVideoUrl struct {
	Url string `json:"url"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Index        int         `json:"index"`
	Message      *ResMessage `json:"message,omitempty"`
	Delta        *Delta      `json:"delta,omitempty"`
	FinishReason *string     `json:"finish_reason"`
}

type ResMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
}

type ToolCall struct {
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function ToolCallFunc `json:"function"`
}

// GetIndex 返回工具调用的索引，若为 nil 则返回 0。
func (tc ToolCall) GetIndex() int {
	if tc.Index == nil {
		return 0
	}
	return *tc.Index
}

type ToolCallFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type Delta struct {
	Role             string     `json:"role,omitempty"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

type UsageDetails struct {
	CachedTokens     int `json:"cached_tokens,omitempty"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
	ImageTokens      int `json:"image_tokens,omitempty"`
	AudioTokens      int `json:"audio_tokens,omitempty"`
	AcceptanceTokens int `json:"acceptance_tokens,omitempty"`
	RejectionTokens  int `json:"rejection_tokens,omitempty"`
}

type Usage struct {
	PromptTokens            int           `json:"prompt_tokens"`
	CompletionTokens        int           `json:"completion_tokens"`
	TotalTokens             int           `json:"total_tokens"`
	PromptTokensDetails     *UsageDetails `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *UsageDetails `json:"completion_tokens_details,omitempty"`
}

type ChatCompletionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"`
}

type ChunkChoice struct {
	Index        int     `json:"index"`
	Delta        *Delta  `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type ModelListResponse struct {
	Data []ModelInfo `json:"data"`
}

type ModelInfo struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	ContextLength *int   `json:"context_length,omitempty"`
}
