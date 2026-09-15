package runtimeconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func TestYamlStoreUpdateModelGroup(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		update     config.ModelGroupConfig
		wantModel  string // 检查写回格式：model 或 models
		wantModels []config.ModelEntry
	}{
		{
			name: "update existing group",
			yaml: `
server:
  listen: ":18000"
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
`,
			update: config.ModelGroupConfig{
				Name:   "gpt-4o",
				Models: config.ModelEntries{{Model: "openai/gpt-4-turbo", Weight: 1}},
			},
			wantModel:  "model", // 单模型应写回 model
			wantModels: []config.ModelEntry{{Model: "openai/gpt-4-turbo", Weight: 1}},
		},
		{
			name: "add new group",
			yaml: `
server:
  listen: ":18000"
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
`,
			update: config.ModelGroupConfig{
				Name:   "new-model",
				Models: config.ModelEntries{{Model: "anthropic/claude", Weight: 1}},
			},
			wantModel:  "model",
			wantModels: []config.ModelEntry{{Model: "anthropic/claude", Weight: 1}},
		},
		{
			name: "multi models should write as models",
			yaml: `
server:
  listen: ":18000"
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
`,
			update: config.ModelGroupConfig{
				Name:   "gpt-4o",
				Models: config.ModelEntries{{Model: "openai/gpt-4o", Weight: 1}, {Model: "anthropic/claude", Weight: 2}},
			},
			wantModel:  "models",
			wantModels: []config.ModelEntry{{Model: "openai/gpt-4o", Weight: 1}, {Model: "anthropic/claude", Weight: 2}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(tt.yaml), 0644); err != nil {
				t.Fatalf("write config: %v", err)
			}

			store, err := NewYamlStore(configPath)
			if err != nil {
				t.Fatalf("create store: %v", err)
			}

			if err := store.UpdateModelGroup(tt.update); err != nil {
				t.Fatalf("update model group: %v", err)
			}

			if err := store.Save(); err != nil {
				t.Fatalf("save: %v", err)
			}

			// 验证配置文件
			data, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("read config: %v", err)
			}

			// 检查格式
			if tt.wantModel == "model" && strings.Contains(string(data), "models:") {
				t.Error("expected single model format, got models array")
			}
			if tt.wantModel == "models" && !strings.Contains(string(data), "models:") {
				t.Error("expected models array format, got single model")
			}

			// 重新加载验证
			cfg, err := config.Load(configPath)
			if err != nil {
				t.Fatalf("reload config: %v", err)
			}

			var found bool
			for _, g := range cfg.ModelGroups {
				if g.Name == tt.update.Name {
					found = true
					if len(g.Models) != len(tt.wantModels) {
						t.Errorf("expected %d models, got %d", len(tt.wantModels), len(g.Models))
					}
					break
				}
			}
			if !found {
				t.Error("model group not found in reloaded config")
			}
		})
	}
}

func TestYamlStoreDeleteModelGroup(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
  - name: "claude"
    model: "anthropic/claude"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	if err := store.DeleteModelGroup("gpt-4o"); err != nil {
		t.Fatalf("delete model group: %v", err)
	}

	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// 验证
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}

	if len(cfg.ModelGroups) != 1 {
		t.Errorf("expected 1 model group, got %d", len(cfg.ModelGroups))
	}
	if len(cfg.ModelGroups) > 0 && cfg.ModelGroups[0].Name != "claude" {
		t.Errorf("expected 'claude', got %q", cfg.ModelGroups[0].Name)
	}
}

func TestYamlStoreSyncFromConfig(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
model_groups:
  - name: "old"
    model: "openai/gpt-3"
redirect:
  old-alias: old
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	// 创建新配置
	newCfg := &config.Config{
		Server: config.ServerConfig{
			Listen: ":18000",
		},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "new", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{
			{Source: "new-alias", Target: "new"},
		},
	}

	store.SyncFromConfig(newCfg)
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// 验证
	reloadCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}

	if len(reloadCfg.ModelGroups) != 1 || reloadCfg.ModelGroups[0].Name != "new" {
		t.Errorf("expected single model group 'new', got %v", reloadCfg.ModelGroups)
	}
	found := false
	for _, rc := range reloadCfg.Redirect {
		if rc.Source == "new-alias" {
			if rc.Target != "new" {
				t.Errorf("expected redirect 'new', got %q", rc.Target)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("redirect 'new-alias' not found")
	}
}

func TestYamlStorePreservesComments(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
  # 这是一个注释
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
  # 另一个注释
  - name: "claude"
    model: "anthropic/claude"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	// 更新配置
	store.UpdateModelGroup(config.ModelGroupConfig{
		Name:   "new-model",
		Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}},
	})

	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// 读取并检查注释
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "# 这是一个注释") {
		t.Error("comment '这是一个注释' should be preserved")
	}
	if !strings.Contains(content, "# 另一个注释") {
		t.Error("comment '另一个注释' should be preserved")
	}
}

