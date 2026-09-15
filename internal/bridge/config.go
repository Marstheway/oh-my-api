// Package bridge 提供独立运行的 oh-my-api-bridge 服务，
// 负责校验 bridge bearer token、按 provider 类型读取本地 OAuth 凭证、
// 向目标上游透传 /v1/responses 与 /v1/chat/completions 请求并返回响应。
package bridge

import (
	"flag"
	"fmt"
	"net"
	"strings"
)

// Config 表示 bridge 服务的运行配置。
type Config struct {
	// Listen 是 HTTP server 监听地址，如 ":8081"。
	Listen string

	// BridgeToken 是 bridge 鉴权用的固定 bearer token。
	// 所有客户端请求都必须携带此 token。
	BridgeToken string

	AllowUnauthenticatedLoopback bool
}

// supportedProviderTypes 是第一版支持的 bridge provider 类型集合。
// 通过 provider 类型校验，防止把通用性预留字段退化成无效装饰。
var supportedProviderTypes = map[string]bool{
	"xai-oauth": true,
}

// LoadConfig 从命令行参数解析 bridge 运行配置。
// 也支持通过环境变量设置默认值。
func LoadConfig(args []string) (*Config, error) {
	cfg := &Config{}

	fs := flag.NewFlagSet("oh-my-api-bridge", flag.ContinueOnError)
	fs.StringVar(&cfg.Listen, "listen", ":8081", "HTTP listen address")
	fs.StringVar(&cfg.BridgeToken, "bridge-token", "", "bridge bearer auth token (required)")
	fs.BoolVar(&cfg.AllowUnauthenticatedLoopback, "allow-unauthenticated-loopback", false, "allow unauthenticated requests on a loopback listener")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	// 拒绝服务模式下的多余位置参数（flag 包将其留在 Args 中，不报错）。
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}

	if cfg.AllowUnauthenticatedLoopback {
		if !isLoopbackListenAddress(cfg.Listen) {
			return nil, fmt.Errorf("-allow-unauthenticated-loopback requires a loopback listen address")
		}
	} else if strings.TrimSpace(cfg.BridgeToken) == "" {
		return nil, fmt.Errorf("bridge bearer token is required, set with -bridge-token")
	}

	return cfg, nil
}

func isLoopbackListenAddress(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ValidateProvider 校验 provider 类型是否受当前 bridge 版本支持。
func ValidateProvider(provider string) error {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return fmt.Errorf("bridge provider type header is required")
	}
	if !supportedProviderTypes[provider] {
		return fmt.Errorf("unsupported bridge provider type: %q", provider)
	}
	return nil
}

// SupportedProviderTypes 返回当前 bridge 版本支持的 provider 类型列表。
func SupportedProviderTypes() []string {
	result := make([]string, 0, len(supportedProviderTypes))
	for k := range supportedProviderTypes {
		result = append(result, k)
	}
	return result
}
