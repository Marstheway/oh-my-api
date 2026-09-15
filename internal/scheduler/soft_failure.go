package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/metrics"
)

type FailureKind string

const (
	FailureKindSuccess FailureKind = "success"
	FailureKindHard    FailureKind = "hard_failure"
	FailureKindSoft    FailureKind = "soft_failure"
)

const (
	failureReasonHTTPStatus          = "http_status"
	failureReasonContentFilterFinish = "content_filter_finish_reason"
	failureReasonContentFilterStop   = "content_filter_stop_reason"
	failureReasonContentFilterDetail = "content_filter_incomplete"
	failureReasonContentFilterPhrase = "content_filter_phrase"
	failureReasonQuotaExceeded       = "quota_exceeded"
	failureReasonContentFilter       = "content_filter"
)

// tencentQuotaErrorCodes 腾讯云推理 API 已知的额度类错误码集合。
// 参考 docs/references/errcode/腾讯云 API错误码.md（产品文档 1823/131595）。
var tencentQuotaErrorCodes = map[string]bool{
	"401007": true, // CodeEndpointNoFreePackage：无免费体验额度
	"401008": true, // CodeEndpointFreeQuotaExhausted：免费体验额度耗尽
	"403004": true, // CodeInsufficientBalance：账号欠费
}

// openAIQuotaErrorCodes OpenAI 429 响应中表示额度耗尽的 error.code 白名单。
// 参考 docs/references/errcode/openai api ErrCode.md。
// 注意：429 本身是限流语义，仅当 error.code 命中此白名单时才升级为额度错误。
var openAIQuotaErrorCodes = map[string]bool{
	"credit_balance_exhausted":          true,
	"organization_spend_limit_exceeded": true,
	"project_spend_limit_exceeded":      true,
	"organization_usage_limit_exceeded": true,
}

// tencentHosts 腾讯云推理 API 家族已知 host 集合。
// token-hub / token-plan / coding-plan 三个产品共享同一套腾讯云错误码。
var tencentHosts = map[string]bool{
	"api.lkeap.cloud.tencent.com": true, // token-plan / coding-plan
	"tokenhub.tencentmaas.com":    true, // token-hub
}

// quotaBaseCooldown 额度类错误（HTTP 402 / 私有额度码）的初始退避时长
const quotaBaseCooldown = 1 * time.Hour

// quotaMaxCooldown 额度类错误的退避封顶时长（指数退避上限）
const quotaMaxCooldown = 12 * time.Hour

// requestTarget 表示请求的目标地址信息，用于 provider 家族识别
type requestTarget struct {
	host string
	path string
}

var softFailurePhrases = []string{
	"i can't help with that request",
	"i cannot help with that request",
	"i can't assist with that request",
	"i cannot assist with that request",
	"i'm sorry, but i can't help with that",
	"i'm sorry, but i cannot help with that",
	"violates our content policy",
	"request was flagged by our safety system",
	"你好，我无法给到相关内容",
	"抱歉，我无法提供相关内容",
}

func parseResponse(resp *http.Response, providerName, upstreamModel, protocol string, prefillTimeout time.Duration, streamIdleTimeout time.Duration, attemptStart time.Time) (*Result, error) {
	result := &Result{
		Response:         resp,
		Winner:           providerName,
		UpstreamModel:    upstreamModel,
		OutboundProtocol: protocol,
		FailureKind:      FailureKindSuccess,
	}

	if resp == nil {
		return result, nil
	}

	if isStreamResponse(resp) {
		if resp.StatusCode >= http.StatusBadRequest {
			result.FailureKind = FailureKindHard
			result.FailureReason = failureReasonHTTPStatus
			return result, nil
		}

		kind, reason, streamTTFT, err := probeStreamPrefix(resp, protocol, prefillTimeout, streamIdleTimeout, attemptStart)
		if err != nil {
			return nil, err
		}
		result.FailureKind = kind
		result.FailureReason = reason
		result.StreamTTFT = streamTTFT
		if streamTTFT > 0 {
			result.StreamStartedAt = attemptStart.Add(streamTTFT)
		}
		return result, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))

	if resp.StatusCode >= http.StatusBadRequest {
		result.FailureKind = FailureKindHard
		result.FailureReason = failureReasonHTTPStatus
		return result, nil
	}

	result.Usage = extractUsage(protocol, body)
	result.FailureKind, result.FailureReason = classifyResponse(protocol, body, nil)
	return result, nil
}

