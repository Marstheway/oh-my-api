package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/model"
)

// SmartRouteDecision 表示 smart route 的决策结果。
type SmartRouteDecision string

const (
	DecisionReason      SmartRouteDecision = "reason"       // 走推理模型
	DecisionScout       SmartRouteDecision = "scout"        // 走探路模型
	DecisionNeedJudge   SmartRouteDecision = "need_judge"   // 需要 cheap judge 判定
	DecisionJudgeScout  SmartRouteDecision = "judge_scout"  // judge 判定为 scout
	DecisionJudgeReason SmartRouteDecision = "judge_reason" // judge 判定为 reason
)

// TurnObservation 表示从请求中抽取的 turn 观察结果。
type TurnObservation struct {
	OriginalModel          string   // 原始请求的模型名
	RecentMessages         []string // 最近几轮的消息摘要（压缩后）
	RecentToolNames        []string // 近期 assistant 发起的 tool 名称
	HasRecentToolCall      bool     // 是否存在近期的 assistant tool call 历史
	LatestIsToolResult     bool     // 最新输入是否为 tool result
	LatestHasMaterialInput bool     // 最新输入是否为资料回灌（网页/搜索结果/文件/源码等）
	HasMaterialTraces      bool     // 是否出现网页/文件/源码相关资料痕迹
	HasCodeOrFileTraces    bool     // 是否出现源码、文件路径、grep 输出等代码资料痕迹
	ExecutionSignals       []string // 命中的执行型信号关键词
	ContinueSearchSignals  []string // 命中的继续搜集信息信号关键词
	SynthesisSignals       []string // 命中的开始整合结论信号关键词
}

// SmartRouteResult 表示 smart route 判定的完整结果。
type SmartRouteResult struct {
	Decision       SmartRouteDecision // 最终决策
	EffectiveModel string             // 有效模型名（用于 resolver.Resolve）
	DecisionPath   string             // 决策路径：rule_reason/rule_scout/need_judge/judge_reason/judge_scout/judge_fallback_to_reason
	FallbackReason string             // 回退原因（仅当决策来自 fallback 时）
}

// ObserveTurnOpenAIChat 从 OpenAI Chat 请求中抽取 turn 观察。
func ObserveTurnOpenAIChat(req *dto.ChatCompletionRequest) *TurnObservation {
	obs := &TurnObservation{
		OriginalModel: req.Model,
	}

	// 固定压缩预算：最多看最近 6 条消息单元
	// 注意：创建副本进行观察，不修改原始请求
	maxMessages := 6
	messages := req.Messages
	if len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}

	for i, msg := range messages {
		// 检测 assistant tool call 历史
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			obs.HasRecentToolCall = true
			for _, tc := range msg.ToolCalls {
				if tc.Function.Name != "" {
					obs.RecentToolNames = append(obs.RecentToolNames, strings.ToLower(tc.Function.Name))
				}
			}
		}

		// 检测 tool result
		if msg.Role == "tool" {
			if i == len(messages)-1 {
				obs.LatestIsToolResult = true
			}
		}

		// 提取文本内容并应用压缩预算
		// 规则：包含材料痕迹的内容最多800字符；其他文本500字符
		text := extractTextFromContent(msg.Content)
		isMaterial := detectMaterialTraces(text) // 只检测内容，不根据角色判断
		if isMaterial && i == len(messages)-1 {
			obs.LatestHasMaterialInput = true
		}
		maxLen := 500
		if isMaterial {
			maxLen = 800
		}
		if len(text) > maxLen {
			text = text[:maxLen]
		}
		obs.RecentMessages = append(obs.RecentMessages, fmt.Sprintf("%s: %s", msg.Role, text))

		// 检测资料痕迹（网页正文、文件内容、源码片段）
		// 注意：只有真正包含材料痕迹的内容才标记，普通的 tool result 不算
		if isMaterial {
			obs.HasMaterialTraces = true
			if containsCodeOrFileKeywords(text) {
				obs.HasCodeOrFileTraces = true
			}
		}

		// 检测执行型信号
		obs.ExecutionSignals = append(obs.ExecutionSignals, detectExecutionSignals(text)...)

		// 检测继续搜集信息信号
		obs.ContinueSearchSignals = append(obs.ContinueSearchSignals, detectContinueSearchSignals(text)...)

		// 检测开始整合结论信号
		obs.SynthesisSignals = append(obs.SynthesisSignals, detectSynthesisSignals(text)...)
	}

	return obs
}

