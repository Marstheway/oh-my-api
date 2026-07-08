package runtimeconfig

import (
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

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