func TestYamlStoreAtomicSave(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	// 保存后临时文件应被删除
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	tmpPath := configPath + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("temp file %q should be removed after save", tmpPath)
	}
}

func TestYamlStoreGetConfigPath(t *testing.T) {
	yaml := `
model_groups:
  - name: "test"
    models:
      - "openai/gpt-4"
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	if store.GetConfigPath() != configPath {
		t.Errorf("expected config path %q, got %q", configPath, store.GetConfigPath())
	}
}

func TestYamlStoreBackupRestore(t *testing.T) {
	yaml := `
model_groups:
  - name: "test"
    models:
      - "openai/gpt-4"
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	// 创建备份
	backupPath, err := store.Backup()
	if err != nil {
		t.Fatalf("backup: %v", err)
	}

	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Errorf("backup file %q should exist", backupPath)
	}

	// 修改配置
	modifiedYaml := []byte(`
model_groups:
  - name: "modified"
    models:
      - "anthropic/claude"
`)
	if err := os.WriteFile(configPath, modifiedYaml, 0644); err != nil {
		t.Fatalf("write modified config: %v", err)
	}

	// 从备份恢复
	if err := store.Restore(backupPath); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// 验证恢复后的内容
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read restored config: %v", err)
	}

	if !strings.Contains(string(data), "test") {
		t.Error("restored config should contain 'test'")
	}
}

func TestYamlStoreLoadConfig(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
model_groups:
  - name: "test"
    models:
      - "openai/gpt-4"
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if len(cfg.ModelGroups) != 1 {
		t.Errorf("expected 1 model group, got %d", len(cfg.ModelGroups))
	}
	if cfg.ModelGroups[0].Name != "test" {
		t.Errorf("expected model group name 'test', got '%s'", cfg.ModelGroups[0].Name)
	}
}

func TestYamlStoreSaveConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "subdir", "config.yaml")

	cfg := &config.Config{
		ModelGroups: []config.ModelGroupConfig{
			{Name: "test", Models: []config.ModelEntry{{Model: "openai/gpt-4"}}},
		},
	}

	if err := SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Errorf("config file %q should exist", configPath)
	}

	// 验证内容
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}

	if !strings.Contains(string(data), "test") {
		t.Error("saved config should contain 'test'")
	}
}

func TestYamlStoreSyncProviders(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
providers:
  openai:
    endpoint: "https://api.openai.com"
    api_key: "sk-old"
    protocols:
      - "openai.chat"
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	// 更新 providers
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{
				"openai": {
					Endpoint:  "https://api.openai.com/v2",
					APIKey:    "sk-new",
					Protocols: []string{"openai.chat"},
				},
			},
		},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4o", Models: config.ModelEntries{{Model: "openai/gpt-4o"}}},
		},
	}

	store.SyncFromConfig(cfg)
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// 验证配置文件
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	t.Logf("Generated YAML:\n%s", string(data))

	// 检查 openai 更新
	if !strings.Contains(string(data), "sk-new") {
		t.Error("expected 'sk-new' in config")
	}
	if !strings.Contains(string(data), "https://api.openai.com/v2") {
		t.Error("expected updated endpoint in config")
	}

	// 重新加载验证
	reloadCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}

	if len(reloadCfg.Providers.Items) != 1 {
		t.Errorf("expected 1 provider, got %d", len(reloadCfg.Providers.Items))
	}

	if reloadCfg.Providers.Items["openai"].APIKey != "sk-new" {
		t.Errorf("expected openai api_key 'sk-new', got '%s'", reloadCfg.Providers.Items["openai"].APIKey)
	}
}

func TestYamlStoreNormalizeProviderProtocols(t *testing.T) {
	yamlContent := `
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai"]
    endpoints:
      - url: "https://api.openai.com/v1"
        protocols: ["anthropic"]
  anthropic:
    endpoint: "https://api.anthropic.com"
    api_key: "sk-yyy"
    protocols: ["anthropic.messages"]
  empty-provider:
    endpoint: "https://example.com"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	changed := store.NormalizeProviderProtocols(config.ResolveProtocolAlias)
	if !changed {
		t.Fatal("expected changed=true when normalizing shorthand protocols")
	}

	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// 重新加载验证所有简写已被替换为全名
	reloadCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}

	openai := reloadCfg.Providers.Items["openai"]
	if openai.Protocols[0] != "openai.chat" {
		t.Errorf("openai.Protocols[0] = %q, want openai.chat", openai.Protocols[0])
	}
	if openai.Endpoints[0].Protocols[0] != "anthropic.messages" {
		t.Errorf("openai.Endpoints[0].Protocols[0] = %q, want anthropic.messages", openai.Endpoints[0].Protocols[0])
	}

	// anthropic 已是全名，不应变化
	anthropic := reloadCfg.Providers.Items["anthropic"]
	if anthropic.Protocols[0] != "anthropic.messages" {
		t.Errorf("anthropic.Protocols[0] = %q, want anthropic.messages", anthropic.Protocols[0])
	}

	// empty-provider 无协议字段，不应 panic
	if _, ok := reloadCfg.Providers.Items["empty-provider"]; !ok {
		t.Error("empty-provider should exist")
	}

	// 再次调用应返回 false（已全部规范化，无变化）
	changedAgain := store.NormalizeProviderProtocols(config.ResolveProtocolAlias)
	if changedAgain {
		t.Error("expected changed=false on second call (already normalized)")
	}
}

func TestYamlStoreNormalizeProviderProtocols_NoProvidersNode(t *testing.T) {
	yamlContent := `
server:
  listen: ":18000"
model_groups:
  - name: "test"
    model: "openai/gpt-4o"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	// 缺失 providers 节点不应 panic，返回 false
	if changed := store.NormalizeProviderProtocols(config.ResolveProtocolAlias); changed {
		t.Error("expected changed=false when providers node missing")
	}
}

