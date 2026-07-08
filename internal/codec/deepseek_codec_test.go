package codec

import (
	"encoding/json"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

func TestNeedsDeepSeekCompat(t *testing.T) {
	tests := []struct {
		name           string
		upstreamModel  string
		wantCompat     bool
	}{
		{"exact match lowercase", "deepseek-chat", true},
		{"exact match uppercase", "DEEPSEEK-CHAT", true},
		{"mixed case", "DeepSeek-Chat", true},
		{"prefix", "deepseek/deepseek-chat", true},
		{"suffix", "my-deepseek-model", true},
		{"no match", "gpt-4o", false},
		{"no match different", "claude-sonnet-4", false},
		{"empty", "", false},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NeedsDeepSeekCompat(tt.upstreamModel); got != tt.wantCompat {
				t.Errorf("NeedsDeepSeekCompat(%q) = %v, want %v", tt.upstreamModel, got, tt.wantCompat)
			}
		})
	}
}

func TestCloneChatRequestWithDeepSeekCompat(t *testing.T) {
	// 测试：assistant tool_calls 消息应补 reasoning_content
	req := &dto.ChatCompletionRequest{
		Model: "test",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []dto.ToolCall{
					{ID: "tc-1", Type: "function", Function: dto.ToolCallFunc{Name: "test"}},
				},
			},
			{Role: "tool", ToolCallID: "tc-1", Content: "result"},
		},
	}
	
	clone := cloneChatRequestWithDeepSeekCompat(req)
	
	// 验证克隆结果
	if clone.Messages[1].ReasoningContent == nil || *clone.Messages[1].ReasoningContent != " " {
		t.Errorf("clone.Messages[1].ReasoningContent = %v, want ' '", clone.Messages[1].ReasoningContent)
	}
	
	// 验证原始请求未被修改
	if req.Messages[1].ReasoningContent != nil {
		t.Errorf("original request.Messages[1].ReasoningContent should be nil, got %v", req.Messages[1].ReasoningContent)
	}
}

func TestCloneChatRequestWithDeepSeekCompat_PreserveExisting(t *testing.T) {
	// 测试：已有 reasoning_content 的不应被覆盖
	existing := "existing reasoning"
	req := &dto.ChatCompletionRequest{
		Model: "test",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
			{
				Role:             "assistant",
				Content:          "",
				ReasoningContent: &existing,
				ToolCalls: []dto.ToolCall{
					{ID: "tc-1", Type: "function", Function: dto.ToolCallFunc{Name: "test"}},
				},
			},
		},
	}
	
	clone := cloneChatRequestWithDeepSeekCompat(req)
	
	if clone.Messages[1].ReasoningContent == nil || *clone.Messages[1].ReasoningContent != "existing reasoning" {
		t.Errorf("clone.Messages[1].ReasoningContent = %v, want 'existing reasoning'", clone.Messages[1].ReasoningContent)
	}
}

func TestCloneChatRequestWithDeepSeekCompat_NoToolCalls(t *testing.T) {
	// 测试：没有 tool_calls 的 assistant 消息不应补 reasoning_content
	req := &dto.ChatCompletionRequest{
		Model: "test",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there"},
		},
	}
	
	clone := cloneChatRequestWithDeepSeekCompat(req)
	
	if clone.Messages[1].ReasoningContent != nil {
		t.Errorf("clone.Messages[1].ReasoningContent should be nil for non-tool_calls assistant, got %v", clone.Messages[1].ReasoningContent)
	}
}

