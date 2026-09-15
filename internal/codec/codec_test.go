package codec

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/gin-gonic/gin"
)

type testCodec struct {
	format Format
}

func (t testCodec) Format() Format {
	return t.format
}

func (t testCodec) DecodeRequest(*gin.Context) (any, error) {
	return nil, nil
}

func (t testCodec) EncodeRequest(Format, any, string, bool) ([]byte, error) {
	return nil, nil
}

func (t testCodec) WriteResponse(*gin.Context, Format, *http.Response, bool, TokenCounter, ResponseModelContext) error {
	return nil
}

func (t testCodec) WriteResponseTo(http.ResponseWriter, Format, *http.Response, bool, TokenCounter, ResponseModelContext) error {
	return nil
}

func TestGetCodec_OpenAIChat(t *testing.T) {
	original := registry
	registry = map[Format]Codec{}
	t.Cleanup(func() {
		registry = original
	})

	register(FormatOpenAIChat, testCodec{format: FormatOpenAIChat})
	got, err := Get(FormatOpenAIChat)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Format() != FormatOpenAIChat {
		t.Fatalf("Get format = %q, want %q", got.Format(), FormatOpenAIChat)
	}
}

func TestGetCodec_AnthropicMessages(t *testing.T) {
	original := registry
	registry = map[Format]Codec{}
	t.Cleanup(func() {
		registry = original
	})

	register(FormatAnthropicMessages, testCodec{format: FormatAnthropicMessages})
	got, err := Get(FormatAnthropicMessages)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Format() != FormatAnthropicMessages {
		t.Fatalf("Get format = %q, want %q", got.Format(), FormatAnthropicMessages)
	}
}

func TestGetCodec_UnknownFormat(t *testing.T) {
	original := registry
	registry = map[Format]Codec{}
	t.Cleanup(func() {
		registry = original
	})

	if _, err := Get(Format("unknown")); err == nil {
		t.Fatalf("expected error for unknown codec")
	}
}

func TestNormalizeProviderFormat(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    Format
		wantErr bool
	}{
		{name: "openai format", input: "openai.chat", want: FormatOpenAIChat},
		{name: "anthropic format", input: "anthropic.messages", want: FormatAnthropicMessages},
		{name: "ollama chat", input: "ollama.chat", want: FormatOllamaChat},
		{name: "ollama chat uppercase", input: "OLLAMA.CHAT", want: FormatOllamaChat},
		{name: "unknown", input: "unknown", wantErr: true},
		// 简写已废弃，必须返回错误（回归用例）
		{name: "deprecated openai shorthand", input: "openai", wantErr: true},
		{name: "deprecated anthropic shorthand", input: "anthropic", wantErr: true},
		{name: "deprecated openai.response singular", input: "openai.response", wantErr: true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeProviderFormat(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeProviderFormat returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeProviderFormat(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatConstants(t *testing.T) {
	if FormatOpenAIChat != "openai.chat" {
		t.Fatalf("FormatOpenAIChat = %q, want %q", FormatOpenAIChat, "openai.chat")
	}
	if FormatAnthropicMessages != "anthropic.messages" {
		t.Fatalf("FormatAnthropicMessages = %q, want %q", FormatAnthropicMessages, "anthropic.messages")
	}
}

func TestFormatConstants_OpenAIResponse(t *testing.T) {
	if FormatOpenAIResponse != "openai.responses" {
		t.Fatalf("FormatOpenAIResponse = %q, want %q", FormatOpenAIResponse, "openai.responses")
	}
}

func TestConversionCost_Directional(t *testing.T) {
	forward, err := ConversionCost(FormatOpenAIChat, FormatAnthropicMessages)
	if err != nil {
		t.Fatalf("ConversionCost forward error: %v", err)
	}
	reverse, err := ConversionCost(FormatAnthropicMessages, FormatOpenAIChat)
	if err != nil {
		t.Fatalf("ConversionCost reverse error: %v", err)
	}
	if reverse <= forward {
		t.Fatalf("expected reverse > forward, got reverse=%d forward=%d", reverse, forward)
	}
}

func TestConversionCost_Ollama(t *testing.T) {
	cases := []struct {
		inbound  Format
		outbound Format
		want     int
	}{
		{FormatOpenAIChat, FormatOllamaChat, 1},
		{FormatAnthropicMessages, FormatOllamaChat, 4},
		{FormatOpenAIResponse, FormatOllamaChat, 4},
	}

	for _, tt := range cases {
		t.Run(string(tt.inbound)+"->"+string(tt.outbound), func(t *testing.T) {
			got, err := ConversionCost(tt.inbound, tt.outbound)
			if err != nil {
				t.Fatalf("ConversionCost(%q, %q) error: %v", tt.inbound, tt.outbound, err)
			}
			if got != tt.want {
				t.Errorf("ConversionCost(%q, %q) = %d, want %d", tt.inbound, tt.outbound, got, tt.want)
			}
		})
	}
}

func TestSelectBestFormat(t *testing.T) {
	t.Run("prefer passthrough", func(t *testing.T) {
		selected, reason, cost, err := SelectBestFormat([]Format{FormatOpenAIChat, FormatAnthropicMessages}, FormatAnthropicMessages)
		if err != nil {
			t.Fatalf("SelectBestFormat returned error: %v", err)
		}
		if selected != FormatAnthropicMessages || reason != "passthrough" || cost != 0 {
			t.Fatalf("SelectBestFormat = (%q,%q,%d), want (%q,%q,%d)", selected, reason, cost, FormatAnthropicMessages, "passthrough", 0)
		}
	})

	t.Run("lowest cost", func(t *testing.T) {
		selected, reason, cost, err := SelectBestFormat([]Format{FormatAnthropicMessages, FormatOpenAIChat}, FormatOpenAIResponse)
		if err != nil {
			t.Fatalf("SelectBestFormat returned error: %v", err)
		}
		if selected != FormatOpenAIChat || reason != "lowest_cost" || cost != 3 {
			t.Fatalf("SelectBestFormat = (%q,%q,%d), want (%q,%q,%d)", selected, reason, cost, FormatOpenAIChat, "lowest_cost", 3)
		}
	})

	t.Run("stable order for same cost", func(t *testing.T) {
		selected, _, _, err := SelectBestFormat([]Format{FormatOpenAIResponse, FormatAnthropicMessages}, FormatAnthropicMessages)
		if err != nil {
			t.Fatalf("SelectBestFormat returned error: %v", err)
		}
		if selected != FormatAnthropicMessages {
			t.Fatalf("SelectBestFormat selected=%q, want %q", selected, FormatAnthropicMessages)
		}
	})
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	t.Run("empty format", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatalf("expected panic for empty format")
			}
		}()
		register("", testCodec{format: FormatOpenAIChat})
	})

	t.Run("nil codec", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatalf("expected panic for nil codec")
			}
		}()
		register(FormatOpenAIChat, nil)
	})
}

func TestGetCodec_OpenAIResponse(t *testing.T) {
	original := registry
	registry = map[Format]Codec{}
	t.Cleanup(func() {
		registry = original
	})

	register(FormatOpenAIResponse, testCodec{format: FormatOpenAIResponse})
	got, err := Get(FormatOpenAIResponse)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Format() != FormatOpenAIResponse {
		t.Fatalf("Get format = %q, want %q", got.Format(), FormatOpenAIResponse)
	}
}

