package codec

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/token"
)

func copyResponseHeaders(dst, src http.Header) {
	for k, v := range src {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		dst[k] = append([]string(nil), v...)
	}
}

func rewriteTopLevelModel(body []byte, requestedModel string) ([]byte, error) {
	if requestedModel == "" {
		return body, nil
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}

	modelJSON, err := json.Marshal(requestedModel)
	if err != nil {
		return nil, err
	}
	obj["model"] = modelJSON
	return json.Marshal(obj)
}

func rewriteNestedModel(body []byte, field, requestedModel string) ([]byte, error) {
	if requestedModel == "" {
		return body, nil
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}

	nestedBody, ok := obj[field]
	if !ok || len(nestedBody) == 0 {
		return body, nil
	}

	var nested map[string]json.RawMessage
	if err := json.Unmarshal(nestedBody, &nested); err != nil {
		return body, nil
	}

	modelJSON, err := json.Marshal(requestedModel)
	if err != nil {
		return nil, err
	}
	nested["model"] = modelJSON

	rewrittenNested, err := json.Marshal(nested)
	if err != nil {
		return nil, err
	}
	obj[field] = rewrittenNested
	return json.Marshal(obj)
}

// passThroughResponsesResponse forwards an upstream Responses API response back
// to the client, extracting token counts and setting latency along the way.
func passThroughResponsesResponse(w http.ResponseWriter, resp *http.Response, isStream bool, counter TokenCounter, rmc ResponseModelContext) error {
	if isStream {
		return passThroughResponsesStream(w, resp, counter, rmc)
	}

	if isResponsesStreamContentType(resp.Header.Get("Content-Type")) {
		responsesResp, err := readResponsesStreamToObject(resp.Body, counter)
		if err != nil {
			return err
		}
		if rmc.RequestedModel != "" {
			responsesResp.Model = rmc.RequestedModel
		}
		outBody, err := json.Marshal(responsesResp)
		if err != nil {
			return err
		}
		copyResponseHeaders(w.Header(), resp.Header)
		return writeBody(w, resp.StatusCode, "application/json", outBody)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	responsesResp, normalizedBody, compat, err := readResponsesNonStreamBody(body)
	if err != nil {
		return err
	}
	if compat.Changed() {
		slog.Debug("responses compat normalized response body",
			"fixed_paths", compat.FixedPaths,
			"requested_model", rmc.RequestedModel,
		)
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			for _, item := range responsesResp.Output {
				if item.Type == "function_call" {
					sc.AddOutputText(item.Arguments)
				}
				for _, part := range item.Content {
					if part.Text != "" {
						sc.AddOutputText(part.Text)
					}
				}
			}
			sc.ComputeOutputTokens()
		}
	}

	// 非流式 passthrough 复用公共入口已归一化的 body 写回（保留未知字段），
	// 仅通过公共入口做解码、对象级补全与 token 统计。
	outBody, err := rewriteTopLevelModel(normalizedBody, rmc.RequestedModel)
	if err != nil {
		return err
	}

	copyResponseHeaders(w.Header(), resp.Header)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	return writeBody(w, resp.StatusCode, contentType, outBody)
}

func isResponsesStreamContentType(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

func rewriteSSEDataLine(originalLine string, data []byte) string {
	if strings.HasSuffix(originalLine, "\n") {
		return "data: " + string(data) + "\n"
	}
	return "data: " + string(data)
}

func responsesSSEEventData(eventLines []string) (string, []int) {
	var dataParts []string
	var dataLineIndexes []int
	for i, line := range eventLines {
		trimmed := strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}

		data := strings.TrimPrefix(trimmed, "data:")
		if strings.HasPrefix(data, " ") {
			data = strings.TrimPrefix(data, " ")
		}
		dataParts = append(dataParts, data)
		dataLineIndexes = append(dataLineIndexes, i)
	}
	return strings.Join(dataParts, "\n"), dataLineIndexes
}

func rewriteResponsesSSEEventData(eventLines []string, dataLineIndexes []int, data []byte) []string {
	if len(dataLineIndexes) == 0 {
		return append([]string(nil), eventLines...)
	}

	outLines := make([]string, 0, len(eventLines)-len(dataLineIndexes)+1)
	firstDataLine := dataLineIndexes[0]
	nextDataLine := 0
	for i, line := range eventLines {
		if nextDataLine < len(dataLineIndexes) && i == dataLineIndexes[nextDataLine] {
			if i == firstDataLine {
				outLines = append(outLines, rewriteSSEDataLine(line, data))
			}
			nextDataLine++
			continue
		}
		outLines = append(outLines, line)
	}
	return outLines
}

