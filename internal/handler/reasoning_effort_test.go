package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/model"
)

const reasoningTestProvider = "test-provider"

func upstreamEffortRule(providerName, model string, effort []string) []config.RuleConfig {
	return []config.RuleConfig{{
		Match: config.RuleMatch{
			UpstreamModel: &config.RuleCondition{
				Op:    "equals",
				Value: providerName + "/" + model,
			},
		},
		Action: config.RuleAction{Effort: effort},
	}}
}

func upstreamStripEffortRule(providerName, model string) []config.RuleConfig {
	return []config.RuleConfig{{
		Match: config.RuleMatch{
			UpstreamModel: &config.RuleCondition{
				Op:    "equals",
				Value: providerName + "/" + model,
			},
		},
		Action: config.RuleAction{EffortMode: "strip"},
	}}
}

func TestApplyReasoningEffortToChatRequest(t *testing.T) {
	tests := []struct {
		name           string
		req            *dto.ChatCompletionRequest
		allowed        []string
		mode           string
		expectEffort   string
		expectHasField bool // 是否期望有 reasoning_effort 字段
	}{
		{
			name: "passthrough when no config",
			req: &dto.ChatCompletionRequest{
				Model:           "gpt-4o",
				ReasoningEffort: "high",
			},
			allowed:        nil,
			expectEffort:   "high",
			expectHasField: true,
		},
		{
			name: "strip mode removes effort",
			req: &dto.ChatCompletionRequest{
				Model:           "gpt-4o",
				ReasoningEffort: "high",
			},
			mode:           "strip",
			expectEffort:   "",
			expectHasField: false,
		},
		{
			name: "replace when allowed set",
			req: &dto.ChatCompletionRequest{
				Model:           "gpt-4o",
				ReasoningEffort: "medium",
			},
			allowed:        []string{"high", "max"},
			expectEffort:   "high",
			expectHasField: true,
		},
		{
			name: "passthrough when empty effort",
			req: &dto.ChatCompletionRequest{
				Model:           "gpt-4o",
				ReasoningEffort: "",
			},
			allowed:        []string{"high", "max"},
			expectEffort:   "",
			expectHasField: false,
		},
		{
			name: "none allowed keeps none as-is",
			req: &dto.ChatCompletionRequest{
				Model:           "gpt-4o",
				ReasoningEffort: "none",
			},
			allowed:        []string{"none"},
			expectEffort:   "none",
			expectHasField: true,
		},
		{
			name: "none only allowed clamps high to none",
			req: &dto.ChatCompletionRequest{
				Model:           "gpt-4o",
				ReasoningEffort: "high",
			},
			allowed:        []string{"none"},
			expectEffort:   "none",
			expectHasField: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyReasoningEffortToRequest(tt.req, tt.allowed, tt.mode)

			chatReq, ok := result.(*dto.ChatCompletionRequest)
			if !ok {
				t.Fatalf("expected *dto.ChatCompletionRequest, got %T", result)
			}

			// 序列化为 JSON 检查字段是否存在
			body, err := json.Marshal(chatReq)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}

			var rawMap map[string]interface{}
			if err := json.Unmarshal(body, &rawMap); err != nil {
				t.Fatalf("unmarshal to map: %v", err)
			}

			_, hasField := rawMap["reasoning_effort"]
			if hasField != tt.expectHasField {
				t.Errorf("has reasoning_effort field = %v, want %v", hasField, tt.expectHasField)
			}

			if chatReq.ReasoningEffort != tt.expectEffort {
				t.Errorf("ReasoningEffort = %q, want %q", chatReq.ReasoningEffort, tt.expectEffort)
			}

			// 检查原始请求未被修改（除非是 passthrough）
			if tt.allowed != nil && tt.req.ReasoningEffort != chatReq.ReasoningEffort {
				// 修改了值，应该返回新对象
				if tt.req == chatReq {
					t.Error("returned same object, expected clone")
				}
			}
		})
	}
}

func TestApplyReasoningEffortToResponsesRequest(t *testing.T) {
	tests := []struct {
		name         string
		req          *dto.ResponsesRequest
		allowed      []string
		mode         string
		expectEffort string
		expectNil    bool // 是否期望 Reasoning 为 nil
	}{
		{
			name: "strip mode removes effort",
			req: &dto.ResponsesRequest{
				Model: "gpt-4o",
				Reasoning: &dto.ResponsesReasoning{
					Effort: "high",
				},
			},
			mode:         "strip",
			expectEffort: "",
			expectNil:    true,
		},
		{
			name: "replace when allowed set",
			req: &dto.ResponsesRequest{
				Model: "gpt-4o",
				Reasoning: &dto.ResponsesReasoning{
					Effort: "medium",
				},
			},
			allowed:      []string{"high", "max"},
			expectEffort: "high",
			expectNil:    false,
		},
		{
			name: "keep summary when stripping effort",
			req: &dto.ResponsesRequest{
				Model: "gpt-4o",
				Reasoning: &dto.ResponsesReasoning{
					Effort:  "high",
					Summary: "auto",
				},
			},
			mode:         "strip",
			expectEffort: "",
			expectNil:    false, // 有 summary，不置 nil
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyReasoningEffortToRequest(tt.req, tt.allowed, tt.mode)

			respReq, ok := result.(*dto.ResponsesRequest)
			if !ok {
				t.Fatalf("expected *dto.ResponsesRequest, got %T", result)
			}

			if tt.expectNil {
				if respReq.Reasoning != nil {
					t.Errorf("expected Reasoning to be nil, got %+v", respReq.Reasoning)
				}
			} else {
				if respReq.Reasoning == nil {
					t.Fatal("expected Reasoning to not be nil")
				}
				if respReq.Reasoning.Effort != tt.expectEffort {
					t.Errorf("Effort = %q, want %q", respReq.Reasoning.Effort, tt.expectEffort)
				}
			}
		})
	}
}

