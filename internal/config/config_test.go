package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/codec"
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
    protocols: ["openai.chat"]
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
			expected: ModelEntries{{Model: "openai/gpt-4", Weight: 1}},
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
				{Model: "openai/gpt-4", Weight: 1},
				{Model: "anthropic/claude", Weight: 1},
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
      - model: "anthropic/claude"
        weight: 2
`,
			expected: ModelEntries{
				{Model: "openai/gpt-4", Weight: 3},
				{Model: "anthropic/claude", Weight: 2},
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
`,
			expected: ModelEntries{
				{Model: "openai/gpt-4", Weight: 1},
				{Model: "anthropic/claude", Weight: 5},
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
    protocols: ["openai.chat"]
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
			expectedModels: ModelEntries{{Model: "openai/gpt-4", Weight: 1}},
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
				{Model: "openai/gpt-4", Weight: 1},
				{Model: "anthropic/claude", Weight: 1},
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
			expectedModels: ModelEntries{{Model: "anthropic/claude", Weight: 1}},
		},
		{
			name: "backward compatible models string format",
			yaml: `
model_groups:
  - name: "test"
    models: "openai/gpt-4"
`,
			expectedModels: ModelEntries{{Model: "openai/gpt-4", Weight: 1}},
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
    protocols: ["openai.chat"]
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
			}
		})
	}
}

