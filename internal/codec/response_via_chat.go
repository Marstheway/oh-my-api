package codec

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/token"
	"github.com/gin-gonic/gin"
)

// readAnthropicStreamToObject 消费 Claude SSE 流并积累为一个非流式 ClaudeResponse 对象。
// 仅做解析与积累，不写任何响应给客户端。
func readAnthropicStreamToObject(body io.Reader, counter TokenCounter) (*dto.ClaudeResponse, error) {
	out := &dto.ClaudeResponse{
		Type: "message",
		Role: "assistant",
	}

	var currentToolBlock *dto.ContentBlock
	var currentTextBuilder strings.Builder

	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		if line == "" && err == io.EOF {
			break
		}
		line = strings.TrimSuffix(line, "\n")
		if !strings.HasPrefix(line, "data: ") {
			if err == io.EOF {
				break
			}
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "" {
			if err == io.EOF {
				break
			}
			continue
		}

		var event dto.ClaudeStreamEvent
		if jsonErr := json.Unmarshal([]byte(data), &event); jsonErr != nil {
			if err == io.EOF {
				break
			}
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				out.ID = event.Message.ID
				out.Model = event.Message.Model
				out.Usage.InputTokens = event.Message.Usage.InputTokens
			}
		case "content_block_start":
			if event.ContentBlock != nil {
				cb := *event.ContentBlock
				currentToolBlock = &cb
			}
		case "content_block_delta":
			if event.Delta != nil {
				switch event.Delta.Type {
				case "text_delta":
					currentTextBuilder.WriteString(event.Delta.Text)
					if counter != nil {
						if sc, ok := counter.(*token.StreamCounter); ok {
							sc.AddOutputText(event.Delta.Text)
						}
					}
				case "input_json_delta":
					if currentToolBlock != nil {
						if acc, ok := currentToolBlock.Input.(string); ok {
							currentToolBlock.Input = acc + event.Delta.PartialJSON
						} else {
							currentToolBlock.Input = event.Delta.PartialJSON
						}
					}
				}
			}
		case "content_block_stop":
			if currentToolBlock != nil {
				if currentToolBlock.Type == "tool_use" {
					// 解析 input JSON 字符串为 map
					if rawStr, ok := currentToolBlock.Input.(string); ok {
						var inputMap map[string]any
						if jsonErr := json.Unmarshal([]byte(rawStr), &inputMap); jsonErr == nil {
							currentToolBlock.Input = inputMap
						}
					}
					out.Content = append(out.Content, *currentToolBlock)
				}
				currentToolBlock = nil
			}
		case "message_delta":
			if event.Delta != nil {
				if event.Delta.StopReason != "" {
					out.StopReason = &event.Delta.StopReason
				}
			}
		case "message_stop":
			// 流结束
		}

		if err == io.EOF {
			break
		}
	}

	// 文本内容追加
	if currentTextBuilder.Len() > 0 {
		out.Content = append([]dto.ContentBlock{{Type: "text", Text: currentTextBuilder.String()}}, out.Content...)
	}

	return out, nil
}

