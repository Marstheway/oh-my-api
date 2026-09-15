package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/modelsdev"
)

func TestIsExpired_EmptyString(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	if !IsExpired("", DefaultTTL, now) {
		t.Error("empty generated_at should be expired")
	}
}

func TestIsExpired_ValidNotExpired(t *testing.T) {
	genAt := "2026-07-12T13:00:00Z" // 11h ago
	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	if IsExpired(genAt, DefaultTTL, now) {
		t.Error("11h old should not be expired with 12h TTL")
	}
}

func TestIsExpired_ValidExpired(t *testing.T) {
	genAt := "2026-07-12T11:00:00Z" // 13h ago
	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	if !IsExpired(genAt, DefaultTTL, now) {
		t.Error("13h old should be expired with 12h TTL")
	}
}

func TestIsExpired_InvalidFormat(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	if !IsExpired("not-a-timestamp", DefaultTTL, now) {
		t.Error("invalid format should be expired")
	}
}

func TestNextRefreshDelay_EmptyString(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	if d := NextRefreshDelay("", DefaultTTL, now); d != 0 {
		t.Errorf("empty generated_at should return 0, got %v", d)
	}
}

func TestNextRefreshDelay_Expired(t *testing.T) {
	genAt := "2026-07-12T11:00:00Z" // 13h ago
	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	if d := NextRefreshDelay(genAt, DefaultTTL, now); d != 0 {
		t.Errorf("expired should return 0, got %v", d)
	}
}

func TestNextRefreshDelay_NotExpired(t *testing.T) {
	genAt := "2026-07-12T13:00:00Z" // 11h ago, 1h remaining
	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	d := NextRefreshDelay(genAt, DefaultTTL, now)
	if d <= 0 {
		t.Errorf("should have positive delay, got %v", d)
	}
	if d > DefaultTTL {
		t.Errorf("delay %v exceeds TTL %v", d, DefaultTTL)
	}
}

func TestNextRefreshDelay_ExactlyAtExpiry(t *testing.T) {
	genAt := "2026-07-12T12:00:00Z" // exactly 12h ago
	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	d := NextRefreshDelay(genAt, DefaultTTL, now)
	if d != 0 {
		t.Errorf("exactly at expiry should return 0, got %v", d)
	}
}