// ObserveTurnAnthropicMessages 从 Anthropic Messages 请求中抽取 turn 观察。
func ObserveTurnAnthropicMessages(req *dto.ClaudeRequest) *TurnObservation {
	obs := &TurnObservation{
		OriginalModel: req.Model,
	}

	// 创建副本进行观察，不修改原始请求
	maxMessages := 6
	messages := req.Messages
	if len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}

	for i, msg := range messages {
		content := extractClaudeContent(msg.Content)

		// 检测 tool_use 和 tool_result
		if hasToolUse(msg.Content) {
			obs.HasRecentToolCall = true
			obs.RecentToolNames = append(obs.RecentToolNames, extractClaudeToolNames(msg.Content)...)
		}
		if isToolResult(msg.Content) && i == len(messages)-1 {
			obs.LatestIsToolResult = true
		}

		// 压缩文本并应用预算
		// 规则：包含材料痕迹的内容最多800字符；其他文本500字符
		text := content
		isMaterial := hasClaudeMaterialTraces(msg.Content) // 只检测内容，不根据类型判断
		if isMaterial && i == len(messages)-1 {
			obs.LatestHasMaterialInput = true
		}
		maxLen := 500
		if isMaterial {
			maxLen = 800
		}
		if len(text) > maxLen {
			text = text[:maxLen]
		}
		obs.RecentMessages = append(obs.RecentMessages, fmt.Sprintf("%s: %s", msg.Role, text))

		// 检测资料痕迹（网页正文、文件内容、源码片段）
		// 注意：只有真正包含材料痕迹的内容才标记，普通的 tool_result 不算
		if isMaterial {
			obs.HasMaterialTraces = true
			if containsCodeOrFileKeywords(text) {
				obs.HasCodeOrFileTraces = true
			}
		}

		obs.ExecutionSignals = append(obs.ExecutionSignals, detectExecutionSignals(text)...)
		obs.ContinueSearchSignals = append(obs.ContinueSearchSignals, detectContinueSearchSignals(text)...)
		obs.SynthesisSignals = append(obs.SynthesisSignals, detectSynthesisSignals(text)...)
	}

	return obs
}

// ObserveTurnOpenAIResponse 从 OpenAI Response 请求中抽取 turn 观察。
func ObserveTurnOpenAIResponse(req *dto.ResponsesRequest) *TurnObservation {
	obs := &TurnObservation{
		OriginalModel: req.Model,
	}

	// 解析 input
	if len(req.Input) == 0 {
		return obs
	}

	var items []dto.ResponsesInputItem
	if err := json.Unmarshal(req.Input, &items); err != nil {
		// input 可能是字符串
		var inputStr string
		if err := json.Unmarshal(req.Input, &inputStr); err == nil {
			obs.RecentMessages = append(obs.RecentMessages, fmt.Sprintf("user: %s", truncateText(inputStr, 500)))
		}
		return obs
	}

	maxItems := 6
	if len(items) > maxItems {
		items = items[len(items)-maxItems:]
	}

	for i, item := range items {
		// 检测 function_call（相当于 tool call）
		if item.Type == "function_call" {
			obs.HasRecentToolCall = true
			if item.Name != "" {
				obs.RecentToolNames = append(obs.RecentToolNames, strings.ToLower(item.Name))
			}
		}

		// 检测 function_call_output（相当于 tool result）
		if item.Type == "function_call_output" && i == len(items)-1 {
			obs.LatestIsToolResult = true
		}

		// 提取内容并应用压缩预算
		// 规则：包含材料痕迹的内容最多800字符；其他文本500字符
		var text string
		if item.Type == "message" {
			text = extractResponsesContent(item.Content)
		} else if item.Type == "function_call_output" {
			// Output 字段是 json.RawMessage，需要解出字符串
			if len(item.Output) > 0 {
				var s string
				if err := json.Unmarshal(item.Output, &s); err == nil {
					text = s
				} else {
					text = string(item.Output)
				}
			}
		}
		isMaterial := containsMaterialKeywords(text) // 只检测内容，不根据类型判断
		if isMaterial && i == len(items)-1 {
			obs.LatestHasMaterialInput = true
		}
		maxLen := 500
		if isMaterial {
			maxLen = 800
		}
		if len(text) > maxLen {
			text = text[:maxLen]
		}
		obs.RecentMessages = append(obs.RecentMessages, fmt.Sprintf("%s: %s", item.Type, text))

		// 检测资料痕迹（网页正文、文件内容、源码片段）
		// 注意：只有真正包含材料痕迹的内容才标记，普通的 function_call_output 不算
		if isMaterial {
			obs.HasMaterialTraces = true
			if containsCodeOrFileKeywords(text) {
				obs.HasCodeOrFileTraces = true
			}
		}

		obs.ExecutionSignals = append(obs.ExecutionSignals, detectExecutionSignals(text)...)
		obs.ContinueSearchSignals = append(obs.ContinueSearchSignals, detectContinueSearchSignals(text)...)
		obs.SynthesisSignals = append(obs.SynthesisSignals, detectSynthesisSignals(text)...)
	}

	return obs
}

