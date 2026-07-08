package health

import (
	"sync"
	"time"

	"github.com/Marstheway/oh-my-api/internal/metrics"
)

// Checker 管理 provider 的健康状态
type Checker struct {
	mu               sync.RWMutex
	failureCounts    map[string]int       // provider -> 连续失败次数
	unhealthyAt      map[string]time.Time // provider -> 标记不健康的时间
	cooldownOverride map[string]time.Duration // provider -> 自定义冷却时间
	threshold        int                       // 连续失败阈值
	cooldown         time.Duration             // 默认冷却恢复时间
}

// NewChecker 创建健康检查器
func NewChecker(threshold int, cooldown time.Duration) *Checker {
	return &Checker{
		failureCounts:    make(map[string]int),
		unhealthyAt:      make(map[string]time.Time),
		cooldownOverride: make(map[string]time.Duration),
		threshold:        threshold,
		cooldown:         cooldown,
	}
}

// IsHealthy 检查 provider 是否健康
// 不健康的 provider 在冷却时间后自动恢复为健康状态
func (c *Checker) IsHealthy(provider string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	unhealthyTime, exists := c.unhealthyAt[provider]
	if !exists {
		return true
	}

	// 优先使用自定义冷却时间，否则使用默认冷却时间
	cooldown := c.cooldown
	if d, ok := c.cooldownOverride[provider]; ok {
		cooldown = d
	}

	// 冷却时间后自动恢复
	if time.Since(unhealthyTime) > cooldown {
		return true
	}

	return false
}

// ReportSuccess 上报成功，重置失败计数并标记为健康
func (c *Checker) ReportSuccess(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failureCounts[provider] = 0
	delete(c.unhealthyAt, provider)
	delete(c.cooldownOverride, provider)

	metrics.SetProviderHealth(provider, true)
}

// ReportFailure 上报失败，增加失败计数
// 超过阈值时标记为不健康，使用默认冷却时间
func (c *Checker) ReportFailure(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failureCounts[provider]++

	if c.failureCounts[provider] >= c.threshold {
		delete(c.cooldownOverride, provider)
		c.unhealthyAt[provider] = time.Now()
		metrics.SetProviderHealth(provider, false)
	}
}

// MarkUnhealthyFor 立即将 provider 标记为不健康并设定自定义冷却时间
// 覆盖现有的阈值累计状态，并将失败计数清零
func (c *Checker) MarkUnhealthyFor(provider string, cooldown time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failureCounts[provider] = 0
	c.unhealthyAt[provider] = time.Now()
	c.cooldownOverride[provider] = cooldown
	metrics.SetProviderHealth(provider, false)
}

// GetStatus 获取所有 provider 的健康状态（用于监控/日志）
func (c *Checker) GetStatus() map[string]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]bool)
	for provider := range c.failureCounts {
		result[provider] = c.isHealthyInternal(provider)
	}
	return result
}

// isHealthyInternal 内部版本，不加锁，供 GetStatus 使用
func (c *Checker) isHealthyInternal(provider string) bool {
	unhealthyTime, exists := c.unhealthyAt[provider]
	if !exists {
		return true
	}
	cooldown := c.cooldown
	if d, ok := c.cooldownOverride[provider]; ok {
		cooldown = d
	}
	return time.Since(unhealthyTime) > cooldown
}
