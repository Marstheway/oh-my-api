package provider

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
)

type Client struct {
	httpClients map[string]*http.Client
}

func NewClient(providers map[string]config.ProviderConfig, timeout time.Duration, connectTimeout time.Duration) *Client {
	c := &Client{
		httpClients: make(map[string]*http.Client, len(providers)),
	}

	for name := range providers {
		c.httpClients[name] = &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				ResponseHeaderTimeout: timeout,
			},
		}
		if connectTimeout > 0 {
			transport := c.httpClients[name].Transport.(*http.Transport)
			transport.DialContext = (&net.Dialer{
				Timeout: connectTimeout,
			}).DialContext
			transport.TLSHandshakeTimeout = connectTimeout
		}
	}

	return c
}

func (c *Client) Do(providerName string, req *http.Request) (*http.Response, error) {
	client, ok := c.httpClients[providerName]
	if !ok {
		return nil, fmt.Errorf("provider not found: %s", providerName)
	}
	return client.Do(req)
}

// SetTransport 设置指定 provider 的 HTTP 传输层，供测试注入 mock transport
func (c *Client) SetTransport(providerName string, transport http.RoundTripper) {
	if client, ok := c.httpClients[providerName]; ok {
		client.Transport = transport
	}
}
