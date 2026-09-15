package model

import (
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

// exposurePtr 将 bool 映射为 *config.Exposure（true=public, false=internal），
// 旧 visible=false 等价于新 internal（不展示且不可外部直调）。
func exposurePtr(b bool) *config.Exposure {
	if b {
		v := config.ExposurePublic
		return &v
	}
	v := config.ExposureInternal
	return &v
}

func TestResolver_ListCallableEntries(t *testing.T) {
	public := config.ExposurePublic
	hidden := config.ExposureHidden
	internal := config.ExposureInternal

	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "pub-group", Exposure: &public, Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
			{Name: "hid-group", Exposure: &hidden, Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
			{Name: "int-group", Exposure: &internal, Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
		},
		Redirect: config.RedirectConfigs{
			{Source: "pub-alias", Target: "pub-group", Exposure: &public},
			{Source: "hid-alias", Target: "hid-group", Exposure: &hidden},
			{Source: "int-alias", Target: "int-group", Exposure: &internal},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	entries := r.ListCallableEntries()
	got := make(map[string]string)
	for _, e := range entries {
		got[e.Name] = e.FinalGroup
	}

	if len(entries) != 4 {
		t.Fatalf("ListCallableEntries len = %d, want 4 (public/hidden groups + redirects, no internal)", len(entries))
	}
	if got["pub-group"] != "pub-group" {
		t.Errorf("pub-group entry = %q, want itself", got["pub-group"])
	}
	if got["hid-group"] != "hid-group" {
		t.Errorf("hid-group entry = %q, want itself", got["hid-group"])
	}
	if got["pub-alias"] != "pub-group" {
		t.Errorf("pub-alias entry = %q, want pub-group", got["pub-alias"])
	}
	if got["hid-alias"] != "hid-group" {
		t.Errorf("hid-alias entry = %q, want hid-group", got["hid-alias"])
	}
	for _, name := range []string{"int-group", "int-alias"} {
		if _, ok := got[name]; ok {
			t.Errorf("internal entry %q must not be enumerated", name)
		}
	}
}

func TestResolver_ListCallableEntries_StableOrder(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "zebra", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
			{Name: "alpha", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
			{Name: "mike", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	a := r.ListCallableEntries()
	b := r.ListCallableEntries()
	if len(a) != 3 {
		t.Fatalf("entries len = %d, want 3", len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("order unstable: %v vs %v", a, b)
		}
	}
	want := []string{"alpha", "mike", "zebra"}
	for i, e := range a {
		if e.Name != want[i] {
			t.Fatalf("entries[%d] = %q, want %q (sorted)", i, e.Name, want[i])
		}
	}
}

// TestListCallableEntries_ConfigEnumerationConsistent 验证 model.Resolver 的入口名称集合
// 与 config.ListDirectCallableNames 的纯配置枚举完全一致（同一配置下去重 public/hidden 集合）。
func TestListCallableEntries_ConfigEnumerationConsistent(t *testing.T) {
	public := config.ExposurePublic
	hidden := config.ExposureHidden
	internal := config.ExposureInternal
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "pub-group", Exposure: &public, Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
			{Name: "hid-group", Exposure: &hidden, Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
			{Name: "int-group", Exposure: &internal, Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
			{Name: "default-group", Models: config.ModelEntries{{Model: "openai/gpt-4"}}},
		},
		Redirect: config.RedirectConfigs{
			{Source: "hid-alias", Target: "hid-group", Exposure: &hidden},
			{Source: "int-alias", Target: "int-group", Exposure: &internal},
			{Source: "pub-alias", Target: "pub-group", Exposure: &public},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	got := r.ListCallableEntries()
	want := config.ListDirectCallableNames(cfg)
	if len(got) != len(want) {
		t.Fatalf("resolver entries = %d, config names = %d; sets must match", len(got), len(want))
	}
	for i, e := range got {
		if e.Name != want[i] {
			t.Fatalf("entries[%d].Name = %q, want %q (identical to config enumeration)", i, e.Name, want[i])
		}
	}

	// 每个入口的最终 group 必须可解析（非空）。
	for _, e := range got {
		if e.FinalGroup == "" {
			t.Fatalf("entry %q has empty final group", e.Name)
		}
	}
}

func TestResolver_Resolve(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {
				Endpoint:  "https://api.openai.com/v1",
				APIKey:    "sk-xxx",
				Protocols: []string{"openai"},
			},
			"anthropic": {
				Endpoint:  "https://api.anthropic.com",
				APIKey:    "sk-ant-xxx",
				Protocols: []string{"anthropic"},
			},
			"openrouter": {
				Endpoint:  "https://openrouter.ai/api/v1",
				APIKey:    "sk-or-xxx",
				Protocols: []string{"openai"},
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
		name      string
		userModel string
		wantErr   bool
		wantMode  string
		wantTasks int
	}{
		{
			name:      "resolve openai model",
			userModel: "gpt-4",
			wantErr:   false,
			wantMode:  "failover",
			wantTasks: 1,
		},
		{
			name:      "resolve anthropic model",
			userModel: "claude-fast",
			wantErr:   false,
			wantMode:  "failover",
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

// TestBuildPlanNode_ModelOrder 验证 buildPlanNode 正确记录 models 列表中
// 叶子与子 group 引用的原始顺序。
func TestBuildPlanNode_ModelOrder(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"prov-a": {Endpoint: "https://a.example.com", APIKey: "key-a", Protocols: []string{"openai"}},
			"prov-b": {Endpoint: "https://b.example.com", APIKey: "key-b", Protocols: []string{"openai"}},
			"prov-c": {Endpoint: "https://c.example.com", APIKey: "key-c", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name: "child-group",
				Mode: "concurrent",
				Models: config.ModelEntries{
					{Model: "prov-a/model-a", Weight: 1},
				},
			},
			{
				Name: "mixed-order",
				Mode: "failover",
				Models: config.ModelEntries{
					{Model: "child-group", Weight: 1},    // 子 group 引用
					{Model: "prov-b/model-b", Weight: 2}, // 叶子
					{Model: "prov-c/model-c", Weight: 3}, // 叶子
				},
			},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver failed: %v", err)
	}

	plan, err := r.BuildPlanNodeForTest("mixed-order")
	if err != nil {
		t.Fatalf("BuildPlanNodeForTest failed: %v", err)
	}

	// 验证 PlanNode 结构
	if len(plan.Leaves) != 2 {
		t.Errorf("Leaves count = %d, want 2", len(plan.Leaves))
	}
	if len(plan.Children) != 1 {
		t.Errorf("Children count = %d, want 1", len(plan.Children))
	}

	// 验证配置顺序：期望 [child-group, prov-b/model-b, prov-c/model-c]
	// 即 [子group, 叶子, 叶子]
	expectedOrder := []struct {
		isLeaf       bool
		providerName string
	}{
		{isLeaf: false, providerName: ""},      // child-group
		{isLeaf: true, providerName: "prov-b"}, // prov-b/model-b
		{isLeaf: true, providerName: "prov-c"}, // prov-c/model-c
	}

	if len(plan.ModelOrder) != len(expectedOrder) {
		t.Fatalf("ModelOrder length = %d, want %d", len(plan.ModelOrder), len(expectedOrder))
	}

	for i, expected := range expectedOrder {
		got := plan.ModelOrder[i]
		if got.IsLeaf != expected.isLeaf {
			t.Errorf("ModelOrder[%d].IsLeaf = %v, want %v", i, got.IsLeaf, expected.isLeaf)
		}
		if got.Index < 0 {
			t.Errorf("ModelOrder[%d].Index = %d, must be >= 0", i, got.Index)
		}
		if expected.isLeaf {
			if got.Index >= len(plan.Leaves) {
				t.Errorf("ModelOrder[%d].Index = %d, exceeds Leaves length %d", i, got.Index, len(plan.Leaves))
			} else if plan.Leaves[got.Index].ProviderName != expected.providerName {
				t.Errorf("ModelOrder[%d] points to Leaves[%d].ProviderName = %s, want %s",
					i, got.Index, plan.Leaves[got.Index].ProviderName, expected.providerName)
			}
		} else {
			if got.Index >= len(plan.Children) {
				t.Errorf("ModelOrder[%d].Index = %d, exceeds Children length %d", i, got.Index, len(plan.Children))
			}
		}
	}
}

func TestResolver_ResolveWithMode(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai":    {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
			"anthropic": {Endpoint: "https://api.anthropic.com", APIKey: "sk-xxx", Protocols: []string{"anthropic"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name: "fast",
				Mode: "concurrent",
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
	if len(result.Tasks) != 2 {
		t.Errorf("tasks count = %d, want 2", len(result.Tasks))
	}
}

func TestResolver_ListUserModels(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocols: []string{"openai"}},
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

// TestResolver_ListUserModelsOrder 锁定 /v1/models 的稳定排序契约：
// redirect（用户显式配置的重点模型）在前、group 在后，各自块内按声明顺序，
// 不跨块混排、不依赖 Go map 随机迭代序。
func TestResolver_ListUserModelsOrder(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "group-b", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "group-a", Models: config.ModelEntries{{Model: "openai/gpt-4o", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{
			{Source: "alias-z", Target: "group-a"},
			{Source: "alias-y", Target: "group-b"},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := r.ListUserModels()
	want := []string{"alias-z", "alias-y", "group-b", "group-a"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order mismatch at %d: got %v, want %v", i, got, want)
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
			"openai":    {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
			"anthropic": {Endpoint: "https://api.anthropic.com", APIKey: "sk-xxx", Protocols: []string{"anthropic"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "claude-fast",
				Exposure: exposurePtr(falseVal),
				Models: config.ModelEntries{
					{Model: "anthropic/claude-sonnet-4-20250514", Weight: 1},
				},
			},
		},
		Redirect: config.RedirectConfigs{{Source: "claude-4-6-20261201", Target: "claude-fast"}},
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
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "alias", Target: "non-existent-model"}},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for redirect target not found")
	}
}

func TestResolver_RedirectAliasConflicts(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "gpt-4", Target: "gpt-4"}},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for redirect alias conflicts")
	}
}

func TestResolver_RedirectCircular(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "model-a", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "alias-a", Target: "alias-b"}, {Source: "alias-b", Target: "alias-a"}},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for circular redirect")
	}
}

func TestResolver_Exposure_ListAndCallable(t *testing.T) {
	falseVal := false
	trueVal := true
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "visible-model", Exposure: exposurePtr(trueVal), Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "internal-model", Exposure: exposurePtr(falseVal), Models: config.ModelEntries{{Model: "openai/gpt-4o", Weight: 1}}},
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

	// internal 模型不能直接调用
	_, err = r.Resolve("internal-model")
	if err == nil {
		t.Error("expected error for internal model")
	}
}

// ========== 更多 Redirect 测试 ==========

func TestResolver_RedirectChained(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "alias-a", Target: "alias-b"}, {Source: "alias-b", Target: "alias-c"}, {Source: "alias-c", Target: "gpt-4"}},
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
	if result.ModelGroup != "gpt-4" {
		t.Errorf("expected model group gpt-4, got %s", result.ModelGroup)
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

func TestResolver_RedirectToInternalGroup(t *testing.T) {
	falseVal := false
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "internal-backend", Exposure: exposurePtr(falseVal), Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "user-friendly-name", Target: "internal-backend"}},
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
	_, err = r.Resolve("internal-backend")
	if err == nil {
		t.Error("expected error for calling internal model directly")
	}

	// 只有别名在列表中，原模型不在
	models := r.ListUserModels()
	foundAlias := false
	foundBackend := false
	for _, m := range models {
		if m == "user-friendly-name" {
			foundAlias = true
		}
		if m == "internal-backend" {
			foundBackend = true
		}
	}
	if !foundAlias {
		t.Error("expected alias in model list")
	}
	if foundBackend {
		t.Error("internal backend should not appear in model list")
	}
}

func TestResolver_RedirectMultipleAliases(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "alias-1", Target: "gpt-4"}, {Source: "alias-2", Target: "gpt-4"}, {Source: "alias-3", Target: "gpt-4"}},
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
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{}, // 空 redirect
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
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
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

// ========== 更多 Exposure 测试 ==========

func TestResolver_VisibleDefault(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocols: []string{"openai"}},
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
			"openai": {Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "internal-1", Exposure: exposurePtr(falseVal), Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "internal-2", Exposure: exposurePtr(falseVal), Models: config.ModelEntries{{Model: "openai/gpt-4o", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "visible-alias", Target: "internal-1"}},
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
			"openai":    {Protocols: []string{"openai"}},
			"anthropic": {Protocols: []string{"anthropic"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "public-1", Exposure: exposurePtr(trueVal), Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "public-2", Exposure: exposurePtr(trueVal), Models: config.ModelEntries{{Model: "openai/gpt-4o", Weight: 1}}},
			{Name: "private-1", Exposure: exposurePtr(falseVal), Models: config.ModelEntries{{Model: "anthropic/claude-3", Weight: 1}}},
			{Name: "private-2", Exposure: exposurePtr(falseVal), Models: config.ModelEntries{{Model: "anthropic/claude-4", Weight: 1}}},
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

// ========== Redirect 与 Exposure 交互测试 ==========

func TestResolver_RedirectAliasExposure(t *testing.T) {
	falseVal := false
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "backend", Exposure: exposurePtr(falseVal), Mode: "concurrent", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "frontend", Target: "backend"}},
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

func TestResolver_RedirectPreservesMode(t *testing.T) {
	falseVal := false
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "backend",
				Exposure: exposurePtr(falseVal),
				Mode:     "load-balance",
				Models: config.ModelEntries{
					{Model: "openai/gpt-4", Weight: 2},
					{Model: "openai/gpt-4o", Weight: 1},
				},
			},
		},
		Redirect: config.RedirectConfigs{{Source: "frontend", Target: "backend"}},
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
	if len(result.Tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(result.Tasks))
	}
}

// ========== 错误场景测试 ==========

func TestResolver_RedirectSelfReference(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "alias", Target: "alias"}},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for self-referencing redirect")
	}
}