// ==================== Integration Tests for Multimodal Flow ====================

func TestIntegration_OpenAIToAnthropic(t *testing.T) {
	t.Run("text only request", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model:     "claude-3-opus",
			MaxTokens: 1000,
			Messages: []dto.Message{
				{Role: "system", Content: "You are a helpful assistant."},
				{Role: "user", Content: "Hello, Claude!"},
			},
		}

		claudeReq, err := convertOpenAIToAnthropicRequest(req, "claude-3-opus-20240229", false)
		if err != nil {
			t.Fatalf("convertOpenAIToAnthropicRequest failed: %v", err)
		}

		if claudeReq.Model != "claude-3-opus-20240229" {
			t.Errorf("Model = %q, want %q", claudeReq.Model, "claude-3-opus-20240229")
		}
		if claudeReq.MaxTokens != 1000 {
			t.Errorf("MaxTokens = %d, want 1000", claudeReq.MaxTokens)
		}

		// Verify JSON serialization
		jsonData, err := json.Marshal(claudeReq)
		if err != nil {
			t.Fatalf("JSON marshal failed: %v", err)
		}

		var unmarshaled dto.ClaudeRequest
		if err := json.Unmarshal(jsonData, &unmarshaled); err != nil {
			t.Fatalf("JSON unmarshal failed: %v", err)
		}

		if unmarshaled.Model != claudeReq.Model {
			t.Errorf("Unmarshaled model mismatch")
		}
	})

	t.Run("multimodal request with image", func(t *testing.T) {
		content := []any{
			map[string]any{
				"type": "text",
				"text": "What's in this image?",
			},
			map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEASABIAAD",
				},
			},
		}

		req := &dto.ChatCompletionRequest{
			Model:     "claude-3-vision",
			MaxTokens: 2000,
			Messages: []dto.Message{
				{Role: "user", Content: content},
			},
		}

		claudeReq, err := convertOpenAIToAnthropicRequest(req, "claude-3-opus-20240229", false)
		if err != nil {
			t.Fatalf("convertOpenAIToAnthropicRequest failed: %v", err)
		}

		if len(claudeReq.Messages) != 1 {
			t.Fatalf("Expected 1 message, got %d", len(claudeReq.Messages))
		}

		msg := claudeReq.Messages[0]
		if msg.Role != "user" {
			t.Errorf("Message role = %q, want user", msg.Role)
		}

		// Verify JSON serialization
		jsonData, err := json.Marshal(claudeReq)
		if err != nil {
			t.Fatalf("JSON marshal failed: %v", err)
		}

		// Verify JSON structure
		var result map[string]any
		if err := json.Unmarshal(jsonData, &result); err != nil {
			t.Fatalf("JSON unmarshal to map failed: %v", err)
		}

		messages, ok := result["messages"].([]any)
		if !ok || len(messages) == 0 {
			t.Fatalf("Messages not found or empty in JSON")
		}

		firstMsg, ok := messages[0].(map[string]any)
		if !ok {
			t.Fatalf("First message not a map")
		}

		contentBlocks, ok := firstMsg["content"].([]any)
		if !ok {
			t.Fatalf("Content not an array in JSON")
		}

		if len(contentBlocks) != 2 {
			t.Errorf("Expected 2 content blocks, got %d", len(contentBlocks))
		}
	})

	t.Run("multimodal request with regular URL image", func(t *testing.T) {
		content := []any{
			map[string]any{
				"type": "text",
				"text": "Analyze this image",
			},
			map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "https://example.com/image.png",
				},
			},
		}

		req := &dto.ChatCompletionRequest{
			Model:     "claude-3-vision",
			MaxTokens: 1000,
			Messages: []dto.Message{
				{Role: "user", Content: content},
			},
		}

		claudeReq, err := convertOpenAIToAnthropicRequest(req, "claude-3-opus-20240229", false)
		if err != nil {
			t.Fatalf("convertOpenAIToAnthropicRequest failed: %v", err)
		}

		// Verify JSON serialization contains the URL-based image
		jsonData, err := json.Marshal(claudeReq)
		if err != nil {
			t.Fatalf("JSON marshal failed: %v", err)
		}

		jsonStr := string(jsonData)
		if !strings.Contains(jsonStr, "https://example.com/image.png") {
			t.Errorf("JSON should contain the image URL")
		}
	})

	t.Run("request with file attachment", func(t *testing.T) {
		content := []any{
			map[string]any{
				"type": "text",
				"text": "Summarize this document",
			},
			map[string]any{
				"type": "file",
				"file": map[string]any{
					"file_data": "data:application/pdf;base64,JVBERi0xLjQKJcOkw7zDtsO",
				},
			},
		}

		req := &dto.ChatCompletionRequest{
			Model:     "claude-3-opus",
			MaxTokens: 2000,
			Messages: []dto.Message{
				{Role: "user", Content: content},
			},
		}

		claudeReq, err := convertOpenAIToAnthropicRequest(req, "claude-3-opus-20240229", false)
		if err != nil {
			t.Fatalf("convertOpenAIToAnthropicRequest failed: %v", err)
		}

		// Verify JSON serialization
		jsonData, err := json.Marshal(claudeReq)
		if err != nil {
			t.Fatalf("JSON marshal failed: %v", err)
		}

		jsonStr := string(jsonData)
		if !strings.Contains(jsonStr, "document") {
			t.Errorf("JSON should contain document block")
		}
	})

	t.Run("request with input_audio returns error", func(t *testing.T) {
		content := []any{
			map[string]any{
				"type": "text",
				"text": "Translate this audio",
			},
			map[string]any{
				"type": "input_audio",
				"input_audio": map[string]any{
					"data":   "base64audiodata",
					"format": "mp3",
				},
			},
		}

		req := &dto.ChatCompletionRequest{
			Model:     "claude-3-opus",
			MaxTokens: 2000,
			Messages: []dto.Message{
				{Role: "user", Content: content},
			},
		}

		_, err := convertOpenAIToAnthropicRequest(req, "claude-3-opus-20240229", false)
		if err == nil {
			t.Fatalf("expected error for input_audio content type, got nil")
		}
		if !strings.Contains(err.Error(), "input_audio content type is not supported") {
			t.Errorf("expected error message to contain 'input_audio content type is not supported', got: %v", err)
		}
	})

	t.Run("request with video_url returns error", func(t *testing.T) {
		content := []any{
			map[string]any{
				"type": "text",
				"text": "Describe this video",
			},
			map[string]any{
				"type": "video_url",
				"video_url": map[string]any{
					"url": "https://example.com/video.mp4",
				},
			},
		}

		req := &dto.ChatCompletionRequest{
			Model:     "claude-3-opus",
			MaxTokens: 2000,
			Messages: []dto.Message{
				{Role: "user", Content: content},
			},
		}

		_, err := convertOpenAIToAnthropicRequest(req, "claude-3-opus-20240229", false)
		if err == nil {
			t.Fatalf("expected error for video_url content type, got nil")
		}
		if !strings.Contains(err.Error(), "video_url content type is not supported") {
			t.Errorf("expected error message to contain 'video_url content type is not supported', got: %v", err)
		}
	})
}