func TestModelGroupConfigMode(t *testing.T) {
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
    protocols: ["openai.chat"]
model_groups:
  - name: "test-default"
    models:
      - "openai/gpt-4"
  - name: "test-concurrent"
    mode: "concurrent"
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

	if cfg.ModelGroups[1].Mode != "concurrent" {
		t.Errorf("expected mode 'concurrent', got %q", cfg.ModelGroups[1].Mode)
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
					{URL: "https://anthropic.api.com", Protocols: []string{"anthropic.messages"}},
					{URL: "https://openai.api.com", Protocols: []string{"openai.chat"}},
				},
			},
			inbound: "openai.chat",
			want:    "https://openai.api.com",
		},
		{
			name: "endpoints match anthropic",
			provider: ProviderConfig{
				Endpoint: "https://default.api.com",
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocols: []string{"anthropic.messages"}},
					{URL: "https://openai.api.com", Protocols: []string{"openai.chat"}},
				},
			},
			inbound: "anthropic.messages",
			want:    "https://anthropic.api.com",
		},
		{
			name: "endpoints no match fallback to first endpoint url",
			provider: ProviderConfig{
				Endpoint: "https://default.api.com",
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocols: []string{"anthropic.messages"}},
				},
			},
			inbound: "openai.chat",
			want:    "https://anthropic.api.com",
		},
		{
			name: "empty endpoints fallback to endpoint",
			provider: ProviderConfig{
				Endpoint:  "https://default.api.com",
				Endpoints: []EndpointConfig{},
			},
			inbound: "openai.chat",
			want:    "https://default.api.com",
		},
		{
			name: "endpoints no match fallback to first endpoint url",
			provider: ProviderConfig{
				Endpoint: "https://default.api.com",
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocols: []string{"anthropic.messages"}},
					{URL: "https://openai.api.com", Protocols: []string{"openai.chat"}},
				},
			},
			inbound: "openai.responses",
			want:    "https://anthropic.api.com",
		},
		{
			name: "no endpoints use endpoint field",
			provider: ProviderConfig{
				Endpoint: "https://default.api.com",
			},
			inbound: "openai.chat",
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
				Protocols: []string{"openai.chat"},
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocols: []string{"anthropic.messages"}},
					{URL: "https://openai.api.com", Protocols: []string{"openai.chat"}},
				},
			},
			inbound: "anthropic.messages",
			want:    "anthropic.messages",
		},
		{
			name: "endpoints match openai returns openai",
			provider: ProviderConfig{
				Protocols: []string{"anthropic.messages"},
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocols: []string{"anthropic.messages"}},
					{URL: "https://openai.api.com", Protocols: []string{"openai.chat"}},
				},
			},
			inbound: "openai.chat",
			want:    "openai.chat",
		},
		{
			name: "endpoints no match fallback to first endpoint protocol",
			provider: ProviderConfig{
				Protocols: []string{"openai.chat"},
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocols: []string{"anthropic.messages"}},
				},
			},
			inbound: "openai.chat",
			want:    "anthropic.messages",
		},
		{
			name: "empty endpoints fallback to default protocol",
			provider: ProviderConfig{
				Protocols: []string{"openai.chat"},
				Endpoints: []EndpointConfig{},
			},
			inbound: "anthropic.messages",
			want:    "openai.chat",
		},
		{
			name: "endpoints no match fallback to first endpoint protocol",
			provider: ProviderConfig{
				Protocols: []string{"openai.chat"},
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocols: []string{"anthropic.messages"}},
					{URL: "https://openai.api.com", Protocols: []string{"openai.chat"}},
				},
			},
			inbound: "openai.responses",
			want:    "openai.chat",
		},
		{
			name: "endpoints present without top-level protocol still works",
			provider: ProviderConfig{
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.api.com", Protocols: []string{"anthropic.messages"}},
					{URL: "https://openai.api.com", Protocols: []string{"openai.chat"}},
				},
			},
			inbound: "openai.responses",
			want:    "openai.chat",
		},
		{
			name: "no endpoints use default protocol",
			provider: ProviderConfig{
				Protocols: []string{"anthropic.messages"},
			},
			inbound: "openai.chat",
			want:    "anthropic.messages",
		},
		// 单协议转换测试
		{
			name: "multi-protocol A->A: inbound anthropic returns anthropic",
			provider: ProviderConfig{
				Protocols: []string{"openai.chat", "anthropic.messages"},
			},
			inbound: "anthropic.messages",
			want:    "anthropic.messages",
		},
		{
			name: "multi-protocol O->O: inbound openai returns openai",
			provider: ProviderConfig{
				Protocols: []string{"openai.chat", "anthropic.messages"},
			},
			inbound: "openai.chat",
			want:    "openai.chat",
		},
		// 单协议转换测试
		{
			name: "single-protocol A->O: inbound anthropic fallback to openai",
			provider: ProviderConfig{
				Protocols: []string{"openai.chat"},
			},
			inbound: "anthropic.messages",
			want:    "openai.chat",
		},
		{
			name: "single-protocol O->A: inbound openai fallback to anthropic",
			provider: ProviderConfig{
				Protocols: []string{"anthropic.messages"},
			},
			inbound: "openai.chat",
			want:    "anthropic.messages",
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

func TestProviderConfig_SelectOutboundFormat(t *testing.T) {
	tests := []struct {
		name     string
		provider ProviderConfig
		inbound  string
		want     string
		reason   string
		cost     int
		wantErr  bool
	}{
		{
			name: "endpoint passthrough",
			provider: ProviderConfig{
				Endpoints: []EndpointConfig{
					{URL: "https://a", Protocols: []string{"anthropic.messages"}},
				},
			},
			inbound: "anthropic.messages",
			want:    "anthropic.messages",
			reason:  "passthrough",
			cost:    0,
		},
		{
			name: "fallback first endpoint with lowest cost",
			provider: ProviderConfig{
				Endpoints: []EndpointConfig{
					{URL: "https://a", Protocols: []string{"openai.chat"}},
				},
			},
			inbound: "anthropic.messages",
			want:    "openai.chat",
			reason:  "lowest_cost",
			cost:    3,
		},
		{
			name: "lowest cost across all endpoints",
			provider: ProviderConfig{
				Protocols: []string{"openai.chat", "anthropic.messages"},
				Endpoints: []EndpointConfig{
					{URL: "https://a", Protocols: []string{"anthropic.messages"}},
					{URL: "https://b", Protocols: []string{"openai.chat"}},
				},
			},
			inbound: "openai.responses",
			want:    "openai.chat",
			reason:  "lowest_cost",
			cost:    3,
		},
		{
			name: "invalid singular response protocol",
			provider: ProviderConfig{
				Protocols: []string{"openai.response"},
			},
			inbound: "openai.chat",
			wantErr: true,
		},
		{
			name: "normalized multi protocol is accepted",
			provider: ProviderConfig{
				Protocols: []string{"openai.chat", "anthropic.messages"},
			},
			inbound: "openai.responses",
			want:    "openai.chat",
			reason:  "lowest_cost",
			cost:    3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inbound, err := codec.NormalizeProviderFormat(tt.inbound)
			if err != nil {
				t.Fatalf("normalize inbound failed: %v", err)
			}
			got, reason, cost, err := tt.provider.SelectOutboundFormat(inbound)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectOutboundFormat returned error: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("SelectOutboundFormat got=%q want=%q", got, tt.want)
			}
			if reason != tt.reason {
				t.Fatalf("reason=%q want=%q", reason, tt.reason)
			}
			if cost != tt.cost {
				t.Fatalf("cost=%d want=%d", cost, tt.cost)
			}
		})
	}
}

func TestProviderConfig_SelectOutboundFormatForModel(t *testing.T) {
	provider := ProviderConfig{
		Protocols: []string{"openai.chat", "anthropic.messages"},
		Endpoints: []EndpointConfig{
			{URL: "https://anthropic.example.com", Protocols: []string{"anthropic.messages"}},
			{URL: "https://openai.example.com", Protocols: []string{"openai.chat"}},
		},
		UpstreamModels: []UpstreamModelConfig{
			{Model: "minimax-m3", AllowedProtocols: []string{"anthropic.messages"}},
		},
	}

	inbound, err := codec.NormalizeProviderFormat("openai.chat")
	if err != nil {
		t.Fatalf("normalize inbound failed: %v", err)
	}

	got, reason, cost, err := provider.SelectOutboundFormatForModel(inbound, "minimax-m3")
	if err != nil {
		t.Fatalf("SelectOutboundFormatForModel returned error: %v", err)
	}
	if got != codec.FormatAnthropicMessages {
		t.Fatalf("format=%q want=%q", got, codec.FormatAnthropicMessages)
	}
	if reason != "lowest_cost" {
		t.Fatalf("reason=%q want=%q", reason, "lowest_cost")
	}
	if cost != 2 {
		t.Fatalf("cost=%d want=%d", cost, 2)
	}
}