func TestApplyReasoningEffortToClaudeRequest(t *testing.T) {
	tests := []struct {
		name           string
		req            *dto.ClaudeRequest
		allowed        []string
		mode           string
		expectEffort   string
		expectHasField bool // 是否期望有 effort 字段
	}{
		{
			name: "strip mode removes effort",
			req: &dto.ClaudeRequest{
				Model:        "claude-3-5-sonnet-20241022",
				OutputConfig: json.RawMessage(`{"effort": "high"}`),
			},
			mode:           "strip",
			expectEffort:   "",
			expectHasField: false,
		},
		{
			name: "replace when allowed set",
			req: &dto.ClaudeRequest{
				Model:        "claude-3-5-sonnet-20241022",
				OutputConfig: json.RawMessage(`{"effort": "medium", "other": "value"}`),
			},
			allowed:        []string{"high", "max"},
			expectEffort:   "high",
			expectHasField: true,
		},
		{
			name: "strip makes output_config nil when empty",
			req: &dto.ClaudeRequest{
				Model:        "claude-3-5-sonnet-20241022",
				OutputConfig: json.RawMessage(`{"effort": "high"}`),
			},
			mode:           "strip",
			expectEffort:   "",
			expectHasField: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyReasoningEffortToRequest(tt.req, tt.allowed, tt.mode)

			claudeReq, ok := result.(*dto.ClaudeRequest)
			if !ok {
				t.Fatalf("expected *dto.ClaudeRequest, got %T", result)
			}

			if tt.expectHasField {
				if claudeReq.OutputConfig == nil {
					t.Fatal("expected OutputConfig to not be nil")
				}

				var cfgMap map[string]interface{}
				if err := json.Unmarshal(claudeReq.OutputConfig, &cfgMap); err != nil {
					t.Fatalf("unmarshal OutputConfig: %v", err)
				}

				effort, has := cfgMap["effort"].(string)
				if !has {
					t.Error("expected effort field")
				}
				if effort != tt.expectEffort {
					t.Errorf("effort = %q, want %q", effort, tt.expectEffort)
				}
			} else {
				if claudeReq.OutputConfig != nil {
					var cfgMap map[string]interface{}
					if err := json.Unmarshal(claudeReq.OutputConfig, &cfgMap); err != nil {
						t.Fatalf("unmarshal OutputConfig: %v", err)
					}
					if _, has := cfgMap["effort"]; has {
						t.Error("expected no effort field")
					}
				}
			}
		})
	}
}

