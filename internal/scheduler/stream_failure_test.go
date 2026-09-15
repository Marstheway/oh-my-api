package scheduler

import (
	"testing"

	"github.com/Marstheway/oh-my-api/internal/health"
)

func TestScheduler_ReportStreamFailure(t *testing.T) {
	s, _ := newTestScheduler(10)
	key := health.MakeHealthKey("test", "openai")

	// 前两次流中断失败不摘除
	s.ReportStreamFailure("test", "openai")
	s.ReportStreamFailure("test", "openai")
	if !s.health.IsHealthy(key) {
		t.Fatalf("provider should be healthy after 2 stream failures")
	}

	// 第三次流中断失败：达到阈值摘除
	s.ReportStreamFailure("test", "openai")
	if s.health.IsHealthy(key) {
		t.Fatalf("provider should be unhealthy after 3 stream failures")
	}

	// 成功上报后重置失败计数，恢复健康
	s.health.ReportSuccess(key)
	if !s.health.IsHealthy(key) {
		t.Fatalf("provider should recover after success")
	}
}

func TestScheduler_ReportStreamFailure_NilSafe(t *testing.T) {
	var s *Scheduler
	// 不应 panic
	s.ReportStreamFailure("test", "openai")
}