func TestProviderConfig_SelectOutboundFormatForModel_IgnoresUnreachableAllowedProtocol(t *testing.T) {
	provider := ProviderConfig{
		Protocols: []string{"openai.chat", "anthropic.messages"},
		Endpoints: []EndpointConfig{
			{URL: "https://anthropic.example.com", Protocols: []string{"anthropic.messages"}},
		},
		UpstreamModels: []UpstreamModelConfig{
			{Model: "glm-5.2", AllowedProtocols: []string{"openai.chat"}},
		},
	}

	inbound, err := codec.NormalizeProviderFormat("anthropic.messages")
	if err != nil {
		t.Fatalf("normalize inbound failed: %v", err)
	}

	got, reason, cost, err := provider.SelectOutboundFormatForModel(inbound, "glm-5.2")
	if err != nil {
		t.Fatalf("SelectOutboundFormatForModel returned error: %v", err)
	}
	if got != codec.FormatAnthropicMessages {
		t.Fatalf("format=%q want=%q", got, codec.FormatAnthropicMessages)
	}
	if reason != "passthrough" {
		t.Fatalf("reason=%q want=%q", reason, "passthrough")
	}
	if cost != 0 {
		t.Fatalf("cost=%d want=%d", cost, 0)
	}
}

func TestProviderConfig_SelectOutboundFormatForModel_DefaultProtocols(t *testing.T) {
	provider := ProviderConfig{
		Protocols:         []string{"openai.chat", "anthropic.messages"},
		DefaultProtocols: []string{"openai.chat"},
		Endpoints: []EndpointConfig{
			{URL: "https://anthropic.example.com", Protocols: []string{"anthropic.messages"}},
			{URL: "https://openai.example.com", Protocols: []string{"openai.chat"}},
		},
		UpstreamModels: []UpstreamModelConfig{
			{Model: "gpt-4o"}, // 没有 allowed_protocols，继承 default_protocols: ["openai"]
		},
	}

	inbound, err := codec.NormalizeProviderFormat("anthropic.messages")
	if err != nil {
		t.Fatalf("normalize inbound failed: %v", err)
	}

	got, reason, cost, err := provider.SelectOutboundFormatForModel(inbound, "gpt-4o")
	if err != nil {
		t.Fatalf("SelectOutboundFormatForModel returned error: %v", err)
	}
	if got != codec.FormatOpenAIChat {
		t.Fatalf("format=%q want=%q", got, codec.FormatOpenAIChat)
	}
	if reason != "lowest_cost" {
		t.Fatalf("reason=%q want=%q", reason, "lowest_cost")
	}
	if cost != 3 {
		t.Fatalf("cost=%d want=%d", cost, 3)
	}
}

func TestProviderConfig_SelectOutboundFormatForModel_AllowedOverridesDefault(t *testing.T) {
	provider := ProviderConfig{
		Protocols:         []string{"openai.chat", "anthropic.messages"},
		DefaultProtocols: []string{"openai.chat"},
		Endpoints: []EndpointConfig{
			{URL: "https://anthropic.example.com", Protocols: []string{"anthropic.messages"}},
			{URL: "https://openai.example.com", Protocols: []string{"openai.chat"}},
		},
		UpstreamModels: []UpstreamModelConfig{
			{Model: "minimax-m3", AllowedProtocols: []string{"anthropic.messages"}}, // 覆写 default
		},
	}

	inbound, err := codec.NormalizeProviderFormat("openai.chat")
	if err != nil {
		t.Fatalf("normalize inbound failed: %v", err)
	}

	got, reason, cost, err := provider.SelectOutboundFormatForModel(inbound, "minimax-m3")
	if err != nil {
		t.Fatalf("SelectOutboundFormatForModel returned error: %v", err)
	}
	if got != codec.FormatAnthropicMessages {
		t.Fatalf("format=%q want=%q", got, codec.FormatAnthropicMessages)
	}
	if reason != "lowest_cost" {
		t.Fatalf("reason=%q want=%q", reason, "lowest_cost")
	}
	if cost != 2 {
		t.Fatalf("cost=%d want=%d", cost, 2)
	}
}

