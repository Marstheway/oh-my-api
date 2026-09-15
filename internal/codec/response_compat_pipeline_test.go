package codec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
)

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestResponsesStreamToChat_UsageFromCompletedEvent_HasPromptTokensDetails 是改动前的必失败信号：
// 上游 response.completed 的 usage 故意省略 input_tokens_details，
// 在 IncludeUsage=true 时，网关写回的 usage-only Chat chunk 必须包含
// prompt_tokens_details 且 cached_tokens==0。接入公共 event 归一化入口前应失败。
func TestResponsesStreamToChat_UsageFromCompletedEvent_HasPromptTokensDetails(t *testing.T) {
	streamBody := "" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-hy3\",\"output\":[]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":11,\"total_tokens\":18}}}\n\n"

	resp := newResponse(200, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := writeOpenAIResponseStreamAsChatStream(w, resp, nil, ResponseModelContext{IncludeUsage: true}); err != nil {
		t.Fatalf("writeOpenAIResponseStreamAsChatStream error: %v", err)
	}

	body := w.Body.String()
	var usageChunk *dto.Usage
	for _, line := range strings.Split(body, "\n\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk dto.ChatCompletionChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			usageChunk = chunk.Usage
			break
		}
	}

	if usageChunk == nil {
		t.Fatalf("expected a usage-only chat chunk, body=%s", body)
	}
	if usageChunk.PromptTokensDetails == nil {
		t.Fatalf("prompt_tokens_details = nil, want non-nil (cached_tokens:0), body=%s", body)
	}
	if usageChunk.PromptTokensDetails.CachedTokens != 0 {
		t.Fatalf("prompt_tokens_details.cached_tokens = %d, want 0", usageChunk.PromptTokensDetails.CachedTokens)
	}
}

// TestResponsesStreamToChat_FunctionCallArgumentsAcrossPaths 固定一组 function call 输入，
// 断言聚合路径、Chat 与 Anthropic 转换都实际消费了 arguments delta 构建的 function call arguments。
func TestResponsesStreamToChat_FunctionCallArgumentsAcrossPaths(t *testing.T) {
	argDelta := `{"loc":"Beijing"}`
	streamBody := "" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-hy3\",\"output\":[]}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"call-1\",\"name\":\"get_weather\"}}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc-1\",\"delta\":" + jsonString(argDelta) + "}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"call-1\",\"name\":\"get_weather\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"call-1\",\"name\":\"get_weather\"}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n"

	// 1) 聚合路径：readResponsesStreamToObject 必须产出 arguments 累加结果。
	agg, err := readResponsesStreamToObject(strings.NewReader(streamBody), nil)
	if err != nil {
		t.Fatalf("readResponsesStreamToObject error: %v", err)
	}
	var aggArgs string
	for _, item := range agg.Output {
		if item.Type == "function_call" {
			aggArgs = item.Arguments
		}
	}
	if aggArgs != `{"loc":"Beijing"}` {
		t.Fatalf("aggregated function call arguments = %q, want %q", aggArgs, `{"loc":"Beijing"}`)
	}

	// 2) Responses -> Chat 流式：最终文本必须包含 tool call 及其 arguments。
	chatResp := newResponse(200, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	_, wChat := newTestContext()
	if err := writeOpenAIResponseStreamAsChatStream(wChat, chatResp, nil, ResponseModelContext{}); err != nil {
		t.Fatalf("writeOpenAIResponseStreamAsChatStream error: %v", err)
	}
	chatBody := wChat.Body.String()
	if !strings.Contains(chatBody, "get_weather") {
		t.Fatalf("chat stream must contain tool call name get_weather, body=%s", chatBody)
	}
	if !strings.Contains(chatBody, "Beijing") {
		t.Fatalf("chat stream must contain tool call arguments, body=%s", chatBody)
	}

	// 3) Responses -> Anthropic 流式：必须产出 tool_use 及其 input arguments。
	claudeResp := newResponse(200, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	_, wClaude := newTestContext()
	if err := writeResponsesStreamAsClaudeStream(wClaude, claudeResp, nil, "my-alias"); err != nil {
		t.Fatalf("writeResponsesStreamAsClaudeStream error: %v", err)
	}
	claudeBody := wClaude.Body.String()
	if !strings.Contains(claudeBody, "get_weather") {
		t.Fatalf("claude stream must contain tool_use name get_weather, body=%s", claudeBody)
	}
	if !strings.Contains(claudeBody, "Beijing") {
		t.Fatalf("claude stream must contain tool_use input, body=%s", claudeBody)
	}
}

// TestResponsesEventPublicEntry_BackfillsInputToArguments 直接断言公共 SSE 入口将
// 缺失 arguments 但含 input 的 tool call item 回填为 arguments。
func TestResponsesEventPublicEntry_BackfillsInputToArguments(t *testing.T) {
	data := []byte(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"get_weather","input":{"loc":"Beijing"}},"response":{"id":"resp-1","object":"response","status":"completed","model":"upstream-hy3","output":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"get_weather","input":{"loc":"Beijing"}}]}}`)

	res, err := normalizeResponsesEventData(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventData error: %v", err)
	}

	var item struct {
		Arguments string `json:"arguments"`
	}
	if len(res.Event.Item) > 0 {
		if err := json.Unmarshal(res.Event.Item, &item); err != nil {
			t.Fatalf("unmarshal item: %v", err)
		}
	}
	if item.Arguments != `{"loc":"Beijing"}` {
		t.Fatalf("event.item.arguments = %q, want %q", item.Arguments, `{"loc":"Beijing"}`)
	}

	// 归一化后的原始 data 也应包含 arguments，passthrough 可保留未知字段写回。
	var normalized map[string]any
	if err := json.Unmarshal(res.NormalizedData, &normalized); err != nil {
		t.Fatalf("unmarshal normalized data: %v", err)
	}
	rawItem, _ := normalized["item"].(map[string]any)
	if rawItem["arguments"] != `{"loc":"Beijing"}` {
		t.Fatalf("normalized item.arguments = %#v, want %q", rawItem["arguments"], `{"loc":"Beijing"}`)
	}
}
