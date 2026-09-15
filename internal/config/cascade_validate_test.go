package config

import (
	"os"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/codec"
)

func cascadeHubProvider(name string) ProviderConfig {
	return ProviderConfig{
		Protocols: []string{
			string(codec.FormatOpenAIChat),
			string(codec.FormatOpenAIResponse),
			string(codec.FormatAnthropicMessages),
		},
		Cascade: &ProviderCascadeConfig{
			Enabled: true,
			Token:   "hub-token",
		},
	}
}

func spokeCascadeConfig() *SpokeCascadeConfig {
	return &SpokeCascadeConfig{
		Hub:   "https://api.example.com",
		Token: "spoke-token",
		Peer:  "openai",
	}
}

func TestValidateCascade_AcceptsValidHubProvider(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["corp-dev"] = cascadeHubProvider("corp-dev")

	if err := ValidateCascade(cfg); err != nil {
		t.Fatalf("expected valid hub cascade provider, got: %v", err)
	}
}

func TestValidateCascade_AcceptsValidSpokeConfig(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Cascade = spokeCascadeConfig()

	if err := ValidateCascade(cfg); err != nil {
		t.Fatalf("expected valid spoke config, got: %v", err)
	}
}

// spokeConfigWithGroups 构建启用顶层 Spoke Cascade、包含给定 group 名的配置。
func spokeConfigWithGroups(t *testing.T, names ...string) *Config {
	t.Helper()
	cfg := validConfigFixture()
	groups := make([]ModelGroupConfig, len(names))
	for i, name := range names {
		groups[i] = ModelGroupConfig{
			Name:   name,
			Models: ModelEntries{{Model: "openai/gpt-4"}},
		}
	}
	cfg.ModelGroups = groups
	cfg.Cascade = spokeCascadeConfig()
	return cfg
}

func TestValidateCascade_RejectsTooManyCallableEntries(t *testing.T) {
	names := make([]string, MaxMetadataSnapshotEntries+1)
	for i := range names {
		names[i] = "group-" + strings.Repeat("a", 6) + itoa(i)
	}
	cfg := spokeConfigWithGroups(t, names...)

	err := ValidateCascade(cfg)
	if err == nil {
		t.Fatal("expected rejection for over-limit callable entries")
	}
	if !strings.Contains(err.Error(), "callable") || !strings.Contains(err.Error(), itoa(MaxMetadataSnapshotEntries)) {
		t.Fatalf("error = %q, want over-limit message mentioning max %d", err.Error(), MaxMetadataSnapshotEntries)
	}
}

func TestValidateCascade_AcceptsBoundaryCallableEntries(t *testing.T) {
	names := make([]string, MaxMetadataSnapshotEntries)
	for i := range names {
		names[i] = "group-" + strings.Repeat("b", 5) + itoa(i)
	}
	cfg := spokeConfigWithGroups(t, names...)
	if err := ValidateCascade(cfg); err != nil {
		t.Fatalf("boundary %d entries should pass, got: %v", MaxMetadataSnapshotEntries, err)
	}

	// 恰好 256 UTF-8 字节的名称可通过；257 字节拒绝。
	ok := strings.Repeat("c", MaxMetadataModelNameBytes)
	if err := ValidateCascade(spokeConfigWithGroups(t, ok)); err != nil {
		t.Fatalf("exactly %d-byte name should pass, got: %v", MaxMetadataModelNameBytes, err)
	}

	tooLong := strings.Repeat("d", MaxMetadataModelNameBytes+1)
	err := ValidateCascade(spokeConfigWithGroups(t, tooLong))
	if err == nil {
		t.Fatal("expected rejection for oversized entry name")
	}
	if !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("error = %q, want byte-length message", err.Error())
	}
}

func TestValidateCascade_NoLimitWithoutSpokeCascade(t *testing.T) {
	// 未启用顶层 Spoke Cascade 时，同规模 public 集合不受协议上限限制。
	names := make([]string, MaxMetadataSnapshotEntries+1)
	for i := range names {
		names[i] = "group-" + strings.Repeat("e", 5) + itoa(i)
	}
	cfg := spokeConfigWithGroups(t, names...)
	cfg.Cascade = nil
	if err := ValidateCascade(cfg); err != nil {
		t.Fatalf("over-limit entries without spoke cascade should pass, got: %v", err)
	}
}

