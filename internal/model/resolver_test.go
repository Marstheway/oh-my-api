package model

import (
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func TestResolver_Resolve(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {
				Endpoint: "https://api.openai.com/v1",
				APIKey:   "sk-xxx",
				Protocol: "openai",
			},
			"anthropic": {
				Endpoint: "https://api.anthropic.com",
				APIKey:   "sk-ant-xxx",
				Protocol: "anthropic",
			},
			"openrouter": {
				Endpoint: "https://openrouter.ai/api/v1",
				APIKey:   "sk-or-xxx",
				Protocol: "openai",
			},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "claude-fast", Models: config.ModelEntries{{Model: "anthropic/claude-sonnet-4-20250514", Weight: 1}}},
			{Name: "multi", Models: config.ModelEntries{
				{Model: "openai/gpt-4o", Weight: 1},
				{Model: "anthropic/claude-3-5-sonnet", Weight: 1},
			}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tests := []struct {
		name         string
		userModel    string
		wantErr      bool
		wantMode     string
		wantTasks    int
	}{
		{
			name:      "resolve openai model",
			userModel: "gpt-4",
			wantErr:   false,
			wantMode:  "concurrent", // 1:1 映射默认使用 concurrent 模式
			wantTasks: 1,
		},
		{
			name:      "resolve anthropic model",
			userModel: "claude-fast",
			wantErr:   false,
			wantMode:  "concurrent", // 1:1 映射默认使用 concurrent 模式
			wantTasks: 1,
		},
		{
			name:      "resolve multi models",
			userModel: "multi",
			wantErr:   false,
			wantMode:  "failover",
			wantTasks: 2,
		},
		{
			name:      "model not found",
			userModel: "unknown-model",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := r.Resolve(tt.userModel)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result.Mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", result.Mode, tt.wantMode)
			}
			if len(result.Tasks) != tt.wantTasks {
				t.Errorf("tasks count = %d, want %d", len(result.Tasks), tt.wantTasks)
			}
		})
	}
}

func TestResolver_ResolveWithMode(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai":    {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocol: "openai"},
			"anthropic": {Endpoint: "https://api.anthropic.com", APIKey: "sk-xxx", Protocol: "anthropic"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:    "fast",
				Mode:    "concurrent",
				Timeout: "60s",
				Models: config.ModelEntries{
					{Model: "openai/gpt-4o", Weight: 1},
					{Model: "anthropic/claude-3-5-sonnet", Weight: 1},
				},
			},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result, err := r.Resolve("fast")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Mode != "concurrent" {
		t.Errorf("mode = %q, want %q", result.Mode, "concurrent")
	}
	if result.Timeout.Seconds() != 60 {
		t.Errorf("timeout = %v, want 60s", result.Timeout)
	}
	if len(result.Tasks) != 2 {
		t.Errorf("tasks count = %d, want 2", len(result.Tasks))
	}
}

func TestResolver_ListUserModels(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "gpt-4o", Models: config.ModelEntries{{Model: "openai/gpt-4o", Weight: 1}}},
			{Name: "o3-mini", Models: config.ModelEntries{{Model: "openai/o3-mini", Weight: 1}}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	models := r.ListUserModels()

	if len(models) != 3 {
		t.Errorf("expected 3 models, got %d", len(models))
	}

	modelSet := make(map[string]bool)
	for _, m := range models {
		modelSet[m] = true
	}

	for _, expected := range []string{"gpt-4", "gpt-4o", "o3-mini"} {
		if !modelSet[expected] {
			t.Errorf("expected model %q not found in list", expected)
		}
	}
}

func TestResolver_EmptyModelGroups(t *testing.T) {
	cfg := &config.Config{
		Providers:   config.ProvidersConfig{Items: map[string]config.ProviderConfig{}},
		ModelGroups: []config.ModelGroupConfig{},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = r.Resolve("any-model")
	if err == nil {
		t.Error("expected error for unknown model")
	}
}

func TestResolver_Redirect(t *testing.T) {
	falseVal := false
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai":    {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocol: "openai"},
			"anthropic": {Endpoint: "https://api.anthropic.com", APIKey: "sk-xxx", Protocol: "anthropic"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:    "claude-fast",
				Visible: &falseVal,
				Models: config.ModelEntries{
					{Model: "anthropic/claude-sonnet-4-20250514", Weight: 1},
				},
			},
		},
		Redirect: map[string]string{
			"claude-4-6-20261201": "claude-fast",
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 通过别名调用应成功
	result, err := r.Resolve("claude-4-6-20261201")
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if len(result.Tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(result.Tasks))
	}

	// 直接调用不可见模型应失败
	_, err = r.Resolve("claude-fast")
	if err == nil {
		t.Error("expected error for invisible model")
	}

	// 不可见模型不应出现在列表中
	models := r.ListUserModels()
	for _, m := range models {
		if m == "claude-fast" {
			t.Error("invisible model should not appear in list")
		}
	}
}

