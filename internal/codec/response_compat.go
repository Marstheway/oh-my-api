package codec

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

type responsesCompatResult struct {
	FixedPaths []string
}

func (r *responsesCompatResult) add(path string) {
	r.FixedPaths = append(r.FixedPaths, path)
}

func (r responsesCompatResult) Changed() bool {
	return len(r.FixedPaths) > 0
}

func normalizeResponsesResponseJSON(body []byte) ([]byte, responsesCompatResult, error) {
	var obj map[string]any
	var result responsesCompatResult
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, result, err
	}

	normalizeResponsesPayloadMap(obj, "output", &result)

	out, err := json.Marshal(obj)
	if err != nil {
		return nil, result, err
	}
	return out, result, nil
}

func normalizeResponsesEventJSON(data []byte) ([]byte, responsesCompatResult, error) {
	var obj map[string]any
	var result responsesCompatResult
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, result, err
	}

	eventType, _ := obj["type"].(string)
	normalizeResponsesEventFieldsMap(obj, eventType, &result)

	if responseObj, ok := obj["response"].(map[string]any); ok {
		normalizeResponsesPayloadMap(responseObj, "response.output", &result)
	}
	if itemObj, ok := obj["item"].(map[string]any); ok {
		normalizeResponsesOutputItemMap(itemObj, "item", &result)
	}
	normalizeResponsesEventPartMap(obj, eventType, &result)

	out, err := json.Marshal(obj)
	if err != nil {
		return nil, result, err
	}
	return out, result, nil
}

func normalizeResponsesEventFieldsMap(obj map[string]any, eventType string, result *responsesCompatResult) {
	switch eventType {
	case "response.function_call_arguments.done":
		normalizeResponsesStringField(obj, "arguments", result)
	case "response.output_text.done", "response.reasoning_text.done":
		normalizeResponsesStringField(obj, "text", result)
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		normalizeResponsesIntegerField(obj, "summary_index", result)
	case "response.reasoning_summary_text.delta":
		normalizeResponsesIntegerField(obj, "summary_index", result)
	case "response.reasoning_summary_text.done":
		normalizeResponsesIntegerField(obj, "summary_index", result)
		normalizeResponsesStringField(obj, "text", result)
	case "response.refusal.done":
		normalizeResponsesStringField(obj, "refusal", result)
	}
}

func normalizeResponsesEventPartMap(obj map[string]any, eventType string, result *responsesCompatResult) {
	if partObj, ok := obj["part"].(map[string]any); ok {
		normalizeResponsesContentPartMap(partObj, "part", result)
		return
	}

	switch eventType {
	case "response.content_part.added", "response.content_part.done":
		obj["part"] = map[string]any{
			"type":        "output_text",
			"text":        "",
			"annotations": []any{},
		}
		result.add("part")
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		obj["part"] = map[string]any{
			"type": "summary_text",
			"text": "",
		}
		result.add("part")
	}
}

func normalizeResponsesIntegerField(obj map[string]any, field string, result *responsesCompatResult) {
	value, exists := obj[field]
	if number, ok := value.(float64); exists && ok && number >= 0 && number == float64(int64(number)) {
		return
	}
	obj[field] = 0
	result.add(field)
}

func normalizeResponsesStringField(obj map[string]any, field string, result *responsesCompatResult) {
	value, exists := obj[field]
	if _, ok := value.(string); exists && ok {
		return
	}
	obj[field] = ""
	result.add(field)
}

func shouldDropResponsesStreamEvent(eventType string) bool {
	// 部分 Responses 客户端不支持上游扩展的 keepalive 事件。
	return eventType == "keepalive"
}

func normalizeResponsesResponseObject(resp *dto.ResponsesResponse) responsesCompatResult {
	var result responsesCompatResult
	if resp == nil {
		return result
	}

	normalizeResponsesUsageObject(&resp.Usage, "usage", &result)

	if resp.Output == nil {
		resp.Output = []dto.ResponsesOutput{}
		result.add("output")
	}
	for i := range resp.Output {
		if resp.Output[i].Content == nil {
			resp.Output[i].Content = []dto.ResponsesOutputContent{}
			result.add(fmt.Sprintf("output[%d].content", i))
		}
		for j := range resp.Output[i].Content {
			if resp.Output[i].Content[j].Annotations == nil {
				resp.Output[i].Content[j].Annotations = []any{}
				result.add(fmt.Sprintf("output[%d].content[%d].annotations", i, j))
			}
		}
	}

	return result
}

func normalizeResponsesUsageObject(usage *dto.ResponsesUsage, usagePath string, result *responsesCompatResult) {
	if usage == nil {
		return
	}

	if usage.InputTokensDetails == nil {
		usage.InputTokensDetails = &dto.ResponsesUsageDetails{}
		result.add(usagePath + ".input_tokens_details")
	}

	if usage.OutputTokensDetails == nil && usage.CompletionTokensDetails != nil {
		details := *usage.CompletionTokensDetails
		usage.OutputTokensDetails = &details
		result.add(usagePath + ".output_tokens_details")
	}
	if usage.OutputTokensDetails == nil {
		usage.OutputTokensDetails = &dto.ResponsesUsageDetails{}
		result.add(usagePath + ".output_tokens_details")
	}

	if usage.CompletionTokensDetails == nil && usage.OutputTokensDetails != nil {
		details := *usage.OutputTokensDetails
		usage.CompletionTokensDetails = &details
	}
}

