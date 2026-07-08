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
				Endpoint: "https://api.openai.com/v2",
				APIKey:   "sk-new",
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
    default_protocols: ["openai"]
    endpoints:
      - url: "https://api.openai.com/v1"
        protocols: ["anthropic"]
    upstream_model:
      - model: "gpt-4o"
        allowed_protocols: ["openai", "anthropic"]
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
	if openai.DefaultProtocols[0] != "openai.chat" {
		t.Errorf("openai.DefaultProtocols[0] = %q, want openai.chat", openai.DefaultProtocols[0])
	}
	if openai.Endpoints[0].Protocols[0] != "anthropic.messages" {
		t.Errorf("openai.Endpoints[0].Protocols[0] = %q, want anthropic.messages", openai.Endpoints[0].Protocols[0])
	}
	if openai.UpstreamModels[0].AllowedProtocols[0] != "openai.chat" {
		t.Errorf("openai.UpstreamModels[0].AllowedProtocols[0] = %q, want openai.chat", openai.UpstreamModels[0].AllowedProtocols[0])
	}
	if openai.UpstreamModels[0].AllowedProtocols[1] != "anthropic.messages" {
		t.Errorf("openai.UpstreamModels[0].AllowedProtocols[1] = %q, want anthropic.messages", openai.UpstreamModels[0].AllowedProtocols[1])
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
