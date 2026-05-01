package health

import (
	"sync"
	"time"

	"github.com/Marstheway/oh-my-api/internal/metrics"
)

// Checker 管理 provider 的健康状态
type Checker struct {
	mu            sync.RWMutex
	failureCounts map[string]int       // provider -> 连续失败次数
	unhealthyAt   map[string]time.Time // provider -> 标记不健康的时间
	threshold     int                  // 连续失败阈值
	cooldown      time.Duration        // 冷却恢复时间
}

// NewChecker 创建健康检查器
func NewChecker(threshold int, cooldown time.Duration) *Checker {
	return &Checker{
		failureCounts: make(map[string]int),
		unhealthyAt:   make(map[string]time.Time),
		threshold:     threshold,
		cooldown:      cooldown,
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

	// 冷却时间后自动恢复
	if time.Since(unhealthyTime) > c.cooldown {
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

	metrics.SetProviderHealth(provider, true)
}

// ReportFailure 上报失败，增加失败计数
// 超过阈值时标记为不健康
func (c *Checker) ReportFailure(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failureCounts[provider]++

	if c.failureCounts[provider] >= c.threshold {
		c.unhealthyAt[provider] = time.Now()
		metrics.SetProviderHealth(provider, false)
	}
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
	return time.Since(unhealthyTime) > c.cooldown
}
