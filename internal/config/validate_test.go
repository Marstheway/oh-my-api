package config

import (
	"strings"
	"testing"
)

// validConfigFixture 返回一个最小合法配置，所有基本校验都能通过。
func validConfigFixture() *Config {
	return &Config{
		Providers: ProvidersConfig{
			Items: map[string]ProviderConfig{
				"openai": {
					Endpoint:  "https://api.openai.com/v1",
					APIKey:    "sk-xxx",
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

// ========== 基本校验测试 ==========

func TestValidateForServe_RequiresAtLeastOneProvider(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items = map[string]ProviderConfig{}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error when no providers configured")
	}
	if !strings.Contains(err.Error(), "providers") {
		t.Fatalf("expected error mentioning providers, got: %v", err)
	}
}

func TestValidateForServe_RequiresProviderEndpoint(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["noep"] = ProviderConfig{
		APIKey:    "sk-xxx",
		Protocols: []string{"openai.chat"},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error when provider has no endpoint")
	}
	if !strings.Contains(err.Error(), "providers.noep.endpoint") {
		t.Fatalf("expected error for providers.noep.endpoint, got: %v", err)
	}
}

func TestValidateForServe_EndpointViaEndpointsList(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Providers.Items["openai"] = ProviderConfig{
		APIKey: "sk-xxx",
		Endpoints: []EndpointConfig{
			{URL: "https://api.openai.com/v1", Protocols: []string{"openai.chat"}},
		},
	}

	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("expected no error when endpoint is provided via endpoints list, got: %v", err)
	}
}

func TestValidateForServe_RequiresAtLeastOneModelGroup(t *testing.T) {
	cfg := validConfigFixture()
	cfg.ModelGroups = nil

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error when no model groups configured")
	}
	if !strings.Contains(err.Error(), "model_groups") {
		t.Fatalf("expected error mentioning model_groups, got: %v", err)
	}
}

func TestValidateForServe_RejectsMissingProviderRef(t *testing.T) {
	cfg := validConfigFixture()
	cfg.ModelGroups[0].Models = ModelEntries{{Model: "missing/gpt-4o", Weight: 1}}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error when model group references missing provider")
	}
	if !strings.Contains(err.Error(), "provider \"missing\" not found") {
		t.Fatalf("expected error about missing provider, got: %v", err)
	}
}

func TestValidateForServe_AcceptsInternalRef(t *testing.T) {
	// 内部引用（不含 /）不参与 provider 存在性检查，交给 resolver 处理
	cfg := validConfigFixture()
	cfg.ModelGroups[0].Models = ModelEntries{{Model: "internal-group", Weight: 1}}

	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("expected no error for internal reference, got: %v", err)
	}
}

func TestValidateForServe_AdaptiveMode_RejectsInternalRef(t *testing.T) {
	cfg := validConfigFixture()
	cfg.ModelGroups[0] = ModelGroupConfig{
		Name: "adaptive-bad",
		Mode: "adaptive",
		Models: ModelEntries{
			{Model: "openai/gpt-4o", Weight: 1},
			{Model: "internal-group", Weight: 1}, // 不含 /，adaptive 不允许
		},
	}

	_, err := ValidateForServe(cfg)
	if err == nil {
		t.Fatal("expected error for adaptive group containing internal reference")
	}
	if !strings.Contains(err.Error(), "adaptive") {
		t.Fatalf("expected error mentioning adaptive, got: %v", err)
	}
}

func TestValidateForServe_AdaptiveMode_AcceptsLeafOnly(t *testing.T) {
	cfg := validConfigFixture()
	cfg.ModelGroups[0] = ModelGroupConfig{
		Name: "adaptive-ok",
		Mode: "adaptive",
		Models: ModelEntries{
			{Model: "openai/gpt-4o", Weight: 1},
			{Model: "openai/gpt-4", Weight: 1},
		},
	}

	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("expected no error for valid adaptive leaf-only group, got: %v", err)
	}
}

// ========== 错误格式与边界测试 ==========

func TestValidationError_Error(t *testing.T) {
	err := ValidationError{Issues: []ValidationIssue{
		{Path: "providers.openai.endpoint", Message: "must have at least one valid endpoint"},
		{Path: "model_groups[0].models[0]", Message: "provider not found"},
	}}

	msg := err.Error()
	if !strings.Contains(msg, "config validation failed (2 issues)") {
		t.Fatalf("unexpected header: %s", msg)
	}
	if !strings.Contains(msg, "providers.openai.endpoint") {
		t.Fatalf("missing path in error: %s", msg)
	}
}

// TestValidateForServe_DoesNotValidateRedirect 验证 ValidateForServe 不校验 redirect 目标，
// redirect 校验由 model.NewResolver 负责。
func TestValidateForServe_DoesNotValidateRedirect(t *testing.T) {
	cfg := validConfigFixture()
	cfg.Redirect = RedirectConfigs{{Source: "alias", Target: "missing-target"}}

	_, err := ValidateForServe(cfg)
	if err != nil {
		t.Fatalf("unexpected error from ValidateForServe: %v", err)
	}
}
