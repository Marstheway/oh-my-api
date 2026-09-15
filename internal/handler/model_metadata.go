package handler

import (
	"encoding/json"
	"log/slog"
	"os"

	"github.com/Marstheway/oh-my-api/internal/catalog"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/model"
)

// catalogContextIndex 是按 provider+upstream_model 查询 context_length 的索引。
// 保留此类型用于从文件加载的兼容路径和测试。
type catalogContextIndex map[string]int

// catalogIndexKey 构造索引键。
func catalogIndexKey(provider, upstreamModel string) string {
	return provider + "/" + upstreamModel
}

// LoadCatalogContextIndex 从 catalog_upstream.json 中读取并建立 context_length 索引。
// 文件缺失、JSON 非法、或 body 中无合法记录时不报错，返回空索引。
// 在共享 catalog source 可用时，优先使用 source；此函数保留用于向后兼容和测试。
func LoadCatalogContextIndex(cfg *config.Config) catalogContextIndex {
	path := config.CatalogPath(cfg)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("failed to read catalog file", "path", path, "error", err)
		}
		return catalogContextIndex{}
	}

	var snapshot struct {
		Providers []struct {
			Provider string `json:"provider"`
			Body     string `json:"body"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		slog.Warn("failed to parse catalog file", "path", path, "error", err)
		return catalogContextIndex{}
	}

	index := catalogContextIndex{}
	for _, entry := range snapshot.Providers {
		if entry.Body == "" {
			continue
		}
		parseProviderBody(entry.Provider, entry.Body, index)
	}
	return index
}

// parseProviderBody 解析单个 provider 的 body，将有效记录写入 index。
// body 形状不合法时静默跳过，不报错。
func parseProviderBody(providerName, body string, index catalogContextIndex) {
	var parsed struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return
	}

	for _, raw := range parsed.Data {
		var record struct {
			ID            string  `json:"id"`
			ContextLength float64 `json:"context_length"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			continue
		}
		if record.ID == "" || record.ContextLength <= 0 {
			continue
		}
		key := catalogIndexKey(providerName, record.ID)
		index[key] = int(record.ContextLength)
	}
}

// LookupContextLength 按 provider + upstream_model 查询 context_length。
// 找不到时返回 0, false。
func (idx catalogContextIndex) LookupContextLength(provider, upstreamModel string) (int, bool) {
	v, ok := idx[catalogIndexKey(provider, upstreamModel)]
	return v, ok
}

// ContextLength 实现 CatalogSource 接口，便于通过 setTestCatalogSource 注入。
func (idx catalogContextIndex) ContextLength(provider, upstreamModel string) (int, bool) {
	return idx.LookupContextLength(provider, upstreamModel)
}

// CatalogView 实现 CatalogSource 接口。
// catalogContextIndex 只携带 context_length，没有 Provider 快照信息，
// 因此返回空且 stale 的目录视图（GetCatalog 的真实数据来自 *catalog.Service）。
func (idx catalogContextIndex) CatalogView() catalog.CatalogView {
	return catalog.EmptyCatalogView()
}

// ResolveContextLength 按优先级规则为指定可见 userModel 计算 context_length。
// 来源于 catalogSrc（共享 catalog source）或 catalogIdx（文件查找结果）。
// 规则：
//  1. 若当前 group（alias 解析后）有配置覆盖，直接返回配置值（覆盖规则，不展开叶子）
//  2. 若当前 group 无配置，递归收集子节点的 context_length：
//     - 子 group 有配置则用配置值，无配置则继续展开
//     - 叶子节点从 lookup（catalogSrc 或 catalogIdx）查询
//  3. 多个有效值取最小；0 个有效值返回 nil（省略字段）
func ResolveContextLength(userModel string, r *model.Resolver, lookup func(provider, upstreamModel string) (int, bool)) *int {
	// 获取最终 group 名（alias 已解析）
	finalGroup := r.FinalGroupName(userModel)

	// 优先检查当前 group 的配置覆盖（若存在则直接返回，不展开）
	if cl := r.GetGroupContextLength(finalGroup); cl != nil {
		return cl
	}

	// 当前 group 无配置，递归收集子节点的 context_length
	candidates := r.CollectChildContextLengths(finalGroup, lookup)

	switch len(candidates) {
	case 0:
		return nil
	case 1:
		val := candidates[0]
		return &val
	default:
		// 取最小值
		minVal := candidates[0]
		for _, v := range candidates[1:] {
			if v < minVal {
				minVal = v
			}
		}
		return &minVal
	}
}
