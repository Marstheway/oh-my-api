package scheduler

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

// ============================================================================
// LatencyTracker 测试
// ============================================================================

func TestLatencyTracker_GetStats_ZeroSamples(t *testing.T) {
	tracker := NewLatencyTracker()
	now := time.Now()

	stats := tracker.GetStats([]string{"a/model"}, now)
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(stats))
	}
	if stats[0].SampleCount != 0 {
		t.Errorf("SampleCount = %d, want 0", stats[0].SampleCount)
	}
	if stats[0].MeanTTFT != 0 {
		t.Errorf("MeanTTFT = %f, want 0", stats[0].MeanTTFT)
	}
	if stats[0].StdDevTTFT != 0 {
		t.Errorf("StdDevTTFT = %f, want 0", stats[0].StdDevTTFT)
	}
	if !stats[0].LastSuccessAt.IsZero() {
		t.Errorf("LastSuccessAt should be zero")
	}
	if !stats[0].LastSelectedAt.IsZero() {
		t.Errorf("LastSelectedAt should be zero")
	}
}

func TestLatencyTracker_GetStats_OneSample(t *testing.T) {
	tracker := NewLatencyTracker()
	now := time.Now()

	tracker.RecordSuccess("a/model", 100.0, now)
	stats := tracker.GetStats([]string{"a/model"}, now)

	if stats[0].SampleCount != 1 {
		t.Fatalf("SampleCount = %d, want 1", stats[0].SampleCount)
	}
	if stats[0].MeanTTFT != 100.0 {
		t.Errorf("MeanTTFT = %f, want 100.0", stats[0].MeanTTFT)
	}
	// 单个样本标准差为 0
	if stats[0].StdDevTTFT != 0 {
		t.Errorf("StdDevTTFT = %f, want 0 (single sample)", stats[0].StdDevTTFT)
	}
	if !stats[0].LastSuccessAt.Equal(now) {
		t.Errorf("LastSuccessAt should equal now")
	}
}

func TestLatencyTracker_GetStats_MultipleSamples(t *testing.T) {
	tracker := NewLatencyTracker()
	now := time.Now()

	// 写入 5 个样本: 100, 200, 300, 400, 500
	for i := 1; i <= 5; i++ {
		tracker.RecordSuccess("a/model", float64(i*100), now.Add(time.Duration(i)*time.Second))
	}

	stats := tracker.GetStats([]string{"a/model"}, now.Add(10*time.Second))
	if stats[0].SampleCount != 5 {
		t.Fatalf("SampleCount = %d, want 5", stats[0].SampleCount)
	}
	// 均值应为 300
	if math.Abs(stats[0].MeanTTFT-300.0) > 0.01 {
		t.Errorf("MeanTTFT = %f, want ~300.0", stats[0].MeanTTFT)
	}
	// 标准差 sqrt(((100-300)^2 + ... + (500-300)^2) / 5)
	// = sqrt((40000+10000+0+10000+40000)/5) = sqrt(20000) ≈ 141.42
	if math.Abs(stats[0].StdDevTTFT-141.42) > 0.5 {
		t.Errorf("StdDevTTFT = %f, want ~141.42", stats[0].StdDevTTFT)
	}
	// LastSuccessAt 应为最新样本的时间
	if !stats[0].LastSuccessAt.Equal(now.Add(5 * time.Second)) {
		t.Errorf("LastSuccessAt should be now+5s")
	}
}

func TestLatencyTracker_GetStats_ExpiredSamples(t *testing.T) {
	tracker := NewLatencyTracker()
	now := time.Now()

	// 写入一个 31 分钟前的样本（已过期）
	tracker.RecordSuccess("a/model", 100.0, now.Add(-31*time.Minute))
	// 写入一个 29 分钟前的样本（未过期）
	tracker.RecordSuccess("a/model", 200.0, now.Add(-29*time.Minute))

	stats := tracker.GetStats([]string{"a/model"}, now)
	if stats[0].SampleCount != 1 {
		t.Fatalf("SampleCount = %d, want 1 (only non-expired)", stats[0].SampleCount)
	}
	if stats[0].MeanTTFT != 200.0 {
		t.Errorf("MeanTTFT = %f, want 200.0", stats[0].MeanTTFT)
	}
}

func TestLatencyTracker_GetStats_AllExpired(t *testing.T) {
	tracker := NewLatencyTracker()
	now := time.Now()

	// 所有样本都超过 30 分钟
	tracker.RecordSuccess("a/model", 100.0, now.Add(-31*time.Minute))
	tracker.RecordSuccess("a/model", 200.0, now.Add(-32*time.Minute))

	stats := tracker.GetStats([]string{"a/model"}, now)
	if stats[0].SampleCount != 0 {
		t.Fatalf("SampleCount = %d, want 0 (all expired)", stats[0].SampleCount)
	}
	if stats[0].MeanTTFT != 0 {
		t.Errorf("MeanTTFT = %f, want 0", stats[0].MeanTTFT)
	}
}

