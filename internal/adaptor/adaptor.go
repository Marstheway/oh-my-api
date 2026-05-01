package adaptor

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
)

type Protocol string

const (
	ProtocolOpenAI         Protocol = "openai"
	ProtocolOpenAIResponse Protocol = "openai.response"
	ProtocolAnthropic      Protocol = "anthropic"
)

type TokenCounter interface {
	AddOutputTokens(text string)
	GetInputTokens() int
	GetOutputTokens() int
	SetStartTime(start time.Time)
	SetLatency()
	GetLatency() time.Duration
}

type Adaptor interface {
	BuildRequest(ctx context.Context, provider *config.ProviderConfig,
		upstreamModel string, body io.Reader, inbound Protocol) *http.Request

	WriteResponse(c *gin.Context, inbound Protocol,
		resp *http.Response, isStream bool, counter TokenCounter) error
}

// BuildURL 根据 endpoint 和协议构建请求 URL
// 规则：
// - endpoint 以 /chat/completions 结尾：视为完整 URL，直接使用（OpenAI 兼容）
// - endpoint 以 /messages 结尾：视为完整 URL，直接使用（Anthropic 兼容）
// - endpoint 以 /responses 结尾：视为完整 URL，直接使用（Responses API 兼容）
// - OpenAI 协议：追加 /chat/completions，空路径先加 /v1
// - OpenAI Response 协议：追加 /responses，空路径先加 /v1
// - Anthropic 协议：追加 /v1/messages
func BuildURL(endpoint string, protocol Protocol) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	path := strings.TrimSuffix(u.Path, "/")

	if strings.HasSuffix(path, "/chat/completions") ||
		strings.HasSuffix(path, "/messages") ||
		strings.HasSuffix(path, "/responses") {
		u.Path = path
		return u.String()
	}

	switch protocol {
	case ProtocolAnthropic:
		if path == "" {
			u.Path = "/v1/messages"
		} else {
			u.Path = path + "/v1/messages"
		}
	case ProtocolOpenAIResponse:
		if path == "" {
			u.Path = "/v1/responses"
		} else {
			u.Path = path + "/responses"
		}
	default:
		if path == "" {
			u.Path = "/v1/chat/completions"
		} else {
			u.Path = path + "/chat/completions"
		}
	}
	return u.String()
}

func GetAdaptor(protocol string) Adaptor {
	switch protocol {
	case "anthropic":
		return &AnthropicAdaptor{}
	default:
		return &OpenAIAdaptor{}
	}
}

type UsageInfo struct {
	InputTokens  int
	OutputTokens int
}

func ExtractUsage(response any) *UsageInfo {
	if response == nil {
		return nil
	}

	switch r := response.(type) {
	case *dto.ChatCompletionResponse:
		return &UsageInfo{
			InputTokens:  r.Usage.PromptTokens,
			OutputTokens: r.Usage.CompletionTokens,
		}
	case *dto.ClaudeResponse:
		return &UsageInfo{
			InputTokens:  r.Usage.InputTokens,
			OutputTokens: r.Usage.OutputTokens,
		}
	default:
		return nil
	}
}
