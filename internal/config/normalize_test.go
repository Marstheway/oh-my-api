package config

import "testing"

func TestResolveProtocolAlias(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"openai", "openai.chat"},
		{"anthropic", "anthropic.messages"},
		{"openai.chat", "openai.chat"},
		{"anthropic.messages", "anthropic.messages"},
		{"openai.responses", "openai.responses"},
		{"ollama.chat", "ollama.chat"},
		{"ollama.embed", "ollama.embed"},
		{"unknown", "unknown"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ResolveProtocolAlias(tt.input)
			if got != tt.expected {
				t.Errorf("ResolveProtocolAlias(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestNormalizeProviderProtocolsInConfig(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			Items: map[string]ProviderConfig{
				"p1": {
					Protocols:        []string{"openai", "openai.chat"},
					DefaultProtocols: []string{"anthropic"},
					Endpoints: []EndpointConfig{
						{URL: "https://a.com", Protocols: []string{"openai"}},
						{URL: "https://b.com", Protocols: []string{"openai.chat"}},
					},
					UpstreamModels: []UpstreamModelConfig{
						{Model: "m1", AllowedProtocols: []string{"anthropic", "openai"}},
					},
				},
				"p2": {
					Protocols: []string{"openai.chat"},
				},
			},
		},
	}

	changed := NormalizeProviderProtocolsInConfig(cfg)
	if !changed {
		t.Error("expected change")
	}

	p1 := cfg.Providers.Items["p1"]

	if p1.Protocols[0] != "openai.chat" {
		t.Errorf("Protocols[0] = %q, want openai.chat", p1.Protocols[0])
	}
	if p1.DefaultProtocols[0] != "anthropic.messages" {
		t.Errorf("DefaultProtocols[0] = %q, want anthropic.messages", p1.DefaultProtocols[0])
	}
	if p1.Endpoints[0].Protocols[0] != "openai.chat" {
		t.Errorf("Endpoints[0].Protocols[0] = %q, want openai.chat", p1.Endpoints[0].Protocols[0])
	}
	if p1.UpstreamModels[0].AllowedProtocols[1] != "openai.chat" {
		t.Errorf("AllowedProtocols[1] = %q, want openai.chat", p1.UpstreamModels[0].AllowedProtocols[1])
	}

	// p2 已经是全名，不应再变化
	if p2 := cfg.Providers.Items["p2"]; p2.Protocols[0] != "openai.chat" {
		t.Errorf("p2 Protocols[0] = %q, want openai.chat", p2.Protocols[0])
	}
}

func TestNormalizeProviderProtocolsInConfig_NoChange(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			Items: map[string]ProviderConfig{
				"p1": {
					Protocols:        []string{"openai.chat"},
					DefaultProtocols: []string{"anthropic.messages"},
					Endpoints: []EndpointConfig{
						{URL: "https://a.com", Protocols: []string{"openai.chat"}},
					},
				},
			},
		},
	}

	if NormalizeProviderProtocolsInConfig(cfg) {
		t.Error("expected no change")
	}
}