func TestLatencyTracker_WindowCapacity(t *testing.T) {
	tracker := NewLatencyTracker()
	now := time.Now()

	// 写入 25 个样本（超过容量 20）
	for i := 0; i < 25; i++ {
		tracker.RecordSuccess("a/model", float64(100+i*10), now.Add(time.Duration(i)*time.Second))
	}

	stats := tracker.GetStats([]string{"a/model"}, now.Add(30*time.Second))
	// 应只保留最近 20 个
	if stats[0].SampleCount != 20 {
		t.Fatalf("SampleCount = %d, want 20 (window cap)", stats[0].SampleCount)
	}
	// 前 5 个样本被丢弃，保留索引 5-24，TTFT 值: 150, 160, ..., 340
	// 均值 = (150+340)*20/2/20 = 4900/20 = 245
	if math.Abs(stats[0].MeanTTFT-245.0) > 0.01 {
		t.Errorf("MeanTTFT = %f, want ~245.0", stats[0].MeanTTFT)
	}
}

func TestLatencyTracker_RecordSelection(t *testing.T) {
	tracker := NewLatencyTracker()
	now := time.Now()

	tracker.RecordSelection("a/model", now)
	stats := tracker.GetStats([]string{"a/model"}, now)

	if !stats[0].LastSelectedAt.Equal(now) {
		t.Errorf("LastSelectedAt should equal now")
	}
	// SampleCount 应为 0（没有 RecordSuccess）
	if stats[0].SampleCount != 0 {
		t.Errorf("SampleCount = %d, want 0", stats[0].SampleCount)
	}
}

func TestLatencyTracker_GetStats_MultipleKeys(t *testing.T) {
	tracker := NewLatencyTracker()
	now := time.Now()

	tracker.RecordSuccess("a/model-a", 100.0, now)
	tracker.RecordSuccess("b/model-b", 200.0, now)

	stats := tracker.GetStats([]string{"a/model-a", "b/model-b"}, now)
	if len(stats) != 2 {
		t.Fatalf("expected 2 stats, got %d", len(stats))
	}

	// 输出顺序与输入 key 顺序一致
	if stats[0].Key != "a/model-a" {
		t.Errorf("stats[0].Key = %q, want %q", stats[0].Key, "a/model-a")
	}
	if stats[0].MeanTTFT != 100.0 {
		t.Errorf("stats[0].MeanTTFT = %f, want 100.0", stats[0].MeanTTFT)
	}
	if stats[1].Key != "b/model-b" {
		t.Errorf("stats[1].Key = %q, want %q", stats[1].Key, "b/model-b")
	}
	if stats[1].MeanTTFT != 200.0 {
		t.Errorf("stats[1].MeanTTFT = %f, want 200.0", stats[1].MeanTTFT)
	}
}

func TestLatencyTracker_GetStats_UnknownKey(t *testing.T) {
	tracker := NewLatencyTracker()
	now := time.Now()

	stats := tracker.GetStats([]string{"unknown/model"}, now)
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(stats))
	}
	if stats[0].Key != "unknown/model" {
		t.Errorf("Key = %q, want %q", stats[0].Key, "unknown/model")
	}
	if stats[0].SampleCount != 0 {
		t.Errorf("SampleCount = %d, want 0", stats[0].SampleCount)
	}
}

func TestLatencyTracker_ConcurrentReadWrite(t *testing.T) {
	tracker := NewLatencyTracker()
	now := time.Now()
	keys := []string{"a/model-a", "b/model-b", "c/model-c"}

	var wg sync.WaitGroup

	// 并发写入
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := keys[idx%len(keys)]
			tracker.RecordSuccess(key, float64(100+idx), now)
			tracker.RecordSelection(key, now)
		}(i)
	}

	// 并发读取
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = tracker.GetStats(keys, now)
		}()
	}

	wg.Wait()

	// 最终读取应成功且不 panic
	stats := tracker.GetStats(keys, now)
	if len(stats) != 3 {
		t.Fatalf("expected 3 stats, got %d", len(stats))
	}
	for _, s := range stats {
		if s.SampleCount > 20 {
			t.Errorf("SampleCount for %s = %d, should not exceed window cap 20", s.Key, s.SampleCount)
		}
	}
}

// ============================================================================
// ComputeScore 测试（通过 RankCandidates 验证）
// ============================================================================

func TestRankCandidates_ColdStartScore(t *testing.T) {
	now := time.Now()
	stats := []CandidateStats{
		{Key: "a/model-a", SampleCount: 0, LastSelectedAt: now},
		{Key: "b/model-b", SampleCount: 5, MeanTTFT: 1500.0, LastSuccessAt: now, LastSelectedAt: now},
	}

	scores := RankCandidates(stats, now)
	if len(scores) != 2 {
		t.Fatalf("expected 2 scores, got %d", len(scores))
	}

	// b 有样本: score = 1500 + 300/5 = 1560，应排第一
	if scores[0].Key != "b/model-b" {
		t.Errorf("expected b/model-b (with samples, lower score) first, got %s", scores[0].Key)
	}
	// a 冷启动: 分数固定为 2000ms
	if scores[1].Score != coldStartScore {
		t.Errorf("cold start score = %f, want %f", scores[1].Score, coldStartScore)
	}
}