func TestConvertOpenAIToAnthropicRequest_DeepSeekCompat(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model: "test",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []dto.ToolCall{
					{ID: "tc-1", Type: "function", Function: dto.ToolCallFunc{Name: "test", Arguments: "{}"}},
				},
			},
		},
	}
	
	// DeepSeek 模式：应补 thinking block
	claudeReq, err := convertOpenAIToAnthropicRequest(req, "deepseek-chat", true)
	if err != nil {
		t.Fatalf("convertOpenAIToAnthropicRequest failed: %v", err)
	}
	
	if len(claudeReq.Messages) != 2 {
		t.Fatalf("len(claudeReq.Messages) = %d, want 2", len(claudeReq.Messages))
	}
	
	assistantMsg := claudeReq.Messages[1]
	if assistantMsg.Role != "assistant" {
		t.Fatalf("assistantMsg.Role = %q, want 'assistant'", assistantMsg.Role)
	}
	
	blocks, ok := assistantMsg.Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("assistantMsg.Content is not []dto.ContentBlock")
	}
	
	// 第一个 block 应是 thinking
	if len(blocks) < 1 || blocks[0].Type != "thinking" {
		t.Fatalf("first block type = %q, want 'thinking'", blocks[0].Type)
	}
	if blocks[0].Thinking == nil || *blocks[0].Thinking != " " {
		t.Fatalf("first block thinking = %v, want ' '", blocks[0].Thinking)
	}
}

func TestConvertOpenAIToAnthropicRequest_PreserveReasoningContent(t *testing.T) {
	reasoning := "my reasoning process"
	req := &dto.ChatCompletionRequest{
		Model: "test",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
			{
				Role:             "assistant",
				Content:          "",
				ReasoningContent: &reasoning,
				ToolCalls: []dto.ToolCall{
					{ID: "tc-1", Type: "function", Function: dto.ToolCallFunc{Name: "test", Arguments: "{}"}},
				},
			},
		},
	}
	
	// 非 DeepSeek 模式也应保留 reasoning_content -> thinking
	claudeReq, err := convertOpenAIToAnthropicRequest(req, "gpt-4", false)
	if err != nil {
		t.Fatalf("convertOpenAIToAnthropicRequest failed: %v", err)
	}
	
	blocks, ok := claudeReq.Messages[1].Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("Content is not []dto.ContentBlock")
	}
	
	if len(blocks) < 1 || blocks[0].Type != "thinking" {
		t.Fatalf("first block type = %q, want 'thinking'", blocks[0].Type)
	}
	if blocks[0].Thinking == nil || *blocks[0].Thinking != "my reasoning process" {
		t.Fatalf("first block thinking = %v, want 'my reasoning process'", blocks[0].Thinking)
	}
}

func TestConvertAnthropicToOpenAIRequest_PreserveThinking(t *testing.T) {
	thinking := "thinking process"
	req := &dto.ClaudeRequest{
		Model: "test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{
				Role: "assistant",
				Content: []dto.ContentBlock{
					{Type: "thinking", Thinking: &thinking},
					{Type: "text", Text: "response"},
					{Type: "tool_use", ID: "tu-1", Name: "test", Input: map[string]any{}},
				},
			},
		},
	}
	
	openaiReq := convertAnthropicToOpenAIRequest(req, "gpt-4")
	
	if len(openaiReq.Messages) != 2 {
		t.Fatalf("len(openaiReq.Messages) = %d, want 2", len(openaiReq.Messages))
	}
	
	assistantMsg := openaiReq.Messages[1]
	if assistantMsg.Role != "assistant" {
		t.Fatalf("assistantMsg.Role = %q, want 'assistant'", assistantMsg.Role)
	}
	
	if assistantMsg.ReasoningContent == nil || *assistantMsg.ReasoningContent != "thinking process" {
		t.Fatalf("assistantMsg.ReasoningContent = %v, want 'thinking process'", assistantMsg.ReasoningContent)
	}
}

func TestOpenAIChatCodec_EncodeRequest_DeepSeekPassthrough(t *testing.T) {
	codec := &OpenAIChatCodec{}
	
	req := &dto.ChatCompletionRequest{
		Model: "user-model",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []dto.ToolCall{
					{ID: "tc-1", Type: "function", Function: dto.ToolCallFunc{Name: "test"}},
				},
			},
		},
	}
	
	// DeepSeek passthrough: OpenAI -> OpenAI
	body, err := codec.EncodeRequest(FormatOpenAIChat, req, "deepseek-chat", true)
	if err != nil {
		t.Fatalf("EncodeRequest failed: %v", err)
	}
	
	var encoded dto.ChatCompletionRequest
	if err := json.Unmarshal(body, &encoded); err != nil {
		t.Fatalf("unmarshal encoded request: %v", err)
	}
	
	// 验证 reasoning_content 已补
	if encoded.Messages[1].ReasoningContent == nil || *encoded.Messages[1].ReasoningContent != " " {
		t.Errorf("encoded.Messages[1].ReasoningContent = %v, want ' '", encoded.Messages[1].ReasoningContent)
	}
	
	// 验证原始请求未修改
	if req.Messages[1].ReasoningContent != nil {
		t.Errorf("original req.Messages[1].ReasoningContent should be nil, got %v", req.Messages[1].ReasoningContent)
	}
}

