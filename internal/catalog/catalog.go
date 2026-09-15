package catalog

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Marstheway/oh-my-api/internal/modelsdev"
)

const DefaultTTL = 12 * time.Hour

// CatalogSnapshot represents the full upstream catalog snapshot.
type CatalogSnapshot struct {
	GeneratedAt string          `json:"generated_at,omitempty"`
	Providers   []ProviderEntry `json:"providers"`
}

// ProviderEntry represents a single provider's catalog probe result.
type ProviderEntry struct {
	Provider      string `json:"provider"`
	Protocol      string `json:"protocol"`
	URL           string `json:"url"`
	StatusCode    int    `json:"status_code,omitempty"`
	Body          string `json:"body,omitempty"`
	Error         string `json:"error,omitempty"`
	LastSuccessAt string `json:"last_success_at,omitempty"`
}

// contextLengthIndex maps "provider/upstream_model" -> context_length.
type contextLengthIndex map[string]int

// Service provides thread-safe shared access to the upstream catalog snapshot.
type Service struct {
	mu       sync.RWMutex
	snapshot CatalogSnapshot
	idx      contextLengthIndex

	// modelsDev 是 models.dev 社区目录的紧凑索引（id → context_length）。
	// 作为探测索引 miss 时的 fallback，通过 atomic 指针在刷新时整体替换。
	modelsDev atomic.Pointer[modelsdev.Index]
}

// NewService creates a new catalog Service with an empty snapshot.
func NewService() *Service {
	return &Service{
		idx: make(contextLengthIndex),
	}
}

// LoadFromFile loads the catalog snapshot from the given file path.
// Returns an error only if file exists but cannot be parsed.
// If the file does not exist, the snapshot remains empty.
func (s *Service) LoadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read catalog file: %w", err)
	}

	var snapshot CatalogSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("parse catalog file: %w", err)
	}

	s.mu.Lock()
	s.snapshot = snapshot
	s.idx = buildContextLengthIndex(snapshot.Providers)
	s.mu.Unlock()

	slog.Info("catalog snapshot loaded", "path", path, "providers", len(snapshot.Providers), "generated_at", snapshot.GeneratedAt)
	return nil
}

// WriteToFile atomically writes the current snapshot to the given file path.
func (s *Service) WriteToFile(path string) error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.snapshot, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("marshal catalog: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write tmp catalog: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename catalog: %w", err)
	}
	return nil
}

// ReplaceSnapshot replaces the in-memory snapshot and rebuilds the context length index.
func (s *Service) ReplaceSnapshot(snapshot CatalogSnapshot) {
	s.mu.Lock()
	s.snapshot = snapshot
	s.idx = buildContextLengthIndex(snapshot.Providers)
	s.mu.Unlock()
}

// Snapshot returns a copy of the current snapshot.
func (s *Service) Snapshot() CatalogSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Return a shallow copy (ProviderEntry values are strings, safe)
	snapshot := s.snapshot
	snapshot.Providers = make([]ProviderEntry, len(s.snapshot.Providers))
	copy(snapshot.Providers, s.snapshot.Providers)
	return snapshot
}

// ContextLength returns the context_length for a given provider/upstream_model pair.
// 查找顺序：probe 探测索引优先（provider/upstream 精确 key）；
// miss 时回退到 models.dev 社区索引（忽略 provider，对 upstream 做规范化后 exact 匹配）。
func (s *Service) ContextLength(provider, upstreamModel string) (int, bool) {
	s.mu.RLock()
	v, ok := s.idx[indexKey(provider, upstreamModel)]
	s.mu.RUnlock()
	if ok {
		return v, true
	}
	if idx := s.modelsDev.Load(); idx != nil {
		return idx.Lookup(upstreamModel)
	}
	return 0, false
}

// SetModelsDevIndex 替换 models.dev 社区索引（刷新时调用，原子替换，旧索引由 GC 回收）。
func (s *Service) SetModelsDevIndex(idx *modelsdev.Index) {
	s.modelsDev.Store(idx)
}

