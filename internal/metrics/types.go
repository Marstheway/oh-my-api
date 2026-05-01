package metrics

// RequestInfo 请求埋点信息
type RequestInfo struct {
	InboundProtocol  string  // 例如 "openai.chat" / "anthropic.messages" / "openai.response"
	OutboundProtocol string  // 例如 "openai" / "anthropic" / "openai.response"
	Provider         string
	UpstreamModel    string
	ModelGroup       string
	KeyName          string
	Status           string  // "success" / "error"
	Duration         float64 // 秒
}
