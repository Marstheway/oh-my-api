package runtimeconfig

import (
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func TestRedirectCRUD_ValidateCreate(t *testing.T) {
	tests := []struct {
		name    string
		draft   *config.Config
		input   *RedirectInput
		wantErr bool
		errCode ErrorCode
		errMsg  string
	}{
		{
			name: "有效创建",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    make(config.RedirectConfigs, 0),
			},
			input:   &RedirectInput{Source: "gpt-4-turbo", Target: "gpt-4"},
			wantErr: false,
		},
		{
			name: "alias 为空",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    make(config.RedirectConfigs, 0),
			},
			input:   &RedirectInput{Source: "", Target: "gpt-4"},
			wantErr: true,
			errCode: ErrCodeBadRequest,
			errMsg:  "must not be empty",
		},
		{
			name: "target 为空",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    make(config.RedirectConfigs, 0),
			},
			input:   &RedirectInput{Source: "gpt-4-turbo", Target: ""},
			wantErr: true,
			errCode: ErrCodeBadRequest,
			errMsg:  "must not be empty",
		},
		{
			name: "alias 包含 /",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    make(config.RedirectConfigs, 0),
			},
			input:   &RedirectInput{Source: "openai/gpt-4-turbo", Target: "gpt-4"},
			wantErr: true,
			errCode: ErrCodeBadRequest,
			errMsg:  "must not contain '/'",
		},
		{
			name: "target 包含 /",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    make(config.RedirectConfigs, 0),
			},
			input:   &RedirectInput{Source: "gpt-4-turbo", Target: "openai/gpt-4"},
			wantErr: true,
			errCode: ErrCodeBadRequest,
			errMsg:  "must not contain '/'",
		},
		{
			name: "alias 与 model_group 名冲突",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    make(config.RedirectConfigs, 0),
			},
			input:   &RedirectInput{Source: "gpt-4", Target: "gpt-4-prod"},
			wantErr: true,
			errCode: ErrCodeConflict,
			errMsg:  "conflicts with existing model_group name",
		},
		{
			name: "alias 已存在",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    config.RedirectConfigs{{Source: "gpt-4-turbo", Target: "gpt-4"}},
			},
			input:   &RedirectInput{Source: "gpt-4-turbo", Target: "gpt-4"},
			wantErr: true,
			errCode: ErrCodeConflict,
			errMsg:  "already exists",
		},
		{
			name: "target 不存在",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-3.5"}},
				Redirect:    make(config.RedirectConfigs, 0),
			},
			input:   &RedirectInput{Source: "gpt-4-turbo", Target: "gpt-4"},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
		{
			name: "循环 redirect - alias 指向自己",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    config.RedirectConfigs{{Source: "alias-a", Target: "gpt-4"}}, // alias-a 存在
			},
			input:   &RedirectInput{Source: "alias-b", Target: "alias-b"}, // 新 alias 指向自己
			wantErr: true,
			errCode: ErrCodeValidation,
			errMsg:  "would create circular redirect",
		},
		{
			name: "间接循环 - target 不存在应报错",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    config.RedirectConfigs{{Source: "alias-b", Target: "alias-c"}}, // alias-c 不存在
			},
			input:   &RedirectInput{Source: "alias-a", Target: "alias-c"},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
		{
			name: "间接循环 2",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    config.RedirectConfigs{{Source: "alias-a", Target: "alias-b"}},
			},
			input:   &RedirectInput{Source: "alias-b", Target: "alias-a"},
			wantErr: true,
			errCode: ErrCodeValidation,
			errMsg:  "would create circular redirect",
		},
		{
			name: "允许指向其他 alias",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    config.RedirectConfigs{{Source: "alias-b", Target: "gpt-4"}},
			},
			input:   &RedirectInput{Source: "alias-a", Target: "alias-b"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crud := NewRedirectCRUD(tt.draft)
			err := crud.ValidateCreate(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateCreate() expected error, got nil")
					return
				}
				if err.Code != tt.errCode {
					t.Errorf("ValidateCreate() error code = %v, want %v", err.Code, tt.errCode)
				}
				if tt.errMsg != "" && err.Message != tt.errMsg {
					t.Errorf("ValidateCreate() error message = %v, want %v", err.Message, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateCreate() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestRedirectCRUD_ValidateUpdate(t *testing.T) {
	tests := []struct {
		name     string
		draft    *config.Config
		oldAlias string
		input    *RedirectInput
		wantErr  bool
		errCode  ErrorCode
	}{
		{
			name: "有效更新",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}, {Name: "gpt-3.5"}},
				Redirect:    config.RedirectConfigs{{Source: "gpt-4-turbo", Target: "gpt-4"}},
			},
			oldAlias: "gpt-4-turbo",
			input:    &RedirectInput{Source: "gpt-4-turbo", Target: "gpt-3.5"},
			wantErr:  false,
		},
		{
			name: "有效改名",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    config.RedirectConfigs{{Source: "gpt-4-turbo", Target: "gpt-4"}},
			},
			oldAlias: "gpt-4-turbo",
			input:    &RedirectInput{Source: "gpt-4-turbo-v2", Target: "gpt-4"},
			wantErr:  false,
		},
		{
			name: "oldAlias 不存在",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    config.RedirectConfigs{{Source: "gpt-4-turbo", Target: "gpt-4"}},
			},
			oldAlias: "not-exists",
			input:    &RedirectInput{Source: "gpt-4-turbo", Target: "gpt-4"},
			wantErr:  true,
			errCode:  ErrCodeNotFound,
		},
		{
			name: "改名冲突",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    config.RedirectConfigs{{Source: "gpt-4-turbo", Target: "gpt-4"}, {Source: "alias-2", Target: "gpt-4"}},
			},
			oldAlias: "gpt-4-turbo",
			input:    &RedirectInput{Source: "alias-2", Target: "gpt-4"},
			wantErr:  true,
			errCode:  ErrCodeConflict,
		},
		{
			name: "更新形成循环",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    config.RedirectConfigs{{Source: "alias-a", Target: "alias-b"}, {Source: "alias-b", Target: "gpt-4"}},
			},
			oldAlias: "alias-b",
			input:    &RedirectInput{Source: "alias-b", Target: "alias-a"},
			wantErr:  true,
			errCode:  ErrCodeValidation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crud := NewRedirectCRUD(tt.draft)
			err := crud.ValidateUpdate(tt.oldAlias, tt.input)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateUpdate() expected error, got nil")
					return
				}
				if err.Code != tt.errCode {
					t.Errorf("ValidateUpdate() error code = %v, want %v", err.Code, tt.errCode)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateUpdate() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestRedirectCRUD_ValidateDelete(t *testing.T) {
	tests := []struct {
		name    string
		draft   *config.Config
		alias   string
		wantErr bool
		errCode ErrorCode
	}{
		{
			name: "有效删除",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    config.RedirectConfigs{{Source: "gpt-4-turbo", Target: "gpt-4"}},
			},
			alias:   "gpt-4-turbo",
			wantErr: false,
		},
		{
			name: "alias 不存在",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    config.RedirectConfigs{{Source: "gpt-4-turbo", Target: "gpt-4"}},
			},
			alias:   "not-exists",
			wantErr: true,
			errCode: ErrCodeNotFound,
		},
		{
			name: "被其他 redirect 引用",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
				Redirect:    config.RedirectConfigs{{Source: "alias-a", Target: "alias-b"}, {Source: "alias-b", Target: "gpt-4"}},
			},
			alias:   "alias-b",
			wantErr: true,
			errCode: ErrCodeConflict,
		},
		{
			name: "被 model_group models 引用",
			draft: &config.Config{
				ModelGroups: []config.ModelGroupConfig{
					{Name: "test-group", Models: config.ModelEntries{{Model: "alias-a"}}},
				},
				Redirect: config.RedirectConfigs{{Source: "alias-a", Target: "gpt-4"}, {Source: "gpt-4", Target: "prod-group"}},
			},
			alias:   "alias-a",
			wantErr: true,
			errCode: ErrCodeConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crud := NewRedirectCRUD(tt.draft)
			err := crud.ValidateDelete(tt.alias)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateDelete() expected error, got nil")
					return
				}
				if err.Code != tt.errCode {
					t.Errorf("ValidateDelete() error code = %v, want %v", err.Code, tt.errCode)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateDelete() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestRedirectCRUD_CRUDOperations(t *testing.T) {
	draft := &config.Config{
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4"},
			{Name: "gpt-3.5"},
		},
		Redirect: make(config.RedirectConfigs, 0),
	}

	crud := NewRedirectCRUD(draft)

	// Create
	input1 := &RedirectInput{Source: "gpt-4-turbo", Target: "gpt-4"}
	if err := crud.Create(input1); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Get
	output, found := crud.Get("gpt-4-turbo")
	if !found {
		t.Fatal("Get() not found after Create()")
	}
	if output.Source != "gpt-4-turbo" || output.Target != "gpt-4" {
		t.Errorf("Get() = %+v, want {Source: gpt-4-turbo, Target: gpt-4}", output)
	}

	// List
	list := crud.List()
	if len(list) != 1 {
		t.Errorf("List() length = %d, want 1", len(list))
	}
	if list[0].Source != "gpt-4-turbo" || list[0].Target != "gpt-4" {
		t.Errorf("List()[0] = %+v, want {Source: gpt-4-turbo, Target: gpt-4}", list[0])
	}
	if list[0].ResolvedGroup != "gpt-4" || list[0].ChainLength != 0 {
		t.Errorf("List()[0] resolved info = {ResolvedGroup: %s, ChainLength: %d}, want {gpt-4, 0}", list[0].ResolvedGroup, list[0].ChainLength)
	}

	// Create another redirect (alias-to-alias)
	input2 := &RedirectInput{Source: "fast-model", Target: "gpt-4-turbo"}
	if err := crud.Create(input2); err != nil {
		t.Fatalf("Create() second redirect error = %v", err)
	}

	// List with chain
	list = crud.List()
	if len(list) != 2 {
		t.Fatalf("List() length = %d, want 2", len(list))
	}

	var fastModelEntry *RedirectListOutput
	var gpt4TurboEntry *RedirectListOutput
	for i := range list {
		if list[i].Source == "fast-model" {
			fastModelEntry = &list[i]
		}
		if list[i].Source == "gpt-4-turbo" {
			gpt4TurboEntry = &list[i]
		}
	}

	if gpt4TurboEntry == nil {
		t.Fatal("gpt-4-turbo redirect not found in List()")
	}
	if gpt4TurboEntry.ResolvedGroup != "gpt-4" || gpt4TurboEntry.ChainLength != 0 {
		t.Errorf("gpt-4-turbo resolved info = {ResolvedGroup: %s, ChainLength: %d}, want {gpt-4, 0}", gpt4TurboEntry.ResolvedGroup, gpt4TurboEntry.ChainLength)
	}

	if fastModelEntry == nil {
		t.Fatal("fast-model redirect not found in List()")
	}
	if fastModelEntry.ResolvedGroup != "gpt-4" || fastModelEntry.ChainLength != 1 {
		t.Errorf("fast-model resolved info = {ResolvedGroup: %s, ChainLength: %d}, want {gpt-4, 1}", fastModelEntry.ResolvedGroup, fastModelEntry.ChainLength)
	}

	// Update (no rename)
	inputUpdate := &RedirectInput{Source: "gpt-4-turbo", Target: "gpt-3.5"}
	if err := crud.Update("gpt-4-turbo", inputUpdate); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	output, _ = crud.Get("gpt-4-turbo")
	if output.Target != "gpt-3.5" {
		t.Errorf("After Update(), target = %s, want gpt-3.5", output.Target)
	}

	// Update with rename
	inputRename := &RedirectInput{Source: "gpt-4-turbo-v2", Target: "gpt-4"}
	if err := crud.Update("gpt-4-turbo", inputRename); err != nil {
		t.Fatalf("Update() with rename error = %v", err)
	}

	_, found = crud.Get("gpt-4-turbo")
	if found {
		t.Error("Old alias should not exist after rename")
	}

	output, found = crud.Get("gpt-4-turbo-v2")
	if !found {
		t.Error("New alias should exist after rename")
	}

	// Delete
	if err := crud.Delete("gpt-4-turbo-v2"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, found = crud.Get("gpt-4-turbo-v2")
	if found {
		t.Error("Alias should not exist after Delete()")
	}
}

func TestRedirectCRUD_Exposure(t *testing.T) {
	draft := &config.Config{
		ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
		Redirect:    make(config.RedirectConfigs, 0),
	}
	crud := NewRedirectCRUD(draft)

	// 非法 exposure 在 create 阶段失败
	bad := &RedirectInput{
		Source:   "turbo",
		Target:   "gpt-4",
		Exposure: strPtr("weird"),
	}
	if err := crud.ValidateCreate(bad); err == nil {
		t.Error("expected validation error for invalid exposure")
	} else if err.Code != ErrCodeBadRequest {
		t.Errorf("expected bad_request, got %s", err.Code)
	}

	// hidden / internal 合法
	for _, exp := range []string{"hidden", "internal"} {
		inp := &RedirectInput{
			Source:   "turbo-" + exp,
			Target:   "gpt-4",
			Exposure: strPtr(exp),
		}
		if err := crud.ValidateCreate(inp); err != nil {
			t.Errorf("exposure %q should be valid: %v", exp, err)
		}
	}

	// 省略 exposure 默认 public，且 create 后输出归一为 public
	input := &RedirectInput{Source: "turbo-default", Target: "gpt-4"}
	if err := crud.ValidateCreate(input); err != nil {
		t.Fatalf("default exposure should be valid: %v", err)
	}
	if err := crud.Create(input); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	out, found := crud.Get("turbo-default")
	if !found {
		t.Fatal("turbo-default not found after Create()")
	}
	if out.Exposure != config.ExposurePublic {
		t.Errorf("expected output exposure public, got %q", out.Exposure)
	}

	// update 阶段非法 exposure 失败
	crud2 := NewRedirectCRUD(&config.Config{
		ModelGroups: []config.ModelGroupConfig{{Name: "gpt-4"}},
		Redirect:    config.RedirectConfigs{{Source: "turbo", Target: "gpt-4"}},
	})
	if err := crud2.ValidateUpdate("turbo", &RedirectInput{Source: "turbo", Target: "gpt-4", Exposure: strPtr("weird")}); err == nil {
		t.Error("expected validation error for invalid exposure on update")
	} else if err.Code != ErrCodeBadRequest {
		t.Errorf("expected bad_request on update, got %s", err.Code)
	}

	for _, exp := range []string{"hidden", "internal"} {
		inp := &RedirectInput{Source: "turbo", Target: "gpt-4", Exposure: strPtr(exp)}
		if err := crud2.ValidateUpdate("turbo", inp); err != nil {
			t.Errorf("exposure %q should be valid on update: %v", exp, err)
		}
	}
}
