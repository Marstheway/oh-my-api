package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObserveTurnOpenAIChat(t *testing.T) {
	t.Run("no_tool_history", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model: "gpt-4",
			Messages: []dto.Message{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi"},
			},
		}

		obs := ObserveTurnOpenAIChat(req)
		if obs.OriginalModel != "gpt-4" {
			t.Errorf("original model = %q, want %q", obs.OriginalModel, "gpt-4")
		}
		if obs.HasRecentToolCall {
			t.Error("has recent tool call should be false")
		}
		if obs.LatestIsToolResult {
			t.Error("latest is tool result should be false")
		}
	})

	t.Run("with_tool_call", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model: "gpt-4",
			Messages: []dto.Message{
				{Role: "user", Content: "Search for Go tutorials"},
				{
					Role:    "assistant",
					Content: "",
					ToolCalls: []dto.ToolCall{{
						ID:   "tool-1",
						Type: "function",
						Function: dto.ToolCallFunc{
							Name:      "web_search",
							Arguments: `{"query":"Go tutorials"}`,
						},
					}},
				},
			},
		}

		obs := ObserveTurnOpenAIChat(req)
		if !obs.HasRecentToolCall {
			t.Error("has recent tool call should be true")
		}
		if len(obs.RecentToolNames) != 1 || obs.RecentToolNames[0] != "web_search" {
			t.Fatalf("recent tool names = %#v, want [web_search]", obs.RecentToolNames)
		}
	})

	t.Run("tool_result", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model: "gpt-4",
			Messages: []dto.Message{
				{Role: "user", Content: "Search for Go tutorials"},
				{Role: "assistant", Content: "", ToolCalls: []dto.ToolCall{{
					ID:       "tool-1",
					Type:     "function",
					Function: dto.ToolCallFunc{Name: "web_search", Arguments: `{}`},
				}}},
				{Role: "tool", ToolCallID: "tool-1", Content: "Search results..."},
			},
		}

		obs := ObserveTurnOpenAIChat(req)
		if !obs.LatestIsToolResult {
			t.Error("latest is tool result should be true")
		}
	})

	t.Run("tool_result_after_truncation_window", func(t *testing.T) {
		messages := []dto.Message{
			{Role: "user", Content: "m1"},
			{Role: "assistant", Content: "m2"},
			{Role: "user", Content: "m3"},
			{Role: "assistant", Content: "m4"},
			{Role: "user", Content: "m5"},
			{Role: "assistant", Content: "", ToolCalls: []dto.ToolCall{{
				ID:       "tool-1",
				Type:     "function",
				Function: dto.ToolCallFunc{Name: "web_search", Arguments: `{}`},
			}}},
			{Role: "tool", ToolCallID: "tool-1", Content: "search output"},
		}
		req := &dto.ChatCompletionRequest{Model: "gpt-4", Messages: messages}

		obs := ObserveTurnOpenAIChat(req)
		if !obs.LatestIsToolResult {
			t.Error("latest is tool result should stay true after truncation window")
		}
	})

	t.Run("compression", func(t *testing.T) {
		longText := string(make([]byte, 1000))
		req := &dto.ChatCompletionRequest{
			Model: "gpt-4",
			Messages: []dto.Message{
				{Role: "user", Content: longText},
			},
		}

		obs := ObserveTurnOpenAIChat(req)
		for _, msg := range obs.RecentMessages {
			if len(msg) > 600 { // "user: " + 500 chars
				t.Errorf("message not compressed: %d chars", len(msg))
			}
		}
	})

	t.Run("latest_material_input_without_tool_role", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model: "gpt-4",
			Messages: []dto.Message{
				{Role: "user", Content: "Search docs"},
				{Role: "assistant", Content: "", ToolCalls: []dto.ToolCall{{
					ID:   "tool-1",
					Type: "function",
					Function: dto.ToolCallFunc{
						Name:      "web_search",
						Arguments: `{"query":"Search docs"}`,
					},
				}}},
				{Role: "user", Content: "Search results:\nTitle: Go docs\nURL: https://go.dev\nSnippet: package docs"},
			},
		}

		obs := ObserveTurnOpenAIChat(req)
		if !obs.LatestHasMaterialInput {
			t.Error("latest has material input should be true")
		}
	})

	t.Run("code_material_sets_code_or_file_trace", func(t *testing.T) {
		req := &dto.ChatCompletionRequest{
			Model: "gpt-4",
			Messages: []dto.Message{
				{Role: "assistant", Content: "", ToolCalls: []dto.ToolCall{{
					ID:   "tool-1",
					Type: "function",
					Function: dto.ToolCallFunc{
						Name:      "read_file",
						Arguments: `{"path":"internal/handler/smart_route.go"}`,
					},
				}}},
				{Role: "tool", ToolCallID: "tool-1", Content: "path: internal/handler/smart_route.go\nline 247\nfunc ApplySmartRouteRules(obs *TurnObservation, aliasInfo *model.AliasSmartRouteInfo) *SmartRouteResult {"},
			},
		}

		obs := ObserveTurnOpenAIChat(req)
		if !obs.HasCodeOrFileTraces {
			t.Error("has code or file traces should be true")
		}
	})
}