// ProviderEntries returns all catalog entries for the given provider from the current snapshot.
func (s *Service) ProviderEntries(provider string) []ProviderEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []ProviderEntry
	for _, e := range s.snapshot.Providers {
		if e.Provider == provider {
			result = append(result, e)
		}
	}
	return result
}

// GeneratedAt returns the top-level generated_at timestamp of the current snapshot.
func (s *Service) GeneratedAt() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot.GeneratedAt
}

// buildContextLengthIndex parses provider entries and builds an index of context_length values.
// Only entries with successful response bodies are parsed.
func buildContextLengthIndex(entries []ProviderEntry) contextLengthIndex {
	idx := make(contextLengthIndex)
	for _, e := range entries {
		if e.Body == "" {
			continue
		}
		var parsed struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(e.Body), &parsed); err != nil {
			continue
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
			key := indexKey(e.Provider, record.ID)
			idx[key] = int(record.ContextLength)
		}
	}
	return idx
}

func indexKey(provider, upstreamModel string) string {
	return provider + "/" + upstreamModel
}

// IsExpired checks whether the given generated_at timestamp has exceeded the TTL.
// nowFunc is the time source; use time.Now in production and fixed times in tests.
func IsExpired(generatedAt string, ttl time.Duration, nowFunc func() time.Time) bool {
	if generatedAt == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, generatedAt)
	if err != nil {
		return true
	}
	return nowFunc().After(t.Add(ttl))
}

// NextRefreshDelay calculates the duration to wait before the next refresh.
// Returns 0 if the snapshot is already expired (immediate refresh needed).
// Returns the remaining TTL duration otherwise.
func NextRefreshDelay(generatedAt string, ttl time.Duration, nowFunc func() time.Time) time.Duration {
	if generatedAt == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, generatedAt)
	if err != nil {
		return 0
	}
	expiresAt := t.Add(ttl)
	if nowFunc().After(expiresAt) {
		return 0
	}
	return expiresAt.Sub(nowFunc())
}

// MergeWithPrevious merges fresh probe entries with the previous snapshot's entries.
// Rules:
//   - Fresh success entries replace previous entries for the same provider.
//   - Fresh failure entries: keep previous success entry if it exists; otherwise, write the failure entry.
//   - The top-level GeneratedAt is set to the latest LastSuccessAt across all merged providers.
//     If there are no successful entries, GeneratedAt is left empty.
func MergeWithPrevious(prev CatalogSnapshot, fresh []ProviderEntry) CatalogSnapshot {
	prevByProvider := make(map[string]ProviderEntry)
	for _, e := range prev.Providers {
		prevByProvider[e.Provider] = e
	}

	merged := make([]ProviderEntry, 0, len(fresh)+len(prev.Providers))
	seenProviders := make(map[string]bool)

	for _, freshEntry := range fresh {
		seenProviders[freshEntry.Provider] = true
		if freshEntry.Error == "" || freshEntry.LastSuccessAt != "" {
			// Success: use fresh entry
			merged = append(merged, freshEntry)
		} else if prevEntry, exists := prevByProvider[freshEntry.Provider]; exists && prevEntry.Error == "" && prevEntry.LastSuccessAt != "" {
			// Failure, but previous had success: keep previous
			merged = append(merged, prevEntry)
		} else {
			// Failure and no previous success: write failure entry
			merged = append(merged, freshEntry)
		}
	}

	// Compute new GeneratedAt as the latest success timestamp
	var latestSuccess string
	for _, e := range merged {
		if e.LastSuccessAt != "" && e.Error == "" {
			if latestSuccess == "" || e.LastSuccessAt > latestSuccess {
				latestSuccess = e.LastSuccessAt
			}
		}
	}

	return CatalogSnapshot{
		GeneratedAt: latestSuccess,
		Providers:   merged,
	}
}

