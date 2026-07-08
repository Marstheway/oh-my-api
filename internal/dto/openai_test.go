package dto

import (
	"encoding/json"
	"testing"
)

func TestChatCompletionRequest_NewFields(t *testing.T) {
	// 测试控制类字段的序列化/反序列化
	tests := []struct {
		name     string
		json     string
		expected ChatCompletionRequest
	}{
		{
			name: "max_completion_tokens",
			json: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":100}`,
			expected: ChatCompletionRequest{
				Model:               "gpt-4",
				Messages:            []Message{{Role: "user", Content: "hi"}},
				MaxCompletionTokens: ptrInt(100),
			},
		},
		{
			name: "top_k",
			json: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"top_k":50}`,
			expected: ChatCompletionRequest{
				Model:    "gpt-4",
				Messages: []Message{{Role: "user", Content: "hi"}},
				TopK:     ptrInt(50),
			},
		},
		{
			name: "stream_options",
			json: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`,
			expected: ChatCompletionRequest{
				Model:         "gpt-4",
				Messages:      []Message{{Role: "user", Content: "hi"}},
				Stream:        true,
				StreamOptions: &StreamOptions{IncludeUsage: true},
			},
		},
		{
			name: "reasoning_effort",
			json: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`,
			expected: ChatCompletionRequest{
				Model:           "gpt-4",
				Messages:        []Message{{Role: "user", Content: "hi"}},
				ReasoningEffort: "high",
			},
		},
		{
			name: "response_format",
			json: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`,
			expected: ChatCompletionRequest{
				Model:          "gpt-4",
				Messages:       []Message{{Role: "user", Content: "hi"}},
				ResponseFormat: &ResponseFormat{Type: "json_object"},
			},
		},
		{
			name: "frequency_penalty",
			json: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"frequency_penalty":0.5}`,
			expected: ChatCompletionRequest{
				Model:            "gpt-4",
				Messages:         []Message{{Role: "user", Content: "hi"}},
				FrequencyPenalty: ptrFloat64(0.5),
			},
		},
		{
			name: "presence_penalty",
			json: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"presence_penalty":0.3}`,
			expected: ChatCompletionRequest{
				Model:           "gpt-4",
				Messages:        []Message{{Role: "user", Content: "hi"}},
				PresencePenalty: ptrFloat64(0.3),
			},
		},
		{
			name: "seed",
			json: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"seed":12345}`,
			expected: ChatCompletionRequest{
				Model:    "gpt-4",
				Messages: []Message{{Role: "user", Content: "hi"}},
				Seed:     ptrInt64(12345),
			},
		},
		{
			name: "logprobs",
			json: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"logprobs":true,"top_logprobs":5}`,
			expected: ChatCompletionRequest{
				Model:       "gpt-4",
				Messages:    []Message{{Role: "user", Content: "hi"}},
				LogProbs:    ptrBool(true),
				TopLogProbs: ptrInt(5),
			},
		},
		{
			name: "web_search_options",
			json: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"web_search_options":{"search_context_size":"high"}}`,
			expected: ChatCompletionRequest{
				Model:            "gpt-4",
				Messages:         []Message{{Role: "user", Content: "hi"}},
				WebSearchOptions: &WebSearchOptions{SearchContextSize: "high"},
			},
		},
		{
			name: "parallel_tool_calls",
			json: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"parallel_tool_calls":false}`,
			expected: ChatCompletionRequest{
				Model:             "gpt-4",
				Messages:          []Message{{Role: "user", Content: "hi"}},
				ParallelToolCalls: ptrBool(false),
			},
		},
		{
			name: "prompt_cache_key",
			json: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"prompt_cache_key":"cache-123"}`,
			expected: ChatCompletionRequest{
				Model:          "gpt-4",
				Messages:       []Message{{Role: "user", Content: "hi"}},
				PromptCacheKey: "cache-123",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req ChatCompletionRequest
			if err := json.Unmarshal([]byte(tt.json), &req); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			// Basic check - model should match
			if req.Model != tt.expected.Model {
				t.Errorf("Model = %q, want %q", req.Model, tt.expected.Model)
			}
		})
	}
}

func TestChatCompletionRequest_RawMessageFields(t *testing.T) {
	// 测试 RawMessage 类型字段
	jsonStr := `{
		"model":"gpt-4",
		"messages":[{"role":"user","content":"hi"}],
		"logit_bias":{"1234":-100},
		"user":"user-abc",
		"prediction":{"type":"content","content":"Hello"},
		"store":true,
		"safety_identifier":"safe-123",
		"prompt_cache_retention":{"type":"ephemeral"},
		"service_tier":"auto",
		"metadata":{"key":"value"}
	}`

	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if req.Model != "gpt-4" {
		t.Errorf("Model = %q, want %q", req.Model, "gpt-4")
	}
}

func TestChatCompletionRequest_ProviderSpecificFields(t *testing.T) {
	// 测试 Provider 特有字段
	jsonStr := `{
		"model":"gpt-4",
		"messages":[{"role":"user","content":"hi"}],
		"reasoning":{"effort":"high"},
		"usage":{"include":true},
		"enable_thinking":true,
		"think":{"budget_tokens":1000},
		"THINKING":{"enabled":true},
		"enable_search":true,
		"search_parameters":{"mode":"auto"},
		"search_domain_filter":["example.com"],
		"search_recency_filter":{"days":7},
		"search_mode":"academic",
		"return_related_questions":true,
		"reasoning_split":{"enabled":true}
	}`

	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if req.Model != "gpt-4" {
		t.Errorf("Model = %q, want %q", req.Model, "gpt-4")
	}
}

func TestChatCompletionRequest_Marshal(t *testing.T) {
	// 测试序列化
	req := ChatCompletionRequest{
		Model:               "gpt-4",
		Messages:            []Message{{Role: "user", Content: "hi"}},
		MaxCompletionTokens: ptrInt(100),
		TopK:                ptrInt(50),
		ReasoningEffort:     "high",
		FrequencyPenalty:    ptrFloat64(0.5),
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// 验证序列化结果包含预期字段
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if result["model"] != "gpt-4" {
		t.Errorf("model = %v, want %v", result["model"], "gpt-4")
	}
	if result["max_completion_tokens"] != float64(100) {
		t.Errorf("max_completion_tokens = %v, want %v", result["max_completion_tokens"], 100)
	}
}

// Helper functions
func ptrInt(v int) *int             { return &v }
func ptrInt64(v int64) *int64       { return &v }
func ptrFloat64(v float64) *float64 { return &v }
func ptrBool(v bool) *bool          { return &v }
func ptrString(v string) *string    { return &v }

func TestUsage_Details(t *testing.T) {
	// 测试 Usage 新增的详情字段
	tests := []struct {
		name     string
		json     string
		expected Usage
	}{
		{
			name: "basic_usage",
			json: `{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}`,
			expected: Usage{
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalTokens:      30,
			},
		},
		{
			name: "usage_with_prompt_tokens_details",
			json: `{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":80,"reasoning_tokens":10}}`,
			expected: Usage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
				PromptTokensDetails: &UsageDetails{
					CachedTokens:    80,
					ReasoningTokens: 10,
				},
			},
		},
		{
			name: "usage_with_completion_tokens_details",
			json: `{"prompt_tokens":100,"completion_tokens":200,"total_tokens":300,"completion_tokens_details":{"reasoning_tokens":150,"acceptance_tokens":30,"rejection_tokens":20}}`,
			expected: Usage{
				PromptTokens:     100,
				CompletionTokens: 200,
				TotalTokens:      300,
				CompletionTokensDetails: &UsageDetails{
					ReasoningTokens:  150,
					AcceptanceTokens: 30,
					RejectionTokens:  20,
				},
			},
		},
		{
			name: "usage_with_all_details",
			json: `{"prompt_tokens":100,"completion_tokens":200,"total_tokens":300,"prompt_tokens_details":{"cached_tokens":50,"reasoning_tokens":20},"completion_tokens_details":{"cached_tokens":10,"reasoning_tokens":100,"acceptance_tokens":50,"rejection_tokens":40}}`,
			expected: Usage{
				PromptTokens:     100,
				CompletionTokens: 200,
				TotalTokens:      300,
				PromptTokensDetails: &UsageDetails{
					CachedTokens:    50,
					ReasoningTokens: 20,
				},
				CompletionTokensDetails: &UsageDetails{
					CachedTokens:     10,
					ReasoningTokens:  100,
					AcceptanceTokens: 50,
					RejectionTokens:  40,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var usage Usage
			if err := json.Unmarshal([]byte(tt.json), &usage); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if usage.PromptTokens != tt.expected.PromptTokens {
				t.Errorf("PromptTokens = %d, want %d", usage.PromptTokens, tt.expected.PromptTokens)
			}
			if usage.CompletionTokens != tt.expected.CompletionTokens {
				t.Errorf("CompletionTokens = %d, want %d", usage.CompletionTokens, tt.expected.CompletionTokens)
			}
			if usage.TotalTokens != tt.expected.TotalTokens {
				t.Errorf("TotalTokens = %d, want %d", usage.TotalTokens, tt.expected.TotalTokens)
			}
		})
	}
}