func TestIntegration_AnthropicToOpenAI(t *testing.T) {
	t.Run("text only request", func(t *testing.T) {
		req := &dto.ClaudeRequest{
			Model:     "gpt-4",
			MaxTokens: 1000,
			Messages: []dto.ClaudeMessage{
				{Role: "user", Content: "Hello, GPT!"},
			},
		}

		openAIReq := convertAnthropicToOpenAIRequest(req, "gpt-4-turbo")

		if openAIReq.Model != "gpt-4-turbo" {
			t.Errorf("Model = %q, want %q", openAIReq.Model, "gpt-4-turbo")
		}
		if openAIReq.MaxTokens != 1000 {
			t.Errorf("MaxTokens = %d, want 1000", openAIReq.MaxTokens)
		}
		if len(openAIReq.Messages) != 1 {
			t.Fatalf("Expected 1 message, got %d", len(openAIReq.Messages))
		}
		if openAIReq.Messages[0].Role != "user" {
			t.Errorf("Message role = %q, want user", openAIReq.Messages[0].Role)
		}
	})

	t.Run("multimodal request with image block", func(t *testing.T) {
		req := &dto.ClaudeRequest{
			Model:     "gpt-4-vision",
			MaxTokens: 2000,
			Messages: []dto.ClaudeMessage{
				{
					Role: "user",
					Content: []dto.ContentBlock{
						{Type: "text", Text: "What's in this image?"},
						{
							Type: "image",
							Source: &dto.MessageSource{
								Type:      "base64",
								MediaType: "image/jpeg",
								Data:      "/9j/4AAQSkZJRgABAQEASABIAAD",
							},
						},
					},
				},
			},
		}

		openAIReq := convertAnthropicToOpenAIRequest(req, "gpt-4o")

		if len(openAIReq.Messages) != 1 {
			t.Fatalf("Expected 1 message, got %d", len(openAIReq.Messages))
		}

		msg := openAIReq.Messages[0]
		if msg.Role != "user" {
			t.Errorf("Message role = %q, want user", msg.Role)
		}

		// Verify content is array format (could be []any)
		_, ok := msg.Content.([]any)
		if !ok {
			t.Fatalf("Content should be array format, got %T", msg.Content)
		}

		// Verify content parts count by marshaling and checking
		contentBytes, err := json.Marshal(msg.Content)
		if err != nil {
			t.Fatalf("Failed to marshal content: %v", err)
		}
		var parts []map[string]any
		if err := json.Unmarshal(contentBytes, &parts); err != nil {
			t.Fatalf("Failed to unmarshal content: %v", err)
		}
		if len(parts) != 2 {
			t.Errorf("Expected 2 content parts, got %d", len(parts))
		}

		// Verify JSON serialization
		jsonData, err := json.Marshal(openAIReq)
		if err != nil {
			t.Fatalf("JSON marshal failed: %v", err)
		}

		var result map[string]any
		if err := json.Unmarshal(jsonData, &result); err != nil {
			t.Fatalf("JSON unmarshal failed: %v", err)
		}
	})

	t.Run("json-unmarshaled base64 image does not panic", func(t *testing.T) {
		// Content 经 JSON 解码是 []any/map，不是 []dto.ContentBlock。
		// base64 source 没有 url 字段，裸断言会 panic。
		raw := []byte(`{
			"model": "vision",
			"max_tokens": 256,
			"messages": [{
				"role": "user",
				"content": [
					{"type": "text", "text": "describe"},
					{"type": "image", "source": {"type": "base64", "media_type": "image/jpeg", "data": "/9j/xxxx"}}
				]
			}]
		}`)
		var req dto.ClaudeRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		openAIReq := convertAnthropicToOpenAIRequest(&req, "gpt-4o")
		jsonData, err := json.Marshal(openAIReq)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(jsonData), "data:image/jpeg;base64,/9j/xxxx") {
			t.Errorf("converted request missing data URI, got %s", jsonData)
		}
	})

	t.Run("json-unmarshaled url image does not panic", func(t *testing.T) {
		raw := []byte(`{
			"model": "vision",
			"messages": [{
				"role": "user",
				"content": [
					{"type": "image", "source": {"type": "url", "url": "https://example.com/a.jpg"}}
				]
			}]
		}`)
		var req dto.ClaudeRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		openAIReq := convertAnthropicToOpenAIRequest(&req, "gpt-4o")
		jsonData, err := json.Marshal(openAIReq)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(jsonData), "https://example.com/a.jpg") {
			t.Errorf("converted request missing image URL, got %s", jsonData)
		}
	})

	t.Run("multimodal request with image URL", func(t *testing.T) {
		req := &dto.ClaudeRequest{
			Model:     "gpt-4-vision",
			MaxTokens: 1000,
			Messages: []dto.ClaudeMessage{
				{
					Role: "user",
					Content: []dto.ContentBlock{
						{Type: "text", Text: "Analyze this image"},
						{
							Type: "image",
							Source: &dto.MessageSource{
								Type: "url",
								Url:  "https://example.com/photo.jpg",
							},
						},
					},
				},
			},
		}

		openAIReq := convertAnthropicToOpenAIRequest(req, "gpt-4o")

		// Verify JSON serialization contains URL
		jsonData, err := json.Marshal(openAIReq)
		if err != nil {
			t.Fatalf("JSON marshal failed: %v", err)
		}

		jsonStr := string(jsonData)
		if !strings.Contains(jsonStr, "https://example.com/photo.jpg") {
			t.Errorf("JSON should contain the image URL")
		}
	})

	t.Run("request with document block", func(t *testing.T) {
		req := &dto.ClaudeRequest{
			Model:     "gpt-4-turbo",
			MaxTokens: 2000,
			Messages: []dto.ClaudeMessage{
				{
					Role: "user",
					Content: []dto.ContentBlock{
						{Type: "text", Text: "Summarize this document"},
						{
							Type: "document",
							Source: &dto.MessageSource{
								Type:      "base64",
								MediaType: "application/pdf",
								Data:      "JVBERi0xLjQKJcOkw7zDtsO",
							},
						},
					},
				},
			},
		}

		openAIReq := convertAnthropicToOpenAIRequest(req, "gpt-4o")

		// Verify JSON serialization contains document
		jsonData, err := json.Marshal(openAIReq)
		if err != nil {
			t.Fatalf("JSON marshal failed: %v", err)
		}

		jsonStr := string(jsonData)
		if !strings.Contains(jsonStr, "file") || !strings.Contains(jsonStr, "data:") {
			t.Errorf("JSON should contain file block with data URI")
		}
	})

	t.Run("request with system message", func(t *testing.T) {
		req := &dto.ClaudeRequest{
			Model:     "gpt-4",
			MaxTokens: 1000,
			System:    "You are a helpful coding assistant.",
			Messages: []dto.ClaudeMessage{
				{Role: "user", Content: "Write a hello world in Go"},
			},
		}

		openAIReq := convertAnthropicToOpenAIRequest(req, "gpt-4-turbo")

		if len(openAIReq.Messages) != 2 {
			t.Fatalf("Expected 2 messages (system + user), got %d", len(openAIReq.Messages))
		}

		if openAIReq.Messages[0].Role != "system" {
			t.Errorf("First message role = %q, want system", openAIReq.Messages[0].Role)
		}
	})
}

