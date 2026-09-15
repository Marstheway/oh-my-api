package scheduler

import (
	"testing"
	"time"
)

func nowAt(hh, mm int) time.Time {
	return time.Date(2026, 1, 1, hh, mm, 0, 0, time.Local)
}

// TestIsProviderDisabledAt_EnableTimeRange 验证 enable_time_range 语义：
// 命中该类后只在窗口内可调度，窗口外跳过；多区间 OR；跨午夜窗口。
func TestIsProviderDisabledAt_EnableTimeRange(t *testing.T) {
	task := Task{EnableTimeRange: []string{"09:00-18:00"}}

	if !isProviderDisabledAt(task, nowAt(20, 0)) {
		t.Error("outside enable window must be disabled")
	}
	if isProviderDisabledAt(task, nowAt(12, 0)) {
		t.Error("inside enable window must be schedulable")
	}
	if isProviderDisabledAt(task, nowAt(9, 0)) {
		t.Error("enable window start boundary is inclusive")
	}
	if !isProviderDisabledAt(task, nowAt(18, 0)) {
		t.Error("enable window end boundary is exclusive (half-open)")
	}

	multi := Task{EnableTimeRange: []string{"09:00-12:00", "14:00-18:00"}}
	if isProviderDisabledAt(multi, nowAt(15, 0)) {
		t.Error("inside second enable window must be schedulable")
	}
	if !isProviderDisabledAt(multi, nowAt(13, 0)) {
		t.Error("between enable windows must be disabled")
	}

	crossMidnight := Task{EnableTimeRange: []string{"23:00-02:00"}}
	if !isProviderDisabledAt(crossMidnight, nowAt(12, 0)) {
		t.Error("outside cross-midnight enable window must be disabled")
	}
	if isProviderDisabledAt(crossMidnight, nowAt(23, 30)) {
		t.Error("inside cross-midnight enable window (before midnight) must be schedulable")
	}
	if isProviderDisabledAt(crossMidnight, nowAt(1, 30)) {
		t.Error("inside cross-midnight enable window (after midnight) must be schedulable")
	}
}

// TestIsProviderDisabledAt_DisableTimeRange 验证 disable_time_range 语义：窗口内跳过，窗口外可调度。
func TestIsProviderDisabledAt_DisableTimeRange(t *testing.T) {
	task := Task{DisableTimeRange: []string{"23:00-02:00"}}

	if !isProviderDisabledAt(task, nowAt(23, 30)) {
		t.Error("inside disable window must be disabled")
	}
	if !isProviderDisabledAt(task, nowAt(1, 30)) {
		t.Error("inside cross-midnight disable window must be disabled")
	}
	if isProviderDisabledAt(task, nowAt(12, 0)) {
		t.Error("outside disable window must be schedulable")
	}
}

// TestIsProviderDisabledAt_EnableAndDisableAnd 验证 enable 与 disable 同时命中时取 AND：
// 需同时位于 enable 窗口内且不在 disable 窗口内。
func TestIsProviderDisabledAt_EnableAndDisableAnd(t *testing.T) {
	task := Task{
		EnableTimeRange:  []string{"09:00-18:00"},
		DisableTimeRange: []string{"12:00-13:00"},
	}

	if !isProviderDisabledAt(task, nowAt(12, 30)) {
		t.Error("inside enable but inside disable must be disabled")
	}
	if isProviderDisabledAt(task, nowAt(10, 0)) {
		t.Error("inside enable and outside disable must be schedulable")
	}
	if !isProviderDisabledAt(task, nowAt(19, 0)) {
		t.Error("outside enable must be disabled even outside disable")
	}
}

// TestIsProviderDisabledAt_NoTimeRangesAlwaysSchedulable 验证未命中时段 rule 的 leaf 全天可调度。
func TestIsProviderDisabledAt_NoTimeRangesAlwaysSchedulable(t *testing.T) {
	task := Task{}
	if isProviderDisabledAt(task, time.Now()) {
		t.Error("task without time ranges must always be schedulable")
	}
}

// TestIsProviderDisabledAt_ParseFailureFailClosed 验证任一时段字符串解析失败 → fail-closed：
// 该 leaf 不可调度（而不是当作全天可用）。
func TestIsProviderDisabledAt_ParseFailureFailClosed(t *testing.T) {
	now := time.Now()

	task := Task{DisableTimeRange: []string{"garbage"}}
	if !isProviderDisabledAt(task, now) {
		t.Error("invalid disable_time_range must make leaf unschedulable (fail-closed)")
	}

	task = Task{EnableTimeRange: []string{"not-a-range"}}
	if !isProviderDisabledAt(task, now) {
		t.Error("invalid enable_time_range must make leaf unschedulable (fail-closed)")
	}

	// 合法区间 + 一个非法区间：整体 fail-closed，不得部分生效。
	task = Task{DisableTimeRange: []string{"09:00-12:00", "bad"}}
	if !isProviderDisabledAt(task, now) {
		t.Error("mixed valid/invalid ranges must fail closed")
	}
}

// TestIsProviderDisabledAt_InvalidRangeSkipsAtStrategyLevel 验证 invalid 时段导致
// allProvidersDisabled 直接短路（无可用候选），返回 ErrNoProviderAvailable 而不是尝试请求。
func TestIsProviderDisabledAt_InvalidRangeSkipsAtStrategyLevel(t *testing.T) {
	now := time.Now()
	tasks := []Task{
		{ProviderName: "a", DisableTimeRange: []string{"invalid"}},
	}
	if !allProvidersDisabled(tasks, now) {
		t.Error("allProvidersDisabled must be true when the only candidate has invalid time range")
	}
}
