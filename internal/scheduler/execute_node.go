package scheduler

import (
	"context"
	"log/slog"
	"math/rand"
	"sort"
	"time"

	"github.com/Marstheway/oh-my-api/internal/health"
)

// ExecuteNode 递归执行计划树节点。
//
// 叶子节点走现有的 provider request -> parseResponse -> health/ratelimit/metric 路径，
// 不改变任何叶子执行语义。
// Group 节点按其 Mode 调度 Children。
func (s *Scheduler) ExecuteNode(ctx context.Context, node *RunNode) (*Result, error) {
	if node == nil {
		return nil, ErrNoTasks
	}
	if node.IsLeaf {
		if isProviderDisabledAt(node.Task, time.Now().Local()) {
			return nil, ErrNoProviderAvailable
		}
		return s.executeLeaf(ctx, node)
	}
	return s.executeGroup(ctx, node)
}

// executeLeaf 执行单个叶子节点，通过 RequestFactory 生成独立 request。
func (s *Scheduler) executeLeaf(ctx context.Context, node *RunNode) (*Result, error) {
	req, err := node.RequestFactory()
	if err != nil {
		return nil, err
	}
	// cloneRequest 将 RequestFactory 创建的 background context 请求绑定到当前执行期的 ctx（如 raceCtx）
	req = cloneRequest(req, ctx)
	task := node.Task
	task.Request = req

	// 使用 failover strategy 的 executeTask 来执行（语义与叶子执行相同，包含健康/限流上报）
	return s.failoverStrat.executeTask(ctx, &task)
}

// executeGroup 执行 group 节点：调度 Children。
func (s *Scheduler) executeGroup(ctx context.Context, node *RunNode) (*Result, error) {
	if len(node.Children) == 0 {
		return nil, ErrNoTasks
	}

	slog.Debug("executing group",
		"group", node.Name,
		"mode", node.Mode,
		"children", len(node.Children),
	)

	return s.executeByMode(ctx, node.Mode, node.Children)
}

// executeByMode 按指定 mode 调度候选分支列表（每个分支可能是叶子或子 group）。
func (s *Scheduler) executeByMode(ctx context.Context, mode string, nodes []*RunNode) (*Result, error) {
	switch mode {
	case "failover":
		return s.executeFailoverNodes(ctx, nodes)
	case "concurrent":
		return s.executeConcurrentNodes(ctx, nodes)
	case "load-balance":
		return s.executeLoadBalanceNodes(ctx, nodes)
	case "adaptive":
		return s.executeAdaptiveNodes(ctx, nodes)
	default:
		return nil, ErrUnknownStrategy
	}
}

