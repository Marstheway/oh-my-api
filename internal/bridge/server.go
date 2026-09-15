package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// NewBridgeMux 构造 bridge 的路由 mux，作为 Run 与路由测试共用的入口。
// 精确匹配方法 + 路径，避免错误方法进入 handler。
func NewBridgeMux(h *BridgeHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/responses", h.HandleResponses)
	mux.HandleFunc("POST /v1/chat/completions", h.HandleChatCompletions)
	mux.HandleFunc("GET /v1/models", h.HandleModels)
	mux.HandleFunc("GET /healthz", h.HandleHealthz)
	return mux
}

// Run 启动 bridge HTTP server 并阻塞直到收到终止信号。
func Run(cfg *Config) error {
	handler := NewBridgeHandler(cfg.BridgeToken)
	handler.allowUnauthenticatedLoopback = cfg.AllowUnauthenticatedLoopback

	mux := NewBridgeMux(handler)

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: mux,
	}

	errCh := make(chan error, 1)

	go func() {
		slog.Info("bridge server starting", "listen", cfg.Listen)
		slog.Info("bridge provider types", "supported", SupportedProviderTypes())
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
		slog.Info("bridge shutting down", "signal", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			return fmt.Errorf("bridge server shutdown: %w", err)
		}
	}

	return nil
}