func TestLoadFromFile_NotExists(t *testing.T) {
	svc := NewService()
	if err := svc.LoadFromFile("/nonexistent/path/catalog.json"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.GeneratedAt() != "" {
		t.Error("generated_at should be empty for missing file")
	}
}

func TestLoadFromFile_Valid(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "catalog_upstream.json")
	data := `{
  "generated_at": "2026-07-12T12:00:00Z",
  "providers": [
    {
      "provider": "openai",
      "protocol": "openai.chat",
      "url": "https://api.openai.com/v1/models",
      "status_code": 200,
      "body": "{\"data\":[{\"id\":\"gpt-4o\",\"context_length\":128000}]}",
      "last_success_at": "2026-07-12T12:00:00Z"
    }
  ]
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	svc := NewService()
	if err := svc.LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if svc.GeneratedAt() != "2026-07-12T12:00:00Z" {
		t.Errorf("generated_at = %q, want %q", svc.GeneratedAt(), "2026-07-12T12:00:00Z")
	}

	v, ok := svc.ContextLength("openai", "gpt-4o")
	if !ok || v != 128000 {
		t.Errorf("context_length = %d, ok=%v; want 128000", v, ok)
	}
}

func TestLoadFromFile_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "catalog_upstream.json")
	if err := os.WriteFile(path, []byte("{not json}"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	svc := NewService()
	if err := svc.LoadFromFile(path); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestContextLength_EmptyIndex(t *testing.T) {
	svc := NewService()
	_, ok := svc.ContextLength("openai", "gpt-4o")
	if ok {
		t.Error("empty index should not find context_length")
	}
}

func TestContextLength_Valid(t *testing.T) {
	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		GeneratedAt: "2026-07-12T12:00:00Z",
		Providers: []ProviderEntry{
			{
				Provider: "openai",
				Body:     `{"data":[{"id":"gpt-4o","context_length":128000}, {"id":"gpt-4","context_length":8192}]}`,
			},
			{
				Provider: "anthropic",
				Body:     `{"data":[{"id":"claude-3-opus","context_length":200000}]}`,
			},
		},
	})

	tests := []struct {
		provider string
		model    string
		want     int
		wantOK   bool
	}{
		{"openai", "gpt-4o", 128000, true},
		{"openai", "gpt-4", 8192, true},
		{"anthropic", "claude-3-opus", 200000, true},
		{"openai", "nonexistent", 0, false},
		{"unknown", "gpt-4o", 0, false},
	}

	for _, tt := range tests {
		got, ok := svc.ContextLength(tt.provider, tt.model)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("ContextLength(%q, %q) = (%d, %v), want (%d, %v)", tt.provider, tt.model, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestContextLength_IgnoresFailedEntries(t *testing.T) {
	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		GeneratedAt: "2026-07-12T12:00:00Z",
		Providers: []ProviderEntry{
			{
				Provider: "openai",
				Error:    "connection refused",
				Body:     "",
			},
			{
				Provider:      "anthropic",
				Body:          `{"data":[{"id":"claude-3-opus","context_length":200000}]}`,
				LastSuccessAt: "2026-07-12T12:00:00Z",
			},
		},
	})

	_, ok := svc.ContextLength("openai", "anything")
	if ok {
		t.Error("failed entry should not produce context_length")
	}

	v, ok := svc.ContextLength("anthropic", "claude-3-opus")
	if !ok || v != 200000 {
		t.Errorf("anthropic entry: got %d, ok=%v, want 200000", v, ok)
	}
}

func TestProviderEntries(t *testing.T) {
	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		GeneratedAt: "2026-07-12T12:00:00Z",
		Providers: []ProviderEntry{
			{Provider: "openai", Body: `{"data":[{"id":"gpt-4o"}]}`, LastSuccessAt: "2026-07-12T12:00:00Z"},
			{Provider: "anthropic", Error: "timeout"},
			{Provider: "openai", Body: `{"data":[{"id":"gpt-4"}]}`, LastSuccessAt: "2026-07-12T11:00:00Z"},
		},
	})

	entries := svc.ProviderEntries("openai")
	if len(entries) != 2 {
		t.Fatalf("openai entries count = %d, want 2", len(entries))
	}
	if entries[0].Provider != "openai" {
		t.Errorf("entry provider = %q, want openai", entries[0].Provider)
	}

	entries = svc.ProviderEntries("unknown")
	if len(entries) != 0 {
		t.Errorf("unknown provider should have 0 entries, got %d", len(entries))
	}
}

func TestReplaceSnapshot_PreservesOldSuccessOnPartialRefresh(t *testing.T) {
	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		GeneratedAt: "2026-07-12T12:00:00Z",
		Providers: []ProviderEntry{
			{Provider: "openai", Body: `{"data":[{"id":"gpt-4o","context_length":128000}]}`, LastSuccessAt: "2026-07-12T12:00:00Z"},
			{Provider: "anthropic", Body: `{"data":[{"id":"claude-3","context_length":200000}]}`, LastSuccessAt: "2026-07-12T12:00:00Z"},
		},
	})

	// fresh entries: anthropic fails, openai succeeds
	fresh := []ProviderEntry{
		{Provider: "openai", Body: `{"data":[{"id":"gpt-4o","context_length":128000}]}`, LastSuccessAt: "2026-07-13T00:00:00Z"},
		{Provider: "anthropic", Error: "timeout"},
	}

	merged := MergeWithPrevious(svc.Snapshot(), fresh)
	svc.ReplaceSnapshot(merged)

	// openai should be updated
	entries := svc.ProviderEntries("openai")
	if len(entries) != 1 {
		t.Fatalf("openai entries = %d, want 1", len(entries))
	}
	if entries[0].LastSuccessAt != "2026-07-13T00:00:00Z" {
		t.Errorf("openai last_success_at = %q, want new timestamp", entries[0].LastSuccessAt)
	}

	// anthropic keeps old success
	entries = svc.ProviderEntries("anthropic")
	if len(entries) != 1 {
		t.Fatalf("anthropic entries = %d, want 1", len(entries))
	}
	if entries[0].Error != "" {
		t.Error("anthropic should retain old success entry without error")
	}
	if entries[0].Body == "" {
		t.Error("anthropic should retain old body")
	}

	v, ok := svc.ContextLength("anthropic", "claude-3")
	if !ok || v != 200000 {
		t.Errorf("context_length for anthropic/claude-3 = %d, want 200000", v)
	}
}

func TestMergeWithPrevious_NeverSuccessfulWritesFailure(t *testing.T) {
	prev := CatalogSnapshot{
		GeneratedAt: "",
		Providers:   []ProviderEntry{},
	}

	fresh := []ProviderEntry{
		{Provider: "new-provider", Error: "connection refused", Protocol: "openai.chat", URL: "https://example.com/v1/models"},
	}

	merged := MergeWithPrevious(prev, fresh)
	if len(merged.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(merged.Providers))
	}
	if merged.Providers[0].Error != "connection refused" {
		t.Errorf("error = %q, want connection refused", merged.Providers[0].Error)
	}
	if merged.GeneratedAt != "" {
		t.Errorf("generated_at should be empty when no success, got %q", merged.GeneratedAt)
	}
}

func TestMergeWithPrevious_MixedResults(t *testing.T) {
	prev := CatalogSnapshot{
		GeneratedAt: "2026-07-12T12:00:00Z",
		Providers: []ProviderEntry{
			{Provider: "a", Body: "old", LastSuccessAt: "2026-07-12T12:00:00Z"},
			{Provider: "b", Body: "old", LastSuccessAt: "2026-07-12T12:00:00Z"},
		},
	}

	fresh := []ProviderEntry{
		{Provider: "a", Body: "new", LastSuccessAt: "2026-07-13T00:00:00Z"},
		{Provider: "b", Error: "timeout", Protocol: "openai.chat", URL: "https://example.com"},
		{Provider: "c", Error: "dns error", Protocol: "ollama.chat", URL: "http://localhost:11434"},
	}

	merged := MergeWithPrevious(prev, fresh)
	if len(merged.Providers) != 3 {
		t.Fatalf("providers = %d, want 3", len(merged.Providers))
	}

	// a: updated
	for _, e := range merged.Providers {
		switch e.Provider {
		case "a":
			if e.Body != "new" {
				t.Error("provider a should have new body")
			}
		case "b":
			if e.Error != "" {
				t.Error("provider b should retain old success (no error)")
			}
		case "c":
			if e.Error == "" {
				t.Error("provider c (never successful) should have error")
			}
		}
	}

	// generated_at should be the latest success (a = 2026-07-13T00:00:00Z)
	if merged.GeneratedAt != "2026-07-13T00:00:00Z" {
		t.Errorf("generated_at = %q, want 2026-07-13T00:00:00Z", merged.GeneratedAt)
	}
}

func TestMergeWithPrevious_DropsProviderNoLongerInFresh(t *testing.T) {
	// Providers that existed in prev but are NOT in fresh are dropped.
	// Only providers present in the current refresh round are retained.
	prev := CatalogSnapshot{
		GeneratedAt: "2026-07-12T12:00:00Z",
		Providers: []ProviderEntry{
			{Provider: "openai", Body: "data", LastSuccessAt: "2026-07-12T12:00:00Z"},
			{Provider: "old-provider", Body: "data", LastSuccessAt: "2026-07-12T12:00:00Z"},
		},
	}

	fresh := []ProviderEntry{
		{Provider: "openai", Body: "new-data", LastSuccessAt: "2026-07-13T00:00:00Z"},
	}

	merged := MergeWithPrevious(prev, fresh)
	if len(merged.Providers) != 1 {
		t.Fatalf("providers = %d, want 1 (old-provider should be dropped)", len(merged.Providers))
	}
	if merged.Providers[0].Provider != "openai" {
		t.Errorf("remaining provider = %q, want openai", merged.Providers[0].Provider)
	}
	if merged.GeneratedAt != "2026-07-13T00:00:00Z" {
		t.Errorf("generated_at = %q, want 2026-07-13T00:00:00Z", merged.GeneratedAt)
	}
}

func TestWriteToFile_Atomically(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "catalog_upstream.json")

	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		GeneratedAt: "2026-07-12T12:00:00Z",
		Providers: []ProviderEntry{
			{Provider: "openai", Body: "test", LastSuccessAt: "2026-07-12T12:00:00Z"},
		},
	})

	if err := svc.WriteToFile(path); err != nil {
		t.Fatalf("WriteToFile: %v", err)
	}

	// Verify temp file doesn't exist (atomically renamed)
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("temp file should not exist after atomic write")
	}

	// Reload and verify
	svc2 := NewService()
	if err := svc2.LoadFromFile(path); err != nil {
		t.Fatalf("reload: %v", err)
	}
	v, ok := svc2.ContextLength("openai", "anything")
	if ok {
		t.Errorf("reloaded index should not have context_length from non-list body, got %d", v)
	}
}

func TestHasAnySuccess_Empty(t *testing.T) {
	svc := NewService()
	if svc.HasAnySuccess() {
		t.Error("empty snapshot should not have success")
	}
}

func TestHasAnySuccess_OnlyFailures(t *testing.T) {
	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		Providers: []ProviderEntry{
			{Provider: "a", Error: "timeout"},
		},
	})
	if svc.HasAnySuccess() {
		t.Error("only failures should not count as success")
	}
}

func TestHasAnySuccess_Mixed(t *testing.T) {
	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		Providers: []ProviderEntry{
			{Provider: "a", Error: "timeout"},
			{Provider: "b", Body: "data", LastSuccessAt: "2026-07-12T12:00:00Z"},
		},
	})
	if !svc.HasAnySuccess() {
		t.Error("should have success when at least one entry succeeded")
	}
}

func TestBuildCatalogView_SuccessEntries(t *testing.T) {
	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		GeneratedAt: "2026-07-12T12:00:00Z",
		Providers: []ProviderEntry{
			{
				Provider:      "openai",
				Protocol:      "openai.chat",
				URL:           "https://api.openai.com/v1/models",
				Body:          `{"data":[{"id":"gpt-4o","context_length":128000},{"id":"gpt-4","context_length":8192}]}`,
				LastSuccessAt: "2026-07-12T12:00:00Z",
			},
		},
	})

	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	view := svc.BuildCatalogView(now)

	if view.Stale {
		t.Error("fresh snapshot should not be stale")
	}
	if len(view.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(view.Providers))
	}
	pv := view.Providers[0]
	if pv.Name != "openai" || pv.Protocol != "openai.chat" {
		t.Errorf("provider = %q/%q, want openai/openai.chat", pv.Name, pv.Protocol)
	}
	if pv.Status != "ok" {
		t.Errorf("status = %q, want ok", pv.Status)
	}
	// models 应按 ID 升序
	if len(pv.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(pv.Models))
	}
	if pv.Models[0].ID != "gpt-4" || pv.Models[1].ID != "gpt-4o" {
		t.Errorf("model order = %v, want ascending", []string{pv.Models[0].ID, pv.Models[1].ID})
	}
	if pv.Models[0].ContextLength == nil || *pv.Models[0].ContextLength != 8192 {
		t.Errorf("gpt-4 context_length = %v, want 8192", pv.Models[0].ContextLength)
	}
	if pv.Models[1].ContextLength == nil || *pv.Models[1].ContextLength != 128000 {
		t.Errorf("gpt-4o context_length = %v, want 128000", pv.Models[1].ContextLength)
	}
}

func TestBuildCatalogView_StaleAndEmpty(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }

	// 无生成时间：stale，无 provider
	empty := NewService()
	v1 := empty.BuildCatalogView(now)
	if !v1.Stale {
		t.Error("empty snapshot should be stale")
	}
	if len(v1.Providers) != 0 {
		t.Errorf("empty providers = %d, want 0", len(v1.Providers))
	}

	// 过期生成时间
	expired := NewService()
	expired.ReplaceSnapshot(CatalogSnapshot{
		GeneratedAt: "2026-07-12T11:00:00Z", // 13h 前，超过 12h TTL
		Providers:   []ProviderEntry{{Provider: "openai", Body: `{"data":[{"id":"gpt-4o"}]}`, LastSuccessAt: "2026-07-12T11:00:00Z"}},
	})
	v2 := expired.BuildCatalogView(now)
	if !v2.Stale {
		t.Error("expired snapshot should be stale")
	}
}

func TestBuildCatalogView_SensitiveFieldsExcluded(t *testing.T) {
	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		GeneratedAt: "2026-07-12T12:00:00Z",
		Providers: []ProviderEntry{
			{
				Provider:      "openai",
				Protocol:      "openai.chat",
				URL:           "https://api.openai.com/v1/models?api_key=sk-secret-123",
				Body:          `{"data":[{"id":"gpt-4o","context_length":128000}]}`,
				LastSuccessAt: "2026-07-12T12:00:00Z",
				Error:         "GET https://api.openai.com/v1/models?api_key=sk-secret-123 failed: 401",
			},
		},
	})

	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	view := svc.BuildCatalogView(now)

	data, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, leak := range []string{"sk-secret-123", "api_key", "401", "failed", "https://api.openai.com/v1/models?api_key"} {
		if strings.Contains(s, leak) {
			t.Errorf("catalog view must not leak %q, got: %s", leak, s)
		}
	}
	// 必须包含受控 status
	if !strings.Contains(s, `"status":"ok"`) {
		t.Errorf("expected ok status in output, got: %s", s)
	}
}

func TestBuildCatalogView_StatusEnum(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }

	cases := []struct {
		name  string
		entry ProviderEntry
		want  string
	}{
		{"ok_no_error", ProviderEntry{Provider: "a", Body: `{"data":[{"id":"m1"}]}`, LastSuccessAt: "2026-07-12T12:00:00Z"}, "ok"},
		{"unsupported_skipped", ProviderEntry{Provider: "b", Error: "skipped: not supported by this provider"}, "unsupported"},
		{"probe_failed", ProviderEntry{Provider: "c", Error: "connection refused"}, "probe_failed"},
	}

	for _, tc := range cases {
		svc := NewService()
		svc.ReplaceSnapshot(CatalogSnapshot{
			GeneratedAt: "2026-07-12T12:00:00Z",
			Providers:   []ProviderEntry{tc.entry},
		})
		view := svc.BuildCatalogView(now)
		if len(view.Providers) != 1 {
			t.Fatalf("%s: providers = %d, want 1", tc.name, len(view.Providers))
		}
		if view.Providers[0].Status != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.name, view.Providers[0].Status, tc.want)
		}
	}
}

func TestBuildCatalogView_MergeDuplicateProvidersAndModels(t *testing.T) {
	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		GeneratedAt: "2026-07-12T12:00:00Z",
		Providers: []ProviderEntry{
			{
				Provider:      "openai",
				Protocol:      "openai.chat",
				Body:          `{"data":[{"id":"gpt-4o","context_length":128000}]}`,
				LastSuccessAt: "2026-07-11T12:00:00Z", // 较早
			},
			{
				Provider:      "openai",
				Protocol:      "openai.chat",
				Body:          `{"data":[{"id":"gpt-4o"},{"id":"gpt-4","context_length":8192}]}`,
				LastSuccessAt: "2026-07-12T12:00:00Z", // 较晚，优先
			},
		},
	})

	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	view := svc.BuildCatalogView(now)

	if len(view.Providers) != 1 {
		t.Fatalf("providers = %d, want 1 (merged)", len(view.Providers))
	}
	pv := view.Providers[0]
	if pv.LastSuccessAt != "2026-07-12T12:00:00Z" {
		t.Errorf("last_success_at = %q, want later entry", pv.LastSuccessAt)
	}
	if len(pv.Models) != 2 {
		t.Fatalf("models = %d, want 2 (deduped)", len(pv.Models))
	}
	// gpt-4o context_length 取优先记录（缺失）-> 查找较早记录有效值 128000
	if pv.Models[0].ID != "gpt-4" || pv.Models[1].ID != "gpt-4o" {
		t.Errorf("model order = %v, want ascending", []string{pv.Models[0].ID, pv.Models[1].ID})
	}
	if pv.Models[1].ContextLength == nil || *pv.Models[1].ContextLength != 128000 {
		t.Errorf("gpt-4o context_length = %v, want 128000 (from earlier entry)", pv.Models[1].ContextLength)
	}
	if pv.Models[0].ContextLength == nil || *pv.Models[0].ContextLength != 8192 {
		t.Errorf("gpt-4 context_length = %v, want 8192", pv.Models[0].ContextLength)
	}
}

func TestBuildCatalogView_PrefEntryContextLengthWins(t *testing.T) {
	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		GeneratedAt: "2026-07-12T12:00:00Z",
		Providers: []ProviderEntry{
			{
				Provider:      "openai",
				Protocol:      "openai.chat",
				Body:          `{"data":[{"id":"gpt-4o","context_length":128000}]}`,
				LastSuccessAt: "2026-07-11T12:00:00Z", // 较早，非优先
			},
			{
				Provider:      "openai",
				Protocol:      "openai.chat",
				Body:          `{"data":[{"id":"gpt-4o","context_length":200000}]}`,
				LastSuccessAt: "2026-07-12T12:00:00Z", // 较晚，优先
			},
		},
	})

	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	view := svc.BuildCatalogView(now)

	if len(view.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(view.Providers))
	}
	pv := view.Providers[0]
	if len(pv.Models) != 1 {
		t.Fatalf("models = %d, want 1", len(pv.Models))
	}
	if pv.Models[0].ContextLength == nil || *pv.Models[0].ContextLength != 200000 {
		t.Errorf("gpt-4o context_length = %v, want 200000 (from preferred entry)", pv.Models[0].ContextLength)
	}
}

func TestBuildCatalogView_InvalidOrEmptyBody(t *testing.T) {
	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		GeneratedAt: "2026-07-12T12:00:00Z",
		Providers: []ProviderEntry{
			{Provider: "a", Body: "", LastSuccessAt: "2026-07-12T12:00:00Z"},
			{Provider: "b", Body: "{not json}", LastSuccessAt: "2026-07-12T12:00:00Z"},
			{Provider: "c", Body: `{"data":[{"id":"","context_length":100}]}`, LastSuccessAt: "2026-07-12T12:00:00Z"},
			{Provider: "d", Body: `{"data":[{"id":"m1","context_length":-5}]}`, LastSuccessAt: "2026-07-12T12:00:00Z"},
		},
	})

	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	view := svc.BuildCatalogView(now)

	if len(view.Providers) != 4 {
		t.Fatalf("providers = %d, want 4", len(view.Providers))
	}
	for _, pv := range view.Providers {
		if pv.Name == "d" {
			// context_length 为负数：模型 ID 仍保留，但 context_length 字段省略
			if len(pv.Models) != 1 {
				t.Errorf("provider %q: models = %d, want 1 (model kept, ctx omitted)", pv.Name, len(pv.Models))
			} else if pv.Models[0].ContextLength != nil {
				t.Errorf("provider %q: context_length should be nil for negative value", pv.Name)
			}
		} else if len(pv.Models) != 0 {
			t.Errorf("provider %q: models = %d, want 0 (no valid candidates)", pv.Name, len(pv.Models))
		}
		if pv.Status != "ok" {
			t.Errorf("provider %q: status = %q, want ok", pv.Name, pv.Status)
		}
	}
}

func TestBuildCatalogView_HTMLInjectionInNames(t *testing.T) {
	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		GeneratedAt: "2026-07-12T12:00:00Z",
		Providers: []ProviderEntry{
			{
				Provider:      `<img src=x onerror=alert(1)>`,
				Protocol:      "openai.chat",
				Body:          `{"data":[{"id":"<script>bad</script>","context_length":1}]}`,
				LastSuccessAt: "2026-07-12T12:00:00Z",
			},
		},
	})

	now := func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) }
	view := svc.BuildCatalogView(now)

	// 直接检查结构体字段值（不依赖 JSON 序列化的 HTML 转义行为）
	// catalog 视图不做任何 HTML 转义，前端负责用 textContent 安全渲染
	if len(view.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(view.Providers))
	}
	pv := view.Providers[0]
	if pv.Name != `<img src=x onerror=alert(1)>` {
		t.Errorf("provider name should be output as-is, got: %q", pv.Name)
	}
	if len(pv.Models) != 1 || pv.Models[0].ID != `<script>bad</script>` {
		t.Errorf("model id should be output as-is, got: %+v", pv.Models)
	}
}

func TestContextLength_ModelsDevFallback(t *testing.T) {
	svc := NewService()
	// 探测索引：只有 openai/gpt-4o
	svc.ReplaceSnapshot(CatalogSnapshot{
		Providers: []ProviderEntry{
			{Provider: "openai", Body: `{"data":[{"id":"gpt-4o","context_length":128000}]}`},
		},
	})
	// models.dev 社区索引：deepseek 带前缀/日期后缀的 upstream 经规范化 exact 命中
	svc.SetModelsDevIndex(modelsdev.NewIndex(map[string]int{
		"deepseek-v4-flash": 1000000,
		"gpt-4o":            200000,
		"o1":                200000, // 短 id：仅 exact 命中，不得子串误伤
	}))

	cases := []struct {
		name     string
		provider string
		model    string
		want     int
		wantOK   bool
	}{
		// 探测命中优先于 models.dev（即使 models.dev 值更大）
		{name: "probe-wins", provider: "openai", model: "gpt-4o", want: 128000, wantOK: true},
		// 探测 miss → fallback 到 models.dev 规范化 exact 匹配
		{name: "fallback-deepseek", provider: "tencent", model: "token-plan/deepseek-v4-flash-20160605", want: 1000000, wantOK: true},
		// 探测 miss 且 models.dev 也无命中
		{name: "no-match", provider: "tencent", model: "unknown-model", want: 0, wantOK: false},
		// 短 id 不得对含 o1 子串的名字误匹配
		{name: "no-substring-o1", provider: "p", model: "photo1-preview", want: 0, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := svc.ContextLength(tc.provider, tc.model)
			if ok != tc.wantOK || (ok && got != tc.want) {
				t.Errorf("ContextLength(%q, %q) = %d, %v; want %d, %v", tc.provider, tc.model, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestContextLength_ModelsDevNotSet(t *testing.T) {
	// 未设置 models.dev 索引时行为与之前一致：仅探测索引
	svc := NewService()
	svc.ReplaceSnapshot(CatalogSnapshot{
		Providers: []ProviderEntry{
			{Provider: "openai", Body: `{"data":[{"id":"gpt-4o","context_length":128000}]}`},
		},
	})
	if v, ok := svc.ContextLength("openai", "gpt-4o"); !ok || v != 128000 {
		t.Errorf("probe ctx = %d, ok=%v; want 128000", v, ok)
	}
	if _, ok := svc.ContextLength("tencent", "deepseek-v4-flash"); ok {
		t.Error("without models.dev index, unknown provider should not match")
	}
}

func TestSetModelsDevIndex_Replace(t *testing.T) {
	svc := NewService()
	svc.SetModelsDevIndex(modelsdev.NewIndex(map[string]int{"a-model": 1000}))
	if v, ok := svc.ContextLength("p", "x/a-model"); !ok || v != 1000 {
		t.Fatalf("first index ctx = %d, ok=%v; want 1000", v, ok)
	}
	// 替换为新的索引，旧索引不再生效
	svc.SetModelsDevIndex(modelsdev.NewIndex(map[string]int{"b-model": 2000}))
	if _, ok := svc.ContextLength("p", "x/a-model"); ok {
		t.Error("replaced index should not match a-model")
	}
	if v, ok := svc.ContextLength("p", "x/b-model"); !ok || v != 2000 {
		t.Errorf("new index ctx = %d, ok=%v; want 2000", v, ok)
	}
}