func TestResolver_RedirectErrorMessages(t *testing.T) {
	tests := []struct {
		name          string
		redirect      config.RedirectConfigs
		modelGroups   []config.ModelGroupConfig
		expectedInErr string
	}{
		{
			name:     "target not found",
			redirect: config.RedirectConfigs{{Source: "alias", Target: "nonexistent"}},
			modelGroups: []config.ModelGroupConfig{
				{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			},
			expectedInErr: "target 'nonexistent' not found",
		},
		{
			name:     "alias conflicts",
			redirect: config.RedirectConfigs{{Source: "gpt-4", Target: "gpt-4"}},
			modelGroups: []config.ModelGroupConfig{
				{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			},
			expectedInErr: "conflicts with existing model_group",
		},
		{
			name:     "circular",
			redirect: config.RedirectConfigs{{Source: "a", Target: "b"}, {Source: "b", Target: "a"}},
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
					"openai": {Protocols: []string{"openai"}},
				}},
				ModelGroups: tt.modelGroups,
				Redirect:    tt.redirect,
			}

			_, err := NewResolver(cfg)
			if err == nil {
				t.Error("expected error, got nil")
				return
			}
			if !strings.Contains(err.Error(), tt.expectedInErr) {
				t.Errorf("error message %q should contain %q", err.Error(), tt.expectedInErr)
			}
		})
	}
}

