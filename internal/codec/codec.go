package codec

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type TokenCounter interface {
	AddOutputTokens(text string)
	GetInputTokens() int
	GetOutputTokens() int
	SetStartTime(start time.Time)
	SetLatency()
	GetLatency() time.Duration
}

type Codec interface {
	Format() Format
	DecodeRequest(c *gin.Context) (any, error)
	EncodeRequest(outbound Format, req any, upstreamModel string) ([]byte, error)
	WriteResponse(c *gin.Context, outbound Format, resp *http.Response, isStream bool, counter TokenCounter) error
}