// ApplySmartRouteRules 应用规则判定，返回决策结果。
// 规则判定顺序固定：
// 1. 若 alias 未启用 smart route，直接返回原模型
// 2. 若无工具链历史，直接走 reason
// 3. 若命中明显执行型信号，直接走 reason
// 4. 若命中少数高把握的"工具中间续轮且更像继续搜集信息"规则，直接走 scout
// 5. 只有命中特定模糊场景时才返回 need_judge
// 6. 其余情况全部走 reason
func ApplySmartRouteRules(obs *TurnObservation, aliasInfo *model.AliasSmartRouteInfo) *SmartRouteResult {
	// 规则 1：未启用 smart route
	if aliasInfo == nil {
		return &SmartRouteResult{
			Decision:       DecisionReason,
			EffectiveModel: obs.OriginalModel,
			DecisionPath:   "disabled",
		}
	}

	// 规则 2：无工具链历史
	if !obs.HasRecentToolCall {
		return &SmartRouteResult{
			Decision:       DecisionReason,
			EffectiveModel: aliasInfo.DefaultGroup,
			DecisionPath:   "rule_reason",
		}
	}

	// 规则 3：命中执行型信号
	// 明显的最终整合、写代码、修改实现、调试、长文输出等
	if len(obs.ExecutionSignals) > 0 {
		return &SmartRouteResult{
			Decision:       DecisionReason,
			EffectiveModel: aliasInfo.DefaultGroup,
			DecisionPath:   "rule_reason",
		}
	}

	// 规则 4：工具中间续轮且更像继续搜集信息
	// 仅对高把握的继续搜索场景直接走 scout；代码/资料探索轮降级到 need_judge
	if shouldRouteScoutByRule(obs) {
		return &SmartRouteResult{
			Decision:       DecisionScout,
			EffectiveModel: "", // 实际的 scout group 由 SmartRoute 主函数决定
			DecisionPath:   "rule_scout",
		}
	}

	// 规则 5：特定模糊场景
	// 包括“继续搜集信息”和“开始整合结论”混合，或代码/资料探索型中间续轮
	if shouldNeedJudge(obs) {
		return &SmartRouteResult{
			Decision:       DecisionNeedJudge,
			EffectiveModel: "",
			DecisionPath:   "need_judge",
		}
	}

	// 规则 6：默认走 reason
	return &SmartRouteResult{
		Decision:       DecisionReason,
		EffectiveModel: aliasInfo.DefaultGroup,
		DecisionPath:   "rule_reason",
	}
}

// JudgeResult 表示 cheap judge 的判定结果，包含决策和回退原因
type JudgeResult struct {
	Decision        SmartRouteDecision
	FallbackReason  string // 仅当 cheap judge 请求失败时有值，如 "judge_timeout", "judge_parse_failed", "judge_upstream_error"
	InvalidDecision bool   // judge 请求成功返回，但输出不符合协议，需按回退处理
}