func TestValidateForServe_RejectsDefaultProtocolsInvalidFormat(t *testing.T) {
	cfg := &Config{
		Inbound: InboundConfig{
			Auth: AuthConfig{Keys: []KeyConfig{{Name: "default", Key: "k"}}},
		},
		Providers: ProvidersConfig{Items: map[string]ProviderConfig{
			"mixed": {
				Endpoint:         "https://api.example.com",
				APIKey:           "sk-test",
				Protocols:         []string{"openai.chat", "anthropic.messages"},
				DefaultProtocols: []string{"unknown_format"},
			},
		}},
		ModelGroups: []ModelGroupConfig{{Name: "glm", Model: "mixed/glm-5.2"}},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}

	verr, ok := err.(ValidationError)
	if !ok {
		t.Fatalf("error type = %T, want ValidationError", err)
	}
	if len(verr.Issues) == 0 {
		t.Fatal("expected at least one validation issue")
	}
	if verr.Issues[0].Path != "providers.mixed.default_protocols[0]" {
		t.Fatalf("path=%q", verr.Issues[0].Path)
	}
}

func TestValidateForServe_RejectsDefaultProtocolsNotReachable(t *testing.T) {
	cfg := &Config{
		Inbound: InboundConfig{
			Auth: AuthConfig{Keys: []KeyConfig{{Name: "default", Key: "k"}}},
		},
		Providers: ProvidersConfig{Items: map[string]ProviderConfig{
			"mixed": {
				Endpoint: "https://api.example.com",
				APIKey:   "sk-test",
				Protocols: []string{"openai.chat", "anthropic.messages"},
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.example.com", Protocols: []string{"anthropic.messages"}},
				},
				DefaultProtocols: []string{"openai.chat"},
			},
		}},
		ModelGroups: []ModelGroupConfig{{Name: "glm", Model: "mixed/glm-5.2"}},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}

	verr, ok := err.(ValidationError)
	if !ok {
		t.Fatalf("error type = %T, want ValidationError", err)
	}
	if len(verr.Issues) == 0 {
		t.Fatal("expected at least one validation issue")
	}
	if verr.Issues[0].Path != "providers.mixed.default_protocols[0]" {
		t.Fatalf("path=%q", verr.Issues[0].Path)
	}
	if verr.Issues[0].Message != `protocol "openai.chat" is not reachable by this provider endpoints` {
		t.Fatalf("message=%q", verr.Issues[0].Message)
	}
}

func TestValidateForServe_RejectsUpstreamModelAllowedProtocolOutsideProvider(t *testing.T) {
	cfg := &Config{
		Inbound: InboundConfig{
			Auth: AuthConfig{Keys: []KeyConfig{{Name: "default", Key: "k"}}},
		},
		Providers: ProvidersConfig{Items: map[string]ProviderConfig{
			"mixed": {
				Endpoint: "https://api.example.com",
				APIKey:   "sk-test",
				Protocols: []string{"openai.chat"},
				UpstreamModels: []UpstreamModelConfig{
					{Model: "glm-5.2", AllowedProtocols: []string{"anthropic.messages"}},
				},
			},
		}},
		ModelGroups: []ModelGroupConfig{{Name: "glm", Model: "mixed/glm-5.2"}},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}

	verr, ok := err.(ValidationError)
	if !ok {
		t.Fatalf("error type = %T, want ValidationError", err)
	}
	if len(verr.Issues) == 0 {
		t.Fatal("expected at least one validation issue")
	}
	if verr.Issues[0].Path != "providers.mixed.upstream_model[0].allowed_protocols[0]" {
		t.Fatalf("path=%q", verr.Issues[0].Path)
	}
}

func TestValidateForServe_RejectsUpstreamModelAllowedProtocolNotReachableByEndpoints(t *testing.T) {
	cfg := &Config{
		Inbound: InboundConfig{
			Auth: AuthConfig{Keys: []KeyConfig{{Name: "default", Key: "k"}}},
		},
		Providers: ProvidersConfig{Items: map[string]ProviderConfig{
			"mixed": {
				Endpoint: "https://api.example.com",
				APIKey:   "sk-test",
				Protocols: []string{"openai.chat", "anthropic.messages"},
				Endpoints: []EndpointConfig{
					{URL: "https://anthropic.example.com", Protocols: []string{"anthropic.messages"}},
				},
				UpstreamModels: []UpstreamModelConfig{
					{Model: "glm-5.2", AllowedProtocols: []string{"openai.chat"}},
				},
			},
		}},
		ModelGroups: []ModelGroupConfig{{Name: "glm", Model: "mixed/glm-5.2"}},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}

	verr, ok := err.(ValidationError)
	if !ok {
		t.Fatalf("error type = %T, want ValidationError", err)
	}
	if len(verr.Issues) == 0 {
		t.Fatal("expected at least one validation issue")
	}
	if verr.Issues[0].Path != "providers.mixed.upstream_model[0].allowed_protocols[0]" {
		t.Fatalf("path=%q", verr.Issues[0].Path)
	}
	if verr.Issues[0].Message != "protocol \"openai.chat\" is not reachable by this provider endpoints" {
		t.Fatalf("message=%q", verr.Issues[0].Message)
	}
}