func TestResolver_RedirectTargetNotFound(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: map[string]string{
			"alias": "non-existent-model",
		},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for redirect target not found")
	}
}

func TestResolver_RedirectAliasConflicts(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: map[string]string{
			"gpt-4": "gpt-4", // 别名与现有 model_group 重名
		},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for redirect alias conflicts")
	}
}

func TestResolver_RedirectCircular(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "model-a", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: map[string]string{
			"alias-a": "alias-b",
			"alias-b": "alias-a",
		},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for circular redirect")
	}
}

func TestResolver_Visible(t *testing.T) {
	falseVal := false
	trueVal := true
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "visible-model", Visible: &trueVal, Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "hidden-model", Visible: &falseVal, Models: config.ModelEntries{{Model: "openai/gpt-4o", Weight: 1}}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	models := r.ListUserModels()
	if len(models) != 1 {
		t.Errorf("expected 1 model, got %d", len(models))
	}
	if models[0] != "visible-model" {
		t.Errorf("expected visible-model, got %s", models[0])
	}

	// 可见模型可以调用
	_, err = r.Resolve("visible-model")
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// 不可见模型不能直接调用
	_, err = r.Resolve("hidden-model")
	if err == nil {
		t.Error("expected error for hidden model")
	}
}

// ========== 更多 Redirect 测试 ==========

func TestResolver_RedirectChained(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: map[string]string{
			"alias-a": "alias-b",
			"alias-b": "alias-c",
			"alias-c": "gpt-4",
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 链式重定向最终解析到 gpt-4
	result, err := r.Resolve("alias-a")
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if len(result.Tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(result.Tasks))
	}
	if result.Tasks[0].UpstreamModel != "gpt-4" {
		t.Errorf("expected upstream model gpt-4, got %s", result.Tasks[0].UpstreamModel)
	}

	// 所有别名都应该在模型列表中
	models := r.ListUserModels()
	modelSet := make(map[string]bool)
	for _, m := range models {
		modelSet[m] = true
	}
	for _, expected := range []string{"gpt-4", "alias-a", "alias-b", "alias-c"} {
		if !modelSet[expected] {
			t.Errorf("expected model %q in list", expected)
		}
	}
}

func TestResolver_RedirectToVisibleFalse(t *testing.T) {
	falseVal := false
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "hidden-backend", Visible: &falseVal, Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: map[string]string{
			"user-friendly-name": "hidden-backend",
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 别名可以调用
	result, err := r.Resolve("user-friendly-name")
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if len(result.Tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(result.Tasks))
	}

	// 原模型不可直接调用
	_, err = r.Resolve("hidden-backend")
	if err == nil {
		t.Error("expected error for calling hidden model directly")
	}

	// 只有别名在列表中，原模型不在
	models := r.ListUserModels()
	foundAlias := false
	foundBackend := false
	for _, m := range models {
		if m == "user-friendly-name" {
			foundAlias = true
		}
		if m == "hidden-backend" {
			foundBackend = true
		}
	}
	if !foundAlias {
		t.Error("expected alias in model list")
	}
	if foundBackend {
		t.Error("hidden backend should not appear in model list")
	}
}

func TestResolver_RedirectMultipleAliases(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: map[string]string{
			"alias-1": "gpt-4",
			"alias-2": "gpt-4",
			"alias-3": "gpt-4",
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 所有别名都可以调用
	for _, alias := range []string{"alias-1", "alias-2", "alias-3"} {
		result, err := r.Resolve(alias)
		if err != nil {
			t.Errorf("expected no error for %s, got: %v", alias, err)
		}
		if len(result.Tasks) != 1 {
			t.Errorf("expected 1 task for %s, got %d", alias, len(result.Tasks))
		}
	}

	// 所有别名和原模型都在列表中
	models := r.ListUserModels()
	if len(models) != 4 {
		t.Errorf("expected 4 models, got %d", len(models))
	}
}

func TestResolver_RedirectEmpty(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: map[string]string{}, // 空 redirect
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 正常调用
	_, err = r.Resolve("gpt-4")
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// 模型列表正常
	models := r.ListUserModels()
	if len(models) != 1 || models[0] != "gpt-4" {
		t.Errorf("expected [gpt-4], got %v", models)
	}
}

func TestResolver_RedirectNil(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: nil, // nil redirect
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 正常调用
	_, err = r.Resolve("gpt-4")
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

// ========== 更多 Visible 测试 ==========

func TestResolver_VisibleDefault(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "default-model", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}}, // 不设置 Visible，默认 true
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	models := r.ListUserModels()
	if len(models) != 1 {
		t.Errorf("expected 1 model, got %d", len(models))
	}

	// 默认可见，可以调用
	_, err = r.Resolve("default-model")
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestResolver_VisibleAllHidden(t *testing.T) {
	falseVal := false
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "hidden-1", Visible: &falseVal, Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "hidden-2", Visible: &falseVal, Models: config.ModelEntries{{Model: "openai/gpt-4o", Weight: 1}}},
		},
		Redirect: map[string]string{
			"visible-alias": "hidden-1",
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	models := r.ListUserModels()
	if len(models) != 1 || models[0] != "visible-alias" {
		t.Errorf("expected only [visible-alias], got %v", models)
	}
}