func TestApplyThinkingOffChat_ClosesAllThinkingReaders(t *testing.T) {
	req := &dto.ChatCompletionRequest{
		Model:           "gpt-4o",
		ReasoningEffort: "high",
		Reasoning:       json.RawMessage(`{"max_tokens":3000}`),
		Think:           json.RawMessage(`true`),
		THINKING:        json.RawMessage(`{"type":"enabled","budget_tokens":1000}`),
		EnableThinking:  json.RawMessage(`true`),
	}

	got, ok := applyThinkingOffToRequest(req).(*dto.ChatCompletionRequest)
	if !ok {
		t.Fatalf("expected *dto.ChatCompletionRequest, got %T", applyThinkingOffToRequest(req))
	}
	if got.ReasoningEffort != "none" {
		t.Errorf("ReasoningEffort = %q, want none", got.ReasoningEffort)
	}
	if got.Reasoning != nil {
		t.Errorf("Reasoning = %s, want nil", got.Reasoning)
	}
	if string(got.Think) != "false" {
		t.Errorf("Think = %s, want false", got.Think)
	}
	if string(got.EnableThinking) != "false" {
		t.Errorf("EnableThinking = %s, want false", got.EnableThinking)
	}
	if string(got.THINKING) != `{"type":"disabled"}` {
		t.Errorf("THINKING = %s, want disabled", got.THINKING)
	}
	if req == got {
		t.Fatal("expected cloned request")
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(raw["reasoning_effort"]) != `"none"` {
		t.Fatalf("reasoning_effort = %s, want \"none\"", raw["reasoning_effort"])
	}
	if _, ok := raw["reasoning"]; ok {
		t.Fatalf("encoded chat body still has reasoning: %s", body)
	}
	if string(raw["think"]) != "false" || string(raw["enable_thinking"]) != "false" {
		t.Fatalf("encoded chat body = %s", body)
	}
}

func TestApplyThinkingOffChat_InjectsNoneWhenClientOmittedEffort(t *testing.T) {
	req := &dto.ChatCompletionRequest{Model: "gpt-4o"}
	got, ok := applyThinkingOffToRequest(req).(*dto.ChatCompletionRequest)
	if !ok {
		t.Fatalf("expected *dto.ChatCompletionRequest, got %T", applyThinkingOffToRequest(req))
	}
	if got.ReasoningEffort != "none" {
		t.Fatalf("ReasoningEffort = %q, want none", got.ReasoningEffort)
	}
}

func TestApplyThinkingOffClaude_DisablesThinkingAndStripsEffort(t *testing.T) {
	budget := 1000
	req := &dto.ClaudeRequest{
		Model:        "claude-sonnet",
		Thinking:     &dto.Thinking{Type: "enabled", BudgetTokens: &budget},
		OutputConfig: json.RawMessage(`{"effort":"high","other":"keep"}`),
	}

	got, ok := applyThinkingOffToRequest(req).(*dto.ClaudeRequest)
	if !ok {
		t.Fatalf("expected *dto.ClaudeRequest, got %T", applyThinkingOffToRequest(req))
	}
	if got.Thinking == nil || got.Thinking.Type != "disabled" || got.Thinking.BudgetTokens != nil {
		t.Fatalf("thinking = %+v, want disabled without budget", got.Thinking)
	}
	var cfg map[string]string
	if err := json.Unmarshal(got.OutputConfig, &cfg); err != nil {
		t.Fatalf("output_config: %v", err)
	}
	if _, ok := cfg["effort"]; ok {
		t.Fatalf("output_config = %v, effort should be stripped", cfg)
	}
	if cfg["other"] != "keep" {
		t.Fatalf("output_config = %v, want other preserved", cfg)
	}
}

func TestApplyThinkingOffResponses_ClosesEnableThinkingAndSetsEffortNone(t *testing.T) {
	req := &dto.ResponsesRequest{
		Model:          "gpt-4o",
		EnableThinking: json.RawMessage(`true`),
		Reasoning:      &dto.ResponsesReasoning{Effort: "high", Summary: "auto"},
	}

	got, ok := applyThinkingOffToRequest(req).(*dto.ResponsesRequest)
	if !ok {
		t.Fatalf("expected *dto.ResponsesRequest, got %T", applyThinkingOffToRequest(req))
	}
	if string(got.EnableThinking) != "false" {
		t.Errorf("EnableThinking = %s, want false", got.EnableThinking)
	}
	if got.Reasoning == nil {
		t.Fatal("expected reasoning preserved for summary")
	}
	if got.Reasoning.Effort != "none" {
		t.Errorf("effort = %q, want none", got.Reasoning.Effort)
	}
	if got.Reasoning.Summary != "auto" {
		t.Errorf("summary = %q, want auto", got.Reasoning.Summary)
	}
}

func TestMaterializeLeafWithReasoningEffort(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"openai.chat"},
	}
	rules := upstreamEffortRule(reasoningTestProvider, "gpt-4o", []string{"high", "max"})

	leaf := model.PlanLeaf{
		ProviderName:  reasoningTestProvider,
		Provider:      provider,
		UpstreamModel: "gpt-4o",
		Weight:        1,
	}

	rawReq := &dto.ChatCompletionRequest{
		Model:           "gpt-4o",
		ReasoningEffort: "medium",
		Messages: []dto.Message{
			{Role: "user", Content: "hello"},
		},
	}

	inboundFormat := codec.FormatOpenAIChat
	inboundCodec, err := codec.Get(inboundFormat)
	if err != nil {
		t.Fatalf("get codec: %v", err)
	}

	node, err := materializeLeaf(context.Background(), leaf, materializeInput{
		InboundFormat: inboundFormat,
		InboundCodec:  inboundCodec,
		RawReq:        rawReq,
		ModelGroup:    "test-group",
		ClientModel:   "gpt-4o",
		KeyName:       "",
		Rules:         rules,
	})
	if err != nil {
		t.Fatalf("materializeLeaf: %v", err)
	}

	// 获取请求工厂并调用
	req, err := node.RequestFactory()
	if err != nil {
		t.Fatalf("RequestFactory: %v", err)
	}

	// 读取 body
	bodyBytes, err := readBody(req)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	// 解析 body 检查 reasoning_effort
	var bodyMap map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &bodyMap); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	effort, ok := bodyMap["reasoning_effort"].(string)
	if !ok {
		t.Fatal("expected reasoning_effort field")
	}

	if effort != "high" {
		t.Errorf("reasoning_effort = %q, want %q", effort, "high")
	}

	// 检查原始 rawReq 未被修改
	if rawReq.ReasoningEffort != "medium" {
		t.Errorf("original rawReq.ReasoningEffort = %q, want %q", rawReq.ReasoningEffort, "medium")
	}
}

