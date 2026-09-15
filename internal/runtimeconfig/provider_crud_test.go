package runtimeconfig

import (
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func validProviderCRUDConfig() *config.Config {
	return &config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{
				"openai": {
					Endpoint:  "https://api.openai.com",
					Protocols: []string{"openai.chat"},
				},
			},
		},
		ModelGroups: []config.ModelGroupConfig{
			{Name: "gpt-4o", Models: config.ModelEntries{{Model: "openai/gpt-4o"}}},
		},
	}
}

func TestProviderCRUDValidateCreate(t *testing.T) {
	tests := []struct {
		name    string
		draft   *config.Config
		input   *ProviderInput
		wantErr bool
		errCode ErrorCode
	}{
		{
			name: "valid provider",
			draft: &config.Config{
				Providers: config.ProvidersConfig{
					Items: map[string]config.ProviderConfig{
						"existing": {Endpoint: "http://existing"},
					},
				},
			},
			input: &ProviderInput{
				Name:     "new-provider",
				Endpoint: "http://new",
			},
			wantErr: false,
		},
		{
			name: "empty name",
			draft: &config.Config{
				Providers: config.ProvidersConfig{
					Items: map[string]config.ProviderConfig{},
				},
			},
			input: &ProviderInput{
				Endpoint: "http://new",
			},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
		{
			name: "duplicate name",
			draft: &config.Config{
				Providers: config.ProvidersConfig{
					Items: map[string]config.ProviderConfig{
						"existing": {Endpoint: "http://existing"},
					},
				},
			},
			input: &ProviderInput{
				Name:     "existing",
				Endpoint: "http://new",
			},
			wantErr: true,
			errCode: ErrCodeConflict,
		},
		{
			name: "empty endpoint and endpoints",
			draft: &config.Config{
				Providers: config.ProvidersConfig{
					Items: map[string]config.ProviderConfig{},
				},
			},
			input: &ProviderInput{
				Name: "new-provider",
			},
			wantErr: true,
			errCode: ErrCodeBadRequest,
		},
		{
			name: "valid endpoints",
			draft: &config.Config{
				Providers: config.ProvidersConfig{
					Items: map[string]config.ProviderConfig{},
				},
			},
			input: &ProviderInput{
				Name: "new-provider",
				Endpoints: []EndpointInput{
					{URL: "http://new", Protocols: []string{"openai.chat"}},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crud := NewProviderCRUD(tt.draft)
			err := crud.ValidateCreate(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
					return
				}
				if err.Code != tt.errCode {
					t.Errorf("expected error code %s, got %s", tt.errCode, err.Code)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestProviderCRUDValidateUpdate(t *testing.T) {
	tests := []struct {
		name    string
		draft   *config.Config
		oldName string
		input   *ProviderInput
		wantErr bool
		errCode ErrorCode
	}{
		{
			name: "valid update",
			draft: &config.Config{
				Providers: config.ProvidersConfig{
					Items: map[string]config.ProviderConfig{
						"existing": {Endpoint: "http://existing"},
					},
				},
			},
			oldName: "existing",
			input: &ProviderInput{
				Name:     "existing",
				Endpoint: "http://updated",
			},
			wantErr: false,
		},
		{
			name: "rename provider",
			draft: &config.Config{
				Providers: config.ProvidersConfig{
					Items: map[string]config.ProviderConfig{
						"existing": {Endpoint: "http://existing"},
					},
				},
			},
			oldName: "existing",
			input: &ProviderInput{
				Name:     "renamed",
				Endpoint: "http://updated",
			},
			wantErr: false,
		},
		{
			name: "provider not found",
			draft: &config.Config{
				Providers: config.ProvidersConfig{
					Items: map[string]config.ProviderConfig{},
				},
			},
			oldName: "nonexistent",
			input: &ProviderInput{
				Name:     "test",
				Endpoint: "http://test",
			},
			wantErr: true,
			errCode: ErrCodeNotFound,
		},
		{
			name: "rename to existing name",
			draft: &config.Config{
				Providers: config.ProvidersConfig{
					Items: map[string]config.ProviderConfig{
						"existing": {Endpoint: "http://existing"},
						"other":    {Endpoint: "http://other"},
					},
				},
			},
			oldName: "existing",
			input: &ProviderInput{
				Name:     "other",
				Endpoint: "http://updated",
			},
			wantErr: true,
			errCode: ErrCodeConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crud := NewProviderCRUD(tt.draft)
			err := crud.ValidateUpdate(tt.oldName, tt.input)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
					return
				}
				if err.Code != tt.errCode {
					t.Errorf("expected error code %s, got %s", tt.errCode, err.Code)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestProviderCRUDValidateDelete(t *testing.T) {
	tests := []struct {
		name    string
		draft   *config.Config
		nameArg string
		wantErr bool
		errCode ErrorCode
	}{
		{
			name: "valid delete",
			draft: &config.Config{
				Providers: config.ProvidersConfig{
					Items: map[string]config.ProviderConfig{
						"existing": {Endpoint: "http://existing"},
					},
				},
				ModelGroups: []config.ModelGroupConfig{},
			},
			nameArg: "existing",
			wantErr: false,
		},
		{
			name: "provider not found",
			draft: &config.Config{
				Providers: config.ProvidersConfig{
					Items: map[string]config.ProviderConfig{},
				},
				ModelGroups: []config.ModelGroupConfig{},
			},
			nameArg: "nonexistent",
			wantErr: true,
			errCode: ErrCodeNotFound,
		},
		{
			name: "referenced by model_group models",
			draft: &config.Config{
				Providers: config.ProvidersConfig{
					Items: map[string]config.ProviderConfig{
						"existing": {Endpoint: "http://existing"},
					},
				},
				ModelGroups: []config.ModelGroupConfig{
					{
						Name:   "test-group",
						Models: config.ModelEntries{{Model: "existing/gpt-4"}},
					},
				},
			},
			nameArg: "existing",
			wantErr: true,
			errCode: ErrCodeConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crud := NewProviderCRUD(tt.draft)
			err := crud.ValidateDelete(tt.nameArg)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
					return
				}
				if err.Code != tt.errCode {
					t.Errorf("expected error code %s, got %s", tt.errCode, err.Code)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestProviderCRUDPropagateRename(t *testing.T) {
	draft := &config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{
				"openai": {Endpoint: "http://openai"},
			},
		},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name:   "gpt-group",
				Models: config.ModelEntries{{Model: "openai/gpt-4"}},
			},
		},
	}

	crud := NewProviderCRUD(draft)
	crud.propagateRename("openai", "openai-renamed")

	// 检查 models[] 是否更新
	if draft.ModelGroups[0].Models[0].Model != "openai-renamed/gpt-4" {
		t.Errorf("expected model 'openai-renamed/gpt-4', got '%s'", draft.ModelGroups[0].Models[0].Model)
	}
}

// ========== Remote Bridge CRUD 校验测试 ==========

func TestProviderCRUDValidateCreate_RemoteBridge_Valid(t *testing.T) {
	crud := NewProviderCRUD(&config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{},
		},
	})
	err := crud.ValidateCreate(&ProviderInput{
		Name:     "xai-bridge",
		Endpoint: "http://bridge.local:8080",
		RemoteBridge: &RemoteBridgeInput{
			Enabled:  true,
			Provider: "xai-oauth",
			Token:    "secret-token",
		},
	})
	if err != nil {
		t.Fatalf("expected no error for valid remote bridge provider, got: %v", err)
	}
}

func TestProviderCRUDValidateCreate_RemoteBridge_EmptyToken(t *testing.T) {
	crud := NewProviderCRUD(&config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{},
		},
	})
	err := crud.ValidateCreate(&ProviderInput{
		Name:     "xai-bridge",
		Endpoint: "http://bridge.local:8080",
		RemoteBridge: &RemoteBridgeInput{
			Enabled:  true,
			Provider: "xai-oauth",
			Token:    "",
		},
	})
	if err == nil {
		t.Fatal("expected error for empty bridge token")
	}
	if err.Code != ErrCodeBadRequest {
		t.Errorf("expected ErrCodeBadRequest, got %s", err.Code)
	}
	if err.Field != "remote_bridge.token" {
		t.Errorf("expected field 'remote_bridge.token', got %q", err.Field)
	}
}

func TestProviderCRUDValidateCreate_RemoteBridge_EmptyProvider(t *testing.T) {
	crud := NewProviderCRUD(&config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{},
		},
	})
	err := crud.ValidateCreate(&ProviderInput{
		Name:     "xai-bridge",
		Endpoint: "http://bridge.local:8080",
		RemoteBridge: &RemoteBridgeInput{
			Enabled:  true,
			Provider: "",
			Token:    "secret-token",
		},
	})
	if err == nil {
		t.Fatal("expected error for empty bridge provider")
	}
	if err.Code != ErrCodeBadRequest {
		t.Errorf("expected ErrCodeBadRequest, got %s", err.Code)
	}
	if err.Field != "remote_bridge.provider" {
		t.Errorf("expected field 'remote_bridge.provider', got %q", err.Field)
	}
}

func TestProviderCRUDValidateCreate_RemoteBridge_UnsupportedProvider(t *testing.T) {
	crud := NewProviderCRUD(&config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{},
		},
	})
	err := crud.ValidateCreate(&ProviderInput{
		Name:     "xai-bridge",
		Endpoint: "http://bridge.local:8080",
		RemoteBridge: &RemoteBridgeInput{
			Enabled:  true,
			Provider: "unknown-oauth",
			Token:    "secret-token",
		},
	})
	if err == nil {
		t.Fatal("expected error for unsupported bridge provider type")
	}
	if err.Code != ErrCodeBadRequest {
		t.Errorf("expected ErrCodeBadRequest, got %s", err.Code)
	}
}

func TestProviderCRUDValidateCreate_RemoteBridge_DisabledSkipsValidation(t *testing.T) {
	crud := NewProviderCRUD(&config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{},
		},
	})
	err := crud.ValidateCreate(&ProviderInput{
		Name:     "normal-provider",
		Endpoint: "https://api.example.com",
		RemoteBridge: &RemoteBridgeInput{
			Enabled:  false,
			Provider: "",
			Token:    "",
		},
	})
	if err != nil {
		t.Fatalf("expected no error when remote_bridge is disabled, got: %v", err)
	}
}