func TestResolver_VisibleMixedModels(t *testing.T) {
	falseVal := false
	trueVal := true
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocol: "openai"},
			"anthropic": {Protocol: "anthropic"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "public-1", Visible: &trueVal, Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "public-2", Visible: &trueVal, Models: config.ModelEntries{{Model: "openai/gpt-4o", Weight: 1}}},
			{Name: "private-1", Visible: &falseVal, Models: config.ModelEntries{{Model: "anthropic/claude-3", Weight: 1}}},
			{Name: "private-2", Visible: &falseVal, Models: config.ModelEntries{{Model: "anthropic/claude-4", Weight: 1}}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	models := r.ListUserModels()
	if len(models) != 2 {
		t.Errorf("expected 2 visible models, got %d", len(models))
	}

	// 公开模型可调用
	for _, m := range []string{"public-1", "public-2"} {
		_, err := r.Resolve(m)
		if err != nil {
			t.Errorf("expected %s to be resolvable, got: %v", m, err)
		}
	}

	// 私有模型不可直接调用
	for _, m := range []string{"private-1", "private-2"} {
		_, err := r.Resolve(m)
		if err == nil {
			t.Errorf("expected %s to not be resolvable", m)
		}
	}
}

// ========== Redirect 与 Visible 交互测试 ==========

func TestResolver_RedirectAliasAlwaysVisible(t *testing.T) {
	falseVal := false
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "backend", Visible: &falseVal, Mode: "concurrent", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: map[string]string{
			"frontend": "backend",
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 别名可调用
	result, err := r.Resolve("frontend")
	if err != nil {
		t.Errorf("expected alias to be resolvable, got: %v", err)
	}

	// 别名的配置继承自目标
	if result.Mode != "concurrent" {
		t.Errorf("expected mode concurrent, got %s", result.Mode)
	}

	// 别名在列表中
	models := r.ListUserModels()
	if len(models) != 1 || models[0] != "frontend" {
		t.Errorf("expected [frontend], got %v", models)
	}
}

func TestResolver_RedirectPreservesTimeoutAndMode(t *testing.T) {
	falseVal := false
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:    "backend",
				Visible: &falseVal,
				Mode:    "load-balance",
				Timeout: "120s",
				Models: config.ModelEntries{
					{Model: "openai/gpt-4", Weight: 2},
					{Model: "openai/gpt-4o", Weight: 1},
				},
			},
		},
		Redirect: map[string]string{
			"frontend": "backend",
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := r.Resolve("frontend")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if result.Mode != "load-balance" {
		t.Errorf("expected mode load-balance, got %s", result.Mode)
	}
	if result.Timeout.Seconds() != 120 {
		t.Errorf("expected timeout 120s, got %v", result.Timeout)
	}
	if len(result.Tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(result.Tasks))
	}
}

// ========== 错误场景测试 ==========

func TestResolver_RedirectSelfReference(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocol: "openai"},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: map[string]string{
			"alias": "alias", // 自引用
		},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for self-referencing redirect")
	}
}

func TestResolver_RedirectErrorMessages(t *testing.T) {
	tests := []struct {
		name           string
		redirect       map[string]string
		modelGroups    []config.ModelGroupConfig
		expectedInErr  string
	}{
		{
			name: "target not found",
			redirect: map[string]string{"alias": "nonexistent"},
			modelGroups: []config.ModelGroupConfig{
				{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			},
			expectedInErr: "target 'nonexistent' not found",
		},
		{
			name: "alias conflicts",
			redirect: map[string]string{"gpt-4": "gpt-4"},
			modelGroups: []config.ModelGroupConfig{
				{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			},
			expectedInErr: "conflicts with existing model_group",
		},
		{
			name: "circular",
			redirect: map[string]string{"a": "b", "b": "a"},
			modelGroups: []config.ModelGroupConfig{
				{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			},
			expectedInErr: "circular redirect",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
					"openai": {Protocol: "openai"},
				}},
				ModelGroups: tt.modelGroups,
				Redirect:    tt.redirect,
			}

			_, err := NewResolver(cfg)
			if err == nil {
				t.Error("expected error, got nil")
				return
			}
			if !containsString(err.Error(), tt.expectedInErr) {
				t.Errorf("error message %q should contain %q", err.Error(), tt.expectedInErr)
			}
		})
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStringHelper(s, substr))
}

func containsStringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