// ExecuteCheapJudge 执行 cheap judge 判定。
// 输入为压缩后的对话摘要，输出为判定结果。
// 任何异常都回退到 REASON，并在 FallbackReason 中说明原因。
func ExecuteCheapJudge(ctx context.Context, obs *TurnObservation, sri *model.SmartRouteIndex) *JudgeResult {
	// 构造最小判别请求
	judgePrompt := buildJudgePrompt(obs)

	// 构造请求对象（使用 OpenAI Chat 格式）
	judgeReq := &dto.ChatCompletionRequest{
		Model: sri.CheapGroup,
		Messages: []dto.Message{
			{
				Role:    "system",
				Content: "You are a classifier. Determine if this turn should use SCOUT (continue gathering information) or REASON (provide final answer). Reply ONLY with 'SCOUT' or 'REASON'. If uncertain, always reply 'REASON'.",
			},
			{
				Role:    "user",
				Content: judgePrompt,
			},
		},
		MaxTokens: 10,
		Stream:    false,
	}

	// 使用独立的短超时（7 秒）
	judgeCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()

	// 内部非递归执行：构造最小请求对象，走 resolver -> materialize -> scheduler
	result, err := executeCheapJudgeInternal(judgeCtx, judgeReq, sri.CheapGroup)
	if err != nil {
		slog.Warn("cheap judge failed, fallback to REASON",
			"error", err,
			"original_model", obs.OriginalModel,
		)
		// 根据错误类型设置回退原因
		reason := "judge_upstream_error"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "judge_timeout"
		} else if strings.Contains(err.Error(), "decode response") {
			reason = "judge_parse_failed"
		} else if strings.Contains(err.Error(), "empty response") {
			reason = "judge_empty_response"
		}
		return &JudgeResult{
			Decision:       DecisionReason,
			FallbackReason: reason,
		}
	}

	// 解析结果
	decision, invalid := parseJudgeResult(result)
	return &JudgeResult{
		Decision:        decision,
		FallbackReason:  "", // 成功时无回退原因
		InvalidDecision: invalid,
	}
}

// buildJudgePrompt 构造 judge 提示词
func buildJudgePrompt(obs *TurnObservation) string {
	var sb strings.Builder
	sb.WriteString("Recent conversation:\n")
	for _, msg := range obs.RecentMessages {
		sb.WriteString(msg)
		sb.WriteString("\n")
	}
	if len(obs.ContinueSearchSignals) > 0 {
		sb.WriteString("\nContinue gathering signals: ")
		sb.WriteString(strings.Join(obs.ContinueSearchSignals, ", "))
	}
	if len(obs.SynthesisSignals) > 0 {
		sb.WriteString("\nSynthesis signals: ")
		sb.WriteString(strings.Join(obs.SynthesisSignals, ", "))
	}
	sb.WriteString("\nHas recent tool calls: ")
	sb.WriteString(fmt.Sprintf("%v", obs.HasRecentToolCall))
	sb.WriteString("\nLatest is tool result: ")
	sb.WriteString(fmt.Sprintf("%v", obs.LatestIsToolResult))
	sb.WriteString("\nLatest has material input: ")
	sb.WriteString(fmt.Sprintf("%v", obs.LatestHasMaterialInput))
	sb.WriteString("\nHas material traces: ")
	sb.WriteString(fmt.Sprintf("%v", obs.HasMaterialTraces))
	sb.WriteString("\n\nShould this turn use SCOUT (continue gathering) or REASON (final answer)?")
	return sb.String()
}

// executeCheapJudgeInternal 内部执行 cheap judge
func executeCheapJudgeInternal(ctx context.Context, req *dto.ChatCompletionRequest, cheapGroup string) (string, error) {
	// 1. 直接按 group 构建执行计划，避免 internal 的内部专用 group 被 Resolve 拒绝（外部直调规则不适用于内部流程）。
	resolveResult, err := resolveInternalGroup(cheapGroup)
	if err != nil {
		return "", fmt.Errorf("resolve cheap group: %w", err)
	}

	// 2. materialize
	inboundCodec, err := codec.Get(codec.FormatOpenAIChat)
	if err != nil {
		return "", fmt.Errorf("get codec: %w", err)
	}

	// Internal judge/scout traffic must not evaluate user-facing rules
	// (client-model/key/upstream actions would otherwise leak onto operator traffic).
	rootNode, matErr := materializePlan(ctx, resolveResult.Plan, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        req,
		ModelGroup:    resolveResult.ModelGroup,
		ClientModel:   req.Model,
		KeyName:       "",
		Rules:         nil,
	})
	if matErr != nil {
		return "", fmt.Errorf("materialize: %w", matErr)
	}

	// 3. execute
	resp, schedErr := sched.ExecuteNode(ctx, rootNode)
	if schedErr != nil {
		return "", fmt.Errorf("execute: %w", schedErr)
	}
	defer resp.Response.Body.Close()

	if resp.Response.StatusCode >= 400 {
		return "", fmt.Errorf("upstream status %d", resp.Response.StatusCode)
	}

	// 4. 读取响应
	var chatResp dto.ChatCompletionResponse
	if err := json.NewDecoder(resp.Response.Body).Decode(&chatResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message == nil {
		return "", fmt.Errorf("empty response")
	}

	return chatResp.Choices[0].Message.Content, nil
}

func resolveInternalGroup(groupName string) (*model.ResolveResult, error) {
	if resolver == nil {
		return nil, model.ErrModelNotFound
	}
	return resolver.ResolveInternal(groupName)
}

