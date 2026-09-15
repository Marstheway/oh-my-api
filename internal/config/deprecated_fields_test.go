package config

import (
	"strings"
	"testing"
)

func TestRejectDeprecatedConfigYAML(t *testing.T) {
	base := `
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

	tests := []struct {
		name    string
		inject  string
		wantKey string
	}{
		{name: "default_protocols", inject: "    default_protocols: [\"openai.chat\"]", wantKey: "default_protocols"},
		{name: "allowed_protocols", inject: "    upstream_model:\n      - model: \"gpt-4o\"\n        allowed_protocols: [\"openai.chat\"]", wantKey: "allowed_protocols"},
		{name: "reasoning_effort", inject: "    upstream_model:\n      - model: \"gpt-4o\"\n        reasoning_effort: [\"high\"]", wantKey: "reasoning_effort"},
		{name: "reasoning_effort_mode", inject: "    upstream_model:\n      - model: \"gpt-4o\"\n        reasoning_effort_mode: strip", wantKey: "reasoning_effort_mode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := strings.Replace(base, `    protocols: ["openai.chat"]`, "    protocols: [\"openai.chat\"]\n"+tt.inject, 1)
			err := RejectDeprecatedConfigYAML([]byte(yaml))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantKey) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantKey)
			}
			if !strings.Contains(err.Error(), "migrate-rules") {
				t.Fatalf("error = %q, want migrate-rules hint", err.Error())
			}
		})
	}
}

func TestLoad_RejectsDeprecatedProviderFields(t *testing.T) {
	_, err := loadTestConfigErr(t, `
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
    default_protocols: ["openai.chat"]
model_groups:
  - name: "gpt-4"
    models:
      - "openai/gpt-4"
`)
	if err == nil {
		t.Fatal("expected load error for deprecated field")
	}
	if !strings.Contains(err.Error(), "default_protocols") {
		t.Fatalf("error = %q, want default_protocols rejection", err.Error())
	}
	if !strings.Contains(err.Error(), "migrate-rules") {
		t.Fatalf("error = %q, want migrate-rules hint", err.Error())
	}
}

// TestRejectDeprecatedProviderKeys 验证 providers.*.disabled_time_ranges 与 providers.*.upstream_model
// 按路径硬拒绝，错误点名路径且不得提示 migrate-rules（这两项只能手工改成 top-level rules）。
func TestRejectDeprecatedProviderKeys(t *testing.T) {
	base := `
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

	tests := []struct {
		name     string
		inject   string
		wantPath string
	}{
		{name: "disabled_time_ranges", inject: "    disabled_time_ranges: [\"09:00-12:00\"]", wantPath: "providers.openai.disabled_time_ranges"},
		{name: "upstream_model", inject: "    upstream_model:\n      - model: \"gpt-4o\"\n        qpm: 10", wantPath: "providers.openai.upstream_model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := strings.Replace(base, `    protocols: ["openai.chat"]`, "    protocols: [\"openai.chat\"]\n"+tt.inject, 1)
			err := RejectDeprecatedConfigYAML([]byte(yaml))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantPath) {
				t.Fatalf("error = %q, want path %q", err.Error(), tt.wantPath)
			}
			if strings.Contains(err.Error(), "migrate-rules") {
				t.Fatalf("error = %q, must not suggest migrate-rules for provider-level keys", err.Error())
			}
		})
	}
}

// TestLoad_RejectsProviderUpstreamModel 验证 Load 硬拒绝 providers.*.upstream_model，
// 错误点名路径且不提示 migrate-rules。
func TestLoad_RejectsProviderUpstreamModel(t *testing.T) {
	_, err := loadTestConfigErr(t, `
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
    upstream_model:
      - model: "gpt-4o"
        qpm: 10
model_groups:
  - name: "gpt-4"
    models:
      - "openai/gpt-4"
`)
	if err == nil {
		t.Fatal("expected load error for providers.openai.upstream_model")
	}
	if !strings.Contains(err.Error(), "providers.openai.upstream_model") {
		t.Fatalf("error = %q, want providers.openai.upstream_model path", err.Error())
	}
	if strings.Contains(err.Error(), "migrate-rules") {
		t.Fatalf("error = %q, must not suggest migrate-rules for upstream_model", err.Error())
	}
}

// TestRejectDeprecatedCascadeKeys 验证历史 YAML 中的 cascade.offer 按路径级硬拒绝，
// 不得静默忽略或作为兼容字段保留。
func TestRejectDeprecatedCascadeKeys(t *testing.T) {
	base := `
server:
  listen: ":18000"
inbound:
  auth:
    keys:
      - name: "test"
        key: "sk-test"
cascade:
  hub: "https://api.example.com"
  token: "spoke-secret"
  peer: "openai"
`
	tests := []struct {
		name    string
		inject  string
		wantKey string
	}{
		{name: "offer list", inject: "  offer:\n    - \"gpt-4o\"", wantKey: "cascade.offer"},
		{name: "empty offer", inject: "  offer: []", wantKey: "cascade.offer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := base + tt.inject + "\n"
			err := RejectDeprecatedConfigYAML([]byte(yaml))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantKey) {
				t.Fatalf("error = %q, want path %q", err.Error(), tt.wantKey)
			}
			if strings.Contains(err.Error(), "migrate-rules") {
				t.Fatalf("error = %q, must not suggest migrate-rules for cascade.offer", err.Error())
			}
		})
	}
}

// TestLoad_RejectsCascadeOffer 验证 Load 启动加载时硬拒绝历史 cascade.offer。
func TestLoad_RejectsCascadeOffer(t *testing.T) {
	_, err := loadTestConfigErr(t, `
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
`)
	if err == nil {
		t.Fatal("expected load error for cascade.offer")
	}
	if !strings.Contains(err.Error(), "cascade.offer") {
		t.Fatalf("error = %q, want cascade.offer path", err.Error())
	}
}

// TestLoad_AcceptsSpokeCascadeWithoutOffer 验证不含 offer 的 spoke cascade 配置正常加载。
func TestLoad_AcceptsSpokeCascadeWithoutOffer(t *testing.T) {
	cfg, err := loadTestConfigErr(t, `
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
`)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if cfg.Cascade == nil {
		t.Fatal("expected cascade config to load")
	}
}