func TestSyncFromConfigAuthKeys(t *testing.T) {
	t.Run("sync existing inbound", func(t *testing.T) {
		yamlContent := `
server:
  listen: ":18000"
inbound:
  auth:
    keys:
      - name: "old"
        key: "sk-old"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
`
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte(yamlContent), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		store, err := NewYamlStore(configPath)
		if err != nil {
			t.Fatalf("create store: %v", err)
		}

		cfg, err := config.Load(configPath)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}

		// 修改 keys
		cfg.Inbound.Auth.Keys = []config.KeyConfig{
			{Name: "k1", Key: "sk-1"},
			{Name: "k2", Key: "sk-2"},
		}

		store.SyncFromConfig(cfg)
		if err := store.Save(); err != nil {
			t.Fatalf("save: %v", err)
		}

		// 重新 Load 验证
		reloadCfg, err := config.Load(configPath)
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		if len(reloadCfg.Inbound.Auth.Keys) != 2 {
			t.Fatalf("reloaded keys len = %d, want 2", len(reloadCfg.Inbound.Auth.Keys))
		}
		if reloadCfg.Inbound.Auth.Keys[0].Name != "k1" || reloadCfg.Inbound.Auth.Keys[0].Key != "sk-1" {
			t.Errorf("reloaded key[0] = %+v, want {k1 sk-1}", reloadCfg.Inbound.Auth.Keys[0])
		}
		if reloadCfg.Inbound.Auth.Keys[1].Name != "k2" || reloadCfg.Inbound.Auth.Keys[1].Key != "sk-2" {
			t.Errorf("reloaded key[1] = %+v, want {k2 sk-2}", reloadCfg.Inbound.Auth.Keys[1])
		}
	})

	t.Run("create inbound when missing", func(t *testing.T) {
		yamlContent := `
server:
  listen: ":18000"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
`
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte(yamlContent), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		store, err := NewYamlStore(configPath)
		if err != nil {
			t.Fatalf("create store: %v", err)
		}

		cfg, err := config.Load(configPath)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}

		// 添加 keys（原配置无 inbound 节点）
		cfg.Inbound.Auth.Keys = []config.KeyConfig{
			{Name: "new", Key: "sk-new"},
		}

		store.SyncFromConfig(cfg)
		if err := store.Save(); err != nil {
			t.Fatalf("save: %v", err)
		}

		// 重新 Load 验证
		reloadCfg, err := config.Load(configPath)
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		if len(reloadCfg.Inbound.Auth.Keys) != 1 {
			t.Fatalf("reloaded keys len = %d, want 1", len(reloadCfg.Inbound.Auth.Keys))
		}
		if reloadCfg.Inbound.Auth.Keys[0].Name != "new" || reloadCfg.Inbound.Auth.Keys[0].Key != "sk-new" {
			t.Errorf("reloaded key = %+v, want {new sk-new}", reloadCfg.Inbound.Auth.Keys[0])
		}
	})
}

// TestYamlStore_SyncFromConfig_WritesExposure 验证 draft apply 后 YAML 落盘写出
// exposure 字段，且不再写出 visible 字段。
func TestYamlStore_SyncFromConfig_WritesExposure(t *testing.T) {
	hidden := config.ExposureHidden
	internal := config.ExposureInternal

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	initial := `
server:
  listen: ":18000"
model_groups:
  - name: "pub"
    model: "openai/gpt-4"
redirect:
  - source: "alias"
    target: "pub"
`
	if err := os.WriteFile(configPath, []byte(initial), 0644); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("NewYamlStore: %v", err)
	}

	cfg := &config.Config{
		ModelGroups: []config.ModelGroupConfig{
			{Name: "pub", Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "hid", Exposure: &hidden, Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
			{Name: "int", Exposure: &internal, Models: config.ModelEntries{{Model: "openai/gpt-4", Weight: 1}}},
		},
		Redirect: config.RedirectConfigs{
			{Source: "alias", Target: "pub"},
			{Source: "hidden-alias", Target: "hid", Exposure: &hidden},
		},
	}

	store.SyncFromConfig(cfg)
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	content := string(data)

	if strings.Contains(content, "visible:") {
		t.Errorf("persisted YAML must not contain 'visible:', got:\n%s", content)
	}
	if !strings.Contains(content, "exposure:") {
		t.Errorf("persisted YAML must contain 'exposure:', got:\n%s", content)
	}
	// 三档都应有显式写出
	for _, exp := range []string{"exposure: public", "exposure: hidden", "exposure: internal"} {
		if !strings.Contains(content, exp) {
			t.Errorf("expected %q in persisted YAML, got:\n%s", exp, content)
		}
	}
}