func TestConvertMapToMessageSource_MissingFields(t *testing.T) {
	if got := convertMapToMessageSource(nil); got != nil {
		t.Fatalf("nil map: got %#v, want nil", got)
	}

	base64Src := convertMapToMessageSource(map[string]any{
		"type":       "base64",
		"media_type": "image/jpeg",
		"data":       "abc",
	})
	if base64Src == nil || base64Src.Type != "base64" || base64Src.Data != "abc" || base64Src.Url != "" {
		t.Fatalf("base64 source = %#v", base64Src)
	}

	urlSrc := convertMapToMessageSource(map[string]any{
		"type": "url",
		"url":  "https://example.com/a.jpg",
	})
	if urlSrc == nil || urlSrc.Type != "url" || urlSrc.Url != "https://example.com/a.jpg" || urlSrc.Data != "" {
		t.Fatalf("url source = %#v", urlSrc)
	}
}

func TestAnthropicEncodeRequest_JSONImageToChat(t *testing.T) {
	raw := []byte(`{
		"model": "vision",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "hi"},
				{"type": "image", "source": {"type": "base64", "media_type": "image/jpeg", "data": "abc"}}
			]
		}]
	}`)
	var req dto.ClaudeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	c := &AnthropicMessagesCodec{}
	body, err := c.EncodeRequest(FormatOpenAIChat, &req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if !strings.Contains(string(body), "data:image/jpeg;base64,abc") {
		t.Errorf("encoded body missing data URI: %s", body)
	}
}

func TestIntegration_Response(t *testing.T) {
	t.Run("Claude response to OpenAI", func(t *testing.T) {
		stopReason := "end_turn"
		claudeResp := &dto.ClaudeResponse{
			ID:         "msg_12345",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-3-opus",
			StopReason: &stopReason,
			Content: []dto.ContentBlock{
				{Type: "text", Text: "Hello! How can I help you today?"},
			},
			Usage: dto.ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 15,
			},
		}

		openAIResp := convertClaudeResponseToOpenAI(claudeResp)

		if openAIResp.ID != "msg_12345" {
			t.Errorf("ID = %q, want %q", openAIResp.ID, "msg_12345")
		}
		if openAIResp.Object != "chat.completion" {
			t.Errorf("Object = %q, want chat.completion", openAIResp.Object)
		}
		if openAIResp.Model != "claude-3-opus" {
			t.Errorf("Model = %q, want claude-3-opus", openAIResp.Model)
		}
		if len(openAIResp.Choices) != 1 {
			t.Fatalf("Expected 1 choice, got %d", len(openAIResp.Choices))
		}

		msg := openAIResp.Choices[0].Message
		if msg == nil {
			t.Fatalf("Message is nil")
		}
		if msg.Role != "assistant" {
			t.Errorf("Message role = %q, want assistant", msg.Role)
		}
		if msg.Content != "Hello! How can I help you today?" {
			t.Errorf("Message content = %q, want 'Hello! How can I help you today?'", msg.Content)
		}

		// Verify usage
		if openAIResp.Usage.PromptTokens != 10 {
			t.Errorf("PromptTokens = %d, want 10", openAIResp.Usage.PromptTokens)
		}
		if openAIResp.Usage.CompletionTokens != 15 {
			t.Errorf("CompletionTokens = %d, want 15", openAIResp.Usage.CompletionTokens)
		}

		// Verify JSON serialization
		jsonData, err := json.Marshal(openAIResp)
		if err != nil {
			t.Fatalf("JSON marshal failed: %v", err)
		}

		var result map[string]any
		if err := json.Unmarshal(jsonData, &result); err != nil {
			t.Fatalf("JSON unmarshal failed: %v", err)
		}
	})

	t.Run("OpenAI response to Claude", func(t *testing.T) {
		finishReason := "stop"
		openAIResp := &dto.ChatCompletionResponse{
			ID:      "chatcmpl_12345",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "gpt-4",
			Choices: []dto.Choice{
				{
					Index: 0,
					Message: &dto.ResMessage{
						Role:    "assistant",
						Content: "Hello! How can I assist you?",
					},
					FinishReason: &finishReason,
				},
			},
			Usage: dto.Usage{
				PromptTokens:     8,
				CompletionTokens: 12,
				TotalTokens:      20,
			},
		}

		claudeResp := convertOpenAIResponseToAnthropic(openAIResp)

		if claudeResp.ID != "chatcmpl_12345" {
			t.Errorf("ID = %q, want %q", claudeResp.ID, "chatcmpl_12345")
		}
		if claudeResp.Type != "message" {
			t.Errorf("Type = %q, want message", claudeResp.Type)
		}
		if claudeResp.Role != "assistant" {
			t.Errorf("Role = %q, want assistant", claudeResp.Role)
		}
		if claudeResp.Model != "gpt-4" {
			t.Errorf("Model = %q, want gpt-4", claudeResp.Model)
		}
		if len(claudeResp.Content) != 1 {
			t.Fatalf("Expected 1 content block, got %d", len(claudeResp.Content))
		}
		if claudeResp.Content[0].Type != "text" {
			t.Errorf("Content type = %q, want text", claudeResp.Content[0].Type)
		}
		if claudeResp.Content[0].Text != "Hello! How can I assist you?" {
			t.Errorf("Content text = %q, want 'Hello! How can I assist you?'", claudeResp.Content[0].Text)
		}

		// Verify usage
		if claudeResp.Usage.InputTokens != 8 {
			t.Errorf("InputTokens = %d, want 8", claudeResp.Usage.InputTokens)
		}
		if claudeResp.Usage.OutputTokens != 12 {
			t.Errorf("OutputTokens = %d, want 12", claudeResp.Usage.OutputTokens)
		}

		// Verify JSON serialization
		jsonData, err := json.Marshal(claudeResp)
		if err != nil {
			t.Fatalf("JSON marshal failed: %v", err)
		}

		var result map[string]any
		if err := json.Unmarshal(jsonData, &result); err != nil {
			t.Fatalf("JSON unmarshal failed: %v", err)
		}
	})

	t.Run("Claude response with tool calls to OpenAI", func(t *testing.T) {
		stopReason := "tool_use"
		claudeResp := &dto.ClaudeResponse{
			ID:         "msg_tool_123",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-3-opus",
			StopReason: &stopReason,
			Content: []dto.ContentBlock{
				{Type: "text", Text: "I'll search for that information."},
				{
					Type:  "tool_use",
					ID:    "tool_123",
					Name:  "web_search",
					Input: map[string]any{"query": "current weather"},
				},
			},
			Usage: dto.ClaudeUsage{
				InputTokens:  20,
				OutputTokens: 25,
			},
		}

		openAIResp := convertClaudeResponseToOpenAI(claudeResp)

		if len(openAIResp.Choices) != 1 {
			t.Fatalf("Expected 1 choice, got %d", len(openAIResp.Choices))
		}

		msg := openAIResp.Choices[0].Message
		if msg == nil {
			t.Fatalf("Message is nil")
		}

		// Content should be the text part
		if msg.Content != "I'll search for that information." {
			t.Errorf("Message content = %q", msg.Content)
		}

		// Should have tool_calls
		if len(msg.ToolCalls) != 1 {
			t.Fatalf("Expected 1 tool call, got %d", len(msg.ToolCalls))
		}

		if msg.ToolCalls[0].ID != "tool_123" {
			t.Errorf("Tool call ID = %q, want tool_123", msg.ToolCalls[0].ID)
		}
		if msg.ToolCalls[0].Function.Name != "web_search" {
			t.Errorf("Tool call name = %q, want web_search", msg.ToolCalls[0].Function.Name)
		}

		// Finish reason should be tool_calls
		if openAIResp.Choices[0].FinishReason == nil || *openAIResp.Choices[0].FinishReason != "tool_calls" {
			t.Errorf("Finish reason should be tool_calls")
		}
	})

	t.Run("OpenAI response with data URI image to Claude", func(t *testing.T) {
		finishReason := "stop"
		openAIResp := &dto.ChatCompletionResponse{
			ID:      "chatcmpl_img_123",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "gpt-4o",
			Choices: []dto.Choice{
				{
					Index: 0,
					Message: &dto.ResMessage{
						Role:    "assistant",
						Content: "data:image/png;base64,iVBORw0KGgoAAAA",
					},
					FinishReason: &finishReason,
				},
			},
			Usage: dto.Usage{
				PromptTokens:     50,
				CompletionTokens: 100,
				TotalTokens:      150,
			},
		}

		claudeResp := convertOpenAIResponseToAnthropic(openAIResp)

		if len(claudeResp.Content) != 1 {
			t.Fatalf("Expected 1 content block, got %d", len(claudeResp.Content))
		}

		// Should be an image block
		if claudeResp.Content[0].Type != "image" {
			t.Errorf("Content type = %q, want image", claudeResp.Content[0].Type)
		}

		if claudeResp.Content[0].Source == nil {
			t.Fatalf("Source is nil")
		}

		if claudeResp.Content[0].Source.Type != "base64" {
			t.Errorf("Source type = %q, want base64", claudeResp.Content[0].Source.Type)
		}

		if claudeResp.Content[0].Source.MediaType != "image/png" {
			t.Errorf("Source media_type = %q, want image/png", claudeResp.Content[0].Source.MediaType)
		}
	})
}

