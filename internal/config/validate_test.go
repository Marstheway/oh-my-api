package config

import (
	"strings"
	"testing"
)

// validConfigFixture 返回一个最小合法配置，所有校验都能通过
func validConfigFixture() *Config {
	return &Config{
		Server: ServerConfig{
			Listen:  ":18000",
			Timeout: "30s",
			HealthCheck: HealthCheckConfig{
				FailureThreshold: 3,
				Cooldown:         "30s",
			},
		},
		Inbound: InboundConfig{
			Auth: AuthConfig{
				Keys: []KeyConfig{{Name: "test", Key: "sk-test"}},
			},
		},
		Providers: ProvidersConfig{
			Items: map[string]ProviderConfig{
				"openai": {
					Endpoint: "https://api.openai.com/v1",
					APIKey:   "sk-xxx",
					Protocols: []string{"openai.chat"},
				},
			},
		},
		ModelGroups: []ModelGroupConfig{
			{
				Name:   "gpt-4o",
				Models: ModelEntries{{Model: "openai/gpt-4o", Weight: 1}},
			},
		},
	}
}

func TestValidateForServe_BaseFields(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Server.Timeout = "bad"

	warnings, err := ValidateForServe(cfg)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateForServe_BaseFields_DoesNotSetDefaults(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Server.Listen = ""

	warnings, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	if cfg.Server.Listen != "" {
		t.Fatalf("validate should not set defaults, got %q", cfg.Server.Listen)
	}
}

func TestValidateForServe_ModelGroupModeLoadbalanceAlias(t *testing.T) {
	cfg := validConfigFixture()
	cfg.ModelGroups[0].Mode = "loadbalance"

	warnings, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
}

func TestValidationError_Error(t *testing.T) {
	err := ValidationError{Issues: []ValidationIssue{
		{Path: "providers.openai.protocol", Message: "invalid protocol"},
		{Path: "model_groups[0].models[0]", Message: "provider not found"},
	}}

	msg := err.Error()
	if !strings.Contains(msg, "config validation failed (2 issues)") {
		t.Fatalf("unexpected header: %s", msg)
	}
	if !strings.Contains(msg, "providers.openai.protocol") {
		t.Fatalf("missing path in error: %s", msg)
	}
}

func TestValidateForServe_ModelGroupProviderWarningAndError(t *testing.T) {
	cfg := validConfigFixture()
	cfg.ModelGroups = []ModelGroupConfig{
		{
			Name: "gpt-4o",
			Models: ModelEntries{
				{Model: "openai/gpt-4o", Weight: 1},
				{Model: "missing/gpt-4o", Weight: 1},
			},
		},
	}

	warnings, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning, got %d: %+v", len(warnings), warnings)
	}

	cfg.ModelGroups[0].Models = ModelEntries{{Model: "missing/gpt-4o", Weight: 1}}
	warnings, err = ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error when no valid model entries")
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
}

// TestValidateForServe_DoesNotValidateRedirect 验证 ValidateForServe 不校验 redirect 目标，
// redirect 校验由 model.NewResolver 的 validateAndApplyRedirect 负责。
func TestValidateForServe_DoesNotValidateRedirect(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Redirect = RedirectConfigs{{Source: "alias", Target: "missing-target"}}

	warnings, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("unexpected error from ValidateForServe: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
}

func TestValidateForServe_RejectsDeprecatedProvidersTimeout(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Timeout = "640s"

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error")
	}

	if !strings.Contains(err.Error(), "providers.timeout") {
		t.Fatalf("expected providers.timeout in error, got: %v", err)
	}
}

func TestValidateForServe_RejectsDeprecatedModelGroupTimeout(t *testing.T) {
	cfg := validConfigFixture()
	cfg.ModelGroups[0].Timeout = "60s"

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error")
	}

	if !strings.Contains(err.Error(), "model_groups[0].timeout") {
		t.Fatalf("expected model_groups[0].timeout in error, got: %v", err)
	}
}

func TestValidateForServe_OllamaEmbed_ValidTopLevelProtocol(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["ollama"] = ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "",
		Protocols: []string{"ollama.embed"},
	}
	cfg.ModelGroups = append(cfg.ModelGroups, ModelGroupConfig{
		Name:   "ollama-embed",
		Models: ModelEntries{{Model: "ollama/all-minilm", Weight: 1}},
	})

	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("expected no error for valid ollama.embed provider, got: %v", err)
	}
}