func TestMaterializeLeafWithReasoningEffortResponses(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"openai.responses"},
	}
	rawReq := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Reasoning: &dto.ResponsesReasoning{
			Effort: "medium",
		},
	}

	body := materializeReasoningEffortBody(t, provider, codec.FormatOpenAIResponse, rawReq, upstreamEffortRule(reasoningTestProvider, "gpt-4o", []string{"high", "max"}))
	var got dto.ResponsesRequest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.Reasoning == nil || got.Reasoning.Effort != "high" {
		t.Errorf("reasoning = %+v, want effort high", got.Reasoning)
	}
	if rawReq.Reasoning.Effort != "medium" {
		t.Errorf("original effort = %q, want medium", rawReq.Reasoning.Effort)
	}
}

func TestMaterializeLeafStripReasoningEffortResponses(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"openai.responses"},
	}
	rawReq := &dto.ResponsesRequest{
		Model: "gpt-4o",
		Reasoning: &dto.ResponsesReasoning{
			Effort: "high",
		},
	}

	body := materializeReasoningEffortBody(t, provider, codec.FormatOpenAIResponse, rawReq, upstreamStripEffortRule(reasoningTestProvider, "gpt-4o"))
	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if _, ok := bodyMap["reasoning"]; ok {
		t.Errorf("body unexpectedly contains reasoning: %s", body)
	}
	if rawReq.Reasoning.Effort != "high" {
		t.Errorf("original effort = %q, want high", rawReq.Reasoning.Effort)
	}
}

func TestMaterializeLeafWithReasoningEffortClaude(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"anthropic.messages"},
	}
	rawReq := &dto.ClaudeRequest{
		Model:        "claude-sonnet",
		MaxTokens:    1,
		Messages:     []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
		OutputConfig: json.RawMessage(`{"effort":"medium","other":"value"}`),
	}

	body := materializeReasoningEffortBody(t, provider, codec.FormatAnthropicMessages, rawReq, upstreamEffortRule(reasoningTestProvider, "claude-sonnet", []string{"high", "max"}))
	var bodyMap map[string]json.RawMessage
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	var outputConfig map[string]string
	if err := json.Unmarshal(bodyMap["output_config"], &outputConfig); err != nil {
		t.Fatalf("unmarshal output_config: %v", err)
	}
	if outputConfig["effort"] != "high" || outputConfig["other"] != "value" {
		t.Errorf("output_config = %v, want clamped effort and preserved fields", outputConfig)
	}
	if string(rawReq.OutputConfig) != `{"effort":"medium","other":"value"}` {
		t.Errorf("original output_config = %s, want unchanged", rawReq.OutputConfig)
	}
}

func TestMaterializeLeafStripReasoningEffortClaude(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"anthropic.messages"},
	}
	rawReq := &dto.ClaudeRequest{
		Model:        "claude-sonnet",
		MaxTokens:    1,
		Messages:     []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
		OutputConfig: json.RawMessage(`{"effort":"high","other":"value"}`),
	}

	body := materializeReasoningEffortBody(t, provider, codec.FormatAnthropicMessages, rawReq, upstreamStripEffortRule(reasoningTestProvider, "claude-sonnet"))
	var bodyMap map[string]json.RawMessage
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	var outputConfig map[string]string
	if err := json.Unmarshal(bodyMap["output_config"], &outputConfig); err != nil {
		t.Fatalf("unmarshal output_config: %v", err)
	}
	if _, ok := outputConfig["effort"]; ok || outputConfig["other"] != "value" {
		t.Errorf("output_config = %v, want effort removed and other preserved", outputConfig)
	}
}

func materializeReasoningEffortBody(t *testing.T, provider config.ProviderConfig, inboundFormat codec.Format, rawReq any, rules []config.RuleConfig) []byte {
	t.Helper()
	inboundCodec, err := codec.Get(inboundFormat)
	if err != nil {
		t.Fatalf("get codec: %v", err)
	}
	node, err := materializeLeaf(context.Background(), model.PlanLeaf{
		ProviderName:  reasoningTestProvider,
		Provider:      provider,
		UpstreamModel: rawReqModel(rawReq),
		Weight:        1,
	}, materializeInput{
		InboundFormat: inboundFormat,
		InboundCodec:  inboundCodec,
		RawReq:        rawReq,
		ModelGroup:    "test-group",
		ClientModel:   rawReqModel(rawReq),
		KeyName:       "",
		Rules:         rules,
	})
	if err != nil {
		t.Fatalf("materializeLeaf: %v", err)
	}
	req, err := node.RequestFactory()
	if err != nil {
		t.Fatalf("RequestFactory: %v", err)
	}
	body, err := readBody(req)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body
}

func rawReqModel(rawReq any) string {
	switch req := rawReq.(type) {
	case *dto.ChatCompletionRequest:
		return req.Model
	case *dto.ResponsesRequest:
		return req.Model
	case *dto.ClaudeRequest:
		return req.Model
	default:
		return ""
	}
}