type responsesCompatLogRule struct {
	EventType  string
	FixedPaths string
}

type responsesCompatLogTracker struct {
	requestedModel string
	counts         map[responsesCompatLogRule]int
	total          int
}

func newResponsesCompatLogTracker(requestedModel string) *responsesCompatLogTracker {
	return &responsesCompatLogTracker{
		requestedModel: requestedModel,
		counts:         make(map[responsesCompatLogRule]int),
	}
}

func (t *responsesCompatLogTracker) record(eventIndex int, eventType string, fixedPaths []string) {
	rule := responsesCompatLogRule{
		EventType:  eventType,
		FixedPaths: strings.Join(fixedPaths, ","),
	}
	t.counts[rule]++
	t.total++
	if t.counts[rule] != 1 {
		return
	}
	slog.Debug("responses compat normalized stream event",
		"event_index", eventIndex,
		"event_type", eventType,
		"requested_model", t.requestedModel,
		"fixed_paths", fixedPaths,
	)
}

func (t *responsesCompatLogTracker) summaryRules() []string {
	rules := make([]string, 0, len(t.counts))
	for rule, count := range t.counts {
		rules = append(rules, fmt.Sprintf("%s[%s]=%d", rule.EventType, rule.FixedPaths, count))
	}
	sort.Strings(rules)
	return rules
}

func (t *responsesCompatLogTracker) logSummary(eventCount int) {
	if t.total == 0 {
		return
	}
	slog.Debug("responses compat stream summary",
		"requested_model", t.requestedModel,
		"event_count", eventCount,
		"normalized_events", t.total,
		"suppressed_logs", t.total-len(t.counts),
		"rules", t.summaryRules(),
	)
}

type processedResponsesSSEEvent struct {
	outLines   []string
	drop       bool
	terminal   bool
	eventIndex int
}

func isResponsesTerminalEvent(eventType string) bool {
	switch eventType {
	case "response.completed", "response.failed", "response.incomplete":
		return true
	}
	return false
}

func processResponsesSSEEvent(eventLines []string, counter TokenCounter, rmc ResponseModelContext, eventIndex int, sequencer *responsesStreamSequencer, compatLogs *responsesCompatLogTracker) (processedResponsesSSEEvent, error) {
	data, dataLineIndexes := responsesSSEEventData(eventLines)
	if len(dataLineIndexes) == 0 || data == "" {
		return processedResponsesSSEEvent{outLines: append([]string(nil), eventLines...), eventIndex: eventIndex}, nil
	}
	if data == "[DONE]" {
		return processedResponsesSSEEvent{outLines: rewriteResponsesSSEEventData(eventLines, dataLineIndexes, []byte(data)), eventIndex: eventIndex}, nil
	}

	eventIndex++
	res, normErr := normalizeResponsesEventData([]byte(data))
	if normErr != nil {
		// 无法解析的事件按原始多行结构透传，避免破坏 SSE data 拼接规则。
		return processedResponsesSSEEvent{outLines: append([]string(nil), eventLines...), eventIndex: eventIndex}, nil
	}

	normalizedData := res.NormalizedData
	event := *res.Event
	compat := res.Compat
	processed := processedResponsesSSEEvent{
		terminal:   isResponsesTerminalEvent(event.Type),
		eventIndex: eventIndex,
	}
	if shouldDropResponsesStreamEvent(event.Type) {
		slog.Debug("responses compat dropped stream event",
			"event_index", eventIndex,
			"event_type", event.Type,
			"requested_model", rmc.RequestedModel,
		)
		processed.drop = true
		return processed, nil
	}

	if compat.Changed() {
		compatLogs.record(eventIndex, event.Type, compat.FixedPaths)
	}

	if event.Type == "response.failed" {
		logResponsesStreamFailure(eventIndex, event, rmc)
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			switch event.Type {
			case "response.output_text.delta":
				var textDelta string
				if len(event.Delta) > 0 {
					_ = json.Unmarshal(event.Delta, &textDelta)
				}
				if textDelta != "" {
					sc.AddOutputText(textDelta)
				}
			case "response.function_call_arguments.delta":
				var argsDelta string
				if len(event.Delta) > 0 {
					_ = json.Unmarshal(event.Delta, &argsDelta)
				}
				if argsDelta != "" {
					sc.AddOutputText(argsDelta)
				}
			case "response.completed":
				sc.ComputeOutputTokens()
			}
		}
	}

	rewrittenData := normalizedData
	if (event.Type == "response.created" || event.Type == "response.completed" || event.Type == "response.failed") && rmc.RequestedModel != "" {
		if out, rewriteErr := rewriteNestedModel(normalizedData, "response", rmc.RequestedModel); rewriteErr == nil {
			rewrittenData = out
		} else {
			slog.Warn("responses passthrough rewrite model failed",
				"event_index", eventIndex,
				"event_type", event.Type,
				"requested_model", rmc.RequestedModel,
				"error", rewriteErr,
			)
		}
	}

	rewrittenData, rewriteErr := sequencer.rewrite(rewrittenData)
	if rewriteErr != nil {
		return processedResponsesSSEEvent{}, rewriteErr
	}
	processed.outLines = rewriteResponsesSSEEventData(eventLines, dataLineIndexes, rewrittenData)
	return processed, nil
}