// ========== Task 1 新增：plan tree / 联合图校验 测试 ==========

// TestResolver_NestGroupRefInModels: models 中含不带 / 的内部引用应能解析到 group
func TestResolver_NestGroupRefInModels(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "leaf", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "parent", Mode: "failover", Models: config.ModelEntries{{Model: "leaf"}}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := r.Resolve("parent")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// plan 树根节点应有子节点
	if result.Plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if result.Plan.Mode != "failover" {
		t.Errorf("expected root mode failover, got %s", result.Plan.Mode)
	}
	if len(result.Plan.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(result.Plan.Children))
	}
	child := result.Plan.Children[0]
	if child.GroupName != "leaf" {
		t.Errorf("expected child group 'leaf', got %q", child.GroupName)
	}
}

// TestResolver_NamingConflict: redirect alias 与 model_group.name 同名应全局冲突拒绝
func TestResolver_NamingConflict(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "other", Models: config.ModelEntries{{Model: "openai/gpt-4o", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "gpt-4", Target: "other"}},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for alias conflicting with model_group name")
	}
}

// TestResolver_RedirectTargetWithSlash: redirect target 含 / 应被拒绝
func TestResolver_RedirectTargetWithSlash(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "alias", Target: "openai/gpt-4"}},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for redirect target containing '/'")
	}
}

