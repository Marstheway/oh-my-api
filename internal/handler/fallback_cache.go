package handler

import "sync"

type protocolFallbackCache struct {
	mu   sync.RWMutex
	data map[string]string
}

func newProtocolFallbackCache() *protocolFallbackCache {
	return &protocolFallbackCache{
		data: make(map[string]string),
	}
}

func (c *protocolFallbackCache) key(provider, inbound string) string {
	return provider + "|" + inbound
}

func (c *protocolFallbackCache) GetPreferred(provider, inbound string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.data[c.key(provider, inbound)]
	return v, ok
}

func (c *protocolFallbackCache) MarkPreferred(provider, inbound, outbound string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[c.key(provider, inbound)] = outbound
}

func (c *protocolFallbackCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = make(map[string]string)
}

var responsesFallbackCache = newProtocolFallbackCache()

