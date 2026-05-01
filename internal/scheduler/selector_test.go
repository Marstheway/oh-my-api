package scheduler

import (
	"net/http"
	"testing"
)

func TestWeightedSelector_SingleProvider(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	tasks := []Task{
		{ProviderName: "a", Weight: 1, Request: req},
	}

	s := NewWeightedSelector(tasks)

	for i := 0; i < 5; i++ {
		task := s.Select()
		if task == nil {
			t.Error("Select should return task")
		}
		if task.ProviderName != "a" {
			t.Errorf("provider = %q, want %q", task.ProviderName, "a")
		}
	}
}

func TestWeightedSelector_WeightDistribution(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	tasks := []Task{
		{ProviderName: "a", Weight: 3, Request: req},
		{ProviderName: "b", Weight: 1, Request: req},
	}

	s := NewWeightedSelector(tasks)

	counts := make(map[string]int)
	// 选择 2000 次，按 3:1 权重随机，a 应接近 75%
	for i := 0; i < 2000; i++ {
		task := s.Select()
		if task != nil {
			counts[task.ProviderName]++
		}
	}

	if counts["a"] < 1300 || counts["a"] > 1700 {
		t.Errorf("provider 'a' selected %d times, expect in [1300,1700]", counts["a"])
	}
	if counts["b"] < 300 || counts["b"] > 700 {
		t.Errorf("provider 'b' selected %d times, expect in [300,700]", counts["b"])
	}
}

func TestWeightedSelector_Remove(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	tasks := []Task{
		{ProviderName: "a", Weight: 1, Request: req},
		{ProviderName: "b", Weight: 1, Request: req},
	}

	s := NewWeightedSelector(tasks)

	s.Remove("a")

	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1", s.Len())
	}

	task := s.Select()
	if task == nil {
		t.Fatal("Select should return task")
	}
	if task.ProviderName != "b" {
		t.Errorf("provider = %q, want %q", task.ProviderName, "b")
	}
}

func TestWeightedSelector_RemoveLast(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	tasks := []Task{
		{ProviderName: "a", Weight: 1, Request: req},
	}

	s := NewWeightedSelector(tasks)

	s.Remove("a")

	if !s.IsEmpty() {
		t.Error("selector should be empty")
	}

	task := s.Select()
	if task != nil {
		t.Error("Select on empty selector should return nil")
	}
}

func TestWeightedSelector_IsEmpty(t *testing.T) {
	s := NewWeightedSelector(nil)

	if !s.IsEmpty() {
		t.Error("empty selector should report IsEmpty")
	}
}

func TestWeightedSelector_ZeroWeight(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	tasks := []Task{
		{ProviderName: "a", Weight: 0, Request: req},
	}

	s := NewWeightedSelector(tasks)

	// 权重为 0 应该被当作 1 处理
	task := s.Select()
	if task == nil {
		t.Error("Select should return task even with weight 0")
	}
}

func TestWeightedSelector_NegativeWeight(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	tasks := []Task{
		{ProviderName: "a", Weight: -5, Request: req},
	}

	s := NewWeightedSelector(tasks)

	// 负权重应该被当作 1 处理
	task := s.Select()
	if task == nil {
		t.Error("Select should return task even with negative weight")
	}
}

func TestWeightedSelector_RemoveUnknown(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	tasks := []Task{
		{ProviderName: "a", Weight: 1, Request: req},
	}

	s := NewWeightedSelector(tasks)

	// 移除不存在的 provider 应该没有副作用
	s.Remove("unknown")

	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1", s.Len())
	}
}

func TestWeightedSelector_SelectByWeightRange(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	tasks := []Task{
		{ProviderName: "a", Weight: 2, Request: req},
		{ProviderName: "b", Weight: 1, Request: req},
	}

	// 权重总和为 3，target=1/2 应命中 a，target=3 应命中 b
	randSeq := []int{0, 1, 2}
	idx := 0
	s := newWeightedSelector(tasks, func(n int) int {
		v := randSeq[idx]
		idx++
		if v >= n {
			return n - 1
		}
		return v
	})

	expected := []string{"a", "a", "b"}
	for i, want := range expected {
		task := s.Select()
		if task == nil {
			t.Fatalf("selection %d should not be nil", i)
		}
		if task.ProviderName != want {
			t.Errorf("selection %d = %q, want %q", i, task.ProviderName, want)
		}
	}
}
