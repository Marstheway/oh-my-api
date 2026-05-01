package scheduler

import "math/rand"

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
			s.total -= s.items[i].weight
			s.items = append(s.items[:i], s.items[i+1:]...)
			return
		}
	}
}

// IsEmpty 检查是否还有可用项
func (s *WeightedSelector) IsEmpty() bool {
	return len(s.items) == 0
}

// Len 返回剩余可用项数量
func (s *WeightedSelector) Len() int {
	return len(s.items)
}