func normalizeResponsesPayloadMap(obj map[string]any, outputPath string, result *responsesCompatResult) {
	normalizeResponsesUsageMap(obj, outputPath, result)

	outputRaw, exists := obj["output"]
	if !exists || outputRaw == nil {
		obj["output"] = []any{}
		result.add(outputPath)
		return
	}

	items, ok := outputRaw.([]any)
	if !ok {
		return
	}
	for i, rawItem := range items {
		itemObj, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		normalizeResponsesOutputItemMap(itemObj, fmt.Sprintf("%s[%d]", outputPath, i), result)
	}
}

func normalizeResponsesOutputItemMap(item map[string]any, itemPath string, result *responsesCompatResult) {
	itemType, _ := item["type"].(string)
	if isResponsesToolCallItemType(itemType) {
		normalizeResponsesCallArgumentsMap(item, itemPath, result)
	}
	if itemType == "reasoning" {
		normalizeResponsesPartArray(item, "summary", itemPath+".summary", result)
	}

	contentRaw, exists := item["content"]
	if !exists {
		if itemType == "message" {
			item["content"] = []any{}
			result.add(itemPath + ".content")
		}
		return
	}
	if contentRaw == nil {
		item["content"] = []any{}
		result.add(itemPath + ".content")
		return
	}

	contentParts, ok := contentRaw.([]any)
	if !ok {
		return
	}
	for i, rawPart := range contentParts {
		partObj, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		normalizeResponsesContentPartMap(partObj, fmt.Sprintf("%s.content[%d]", itemPath, i), result)
	}
}

func normalizeResponsesPartArray(item map[string]any, field, fieldPath string, result *responsesCompatResult) {
	rawParts, exists := item[field]
	if !exists || rawParts == nil {
		item[field] = []any{}
		result.add(fieldPath)
		return
	}
	parts, ok := rawParts.([]any)
	if !ok {
		return
	}
	for i, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		normalizeResponsesContentPartMap(part, fmt.Sprintf("%s[%d]", fieldPath, i), result)
	}
}
func isResponsesToolCallItemType(itemType string) bool {
	itemType = strings.TrimSpace(itemType)
	if itemType == "" {
		return false
	}
	switch itemType {
	case "function_call", "custom_tool_call", "mcp_tool_call":
		return true
	default:
		return strings.HasSuffix(itemType, "_call") && !strings.HasSuffix(itemType, "_call_output")
	}
}

func normalizeResponsesCallArgumentsMap(item map[string]any, itemPath string, result *responsesCompatResult) {
	argumentsRaw, exists := item["arguments"]
	if exists && argumentsRaw != nil {
		return
	}

	if inputRaw, exists := item["input"]; exists && inputRaw != nil {
		if inputText, ok := normalizeValueToJSONString(inputRaw); ok {
			item["arguments"] = inputText
			result.add(itemPath + ".arguments")
			return
		}
	}

	item["arguments"] = ""
	result.add(itemPath + ".arguments")
}

func normalizeValueToJSONString(v any) (string, bool) {
	switch val := v.(type) {
	case string:
		return val, true
	case json.RawMessage:
		return string(val), true
	case []byte:
		return string(val), true
	default:
		buf, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		return string(buf), true
	}
}