// executeFailoverNodes 顺序遍历候选分支，遇到 success 立即返回，
// 使用与 FailoverStrategy 相同的结果优先级语义。
func (s *Scheduler) executeFailoverNodes(ctx context.Context, nodes []*RunNode) (*Result, error) {
	if len(nodes) == 0 {
		return nil, ErrNoTasks
	}

	now := time.Now().Local()

	// 对纯叶子平铺场景，直接转发给现有 FailoverStrategy 以保持精确相同的行为
	// （包含 health 跳过、deferred 二阶段、ratelimit wait 等逻辑）
	if allLeaves(nodes) {
		tasks, err := buildTasksFromLeaves(nodes)
		if err != nil {
			return nil, err
		}
		return s.failoverStrat.Execute(ctx, tasks)
	}

	// 混合（含子 group）场景：保留 failover 对叶子的健康检查与 deferred 二阶段尝试语义。
	fallback := newSequentialFallback()
	type deferredLeaf struct {
		node             *RunNode
		waitForRateLimit bool
	}
	deferred := make([]deferredLeaf, 0, len(nodes))

	for _, node := range nodes {

		if node.IsLeaf && isProviderDisabledAt(node.Task, now) {
			slog.Debug("failover child skipped by disabled time range",
				"provider", node.Task.ProviderName,
				"upstream_identity", node.Task.ProviderName+"/"+node.Task.UpstreamModel,
			)
			continue
		}

		if !node.IsLeaf {
			result, err := s.executeNode(ctx, node)
			logFailoverNodeOutcome(node, result, err, false)
			if success, doneResult, doneErr := s.handleNodeOutcome(fallback, result, err); success {
				return doneResult, doneErr
			}
			slog.Debug("group child exhausted, trying next",
				"group", node.Name,
				"mode", node.Mode,
			)
			continue
		}

		healthKey := health.MakeHealthKey(node.Task.ProviderName, node.Task.OutboundProtocol)
		if !s.health.IsHealthy(healthKey) {
			deferred = append(deferred, deferredLeaf{node: node})
			continue
		}

		if !s.ratelimit.Allow(node.Task.ProviderName, node.Task.UpstreamModel) {
			slog.Warn("provider rate limited, skipping",
				"provider", node.Task.ProviderName,
				"upstream_identity", node.Task.ProviderName+"/"+node.Task.UpstreamModel,
			)
			deferred = append(deferred, deferredLeaf{node: node, waitForRateLimit: true})
			continue
		}

		result, err := s.executeNode(ctx, node)
		logFailoverNodeOutcome(node, result, err, false)
		if success, doneResult, doneErr := s.handleNodeOutcome(fallback, result, err); success {
			return doneResult, doneErr
		}
	}

	for _, deferredLeaf := range deferred {

		node := deferredLeaf.node
		if deferredLeaf.waitForRateLimit {
			if !s.ratelimit.Allow(node.Task.ProviderName, node.Task.UpstreamModel) {
				if err := s.ratelimit.Wait(ctx, node.Task.ProviderName, node.Task.UpstreamModel); err != nil {
					slog.Warn("provider rate limit wait failed",
						"provider", node.Task.ProviderName,
						"upstream_identity", node.Task.ProviderName+"/"+node.Task.UpstreamModel,
						"error", err.Error(),
					)
					fallback.RecordRateLimit()
					continue
				}
			}
		} else if !s.ratelimit.Allow(node.Task.ProviderName, node.Task.UpstreamModel) {
			slog.Warn("provider rate limited, skipping",
				"provider", node.Task.ProviderName,
				"upstream_identity", node.Task.ProviderName+"/"+node.Task.UpstreamModel,
			)
			fallback.RecordRateLimit()
			continue
		}

		result, err := s.executeNode(ctx, node)
		logFailoverNodeOutcome(node, result, err, true)
		if success, doneResult, doneErr := s.handleNodeOutcome(fallback, result, err); success {
			return doneResult, doneErr
		}
	}

	result, err := fallback.Final()
	logFailoverFinalOutcome(result, err)
	return result, err
}

// executeConcurrentNodes 并发执行候选分支，取首个 success；失败聚合优先级同 ConcurrentStrategy。
func (s *Scheduler) executeConcurrentNodes(ctx context.Context, nodes []*RunNode) (*Result, error) {
	if len(nodes) == 0 {
		return nil, ErrNoTasks
	}

	now := time.Now().Local()

	// 纯叶子场景，转发给 ConcurrentStrategy
	if allLeaves(nodes) {
		tasks, err := buildTasksFromLeaves(nodes)
		if err != nil {
			return nil, err
		}
		return s.concurrentStrat.Execute(ctx, tasks)
	}

	// 混合场景：叶子候选沿用 concurrent 的限流预检，group 候选直接参与竞速。
	available := make([]*RunNode, 0, len(nodes))
	for _, n := range nodes {
		if n.IsLeaf {
			if isProviderDisabledAt(n.Task, now) {
				continue
			}
			if s.ratelimit.Allow(n.Task.ProviderName, n.Task.UpstreamModel) {
				available = append(available, n)
			}
			continue
		}
		available = append(available, n)
	}
	if len(available) == 0 {
		return nil, ErrAllRateLimited
	}

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type outcome struct {
		result *Result
		err    error
	}
	ch := make(chan outcome, len(available))

	for _, n := range available {
		go func(node *RunNode) {
			result, err := s.executeNode(raceCtx, node)
			if raceCtx.Err() != nil {
				closeResultBody(result)
				return
			}
			select {
			case ch <- outcome{result: result, err: err}:
			case <-raceCtx.Done():
				closeResultBody(result)
			}
		}(n)
	}

	var lastHardResult *Result
	var lastHardErr error
	var lastSoftResult *Result
	remaining := len(available)

	for remaining > 0 {
		select {
		case o := <-ch:
			remaining--

			if o.err == nil && o.result != nil && o.result.FailureKind == FailureKindSuccess {
				cancel()
				closeResultBody(lastHardResult)
				closeResultBody(lastSoftResult)
				return o.result, nil
			}

			if o.err != nil {
				lastHardErr = o.err
				continue
			}
			if o.result == nil {
				continue
			}

			switch o.result.FailureKind {
			case FailureKindSoft:
				closeResultBody(lastSoftResult)
				lastSoftResult = o.result
			default:
				closeResultBody(lastHardResult)
				lastHardResult = o.result
			}
		case <-ctx.Done():
			cancel()
			closeResultBody(lastHardResult)
			closeResultBody(lastSoftResult)
			if lastHardErr != nil {
				return nil, lastHardErr
			}
			return nil, ctx.Err()
		}
	}

	if lastHardResult != nil {
		closeResultBody(lastSoftResult)
		return lastHardResult, nil
	}
	if lastHardErr != nil {
		closeResultBody(lastSoftResult)
		return nil, lastHardErr
	}
	if lastSoftResult != nil {
		return lastSoftResult, nil
	}
	return nil, ErrAllProvidersFailed
}

