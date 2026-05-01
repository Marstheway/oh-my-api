package adaptor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
)

func TestConvertOpenAIToClaude(t *testing.T) {
	tests := []struct {
		name     string
		request  *dto.ChatCompletionRequest
		model    string
		wantModel string
		wantSystem string
		wantMsgCount int
	}{
		{
			name: "basic conversion",
			request: &dto.ChatCompletionRequest{
				Model: "gpt-4",
				Messages: []dto.Message{
					{Role: "system", Content: "You are helpful."},
					{Role: "user", Content: "Hello"},
				},
				MaxTokens: 100,
			},
			model: "claude-3-opus",
			wantModel: "claude-3-opus",
			wantSystem: "You are helpful.",
			wantMsgCount: 1,
		},
		{
			name: "default max_tokens",
			request: &dto.ChatCompletionRequest{
				Model: "gpt-4",
				Messages: []dto.Message{
					{Role: "user", Content: "Hello"},
				},
			},
			model: "claude-3-opus",
			wantModel: "claude-3-opus",
			wantMsgCount: 1,
		},
		{
			name: "with tools",
			request: &dto.ChatCompletionRequest{
				Model: "gpt-4",
				Messages: []dto.Message{
					{Role: "user", Content: "What's the weather?"},
				},
				Tools: []dto.Tool{
					{
						Type: "function",
						Function: dto.ToolFunction{
							Name:        "get_weather",
							Description: "Get weather info",
							Parameters:  map[string]any{"type": "object"},
						},
					},
				},
			},
			model: "claude-3-opus",
			wantModel: "claude-3-opus",
			wantMsgCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertOpenAIToClaude(tt.request, tt.model)

			if result.Model != tt.wantModel {
				t.Errorf("model = %q, want %q", result.Model, tt.wantModel)
			}

			if tt.wantSystem != "" {
				if sys, ok := result.System.(string); !ok || sys != tt.wantSystem {
					t.Errorf("system = %v, want %q", result.System, tt.wantSystem)
				}
			}

			if len(result.Messages) != tt.wantMsgCount {
				t.Errorf("messages count = %d, want %d", len(result.Messages), tt.wantMsgCount)
			}

			if tt.request.MaxTokens == 0 && result.MaxTokens != 4096 {
				t.Errorf("default max_tokens should be 4096, got %d", result.MaxTokens)
			}
		})
	}
}

func TestConvertTools(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model:   "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "hi"}},
		Tools: []dto.Tool{
			{
				Type: "function",
				Function: dto.ToolFunction{
					Name:        "get_weather",
					Description: "Get weather",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		},
	}

	result := ConvertOpenAIToClaudeV2(req, "claude-3-opus")

	if len(result.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result.Tools))
	}

	if result.Tools[0].Name != "get_weather" {
		t.Errorf("tool name = %q, want %q", result.Tools[0].Name, "get_weather")
	}
}

func TestConvertToolChoice(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		wantType string
	}{
		{
			name:     "auto choice",
			input:    "auto",
			wantType: "auto",
		},
		{
			name:     "none choice",
			input:    "none",
			wantType: "auto", // Claude does not support none, fallback to auto
		},
		{
			name: "function choice",
			input: map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "get_weather",
				},
			},
			wantType: "tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertOpenAIToolChoiceToClaude(tt.input)

			if m, ok := result.(map[string]any); ok {
				if typ, ok := m["type"].(string); ok && typ != tt.wantType {
					t.Errorf("type = %q, want %q", typ, tt.wantType)
				}
			}
		})
	}
}

func TestOpenAIAdaptor_BuildRequest(t *testing.T) {
	adaptor := &OpenAIAdaptor{}
	provider := &config.ProviderConfig{
		Endpoint: "https://api.openai.com/v1",
		APIKey:   "sk-test",
	}

	req := &dto.ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "Hello"}},
	}

	bodyBytes, _ := json.Marshal(req)
	httpReq := adaptor.BuildRequest(context.Background(), provider, "gpt-4o", bytes.NewReader(bodyBytes), ProtocolOpenAI)

	if httpReq.Method != "POST" {
		t.Errorf("method = %q, want POST", httpReq.Method)
	}

	if httpReq.Header.Get("Authorization") != "Bearer sk-test" {
		t.Errorf("authorization header not set correctly")
	}

	body, _ := io.ReadAll(httpReq.Body)
	var parsed dto.ChatCompletionRequest
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("failed to parse body: %v", err)
	}

	if parsed.Model != "gpt-4" {
		t.Errorf("model in body = %q, want %q", parsed.Model, "gpt-4")
	}
}