func TestValidateForServe_OllamaEmbed_ValidEndpointProtocol(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["multi"] = ProviderConfig{
		Endpoint: "http://default:11434",
		APIKey:   "sk-xxx",
		Endpoints: []EndpointConfig{
			{URL: "http://embedding:11434", Protocols: []string{"ollama.embed"}},
		},
	}

	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("expected no error for provider with endpoints containing ollama.embed, got: %v", err)
	}
}

func TestValidateForServe_OllamaEmbed_RejectsMixedWithChat(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["badmixed"] = ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "sk-xxx",
		Protocols: []string{"ollama.embed", "openai.chat"},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for provider mixing ollama.embed with chat protocol")
	}
	if !strings.Contains(err.Error(), "providers.badmixed.protocol") {
		t.Fatalf("expected error path providers.badmixed.protocol, got: %v", err)
	}
}

func TestValidateForServe_OllamaEmbed_RejectsMixedInEndpoints(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["badmixed"] = ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "sk-xxx",
		Endpoints: []EndpointConfig{
			{URL: "http://embedding:11434", Protocols: []string{"ollama.embed"}},
			{URL: "http://chat:11434", Protocols: []string{"openai.chat"}},
		},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for provider mixing ollama.embed with chat protocol in endpoints")
	}
	if !strings.Contains(err.Error(), "providers.badmixed") {
		t.Fatalf("expected error mentioning providers.badmixed, got: %v", err)
	}
}

func TestValidateForServe_OllamaEmbed_RejectsTopLevelChatWithEndpointOllamaEmbed(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["badmixed"] = ProviderConfig{
		Endpoint: "http://chat:11434",
		APIKey:   "sk-xxx",
		Protocols: []string{"openai.chat"},
		Endpoints: []EndpointConfig{
			{URL: "http://embedding:11434", Protocols: []string{"ollama.embed"}},
		},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for provider mixing top-level chat protocol with endpoints ollama.embed")
	}
}

func TestValidateForServe_OllamaEmbed_RejectsInvalidTopLevelProtocolAlongsideEmbedding(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["badollama"] = ProviderConfig{
		Endpoint: "http://localhost:11434",
		Protocols: []string{"typo"},
		Endpoints: []EndpointConfig{
			{URL: "http://embedding:11434", Protocols: []string{"ollama.embed"}},
		},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for invalid top-level protocol alongside ollama.embed")
	}
	if !strings.Contains(err.Error(), "providers.badollama.protocol") {
		t.Fatalf("expected protocol error, got: %v", err)
	}
}

func TestValidateForServe_OllamaEmbed_RejectsInvalidEndpointProtocol(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["badollama"] = ProviderConfig{
		Endpoint: "http://localhost:11434",
		Protocols: []string{"ollama.embed"},
		Endpoints: []EndpointConfig{
			{URL: "http://other:11434", Protocols: []string{"typo"}},
		},
	}
	cfg.ModelGroups = append(cfg.ModelGroups, ModelGroupConfig{
		Name:   "ollama-embed",
		Models: ModelEntries{{Model: "badollama/model", Weight: 1}},
	})

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for invalid endpoint protocol alongside ollama.embed")
	}
	if !strings.Contains(err.Error(), "providers.badollama.endpoints[0].protocol") {
		t.Fatalf("expected endpoint protocol error, got: %v", err)
	}
}

func TestValidateForServe_OllamaEmbed_RejectsMissingSelectedEmbeddingEndpoint(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["badollama"] = ProviderConfig{
		Protocols: []string{"ollama.embed"},
		Endpoints: []EndpointConfig{
			{URL: "http://other:11434"},
		},
	}
	cfg.ModelGroups = append(cfg.ModelGroups, ModelGroupConfig{
		Name:   "ollama-embed",
		Models: ModelEntries{{Model: "badollama/model", Weight: 1}},
	})

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error when embedding endpoint cannot be selected")
	}
	if !strings.Contains(err.Error(), "providers.badollama.endpoint") {
		t.Fatalf("expected endpoint error, got: %v", err)
	}
}

func TestValidateForServe_OllamaEmbed_RejectsMissingEndpoint(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["badollama"] = ProviderConfig{
		APIKey:   "",
		Protocols: []string{"ollama.embed"},
	}
	cfg.ModelGroups = append(cfg.ModelGroups, ModelGroupConfig{
		Name:   "ollama-embed",
		Models: ModelEntries{{Model: "badollama/model", Weight: 1}},
	})

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for ollama.embed provider without valid endpoint")
	}
	if !strings.Contains(err.Error(), "providers.badollama.endpoint") {
		t.Fatalf("expected error for endpoint, got: %v", err)
	}
}

