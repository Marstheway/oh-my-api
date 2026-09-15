package codec

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type TokenCounter interface {
	AddOutputTokens(text string)
	GetInputTokens() int
	GetOutputTokens() int
}

// ResponseModelContext carries the client-visible and winner identity needed
// for codec helpers to fill response model fields correctly.
type ResponseModelContext struct {
	RequestedModel      string // model name as the client sent it (may be an alias)
	ModelGroup          string // resolved model group name
	WinnerProvider      string // scheduler winner provider name
	WinnerUpstreamModel string // upstream model name for the winning provider
	IncludeUsage        bool   // whether to include usage in streaming chunks (stream_options.include_usage)
}

type Codec interface {
	Format() Format
	DecodeRequest(c *gin.Context) (any, error)
	EncodeRequest(outbound Format, req any, upstreamModel string, needsDeepSeekCompat bool) ([]byte, error)
	WriteResponse(c *gin.Context, outbound Format, resp *http.Response, isStream bool, counter TokenCounter, rmc ResponseModelContext) error
	WriteResponseTo(w http.ResponseWriter, outbound Format, resp *http.Response, isStream bool, counter TokenCounter, rmc ResponseModelContext) error
}
