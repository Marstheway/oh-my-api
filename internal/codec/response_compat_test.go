package codec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
)

func TestNormalizeResponsesEventJSON_FixesKnownNullArrays(t *testing.T) {
	data := []byte(`{
		"type":"response.output_item.done",
		"item":{
			"type":"message",
			"content":null
		},
		"response":{
			"output":[
				{
					"type":"message",
					"content":[
						{
							"type":"output_text",
							"annotations":null
						}
					]
				}
			]
		}
	}`)

	out, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventJSON error: %v", err)
	}

	wantPaths := []string{
		"response.output[0].content[0].annotations",
		"response.output[0].content[0].text",
		"item.content",
	}
	if !reflect.DeepEqual(compat.FixedPaths, wantPaths) {
		t.Fatalf("fixed_paths = %v, want %v", compat.FixedPaths, wantPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}

	item := got["item"].(map[string]any)
	if content, ok := item["content"].([]any); !ok || len(content) != 0 {
		t.Fatalf("item.content = %#v, want empty array", item["content"])
	}

	response := got["response"].(map[string]any)
	output := response["output"].([]any)
	part := output[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if annotations, ok := part["annotations"].([]any); !ok || len(annotations) != 0 {
		t.Fatalf("annotations = %#v, want empty array", part["annotations"])
	}
	if text, ok := part["text"].(string); !ok || text != "" {
		t.Fatalf("text = %#v, want empty string", part["text"])
	}
}

func TestNormalizeResponsesEventJSON_FixesMissingMessageContent(t *testing.T) {
	data := []byte(`{
		"type":"response.output_item.done",
		"item":{
			"type":"message"
		},
		"response":{
			"output":[
				{
					"type":"message"
				}
			]
		}
	}`)

	out, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventJSON error: %v", err)
	}

	wantPaths := []string{
		"response.output[0].content",
		"item.content",
	}
	if !reflect.DeepEqual(compat.FixedPaths, wantPaths) {
		t.Fatalf("fixed_paths = %v, want %v", compat.FixedPaths, wantPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}

	item := got["item"].(map[string]any)
	if content, ok := item["content"].([]any); !ok || len(content) != 0 {
		t.Fatalf("item.content = %#v, want empty array", item["content"])
	}

	response := got["response"].(map[string]any)
	output := response["output"].([]any)
	msg := output[0].(map[string]any)
	if content, ok := msg["content"].([]any); !ok || len(content) != 0 {
		t.Fatalf("response.output[0].content = %#v, want empty array", msg["content"])
	}
}

func TestNormalizeResponsesEventJSON_FixesMissingAnnotations(t *testing.T) {
	data := []byte(`{
		"type":"response.content_part.done",
		"part":{
			"type":"output_text"
		},
		"item":{
			"type":"message",
			"content":[
				{
					"type":"output_text"
				}
			]
		},
		"response":{
			"output":[
				{
					"type":"message",
					"content":[
						{
							"type":"output_text"
						}
					]
				}
			]
		}
	}`)

	out, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventJSON error: %v", err)
	}

	wantPaths := []string{
		"response.output[0].content[0].annotations",
		"response.output[0].content[0].text",
		"item.content[0].annotations",
		"item.content[0].text",
		"part.annotations",
		"part.text",
	}
	if !reflect.DeepEqual(compat.FixedPaths, wantPaths) {
		t.Fatalf("fixed_paths = %v, want %v", compat.FixedPaths, wantPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}

	part := got["part"].(map[string]any)
	if annotations, ok := part["annotations"].([]any); !ok || len(annotations) != 0 {
		t.Fatalf("part.annotations = %#v, want empty array", part["annotations"])
	}
	if text, ok := part["text"].(string); !ok || text != "" {
		t.Fatalf("part.text = %#v, want empty string", part["text"])
	}
}

func TestNormalizeResponsesEventJSON_FixesMissingPartObject(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		wantPart  map[string]any
		wantPaths []string
	}{
		{
			name:      "content part done",
			eventType: "response.content_part.done",
			wantPart: map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
			wantPaths: []string{"part"},
		},
		{
			name:      "reasoning summary part added",
			eventType: "response.reasoning_summary_part.added",
			wantPart: map[string]any{
				"type": "summary_text",
				"text": "",
			},
			wantPaths: []string{"summary_index", "part"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(`{"type":"` + tt.eventType + `"}`)

			out, compat, err := normalizeResponsesEventJSON(data)
			if err != nil {
				t.Fatalf("normalizeResponsesEventJSON error: %v", err)
			}
			if !reflect.DeepEqual(compat.FixedPaths, tt.wantPaths) {
				t.Fatalf("fixed_paths = %v, want %v", compat.FixedPaths, tt.wantPaths)
			}

			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unmarshal normalized event: %v", err)
			}
			if !reflect.DeepEqual(got["part"], tt.wantPart) {
				t.Fatalf("part = %#v, want %#v", got["part"], tt.wantPart)
			}
		})
	}
}

