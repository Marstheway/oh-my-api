package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	yaml := `
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
    protocol: "openai"
model_groups:
  - name: "gpt-4"
    models:
      - "openai/gpt-4"
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("config should not be nil")
	}
	if cfg.Server.Listen != ":18000" {
		t.Errorf("expected listen ':18000', got %q", cfg.Server.Listen)
	}
	if len(cfg.Providers.Items) != 1 {
		t.Errorf("expected 1 provider, got %d", len(cfg.Providers.Items))
	}
	if cfg.Providers.Items["openai"].Endpoint != "https://api.openai.com/v1" {
		t.Errorf("unexpected endpoint: %q", cfg.Providers.Items["openai"].Endpoint)
	}
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("invalid: yaml: content: ["), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestModelEntriesParsing(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		expected ModelEntries
	}{
		{
			name: "single string model",
			yaml: `
model_groups:
  - name: "test"
    models:
      - "openai/gpt-4"
`,
			expected: ModelEntries{{Model: "openai/gpt-4", Weight: 1, Priority: 0}},
		},
		{
			name: "multiple string models",
			yaml: `
model_groups:
  - name: "test"
    models:
      - "openai/gpt-4"
      - "anthropic/claude"
`,
			expected: ModelEntries{
				{Model: "openai/gpt-4", Weight: 1, Priority: 0},
				{Model: "anthropic/claude", Weight: 1, Priority: 0},
			},
		},
		{
			name: "full model entries",
			yaml: `
model_groups:
  - name: "test"
    models:
      - model: "openai/gpt-4"
        weight: 3
        priority: 1
      - model: "anthropic/claude"
        weight: 2
        priority: 2
`,
			expected: ModelEntries{
				{Model: "openai/gpt-4", Weight: 3, Priority: 1},
				{Model: "anthropic/claude", Weight: 2, Priority: 2},
			},
		},
		{
			name: "mixed formats",
			yaml: `
model_groups:
  - name: "test"
    models:
      - "openai/gpt-4"
      - model: "anthropic/claude"
        weight: 5
        priority: 10
`,
			expected: ModelEntries{
				{Model: "openai/gpt-4", Weight: 1, Priority: 0},
				{Model: "anthropic/claude", Weight: 5, Priority: 10},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")
			fullYaml := `
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
    protocol: "openai"
` + tt.yaml
			if err := os.WriteFile(configPath, []byte(fullYaml), 0644); err != nil {
				t.Fatalf("failed to write config file: %v", err)
			}

			cfg, err := Load(configPath)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(cfg.ModelGroups) != 1 {
				t.Fatalf("expected 1 model group, got %d", len(cfg.ModelGroups))
			}

			entries := cfg.ModelGroups[0].Models
			if len(entries) != len(tt.expected) {
				t.Fatalf("expected %d entries, got %d", len(tt.expected), len(entries))
			}

			for i, exp := range tt.expected {
				if entries[i].Model != exp.Model {
					t.Errorf("entry %d: expected model %q, got %q", i, exp.Model, entries[i].Model)
				}
				if entries[i].Weight != exp.Weight {
					t.Errorf("entry %d: expected weight %d, got %d", i, exp.Weight, entries[i].Weight)
				}
				if entries[i].Priority != exp.Priority {
					t.Errorf("entry %d: expected priority %d, got %d", i, exp.Priority, entries[i].Priority)
				}
			}
		})
	}
}

func TestModelGroupConfigSingularModel(t *testing.T) {
	tests := []struct {
		name           string
		yaml           string
		expectedModels ModelEntries
	}{
		{
			name: "singular model field",
			yaml: `
model_groups:
  - name: "test"
    model: "openai/gpt-4"
`,
			expectedModels: ModelEntries{{Model: "openai/gpt-4", Weight: 1, Priority: 0}},
		},
		{
			name: "plural models field array",
			yaml: `
model_groups:
  - name: "test"
    models:
      - "openai/gpt-4"
      - "anthropic/claude"