func TestOpenAIChatCodec_EncodeRequest_NonDeepSeekPassthrough(t *testing.T) {
	codec := &OpenAIChatCodec{}
	
	req := &dto.ChatCompletionRequest{
		Model: "user-model",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []dto.ToolCall{
					{ID: "tc-1", Type: "function", Function: dto.ToolCallFunc{Name: "test"}},
				},
			},
		},
	}
	
	// 非 DeepSeek: 不应补 reasoning_content
	body, err := codec.EncodeRequest(FormatOpenAIChat, req, "gpt-4o", false)
	if err != nil {
		t.Fatalf("EncodeRequest failed: %v", err)
	}
	
	var encoded dto.ChatCompletionRequest
	if err := json.Unmarshal(body, &encoded); err != nil {
		t.Fatalf("unmarshal encoded request: %v", err)
	}
	
	// 验证 reasoning_content 未补
	if encoded.Messages[1].ReasoningContent != nil {
		t.Errorf("encoded.Messages[1].ReasoningContent should be nil for non-DeepSeek, got %v", encoded.Messages[1].ReasoningContent)
	}
}

// Test: Anthropic clone with empty thinking block and signature preservation
func TestCloneClaudeRequestWithDeepSeekCompat_EmptyThinkingWithSignature(t *testing.T) {
	sig := "test-signature-abc123"
	req := &dto.ClaudeRequest{
		Model: "test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{
				Role: "assistant",
				Content: []dto.ContentBlock{
					{Type: "thinking", Thinking: nil, Signature: sig}, // Empty thinking with signature
					{Type: "tool_use", ID: "tu-1", Name: "test", Input: map[string]any{}},
				},
			},
		},
	}

	clone := cloneClaudeRequestWithDeepSeekCompat(req)

	blocks, ok := clone.Messages[1].Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("Content is not []dto.ContentBlock")
	}

	// 验证第一个 thinking block 已被填充
	if len(blocks) < 1 || blocks[0].Type != "thinking" {
		t.Fatalf("first block type = %q, want 'thinking'", blocks[0].Type)
	}
	if blocks[0].Thinking == nil || *blocks[0].Thinking != " " {
		t.Fatalf("first block thinking = %v, want ' ' (space placeholder)", blocks[0].Thinking)
	}
	// 验证 signature 已保留
	if blocks[0].Signature != sig {
		t.Fatalf("first block signature = %q, want %q (should be preserved)", blocks[0].Signature, sig)
	}

	// 验证原始请求未被修改
	origBlocks, ok := req.Messages[1].Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("original Content is not []dto.ContentBlock")
	}
	if origBlocks[0].Thinking != nil {
		t.Errorf("original request thinking should still be nil, got %v", origBlocks[0].Thinking)
	}
}

