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
	failureReasonTokenHubQuota       = "tokenhub_quota_exceeded"
	failureReasonTokenHubContent     = "tokenhub_content_filter"
)

// tokenHubQuotaErrorCodes 已知的额度类错误码集合
var tokenHubQuotaErrorCodes = map[string]bool{
	"401007": true,
	"401008": true,
	"403004": true,
	"20097":  true,
}

// tokenHubHosts TokenHub 已知的目标 host 集合
var tokenHubHosts = map[string]bool{
	"api.lkeap.cloud.tencent.com": true,
	"tokenhub.tencentmaas.com":    true,
}

// quotaCooldown 额度类错误（HTTP 402 / TokenHub 私有额度码）的统一不健康冷却时间
const quotaCooldown = 1 * time.Hour

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

func parseResponse(resp *http.Response, providerName, upstreamModel, protocol string, prefillTimeout time.Duration, streamIdleTimeout time.Duration) (*Result, error) {
	result := &Result{
		Response:      resp,
		Winner:        providerName,
		UpstreamModel: upstreamModel,
		FailureKind:   FailureKindSuccess,
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

		kind, reason, streamTTFT, err := probeStreamPrefix(resp, protocol, prefillTimeout, streamIdleTimeout)
		if err != nil {
			return nil, err
		}
		result.FailureKind = kind
		result.FailureReason = reason
		result.StreamTTFT = streamTTFT
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

// applyTokenHubClassification 对 hard failure 做 provider 错误分类
// 先处理通用 HTTP 规则（如 402），再针对 TokenHub 做私有错误码分级
func applyTokenHubClassification(result *Result, resp *http.Response, taskRequest *http.Request) {
	if result.FailureKind != FailureKindHard {
		return
	}
	if isStreamResponse(resp) {
		return
	}

	// 通用：HTTP 402 表示欠费/额度耗尽，立即打 1 小时不健康
	if resp != nil && resp.StatusCode == http.StatusPaymentRequired {
		result.FailureKind = FailureKindHard
		result.FailureReason = failureReasonTokenHubQuota
		result.HealthActionInfo = HealthActionInfo{
			Action:           HealthActionMarkUnhealthy,
			CooldownOverride: quotaCooldown,
		}
		return
	}

	target := makeRequestTarget(resp, taskRequest)
	if !isTokenHubTarget(target) {
		return
	}

	info := classifyTokenHubError(target, resp)
	result.FailureKind = info.FailureKind
	result.FailureReason = info.FailureReason
	result.HealthActionInfo = info.HealthActionInfo
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

// isTokenHubTarget 判断请求目标 host 是否属于 TokenHub
func isTokenHubTarget(target requestTarget) bool {
	return tokenHubHosts[target.host]
}

// tokenHubErrorBody 表示 TokenHub 返回的统一错误体
type tokenHubErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// parseTokenHubErrorCode 从响应体中提取 error.code
// 返回空字符串表示解析失败或不存在
func parseTokenHubErrorCode(resp *http.Response) string {
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

	var errBody tokenHubErrorBody
	if err := json.Unmarshal(body, &errBody); err != nil {
		return ""
	}

	return errBody.Error.Code
}

// classifyTokenHubError 对 TokenHub 的错误码进行分类
// 返回默认的 FailureKindResult，HealthAction 和 FailureKind 表示分类结果
func classifyTokenHubError(target requestTarget, resp *http.Response) failureKindResult {
	if !isTokenHubTarget(target) {
		return defaultHardFailureResult()
	}

	// HTTP 429 只影响当前请求 failover，不写入长期不健康
	if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
		return failureKindResult{FailureKind: FailureKindHard, FailureReason: failureReasonHTTPStatus}
	}

	code := parseTokenHubErrorCode(resp)
	if code == "" {
		return defaultHardFailureResult()
	}

	return classifyTokenHubErrorCode(code)
}

// failureKindResult 封装分类结果，包含 FailureKind、FailureReason 和 HealthActionInfo
type failureKindResult struct {
	FailureKind      FailureKind
	FailureReason    string
	HealthActionInfo HealthActionInfo
}

func defaultHardFailureResult() failureKindResult {
	return failureKindResult{
		FailureKind:   FailureKindHard,
		FailureReason: failureReasonHTTPStatus,
	}
}

// applyHealthAction 根据分类结果对 provider 健康状态执行对应操作
func applyHealthAction(chk *health.Checker, healthKey string, result *Result) {
	switch result.HealthActionInfo.Action {
	case HealthActionMarkUnhealthy:
		chk.MarkUnhealthyFor(healthKey, result.HealthActionInfo.CooldownOverride)
	}
}

// classifyTokenHubErrorCode 根据 TokenHub 错误码返回对应的分类结果
func classifyTokenHubErrorCode(code string) failureKindResult {
	// 额度类错误：hard failure + 立即 1 小时不健康
	if tokenHubQuotaErrorCodes[code] {
		return failureKindResult{
			FailureKind:   FailureKindHard,
			FailureReason: failureReasonTokenHubQuota,
			HealthActionInfo: HealthActionInfo{
				Action:           HealthActionMarkUnhealthy,
				CooldownOverride: quotaCooldown,
			},
		}
	}

	// 内容安全过滤：映射到 soft_failure
	if code == "451001" {
		return failureKindResult{
			FailureKind:   FailureKindSoft,
			FailureReason: failureReasonTokenHubContent,
		}
	}

	// 其它 TokenHub 私有错误码：保持 hard failure，不写入长期不健康
	return defaultHardFailureResult()
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

// recordAdaptiveTTFTIfNeeded 在 attempt 确认为流式成功时写入 TTFT 样本。
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