func TestProviderConfig_SupportsEmbeddingProtocol(t *testing.T) {
	tests := []struct {
		name     string
		provider ProviderConfig
		want     bool
	}{
		{
			name: "top-level protocol ollama.embed",
			provider: ProviderConfig{
				Protocols: []string{"ollama.embed"},
				Endpoint: "http://localhost:11434",
			},
			want: true,
		},
		{
			name: "endpoints with ollama.embed protocol",
			provider: ProviderConfig{
				Endpoint: "http://localhost:11434",
				Endpoints: []EndpointConfig{
					{URL: "http://localhost:11434", Protocols: []string{"ollama.embed"}},
				},
			},
			want: true,
		},
		{
			name: "top-level protocol is openai",
			provider: ProviderConfig{
				Protocols: []string{"openai.chat"},
				Endpoint: "https://api.openai.com/v1",
			},
			want: false,
		},
		{
			name: "endpoints without ollama.embed",
			provider: ProviderConfig{
				Endpoint: "http://localhost:11434",
				Endpoints: []EndpointConfig{
					{URL: "http://localhost:11434", Protocols: []string{"anthropic.messages"}},
				},
			},
			want: false,
		},
		{
			name: "no protocol at all",
			provider: ProviderConfig{
				Endpoint: "http://localhost:11434",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.provider.SupportsEmbeddingProtocol()
			if got != tt.want {
				t.Errorf("SupportsEmbeddingProtocol() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProviderConfig_SupportsOllamaChatProtocol(t *testing.T) {
	tests := []struct {
		name     string
		provider ProviderConfig
		want     bool
	}{
		{
			name:     "top-level protocol ollama.chat",
			provider: ProviderConfig{Protocols: []string{"ollama.chat"}, Endpoint: "http://localhost:11434"},
			want:     true,
		},
		{
			name: "endpoint-level protocol ollama.chat",
			provider: ProviderConfig{
				Endpoints: []EndpointConfig{
					{URL: "http://localhost:11434", Protocols: []string{"ollama.chat"}},
				},
			},
			want: true,
		},
		{
			name:     "openai protocol is not ollama.chat",
			provider: ProviderConfig{Protocols: []string{"openai.chat"}, Endpoint: "https://api.openai.com/v1"},
			want:     false,
		},
		{
			name:     "top-level mixed protocol still reports ollama.chat support",
			provider: ProviderConfig{Protocols: []string{"openai.chat", "ollama.chat"}, Endpoint: "http://localhost:11434"},
			want:     true,
		},
		{
			name:     "ollama.embed is not ollama.chat",
			provider: ProviderConfig{Protocols: []string{"ollama.embed"}, Endpoint: "http://localhost:11434"},
			want:     false,
		},
		{
			name:     "no protocol",
			provider: ProviderConfig{Endpoint: "http://localhost:11434"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.provider.SupportsOllamaChatProtocol()
			if got != tt.want {
				t.Errorf("SupportsOllamaChatProtocol() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProviderConfig_GetEndpoint_OllamaChat(t *testing.T) {
	provider := ProviderConfig{
		Endpoint: "http://default:11434",
		Endpoints: []EndpointConfig{
			{URL: "http://ollama-chat:11434", Protocols: []string{"ollama.chat"}},
		},
	}

	got := provider.GetEndpoint("ollama.chat")
	want := "http://ollama-chat:11434"
	if got != want {
		t.Errorf("GetEndpoint(ollama.chat) = %q, want %q", got, want)
	}
}

func TestProviderConfig_GetEmbeddingEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		provider ProviderConfig
		want     string
	}{
		{
			name: "first endpoints with ollama.embed protocol",
			provider: ProviderConfig{
				Endpoint: "http://default:11434",
				Endpoints: []EndpointConfig{
					{URL: "http://endpoint1:11434", Protocols: []string{"ollama.embed"}},
					{URL: "http://endpoint2:11434", Protocols: []string{"ollama.embed"}},
				},
			},
			want: "http://endpoint1:11434",
		},
		{
			name: "fallback to endpoint field when no ollama.embed in endpoints",
			provider: ProviderConfig{
				Endpoint: "http://default:11434",
				Endpoints: []EndpointConfig{
					{URL: "http://other:11434", Protocols: []string{"anthropic.messages"}},
				},
			},
			want: "http://default:11434",
		},
		{
			name: "endpoint field when endpoints empty",
			provider: ProviderConfig{
				Endpoint:  "http://default:11434",
				Endpoints: []EndpointConfig{},
			},
			want: "http://default:11434",
		},
		{
			name: "top-level endpoint field no endpoints",
			provider: ProviderConfig{
				Endpoint: "http://default:11434",
			},
			want: "http://default:11434",
		},
		{
			name: "empty when no valid endpoint",
			provider: ProviderConfig{
				Endpoint:  "",
				Endpoints: []EndpointConfig{},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.provider.GetEmbeddingEndpoint()
			if got != tt.want {
				t.Errorf("GetEmbeddingEndpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestModelGroupConfig_ContextLength(t *testing.T) {
	tests := []struct {
		name              string
		yaml              string
		wantContextLength *int
	}{
		{
			name: "context_length set",
			yaml: `
model_groups:
  - name: "gpt-4"
    model: "openai/gpt-4"
    model_metadata:
      context_length: 8192
`,
			wantContextLength: func() *int { v := 8192; return &v }(),
		},
		{
			name: "context_length absent",
			yaml: `
model_groups:
  - name: "gpt-4"
    model: "openai/gpt-4"
`,
			wantContextLength: nil,
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
    protocols: ["openai.chat"]
` + tt.yaml
			if err := os.WriteFile(configPath, []byte(fullYaml), 0644); err != nil {
				t.Fatalf("failed to write config file: %v", err)
			}

			cfg, err := Load(configPath)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			g := cfg.ModelGroups[0]
			if tt.wantContextLength == nil {
				if g.ModelMetadata.ContextLength != nil {
					t.Errorf("expected nil context_length, got %d", *g.ModelMetadata.ContextLength)
				}
			} else {
				if g.ModelMetadata.ContextLength == nil {
					t.Fatalf("expected context_length %d, got nil", *tt.wantContextLength)
				}
				if *g.ModelMetadata.ContextLength != *tt.wantContextLength {
					t.Errorf("expected context_length %d, got %d", *tt.wantContextLength, *g.ModelMetadata.ContextLength)
				}
			}
		})
	}
}


func TestParseTimeRange(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    DisabledTimeRange
		wantErr bool
	}{
		{
			name: "normal range",
			raw:  "09:00-12:00",
			want: DisabledTimeRange{Start: 540, End: 720},
		},
		{
			name: "single digit hour",
			raw:  "9:00-12:00",
			want: DisabledTimeRange{Start: 540, End: 720},
		},
		{
			name: "cross midnight",
			raw:  "23:00-02:00",
			want: DisabledTimeRange{Start: 1380, End: 120},
		},
		{
			name: "full day range",
			raw:  "00:00-24:00",
			want: DisabledTimeRange{Start: 0, End: 1440},
		},
		{
			name: "short window",
			raw:  "14:30-14:45",
			want: DisabledTimeRange{Start: 870, End: 885},
		},
		{
			name:    "invalid format no dash",
			raw:     "09:00",
			wantErr: true,
		},
		{
			name:    "invalid format empty",
			raw:     "",
			wantErr: true,
		},
		{
			name:    "equal start and end",
			raw:     "12:00-12:00",
			wantErr: true,
		},
		{
			name:    "invalid hour",
			raw:     "25:00-12:00",
			wantErr: true,
		},
		{
			name:    "invalid minute",
			raw:     "09:60-12:00",
			wantErr: true,
		},
		{
			name:    "negative hour",
			raw:     "-1:00-12:00",
			wantErr: true,
		},
		{
			name:    "invalid end time",
			raw:     "09:00-25:00",
			wantErr: true,
		},
		{
			name:    "24:01 invalid",
			raw:     "24:01-00:00",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTimeRange(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Start != tt.want.Start {
				t.Errorf("Start = %d, want %d", got.Start, tt.want.Start)
			}
			if got.End != tt.want.End {
				t.Errorf("End = %d, want %d", got.End, tt.want.End)
			}
		})
	}
}

func TestIsInDisabledTimeRange(t *testing.T) {
	// 使用固定时区 Asia/Shanghai 进行测试
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("failed to load location: %v", err)
	}

	tests := []struct {
		name     string
		ranges   []DisabledTimeRange
		timeStr  string // "15:04" format time in Asia/Shanghai
		expected bool
	}{
		{
			name: "hit normal range start boundary",
			ranges: []DisabledTimeRange{
				{Start: 540, End: 720}, // 09:00-12:00
			},
			timeStr:  "09:00",
			expected: true,
		},
		{
			name: "hit normal range middle",
			ranges: []DisabledTimeRange{
				{Start: 540, End: 720},
			},
			timeStr:  "10:30",
			expected: true,
		},
		{
			name: "not hit normal range at end boundary",
			ranges: []DisabledTimeRange{
				{Start: 540, End: 720},
			},
			timeStr:  "12:00",
			expected: false,
		},
		{
			name: "not hit normal range before start",
			ranges: []DisabledTimeRange{
				{Start: 540, End: 720},
			},
			timeStr:  "08:59",
			expected: false,
		},
		{
			name: "not hit normal range after end",
			ranges: []DisabledTimeRange{
				{Start: 540, End: 720},
			},
			timeStr:  "12:01",
			expected: false,
		},
		{
			name: "hit cross midnight before midnight",
			ranges: []DisabledTimeRange{
				{Start: 1380, End: 120}, // 23:00-02:00
			},
			timeStr:  "23:30",
			expected: true,
		},
		{
			name: "hit cross midnight at start boundary",
			ranges: []DisabledTimeRange{
				{Start: 1380, End: 120},
			},
			timeStr:  "23:00",
			expected: true,
		},
		{
			name: "hit cross midnight after midnight",
			ranges: []DisabledTimeRange{
				{Start: 1380, End: 120},
			},
			timeStr:  "01:30",
			expected: true,
		},
		{
			name: "not hit cross midnight at end boundary",
			ranges: []DisabledTimeRange{
				{Start: 1380, End: 120},
			},
			timeStr:  "02:00",
			expected: false,
		},
		{
			name: "not hit cross midnight in daytime",
			ranges: []DisabledTimeRange{
				{Start: 1380, End: 120},
			},
			timeStr:  "12:00",
			expected: false,
		},
		{
			name: "multiple ranges, hit second",
			ranges: []DisabledTimeRange{
				{Start: 0, End: 360},   // 00:00-06:00
				{Start: 720, End: 840}, // 12:00-14:00
			},
			timeStr:  "13:00",
			expected: true,
		},
		{
			name: "multiple ranges, hit none",
			ranges: []DisabledTimeRange{
				{Start: 0, End: 360},
				{Start: 720, End: 840},
			},
			timeStr:  "10:00",
			expected: false,
		},
		{
			name:     "empty ranges",
			ranges:   nil,
			timeStr:  "09:00",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse the time string and construct a time.Time in Asia/Shanghai
			parts := strings.SplitN(tt.timeStr, ":", 2)
			if len(parts) != 2 {
				t.Fatalf("invalid timeStr: %q", tt.timeStr)
			}
			h, _ := strconv.Atoi(parts[0])
			m, _ := strconv.Atoi(parts[1])
			now := time.Date(2026, 1, 1, h, m, 0, 0, loc)

			got := IsInDisabledTimeRange(now, tt.ranges)
			if got != tt.expected {
				t.Errorf("IsInDisabledTimeRange(%s, ...) = %v, want %v", tt.timeStr, got, tt.expected)
			}
		})
	}
}

func TestProviderConfig_DisabledTimeRangesYAML(t *testing.T) {
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
    protocols: ["openai.chat"]
    disabled_time_ranges:
      - "09:00-12:00"
      - "14:00-18:00"
      - "23:00-02:00"
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

	pc := cfg.Providers.Items["openai"]
	if len(pc.DisabledTimeRanges) != 3 {
		t.Fatalf("expected 3 disabled_time_ranges, got %d: %v", len(pc.DisabledTimeRanges), pc.DisabledTimeRanges)
	}
	if pc.DisabledTimeRanges[0] != "09:00-12:00" {
		t.Errorf("range[0] = %q, want %q", pc.DisabledTimeRanges[0], "09:00-12:00")
	}
	if pc.DisabledTimeRanges[1] != "14:00-18:00" {
		t.Errorf("range[1] = %q, want %q", pc.DisabledTimeRanges[1], "14:00-18:00")
	}
	if pc.DisabledTimeRanges[2] != "23:00-02:00" {
		t.Errorf("range[2] = %q, want %q", pc.DisabledTimeRanges[2], "23:00-02:00")
	}
}

func TestAdminGetSessionSecret(t *testing.T) {
	tests := []struct {
		name     string
		admin    AdminConfig
		expected string
	}{
		{
			name: "auto-derived from password",
			admin: AdminConfig{
				Password: "mypassword",
			},
			expected: "", // 会动态计算，只验证非空且为 hex 编码
		},
		{
			name: "empty password returns empty secret",
			admin: AdminConfig{
				Password: "",
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := tt.admin.GetSessionSecret()
			if tt.name == "auto-derived from password" {
				if secret == "" {
					t.Error("expected non-empty derived secret")
				}
				// hex 编码后应该是 64 字符（32 字节 * 2）
				if len(secret) != 64 {
					t.Errorf("expected 64-character hex-encoded secret, got %d", len(secret))
				}
				// 验证是有效的 hex 字符串
				for _, c := range secret {
					if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
						t.Errorf("expected hex-encoded secret, got invalid character %c", c)
						break
					}
				}
			} else {
				if secret != tt.expected {
					t.Errorf("expected %q, got %q", tt.expected, secret)
				}
			}
		})
	}
}

func TestSmartRouteYAML(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantSR  *SmartRouteConfig
		wantErr bool
	}{
		{
			name: "smart_route block parsed correctly",
			yaml: `
smart_route:
  cheap: "cheap-group"
  scout: "scout-group"
  enabled_models:
    - "alias-a"
    - "group-b"
`,
			wantSR: &SmartRouteConfig{
				Cheap:         "cheap-group",
				Scout:         "scout-group",
				EnabledModels: []string{"alias-a", "group-b"},
			},
		},
		{
			name: "smart_route missing results in nil",
			yaml: `
model_groups:
  - name: "gpt-4"
    model: "openai/gpt-4"
`,
			wantSR: nil,
		},
		{
			name: "empty enabled_models",
			yaml: `
smart_route:
  cheap: "cheap-group"
  scout: "scout-group"
  enabled_models: []
`,
			wantSR: &SmartRouteConfig{
				Cheap:         "cheap-group",
				Scout:         "scout-group",
				EnabledModels: []string{},
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
    protocols: ["openai.chat"]
` + tt.yaml
			if err := os.WriteFile(configPath, []byte(fullYaml), 0644); err != nil {
				t.Fatalf("failed to write config file: %v", err)
			}

			cfg, err := Load(configPath)
			if err != nil {
				if tt.wantErr {
					return
				}
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantSR == nil {
				if cfg.SmartRoute != nil {
					t.Errorf("expected SmartRoute to be nil, got %+v", cfg.SmartRoute)
				}
				return
			}

			if cfg.SmartRoute == nil {
				t.Fatalf("expected SmartRoute to be non-nil")
			}

			if cfg.SmartRoute.Cheap != tt.wantSR.Cheap {
				t.Errorf("Cheap = %q, want %q", cfg.SmartRoute.Cheap, tt.wantSR.Cheap)
			}
			if cfg.SmartRoute.Scout != tt.wantSR.Scout {
				t.Errorf("Scout = %q, want %q", cfg.SmartRoute.Scout, tt.wantSR.Scout)
			}
			if len(cfg.SmartRoute.EnabledModels) != len(tt.wantSR.EnabledModels) {
				t.Errorf("EnabledModels length = %d, want %d", len(cfg.SmartRoute.EnabledModels), len(tt.wantSR.EnabledModels))
			}
			for i, got := range cfg.SmartRoute.EnabledModels {
				if i >= len(tt.wantSR.EnabledModels) {
					break
				}
				want := tt.wantSR.EnabledModels[i]
				if got != want {
					t.Errorf("EnabledModels[%d] = %q, want %q", i, got, want)
				}
			}
		})
	}
}

func TestNormalizeExposure(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		want      Exposure
		wantError bool
	}{
		{name: "empty defaults to public", raw: "", want: ExposurePublic, wantError: false},
		{name: "explicit public", raw: "public", want: ExposurePublic, wantError: false},
		{name: "hidden", raw: "hidden", want: ExposureHidden, wantError: false},
		{name: "internal", raw: "internal", want: ExposureInternal, wantError: false},
		{name: "invalid value", raw: "invalid", want: "", wantError: true},
		{name: "visible (old name)", raw: "visible", want: "", wantError: true},
		{name: "none", raw: "none", want: "", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeExposure(tt.raw)
			if tt.wantError {
				if err == nil {
					t.Errorf("expected error for raw %q, got nil", tt.raw)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for raw %q: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeExposure(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestExposureYAMLParsing(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantExpos []Exposure // expected exposures for each group
		wantError bool
	}{
		{
			name: "valid exposure values",
			yaml: `
server:
  listen: ":18000"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "pub"
    exposure: public
    models:
      - "openai/gpt-4"
  - name: "hid"
    exposure: hidden
    models:
      - "openai/gpt-4"
  - name: "int"
    exposure: internal
    models:
      - "openai/gpt-4"
redirect:
  - source: "r-pub"
    target: "pub"
    exposure: public
  - source: "r-hid"
    target: "pub"
    exposure: hidden
  - source: "r-int"
    target: "pub"
    exposure: internal
`,
			wantExpos: []Exposure{ExposurePublic, ExposureHidden, ExposureInternal, ExposurePublic, ExposureHidden, ExposureInternal},
			wantError: false,
		},
		{
			name: "omitted exposure defaults to public",
			yaml: `
server:
  listen: ":18000"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "default-group"
    models:
      - "openai/gpt-4"
redirect:
  - source: "default-redirect"
    target: "default-group"
`,
			wantExpos: []Exposure{ExposurePublic, ExposurePublic},
			wantError: false,
		},
		{
			name: "invalid exposure in model_group fails",
			yaml: `
server:
  listen: ":18000"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "bad"
    exposure: invalid
    models:
      - "openai/gpt-4"
`,
			wantError: true,
		},
		{
			name: "invalid exposure in redirect fails",
			yaml: `
server:
  listen: ":18000"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
model_groups:
  - name: "gpt"
    models:
      - "openai/gpt-4"
redirect:
  - source: "bad"
    target: "gpt"
    exposure: invalid
`,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(tt.yaml), 0644); err != nil {
				t.Fatalf("failed to write config file: %v", err)
			}

			cfg, err := Load(configPath)
			if tt.wantError {
				if err == nil {
					t.Error("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// 验证 model_group 的 exposure
			for i, mg := range cfg.ModelGroups {
				if i >= len(tt.wantExpos) {
					break
				}
				got := ExposurePublic
				if mg.Exposure != nil {
					got = *mg.Exposure
				}
				if got != tt.wantExpos[i] {
					t.Errorf("model_group[%d] exposure = %q, want %q", i, got, tt.wantExpos[i])
				}
			}

			// 验证 redirect 的 exposure
			nGroups := len(cfg.ModelGroups)
			for i, rd := range cfg.Redirect {
				idx := nGroups + i
				if idx >= len(tt.wantExpos) {
					break
				}
				got := ExposurePublic
				if rd.Exposure != nil {
					got = *rd.Exposure
				}
				if got != tt.wantExpos[idx] {
					t.Errorf("redirect[%d] exposure = %q, want %q", i, got, tt.wantExpos[idx])
				}
			}
		})
	}
}