// Test: Anthropic clone with []any content form and signature preservation
func TestCloneClaudeRequestWithDeepSeekCompat_ArrayAnyForm(t *testing.T) {
	sig := "sig-xyz"
	req := &dto.ClaudeRequest{
		Model: "test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{
				Role: "assistant",
				Content: []any{
					map[string]any{"type": "thinking", "thinking": "", "signature": sig}, // Empty thinking via []any
					map[string]any{"type": "tool_use", "id": "tu-1", "name": "test", "input": map[string]any{}},
				},
			},
		},
	}

	clone := cloneClaudeRequestWithDeepSeekCompat(req)

	anyBlocks, ok := clone.Messages[1].Content.([]any)
	if !ok {
		t.Fatalf("Content is not []any")
	}

	// 验证第一个 block 是 thinking 且已填充
	firstBlock, ok := anyBlocks[0].(map[string]any)
	if !ok {
		t.Fatalf("first block is not map[string]any")
	}
	if firstBlock["type"] != "thinking" {
		t.Fatalf("first block type = %q, want 'thinking'", firstBlock["type"])
	}
	thinking, ok := firstBlock["thinking"].(string)
	if !ok || thinking != " " {
		t.Fatalf("first block thinking = %v, want ' ' (space placeholder)", firstBlock["thinking"])
	}
	// 验证 signature 已保留
	if firstBlock["signature"] != sig {
		t.Fatalf("first block signature = %q, want %q (should be preserved)", firstBlock["signature"], sig)
	}

	// 验证原始请求未被修改
	origAnyBlocks, ok := req.Messages[1].Content.([]any)
	if !ok {
		t.Fatalf("original Content is not []any")
	}
	origFirstBlock, ok := origAnyBlocks[0].(map[string]any)
	if !ok {
		t.Fatalf("original first block is not map[string]any")
	}
	if origFirstBlock["thinking"] != "" {
		t.Errorf("original request thinking should still be empty string, got %v", origFirstBlock["thinking"])
	}
}

// Test: Anthropic clone with NO thinking block at all (should add new one without signature)
func TestCloneClaudeRequestWithDeepSeekCompat_NoThinkingBlock(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model: "test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{
				Role: "assistant",
				Content: []dto.ContentBlock{
					{Type: "tool_use", ID: "tu-1", Name: "test", Input: map[string]any{}},
				},
			},
		},
	}

	clone := cloneClaudeRequestWithDeepSeekCompat(req)

	blocks, ok := clone.Messages[1].Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("Content is not []dto.ContentBlock")
	}

	// 验证新增了 thinking block 在 tool_use 之前
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2 (one new thinking + one tool_use)", len(blocks))
	}
	if blocks[0].Type != "thinking" {
		t.Fatalf("first block type = %q, want 'thinking'", blocks[0].Type)
	}
	if blocks[0].Thinking == nil || *blocks[0].Thinking != " " {
		t.Fatalf("first block thinking = %v, want ' ' (placeholder)", blocks[0].Thinking)
	}
	// 验证新增的 thinking block 没有 signature
	if blocks[0].Signature != "" {
		t.Fatalf("new thinking block should have empty signature, got %q", blocks[0].Signature)
	}
	if blocks[1].Type != "tool_use" {
		t.Fatalf("second block type = %q, want 'tool_use'", blocks[1].Type)
	}

	// 验证原始请求未被修改
	origBlocks, ok := req.Messages[1].Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("original Content is not []dto.ContentBlock")
	}
	if len(origBlocks) != 1 || origBlocks[0].Type != "tool_use" {
		t.Fatalf("original request should still have only tool_use block")
	}
}

// Test: AnthropicMessagesCodec.EncodeRequest with DeepSeek passthrough
func TestAnthropicMessagesCodec_EncodeRequest_DeepSeekPassthrough(t *testing.T) {
	codec := &AnthropicMessagesCodec{}

	req := &dto.ClaudeRequest{
		Model: "test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{
				Role: "assistant",
				Content: []dto.ContentBlock{
					{Type: "tool_use", ID: "tu-1", Name: "test", Input: map[string]any{}},
				},
			},
		},
	}

	// DeepSeek passthrough: Anthropic -> Anthropic
	body, err := codec.EncodeRequest(FormatAnthropicMessages, req, "deepseek-chat", true)
	if err != nil {
		t.Fatalf("EncodeRequest failed: %v", err)
	}

	var encoded dto.ClaudeRequest
	if err := json.Unmarshal(body, &encoded); err != nil {
		t.Fatalf("unmarshal encoded request: %v", err)
	}

	// 验证已补 thinking block
	// JSON unmarshal will produce []any with map[string]any elements
	anyBlocks, ok := encoded.Messages[1].Content.([]any)
	if !ok {
		t.Fatalf("encoded Content is not []any (JSON unmarshal behavior)")
	}
	if len(anyBlocks) != 2 {
		t.Fatalf("len(anyBlocks) = %d, want 2", len(anyBlocks))
	}
	firstBlock, ok := anyBlocks[0].(map[string]any)
	if !ok {
		t.Fatalf("first block is not map[string]any")
	}
	if firstBlock["type"] != "thinking" {
		t.Fatalf("first block type = %v, want 'thinking'", firstBlock["type"])
	}
	thinking, ok := firstBlock["thinking"].(string)
	if !ok || thinking != " " {
		t.Fatalf("first block thinking = %v, want ' ' (placeholder)", firstBlock["thinking"])
	}

	// 验证原始请求未修改
	origBlocks, ok := req.Messages[1].Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("original Content is not []dto.ContentBlock")
	}
	if len(origBlocks) != 1 || origBlocks[0].Type != "tool_use" {
		t.Fatalf("original request should still have only tool_use block")
	}
}

