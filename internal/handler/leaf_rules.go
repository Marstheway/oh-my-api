package handler

import (
	"encoding/json"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/rules"
)

// applyLeafRules 在请求副本上应用合并后的请求改写类 action
//（effort / thinking / temperature_mode / max_tokens）。调度类不在这里处理。
func applyLeafRules(rawReq any, merged rules.MergedAction) any {
	allowed, mode := resolveEffortForLeaf(merged)
	processed := applyReasoningEffortToRequest(rawReq, allowed, mode)
	if merged.Thinking == "off" {
		processed = applyThinkingOffToRequest(processed)
	}
	if merged.TemperatureMode == "strip" {
		processed = applyTemperatureStripToRequest(processed)
	}
	if merged.MaxTokens != nil {
		processed = applyMaxTokensFloorToRequest(processed, *merged.MaxTokens)
	}
	return processed
}

// applyTemperatureStripToRequest 剔除请求中的 temperature。
// Temperature 为 *float64，置 nil 后 omitempty 会让字段整体消失；
// 未知请求类型原样返回（passthrough，不 panic）。
func applyTemperatureStripToRequest(rawReq any) any {
	switch req := rawReq.(type) {
	case *dto.ChatCompletionRequest:
		if req.Temperature == nil {
			return req
		}
		clone := *req
		clone.Temperature = nil
		return &clone
	case *dto.ResponsesRequest:
		if req.Temperature == nil {
			return req
		}
		clone := *req
		clone.Temperature = nil
		return &clone
	case *dto.ClaudeRequest:
		if req.Temperature == nil {
			return req
		}
		clone := *req
		clone.Temperature = nil
		return &clone
	default:
		return rawReq
	}
}

// applyMaxTokensFloorToRequest 对请求的 max_tokens / max_completion_tokens / max_output_tokens
// 做下限（floor）钳制：请求未带或小于配置值时补/提到配置值，请求已带且大于配置值时保持不动。
// 未知请求类型原样返回（passthrough，不 panic）。
func applyMaxTokensFloorToRequest(rawReq any, floor int) any {
	switch req := rawReq.(type) {
	case *dto.ChatCompletionRequest:
		return applyMaxTokensFloorChat(req, floor)
	case *dto.ResponsesRequest:
		return applyMaxTokensFloorResponses(req, floor)
	case *dto.ClaudeRequest:
		return applyMaxTokensFloorClaude(req, floor)
	default:
		return rawReq
	}
}

// applyMaxTokensFloorChat 只 floor 客户端正在用的那一个字段：
// 带了 max_completion_tokens 就只动它；否则只写 max_tokens。
// 两个字段是同一份输出预算的互斥写法，禁止双写，以免出站 openai.chat 被上游 400，
// 或注入更小的 max_tokens 把已高于 floor 的 max_completion_tokens 压下去。
func applyMaxTokensFloorChat(req *dto.ChatCompletionRequest, floor int) any {
	if req.MaxCompletionTokens != nil {
		if *req.MaxCompletionTokens >= floor {
			return req
		}
		clone := *req
		n := floor
		clone.MaxCompletionTokens = &n
		return &clone
	}
	if req.MaxTokens >= floor {
		return req
	}
	clone := *req
	clone.MaxTokens = floor
	return &clone
}

func applyMaxTokensFloorResponses(req *dto.ResponsesRequest, floor int) any {
	if req.MaxOutputTokens >= floor {
		return req
	}
	clone := *req
	clone.MaxOutputTokens = floor
	return &clone
}

func applyMaxTokensFloorClaude(req *dto.ClaudeRequest, floor int) any {
	if req.MaxTokens >= floor {
		return req
	}
	clone := *req
	clone.MaxTokens = floor
	return &clone
}

func resolveEffortForLeaf(merged rules.MergedAction) (allowed []string, mode string) {
	if merged.EffortMode == "strip" {
		return nil, "strip"
	}
	if len(merged.Effort) > 0 {
		return merged.Effort, ""
	}
	return nil, ""
}

func applyThinkingOffToRequest(rawReq any) any {
	switch req := rawReq.(type) {
	case *dto.ChatCompletionRequest:
		return applyThinkingOffChat(req)
	case *dto.ResponsesRequest:
		return applyThinkingOffResponses(req)
	case *dto.ClaudeRequest:
		return applyThinkingOffClaude(req)
	default:
		return rawReq
	}
}

func applyThinkingOffChat(req *dto.ChatCompletionRequest) any {
	clone := *req
	clone.ReasoningEffort = "none"
	clone.Reasoning = nil
	clone.Thinking = json.RawMessage(`{"type":"disabled"}`)
	clone.Think = json.RawMessage(`false`)
	clone.THINKING = json.RawMessage(`{"type":"disabled"}`)
	clone.EnableThinking = json.RawMessage(`false`)
	return &clone
}

func applyThinkingOffResponses(req *dto.ResponsesRequest) any {
	clone := *req
	clone.EnableThinking = json.RawMessage(`false`)
	if clone.Reasoning == nil {
		clone.Reasoning = &dto.ResponsesReasoning{Effort: "none"}
		return &clone
	}
	rc := *clone.Reasoning
	rc.Effort = "none"
	clone.Reasoning = &rc
	return &clone
}

