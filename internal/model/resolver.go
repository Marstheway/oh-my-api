package model

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
)

// PlanLeaf 表示计划树中的叶子节点，持有 provider 标识和引用边属性。
type PlanLeaf struct {
	ProviderName  string
	Provider      config.ProviderConfig
	UpstreamModel string
	Weight        int
}

// OrderEntry 记录 models 列表中各条目的类型和在对应列表中的位置。
type OrderEntry struct {
	IsLeaf bool // true 表示叶子，false 表示子 group
	Index  int  // 在 Leaves 或 Children 列表中的索引位置
}

// PlanNode 表示计划树中的 group 节点（协议无关，不含 HTTP 对象）。
type PlanNode struct {
	GroupName  string
	Mode       string
	Weight     int          // 父节点对本节点的引用边权重
	Leaves     []PlanLeaf   // 直接叶子
	Children   []*PlanNode  // 内部引用的子 group 节点
	ModelOrder []OrderEntry // models 列表中各条目的原始顺序
}

// hasAnyLeaf 递归判断主链路中是否含有任何叶子。
func (n *PlanNode) hasAnyLeaf() bool {
	if len(n.Leaves) > 0 {
		return true
	}
	for _, c := range n.Children {
		if c.hasAnyLeaf() {
			return true
		}
	}
	return false
}

// HasAnyLeafForTest 暴露主链路叶子检查，供 handler 内部特殊流程复用。
func (n *PlanNode) HasAnyLeafForTest() bool {
	return n.hasAnyLeaf()
}

// ResolveResult 解析结果，同时保留旧字段（向后兼容 handler）。
type ResolveResult struct {
	Mode       string
	ModelGroup string
	Plan       *PlanNode
	// 以下字段保持向后兼容，供现有 handler 使用
	Tasks []scheduler.Task
}

// modelGroupDef 内部保存 group 配置，不含已解析的 Tasks。
type modelGroupDef struct {
	name          string
	mode          string
	exposure      config.Exposure
	models        config.ModelEntries
	modelMetadata config.ModelMetadataConfig
}

// redirectDef 内部保存 redirect 配置，扩展支持 exposure 字段
type redirectDef struct {
	source     string          // 源名称（用户请求的模型名）
	target     string          // 目标名称
	finalGroup string          // 解析后的最终 group name
	exposure   config.Exposure // 暴露级别，默认 public
}

// exposureListed 表示该暴露级别是否应出现在 /v1/models 列表（仅 public）。
func exposureListed(e config.Exposure) bool {
	return e == config.ExposurePublic
}

// exposureDirectCallable 表示该暴露级别是否允许外部请求按这个名字直接调用。
// public 与 hidden 允许；internal 不允许（仅限内部引用）。
func exposureDirectCallable(e config.Exposure) bool {
	return e == config.ExposurePublic || e == config.ExposureHidden
}

// Resolver 保存已校验的 group 定义和 redirect 映射。
type Resolver struct {
	groups    map[string]*modelGroupDef // group name -> def
	redirects map[string]*redirectDef   // source → redirectDef（扩展）
	providers map[string]config.ProviderConfig

	// smart route 索引
	smartRoute *SmartRouteIndex // 若未启用则为 nil
}

// SmartRouteIndex 保存 smart route 的解析结果，供运行期只读查询。
type SmartRouteIndex struct {
	Enabled          bool
	CheapGroup       string // cheap 解析后的最终 group 名
	ScoutGroup       string // scout 解析后的最终 group 名
	EnabledModelInfo map[string]*AliasSmartRouteInfo
}

// AliasSmartRouteInfo 保存每个启用入口模型的 smart route 信息。
type AliasSmartRouteInfo struct {
	DefaultGroup string // 入口模型按原始解析规则命中的最终 group
}

var (
	ErrModelNotFound   = fmt.Errorf("model not found")
	ErrNoValidProvider = fmt.Errorf("no valid provider")
)

