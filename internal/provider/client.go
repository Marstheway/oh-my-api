package provider

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/config"
)

type Client struct {
	httpClients     map[string]*http.Client
	providerConfigs map[string]config.ProviderConfig
	responsesCompat *responsesCompat
	cascadeHubs     *cascade.HubRegistry
}

func NewClient(providers map[string]config.ProviderConfig, timeout time.Duration, connectTimeout time.Duration, responseHeaderTimeout time.Duration) *Client {
	c := &Client{
		httpClients:     make(map[string]*http.Client, len(providers)),
		providerConfigs: providers,
		responsesCompat: newResponsesCompat(),
	}

	for name := range providers {
		c.httpClients[name] = &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{ResponseHeaderTimeout: responseHeaderTimeout},
		}
		if connectTimeout > 0 {
			transport := c.httpClients[name].Transport.(*http.Transport)
			transport.DialContext = (&net.Dialer{Timeout: connectTimeout}).DialContext
			transport.TLSHandshakeTimeout = connectTimeout
		}
	}
	return c
}

// SetCascadeHubRegistry wires the dynamic hub registry used by cascade.enabled providers.
func (c *Client) SetCascadeHubRegistry(registry *cascade.HubRegistry) {
	c.cascadeHubs = registry
}

func (c *Client) Do(providerName string, req *http.Request) (*http.Response, error) {
	return c.do(providerName, req)
}

// DoWithMeta sends a model request and applies learned Responses compatibility rules.
func (c *Client) DoWithMeta(providerName string, meta RequestMeta, req *http.Request) (*http.Response, error) {
	if c.isCascadeProvider(providerName) {
		return c.doCascadeJob(providerName, meta, req)
	}
	return c.responsesCompat.do(c.do, providerName, meta, req)
}

// CascadeReady reports whether a cascade.enabled provider currently has a spoke session.
// Non-cascade providers are always ready.
func (c *Client) CascadeReady(providerName string) bool {
	if !c.isCascadeProvider(providerName) {
		return true
	}
	if c.cascadeHubs == nil {
		return false
	}
	hub := c.cascadeHubs.Get()
	if hub == nil {
		return false
	}
	_, ok := hub.Session()
	return ok
}

func (c *Client) do(providerName string, req *http.Request) (*http.Response, error) {
	if c.isCascadeProvider(providerName) {
		return nil, fmt.Errorf("cascade provider %s does not support HTTP dial", providerName)
	}
	client, ok := c.httpClients[providerName]
	if !ok {
		return nil, fmt.Errorf("provider not found: %s", providerName)
	}
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}
	if cfg, ok := c.providerConfigs[providerName]; ok && cfg.RemoteBridge != nil && cfg.RemoteBridge.Enabled {
		if !cfg.RemoteBridge.Local {
			req.Header.Set("Authorization", "Bearer "+cfg.RemoteBridge.Token)
		}
		req.Header.Set("X-Oh-My-API-Bridge-Provider", cfg.RemoteBridge.Provider)
	}
	return client.Do(req)
}

// SetTransport sets a provider HTTP transport for tests.
func (c *Client) SetTransport(providerName string, transport http.RoundTripper) {
	if client, ok := c.httpClients[providerName]; ok {
		client.Transport = transport
	}
}