// ========== Remote Bridge YAML 持久化测试 ==========

func TestYamlStoreSyncRemoteBridgeProvider(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
providers:
  openai:
    endpoint: "https://api.openai.com"
    api_key: "sk-old"
    protocols:
      - "openai.chat"
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{
				"openai": {
					Endpoint:  "https://api.openai.com/v2",
					APIKey:    "sk-new",
					Protocols: []string{"openai.chat"},
				},
				"xai-bridge": {
					Endpoint:  "http://bridge.local:8080",
					APIKey:    "",
					Protocols: []string{"openai.responses"},
					RemoteBridge: &config.RemoteBridgeConfig{
						Enabled:  true,
						Provider: "xai-oauth",
						Token:    "secret-token",
					},
				},
			},
		},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4o", Models: config.ModelEntries{{Model: "openai/gpt-4o"}}},
			{Name: "grok", Models: config.ModelEntries{{Model: "xai-bridge/grok"}}},
		},
	}

	store.SyncFromConfig(cfg)
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	content := string(data)
	t.Logf("Generated YAML:\n%s", content)

	if !strings.Contains(content, "remote_bridge") {
		t.Error("expected 'remote_bridge' in persisted YAML")
	}
	if !strings.Contains(content, "enabled: true") {
		t.Error("expected 'enabled: true' in persisted YAML")
	}
	if !strings.Contains(content, "provider: xai-oauth") {
		t.Error("expected 'provider: xai-oauth' in persisted YAML")
	}
	if !strings.Contains(content, "token: secret-token") {
		t.Error("expected 'token: secret-token' in persisted YAML")
	}
	// 注意：api_key 为空时不写入 YAML（现有行为），不检查显示的空值字符串

	// 重新加载验证 roundtrip
	reloadCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}

	bridge := reloadCfg.Providers.Items["xai-bridge"]
	if bridge.RemoteBridge == nil {
		t.Fatal("expected RemoteBridge to survive roundtrip")
	}
	if !bridge.RemoteBridge.Enabled {
		t.Error("expected Enabled=true after roundtrip")
	}
	if bridge.RemoteBridge.Provider != "xai-oauth" {
		t.Errorf("expected Provider='xai-oauth' after roundtrip, got %q", bridge.RemoteBridge.Provider)
	}
	if bridge.RemoteBridge.Token != "secret-token" {
		t.Errorf("expected Token='secret-token' after roundtrip, got %q", bridge.RemoteBridge.Token)
	}
}

func TestYamlStoreSyncRemoteBridgeProvider_RoundtripDisabled(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
providers:
  openai:
    endpoint: "https://api.openai.com"
    api_key: "sk-old"
    protocols:
      - "openai.chat"
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{
				"openai": {
					Endpoint:  "https://api.openai.com/v2",
					APIKey:    "sk-new",
					Protocols: []string{"openai.chat"},
					RemoteBridge: &config.RemoteBridgeConfig{
						Enabled:  false,
						Provider: "xai-oauth",
						Token:    "should-not-matter",
						Local:    true,
					},
				},
			},
		},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4o", Models: config.ModelEntries{{Model: "openai/gpt-4o"}}},
		},
	}

	store.SyncFromConfig(cfg)
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	content := string(data)
	t.Logf("Generated YAML:\n%s", content)

	if !strings.Contains(content, "remote_bridge") {
		t.Error("expected 'remote_bridge' section to be persisted even when disabled")
	}
	if !strings.Contains(content, "local: true") {
		t.Error("expected 'local: true' in persisted YAML")
	}

	// roundtrip
	reloadCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}

	openai := reloadCfg.Providers.Items["openai"]
	if openai.RemoteBridge == nil {
		t.Fatal("expected RemoteBridge to survive roundtrip")
	}
	if openai.RemoteBridge.Enabled {
		t.Error("expected Enabled=false after roundtrip")
	}
	if !openai.RemoteBridge.Local {
		t.Error("expected Local=true after roundtrip")
	}
}

// ========== Cascade YAML 持久化测试 ==========