// NewResolver 构建 Resolver，执行联合引用图校验。
func NewResolver(cfg *config.Config) (*Resolver, error) {
	// 1. 构建 group 定义表
	groups := make(map[string]*modelGroupDef, len(cfg.ModelGroups))
	for _, g := range cfg.ModelGroups {
		if strings.Contains(g.Name, "/") {
			return nil, fmt.Errorf("model_group name %q must not contain '/'", g.Name)
		}
		exposure := config.ExposurePublic
		if g.Exposure != nil {
			exposure = *g.Exposure
		}
		mode := g.Mode
		if mode == "" {
			if len(g.Models) == 1 {
				mode = "concurrent"
			} else {
				mode = "failover"
			}
		}
		groups[g.Name] = &modelGroupDef{
			name:          g.Name,
			mode:          mode,
			exposure:      exposure,
			models:        g.Models,
			modelMetadata: g.ModelMetadata,
		}
	}

	// 2. 校验并解析 redirect
	redirects, err := buildRedirects(groups, cfg.Redirect)
	if err != nil {
		return nil, err
	}

	r := &Resolver{
		groups:    groups,
		redirects: redirects,
		providers: cfg.Providers.Items,
	}

	// 3. 校验所有 group 内的 models 引用合法性（启动期，不等到 Resolve 调用）
	if err := r.validateGroupRefs(); err != nil {
		return nil, err
	}

	// 4. 联合依赖图环检测
	if err := r.detectCycles(); err != nil {
		return nil, err
	}

	// 5. 若配置了 smart_route，建立索引并执行 resolver 期校验
	if cfg.SmartRoute != nil {
		sri, err := r.buildSmartRouteIndex(cfg)
		if err != nil {
			return nil, err
		}
		r.smartRoute = sri

	}

	return r, nil
}

// buildSmartRouteIndex 构建 smart route 索引并执行 resolver 期校验。
func (r *Resolver) buildSmartRouteIndex(cfg *config.Config) (*SmartRouteIndex, error) {
	sr := cfg.SmartRoute

	// 解析 cheap/scout 两个目标
	cheapGroup := r.resolveInternalName(sr.Cheap)
	if cheapGroup == "" {
		return nil, fmt.Errorf("smart_route.cheap %q cannot be resolved to a known group or redirect", sr.Cheap)
	}

	scoutGroup := r.resolveInternalName(sr.Scout)
	if scoutGroup == "" {
		return nil, fmt.Errorf("smart_route.scout %q cannot be resolved to a known group or redirect", sr.Scout)
	}

	// enabled_models 允许填写 redirect alias 或 model group，运行期 reason 落点使用入口模型自身的默认 final group
	enabledInfo := make(map[string]*AliasSmartRouteInfo, len(sr.EnabledModels))
	for _, name := range sr.EnabledModels {
		defaultGroup := r.resolveInternalName(name)
		if defaultGroup == "" {
			return nil, fmt.Errorf("smart_route.enabled_models %q cannot be resolved to a known group or redirect", name)
		}

		enabledInfo[name] = &AliasSmartRouteInfo{
			DefaultGroup: defaultGroup,
		}
	}

	return &SmartRouteIndex{
		Enabled:          true,
		CheapGroup:       cheapGroup,
		ScoutGroup:       scoutGroup,
		EnabledModelInfo: enabledInfo,
	}, nil
}

// buildRedirects 校验 redirect 规则并返回 source→redirectDef 映射。
func buildRedirects(groups map[string]*modelGroupDef, cfg config.RedirectConfigs) (map[string]*redirectDef, error) {
	// 命名空间统一：source 不能和 group name 重名
	for _, rc := range cfg {
		if strings.Contains(rc.Source, "/") {
			return nil, fmt.Errorf("redirect source %q must not contain '/'", rc.Source)
		}
		if strings.Contains(rc.Target, "/") {
			return nil, fmt.Errorf("redirect target %q must not contain '/'", rc.Target)
		}
		if _, exists := groups[rc.Source]; exists {
			return nil, fmt.Errorf("redirect source %q conflicts with existing model_group", rc.Source)
		}
	}

	// 解析每条 redirect 到最终 group name
	result := make(map[string]*redirectDef, len(cfg))
	for _, rc := range cfg {
		// 解析 exposure（默认 public）
		exposure := config.ExposurePublic
		if rc.Exposure != nil {
			exposure = *rc.Exposure
		}

		// 解析到最终 group（支持多级 redirect）
		final, err := resolveAlias(groups, cfg, rc.Target, nil)
		if err != nil {
			return nil, fmt.Errorf("redirect '%s' → '%s': %w", rc.Source, rc.Target, err)
		}

		result[rc.Source] = &redirectDef{
			source:     rc.Source,
			target:     rc.Target,
			finalGroup: final,
			exposure:   exposure,
		}
	}
	return result, nil
}