func TestRankCandidates_ScoreFormula(t *testing.T) {
	now := time.Now()

	// n=1: score = mean_ttft + 300/1 + 0 = 200 + 300 = 500
	stats := []CandidateStats{
		{Key: "a/model-a", SampleCount: 1, MeanTTFT: 200.0, LastSuccessAt: now, LastSelectedAt: now},
		{Key: "b/model-b", SampleCount: 5, MeanTTFT: 300.0, LastSuccessAt: now, LastSelectedAt: now},
	}

	scores := RankCandidates(stats, now)
	if len(scores) != 2 {
		t.Fatalf("expected 2 scores, got %d", len(scores))
	}

	// n=1: 200 + 300/1 = 500
	// n=5: 300 + 300/5 = 360
	if math.Abs(scores[0].Score-360.0) > 0.01 {
		t.Errorf("score for n=5 = %f, want ~360.0", scores[0].Score)
	}
	if math.Abs(scores[1].Score-500.0) > 0.01 {
		t.Errorf("score for n=1 = %f, want ~500.0", scores[1].Score)
	}
}

func TestRankCandidates_StalePenalty(t *testing.T) {
	now := time.Now()

	// 10 分钟前的成功样本: stale_penalty = min(300, 15*10) = 150
	stats := []CandidateStats{
		{
			Key:            "a/model-a",
			SampleCount:    3,
			MeanTTFT:       200.0,
			LastSuccessAt:  now.Add(-10 * time.Minute),
			LastSelectedAt: now,
		},
		{
			Key:            "b/model-b",
			SampleCount:    3,
			MeanTTFT:       300.0,
			LastSuccessAt:  now, // 刚成功
			LastSelectedAt: now,
		},
	}

	scores := RankCandidates(stats, now)

	// b: 300 + 300/3 + 0 = 400
	// a: 200 + 300/3 + 15*10 = 200 + 100 + 150 = 450
	if math.Abs(scores[0].Score-400.0) > 0.01 {
		t.Errorf("b score = %f, want ~400.0", scores[0].Score)
	}
	if math.Abs(scores[1].Score-450.0) > 0.01 {
		t.Errorf("a score = %f, want ~450.0", scores[1].Score)
	}
}

func TestRankCandidates_StalePenaltyCap(t *testing.T) {
	now := time.Now()

	// 30 分钟前: stale_penalty = min(300, 15*30) = 300 (cap)
	stats := []CandidateStats{
		{
			Key:            "a/model-a",
			SampleCount:    3,
			MeanTTFT:       200.0,
			LastSuccessAt:  now.Add(-30 * time.Minute),
			LastSelectedAt: now,
		},
	}

	scores := RankCandidates(stats, now)
	// 200 + 300/3 + 300 = 200 + 100 + 300 = 600
	if math.Abs(scores[0].Score-600.0) > 0.01 {
		t.Errorf("score = %f, want ~600.0", scores[0].Score)
	}
}

func TestRankCandidates_ForcedCorrection(t *testing.T) {
	now := time.Now()

	// 候选 a 超过 30 分钟未被调度
	stats := []CandidateStats{
		{
			Key:            "a/model-a",
			SampleCount:    5,
			MeanTTFT:       100.0,
			LastSelectedAt: now.Add(-31 * time.Minute),
			LastSuccessAt:  now,
		},
		{
			Key:            "b/model-b",
			SampleCount:    5,
			MeanTTFT:       50.0, // 更好的 score
			LastSelectedAt: now,  // 最近被调度
			LastSuccessAt:  now,
		},
	}

	scores := RankCandidates(stats, now)

	// a 应被强制纠偏到第一位
	if scores[0].Key != "a/model-a" {
		t.Errorf("expected a/model-a (forced correction) first, got %s", scores[0].Key)
	}
}

func TestRankCandidates_MultipleForcedCorrection(t *testing.T) {
	now := time.Now()

	// 两个候选都超过 30 分钟未被调度，a 更久
	stats := []CandidateStats{
		{
			Key:            "a/model-a",
			SampleCount:    5,
			MeanTTFT:       100.0,
			LastSelectedAt: now.Add(-60 * time.Minute), // 最久未调度
			LastSuccessAt:  now,
		},
		{
			Key:            "b/model-b",
			SampleCount:    5,
			MeanTTFT:       50.0,
			LastSelectedAt: now.Add(-31 * time.Minute), // 也未调度但较短
			LastSuccessAt:  now,
		},
	}

	scores := RankCandidates(stats, now)

	// 最久未调度的 a 应排在第一位
	if scores[0].Key != "a/model-a" {
		t.Errorf("expected a/model-a (oldest unselected) first, got %s", scores[0].Key)
	}
}

func TestRankCandidates_StableTieBreak(t *testing.T) {
	now := time.Now()

	// 两个候选分数相同
	stats := []CandidateStats{
		{Key: "b/model-z", SampleCount: 5, MeanTTFT: 240.0, LastSuccessAt: now, LastSelectedAt: now},
		{Key: "a/model-a", SampleCount: 5, MeanTTFT: 240.0, LastSuccessAt: now, LastSelectedAt: now},
	}

	scores := RankCandidates(stats, now)

	// 分数相同时按 key 字典序
	if scores[0].Key != "a/model-a" {
		t.Errorf("expected a/model-a (lexicographically first) first, got %s", scores[0].Key)
	}
	if scores[1].Key != "b/model-z" {
		t.Errorf("expected b/model-z second, got %s", scores[1].Key)
	}
}

