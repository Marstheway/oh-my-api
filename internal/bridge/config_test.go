package bridge

import (
	"testing"
)

func TestLoadConfig(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		cfg, err := LoadConfig([]string{
			"-bridge-token", "test-token",
			"-listen", ":9999",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.BridgeToken != "test-token" {
			t.Errorf("expected bridge token 'test-token', got %q", cfg.BridgeToken)
		}
		if cfg.Listen != ":9999" {
			t.Errorf("expected listen ':9999', got %q", cfg.Listen)
		}
	})

	t.Run("missing bridge token", func(t *testing.T) {
		_, err := LoadConfig([]string{
			"-listen", ":9999",
		})
		if err == nil {
			t.Fatal("expected error for missing bridge token")
		}
	})

	t.Run("default values", func(t *testing.T) {
		cfg, err := LoadConfig([]string{
			"-bridge-token", "test-token",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Listen != ":8081" {
			t.Errorf("expected default listen ':8081', got %q", cfg.Listen)
		}
	})

	t.Run("rejects deprecated -auth-json flag", func(t *testing.T) {
		_, err := LoadConfig([]string{
			"-bridge-token", "test-token",
			"-auth-json", "/tmp/test_auth.json",
		})
		if err == nil {
			t.Fatal("expected error: -auth-json must be rejected")
		}
		if !containsAll(err.Error(), "auth-json") {
			t.Errorf("error should mention -auth-json rejection, got %q", err.Error())
		}
	})
}

// containsAll 判定 s 是否包含子串 sub。
func containsAll(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestValidateProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		wantErr  bool
	}{
		{"valid xai-oauth", "xai-oauth", false},
		{"empty provider", "", true},
		{"unsupported provider", "openai-codex", true},
		{"whitespace only", "   ", true},
		{"future provider type", "chatgpt-subscribe", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProvider(tt.provider)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateProvider(%q) error = %v, wantErr = %v", tt.provider, err, tt.wantErr)
			}
		})
	}
}

func TestSupportedProviderTypes(t *testing.T) {
	types := SupportedProviderTypes()
	if len(types) != 1 {
		t.Errorf("expected 1 supported provider type, got %d: %v", len(types), types)
	}
	if types[0] != "xai-oauth" {
		t.Errorf("expected 'xai-oauth', got %q", types[0])
	}
}