// ==================== Integration Tests for OpenAI Response Protocol ====================

func TestIntegration_OpenAIResponseProtocol(t *testing.T) {
	t.Run("text only request conversion", func(t *testing.T) {
		req := &dto.ResponsesRequest{
			Model:           "gpt-4o",
			MaxOutputTokens: 1000,
			Instructions:    json.RawMessage(`"You are a helpful assistant"`),
			Input:           json.RawMessage(`"Hello, how are you?"`),
		}

		chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4-turbo")
		if err != nil {
			t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
		}

		if chatReq.Model != "gpt-4-turbo" {
			t.Errorf("Model = %q, want %q", chatReq.Model, "gpt-4-turbo")
		}
		if chatReq.MaxTokens != 1000 {
			t.Errorf("MaxTokens = %d, want 1000", chatReq.MaxTokens)
		}
		if len(chatReq.Messages) != 2 {
			t.Fatalf("Expected 2 messages (system + user), got %d", len(chatReq.Messages))
		}
		if chatReq.Messages[0].Role != "system" {
			t.Errorf("First message role = %q, want system", chatReq.Messages[0].Role)
		}
		if chatReq.Messages[1].Role != "user" {
			t.Errorf("Second message role = %q, want user", chatReq.Messages[1].Role)
		}

		// Verify JSON serialization
		jsonData, err := json.Marshal(chatReq)
		if err != nil {
			t.Fatalf("JSON marshal failed: %v", err)
		}

		var result map[string]any
		if err := json.Unmarshal(jsonData, &result); err != nil {
			t.Fatalf("JSON unmarshal failed: %v", err)
		}
	})

	t.Run("text array content conversion", func(t *testing.T) {
		inputItems := []dto.ResponsesInputItem{
			{
				Type: "message",
				Role: "user",
				Content: json.RawMessage(`[
					{"type": "input_text", "text": "Hello "},
					{"type": "output_text", "text": "world!"}
				]`),
			},
		}

		inputJSON, _ := json.Marshal(inputItems)
		req := &dto.ResponsesRequest{
			Model:           "gpt-4o",
			MaxOutputTokens: 2000,
			Input:           json.RawMessage(inputJSON),
		}

		chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
		if err != nil {
			t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
		}

		if len(chatReq.Messages) != 1 {
			t.Fatalf("Expected 1 message, got %d", len(chatReq.Messages))
		}

		// Text content should be concatenated
		if content, ok := chatReq.Messages[0].Content.(string); ok {
			if content != "Hello world!" {
				t.Errorf("Content = %q, want \"Hello world!\"", content)
			}
		} else {
			t.Errorf("Content should be string, got %T", chatReq.Messages[0].Content)
		}
	})

	t.Run("Chat to Response conversion with multimodal content", func(t *testing.T) {
		content := []any{
			map[string]any{
				"type": "text",
				"text": "Analyze these inputs",
			},
			map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "https://example.com/photo.png",
				},
			},
		}

		req := &dto.ChatCompletionRequest{
			Model:     "gpt-4o",
			MaxTokens: 2000,
			Messages: []dto.Message{
				{Role: "user", Content: content},
			},
		}

		respReq, err := convertChatToResponseRequest(req, "gpt-4o-2024-08-06")
		if err != nil {
			t.Fatalf("convertChatToResponseRequest failed: %v", err)
		}

		if respReq.Model != "gpt-4o-2024-08-06" {
			t.Errorf("Model = %q, want %q", respReq.Model, "gpt-4o-2024-08-06")
		}
		if respReq.MaxOutputTokens != 2000 {
			t.Errorf("MaxOutputTokens = %d, want 2000", respReq.MaxOutputTokens)
		}

		// Verify JSON serialization
		jsonData, err := json.Marshal(respReq)
		if err != nil {
			t.Fatalf("JSON marshal failed: %v", err)
		}

		// Verify input contains image
		jsonStr := string(jsonData)
		if !strings.Contains(jsonStr, "input_image") {
			t.Errorf("JSON should contain input_image")
		}
		if !strings.Contains(jsonStr, "https://example.com/photo.png") {
			t.Errorf("JSON should contain image URL")
		}
	})

	t.Run("Chat to Response conversion with file attachment", func(t *testing.T) {
		content := []any{
			map[string]any{
				"type": "text",
				"text": "Summarize this document",
			},
			map[string]any{
				"type": "file",
				"file": map[string]any{
					"file_data": "data:application/pdf;base64,JVBERi0xLjQKJcOkw7zDtsO",
				},
			},
		}

		req := &dto.ChatCompletionRequest{
			Model:     "gpt-4o",
			MaxTokens: 3000,
			Messages: []dto.Message{
				{Role: "user", Content: content},
			},
		}

		respReq, err := convertChatToResponseRequest(req, "gpt-4o")
		if err != nil {
			t.Fatalf("convertChatToResponseRequest failed: %v", err)
		}

		// Verify JSON serialization
		jsonData, err := json.Marshal(respReq)
		if err != nil {
			t.Fatalf("JSON marshal failed: %v", err)
		}

		// Verify input contains file
		jsonStr := string(jsonData)
		if !strings.Contains(jsonStr, "input_file") {
			t.Errorf("JSON should contain input_file")
		}
	})

	t.Run("multimodal passthrough with Response protocol passthrough mode", func(t *testing.T) {
		// Test conversion using best format selection for Response inbound
		formats := []Format{FormatOpenAIResponse, FormatOpenAIChat}
		selected, reason, cost, err := SelectBestFormat(formats, FormatOpenAIResponse)
		if err != nil {
			t.Fatalf("SelectBestFormat returned error: %v", err)
		}
		if selected != FormatOpenAIResponse {
			t.Errorf("Expected passthrough to OpenAI Response, got %q", selected)
		}
		if reason != "passthrough" {
			t.Errorf("Expected reason 'passthrough', got %q", reason)
		}
		if cost != 0 {
			t.Errorf("Expected cost 0 for passthrough, got %d", cost)
		}
	})
}