func TestRankCandidates_AllSameSamples(t *testing.T) {
	now := time.Now()

	// 所有样本 TTFT 相同
	tracker := NewLatencyTracker()
	for i := 0; i < 5; i++ {
		tracker.RecordSuccess("a/model", 200.0, now)
	}

	stats := tracker.GetStats([]string{"a/model"}, now)
	if stats[0].StdDevTTFT != 0 {
		t.Errorf("StdDevTTFT for identical samples = %f, want 0", stats[0].StdDevTTFT)
	}
	if stats[0].MeanTTFT != 200.0 {
		t.Errorf("MeanTTFT = %f, want 200.0", stats[0].MeanTTFT)
	}
}

func TestRankCandidates_NoCorrectionWhenNotExceeded(t *testing.T) {
	now := time.Now()

	// 候选刚被调度，不应触发纠偏
	stats := []CandidateStats{
		{
			Key:            "a/model-a",
			SampleCount:    5,
			MeanTTFT:       100.0,
			LastSelectedAt: now, // 刚被调度
			LastSuccessAt:  now,
		},
	}

	scores := RankCandidates(stats, now)
	// 应只有一个候选
	if len(scores) != 1 {
		t.Fatalf("expected 1 score, got %d", len(scores))
	}
	if scores[0].Key != "a/model-a" {
		t.Errorf("expected a/model-a, got %s", scores[0].Key)
	}
}

func TestRankCandidates_ZeroSelectedAt_TriggersCorrection(t *testing.T) {
	now := time.Now()

	// LastSelectedAt 为零值（从未被调度），应触发强制纠偏，
	// 且优先于有实际 LastSelectedAt 但同样超过阈值的候选。
	stats := []CandidateStats{
		{
			Key:            "a/model-a",
			SampleCount:    5,
			MeanTTFT:       500.0, // worse score
			LastSelectedAt: now.Add(-31 * time.Minute),
			LastSuccessAt:  now,
		},
		{
			Key:         "b/model-b",
			SampleCount: 5,
			MeanTTFT:    100.0, // better score, but never selected
			// LastSelectedAt 为零值
			LastSuccessAt: now,
		},
	}

	scores := RankCandidates(stats, now)
	if len(scores) != 2 {
		t.Fatalf("expected 2 scores, got %d", len(scores))
	}

	// b 从未被调度，零值 LastSelectedAt 应触发强制纠偏
	if scores[0].Key != "b/model-b" {
		t.Errorf("expected b/model-b (never selected, forced correction) first, got %s", scores[0].Key)
	}
}

func TestRankCandidates_ZeroSelectedAt_SingleCandidate(t *testing.T) {
	now := time.Now()

	// 单个候选，LastSelectedAt 为零值
	stats := []CandidateStats{
		{
			Key:           "a/model-a",
			SampleCount:   5,
			MeanTTFT:      100.0,
			LastSuccessAt: now,
			// LastSelectedAt 为零值
		},
	}

	scores := RankCandidates(stats, now)
	if len(scores) != 1 {
		t.Fatalf("expected 1 score, got %d", len(scores))
	}
	// 单候选即使触发纠偏也是它本身排第一
	if scores[0].Key != "a/model-a" {
		t.Errorf("expected a/model-a, got %s", scores[0].Key)
	}
}

func TestRankCandidates_StdDevEdgeCases(t *testing.T) {
	now := time.Now()

	// 0 样本: StdDevTTFT = 0
	tracker := NewLatencyTracker()
	stats := tracker.GetStats([]string{"a/model"}, now)
	if stats[0].StdDevTTFT != 0 {
		t.Errorf("StdDevTTFT for 0 samples = %f, want 0", stats[0].StdDevTTFT)
	}

	// 1 样本: StdDevTTFT = 0
	tracker.RecordSuccess("a/model", 100.0, now)
	stats = tracker.GetStats([]string{"a/model"}, now)
	if stats[0].StdDevTTFT != 0 {
		t.Errorf("StdDevTTFT for 1 sample = %f, want 0", stats[0].StdDevTTFT)
	}

	// N 样本（N>1）: StdDevTTFT 应 > 0（如果值不同）
	tracker2 := NewLatencyTracker()
	tracker2.RecordSuccess("b/model", 100.0, now)
	tracker2.RecordSuccess("b/model", 200.0, now)
	stats2 := tracker2.GetStats([]string{"b/model"}, now)
	if stats2[0].StdDevTTFT <= 0 {
		t.Errorf("StdDevTTFT for 2 different samples = %f, want > 0", stats2[0].StdDevTTFT)
	}
}

// ============================================================================
// recordAdaptiveTTFTIfNeeded 测试
// ============================================================================