func TestOpenAIAdaptor_BuildRequest_UsesResponsePath(t *testing.T) {
	adaptor := &OpenAIAdaptor{}
	provider := &config.ProviderConfig{
		Endpoint: "https://api.openai.com",
		APIKey:   "sk-test",
	}

	httpReq := adaptor.BuildRequest(context.Background(), provider, "gpt-4o", bytes.NewReader([]byte(`{"model":"gpt-4.1"}`)), ProtocolOpenAIResponse)

	if got, want := httpReq.URL.String(), "https://api.openai.com/v1/responses"; got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestAnthropicAdaptor_BuildRequest(t *testing.T) {
	adaptor := &AnthropicAdaptor{}
	provider := &config.ProviderConfig{
		Endpoint: "https://api.anthropic.com",
		APIKey:   "sk-ant-test",
	}

	t.Run("from Claude request body", func(t *testing.T) {
		req := &dto.ClaudeRequest{
			Model:    "claude-fast",
			Messages: []dto.ClaudeMessage{{Role: "user", Content: "Hello"}},
		}

		bodyBytes, _ := json.Marshal(req)
		httpReq := adaptor.BuildRequest(context.Background(), provider, "claude-3-opus", bytes.NewReader(bodyBytes), ProtocolAnthropic)

		if httpReq.Header.Get("x-api-key") != "sk-ant-test" {
			t.Errorf("x-api-key header not set correctly")
		}

		if httpReq.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version header not set correctly")
		}

		body, _ := io.ReadAll(httpReq.Body)
		var parsed dto.ClaudeRequest
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("failed to parse body: %v", err)
		}

		if parsed.Model != "claude-fast" {
			t.Errorf("model = %q, want %q", parsed.Model, "claude-fast")
		}
	})
}

func TestAnthropicAdaptor_ConvertResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("non-stream response", func(t *testing.T) {
		adaptor := &AnthropicAdaptor{}

		claudeResp := dto.ClaudeResponse{
			ID:   "msg-123",
			Type: "message",
			Role: "assistant",
			Content: []dto.ContentBlock{
				{Type: "text", Text: "Hello!"},
			},
			Model: "claude-3-opus",
			Usage: dto.ClaudeUsage{InputTokens: 10, OutputTokens: 5},
		}
		respBody, _ := json.Marshal(claudeResp)

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(respBody)
		}))
		defer upstream.Close()

		resp, err := http.Get(upstream.URL)
		if err != nil {
			t.Fatalf("failed to get response: %v", err)
		}
		defer resp.Body.Close()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

		err = adaptor.convertClaudeResponseToOpenAI(c, resp, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
		}

		var result dto.ChatCompletionResponse
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if result.ID != "msg-123" {
			t.Errorf("id = %q, want %q", result.ID, "msg-123")
		}

		if len(result.Choices) != 1 {
			t.Fatalf("expected 1 choice, got %d", len(result.Choices))
		}

		if result.Choices[0].Message.Content != "Hello!" {
			t.Errorf("content = %q, want %q", result.Choices[0].Message.Content, "Hello!")
		}
	})
}

func TestGetAdaptor(t *testing.T) {
	if _, ok := GetAdaptor("openai").(*OpenAIAdaptor); !ok {
		t.Error("expected *OpenAIAdaptor for openai protocol")
	}
	if _, ok := GetAdaptor("anthropic").(*AnthropicAdaptor); !ok {
		t.Error("expected *AnthropicAdaptor for anthropic protocol")
	}
	if _, ok := GetAdaptor("unknown").(*OpenAIAdaptor); !ok {
		t.Error("expected *OpenAIAdaptor for unknown protocol (fallback)")
	}
	if _, ok := GetAdaptor("").(*OpenAIAdaptor); !ok {
		t.Error("expected *OpenAIAdaptor for empty protocol (fallback)")
	}
}

func TestBuildURL_OpenAIResponse_EmptyPath(t *testing.T) {
	got := BuildURL("https://api.openai.com", ProtocolOpenAIResponse)
	want := "https://api.openai.com/v1/responses"
	if got != want {
		t.Fatalf("BuildURL() = %q, want %q", got, want)
	}
}

func TestBuildURL_OpenAIResponse_BasePath(t *testing.T) {
	got := BuildURL("https://api.example.com/proxy", ProtocolOpenAIResponse)
	want := "https://api.example.com/proxy/responses"
	if got != want {
		t.Fatalf("BuildURL() = %q, want %q", got, want)
	}
}

func TestBuildURL_OpenAIResponse_FullResponsesURL(t *testing.T) {
	got := BuildURL("https://api.example.com/proxy/responses", ProtocolOpenAIResponse)
	want := "https://api.example.com/proxy/responses"
	if got != want {
		t.Fatalf("BuildURL() = %q, want %q", got, want)
	}
}