// CatalogModelView 表示目录中单个上游模型（脱敏，仅 UI 所需字段）。
type CatalogModelView struct {
	ID            string `json:"id"`
	ContextLength *int   `json:"context_length,omitempty"`
}

// CatalogProviderView 表示目录中单个 Provider 的脱敏视图。
// status 仅反映受控枚举：ok / probe_failed / unsupported，绝不透传原始错误文本。
type CatalogProviderView struct {
	Name          string             `json:"name"`
	Protocol      string             `json:"protocol"`
	LastSuccessAt string             `json:"last_success_at,omitempty"`
	Status        string             `json:"status"`
	Models        []CatalogModelView `json:"models"`
}

// CatalogView 表示脱敏、规范化的目录视图（供 Admin UI 自动补全使用）。
type CatalogView struct {
	GeneratedAt string                `json:"generated_at"`
	Stale       bool                  `json:"stale"`
	Providers   []CatalogProviderView `json:"providers"`
}

// CatalogProviderStatus 是受控的状态枚举值。
const (
	CatalogStatusOK          = "ok"
	CatalogStatusProbeFailed = "probe_failed"
	CatalogStatusUnsupported = "unsupported"
)

// BuildCatalogView 从当前快照构建脱敏、规范化的目录视图。
// 规则：
//   - 对同名 Provider 的多条快照记录按 Provider 聚合，每条快照记录作为一个候选优先记录；
//     记录比较使用可解析的 RFC3339 last_success_at：较晚者优先，时间缺失或无效时
//     按快照原始顺序中靠后的记录兜底。
//   - 优先记录决定该 Provider 的 protocol、last_success_at 与 status。
//   - 模型按 ID 去重并升序输出；同 ID 的 context_length 取优先记录中的有效值，
//     缺失时按记录顺序查找其他有效值。
//   - status：优先记录有成功（Error 为空且 LastSuccessAt 非空）或本身无错误为 ok；
//     错误以 skipped: 开头为 unsupported，其他错误为 probe_failed。
//   - 不输出 URL、Body、Error、API Key 或任何错误原文。
//   - stale 由 IsExpired 服务端计算（无生成时间视为 stale）。
func (s *Service) BuildCatalogView(nowFunc func() time.Time) CatalogView {
	s.mu.RLock()
	snapshot := s.snapshot
	s.mu.RUnlock()

	view := CatalogView{
		GeneratedAt: snapshot.GeneratedAt,
		Stale:       IsExpired(snapshot.GeneratedAt, DefaultTTL, nowFunc),
	}

	// 按 Provider 聚合原始记录（保留原始顺序）
	type agg struct {
		provider string
		entries  []ProviderEntry
	}
	order := make([]string, 0)
	byProvider := make(map[string]*agg)
	for _, e := range snapshot.Providers {
		if a, ok := byProvider[e.Provider]; ok {
			a.entries = append(a.entries, e)
			continue
		}
		order = append(order, e.Provider)
		byProvider[e.Provider] = &agg{provider: e.Provider, entries: []ProviderEntry{e}}
	}

	for _, name := range order {
		a := byProvider[name]
		pref := pickPreferredEntry(a.entries)
		pv := CatalogProviderView{
			Name:          a.provider,
			Protocol:      pref.Protocol,
			LastSuccessAt: pref.LastSuccessAt,
			Status:        deriveStatus(pref),
		}

		// 收集模型：按 ID 去重，context_length 取优先记录有效值，缺失时按优先顺序查找
		ordered := orderedEntries(a.entries, pref)
		modelIdx := make(map[string]*CatalogModelView)
		modelOrder := make([]string, 0)
		for _, e := range ordered {
			models := parseModels(e)
			for _, m := range models {
				existing, ok := modelIdx[m.ID]
				if !ok {
					modelOrder = append(modelOrder, m.ID)
					existing = &CatalogModelView{ID: m.ID}
					modelIdx[m.ID] = existing
				}
				if existing.ContextLength == nil && m.ContextLength != nil {
					existing.ContextLength = m.ContextLength
				}
			}
		}

		sortStrings(modelOrder)
		for _, id := range modelOrder {
			pv.Models = append(pv.Models, *modelIdx[id])
		}
		view.Providers = append(view.Providers, pv)
	}

	return view
}