func TestObserveTurnAnthropicMessages(t *testing.T) {
	t.Run("tool_use", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{
				"type": "tool_use",
				"id":   "tool-1",
				"name": "read_file",
				"input": map[string]interface{}{
					"path": "/src/main.go",
				},
			},
		}
		req := &dto.ClaudeRequest{
			Model:    "claude-3",
			Messages: []dto.ClaudeMessage{{Role: "assistant", Content: content}},
		}

		obs := ObserveTurnAnthropicMessages(req)
		if !obs.HasRecentToolCall {
			t.Error("has recent tool call should be true")
		}
		if len(obs.RecentToolNames) != 1 || obs.RecentToolNames[0] != "read_file" {
			t.Fatalf("recent tool names = %#v, want [read_file]", obs.RecentToolNames)
		}
	})

	t.Run("tool_result", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": "tool-1",
				"content":     "file contents...",
			},
		}
		req := &dto.ClaudeRequest{
			Model:    "claude-3",
			Messages: []dto.ClaudeMessage{{Role: "user", Content: content}},
		}

		obs := ObserveTurnAnthropicMessages(req)
		if !obs.LatestIsToolResult {
			t.Error("latest is tool result should be true")
		}
	})

	t.Run("latest_material_input", func(t *testing.T) {
		content := []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": "Search results\nTitle: Go docs\nURL: https://go.dev\nSnippet: package docs",
			},
		}
		req := &dto.ClaudeRequest{
			Model: "claude-3",
			Messages: []dto.ClaudeMessage{
				{Role: "assistant", Content: []interface{}{map[string]interface{}{"type": "tool_use", "id": "tool-1", "name": "web_search", "input": map[string]interface{}{"query": "go docs"}}}},
				{Role: "user", Content: content},
			},
		}

		obs := ObserveTurnAnthropicMessages(req)
		if !obs.LatestHasMaterialInput {
			t.Error("latest has material input should be true")
		}
	})
}

func TestObserveTurnOpenAIResponse(t *testing.T) {
	t.Run("function_call", func(t *testing.T) {
		input, _ := json.Marshal([]dto.ResponsesInputItem{
			{Type: "message", Role: "user", Content: json.RawMessage(`"Hello"`)},
			{Type: "function_call", Name: "get_weather", Arguments: `{"city":"Beijing"}`},
		})
		req := &dto.ResponsesRequest{
			Model: "gpt-4",
			Input: input,
		}

		obs := ObserveTurnOpenAIResponse(req)
		if !obs.HasRecentToolCall {
			t.Error("has recent tool call should be true")
		}
		if len(obs.RecentToolNames) != 1 || obs.RecentToolNames[0] != "get_weather" {
			t.Fatalf("recent tool names = %#v, want [get_weather]", obs.RecentToolNames)
		}
	})

	t.Run("function_call_output", func(t *testing.T) {
		input, _ := json.Marshal([]dto.ResponsesInputItem{
			{Type: "message", Role: "user", Content: json.RawMessage(`"Hello"`)},
			{Type: "function_call", Name: "get_weather", Arguments: `{}`},
			{Type: "function_call_output", Output: json.RawMessage(`"Sunny"`)},
		})
		req := &dto.ResponsesRequest{
			Model: "gpt-4",
			Input: input,
		}

		obs := ObserveTurnOpenAIResponse(req)
		if !obs.LatestIsToolResult {
			t.Error("latest is tool result should be true")
		}
	})

	t.Run("latest_material_input_message", func(t *testing.T) {
		input, _ := json.Marshal([]dto.ResponsesInputItem{
			{Type: "function_call", Name: "web_search", Arguments: `{}`},
			{Type: "message", Role: "user", Content: json.RawMessage(`[{"type":"input_text","text":"Search results\\nTitle: Go docs\\nURL: https://go.dev\\nSnippet: package docs"}]`)},
		})
		req := &dto.ResponsesRequest{
			Model: "gpt-4",
			Input: input,
		}

		obs := ObserveTurnOpenAIResponse(req)
		if !obs.LatestHasMaterialInput {
			t.Error("latest has material input should be true")
		}
	})
}

