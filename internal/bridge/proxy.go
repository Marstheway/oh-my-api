package bridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

var xaiResponsesURL = "https://api.x.ai/v1/responses"

// xaiChatURL 是 xAI OpenAI 兼容 Chat Completions 接口地址，
// 提供与 xaiResponsesURL 相同的本地替换测试入口。
var xaiChatURL = "https://api.x.ai/v1/chat/completions"

// xaiModelsURL 是 xAI 模型列表接口地址，提供与 Responses URL 相同的本地替换测试入口。
// 仅包内测试替换（非导出，不扩大生产 API 表面，不允许运行时 URL 覆盖）。
var xaiModelsURL = "https://api.x.ai/v1/models"

// SetXaiModelsURLForTest 仅在测试中替换 xAI 模型列表地址，返回恢复函数。
// 生产代码不得调用；用于 cmd/oh-my-api 的 catalog probe 集成测试将真实 bridge
// handler 的 xAI 请求导向本地 mock，而不扩大运行时 URL 覆盖能力。
func SetXaiModelsURLForTest(url string) func() {
	orig := xaiModelsURL
	xaiModelsURL = url
	return func() { xaiModelsURL = orig }
}

// proxyHopHeaders 是不应在桥接层盲目复制的 hop-by-hop headers。
// 包含这些键或前缀的响应头不会被复制到客户端响应。
var proxyHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

// upstreamResponse 封装来自上游的响应。
type upstreamResponse struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

// proxyToXAI 向指定 xAI 接口发送 POST 请求并返回上游响应。
// targetURL 为实际请求地址；accessToken 是刷新后的 xAI access token，绝不写入日志。
// 调用方负责关闭响应 body。
func proxyToXAI(ctx context.Context, targetURL string, body io.Reader, accessToken string) (*upstreamResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, body)
	if err != nil {
		return nil, fmt.Errorf("bridge: cannot create upstream request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bridge: upstream request failed: %w", err)
	}

	return &upstreamResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       resp.Body,
	}, nil
}

// proxyModelsToXAI 向 xAI /v1/models 发起无 body 的 GET 请求并返回上游响应。
// 请求设置 Accept: application/json 与 xAI OAuth Authorization。
// accessToken 绝不写入日志。调用方负责关闭响应 body。
func proxyModelsToXAI(ctx context.Context, accessToken string) (*upstreamResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xaiModelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bridge: cannot create upstream models request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bridge: upstream models request failed: %w", err)
	}

	return &upstreamResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       resp.Body,
	}, nil
}

// copyUpstreamHeaders 将上游响应头安全地复制到客户端响应中，
// 跳过 hop-by-hop headers，并保留所有非 hop-by-hop 的多值。
func copyUpstreamHeaders(dst, src http.Header) {
	for key, values := range src {
		if proxyHopHeaders[key] {
			continue
		}
		for _, v := range values {
			dst.Add(key, v)
		}
	}
}