// Test: Multiple thinking blocks concatenation in Anthropic->OpenAI conversion
func TestConvertAnthropicToOpenAIRequest_MultipleThinkingBlocks(t *testing.T) {
	thinking1 := "first reasoning"
	thinking2 := "second reasoning"
	sig1 := "sig-1"
	sig2 := "sig-2"
	req := &dto.ClaudeRequest{
		Model: "test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{
				Role: "assistant",
				Content: []dto.ContentBlock{
					{Type: "thinking", Thinking: &thinking1, Signature: sig1},
					{Type: "thinking", Thinking: &thinking2, Signature: sig2},
					{Type: "text", Text: "response"},
					{Type: "tool_use", ID: "tu-1", Name: "test", Input: map[string]any{}},
				},
			},
		},
	}

	openaiReq := convertAnthropicToOpenAIRequest(req, "gpt-4")

	assistantMsg := openaiReq.Messages[1]
	if assistantMsg.Role != "assistant" {
		t.Fatalf("assistantMsg.Role = %q, want 'assistant'", assistantMsg.Role)
	}

	// 验证多个 thinking blocks 已拼接为一个 reasoning_content
	expectedReasoning := "first reasoningsecond reasoning"
	if assistantMsg.ReasoningContent == nil || *assistantMsg.ReasoningContent != expectedReasoning {
		t.Fatalf("ReasoningContent = %v, want %q (concatenated thinking blocks)", assistantMsg.ReasoningContent, expectedReasoning)
	}
	}

// Test: Empty string reasoning_content should be treated as missing and padded
func TestCloneChatRequestWithDeepSeekCompat_EmptyString(t *testing.T) {
	empty := ""
	req := &dto.ChatCompletionRequest{
		Model: "test",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello"},
			{
				Role:             "assistant",
				Content:          "",
				ReasoningContent: &empty, // 空字符串，应被视为缺失
				ToolCalls: []dto.ToolCall{
					{ID: "tc-1", Type: "function", Function: dto.ToolCallFunc{Name: "test"}},
				},
			},
			{Role: "tool", ToolCallID: "tc-1", Content: "result"},
		},
	}

	clone := cloneChatRequestWithDeepSeekCompat(req)

	// 验证空字符串被补为空格
	if clone.Messages[1].ReasoningContent == nil || *clone.Messages[1].ReasoningContent != " " {
		t.Errorf("clone.Messages[1].ReasoningContent = %v, want ' ' (empty string should be padded)", clone.Messages[1].ReasoningContent)
	}

	// 验证原始请求保持空字符串不变
	if req.Messages[1].ReasoningContent == nil || *req.Messages[1].ReasoningContent != "" {
		t.Errorf("original request.Messages[1].ReasoningContent should remain empty string, got %v", req.Messages[1].ReasoningContent)
	}
}