func TestProviderCRUDValidateCreate_RemoteBridge_NilBridgeSkipsValidation(t *testing.T) {
	crud := NewProviderCRUD(&config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{},
		},
	})
	err := crud.ValidateCreate(&ProviderInput{
		Name:     "normal-provider",
		Endpoint: "https://api.example.com",
	})
	if err != nil {
		t.Fatalf("expected no error when remote_bridge is nil, got: %v", err)
	}
}

func TestProviderCRUDValidateCreate_LocalOAuthBridgeRejectsToken(t *testing.T) {
	crud := NewProviderCRUD(&config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{},
		},
	})
	err := crud.ValidateCreate(&ProviderInput{
		Name:     "xai-local-bridge",
		Endpoint: "http://127.0.0.1:8081",
		RemoteBridge: &RemoteBridgeInput{
			Enabled:  true,
			Provider: "xai-oauth",
			Token:    "must-not-be-sent",
			Local:    true,
		},
	})
	if err == nil || err.Field != "remote_bridge.token" {
		t.Fatalf("expected local bridge token error, got %v", err)
	}
}

func TestProviderCRUD_LocalOAuthBridgeRoundTrip(t *testing.T) {
	crud := NewProviderCRUD(&config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{},
		},
	})
	input := &ProviderInput{
		Name:     "xai-local-bridge",
		Endpoint: "http://127.0.0.1:8081",
		RemoteBridge: &RemoteBridgeInput{
			Enabled:  true,
			Provider: "xai-oauth",
			Local:    true,
		},
	}
	if err := crud.Create(input); err != nil {
		t.Fatalf("create local OAuth bridge: %v", err)
	}

	stored := crud.draft.Providers.Items[input.Name]
	if stored.RemoteBridge == nil || !stored.RemoteBridge.Local {
		t.Fatalf("stored local bridge = %+v, want Local=true", stored.RemoteBridge)
	}

	output, ok := crud.Get(input.Name)
	if !ok || output.RemoteBridge == nil || !output.RemoteBridge.Local {
		t.Fatalf("output local bridge = %+v, want Local=true", output.RemoteBridge)
	}
}