// TestResolver_RedirectAliasWithSlash: redirect alias 含 / 应被拒绝
func TestResolver_RedirectAliasWithSlash(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "bad/alias", Target: "gpt-4"}},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for redirect alias containing '/'")
	}
}

// TestResolver_CycleDetection: group -> alias -> group 环检测
func TestResolver_CycleDetection(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:   "group-a",
				Models: config.ModelEntries{{Model: "alias-b", Weight: 1}}, // group-a -> alias -> group-b -> group-a (循环)
			},
			{
				Name:   "group-b",
				Models: config.ModelEntries{{Model: "group-a"}}, // group-b 引用 group-a
			},
		},
		Redirect: config.RedirectConfigs{{Source: "alias-b", Target: "group-b"}},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for cycle: group-a -> alias-b -> group-b -> group-a")
	}
	if err != nil && !strings.Contains(err.Error(), "cycle") && !strings.Contains(err.Error(), "circular") {
		t.Errorf("error should mention cycle/circular, got: %v", err)
	}
}

// TestResolver_EmbeddingsCompatible: 纯叶子 plan 应兼容 embeddings
func TestResolver_EmbeddingsCompatible(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"ollama": {Endpoint: "http://localhost:11434", Protocols: []string{"ollama.embed"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "embed-model", Models: config.ModelEntries{{Model: "ollama/all-minilm", Weight: 1}}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := r.Resolve("embed-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !IsEmbeddingsCompatiblePlan(result.Plan) {
		t.Error("expected plan to be embeddings compatible (pure leaf)")
	}
}

// TestResolver_EmbeddingsIncompatible_WithChildren: 有子 group 的 plan 不应兼容 embeddings
func TestResolver_EmbeddingsIncompatible_WithChildren(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"ollama": {Endpoint: "http://localhost:11434", Protocols: []string{"ollama.embed"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "child-group", Models: config.ModelEntries{{Model: "ollama/nomic-embed-text", Weight: 1}}},
			{Name: "parent-group", Models: config.ModelEntries{{Model: "child-group", Weight: 1}}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := r.Resolve("parent-group")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if IsEmbeddingsCompatiblePlan(result.Plan) {
		t.Error("expected plan with children to be NOT embeddings compatible")
	}
}

// TestResolver_EmbeddingsIncompatible_WithChildGroup: 含子 group 的 plan 不应兼容 embeddings
func TestResolver_EmbeddingsIncompatible_WithChildGroup(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"ollama": {Endpoint: "http://localhost:11434", Protocols: []string{"ollama.embed"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "leaf-embed", Models: config.ModelEntries{{Model: "ollama/all-minilm", Weight: 1}}},
			{Name: "nested-embed", Models: config.ModelEntries{{Model: "leaf-embed"}}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := r.Resolve("nested-embed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if IsEmbeddingsCompatiblePlan(result.Plan) {
		t.Error("expected nested plan to be NOT embeddings compatible")
	}
}

// TestResolver_RedirectNormalization: Resolve 时 ModelGroup 应指向最终 group，不是 alias
func TestResolver_RedirectNormalization(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "actual-group", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "my-alias", Target: "actual-group"}},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := r.Resolve("my-alias")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ModelGroup != "actual-group" {
		t.Errorf("expected ModelGroup 'actual-group', got %q", result.ModelGroup)
	}
}

// TestResolver_NoLeafError: 无主链路可执行叶子应返回 ErrNoValidProvider
func TestResolver_NoLeafError(t *testing.T) {
	// group models 仅包含一个内部引用，但该 group 是空的
	// 通过 visible=false 且 models 为空来模拟
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			// inner 没有任何有效 provider（models 会在 resolve 时跳过未知 provider）
			{Name: "inner", Models: config.ModelEntries{{Model: "missing-provider/model", Weight: 1}}},
			{Name: "outer", Models: config.ModelEntries{{Model: "inner"}}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected NewResolver error: %v", err)
	}

	_, err = r.Resolve("outer")
	if err == nil {
		t.Error("expected ErrNoValidProvider for group with no valid leaf")
	}
}

// TestResolver_GroupNameWithSlash: model_group.name 含 / 应在启动期拒绝
func TestResolver_GroupNameWithSlash(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "bad/name", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for model_group name containing '/'")
	}
	if err != nil && !strings.Contains(err.Error(), "/") {
		t.Errorf("error should mention the slash issue, got: %v", err)
	}
}

// TestResolver_ModelsInternalRefNotFound: models 中内部引用不存在应为启动期错误
func TestResolver_ModelsInternalRefNotFound(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "parent", Mode: "failover", Models: config.ModelEntries{{Model: "nonexistent-group"}}},
		},
	}

	_, err := NewResolver(cfg)
	if err == nil {
		t.Error("expected error for models ref pointing to nonexistent group")
	}
	if err != nil && !strings.Contains(err.Error(), "nonexistent-group") {
		t.Errorf("error should mention the missing ref, got: %v", err)
	}
}