func TestUsageDetails_Fields(t *testing.T) {
	// 测试 UsageDetails 结构体字段
	tests := []struct {
		name     string
		json     string
		expected UsageDetails
	}{
		{
			name: "all_fields",
			json: `{"cached_tokens":100,"reasoning_tokens":50,"acceptance_tokens":30,"rejection_tokens":20}`,
			expected: UsageDetails{
				CachedTokens:     100,
				ReasoningTokens:  50,
				AcceptanceTokens: 30,
				RejectionTokens:  20,
			},
		},
		{
			name: "partial_fields",
			json: `{"cached_tokens":80}`,
			expected: UsageDetails{
				CachedTokens: 80,
			},
		},
		{
			name:     "empty",
			json:     `{}`,
			expected: UsageDetails{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var details UsageDetails
			if err := json.Unmarshal([]byte(tt.json), &details); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if details.CachedTokens != tt.expected.CachedTokens {
				t.Errorf("CachedTokens = %d, want %d", details.CachedTokens, tt.expected.CachedTokens)
			}
			if details.ReasoningTokens != tt.expected.ReasoningTokens {
				t.Errorf("ReasoningTokens = %d, want %d", details.ReasoningTokens, tt.expected.ReasoningTokens)
			}
			if details.AcceptanceTokens != tt.expected.AcceptanceTokens {
				t.Errorf("AcceptanceTokens = %d, want %d", details.AcceptanceTokens, tt.expected.AcceptanceTokens)
			}
			if details.RejectionTokens != tt.expected.RejectionTokens {
				t.Errorf("RejectionTokens = %d, want %d", details.RejectionTokens, tt.expected.RejectionTokens)
			}
		})
	}
}

