package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestChat_RulesForcedAnthropicOutbound_WriteResponseAndMetricsUseMaterializedProtocol(t *testing.T) {
	metrics.ResetForTest()
	stats.Reset()

	var anthropicHits int
	var openAIHits int
	anthropicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicHits++
		if r.URL.Path != "/v1/messages" {
			t.Errorf("anthropic path = %q, want /v1/messages", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"msg-rules",
			"type":"message",
			"role":"assistant",
			"content":[{"type":"text","text":"anthropic-outbound"}],
			"model":"claude-sonnet",
			"stop_reason":"end_turn",
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer anthropicSrv.Close()

	openAISrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openAIHits++
		t.Fatalf("unexpected openai request path=%q; rules must force anthropic outbound", r.URL.Path)
	}))
	defer openAISrv.Close()

	providers := map[string]config.ProviderConfig{
		"mixed-provider": {
			Endpoint: openAISrv.URL,
			Endpoints: []config.EndpointConfig{
				{URL: anthropicSrv.URL, Protocols: []string{"anthropic.messages"}},
				{URL: openAISrv.URL, Protocols: []string{"openai.chat"}},
			},
			APIKey:    "test-key",
			Protocols: []string{"openai.chat", "anthropic.messages"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{{
			Name:   "claude-alias",
			Models: config.ModelEntries{{Model: "mixed-provider/claude-sonnet", Weight: 1}},
		}},
		Rules: []config.RuleConfig{{
			Match: config.RuleMatch{
				UpstreamModel: &config.RuleCondition{Op: "equals", Value: "mixed-provider/claude-sonnet"},
			},
			Action: config.RuleAction{Protocol: "anthropic.messages"},
		}},
	}

	testResolver, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	testClient := provider.NewClient(providers, 120*time.Second, 0, 0)
	testRLManager := ratelimit.NewManager(providers)
	testHealthChecker := health.NewChecker(3, 30*time.Second)
	testSched := scheduler.New(testRLManager, testClient, testHealthChecker, 500*time.Millisecond, 0, 0)

	testDBPath := filepath.Join(t.TempDir(), "rules-materialized-outbound-stats.db")
	if err := stats.Init(testDBPath); err != nil {
		t.Fatalf("Init stats: %v", err)
	}
	defer stats.Reset()

	oldCfg, oldResolver, oldSched := cfg, resolver, sched
	cfg, resolver, sched = testCfg, testResolver, testSched
	defer func() { cfg, resolver, sched = oldCfg, oldResolver, oldSched }()

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("key_name", "test-key-123")
		c.Next()
	})
	router.POST("/v1/chat/completions", Chat)

	reqBody := `{"model":"claude-alias","messages":[{"role":"user","content":"Hello"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if anthropicHits != 1 {
		t.Fatalf("anthropic hits = %d, want 1", anthropicHits)
	}
	if openAIHits != 0 {
		t.Fatalf("openai hits = %d, want 0", openAIHits)
	}

	var chatResp dto.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("unmarshal chat response: %v", err)
	}
	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message == nil {
		t.Fatal("expected chat choices")
	}
	if chatResp.Choices[0].Message.Content != "anthropic-outbound" {
		t.Fatalf("content = %q, want anthropic-outbound (WriteResponse must decode anthropic upstream)", chatResp.Choices[0].Message.Content)
	}

	err = testutil.CollectAndCompare(metrics.GetRequestTotal(), strings.NewReader(`
		# HELP request_total 请求总数
		# TYPE request_total counter
		request_total{inbound_protocol="openai.chat",key_name="test-key-123",model_group="claude-alias",outbound_protocol="anthropic.messages",provider="mixed-provider",status="success",upstream_model="claude-sonnet"} 1
	`), "request_total")
	if err != nil {
		t.Errorf("request_total metric mismatch: %v", err)
	}
}

func TestMaterializeLeaf_ForcedProtocolUnreachable(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"openai.chat"},
	}
	leaf := model.PlanLeaf{
		ProviderName:  "prov-a",
		Provider:      provider,
		UpstreamModel: "gpt-4o",
	}
	rawReq := &dto.ChatCompletionRequest{
		Model:    "alias-model",
		Messages: []dto.Message{{Role: "user", Content: "hi"}},
	}
	rules := []config.RuleConfig{{
		Match: config.RuleMatch{
			UpstreamModel: &config.RuleCondition{Op: "equals", Value: "prov-a/gpt-4o"},
		},
		Action: config.RuleAction{Protocol: "anthropic.messages"},
	}}

	inboundCodec, err := codec.Get(codec.FormatOpenAIChat)
	if err != nil {
		t.Fatalf("get codec: %v", err)
	}

	node, err := materializeLeaf(context.Background(), leaf, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        rawReq,
		ModelGroup:    "g",
		ClientModel:   "alias-model",
		KeyName:       "key-1",
		Rules:         rules,
	})
	if err != nil {
		t.Fatalf("materializeLeaf: %v", err)
	}
	if !node.Unschedulable {
		t.Fatal("expected unschedulable leaf")
	}
	if node.RequestFactory != nil {
		t.Fatal("unschedulable leaf must not have RequestFactory")
	}
}

func TestMaterializeLeaf_InvalidForcedProtocol_ReturnsError(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"openai.chat"},
	}
	leaf := model.PlanLeaf{
		ProviderName:  "prov-a",
		Provider:      provider,
		UpstreamModel: "gpt-4o",
	}
	rawReq := &dto.ChatCompletionRequest{
		Model:    "alias-model",
		Messages: []dto.Message{{Role: "user", Content: "hi"}},
	}
	rules := []config.RuleConfig{{
		Action: config.RuleAction{Protocol: "not-a-real-protocol"},
	}}

	inboundCodec, err := codec.Get(codec.FormatOpenAIChat)
	if err != nil {
		t.Fatalf("get codec: %v", err)
	}

	_, err = materializeLeaf(context.Background(), leaf, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        rawReq,
		ModelGroup:    "g",
		ClientModel:   "alias-model",
		KeyName:       "key-1",
		Rules:         rules,
	})
	if err == nil {
		t.Fatal("expected invalid forced protocol error")
	}
	if !strings.Contains(err.Error(), "invalid forced protocol") {
		t.Fatalf("error = %q, want invalid forced protocol", err.Error())
	}
}

func TestMaterializeLeaf_RulesEffortClamp(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"openai.chat"},
	}
	leaf := model.PlanLeaf{
		ProviderName:  "prov-a",
		Provider:      provider,
		UpstreamModel: "gpt-4o",
	}
	rawReq := &dto.ChatCompletionRequest{
		Model:           "alias-model",
		ReasoningEffort: "medium",
		Messages:        []dto.Message{{Role: "user", Content: "hi"}},
	}
	rules := []config.RuleConfig{{
		Match: config.RuleMatch{
			ClientModel: &config.RuleCondition{Op: "equals", Value: "alias-model"},
		},
		Action: config.RuleAction{Effort: []string{"high", "max"}},
	}}

	body := materializeLeafBody(t, leaf, codec.FormatOpenAIChat, rawReq, "alias-model", "key-1", rules)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v, want high", got["reasoning_effort"])
	}
}

func TestMaterializeLeaf_ThinkingOff_EncodedChatOutboundClosesThinkFields(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"openai.chat"},
	}
	leaf := model.PlanLeaf{
		ProviderName:  "prov-a",
		Provider:      provider,
		UpstreamModel: "gpt-4o",
	}
	rawReq := &dto.ChatCompletionRequest{
		Model:           "alias-model",
		ReasoningEffort: "high",
		Reasoning:       json.RawMessage(`{"max_tokens":3000}`),
		Think:           json.RawMessage(`true`),
		THINKING:        json.RawMessage(`{"type":"enabled","budget_tokens":1000}`),
		EnableThinking:  json.RawMessage(`true`),
		Messages:        []dto.Message{{Role: "user", Content: "hi"}},
	}
	rules := []config.RuleConfig{{
		Match:  config.RuleMatch{},
		Action: config.RuleAction{Thinking: "off"},
	}}

	body := materializeLeafBody(t, leaf, codec.FormatOpenAIChat, rawReq, "alias-model", "", rules)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if string(got["reasoning_effort"]) != `"none"` {
		t.Fatalf("reasoning_effort = %s, want \"none\"", got["reasoning_effort"])
	}
	if _, ok := got["reasoning"]; ok {
		t.Fatalf("body still has reasoning: %s", body)
	}
	if string(got["think"]) != "false" {
		t.Fatalf("think = %s, want false", got["think"])
	}
	if string(got["enable_thinking"]) != "false" {
		t.Fatalf("enable_thinking = %s, want false", got["enable_thinking"])
	}
	if string(got["THINKING"]) != `{"type":"disabled"}` {
		t.Fatalf("THINKING = %s, want disabled", got["THINKING"])
	}
}

func TestMaterializeLeaf_ThinkingOff_InjectsNoneWhenClientOmittedEffort(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"openai.chat"},
	}
	leaf := model.PlanLeaf{
		ProviderName:  "prov-a",
		Provider:      provider,
		UpstreamModel: "gpt-4o",
	}
	rawReq := &dto.ChatCompletionRequest{
		Model:    "translator",
		Messages: []dto.Message{{Role: "user", Content: "hi"}},
	}
	rules := []config.RuleConfig{{
		Match:  config.RuleMatch{},
		Action: config.RuleAction{Thinking: "off"},
	}}

	body := materializeLeafBody(t, leaf, codec.FormatOpenAIChat, rawReq, "translator", "", rules)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got["reasoning_effort"] != "none" {
		t.Fatalf("reasoning_effort = %v, want none", got["reasoning_effort"])
	}
}

func TestMaterializeLeaf_MaxTokensFloor_ChatOutbound(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"openai.chat"},
	}
	leaf := model.PlanLeaf{
		ProviderName:  "token-hub",
		Provider:      provider,
		UpstreamModel: "deepseek/deepseek-v4-flash-vision",
	}
	floor := 2000
	rules := []config.RuleConfig{{
		Match: config.RuleMatch{
			UpstreamModel: &config.RuleCondition{Op: "startWith", Value: "token-hub/deepseek/deepseek-v4-flash-vision"},
		},
		Action: config.RuleAction{MaxTokens: &floor},
	}}

	assertTokenFields := func(t *testing.T, body []byte, wantMaxTokens, wantMCT *int) {
		t.Helper()
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if wantMaxTokens == nil {
			if _, ok := got["max_tokens"]; ok {
				t.Errorf("max_tokens must be omitted, got %v", got["max_tokens"])
			}
		} else {
			n, ok := got["max_tokens"].(float64)
			if !ok || int(n) != *wantMaxTokens {
				t.Errorf("max_tokens = %v, want %d", got["max_tokens"], *wantMaxTokens)
			}
		}
		if wantMCT == nil {
			if _, ok := got["max_completion_tokens"]; ok {
				t.Errorf("max_completion_tokens must be omitted, got %v", got["max_completion_tokens"])
			}
		} else {
			n, ok := got["max_completion_tokens"].(float64)
			if !ok || int(n) != *wantMCT {
				t.Errorf("max_completion_tokens = %v, want %d", got["max_completion_tokens"], *wantMCT)
			}
		}
	}

	t.Run("unset fills max_tokens only", func(t *testing.T) {
		rawReq := &dto.ChatCompletionRequest{
			Model:    "vision",
			Messages: []dto.Message{{Role: "user", Content: "hi"}},
		}
		body := materializeLeafBody(t, leaf, codec.FormatOpenAIChat, rawReq, "vision", "", rules)
		want := 2000
		assertTokenFields(t, body, &want, nil)
	})

	t.Run("MCT above floor is kept without injecting max_tokens", func(t *testing.T) {
		mct := 8000
		rawReq := &dto.ChatCompletionRequest{
			Model:               "vision",
			MaxCompletionTokens: &mct,
			Messages:            []dto.Message{{Role: "user", Content: "hi"}},
		}
		body := materializeLeafBody(t, leaf, codec.FormatOpenAIChat, rawReq, "vision", "", rules)
		want := 8000
		assertTokenFields(t, body, nil, &want)
	})
}

func TestMaterializeLeaf_ThinkingOff_ChatToAnthropicMapsNoneToDisabled(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"anthropic.messages"},
	}
	leaf := model.PlanLeaf{
		ProviderName:  "prov-a",
		Provider:      provider,
		UpstreamModel: "claude-sonnet",
	}
	rawReq := &dto.ChatCompletionRequest{
		Model:           "alias-model",
		ReasoningEffort: "high",
		Messages:        []dto.Message{{Role: "user", Content: "hi"}},
	}
	rules := []config.RuleConfig{{
		Match:  config.RuleMatch{},
		Action: config.RuleAction{Thinking: "off"},
	}}

	body := materializeLeafBody(t, leaf, codec.FormatOpenAIChat, rawReq, "alias-model", "", rules)
	var got dto.ClaudeRequest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.Thinking == nil || got.Thinking.Type != "disabled" {
		t.Fatalf("thinking = %+v, want disabled", got.Thinking)
	}
	if got.Thinking.BudgetTokens != nil {
		t.Fatalf("thinking.budget_tokens = %v, want nil", got.Thinking.BudgetTokens)
	}
}

func TestMaterializeLeaf_ThinkingOff_EncodedOllamaOutboundClosesThink(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://ollama.example.com",
		APIKey:    "test-key",
		Protocols: []string{"ollama.chat"},
		Endpoints: []config.EndpointConfig{{
			URL:       "https://ollama.example.com",
			Protocols: []string{"ollama.chat"},
		}},
	}
	leaf := model.PlanLeaf{
		ProviderName:  "prov-a",
		Provider:      provider,
		UpstreamModel: "qwen3",
	}
	rawReq := &dto.ChatCompletionRequest{
		Model:          "alias-model",
		Think:          json.RawMessage(`true`),
		EnableThinking: json.RawMessage(`{"budget_tokens":1000}`),
		Messages:       []dto.Message{{Role: "user", Content: "hi"}},
	}
	rules := []config.RuleConfig{{
		Match:  config.RuleMatch{},
		Action: config.RuleAction{Thinking: "off"},
	}}

	body := materializeLeafBody(t, leaf, codec.FormatOpenAIChat, rawReq, "alias-model", "", rules)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if string(got["think"]) != "false" {
		t.Fatalf("ollama think = %s, want false", got["think"])
	}
}

func TestMaterializeLeaf_ThinkingOff_AnthropicInbound(t *testing.T) {
	budget := 1000
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"anthropic.messages"},
	}
	leaf := model.PlanLeaf{
		ProviderName:  "prov-a",
		Provider:      provider,
		UpstreamModel: "claude-sonnet",
	}
	rawReq := &dto.ClaudeRequest{
		Model:        "alias-model",
		MaxTokens:    128,
		Messages:     []dto.ClaudeMessage{{Role: "user", Content: "hi"}},
		Thinking:     &dto.Thinking{Type: "enabled", BudgetTokens: &budget},
		OutputConfig: json.RawMessage(`{"effort":"high"}`),
	}
	rules := []config.RuleConfig{{
		Match:  config.RuleMatch{},
		Action: config.RuleAction{Thinking: "off"},
	}}

	body := materializeLeafBody(t, leaf, codec.FormatAnthropicMessages, rawReq, "alias-model", "", rules)
	var got dto.ClaudeRequest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.Thinking == nil || got.Thinking.Type != "disabled" {
		t.Fatalf("thinking = %+v, want disabled", got.Thinking)
	}
	if len(got.OutputConfig) > 0 {
		var cfg map[string]any
		if err := json.Unmarshal(got.OutputConfig, &cfg); err != nil {
			t.Fatalf("output_config: %v", err)
		}
		if _, ok := cfg["effort"]; ok {
			t.Fatalf("output_config still has effort: %v", cfg)
		}
	}
}

func TestMaterializeLeaf_ThinkingOff_ResponsesInbound(t *testing.T) {
	provider := config.ProviderConfig{
		Endpoint:  "https://api.example.com",
		APIKey:    "test-key",
		Protocols: []string{"openai.responses"},
	}
	leaf := model.PlanLeaf{
		ProviderName:  "prov-a",
		Provider:      provider,
		UpstreamModel: "gpt-4o",
	}
	rawReq := &dto.ResponsesRequest{
		Model:          "alias-model",
		Input:          json.RawMessage(`"hello"`),
		EnableThinking: json.RawMessage(`true`),
		Reasoning:      &dto.ResponsesReasoning{Effort: "high"},
	}
	rules := []config.RuleConfig{{
		Match:  config.RuleMatch{},
		Action: config.RuleAction{Thinking: "off"},
	}}

	body := materializeLeafBody(t, leaf, codec.FormatOpenAIResponse, rawReq, "alias-model", "", rules)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if string(got["enable_thinking"]) != "false" {
		t.Fatalf("enable_thinking = %s, want false", got["enable_thinking"])
	}
	var reasoning dto.ResponsesReasoning
	if err := json.Unmarshal(got["reasoning"], &reasoning); err != nil {
		t.Fatalf("reasoning = %s, unmarshal: %v", got["reasoning"], err)
	}
	if reasoning.Effort != "none" {
		t.Fatalf("reasoning.effort = %q, want none", reasoning.Effort)
	}
}

func TestExecuteNode_AllLeavesUnschedulable_ReturnsErrNoProviderAvailable(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"prov-a": {
			Endpoint:  srv.URL,
			APIKey:    "key",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"prov-b": {
			Endpoint:  srv.URL,
			APIKey:    "key",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := scheduler.New(rl, client, h, 500*time.Millisecond, 0, 0)

	root := &scheduler.RunNode{
		IsLeaf: false,
		Name:   "group",
		Mode:   "failover",
		Children: []*scheduler.RunNode{
			{
				IsLeaf:        true,
				Unschedulable: true,
				Task: scheduler.Task{
					ProviderName:     "prov-a",
					Provider:         providers["prov-a"],
					UpstreamModel:    "m1",
					OutboundProtocol: "anthropic.messages",
				},
			},
			{
				IsLeaf:        true,
				Unschedulable: true,
				Task: scheduler.Task{
					ProviderName:     "prov-b",
					Provider:         providers["prov-b"],
					UpstreamModel:    "m2",
					OutboundProtocol: "anthropic.messages",
				},
			},
		},
	}

	_, err := sched.ExecuteNode(context.Background(), root)
	if err != scheduler.ErrNoProviderAvailable {
		t.Fatalf("expected ErrNoProviderAvailable, got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream called %d times, want 0", calls.Load())
	}
}

func TestExecuteNode_MixedReachableAndUnschedulable_UsesReachableLeaf(t *testing.T) {
	var winner atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		winner.Store(r.Host)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"prov-a": {
			Endpoint:  srv.URL,
			APIKey:    "key",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"prov-b": {
			Endpoint:  srv.URL,
			APIKey:    "key",
			Protocols: []string{"openai.chat"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := scheduler.New(rl, client, h, 500*time.Millisecond, 0, 0)

	body := []byte(`{"model":"m2","messages":[{"role":"user","content":"hi"}]}`)
	reachable := &scheduler.RunNode{
		IsLeaf: true,
		Task: scheduler.Task{
			ProviderName:     "prov-b",
			Provider:         providers["prov-b"],
			UpstreamModel:    "m2",
			OutboundProtocol: "openai.chat",
		},
		RequestFactory: func() (*http.Request, error) {
			req, err := http.NewRequest(http.MethodPost, srv.URL, nil)
			if err != nil {
				return nil, err
			}
			req.Body = http.NoBody
			req.ContentLength = int64(len(body))
			return req, nil
		},
	}

	root := &scheduler.RunNode{
		IsLeaf: false,
		Name:   "group",
		Mode:   "failover",
		Children: []*scheduler.RunNode{
			{
				IsLeaf:        true,
				Unschedulable: true,
				Task: scheduler.Task{
					ProviderName:     "prov-a",
					Provider:         providers["prov-a"],
					UpstreamModel:    "m1",
					OutboundProtocol: "anthropic.messages",
				},
			},
			reachable,
		},
	}

	result, err := sched.ExecuteNode(context.Background(), root)
	if err != nil {
		t.Fatalf("ExecuteNode: %v", err)
	}
	if result.OutboundProtocol != "openai.chat" {
		t.Fatalf("OutboundProtocol = %q, want openai.chat", result.OutboundProtocol)
	}
	if winner.Load() == nil {
		t.Fatal("expected reachable leaf to be called")
	}
}

func materializeLeafBody(t *testing.T, leaf model.PlanLeaf, inboundFormat codec.Format, rawReq any, clientModel, keyName string, rules []config.RuleConfig) []byte {
	t.Helper()
	inboundCodec, err := codec.Get(inboundFormat)
	if err != nil {
		t.Fatalf("get codec: %v", err)
	}
	node, err := materializeLeaf(context.Background(), leaf, materializeInput{
		InboundFormat: inboundFormat,
		InboundCodec:  inboundCodec,
		RawReq:        rawReq,
		ModelGroup:    "test-group",
		ClientModel:   clientModel,
		KeyName:       keyName,
		Rules:         rules,
	})
	if err != nil {
		t.Fatalf("materializeLeaf: %v", err)
	}
	if node.Unschedulable {
		t.Fatal("expected schedulable leaf")
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

// TestMaterializeLeaf_WritesSchedulingActionsToTask 验证 chat 路径 materializeLeaf
// 把合并后的 qpm / 时段 rule 结果写入 Task（调度层在物化时不因当前时钟把 leaf 标为 Unschedulable；
// 时段只影响执行时跳过，不影响请求改写）。
func TestMaterializeLeaf_WritesSchedulingActionsToTask(t *testing.T) {
	leaf := model.PlanLeaf{
		ProviderName:  "openai",
		Provider:      config.ProviderConfig{Protocols: []string{"openai.chat"}},
		UpstreamModel: "gpt-4o",
	}
	qpm := 120
	rules := []config.RuleConfig{
		{
			Match: config.RuleMatch{
				UpstreamModel: &config.RuleCondition{Op: "equals", Value: "openai/gpt-4o"},
			},
			Action: config.RuleAction{QPM: &qpm},
		},
		{
			Match: config.RuleMatch{
				UpstreamModel: &config.RuleCondition{Op: "equals", Value: "openai/gpt-4o"},
			},
			Action: config.RuleAction{EnableTimeRange: []string{"09:00-18:00"}},
		},
		{
			Match: config.RuleMatch{
				UpstreamModel: &config.RuleCondition{Op: "equals", Value: "openai/gpt-4o"},
			},
			Action: config.RuleAction{DisableTimeRange: []string{"23:00-02:00"}},
		},
	}

	inboundCodec, err := codec.Get(codec.FormatOpenAIChat)
	if err != nil {
		t.Fatalf("get codec: %v", err)
	}
	rawReq := &dto.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []dto.Message{{Role: "user", Content: "hi"}},
	}
	node, err := materializeLeaf(context.Background(), leaf, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        rawReq,
		ModelGroup:    "test-group",
		ClientModel:   "gpt-4o",
		KeyName:       "key-1",
		Rules:         rules,
	})
	if err != nil {
		t.Fatalf("materializeLeaf: %v", err)
	}
	if node.Unschedulable {
		t.Fatal("scheduling action must not mark leaf unschedulable")
	}
	if node.Task.ModelQPM != 120 {
		t.Errorf("Task.ModelQPM = %d, want 120", node.Task.ModelQPM)
	}
	if len(node.Task.EnableTimeRange) != 1 || node.Task.EnableTimeRange[0] != "09:00-18:00" {
		t.Errorf("Task.EnableTimeRange = %v, want [09:00-18:00]", node.Task.EnableTimeRange)
	}
	if len(node.Task.DisableTimeRange) != 1 || node.Task.DisableTimeRange[0] != "23:00-02:00" {
		t.Errorf("Task.DisableTimeRange = %v, want [23:00-02:00]", node.Task.DisableTimeRange)
	}
}

func TestMaterializeLeaf_RetriesWrittenToTask(t *testing.T) {
	leaf := model.PlanLeaf{
		ProviderName:  "openai",
		Provider:      config.ProviderConfig{Protocols: []string{"openai.chat"}},
		UpstreamModel: "gpt-4o",
	}
	retries := 2
	inboundCodec, err := codec.Get(codec.FormatOpenAIChat)
	if err != nil {
		t.Fatalf("get codec: %v", err)
	}
	rawReq := &dto.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []dto.Message{{Role: "user", Content: "hi"}},
	}
	node, err := materializeLeaf(context.Background(), leaf, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        rawReq,
		ModelGroup:    "test-group",
		ClientModel:   "gpt-4o",
		KeyName:       "key-1",
		Rules: []config.RuleConfig{{
			Match:  config.RuleMatch{UpstreamModel: &config.RuleCondition{Op: "equals", Value: "openai/gpt-4o"}},
			Action: config.RuleAction{Retries: &retries},
		}},
	})
	if err != nil {
		t.Fatalf("materializeLeaf: %v", err)
	}
	if node.Task.Retries != 2 {
		t.Errorf("Task.Retries = %d, want 2", node.Task.Retries)
	}
}