// resolveAlias 沿 redirect 链找到最终 group name，visited 用于检测环。
func resolveAlias(groups map[string]*modelGroupDef, cfg config.RedirectConfigs, name string, visited map[string]bool) (string, error) {
	if visited == nil {
		visited = make(map[string]bool)
	}
	if visited[name] {
		return "", fmt.Errorf("circular redirect detected involving '%s'", name)
	}
	visited[name] = true

	if _, ok := groups[name]; ok {
		return name, nil
	}
	// 在 RedirectConfigs 中查找目标
	for _, rc := range cfg {
		if rc.Source == name {
			return resolveAlias(groups, cfg, rc.Target, visited)
		}
	}
	return "", fmt.Errorf("target '%s' not found", name)
}

// validateGroupRefs 在启动期校验所有 group 的 models 引用合法性：
// models 中叶子 provider 必须存在；内部引用必须能解析到已知 group/alias。
func (r *Resolver) validateGroupRefs() error {
	for _, def := range r.groups {
		for _, entry := range def.models {
			if !strings.Contains(entry.Model, "/") {
				if r.resolveInternalName(entry.Model) == "" {
					return fmt.Errorf("models ref %q not found in group %q", entry.Model, def.name)
				}
			}
		}
	}
	return nil
}

// detectCycles 对联合依赖图（models 内部引用 + redirect 跳转）做环检测。
func (r *Resolver) detectCycles() error {
	// 颜色标记：0=未访问, 1=访问中（在栈上）, 2=已完成
	const (
		unvisited = 0
		inStack   = 1
		done      = 2
	)
	color := make(map[string]int, len(r.groups))

	var dfs func(name string, path []string) error
	dfs = func(name string, path []string) error {
		if color[name] == done {
			return nil
		}
		if color[name] == inStack {
			return fmt.Errorf("cycle detected: %s -> ... -> %s", path[0], name)
		}
		color[name] = inStack
		path = append(path, name)

		def, ok := r.groups[name]
		if !ok {
			color[name] = done
			return nil
		}

		// 遍历 models 中的内部引用
		for _, entry := range def.models {
			if strings.Contains(entry.Model, "/") {
				continue // 叶子节点，不会产生 group 引用边
			}
			target := r.resolveInternalName(entry.Model)
			if target != "" {
				if err := dfs(target, path); err != nil {
					return err
				}
			}
		}

		color[name] = done
		return nil
	}

	for name := range r.groups {
		if err := dfs(name, nil); err != nil {
			return err
		}
	}
	return nil
}

// resolveInternalName 将内部名字（不含 /）解析为最终 group name；找不到返回空字符串。
func (r *Resolver) resolveInternalName(name string) string {
	if _, ok := r.groups[name]; ok {
		return name
	}
	if def, ok := r.redirects[name]; ok {
		return def.finalGroup
	}
	return ""
}

// Resolve 解析 userModel，返回协议无关 plan tree 及向后兼容的 Tasks。
func (r *Resolver) Resolve(userModel string) (*ResolveResult, error) {
	// 外部直调规则：public/hidden 允许，internal 拒绝
	def, isDirect := r.groups[userModel]
	finalGroupName := userModel

	if isDirect {
		if !exposureDirectCallable(def.exposure) {
			return nil, ErrModelNotFound
		}
	} else {
		// 尝试 redirect：以 alias 自身的 exposure 判断是否允许外部直调
		redirectDef, ok := r.redirects[userModel]
		if !ok {
			return nil, ErrModelNotFound
		}
		if !exposureDirectCallable(redirectDef.exposure) {
			return nil, ErrModelNotFound
		}
		finalGroupName = redirectDef.finalGroup
		def = r.groups[finalGroupName]
	}

	// 构建 plan tree
	plan, err := r.buildPlanNode(finalGroupName)
	if err != nil {
		return nil, err
	}

	if !plan.hasAnyLeaf() {
		return nil, ErrNoValidProvider
	}

	// 为向后兼容，生成平铺 Tasks（仅限根节点的直接叶子）
	tasks := make([]scheduler.Task, 0, len(plan.Leaves))
	for _, leaf := range plan.Leaves {
		tasks = append(tasks, scheduler.Task{
			ProviderName:  leaf.ProviderName,
			Provider:      leaf.Provider,
			UpstreamModel: leaf.UpstreamModel,
			Weight:        leaf.Weight,
		})
	}
	// 对于子 group 中的叶子，也需要平铺（保证 handler 可正常使用）
	for _, child := range plan.Children {
		collectLeaves(child, &tasks)
	}

	return &ResolveResult{
		Mode:       def.mode,
		ModelGroup: finalGroupName,
		Plan:       plan,
		Tasks:      tasks,
	}, nil
}