func TestMessage_NewFields(t *testing.T) {
	// 测试 Message 新增字段
	tests := []struct {
		name     string
		json     string
		expected Message
	}{
		{
			name: "reasoning_content",
			json: `{"role":"assistant","content":"Hello","reasoning_content":"Let me think..."}`,
			expected: Message{
				Role:             "assistant",
				Content:          "Hello",
				ReasoningContent: ptrString("Let me think..."),
			},
		},
		{
			name: "reasoning",
			json: `{"role":"assistant","content":"Hello","reasoning":"Thinking process"}`,
			expected: Message{
				Role:      "assistant",
				Content:   "Hello",
				Reasoning: ptrString("Thinking process"),
			},
		},
		{
			name: "prefix",
			json: `{"role":"assistant","content":"Hello","prefix":true}`,
			expected: Message{
				Role:    "assistant",
				Content: "Hello",
				Prefix:  ptrBool(true),
			},
		},
		{
			name: "all_new_fields",
			json: `{"role":"assistant","content":"Hello","reasoning_content":"think","reasoning":"process","prefix":false}`,
			expected: Message{
				Role:             "assistant",
				Content:          "Hello",
				ReasoningContent: ptrString("think"),
				Reasoning:        ptrString("process"),
				Prefix:           ptrBool(false),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg Message
			if err := json.Unmarshal([]byte(tt.json), &msg); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if msg.Role != tt.expected.Role {
				t.Errorf("Role = %q, want %q", msg.Role, tt.expected.Role)
			}
		})
	}
}