func TestRecordAdaptiveTTFT_OnlyStreamSuccess(t *testing.T) {
	resetLatencyTracker()
	tracker := GetLatencyTracker()

	now := time.Now()
	task := Task{ProviderName: "p", UpstreamModel: "m"}
	key := "p/m"

	// 非 success 不应写入
	result := &Result{FailureKind: FailureKindSoft, StreamTTFT: 100 * time.Millisecond}
	recordAdaptiveTTFTIfNeeded(result, task)
	stats := tracker.GetStats([]string{key}, now)
	if stats[0].SampleCount != 0 {
		t.Errorf("soft failure should not write sample, got %d", stats[0].SampleCount)
	}

	// success 但无 StreamTTFT 不应写入
	result = &Result{FailureKind: FailureKindSuccess, StreamTTFT: 0}
	recordAdaptiveTTFTIfNeeded(result, task)
	stats = tracker.GetStats([]string{key}, now)
	if stats[0].SampleCount != 0 {
		t.Errorf("success without StreamTTFT should not write, got %d", stats[0].SampleCount)
	}

	// 成功流式写入
	result = &Result{FailureKind: FailureKindSuccess, StreamTTFT: 150 * time.Millisecond}
	recordAdaptiveTTFTIfNeeded(result, task)
	stats = tracker.GetStats([]string{key}, now)
	if stats[0].SampleCount != 1 {
		t.Errorf("stream success should write sample, got %d", stats[0].SampleCount)
	}
	if stats[0].MeanTTFT != 150.0 {
		t.Errorf("MeanTTFT = %f, want 150.0", stats[0].MeanTTFT)
	}
}

func TestRecordAdaptiveTTFT_NilResult(t *testing.T) {
	resetLatencyTracker()
	tracker := GetLatencyTracker()

	task := Task{ProviderName: "p", UpstreamModel: "m"}

	// nil result 不应 panic
	recordAdaptiveTTFTIfNeeded(nil, task)

	now := time.Now()
	stats := tracker.GetStats([]string{"p/m"}, now)
	if stats[0].SampleCount != 0 {
		t.Errorf("nil result should not write, got %d", stats[0].SampleCount)
	}
}

func TestAdaptiveCandidateKey(t *testing.T) {
	task := Task{ProviderName: "openai", UpstreamModel: "gpt-4o"}
	key := adaptiveCandidateKey(task)
	if key != "openai/gpt-4o" {
		t.Errorf("key = %q, want %q", key, "openai/gpt-4o")
	}
}

func TestGetLatencyTracker_ConcurrentAccess(t *testing.T) {
	// 验证 GetLatencyTracker 单例的并发安全性
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task := Task{ProviderName: "p", UpstreamModel: "m"}
			result := &Result{
				FailureKind: FailureKindSuccess,
				StreamTTFT:  100 * time.Millisecond,
			}
			recordAdaptiveTTFTIfNeeded(result, task)
		}()
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = GetLatencyTracker()
		}()
	}
	wg.Wait()
}

// ============================================================================
// AdaptiveStrategy 端到端测试
// ============================================================================

func TestAdaptiveStrategy_SingleProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"test": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewAdaptiveStrategy(client, rl, h, 500*time.Millisecond, 0, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Provider: providers["test"], UpstreamModel: "model", Request: req, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "test" {
		t.Errorf("winner = %q, want %q", result.Winner, "test")
	}
}

func TestAdaptiveStrategy_FirstCandidateFails_SecondSuccess(t *testing.T) {
	// 第一个候选返回 500，第二个候选返回 200
	var firstCalled, secondCalled bool

	firstSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalled = true
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer firstSrv.Close()

	secondSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success"}`))
	}))
	defer secondSrv.Close()

	providers := map[string]config.ProviderConfig{
		"first": {
			Endpoint:  firstSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"second": {
			Endpoint:  secondSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	resetLatencyTracker()
	tracker := GetLatencyTracker()
	// 给两个候选都预写样本，first 有更好的 score
	now := time.Now()
	tracker.RecordSuccess("first/model", 100.0, now)
	tracker.RecordSuccess("second/model", 200.0, now)
	tracker.RecordSelection("first/model", now)
	tracker.RecordSelection("second/model", now)

	strategy := NewAdaptiveStrategy(client, rl, h, 500*time.Millisecond, 0, 0)

	firstReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, firstSrv.URL, nil)
	secondReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, secondSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "first", Provider: providers["first"], UpstreamModel: "model", Request: firstReq, Weight: 1},
		{ProviderName: "second", Provider: providers["second"], UpstreamModel: "model", Request: secondReq, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if !firstCalled {
		t.Error("first candidate should have been called")
	}
	if !secondCalled {
		t.Error("second candidate should have been called as fallback")
	}
	if result.Winner != "second" {
		t.Errorf("winner = %q, want %q", result.Winner, "second")
	}
}

func TestAdaptiveStrategy_NoHealthyProvider(t *testing.T) {
	client := provider.NewClient(nil, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(nil)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewAdaptiveStrategy(client, rl, h, 500*time.Millisecond, 0, 0)

	_, err := strategy.Execute(context.Background(), []Task{})
	if err != ErrNoTasks {
		t.Errorf("error = %v, want %v", err, ErrNoTasks)
	}
}

func TestAdaptiveStrategy_AllDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"disabled": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewAdaptiveStrategy(client, rl, h, 500*time.Millisecond, 0, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "disabled", Provider: providers["disabled"], UpstreamModel: "model", Request: req, DisableTimeRange: []string{"00:00-24:00"}},
	}

	_, err := strategy.Execute(context.Background(), tasks)
	if err != ErrNoProviderAvailable {
		t.Errorf("error = %v, want %v", err, ErrNoProviderAvailable)
	}
}

