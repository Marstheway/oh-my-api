package cascade

import "sync"

// HubRegistry holds the active hub for /cascade and provider clients.
// Updated on process start and Admin Apply so routes always see the current hub.
type HubRegistry struct {
	mu  sync.RWMutex
	hub *Hub
}

func NewHubRegistry() *HubRegistry {
	return &HubRegistry{}
}

func (r *HubRegistry) Set(h *Hub) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hub = h
}

func (r *HubRegistry) Get() *Hub {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hub
}

// ContextLength 提供无副作用的 Cascade 会话元数据 context_length 查询。
// 无活跃 hub / session 或 provider 不匹配时返回 miss，供 handler 的
// context 推导作为普通 catalog 的补充来源。
func (r *HubRegistry) ContextLength(provider, upstreamModel string) (int, bool) {
	hub := r.Get()
	if hub == nil {
		return 0, false
	}
	return hub.ContextLength(provider, upstreamModel)
}
