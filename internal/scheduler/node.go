package scheduler

import "net/http"

// RunNode 是 scheduler 的运行时执行节点，携带请求工厂（叶子）或子候选分支（group）。
// Task 3 的 handler 通过 materializePlan 将 model.PlanNode 转换为 RunNode，
// 然后传入 Scheduler.ExecuteNode。
type RunNode struct {
	IsLeaf bool

	// 叶子字段（IsLeaf = true 时有效）
	Task           Task                          // provider/model/协议等元数据，不含 Request
	RequestFactory func() (*http.Request, error) // 每次调用生成独立 request/body

	// group 字段（IsLeaf = false 时有效）
	Name     string // group 名称，用于日志
	Mode     string // 调度模式：failover / concurrent / load-balance
	Weight   int    // 在父节点中的引用边权重

	Children []*RunNode // 候选列表
}