func TestNormalizeResponsesEventJSON_FixesMissingFunctionCallArguments(t *testing.T) {
	data := []byte(`{
		"type":"response.output_item.done",
		"item":{
			"type":"function_call",
			"id":"fc-1",
			"call_id":"call-1",
			"name":"get_weather"
		},
		"response":{
			"output":[
				{
					"type":"function_call",
					"id":"fc-1",
					"call_id":"call-1",
					"name":"get_weather"
				}
			]
		}
	}`)

	out, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventJSON error: %v", err)
	}

	wantPaths := []string{
		"response.output[0].arguments",
		"item.arguments",
	}
	if !reflect.DeepEqual(compat.FixedPaths, wantPaths) {
		t.Fatalf("fixed_paths = %v, want %v", compat.FixedPaths, wantPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}

	item := got["item"].(map[string]any)
	if args, ok := item["arguments"].(string); !ok || args != "" {
		t.Fatalf("item.arguments = %#v, want empty string", item["arguments"])
	}

	response := got["response"].(map[string]any)
	output := response["output"].([]any)
	call := output[0].(map[string]any)
	if args, ok := call["arguments"].(string); !ok || args != "" {
		t.Fatalf("response.output[0].arguments = %#v, want empty string", call["arguments"])
	}
}

func TestNormalizeResponsesEventJSON_FixesMissingArgumentsForUnknownCallTypeFromInput(t *testing.T) {
	data := []byte(`{
		"type":"response.output_item.done",
		"item":{
			"type":"computer_call",
			"id":"cc-1",
			"call_id":"call-1",
			"name":"computer",
			"input":{"cmd":"pwd"}
		},
		"response":{
			"output":[
				{
					"type":"computer_call",
					"id":"cc-1",
					"call_id":"call-1",
					"name":"computer",
					"input":{"cmd":"pwd"}
				}
			]
		}
	}`)

	out, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventJSON error: %v", err)
	}

	wantPaths := []string{
		"response.output[0].arguments",
		"item.arguments",
	}
	if !reflect.DeepEqual(compat.FixedPaths, wantPaths) {
		t.Fatalf("fixed_paths = %v, want %v", compat.FixedPaths, wantPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}

	item := got["item"].(map[string]any)
	if args, ok := item["arguments"].(string); !ok || args != `{"cmd":"pwd"}` {
		t.Fatalf("item.arguments = %#v, want JSON string", item["arguments"])
	}

	response := got["response"].(map[string]any)
	output := response["output"].([]any)
	call := output[0].(map[string]any)
	if args, ok := call["arguments"].(string); !ok || args != `{"cmd":"pwd"}` {
		t.Fatalf("response.output[0].arguments = %#v, want JSON string", call["arguments"])
	}
}

