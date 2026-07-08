package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/provider"
)

func TestResolveCatalogPath_UsesDatabaseDir(t *testing.T) {
	cfg := &config.Config{Database: config.DatabaseConfig{Path: "/tmp/oh-my-api/stats.db"}}
	got := resolveCatalogPath(cfg)
	want := "/tmp/oh-my-api/catalog_upstream.json"
	if got != want {
		t.Fatalf("resolveCatalogPath() = %q, want %q", got, want)
	}
}

func TestBuildCatalogProbeURL(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		protocol string
		want     string
	}{
		{name: "openai root", endpoint: "https://api.openai.com", protocol: "openai.chat", want: "https://api.openai.com/v1/models"},
		{name: "anthropic root", endpoint: "https://api.anthropic.com", protocol: "anthropic.messages", want: "https://api.anthropic.com/v1/models"},
		{name: "ollama root", endpoint: "http://127.0.0.1:11434", protocol: "ollama.chat", want: "http://127.0.0.1:11434/api/tags"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCatalogProbeURL(tt.endpoint, tt.protocol)
			if got != tt.want {
				t.Fatalf("buildCatalogProbeURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProbeUpstreamCatalog_WriteOnceAndSkipExisting(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "stats.db")
	catalogPath := filepath.Join(tmp, "catalog_upstream.json")

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %s, want /v1/models", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Fatalf("missing bearer auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		Database: config.DatabaseConfig{Path: dbPath},
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: srv.URL, APIKey: "sk-test", Protocols: []string{"openai.chat"}},
		}},
	}

	client := provider.NewClient(cfg.Providers.Items, 2*time.Second, 0)

	if err := probeUpstreamCatalog(cfg, client, 2*time.Second); err != nil {
		t.Fatalf("first probeUpstreamCatalog error: %v", err)
	}
	if hits != 1 {
		t.Fatalf("first probe hits = %d, want 1", hits)
	}

	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatalf("read catalog file error: %v", err)
	}

	var got upstreamCatalogSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal catalog file error: %v", err)
	}
	if len(got.Providers) != 1 {
		t.Fatalf("providers count = %d, want 1", len(got.Providers))
	}
	if got.Providers[0].StatusCode != http.StatusOK {
		t.Fatalf("status_code = %d, want %d", got.Providers[0].StatusCode, http.StatusOK)
	}
	if !strings.Contains(got.Providers[0].Body, "gpt-4o") {
		t.Fatalf("body = %q, want contains gpt-4o", got.Providers[0].Body)
	}

	if err := probeUpstreamCatalog(cfg, client, 2*time.Second); err != nil {
		t.Fatalf("second probeUpstreamCatalog error: %v", err)
	}
	if hits != 1 {
		t.Fatalf("second probe should skip existing file, hits = %d, want 1", hits)
	}
}

func TestStartUpstreamCatalogProbe_DoesNotBlockStartup(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "stats.db")

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		Database: config.DatabaseConfig{Path: dbPath},
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: srv.URL, APIKey: "sk-test", Protocols: []string{"openai.chat"}},
		}},
	}

	client := provider.NewClient(cfg.Providers.Items, 2*time.Second, 0)
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	defer slog.SetDefault(oldLogger)

	start := time.Now()
	startUpstreamCatalogProbe(cfg, client, 2*time.Second)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		close(release)
		t.Fatalf("startUpstreamCatalogProbe blocked for %v", elapsed)
	}

	select {
	case <-started:
	case <-time.After(500 * time.Millisecond):
		close(release)
		t.Fatal("background probe did not start")
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(tmp, "catalog_upstream.json")); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("catalog file was not written by background probe")
}

func TestStartUpstreamCatalogProbe_SkipsWhenCatalogExists(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "stats.db")
	catalogPath := filepath.Join(tmp, "catalog_upstream.json")
	if err := os.WriteFile(catalogPath, []byte(`{"generated_at":"2026-06-10T00:00:00Z","providers":[]}`), 0o644); err != nil {
		t.Fatalf("write existing catalog file error: %v", err)
	}

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Database: config.DatabaseConfig{Path: dbPath},
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: srv.URL, APIKey: "sk-test", Protocols: []string{"openai.chat"}},
		}},
	}

	client := provider.NewClient(cfg.Providers.Items, 2*time.Second, 0)
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	defer slog.SetDefault(oldLogger)

	startUpstreamCatalogProbe(cfg, client, 2*time.Second)
	time.Sleep(100 * time.Millisecond)

	if hits != 0 {
		t.Fatalf("expected existing catalog to skip probe, hits = %d", hits)
	}

	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatalf("read existing catalog file error: %v", err)
	}
	if !strings.Contains(string(data), `"providers":[]`) {
		t.Fatalf("existing catalog file was unexpectedly changed: %s", string(data))
	}
}