func TestMediaContent(t *testing.T) {
	// 测试 MediaContent 结构体
	tests := []struct {
		name     string
		json     string
		expected MediaContent
	}{
		{
			name: "text_content",
			json: `{"type":"text","text":"Hello world"}`,
			expected: MediaContent{
				Type: "text",
				Text: "Hello world",
			},
		},
		{
			name: "text_with_cache_control",
			json: `{"type":"text","text":"Hello","cache_control":{"type":"ephemeral"}}`,
			expected: MediaContent{
				Type:         "text",
				Text:         "Hello",
				CacheControl: json.RawMessage(`{"type":"ephemeral"}`),
			},
		},
		{
			name: "image_url_type",
			json: `{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}`,
			expected: MediaContent{
				Type: "image_url",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mc MediaContent
			if err := json.Unmarshal([]byte(tt.json), &mc); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if mc.Type != tt.expected.Type {
				t.Errorf("Type = %q, want %q", mc.Type, tt.expected.Type)
			}
			if mc.Text != tt.expected.Text {
				t.Errorf("Text = %q, want %q", mc.Text, tt.expected.Text)
			}
		})
	}
}

func TestMediaContent_ImageUrlSerialization(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected MediaContent
	}{
		{
			name: "image_url_with_object",
			json: `{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}`,
			expected: MediaContent{
				Type:     "image_url",
				ImageUrl: MessageImageUrl{Url: "https://example.com/image.png"},
			},
		},
		{
			name: "image_url_with_detail",
			json: `{"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":"high"}}`,
			expected: MediaContent{
				Type:     "image_url",
				ImageUrl: MessageImageUrl{Url: "https://example.com/image.png", Detail: "high"},
			},
		},
		{
			name: "image_url_string",
			json: `{"type":"image_url","image_url":"https://example.com/image.png"}`,
			expected: MediaContent{
				Type:     "image_url",
				ImageUrl: "https://example.com/image.png",
			},
		},
		{
			name: "base64_image_url",
			json: `{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}`,
			expected: MediaContent{
				Type:     "image_url",
				ImageUrl: MessageImageUrl{Url: "data:image/png;base64,iVBORw0KGgo="},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mc MediaContent
			if err := json.Unmarshal([]byte(tt.json), &mc); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if mc.Type != tt.expected.Type {
				t.Errorf("Type = %q, want %q", mc.Type, tt.expected.Type)
			}
			if mc.ImageUrl == nil {
				t.Errorf("ImageUrl is nil, expected not nil")
				return
			}
			switch expected := tt.expected.ImageUrl.(type) {
			case string:
				actual, ok := mc.ImageUrl.(string)
				if !ok {
					t.Errorf("ImageUrl type = %T, want string", mc.ImageUrl)
				} else if actual != expected {
					t.Errorf("ImageUrl = %q, want %q", actual, expected)
				}
			case MessageImageUrl:
				actualMap, ok := mc.ImageUrl.(map[string]interface{})
				if !ok {
					t.Errorf("ImageUrl type = %T, want map[string]interface{}", mc.ImageUrl)
					return
				}
				if actualUrl, ok := actualMap["url"].(string); ok {
					if actualUrl != expected.Url {
						t.Errorf("ImageUrl.url = %q, want %q", actualUrl, expected.Url)
					}
				} else {
					t.Errorf("ImageUrl.url not found or not a string")
				}
				if expected.Detail != "" {
					if actualDetail, ok := actualMap["detail"].(string); ok {
						if actualDetail != expected.Detail {
							t.Errorf("ImageUrl.detail = %q, want %q", actualDetail, expected.Detail)
						}
					} else {
						t.Errorf("ImageUrl.detail not found or not a string")
					}
				}
			}
		})
	}
}