// parseJudgeResult 解析 judge 结果
func parseJudgeResult(result string) (SmartRouteDecision, bool) {
	result = strings.TrimSpace(strings.ToUpper(result))
	if result == "SCOUT" {
		return DecisionScout, false
	}
	if result == "REASON" {
		return DecisionReason, false
	}
	// 非法或空输出按 REASON 回退，但要保留原因以便观测
	return DecisionReason, true
}

// SmartRoute 主入口：对三条 handler 链路提供统一的 smart route 判定
func SmartRoute(ctx context.Context, originalModel string, obs *TurnObservation, sri *model.SmartRouteIndex) *SmartRouteResult {
	// 1. 检查是否启用
	if sri == nil || !sri.Enabled {
		result := &SmartRouteResult{
			Decision:       DecisionReason,
			EffectiveModel: originalModel,
			DecisionPath:   "disabled",
		}
		metrics.RecordSmartRouteDecision(result.DecisionPath)
		return result
	}

	// 2. 检查原始模型是否在 enabled_models 中
	aliasInfo := resolver.GetAliasSmartRouteInfo(originalModel)
	if aliasInfo == nil {
		// 未启用 smart route 的入口模型，走原模型
		result := &SmartRouteResult{
			Decision:       DecisionReason,
			EffectiveModel: originalModel,
			DecisionPath:   "disabled",
		}
		metrics.RecordSmartRouteDecision(result.DecisionPath)
		return result
	}

	// 3. 应用规则判定
	result := ApplySmartRouteRules(obs, aliasInfo)

	// 4. 如果决策是 scout，设置 scout group
	if result.Decision == DecisionScout {
		result.EffectiveModel = sri.ScoutGroup
	}

	// 5. 如果需要 judge，执行 cheap judge
	if result.Decision == DecisionNeedJudge {
		judgeResult := ExecuteCheapJudge(ctx, obs, sri)
		if judgeResult.Decision == DecisionScout {
			result.Decision = DecisionJudgeScout
			result.EffectiveModel = sri.ScoutGroup
			result.DecisionPath = "judge_scout"
		} else {
			result.Decision = DecisionJudgeReason
			result.EffectiveModel = aliasInfo.DefaultGroup
			result.DecisionPath = "judge_reason"
			// 任何非正常 judge_reason 都要保留回退原因，避免与正常 REASON 判定混淆
			if judgeResult.InvalidDecision {
				result.FallbackReason = "judge_invalid_output"
				result.DecisionPath = "judge_fallback_to_reason"
			} else if judgeResult.FallbackReason != "" {
				result.FallbackReason = judgeResult.FallbackReason
				result.DecisionPath = "judge_fallback_to_reason"
			}
		}
	}

	// 6. 记录最终决策 metrics
	metrics.RecordSmartRouteDecision(result.DecisionPath)

	return result
}

// 辅助函数

func extractTextFromContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var texts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if text, ok := m["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
		return strings.Join(texts, " ")
	default:
		return fmt.Sprintf("%v", content)
	}
}

func detectMaterialTraces(content any) bool {
	text := extractTextFromContent(content)
	return containsMaterialKeywords(text)
}

func containsMaterialKeywords(text string) bool {
	lowerText := strings.ToLower(text)
	keywords := []string{
		"```", "def ", "function ", "class ", "import ",
		"http://", "https://", "www.", ".com", ".org",
		"file:", "path:", "source:", "code:", "line ", "lines ",
		"search result", "search results", "snippet:", "title:", "url:",
		"搜索结果", "网页", "网页内容", "文件内容", "源码", "代码片段", "文件路径", "行号",
	}
	for _, kw := range keywords {
		if strings.Contains(lowerText, kw) {
			return true
		}
	}
	return false
}

func containsCodeOrFileKeywords(text string) bool {
	lowerText := strings.ToLower(text)
	keywords := []string{
		"```", "def ", "func ", "function ", "class ", "import ", "package ",
		"path:", "file:", "source:", "code:", "line ", "lines ", ".go", ".ts", ".js", ".py", ".rs",
		"grep", "rg ", "ripgrep", "read_file", "view_file", "open_file",
		"文件内容", "文件路径", "源码", "代码片段", "行号", "读取文件", "读文件", "grep结果",
	}
	for _, kw := range keywords {
		if strings.Contains(lowerText, kw) {
			return true
		}
	}
	return false
}

