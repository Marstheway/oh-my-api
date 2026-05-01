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
