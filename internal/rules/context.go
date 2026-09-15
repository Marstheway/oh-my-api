package rules

// MatchContext 提供规则匹配所需的运行时上下文。
type MatchContext struct {
	ClientModel   string
	KeyName       string
	UpstreamModel string
}