// Test: CacheControl should be preserved in Claude request clone
func TestCloneClaudeRequestWithDeepSeekCompat_PreserveCacheControl(t *testing.T) {
	cacheControl := json.RawMessage(`{"type":"ephemeral"}`)
	req := &dto.ClaudeRequest{
		Model: "test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{
				Role: "assistant",
				Content: []dto.ContentBlock{
					{
						Type:         "tool_use",
						ID:           "tu-1",
						Name:         "test",
						Input:        map[string]any{},
						CacheControl: cacheControl,
					},
				},
			},
			{Role: "user", Content: "result"},
		},
	}

	clone := cloneClaudeRequestWithDeepSeekCompat(req)

	// 验证 CacheControl 被保留
	assistantContent, ok := clone.Messages[1].Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("clone.Messages[1].Content is not []dto.ContentBlock")
	}

	if len(assistantContent) == 0 {
		t.Fatalf("assistantContent is empty")
	}

	// 检查 tool_use block 的 cache_control
	toolUseBlock := assistantContent[len(assistantContent)-1]
	if toolUseBlock.Type != "tool_use" {
		t.Fatalf("expected tool_use block, got %s", toolUseBlock.Type)
	}

	if toolUseBlock.CacheControl == nil {
		t.Errorf("tool_use block CacheControl should be preserved, got nil")
	} else if string(toolUseBlock.CacheControl) != string(cacheControl) {
		t.Errorf("CacheControl = %s, want %s", string(toolUseBlock.CacheControl), string(cacheControl))
	}

	// 验证原始请求的 CacheControl 不变
	originalContent, ok := req.Messages[1].Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("original req.Messages[1].Content is not []dto.ContentBlock")
	}

	if originalContent[0].CacheControl == nil {
		t.Errorf("original CacheControl should still be present")
	}
}

// Test: CacheControl should be preserved in []any form after clone
func TestCloneClaudeRequestWithDeepSeekCompat_ArrayAnyCacheControl(t *testing.T) {
	// 模拟 JSON 解码后的形态
	content := []any{
		map[string]any{
			"type":          "tool_use",
			"id":            "tu-1",
			"name":          "test",
			"input":         map[string]any{},
			"cache_control": map[string]any{"type": "ephemeral"},
		},
	}
	req := &dto.ClaudeRequest{
		Model: "test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: content},
			{Role: "user", Content: "result"},
		},
	}

	clone := cloneClaudeRequestWithDeepSeekCompat(req)

	// 验证 CacheControl 在 []any 形态下也被保留
	assistantContent, ok := clone.Messages[1].Content.([]any)
	if !ok {
		t.Fatalf("clone.Messages[1].Content is not []any")
	}

	toolUseMap, ok := assistantContent[len(assistantContent)-1].(map[string]any)
	if !ok {
		t.Fatalf("tool_use block is not map[string]any")
	}

	cacheControl, exists := toolUseMap["cache_control"]
	if !exists {
		t.Errorf("cache_control field should be preserved in []any form")
	} else {
		ccMap, ok := cacheControl.(map[string]any)
		if !ok {
			t.Errorf("cache_control is not map[string]any")
		} else if ccMap["type"] != "ephemeral" {
			t.Errorf("cache_control.type = %v, want 'ephemeral'", ccMap["type"])
		}
	}

	// 验证原始请求的 cache_control 不变
	originalContent, ok := req.Messages[1].Content.([]any)
	if !ok {
		t.Fatalf("original req.Messages[1].Content is not []any")
	}

	originalMap, ok := originalContent[0].(map[string]any)
	if !ok {
		t.Fatalf("original tool_use block is not map[string]any")
	}

	if _, exists := originalMap["cache_control"]; !exists {
		t.Errorf("original cache_control should still be present")
	}
}

// Test: []dto.ContentBlock form - existing non-empty thinking should NOT be overwritten
func TestCloneClaudeRequestWithDeepSeekCompat_NonEmptyThinkingBlock(t *testing.T) {
	existingThinking := "my reasoning process"
	req := &dto.ClaudeRequest{
		Model: "test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{
				Role: "assistant",
				Content: []dto.ContentBlock{
					{Type: "thinking", Thinking: &existingThinking},
					{Type: "tool_use", ID: "tu-1", Name: "test", Input: map[string]any{}},
				},
			},
		},
	}

	clone := cloneClaudeRequestWithDeepSeekCompat(req)

	blocks, ok := clone.Messages[1].Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("Content is not []dto.ContentBlock")
	}

	// 验证 thinking block 内容未被修改
	if len(blocks) < 1 || blocks[0].Type != "thinking" {
		t.Fatalf("first block type = %q, want 'thinking'", blocks[0].Type)
	}
	if blocks[0].Thinking == nil || *blocks[0].Thinking != "my reasoning process" {
		t.Fatalf("thinking = %v, want 'my reasoning process' (should not be overwritten)", blocks[0].Thinking)
	}

	// 验证原始请求未修改
	origBlocks, ok := req.Messages[1].Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("original Content is not []dto.ContentBlock")
	}
	if origBlocks[0].Thinking == nil || *origBlocks[0].Thinking != "my reasoning process" {
		t.Errorf("original thinking should remain unchanged")
	}
}