func TestYamlStoreSyncCascadeProvider(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
providers:
  openai:
    endpoint: "https://api.openai.com"
    api_key: "sk-old"
    protocols:
      - "openai.chat"
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{
				"openai": {
					Endpoint:  "https://api.openai.com/v2",
					APIKey:    "sk-new",
					Protocols: []string{"openai.chat"},
				},
				"corp-dev": {
					Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
					Cascade: &config.ProviderCascadeConfig{
						Enabled: true,
						Token:   "hub-secret",
					},
				},
			},
		},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4o", Models: config.ModelEntries{{Model: "openai/gpt-4o"}}},
			{Name: "corp", Models: config.ModelEntries{{Model: "corp-dev/gpt-4o"}}},
		},
	}

	store.SyncFromConfig(cfg)
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "cascade") {
		t.Error("expected 'cascade' in persisted YAML")
	}
	if !strings.Contains(content, "hub-secret") {
		t.Error("expected cascade token in persisted YAML")
	}

	reloaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}

	corp := reloaded.Providers.Items["corp-dev"]
	if corp.Cascade == nil {
		t.Fatal("expected Cascade to survive roundtrip")
	}
	if !corp.Cascade.Enabled {
		t.Error("expected Cascade.Enabled=true after roundtrip")
	}
	if corp.Cascade.Token != "hub-secret" {
		t.Errorf("expected Token='hub-secret' after roundtrip, got %q", corp.Cascade.Token)
	}
}

func TestYamlStoreSyncSpokeCascade(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
providers:
  openai:
    endpoint: "https://api.openai.com"
    api_key: "sk-old"
    protocols:
      - "openai.chat"
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{
				"openai": {
					Endpoint:  "https://api.openai.com",
					APIKey:    "sk-old",
					Protocols: []string{"openai.chat"},
				},
			},
		},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4o", Models: config.ModelEntries{{Model: "openai/gpt-4o"}}},
		},
		Cascade: &config.SpokeCascadeConfig{
			Hub:   "https://api.example.com",
			Token: "spoke-secret",
			Peer:  "openai",
		},
	}

	store.SyncFromConfig(cfg)
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}

	if reloaded.Cascade == nil {
		t.Fatal("expected top-level Cascade to survive roundtrip")
	}
	if reloaded.Cascade.Hub != "https://api.example.com" {
		t.Errorf("expected hub, got %q", reloaded.Cascade.Hub)
	}
	if reloaded.Cascade.Token != "spoke-secret" {
		t.Errorf("expected token, got %q", reloaded.Cascade.Token)
	}
	if reloaded.Cascade.Peer != "openai" {
		t.Errorf("expected peer, got %q", reloaded.Cascade.Peer)
	}
}

func TestYamlStoreSyncFromConfig_RemovesTopLevelCascadeWhenNil(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
providers:
  openai:
    endpoint: "https://api.openai.com"
    api_key: "sk-old"
    protocols:
      - "openai.chat"
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
cascade:
  hub: "https://api.example.com"
  token: "spoke-secret"
  peer: "openai"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{
				"openai": {
					Endpoint:  "https://api.openai.com",
					APIKey:    "sk-old",
					Protocols: []string{"openai.chat"},
				},
			},
		},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4o", Models: config.ModelEntries{{Model: "openai/gpt-4o"}}},
		},
		Cascade: nil,
	}

	store.SyncFromConfig(cfg)
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "\ncascade:") || strings.HasPrefix(content, "cascade:") {
		t.Errorf("expected top-level cascade key removed, got:\n%s", content)
	}

	reloaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.Cascade != nil {
		t.Fatalf("expected nil top-level cascade after removal, got %+v", reloaded.Cascade)
	}
}