// logResponsesStreamFailure records observability for an upstream Responses
// stream that ended in a `response.failed` event. The HTTP status is still 200,
// so without this the failure is otherwise invisible in our logs and metrics.
func logResponsesStreamFailure(eventIndex int, event dto.ResponsesStreamEvent, rmc ResponseModelContext) {
	code, message := extractResponsesError(event)
	slog.Warn("responses upstream stream failed",
		"event_index", eventIndex,
		"requested_model", rmc.RequestedModel,
		"model_group", rmc.ModelGroup,
		"provider", rmc.WinnerProvider,
		"upstream_model", rmc.WinnerUpstreamModel,
		"error_code", code,
		"error_message", message,
	)
	if rmc.WinnerProvider != "" {
		errType := code
		if errType == "" {
			errType = "response_failed"
		}
		metrics.RecordProviderFailure(rmc.WinnerProvider, errType)
	}
}

// extractResponsesError pulls error code/message from a response.failed event,
// checking both the nested response.error object and a top-level error field.
func extractResponsesError(event dto.ResponsesStreamEvent) (string, string) {
	if len(event.Response) > 0 {
		var parsed struct {
			Error *dto.ResponsesError `json:"error"`
		}
		if err := json.Unmarshal(event.Response, &parsed); err == nil && parsed.Error != nil {
			return parsed.Error.Code, parsed.Error.Message
		}
	}
	if len(event.Error) > 0 {
		var parsed dto.ResponsesError
		if err := json.Unmarshal(event.Error, &parsed); err == nil {
			return parsed.Code, parsed.Message
		}
	}
	return "", ""
}

// passThroughResponsesStream forwards raw SSE chunks from an upstream Responses
// API stream, extracting text for token counting.
func passThroughResponsesStream(w http.ResponseWriter, resp *http.Response, counter TokenCounter, rmc ResponseModelContext) (retErr error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	reader := bufio.NewReader(resp.Body)
	sequencer := &responsesStreamSequencer{}
	compatLogs := newResponsesCompatLogTracker(rmc.RequestedModel)
	eventIndex := 0
	sawTerminal := false
	defer func() {
		compatLogs.logSummary(eventIndex)
		if retErr == nil && !sawTerminal {
			retErr = ErrStreamTruncated
		}
	}()

	var eventLines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		if line != "" {
			eventLines = append(eventLines, line)
		}

		eventEnded := line != "" && strings.TrimSpace(line) == ""
		if (eventEnded || err == io.EOF) && len(eventLines) > 0 {
			processed, processErr := processResponsesSSEEvent(eventLines, counter, rmc, eventIndex, sequencer, compatLogs)
			if processErr != nil {
				return processErr
			}
			eventIndex = processed.eventIndex
			sawTerminal = sawTerminal || processed.terminal
			if !processed.drop {
				for _, outLine := range processed.outLines {
					_, _ = io.WriteString(w, outLine)
				}
				flusher.Flush()
			}
			eventLines = nil
		}

		if err == io.EOF {
			break
		}
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.ComputeOutputTokens()
		}
	}

	return nil
}