func hasGatheringToolName(obs *TurnObservation) bool {
	for _, name := range obs.RecentToolNames {
		switch name {
		case "web_search", "web_extract", "agent-fetch", "agent_fetch", "read_file", "view_file", "open_file", "grep", "rg", "glob_search", "code_search", "search_code":
			return true
		}
	}
	return false
}

func shouldRouteScoutByRule(obs *TurnObservation) bool {
	if !(obs.LatestIsToolResult || obs.LatestHasMaterialInput) {
		return false
	}
	if !obs.HasMaterialTraces {
		return false
	}
	if len(obs.SynthesisSignals) > 0 || len(obs.ExecutionSignals) > 0 {
		return false
	}
	if len(obs.ContinueSearchSignals) > 0 {
		return true
	}
	if hasGatheringToolName(obs) && !obs.HasCodeOrFileTraces {
		return true
	}
	return false
}

func shouldNeedJudge(obs *TurnObservation) bool {
	if !obs.HasRecentToolCall {
		return false
	}
	if !(obs.LatestIsToolResult || obs.LatestHasMaterialInput) {
		return false
	}
	if !obs.HasMaterialTraces {
		return false
	}
	if len(obs.ExecutionSignals) > 0 {
		return false
	}
	if len(obs.ContinueSearchSignals) > 0 && len(obs.SynthesisSignals) > 0 {
		return true
	}
	if obs.HasCodeOrFileTraces && hasGatheringToolName(obs) {
		return true
	}
	return false
}

func detectExecutionSignals(text string) []string {
	var signals []string
	keywords := []string{
		"write", "implement", "create", "modify", "fix", "debug",
		"refactor", "optimize", "final", "summary", "conclude",
		"answer", "generate", "build",
	}
	lowerText := strings.ToLower(text)
	for _, kw := range keywords {
		if strings.Contains(lowerText, kw) {
			signals = append(signals, kw)
		}
	}
	return signals
}

func detectContinueSearchSignals(text string) []string {
	var signals []string
	keywords := []string{
		"search", "find", "look for", "check", "verify",
		"investigate", "explore", "analyze", "compare", "gather",
	}
	lowerText := strings.ToLower(text)
	for _, kw := range keywords {
		if strings.Contains(lowerText, kw) {
			signals = append(signals, kw)
		}
	}
	return signals
}

func detectSynthesisSignals(text string) []string {
	var signals []string
	keywords := []string{
		"based on", "according to", "from this", "using this",
		"what did you find", "what have you found", "what did we find",
		"what have we found", "explain this", "explain what",
	}
	lowerText := strings.ToLower(text)
	for _, kw := range keywords {
		if strings.Contains(lowerText, kw) {
			signals = append(signals, kw)
		}
	}
	return signals
}

func truncateText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen]
}

// Claude 相关辅助函数

func extractClaudeContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var texts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if text, ok := m["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
		return strings.Join(texts, " ")
	default:
		return fmt.Sprintf("%v", content)
	}
}

func hasToolUse(content any) bool {
	items, ok := content.([]interface{})
	if !ok {
		return false
	}
	for _, item := range items {
		if m, ok := item.(map[string]interface{}); ok {
			if t, ok := m["type"].(string); ok && t == "tool_use" {
				return true
			}
		}
	}
	return false
}

func extractClaudeToolNames(content any) []string {
	items, ok := content.([]interface{})
	if !ok {
		return nil
	}
	var names []string
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		if t != "tool_use" {
			continue
		}
		name, _ := m["name"].(string)
		if name != "" {
			names = append(names, strings.ToLower(name))
		}
	}
	return names
}

func isToolResult(content any) bool {
	items, ok := content.([]interface{})
	if !ok {
		return false
	}
	for _, item := range items {
		if m, ok := item.(map[string]interface{}); ok {
			if t, ok := m["type"].(string); ok && t == "tool_result" {
				return true
			}
		}
	}
	return false
}

func hasClaudeMaterialTraces(content any) bool {
	return containsMaterialKeywords(extractClaudeContent(content))
}

// Responses 相关辅助函数

func extractResponsesContent(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}

	// 尝试解析为字符串
	var str string
	if err := json.Unmarshal(content, &str); err == nil {
		return str
	}

	// 尝试解析为内容数组
	var parts []dto.ResponsesContentPart
	if err := json.Unmarshal(content, &parts); err == nil {
		var texts []string
		for _, p := range parts {
			if p.Type == "input_text" || p.Type == "output_text" {
				texts = append(texts, p.Text)
			}
		}
		return strings.Join(texts, " ")
	}

	return string(content)
}