func applyThinkingOffClaude(req *dto.ClaudeRequest) any {
	clone := *req
	clone.Thinking = &dto.Thinking{Type: "disabled"}
	if len(clone.OutputConfig) == 0 {
		return &clone
	}
	var cfgMap map[string]interface{}
	if err := json.Unmarshal(clone.OutputConfig, &cfgMap); err != nil {
		return &clone
	}
	delete(cfgMap, "effort")
	if len(cfgMap) == 0 {
		clone.OutputConfig = nil
		return &clone
	}
	newConfig, err := json.Marshal(cfgMap)
	if err != nil {
		return &clone
	}
	clone.OutputConfig = newConfig
	return &clone
}

// applyReasoningEffortToRequest 根据配置对请求中的 effort 字段进行钳制或剔除。
// 返回处理后的请求对象（可能是原对象或克隆）。
// 未知请求类型原样返回（passthrough，不 panic）。
func applyReasoningEffortToRequest(rawReq any, allowed []string, mode string) any {
	if len(allowed) == 0 && mode != "strip" {
		return rawReq // 未配置，透传
	}

	switch req := rawReq.(type) {
	case *dto.ChatCompletionRequest:
		return applyToChatRequest(req, allowed, mode)
	case *dto.ResponsesRequest:
		return applyToResponsesRequest(req, allowed, mode)
	case *dto.ClaudeRequest:
		return applyToClaudeRequest(req, allowed, mode)
	default:
		// 未知类型，透传
		return rawReq
	}
}

// applyToChatRequest 处理 OpenAI Chat 请求的 reasoning_effort 字段
func applyToChatRequest(req *dto.ChatCompletionRequest, allowed []string, mode string) any {
	// 规范化 client 传入的值
	trimmed := strings.TrimSpace(req.ReasoningEffort)
	if trimmed == "" {
		return req // client 未传，不注入
	}

	if mode == "strip" {
		clone := *req
		clone.ReasoningEffort = ""
		return &clone
	}

	result, action := config.ApplyReasoningEffort(req.ReasoningEffort, allowed)

	// 根据动作决定是否 clone
	switch action {
	case config.ActionPassthrough:
		return req
	case config.ActionReplace:
		// clone 并替换
		clone := *req
		clone.ReasoningEffort = result
		return &clone
	default:
		return req
	}
}

// applyToResponsesRequest 处理 OpenAI Responses 请求的 reasoning.effort 字段
func applyToResponsesRequest(req *dto.ResponsesRequest, allowed []string, mode string) any {
	// 检查是否配置了 reasoning
	if req.Reasoning == nil {
		return req
	}

	// 规范化 client 传入的值
	trimmed := strings.TrimSpace(req.Reasoning.Effort)
	if trimmed == "" {
		return req // client 未传，不注入
	}

	if mode == "strip" {
		clone := *req
		rc := *req.Reasoning
		clone.Reasoning = &rc
		clone.Reasoning.Effort = ""
		if clone.Reasoning.Summary == "" {
			clone.Reasoning = nil
		}
		return &clone
	}

	result, action := config.ApplyReasoningEffort(req.Reasoning.Effort, allowed)

	// 根据动作决定是否 clone
	switch action {
	case config.ActionPassthrough:
		return req
	case config.ActionReplace:
		// clone Reasoning 结构
		clone := *req
		rc := *req.Reasoning
		clone.Reasoning = &rc
		clone.Reasoning.Effort = result
		return &clone
	default:
		return req
	}
}

// applyToClaudeRequest 处理 Anthropic 请求的 output_config.effort 字段
func applyToClaudeRequest(req *dto.ClaudeRequest, allowed []string, mode string) any {
	// 检查是否配置了 output_config
	if len(req.OutputConfig) == 0 {
		return req
	}

	// 解析 output_config JSON
	var cfgMap map[string]interface{}
	if err := json.Unmarshal(req.OutputConfig, &cfgMap); err != nil {
		// 解析失败，透传
		return req
	}

	// 检查是否有 effort 字段
	effortVal, hasEffort := cfgMap["effort"]
	if !hasEffort {
		return req // 无 effort 字段，透传
	}

	// 提取 effort 字符串
	effortStr, ok := effortVal.(string)
	if !ok {
		return req // 非字符串，透传
	}

	// 规范化
	trimmed := strings.TrimSpace(effortStr)
	if trimmed == "" {
		return req // 空值，透传
	}

	if mode == "strip" {
		delete(cfgMap, "effort")
		if len(cfgMap) == 0 {
			clone := *req
			clone.OutputConfig = nil
			return &clone
		}
		newConfig, err := json.Marshal(cfgMap)
		if err != nil {
			return req
		}
		clone := *req
		clone.OutputConfig = newConfig
		return &clone
	}

	result, action := config.ApplyReasoningEffort(effortStr, allowed)

	// 根据动作决定是否 clone
	switch action {
	case config.ActionPassthrough:
		return req
	case config.ActionReplace:
		// 更新 effort 值
		cfgMap["effort"] = result
		// 重新序列化
		newConfig, err := json.Marshal(cfgMap)
		if err != nil {
			return req // 序列化失败，透传
		}
		clone := *req
		clone.OutputConfig = newConfig
		return &clone
	default:
		return req
	}
}