// readResponsesStreamToObject 消费 Responses API SSE 流并积累为一个非流式 ResponsesResponse 对象。
// 仅做解析与积累，不写任何响应给客户端。
func readResponsesStreamToObject(body io.Reader, counter TokenCounter) (*dto.ResponsesResponse, error) {
	out := &dto.ResponsesResponse{
		Object: "response",
		Status: "completed",
	}

	var textBuilder strings.Builder
	toolArgs := make(map[string]string)     // item_id -> accumulated arguments
	toolCallIDs := make(map[string]string)  // item_id -> call_id
	toolCallNames := make(map[string]string) // item_id -> name

	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		if line == "" && err == io.EOF {
			break
		}
		line = strings.TrimSuffix(line, "\n")
		if !strings.HasPrefix(line, "data: ") {
			if err == io.EOF {
				break
			}
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "" {
			if err == io.EOF {
				break
			}
			continue
		}

		var event dto.ResponsesStreamEvent
		if jsonErr := json.Unmarshal([]byte(data), &event); jsonErr != nil {
			if err == io.EOF {
				break
			}
			continue
		}

		switch event.Type {
		case "response.created":
			if len(event.Response) > 0 {
				var r struct {
					ID    string `json:"id"`
					Model string `json:"model"`
				}
				if jsonErr := json.Unmarshal(event.Response, &r); jsonErr == nil {
					out.ID = r.ID
					out.Model = r.Model
				}
			}
		case "response.output_item.added":
			// 记录 function_call item
			if len(event.Item) > 0 {
				var item struct {
					Type   string `json:"type"`
					ID     string `json:"id"`
					CallID string `json:"call_id"`
					Name   string `json:"name"`
				}
				if jsonErr := json.Unmarshal(event.Item, &item); jsonErr == nil {
					if item.Type == "function_call" {
						toolArgs[item.ID] = ""
						toolCallIDs[item.ID] = item.CallID
						toolCallNames[item.ID] = item.Name
					}
				}
			}
		case "response.output_text.delta":
			var textDelta string
			if len(event.Delta) > 0 {
				_ = json.Unmarshal(event.Delta, &textDelta)
			}
			if textDelta != "" {
				textBuilder.WriteString(textDelta)
				if counter != nil {
					if sc, ok := counter.(*token.StreamCounter); ok {
						sc.AddOutputText(textDelta)
					}
				}
			}
		case "response.function_call_arguments.delta":
			var argsDelta string
			if len(event.Delta) > 0 {
				_ = json.Unmarshal(event.Delta, &argsDelta)
			}
			if argsDelta != "" && event.ItemID != "" {
				toolArgs[event.ItemID] += argsDelta
				if counter != nil {
					if sc, ok := counter.(*token.StreamCounter); ok {
						sc.AddOutputText(argsDelta)
					}
				}
			}
		case "response.completed":
			if len(event.Response) > 0 {
				var r struct {
					ID     string             `json:"id"`
					Model  string             `json:"model"`
					Status string             `json:"status"`
					Usage  *dto.ResponsesUsage `json:"usage"`
				}
				if jsonErr := json.Unmarshal(event.Response, &r); jsonErr == nil {
					if r.ID != "" {
						out.ID = r.ID
					}
					if r.Model != "" {
						out.Model = r.Model
					}
					if r.Status != "" {
						out.Status = r.Status
					}
					if r.Usage != nil {
						out.Usage = *r.Usage
					}
				}
			}
		case "response.failed":
			var r struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if len(event.Response) > 0 {
				_ = json.Unmarshal(event.Response, &r)
			}
			if r.Error != nil {
				return nil, fmt.Errorf("response failed [%s]: %s", r.Error.Code, r.Error.Message)
			}
			return nil, fmt.Errorf("response failed")
		}

		if err == io.EOF {
			break
		}
	}

	// 整理输出
	if textBuilder.Len() > 0 {
		out.Output = append(out.Output, dto.ResponsesOutput{
			Type:   "message",
			ID:     out.ID,
			Status: "completed",
			Role:   "assistant",
			Content: []dto.ResponsesOutputContent{
				{Type: "output_text", Text: textBuilder.String()},
			},
		})
	}
	for itemID, args := range toolArgs {
		out.Output = append(out.Output, dto.ResponsesOutput{
			Type:      "function_call",
			CallID:    toolCallIDs[itemID],
			Name:      toolCallNames[itemID],
			Arguments: args,
		})
	}

	return out, nil
}

