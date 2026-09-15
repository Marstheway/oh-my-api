package scheduler

import (
	"math"
	"sync"
	"time"
)

const (
	// sampleWindowSize 每个候选保留的最大样本数
	sampleWindowSize = 20
	// sampleExpiryDuration 样本过期时间
	sampleExpiryDuration = 30 * time.Minute
)

// ttftSample 表示一次流式成功请求的端到端 TTFT 样本
type ttftSample struct {
	ttftMs    float64
	timestamp time.Time
}

// candidateWindow 维护单个候选的样本窗口和调度时间
type candidateWindow struct {
	samples        []ttftSample
	lastSelectedAt time.Time
}

// LatencyTracker 维护候选 provider/upstream_model 的 TTFT 样本窗口，
// 提供统计读取接口，支持并发读写。
// TTFT 表示单次上游尝试从发起 HTTP 请求到收到首个 SSE 事件的端到端延迟。
type LatencyTracker struct {
	mu      sync.RWMutex
	windows map[string]*candidateWindow
}

var (
	globalTracker   *LatencyTracker
	globalTrackerMu sync.Mutex
)

// GetLatencyTracker 返回进程内唯一的 LatencyTracker 单例。
// 无论 Scheduler 重建多少次，始终复用同一个 tracker，保证 TTFT 样本不会丢失。
func GetLatencyTracker() *LatencyTracker {
	globalTrackerMu.Lock()
	defer globalTrackerMu.Unlock()
	if globalTracker == nil {
		globalTracker = NewLatencyTracker()
	}
	return globalTracker
}

// resetLatencyTracker 重置全局 tracker 为 nil，仅供测试使用。
func resetLatencyTracker() {
	globalTrackerMu.Lock()
	defer globalTrackerMu.Unlock()
	globalTracker = nil
}

// NewLatencyTracker 创建新的 LatencyTracker 实例，主要用于单元测试。
func NewLatencyTracker() *LatencyTracker {
	return &LatencyTracker{
		windows: make(map[string]*candidateWindow),
	}
}

// RecordSuccess 记录一次流式成功请求的端到端 TTFT（单位 ms）。
// TTFT 表示单次上游尝试从发起 HTTP 请求到收到首个 SSE 事件的耗时。
func (t *LatencyTracker) RecordSuccess(key string, ttftMs float64, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	w := t.ensureWindow(key)
	sample := ttftSample{ttftMs: ttftMs, timestamp: now}
	w.samples = append(w.samples, sample)
	if len(w.samples) > sampleWindowSize {
		// 保留最近的 20 个样本
		w.samples = w.samples[len(w.samples)-sampleWindowSize:]
	}
}

// RecordSelection 记录候选被调度选中。
func (t *LatencyTracker) RecordSelection(key string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	w := t.ensureWindow(key)
	w.lastSelectedAt = now
}

// GetStats 返回指定 key 的统计信息。
// 统计仅基于未过期样本；读取操作不修改样本窗口内容。
func (t *LatencyTracker) GetStats(keys []string, now time.Time) []CandidateStats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]CandidateStats, 0, len(keys))
	for _, key := range keys {
		stats := CandidateStats{Key: key}
		w := t.windows[key]
		if w != nil {
			stats.LastSelectedAt = w.lastSelectedAt
			valid := filterValidSamples(w.samples, now)
			stats.SampleCount = len(valid)
			if len(valid) > 0 {
				stats.MeanTTFT = computeMean(valid)
				stats.StdDevTTFT = computeStdDev(valid, stats.MeanTTFT)
				stats.LastSuccessAt = valid[len(valid)-1].timestamp
			}
		}
		result = append(result, stats)
	}
	return result
}

func (t *LatencyTracker) ensureWindow(key string) *candidateWindow {
	w := t.windows[key]
	if w == nil {
		w = &candidateWindow{}
		t.windows[key] = w
	}
	return w
}

// filterValidSamples 过滤出未过期的样本
func filterValidSamples(samples []ttftSample, now time.Time) []ttftSample {
	cutoff := now.Add(-sampleExpiryDuration)
	valid := make([]ttftSample, 0, len(samples))
	for _, s := range samples {
		if s.timestamp.After(cutoff) {
			valid = append(valid, s)
		}
	}
	return valid
}

// computeMean 计算样本均值
func computeMean(samples []ttftSample) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += s.ttftMs
	}
	return sum / float64(len(samples))
}

// computeStdDev 计算样本标准差（总体标准差）
func computeStdDev(samples []ttftSample, mean float64) float64 {
	if len(samples) <= 1 {
		return 0
	}
	var sumSqDiff float64
	for _, s := range samples {
		diff := s.ttftMs - mean
		sumSqDiff += diff * diff
	}
	return math.Sqrt(sumSqDiff / float64(len(samples)))
}
