package scheduler

import (
	"math"
	"sort"
	"time"
)

const (
	// coldStartScore 无样本时的默认分数（ms）
	coldStartScore = 2000.0
	// forcedCorrectionThreshold 候选超过此时间未被调度则具备强制纠偏资格
	forcedCorrectionThreshold = 30 * time.Minute
)

// CandidateStats 候选的 TTFT 统计信息。
// TTFT 表示单次上游尝试从发起 HTTP 请求到收到首个 SSE 事件的端到端延迟。
type CandidateStats struct {
	Key            string    // provider/upstream_model
	MeanTTFT       float64   // 有效样本平均 TTFT，单位 ms
	StdDevTTFT     float64   // 有效样本标准差，单位 ms
	SampleCount    int       // 有效样本数
	LastSuccessAt  time.Time // 最近一次成功样本时间
	LastSelectedAt time.Time // 最近一次被调度选中的时间
}

// CandidateScore 候选的评分和排序结果
type CandidateScore struct {
	Key   string
	Score float64
	Stats CandidateStats
}

// RankCandidates 对候选按 score 排序。
// 若命中强制纠偏，纠偏候选排第一，其余按 score 升序；
// 否则全量按 score 升序。
// 同分时按 Key 字典序升序（稳定 tie-break）。
func RankCandidates(stats []CandidateStats, now time.Time) []CandidateScore {
	// 寻找强制纠偏候选：最久未被调度且超过阈值的候选
	correctionKey := findCorrectionCandidate(stats, now)

	// 计算所有候选的 score
	scores := make([]CandidateScore, len(stats))
	for i, s := range stats {
		scores[i] = CandidateScore{
			Key:   s.Key,
			Score: computeScore(s, now),
			Stats: s,
		}
	}

	// 稳定排序：按 score 升序，同分按 Key 字典序升序
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].Score != scores[j].Score {
			return scores[i].Score < scores[j].Score
		}
		return scores[i].Key < scores[j].Key
	})

	// 强制纠偏：将纠偏候选移到第一位
	if correctionKey != "" {
		for i, s := range scores {
			if s.Key == correctionKey {
				correction := s
				scores = append(scores[:i], scores[i+1:]...)
				scores = append([]CandidateScore{correction}, scores...)
				break
			}
		}
	}

	return scores
}

// findCorrectionCandidate 找到最久未被调度的、超过阈值的候选。
// 返回候选 key，无符合条件时返回空字符串。
//
// 零值 LastSelectedAt 表示从未被调度，视为无限久远，优先于任何有实际时间戳的候选。
// 若多个候选均为零值，取遍历顺序中第一个（排序阶段会通过 RankCandidates 的 tie-break 稳定化）。
func findCorrectionCandidate(stats []CandidateStats, now time.Time) string {
	var correctionKey string
	var longestUnselected time.Duration
	foundZeroSelected := false

	for _, s := range stats {
		if s.LastSelectedAt.IsZero() {
			// 从未被调度：具备强制纠偏资格，且优先级高于任何有实际时间戳的候选
			if !foundZeroSelected {
				correctionKey = s.Key
				foundZeroSelected = true
			}
			continue
		}
		// 已找到零值候选，后续只可能被另一个零值候选替代（已在上面处理）
		if foundZeroSelected {
			continue
		}
		sinceSelected := now.Sub(s.LastSelectedAt)
		if sinceSelected > forcedCorrectionThreshold && sinceSelected > longestUnselected {
			longestUnselected = sinceSelected
			correctionKey = s.Key
		}
	}
	return correctionKey
}

// computeScore 计算候选的固定分数。
// n == 0: coldStartScore
// n > 0: mean_ttft + sample_penalty + stale_penalty
//
//	sample_penalty = 300ms / min(n, 5)
//	stale_penalty = min(300ms, 15ms * 最近成功样本距今的整分钟数)
func computeScore(stats CandidateStats, now time.Time) float64 {
	n := stats.SampleCount
	if n == 0 {
		return coldStartScore
	}

	// sample_penalty = 300ms / min(n, 5)
	divisor := float64(n)
	if n > 5 {
		divisor = 5
	}
	samplePenalty := 300.0 / divisor

	// stale_penalty = min(300ms, 15ms * floor(minutes_since_last_success))
	var stalePenalty float64
	if !stats.LastSuccessAt.IsZero() {
		minutesSince := now.Sub(stats.LastSuccessAt).Minutes()
		if minutesSince > 0 {
			stalePenalty = math.Min(300.0, 15.0*math.Floor(minutesSince))
		}
	}

	return stats.MeanTTFT + samplePenalty + stalePenalty
}