// writeClaudeObjectAsStream 将 ClaudeResponse 对象以 Anthropic SSE 格式写给客户端。
func writeClaudeObjectAsStream(c *gin.Context, claudeResp *dto.ClaudeResponse, counter TokenCounter) error {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	writeEvent := func(event dto.ClaudeStreamEvent) error {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if event.Type != "" {
			if _, err := fmt.Fprintf(c.Writer, "event: %s\n", event.Type); err != nil {
				return err
			}
		}
		_, err = fmt.Fprintf(c.Writer, "data: %s\n\n", data)
		flusher.Flush()
		return err
	}

	stopReason := "end_turn"
	if claudeResp.StopReason != nil {
		stopReason = *claudeResp.StopReason
	}

	// message_start
	if err := writeEvent(dto.ClaudeStreamEvent{
		Type: "message_start",
		Message: &dto.ClaudeMessageStart{
			ID:    claudeResp.ID,
			Type:  "message",
			Role:  "assistant",
			Model: claudeResp.Model,
			Usage: claudeResp.Usage,
		},
	}); err != nil {
		return err
	}

	contentIndex := 0
	for _, block := range claudeResp.Content {
		switch block.Type {
		case "text":
			// content_block_start
			if err := writeEvent(dto.ClaudeStreamEvent{
				Type:         "content_block_start",
				Index:        contentIndex,
				ContentBlock: &dto.ContentBlock{Type: "text", Text: ""},
			}); err != nil {
				return err
			}
			// content_block_delta
			if block.Text != "" {
				if counter != nil {
					if sc, ok := counter.(*token.StreamCounter); ok {
						sc.AddOutputText(block.Text)
					}
				}
				if err := writeEvent(dto.ClaudeStreamEvent{
					Type:  "content_block_delta",
					Index: contentIndex,
					Delta: &dto.ClaudeDelta{Type: "text_delta", Text: block.Text},
				}); err != nil {
					return err
				}
			}
			// content_block_stop
			if err := writeEvent(dto.ClaudeStreamEvent{
				Type:  "content_block_stop",
				Index: contentIndex,
			}); err != nil {
				return err
			}
			contentIndex++

		case "tool_use":
			argsJSON, _ := json.Marshal(block.Input)
			inputMap := map[string]any{}
			// content_block_start
			if err := writeEvent(dto.ClaudeStreamEvent{
				Type:  "content_block_start",
				Index: contentIndex,
				ContentBlock: &dto.ContentBlock{
					Type:  "tool_use",
					ID:    block.ID,
					Name:  block.Name,
					Input: inputMap,
				},
			}); err != nil {
				return err
			}
			// content_block_delta (input_json_delta)
			if len(argsJSON) > 2 { // 非空 JSON（不只是 {}）
				if err := writeEvent(dto.ClaudeStreamEvent{
					Type:  "content_block_delta",
					Index: contentIndex,
					Delta: &dto.ClaudeDelta{Type: "input_json_delta", PartialJSON: string(argsJSON)},
				}); err != nil {
					return err
				}
			}
			// content_block_stop
			if err := writeEvent(dto.ClaudeStreamEvent{
				Type:  "content_block_stop",
				Index: contentIndex,
			}); err != nil {
				return err
			}
			contentIndex++
		}
	}

	// message_delta
	if err := writeEvent(dto.ClaudeStreamEvent{
		Type:  "message_delta",
		Delta: &dto.ClaudeDelta{Type: "message_delta", StopReason: stopReason},
	}); err != nil {
		return err
	}

	// message_stop
	if err := writeEvent(dto.ClaudeStreamEvent{Type: "message_stop"}); err != nil {
		return err
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.ComputeOutputTokens()
		}
		counter.SetLatency()
	}

	return nil
}