// TestResolver_PlanNodeWeightPriority: 内部 group 引用的 weight/priority 应传递到子节点
func TestResolver_PlanNodeWeightPriority(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "child-group", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "parent-group", Mode: "loadbalance", Models: config.ModelEntries{
				{Model: "child-group", Weight: 3},
			}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := r.Resolve("parent-group")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(result.Plan.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(result.Plan.Children))
	}
	child := result.Plan.Children[0]
	if child.Weight != 3 {
		t.Errorf("expected child Weight=3, got %d", child.Weight)
	}
}

// ========== VisibleModelLeaves 测试 ==========

func TestResolver_VisibleModelLeaves_Simple(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	leaves, err := r.VisibleModelLeaves("gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(leaves) != 1 {
		t.Fatalf("expected 1 leaf, got %d", len(leaves))
	}
	if leaves[0].ProviderName != "openai" || leaves[0].UpstreamModel != "gpt-4" {
		t.Errorf("unexpected leaf: %+v", leaves[0])
	}
}

func TestResolver_VisibleModelLeaves_InternalModel(t *testing.T) {
	falseVal := false
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "internal-model", Exposure: exposurePtr(falseVal), Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = r.VisibleModelLeaves("internal-model")
	if err == nil {
		t.Error("expected error for internal model")
	}
}

func TestResolver_VisibleModelLeaves_NotFound(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = r.VisibleModelLeaves("unknown")
	if err == nil {
		t.Error("expected error for unknown model")
	}
}