func normalizeResponsesUsageMap(obj map[string]any, objPath string, result *responsesCompatResult) {
	usageRaw, exists := obj["usage"]
	if !exists || usageRaw == nil {
		status, _ := obj["status"].(string)
		if !isTerminalResponsesStatus(status) {
			return
		}
		obj["usage"] = map[string]any{
			"input_tokens":          0,
			"output_tokens":         0,
			"total_tokens":          0,
			"input_tokens_details":  map[string]any{"cached_tokens": 0},
			"output_tokens_details": map[string]any{"reasoning_tokens": 0},
		}
		result.add(objPath + ".usage")
		return
	}

	usage, ok := usageRaw.(map[string]any)
	if !ok {
		return
	}

	if inputDetailsRaw, exists := usage["input_tokens_details"]; (!exists || inputDetailsRaw == nil) && usage["prompt_tokens_details"] != nil {
		if promptDetails, ok := usage["prompt_tokens_details"].(map[string]any); ok {
			usage["input_tokens_details"] = cloneMap(promptDetails)
			result.add(objPath + ".usage.input_tokens_details")
		}
	}

	if outputDetailsRaw, exists := usage["output_tokens_details"]; (!exists || outputDetailsRaw == nil) && usage["completion_tokens_details"] != nil {
		if completionDetails, ok := usage["completion_tokens_details"].(map[string]any); ok {
			usage["output_tokens_details"] = cloneMap(completionDetails)
			result.add(objPath + ".usage.output_tokens_details")
		}
	}

	inputDetailsRaw, exists := usage["input_tokens_details"]
	if !exists || inputDetailsRaw == nil {
		usage["input_tokens_details"] = map[string]any{"cached_tokens": 0}
		result.add(objPath + ".usage.input_tokens_details")
	} else if inputDetails, ok := inputDetailsRaw.(map[string]any); ok {
		if _, ok := inputDetails["cached_tokens"]; !ok {
			inputDetails["cached_tokens"] = 0
			result.add(objPath + ".usage.input_tokens_details.cached_tokens")
		}
	}

	outputDetailsRaw, exists := usage["output_tokens_details"]
	if !exists || outputDetailsRaw == nil {
		usage["output_tokens_details"] = map[string]any{"reasoning_tokens": 0}
		result.add(objPath + ".usage.output_tokens_details")
	} else if outputDetails, ok := outputDetailsRaw.(map[string]any); ok {
		if _, ok := outputDetails["reasoning_tokens"]; !ok {
			outputDetails["reasoning_tokens"] = 0
			result.add(objPath + ".usage.output_tokens_details.reasoning_tokens")
		}
	}

	if completionDetailsRaw, exists := usage["completion_tokens_details"]; (!exists || completionDetailsRaw == nil) && usage["output_tokens_details"] != nil {
		if outputDetails, ok := usage["output_tokens_details"].(map[string]any); ok {
			usage["completion_tokens_details"] = cloneMap(outputDetails)
			result.add(objPath + ".usage.completion_tokens_details")
		}
	}
}

func isTerminalResponsesStatus(status string) bool {
	switch status {
	case "completed", "incomplete", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func cloneMap(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func normalizeResponsesContentPartMap(part map[string]any, partPath string, result *responsesCompatResult) {
	partType, _ := part["type"].(string)
	switch partType {
	case "output_text":
		annotationsRaw, exists := part["annotations"]
		if !exists || annotationsRaw == nil {
			part["annotations"] = []any{}
			result.add(partPath + ".annotations")
		}
		normalizeResponsesStringFieldAtPath(part, "text", partPath+".text", result)
	case "summary_text", "reasoning_text":
		normalizeResponsesStringFieldAtPath(part, "text", partPath+".text", result)
	case "refusal":
		normalizeResponsesStringFieldAtPath(part, "refusal", partPath+".refusal", result)
	}
}

func normalizeResponsesStringFieldAtPath(obj map[string]any, field, path string, result *responsesCompatResult) {
	value, exists := obj[field]
	if _, ok := value.(string); exists && ok {
		return
	}
	obj[field] = ""
	result.add(path)
}

// readResponsesNonStreamBody 是上游非流式 Responses JSON 的公共读取入口：
// 读取 body 后执行归一化、解码为 dto.ResponsesResponse 并补齐对象级默认值，
// 返回对象供转换路径消费，同时返回归一化后的 body 供 passthrough 写回以保留未知字段。
// 它不写 HTTP 响应，也不做 token 统计；token 统计与模型名回显由调用点负责。
func readResponsesNonStreamBody(body []byte) (*dto.ResponsesResponse, []byte, responsesCompatResult, error) {
	normalizedBody, compat, err := normalizeResponsesResponseJSON(body)
	if err != nil {
		return nil, nil, compat, err
	}

	var responsesResp dto.ResponsesResponse
	if err := json.Unmarshal(normalizedBody, &responsesResp); err != nil {
		return nil, nil, compat, err
	}
	normalizeResponsesResponseObject(&responsesResp)

	return &responsesResp, normalizedBody, compat, nil
}

// responsesEventResult 是单条上游 Responses SSE data 公共入口的结果。
type responsesEventResult struct {
	// NormalizedData 是归一化后的原始 JSON data，passthrough 用它写回以保留未知字段。
	NormalizedData []byte
	// Event 是归一化 data 解码后的事件，Chat、Anthropic 与聚合路径使用它。
	Event *dto.ResponsesStreamEvent
	// Compat 记录本次归一化修复的字段路径。
	Compat responsesCompatResult
}

// normalizeResponsesEventData 是上游 Responses SSE 单条 data 的公共入口：
// 归一化后解码为 dto.ResponsesStreamEvent，同时返回归一化后的原始 JSON data。
// passthrough 使用 NormalizedData 写回以保留未知字段与 SSE 元数据；
// Chat、Anthropic 与聚合路径使用 Event。
// 公共层不执行 keepalive 丢弃、模型名改写、HTTP 写回或 token 统计。
func normalizeResponsesEventData(data []byte) (*responsesEventResult, error) {
	normalized, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		return nil, err
	}

	var event dto.ResponsesStreamEvent
	if err := json.Unmarshal(normalized, &event); err != nil {
		return nil, err
	}

	return &responsesEventResult{
		NormalizedData: normalized,
		Event:          &event,
		Compat:         compat,
	}, nil
}