func TestProviderCRUDValidateCreate_CascadeEnabled_AllowsEmptyEndpoint(t *testing.T) {
	cfg := validProviderCRUDConfig()
	crud := NewProviderCRUD(cfg)

	input := &ProviderInput{
		Name: "corp-dev",
		Protocols: []string{
			"openai.chat",
			"openai.responses",
			"anthropic.messages",
		},
		Cascade: &CascadeInput{
			Enabled: true,
			Token:   "hub-secret",
		},
	}

	if err := crud.ValidateCreate(input); err != nil {
		t.Fatalf("expected cascade.enabled without endpoint to be valid, got: %v", err)
	}
}

func TestProviderCRUDValidateCreate_CascadeEnabled_EmptyToken(t *testing.T) {
	cfg := validProviderCRUDConfig()
	crud := NewProviderCRUD(cfg)

	input := &ProviderInput{
		Name:      "corp-dev",
		Endpoint:  "https://example.com",
		Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
		Cascade: &CascadeInput{
			Enabled: true,
			Token:   "",
		},
	}

	err := crud.ValidateCreate(input)
	if err == nil {
		t.Fatal("expected error when cascade.enabled without token")
	}
	if err.Field != "cascade.token" {
		t.Errorf("expected field 'cascade.token', got %q", err.Field)
	}
}