// executeLoadBalanceNodes 加权选择候选分支，失败移除继续选下一个。
func (s *Scheduler) executeLoadBalanceNodes(ctx context.Context, nodes []*RunNode) (*Result, error) {
	if len(nodes) == 0 {
		return nil, ErrNoTasks
	}

	now := time.Now().Local()

	// 纯叶子场景，转发给 LoadBalanceStrategy
	if allLeaves(nodes) {
		tasks, err := buildTasksFromLeaves(nodes)
		if err != nil {
			return nil, err
		}
		return s.lbStrat.Execute(ctx, tasks)
	}

	// 混合场景：对健康叶子和子 group 一起做加权选择
	healthyNodes := s.filterHealthyNodes(nodes, now)
	if len(healthyNodes) == 0 {
		return nil, ErrNoProviderAvailable
	}

	selector := newRunNodeSelector(healthyNodes)
	fallback := newSequentialFallback()

	for !selector.isEmpty() {
		node := selector.selectOne()
		if node == nil {
			break
		}

		if node.IsLeaf && !s.ratelimit.Allow(node.Task.ProviderName, node.Task.UpstreamModel) {
			fallback.RecordRateLimit()
			selector.remove(node)
			continue
		}

		result, err := s.executeNode(ctx, node)
		if err == nil && result != nil && result.FailureKind == FailureKindSuccess {
			fallback.DiscardSoftResult()
			return result, nil
		}

		selector.remove(node)
		if node.IsLeaf {
			healthKey := health.MakeHealthKey(node.Task.ProviderName, node.Task.OutboundProtocol)
			if !s.health.IsHealthy(healthKey) {
				selector.removeByHealthKey(healthKey)
			}
		}

		if IsRateLimitError(err) {
			fallback.RecordRateLimit()
			continue
		}
		if err != nil {
			fallback.RecordHardError(err)
			continue
		}
		if result == nil {
			continue
		}
		if result.FailureKind == FailureKindSoft {
			fallback.RecordSoftResult(result)
			continue
		}
		fallback.RecordHardResult(result)
	}

	return fallback.Final()
}

// executeNode 是递归入口（内部版本，不做 nil 检查）。
func (s *Scheduler) executeNode(ctx context.Context, node *RunNode) (*Result, error) {
	if node.IsLeaf {
		return s.executeLeaf(ctx, node)
	}
	return s.executeGroup(ctx, node)
}

// handleNodeOutcome 处理单次分支执行结果，语义与 failoverStrategy.handleAttemptOutcome 一致。
// 返回 (true, result, err) 表示已有最终结果，(false, nil, nil) 表示继续尝试下一个。
func (s *Scheduler) handleNodeOutcome(fallback *sequentialFallback, result *Result, err error) (bool, *Result, error) {
	if err != nil {
		fallback.RecordHardError(err)
		return false, nil, nil
	}
	if result == nil {
		return false, nil, nil
	}

	switch result.FailureKind {
	case FailureKindSuccess:
		fallback.DiscardSoftResult()
		return true, result, nil
	case FailureKindSoft:
		fallback.RecordSoftResult(result)
		return false, nil, nil
	default:
		fallback.RecordHardResult(result)
		return false, nil, nil
	}
}

func logFailoverNodeOutcome(node *RunNode, result *Result, err error, forced bool) {
	if node == nil {
		return
	}

	logAttrs := []any{"forced", forced}
	if node.IsLeaf {
		logAttrs = append(logAttrs,
			"provider", node.Task.ProviderName,
			"upstream_identity", node.Task.ProviderName+"/"+node.Task.UpstreamModel,
		)
	} else {
		logAttrs = append(logAttrs,
			"group", node.Name,
			"mode", node.Mode,
		)
	}

	if err != nil {
		slog.Warn("failover child failed, trying next", append(logAttrs, "error", err.Error())...)
		return
	}
	if result == nil {
		return
	}

	switch result.FailureKind {
	case FailureKindSoft:
		slog.Warn("failover child soft failure, trying next", append(logAttrs, "reason", result.FailureReason)...)
	case FailureKindHard:
		attrs := append(logAttrs, "reason", result.FailureReason)
		if result.Response != nil {
			attrs = append(attrs, "status", result.Response.StatusCode)
		}
		slog.Warn("failover child hard failure, trying next", attrs...)
	}
}