`,
			expectedModels: ModelEntries{
				{Model: "openai/gpt-4", Weight: 1, Priority: 0},
				{Model: "anthropic/claude", Weight: 1, Priority: 0},
			},
		},
		{
			name: "both model and models specified - models takes precedence",
			yaml: `
model_groups:
  - name: "test"
    model: "openai/gpt-4"
    models:
      - "anthropic/claude"
`,
			expectedModels: ModelEntries{{Model: "anthropic/claude", Weight: 1, Priority: 0}},
		},
		{
			name: "backward compatible models string format",
			yaml: `
model_groups:
  - name: "test"
    models: "openai/gpt-4"
`,
			expectedModels: ModelEntries{{Model: "openai/gpt-4", Weight: 1, Priority: 0}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")
			fullYaml := `
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
    protocol: "openai"
` + tt.yaml
			if err := os.WriteFile(configPath, []byte(fullYaml), 0644); err != nil {
				t.Fatalf("failed to write config file: %v", err)
			}

			cfg, err := Load(configPath)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(cfg.ModelGroups) != 1 {
				t.Fatalf("expected 1 model group, got %d", len(cfg.ModelGroups))
			}

			entries := cfg.ModelGroups[0].Models
			if len(entries) != len(tt.expectedModels) {
				t.Fatalf("expected %d entries, got %d", len(tt.expectedModels), len(entries))
			}

			for i, exp := range tt.expectedModels {
				if entries[i].Model != exp.Model {
					t.Errorf("entry %d: expected model %q, got %q", i, exp.Model, entries[i].Model)
				}
				if entries[i].Weight != exp.Weight {
					t.Errorf("entry %d: expected weight %d, got %d", i, exp.Weight, entries[i].Weight)
				}
				if entries[i].Priority != exp.Priority {
					t.Errorf("entry %d: expected priority %d, got %d", i, exp.Priority, entries[i].Priority)
				}
			}
		})
	}
}

func TestModelGroupConfigModeAndTimeout(t *testing.T) {
	yaml := `
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
    protocol: "openai"
model_groups:
  - name: "test-default"
    models:
      - "openai/gpt-4"
  - name: "test-concurrent"
    mode: "concurrent"
    timeout: "60s"
    models:
      - "openai/gpt-4"
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.ModelGroups) != 2 {
		t.Fatalf("expected 2 model groups, got %d", len(cfg.ModelGroups))
	}

	if cfg.ModelGroups[0].Mode != "" {
		t.Errorf("expected empty mode for first group, got %q", cfg.ModelGroups[0].Mode)
	}
	if cfg.ModelGroups[0].Timeout != "" {
		t.Errorf("expected empty timeout for first group, got %q", cfg.ModelGroups[0].Timeout)
	}

	if cfg.ModelGroups[1].Mode != "concurrent" {
		t.Errorf("expected mode 'concurrent', got %q", cfg.ModelGroups[1].Mode)
	}
	if cfg.ModelGroups[1].Timeout != "60s" {
		t.Errorf("expected timeout '60s', got %q", cfg.ModelGroups[1].Timeout)
	}
}

func TestProviderConfig_GetEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		provider ProviderConfig
		inbound  string
		want     string
	}{
		{
			name: "endpoints match openai",
			provider: ProviderConfig{
				Endpoint: "https://default.api.com",
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocol: "anthropic"},
					{URL: "https://openai.api.com", Protocol: "openai"},
				},
			},
			inbound: "openai",
			want:    "https://openai.api.com",
		},
		{
			name: "endpoints match anthropic",
			provider: ProviderConfig{
				Endpoint: "https://default.api.com",
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocol: "anthropic"},
					{URL: "https://openai.api.com", Protocol: "openai"},
				},
			},
			inbound: "anthropic",
			want:    "https://anthropic.api.com",
		},
		{
			name: "endpoints no match fallback to first endpoint url",
			provider: ProviderConfig{
				Endpoint: "https://default.api.com",
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocol: "anthropic"},
				},
			},
			inbound: "openai",
			want:    "https://anthropic.api.com",
		},
		{
			name: "empty endpoints fallback to endpoint",
			provider: ProviderConfig{
				Endpoint:  "https://default.api.com",
				Endpoints: []EndpointConfig{},
			},
			inbound: "openai",
			want:    "https://default.api.com",
		},
		{
			name: "endpoints no match fallback to first endpoint url",
			provider: ProviderConfig{
				Endpoint: "https://default.api.com",
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocol: "anthropic"},
					{URL: "https://openai.api.com", Protocol: "openai"},
				},
			},
			inbound: "openai.response",
			want:    "https://anthropic.api.com",
		},
		{
			name: "no endpoints use endpoint field",
			provider: ProviderConfig{
				Endpoint: "https://default.api.com",
			},
			inbound: "openai",
			want:    "https://default.api.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.provider.GetEndpoint(tt.inbound)
			if got != tt.want {
				t.Errorf("GetEndpoint(%q) = %q, want %q", tt.inbound, got, tt.want)
			}
		})
	}
}