// collectLeaves 递归收集 plan 子树中的所有叶子到 tasks。
func collectLeaves(node *PlanNode, tasks *[]scheduler.Task) {
	for _, leaf := range node.Leaves {
		*tasks = append(*tasks, scheduler.Task{
			ProviderName:  leaf.ProviderName,
			Provider:      leaf.Provider,
			UpstreamModel: leaf.UpstreamModel,
			Weight:        leaf.Weight,
		})
	}
	for _, child := range node.Children {
		collectLeaves(child, tasks)
	}
}

// buildPlanNode 为指定 group 构建 plan 节点（每次新建实例）。
func (r *Resolver) buildPlanNode(groupName string) (*PlanNode, error) {
	def, ok := r.groups[groupName]
	if !ok {
		return nil, fmt.Errorf("group %q not found", groupName)
	}

	node := &PlanNode{
		GroupName: groupName,
		Mode:      def.mode,
	}

	// 解析 models
	for _, entry := range def.models {
		if strings.Contains(entry.Model, "/") {
			// 叶子：provider/model
			parts := strings.SplitN(entry.Model, "/", 2)
			providerName, upstreamModel := parts[0], parts[1]
			prov, ok := r.providers[providerName]
			if !ok {
				slog.Warn("provider not found, skipping", "provider", providerName, "group", groupName)
				continue
			}
			node.ModelOrder = append(node.ModelOrder, OrderEntry{IsLeaf: true, Index: len(node.Leaves)})
			node.Leaves = append(node.Leaves, PlanLeaf{
				ProviderName:  providerName,
				Provider:      prov,
				UpstreamModel: upstreamModel,
				Weight:        entry.Weight,
			})
		} else {
			// 内部引用：group 或 alias
			target := r.resolveInternalName(entry.Model)
			if target == "" {
				slog.Warn("internal ref not found, skipping", "ref", entry.Model, "group", groupName)
				continue
			}
			child, err := r.buildPlanNode(target)
			if err != nil {
				return nil, err
			}
			child.Weight = entry.Weight
			node.ModelOrder = append(node.ModelOrder, OrderEntry{IsLeaf: false, Index: len(node.Children)})
			node.Children = append(node.Children, child)
		}
	}

	return node, nil
}

// BuildPlanNodeForTest 暴露内部 group 的 plan 构建能力，供非用户可见的内部流程复用。
func (r *Resolver) BuildPlanNodeForTest(groupName string) (*PlanNode, error) {
	return r.buildPlanNode(groupName)
}

// ProviderModelKey 标识一个 provider + upstream model 对，作为叶子集合元素。
type ProviderModelKey struct {
	ProviderName  string
	UpstreamModel string
}

// VisibleModelLeaves 返回指定用户可见模型名对应的所有叶子 provider/upstream_model 集合（去重）。
// 若输入是 alias，将先解析到最终 group，再展开其叶子。
// 若模型不可见或不存在，返回 ErrModelNotFound。
func (r *Resolver) VisibleModelLeaves(userModel string) ([]ProviderModelKey, error) {
	// 先做直调检查，复用 Resolve 的判断逻辑
	def, isDirect := r.groups[userModel]
	finalGroupName := userModel

	if isDirect {
		if !exposureDirectCallable(def.exposure) {
			return nil, ErrModelNotFound
		}
	} else {
		redirectDef, ok := r.redirects[userModel]
		if !ok {
			return nil, ErrModelNotFound
		}
		if !exposureDirectCallable(redirectDef.exposure) {
			return nil, ErrModelNotFound
		}
		finalGroupName = redirectDef.finalGroup
	}

	seen := make(map[ProviderModelKey]bool)
	var result []ProviderModelKey
	r.collectGroupLeaves(finalGroupName, seen, &result)
	return result, nil
}

// collectGroupLeaves 递归收集指定 group 的所有叶子（去重）。
func (r *Resolver) collectGroupLeaves(groupName string, seen map[ProviderModelKey]bool, result *[]ProviderModelKey) {
	def, ok := r.groups[groupName]
	if !ok {
		return
	}
	for _, entry := range def.models {
		if strings.Contains(entry.Model, "/") {
			parts := strings.SplitN(entry.Model, "/", 2)
			key := ProviderModelKey{ProviderName: parts[0], UpstreamModel: parts[1]}
			if !seen[key] {
				seen[key] = true
				*result = append(*result, key)
			}
		} else {
			target := r.resolveInternalName(entry.Model)
			if target != "" {
				r.collectGroupLeaves(target, seen, result)
			}
		}
	}
}

