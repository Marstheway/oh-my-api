package config

import (
	"strings"
	"testing"
)

func TestCatalogPath_UsesDatabaseDir(t *testing.T) {
	cfg := &Config{Database: DatabaseConfig{Path: "/tmp/oh-my-api/stats.db"}}
	got := CatalogPath(cfg)
	want := "/tmp/oh-my-api/catalog_upstream.json"
	if got != want {
		t.Fatalf("CatalogPath() = %q, want %q", got, want)
	}
}

func TestCatalogPath_DefaultWhenNilConfig(t *testing.T) {
	got := CatalogPath(nil)
	// 路径分隔符在不同 OS 不同，只检查文件名后缀
	if !strings.HasSuffix(got, "catalog_upstream.json") {
		t.Fatalf("CatalogPath(nil) = %q, expected to end with catalog_upstream.json", got)
	}
}

func TestCatalogPath_DefaultWhenEmptyDatabasePath(t *testing.T) {
	cfg := &Config{}
	got := CatalogPath(cfg)
	if !strings.HasSuffix(got, "catalog_upstream.json") {
		t.Fatalf("CatalogPath(&Config{}) = %q, expected to end with catalog_upstream.json", got)
	}
}
