package runtimeconfig

import (
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func TestModelGroupCRUDValidateCreate(t *testing.T) {
	tests := []struct {
		name    string
		draft   *config.Config
		input   *ModelGroupInput
		wantErr bool
		errCode ErrorCode
	}{
		{
			name: "valid single model",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{
					{Name: "existing"},
				},
				Redirect: config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Name:  "new-group",
				Model: "openai/gpt-4",
			},
			wantErr: false,
		},
		{
			name: "empty name",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Model: "openai/gpt-4",
			},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
		{
			name: "duplicate name",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{
					{Name: "existing"},
				},
				Redirect: config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Name:  "existing",
				Model: "openai/gpt-4",
			},
			wantErr: true,
			errCode: ErrCodeConflict,
		},
		{
			name: "conflicts with redirect key",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{{Source: "gpt-4", Target: "existing"}},
			},
			input: &ModelGroupInput{
				Name:  "gpt-4",
				Model: "openai/gpt-4",
			},
			wantErr: true,
			errCode: ErrCodeConflict,
		},
		{
			name: "invalid mode",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Name:  "test",
				Model: "openai/gpt-4",
				Mode:  "invalid-mode",
			},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
		{
			name: "both model and models",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Name:   "test",
				Model:  "openai/gpt-4",
				Models: []ModelEntryInput{{Model: "openai/gpt-4"}},
			},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
		{
			name: "neither model nor models",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Name: "test",
			},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
		{
			name: "invalid weight",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Name: "test",
				Models: []ModelEntryInput{
					{Model: "openai/gpt-4", Weight: intPtr(0)},
				},
			},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
		{
			name: "valid multi models",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Name: "test",
				Models: []ModelEntryInput{
					{Model: "openai/gpt-4", Weight: intPtr(2)},
					{Model: "anthropic/claude", Weight: intPtr(1)},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid context_length",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Name:  "test",
				Model: "openai/gpt-4",
				ModelMetadata: &ModelMetadataInput{
					ContextLength: intPtr(0),
				},
			},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
		{
			name: "adaptive mode valid leaf only",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Name: "adaptive-ok",
				Mode: "adaptive",
				Models: []ModelEntryInput{
					{Model: "openai/gpt-4o"},
					{Model: "anthropic/claude"},
				},
			},
			wantErr: false,
		},
		{
			name: "adaptive mode rejects internal reference",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Name: "adaptive-bad",
				Mode: "adaptive",
				Models: []ModelEntryInput{
					{Model: "openai/gpt-4o"},
					{Model: "internal-group"},
				},
			},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
		{
			name: "adaptive mode rejects all internal references",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Name: "adaptive-pure-ref",
				Mode: "adaptive",
				Models: []ModelEntryInput{
					{Model: "child-group-1"},
					{Model: "child-group-2"},
				},
			},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
		{
			name: "adaptive mode accepts single model shorthand",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Name:  "adaptive-single",
				Mode:  "adaptive",
				Model: "openai/gpt-4o",
			},
			wantErr: false,
		},
		{
			name: "adaptive mode rejects single model internal ref",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{},
			},
			input: &ModelGroupInput{
				Name:  "adaptive-bad-single",
				Mode:  "adaptive",
				Model: "internal-group",
			},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crud := NewModelGroupCRUD(tt.draft)
			err := crud.ValidateCreate(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if err.Code != tt.errCode {
					t.Errorf("expected error code %s, got %s", tt.errCode, err.Code)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestModelGroupCRUDUpdateRename(t *testing.T) {
	tests := []struct {
		name    string
		draft   *config.Config
		oldName string
		input   *ModelGroupInput
		wantErr bool
		errCode ErrorCode
		check   func(t *testing.T, draft *config.Config)
	}{
		{
			name: "rename updates redirect",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{
					{Name: "old-name", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
				},
				Redirect: config.RedirectConfigs{{Source: "gpt-4", Target: "old-name"}},
			},
			oldName: "old-name",
			input: &ModelGroupInput{
				Name:  "new-name",
				Model: "openai/gpt-4",
			},
			wantErr: false,
			check: func(t *testing.T, draft *config.Config) {
				found := false
				for _, rc := range draft.Redirect {
					if rc.Source == "gpt-4" {
						if rc.Target != "new-name" {
							t.Errorf("expected redirect value 'new-name', got %q", rc.Target)
						}
						found = true
						break
					}
				}
				if !found {
					t.Error("redirect source 'gpt-4' not found")
				}
			},
		},
		{
			name: "rename updates models reference",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{
					{Name: "group-a", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
					{Name: "group-b", Models: config.ModelEntries{{Model: "group-a"}}},
				},
				Redirect: config.RedirectConfigs{},
			},
			oldName: "group-a",
			input: &ModelGroupInput{
				Name:  "group-a-new",
				Model: "openai/gpt-4",
			},
			wantErr: false,
			check: func(t *testing.T, draft *config.Config) {
				for _, g := range draft.ModelGroups {
					if g.Name == "group-b" {
						if len(g.Models) != 1 || g.Models[0].Model != "group-a-new" {
							t.Errorf("expected models reference to be 'group-a-new', got %q", g.Models[0].Model)
						}
						return
					}
				}
				t.Error("group-b not found")
			},
		},
		{
			name: "rename conflict with existing group",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{
					{Name: "group-a", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
					{Name: "group-b", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
				},
				Redirect: config.RedirectConfigs{},
			},
			oldName: "group-a",
			input: &ModelGroupInput{
				Name:  "group-b",
				Model: "openai/gpt-4",
			},
			wantErr: true,
			errCode: ErrCodeConflict,
		},
		{
			name: "rename conflict with redirect key",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{
					{Name: "group-a", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
				},
				Redirect: config.RedirectConfigs{{Source: "gpt-4", Target: "group-a"}},
			},
			oldName: "group-a",
			input: &ModelGroupInput{
				Name:  "gpt-4",
				Model: "openai/gpt-4",
			},
			wantErr: true,
			errCode: ErrCodeConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crud := NewModelGroupCRUD(tt.draft)
			_, err := crud.Update(tt.oldName, tt.input)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else {
					if e, ok := err.(*Error); ok && e.Code != tt.errCode {
						t.Errorf("expected error code %s, got %s", tt.errCode, e.Code)
					}
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if tt.check != nil {
					tt.check(t, tt.draft)
				}
			}
		})
	}
}

func TestModelGroupCRUDValidateDelete(t *testing.T) {
	tests := []struct {
		name    string
		draft   *config.Config
		target  string
		wantErr bool
		errCode ErrorCode
	}{
		{
			name: "delete unreferenced group",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{
					{Name: "unused", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
				},
				Redirect: config.RedirectConfigs{},
			},
			target:  "unused",
			wantErr: false,
		},
		{
			name: "delete referenced by redirect",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{
					{Name: "used", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
				},
				Redirect: config.RedirectConfigs{{Source: "alias", Target: "used"}},
			},
			target:  "used",
			wantErr: true,
			errCode: ErrCodeConflict,
		},
		{
			name: "delete referenced by models",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{
					{Name: "child", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
					{Name: "parent", Models: config.ModelEntries{{Model: "child"}}},
				},
				Redirect: config.RedirectConfigs{},
			},
			target:  "child",
			wantErr: true,
			errCode: ErrCodeConflict,
		},
		{
			name: "delete nonexistent",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{},
				Redirect:    config.RedirectConfigs{},
			},
			target:  "nonexistent",
			wantErr: true,
			errCode: ErrCodeNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crud := NewModelGroupCRUD(tt.draft)
			err := crud.ValidateDelete(tt.target)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if err.Code != tt.errCode {
					t.Errorf("expected error code %s, got %s", tt.errCode, err.Code)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestModelGroupInputNormalize(t *testing.T) {
	t.Run("single model input", func(t *testing.T) {
		input := &ModelGroupInput{
			Name:  "test",
			Model: "openai/gpt-4",
		}

		cfg := input.NormalizeInput()
		if len(cfg.Models) != 1 {
			t.Fatalf("expected 1 model, got %d", len(cfg.Models))
		}
		if cfg.Models[0].Model != "openai/gpt-4" {
			t.Errorf("expected model 'openai/gpt-4', got %q", cfg.Models[0].Model)
		}
		if cfg.Models[0].Weight != 1 {
			t.Errorf("expected weight 1, got %d", cfg.Models[0].Weight)
		}
		if cfg.Mode != "failover" {
			t.Errorf("expected default mode failover, got %q", cfg.Mode)
		}
	})

	t.Run("multi models input with defaults", func(t *testing.T) {
		input := &ModelGroupInput{
			Name: "test",
			Models: []ModelEntryInput{
				{Model: "openai/gpt-4"},
				{Model: "anthropic/claude"},
			},
		}

		cfg := input.NormalizeInput()
		if len(cfg.Models) != 2 {
			t.Fatalf("expected 2 models, got %d", len(cfg.Models))
		}
		for i, e := range cfg.Models {
			if e.Weight != 1 {
				t.Errorf("models[%d]: expected weight 1, got %d", i, e.Weight)
			}
		}
		if cfg.Mode != "failover" {
			t.Errorf("expected default mode failover, got %q", cfg.Mode)
		}
	})
}

func TestModelGroupCRUDRenameMixedReferences(t *testing.T) {
	draft := &config.Config{
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:   "group-a",
				Models: config.ModelEntries{{Model: "openai/gpt-4"}},
			},
			{
				Name: "group-b",
				Models: config.ModelEntries{
					{Model: "group-a"},          // 内部引用
					{Model: "anthropic/claude"}, // 外部 provider/model
				},
			},
		},
		Redirect: config.RedirectConfigs{{Source: "alias", Target: "group-a"}},
	}

	// 将 group-a 改名为 group-x
	input := &ModelGroupInput{
		Name:  "group-x",
		Model: "openai/gpt-4",
	}

	crud := NewModelGroupCRUD(draft)
	_, err := crud.Update("group-a", input)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}

	// 验证 redirect value 被更新
	found := false
	for _, rc := range draft.Redirect {
		if rc.Source == "alias" {
			if rc.Target != "group-x" {
				t.Errorf("redirect value: expected 'group-x', got '%s'", rc.Target)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("redirect source 'alias' not found")
	}

	// 验证 group-b 的 models 中只有内部引用被更新
	if len(draft.ModelGroups) != 2 {
		t.Fatalf("expected 2 model groups, got %d", len(draft.ModelGroups))
	}

	groupB := draft.ModelGroups[1]
	if groupB.Name != "group-b" {
		t.Fatalf("expected group name 'group-b', got '%s'", groupB.Name)
	}

	if len(groupB.Models) != 2 {
		t.Fatalf("expected 2 models in group-b, got %d", len(groupB.Models))
	}

	// models[0] 应该是内部引用，被更新
	if groupB.Models[0].Model != "group-x" {
		t.Errorf("models[0]: expected 'group-x', got '%s'", groupB.Models[0].Model)
	}

	// models[1] 应该是外部引用，不变
	if groupB.Models[1].Model != "anthropic/claude" {
		t.Errorf("models[1]: expected 'anthropic/claude', got '%s'", groupB.Models[1].Model)
	}
}

func TestModelGroupCRUDValidateUpdateAdaptive(t *testing.T) {
	tests := []struct {
		name    string
		draft   *config.Config
		oldName string
		input   *ModelGroupInput
		wantErr bool
		errCode ErrorCode
	}{
		{
			name: "update to adaptive mode with valid leaf entries",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{
					{Name: "existing", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
				},
				Redirect: config.RedirectConfigs{},
			},
			oldName: "existing",
			input: &ModelGroupInput{
				Name: "existing",
				Mode: "adaptive",
				Models: []ModelEntryInput{
					{Model: "openai/gpt-4o"},
					{Model: "anthropic/claude"},
				},
			},
			wantErr: false,
		},
		{
			name: "update to adaptive with internal reference rejected",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{
					{Name: "existing", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
				},
				Redirect: config.RedirectConfigs{},
			},
			oldName: "existing",
			input: &ModelGroupInput{
				Name: "existing",
				Mode: "adaptive",
				Models: []ModelEntryInput{
					{Model: "internal-group"},
				},
			},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
		{
			name: "update to adaptive with mixed leaf and internal ref rejected",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{
					{Name: "existing", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
				},
				Redirect: config.RedirectConfigs{},
			},
			oldName: "existing",
			input: &ModelGroupInput{
				Name: "existing",
				Mode: "adaptive",
				Models: []ModelEntryInput{
					{Model: "openai/gpt-4o"},
					{Model: "child-group"},
				},
			},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crud := NewModelGroupCRUD(tt.draft)
			err := crud.ValidateUpdate(tt.oldName, tt.input)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if err.Code != tt.errCode {
					t.Errorf("expected error code %s, got %s", tt.errCode, err.Code)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestModelGroupCRUDValidateDeleteMixedReferences(t *testing.T) {
	draft := &config.Config{
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:   "group-a",
				Models: config.ModelEntries{{Model: "openai/gpt-4"}},
			},
			{
				Name: "group-b",
				Models: config.ModelEntries{
					{Model: "group-a"},          // 内部引用
					{Model: "anthropic/claude"}, // 外部引用
				},
			},
		},
		Redirect: config.RedirectConfigs{},
	}

	crud := NewModelGroupCRUD(draft)
	err := crud.ValidateDelete("group-a")

	// 应该因为内部引用而失败
	if err == nil {
		t.Error("expected error for group referenced in models")
	} else if err.Code != ErrCodeConflict {
		t.Errorf("expected conflict error, got %s", err.Code)
	}
}

func intPtr(v int) *int {
	return &v
}

func TestModelGroupCRUD_Exposure(t *testing.T) {
	draft := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{},
	}
	crud := NewModelGroupCRUD(draft)

	// 非法 exposure 在 create 阶段失败
	bad := &ModelGroupInput{
		Name:     "bad",
		Model:    "openai/gpt-4",
		Exposure: strPtr("weird"),
	}
	if err := crud.ValidateCreate(bad); err == nil {
		t.Error("expected validation error for invalid exposure")
	} else if err.Code != ErrCodeBadRequest {
		t.Errorf("expected bad_request, got %s", err.Code)
	}

	// hidden / internal 合法
	for _, exp := range []string{"hidden", "internal"} {
		inp := &ModelGroupInput{
			Name:     "g-" + exp,
			Model:    "openai/gpt-4",
			Exposure: strPtr(exp),
		}
		if err := crud.ValidateCreate(inp); err != nil {
			t.Errorf("exposure %q should be valid: %v", exp, err)
		}
	}

	// 省略 exposure 默认 public，且 create 后落盘为 public
	input := &ModelGroupInput{Name: "g-default", Model: "openai/gpt-4"}
	if err := crud.ValidateCreate(input); err != nil {
		t.Fatalf("default exposure should be valid: %v", err)
	}
	cfg, err := crud.Create(input)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if cfg.Exposure != nil {
		t.Errorf("expected nil exposure (default public), got %v", *cfg.Exposure)
	}

	// 通过输出 DTO 验证默认归一为 public
	out := ToOutput(cfg)
	if out.Exposure != config.ExposurePublic {
		t.Errorf("expected output exposure public, got %q", out.Exposure)
	}
}

func TestModelGroupCRUD_Sticky_Create(t *testing.T) {
	crud := NewModelGroupCRUD(&config.Config{
		ModelGroups: []config.ModelGroupConfig{},
		Redirect:    config.RedirectConfigs{},
	})

	// 创建带有 sticky 的 load-balance group
	input := &ModelGroupInput{
		Name:  "lb-group",
		Mode:  "load-balance",
		Model: "openai/gpt-4",
		Sticky: &StickyInput{
			Enabled:     true,
			IdleTimeout: "10m",
		},
	}

	if err := crud.ValidateCreate(input); err != nil {
		t.Fatalf("expected valid, got error: %v", err)
	}

	cfg, err := crud.Create(input)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}

	if cfg.Sticky == nil || !cfg.Sticky.Enabled {
		t.Error("expected sticky enabled")
	}
	if cfg.Sticky.IdleTimeout != "10m" {
		t.Errorf("expected idle_timeout 10m, got %s", cfg.Sticky.IdleTimeout)
	}
}

func TestModelGroupCRUD_Sticky_RejectsNonLBMode(t *testing.T) {
	crud := NewModelGroupCRUD(&config.Config{
		ModelGroups: []config.ModelGroupConfig{},
		Redirect:    config.RedirectConfigs{},
	})

	// 非 load-balance 模式启用 sticky 应该失败
	input := &ModelGroupInput{
		Name:  "concurrent-group",
		Mode:  "concurrent",
		Model: "openai/gpt-4",
		Sticky: &StickyInput{
			Enabled: true,
		},
	}

	err := crud.ValidateCreate(input)
	if err == nil {
		t.Fatal("expected error for sticky on non-LB mode")
	}
	if err.Code != ErrCodeBadRequest {
		t.Errorf("expected bad request error, got %s", err.Code)
	}
}

func TestModelGroupCRUD_Sticky_UpdatePreservesWhenOmitted(t *testing.T) {
	// 初始配置带有 sticky
	crud := NewModelGroupCRUD(&config.Config{
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:  "lb-group",
				Mode:  "load-balance",
				Model: "openai/gpt-4",
				Sticky: &config.StickyConfig{
					Enabled:     true,
					IdleTimeout: "15m",
				},
			},
		},
		Redirect: config.RedirectConfigs{},
	})

	// Update 省略 sticky
	input := &ModelGroupInput{
		Name:  "lb-group",
		Mode:  "load-balance",
		Model: "anthropic/claude", // 只改 model
		// Sticky 为 nil，应该保留原配置
	}

	cfg, err := crud.Update("lb-group", input)
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}

	// 验证 sticky 被保留
	if cfg.Sticky == nil || !cfg.Sticky.Enabled {
		t.Error("expected sticky to be preserved")
	}
	if cfg.Sticky.IdleTimeout != "15m" {
		t.Errorf("expected idle_timeout 15m, got %s", cfg.Sticky.IdleTimeout)
	}
}

func TestModelGroupCRUD_Sticky_UpdateOverwritesWhenProvided(t *testing.T) {
	crud := NewModelGroupCRUD(&config.Config{
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:  "lb-group",
				Mode:  "load-balance",
				Model: "openai/gpt-4",
				Sticky: &config.StickyConfig{
					Enabled:     true,
					IdleTimeout: "15m",
				},
			},
		},
		Redirect: config.RedirectConfigs{},
	})

	// Update 显式提供 sticky
	input := &ModelGroupInput{
		Name:  "lb-group",
		Mode:  "load-balance",
		Model: "anthropic/claude",
		Sticky: &StickyInput{
			Enabled: false, // 显式禁用
		},
	}

	cfg, err := crud.Update("lb-group", input)
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}

	// 验证 sticky 被覆盖
	if cfg.Sticky == nil || cfg.Sticky.Enabled {
		t.Error("expected sticky to be disabled")
	}
}

func TestModelGroupCRUD_Sticky_UpdateClearsWhenModeLeavesLB(t *testing.T) {
	crud := NewModelGroupCRUD(&config.Config{
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:  "lb-group",
				Mode:  "load-balance",
				Model: "openai/gpt-4",
				Sticky: &config.StickyConfig{
					Enabled:     true,
					IdleTimeout: "15m",
				},
			},
		},
		Redirect: config.RedirectConfigs{},
	})

	// 省略 sticky，改 mode 为 failover → sticky 应被清理
	input := &ModelGroupInput{
		Name:  "lb-group",
		Mode:  "failover",
		Model: "openai/gpt-4",
	}

	cfg, err := crud.Update("lb-group", input)
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if cfg.Sticky != nil {
		t.Errorf("expected sticky cleared on non-LB mode, got %+v", cfg.Sticky)
	}
}

func strPtr(s string) *string { return &s }