// CollectChildContextLengths 递归收集指定 group 所有子节点的 context_length。
// 规则（覆盖语义，不信任 catalog）：
//  1. 子 group 有配置 → 用配置值（不展开）
//  2. 子 group 无配置 → 继续展开递归处理
//  3. 叶子节点 → 从 catalog 查询
//
// 返回所有有效值（去重）。
func (r *Resolver) CollectChildContextLengths(groupName string, lookupFn func(provider, upstreamModel string) (int, bool)) []int {
	seen := make(map[int]bool)
	var result []int
	r.collectChildContextLengthsRecursive(groupName, lookupFn, seen, &result)
	return result
}

// collectChildContextLengthsRecursive 递归收集子节点 context_length。
func (r *Resolver) collectChildContextLengthsRecursive(groupName string, lookupFn func(provider, upstreamModel string) (int, bool), seen map[int]bool, result *[]int) {
	def, ok := r.groups[groupName]
	if !ok {
		return
	}

	for _, entry := range def.models {
		if strings.Contains(entry.Model, "/") {
			// 叶子节点：provider/upstream_model，从 catalog 查询
			parts := strings.SplitN(entry.Model, "/", 2)
			if cl, ok := lookupFn(parts[0], parts[1]); ok && cl > 0 && !seen[cl] {
				seen[cl] = true
				*result = append(*result, cl)
			}
		} else {
			// 子 group 引用
			target := r.resolveInternalName(entry.Model)
			if target == "" {
				continue
			}
			childDef, ok := r.groups[target]
			if !ok {
				continue
			}
			// 子 group 有配置 → 用配置值（不展开）
			if childDef.modelMetadata.ContextLength != nil {
				cl := *childDef.modelMetadata.ContextLength
				if cl > 0 && !seen[cl] {
					seen[cl] = true
					*result = append(*result, cl)
				}
				continue
			}
			// 子 group 无配置 → 继续展开
			r.collectChildContextLengthsRecursive(target, lookupFn, seen, result)
		}
	}
}

// GetGroupContextLength 返回指定 group 的 context_length 配置覆盖值。
func (r *Resolver) GetGroupContextLength(groupName string) *int {
	def, ok := r.groups[groupName]
	if !ok {
		return nil
	}
	return def.modelMetadata.ContextLength
}

// FinalGroupName 返回 userModel 对应的最终 group 名。
// 若 userModel 是直接可见 group，返回自身；若是 source，返回 redirect 解析后的 group 名。
// 若 userModel 既不是可见 group 也不是已知 source，返回 userModel 自身。
func (r *Resolver) FinalGroupName(userModel string) string {
	if _, ok := r.groups[userModel]; ok {
		return userModel
	}
	if def, ok := r.redirects[userModel]; ok {
		return def.finalGroup
	}
	return userModel
}

// ListUserModels 返回所有应出现在 /v1/models 的模型名（group + redirect，仅 public）。
func (r *Resolver) ListUserModels() []string {
	models := make([]string, 0)
	// 1. 添加所有 public 的 group
	for name, def := range r.groups {
		if exposureListed(def.exposure) {
			models = append(models, name)
		}
	}
	// 2. 添加所有 public 的 redirect
	for _, def := range r.redirects {
		if exposureListed(def.exposure) {
			models = append(models, def.source)
		}
	}
	return models
}

// IsEmbeddingsCompatiblePlan 判断 plan 是否兼容 embeddings 调度：
// 仅当整棵计划只包含单层直接叶子（无子 group）时返回 true。
func IsEmbeddingsCompatiblePlan(plan *PlanNode) bool {
	if plan == nil {
		return false
	}
	if len(plan.Children) > 0 {
		return false
	}
	return true
}

// SmartRouteEnabled 返回 smart route 是否启用（配置存在且校验通过）。
func (r *Resolver) SmartRouteEnabled() bool {
	return r.smartRoute != nil && r.smartRoute.Enabled
}

// GetSmartRouteIndex 返回 smart route 索引（若未启用则返回 nil）。
// Task 2 可使用此接口获取 cheap/scout/reason 目标。
func (r *Resolver) GetSmartRouteIndex() *SmartRouteIndex {
	return r.smartRoute
}

// GetAliasSmartRouteInfo 按入口模型名查询 smart route 配置是否启用，以及相关信息。
// 若 name 不在 enabled_models 中，返回 nil。
// 若 smart route 未启用，返回 nil。
func (r *Resolver) GetAliasSmartRouteInfo(name string) *AliasSmartRouteInfo {
	if r.smartRoute == nil || r.smartRoute.EnabledModelInfo == nil {
		return nil
	}
	return r.smartRoute.EnabledModelInfo[name]
}
