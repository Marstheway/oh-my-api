package health

import (
	"sync"
	"testing"
	"time"
)

func TestChecker_IsHealthy_Healthy(t *testing.T) {
	c := NewChecker(3, 30*time.Second)

	if !c.IsHealthy("test") {
		t.Error("new provider should be healthy")
	}
}

func TestChecker_IsHealthy_Unhealthy(t *testing.T) {
	c := NewChecker(3, 30*time.Second)

	// 连续失败 3 次
	c.ReportFailure("test")
	c.ReportFailure("test")
	c.ReportFailure("test")

	if c.IsHealthy("test") {
		t.Error("provider should be unhealthy after 3 failures")
	}
}

func TestChecker_IsHealthy_RecoverAfterCooldown(t *testing.T) {
	c := NewChecker(3, 100*time.Millisecond)

	// 标记为不健康
	c.ReportFailure("test")
	c.ReportFailure("test")
	c.ReportFailure("test")

	if c.IsHealthy("test") {
		t.Error("provider should be unhealthy")
	}

	// 等待冷却时间
	time.Sleep(150 * time.Millisecond)

	if !c.IsHealthy("test") {
		t.Error("provider should be healthy after cooldown")
	}
}

func TestChecker_ReportSuccess_ResetsFailureCount(t *testing.T) {
	c := NewChecker(3, 30*time.Second)

	// 失败 2 次
	c.ReportFailure("test")
	c.ReportFailure("test")

	// 成功后重置
	c.ReportSuccess("test")

	// 再失败 2 次不应该标记为不健康
	c.ReportFailure("test")
	c.ReportFailure("test")

	if !c.IsHealthy("test") {
		t.Error("provider should be healthy after reset, only 2 failures")
	}
}

func TestChecker_ReportFailure_ThresholdBoundary(t *testing.T) {
	c := NewChecker(3, 30*time.Second)

	// 失败 2 次，不应该标记为不健康
	c.ReportFailure("test")
	c.ReportFailure("test")
	if !c.IsHealthy("test") {
		t.Error("provider should be healthy with only 2 failures")
	}

	// 第 3 次失败，应该标记为不健康
	c.ReportFailure("test")
	if c.IsHealthy("test") {
		t.Error("provider should be unhealthy after 3 failures")
	}
}

func TestChecker_ConcurrentAccess(t *testing.T) {
	c := NewChecker(3, 30*time.Second)

	var wg sync.WaitGroup
	// 并发上报成功和失败
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			c.ReportFailure("test")
		}()
		go func() {
			defer wg.Done()
			c.ReportSuccess("test")
		}()
	}
	wg.Wait()

	// 不应该 panic 或死锁
	_ = c.IsHealthy("test")
}

func TestChecker_GetStatus(t *testing.T) {
	c := NewChecker(2, 30*time.Second)

	c.ReportFailure("a")
	c.ReportFailure("a")
	c.ReportFailure("b")

	status := c.GetStatus()

	if status["a"] {
		t.Error("provider 'a' should be unhealthy")
	}
	if !status["b"] {
		t.Error("provider 'b' should be healthy (only 1 failure)")
	}
}

func TestChecker_ZeroThreshold(t *testing.T) {
	c := NewChecker(0, 30*time.Second)

	// 阈值为 0 时，任何失败都应该标记为不健康
	c.ReportFailure("test")

	if c.IsHealthy("test") {
		t.Error("provider should be unhealthy with threshold 0")
	}
}

func TestChecker_MultipleProviders(t *testing.T) {
	c := NewChecker(3, 30*time.Second)

	// provider-a 失败
	c.ReportFailure("a")
	c.ReportFailure("a")
	c.ReportFailure("a")

	// provider-b 成功
	c.ReportSuccess("b")

	if c.IsHealthy("a") {
		t.Error("provider 'a' should be unhealthy")
	}
	if !c.IsHealthy("b") {
		t.Error("provider 'b' should be healthy")
	}
}

func TestChecker_ProtocolGranularityIsolation(t *testing.T) {
	c := NewChecker(3, 30*time.Second)

	responseKey := MakeHealthKey("token-hub", "openai.response")
	chatKey := MakeHealthKey("token-hub", "openai")

	c.ReportFailure(responseKey)
	c.ReportFailure(responseKey)
	c.ReportFailure(responseKey)

	if c.IsHealthy(responseKey) {
		t.Error("response key should be unhealthy after threshold failures")
	}
	if !c.IsHealthy(chatKey) {
		t.Error("chat key should remain healthy and not be affected")
	}
}