// Test: []any form - existing non-empty thinking should NOT be overwritten
func TestCloneClaudeRequestWithDeepSeekCompat_ArrayAnyNonEmptyThinking(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model: "test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{
				Role: "assistant",
				Content: []any{
					map[string]any{"type": "thinking", "thinking": "existing reasoning"},
					map[string]any{"type": "tool_use", "id": "tu-1", "name": "test", "input": map[string]any{}},
				},
			},
		},
	}

	clone := cloneClaudeRequestWithDeepSeekCompat(req)

	anyBlocks, ok := clone.Messages[1].Content.([]any)
	if !ok {
		t.Fatalf("Content is not []any")
	}

	firstBlock, ok := anyBlocks[0].(map[string]any)
	if !ok {
		t.Fatalf("first block is not map[string]any")
	}

	// 验证 thinking 内容未被修改
	if firstBlock["type"] != "thinking" {
		t.Fatalf("first block type = %q, want 'thinking'", firstBlock["type"])
	}
	thinking, ok := firstBlock["thinking"].(string)
	if !ok || thinking != "existing reasoning" {
		t.Fatalf("thinking = %v, want 'existing reasoning' (should not be overwritten)", firstBlock["thinking"])
	}

	// 验证原始请求未修改
	origAnyBlocks, ok := req.Messages[1].Content.([]any)
	if !ok {
		t.Fatalf("original Content is not []any")
	}
	origFirstBlock, ok := origAnyBlocks[0].(map[string]any)
	if !ok {
		t.Fatalf("original first block is not map[string]any")
	}
	if origFirstBlock["thinking"] != "existing reasoning" {
		t.Errorf("original thinking should remain unchanged")
	}
}

// Test: []dto.ContentBlock form - no tool_use means no padding needed
func TestCloneClaudeRequestWithDeepSeekCompat_NoToolUseBlock(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model: "test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{
				Role: "assistant",
				Content: []dto.ContentBlock{
					{Type: "text", Text: "response"},
				},
			},
		},
	}

	clone := cloneClaudeRequestWithDeepSeekCompat(req)

	blocks, ok := clone.Messages[1].Content.([]dto.ContentBlock)
	if !ok {
		t.Fatalf("Content is not []dto.ContentBlock")
	}

	// 验证没有添加 thinking block
	if len(blocks) != 1 || blocks[0].Type != "text" {
		t.Fatalf("blocks = %v, want only one text block (no thinking should be added)", blocks)
	}

	// 验证原始请求未修改
	if len(req.Messages[1].Content.([]dto.ContentBlock)) != 1 {
		t.Errorf("original request should not be modified")
	}
}

// Test: []any form - no tool_use means no padding needed
func TestCloneClaudeRequestWithDeepSeekCompat_ArrayAnyNoToolUse(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model: "test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{
				Role: "assistant",
				Content: []any{
					map[string]any{"type": "text", "text": "response"},
				},
			},
		},
	}

	clone := cloneClaudeRequestWithDeepSeekCompat(req)

	anyBlocks, ok := clone.Messages[1].Content.([]any)
	if !ok {
		t.Fatalf("Content is not []any")
	}

	// 验证没有添加 thinking block
	if len(anyBlocks) != 1 {
		t.Fatalf("len(anyBlocks) = %d, want 1 (no thinking should be added)", len(anyBlocks))
	}

	firstBlock, ok := anyBlocks[0].(map[string]any)
	if !ok {
		t.Fatalf("first block is not map[string]any")
	}
	if firstBlock["type"] != "text" {
		t.Fatalf("first block type = %q, want 'text'", firstBlock["type"])
	}

	// 验证原始请求未修改
	if len(req.Messages[1].Content.([]any)) != 1 {
		t.Errorf("original request should not be modified")
	}
}