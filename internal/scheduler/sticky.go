package scheduler

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	defaultStickyIdleTimeout = 10 * time.Minute
	stickyPruneInterval      = time.Minute
)

// stickyRecord 记录某个亲和键的粘住信息。
type stickyRecord struct {
	candidate string
	expiresAt time.Time
}

// StickyStore 维护 load-balance sticky session 和成功计数。
// 线程安全，支持并发读写。
type StickyStore struct {
	mu         sync.RWMutex
	stickies   map[string]*stickyRecord  // affinity -> sticky record
	successCnt map[string]map[string]int // group -> candidate -> count
	lastPrune  time.Time
}

var (
	globalStickyStore   *StickyStore
	globalStickyStoreMu sync.Mutex
)

// GetStickyStore 返回进程内唯一的 StickyStore 单例。
func GetStickyStore() *StickyStore {
	globalStickyStoreMu.Lock()
	defer globalStickyStoreMu.Unlock()
	if globalStickyStore == nil {
		globalStickyStore = NewStickyStore()
	}
	return globalStickyStore
}

// resetStickyStore 重置全局 store 为 nil，仅供测试使用。
func resetStickyStore() {
	globalStickyStoreMu.Lock()
	defer globalStickyStoreMu.Unlock()
	globalStickyStore = nil
}

// NewStickyStore 创建新的 StickyStore 实例。
func NewStickyStore() *StickyStore {
	return &StickyStore{
		stickies:   make(map[string]*stickyRecord),
		successCnt: make(map[string]map[string]int),
	}
}

// Lookup 查询亲和键的粘住候选。
// 若存在记录且 now 在 expiresAt 之前，返回 (candidate, true)。
// 调度访问会按固定间隔清理所有过期记录，避免不再访问的亲和键常驻内存。
func (s *StickyStore) Lookup(affinity string, now time.Time) (candidate string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneExpiredLocked(now)
	rec, exists := s.stickies[affinity]
	if !exists {
		return "", false
	}
	if !now.Before(rec.expiresAt) {
		delete(s.stickies, affinity)
		return "", false
	}

	return rec.candidate, true
}

// Remember 记录亲和键到候选的绑定，并设置过期时间。
func (s *StickyStore) Remember(affinity string, candidate string, now time.Time, idleTimeout time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneExpiredLocked(now)
	s.stickies[affinity] = &stickyRecord{
		candidate: candidate,
		expiresAt: now.Add(idleTimeout),
	}
}

func (s *StickyStore) pruneExpiredLocked(now time.Time) {
	if now.Sub(s.lastPrune) < stickyPruneInterval {
		return
	}
	for affinity, rec := range s.stickies {
		if !now.Before(rec.expiresAt) {
			delete(s.stickies, affinity)
		}
	}
	s.lastPrune = now
}

// IncrSuccess 增加指定 group 下候选的成功计数。
func (s *StickyStore) IncrSuccess(group string, candidate string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.successCnt[group] == nil {
		s.successCnt[group] = make(map[string]int)
	}
	s.successCnt[group][candidate]++
}

// GetSuccessCounts 返回指定 group 下所有候选的成功计数（只读副本）。
// 主要用于测试与诊断。
func (s *StickyStore) GetSuccessCounts(group string) map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]int)
	if s.successCnt[group] == nil {
		return result
	}
	for k, v := range s.successCnt[group] {
		result[k] = v
	}
	return result
}

// PickDeficit 从候选列表中按 deficit 选择：选 success_count/weight 最小者。
// 并列时优先 weight 更大，再按候选在列表中的顺序。
func (s *StickyStore) PickDeficit(group string, candidates []string, weights map[string]int) string {
	if len(candidates) == 0 {
		return ""
	}
	if len(candidates) == 1 {
		return candidates[0]
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	counts := s.successCnt[group]
	if counts == nil {
		counts = make(map[string]int)
	}

	minIdx := 0
	minScore := float64(counts[candidates[0]]) / float64(effectiveWeight(weights[candidates[0]]))
	minWeight := effectiveWeight(weights[candidates[0]])

	for i := 1; i < len(candidates); i++ {
		c := candidates[i]
		w := effectiveWeight(weights[c])
		score := float64(counts[c]) / float64(w)
		if score < minScore {
			minIdx = i
			minScore = score
			minWeight = w
			continue
		}
		if score == minScore {
			if w > minWeight {
				minIdx = i
				minScore = score
				minWeight = w
			}
			// weight 相同则保留更小 index（配置顺序）
		}
	}

	return candidates[minIdx]
}

func effectiveWeight(w int) int {
	if w <= 0 {
		return 1
	}
	return w
}

// affinityKey 构造亲和键：key_name + "\0" + group_name。
func affinityKey(keyName, groupName string) string {
	return keyName + "\000" + groupName
}

// pickStickyOrDeficit 在 sticky 命中且仍在 remaining 时返回粘住候选，否则 deficit。
// keyName 为空时不查 sticky 表，直接 deficit。
func pickStickyOrDeficit(
	store *StickyStore,
	groupName, keyName string,
	sticky *StickyMeta,
	now time.Time,
	remaining []string,
	weights map[string]int,
) string {
	if len(remaining) == 0 {
		return ""
	}
	if sticky != nil && sticky.Enabled && keyName != "" {
		if cand, ok := store.Lookup(affinityKey(keyName, groupName), now); ok {
			for _, r := range remaining {
				if r == cand {
					slog.Debug("sticky hit, reusing candidate",
						"group", groupName,
						"key_name", keyName,
						"candidate", cand,
					)
					return cand
				}
			}
		}
	}
	return store.PickDeficit(groupName, remaining, weights)
}

// recordStickySuccess 在 sticky 启用路径的最终成功时调用：成功计数 +1，有 key 时刷新亲和。
// now 应为成功时刻。
func recordStickySuccess(store *StickyStore, groupName, keyName, candidate string, idleTimeout time.Duration, now time.Time) {
	if store == nil || candidate == "" {
		return
	}
	store.IncrSuccess(groupName, candidate)
	if keyName != "" {
		store.Remember(affinityKey(keyName, groupName), candidate, now, idleTimeout)
	}
}

// ParseStickyMeta 将配置层 sticky 字段转为运行时 StickyMeta。
// cfg 为 nil 或 enabled==false 时返回 (nil, nil)。
// enabled 且 idleTimeout 空字符串时默认 10m；非法 duration 返回 error。
func ParseStickyMeta(enabled bool, idleTimeout string) (*StickyMeta, error) {
	if !enabled {
		return nil, nil
	}
	d := defaultStickyIdleTimeout
	if idleTimeout != "" {
		parsed, err := time.ParseDuration(idleTimeout)
		if err != nil {
			return nil, fmt.Errorf("invalid sticky idle_timeout: %w", err)
		}
		if parsed <= 0 {
			return nil, fmt.Errorf("sticky idle_timeout must be greater than 0")
		}
		d = parsed
	}
	return &StickyMeta{
		Enabled:     true,
		IdleTimeout: d,
	}, nil
}

// runNodeCandidateKey 返回混合 LB 场景下候选的稳定身份键。
func runNodeCandidateKey(n *RunNode) string {
	if n == nil {
		return ""
	}
	if n.IsLeaf {
		return adaptiveCandidateKey(n.Task)
	}
	return n.Name
}