func logFailoverFinalOutcome(result *Result, err error) {
	if err != nil {
		slog.Warn("failover finished with error", "error", err.Error())
		return
	}
	if result == nil {
		return
	}

	attrs := []any{
		"failure_kind", result.FailureKind,
		"reason", result.FailureReason,
		"winner", result.Winner,
		"upstream_model", result.UpstreamModel,
	}
	if result.Response != nil {
		attrs = append(attrs, "status", result.Response.StatusCode)
	}

	slog.Debug("failover finished with result", attrs...)
}

// allLeaves 判断候选列表是否全为叶子节点。
func allLeaves(nodes []*RunNode) bool {
	for _, n := range nodes {
		if !n.IsLeaf {
			return false
		}
	}
	return true
}

// buildTasksFromLeaves 将叶子节点列表转换为 []Task，每个叶子独立调用 RequestFactory。
func buildTasksFromLeaves(nodes []*RunNode) ([]Task, error) {
	tasks := make([]Task, 0, len(nodes))
	for _, n := range nodes {
		req, err := n.RequestFactory()
		if err != nil {
			return nil, err
		}
		task := n.Task
		task.Request = req
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// filterHealthyNodes 过滤出健康的候选节点（子 group 始终认为健康，叶子检查 health）。
func (s *Scheduler) filterHealthyNodes(nodes []*RunNode, now time.Time) []*RunNode {
	var healthy []*RunNode
	for _, n := range nodes {
		if !n.IsLeaf {
			healthy = append(healthy, n)
			continue
		}
		if isProviderDisabledAt(n.Task, now) {
			continue
		}
		healthKey := health.MakeHealthKey(n.Task.ProviderName, n.Task.OutboundProtocol)
		if s.health.IsHealthy(healthKey) {
			healthy = append(healthy, n)
		}
	}
	return healthy
}

// runNodeSelector 支持 RunNode 的加权选择器（混合场景用）。
type runNodeSelector struct {
	items []runNodeSelectorItem
	total int
}

type runNodeSelectorItem struct {
	node   *RunNode
	weight int
}

func newRunNodeSelector(nodes []*RunNode) *runNodeSelector {
	items := make([]runNodeSelectorItem, len(nodes))
	total := 0
	for i, n := range nodes {
		w := nodeWeight(n)
		items[i] = runNodeSelectorItem{node: n, weight: w}
		total += w
	}
	return &runNodeSelector{items: items, total: total}
}

func nodeWeight(n *RunNode) int {
	if n.IsLeaf {
		if n.Task.Weight > 0 {
			return n.Task.Weight
		}
		return 1
	}
	if n.Weight > 0 {
		return n.Weight
	}
	return 1
}

func (s *runNodeSelector) isEmpty() bool {
	return len(s.items) == 0
}

func (s *runNodeSelector) selectOne() *RunNode {
	if len(s.items) == 0 {
		return nil
	}
	if s.total <= 0 {
		return s.items[0].node
	}
	target := rand.Intn(s.total) + 1
	cumulative := 0
	for i := range s.items {
		cumulative += s.items[i].weight
		if target <= cumulative {
			return s.items[i].node
		}
	}
	return s.items[len(s.items)-1].node
}

func (s *runNodeSelector) remove(node *RunNode) {
	for i := range s.items {
		if s.items[i].node == node {
			s.total -= s.items[i].weight
			s.items = append(s.items[:i], s.items[i+1:]...)
			return
		}
	}
}

func (s *runNodeSelector) removeByHealthKey(healthKey string) {
	if healthKey == "" {
		return
	}

	filtered := s.items[:0]
	total := 0
	for _, item := range s.items {
		if item.node.IsLeaf {
			if health.MakeHealthKey(item.node.Task.ProviderName, item.node.Task.OutboundProtocol) == healthKey {
				continue
			}
		}
		filtered = append(filtered, item)
		total += item.weight
	}
	s.items = filtered
	s.total = total
}

// executeAdaptiveNodes 按 TTFT score 排序候选叶子，顺序尝试。
// Task 1 已保证 adaptive group 只含叶子节点，这里按"全叶子 group"处理。
func (s *Scheduler) executeAdaptiveNodes(ctx context.Context, nodes []*RunNode) (*Result, error) {
	if len(nodes) == 0 {
		return nil, ErrNoTasks
	}

	now := time.Now().Local()

	// 纯叶子场景：直接转发给 AdaptiveStrategy
	if allLeaves(nodes) {
		tasks, err := buildTasksFromLeaves(nodes)
		if err != nil {
			return nil, err
		}
		return s.adaptiveStrat.Execute(ctx, tasks)
	}

	// 混合场景（不应当出现在 adaptive 配置中，但做防御性处理）：
	// 对叶子按 score 排序，group 节点插入叶子排序后（保留原始顺序）。
	healthyNodes := s.filterHealthyNodes(nodes, now)
	if len(healthyNodes) == 0 {
		return nil, ErrNoProviderAvailable
	}

	// 收集叶子节点的统计信息并排序
	type indexedLeaf struct {
		node  *RunNode
		score float64
	}
	leaves := make([]indexedLeaf, 0)
	nonLeaves := make([]*RunNode, 0)

	leafKeys := make([]string, 0)
	leafIndexMap := make(map[string]*RunNode)
	for _, n := range healthyNodes {
		if n.IsLeaf {
			key := adaptiveCandidateKey(n.Task)
			leafKeys = append(leafKeys, key)
			leafIndexMap[key] = n
		} else {
			nonLeaves = append(nonLeaves, n)
		}
	}

	if len(leafKeys) > 0 {
		stats := GetLatencyTracker().GetStats(leafKeys, now)
		scores := RankCandidates(stats, now)
		scoreMap := make(map[string]float64)
		keyOrder := make(map[string]int) // 用于 tie-break：key 在 scores 中的位置
		for i, sc := range scores {
			scoreMap[sc.Key] = sc.Score
			keyOrder[sc.Key] = i
		}
		for _, key := range leafKeys {
			score := scoreMap[key]
			leaves = append(leaves, indexedLeaf{node: leafIndexMap[key], score: score})
		}
		// 按 score 升序，同分按 scores 中的位置（稳定 tie-break）
		sort.SliceStable(leaves, func(i, j int) bool {
			if leaves[i].score != leaves[j].score {
				return leaves[i].score < leaves[j].score
			}
			ki := adaptiveCandidateKey(leaves[i].node.Task)
			kj := adaptiveCandidateKey(leaves[j].node.Task)
			return keyOrder[ki] < keyOrder[kj]
		})
	}

	// 叶子在前（按 score 排序），group 在后
	ordered := make([]*RunNode, 0, len(leaves)+len(nonLeaves))
	for _, l := range leaves {
		ordered = append(ordered, l.node)
	}
	ordered = append(ordered, nonLeaves...)

	fallback := newSequentialFallback()
	for i := 0; i < len(ordered); i++ {
		node := ordered[i]

		if node.IsLeaf && !s.ratelimit.Allow(node.Task.ProviderName, node.Task.UpstreamModel) {
			fallback.RecordRateLimit()
			continue
		}

		if node.IsLeaf {
			key := adaptiveCandidateKey(node.Task)
			GetLatencyTracker().RecordSelection(key, time.Now())
		}

		result, err := s.executeNode(ctx, node)
		if err == nil && result != nil && result.FailureKind == FailureKindSuccess {
			fallback.DiscardSoftResult()
			return result, nil
		}

		if node.IsLeaf {
			healthKey := health.MakeHealthKey(node.Task.ProviderName, node.Task.OutboundProtocol)
			if !s.health.IsHealthy(healthKey) {
				// 移除后续同 health key 的候选（会收缩 ordered，索引循环正确跳过）
				s.removeUnhealthyLeafNodes(&ordered, node)
			}
		}

		if IsRateLimitError(err) {
			fallback.RecordRateLimit()
			continue
		}
		if err != nil {
			fallback.RecordHardError(err)
			continue
		}
		if result == nil {
			continue
		}
		if result.FailureKind == FailureKindSoft {
			fallback.RecordSoftResult(result)
			continue
		}
		fallback.RecordHardResult(result)
	}

	return fallback.Final()
}

// removeUnhealthyLeafNodes 从 candidates 中移除与当前叶子同 health key 的后续兄弟候选（不含当前节点自身）。
func (s *Scheduler) removeUnhealthyLeafNodes(candidates *[]*RunNode, current *RunNode) {
	if candidates == nil || current == nil {
		return
	}
	healthKey := health.MakeHealthKey(current.Task.ProviderName, current.Task.OutboundProtocol)

	filtered := (*candidates)[:0]
	for _, n := range *candidates {
		if n == current {
			filtered = append(filtered, n)
			continue // 当前节点保留，只移除兄弟
		}
		if n.IsLeaf {
			if health.MakeHealthKey(n.Task.ProviderName, n.Task.OutboundProtocol) == healthKey {
				continue
			}
		}
		filtered = append(filtered, n)
	}
	*candidates = filtered
}
