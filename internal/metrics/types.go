package metrics

// RequestInfo 请求埋点信息
type RequestInfo struct {
	InboundProtocol  string // 例如 "openai.chat" / "anthropic.messages" / "openai.response"
	OutboundProtocol string // 例如 "openai" / "anthropic" / "openai.response"
	Provider         string
	UpstreamModel    string
	ModelGroup       string
	KeyName          string
	Status              string  // "success" / "error"
	Duration            float64 // 秒（端到端延迟）
	FirstTokenDuration  float64 // 秒（首 token 延迟，仅流式请求；0 表示无）
}

// ProviderAttemptInfo Provider 尝试埋点信息
type ProviderAttemptInfo struct {
	Scheduler        string // 调度模式: "failover" / "loadbalance" / "concurrent" / "adaptive"
	Provider         string
	UpstreamModel    string
	ModelGroup       string
	OutboundProtocol string
	Result           string // "success" / "hard_failure" / "soft_failure" / "canceled" / "transport_error"
	FailureReason    string // 失败原因，成功时为空
	StatusCode       string // HTTP 状态码字符串，非 HTTP 失败时为空
	Duration         float64
}
