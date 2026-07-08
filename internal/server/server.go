package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/router"
	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/gin-gonic/gin"
)

func Run(cfg *config.Config, metricsHandler http.Handler, runtimeManager *runtimeconfig.Manager) error {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	router.Setup(r, runtimeManager)
	router.SetupAdmin(r, cfg, runtimeManager, stats.GetQuerier())

	srv := &http.Server{
		Addr:    cfg.Server.Listen,
		Handler: r,
	}

	errCh := make(chan error, 1)

	// 启动 metrics server（如果配置了）
	var metricsSrv *http.Server
	if cfg.Server.MetricsListen != "" {
		metricsSrv = &http.Server{
			Addr:    cfg.Server.MetricsListen,
			Handler: metricsHandler,
		}
		go func() {
			slog.Info("metrics server starting", "listen", cfg.Server.MetricsListen)
			if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				errCh <- err
			}
		}()
	}

	go func() {
		for name, p := range cfg.Providers.Items {
			slog.Info("provider", "name", name, "protocol", providerProtocols(p))
		}
		for _, mg := range cfg.ModelGroups {
			slog.Info("model_group", "name", mg.Name, "mode", mg.Mode, "models", mg.Models)
		}
		slog.Info("server starting", "listen", cfg.Server.Listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		slog.Info("shutting down server", "signal", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			return fmt.Errorf("server shutdown: %w", err)
		}
		if metricsSrv != nil {
			if err := metricsSrv.Shutdown(ctx); err != nil {
				return fmt.Errorf("metrics server shutdown: %w", err)
			}
		}
	}

	return nil
}

func providerProtocols(p config.ProviderConfig) string {
	if len(p.Protocols) > 0 {
		return strings.Join(p.Protocols, ", ")
	}

	seen := make(map[string]struct{})
	protocols := make([]string, 0)
	for _, ep := range p.Endpoints {
		for _, proto := range ep.Protocols {
			protocol := strings.TrimSpace(proto)
			if protocol == "" {
				continue
			}
			if _, ok := seen[protocol]; ok {
				continue
			}
			seen[protocol] = struct{}{}
			protocols = append(protocols, protocol)
		}
	}

	if len(protocols) == 0 {
		return "unknown"
	}

	return strings.Join(protocols, "/")
}