// writeResponsesObjectAsStream 将 ResponsesResponse 对象以 Responses API SSE 格式写给客户端。
func writeResponsesObjectAsStream(c *gin.Context, responsesResp *dto.ResponsesResponse, counter TokenCounter) error {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	chatID := responsesResp.ID
	if chatID == "" {
		chatID = fmt.Sprintf("resp-%d", time.Now().UnixNano())
	}
	model := responsesResp.Model

	writeEvent := func(v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(c.Writer, "data: %s\n\n", data)
		flusher.Flush()
		return err
	}

	// response.created
	if err := writeEvent(map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":     chatID,
			"object": "response",
			"status": "in_progress",
			"model":  model,
		},
	}); err != nil {
		return err
	}

	for outputIdx, item := range responsesResp.Output {
		switch item.Type {
		case "message":
			messageID := item.ID
			if messageID == "" {
				messageID = fmt.Sprintf("msg-%d", time.Now().UnixNano())
			}
			// response.output_item.added
			if err := writeEvent(map[string]any{
				"type":         "response.output_item.added",
				"response_id":  chatID,
				"output_index": outputIdx,
				"item": map[string]any{
					"type":    "message",
					"id":      messageID,
					"status":  "in_progress",
					"role":    "assistant",
					"content": []any{},
				},
			}); err != nil {
				return err
			}

			for contentIdx, part := range item.Content {
				if part.Type != "output_text" {
					continue
				}
				// response.content_part.added
				if err := writeEvent(map[string]any{
					"type":          "response.content_part.added",
					"response_id":   chatID,
					"item_id":       messageID,
					"output_index":  outputIdx,
					"content_index": contentIdx,
					"part": map[string]any{
						"type": "output_text",
						"text": "",
					},
				}); err != nil {
					return err
				}

				if part.Text != "" {
					if counter != nil {
						if sc, ok := counter.(*token.StreamCounter); ok {
							sc.AddOutputText(part.Text)
						}
					}
					// response.output_text.delta
					if err := writeEvent(map[string]any{
						"type":          "response.output_text.delta",
						"response_id":   chatID,
						"item_id":       messageID,
						"output_index":  outputIdx,
						"content_index": contentIdx,
						"delta":         part.Text,
					}); err != nil {
						return err
					}
				}

				// response.output_text.done
				if err := writeEvent(map[string]any{
					"type":          "response.output_text.done",
					"response_id":   chatID,
					"item_id":       messageID,
					"output_index":  outputIdx,
					"content_index": contentIdx,
					"text":          part.Text,
				}); err != nil {
					return err
				}

				// response.content_part.done
				if err := writeEvent(map[string]any{
					"type":          "response.content_part.done",
					"response_id":   chatID,
					"item_id":       messageID,
					"output_index":  outputIdx,
					"content_index": contentIdx,
					"part": map[string]any{
						"type": "output_text",
						"text": part.Text,
					},
				}); err != nil {
					return err
				}
			}

			// response.output_item.done
			if err := writeEvent(map[string]any{
				"type":         "response.output_item.done",
				"response_id":  chatID,
				"output_index": outputIdx,
				"item": map[string]any{
					"type":    "message",
					"id":      messageID,
					"status":  "completed",
					"role":    "assistant",
					"content": item.Content,
				},
			}); err != nil {
				return err
			}

		case "function_call":
			fcID := fmt.Sprintf("fc-%s", item.CallID)
			// response.output_item.added
			if err := writeEvent(map[string]any{
				"type":         "response.output_item.added",
				"response_id":  chatID,
				"output_index": outputIdx,
				"item": map[string]any{
					"type":      "function_call",
					"id":        fcID,
					"call_id":   item.CallID,
					"name":      item.Name,
					"arguments": "",
				},
			}); err != nil {
				return err
			}

			if item.Arguments != "" {
				if counter != nil {
					if sc, ok := counter.(*token.StreamCounter); ok {
						sc.AddOutputText(item.Arguments)
					}
				}
				// response.function_call_arguments.delta
				if err := writeEvent(map[string]any{
					"type":         "response.function_call_arguments.delta",
					"response_id":  chatID,
					"item_id":      fcID,
					"output_index": outputIdx,
					"delta":        item.Arguments,
				}); err != nil {
					return err
				}
			}

			// response.function_call_arguments.done
			if err := writeEvent(map[string]any{
				"type":         "response.function_call_arguments.done",
				"response_id":  chatID,
				"item_id":      fcID,
				"output_index": outputIdx,
				"arguments":    item.Arguments,
			}); err != nil {
				return err
			}

			// response.output_item.done
			if err := writeEvent(map[string]any{
				"type":         "response.output_item.done",
				"response_id":  chatID,
				"output_index": outputIdx,
				"item": map[string]any{
					"type":      "function_call",
					"id":        fcID,
					"call_id":   item.CallID,
					"name":      item.Name,
					"arguments": item.Arguments,
				},
			}); err != nil {
				return err
			}
		}
	}

	// response.completed
	if err := writeEvent(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":     chatID,
			"object": "response",
			"status": "completed",
			"model":  model,
		},
	}); err != nil {
		return err
	}

	if counter != nil {
		if sc, ok := counter.(*token.StreamCounter); ok {
			sc.ComputeOutputTokens()
		}
		counter.SetLatency()
	}

	return nil
}