// ==================== Responses Protocol Completion Tests ====================

func TestResponseToChat_InputImage(t *testing.T) {
	// 测试纯文本 + 图片混合转换为 Chat 格式
	inputItems := []dto.ResponsesInputItem{
		{
			Type: "message",
			Role: "user",
			Content: json.RawMessage(`[
				{"type": "input_text", "text": "What's in this image?"},
				{"type": "input_image", "image_url": "https://example.com/image.png"}
			]`),
		},
	}

	inputJSON, _ := json.Marshal(inputItems)
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
	}

	if len(chatReq.Messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(chatReq.Messages))
	}

	// 验证返回的是 []MediaContent 数组
	content, ok := chatReq.Messages[0].Content.([]dto.MediaContent)
	if !ok {
		t.Fatalf("Content should be []MediaContent, got %T", chatReq.Messages[0].Content)
	}

	if len(content) != 2 {
		t.Fatalf("Expected 2 content parts, got %d", len(content))
	}

	// 验证第一部分是文本
	if content[0].Type != "text" {
		t.Errorf("First part type = %q, want text", content[0].Type)
	}
	if content[0].Text != "What's in this image?" {
		t.Errorf("First part text = %q, want 'What's in this image?'", content[0].Text)
	}

	// 验证第二部分是图片
	if content[1].Type != "image_url" {
		t.Errorf("Second part type = %q, want image_url", content[1].Type)
	}
	if content[1].ImageUrl == nil {
		t.Errorf("Second part image_url is nil")
	} else {
		imgUrl, ok := content[1].ImageUrl.(*dto.MessageImageUrl)
		if !ok {
			t.Errorf("ImageUrl type = %T, want *dto.MessageImageUrl", content[1].ImageUrl)
		} else if imgUrl.Url != "https://example.com/image.png" {
			t.Errorf("ImageUrl.Url = %q, want 'https://example.com/image.png'", imgUrl.Url)
		}
	}
}

func TestResponseToChat_InputImageOnly(t *testing.T) {
	// 测试纯图片（无文本）
	inputItems := []dto.ResponsesInputItem{
		{
			Type: "message",
			Role: "user",
			Content: json.RawMessage(`[
				{"type": "input_image", "image_url": {"url": "https://example.com/photo.jpg", "detail": "high"}}
			]`),
		},
	}

	inputJSON, _ := json.Marshal(inputItems)
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
	}

	content, ok := chatReq.Messages[0].Content.([]dto.MediaContent)
	if !ok {
		t.Fatalf("Content should be []MediaContent, got %T", chatReq.Messages[0].Content)
	}

	if len(content) != 1 {
		t.Fatalf("Expected 1 content part, got %d", len(content))
	}

	if content[0].Type != "image_url" {
		t.Errorf("Content type = %q, want image_url", content[0].Type)
	}
	if content[0].ImageUrl == nil {
		t.Errorf("Image URL is nil")
	} else {
		imgUrl, ok := content[0].ImageUrl.(*dto.MessageImageUrl)
		if !ok {
			t.Errorf("ImageUrl type = %T, want *dto.MessageImageUrl", content[0].ImageUrl)
		} else if imgUrl.Url != "https://example.com/photo.jpg" {
			t.Errorf("ImageUrl.Url = %q, want 'https://example.com/photo.jpg'", imgUrl.Url)
		}
	}
}

func TestResponseToChat_McpToolCallOutput(t *testing.T) {
	// 测试 MCP 工具输出转换为 Chat 格式
	inputItems := []dto.ResponsesInputItem{
		{
			Type:   "mcp_tool_call_output",
			CallID: "call_123",
			Output: json.RawMessage(`{"content": [{"type": "text", "text": "Tool result from MCP"}]}`),
		},
	}

	inputJSON, _ := json.Marshal(inputItems)
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
	}

	if len(chatReq.Messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(chatReq.Messages))
	}

	msg := chatReq.Messages[0]
	if msg.Role != "tool" {
		t.Errorf("Message role = %q, want tool", msg.Role)
	}
	if msg.ToolCallID != "call_123" {
		t.Errorf("ToolCallID = %q, want call_123", msg.ToolCallID)
	}
	if msg.Content != "Tool result from MCP" {
		t.Errorf("Content = %q, want 'Tool result from MCP'", msg.Content)
	}
}

func TestResponseToChat_McpToolCallOutputEmpty(t *testing.T) {
	// 测试空 output 对象降级处理
	inputItems := []dto.ResponsesInputItem{
		{
			Type:   "mcp_tool_call_output",
			CallID: "call_empty",
			Output: json.RawMessage(`{}`),
		},
	}

	inputJSON, _ := json.Marshal(inputItems)
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
	}

	// 应该成功转换，不报错，content 为空字符串
	if chatReq.Messages[0].Content != "" {
		t.Errorf("Content = %q, want empty string", chatReq.Messages[0].Content)
	}
}

func TestResponseToChat_CustomToolCallOutput(t *testing.T) {
	// 测试自定义工具输出（字符串格式）
	inputItems := []dto.ResponsesInputItem{
		{
			Type:   "custom_tool_call_output",
			CallID: "call_456",
			Output: json.RawMessage(`"Simple string output"`),
		},
	}

	inputJSON, _ := json.Marshal(inputItems)
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
	}

	msg := chatReq.Messages[0]
	if msg.Role != "tool" {
		t.Errorf("Message role = %q, want tool", msg.Role)
	}
	if msg.Content != "Simple string output" {
		t.Errorf("Content = %q, want 'Simple string output'", msg.Content)
	}
}

func TestResponseToChat_ParallelToolCalls(t *testing.T) {
	// 测试 parallel_tool_calls 字段透传
	req := &dto.ResponsesRequest{
		Model:             "gpt-4o",
		Input:             json.RawMessage(`"test"`),
		ParallelToolCalls: json.RawMessage(`true`),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
	}

	if chatReq.ParallelToolCalls == nil || *chatReq.ParallelToolCalls != true {
		t.Errorf("ParallelToolCalls should be true, got %v", chatReq.ParallelToolCalls)
	}
}

