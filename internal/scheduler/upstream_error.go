package scheduler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const (
	maxUpstreamErrorLogRunes = 2048
	maxUpstreamErrorLogBytes = maxUpstreamErrorLogRunes * 4
	// 诊断字段只取顶层 key；限制读取体积避免异常大 body 拖垮日志路径
	maxRequestDiagBodyBytes  = 2 << 20
	maxRequestDiagFieldRunes = 256
)

// upstreamErrorLogAttrs 为 HTTP 错误响应追加可诊断且有界的上游错误摘要。
// 已读取的前缀会与原 body 组合，保证后续仍可读取完整响应并关闭底层连接。
// 若 request 配置了 GetBody，额外附带 tool_choice / thinking 等请求侧诊断字段。
func upstreamErrorLogAttrs(attrs []any, result *Result) []any {
	if result == nil || result.Response == nil || result.Response.StatusCode < http.StatusBadRequest {
		return attrs
	}

	if result.Response.Body != nil {
		body := result.Response.Body
		prefix, _ := io.ReadAll(io.LimitReader(body, maxUpstreamErrorLogBytes))
		result.Response.Body = &prefixedBody{prefix: bytes.NewReader(prefix), tail: body}

		if summary := summarizeUpstreamError(prefix, maxUpstreamErrorLogRunes); summary != "" {
			attrs = append(attrs, "upstream_error", summary)
		}
	}

	if result.Response.Request != nil {
		attrs = append(attrs, RequestBodyDiagAttrs(result.Response.Request)...)
	}
	return attrs
}

// RequestBodyDiagAttrs 从已发送的上游请求中提取有界诊断字段（不记录 messages 正文）。
// 依赖 req.GetBody；未设置时返回 nil（不尝试消费已耗尽的 Body）。
func RequestBodyDiagAttrs(req *http.Request) []any {
	if req == nil || req.GetBody == nil {
		return nil
	}
	rc, err := req.GetBody()
	if err != nil || rc == nil {
		return nil
	}
	defer rc.Close()

	body, err := io.ReadAll(io.LimitReader(rc, maxRequestDiagBodyBytes))
	if err != nil || len(body) == 0 {
		return nil
	}
	return ParseRequestBodyDiagAttrs(body)
}

// ParseRequestBodyDiagAttrs 从请求 JSON 顶层字段提取诊断 attr，供失败日志与 materialize 复用。
func ParseRequestBodyDiagAttrs(body []byte) []any {
	if len(body) == 0 {
		return nil
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return []any{"req_body_bytes", len(body), "req_diag_parse", "invalid_json"}
	}

	attrs := []any{"req_body_bytes", len(body)}

	if v, ok := top["tool_choice"]; ok {
		attrs = append(attrs, "req_tool_choice", compactJSONRaw(v, maxRequestDiagFieldRunes))
	}
	if v, ok := top["thinking"]; ok {
		attrs = append(attrs, "req_thinking", compactJSONRaw(v, maxRequestDiagFieldRunes))
	}
	if v, ok := top["reasoning_effort"]; ok {
		attrs = append(attrs, "req_reasoning_effort", compactJSONRaw(v, maxRequestDiagFieldRunes))
	}
	if v, ok := top["reasoning"]; ok {
		attrs = append(attrs, "req_reasoning", compactJSONRaw(v, maxRequestDiagFieldRunes))
	}
	if v, ok := top["enable_thinking"]; ok {
		attrs = append(attrs, "req_enable_thinking", compactJSONRaw(v, maxRequestDiagFieldRunes))
	}
	if v, ok := top["parallel_tool_calls"]; ok {
		attrs = append(attrs, "req_parallel_tool_calls", compactJSONRaw(v, maxRequestDiagFieldRunes))
	}
	if v, ok := top["tools"]; ok {
		if n := jsonArrayLen(v); n >= 0 {
			attrs = append(attrs, "req_tools_count", n)
		} else {
			attrs = append(attrs, "req_tools_kind", "non_array")
		}
	}
	// Anthropic 用 messages；Chat 也有 messages；Responses 用 input
	if v, ok := top["messages"]; ok {
		if n := jsonArrayLen(v); n >= 0 {
			attrs = append(attrs, "req_messages_count", n)
		} else {
			attrs = append(attrs, "req_messages_kind", "non_array")
		}
	} else if v, ok := top["input"]; ok {
		// input 可能是 string 或 array
		if n := jsonArrayLen(v); n >= 0 {
			attrs = append(attrs, "req_input_count", n)
			if reasoning, calls := countResponsesInputItems(v); reasoning > 0 || calls > 0 {
				attrs = append(attrs, "req_input_reasoning_count", reasoning, "req_input_function_call_count", calls)
			}
		} else {
			attrs = append(attrs, "req_input_kind", "non_array")
		}
	}

	return attrs
}

// countResponsesInputItems 统计 Responses input 数组中的 reasoning 与
// function_call item 数量，供诊断 400 时判断是否缺少 reasoning 回传。
func countResponsesInputItems(raw json.RawMessage) (reasoning, functionCalls int) {
	var items []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0, 0
	}
	for _, it := range items {
		switch it.Type {
		case "reasoning":
			reasoning++
		case "function_call":
			functionCalls++
		}
	}
	return reasoning, functionCalls
}

func compactJSONRaw(raw json.RawMessage, maxRunes int) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	// 已经是 JSON token（string/number/object/array/bool/null），压成单行
	compact := &bytes.Buffer{}
	if err := json.Compact(compact, trimmed); err == nil {
		return summarizeUpstreamError(compact.Bytes(), maxRunes)
	}
	return summarizeUpstreamError(trimmed, maxRunes)
}

// jsonArrayLen 返回 JSON 数组长度；非数组返回 -1。
func jsonArrayLen(raw json.RawMessage) int {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return -1
	}
	return len(arr)
}

func summarizeUpstreamError(body []byte, maxRunes int) string {
	trimmed := strings.Join(strings.Fields(string(body)), " ")
	if trimmed == "" {
		return ""
	}

	runes := []rune(trimmed)
	if maxRunes > 0 && len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return trimmed
}

type prefixedBody struct {
	prefix *bytes.Reader
	tail   io.ReadCloser
}

func (b *prefixedBody) Read(p []byte) (int, error) {
	if b.prefix != nil {
		n, err := b.prefix.Read(p)
		if n > 0 {
			if err == io.EOF {
				return n, nil
			}
			return n, err
		}
		if err != nil && err != io.EOF {
			return 0, err
		}
	}
	return b.tail.Read(p)
}

func (b *prefixedBody) Close() error {
	return b.tail.Close()
}
