package handler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func writeCatalogFile(t *testing.T, dir string, snapshot interface{}) string {
	t.Helper()
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	path := filepath.Join(dir, "catalog_upstream.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	return path
}

func TestModelMetadata_LoadCatalogContextIndex_Basic(t *testing.T) {
	tmp := t.TempDir()
	snapshot := map[string]interface{}{
		"generated_at": "2026-06-10T00:00:00Z",
		"providers": []map[string]interface{}{
			{
				"provider": "openai",
				"body": `{"data":[
					{"id":"gpt-4o","context_length":128000},
					{"id":"gpt-4","context_length":8192}
				]}`,
			},
		},
	}
	dbPath := filepath.Join(tmp, "stats.db")
	writeCatalogFile(t, tmp, snapshot)

	cfg := &config.Config{Database: config.DatabaseConfig{Path: dbPath}}
	idx := LoadCatalogContextIndex(cfg)

	if cl, ok := idx.LookupContextLength("openai", "gpt-4o"); !ok || cl != 128000 {
		t.Errorf("gpt-4o context_length = %d, ok=%v; want 128000", cl, ok)
	}
	if cl, ok := idx.LookupContextLength("openai", "gpt-4"); !ok || cl != 8192 {
		t.Errorf("gpt-4 context_length = %d, ok=%v; want 8192", cl, ok)
	}
}

func TestModelMetadata_IgnoresRecordWithoutID(t *testing.T) {
	tmp := t.TempDir()
	snapshot := map[string]interface{}{
		"providers": []map[string]interface{}{
			{
				"provider": "openai",
				"body":     `{"data":[{"context_length":128000}]}`,
			},
		},
	}
	writeCatalogFile(t, tmp, snapshot)

	cfg := &config.Config{Database: config.DatabaseConfig{Path: filepath.Join(tmp, "stats.db")}}
	idx := LoadCatalogContextIndex(cfg)

	if len(idx) != 0 {
		t.Errorf("expected empty index for record without id, got %d entries", len(idx))
	}
}

func TestModelMetadata_IgnoresRecordWithoutContextLength(t *testing.T) {
	tmp := t.TempDir()
	snapshot := map[string]interface{}{
		"providers": []map[string]interface{}{
			{
				"provider": "openai",
				"body":     `{"data":[{"id":"gpt-4o"}]}`,
			},
		},
	}
	writeCatalogFile(t, tmp, snapshot)

	cfg := &config.Config{Database: config.DatabaseConfig{Path: filepath.Join(tmp, "stats.db")}}
	idx := LoadCatalogContextIndex(cfg)

	if _, ok := idx.LookupContextLength("openai", "gpt-4o"); ok {
		t.Error("expected record without context_length to be ignored")
	}
}

func TestModelMetadata_FileMissing(t *testing.T) {
	cfg := &config.Config{Database: config.DatabaseConfig{Path: "/nonexistent/stats.db"}}
	idx := LoadCatalogContextIndex(cfg)

	if len(idx) != 0 {
		t.Errorf("expected empty index for missing file, got %d entries", len(idx))
	}
}

func TestModelMetadata_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "catalog_upstream.json")
	if err := os.WriteFile(path, []byte(`{invalid`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := &config.Config{Database: config.DatabaseConfig{Path: filepath.Join(tmp, "stats.db")}}
	idx := LoadCatalogContextIndex(cfg)

	if len(idx) != 0 {
		t.Errorf("expected empty index for invalid JSON, got %d entries", len(idx))
	}
}

func TestModelMetadata_InvalidBodyJSON(t *testing.T) {
	tmp := t.TempDir()
	snapshot := map[string]interface{}{
		"providers": []map[string]interface{}{
			{
				"provider": "openai",
				"body":     `not-json`,
			},
		},
	}
	writeCatalogFile(t, tmp, snapshot)

	cfg := &config.Config{Database: config.DatabaseConfig{Path: filepath.Join(tmp, "stats.db")}}
	idx := LoadCatalogContextIndex(cfg)

	if len(idx) != 0 {
		t.Errorf("expected empty index for invalid body JSON, got %d entries", len(idx))
	}
}

func TestModelMetadata_MultipleProviders(t *testing.T) {
	tmp := t.TempDir()
	snapshot := map[string]interface{}{
		"providers": []map[string]interface{}{
			{
				"provider": "openai",
				"body":     `{"data":[{"id":"gpt-4o","context_length":128000}]}`,
			},
			{
				"provider": "anthropic",
				"body":     `{"data":[{"id":"claude-3-5-sonnet-20241022","context_length":200000}]}`,
			},
		},
	}
	writeCatalogFile(t, tmp, snapshot)

	cfg := &config.Config{Database: config.DatabaseConfig{Path: filepath.Join(tmp, "stats.db")}}
	idx := LoadCatalogContextIndex(cfg)

	if cl, ok := idx.LookupContextLength("openai", "gpt-4o"); !ok || cl != 128000 {
		t.Errorf("openai/gpt-4o: got %d, want 128000", cl)
	}
	if cl, ok := idx.LookupContextLength("anthropic", "claude-3-5-sonnet-20241022"); !ok || cl != 200000 {
		t.Errorf("anthropic/claude: got %d, want 200000", cl)
	}
}

func TestModelMetadata_IgnoresRecordWithZeroContextLength(t *testing.T) {
	tmp := t.TempDir()
	snapshot := map[string]interface{}{
		"providers": []map[string]interface{}{
			{
				"provider": "openai",
				"body":     `{"data":[{"id":"gpt-4o","context_length":0}]}`,
			},
		},
	}
	writeCatalogFile(t, tmp, snapshot)

	cfg := &config.Config{Database: config.DatabaseConfig{Path: filepath.Join(tmp, "stats.db")}}
	idx := LoadCatalogContextIndex(cfg)

	if _, ok := idx.LookupContextLength("openai", "gpt-4o"); ok {
		t.Error("expected record with context_length=0 to be filtered out")
	}
}

func TestModelMetadata_BodyWithoutDataField(t *testing.T) {
	tmp := t.TempDir()
	// body 有 json 但没有 data 字段
	snapshot := map[string]interface{}{
		"providers": []map[string]interface{}{
			{
				"provider": "openai",
				"body":     `{"models":[{"id":"gpt-4o","context_length":128000}]}`,
			},
		},
	}
	writeCatalogFile(t, tmp, snapshot)

	cfg := &config.Config{Database: config.DatabaseConfig{Path: filepath.Join(tmp, "stats.db")}}
	idx := LoadCatalogContextIndex(cfg)

	// 没有 data 字段，索引应为空
	if len(idx) != 0 {
		t.Errorf("expected empty index for body without data field, got %d entries", len(idx))
	}
}