func TestValidateForServe_OllamaEmbed_AllowsEmptyAPIKey(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["ollama"] = ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "",
		Protocols: []string{"ollama.embed"},
	}
	cfg.ModelGroups = append(cfg.ModelGroups, ModelGroupConfig{
		Name:   "ollama-embed",
		Models: ModelEntries{{Model: "ollama/all-minilm", Weight: 1}},
	})

	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("expected no error for ollama.embed with empty api_key, got: %v", err)
	}
}

func TestValidateForServe_OllamaChat_AllowsEmptyAPIKey(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["ollama-chat"] = ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "",
		Protocols: []string{"ollama.chat"},
	}
	cfg.ModelGroups = append(cfg.ModelGroups, ModelGroupConfig{
		Name:   "ollama-chat",
		Models: ModelEntries{{Model: "ollama-chat/llama3", Weight: 1}},
	})

	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("expected no error for ollama.chat with empty api_key, got: %v", err)
	}
}

func TestValidateForServe_OllamaChat_ValidEndpointProtocol(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["ollama-chat"] = ProviderConfig{
		APIKey: "",
		Endpoints: []EndpointConfig{
			{URL: "http://localhost:11434", Protocols: []string{"ollama.chat"}},
		},
	}
	cfg.ModelGroups = append(cfg.ModelGroups, ModelGroupConfig{
		Name:   "ollama-chat-ep",
		Models: ModelEntries{{Model: "ollama-chat/llama3", Weight: 1}},
	})

	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("expected no error for endpoint-level ollama.chat with empty api_key, got: %v", err)
	}
}

func TestValidateForServe_OllamaChat_RejectsMixedWithOllamaEmbed(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["ollama-mixed"] = ProviderConfig{
		Endpoint: "http://localhost:11434",
		Protocols: []string{"ollama.chat"},
		Endpoints: []EndpointConfig{
			{URL: "http://localhost:11434/embed", Protocols: []string{"ollama.embed"}},
		},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for provider mixing ollama.chat and ollama.embed")
	}
	if !strings.Contains(err.Error(), "providers.ollama-mixed") {
		t.Fatalf("expected error mentioning providers.ollama-mixed, got: %v", err)
	}
}

func TestValidateForServe_OllamaChat_RejectsMixedWithNonOllamaProtocol(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["ollama-mixed"] = ProviderConfig{
		Endpoint: "http://localhost:11434",
		Protocols: []string{"ollama.chat"},
		Endpoints: []EndpointConfig{
			{URL: "https://api.openai.com/v1", Protocols: []string{"openai.chat"}},
		},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for provider mixing ollama.chat with non-ollama protocol")
	}
	if !strings.Contains(err.Error(), "providers.ollama-mixed") {
		t.Fatalf("expected error mentioning providers.ollama-mixed, got: %v", err)
	}
}

func TestValidateForServe_OllamaChat_RejectsMixedTopLevelProtocol(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["ollama-mixed"] = ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "sk-ollama",
		Protocols: []string{"openai.chat", "ollama.chat"},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for top-level provider protocol mixing openai and ollama.chat")
	}
	if !strings.Contains(err.Error(), "providers.ollama-mixed.protocol") {
		t.Fatalf("expected protocol error, got: %v", err)
	}
}

func TestValidateForServe_OllamaChat_RejectsEndpointLevelMixedProtocol(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["ollama-mixed"] = ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "",
		Endpoints: []EndpointConfig{
			{URL: "http://localhost:11434", Protocols: []string{"ollama.chat/openai"}},
		},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for endpoint-level protocol mixing ollama.chat and openai")
	}
	if !strings.Contains(err.Error(), "providers.ollama-mixed.endpoints[0].protocol") {
		t.Fatalf("expected endpoint protocol error, got: %v", err)
	}
}