// TestChecker_MarkUnhealthyFor_ImmediateUnhealthy 验证 MarkUnhealthyFor 立即标记为不健康
func TestChecker_MarkUnhealthyFor_ImmediateUnhealthy(t *testing.T) {
	c := NewChecker(3, 30*time.Second)

	if !c.IsHealthy("test") {
		t.Error("new provider should be healthy")
	}

	c.MarkUnhealthyFor("test", 1*time.Hour)

	if c.IsHealthy("test") {
		t.Error("provider should be unhealthy immediately after MarkUnhealthyFor")
	}
}

// TestChecker_MarkUnhealthyFor_RecoveryAfterDuration 验证自定义冷却时间后恢复
func TestChecker_MarkUnhealthyFor_RecoveryAfterDuration(t *testing.T) {
	c := NewChecker(3, 30*time.Second)

	c.MarkUnhealthyFor("test", 50*time.Millisecond)

	if c.IsHealthy("test") {
		t.Error("provider should be unhealthy immediately")
	}

	time.Sleep(100 * time.Millisecond)

	if !c.IsHealthy("test") {
		t.Error("provider should recover after custom cooldown")
	}
}

// TestChecker_MarkUnhealthyFor_OverridesThresholdFailure 验证 MarkUnhealthyFor 覆盖基于阈值的冷却时间
func TestChecker_MarkUnhealthyFor_OverridesThresholdFailure(t *testing.T) {
	c := NewChecker(3, 30*time.Second)

	c.ReportFailure("test")
	c.ReportFailure("test")
	c.ReportFailure("test")

	if c.IsHealthy("test") {
		t.Error("provider should be unhealthy after threshold failures")
	}

	c.MarkUnhealthyFor("test", 50*time.Millisecond)

	if c.IsHealthy("test") {
		t.Error("provider should still be unhealthy after MarkUnhealthyFor override")
	}

	time.Sleep(100 * time.Millisecond)

	if !c.IsHealthy("test") {
		t.Error("provider should recover after custom cooldown, not default")
	}
}

// TestChecker_MarkUnhealthyFor_ResetsFailureCount 验证 MarkUnhealthyFor 清零连续失败计数
func TestChecker_MarkUnhealthyFor_ResetsFailureCount(t *testing.T) {
	c := NewChecker(3, 30*time.Second)

	c.ReportFailure("test")
	c.ReportFailure("test")

	c.MarkUnhealthyFor("test", 50*time.Millisecond)

	time.Sleep(100 * time.Millisecond)

	if !c.IsHealthy("test") {
		t.Error("provider should be healthy after cooldown")
	}

	c.ReportFailure("test")
	if !c.IsHealthy("test") {
		t.Error("provider should be healthy with only 1 failure after reset")
	}
}

// TestChecker_MarkUnhealthyFor_NoEffectOnOtherProviders 验证排他性
func TestChecker_MarkUnhealthyFor_NoEffectOnOtherProviders(t *testing.T) {
	c := NewChecker(3, 30*time.Second)

	c.MarkUnhealthyFor("a", 1*time.Hour)

	if c.IsHealthy("a") {
		t.Error("provider 'a' should be unhealthy")
	}
	if !c.IsHealthy("b") {
		t.Error("provider 'b' should remain healthy")
	}
}

// TestChecker_MarkUnhealthyFor_ThresholdUsesDefaultCooldown 验证额度错误恢复后，
// 后续阈值熔断使用默认冷却时间而非过期的自定义冷却时间
func TestChecker_MarkUnhealthyFor_ThresholdUsesDefaultCooldown(t *testing.T) {
	c := NewChecker(2, 30*time.Second)

	// 先通过 MarkUnhealthyFor 设置 50ms 冷却
	c.MarkUnhealthyFor("test", 50*time.Millisecond)
	if c.IsHealthy("test") {
		t.Error("should be unhealthy after MarkUnhealthyFor")
	}

	// 等待恢复
	time.Sleep(100 * time.Millisecond)
	if !c.IsHealthy("test") {
		t.Error("should recover after custom cooldown expires")
	}

	// 现在通过阈值熔断（2 次失败），应该使用默认 30s 冷却，而不是旧的 50ms
	c.ReportFailure("test")
	c.ReportFailure("test")
	if c.IsHealthy("test") {
		t.Error("should be unhealthy after threshold failures")
	}

	// 50ms 后不应该恢复（因为应该使用默认 30s 冷却，不是旧的 50ms override）
	time.Sleep(100 * time.Millisecond)
	if c.IsHealthy("test") {
		t.Error("should still be unhealthy after 100ms when using default 30s cooldown, not stale 50ms override")
	}
}