func TestAdaptiveStrategy_AllProvidersUnhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"a": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"b": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewAdaptiveStrategy(client, rl, h, 500*time.Millisecond, 0, 0)

	// 标记所有 provider 为不健康（设置足够长的 cooldown）
	h.MarkUnhealthyFor(health.MakeHealthKey("a", "openai"), time.Hour)
	h.MarkUnhealthyFor(health.MakeHealthKey("b", "openai"), time.Hour)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "a", Provider: providers["a"], UpstreamModel: "model", OutboundProtocol: "openai", Request: req},
		{ProviderName: "b", Provider: providers["b"], UpstreamModel: "model", OutboundProtocol: "openai", Request: req},
	}

	_, err := strategy.Execute(context.Background(), tasks)
	if err != ErrNoProviderAvailable {
		t.Errorf("error = %v, want %v", err, ErrNoProviderAvailable)
	}
}

// TestAdaptiveStrategy_RankOrderIsFixed 验证 adaptive 按固定 score 排序，而非随机。
func TestAdaptiveStrategy_RankOrderIsFixed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"fast": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"slow": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	resetLatencyTracker()
	tracker := GetLatencyTracker()
	now := time.Now()
	// fast 有更好的 TTFT
	tracker.RecordSuccess("fast/model", 100.0, now)
	tracker.RecordSuccess("slow/model", 500.0, now)
	tracker.RecordSelection("fast/model", now)
	tracker.RecordSelection("slow/model", now)

	strategy := NewAdaptiveStrategy(client, rl, h, 500*time.Millisecond, 0, 0)

	// 多次执行，验证 fast 总是被选为首选
	for i := 0; i < 10; i++ {
		fastReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
		slowReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
		tasks := []Task{
			{ProviderName: "slow", Provider: providers["slow"], UpstreamModel: "model", Request: slowReq, Weight: 1},
			{ProviderName: "fast", Provider: providers["fast"], UpstreamModel: "model", Request: fastReq, Weight: 1},
		}

		result, err := strategy.Execute(context.Background(), tasks)
		if err != nil {
			t.Fatalf("iteration %d: Execute failed: %v", i, err)
		}
		result.Response.Body.Close()

		// fast 始终胜出（因为更好的 score 且都返回 200）
		if result.Winner != "fast" {
			t.Errorf("iteration %d: winner = %q, want %q", i, result.Winner, "fast")
		}
	}
}

// TestAdaptiveStrategy_MarkSelectedBeforeRequest 验证选中候选时先更新 MarkSelected。
func TestAdaptiveStrategy_MarkSelectedBeforeRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"p": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	resetLatencyTracker()
	now := time.Now()

	strategy := NewAdaptiveStrategy(client, rl, h, 500*time.Millisecond, 0, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "p", Provider: providers["p"], UpstreamModel: "model", Request: req, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	// 验证 LastSelectedAt 被更新
	stats := GetLatencyTracker().GetStats([]string{"p/model"}, now.Add(time.Second))
	if stats[0].LastSelectedAt.IsZero() {
		t.Error("LastSelectedAt should be non-zero after execution")
	}
}

// ============================================================================
// ExecuteNode + adaptive 端到端测试
// ============================================================================

func TestExecuteNode_Adaptive_SingleProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"p": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0, 0)

	node := &RunNode{
		IsLeaf: false,
		Name:   "root",
		Mode:   "adaptive",
		Children: []*RunNode{
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "p",
					Provider:         providers["p"],
					UpstreamModel:    "model",
					OutboundProtocol: "openai.chat",
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, srv.URL, nil)
				},
			},
		},
	}

	result, err := scheduler.ExecuteNode(context.Background(), node)
	if err != nil {
		t.Fatalf("ExecuteNode failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
}

func TestExecuteNode_Adaptive_FirstFailsSecondSuccess(t *testing.T) {
	var firstCalled, secondCalled bool

	firstSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalled = true
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"fail"}`))
	}))
	defer firstSrv.Close()

	secondSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}]}`))
	}))
	defer secondSrv.Close()

	providers := map[string]config.ProviderConfig{
		"first": {
			Endpoint:  firstSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"second": {
			Endpoint:  secondSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0, 0)

	// 给两个候选预写样本，first 有更好的 score
	now := time.Now()
	GetLatencyTracker().RecordSuccess("first/model", 100.0, now)
	GetLatencyTracker().RecordSuccess("second/model", 200.0, now)
	GetLatencyTracker().RecordSelection("first/model", now)
	GetLatencyTracker().RecordSelection("second/model", now)

	node := &RunNode{
		IsLeaf: false,
		Name:   "root",
		Mode:   "adaptive",
		Children: []*RunNode{
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "first",
					Provider:         providers["first"],
					UpstreamModel:    "model",
					OutboundProtocol: "openai.chat",
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, firstSrv.URL, nil)
				},
			},
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "second",
					Provider:         providers["second"],
					UpstreamModel:    "model",
					OutboundProtocol: "openai.chat",
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, secondSrv.URL, nil)
				},
			},
		},
	}

	result, err := scheduler.ExecuteNode(context.Background(), node)
	if err != nil {
		t.Fatalf("ExecuteNode failed: %v", err)
	}
	defer result.Response.Body.Close()

	if !firstCalled {
		t.Error("first candidate should have been called")
	}
	if !secondCalled {
		t.Error("second candidate should have been called as fallback")
	}
	if result.Winner != "second" {
		t.Errorf("winner = %q, want %q", result.Winner, "second")
	}
	if result.FailureKind != FailureKindSuccess {
		t.Errorf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
}