func TestMaterializeLeafDoesNotAddAbsentReasoningEffort(t *testing.T) {
	tests := []struct {
		name          string
		provider      config.ProviderConfig
		inboundFormat codec.Format
		rawReq        any
		rules         []config.RuleConfig
		assertAbsent  func(t *testing.T, body []byte)
	}{
		{
			name: "chat",
			provider: config.ProviderConfig{
				Endpoint:  "https://api.example.com",
				Protocols: []string{"openai.chat"},
			},
			inboundFormat: codec.FormatOpenAIChat,
			rawReq: &dto.ChatCompletionRequest{
				Model:    "gpt-4o",
				Messages: []dto.Message{{Role: "user", Content: "hello"}},
			},
			rules: upstreamStripEffortRule(reasoningTestProvider, "gpt-4o"),
			assertAbsent: func(t *testing.T, body []byte) {
				t.Helper()
				var got map[string]any
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatalf("unmarshal body: %v", err)
				}
				if _, ok := got["reasoning_effort"]; ok {
					t.Errorf("body unexpectedly contains reasoning_effort: %s", body)
				}
			},
		},
		{
			name: "responses",
			provider: config.ProviderConfig{
				Endpoint:  "https://api.example.com",
				Protocols: []string{"openai.responses"},
			},
			inboundFormat: codec.FormatOpenAIResponse,
			rawReq:        &dto.ResponsesRequest{Model: "gpt-4o"},
			rules:         upstreamEffortRule(reasoningTestProvider, "gpt-4o", []string{"high"}),
			assertAbsent: func(t *testing.T, body []byte) {
				t.Helper()
				var got map[string]any
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatalf("unmarshal body: %v", err)
				}
				if _, ok := got["reasoning"]; ok {
					t.Errorf("body unexpectedly contains reasoning: %s", body)
				}
			},
		},
		{
			name: "anthropic",
			provider: config.ProviderConfig{
				Endpoint:  "https://api.example.com",
				Protocols: []string{"anthropic.messages"},
			},
			inboundFormat: codec.FormatAnthropicMessages,
			rawReq: &dto.ClaudeRequest{
				Model:     "claude-sonnet",
				MaxTokens: 1,
				Messages:  []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
			},
			rules: upstreamStripEffortRule(reasoningTestProvider, "claude-sonnet"),
			assertAbsent: func(t *testing.T, body []byte) {
				t.Helper()
				var got map[string]any
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatalf("unmarshal body: %v", err)
				}
				if _, ok := got["output_config"]; ok {
					t.Errorf("body unexpectedly contains output_config: %s", body)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := materializeReasoningEffortBody(t, tt.provider, tt.inboundFormat, tt.rawReq, tt.rules)
			tt.assertAbsent(t, body)
		})
	}
}

func TestDualLeafIsolation(t *testing.T) {
	provider1 := config.ProviderConfig{
		Endpoint:  "https://api1.example.com",
		APIKey:    "test-key-1",
		Protocols: []string{"openai.chat"},
	}

	provider2 := config.ProviderConfig{
		Endpoint:  "https://api2.example.com",
		APIKey:    "test-key-2",
		Protocols: []string{"openai.chat"},
	}

	rules := append(
		upstreamEffortRule("provider-1", "gpt-4o", []string{"high", "max"}),
		upstreamStripEffortRule("provider-2", "gpt-4o")...,
	)

	leaf1 := model.PlanLeaf{
		ProviderName:  "provider-1",
		Provider:      provider1,
		UpstreamModel: "gpt-4o",
		Weight:        1,
	}

	leaf2 := model.PlanLeaf{
		ProviderName:  "provider-2",
		Provider:      provider2,
		UpstreamModel: "gpt-4o",
		Weight:        1,
	}

	// 共享的 rawReq
	rawReq := &dto.ChatCompletionRequest{
		Model:           "gpt-4o",
		ReasoningEffort: "medium",
		Messages: []dto.Message{
			{Role: "user", Content: "hello"},
		},
	}

	inboundFormat := codec.FormatOpenAIChat
	inboundCodec, err := codec.Get(inboundFormat)
	if err != nil {
		t.Fatalf("get codec: %v", err)
	}

	// 物化第一个叶子
	node1, err := materializeLeaf(context.Background(), leaf1, materializeInput{
		InboundFormat: inboundFormat,
		InboundCodec:  inboundCodec,
		RawReq:        rawReq,
		ModelGroup:    "test-group",
		ClientModel:   "gpt-4o",
		KeyName:       "",
		Rules:         rules,
	})
	if err != nil {
		t.Fatalf("materializeLeaf leaf1: %v", err)
	}

	node2, err := materializeLeaf(context.Background(), leaf2, materializeInput{
		InboundFormat: inboundFormat,
		InboundCodec:  inboundCodec,
		RawReq:        rawReq,
		ModelGroup:    "test-group",
		ClientModel:   "gpt-4o",
		KeyName:       "",
		Rules:         rules,
	})
	if err != nil {
		t.Fatalf("materializeLeaf leaf2: %v", err)
	}

	// 检查第一个叶子的 body
	req1, err := node1.RequestFactory()
	if err != nil {
		t.Fatalf("RequestFactory leaf1: %v", err)
	}
	body1, err := readBody(req1)
	if err != nil {
		t.Fatalf("read body leaf1: %v", err)
	}
	var bodyMap1 map[string]interface{}
	if err := json.Unmarshal(body1, &bodyMap1); err != nil {
		t.Fatalf("unmarshal body leaf1: %v", err)
	}
	effort1, _ := bodyMap1["reasoning_effort"].(string)
	if effort1 != "high" {
		t.Errorf("leaf1 reasoning_effort = %q, want %q", effort1, "high")
	}

	// 检查第二个叶子的 body
	req2, err := node2.RequestFactory()
	if err != nil {
		t.Fatalf("RequestFactory leaf2: %v", err)
	}
	body2, err := readBody(req2)
	if err != nil {
		t.Fatalf("read body leaf2: %v", err)
	}
	var bodyMap2 map[string]interface{}
	if err := json.Unmarshal(body2, &bodyMap2); err != nil {
		t.Fatalf("unmarshal body leaf2: %v", err)
	}
	_, hasEffort2 := bodyMap2["reasoning_effort"]
	if hasEffort2 {
		t.Error("leaf2 should not have reasoning_effort field in strip mode")
	}

	// 检查原始 rawReq 未被修改
	if rawReq.ReasoningEffort != "medium" {
		t.Errorf("original rawReq.ReasoningEffort = %q, want %q", rawReq.ReasoningEffort, "medium")
	}
}

