package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/google/uuid"
)

func (c *Client) doCascadeJob(providerName string, meta RequestMeta, req *http.Request) (*http.Response, error) {
	if c.cascadeHubs == nil {
		return nil, fmt.Errorf("cascade hub not configured")
	}
	hub := c.cascadeHubs.Get()
	if hub == nil {
		return nil, fmt.Errorf("cascade hub not configured")
	}
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}

	body, err := readRequestBody(req)
	if err != nil {
		return nil, err
	}

	stream := requestStream(body)
	hop := ""
	if req.Header != nil {
		hop = req.Header.Get(cascade.HopHeader)
	}

	ctx := req.Context()

	return hub.ExecuteJob(ctx, cascade.JobRequest{
		ID:       uuid.NewString(),
		Protocol: meta.OutboundProtocol,
		Model:    meta.UpstreamModel,
		Stream:   stream,
		Body:     body,
		Hop:      hop,
	})
}

func readRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("read cascade job body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(data))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return data, nil
}

func requestStream(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var probe struct {
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Stream != nil && *probe.Stream
}

func (c *Client) isCascadeProvider(providerName string) bool {
	cfg, ok := c.providerConfigs[providerName]
	return ok && cfg.Cascade != nil && cfg.Cascade.Enabled
}
