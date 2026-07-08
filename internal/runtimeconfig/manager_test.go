package runtimeconfig

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"gopkg.in/yaml.v3"
)

// exposurePtr 将 bool 映射为 *config.Exposure（true=public, false=internal）。
func exposurePtr(b bool) *config.Exposure {
	if b {
		v := config.ExposurePublic
		return &v
	}
	v := config.ExposureInternal
	return &v
}

func createTestConfig(t *testing.T) (*config.Config, string) {
	t.Helper()

	yamlContent := `
server:
  listen: ":18000"
inbound:
  auth:
    keys:
      - name: "test"
        key: "sk-test"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "gpt-4o"
    models:
      - "openai/gpt-4o"
  - name: "unused"
    models:
      - "openai/gpt-4o"
redirect:
  gpt-4: gpt-4o
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	return cfg, configPath
}

func TestManagerCreateModelGroup(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 测试创建成功
	input := &ModelGroupInput{
		Name:  "new-model",
		Model: "openai/gpt-4",
	}

	result, err := mgr.CreateModelGroup(input)
	if err != nil {
		t.Fatalf("create model group: %v", err)
	}

	if result.Name != "new-model" {
		t.Errorf("expected name 'new-model', got %q", result.Name)
	}

	// 测试 draft 已更新
	groups := mgr.ListModelGroups()
	found := false
	for _, g := range groups {
		if g.Name == "new-model" {
			found = true
			break
		}
	}
	if !found {
		t.Error("new model group not found in draft")
	}

	// 测试 active 未更新
	activeGroups := mgr.GetActive().ModelGroups
	for _, g := range activeGroups {
		if g.Name == "new-model" {
			t.Error("new model group should not exist in active before apply")
		}
	}
}

func TestManagerCreateModelGroupConflict(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 测试重复名称
	input := &ModelGroupInput{
		Name:  "gpt-4o", // 已存在
		Model: "openai/gpt-4",
	}

	_, createErr := mgr.CreateModelGroup(input)
	if createErr == nil {
		t.Error("expected conflict error")
	} else {
		if e, ok := createErr.(*Error); ok && e.Code != ErrCodeConflict {
			t.Errorf("expected conflict error code, got %s", e.Code)
		}
	}

	// 测试与 redirect key 冲突
	input2 := &ModelGroupInput{
		Name:  "gpt-4", // redirect key
		Model: "openai/gpt-4",
	}

	_, createErr = mgr.CreateModelGroup(input2)
	if createErr == nil {
		t.Error("expected conflict error")
	} else {
		if e, ok := createErr.(*Error); ok && e.Code != ErrCodeConflict {
			t.Errorf("expected conflict error code, got %s", e.Code)
		}
	}
}

func TestManagerUpdateModelGroup(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 更新 model group
	input := &ModelGroupInput{
		Name:  "gpt-4o",
		Model: "openai/gpt-4-turbo",
	}

	result, err := mgr.UpdateModelGroup("gpt-4o", input)
	if err != nil {
		t.Fatalf("update model group: %v", err)
	}

	if result.Name != "gpt-4o" {
		t.Errorf("expected name 'gpt-4o', got %q", result.Name)
	}

	// 验证 draft 已更新
	group, found := mgr.GetModelGroup("gpt-4o")
	if !found {
		t.Fatal("model group not found")
	}
	if len(group.Models) != 1 || group.Models[0].Model != "openai/gpt-4-turbo" {
		t.Errorf("expected model 'openai/gpt-4-turbo', got %q", group.Models[0].Model)
	}
}

func TestManagerUpdateModelGroupRename(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 改名并检查传播
	input := &ModelGroupInput{
		Name:  "gpt-4o-new",
		Model: "openai/gpt-4o",
	}

	_, err = mgr.UpdateModelGroup("gpt-4o", input)
	if err != nil {
		t.Fatalf("update model group: %v", err)
	}

	// 验证 redirect 已更新
	draft := mgr.GetDraft()
	found := false
	for _, rc := range draft.Redirect {
		if rc.Source == "gpt-4" {
			if rc.Target != "gpt-4o-new" {
				t.Errorf("expected redirect value 'gpt-4o-new', got %q", rc.Target)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("redirect source 'gpt-4' not found")
	}
}

func TestManagerDeleteModelGroup(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 删除不存在的 group
	delErr := mgr.DeleteModelGroup("nonexistent")
	if delErr == nil {
		t.Error("expected not found error")
	} else {
		if e, ok := delErr.(*Error); ok && e.Code != ErrCodeNotFound {
			t.Errorf("expected not found error code, got %s", e.Code)
		}
	}

	// 删除存在的 group（未被引用）
	delErr = mgr.DeleteModelGroup("unused")
	if delErr != nil {
		t.Fatalf("delete model group: %v", delErr)
	}

	// 验证 draft 已更新
	_, found := mgr.GetModelGroup("unused")
	if found {
		t.Error("model group should be deleted from draft")
	}
}

func TestManagerDeleteModelGroupReferenced(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// gpt-4o 被 redirect gpt-4 引用
	delErr := mgr.DeleteModelGroup("gpt-4o")
	if delErr == nil {
		t.Error("expected conflict error for referenced group")
	} else {
		if e, ok := delErr.(*Error); ok && e.Code != ErrCodeConflict {
			t.Errorf("expected conflict error code, got %s", e.Code)
		}
	}
}

func TestManagerApply(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{shouldSucceed: true}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 先创建一个新 model group
	input := &ModelGroupInput{
		Name:  "new-model",
		Model: "openai/gpt-4",
	}

	_, err = mgr.CreateModelGroup(input)
	if err != nil {
		t.Fatalf("create model group: %v", err)
	}

	// 执行 apply
	result := mgr.Apply()
	if !result.Success {
		t.Fatalf("apply failed: %s", result.Message)
	}

	// 验证 rebuilder 和 reinit 被调用
	if !rebuilder.called {
		t.Error("rebuilder should be called")
	}
	if !reinit.called {
		t.Error("reinit should be called")
	}

	// 验证 active 已更新
	active := mgr.GetActive()
	found := false
	for _, g := range active.ModelGroups {
		if g.Name == "new-model" {
			found = true
			break
		}
	}
	if !found {
		t.Error("new model group should exist in active after apply")
	}

	// 验证配置文件已更新
	reloadCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	found = false
	for _, g := range reloadCfg.ModelGroups {
		if g.Name == "new-model" {
			found = true
			break
		}
	}
	if !found {
		t.Error("new model group should exist in config file after apply")
	}
}
func TestManagerApplyValidationFailed(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{shouldSucceed: true}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 创建一个无效的 model group（引用不存在的 provider）
	invalidGroup := config.ModelGroupConfig{
		Name:   "invalid-group",
		Models: config.ModelEntries{{Model: "nonexistent/model"}},
	}
	mgr.mu.Lock()
	mgr.draft.ModelGroups = append(mgr.draft.ModelGroups, invalidGroup)
	mgr.mu.Unlock()

	// 执行 apply，应该因验证失败而返回错误
	result := mgr.Apply()
	if result.Success {
		t.Error("expected apply to fail due to validation error")
	}

	// 验证 active 未更新
	active := mgr.GetActive()
	for _, g := range active.ModelGroups {
		if g.Name == "invalid-group" {
			t.Error("invalid model group should not exist in active after failed apply")
		}
	}

	// 验证 draft 仍然保留无效 group（验证失败不应修改 draft）
	draft := mgr.GetDraft()
	found := false
	for _, g := range draft.ModelGroups {
		if g.Name == "invalid-group" {
			found = true
			break
		}
	}
	if !found {
		t.Error("invalid model group should still exist in draft after validation failed")
	}
}

func TestManagerApplyRebuildFailed(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{shouldSucceed: false} // rebuild 会失败
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 创建一个新 model group
	input := &ModelGroupInput{
		Name:  "new-model",
		Model: "openai/gpt-4",
	}

	_, err = mgr.CreateModelGroup(input)
	if err != nil {
		t.Fatalf("create model group: %v", err)
	}

	// 执行 apply，应该因 rebuild 失败而返回错误
	result := mgr.Apply()
	if result.Success {
		t.Error("expected apply to fail due to rebuild error")
	}

	// 验证 active 未更新
	active := mgr.GetActive()
	for _, g := range active.ModelGroups {
		if g.Name == "new-model" {
			t.Error("new model group should not exist in active after failed apply")
		}
	}

	// 验证配置文件未更新（rebuild 失败不应写入文件）
	reloadCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	for _, g := range reloadCfg.ModelGroups {
		if g.Name == "new-model" {
			t.Error("new model group should not exist in config file after failed apply")
		}
	}

	// 验证 draft 仍然保留新 group（rebuild 失败不应修改 draft）
	draft := mgr.GetDraft()
	found := false
	for _, g := range draft.ModelGroups {
		if g.Name == "new-model" {
			found = true
			break
		}
	}
	if !found {
		t.Error("new model group should still exist in draft after rebuild failed")
	}
}

func TestManagerApplySaveFailedASTRecovery(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{shouldSucceed: true}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 创建一个新 model group
	input := &ModelGroupInput{
		Name:  "new-model",
		Model: "openai/gpt-4",
	}

	_, err = mgr.CreateModelGroup(input)
	if err != nil {
		t.Fatalf("create model group: %v", err)
	}

	// 保存原始 AST 状态
	mgr.mu.RLock()
	originalGroupsCount := len(mgr.store.rootNode.Content[0].Content)
	mgr.mu.RUnlock()

	// 使配置目录只读，导致 Save 失败
	if err := os.Chmod(filepath.Dir(configPath), 0555); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	defer os.Chmod(filepath.Dir(configPath), 0755) // 恢复权限

	// 执行 apply，应该因 Save 失败而返回错误
	result := mgr.Apply()
	if result.Success {
		t.Error("expected apply to fail due to save error")
	}

	// 恢复目录权限，以便后续操作
	if err := os.Chmod(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("restore dir permissions: %v", err)
	}

	// 验证 AST 已通过 Reload 恢复
	mgr.mu.RLock()
	recoveredGroupsCount := len(mgr.store.rootNode.Content[0].Content)
	mgr.mu.RUnlock()

	// AST 应该恢复到原始状态（新增的 new-model 被移除）
	if recoveredGroupsCount != originalGroupsCount {
		t.Errorf("AST content count mismatch: expected %d, got %d", originalGroupsCount, recoveredGroupsCount)
	}

	// 验证 new-model 不在 AST 的 model_groups 中
	mgr.mu.RLock()
	modelGroupsNode, exists := findSequenceNode(mgr.store.rootNode.Content[0], "model_groups")
	mgr.mu.RUnlock()
	if exists {
		for _, item := range modelGroupsNode.Content {
			if item.Kind != yaml.MappingNode {
				continue
			}
			nameNode, ok := findMappingNode(item, "name")
			if ok && nameNode.Value == "new-model" {
				t.Error("new-model should not exist in AST after failed apply")
			}
		}
	}

	// 清理 draft 中的 new-model
	mgr.mu.Lock()
	for i, g := range mgr.draft.ModelGroups {
		if g.Name == "new-model" {
			mgr.draft.ModelGroups = append(mgr.draft.ModelGroups[:i], mgr.draft.ModelGroups[i+1:]...)
			break
		}
	}
	mgr.mu.Unlock()

	// 再次 apply 应该能正常工作（AST 未被污染）
	input2 := &ModelGroupInput{
		Name:  "valid-model",
		Model: "openai/gpt-4",
	}

	_, err = mgr.CreateModelGroup(input2)
	if err != nil {
		t.Fatalf("create valid model group: %v", err)
	}

	result = mgr.Apply()
	if !result.Success {
		t.Errorf("second apply should succeed after AST recovery: %s", result.Message)
	}
}
func TestManagerApplyConcurrent(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{shouldSucceed: true}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 并发 apply
	var wg sync.WaitGroup
	results := make([]ApplyResult, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = mgr.Apply()
		}(i)
	}

	wg.Wait()

	// 所有 apply 都应该成功（串行执行）
	for i, result := range results {
		if !result.Success {
			t.Errorf("apply %d failed: %s", i, result.Message)
		}
	}
}

func TestManagerDiscardDraft(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 创建新 model group
	input := &ModelGroupInput{
		Name:  "new-model",
		Model: "openai/gpt-4",
	}

	_, err = mgr.CreateModelGroup(input)
	if err != nil {
		t.Fatalf("create model group: %v", err)
	}

	// 验证 draft 已更新
	groups := mgr.ListModelGroups()
	if len(groups) != 3 {
		t.Errorf("expected 3 model groups in draft, got %d", len(groups))
	}

	// Discard draft
	mgr.DiscardDraft()

	// 验证 draft 已重置为 active
	groups = mgr.ListModelGroups()
	if len(groups) != 2 {
		t.Errorf("expected 2 model groups in draft after discard, got %d", len(groups))
	}

	_, found := mgr.GetModelGroup("new-model")
	if found {
		t.Error("new model group should not exist after discard")
	}
}
func TestManagerGetDraftModelGroupCRUD(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 获取 CRUD 服务
	crud := mgr.GetDraftModelGroupCRUD()
	if crud == nil {
		t.Error("GetDraftModelGroupCRUD should return non-nil CRUD service")
	}

	// 验证 CRUD 服务可用
	groups := crud.List()
	if len(groups) != 2 {
		t.Errorf("expected 2 model groups, got %d", len(groups))
	}
}

func TestManagerResetDraftToActive(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 创建新 model group
	input := &ModelGroupInput{
		Name:  "new-model",
		Model: "openai/gpt-4",
	}

	_, err = mgr.CreateModelGroup(input)
	if err != nil {
		t.Fatalf("create model group: %v", err)
	}

	// 验证 draft 已更新
	groups := mgr.ListModelGroups()
	if len(groups) != 3 {
		t.Errorf("expected 3 model groups in draft, got %d", len(groups))
	}

	// ResetDraftToActive (alias of DiscardDraft)
	mgr.ResetDraftToActive()

	// 验证 draft 已重置为 active
	groups = mgr.ListModelGroups()
	if len(groups) != 2 {
		t.Errorf("expected 2 model groups in draft after reset, got %d", len(groups))
	}
}

func TestErrorMethod(t *testing.T) {
	// 测试 Error 方法的两种形式
	errWithField := &Error{Code: ErrCodeBadRequest, Message: "invalid value", Field: "name"}
	if errWithField.Error() != "name: invalid value" {
		t.Errorf("expected 'name: invalid value', got '%s'", errWithField.Error())
	}

	errWithoutField := &Error{Code: ErrCodeBadRequest, Message: "invalid value"}
	if errWithoutField.Error() != "invalid value" {
		t.Errorf("expected 'invalid value', got '%s'", errWithoutField.Error())
	}
}

func TestToOutput(t *testing.T) {
	visible := false
	contextLength := 4096

	cfgGroup := config.ModelGroupConfig{
		Name:    "test-group",
		Mode:    "concurrent",
		Exposure: exposurePtr(visible),
		Models: []config.ModelEntry{
			{Model: "openai/gpt-4", Weight: 2},
		},
		ModelMetadata: config.ModelMetadataConfig{
			ContextLength: &contextLength,
		},
	}

	output := ToOutput(cfgGroup)

	if output.Name != "test-group" {
		t.Errorf("expected name 'test-group', got '%s'", output.Name)
	}
	if output.Mode != "concurrent" {
		t.Errorf("expected mode 'concurrent', got '%s'", output.Mode)
	}
	if len(output.Models) != 1 {
		t.Errorf("expected 1 model, got %d", len(output.Models))
	}
	if output.Models[0].Model != "openai/gpt-4" {
		t.Errorf("expected model 'openai/gpt-4', got '%s'", output.Models[0].Model)
	}
	if output.Models[0].Weight != 2 {
		t.Errorf("expected weight 2, got %d", output.Models[0].Weight)
	}
	if output.Exposure != config.ExposureInternal {
		t.Errorf("expected exposure internal, got %q", output.Exposure)
	}
	if output.ModelMetadata == nil || *output.ModelMetadata.ContextLength != 4096 {
		t.Errorf("expected context_length 4096")
	}
}

func TestManagerActiveKeys(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{shouldSucceed: true}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 初始 active 有 1 个 key
	keys := mgr.ActiveKeys()
	if len(keys) != 1 || keys[0].Name != "test" {
		t.Fatalf("initial ActiveKeys = %+v, want 1 key named test", keys)
	}

	// 在 draft 创建新 key
	err = mgr.CreateAuthKey(&AuthKeyInput{Name: "new", Key: "sk-new"})
	if err != nil {
		t.Fatalf("create auth key: %v", err)
	}

	// Apply 前 ActiveKeys 仍是旧的
	keys = mgr.ActiveKeys()
	if len(keys) != 1 {
		t.Errorf("ActiveKeys before apply: len = %d, want 1", len(keys))
	}

	// Apply 后 ActiveKeys 立即返回新值
	result := mgr.Apply()
	if !result.Success {
		t.Fatalf("apply failed: %s", result.Message)
	}

	keys = mgr.ActiveKeys()
	if len(keys) != 2 {
		t.Fatalf("ActiveKeys after apply: len = %d, want 2", len(keys))
	}
	found := false
	for _, k := range keys {
		if k.Name == "new" && k.Key == "sk-new" {
			found = true
		}
	}
	if !found {
		t.Error("new key not found in ActiveKeys after apply")
	}
}

func TestDeepCopyConfigAuthKeysIsolation(t *testing.T) {
	cfg := &config.Config{
		Inbound: config.InboundConfig{
			Auth: config.AuthConfig{
				Keys: []config.KeyConfig{
					{Name: "k1", Key: "sk-1"},
				},
			},
		},
	}

	copy := deepCopyConfig(cfg)

	// 修改原 config 的 keys
	cfg.Inbound.Auth.Keys[0].Key = "sk-modified"
	cfg.Inbound.Auth.Keys = append(cfg.Inbound.Auth.Keys, config.KeyConfig{Name: "k2", Key: "sk-2"})

	// copy 不受影响
	if len(copy.Inbound.Auth.Keys) != 1 {
		t.Fatalf("copy keys len = %d, want 1", len(copy.Inbound.Auth.Keys))
	}
	if copy.Inbound.Auth.Keys[0].Key != "sk-1" {
		t.Errorf("copy key = %q, want %q (should be isolated)", copy.Inbound.Auth.Keys[0].Key, "sk-1")
	}
}

func TestManagerAuthKeyCRUD(t *testing.T) {
	cfg, configPath := createTestConfig(t)

	rebuilder := &mockRebuilder{}
	reinit := &mockReinit{}

	mgr, err := NewManager(cfg, configPath, rebuilder, reinit)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// 初始有 1 个 key (test)
	keys := mgr.ListAuthKeys()
	if len(keys) != 1 || keys[0].Name != "test" {
		t.Fatalf("initial keys = %+v, want 1 key named test", keys)
	}

	// Create
	err = mgr.CreateAuthKey(&AuthKeyInput{Name: "k2", Key: "sk-2"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Get
	got, found := mgr.GetAuthKey("k2")
	if !found || got.Key != "sk-2" {
		t.Errorf("get k2 = %+v, want {k2 sk-2}", got)
	}

	// Get not found
	_, found = mgr.GetAuthKey("nope")
	if found {
		t.Error("should not find non-existent key")
	}

	// Update（改 key 值）
	err = mgr.UpdateAuthKey("k2", &AuthKeyInput{Name: "k2", Key: "sk-updated"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = mgr.GetAuthKey("k2")
	if got.Key != "sk-updated" {
		t.Errorf("after update: key = %q, want sk-updated", got.Key)
	}

	// Update 改名
	err = mgr.UpdateAuthKey("k2", &AuthKeyInput{Name: "k2-renamed", Key: "sk-updated"})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, found := mgr.GetAuthKey("k2"); found {
		t.Error("old name should not exist after rename")
	}
	if _, found := mgr.GetAuthKey("k2-renamed"); !found {
		t.Error("new name should exist after rename")
	}

	// Delete
	err = mgr.DeleteAuthKey("k2-renamed")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(mgr.ListAuthKeys()) != 1 {
		t.Errorf("after delete: keys len = %d, want 1", len(mgr.ListAuthKeys()))
	}

	// Delete not found
	err = mgr.DeleteAuthKey("nope")
	if err == nil {
		t.Fatal("expected error for delete not found")
	}
}

// Mock implementations

type mockRebuilder struct {
	shouldSucceed bool
	called        bool
}

func (r *mockRebuilder) Rebuild(cfg *config.Config) (*model.Resolver, *scheduler.Scheduler, error) {
	r.called = true
	if !r.shouldSucceed {
		return nil, nil, os.ErrNotExist
	}
	// 返回空的 resolver 和 scheduler
	return &model.Resolver{}, &scheduler.Scheduler{}, nil
}

func (r *mockRebuilder) RebuildResolver(cfg *config.Config) (*model.Resolver, error) {
	r.called = true
	if !r.shouldSucceed {
		return nil, os.ErrNotExist
	}
	return &model.Resolver{}, nil
}

type mockReinit struct {
	called bool
}

func (r *mockReinit) Reinit(cfg *config.Config, resolver *model.Resolver, sched *scheduler.Scheduler) {
	r.called = true
}