func TestMediaContent_InputAudioSerialization(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected MediaContent
	}{
		{
			name: "input_audio_wav",
			json: `{"type":"input_audio","input_audio":{"data":"base64data","format":"wav"}}`,
			expected: MediaContent{
				Type:       "input_audio",
				InputAudio: MessageInputAudio{Data: "base64data", Format: "wav"},
			},
		},
		{
			name: "input_audio_mp3",
			json: `{"type":"input_audio","input_audio":{"data":"base64mp3","format":"mp3"}}`,
			expected: MediaContent{
				Type:       "input_audio",
				InputAudio: MessageInputAudio{Data: "base64mp3", Format: "mp3"},
			},
		},
		{
			name: "input_audio_m4a",
			json: `{"type":"input_audio","input_audio":{"data":"base64m4a","format":"m4a"}}`,
			expected: MediaContent{
				Type:       "input_audio",
				InputAudio: MessageInputAudio{Data: "base64m4a", Format: "m4a"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mc MediaContent
			if err := json.Unmarshal([]byte(tt.json), &mc); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if mc.Type != tt.expected.Type {
				t.Errorf("Type = %q, want %q", mc.Type, tt.expected.Type)
			}
			if mc.InputAudio == nil {
				t.Errorf("InputAudio is nil, expected not nil")
				return
			}
			actualMap, ok := mc.InputAudio.(map[string]interface{})
			if !ok {
				t.Errorf("InputAudio type = %T, want map[string]interface{}", mc.InputAudio)
				return
			}
			expected := tt.expected.InputAudio.(MessageInputAudio)
			if actualData, ok := actualMap["data"].(string); ok {
				if actualData != expected.Data {
					t.Errorf("InputAudio.data = %q, want %q", actualData, expected.Data)
				}
			} else {
				t.Errorf("InputAudio.data not found or not a string")
			}
			if actualFormat, ok := actualMap["format"].(string); ok {
				if actualFormat != expected.Format {
					t.Errorf("InputAudio.format = %q, want %q", actualFormat, expected.Format)
				}
			} else {
				t.Errorf("InputAudio.format not found or not a string")
			}
		})
	}
}

func TestMediaContent_FileSerialization(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected MediaContent
	}{
		{
			name: "file_with_data",
			json: `{"type":"file","file":{"filename":"test.pdf","file_data":"base64data"}}`,
			expected: MediaContent{
				Type: "file",
				File: MessageFile{FileName: "test.pdf", FileData: "base64data"},
			},
		},
		{
			name: "file_with_id",
			json: `{"type":"file","file":{"file_id":"file-123"}}`,
			expected: MediaContent{
				Type: "file",
				File: MessageFile{FileId: "file-123"},
			},
		},
		{
			name: "file_all_fields",
			json: `{"type":"file","file":{"filename":"doc.pdf","file_data":"data","file_id":"file-abc"}}`,
			expected: MediaContent{
				Type: "file",
				File: MessageFile{FileName: "doc.pdf", FileData: "data", FileId: "file-abc"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mc MediaContent
			if err := json.Unmarshal([]byte(tt.json), &mc); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if mc.Type != tt.expected.Type {
				t.Errorf("Type = %q, want %q", mc.Type, tt.expected.Type)
			}
			if mc.File == nil {
				t.Errorf("File is nil, expected not nil")
				return
			}
			actualMap, ok := mc.File.(map[string]interface{})
			if !ok {
				t.Errorf("File type = %T, want map[string]interface{}", mc.File)
				return
			}
			expected := tt.expected.File.(MessageFile)
			if expected.FileName != "" {
				if actualName, ok := actualMap["filename"].(string); ok {
					if actualName != expected.FileName {
						t.Errorf("File.filename = %q, want %q", actualName, expected.FileName)
					}
				} else {
					t.Errorf("File.filename not found or not a string")
				}
			}
			if expected.FileData != "" {
				if actualData, ok := actualMap["file_data"].(string); ok {
					if actualData != expected.FileData {
						t.Errorf("File.file_data = %q, want %q", actualData, expected.FileData)
					}
				} else {
					t.Errorf("File.file_data not found or not a string")
				}
			}
			if expected.FileId != "" {
				if actualId, ok := actualMap["file_id"].(string); ok {
					if actualId != expected.FileId {
						t.Errorf("File.file_id = %q, want %q", actualId, expected.FileId)
					}
				} else {
					t.Errorf("File.file_id not found or not a string")
				}
			}
		})
	}
}

func TestMediaContent_VideoUrlSerialization(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected MediaContent
	}{
		{
			name: "video_url_simple",
			json: `{"type":"video_url","video_url":{"url":"https://example.com/video.mp4"}}`,
			expected: MediaContent{
				Type:     "video_url",
				VideoUrl: MessageVideoUrl{Url: "https://example.com/video.mp4"},
			},
		},
		{
			name: "video_url_with_detail",
			json: `{"type":"video_url","video_url":{"url":"https://example.com/video.mp4"}}`,
			expected: MediaContent{
				Type:     "video_url",
				VideoUrl: MessageVideoUrl{Url: "https://example.com/video.mp4"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mc MediaContent
			if err := json.Unmarshal([]byte(tt.json), &mc); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if mc.Type != tt.expected.Type {
				t.Errorf("Type = %q, want %q", mc.Type, tt.expected.Type)
			}
			if mc.VideoUrl == nil {
				t.Errorf("VideoUrl is nil, expected not nil")
				return
			}
			actualMap, ok := mc.VideoUrl.(map[string]interface{})
			if !ok {
				t.Errorf("VideoUrl type = %T, want map[string]interface{}", mc.VideoUrl)
				return
			}
			expected := tt.expected.VideoUrl.(MessageVideoUrl)
			if actualUrl, ok := actualMap["url"].(string); ok {
				if actualUrl != expected.Url {
					t.Errorf("VideoUrl.url = %q, want %q", actualUrl, expected.Url)
				}
			} else {
				t.Errorf("VideoUrl.url not found or not a string")
			}
		})
	}
}