// EmptyCatalogView 返回空且 stale 的目录视图（用于无 source 的降级场景）。
func EmptyCatalogView() CatalogView {
	return CatalogView{
		GeneratedAt: "",
		Stale:       true,
		Providers:   []CatalogProviderView{},
	}
}

// CatalogView 实现 handler.CatalogSource 接口，返回当前快照的脱敏视图。
func (s *Service) CatalogView() CatalogView {
	return s.BuildCatalogView(time.Now)
}

// pickPreferredEntry 从多条同 Provider 记录中选择优先记录：
// 可解析的 RFC3339 last_success_at 较晚者优先；时间缺失或无效时
// 按传入原始顺序中靠后的记录兜底。
func pickPreferredEntry(entries []ProviderEntry) ProviderEntry {
	if len(entries) == 0 {
		return ProviderEntry{}
	}
	pref := entries[0]
	prefTime, prefOK := parseRFC3339(pref.LastSuccessAt)
	for _, e := range entries[1:] {
		t, ok := parseRFC3339(e.LastSuccessAt)
		if ok && prefOK {
			if t.After(prefTime) {
				pref = e
				prefTime = t
			}
		} else if ok && !prefOK {
			pref = e
			prefTime = t
			prefOK = true
		} else if !ok && !prefOK {
			// 两者都缺失/无效：靠后者兜底
			pref = e
		}
	}
	return pref
}

// orderedEntries 将优先记录排在首位，其余按原始顺序跟在后面，
// 供模型收集按优先顺序遍历。
func orderedEntries(entries []ProviderEntry, pref ProviderEntry) []ProviderEntry {
	if len(entries) == 0 {
		return entries
	}
	ordered := make([]ProviderEntry, 0, len(entries))
	ordered = append(ordered, pref)
	for _, e := range entries {
		if e == pref {
			continue
		}
		ordered = append(ordered, e)
	}
	return ordered
}

func parseRFC3339(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// deriveStatus 由优先记录推导受控状态枚举。
func deriveStatus(e ProviderEntry) string {
	if e.Error == "" || e.LastSuccessAt != "" {
		return CatalogStatusOK
	}
	if strings.HasPrefix(e.Error, "skipped:") {
		return CatalogStatusUnsupported
	}
	return CatalogStatusProbeFailed
}

// catalogModel 表示从 body 解析出的模型候选。
type catalogModel struct {
	ID            string
	ContextLength *int
}

// parseModels 仅解析 OpenAI 风格 data[] 模型记录，返回去重前的候选列表。
// 空 ID 或非正数 context_length 不作为有效长度输出；无法解析响应不产生候选。
func parseModels(e ProviderEntry) []catalogModel {
	var out []catalogModel
	if e.Body == "" {
		return out
	}
	var parsed struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(e.Body), &parsed); err != nil {
		return out
	}
	for _, raw := range parsed.Data {
		var record struct {
			ID            string  `json:"id"`
			ContextLength float64 `json:"context_length"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			continue
		}
		if record.ID == "" {
			continue
		}
		m := catalogModel{ID: record.ID}
		if record.ContextLength > 0 {
			v := int(record.ContextLength)
			m.ContextLength = &v
		}
		out = append(out, m)
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// HasAnySuccess checks whether the snapshot contains at least one successful entry.

func (s *Service) HasAnySuccess() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.snapshot.Providers {
		if e.LastSuccessAt != "" && e.Error == "" {
			return true
		}
	}
	return false
}