func TestResolver_VisibleModelLeaves_Alias(t *testing.T) {
	falseVal := false
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"anthropic": {Endpoint: "https://api.anthropic.com", APIKey: "sk-ant-xxx", Protocols: []string{"anthropic"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:     "claude-fast",
				Exposure: exposurePtr(falseVal),
				Models:   config.ModelEntries{{Model: "anthropic/claude-sonnet-4-20250514", Weight: 1}},
			},
		},
		Redirect: config.RedirectConfigs{{Source: "claude-4-6-20261201", Target: "claude-fast"}},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// alias 可以展开叶子
	leaves, err := r.VisibleModelLeaves("claude-4-6-20261201")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(leaves) != 1 {
		t.Fatalf("expected 1 leaf, got %d", len(leaves))
	}
	if leaves[0].ProviderName != "anthropic" || leaves[0].UpstreamModel != "claude-sonnet-4-20250514" {
		t.Errorf("unexpected leaf: %+v", leaves[0])
	}

	// internal group 不能直接展开
	_, err = r.VisibleModelLeaves("claude-fast")
	if err == nil {
		t.Error("expected error for internal group accessed directly")
	}
}

func TestResolver_VisibleModelLeaves_MultiProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai":    {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
			"anthropic": {Endpoint: "https://api.anthropic.com", APIKey: "sk-ant-xxx", Protocols: []string{"anthropic"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "multi", Mode: "concurrent", Models: config.ModelEntries{
				{Model: "openai/gpt-4o", Weight: 1},
				{Model: "anthropic/claude-3-5-sonnet", Weight: 1},
			}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	leaves, err := r.VisibleModelLeaves("multi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(leaves) != 2 {
		t.Fatalf("expected 2 leaves, got %d", len(leaves))
	}
}

func TestResolver_VisibleModelLeaves_NestedGroup(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai":    {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
			"anthropic": {Endpoint: "https://api.anthropic.com", APIKey: "sk-ant-xxx", Protocols: []string{"anthropic"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "leaf-a", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "leaf-b", Models: config.ModelEntries{{Model: "anthropic/claude-3", Weight: 1}}},
			{Name: "parent", Mode: "failover", Models: config.ModelEntries{
				{Model: "leaf-a"},
				{Model: "leaf-b"},
			}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	leaves, err := r.VisibleModelLeaves("parent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(leaves) != 2 {
		t.Fatalf("expected 2 leaves from nested groups, got %d", len(leaves))
	}
}

func TestResolver_VisibleModelLeaves_Dedup(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			// 两个子 group 都引用同一个叶子
			{Name: "leaf-x", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "child-1", Models: config.ModelEntries{{Model: "leaf-x"}}},
			{Name: "child-2", Models: config.ModelEntries{{Model: "leaf-x"}}},
			{Name: "root", Mode: "failover", Models: config.ModelEntries{
				{Model: "child-1"},
				{Model: "child-2"},
			}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	leaves, err := r.VisibleModelLeaves("root")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 重复的 openai/gpt-4 只应出现一次
	if len(leaves) != 1 {
		t.Fatalf("expected 1 deduplicated leaf, got %d: %+v", len(leaves), leaves)
	}
	if leaves[0].ProviderName != "openai" || leaves[0].UpstreamModel != "gpt-4" {
		t.Errorf("unexpected leaf: %+v", leaves[0])
	}
}

// ========== Smart Route Resolver 测试 ==========

func TestNewResolver_SmartRoute_CheapCannotResolve(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4o", Model: "openai/gpt-4o"},
		},
		SmartRoute: &config.SmartRouteConfig{
			Cheap: "nonexistent-cheap",
			Scout: "gpt-4o",
		},
	}
	_, err := NewResolver(cfg)
	if err == nil {
		t.Fatal("expected error when smart_route.cheap cannot be resolved")
	}
	if !strings.Contains(err.Error(), "cannot be resolved") {
		t.Errorf("expected error mentioning cannot be resolved, got: %v", err)
	}
}

func TestNewResolver_SmartRoute_EnabledModelCannotResolve(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4o", Model: "openai/gpt-4o"},
			{Name: "cheap-group", Model: "openai/gpt-4o-mini"},
			{Name: "scout-group", Model: "openai/gpt-4o"},
		},
		Redirect: config.RedirectConfigs{{Source: "alias-a", Target: "gpt-4o"}},
		SmartRoute: &config.SmartRouteConfig{
			Cheap:         "cheap-group",
			Scout:         "scout-group",
			EnabledModels: []string{"missing-model"},
		},
	}
	_, err := NewResolver(cfg)
	if err == nil {
		t.Fatal("expected error when enabled_models contains unknown model")
	}
	if !strings.Contains(err.Error(), "smart_route.enabled_models \"missing-model\" cannot be resolved") {
		t.Errorf("expected error mentioning cannot be resolved, got: %v", err)
	}
}