func TestNormalizeResponsesEventJSON_FixesMissingFunctionCallArgumentsDoneField(t *testing.T) {
	data := []byte(`{
		"type":"response.function_call_arguments.done",
		"item_id":"fc-1"
	}`)

	out, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventJSON error: %v", err)
	}

	wantPaths := []string{"arguments"}
	if !reflect.DeepEqual(compat.FixedPaths, wantPaths) {
		t.Fatalf("fixed_paths = %v, want %v", compat.FixedPaths, wantPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}
	if args, ok := got["arguments"].(string); !ok || args != "" {
		t.Fatalf("arguments = %#v, want empty string", got["arguments"])
	}
}

func TestNormalizeResponsesEventJSON_FixesMissingOutputTextDoneText(t *testing.T) {
	data := []byte(`{
		"type":"response.output_text.done",
		"item_id":"msg-1",
		"output_index":0,
		"content_index":0
	}`)

	out, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventJSON error: %v", err)
	}
	if !reflect.DeepEqual(compat.FixedPaths, []string{"text"}) {
		t.Fatalf("fixed_paths = %v, want [text]", compat.FixedPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}
	if text, ok := got["text"].(string); !ok || text != "" {
		t.Fatalf("text = %#v, want empty string", got["text"])
	}
}

func TestNormalizeResponsesEventJSON_FixesReasoningSummaryPartText(t *testing.T) {
	data := []byte(`{
		"type":"response.reasoning_summary_part.added",
		"item_id":"rs-1",
		"output_index":0,
		"summary_index":0,
		"part":{"type":"summary_text"}
	}`)

	out, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventJSON error: %v", err)
	}
	if !reflect.DeepEqual(compat.FixedPaths, []string{"part.text"}) {
		t.Fatalf("fixed_paths = %v, want [part.text]", compat.FixedPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}
	part := got["part"].(map[string]any)
	if text, ok := part["text"].(string); !ok || text != "" {
		t.Fatalf("part.text = %#v, want empty string", part["text"])
	}
	if _, exists := part["annotations"]; exists {
		t.Fatalf("summary_text part must not gain annotations: %#v", part)
	}
}

func TestNormalizeResponsesEventJSON_FixesReasoningDoneText(t *testing.T) {
	for _, eventType := range []string{
		"response.reasoning_summary_text.done",
		"response.reasoning_text.done",
	} {
		t.Run(eventType, func(t *testing.T) {
			extra := ""
			if eventType == "response.reasoning_summary_text.done" {
				extra = `,"summary_index":0`
			}
			data := []byte(`{"type":"` + eventType + `","item_id":"rs-1","output_index":0` + extra + `}`)
			out, compat, err := normalizeResponsesEventJSON(data)
			if err != nil {
				t.Fatalf("normalizeResponsesEventJSON error: %v", err)
			}
			if !reflect.DeepEqual(compat.FixedPaths, []string{"text"}) {
				t.Fatalf("fixed_paths = %v, want [text]", compat.FixedPaths)
			}
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unmarshal normalized event: %v", err)
			}
			if text, ok := got["text"].(string); !ok || text != "" {
				t.Fatalf("text = %#v, want empty string", got["text"])
			}
		})
	}
}