func readBody(req interface{}) ([]byte, error) {
	switch r := req.(type) {
	case *http.Request:
		return io.ReadAll(r.Body)
	default:
		return nil, fmt.Errorf("unexpected request type: %T", req)
	}
}

// ── max_tokens floor 钳制测试 ─────────────────────────────────────────────

func intPtr(v int) *int { return &v }

func TestApplyMaxTokensFloorChat(t *testing.T) {
	tests := []struct {
		name          string
		req           *dto.ChatCompletionRequest
		floor         int
		wantMaxTokens int
		wantMCT       *int
	}{
		{
			name:          "未带任一字段 → 只补 max_tokens",
			req:           &dto.ChatCompletionRequest{Model: "m"},
			floor:         2000,
			wantMaxTokens: 2000,
		},
		{
			name:          "max_tokens 小于 floor → 提到 floor，不写 max_completion_tokens",
			req:           &dto.ChatCompletionRequest{Model: "m", MaxTokens: 200},
			floor:         2000,
			wantMaxTokens: 2000,
		},
		{
			name:          "max_tokens 大于 floor → 保持不动",
			req:           &dto.ChatCompletionRequest{Model: "m", MaxTokens: 4000},
			floor:         2000,
			wantMaxTokens: 4000,
		},
		{
			name:          "max_tokens 等于 floor → 保持",
			req:           &dto.ChatCompletionRequest{Model: "m", MaxTokens: 2000},
			floor:         2000,
			wantMaxTokens: 2000,
		},
		{
			name:          "只带 max_completion_tokens 且小于 floor → 只抬 MCT",
			req:           &dto.ChatCompletionRequest{Model: "m", MaxCompletionTokens: intPtr(100)},
			floor:         2000,
			wantMaxTokens: 0,
			wantMCT:       intPtr(2000),
		},
		{
			name:          "只带 max_completion_tokens 且大于 floor → 保持，不注入 max_tokens",
			req:           &dto.ChatCompletionRequest{Model: "m", MaxCompletionTokens: intPtr(8000)},
			floor:         2000,
			wantMaxTokens: 0,
			wantMCT:       intPtr(8000),
		},
		{
			name:          "两字段都带且 MCT 已高于 floor → 不碰 max_tokens",
			req:           &dto.ChatCompletionRequest{Model: "m", MaxTokens: 100, MaxCompletionTokens: intPtr(8000)},
			floor:         2000,
			wantMaxTokens: 100,
			wantMCT:       intPtr(8000),
		},
		{
			name:          "两字段都带且 MCT 低于 floor → 只抬 MCT",
			req:           &dto.ChatCompletionRequest{Model: "m", MaxTokens: 4000, MaxCompletionTokens: intPtr(100)},
			floor:         2000,
			wantMaxTokens: 4000,
			wantMCT:       intPtr(2000),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origTokens := tt.req.MaxTokens
			var origMCT *int
			if tt.req.MaxCompletionTokens != nil {
				v := *tt.req.MaxCompletionTokens
				origMCT = &v
			}
			got := applyMaxTokensFloorToRequest(tt.req, tt.floor)
			chat, ok := got.(*dto.ChatCompletionRequest)
			if !ok {
				t.Fatalf("type = %T, want *dto.ChatCompletionRequest", got)
			}
			if chat.MaxTokens != tt.wantMaxTokens {
				t.Errorf("MaxTokens = %d, want %d", chat.MaxTokens, tt.wantMaxTokens)
			}
			if tt.wantMCT == nil {
				if chat.MaxCompletionTokens != nil {
					t.Errorf("MaxCompletionTokens = %v, want nil", *chat.MaxCompletionTokens)
				}
			} else if chat.MaxCompletionTokens == nil || *chat.MaxCompletionTokens != *tt.wantMCT {
				t.Errorf("MaxCompletionTokens = %v, want %d", chat.MaxCompletionTokens, *tt.wantMCT)
			}
			if tt.req.MaxTokens != origTokens {
				t.Errorf("original MaxTokens mutated: %d → %d", origTokens, tt.req.MaxTokens)
			}
			if origMCT == nil {
				if tt.req.MaxCompletionTokens != nil {
					t.Errorf("original MaxCompletionTokens mutated to %v", *tt.req.MaxCompletionTokens)
				}
			} else if tt.req.MaxCompletionTokens == nil || *tt.req.MaxCompletionTokens != *origMCT {
				t.Errorf("original MaxCompletionTokens mutated: %v", tt.req.MaxCompletionTokens)
			}
		})
	}
}

