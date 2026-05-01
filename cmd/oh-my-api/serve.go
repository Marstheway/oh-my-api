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

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.Server.LogLevel),
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})))

	slog.Info("config loaded", "providers", len(cfg.Providers.Items), "model_groups", len(cfg.ModelGroups))

	if err := token.Init(); err != nil {
		slog.Error("failed to init token estimator", "error", err)
		os.Exit(1)
	}

	dbPath := cfg.Database.Path
	if dbPath == "" {
		dbPath = "./data/oh-my-api.db"
	}
	if err := stats.Init(dbPath); err != nil {
		slog.Error("failed to init stats database", "error", err)
		os.Exit(1)
	}
	defer stats.Close()

	// 初始化 metrics
	metricsHandler := metrics.Init()

	resolver, err := model.NewResolver(cfg)
	if err != nil {
		slog.Error("failed to create resolver", "error", err)
		os.Exit(1)
	}

	client := provider.NewClient(cfg.Providers.Items, cfg.Providers.Timeout)
	rlManager := ratelimit.NewManager(cfg.Providers.Items)
	healthChecker := health.NewChecker(3, 30*time.Second)
	sched := scheduler.New(rlManager, client, healthChecker)
	handler.Init(cfg, resolver, sched)

	if err := server.Run(cfg, metricsHandler); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
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
