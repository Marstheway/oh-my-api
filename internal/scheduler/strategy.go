package scheduler

import (
	"context"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/config"
)

type Task struct {
	ProviderName  string
	Provider      config.ProviderConfig
	UpstreamModel string
	OutboundProtocol string
	Weight        int
	Priority      int
	Request       *http.Request
}

type Result struct {
	Response      *http.Response
	Winner        string
	UpstreamModel string
	Usage         *UsageInfo
}

type UsageInfo struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	FinishReason     string
}

type Strategy interface {
	Execute(ctx context.Context, tasks []Task) (*Result, error)
}