func TestResponseToChat_ReasoningContent(t *testing.T) {
	// 测试 reasoning output item 的 content 字段提取
	resp := &dto.ResponsesResponse{
		ID:        "resp_123",
		Object:    "response",
		CreatedAt: 1234567890,
		Model:     "o1",
		Status:    "completed",
		Output: []dto.ResponsesOutput{
			{
				Type: "reasoning",
				ID:   "reasoning_1",
				Content: []dto.ResponsesOutputContent{
					{Type: "reasoning_text", Text: "Step 1: Analyze the problem. "},
					{Type: "reasoning_text", Text: "Step 2: Find solution."},
				},
				Summary: json.RawMessage(`[{"type": "summary_text", "text": "Problem solved."}]`),
			},
			{
				Type: "message",
				Role: "assistant",
				Content: []dto.ResponsesOutputContent{
					{Type: "output_text", Text: "Final answer"},
				},
			},
		},
		Usage: dto.ResponsesUsage{
			InputTokens:  10,
			OutputTokens: 20,
		},
	}

	chatResp, err := convertOpenAIResponseToChat(resp)
	if err != nil {
		t.Fatalf("convertOpenAIResponseToChat failed: %v", err)
	}

	if len(chatResp.Choices) != 1 {
		t.Fatalf("Expected 1 choice, got %d", len(chatResp.Choices))
	}

	msg := chatResp.Choices[0].Message
	if msg == nil {
		t.Fatal("Message is nil")
	}

	// 验证 reasoning_content 包含详细推理和摘要
	expectedReasoning := "Step 1: Analyze the problem. Step 2: Find solution.Problem solved."
	if msg.ReasoningContent != expectedReasoning {
		t.Errorf("ReasoningContent = %q, want %q", msg.ReasoningContent, expectedReasoning)
	}

	// 验证最终输出文本
	if msg.Content != "Final answer" {
		t.Errorf("Content = %q, want 'Final answer'", msg.Content)
	}
}

func TestResponseToChat_UnknownOutputType(t *testing.T) {
	// 测试未知 output item type 优雅跳过
	resp := &dto.ResponsesResponse{
		ID:        "resp_456",
		Object:    "response",
		CreatedAt: 1234567890,
		Model:     "gpt-4o",
		Status:    "completed",
		Output: []dto.ResponsesOutput{
			{
				Type: "tool_search_call", // 未知类型
				ID:   "unknown_1",
			},
			{
				Type: "message",
				Role: "assistant",
				Content: []dto.ResponsesOutputContent{
					{Type: "output_text", Text: "Valid output"},
				},
			},
		},
		Usage: dto.ResponsesUsage{
			InputTokens:  5,
			OutputTokens: 10,
		},
	}

	chatResp, err := convertOpenAIResponseToChat(resp)
	if err != nil {
		t.Fatalf("convertOpenAIResponseToChat should not fail for unknown output type, got: %v", err)
	}

	// 验证正常生成响应
	if len(chatResp.Choices) != 1 {
		t.Fatalf("Expected 1 choice, got %d", len(chatResp.Choices))
	}

	// 验证有效输出仍然存在
	if chatResp.Choices[0].Message.Content != "Valid output" {
		t.Errorf("Content = %q, want 'Valid output'", chatResp.Choices[0].Message.Content)
	}
}

func TestResponseToChat_AdditionalTools(t *testing.T) {
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(`[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"additional_tools"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}
		]`),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest error: %v", err)
	}

	if len(chatReq.Messages) != 2 {
		t.Fatalf("expected 2 messages (additional_tools skipped), got %d: %+v", len(chatReq.Messages), chatReq.Messages)
	}
	if chatReq.Messages[0].Role != "user" {
		t.Errorf("messages[0].Role = %q, want user", chatReq.Messages[0].Role)
	}
	if chatReq.Messages[1].Role != "assistant" {
		t.Errorf("messages[1].Role = %q, want assistant", chatReq.Messages[1].Role)
	}
}

func TestResponseToChat_ReasoningItemSkipped(t *testing.T) {
	// reasoning item 在 Chat API 无对应输入语义，应跳过且不打乱 tool_call 与 output 的交错排列。
	req := &dto.ResponsesRequest{
		Model: "deepseek-v4-flash",
		Input: json.RawMessage(`[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thinking..."}]},
			{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"beijing\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"sunny"},
			{"type":"reasoning","id":"rs_2","content":[{"type":"reasoning_text","text":"done"}]}
		]`),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest error: %v", err)
	}

	if len(chatReq.Messages) != 3 {
		t.Fatalf("expected 3 messages (reasoning skipped), got %d: %+v", len(chatReq.Messages), chatReq.Messages)
	}
	if chatReq.Messages[0].Role != "user" {
		t.Errorf("messages[0].Role = %q, want user", chatReq.Messages[0].Role)
	}
	if chatReq.Messages[1].Role != "assistant" || len(chatReq.Messages[1].ToolCalls) != 1 {
		t.Errorf("messages[1] = %+v, want assistant with 1 tool call", chatReq.Messages[1])
	}
	if chatReq.Messages[2].Role != "tool" || chatReq.Messages[2].ToolCallID != "call_1" {
		t.Errorf("messages[2] = %+v, want tool call_1", chatReq.Messages[2])
	}
}

func TestResponseToChat_ReasoningItemEncodesToChat(t *testing.T) {
	// responses -> chat 的出站编码不应因 reasoning item 报 conversion error。
	req := &dto.ResponsesRequest{
		Model: "coding",
		Input: json.RawMessage(`[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thinking..."}]}
		]`),
	}

	body, err := (&OpenAIResponseCodec{}).EncodeRequest(FormatOpenAIChat, req, "coding", false)
	if err != nil {
		t.Fatalf("EncodeRequest error: %v", err)
	}

	var encoded dto.ChatCompletionRequest
	if err := json.Unmarshal(body, &encoded); err != nil {
		t.Fatalf("unmarshal encoded body: %v", err)
	}
	if len(encoded.Messages) != 1 || encoded.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want single user message", encoded.Messages)
	}
}