// applyProviderErrorClassification 对 hard failure 做 provider 级错误再分类。
// 只处理会影响健康策略或 soft 语义的关键码，其余保持 parseResponse 原样：
//  1. 通用 HTTP 402 → 额度退避
//  2. 腾讯云推理 host：额度私有码 → 额度退避；451001 → soft（换源、不摘除）
//  3. OpenAI 兼容 429 + 额度 error.code → 额度退避
//
// 普通 429 / 其它 4xx 不摘除，仅影响当前请求 failover。
func applyProviderErrorClassification(result *Result, resp *http.Response, taskRequest *http.Request) {
	if result.FailureKind != FailureKindHard {
		return
	}
	if isStreamResponse(resp) {
		return
	}

	// 通用：HTTP 402 表示欠费/额度耗尽。
	// 覆盖 Anthropic billing_error、DeepSeek Insufficient Balance、腾讯云 402 等。
	if resp != nil && resp.StatusCode == http.StatusPaymentRequired {
		markQuotaFailure(result)
		return
	}

	target := makeRequestTarget(resp, taskRequest)
	if isTencentTarget(target) {
		switch code := parseErrorCode(resp); {
		case tencentQuotaErrorCodes[code]:
			markQuotaFailure(result)
		case code == "451001":
			// 腾讯内容审核：soft，便于指标识别 prompt 是否触发审核；换源且不 unhealthy
			result.FailureKind = FailureKindSoft
			result.FailureReason = failureReasonContentFilter
			result.HealthActionInfo = HealthActionInfo{}
		}
		// 其它腾讯私有码（含 429 限流）：保持 hard 4xx 默认——换源、不摘除
		return
	}

	// OpenAI 兼容错误体：429 + 额度类 error.code → 额度退避
	if resp != nil && resp.StatusCode == http.StatusTooManyRequests && isOpenAIQuotaError(resp) {
		markQuotaFailure(result)
	}
}

// markQuotaFailure 将 hard failure 标记为额度类错误并挂额度退避动作
func markQuotaFailure(result *Result) {
	result.FailureKind = FailureKindHard
	result.FailureReason = failureReasonQuotaExceeded
	result.HealthActionInfo = HealthActionInfo{
		Action: HealthActionMarkUnhealthyQuota,
	}
}

// isOpenAIQuotaError 判断响应是否为 OpenAI 风格额度耗尽错误
// （HTTP 429 + error.code 命中额度白名单）
func isOpenAIQuotaError(resp *http.Response) bool {
	code := parseErrorCode(resp)
	return code != "" && openAIQuotaErrorCodes[code]
}

// makeRequestTarget 从 HTTP 响应和任务请求中提取目标 URL 信息
func makeRequestTarget(resp *http.Response, taskRequest *http.Request) requestTarget {
	// 优先从 resp.Request.URL 获取
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		return requestTarget{
			host: resp.Request.URL.Host,
			path: resp.Request.URL.Path,
		}
	}
	// 回退到 taskRequest.URL
	if taskRequest != nil && taskRequest.URL != nil {
		return requestTarget{
			host: taskRequest.URL.Host,
			path: taskRequest.URL.Path,
		}
	}
	return requestTarget{}
}

// isTencentTarget 判断请求目标 host 是否属于腾讯云推理 API 家族
func isTencentTarget(target requestTarget) bool {
	return tencentHosts[target.host]
}

// errorBody 表示 OpenAI 兼容的统一错误体；code 可能是 string 或 number
type errorBody struct {
	Error struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	} `json:"error"`
}

// parseErrorCode 从响应体中提取 error.code（OpenAI 兼容格式）
// 支持 JSON string 与 number；返回空字符串表示解析失败或不存在
func parseErrorCode(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))

	if len(body) == 0 {
		return ""
	}

	var errBody errorBody
	if err := json.Unmarshal(body, &errBody); err != nil {
		return ""
	}

	return normalizeErrorCode(errBody.Error.Code)
}

// normalizeErrorCode 将 error.code 的 RawMessage 规范为字符串
func normalizeErrorCode(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return strconv.FormatInt(int64(f), 10)
	}
	return ""
}