func jsonInt(m map[string]any, key string) (int, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	n, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(n), true
}

func TestApplyLeafRules_MaxTokensFloor_ChatEncodeOmitsInactiveField(t *testing.T) {
	c := &codec.OpenAIChatCodec{}
	floor := 2000
	merged := config.RuleAction{MaxTokens: &floor}

	encode := func(t *testing.T, req *dto.ChatCompletionRequest) map[string]any {
		t.Helper()
		processed := applyLeafRules(req, merged)
		body, err := c.EncodeRequest(codec.FormatOpenAIChat, processed, "deepseek-v4-flash-vision-exp", false)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return m
	}

	t.Run("unset writes only max_tokens", func(t *testing.T) {
		m := encode(t, &dto.ChatCompletionRequest{
			Model:    "vision",
			Messages: []dto.Message{{Role: "user", Content: "hi"}},
		})
		got, ok := jsonInt(m, "max_tokens")
		if !ok || got != 2000 {
			t.Errorf("max_tokens = %v present=%v, want 2000", m["max_tokens"], ok)
		}
		if _, ok := m["max_completion_tokens"]; ok {
			t.Errorf("max_completion_tokens must be omitted, got %v", m["max_completion_tokens"])
		}
	})

	t.Run("only MCT above floor does not inject max_tokens", func(t *testing.T) {
		mct := 8000
		m := encode(t, &dto.ChatCompletionRequest{
			Model:               "vision",
			MaxCompletionTokens: &mct,
			Messages:            []dto.Message{{Role: "user", Content: "hi"}},
		})
		if _, ok := m["max_tokens"]; ok {
			t.Errorf("max_tokens must be omitted, got %v", m["max_tokens"])
		}
		got, ok := jsonInt(m, "max_completion_tokens")
		if !ok || got != 8000 {
			t.Errorf("max_completion_tokens = %v present=%v, want 8000", m["max_completion_tokens"], ok)
		}
	})

	t.Run("only MCT below floor raises MCT only", func(t *testing.T) {
		mct := 100
		m := encode(t, &dto.ChatCompletionRequest{
			Model:               "vision",
			MaxCompletionTokens: &mct,
			Messages:            []dto.Message{{Role: "user", Content: "hi"}},
		})
		if _, ok := m["max_tokens"]; ok {
			t.Errorf("max_tokens must be omitted, got %v", m["max_tokens"])
		}
		got, ok := jsonInt(m, "max_completion_tokens")
		if !ok || got != 2000 {
			t.Errorf("max_completion_tokens = %v present=%v, want 2000", m["max_completion_tokens"], ok)
		}
	})
}

func TestApplyMaxTokensFloorResponses(t *testing.T) {
	// 未带 → 补 floor
	req := &dto.ResponsesRequest{Model: "m"}
	got := applyMaxTokensFloorToRequest(req, 2000).(*dto.ResponsesRequest)
	if got.MaxOutputTokens != 2000 {
		t.Errorf("MaxOutputTokens = %d, want 2000", got.MaxOutputTokens)
	}

	// 已带且大于 floor → 保持
	req2 := &dto.ResponsesRequest{Model: "m", MaxOutputTokens: 5000}
	got2 := applyMaxTokensFloorToRequest(req2, 2000).(*dto.ResponsesRequest)
	if got2.MaxOutputTokens != 5000 {
		t.Errorf("MaxOutputTokens = %d, want 5000", got2.MaxOutputTokens)
	}
}

func TestApplyMaxTokensFloorClaude(t *testing.T) {
	req := &dto.ClaudeRequest{Model: "m"}
	got := applyMaxTokensFloorToRequest(req, 2000).(*dto.ClaudeRequest)
	if got.MaxTokens != 2000 {
		t.Errorf("MaxTokens = %d, want 2000", got.MaxTokens)
	}

	req2 := &dto.ClaudeRequest{Model: "m", MaxTokens: 4096}
	got2 := applyMaxTokensFloorToRequest(req2, 2000).(*dto.ClaudeRequest)
	if got2.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096 (already above floor, unchanged)", got2.MaxTokens)
	}
}

func TestApplyMaxTokensFloor_UnknownTypePassthrough(t *testing.T) {
	raw := "not-a-request"
	if got := applyMaxTokensFloorToRequest(raw, 2000); got != raw {
		t.Errorf("unknown type should passthrough, got %#v", got)
	}
}

// ── temperature strip 测试 ────────────────────────────────────────────────

func floatPtr(v float64) *float64 { return &v }

func upstreamStripTemperatureRule(providerName, model string) []config.RuleConfig {
	return []config.RuleConfig{{
		Match: config.RuleMatch{
			UpstreamModel: &config.RuleCondition{
				Op:    "equals",
				Value: providerName + "/" + model,
			},
		},
		Action: config.RuleAction{TemperatureMode: "strip"},
	}}
}

