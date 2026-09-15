package modelsdev

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultURL 是 models.dev 社区模型目录的 API 地址。
const DefaultURL = "https://models.dev/api.json"

// maxFetchBytes 限制拉取响应体大小，防止上游异常膨胀拖垮小内存机器。
const maxFetchBytes = 20 << 20 // 20 MiB

// trailingDateSuffix 匹配常见尾部日期/日期戳，用于从 upstream 还原社区 base id。
// 例：-20160605（YYYYMMDD）、-202606（YYYYMM）、-2024-07-18（YYYY-MM-DD）
var trailingDateSuffix = regexp.MustCompile(`(-\d{8}|-\d{6}|-\d{4}-\d{2}-\d{2})$`)

// Index 是紧凑的模型目录索引：模型 id（小写）→ 上下文窗口。
//
// 只保留 context_length，不保留原始 api.json 全量数据。
// Lookup 对 upstream 做规范化后做 exact map 查询，避免子串误匹配。
type Index struct {
	byID map[string]int
}

// NewIndex 从 id→ctx 映射构建索引（自动小写化、去重取最大值）。
func NewIndex(m map[string]int) *Index {
	byID := make(map[string]int, len(m))
	for id, ctx := range m {
		if id == "" || ctx <= 0 {
			continue
		}
		low := strings.ToLower(id)
		if ctx > byID[low] {
			byID[low] = ctx
		}
	}
	return &Index{byID: byID}
}

// Len 返回索引中的模型数量。
func (ix *Index) Len() int {
	if ix == nil {
		return 0
	}
	return len(ix.byID)
}

// Lookup 返回 upstreamModel 经规范化后 exact 命中的上下文窗口。
//
// 候选生成（小写）：
//  1. 完整 upstream
//  2. 每个 path 分段（按 / 切）
//  3. 上述候选去掉尾部日期后缀后的形式
//
// 多候选同时命中时取最长 id（如同时有 gpt-4 与 gpt-4o 时优先更具体者）。
// 无命中返回 0, false。不使用子串 Contains，避免 o1/auto/fast 等短 id 误伤。
func (ix *Index) Lookup(upstreamModel string) (int, bool) {
	if ix == nil || len(ix.byID) == 0 || upstreamModel == "" {
		return 0, false
	}
	bestLen := -1
	bestCtx := 0
	for _, c := range lookupCandidates(upstreamModel) {
		ctx, ok := ix.byID[c]
		if !ok {
			continue
		}
		if len(c) > bestLen {
			bestLen = len(c)
			bestCtx = ctx
		}
	}
	if bestLen < 0 {
		return 0, false
	}
	return bestCtx, true
}

// lookupCandidates 生成 upstream 的 exact-match 候选（已小写、去重）。
func lookupCandidates(upstreamModel string) []string {
	up := strings.ToLower(strings.TrimSpace(upstreamModel))
	if up == "" {
		return nil
	}
	seen := make(map[string]struct{}, 8)
	out := make([]string, 0, 8)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
		// 剥一层日期后缀再作为候选
		if stripped := trailingDateSuffix.ReplaceAllString(s, ""); stripped != s && stripped != "" {
			if _, ok := seen[stripped]; !ok {
				seen[stripped] = struct{}{}
				out = append(out, stripped)
			}
		}
	}

	add(up)
	for _, part := range strings.Split(up, "/") {
		add(part)
	}
	return out
}

// WriteToFile 原子写入 tsv（id\tctx，按 id 字典序，便于 diff/排查）。
// 必要时创建父目录。
func (ix *Index) WriteToFile(path string) error {
	if ix == nil {
		ix = NewIndex(nil)
	}
	ids := make([]string, 0, len(ix.byID))
	for id := range ix.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var sb strings.Builder
	for _, id := range ids {
		sb.WriteString(id)
		sb.WriteByte('\t')
		sb.WriteString(strconv.Itoa(ix.byID[id]))
		sb.WriteByte('\n')
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir modelsdev index dir: %w", err)
		}
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("write tmp modelsdev index: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename modelsdev index: %w", err)
	}
	return nil
}

// LoadFromFile 从 tsv 读取索引。文件不存在时返回空索引（不报错）。
// 非法行静默跳过；通过 NewIndex 再次收敛大小写与去重。
func LoadFromFile(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewIndex(nil), nil
		}
		return nil, fmt.Errorf("open modelsdev index: %w", err)
	}
	defer f.Close()

	m := make(map[string]int)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		ctx, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || ctx <= 0 {
			continue
		}
		m[parts[0]] = ctx
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan modelsdev index: %w", err)
	}
	return NewIndex(m), nil
}

// Fetch 拉取 models.dev api.json 并构建紧凑索引。
// 顶层按 provider 流式解码，不把完整响应体保留为单一大对象。
func Fetch(client *http.Client, url string, timeout time.Duration) (*Index, error) {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build modelsdev request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "oh-my-api/1.0 (catalog context-length enrichment)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch modelsdev: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("modelsdev fetch status %d", resp.StatusCode)
	}
	return Parse(io.LimitReader(resp.Body, maxFetchBytes))
}

// Parse 流式解析 api.json：顶层 {provider: {models: {id: {limit.context}}}}。
func Parse(r io.Reader) (*Index, error) {
	dec := json.NewDecoder(r)
	// 读顶层 {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("parse modelsdev top-level: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("parse modelsdev top-level: expected object, got %v", tok)
	}

	m := make(map[string]int)
	for dec.More() {
		// provider key
		if _, err := dec.Token(); err != nil {
			return nil, fmt.Errorf("parse modelsdev provider key: %w", err)
		}
		// provider value：只提取 models 子字段，其余（name/api/cost 等）丢弃
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("parse modelsdev provider value: %w", err)
		}
		collectContexts(raw, m)
	}
	return NewIndex(m), nil
}

// collectContexts 从单个 provider 的 JSON 中提取 id → context 到 m（同名取最大值）。
func collectContexts(raw json.RawMessage, m map[string]int) {
	var p struct {
		Models map[string]struct {
			Limit *struct {
				Context int `json:"context"`
			} `json:"limit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	for id, md := range p.Models {
		if md.Limit == nil || md.Limit.Context <= 0 {
			continue
		}
		low := strings.ToLower(id)
		if md.Limit.Context > m[low] {
			m[low] = md.Limit.Context
		}
	}
}