func TestBuildURL(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		protocol Protocol
		want     string
	}{
		{
			name:     "openai without path",
			endpoint: "https://api.openai.com",
			protocol: ProtocolOpenAI,
			want:     "https://api.openai.com/v1/chat/completions",
		},
		{
			name:     "openai with trailing slash",
			endpoint: "https://api.openai.com/",
			protocol: ProtocolOpenAI,
			want:     "https://api.openai.com/v1/chat/completions",
		},
		{
			name:     "openai with v1 path",
			endpoint: "https://api.openai.com/v1",
			protocol: ProtocolOpenAI,
			want:     "https://api.openai.com/v1/chat/completions",
		},
		{
			name:     "openai with custom path",
			endpoint: "https://api.example.com/v3",
			protocol: ProtocolOpenAI,
			want:     "https://api.example.com/v3/chat/completions",
		},
		{
			name:     "anthropic without path",
			endpoint: "https://api.anthropic.com",
			protocol: ProtocolAnthropic,
			want:     "https://api.anthropic.com/v1/messages",
		},
		{
			name:     "anthropic with trailing slash",
			endpoint: "https://api.anthropic.com/",
			protocol: ProtocolAnthropic,
			want:     "https://api.anthropic.com/v1/messages",
		},
		{
			name:     "anthropic with custom path",
			endpoint: "https://api.example.com/anthropic",
			protocol: ProtocolAnthropic,
			want:     "https://api.example.com/anthropic/v1/messages",
		},
		{
			name:     "anthropic with nested custom path",
			endpoint: "https://api.lkeap.cloud.tencent.com/coding/anthropic",
			protocol: ProtocolAnthropic,
			want:     "https://api.lkeap.cloud.tencent.com/coding/anthropic/v1/messages",
		},
		{
			name:     "full url with chat/completions suffix, openai protocol",
			endpoint: "http://v2.open.venus.oa.com/llmproxy/chat/completions",
			protocol: ProtocolOpenAI,
			want:     "http://v2.open.venus.oa.com/llmproxy/chat/completions",
		},
		{
			name:     "full url with chat/completions suffix, anthropic protocol",
			endpoint: "http://v2.open.venus.oa.com/llmproxy/chat/completions",
			protocol: ProtocolAnthropic,
			want:     "http://v2.open.venus.oa.com/llmproxy/chat/completions",
		},
		{
			name:     "full url with chat/completions suffix and trailing slash",
			endpoint: "http://v2.open.venus.oa.com/llmproxy/chat/completions/",
			protocol: ProtocolOpenAI,
			want:     "http://v2.open.venus.oa.com/llmproxy/chat/completions",
		},
		{
			name:     "full url with v1/chat/completions suffix",
			endpoint: "https://api.openai.com/v1/chat/completions",
			protocol: ProtocolOpenAI,
			want:     "https://api.openai.com/v1/chat/completions",
		},
		{
			name:     "full url with messages suffix, anthropic protocol",
			endpoint: "https://api.anthropic.com/v1/messages",
			protocol: ProtocolAnthropic,
			want:     "https://api.anthropic.com/v1/messages",
		},
		{
			name:     "full url with messages suffix, openai protocol",
			endpoint: "https://api.anthropic.com/v1/messages",
			protocol: ProtocolOpenAI,
			want:     "https://api.anthropic.com/v1/messages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildURL(tt.endpoint, tt.protocol)
			if got != tt.want {
				t.Errorf("BuildURL(%q, %q) = %q, want %q", tt.endpoint, tt.protocol, got, tt.want)
			}
		})
	}
}