func rawReqTemperature(rawReq any) *float64 {
	switch req := rawReq.(type) {
	case *dto.ChatCompletionRequest:
		return req.Temperature
	case *dto.ResponsesRequest:
		return req.Temperature
	case *dto.ClaudeRequest:
		return req.Temperature
	default:
		return nil
	}
}

func TestApplyLeafRules_TemperatureStrip(t *testing.T) {
	merged := config.RuleAction{TemperatureMode: "strip"}

	t.Run("chat request", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{Model: "m", Temperature: floatPtr(0.7)}
		got, ok := applyLeafRules(req, merged).(*dto.ChatCompletionRequest)
		if !ok {
			t.Fatalf("type = %T, want *dto.ChatCompletionRequest", got)
		}
		if got.Temperature != nil {
			t.Errorf("Temperature = %v, want nil", *got.Temperature)
		}
		if req.Temperature == nil {
			t.Error("original request must not be mutated")
		}
	})

	t.Run("responses request", func(t *testing.T) {
		req := &dto.ResponsesRequest{Model: "m", Temperature: floatPtr(0.2)}
		got := applyLeafRules(req, merged).(*dto.ResponsesRequest)
		if got.Temperature != nil {
			t.Errorf("Temperature = %v, want nil", *got.Temperature)
		}
		if req.Temperature == nil {
			t.Error("original request must not be mutated")
		}
	})

	t.Run("claude request", func(t *testing.T) {
		req := &dto.ClaudeRequest{Model: "m", Temperature: floatPtr(1.0)}
		got := applyLeafRules(req, merged).(*dto.ClaudeRequest)
		if got.Temperature != nil {
			t.Errorf("Temperature = %v, want nil", *got.Temperature)
		}
		if req.Temperature == nil {
			t.Error("original request must not be mutated")
		}
	})

	t.Run("absent temperature returns same request", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{Model: "m"}
		if got := applyLeafRules(req, merged).(*dto.ChatCompletionRequest); got != req {
			t.Error("request without temperature must be returned as-is (no clone)")
		}
	})

	t.Run("no temperature_mode leaves request untouched", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{Model: "m", Temperature: floatPtr(0.5)}
		got := applyLeafRules(req, config.RuleAction{}).(*dto.ChatCompletionRequest)
		if got.Temperature == nil || *got.Temperature != 0.5 {
			t.Errorf("Temperature = %v, want 0.5 (passthrough)", got.Temperature)
		}
	})

	t.Run("unknown type passthrough", func(t *testing.T) {
		raw := "not-a-request"
		if got := applyTemperatureStripToRequest(raw); got != raw {
			t.Errorf("unknown type should passthrough, got %#v", got)
		}
	})
}

func TestMaterializeLeafStripsTemperature(t *testing.T) {
	tests := []struct {
		name          string
		provider      config.ProviderConfig
		inboundFormat codec.Format
		rawReq        any
	}{
		{
			name: "chat",
			provider: config.ProviderConfig{
				Endpoint:  "https://api.example.com",
				Protocols: []string{"openai.chat"},
			},
			inboundFormat: codec.FormatOpenAIChat,
			rawReq: &dto.ChatCompletionRequest{
				Model:       "gpt-4o",
				Temperature: floatPtr(0.7),
				Messages:    []dto.Message{{Role: "user", Content: "hello"}},
			},
		},
		{
			name: "responses",
			provider: config.ProviderConfig{
				Endpoint:  "https://api.example.com",
				Protocols: []string{"openai.responses"},
			},
			inboundFormat: codec.FormatOpenAIResponse,
			rawReq:        &dto.ResponsesRequest{Model: "gpt-4o", Temperature: floatPtr(0.7)},
		},
		{
			name: "anthropic",
			provider: config.ProviderConfig{
				Endpoint:  "https://api.example.com",
				Protocols: []string{"anthropic.messages"},
			},
			inboundFormat: codec.FormatAnthropicMessages,
			rawReq: &dto.ClaudeRequest{
				Model:       "claude-sonnet",
				MaxTokens:   1,
				Temperature: floatPtr(0.7),
				Messages:    []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
			},
		},
		{
			// responses 入站 + anthropic-only provider：走 response→chat→anthropic 二次转换，
			// temperature 也不能在中间跳被重新带出来。
			name: "responses to anthropic second hop",
			provider: config.ProviderConfig{
				Endpoint:  "https://api.example.com",
				Protocols: []string{"anthropic.messages"},
			},
			inboundFormat: codec.FormatOpenAIResponse,
			rawReq:        &dto.ResponsesRequest{Model: "claude-sonnet", Temperature: floatPtr(0.7)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := upstreamStripTemperatureRule(reasoningTestProvider, rawReqModel(tt.rawReq))
			body := materializeReasoningEffortBody(t, tt.provider, tt.inboundFormat, tt.rawReq, rules)

			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if _, ok := got["temperature"]; ok {
				t.Errorf("body unexpectedly contains temperature: %s", body)
			}
			if rawReqTemperature(tt.rawReq) == nil {
				t.Error("original request must keep its temperature")
			}
		})
	}
}
