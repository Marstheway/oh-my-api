package modelsdev

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleAPI = `{
  "openai": {
    "name": "OpenAI",
    "api": null,
    "models": {
      "gpt-4": {"limit": {"context": 8192, "output": 4096}},
      "gpt-4o": {"limit": {"context": 128000, "output": 16384}},
      "gpt-4o-mini": {"limit": {"context": 128000}},
      "no-limit-model": {"cost": {"input": 1}}
    }
  },
  "deepseek": {
    "models": {
      "deepseek-v4-flash": {"limit": {"context": 1000000}},
      "DeepSeek-V4-Pro": {"limit": {"context": 1000000}}
    }
  },
  "empty-provider": null,
  "dup": {
    "models": {
      "gpt-4o": {"limit": {"context": 128000}},
      "gpt-4o": {"limit": {"context": 200000}}
    }
  },
  "generic": {
    "models": {
      "o1": {"limit": {"context": 200000}},
      "auto": {"limit": {"context": 1000000}},
      "fast": {"limit": {"context": 1000000}},
      "custom": {"limit": {"context": 128000}},
      "e2e": {"limit": {"context": 1000000}}
    }
  }
}`

func TestParse_BuildsCompactIndex(t *testing.T) {
	idx, err := Parse(strings.NewReader(sampleAPI))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// 同名多源取最大值（dup provider 里 gpt-4o 出现两次，取 200000）
	if v, ok := idx.Lookup("openai/gpt-4o"); !ok || v != 200000 {
		t.Errorf("gpt-4o ctx = %d, ok=%v; want 200000", v, ok)
	}
	// 无 limit.context 的模型不入索引
	if _, ok := idx.Lookup("no-limit-model"); ok {
		t.Error("model without limit.context should not be indexed")
	}
	// id 统一小写
	if v, ok := idx.Lookup("deepseek/deepseek-v4-pro"); !ok || v != 1000000 {
		t.Errorf("deepseek-v4-pro ctx = %d, ok=%v; want 1000000", v, ok)
	}
}

func TestParse_RejectsNonObjectTopLevel(t *testing.T) {
	if _, err := Parse(strings.NewReader(`[1,2,3]`)); err == nil {
		t.Fatal("expected error for array top-level")
	}
	if _, err := Parse(strings.NewReader(`"hello"`)); err == nil {
		t.Fatal("expected error for string top-level")
	}
}

