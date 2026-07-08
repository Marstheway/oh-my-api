package adaptor

import (
	"context"
	"io"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/gin-gonic/gin"
)

// OllamaAdaptor 处理 ollama.chat 协议的请求构造。
// WriteResponse 仅满足 Adaptor 接口约束，协议写回转换由上层 handler 负责。
type OllamaAdaptor struct{}

func (a *OllamaAdaptor) BuildRequest(ctx context.Context, provider *config.ProviderConfig,
	upstreamModel string, body io.Reader, inbound Protocol) *http.Request {

	endpoint := provider.GetEndpoint(string(ProtocolOllamaChat))
	rawURL := BuildURL(endpoint, ProtocolOllamaChat)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, body)
	req.Header.Set("Content-Type", "application/json")
	if provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}
	return req
}

func (a *OllamaAdaptor) WriteResponse(c *gin.Context, inbound Protocol,
	resp *http.Response, isStream bool, counter TokenCounter) error {

	// 协议写回转换由上层 handler 负责，此处仅作最小实现以满足接口约束。
	return nil
}
