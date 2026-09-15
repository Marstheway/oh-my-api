package health

import (
	"sync"
	"time"

	"github.com/Marstheway/oh-my-api/internal/metrics"
)

// Checker 管理 provider 的健康状态
type Checker struct {
	mu               sync.RWMutex
	failureCounts    map[string]int           // provider -> 连续失败次数
	unhealthyAt      map[string]time.Time     // provider -> 标记不健康的时间
	cooldownOverride map[string]time.Duration // provider -> 自定义冷却时间
	threshold        int                      // 连续失败阈值
	cooldown         time.Duration            // 默认冷却恢复时间
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

// MarkUnhealthyEscalating 立即将 provider 标记为不健康，冷却时间按指数退避递增。
//
// 语义（按「退避周期」升档，不是按调用次数）：
//   - 首次标记使用 baseCooldown（不超过 maxCooldown）；
//   - 仍在当前冷却窗口内再次调用：不升档、不刷新计时（避免 concurrent / 多 in-flight 连跳）；
//   - 冷却已到期后再次标记（被动恢复后问题未消除）：冷却翻倍，最高 maxCooldown；
//   - 当前因阈值熔断等处于不健康、但尚无 override 时：升级为 baseCooldown 额度退避。
//
// ReportSuccess 后退避等级重置。状态在内存中，进程重启后从 baseCooldown 重新开始。
func (c *Checker) MarkUnhealthyEscalating(provider string, baseCooldown, maxCooldown time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if baseCooldown <= 0 {
		baseCooldown = c.cooldown
	}
	if maxCooldown > 0 && baseCooldown > maxCooldown {
		baseCooldown = maxCooldown
	}

	if t, ok := c.unhealthyAt[provider]; ok {
		cd := c.cooldown
		if d, has := c.cooldownOverride[provider]; has {
			cd = d
		}
		if time.Since(t) <= cd {
			// 仍在冷却窗口内
			if _, hasOverride := c.cooldownOverride[provider]; hasOverride {
				// 已在额度退避中：忽略重复 mark，防止并发连跳
				return
			}
			// 阈值熔断等路径留下的不健康、无 override：升级为额度 base 退避
			c.cooldownOverride[provider] = baseCooldown
			c.failureCounts[provider] = 0
			c.unhealthyAt[provider] = time.Now()
			metrics.SetProviderHealth(provider, false)
			return
		}
	}

	// 不在冷却中：首次，或被动恢复后再次命中 → 按周期升档
	if prev, ok := c.cooldownOverride[provider]; ok && prev > 0 {
		next := prev * 2
		if maxCooldown > 0 && next > maxCooldown {
			next = maxCooldown
		}
		c.cooldownOverride[provider] = next
	} else {
		c.cooldownOverride[provider] = baseCooldown
	}
	c.failureCounts[provider] = 0
	c.unhealthyAt[provider] = time.Now()
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