// TestYamlStoreProviderUpstreamModelRejected 验证 providers.*.upstream_model 被硬拒绝：
// config.Load 失败并点名路径；YamlStore.RejectDeprecatedFields 同样拒绝；
// SyncFromConfig 不再写回该键（provider 渲染不输出 upstream_model）。
func TestYamlStoreProviderUpstreamModelRejected(t *testing.T) {
	yamlContent := `
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
    upstream_model:
      - model: "gpt-4o"
        qpm: 10
      - model: "gpt-3.5-turbo"
inbound:
  auth:
    keys:
      - name: "default"
        key: "sk-test"
model_groups:
  - name: "test-group"
    models:
      - model: "openai/gpt-4o"
database:
  path: "./test.db"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := config.Load(configPath); err == nil {
		t.Fatal("expected load error for providers.openai.upstream_model")
	} else if !strings.Contains(err.Error(), "providers.openai.upstream_model") {
		t.Fatalf("load error = %q, want providers.openai.upstream_model path", err.Error())
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.RejectDeprecatedFields(); err == nil {
		t.Fatal("expected RejectDeprecatedFields error for providers.openai.upstream_model")
	} else if !strings.Contains(err.Error(), "providers.openai.upstream_model") {
		t.Fatalf("reject error = %q, want providers.openai.upstream_model path", err.Error())
	}
}

// TestYamlStoreCascadeOfferRejected 验证 Admin Apply 侧的废弃字段扫描同样
// 按路径硬拒绝历史 cascade.offer。
func TestYamlStoreCascadeOfferRejected(t *testing.T) {
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
cascade:
  hub: "https://api.example.com"
  token: "spoke-secret"
  peer: "openai"
  offer:
    - "gpt-4o"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := config.Load(configPath); err == nil {
		t.Fatal("expected load error for cascade.offer")
	} else if !strings.Contains(err.Error(), "cascade.offer") {
		t.Fatalf("load error = %q, want cascade.offer path", err.Error())
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.RejectDeprecatedFields(); err == nil {
		t.Fatal("expected RejectDeprecatedFields error for cascade.offer")
	} else if !strings.Contains(err.Error(), "cascade.offer") {
		t.Fatalf("reject error = %q, want cascade.offer path", err.Error())
	}
}

// TestYamlStoreRulesSchedulingActionRoundtrip 验证调度类 action（qpm/enable_time_range/disable_time_range）
// 经 SyncFromConfig 落盘与 reload 后一致，键名保持 snake_case。
func TestYamlStoreRulesSchedulingActionRoundtrip(t *testing.T) {
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
  - name: "gpt-4"
    models:
      - "openai/gpt-4o"
rules:
  - match:
      upstream-model: { op: equals, value: "openai/gpt-4o" }
    action:
      qpm: 120
  - match:
      upstream-model: { op: startWith, value: "openrouter/" }
    action:
      enable_time_range: ["09:00-18:00"]
  - match:
      key: { op: equals, value: "sk-test" }
    action:
      disable_time_range: ["23:00-02:00"]
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	store.SyncFromConfig(cfg)
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloadCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(reloadCfg.Rules) != 3 {
		t.Fatalf("rules len = %d, want 3", len(reloadCfg.Rules))
	}
	r0 := reloadCfg.Rules[0]
	if r0.Action.QPM == nil || *r0.Action.QPM != 120 {
		t.Errorf("rules[0].action.qpm = %v, want 120", r0.Action.QPM)
	}
	r1 := reloadCfg.Rules[1]
	if len(r1.Action.EnableTimeRange) != 1 || r1.Action.EnableTimeRange[0] != "09:00-18:00" {
		t.Errorf("rules[1].action.enable_time_range = %v, want [09:00-18:00]", r1.Action.EnableTimeRange)
	}
	r2 := reloadCfg.Rules[2]
	if len(r2.Action.DisableTimeRange) != 1 || r2.Action.DisableTimeRange[0] != "23:00-02:00" {
		t.Errorf("rules[2].action.disable_time_range = %v, want [23:00-02:00]", r2.Action.DisableTimeRange)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	for _, key := range []string{"qpm:", "enable_time_range:", "disable_time_range:"} {
		if !strings.Contains(content, key) {
			t.Errorf("persisted YAML must contain %q, got:\n%s", key, content)
		}
	}
}

// TestYamlStoreSyncFromConfigRules 验证 SyncFromConfig 按 cfg.Rules 重建顶层 rules 节点：
// 改 rules 后 Sync+Save+Load 一致（顺序与内容）；只改其它段时 rules 按 cfg 保真；
// YAML 原文使用 kebab-case 键名（client-model），不得出现 JSON 的 client_model。
func TestYamlStoreSyncFromConfigRules(t *testing.T) {
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
  - name: "gpt-4"
    models:
      - "openai/gpt-4"
rules:
  - match:
      upstream-model: { op: equals, value: "openai/gpt-4o" }
    action:
      effort: [high, max]
`

	setup := func(t *testing.T) (*YamlStore, string, *config.Config) {
		t.Helper()
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte(yamlContent), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		cfg, err := config.Load(configPath)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}

		store, err := NewYamlStore(configPath)
		if err != nil {
			t.Fatalf("create store: %v", err)
		}
		return store, configPath, cfg
	}

	t.Run("rules changed persist in order with kebab keys", func(t *testing.T) {
		store, configPath, cfg := setup(t)

		cfg.Rules = []config.RuleConfig{
			{
				Match: config.RuleMatch{
					ClientModel: &config.RuleCondition{Op: "equals", Value: "gpt-4"},
				},
				Action: config.RuleAction{Protocol: "openai.chat"},
			},
			{
				Match: config.RuleMatch{
					ClientModel: &config.RuleCondition{Op: "equals", Value: "gpt-4"},
				},
				Action: config.RuleAction{Thinking: "off"},
			},
			{
				Action: config.RuleAction{Effort: []string{"high", "max"}},
			},
		}

		store.SyncFromConfig(cfg)
		if err := store.Save(); err != nil {
			t.Fatalf("save: %v", err)
		}

		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, "client-model") {
			t.Errorf("persisted YAML must contain 'client-model', got:\n%s", content)
		}
		if strings.Contains(content, "client_model") {
			t.Errorf("persisted YAML must not contain 'client_model', got:\n%s", content)
		}

		reloadCfg, err := config.Load(configPath)
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		if len(reloadCfg.Rules) != 3 {
			t.Fatalf("rules len = %d, want 3", len(reloadCfg.Rules))
		}
		r0 := reloadCfg.Rules[0]
		if r0.Match.ClientModel == nil || r0.Match.ClientModel.Op != "equals" || r0.Match.ClientModel.Value != "gpt-4" {
			t.Errorf("rules[0].match.client-model = %+v, want equals gpt-4", r0.Match.ClientModel)
		}
		if r0.Match.Key != nil || r0.Match.UpstreamModel != nil {
			t.Errorf("rules[0].match = %+v, want only client-model", r0.Match)
		}
		if r0.Action.Protocol != "openai.chat" {
			t.Errorf("rules[0].action = %+v, want protocol openai.chat", r0.Action)
		}
		r1 := reloadCfg.Rules[1]
		if r1.Match.ClientModel == nil || r1.Match.ClientModel.Value != "gpt-4" {
			t.Errorf("rules[1].match = %+v, want client-model gpt-4", r1.Match)
		}
		if r1.Action.Thinking != "off" {
			t.Errorf("rules[1].action = %+v, want thinking off", r1.Action)
		}
		r2 := reloadCfg.Rules[2]
		if r2.Match.ClientModel != nil || r2.Match.Key != nil || r2.Match.UpstreamModel != nil {
			t.Errorf("rules[2].match = %+v, want empty (global rule)", r2.Match)
		}
		if len(r2.Action.Effort) != 2 || r2.Action.Effort[0] != "high" || r2.Action.Effort[1] != "max" {
			t.Errorf("rules[2].action.effort = %+v, want [high max]", r2.Action.Effort)
		}
	})

	t.Run("other sections changed keep rules per cfg", func(t *testing.T) {
		store, configPath, cfg := setup(t)

		p := cfg.Providers.Items["openai"]
		p.APIKey = "sk-updated"
		cfg.Providers.Items["openai"] = p

		store.SyncFromConfig(cfg)
		if err := store.Save(); err != nil {
			t.Fatalf("save: %v", err)
		}

		reloadCfg, err := config.Load(configPath)
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		if reloadCfg.Providers.Items["openai"].APIKey != "sk-updated" {
			t.Errorf("api_key = %q, want sk-updated", reloadCfg.Providers.Items["openai"].APIKey)
		}
		if len(reloadCfg.Rules) != 1 {
			t.Fatalf("rules len = %d, want 1", len(reloadCfg.Rules))
		}
		rule := reloadCfg.Rules[0]
		if rule.Match.UpstreamModel == nil || rule.Match.UpstreamModel.Op != "equals" || rule.Match.UpstreamModel.Value != "openai/gpt-4o" {
			t.Errorf("rule.match.upstream-model = %+v, want equals openai/gpt-4o", rule.Match.UpstreamModel)
		}
		if len(rule.Action.Effort) != 2 || rule.Action.Effort[0] != "high" || rule.Action.Effort[1] != "max" {
			t.Errorf("rule.action.effort = %+v, want [high max]", rule.Action.Effort)
		}
	})

	t.Run("empty rules write empty sequence", func(t *testing.T) {
		store, configPath, cfg := setup(t)

		cfg.Rules = nil

		store.SyncFromConfig(cfg)
		if err := store.Save(); err != nil {
			t.Fatalf("save: %v", err)
		}

		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, "rules: []") {
			t.Errorf("persisted YAML must contain empty rules sequence 'rules: []', got:\n%s", content)
		}

		reloadCfg, err := config.Load(configPath)
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		if len(reloadCfg.Rules) != 0 {
			t.Errorf("rules len = %d after clear, want 0", len(reloadCfg.Rules))
		}
	})

	t.Run("rules node created when missing", func(t *testing.T) {
		store, configPath, cfg := setup(t)

		// 从 AST 删除 rules 节点，模拟旧配置无 rules 段
		doc := store.rootNode.Content[0]
		for i := 0; i+1 < len(doc.Content); i += 2 {
			if doc.Content[i].Value == "rules" {
				doc.Content = append(doc.Content[:i], doc.Content[i+2:]...)
				break
			}
		}
		cfg.Rules = []config.RuleConfig{{
			Action: config.RuleAction{Effort: []string{"none"}},
		}}

		store.SyncFromConfig(cfg)
		if err := store.Save(); err != nil {
			t.Fatalf("save: %v", err)
		}

		reloadCfg, err := config.Load(configPath)
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		if len(reloadCfg.Rules) != 1 || len(reloadCfg.Rules[0].Action.Effort) != 1 || reloadCfg.Rules[0].Action.Effort[0] != "none" {
			t.Errorf("rules = %+v, want one rule with [none]", reloadCfg.Rules)
		}
	})
}