func TestProviderConfig_GetOutboundProtocol(t *testing.T) {
	tests := []struct {
		name     string
		provider ProviderConfig
		inbound  string
		want     string
	}{
		{
			name: "endpoints match anthropic returns anthropic",
			provider: ProviderConfig{
				Protocol: "openai",
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocol: "anthropic"},
					{URL: "https://openai.api.com", Protocol: "openai"},
				},
			},
			inbound: "anthropic",
			want:    "anthropic",
		},
		{
			name: "endpoints match openai returns openai",
			provider: ProviderConfig{
				Protocol: "anthropic",
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocol: "anthropic"},
					{URL: "https://openai.api.com", Protocol: "openai"},
				},
			},
			inbound: "openai",
			want:    "openai",
		},
		{
			name: "endpoints no match fallback to first endpoint protocol",
			provider: ProviderConfig{
				Protocol: "openai",
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocol: "anthropic"},
				},
			},
			inbound: "openai",
			want:    "anthropic",
		},
		{
			name: "empty endpoints fallback to default protocol",
			provider: ProviderConfig{
				Protocol:  "openai",
				Endpoints: []EndpointConfig{},
			},
			inbound: "anthropic",
			want:    "openai",
		},
		{
			name: "endpoints no match fallback to first endpoint protocol",
			provider: ProviderConfig{
				Protocol: "openai",
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocol: "anthropic"},
					{URL: "https://openai.api.com", Protocol: "openai"},
				},
			},
			inbound: "openai.response",
			want:    "anthropic",
		},
		{
			name: "endpoints present without top-level protocol still works",
			provider: ProviderConfig{
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocol: "anthropic"},
					{URL: "https://openai.api.com", Protocol: "openai"},
				},
			},
			inbound: "openai.response",
			want:    "anthropic",
		},
		{
			name: "no endpoints use default protocol",
			provider: ProviderConfig{
				Protocol: "anthropic",
			},
			inbound: "openai",
			want:    "anthropic",
		},
		// 多协议直通测试: protocol: "openai/anthropic"
		{
			name: "multi-protocol A->A: inbound anthropic returns anthropic",
			provider: ProviderConfig{
				Protocol: "openai/anthropic",
			},
			inbound: "anthropic",
			want:    "anthropic",
		},
		{
			name: "multi-protocol O->O: inbound openai returns openai",
			provider: ProviderConfig{
				Protocol: "openai/anthropic",
			},
			inbound: "openai",
			want:    "openai",
		},
		// 单协议转换测试
		{
			name: "single-protocol A->O: inbound anthropic fallback to openai",
			provider: ProviderConfig{
				Protocol: "openai",
			},
			inbound: "anthropic",
			want:    "openai",
		},
		{
			name: "single-protocol O->A: inbound openai fallback to anthropic",
			provider: ProviderConfig{
				Protocol: "anthropic",
			},
			inbound: "openai",
			want:    "anthropic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.provider.GetOutboundProtocol(tt.inbound)
			if got != tt.want {
				t.Errorf("GetOutboundProtocol(%q) = %q, want %q", tt.inbound, got, tt.want)
			}
		})
	}
}
