package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"log/slog"
)

type Resolver struct {
	modelGroups map[string]*ModelGroup
	providers   map[string]config.ProviderConfig
}

type ModelGroup struct {
	Mode    string
	Timeout time.Duration
	Tasks   []scheduler.Task
	Visible bool
}

type ResolveResult struct {
	Mode       string
	ModelGroup string
	Timeout    time.Duration
	Tasks      []scheduler.Task
}

func NewResolver(cfg *config.Config) (*Resolver, error) {
	mg := make(map[string]*ModelGroup, len(cfg.ModelGroups))
	for _, g := range cfg.ModelGroups {
		tasks := make([]scheduler.Task, 0, len(g.Models))
		for _, entry := range g.Models {
			parts := strings.SplitN(entry.Model, "/", 2)
			if len(parts) != 2 {
				slog.Warn("invalid model entry format, skipping", "model", entry.Model, "group", g.Name)
				continue
			}
			providerName, upstreamModel := parts[0], parts[1]

			provider, ok := cfg.Providers.Items[providerName]
			if !ok {
				slog.Warn("provider not found, skipping", "provider", providerName, "group", g.Name)
				continue
			}

			tasks = append(tasks, scheduler.Task{
				ProviderName:  providerName,
				Provider:      provider,
				UpstreamModel: upstreamModel,
				Weight:        entry.Weight,
				Priority:      entry.Priority,
			})
		}

		mode := g.Mode
		if mode == "" {
			// 1:1 模型映射默认使用 concurrent 模式，实现直通效果，避免健康检查干扰
			if len(tasks) == 1 {
				mode = "concurrent"
			} else {
				mode = "failover"
			}
		}

		timeout := 30 * time.Second
		if g.Timeout != "" {
			if d, err := time.ParseDuration(g.Timeout); err == nil {
				timeout = d
			}
		}

		visible := true
		if g.Visible != nil {
			visible = *g.Visible
		}

		mg[g.Name] = &ModelGroup{
			Mode:    mode,
			Timeout: timeout,
			Tasks:   tasks,
			Visible: visible,
		}
	}

	if err := validateAndApplyRedirect(mg, cfg.Redirect); err != nil {
		return nil, err
	}

	return &Resolver{
		modelGroups: mg,
		providers:   cfg.Providers.Items,
	}, nil
}

func validateAndApplyRedirect(mg map[string]*ModelGroup, redirect map[string]string) error {
	for alias, target := range redirect {
		if _, exists := mg[alias]; exists {
			return fmt.Errorf("redirect alias '%s' conflicts with existing model_group", alias)
		}

		finalTarget, err := resolveRedirectTarget(mg, redirect, target)
		if err != nil {
			return fmt.Errorf("redirect '%s': %w", alias, err)
		}

		group, ok := mg[finalTarget]
		if !ok {
			return fmt.Errorf("redirect '%s' target '%s' not found", alias, finalTarget)
		}

		mg[alias] = &ModelGroup{
			Mode:    group.Mode,
			Timeout: group.Timeout,
			Tasks:   group.Tasks,
			Visible: true,
		}
	}
	return nil
}

func resolveRedirectTarget(mg map[string]*ModelGroup, redirect map[string]string, target string) (string, error) {
	visited := make(map[string]bool)
	current := target

	for {
		if visited[current] {
			return "", fmt.Errorf("circular redirect detected: %s", current)
		}
		visited[current] = true

		if _, ok := mg[current]; ok {
			return current, nil
		}

		if next, ok := redirect[current]; ok {
			current = next
			continue
		}

		return "", fmt.Errorf("target '%s' not found", target)
	}
}

var (
	ErrModelNotFound    = fmt.Errorf("model not found")
	ErrNoValidProvider  = fmt.Errorf("no valid provider")
)

func (r *Resolver) Resolve(userModel string) (*ResolveResult, error) {
	group, ok := r.modelGroups[userModel]
	if !ok {
		return nil, ErrModelNotFound
	}

	if !group.Visible {
		return nil, ErrModelNotFound
	}

	if len(group.Tasks) == 0 {
		return nil, ErrNoValidProvider
	}

	return &ResolveResult{
		Mode:       group.Mode,
		ModelGroup: userModel,
		Timeout:    group.Timeout,
		Tasks:      group.Tasks,
	}, nil
}

func (r *Resolver) ListUserModels() []string {
	models := make([]string, 0, len(r.modelGroups))
	for name, group := range r.modelGroups {
		if group.Visible {
			models = append(models, name)
		}
	}
	return models
}
