package cascade

import (
	"time"

	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/health"
)

// CascadeOutboundProtocols are the health keys pinned on hub session disconnect.
var CascadeOutboundProtocols = []string{
	string(codec.FormatOpenAIChat),
	string(codec.FormatOpenAIResponse),
	string(codec.FormatAnthropicMessages),
}

// pinnedUnhealthyCooldown keeps admin/metrics keys down until reconnect ReportSuccess.
// Routing must not rely on this cooldown: scheduler skips cascade leaves via Client.CascadeReady.
const pinnedUnhealthyCooldown = 365 * 24 * time.Hour

func ReportSessionHealthy(checker *health.Checker, provider string) {
	if checker == nil {
		return
	}
	for _, proto := range CascadeOutboundProtocols {
		checker.ReportSuccess(health.MakeHealthKey(provider, proto))
	}
}

func ReportSessionUnhealthy(checker *health.Checker, provider string) {
	if checker == nil {
		return
	}
	for _, proto := range CascadeOutboundProtocols {
		checker.MarkUnhealthyFor(health.MakeHealthKey(provider, proto), pinnedUnhealthyCooldown)
	}
}
