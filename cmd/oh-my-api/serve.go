package main

import (
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/handler"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/Marstheway/oh-my-api/internal/server"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/Marstheway/oh-my-api/internal/token"
)

func runServe(configPath string) {
	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	runServeWithExit(cfg, configPath, os.Exit)
}

func runServeWithExit(cfg *config.Config, configPath string, exitFn func(int)) {
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = ":18000"
	}

	// 默认 120 秒
	timeout := 120 * time.Second
	if cfg.Server.Timeout != "" {
		if d, err := time.ParseDuration(cfg.Server.Timeout); err == nil {
			timeout = d
		}
	}

	// connect_timeout 默认 10s
	connectTimeout := 10 * time.Second
	if cfg.Server.ConnectTimeout != "" {
		if d, err := time.ParseDuration(cfg.Server.ConnectTimeout); err == nil {
			connectTimeout = d
		} else {
			slog.Warn("connect_timeout parse failed, fallback to default",
				"error", err,
				"connect_timeout", cfg.Server.ConnectTimeout,
				"default", "10s")
		}
	}

	// prefill_timeout 默认 30s
	prefillTimeout := 30 * time.Second
	if cfg.Server.PrefillTimeout != "" {
		if d, err := time.ParseDuration(cfg.Server.PrefillTimeout); err == nil {
			prefillTimeout = d
		} else {
			slog.Warn("prefill_timeout parse failed, fallback to default",
				"error", err,
				"prefill_timeout", cfg.Server.PrefillTimeout,
				"default", "30s")
		}
	}

	// stream_idle_timeout 默认 60s
	streamIdleTimeout := 60 * time.Second
	if cfg.Server.StreamIdleTimeout != "" {
		if d, err := time.ParseDuration(cfg.Server.StreamIdleTimeout); err == nil {
			streamIdleTimeout = d
		} else {
			slog.Warn("stream_idle_timeout parse failed, fallback to default",
				"error", err,
				"stream_idle_timeout", cfg.Server.StreamIdleTimeout,
				"default", "60s")
		}
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.Server.LogLevel),
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})))

	slog.Info("config loaded",
		"providers", len(cfg.Providers.Items),
		"model_groups", len(cfg.ModelGroups),
		"timeout", timeout.String(),
		"connect_timeout", connectTimeout.String(),
		"prefill_timeout", prefillTimeout.String(),
		"stream_idle_timeout", streamIdleTimeout.String())

	// 启动时将配置文件中的协议简写（"openai" → "openai.chat" 等）持久化写回。
	// 仅用于配置文件的"升级"，不影响运行时正确性：config.Load 已在内存中完成规范化。
	// 写回失败时仅记录警告，cfg 仍持有规范化后的值，后续流程不受影响。
	if store, err := runtimeconfig.NewYamlStore(configPath); err == nil {
		if store.NormalizeProviderProtocols(config.ResolveProtocolAlias) {
			if err := store.Save(); err != nil {
				slog.Warn("failed to write normalized protocols to config file", "error", err)
			} else {
				slog.Info("normalized protocol aliases in config file")
				if reloadedCfg, reloadErr := config.Load(configPath); reloadErr == nil {
					cfg = reloadedCfg
				}
			}
		}
	}

	warnings, err := config.ValidateForServe(cfg)
	if err != nil {
		slog.Error("config validation failed", "error", err)
		exitFn(1)
		return
	}
	for _, w := range warnings {
		slog.Warn("config validation warning", "path", w.Path, "message", w.Message)
	}

	if configPath == "" {
		slog.Error("failed to init token estimator", "error", "config path is required")
		exitFn(1)
		return
	}

	if err := token.Init(); err != nil {
		slog.Error("failed to init token estimator", "error", err)
		exitFn(1)
		return
	}

	dbPath := cfg.Database.Path
	if dbPath == "" {
		dbPath = "./data/oh-my-api.db"
	}
	if err := stats.Init(dbPath); err != nil {
		slog.Error("failed to init stats database", "error", err)
		exitFn(1)
		return
	}
	defer stats.Close()

	// 初始化 metrics
	metricsHandler := metrics.Init()

	resolver, err := model.NewResolver(cfg)
	if err != nil {
		slog.Error("failed to create resolver", "error", err)
		exitFn(1)
		return
	}

	client := provider.NewClient(cfg.Providers.Items, timeout, connectTimeout)
	startUpstreamCatalogProbe(cfg, client, timeout)
	rlManager := ratelimit.NewManager(cfg.Providers.Items)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched := scheduler.New(rlManager, client, healthChecker, prefillTimeout, streamIdleTimeout)
	handler.Init(cfg, resolver, sched)

	// 创建 runtimeconfig manager
	rebuilder := &runtimeconfig.DefaultRuntimeRebuilder{
		NewResolver: func(c *config.Config) (*model.Resolver, error) {
			return model.NewResolver(c)
		},
		NewScheduler: func(c *config.Config, r *model.Resolver) (*scheduler.Scheduler, error) {
			return scheduler.New(rlManager, client, healthChecker, prefillTimeout, streamIdleTimeout), nil
		},
	}
	reinitHandler := &runtimeconfig.DefaultReinitHandler{
		InitFunc: handler.Init,
	}
	runtimeManager, err := runtimeconfig.NewManager(cfg, configPath, rebuilder, reinitHandler)
	if err != nil {
		slog.Error("failed to create runtimeconfig manager", "error", err)
		exitFn(1)
		return
	}

	if err := server.Run(cfg, metricsHandler, runtimeManager); err != nil {
		slog.Error("server error", "error", err)
		exitFn(1)
		return
	}
}

func parseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