func TestValidateForServe_OllamaChat_RejectsMixedNonOllamaEmptyKey(t *testing.T) {
	cfg := validConfigFixture()
	// provider with both ollama.chat and non-ollama protocol, empty api_key
	// ollama.chat empty-key exception must NOT apply when protocols are mixed
	cfg.Providers.Items["ollama-mixed"] = ProviderConfig{
		Endpoint: "http://localhost:11434",
		APIKey:   "",
		Endpoints: []EndpointConfig{
			{URL: "http://localhost:11434", Protocols: []string{"ollama.chat"}},
			{URL: "https://api.openai.com/v1", Protocols: []string{"openai.chat"}},
		},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for provider mixing ollama.chat with non-ollama protocol and empty api_key")
	}
}

func TestValidateForServe_NonEmbeddingProviderRequiresAPIKey(t *testing.T) {
	cfg := validConfigFixture()
	openaiProvider := cfg.Providers.Items["openai"]
	openaiProvider.APIKey = ""
	cfg.Providers.Items["openai"] = openaiProvider

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for non-embedding provider with empty api_key")
	}
	if !strings.Contains(err.Error(), "providers.openai.api_key") {
		t.Fatalf("expected error for openai.api_key, got: %v", err)
	}
}


func TestValidateForServe_ContextLength(t *testing.T) {
	tests := []struct {
		name        string
		cl          *int
		expectErr   bool
		errContains string
	}{
		{
			name: "context_length absent is valid",
			cl:   nil,
		},
		{
			name: "context_length positive is valid",
			cl:   func() *int { v := 128000; return &v }(),
		},
		{
			name:        "context_length zero is invalid",
			cl:          func() *int { v := 0; return &v }(),
			expectErr:   true,
			errContains: "context_length",
		},
		{
			name:        "context_length negative is invalid",
			cl:          func() *int { v := -1; return &v }(),
			expectErr:   true,
			errContains: "context_length",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfigFixture()
			cfg.ModelGroups[0].ModelMetadata.ContextLength = tt.cl

			_, err := ValidateForServe(cfg)
			if tt.expectErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errContains)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestValidateForServe_DisabledTimeRanges(t *testing.T) {
	cfg := validConfigFixture()
	prov := cfg.Providers.Items["openai"]
	prov.DisabledTimeRanges = []string{"09:00-12:00", "14:00-18:00"}
	cfg.Providers.Items["openai"] = prov

	warnings, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("unexpected error for valid disabled_time_ranges: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
}

func TestValidateForServe_DisabledTimeRanges_CrossMidnight(t *testing.T) {
	cfg := validConfigFixture()
	prov := cfg.Providers.Items["openai"]
	prov.DisabledTimeRanges = []string{"23:00-02:00"}
	cfg.Providers.Items["openai"] = prov

	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("unexpected error for cross-midnight range: %v", err)
	}
}

func TestValidateForServe_DisabledTimeRanges_InvalidFormat(t *testing.T) {
	tests := []struct {
		name     string
		ranges   []string
		contains string
	}{
		{
			name:     "missing dash",
			ranges:   []string{"09:00"},
			contains: "invalid format",
		},
		{
			name:     "equal start end",
			ranges:   []string{"12:00-12:00"},
			contains: "must not be equal",
		},
		{
			name:     "invalid hour",
			ranges:   []string{"25:00-12:00"},
			contains: "hour must be 0-24",
		},
		{
			name:     "invalid minute",
			ranges:   []string{"09:60-12:00"},
			contains: "minute must be 0-59",
		},
		{
			name:     "24:01 invalid",
			ranges:   []string{"24:01-00:00"},
			contains: "invalid time",
		},
		{
			name:     "invalid second entry",
			ranges:   []string{"09:00-12:00", "invalid"},
			contains: "invalid format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfigFixture()
			prov := cfg.Providers.Items["openai"]
			prov.DisabledTimeRanges = tt.ranges
			cfg.Providers.Items["openai"] = prov

			_, err := ValidateForServe(cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("error %q should contain %q", err.Error(), tt.contains)
			}
		})
	}
}

// ========== Smart Route 测试 ==========

func TestValidateForServe_SmartRoute_NotConfigured(t *testing.T) {
	cfg := validConfigFixture()
	// smart_route 未配置，校验应通过
	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("expected no error when smart_route not configured, got: %v", err)
	}
}

func TestValidateForServe_SmartRoute_EmptyFields(t *testing.T) {
	cfg := validConfigFixture()
	cfg.SmartRoute = &SmartRouteConfig{
		Cheap: "", // empty
		Scout: "scout-group",
	}
	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error when smart_route.cheap is empty")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Errorf("expected error mentioning empty cheap, got: %v", err)
	}
}

