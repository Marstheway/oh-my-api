package scheduler

import (
	"net/http"
	"time"
)

// RunNode 是 scheduler 的运行时执行节点，携带请求工厂（叶子）或子候选分支（group）。
// Task 3 的 handler 通过 materializePlan 将 model.PlanNode 转换为 RunNode，
// 然后传入 Scheduler.ExecuteNode。
type RunNode struct {
	IsLeaf bool

	// 叶子字段（IsLeaf = true 时有效）
	Task           Task                          // provider/model/协议等元数据，不含 Request
	RequestFactory func() (*http.Request, error) // 每次调用生成独立 request/body
	// Unschedulable 为 true 时该叶子因规则强制协议不可达等原因跳过调度，且不得调用 RequestFactory。
	Unschedulable bool

	// group 字段（IsLeaf = false 时有效）
	Name   string      // group 名称，用于日志
	Mode   string      // 调度模式：failover / concurrent / load-balance
	Weight int         // 在父节点中的引用边权重
	Sticky *StickyMeta // 可选 sticky session 配置（仅 load-balance 模式）

	Children []*RunNode // 候选列表
}

// StickyMeta 是 RunNode 使用的 sticky 配置元数据
type StickyMeta struct {
	Enabled     bool
	IdleTimeout time.Duration
}