func TestNewResolver_SmartRoute_Success(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "reason-group", Model: "openai/gpt-4o"},
			{Name: "cheap-group", Model: "openai/gpt-4o-mini"},
			{Name: "scout-group", Model: "openai/gpt-4o"},
		},
		Redirect: config.RedirectConfigs{{Source: "alias-a", Target: "reason-group"}, {Source: "alias-b", Target: "reason-group"}},
		SmartRoute: &config.SmartRouteConfig{
			Cheap:         "cheap-group",
			Scout:         "scout-group",
			EnabledModels: []string{"alias-a", "alias-b", "reason-group"},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 验证 smart route 索引已建立
	if !r.SmartRouteEnabled() {
		t.Error("expected SmartRouteEnabled to return true")
	}

	sri := r.GetSmartRouteIndex()
	if sri == nil {
		t.Fatal("expected GetSmartRouteIndex to return non-nil")
	}

	if sri.CheapGroup != "cheap-group" {
		t.Errorf("expected CheapGroup=cheap-group, got %q", sri.CheapGroup)
	}
	if sri.ScoutGroup != "scout-group" {
		t.Errorf("expected ScoutGroup=scout-group, got %q", sri.ScoutGroup)
	}
	// 验证入口模型信息
	infoA := r.GetAliasSmartRouteInfo("alias-a")
	if infoA == nil {
		t.Fatal("expected GetAliasSmartRouteInfo(alias-a) to return non-nil")
	}
	if infoA.DefaultGroup != "reason-group" {
		t.Errorf("expected DefaultGroup=reason-group, got %q", infoA.DefaultGroup)
	}

	infoGroup := r.GetAliasSmartRouteInfo("reason-group")
	if infoGroup == nil {
		t.Fatal("expected GetAliasSmartRouteInfo(reason-group) to return non-nil")
	}
	if infoGroup.DefaultGroup != "reason-group" {
		t.Errorf("expected DefaultGroup=reason-group, got %q", infoGroup.DefaultGroup)
	}

	// 未启用的 alias 应返回 nil
	infoC := r.GetAliasSmartRouteInfo("alias-c")
	if infoC != nil {
		t.Error("expected GetAliasSmartRouteInfo(alias-c) to return nil")
	}
}

func TestNewResolver_SmartRoute_TargetIsRedirectChain(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "final-group", Model: "openai/gpt-4o"},
			{Name: "cheap-group", Model: "openai/gpt-4o-mini"},
		},
		Redirect: config.RedirectConfigs{{Source: "alias-a", Target: "alias-b"}, {Source: "alias-b", Target: "final-group"}},
		SmartRoute: &config.SmartRouteConfig{
			Cheap:         "cheap-group",
			Scout:         "alias-b", // redirect chain
			EnabledModels: []string{"alias-a"},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sri := r.GetSmartRouteIndex()
	if sri == nil {
		t.Fatal("expected GetSmartRouteIndex to return non-nil")
	}

	// 应解析 redirect 链，得到最终 group
	if sri.ScoutGroup != "final-group" {
		t.Errorf("expected ScoutGroup=final-group, got %q", sri.ScoutGroup)
	}
	// alias-a 的默认 group 应解析到 final-group
	infoA := r.GetAliasSmartRouteInfo("alias-a")
	if infoA == nil {
		t.Fatal("expected GetAliasSmartRouteInfo(alias-a) to return non-nil")
	}
	if infoA.DefaultGroup != "final-group" {
		t.Errorf("expected DefaultGroup=final-group, got %q", infoA.DefaultGroup)
	}
}

func TestNewResolver_SmartRoute_DisabledWhenNil(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4o", Model: "openai/gpt-4o"},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r.SmartRouteEnabled() {
		t.Error("expected SmartRouteEnabled to return false when config is nil")
	}
	if r.GetSmartRouteIndex() != nil {
		t.Error("expected GetSmartRouteIndex to return nil when config is nil")
	}
}

// TestResolver_Exposure_ThreeStates 验证 public/hidden/internal 三档语义：
// - public：出现在 /v1/models 且可直调
// - hidden：不出现在 /v1/models，但可直调
// - internal：不出现在 /v1/models，且不可外部直调，但可内部引用
func TestResolver_Exposure_ThreeStates(t *testing.T) {
	hidden := config.ExposureHidden
	internal := config.ExposureInternal
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "pub", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "hid", Exposure: &hidden, Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "int", Exposure: &internal, Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// /v1/models 只列出 public
	models := r.ListUserModels()
	modelSet := make(map[string]bool)
	for _, m := range models {
		modelSet[m] = true
	}
	if !modelSet["pub"] {
		t.Error("expected public model in list")
	}
	if modelSet["hid"] || modelSet["int"] {
		t.Errorf("hidden/internal models must not appear in list: %v", models)
	}

	// 直调规则
	if _, err := r.Resolve("pub"); err != nil {
		t.Errorf("public should be resolvable: %v", err)
	}
	if _, err := r.Resolve("hid"); err != nil {
		t.Errorf("hidden should be resolvable: %v", err)
	}
	if _, err := r.Resolve("int"); err == nil {
		t.Error("internal should NOT be resolvable externally")
	}
	if _, err := r.ResolveInternal("int"); err != nil {
		t.Errorf("internal should be resolvable via ResolveInternal: %v", err)
	}

	// VisibleModelLeaves 直调规则与 Resolve 一致
	if _, err := r.VisibleModelLeaves("pub"); err != nil {
		t.Errorf("public leaves should be resolvable: %v", err)
	}
	if _, err := r.VisibleModelLeaves("hid"); err != nil {
		t.Errorf("hidden leaves should be resolvable: %v", err)
	}
	if _, err := r.VisibleModelLeaves("int"); err == nil {
		t.Error("internal leaves should NOT be resolvable externally")
	}
}