func TestProviderCRUDUpdate_PreservesCascadeWhenOmitted(t *testing.T) {
	cfg := validProviderCRUDConfig()
	cfg.Providers.Items["corp-dev"] = config.ProviderConfig{
		Endpoint:  "https://example.com",
		Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
		Cascade: &config.ProviderCascadeConfig{
			Enabled: true,
			Token:   "keep-me",
		},
	}
	crud := NewProviderCRUD(cfg)

	input := &ProviderInput{
		Name:      "corp-dev",
		Endpoint:  "https://example.com",
		Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
		RateLimit: RateLimitInput{QPM: 100},
		// Cascade intentionally omitted
	}

	if err := crud.Update("corp-dev", input); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	stored := crud.draft.Providers.Items["corp-dev"]
	if stored.Cascade == nil {
		t.Fatal("expected cascade block to be preserved")
	}
	if stored.Cascade.Token != "keep-me" {
		t.Errorf("expected token 'keep-me', got %q", stored.Cascade.Token)
	}
	if !stored.Cascade.Enabled {
		t.Error("expected cascade.enabled to remain true")
	}
}

func TestProviderCRUDUpdate_PreservesCascadeEnabledWithoutEndpoint(t *testing.T) {
	cfg := validProviderCRUDConfig()
	cfg.Providers.Items["corp-dev"] = config.ProviderConfig{
		Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
		Cascade: &config.ProviderCascadeConfig{
			Enabled: true,
			Token:   "keep-me",
		},
	}
	crud := NewProviderCRUD(cfg)

	input := &ProviderInput{
		Name: "corp-dev",
		Protocols: []string{
			"openai.chat",
			"openai.responses",
			"anthropic.messages",
		},
		RateLimit: RateLimitInput{QPM: 100},
	}

	if err := crud.ValidateUpdate("corp-dev", input); err != nil {
		t.Fatalf("expected update without endpoint to pass when cascade preserved, got: %v", err)
	}
	if err := crud.Update("corp-dev", input); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	stored := crud.draft.Providers.Items["corp-dev"]
	if stored.Cascade == nil || stored.Cascade.Token != "keep-me" {
		t.Fatalf("expected preserved cascade token, got %+v", stored.Cascade)
	}
}