func TestValidateForServe_SmartRoute_ContainsSlash(t *testing.T) {
	cfg := validConfigFixture()
	cfg.SmartRoute = &SmartRouteConfig{
		Cheap: "openai/gpt-4o-mini", // contains /
		Scout: "scout-group",
	}
	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error when smart_route.cheap contains '/'")
	}
	if !strings.Contains(err.Error(), "must not contain '/'") {
		t.Errorf("expected error mentioning slash restriction, got: %v", err)
	}
}

func TestValidateForServe_SmartRoute_EnabledModels_ContainsSlash(t *testing.T) {
	cfg := validConfigFixture()
	cfg.SmartRoute = &SmartRouteConfig{
		Cheap:         "cheap-group",
		Scout:         "scout-group",
		EnabledModels: []string{"openai/gpt-4o"}, // contains /
	}
	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error when enabled_models contains '/'")
	}
	if !strings.Contains(err.Error(), "must not contain '/'") {
		t.Errorf("expected error mentioning slash restriction, got: %v", err)
	}
}

// ========== Adaptive 模式测试 ==========

func TestValidateForServe_AdaptiveMode_ValidLeafOnly(t *testing.T) {
	cfg := validConfigFixture()
	cfg.ModelGroups[0] = ModelGroupConfig{
		Name: "adaptive-test",
		Mode: "adaptive",
		Models: ModelEntries{
			{Model: "openai/gpt-4o", Weight: 1},
			{Model: "openai/gpt-4", Weight: 1},
		},
	}

	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("expected no error for valid adaptive group, got: %v", err)
	}
}

func TestValidateForServe_AdaptiveMode_RejectsInternalRef(t *testing.T) {
	cfg := validConfigFixture()
	cfg.ModelGroups[0] = ModelGroupConfig{
		Name: "adaptive-bad",
		Mode: "adaptive",
		Models: ModelEntries{
			{Model: "openai/gpt-4o", Weight: 1},
			{Model: "internal-group", Weight: 1}, // 内部引用，不含 /
		},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for adaptive group containing internal reference")
	}
	if !strings.Contains(err.Error(), "adaptive") {
		t.Errorf("expected error mentioning adaptive, got: %v", err)
	}
}

func TestValidateForServe_AdaptiveMode_RejectsOnlyInternalRef(t *testing.T) {
	cfg := validConfigFixture()
	// 全部是内部引用
	cfg.ModelGroups[0] = ModelGroupConfig{
		Name: "adaptive-pure-ref",
		Mode: "adaptive",
		Models: ModelEntries{
			{Model: "child-group-1", Weight: 1},
			{Model: "child-group-2", Weight: 1},
		},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for adaptive group with only internal references")
	}
}

func TestValidateForServe_AdaptiveMode_RejectsRedirectRef(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Redirect = RedirectConfigs{{Source: "my-alias", Target: "gpt-4o"}}
	cfg.ModelGroups[0] = ModelGroupConfig{
		Name: "adaptive-redirect",
		Mode: "adaptive",
		Models: ModelEntries{
			{Model: "openai/gpt-4o", Weight: 1},
			{Model: "my-alias", Weight: 1}, // redirect 别名，不含 /
		},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for adaptive group containing redirect reference")
	}
}

func TestValidateForServe_AdaptiveMode_AcceptedOnOtherModes(t *testing.T) {
	// adaptive 是合法 mode 枚举值
	cfg := validConfigFixture()
	cfg.ModelGroups[0] = ModelGroupConfig{
		Name: "ok-adaptive",
		Mode: "adaptive",
		Models: ModelEntries{
			{Model: "openai/gpt-4o", Weight: 1},
		},
	}

	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("expected adaptive to be accepted as valid mode, got: %v", err)
	}
}

func TestValidateForServe_SmartRoute_ValidConfig(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Redirect = RedirectConfigs{{Source: "alias-a", Target: "gpt-4o"}}
	cfg.SmartRoute = &SmartRouteConfig{
		Cheap:         "cheap-group",
		Scout:         "scout-group",
		EnabledModels: []string{"alias-a", "gpt-4o"},
	}
	// Add missing groups
	cfg.ModelGroups = append(cfg.ModelGroups,
		ModelGroupConfig{Name: "cheap-group", Model: "openai/gpt-4o-mini"},
		ModelGroupConfig{Name: "scout-group", Model: "openai/gpt-4o"},
	)
	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("expected no error for valid smart_route config, got: %v", err)
	}
}