func TestLookup_NormalizedExactMatch(t *testing.T) {
	idx, err := Parse(strings.NewReader(sampleAPI))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	cases := []struct {
		name  string
		input string
		want  int
		ok    bool
	}{
		{name: "exact", input: "gpt-4o-mini", want: 128000, ok: true},
		// path 前缀 + 日期后缀：剥路径与日期后 exact 命中 gpt-4o-mini
		{name: "path-and-date", input: "openai/gpt-4o-mini-2024-07-18", want: 128000, ok: true},
		// 最后一段 exact 命中 gpt-4（不会因为子串误伤）
		{name: "last-segment", input: "legacy/gpt-4", want: 8192, ok: true},
		{name: "case-insensitive", input: "OPENAI/GPT-4O", want: 200000, ok: true},
		{name: "no-match", input: "unknown/foo-model", want: 0, ok: false},
		{name: "empty", input: "", want: 0, ok: false},
		// 紧凑 8 位日期戳
		{name: "eight-digit-date", input: "vendor/gpt-4o-20160605", want: 200000, ok: true},
		// 6 位 YYYYMM 后缀（token-plan / token-hub 风格）
		{name: "six-digit-yyyymm", input: "token-plan/deepseek-v4-pro-202606", want: 1000000, ok: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := idx.Lookup(tc.input)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Errorf("Lookup(%q) = %d, %v; want %d, %v", tc.input, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestLookup_DeepseekTokenPlanExample(t *testing.T) {
	// 用户场景：腾讯侧模型 id 带 token-plan/ 前缀和日期后缀，仍应命中社区 base id
	idx, err := Parse(strings.NewReader(sampleAPI))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if v, ok := idx.Lookup("token-plan/deepseek-v4-flash-20160605"); !ok || v != 1000000 {
		t.Errorf("token-plan deepseek ctx = %d, ok=%v; want 1000000", v, ok)
	}
}

func TestLookup_RejectsSubstringFalsePositives(t *testing.T) {
	// 社区索引含短/通用 id 时，Contains 会误伤；exact+normalize 必须拒绝。
	idx, err := Parse(strings.NewReader(sampleAPI))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// 这些 upstream 不含与短 id exact 相等的分段，不得命中
	rejects := []string{
		"photo1-preview",
		"my-custom-auto-router",
		"provider/fast-model-v2",
		"foo/bar-e2e-test",
		"something-o1-wrapper",
		"azure/gpt-4-turbo", // 索引无 gpt-4-turbo，不应退回 gpt-4 子串
	}
	for _, up := range rejects {
		if v, ok := idx.Lookup(up); ok {
			t.Errorf("Lookup(%q) = %d, true; want no match (false-positive)", up, v)
		}
	}

	// 字面量恰好为短 id 时仍可命中（exact 合法）
	if v, ok := idx.Lookup("openrouter/auto"); !ok || v != 1000000 {
		t.Errorf("exact short id Lookup = %d, ok=%v; want 1000000", v, ok)
	}
	if v, ok := idx.Lookup("o1"); !ok || v != 200000 {
		t.Errorf("exact o1 Lookup = %d, ok=%v; want 200000", v, ok)
	}
}

func TestLookup_PrefersLongerExactCandidate(t *testing.T) {
	// 同时有 gpt-4 与 gpt-4o 时，候选 gpt-4o 应优先于 gpt-4
	idx := NewIndex(map[string]int{
		"gpt-4":  8192,
		"gpt-4o": 128000,
	})
	if v, ok := idx.Lookup("openai/gpt-4o"); !ok || v != 128000 {
		t.Errorf("Lookup gpt-4o = %d, ok=%v; want 128000", v, ok)
	}
	if v, ok := idx.Lookup("openai/gpt-4"); !ok || v != 8192 {
		t.Errorf("Lookup gpt-4 = %d, ok=%v; want 8192", v, ok)
	}
}

func TestNewIndex_Empty(t *testing.T) {
	idx := NewIndex(nil)
	if idx.Len() != 0 {
		t.Fatalf("Len = %d, want 0", idx.Len())
	}
	if _, ok := idx.Lookup("anything"); ok {
		t.Error("empty index should never match")
	}
	// nil receiver 安全
	var nilIdx *Index
	if _, ok := nilIdx.Lookup("anything"); ok {
		t.Error("nil index should never match")
	}
}

func TestWriteLoad_RoundTrip(t *testing.T) {
	idx, err := Parse(strings.NewReader(sampleAPI))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	path := filepath.Join(t.TempDir(), "models_dev_ctx.tsv")
	if err := idx.WriteToFile(path); err != nil {
		t.Fatalf("WriteToFile error: %v", err)
	}

	loaded, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile error: %v", err)
	}
	if loaded.Len() != idx.Len() {
		t.Fatalf("loaded Len = %d, want %d", loaded.Len(), idx.Len())
	}
	for _, probe := range []string{"gpt-4", "gpt-4o-mini", "deepseek-v4-pro", "deepseek-v4-flash"} {
		want, ok := idx.Lookup(probe)
		got, gotOK := loaded.Lookup(probe)
		if ok != gotOK || want != got {
			t.Errorf("Lookup(%q) after round-trip = %d,%v; want %d,%v", probe, got, gotOK, want, ok)
		}
	}
}

func TestWriteToFile_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "models_dev_ctx.tsv")
	idx := NewIndex(map[string]int{"gpt-4o": 128000})
	if err := idx.WriteToFile(path); err != nil {
		t.Fatalf("WriteToFile: %v", err)
	}
	loaded, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if v, ok := loaded.Lookup("gpt-4o"); !ok || v != 128000 {
		t.Errorf("loaded = %d, ok=%v; want 128000", v, ok)
	}
}

func TestLoadFromFile_MissingFileReturnsEmpty(t *testing.T) {
	idx, err := LoadFromFile(filepath.Join(t.TempDir(), "does-not-exist.tsv"))
	if err != nil {
		t.Fatalf("LoadFromFile should not error for missing file: %v", err)
	}
	if idx.Len() != 0 {
		t.Fatalf("Len = %d, want 0", idx.Len())
	}
}

func TestLoadFromFile_SkipsBadLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models_dev_ctx.tsv")
	content := "gpt-4o\t128000\nbad-line\n\ngpt-4\tnot-a-number\ndeepseek-v4-flash\t1000000\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	idx, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile error: %v", err)
	}
	if idx.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (bad lines skipped)", idx.Len())
	}
	if v, ok := idx.Lookup("x/gpt-4o"); !ok || v != 128000 {
		t.Errorf("gpt-4o ctx = %d, ok=%v; want 128000", v, ok)
	}
}

func TestFetch_FromHTTPServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q, want application/json", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleAPI))
	}))
	defer srv.Close()

	idx, err := Fetch(nil, srv.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if idx.Len() == 0 {
		t.Fatal("Fetch returned empty index")
	}
	if v, ok := idx.Lookup("openai/gpt-4o"); !ok || v != 200000 {
		t.Errorf("gpt-4o ctx = %d, ok=%v; want 200000", v, ok)
	}
}

func TestFetch_Non2xxFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := Fetch(nil, srv.URL, 5*time.Second); err == nil {
		t.Fatal("expected error for non-2xx status")
	}
}

func TestFetch_NetworkErrorFails(t *testing.T) {
	if _, err := Fetch(nil, "http://127.0.0.1:1", time.Second); err == nil {
		t.Fatal("expected error for unreachable server")
	}
}
