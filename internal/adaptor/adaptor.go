package adaptor

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/gin-gonic/gin"
)

type Protocol string

const (
	ProtocolOpenAI         Protocol = "openai.chat"
	ProtocolOpenAIResponse Protocol = "openai.responses"
	ProtocolAnthropic      Protocol = "anthropic.messages"
	ProtocolOllamaChat     Protocol = "ollama.chat"
)

type TokenCounter interface {
	AddOutputTokens(text string)
	GetInputTokens() int
	GetOutputTokens() int
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
// - endpoint 以 /api/chat 结尾：视为完整 URL，直接使用（Ollama chat 兼容）
// - OpenAI 协议：追加 /chat/completions，空路径先加 /v1
// - OpenAI Response 协议：追加 /responses，空路径先加 /v1
// - Anthropic 协议：追加 /v1/messages
// - Ollama chat 协议：追加 /api/chat
func BuildURL(endpoint string, protocol Protocol) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	path := strings.TrimSuffix(u.Path, "/")

	if protocol == ProtocolOllamaChat {
		if strings.HasSuffix(path, "/api/chat") {
			u.Path = path
			return u.String()
		}
		u.Path = path + "/api/chat"
		return u.String()
	}

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
		} else if strings.HasSuffix(path, "/v1") {
			u.Path = path + "/messages"
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

// BuildCatalogURL 基于 BuildURL 规范化的 endpoint 构造 catalog 探测 URL
// (list models / api tags)。
// 规则：
//   - 如果 endpoint 已经以 /models 或 /api/tags 结尾，直接返回规范化后的 endpoint
//   - 先从 BuildURL 得到规范化基础 URL
//   - OpenAI chat 协议：/v1/chat/completions → /v1/models 或 /chat/completions → /models
//   - OpenAI Response 协议：/v1/responses → /v1/models 或 /responses → /models
//   - Anthropic 协议：/v1/messages → /v1/models 或 /messages → /models
//   - Ollama chat 协议：/api/chat → /api/tags
func BuildCatalogURL(endpoint string, protocol Protocol) string {
	// If the endpoint already ends with a known list endpoint, normalize and return directly.
	// This avoids BuildURL adding chat/completions on top.
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	path := strings.TrimSuffix(u.Path, "/")

	if strings.HasSuffix(path, "/models") || strings.HasSuffix(path, "/api/tags") {
		// Already a list-models/tags endpoint, normalize and return
		u.Path = path
		return u.String()
	}

	baseURL := BuildURL(endpoint, protocol)
	u, err = url.Parse(baseURL)
	if err != nil {
		return baseURL
	}

	path = strings.TrimSuffix(u.Path, "/")

	if protocol == ProtocolOllamaChat {
		path = strings.Replace(path, "/api/chat", "/api/tags", 1)
	} else if strings.HasSuffix(path, "/chat/completions") {
		path = strings.TrimSuffix(path, "/chat/completions") + "/models"
	} else if strings.HasSuffix(path, "/messages") {
		path = strings.TrimSuffix(path, "/messages") + "/models"
	} else if strings.HasSuffix(path, "/responses") {
		path = strings.TrimSuffix(path, "/responses") + "/models"
	} else {
		// Unknown suffix, append /models
		path = path + "/models"
	}

	u.Path = path
	return u.String()
}

func GetAdaptor(protocol string) Adaptor {
	switch Protocol(protocol) {
	case ProtocolAnthropic:
		return &AnthropicAdaptor{}
	case ProtocolOllamaChat:
		return &OllamaAdaptor{}
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
