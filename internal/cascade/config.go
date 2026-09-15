package cascade

import (
	"fmt"
	"net/url"
	"strings"
)

// HubWSURL derives the cascade WebSocket dial URL from a spoke hub origin.
func HubWSURL(origin string) (string, error) {
	origin = strings.TrimSpace(origin)
	u, err := url.Parse(origin)
	if err != nil {
		return "", fmt.Errorf("parse hub origin: %w", err)
	}

	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("hub origin scheme must be http or https, got %q", u.Scheme)
	}

	u.Path = "/cascade"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// HubFromProvider builds a configured hub for the single cascade provider.
func HubFromProvider(providerName, token string) (*Hub, bool) {
	if strings.TrimSpace(providerName) == "" || strings.TrimSpace(token) == "" {
		return nil, false
	}
	return NewHub(HubConfig{
		ProviderName: providerName,
		Token:        token,
	}), true
}