func TestListDirectCallableNames(t *testing.T) {
	public := ExposurePublic
	hidden := ExposureHidden
	internal := ExposureInternal
	cfg := &Config{
		Providers: ProvidersConfig{Items: map[string]ProviderConfig{
			"openai": {Endpoint: "https://api.openai.com", APIKey: "k", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []ModelGroupConfig{
			{Name: "zeta", Models: ModelEntries{{Model: "openai/gpt-4"}}}, // 默认 public
			{Name: "alpha", Exposure: &public, Models: ModelEntries{{Model: "openai/gpt-4"}}},
			{Name: "hid", Exposure: &hidden, Models: ModelEntries{{Model: "openai/gpt-4"}}},
			{Name: "int", Exposure: &internal, Models: ModelEntries{{Model: "openai/gpt-4"}}},
		},
		Redirect: RedirectConfigs{
			{Source: "mike-alias", Target: "alpha", Exposure: &public},
			{Source: "dup-alias", Target: "hid", Exposure: &hidden},
			{Source: "int-alias", Target: "int", Exposure: &internal},
		},
	}

	got := ListDirectCallableNames(cfg)
	want := []string{"alpha", "dup-alias", "hid", "mike-alias", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names[%d] = %q, want %q (sorted, dedup, no internal)", i, got[i], want[i])
		}
	}
}

// itoa 供测试生成确定性名称后缀。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

func TestValidateCascade_RejectsMultipleEnabledProviders(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["corp-a"] = cascadeHubProvider("corp-a")
	cfg.Providers.Items["corp-b"] = cascadeHubProvider("corp-b")

	err := ValidateCascade(cfg)
	if err == nil {
		t.Fatal("expected error for multiple cascade.enabled providers")
	}
	if !strings.Contains(err.Error(), "cascade.enabled") {
		t.Fatalf("expected cascade.enabled error, got: %v", err)
	}
}

func TestValidateCascade_RejectsRemoteBridgeAndCascadeBothEnabled(t *testing.T) {
	cfg := validConfigFixture()
	p := cascadeHubProvider("corp-dev")
	p.RemoteBridge = &RemoteBridgeConfig{Enabled: true, Provider: "xai-oauth", Token: "bridge-token"}
	cfg.Providers.Items["corp-dev"] = p

	err := ValidateCascade(cfg)
	if err == nil {
		t.Fatal("expected error when remote_bridge and cascade both enabled")
	}
	if !strings.Contains(err.Error(), "remote_bridge") || !strings.Contains(err.Error(), "cascade") {
		t.Fatalf("expected mutual exclusion error, got: %v", err)
	}
}

func TestValidateCascade_RejectsHubAndSpokeTogether(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Cascade = spokeCascadeConfig()
	cfg.Providers.Items["corp-dev"] = cascadeHubProvider("corp-dev")

	err := ValidateCascade(cfg)
	if err == nil {
		t.Fatal("expected error when hub cascade provider and spoke config coexist")
	}
	if !strings.Contains(err.Error(), "cascade") {
		t.Fatalf("expected hub/spoke mutual exclusion error, got: %v", err)
	}
}

func TestValidateCascade_RejectsEnabledWithoutToken(t *testing.T) {
	cfg := validConfigFixture()
	p := cascadeHubProvider("corp-dev")
	p.Cascade.Token = ""
	cfg.Providers.Items["corp-dev"] = p

	err := ValidateCascade(cfg)
	if err == nil {
		t.Fatal("expected error when cascade.enabled without token")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("expected token error, got: %v", err)
	}
}

func TestValidateCascade_RejectsWrongProtocolSet(t *testing.T) {
	tests := []struct {
		name      string
		protocols []string
	}{
		{"only two protocols", []string{"openai.chat", "anthropic.messages"}},
		{"includes embeddings", []string{"openai.chat", "openai.responses", "anthropic.messages", "openai.embeddings"}},
		{"empty protocols", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfigFixture()
			p := cascadeHubProvider("corp-dev")
			p.Protocols = tt.protocols
			cfg.Providers.Items["corp-dev"] = p

			err := ValidateCascade(cfg)
			if err == nil {
				t.Fatal("expected protocol set validation error")
			}
			if !strings.Contains(err.Error(), "protocol") {
				t.Fatalf("expected protocol error, got: %v", err)
			}
		})
	}
}

func TestValidateCascade_RejectsInvalidSpokeHub(t *testing.T) {
	tests := []struct {
		name string
		hub  string
	}{
		{"wss scheme", "wss://api.example.com"},
		{"ws scheme", "ws://api.example.com"},
		{"with path", "https://api.example.com/cascade"},
		{"trailing slash only", "https://api.example.com/"},
		{"with userinfo", "https://user:pass@api.example.com"},
		{"with query", "https://api.example.com?x=1"},
		{"with fragment", "https://api.example.com#frag"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfigFixture()
			spoke := spokeCascadeConfig()
			spoke.Hub = tt.hub
			cfg.Cascade = spoke

			err := ValidateCascade(cfg)
			if err == nil {
				t.Fatalf("expected error for hub %q", tt.hub)
			}
			if !strings.Contains(err.Error(), "hub") {
				t.Fatalf("expected hub error, got: %v", err)
			}
		})
	}
}