// applyHealthAction 根据分类结果对 provider 健康状态执行对应操作
func applyHealthAction(chk *health.Checker, healthKey string, result *Result) {
	switch result.HealthActionInfo.Action {
	case HealthActionMarkUnhealthyQuota:
		// 额度策略集中在此：base/max 不经由 Result 透传
		chk.MarkUnhealthyEscalating(healthKey, quotaBaseCooldown, quotaMaxCooldown)
	case HealthActionMarkUnhealthy:
		chk.MarkUnhealthyFor(healthKey, result.HealthActionInfo.CooldownOverride)
	}
}

func classifyResponse(protocol string, body []byte, sseDataEvents []string) (FailureKind, string) {
	normalized := normalizeFailureProtocol(protocol)
	if len(sseDataEvents) > 0 {
		return classifyStreamResponse(normalized, sseDataEvents)
	}
	if len(body) == 0 {
		return FailureKindSuccess, ""
	}
	return classifyBufferedResponse(normalized, body)
}

func normalizeFailureProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "", "openai", "openai.chat", "openai/chat":
		return "openai.chat"
	case "openai.responses", "openai.response", "openai/responses":
		return "openai.responses"
	case "anthropic", "anthropic.messages", "anthropic/messages":
		return "anthropic.messages"
	default:
		return strings.ToLower(strings.TrimSpace(protocol))
	}
}

func extractUsage(protocol string, body []byte) *UsageInfo {
	switch normalizeFailureProtocol(protocol) {
	case "anthropic.messages":
		var claudeResp dto.ClaudeResponse
		if err := json.Unmarshal(body, &claudeResp); err != nil {
			return nil
		}
		usage := &UsageInfo{
			PromptTokens:     claudeResp.Usage.InputTokens,
			CompletionTokens: claudeResp.Usage.OutputTokens,
			TotalTokens:      claudeResp.Usage.InputTokens + claudeResp.Usage.OutputTokens,
		}
		if claudeResp.StopReason != nil {
			usage.FinishReason = *claudeResp.StopReason
		}
		return usage
	case "openai.responses":
		var responsesResp dto.ResponsesResponse
		if err := json.Unmarshal(body, &responsesResp); err != nil {
			return nil
		}
		totalTokens := responsesResp.Usage.TotalTokens
		if totalTokens == 0 {
			totalTokens = responsesResp.Usage.InputTokens + responsesResp.Usage.OutputTokens
		}
		return &UsageInfo{
			PromptTokens:     responsesResp.Usage.InputTokens,
			CompletionTokens: responsesResp.Usage.OutputTokens,
			TotalTokens:      totalTokens,
		}
	default:
		var openaiResp dto.ChatCompletionResponse
		if err := json.Unmarshal(body, &openaiResp); err != nil {
			return nil
		}
		usage := &UsageInfo{
			PromptTokens:     openaiResp.Usage.PromptTokens,
			CompletionTokens: openaiResp.Usage.CompletionTokens,
			TotalTokens:      openaiResp.Usage.TotalTokens,
		}
		if len(openaiResp.Choices) > 0 && openaiResp.Choices[0].FinishReason != nil {
			usage.FinishReason = *openaiResp.Choices[0].FinishReason
		}
		return usage
	}
}

func classifyBufferedResponse(protocol string, body []byte) (FailureKind, string) {
	switch protocol {
	case "anthropic.messages":
		return classifyBufferedAnthropic(body)
	case "openai.responses":
		return classifyBufferedResponses(body)
	default:
		return classifyBufferedOpenAIChat(body)
	}
}

func classifyBufferedOpenAIChat(body []byte) (FailureKind, string) {
	var resp dto.ChatCompletionResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return FailureKindSuccess, ""
	}

	texts := make([]string, 0, len(resp.Choices))
	for _, choice := range resp.Choices {
		if choice.FinishReason != nil && strings.EqualFold(*choice.FinishReason, "content_filter") {
			return FailureKindSoft, failureReasonContentFilterFinish
		}
		if choice.Message != nil {
			texts = append(texts, choice.Message.Content)
		}
	}

	if containsSoftFailurePhrase(strings.Join(texts, "\n")) {
		return FailureKindSoft, failureReasonContentFilterPhrase
	}
	return FailureKindSuccess, ""
}

