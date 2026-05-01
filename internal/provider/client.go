package provider

import (
	"fmt"
	"net/http"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
)

type Client struct {
	httpClients map[string]*http.Client
}

func NewClient(providers map[string]config.ProviderConfig, globalTimeout string) *Client {
	c := &Client{
		httpClients: make(map[string]*http.Client, len(providers)),
	}

	timeout := 120 * time.Second
	if globalTimeout != "" {
		if d, err := time.ParseDuration(globalTimeout); err == nil {
			timeout = d
		}
	}

	for name := range providers {
		c.httpClients[name] = &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				ResponseHeaderTimeout: timeout,
			},
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