func TestValidateCascade_AcceptsSpokeHubOrigins(t *testing.T) {
	tests := []string{
		"https://api.example.com",
		"http://localhost:8080",
		"https://api.example.com:443",
	}

	for _, hub := range tests {
		t.Run(hub, func(t *testing.T) {
			cfg := validConfigFixture()
			spoke := spokeCascadeConfig()
			spoke.Hub = hub
			cfg.Cascade = spoke

			if err := ValidateCascade(cfg); err != nil {
				t.Fatalf("expected valid hub %q, got: %v", hub, err)
			}
		})
	}
}

func TestValidateCascade_AcceptsProtocolsViaEndpointsOnly(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["corp-dev"] = ProviderConfig{
		Cascade: &ProviderCascadeConfig{
			Enabled: true,
			Token:   "hub-token",
		},
		Endpoints: []EndpointConfig{
			{URL: "https://unused1", Protocols: []string{"openai.chat"}},
			{URL: "https://unused2", Protocols: []string{"openai.responses"}},
			{URL: "https://unused3", Protocols: []string{"anthropic.messages"}},
		},
	}

	if err := ValidateCascade(cfg); err != nil {
		t.Fatalf("expected valid cascade provider with endpoints-only protocols, got: %v", err)
	}
}

func TestValidateCascade_RejectsExtraProtocolViaEndpoints(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["corp-dev"] = ProviderConfig{
		Cascade: &ProviderCascadeConfig{
			Enabled: true,
			Token:   "hub-token",
		},
		Endpoints: []EndpointConfig{
			{URL: "https://unused1", Protocols: []string{"openai.chat"}},
			{URL: "https://unused2", Protocols: []string{"openai.responses"}},
			{URL: "https://unused3", Protocols: []string{"anthropic.messages"}},
			{URL: "https://unused4", Protocols: []string{"ollama.chat"}},
		},
	}

	err := ValidateCascade(cfg)
	if err == nil {
		t.Fatal("expected error when endpoints include extra protocol")
	}
	if !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("expected protocol error, got: %v", err)
	}
}

func TestValidateCascade_RejectsSpokePeerNotFound(t *testing.T) {
	cfg := validConfigFixture()
	spoke := spokeCascadeConfig()
	spoke.Peer = "missing-provider"
	cfg.Cascade = spoke

	err := ValidateCascade(cfg)
	if err == nil {
		t.Fatal("expected error when spoke peer provider not found")
	}
	if !strings.Contains(err.Error(), "peer") {
		t.Fatalf("expected peer error, got: %v", err)
	}
}

func TestValidateCascade_AcceptsSpokeConfigWithoutOffer(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Cascade = spokeCascadeConfig()

	if err := ValidateCascade(cfg); err != nil {
		t.Fatalf("expected spoke config without offer to be valid, got: %v", err)
	}
}

func TestValidateForServe_SkipsEndpointForCascadeEnabled(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["corp-dev"] = cascadeHubProvider("corp-dev")
	cfg.ModelGroups = append(cfg.ModelGroups, ModelGroupConfig{
		Name:   "corp",
		Models: ModelEntries{{Model: "corp-dev/gpt-4o"}},
	})

	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("expected cascade.enabled provider without endpoint to pass ValidateForServe, got: %v", err)
	}
}

func TestValidateForServe_StillRequiresEndpointWhenCascadeDisabled(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["corp-dev"] = ProviderConfig{
		Protocols: []string{"openai.chat"},
		Cascade:   &ProviderCascadeConfig{Enabled: false, Token: "unused"},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error when cascade.enabled is false and no endpoint")
	}
	if !strings.Contains(err.Error(), "providers.corp-dev.endpoint") {
		t.Fatalf("expected endpoint error, got: %v", err)
	}
}

func TestLoad_ValidateCascade(t *testing.T) {
	yaml := `
server:
  listen: ":8080"
providers:
  openai:
    endpoint: "https://api.openai.com/v1"
    api_key: "sk-xxx"
    protocols: ["openai.chat"]
  corp-dev:
    protocols: ["openai.chat", "openai.responses", "anthropic.messages"]
    cascade:
      enabled: true
      token: "hub-secret"
model_groups:
  - name: "gpt-4o"
    model: "openai/gpt-4o"
`
	tmpDir := t.TempDir()
	configPath := tmpDir + "/config.yaml"
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Providers.Items["corp-dev"].Cascade == nil || !cfg.Providers.Items["corp-dev"].Cascade.Enabled {
		t.Fatal("expected corp-dev cascade to load")
	}
}