func TestExecuteNode_Adaptive_RateLimitedThenSuccess(t *testing.T) {
	var secondCalled bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"limited": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 1},
		},
		"ok": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	// 消耗 limited provider 的令牌
	rl.Allow("limited", "model", 0)

	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0, 0)

	// 给 limited 更好的 score
	now := time.Now()
	GetLatencyTracker().RecordSuccess("limited/model", 100.0, now)
	GetLatencyTracker().RecordSuccess("ok/model", 200.0, now)
	GetLatencyTracker().RecordSelection("limited/model", now)
	GetLatencyTracker().RecordSelection("ok/model", now)

	node := &RunNode{
		IsLeaf: false,
		Name:   "root",
		Mode:   "adaptive",
		Children: []*RunNode{
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "limited",
					Provider:         providers["limited"],
					UpstreamModel:    "model",
					OutboundProtocol: "openai.chat",
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, srv.URL, nil)
				},
			},
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "ok",
					Provider:         providers["ok"],
					UpstreamModel:    "model",
					OutboundProtocol: "openai.chat",
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, srv.URL, nil)
				},
			},
		},
	}

	result, err := scheduler.ExecuteNode(context.Background(), node)
	if err != nil {
		t.Fatalf("ExecuteNode failed: %v", err)
	}
	defer result.Response.Body.Close()

	if !secondCalled {
		t.Error("second candidate should have been called after first was rate limited")
	}
	if result.FailureKind != FailureKindSuccess {
		t.Errorf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
}

func TestExecuteNode_Adaptive_SoftFailureThenSuccess(t *testing.T) {
	var secondCalled bool

	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"choices": [{"message": {"role": "assistant", "content": "I can't help with that request."}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
		}`))
	}))
	defer softSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	scheduler := New(rl, client, h, 500*time.Millisecond, 0, 0)

	// 给 soft 更好的 score，让它成为首选
	now := time.Now()
	GetLatencyTracker().RecordSuccess("soft/model", 100.0, now)
	GetLatencyTracker().RecordSuccess("success/model", 200.0, now)
	GetLatencyTracker().RecordSelection("soft/model", now)
	GetLatencyTracker().RecordSelection("success/model", now)

	node := &RunNode{
		IsLeaf: false,
		Name:   "root",
		Mode:   "adaptive",
		Children: []*RunNode{
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "soft",
					Provider:         providers["soft"],
					UpstreamModel:    "model",
					OutboundProtocol: "openai.chat",
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, softSrv.URL, nil)
				},
			},
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "success",
					Provider:         providers["success"],
					UpstreamModel:    "model",
					OutboundProtocol: "openai.chat",
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, successSrv.URL, nil)
				},
			},
		},
	}

	result, err := scheduler.ExecuteNode(context.Background(), node)
	if err != nil {
		t.Fatalf("ExecuteNode failed: %v", err)
	}
	defer result.Response.Body.Close()

	if !secondCalled {
		t.Error("second candidate should have been called after soft failure")
	}
	// soft failure 不会阻止继续尝试，如果 soft 是唯一候选才返回 soft
	if result.FailureKind != FailureKindSuccess {
		t.Errorf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
}

// TestExecuteNode_Adaptive_HealthIsolation 验证同 provider 不同 protocol 的健康隔离。
func TestExecuteNode_Adaptive_HealthIsolation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"shared": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai.chat", "anthropic.messages"},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	// 将 shared 的 anthropic 协议侧标记为不健康
	h.MarkUnhealthyFor("shared/anthropic", 0)

	scheduler := New(rl, client, h, 500*time.Millisecond, 0, 0)

	node := &RunNode{
		IsLeaf: false,
		Name:   "root",
		Mode:   "adaptive",
		Children: []*RunNode{
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "shared",
					Provider:         providers["shared"],
					UpstreamModel:    "model",
					OutboundProtocol: "anthropic.messages",
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, srv.URL, nil)
				},
			},
			{
				IsLeaf: true,
				Task: Task{
					ProviderName:     "shared",
					Provider:         providers["shared"],
					UpstreamModel:    "model",
					OutboundProtocol: "openai.chat",
				},
				RequestFactory: func() (*http.Request, error) {
					return http.NewRequest(http.MethodPost, srv.URL, nil)
				},
			},
		},
	}

	result, err := scheduler.ExecuteNode(context.Background(), node)
	if err != nil {
		t.Fatalf("ExecuteNode failed: %v", err)
	}
	defer result.Response.Body.Close()

	// openai protocol 侧健康，应成功
	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
	if result.Winner != "shared" {
		t.Errorf("winner = %q, want %q", result.Winner, "shared")
	}
}

// TestAdaptiveStrategy_UnhealthyMidIteration 验证在执行过程中某个候选变为 unhealthy 后，
// 同 health key 的兄弟被跳过，但下一个不同 provider 的候选仍被尝试。
// 覆盖 removeUnhealthyFromRemaining 保留当前元素、仅移除兄弟的语义。
func TestAdaptiveStrategy_UnhealthyMidIteration(t *testing.T) {
	var callCount int
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		mu.Unlock()
		// 默认返回 500，使 provider 连续失败触发不健康
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"fail"}`))
	}))
	defer srv.Close()

	// all-same 是三个候选共享同一个 upstream server
	providers := map[string]config.ProviderConfig{
		"prov-a": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"prov-b": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"prov-c": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(1, 30*time.Second) // 阈值 1，一次失败就标记 unhealthy
	resetLatencyTracker()
	tracker := GetLatencyTracker()

	// 给三个候选预写样本，prov-a 排第一、prov-b 排第二、prov-c 排第三
	now := time.Now()
	tracker.RecordSuccess("prov-a/m1", 100.0, now)
	tracker.RecordSuccess("prov-b/m2", 200.0, now)
	tracker.RecordSuccess("prov-c/m3", 300.0, now)
	tracker.RecordSelection("prov-a/m1", now)
	tracker.RecordSelection("prov-b/m2", now)
	tracker.RecordSelection("prov-c/m3", now)

	strategy := NewAdaptiveStrategy(client, rl, h, 500*time.Millisecond, 0, 0)

	reqA, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	reqB, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	reqC, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "prov-a", Provider: providers["prov-a"], UpstreamModel: "m1", OutboundProtocol: "openai", Request: reqA, Weight: 1},
		{ProviderName: "prov-b", Provider: providers["prov-b"], UpstreamModel: "m2", OutboundProtocol: "openai", Request: reqB, Weight: 1},
		{ProviderName: "prov-c", Provider: providers["prov-c"], UpstreamModel: "m3", OutboundProtocol: "openai", Request: reqC, Weight: 1},
	}

	// adaptive 顺序：prov-a (score best) → prov-b → prov-c
	// prov-a 返回 500 → health threshold=1 触发 unhealthy → prov-a|openai unhealthy
	// removeUnhealthyFromRemaining 移除同 health key 的兄弟 → 但没有兄弟
	// prov-b 继续被尝试 → 返回 500 → prov-b|openai unhealthy → 没有兄弟
	// prov-c 继续被尝试 → 返回 500 → 全部失败
	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result != nil && result.Response != nil {
		result.Response.Body.Close()
	}

	mu.Lock()
	c := callCount
	mu.Unlock()

	// 三个候选都应被尝试（它们来自不同 provider，不会互相移除）
	if c != 3 {
		t.Errorf("callCount = %d, want 3 (all three different providers should be attempted)", c)
	}
}