func TestApplySmartRouteRules(t *testing.T) {
	reasonGroup := "reason-backend"

	t.Run("disabled", func(t *testing.T) {
		obs := &TurnObservation{OriginalModel: "my-model"}
		result := ApplySmartRouteRules(obs, nil)
		if result.Decision != DecisionReason {
			t.Errorf("decision = %q, want %q", result.Decision, DecisionReason)
		}
		if result.DecisionPath != "disabled" {
			t.Errorf("decision path = %q, want %q", result.DecisionPath, "disabled")
		}
	})

	t.Run("no_tool_history", func(t *testing.T) {
		obs := &TurnObservation{
			OriginalModel:     "my-alias",
			HasRecentToolCall: false,
		}
		aliasInfo := &model.AliasSmartRouteInfo{
			DefaultGroup: reasonGroup,
		}

		result := ApplySmartRouteRules(obs, aliasInfo)
		if result.Decision != DecisionReason {
			t.Errorf("decision = %q, want %q", result.Decision, DecisionReason)
		}
		if result.DecisionPath != "rule_reason" {
			t.Errorf("decision path = %q, want %q", result.DecisionPath, "rule_reason")
		}
	})

	t.Run("execution_signals", func(t *testing.T) {
		obs := &TurnObservation{
			OriginalModel:     "my-alias",
			HasRecentToolCall: true,
			ExecutionSignals:  []string{"write", "implement"},
		}
		aliasInfo := &model.AliasSmartRouteInfo{
			DefaultGroup: reasonGroup,
		}

		result := ApplySmartRouteRules(obs, aliasInfo)
		if result.Decision != DecisionReason {
			t.Errorf("decision = %q, want %q", result.Decision, DecisionReason)
		}
	})

	t.Run("rule_scout", func(t *testing.T) {
		obs := &TurnObservation{
			OriginalModel:         "my-alias",
			HasRecentToolCall:     true,
			LatestIsToolResult:    true,
			HasMaterialTraces:     true,
			ContinueSearchSignals: []string{"search", "find"},
		}
		aliasInfo := &model.AliasSmartRouteInfo{
			DefaultGroup: reasonGroup,
		}

		result := ApplySmartRouteRules(obs, aliasInfo)
		if result.Decision != DecisionScout {
			t.Errorf("decision = %q, want %q", result.Decision, DecisionScout)
		}
	})

	t.Run("code_gathering_without_search_keyword_goes_need_judge", func(t *testing.T) {
		obs := &TurnObservation{
			OriginalModel:       "my-alias",
			RecentToolNames:     []string{"grep", "read_file"},
			HasRecentToolCall:   true,
			LatestIsToolResult:  true,
			HasMaterialTraces:   true,
			HasCodeOrFileTraces: true,
		}
		aliasInfo := &model.AliasSmartRouteInfo{
			DefaultGroup: reasonGroup,
		}

		result := ApplySmartRouteRules(obs, aliasInfo)
		if result.Decision != DecisionNeedJudge {
			t.Errorf("decision = %q, want %q", result.Decision, DecisionNeedJudge)
		}
		if result.DecisionPath != "need_judge" {
			t.Errorf("decision path = %q, want %q", result.DecisionPath, "need_judge")
		}
	})

	t.Run("rule_scout_for_web_extract_material_without_search_keyword", func(t *testing.T) {
		obs := &TurnObservation{
			OriginalModel:      "my-alias",
			RecentToolNames:    []string{"web_extract"},
			HasRecentToolCall:  true,
			LatestIsToolResult: true,
			HasMaterialTraces:  true,
			RecentMessages:     []string{"tool: Title: Go docs URL: https://go.dev Snippet: package docs"},
		}
		aliasInfo := &model.AliasSmartRouteInfo{
			DefaultGroup: reasonGroup,
		}

		result := ApplySmartRouteRules(obs, aliasInfo)
		if result.Decision != DecisionScout {
			t.Errorf("decision = %q, want %q", result.Decision, DecisionScout)
		}
	})

	t.Run("rule_scout_for_agent_fetch_material_without_search_keyword", func(t *testing.T) {
		obs := &TurnObservation{
			OriginalModel:      "my-alias",
			RecentToolNames:    []string{"agent-fetch"},
			HasRecentToolCall:  true,
			LatestIsToolResult: true,
			HasMaterialTraces:  true,
			RecentMessages:     []string{"tool: url: https://example.com title: docs snippet: api behavior"},
		}
		aliasInfo := &model.AliasSmartRouteInfo{
			DefaultGroup: reasonGroup,
		}

		result := ApplySmartRouteRules(obs, aliasInfo)
		if result.Decision != DecisionScout {
			t.Errorf("decision = %q, want %q", result.Decision, DecisionScout)
		}
	})

	t.Run("rule_scout_on_material_fallback", func(t *testing.T) {
		obs := &TurnObservation{
			OriginalModel:          "my-alias",
			HasRecentToolCall:      true,
			LatestHasMaterialInput: true,
			HasMaterialTraces:      true,
			ContinueSearchSignals:  []string{"search", "find"},
		}
		aliasInfo := &model.AliasSmartRouteInfo{
			DefaultGroup: reasonGroup,
		}

		result := ApplySmartRouteRules(obs, aliasInfo)
		if result.Decision != DecisionScout {
			t.Errorf("decision = %q, want %q", result.Decision, DecisionScout)
		}
	})

	t.Run("need_judge", func(t *testing.T) {
		obs := &TurnObservation{
			OriginalModel:         "my-alias",
			HasRecentToolCall:     true,
			LatestIsToolResult:    true,
			HasMaterialTraces:     true,
			ContinueSearchSignals: []string{"search"},
			SynthesisSignals:      []string{"based on"},
		}
		aliasInfo := &model.AliasSmartRouteInfo{
			DefaultGroup: reasonGroup,
		}

		result := ApplySmartRouteRules(obs, aliasInfo)
		if result.Decision != DecisionNeedJudge {
			t.Errorf("decision = %q, want %q", result.Decision, DecisionNeedJudge)
		}
	})

	t.Run("tool_result_with_material_but_no_ambiguity_stays_reason", func(t *testing.T) {
		obs := &TurnObservation{
			OriginalModel:      "my-alias",
			HasRecentToolCall:  true,
			LatestIsToolResult: true,
			HasMaterialTraces:  true,
		}
		aliasInfo := &model.AliasSmartRouteInfo{
			DefaultGroup: reasonGroup,
		}

		result := ApplySmartRouteRules(obs, aliasInfo)
		if result.Decision != DecisionReason {
			t.Errorf("decision = %q, want %q", result.Decision, DecisionReason)
		}
		if result.DecisionPath != "rule_reason" {
			t.Errorf("decision path = %q, want %q", result.DecisionPath, "rule_reason")
		}
	})

	t.Run("continue_search_with_synthesis_goes_need_judge_not_scout", func(t *testing.T) {
		obs := &TurnObservation{
			OriginalModel:         "my-alias",
			HasRecentToolCall:     true,
			LatestIsToolResult:    true,
			HasMaterialTraces:     true,
			ContinueSearchSignals: []string{"search"},
			SynthesisSignals:      []string{"based on"},
		}
		aliasInfo := &model.AliasSmartRouteInfo{
			DefaultGroup: reasonGroup,
		}

		result := ApplySmartRouteRules(obs, aliasInfo)
		if result.Decision != DecisionNeedJudge {
			t.Errorf("decision = %q, want %q", result.Decision, DecisionNeedJudge)
		}
	})

	t.Run("default_reason", func(t *testing.T) {
		obs := &TurnObservation{
			OriginalModel:     "my-alias",
			HasRecentToolCall: true,
		}
		aliasInfo := &model.AliasSmartRouteInfo{
			DefaultGroup: reasonGroup,
		}

		result := ApplySmartRouteRules(obs, aliasInfo)
		if result.Decision != DecisionReason {
			t.Errorf("decision = %q, want %q", result.Decision, DecisionReason)
		}
	})
}

