package provider

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Marstheway/oh-my-api/internal/codec"
)

const (
	responsesCompatCacheTTL  = time.Hour
	responsesCompatCacheSize = 256
)

type RequestMeta struct {
	UpstreamModel    string
	OutboundProtocol string
}

type responsesCompatCacheKey struct {
	providerName     string
	upstreamModel    string
	outboundProtocol string
	rule             string
}

type responsesCompatCache struct {
	mu      sync.Mutex
	entries map[responsesCompatCacheKey]time.Time
	now     func() time.Time
}

func newResponsesCompatCache() *responsesCompatCache {
	return &responsesCompatCache{entries: make(map[responsesCompatCacheKey]time.Time), now: time.Now}
}

func (c *responsesCompatCache) has(key responsesCompatCacheKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	expiresAt, ok := c.entries[key]
	if !ok {
		return false
	}
	if !c.now().Before(expiresAt) {
		delete(c.entries, key)
		return false
	}
	return true
}

func (c *responsesCompatCache) add(key responsesCompatCacheKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= responsesCompatCacheSize {
		c.entries = make(map[responsesCompatCacheKey]time.Time)
	}
	c.entries[key] = c.now().Add(responsesCompatCacheTTL)
}

func (c *responsesCompatCache) remove(key responsesCompatCacheKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

type responsesCompat struct {
	cache *responsesCompatCache
}

func newResponsesCompat() *responsesCompat {
	return &responsesCompat{cache: newResponsesCompatCache()}
}

type providerDo func(string, *http.Request) (*http.Response, error)

// do sends a model request and learns a safe Responses fallback only after a
// known upstream tool_choice conversion error is recovered successfully.
func (c *responsesCompat) do(send providerDo, providerName string, meta RequestMeta, req *http.Request) (*http.Response, error) {
	if req == nil || meta.OutboundProtocol != "openai.responses" {
		return send(providerName, req)
	}

	key := responsesCompatCacheKey{
		providerName:     providerName,
		upstreamModel:    meta.UpstreamModel,
		outboundProtocol: meta.OutboundProtocol,
		rule:             codec.ResponsesToolChoiceSingleFunctionCompatRule,
	}
	fallback, applicable, err := responsesToolChoiceFallbackRequest(req)
	if err != nil {
		slog.Debug("responses tool choice compatibility unavailable", compatLogAttrs(providerName, meta, "not_applicable", "error", err.Error())...)
		return send(providerName, req)
	}
	if !applicable {
		return send(providerName, req)
	}

	if c.cache.has(key) {
		slog.Debug("responses tool choice compatibility cache hit", compatLogAttrs(providerName, meta, "cache_hit")...)
		resp, err := send(providerName, fallback)
		if err != nil || !isSuccessResponse(resp) {
			c.cache.remove(key)
			slog.Debug("responses tool choice compatibility cache evicted", compatLogAttrs(providerName, meta, "evicted", "reason", compatFailureReason(resp, err))...)
		}
		return resp, err
	}

	resp, err := send(providerName, req)
	if err != nil || !isResponsesToolChoiceCompatResponse(resp) {
		return resp, err
	}

	triggerBody := restoreResponseBody(resp)
	slog.Debug("responses tool choice compatibility retry", compatLogAttrs(providerName, meta,
		"probe_retry", "trigger_status", resp.StatusCode, "trigger_error", summarizeCompatError(triggerBody))...)
	resp.Body.Close()
	if err := req.Context().Err(); err != nil {
		return nil, err
	}

	fallbackResp, fallbackErr := send(providerName, fallback)
	if fallbackErr == nil && isSuccessResponse(fallbackResp) {
		c.cache.add(key)
		slog.Debug("responses tool choice compatibility learned", compatLogAttrs(providerName, meta, "learned")...)
	}
	return fallbackResp, fallbackErr
}

func responsesToolChoiceFallbackRequest(req *http.Request) (*http.Request, bool, error) {
	if req.GetBody == nil {
		return nil, false, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, false, err
	}
	defer body.Close()
	original, err := io.ReadAll(body)
	if err != nil {
		return nil, false, err
	}
	fallbackBody, changed, err := codec.BuildResponsesToolChoiceSingleFunctionFallback(original)
	if err != nil || !changed {
		return nil, changed, err
	}
	fallback := req.Clone(req.Context())
	fallback.Body = io.NopCloser(bytes.NewReader(fallbackBody))
	fallback.ContentLength = int64(len(fallbackBody))
	fallback.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(fallbackBody)), nil
	}
	return fallback, true, nil
}

func isResponsesToolChoiceCompatResponse(resp *http.Response) bool {
	if resp == nil || resp.Body == nil || resp.StatusCode != http.StatusBadRequest {
		return false
	}
	body := restoreResponseBody(resp)
	return codec.IsResponsesToolChoiceSingleFunctionCompatError(resp.StatusCode, body)
}

func restoreResponseBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return body
}

func isSuccessResponse(resp *http.Response) bool {
	return resp != nil && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
}

func compatFailureReason(resp *http.Response, err error) string {
	if err != nil {
		return "transport_error"
	}
	if resp == nil {
		return "no_response"
	}
	return fmt.Sprintf("status_%d", resp.StatusCode)
}

func summarizeCompatError(body []byte) string {
	const maxRunes = 256
	text := strings.Join(strings.Fields(string(body)), " ")
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return text
}

func compatLogAttrs(providerName string, meta RequestMeta, action string, extra ...any) []any {
	attrs := []any{
		"responses_tool_choice_compat", action,
		"provider", providerName,
		"upstream_model", meta.UpstreamModel,
		"outbound_protocol", meta.OutboundProtocol,
		"compat_rule", codec.ResponsesToolChoiceSingleFunctionCompatRule,
	}
	return append(attrs, extra...)
}