func classifyBufferedResponses(body []byte) (FailureKind, string) {
	var resp dto.ResponsesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return FailureKindSuccess, ""
	}

	if resp.IncompleteDetails != nil && strings.EqualFold(resp.IncompleteDetails.Reason, "content_filter") {
		return FailureKindSoft, failureReasonContentFilterDetail
	}

	var texts []string
	for _, output := range resp.Output {
		if output.Type != "message" {
			continue
		}
		if output.Role != "" && output.Role != "assistant" {
			continue
		}
		for _, content := range output.Content {
			if content.Text != "" {
				texts = append(texts, content.Text)
			}
		}
	}

	if containsSoftFailurePhrase(strings.Join(texts, "\n")) {
		return FailureKindSoft, failureReasonContentFilterPhrase
	}
	return FailureKindSuccess, ""
}

func classifyBufferedAnthropic(body []byte) (FailureKind, string) {
	var resp dto.ClaudeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return FailureKindSuccess, ""
	}

	if resp.StopReason != nil && strings.EqualFold(*resp.StopReason, "content_filter") {
		return FailureKindSoft, failureReasonContentFilterStop
	}

	var texts []string
	for _, block := range resp.Content {
		if block.Type == "text" && block.Text != "" {
			texts = append(texts, block.Text)
		}
	}

	if containsSoftFailurePhrase(strings.Join(texts, "\n")) {
		return FailureKindSoft, failureReasonContentFilterPhrase
	}
	return FailureKindSuccess, ""
}

func classifyStreamResponse(protocol string, sseDataEvents []string) (FailureKind, string) {
	switch protocol {
	case "anthropic.messages":
		return classifyStreamAnthropic(sseDataEvents)
	case "openai.responses":
		return classifyStreamResponses(sseDataEvents)
	default:
		return classifyStreamOpenAIChat(sseDataEvents)
	}
}

func classifyStreamOpenAIChat(sseDataEvents []string) (FailureKind, string) {
	var textBuilder strings.Builder
	for _, event := range sseDataEvents {
		if strings.TrimSpace(event) == "[DONE]" {
			continue
		}

		var chunk dto.ChatCompletionChunk
		if err := json.Unmarshal([]byte(event), &chunk); err != nil {
			continue
		}

		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil && strings.EqualFold(*choice.FinishReason, "content_filter") {
				return FailureKindSoft, failureReasonContentFilterFinish
			}
			if choice.Delta != nil && choice.Delta.Content != "" {
				textBuilder.WriteString(choice.Delta.Content)
				if containsSoftFailurePhrase(textBuilder.String()) {
					return FailureKindSoft, failureReasonContentFilterPhrase
				}
			}
		}
	}
	return FailureKindSuccess, ""
}

func classifyStreamResponses(sseDataEvents []string) (FailureKind, string) {
	var textBuilder strings.Builder
	for _, event := range sseDataEvents {
		if strings.TrimSpace(event) == "[DONE]" {
			continue
		}

		var streamEvent dto.ResponsesStreamEvent
		if err := json.Unmarshal([]byte(event), &streamEvent); err != nil {
			continue
		}

		if streamEvent.Type == "response.completed" || streamEvent.Type == "response.failed" {
			var response struct {
				IncompleteDetails *dto.IncompleteDetails `json:"incomplete_details,omitempty"`
			}
			if len(streamEvent.Response) > 0 && json.Unmarshal(streamEvent.Response, &response) == nil {
				if response.IncompleteDetails != nil && strings.EqualFold(response.IncompleteDetails.Reason, "content_filter") {
					return FailureKindSoft, failureReasonContentFilterDetail
				}
			}
		}

		if streamEvent.Type != "response.output_text.delta" {
			continue
		}

		textDelta := parseResponsesTextDelta(streamEvent.Delta)
		if textDelta == "" {
			continue
		}
		textBuilder.WriteString(textDelta)
		if containsSoftFailurePhrase(textBuilder.String()) {
			return FailureKindSoft, failureReasonContentFilterPhrase
		}
	}
	return FailureKindSuccess, ""
}