func TestParseJudgeResult(t *testing.T) {
	tests := []struct {
		input    string
		expected SmartRouteDecision
		invalid  bool
	}{
		{"SCOUT", DecisionScout, false},
		{"REASON", DecisionReason, false},
		{"scout", DecisionScout, false},
		{"reason", DecisionReason, false},
		{"  SCOUT  ", DecisionScout, false},
		{"invalid", DecisionReason, true},
		{"", DecisionReason, true},
		{"SCOUTS", DecisionReason, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, invalid := parseJudgeResult(tt.input)
			if result != tt.expected {
				t.Errorf("parseJudgeResult(%q) = %q, want %q", tt.input, result, tt.expected)
			}
			if invalid != tt.invalid {
				t.Errorf("parseJudgeResult(%q) invalid = %v, want %v", tt.input, invalid, tt.invalid)
			}
		})
	}
}

func TestBuildJudgePromptIncludesAmbiguousSignals(t *testing.T) {
	obs := &TurnObservation{
		RecentMessages:         []string{"tool: search result"},
		HasRecentToolCall:      true,
		LatestIsToolResult:     true,
		LatestHasMaterialInput: true,
		HasMaterialTraces:      true,
		ContinueSearchSignals:  []string{"search", "find"},
		SynthesisSignals:       []string{"based on", "what did you find"},
	}

	prompt := buildJudgePrompt(obs)
	if !strings.Contains(prompt, "Continue gathering signals: search, find") {
		t.Fatalf("prompt missing continue gathering signals: %q", prompt)
	}
	if !strings.Contains(prompt, "Synthesis signals: based on, what did you find") {
		t.Fatalf("prompt missing synthesis signals: %q", prompt)
	}
	if !strings.Contains(prompt, "Latest has material input: true") {
		t.Fatalf("prompt missing latest material input flag: %q", prompt)
	}
}