// TestAdaptiveStrategy_SiblingSkippedWhenUnhealthy 验证同一 provider
// 的不同 model 作为兄弟候选时，第一个失败触发 unhealthy 后兄弟被跳过。
func TestAdaptiveStrategy_SiblingSkippedWhenUnhealthy(t *testing.T) {
	var callCount int
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"fail"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"same-prov": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"other-prov": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(1, 30*time.Second)
	resetLatencyTracker()
	tracker := GetLatencyTracker()

	now := time.Now()
	tracker.RecordSuccess("same-prov/model-a", 100.0, now)
	tracker.RecordSuccess("same-prov/model-b", 200.0, now)
	tracker.RecordSuccess("other-prov/model-c", 300.0, now)
	tracker.RecordSelection("same-prov/model-a", now)
	tracker.RecordSelection("same-prov/model-b", now)
	tracker.RecordSelection("other-prov/model-c", now)

	strategy := NewAdaptiveStrategy(client, rl, h, 500*time.Millisecond, 0, 0)

	reqA, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	reqB, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	reqC, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "same-prov", Provider: providers["same-prov"], UpstreamModel: "model-a", OutboundProtocol: "openai", Request: reqA, Weight: 1},
		{ProviderName: "same-prov", Provider: providers["same-prov"], UpstreamModel: "model-b", OutboundProtocol: "openai", Request: reqB, Weight: 1},
		{ProviderName: "other-prov", Provider: providers["other-prov"], UpstreamModel: "model-c", OutboundProtocol: "openai", Request: reqC, Weight: 1},
	}

	// adaptive 顺序：same-prov/model-a → same-prov/model-b → other-prov/model-c
	// model-a 返回 500 → same-prov|openai unhealthy
	// removeUnhealthyFromRemaining 移除 same-prov/model-b（同 health key 兄弟）
	// other-prov/model-c 继续被尝试
	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result != nil && result.Response != nil {
		result.Response.Body.Close()
	}

	mu.Lock()
	c := callCount
	mu.Unlock()

	// model-a + model-c = 2 次尝试，model-b 被跳过
	if c != 2 {
		t.Errorf("callCount = %d, want 2 (model-b should be skipped as sibling)", c)
	}
}