func TestResponseToChat_FunctionCallInterleaved(t *testing.T) {
	// 测试 function_call 和 function_call_output 交错排列
	inputItems := []dto.ResponsesInputItem{
		{
			Type: "message", Role: "user",
			Content: json.RawMessage(`"call func"`),
		},
		{
			Type:      "function_call",
			CallID:    "call_1",
			Name:      "get_weather",
			Arguments: `{"city":"beijing"}`,
		},
		{
			Type:   "function_call_output",
			CallID: "call_1",
			Output: json.RawMessage(`{"content":[{"type":"text","text":"sunny"}]}`),
		},
		{
			Type: "message", Role: "assistant",
			Content: json.RawMessage(`"done"`),
		},
	}

	inputJSON, _ := json.Marshal(inputItems)
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
	}

	if len(chatReq.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(chatReq.Messages))
	}
	// user
	if chatReq.Messages[0].Role != "user" {
		t.Errorf("messages[0].Role = %q, want user", chatReq.Messages[0].Role)
	}
	// assistant (function_call)
	if chatReq.Messages[1].Role != "assistant" {
		t.Errorf("messages[1].Role = %q, want assistant", chatReq.Messages[1].Role)
	}
	if len(chatReq.Messages[1].ToolCalls) != 1 {
		t.Errorf("messages[1].ToolCalls = %d, want 1", len(chatReq.Messages[1].ToolCalls))
	}
	// tool (function_call_output interleaved)
	if chatReq.Messages[2].Role != "tool" {
		t.Errorf("messages[2].Role = %q, want tool", chatReq.Messages[2].Role)
	}
	if chatReq.Messages[2].ToolCallID != "call_1" {
		t.Errorf("messages[2].ToolCallID = %q, want call_1", chatReq.Messages[2].ToolCallID)
	}
	// assistant
	if chatReq.Messages[3].Role != "assistant" {
		t.Errorf("messages[3].Role = %q, want assistant", chatReq.Messages[3].Role)
	}
}

func TestResponseToChat_McpToolCallInterleaved(t *testing.T) {
	// 测试 mcp_tool_call 和 mcp_tool_call_output 交错排列
	inputItems := []dto.ResponsesInputItem{
		{
			Type: "message", Role: "user",
			Content: json.RawMessage(`"mcp call"`),
		},
		{
			Type:      "mcp_tool_call",
			CallID:    "mcp_1",
			Name:      "read_file",
			Arguments: `{"path":"/tmp/test"}`,
		},
		{
			Type:   "mcp_tool_call_output",
			CallID: "mcp_1",
			Output: json.RawMessage(`{"content":[{"type":"text","text":"file content"}]}`),
		},
	}

	inputJSON, _ := json.Marshal(inputItems)
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
	}

	if len(chatReq.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[1].Role != "assistant" {
		t.Errorf("messages[1].Role = %q, want assistant", chatReq.Messages[1].Role)
	}
	if chatReq.Messages[2].Role != "tool" {
		t.Errorf("messages[2].Role = %q, want tool", chatReq.Messages[2].Role)
	}
	if chatReq.Messages[2].ToolCallID != "mcp_1" {
		t.Errorf("messages[2].ToolCallID = %q, want mcp_1", chatReq.Messages[2].ToolCallID)
	}
}

func TestResponseToChat_CustomToolCallInterleaved(t *testing.T) {
	// 测试 custom_tool_call 和 custom_tool_call_output 交错排列
	inputItems := []dto.ResponsesInputItem{
		{
			Type: "message", Role: "user",
			Content: json.RawMessage(`"custom tool"`),
		},
		{
			Type:      "custom_tool_call",
			CallID:    "custom_1",
			Name:      "my_tool",
			Arguments: `{"key":"val"}`,
		},
		{
			Type:   "custom_tool_call_output",
			CallID: "custom_1",
			Output: json.RawMessage(`"result"`),
		},
	}

	inputJSON, _ := json.Marshal(inputItems)
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
	}

	if len(chatReq.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[1].Role != "assistant" {
		t.Errorf("messages[1].Role = %q, want assistant", chatReq.Messages[1].Role)
	}
	if chatReq.Messages[2].Role != "tool" {
		t.Errorf("messages[2].Role = %q, want tool", chatReq.Messages[2].Role)
	}
	if chatReq.Messages[2].ToolCallID != "custom_1" {
		t.Errorf("messages[2].ToolCallID = %q, want custom_1", chatReq.Messages[2].ToolCallID)
	}
}

func TestResponseToChat_MultipleToolCallsInterleaved(t *testing.T) {
	// 测试多个不同类型的 tool_call 交错排列
	inputItems := []dto.ResponsesInputItem{
		{
			Type:      "function_call",
			CallID:    "fc_1",
			Name:      "get_weather",
			Arguments: `{"city":"bj"}`,
		},
		{
			Type:   "function_call_output",
			CallID: "fc_1",
			Output: json.RawMessage(`"sunny"`),
		},
		{
			Type:      "mcp_tool_call",
			CallID:    "mcp_1",
			Name:      "read_file",
			Arguments: `{"path":"/tmp/x"}`,
		},
		{
			Type:   "mcp_tool_call_output",
			CallID: "mcp_1",
			Output: json.RawMessage(`"data"`),
		},
		{
			Type: "message", Role: "assistant",
			Content: json.RawMessage(`"final"`),
		},
	}

	inputJSON, _ := json.Marshal(inputItems)
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
	}

	if len(chatReq.Messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(chatReq.Messages))
	}
	// assistant (function_call) + tool + assistant (mcp_tool_call) + tool + assistant
	roles := []string{"assistant", "tool", "assistant", "tool", "assistant"}
	for i, want := range roles {
		if chatReq.Messages[i].Role != want {
			t.Errorf("messages[%d].Role = %q, want %q", i, chatReq.Messages[i].Role, want)
		}
	}
}

func TestResponseToChat_OutputWithoutCallFallback(t *testing.T) {
	// 测试仅有 output 没有对应 tool_call 时，output 被 fallback 追加到末尾
	inputItems := []dto.ResponsesInputItem{
		{
			Type: "message", Role: "user",
			Content: json.RawMessage(`"hi"`),
		},
		{
			Type:   "mcp_tool_call_output",
			CallID: "orphan_1",
			Output: json.RawMessage(`"orphan result"`),
		},
	}

	inputJSON, _ := json.Marshal(inputItems)
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
	}

	if len(chatReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[0].Role != "user" {
		t.Errorf("messages[0].Role = %q, want user", chatReq.Messages[0].Role)
	}
	if chatReq.Messages[1].Role != "tool" {
		t.Errorf("messages[1].Role = %q, want tool", chatReq.Messages[1].Role)
	}
	if chatReq.Messages[1].ToolCallID != "orphan_1" {
		t.Errorf("messages[1].ToolCallID = %q, want orphan_1", chatReq.Messages[1].ToolCallID)
	}
}

func TestResponseToChat_CustomToolCallOutputFallback(t *testing.T) {
	// 测试仅有 custom_tool_call_output 无对应的 call，fallback 到末尾
	inputItems := []dto.ResponsesInputItem{
		{
			Type: "message", Role: "user",
			Content: json.RawMessage(`"hi"`),
		},
		{
			Type:   "custom_tool_call_output",
			CallID: "custom_orphan",
			Output: json.RawMessage(`"custom orphan"`),
		},
	}

	inputJSON, _ := json.Marshal(inputItems)
	req := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(inputJSON),
	}

	chatReq, err := convertResponseRequestToChatRequest(req, "gpt-4o")
	if err != nil {
		t.Fatalf("convertResponseRequestToChatRequest failed: %v", err)
	}

	if len(chatReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(chatReq.Messages))
	}
	if chatReq.Messages[1].Role != "tool" {
		t.Errorf("messages[1].Role = %q, want tool", chatReq.Messages[1].Role)
	}
	if chatReq.Messages[1].ToolCallID != "custom_orphan" {
		t.Errorf("messages[1].ToolCallID = %q, want custom_orphan", chatReq.Messages[1].ToolCallID)
	}
}