func classifyStreamAnthropic(sseDataEvents []string) (FailureKind, string) {
	var textBuilder strings.Builder
	for _, event := range sseDataEvents {
		if strings.TrimSpace(event) == "[DONE]" {
			continue
		}

		var streamEvent dto.ClaudeStreamEvent
		if err := json.Unmarshal([]byte(event), &streamEvent); err != nil {
			continue
		}

		switch streamEvent.Type {
		case "message_delta":
			if streamEvent.Delta != nil && strings.EqualFold(streamEvent.Delta.StopReason, "content_filter") {
				return FailureKindSoft, failureReasonContentFilterStop
			}
		case "message_stop":
			var stopEvent struct {
				StopReason string `json:"stop_reason,omitempty"`
			}
			if json.Unmarshal([]byte(event), &stopEvent) == nil && strings.EqualFold(stopEvent.StopReason, "content_filter") {
				return FailureKindSoft, failureReasonContentFilterStop
			}
		case "content_block_delta":
			if streamEvent.Delta != nil && streamEvent.Delta.Text != "" {
				textBuilder.WriteString(streamEvent.Delta.Text)
				if containsSoftFailurePhrase(textBuilder.String()) {
					return FailureKindSoft, failureReasonContentFilterPhrase
				}
			}
		}
	}
	return FailureKindSuccess, ""
}

func parseResponsesTextDelta(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var direct string
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct
	}

	var payload struct {
		Delta string `json:"delta,omitempty"`
		Text  string `json:"text,omitempty"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	if payload.Delta != "" {
		return payload.Delta
	}
	return payload.Text
}

func containsSoftFailurePhrase(text string) bool {
	lowerText := strings.ToLower(text)
	for _, phrase := range softFailurePhrases {
		if strings.Contains(lowerText, phrase) {
			return true
		}
	}
	return false
}

// classifyAttemptOutcome 将执行结果分类为 attempt 指标所需的结构
func classifyAttemptOutcome(scheduler string, task Task, result *Result, err error, elapsed time.Duration) metrics.ProviderAttemptInfo {
	info := metrics.ProviderAttemptInfo{
		Scheduler:        scheduler,
		Provider:         task.ProviderName,
		UpstreamModel:    task.UpstreamModel,
		ModelGroup:       task.ModelGroup,
		OutboundProtocol: responseProtocol(task),
		Duration:         elapsed.Seconds(),
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			info.Result = "canceled"
			info.FailureReason = "context_canceled"
		} else {
			info.Result = "transport_error"
			info.FailureReason = "transport_error"
		}
		return info
	}

	if result == nil {
		info.Result = "transport_error"
		info.FailureReason = "transport_error"
		return info
	}

	switch result.FailureKind {
	case FailureKindSuccess:
		info.Result = "success"
	case FailureKindSoft:
		info.Result = "soft_failure"
		info.FailureReason = result.FailureReason
		if result.Response != nil {
			info.StatusCode = strconv.Itoa(result.Response.StatusCode)
		}
	case FailureKindHard:
		info.Result = "hard_failure"
		info.FailureReason = result.FailureReason
		if result.Response != nil {
			info.StatusCode = strconv.Itoa(result.Response.StatusCode)
		}
	}

	return info
}

// recordAttemptMetric 记录 attempt 指标
func recordAttemptMetric(scheduler string, task Task, result *Result, err error, elapsed time.Duration) {
	info := classifyAttemptOutcome(scheduler, task, result, err, elapsed)
	metrics.RecordProviderAttempt(info)

	// 流式探测成功后将 TTFT 写入 adaptive tracker
	recordAdaptiveTTFTIfNeeded(result, task)
}

// adaptiveCandidateKey 根据 Task 构造 provider/upstream_model 键。
func adaptiveCandidateKey(task Task) string {
	return task.ProviderName + "/" + task.UpstreamModel
}

// recordAdaptiveTTFTIfNeeded 在 attempt 确认为流式成功时，将端到端 TTFT 样本写入 adaptive tracker。
// TTFT 表示单次上游尝试从发起 HTTP 请求到收到首个 SSE 事件的耗时。
func recordAdaptiveTTFTIfNeeded(result *Result, task Task) {
	if result == nil {
		return
	}
	if result.FailureKind != FailureKindSuccess {
		return
	}
	if result.StreamTTFT <= 0 {
		return
	}
	// 使用浮点除法保留亚毫秒精度，避免 Milliseconds() 截断为 0
	key := adaptiveCandidateKey(task)
	ttftMs := float64(result.StreamTTFT) / float64(time.Millisecond)
	GetLatencyTracker().RecordSuccess(key, ttftMs, time.Now())
}