func TestMediaContent_MixedContent(t *testing.T) {
	// 测试 Message 中的混合内容（文本+图片）
	jsonData := `{
		"role": "user",
		"content": [
			{"type":"text","text":"What is in this image?"},
			{"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":"high"}}
		]
	}`

	var msg Message
	if err := json.Unmarshal([]byte(jsonData), &msg); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if msg.Role != "user" {
		t.Errorf("Role = %q, want %q", msg.Role, "user")
	}
}

func TestMessageImageUrl_MarshalUnmarshal(t *testing.T) {
	original := MessageImageUrl{
		Url:    "https://example.com/image.png",
		Detail: "high",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var result MessageImageUrl
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if result.Url != original.Url {
		t.Errorf("Url = %q, want %q", result.Url, original.Url)
	}
	if result.Detail != original.Detail {
		t.Errorf("Detail = %q, want %q", result.Detail, original.Detail)
	}
}

func TestMessageInputAudio_MarshalUnmarshal(t *testing.T) {
	original := MessageInputAudio{
		Data:   "base64encodeddata",
		Format: "wav",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var result MessageInputAudio
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if result.Data != original.Data {
		t.Errorf("Data = %q, want %q", result.Data, original.Data)
	}
	if result.Format != original.Format {
		t.Errorf("Format = %q, want %q", result.Format, original.Format)
	}
}

func TestMessageFile_MarshalUnmarshal(t *testing.T) {
	original := MessageFile{
		FileName: "document.pdf",
		FileData: "base64filedata",
		FileId:   "file-12345",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var result MessageFile
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if result.FileName != original.FileName {
		t.Errorf("FileName = %q, want %q", result.FileName, original.FileName)
	}
	if result.FileData != original.FileData {
		t.Errorf("FileData = %q, want %q", result.FileData, original.FileData)
	}
	if result.FileId != original.FileId {
		t.Errorf("FileId = %q, want %q", result.FileId, original.FileId)
	}
}

func TestMessageVideoUrl_MarshalUnmarshal(t *testing.T) {
	original := MessageVideoUrl{
		Url: "https://example.com/video.mp4",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var result MessageVideoUrl
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if result.Url != original.Url {
		t.Errorf("Url = %q, want %q", result.Url, original.Url)
	}
}

func TestMediaContent_InvalidJSON(t *testing.T) {
	// Test malformed multimodal content JSON
	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{
			name:    "malformed_json",
			json:    `{"type":"image_url","image_url":{`,
			wantErr: true,
		},
		{
			name:    "invalid_image_url_structure",
			json:    `{"type":"image_url","image_url":123}`,
			wantErr: false, // any type can unmarshal from number, just won't have expected structure
		},
		{
			name:    "empty_json_object",
			json:    `{}`,
			wantErr: false,
		},
		{
			name:    "missing_required_type",
			json:    `{"image_url":{"url":"https://example.com/image.png"}}`,
			wantErr: false, // Type is not actually required for unmarshal
		},
		{
			name:    "null_content_fields",
			json:    `{"type":"image_url","image_url":null}`,
			wantErr: false,
		},
		{
			name:    "invalid_input_audio_format",
			json:    `{"type":"input_audio","input_audio":{"data":"base64","format":"invalid_format"}}`,
			wantErr: false, // Format validation not enforced at unmarshal level
		},
		{
			name:    "unexpected_field_types",
			json:    `{"type":"text","text":12345}`,
			wantErr: true, // Text field is string, numbers should fail
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mc MediaContent
			err := json.Unmarshal([]byte(tt.json), &mc)
			if tt.wantErr && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