func TestOpenAIAdaptor_WriteResponse_NonStreamWithCounter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	adaptor := &OpenAIAdaptor{}
	counter := token.NewStreamCounter(0)

	responseBody := `{"id":"chatcmpl-1","object":"chat.completion","created":1234,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello World"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(responseBody))
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatalf("failed to get response: %v", err)
	}
	defer resp.Body.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	err = adaptor.WriteResponse(c, ProtocolOpenAI, resp, false, counter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 提取内容统计，应该 > 0
	if counter.GetOutputTokens() == 0 {
		t.Error("counter should have recorded output tokens from non-stream response")
	}
	// 验证统计的是提取的内容（不是 raw JSON）
	// 实际内容 "Hello World"，token 数应该是 2-3 个
	if counter.GetOutputTokens() < 1 || counter.GetOutputTokens() > 10 {
		t.Errorf("expected 1-10 tokens for extracted content 'Hello World', got %d", counter.GetOutputTokens())
	}
}

func TestOpenAIAdaptor_WriteResponse_StreamWithCounter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	adaptor := &OpenAIAdaptor{}
	counter := token.NewStreamCounter(0)

	// 流式数据，包含一个 delta content "Hello"
	streamData := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(streamData))
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatalf("failed to get response: %v", err)
	}
	defer resp.Body.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	err = adaptor.WriteResponse(c, ProtocolOpenAI, resp, true, counter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 提取内容统计，应该 > 0
	if counter.GetOutputTokens() == 0 {
		t.Error("counter should have recorded output tokens from stream")
	}
	// 验证统计的是提取的内容（不是 raw JSON）
	// 实际内容 "Hello"，token 数应该是 1-2 个
	if counter.GetOutputTokens() < 1 || counter.GetOutputTokens() > 5 {
		t.Errorf("expected 1-5 tokens for extracted content 'Hello', got %d", counter.GetOutputTokens())
	}
}

func TestAnthropicAdaptor_WriteResponse_NonStreamWithCounter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	adaptor := &AnthropicAdaptor{}
	counter := token.NewStreamCounter(0)

	responseBody := `{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"Hello World"}],"model":"claude-3","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(responseBody))
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatalf("failed to get response: %v", err)
	}
	defer resp.Body.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

	err = adaptor.WriteResponse(c, ProtocolAnthropic, resp, false, counter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 提取内容统计，应该 > 0
	if counter.GetOutputTokens() == 0 {
		t.Error("counter should have recorded output tokens from non-stream passThrough response")
	}
	// 验证统计的是提取的内容（不是 raw JSON）
	// 实际内容 "Hello World"，token 数应该是 2-3 个
	if counter.GetOutputTokens() < 1 || counter.GetOutputTokens() > 10 {
		t.Errorf("expected 1-10 tokens for extracted content 'Hello World', got %d", counter.GetOutputTokens())
	}
}

func TestAnthropicAdaptor_WriteResponse_StreamWithCounter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	adaptor := &AnthropicAdaptor{}
	counter := token.NewStreamCounter(0)

	streamEvents := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(streamEvents))
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatalf("failed to get response: %v", err)
	}
	defer resp.Body.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

	err = adaptor.WriteResponse(c, ProtocolOpenAI, resp, true, counter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 提取内容统计，应该 > 0
	if counter.GetOutputTokens() == 0 {
		t.Error("counter should have recorded output tokens from stream")
	}
	// 验证统计的是提取的内容（不是 raw JSON）
	// 实际内容 "Hello"，token 数应该是 1-2 个
	if counter.GetOutputTokens() < 1 || counter.GetOutputTokens() > 5 {
		t.Errorf("expected 1-5 tokens for extracted content 'Hello', got %d", counter.GetOutputTokens())
	}
}

func TestAnthropicAdaptor_PassThroughStreamWithCounter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	adaptor := &AnthropicAdaptor{}
	counter := token.NewStreamCounter(0)

	streamEvents := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello World\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(streamEvents))
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatalf("failed to get response: %v", err)
	}
	defer resp.Body.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

	err = adaptor.WriteResponse(c, ProtocolAnthropic, resp, true, counter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 提取内容统计，应该 > 0
	if counter.GetOutputTokens() == 0 {
		t.Error("counter should have recorded output tokens in passThrough mode")
	}
	// 验证统计的是提取的内容（不是 raw JSON）
	// 实际内容 "Hello World"，token 数应该是 2-3 个
	if counter.GetOutputTokens() < 1 || counter.GetOutputTokens() > 10 {
		t.Errorf("expected 1-10 tokens for extracted content 'Hello World', got %d", counter.GetOutputTokens())
	}

	if !strings.Contains(w.Body.String(), "Hello World") {
		t.Error("passThrough should forward the original content")
	}
}

func TestOpenAIAdaptor_StreamFromAnthropicInbound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	adaptor := &OpenAIAdaptor{}
	counter := token.NewStreamCounter(0)

	streamData := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1234,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello from OpenAI\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(streamData))
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatalf("failed to get response: %v", err)
	}
	defer resp.Body.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	err = adaptor.WriteResponse(c, ProtocolAnthropic, resp, true, counter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 提取内容统计，应该 > 0
	if counter.GetOutputTokens() == 0 {
		t.Error("counter should have recorded output tokens for A->O direction")
	}
	// 验证统计的是提取的内容（不是 raw JSON）
	// 实际内容 "Hello from OpenAI"，token 数应该是 3-5 个
	if counter.GetOutputTokens() < 1 || counter.GetOutputTokens() > 10 {
		t.Errorf("expected 1-10 tokens for extracted content 'Hello from OpenAI', got %d", counter.GetOutputTokens())
	}

	if !strings.Contains(w.Body.String(), "Hello from OpenAI") {
		t.Error("should forward the original OpenAI stream content")
	}
}

func init() {
	gin.SetMode(gin.TestMode)
}