func TestYamlStoreStickyRoundtrip(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
model_groups:
  - name: "lb-group"
    mode: "load-balance"
    models:
      - model: "openai/gpt-4o"
        weight: 1
    exposure: public
    sticky:
      enabled: true
      idle_timeout: 10m
redirect: []
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	// 修改 sticky 配置（模拟 admin UI 编辑后 Apply）
	cfg := &config.Config{
		Server: config.ServerConfig{Listen: ":18000"},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name: "lb-group",
				Mode: "load-balance",
				Models: config.ModelEntries{
					{Model: "openai/gpt-4o", Weight: 1},
					{Model: "anthropic/claude", Weight: 2},
				},
				Exposure: ptrExposure(config.ExposurePublic),
				Sticky: &config.StickyConfig{
					Enabled:     true,
					IdleTimeout: "5m",
				},
			},
		},
	}

	store.SyncFromConfig(cfg)
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// 重新加载验证 sticky 字段仍在
	reloadCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}

	if len(reloadCfg.ModelGroups) != 1 {
		t.Fatalf("expected 1 model group, got %d", len(reloadCfg.ModelGroups))
	}

	mg := reloadCfg.ModelGroups[0]
	if mg.Sticky == nil {
		t.Fatal("sticky should not be nil after roundtrip")
	}
	if !mg.Sticky.Enabled {
		t.Error("sticky.enabled should be true")
	}
	if mg.Sticky.IdleTimeout != "5m" {
		t.Errorf("sticky.idle_timeout = %q, want 5m", mg.Sticky.IdleTimeout)
	}
	if len(mg.Models) != 2 {
		t.Errorf("expected 2 models, got %d", len(mg.Models))
	}
}

