package health

import "strings"

// MakeHealthKey builds a health tracking key in provider+outboundProtocol granularity.
// When outboundProtocol is empty, it falls back to provider-level key for compatibility.
func MakeHealthKey(provider, outboundProtocol string) string {
	provider = strings.TrimSpace(provider)
	outboundProtocol = strings.TrimSpace(outboundProtocol)
	if outboundProtocol == "" {
		return provider
	}
	return provider + "|" + outboundProtocol
}