func TestNormalizeResponsesEventJSON_FixesReasoningItemSummaryText(t *testing.T) {
	data := []byte(`{
		"type":"response.output_item.done",
		"item":{
			"type":"reasoning",
			"id":"rs-1",
			"summary":[{"type":"summary_text"}]
		}
	}`)

	out, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventJSON error: %v", err)
	}
	if !reflect.DeepEqual(compat.FixedPaths, []string{"item.summary[0].text"}) {
		t.Fatalf("fixed_paths = %v, want [item.summary[0].text]", compat.FixedPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}
	item := got["item"].(map[string]any)
	summary := item["summary"].([]any)
	part := summary[0].(map[string]any)
	if text, ok := part["text"].(string); !ok || text != "" {
		t.Fatalf("summary text = %#v, want empty string", part["text"])
	}
}
func TestNormalizeResponsesResponseJSON_FixesOutputNull(t *testing.T) {
	body := []byte(`{"id":"resp-1","object":"response","created_at":1,"model":"hy3","status":"completed","output":null,"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`)

	out, compat, err := normalizeResponsesResponseJSON(body)
	if err != nil {
		t.Fatalf("normalizeResponsesResponseJSON error: %v", err)
	}
	wantPaths := []string{
		"output.usage.input_tokens_details",
		"output.usage.output_tokens_details",
		"output.usage.completion_tokens_details",
		"output",
	}
	if !reflect.DeepEqual(compat.FixedPaths, wantPaths) {
		t.Fatalf("fixed_paths = %v, want %v", compat.FixedPaths, wantPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized body: %v", err)
	}
	if output, ok := got["output"].([]any); !ok || len(output) != 0 {
		t.Fatalf("output = %#v, want empty slice", got["output"])
	}
	usage := got["usage"].(map[string]any)
	if _, ok := usage["input_tokens_details"].(map[string]any); !ok {
		t.Fatalf("input_tokens_details = %#v, want object", usage["input_tokens_details"])
	}
	if _, ok := usage["output_tokens_details"].(map[string]any); !ok {
		t.Fatalf("output_tokens_details = %#v, want object", usage["output_tokens_details"])
	}
	if _, ok := usage["completion_tokens_details"].(map[string]any); !ok {
		t.Fatalf("completion_tokens_details = %#v, want object", usage["completion_tokens_details"])
	}
}

func TestPassThroughResponsesStream_NormalizesKnownNullArrays(t *testing.T) {
	streamBody := "" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-hy3\",\"output\":null}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":null}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"output\":null}}\n\n"

	resp := newResponse(200, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesResponse(w, resp, true, nil, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("passThroughResponsesResponse stream error: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, `"output":null`) {
		t.Fatalf("stream output must not contain output:null, body=%s", body)
	}
	if strings.Contains(body, `"content":null`) {
		t.Fatalf("stream output must not contain content:null, body=%s", body)
	}
	if !strings.Contains(body, `"output":[]`) {
		t.Fatalf("stream output must contain output:[], body=%s", body)
	}
	if !strings.Contains(body, `"content":[]`) {
		t.Fatalf("stream output must contain content:[], body=%s", body)
	}
	if strings.Contains(body, "upstream-hy3") {
		t.Fatalf("stream output must use requested model, body=%s", body)
	}
}

func TestPassThroughResponsesStream_NormalizesMissingMessageContent(t *testing.T) {
	streamBody := "" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-hy3\",\"output\":null}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"status\":\"in_progress\",\"role\":\"assistant\"}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"status\":\"completed\",\"role\":\"assistant\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"output\":[{\"type\":\"message\",\"id\":\"msg-1\",\"status\":\"completed\",\"role\":\"assistant\"}]}}\n\n"

	resp := newResponse(200, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesResponse(w, resp, true, nil, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("passThroughResponsesResponse stream error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"content":[]`) {
		t.Fatalf("stream output must contain content:[], body=%s", body)
	}
}

func TestPassThroughResponsesStream_NormalizesMissingAnnotations(t *testing.T) {
	streamBody := "" +
		"data: {\"type\":\"response.content_part.added\",\"part\":{\"type\":\"output_text\"}}\n\n" +
		"data: {\"type\":\"response.content_part.done\",\"part\":{\"type\":\"output_text\"}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\"}]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"

	resp := newResponse(200, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesResponse(w, resp, true, nil, ResponseModelContext{}); err != nil {
		t.Fatalf("passThroughResponsesResponse stream error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"annotations":[]`) {
		t.Fatalf("stream output must contain annotations:[], body=%s", body)
	}
	if !strings.Contains(body, `"text":""`) {
		t.Fatalf("stream output must contain text:\"\", body=%s", body)
	}
}

func TestPassThroughResponsesStream_NormalizesMissingPartObject(t *testing.T) {
	streamBody := "" +
		"data: {\"type\":\"response.content_part.done\"}\n\n" +
		"data: {\"type\":\"response.reasoning_summary_part.added\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"

	resp := newResponse(200, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesResponse(w, resp, true, nil, ResponseModelContext{}); err != nil {
		t.Fatalf("passThroughResponsesResponse stream error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"type":"output_text"`) {
		t.Fatalf("stream output must contain default output_text part, body=%s", body)
	}
	if !strings.Contains(body, `"annotations":[]`) {
		t.Fatalf("stream output must contain annotations for content part, body=%s", body)
	}
	if !strings.Contains(body, `"type":"summary_text"`) {
		t.Fatalf("stream output must contain default summary_text part, body=%s", body)
	}
	if !strings.Contains(body, `"summary_index":0`) {
		t.Fatalf("stream output must contain normalized summary_index, body=%s", body)
	}
}

func TestPassThroughResponsesStream_NormalizesMissingFunctionCallArguments(t *testing.T) {
	streamBody := "" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"call-1\",\"name\":\"get_weather\"}}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc-1\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"call-1\",\"name\":\"get_weather\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"call-1\",\"name\":\"get_weather\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"

	resp := newResponse(200, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesResponse(w, resp, true, nil, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("passThroughResponsesResponse stream error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"arguments":""`) {
		t.Fatalf("stream output must contain arguments:\"\", body=%s", body)
	}
}

func TestPassThroughResponsesStream_NormalizesUnknownCallTypeArgumentsFromInput(t *testing.T) {
	streamBody := "" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"computer_call\",\"id\":\"cc-1\",\"call_id\":\"call-1\",\"name\":\"computer\",\"input\":{\"cmd\":\"pwd\"}}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"computer_call\",\"id\":\"cc-1\",\"call_id\":\"call-1\",\"name\":\"computer\",\"input\":{\"cmd\":\"pwd\"}}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"

	resp := newResponse(200, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesResponse(w, resp, true, nil, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("passThroughResponsesResponse stream error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"arguments":"{\"cmd\":\"pwd\"}"`) {
		t.Fatalf("stream output must contain normalized arguments, body=%s", body)
	}
}

func TestPassThroughResponsesStream_NormalizesMissingUsageDetails(t *testing.T) {
	streamBody := "" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-hy3\",\"output\":[]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"

	resp := newResponse(200, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesResponse(w, resp, true, nil, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("passThroughResponsesResponse stream error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"input_tokens_details":{"cached_tokens":0}`) {
		t.Fatalf("stream output must contain input_tokens_details, body=%s", body)
	}
	if !strings.Contains(body, `"output_tokens_details":{"reasoning_tokens":0}`) {
		t.Fatalf("stream output must contain output_tokens_details, body=%s", body)
	}
}

func TestPassThroughResponsesResponse_NonStreamBody_NormalizesOutputNull(t *testing.T) {
	body := `{"id":"resp-1","object":"response","created_at":1,"model":"upstream-hy3","status":"completed","output":null,"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`
	resp := newResponse(200, body, map[string]string{"Content-Type": "application/json"})
	_, w := newTestContext()
	counter := token.NewStreamCounter(0)

	if err := passThroughResponsesResponse(w, resp, false, counter, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("passThroughResponsesResponse non-stream error: %v", err)
	}

	if strings.Contains(w.Body.String(), `"output":null`) {
		t.Fatalf("response body must not contain output:null, body=%s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"output_tokens_details"`) {
		t.Fatalf("response body must contain output_tokens_details, body=%s", w.Body.String())
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if out.Model != "my-alias" {
		t.Fatalf("model = %q, want my-alias", out.Model)
	}
	if out.Output == nil || len(out.Output) != 0 {
		t.Fatalf("output = %#v, want empty slice", out.Output)
	}
}

func TestNormalizeResponsesEventJSON_FixesMissingUsageDetails(t *testing.T) {
	data := []byte(`{
		"type":"response.completed",
		"response":{
			"id":"resp-1",
			"object":"response",
			"status":"completed",
			"usage":{
				"input_tokens":1,
				"output_tokens":2,
				"total_tokens":3
			}
		}
	}`)

	out, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventJSON error: %v", err)
	}

	wantPaths := []string{
		"response.output.usage.input_tokens_details",
		"response.output.usage.output_tokens_details",
		"response.output.usage.completion_tokens_details",
		"response.output",
	}
	if !reflect.DeepEqual(compat.FixedPaths, wantPaths) {
		t.Fatalf("fixed_paths = %v, want %v", compat.FixedPaths, wantPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}
	response := got["response"].(map[string]any)
	if output, ok := response["output"].([]any); !ok || len(output) != 0 {
		t.Fatalf("output = %#v, want empty array", response["output"])
	}
	usage := response["usage"].(map[string]any)
	if _, ok := usage["input_tokens_details"].(map[string]any); !ok {
		t.Fatalf("input_tokens_details = %#v, want object", usage["input_tokens_details"])
	}
	if _, ok := usage["output_tokens_details"].(map[string]any); !ok {
		t.Fatalf("output_tokens_details = %#v, want object", usage["output_tokens_details"])
	}
	if _, ok := usage["completion_tokens_details"].(map[string]any); !ok {
		t.Fatalf("completion_tokens_details = %#v, want object", usage["completion_tokens_details"])
	}
}

func TestNormalizeResponsesEventJSON_FixesMissingUsageObject(t *testing.T) {
	data := []byte(`{
		"type":"response.completed",
		"response":{
			"id":"resp-1",
			"object":"response",
			"status":"completed"
		}
	}`)

	out, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventJSON error: %v", err)
	}

	wantPaths := []string{
		"response.output.usage",
		"response.output",
	}
	if !reflect.DeepEqual(compat.FixedPaths, wantPaths) {
		t.Fatalf("fixed_paths = %v, want %v", compat.FixedPaths, wantPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}
	response := got["response"].(map[string]any)
	if output, ok := response["output"].([]any); !ok || len(output) != 0 {
		t.Fatalf("output = %#v, want empty array", response["output"])
	}
	usage := response["usage"].(map[string]any)
	if _, ok := usage["input_tokens_details"].(map[string]any); !ok {
		t.Fatalf("input_tokens_details = %#v, want object", usage["input_tokens_details"])
	}
	if _, ok := usage["output_tokens_details"].(map[string]any); !ok {
		t.Fatalf("output_tokens_details = %#v, want object", usage["output_tokens_details"])
	}
}

func TestNormalizeResponsesEventJSON_FixesPromptTokensDetailsAlias(t *testing.T) {
	data := []byte(`{
		"type":"response.completed",
		"response":{
			"id":"resp-1",
			"object":"response",
			"status":"completed",
			"usage":{
				"input_tokens":1,
				"output_tokens":2,
				"total_tokens":3,
				"prompt_tokens_details":{
					"cached_tokens":4
				}
			}
		}
	}`)

	out, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventJSON error: %v", err)
	}

	wantPaths := []string{
		"response.output.usage.input_tokens_details",
		"response.output.usage.output_tokens_details",
		"response.output.usage.completion_tokens_details",
		"response.output",
	}
	if !reflect.DeepEqual(compat.FixedPaths, wantPaths) {
		t.Fatalf("fixed_paths = %v, want %v", compat.FixedPaths, wantPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}
	response := got["response"].(map[string]any)
	usage := response["usage"].(map[string]any)
	inputDetails := usage["input_tokens_details"].(map[string]any)
	if inputDetails["cached_tokens"] != float64(4) {
		t.Fatalf("cached_tokens = %#v, want 4", inputDetails["cached_tokens"])
	}
	if _, ok := usage["completion_tokens_details"].(map[string]any); !ok {
		t.Fatalf("completion_tokens_details = %#v, want object", usage["completion_tokens_details"])
	}
}

func TestNormalizeResponsesEventJSON_BackfillsCompletionTokensDetailsFromOutputTokensDetails(t *testing.T) {
	data := []byte(`{
		"type":"response.completed",
		"response":{
			"id":"resp-1",
			"object":"response",
			"status":"completed",
			"usage":{
				"input_tokens":1,
				"output_tokens":2,
				"total_tokens":3,
				"output_tokens_details":{
					"reasoning_tokens":5
				}
			}
		}
	}`)

	out, compat, err := normalizeResponsesEventJSON(data)
	if err != nil {
		t.Fatalf("normalizeResponsesEventJSON error: %v", err)
	}

	wantPaths := []string{
		"response.output.usage.input_tokens_details",
		"response.output.usage.completion_tokens_details",
		"response.output",
	}
	if !reflect.DeepEqual(compat.FixedPaths, wantPaths) {
		t.Fatalf("fixed_paths = %v, want %v", compat.FixedPaths, wantPaths)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}
	response := got["response"].(map[string]any)
	usage := response["usage"].(map[string]any)
	completionDetails := usage["completion_tokens_details"].(map[string]any)
	if completionDetails["reasoning_tokens"] != float64(5) {
		t.Fatalf("reasoning_tokens = %#v, want 5", completionDetails["reasoning_tokens"])
	}
}

func TestPassThroughResponsesResponse_NonStreamSSEBody_NormalizesOutputNull(t *testing.T) {
	sseBody := "" +
		"event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-sse\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-hy3\",\"output\":null}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-sse\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"output\":null,\"usage\":{\"input_tokens\":3,\"output_tokens\":0,\"total_tokens\":3}}}\n\n"
	resp := newResponse(200, sseBody, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesResponse(w, resp, false, nil, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("passThroughResponsesResponse non-stream SSE error: %v", err)
	}

	if strings.Contains(w.Body.String(), `"output":null`) {
		t.Fatalf("aggregated response body must not contain output:null, body=%s", w.Body.String())
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal aggregated response body: %v", err)
	}
	if out.Model != "my-alias" {
		t.Fatalf("model = %q, want my-alias", out.Model)
	}
	if out.Output == nil || len(out.Output) != 0 {
		t.Fatalf("output = %#v, want empty slice", out.Output)
	}
}

func TestPassThroughResponsesResponse_NonStreamSSEBody_NormalizesMissingUsageDetails(t *testing.T) {
	sseBody := "" +
		"event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-sse\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-hy3\",\"output\":[]}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-sse\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n"
	resp := newResponse(200, sseBody, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesResponse(w, resp, false, nil, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("passThroughResponsesResponse non-stream SSE error: %v", err)
	}

	var out dto.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal aggregated response body: %v", err)
	}
	if out.Usage.InputTokensDetails == nil {
		t.Fatalf("input_tokens_details = nil")
	}
	if out.Usage.OutputTokensDetails == nil {
		t.Fatalf("output_tokens_details = nil")
	}
}

func TestPassThroughResponsesStream_NormalizesMissingUsageObject(t *testing.T) {
	streamBody := "" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-hy3\",\"output\":[]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-hy3\",\"output\":[]}}\n\n"

	resp := newResponse(200, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()

	if err := passThroughResponsesResponse(w, resp, true, nil, ResponseModelContext{RequestedModel: "my-alias"}); err != nil {
		t.Fatalf("passThroughResponsesResponse stream error: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"usage":{`) {
		t.Fatalf("stream output must contain usage object, body=%s", body)
	}
	if !strings.Contains(body, `"input_tokens_details":{"cached_tokens":0}`) {
		t.Fatalf("stream output must contain input_tokens_details, body=%s", body)
	}
	if !strings.Contains(body, `"output_tokens_details":{"reasoning_tokens":0}`) {
		t.Fatalf("stream output must contain output_tokens_details, body=%s", body)
	}
}

func TestPassThroughResponsesStream_RewritesSequenceNumbersAfterCompatFiltering(t *testing.T) {
	streamBody := "" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream\",\"output\":[]}}\n\n" +
		"data: {\"type\":\"keepalive\",\"sequence_number\":99}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":\"bad\",\"delta\":\"hello\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"sequence_number\":42,\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream\",\"output\":[]}}\n\n" +
		"data: [DONE]\n\n"

	resp := newResponse(200, streamBody, map[string]string{"Content-Type": "text/event-stream"})
	_, w := newTestContext()
	if err := passThroughResponsesResponse(w, resp, true, nil, ResponseModelContext{}); err != nil {
		t.Fatalf("passThroughResponsesResponse stream error: %v", err)
	}

	var sequences []int
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("unmarshal event %q: %v", data, err)
		}
		sequence, ok := event["sequence_number"].(float64)
		if !ok {
			t.Fatalf("event %q missing numeric sequence_number: %#v", event["type"], event["sequence_number"])
		}
		sequences = append(sequences, int(sequence))
	}

	if !reflect.DeepEqual(sequences, []int{0, 1, 2}) {
		t.Fatalf("sequence numbers = %v, want [0 1 2]", sequences)
	}
	if strings.Contains(w.Body.String(), `"type":"keepalive"`) {
		t.Fatalf("stream body should not contain keepalive: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "data: [DONE]") {
		t.Fatalf("stream body should preserve [DONE]: %s", w.Body.String())
	}
}

func TestResponsesStreamWriter_InstancesStartAtZero(t *testing.T) {
	for i := 0; i < 2; i++ {
		_, w := newTestContext()
		writer := newResponsesStreamWriter(w)
		if err := writer.writeEvent(map[string]any{
			"type":            "response.created",
			"sequence_number": 100,
		}); err != nil {
			t.Fatalf("writeEvent error: %v", err)
		}

		var event map[string]any
		data := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(w.Body.String()), "data: "))
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("unmarshal written event: %v", err)
		}
		if got := int(event["sequence_number"].(float64)); got != 0 {
			t.Fatalf("writer %d first sequence_number = %d, want 0", i, got)
		}
	}
}

func TestNormalizeResponsesEventJSON_FixesReasoningSummaryIndex(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		extra     string
		wantPaths []string
	}{
		{
			name:      "part added missing",
			eventType: "response.reasoning_summary_part.added",
			extra:     `,"part":{"type":"summary_text","text":""}`,
			wantPaths: []string{"summary_index"},
		},
		{
			name:      "part done invalid",
			eventType: "response.reasoning_summary_part.done",
			extra:     `,"summary_index":"bad","part":{"type":"summary_text","text":"done"}`,
			wantPaths: []string{"summary_index"},
		},
		{
			name:      "text delta missing",
			eventType: "response.reasoning_summary_text.delta",
			extra:     `,"delta":"step"`,
			wantPaths: []string{"summary_index"},
		},
		{
			name:      "text done missing",
			eventType: "response.reasoning_summary_text.done",
			extra:     `,"text":"step"`,
			wantPaths: []string{"summary_index"},
		},
		{
			name:      "valid preserved",
			eventType: "response.reasoning_summary_text.done",
			extra:     `,"summary_index":2,"text":"step"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(`{"type":"` + tt.eventType + `","item_id":"rs-1","output_index":0` + tt.extra + `}`)
			out, compat, err := normalizeResponsesEventJSON(data)
			if err != nil {
				t.Fatalf("normalizeResponsesEventJSON error: %v", err)
			}
			if !reflect.DeepEqual(compat.FixedPaths, tt.wantPaths) {
				t.Fatalf("fixed_paths = %v, want %v", compat.FixedPaths, tt.wantPaths)
			}

			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unmarshal normalized event: %v", err)
			}
			wantIndex := 0
			if tt.name == "valid preserved" {
				wantIndex = 2
			}
			if gotIndex, ok := got["summary_index"].(float64); !ok || int(gotIndex) != wantIndex {
				t.Fatalf("summary_index = %#v, want %d", got["summary_index"], wantIndex)
			}
		})
	}
}