func TestYamlStoreStickyDisabledRoundtrip(t *testing.T) {
	yaml := `
server:
  listen: ":18000"
model_groups:
  - name: "lb-group"
    mode: "load-balance"
    models:
      - model: "openai/gpt-4o"
        weight: 1
    exposure: public
    sticky:
      enabled: true
      idle_timeout: 10m
redirect: []
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	// 模拟禁用 sticky（用户在 admin UI 取消勾选并保存）
	cfg := &config.Config{
		Server: config.ServerConfig{Listen: ":18000"},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name: "lb-group",
				Mode: "load-balance",
				Models: config.ModelEntries{
					{Model: "openai/gpt-4o", Weight: 1},
				},
				Exposure: ptrExposure(config.ExposurePublic),
				Sticky: &config.StickyConfig{
					Enabled: false,
				},
			},
		},
	}

	store.SyncFromConfig(cfg)
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// 重新加载验证 sticky 状态已更新为 disabled
	reloadCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}

	mg := reloadCfg.ModelGroups[0]
	if mg.Sticky == nil {
		t.Fatal("sticky should not be nil (disabled should be explicit)")
	}
	if mg.Sticky.Enabled {
		t.Error("sticky.enabled should be false after disabling")
	}
}

func ptrExposure(e config.Exposure) *config.Exposure {
	return &e
}

// TestModelGroupToYamlNode_ExhaustiveKeys 穷举 ModelGroupConfig 所有应写入 YAML 的字段。
// 当新增字段时，必须同步更新 modelGroupToYamlNode 和此测试的 expectedKeys，
// 否则测试失败，防止序列化遗漏。
func TestModelGroupToYamlNode_ExhaustiveKeys(t *testing.T) {
	ctxLen := 128000
	exposure := config.ExposurePublic
	cfg := config.ModelGroupConfig{
		Name: "exhaustive-test",
		Mode: "load-balance",
		Models: config.ModelEntries{
			{Model: "openai/gpt-4o", Weight: 2},
			{Model: "anthropic/claude", Weight: 1},
		},
		Exposure: &exposure,
		ModelMetadata: config.ModelMetadataConfig{
			ContextLength: &ctxLen,
		},
		Sticky: &config.StickyConfig{
			Enabled:     true,
			IdleTimeout: "5m",
		},
	}

	// 用任意合法 YAML 构造 YamlStore，目的仅是拿到 modelGroupToYamlNode 产物
	yamlContent := `
server:
  listen: ":18000"
model_groups: []
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	store, err := NewYamlStore(configPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	node := store.modelGroupToYamlNode(cfg)

	// 收集 YAML node 中实际写出的 key
	wroteKeys := make(map[string]bool)
	for i := 0; i+1 < len(node.Content); i += 2 {
		wroteKeys[node.Content[i].Value] = true
	}

	// 穷举当前 ModelGroupConfig 所有应序列化的字段。
	// ⚠️ 新增字段时务必在此列表中添加，否则本测试会失败。
	expectedKeys := []string{
		"name",
		"mode",
		"models",
		"exposure",
		"model_metadata",
		"sticky",
	}

	for _, k := range expectedKeys {
		if !wroteKeys[k] {
			t.Errorf("key %q not found in YAML output — update modelGroupToYamlNode to serialize this field", k)
		}
	}

	// 反向检查：是否有 unexpected key 写出（防止旧字段误删）
	for k := range wroteKeys {
		found := false
		for _, ek := range expectedKeys {
			if k == ek {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("unexpected key %q in YAML output — remove from expectedKeys if field was deleted from ModelGroupConfig", k)
		}
	}
}