func TestSmartRouteMetrics(t *testing.T) {
	// 重置 metrics
	metrics.ResetForTest()

	// 记录几个决策
	metrics.RecordSmartRouteDecision("rule_reason")
	metrics.RecordSmartRouteDecision("rule_reason")
	metrics.RecordSmartRouteDecision("rule_scout")
	metrics.RecordSmartRouteDecision("judge_reason")

	// 验证计数
	counter := metrics.GetSmartRouteDecisionTotal()
	if counter == nil {
		t.Fatal("smart route counter is nil")
	}

	reasonCount := testutil.ToFloat64(counter.WithLabelValues("rule_reason"))
	if reasonCount != 2 {
		t.Errorf("rule_reason count = %f, want 2", reasonCount)
	}

	scoutCount := testutil.ToFloat64(counter.WithLabelValues("rule_scout"))
	if scoutCount != 1 {
		t.Errorf("rule_scout count = %f, want 1", scoutCount)
	}
}

func TestSmartRouteIntegration(t *testing.T) {
	// 这个测试验证 smart route 的完整集成流程
	// 需要在 handler 测试环境中进行
	t.Run("disabled_alias", func(t *testing.T) {
		// 未启用 smart route 的 alias 应该返回原模型
		obs := &TurnObservation{OriginalModel: "normal-alias"}
		result := SmartRoute(context.Background(), "normal-alias", obs, nil)

		if result.DecisionPath != "disabled" {
			t.Errorf("decision path = %q, want %q", result.DecisionPath, "disabled")
		}
	})
}

func TestDetectExecutionSignals(t *testing.T) {
	tests := []struct {
		text     string
		expected int
	}{
		{"Please write a function to sort array", 1},
		{"Implement the algorithm", 1},
		{"Fix the bug in main.go", 1},
		{"This is the final answer", 1}, // "final" 是执行型信号
		{"Just looking for information", 0},
		{"", 0},
	}

	for _, tt := range tests {
		signals := detectExecutionSignals(tt.text)
		if len(signals) < tt.expected {
			t.Errorf("detectExecutionSignals(%q) = %d signals, want at least %d", tt.text, len(signals), tt.expected)
		}
	}
}

func TestDetectContinueSearchSignals(t *testing.T) {
	tests := []struct {
		text     string
		expected int
	}{
		{"Please search for more information", 1},
		{"Find the documentation", 1},
		{"Check the logs", 1},
		{"This is the final answer", 0},
		{"", 0},
	}

	for _, tt := range tests {
		signals := detectContinueSearchSignals(tt.text)
		if len(signals) != tt.expected {
			t.Errorf("detectContinueSearchSignals(%q) = %d signals, want %d", tt.text, len(signals), tt.expected)
		}
	}
}

func TestContainsMaterialKeywords(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"Search results\nTitle: Go docs\nURL: https://go.dev\nSnippet: package docs", true},
		{"搜索结果：网页内容和代码片段", true},
		{"plain short text without any material markers", false},
	}

	for _, tt := range tests {
		got := containsMaterialKeywords(tt.text)
		if got != tt.want {
			t.Errorf("containsMaterialKeywords(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestDetectSynthesisSignals(t *testing.T) {
	tests := []struct {
		text     string
		expected int
	}{
		{"Based on these search results, explain this issue", 2},
		{"According to this page, what did you find", 2},
		{"Please continue searching", 0},
		{"", 0},
	}

	for _, tt := range tests {
		signals := detectSynthesisSignals(tt.text)
		if len(signals) < tt.expected {
			t.Errorf("detectSynthesisSignals(%q) = %d signals, want at least %d", tt.text, len(signals), tt.expected)
		}
	}
}
