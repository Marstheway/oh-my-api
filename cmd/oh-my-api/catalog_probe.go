package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Marstheway/oh-my-api/internal/adaptor"
	"github.com/Marstheway/oh-my-api/internal/catalog"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/modelsdev"
	"github.com/Marstheway/oh-my-api/internal/provider"
)

func resolveCatalogPath(cfg *config.Config) string {
	return config.CatalogPath(cfg)
}

// modelsDevAPIURL 是 models.dev 社区目录的拉取地址，测试可替换。
var modelsDevAPIURL = modelsdev.DefaultURL

// modelsDevHTTPClient 拉取 models.dev 使用独立 client（不经 provider client 的认证逻辑）。
var modelsDevHTTPClient = &http.Client{}

// activeCatalogCfg 保存当前生效的配置。Apply 时更新，catalog 刷新循环每次取最新的 provider 列表。
var activeCatalogCfg atomic.Pointer[config.Config]

// activeCatalogClient 保存当前生效的 provider client。Apply 时更新，与 config 保持同步。
var activeCatalogClient atomic.Pointer[provider.Client]

// initCatalogService 创建 catalog service，加载已有快照文件，启动后台定时刷新。
func initCatalogService(cfg *config.Config, client *provider.Client, timeout time.Duration) *catalog.Service {
	svc := catalog.NewService()
	path := resolveCatalogPath(cfg)

	activeCatalogCfg.Store(cfg)
	activeCatalogClient.Store(client)

	if err := svc.LoadFromFile(path); err != nil {
		slog.Warn("failed to load catalog snapshot, will refresh in background", "path", path, "error", err)
	}

	// 加载 models.dev 紧凑索引（本地 tsv，无网络），供 context_length 查询回退。
	modelsDevPath := config.ModelsDevPath(cfg)
	if idx, err := modelsdev.LoadFromFile(modelsDevPath); err != nil {
		slog.Warn("failed to load models.dev index, will refresh in background", "path", modelsDevPath, "error", err)
	} else {
		svc.SetModelsDevIndex(idx)
		slog.Info("models.dev index loaded", "path", modelsDevPath, "models", idx.Len())
	}

	slog.Info("catalog service initialized",
		"generated_at", svc.GeneratedAt(),
		"has_any_success", svc.HasAnySuccess())

	go runCatalogRefreshLoop(svc, timeout, path, modelsDevPath)

	return svc
}

// updateCatalogConfig 更新 catalog 刷新循环使用的配置（Apply 时调用）。
func updateCatalogConfig(newCfg *config.Config) {
	activeCatalogCfg.Store(newCfg)
}

// updateCatalogClient 更新 catalog 刷新循环使用的 provider client（Apply 时调用）。
func updateCatalogClient(newClient *provider.Client) {
	activeCatalogClient.Store(newClient)
}

// runCatalogRefreshLoop 启动后立即首轮刷新，之后每 DefaultTTL 刷新一次。
// cfg 和 client 从 activeCatalogCfg / activeCatalogClient 动态读取，Apply 后下次循环自动生效。
// 每轮并行执行 provider 探测与 models.dev 拉取：二者互不影响；任一侧失败不阻塞另一侧落盘/换内存。
func runCatalogRefreshLoop(svc *catalog.Service, timeout time.Duration, path string, modelsDevPath string) {
	for {
		cfg := activeCatalogCfg.Load()
		client := activeCatalogClient.Load()

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := doCatalogRefresh(svc, cfg, client, timeout); err != nil {
				slog.Warn("catalog refresh failed", "error", err)
			}
			if err := svc.WriteToFile(path); err != nil {
				slog.Warn("failed to write catalog snapshot", "path", path, "error", err)
			}
			// provider catalog 已成功替换：通知 Cascade Spoke publisher 重算快照。
			cascadeMetadataNotify()
		}()
		go func() {
			defer wg.Done()
			refreshModelsDev(svc, modelsDevPath, timeout)
		}()
		wg.Wait()

		time.Sleep(catalog.DefaultTTL)
	}
}

// refreshModelsDev 拉取 models.dev 社区目录，构建紧凑索引并落盘、替换内存。
// 失败仅告警并保留上一份索引；与 provider 探测并行执行。
func refreshModelsDev(svc *catalog.Service, path string, timeout time.Duration) {
	idx, err := modelsdev.Fetch(modelsDevHTTPClient, modelsDevAPIURL, timeout)
	if err != nil {
		slog.Warn("models.dev fetch failed, keeping previous index", "error", err)
		return
	}
	if err := idx.WriteToFile(path); err != nil {
		slog.Warn("failed to write models.dev index", "path", path, "error", err)
	}
	svc.SetModelsDevIndex(idx)
	slog.Info("models.dev index refreshed", "models", idx.Len(), "path", path)
	// models.dev 索引已成功替换：通知 Cascade Spoke publisher 重算快照。
	cascadeMetadataNotify()
}

// doCatalogRefresh 执行整轮 catalog 探测，按 provider 合并结果并替换内存快照。
func doCatalogRefresh(svc *catalog.Service, cfg *config.Config, client *provider.Client, timeout time.Duration) error {
	slog.Info("catalog refresh started", "providers", len(cfg.Providers.Items))

	fresh := make([]catalog.ProviderEntry, 0, len(cfg.Providers.Items))
	for providerName, providerCfg := range cfg.Providers.Items {
		entry := probeSingleProvider(client, providerName, providerCfg, timeout)
		fresh = append(fresh, entry)
	}

	prev := svc.Snapshot()
	merged := catalog.MergeWithPrevious(prev, fresh)
	svc.ReplaceSnapshot(merged)

	successCount := 0
	for _, e := range merged.Providers {
		if e.Error == "" && e.LastSuccessAt != "" {
			successCount++
		}
	}
	slog.Info("catalog refresh completed",
		"total_providers", len(cfg.Providers.Items),
		"success_entries", successCount,
		"generated_at", merged.GeneratedAt)

	return nil
}

// probeSingleProvider 探测单个 provider 的 model 列表。
func probeSingleProvider(client *provider.Client, providerName string, providerCfg config.ProviderConfig, timeout time.Duration) catalog.ProviderEntry {
	if !providerCfg.HTTPDialable() {
		return catalog.ProviderEntry{
			Provider: providerName,
			Error:    "cascade provider skipped",
		}
	}

	protocol, endpoint := pickProbeTarget(providerCfg)
	protocolNorm := adaptor.Protocol(strings.ToLower(strings.TrimSpace(protocol)))

	// 统一复用 adaptor.BuildCatalogURL 构造探测 URL
	probeURL := adaptor.BuildCatalogURL(endpoint, protocolNorm)

	entry := catalog.ProviderEntry{
		Provider: providerName,
		Protocol: string(protocolNorm),
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
	if protocolNorm == adaptor.ProtocolAnthropic {
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

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		entry.Body = string(data)
		entry.LastSuccessAt = time.Now().UTC().Format(time.RFC3339)
	} else {
		// 非 2xx 视为失败，不存 body
		entry.Error = "non-2xx response"
	}

	return entry
}

// pickProbeTarget 选择探测目标协议和 endpoint。
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
