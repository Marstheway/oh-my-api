package scheduler

import (
	"math/rand"

	"github.com/Marstheway/oh-my-api/internal/health"
)

type WeightedSelector struct {
	items []selectorItem
	total int
	rand  func(int) int
}

type selectorItem struct {
	task   Task
	weight int
}

// NewWeightedSelector 创建按权重随机选择器
func NewWeightedSelector(tasks []Task) *WeightedSelector {
	return newWeightedSelector(tasks, rand.Intn)
}

func newWeightedSelector(tasks []Task, randIntn func(int) int) *WeightedSelector {
	items := make([]selectorItem, len(tasks))
	total := 0
	for i, t := range tasks {
		w := t.Weight
		if w <= 0 {
			w = 1
		}
		items[i] = selectorItem{
			task:   t,
			weight: w,
		}
		total += w
	}
	return &WeightedSelector{items: items, total: total, rand: randIntn}
}

// Select 按权重随机选择下一个可用 task
func (s *WeightedSelector) Select() *Task {
	if len(s.items) == 0 {
		return nil
	}
	if s.total <= 0 {
		return &s.items[0].task
	}

	target := s.rand(s.total) + 1
	cumulative := 0
	for i := range s.items {
		cumulative += s.items[i].weight
		if target <= cumulative {
			return &s.items[i].task
		}
	}

	// 正常情况下不会走到这里，作为兜底返回最后一个
	return &s.items[len(s.items)-1].task
}

// Remove 从选择器中移除指定的 provider（失败后重试其他时使用）
func (s *WeightedSelector) Remove(providerName string) {
	for i := range s.items {
		if s.items[i].task.ProviderName == providerName {
			s.removeAt(i)
			return
		}
	}
}

// RemoveTask 从选择器中移除当前被选中的具体 task。
func (s *WeightedSelector) RemoveTask(task *Task) {
	if task == nil {
		return
	}
	for i := range s.items {
		if &s.items[i].task == task {
			s.removeAt(i)
			return
		}
	}
}

// RemoveByHealthKey 从选择器中移除同一健康键下的所有剩余 task。
func (s *WeightedSelector) RemoveByHealthKey(healthKey string) {
	if healthKey == "" {
		return
	}

	filtered := s.items[:0]
	total := 0
	for _, item := range s.items {
		if health.MakeHealthKey(item.task.ProviderName, item.task.OutboundProtocol) == healthKey {
			continue
		}
		filtered = append(filtered, item)
		total += item.weight
	}
	s.items = filtered
	s.total = total
}

func (s *WeightedSelector) removeAt(i int) {
	s.total -= s.items[i].weight
	s.items = append(s.items[:i], s.items[i+1:]...)
}

// IsEmpty 检查是否还有可用项
func (s *WeightedSelector) IsEmpty() bool {
	return len(s.items) == 0
}

// Len 返回剩余可用项数量
func (s *WeightedSelector) Len() int {
	return len(s.items)
}
