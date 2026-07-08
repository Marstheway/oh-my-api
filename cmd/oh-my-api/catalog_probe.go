package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/provider"
)

type upstreamCatalogSnapshot struct {
	GeneratedAt string                 `json:"generated_at"`
	Providers   []upstreamCatalogEntry `json:"providers"`
}

type upstreamCatalogEntry struct {
	Provider   string `json:"provider"`
	Protocol   string `json:"protocol"`
	URL        string `json:"url"`
	StatusCode int    `json:"status_code,omitempty"`
	Body       string `json:"body,omitempty"`
	Error      string `json:"error,omitempty"`
}

func resolveCatalogPath(cfg *config.Config) string {
	return config.CatalogPath(cfg)
}

func probeUpstreamCatalog(cfg *config.Config, client *provider.Client, timeout time.Duration) error {
	path := resolveCatalogPath(cfg)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	snapshot := upstreamCatalogSnapshot{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Providers:   make([]upstreamCatalogEntry, 0, len(cfg.Providers.Items)),
	}

	for providerName, providerCfg := range cfg.Providers.Items {
		entry := probeSingleProvider(client, providerName, providerCfg, timeout)
		snapshot.Providers = append(snapshot.Providers, entry)
	}

	body, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, body, 0o644)
}

func startUpstreamCatalogProbe(cfg *config.Config, client *provider.Client, timeout time.Duration) {
	path := resolveCatalogPath(cfg)
	if _, err := os.Stat(path); err == nil {
		return
	} else if !os.IsNotExist(err) {
		slog.Warn("upstream catalog probe skipped", "path", path, "error", err)
		return
	}

	go func() {
		if err := probeUpstreamCatalog(cfg, client, timeout); err != nil {
			slog.Warn("upstream catalog probe failed", "error", err)
		}
	}()
}

func probeSingleProvider(client *provider.Client, providerName string, providerCfg config.ProviderConfig, timeout time.Duration) upstreamCatalogEntry {
	protocol, endpoint := pickProbeTarget(providerCfg)
	probeURL := buildCatalogProbeURL(endpoint, protocol)

	entry := upstreamCatalogEntry{
		Provider: providerName,
		Protocol: protocol,
		URL:      probeURL,
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	req.Header.Set("Accept", "application/json")
	if strings.EqualFold(protocol, "anthropic.messages") {
		req.Header.Set("x-api-key", providerCfg.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if providerCfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+providerCfg.APIKey)
	}

	resp, err := client.Do(providerName, req)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	defer resp.Body.Close()

	entry.StatusCode = resp.StatusCode
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	entry.Body = string(data)

	return entry
}

func pickProbeTarget(providerCfg config.ProviderConfig) (protocol, endpoint string) {
	if len(providerCfg.Endpoints) > 0 {
		ep := providerCfg.Endpoints[0]
		if len(ep.Protocols) > 0 {
			protocol = ep.Protocols[0]
		} else if len(providerCfg.Protocols) > 0 {
			protocol = providerCfg.Protocols[0]
		}
		endpoint = ep.URL
		return strings.TrimSpace(protocol), strings.TrimSpace(endpoint)
	}
	proto := ""
	if len(providerCfg.Protocols) > 0 {
		proto = providerCfg.Protocols[0]
	}
	return strings.TrimSpace(proto), strings.TrimSpace(providerCfg.Endpoint)
}

func buildCatalogProbeURL(endpoint, protocol string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}

	path := strings.TrimSuffix(u.Path, "/")
	p := strings.ToLower(strings.TrimSpace(protocol))

	switch p {
	case "anthropic.messages":
		if path == "" {
			u.Path = "/v1/models"
		} else if strings.HasSuffix(path, "/v1") {
			u.Path = path + "/models"
		} else if strings.HasSuffix(path, "/models") {
			u.Path = path
		} else {
			u.Path = path + "/v1/models"
		}
	case "ollama.chat", "ollama.embed", "ollama":
		if path == "" {
			u.Path = "/api/tags"
		} else if strings.HasSuffix(path, "/api/tags") {
			u.Path = path
		} else {
			u.Path = path + "/api/tags"
		}
	default:
		if path == "" {
			u.Path = "/v1/models"
		} else if strings.HasSuffix(path, "/models") {
			u.Path = path
		} else {
			u.Path = path + "/models"
		}
	}

	return u.String()
}