// TestResolver_Exposure_PublicAliasToInternalGroup 验证 public alias 指向 internal group 时，
// alias 自身出现在 /v1/models 且可直调，internal group 不展示也不可直调，但可被内部引用。
func TestResolver_Exposure_PublicAliasToInternalGroup(t *testing.T) {
	internal := config.ExposureInternal
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "backend", Exposure: &internal, Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{{Source: "frontend", Target: "backend"}},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 列表只出现 public alias，不出现 internal group
	models := r.ListUserModels()
	modelSet := make(map[string]bool)
	for _, m := range models {
		modelSet[m] = true
	}
	if !modelSet["frontend"] {
		t.Error("expected public alias in list")
	}
	if modelSet["backend"] {
		t.Error("internal group must not appear in list")
	}

	// alias 可直调（按 alias 自身入口名返回）
	if _, err := r.Resolve("frontend"); err != nil {
		t.Errorf("public alias should be resolvable: %v", err)
	}
	if _, err := r.VisibleModelLeaves("frontend"); err != nil {
		t.Errorf("public alias leaves should be resolvable: %v", err)
	}

	// internal group 不可外部直调
	if _, err := r.Resolve("backend"); err == nil {
		t.Error("internal group should NOT be resolvable externally")
	}
	if _, err := r.VisibleModelLeaves("backend"); err == nil {
		t.Error("internal group leaves should NOT be resolvable externally")
	}

	// internal group 仍可被其他 group 内部引用（plan 构建）
	internalRefCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "backend", Exposure: &internal, Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "wrapper", Mode: "failover", Models: config.ModelEntries{{Model: "backend"}}},
		},
	}
	r2, err := NewResolver(internalRefCfg)
	if err != nil {
		t.Fatalf("unexpected error building internal reference: %v", err)
	}
	if _, err := r2.Resolve("wrapper"); err != nil {
		t.Errorf("internal group should be referenceable by other groups: %v", err)
	}
}

// TestResolver_Exposure_RedirectThreeStates 验证 redirect 自身的三档 exposure：
// - public redirect：出现在 /v1/models 且可直调
// - hidden redirect：不出现在 /v1/models，但可直调
// - internal redirect：不出现在 /v1/models，且不可外部直调
func TestResolver_Exposure_RedirectThreeStates(t *testing.T) {
	hidden := config.ExposureHidden
	internal := config.ExposureInternal
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com/v1", APIKey: "sk-xxx", Protocols: []string{"openai"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "backend", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{
			{Source: "pub-r", Target: "backend"},
			{Source: "hid-r", Target: "backend", Exposure: &hidden},
			{Source: "int-r", Target: "backend", Exposure: &internal},
		},
	}

	r, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// /v1/models 只列出 public redirect
	models := r.ListUserModels()
	modelSet := make(map[string]bool)
	for _, m := range models {
		modelSet[m] = true
	}
	if !modelSet["pub-r"] {
		t.Error("expected pub-r in /v1/models")
	}
	if modelSet["hid-r"] {
		t.Error("hidden redirect should not appear in /v1/models")
	}
	if modelSet["int-r"] {
		t.Error("internal redirect should not appear in /v1/models")
	}

	// 直调规则
	if _, err := r.Resolve("pub-r"); err != nil {
		t.Errorf("public redirect should be resolvable: %v", err)
	}
	if _, err := r.Resolve("hid-r"); err != nil {
		t.Errorf("hidden redirect should be resolvable: %v", err)
	}
	if _, err := r.Resolve("int-r"); err == nil {
		t.Error("internal redirect should NOT be resolvable externally")
	}

	// VisibleModelLeaves 直调规则与 Resolve 一致
	if _, err := r.VisibleModelLeaves("pub-r"); err != nil {
		t.Errorf("public redirect leaves should be resolvable: %v", err)
	}
	if _, err := r.VisibleModelLeaves("hid-r"); err != nil {
		t.Errorf("hidden redirect leaves should be resolvable: %v", err)
	}
	if _, err := r.VisibleModelLeaves("int-r"); err == nil {
		t.Error("internal redirect leaves should NOT be resolvable externally")
	}
}
