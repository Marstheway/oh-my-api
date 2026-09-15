package cascade

import (
	"net/http"
	"strings"
)

const authorizationHeader = "Authorization"
const bearerPrefix = "Bearer "

// AuthorizationHeaders is the WebSocket handshake header carrying the cascade token.
func AuthorizationHeaders(token string) http.Header {
	h := make(http.Header)
	token = strings.TrimSpace(token)
	if token != "" {
		h.Set(authorizationHeader, bearerPrefix+token)
	}
	return h
}

// TokenMatches compares cascade tokens in constant time.
func TokenMatches(got, want string) bool {
	return tokensEqual(got, want)
}

func requestBearerToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	raw := strings.TrimSpace(r.Header.Get(authorizationHeader))
	if len(raw) < len(bearerPrefix) || !strings.EqualFold(raw[:len(bearerPrefix)], bearerPrefix) {
		return ""
	}
	return strings.TrimSpace(raw[len(bearerPrefix):])
}
