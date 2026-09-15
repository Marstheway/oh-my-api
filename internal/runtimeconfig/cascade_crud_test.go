package runtimeconfig

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
)

func cascadeTestConfig() *config.Config {
	return &config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.ProviderConfig{
				"openai": {Endpoint: "https://api.openai.com/v1", Protocols: []string{"openai.chat"}},
			},
		},
	}
}

func TestCascadeCRUD_ViewExposesToken(t *testing.T) {
	cfg := cascadeTestConfig()
	cfg.Providers.Items["corp-dev"] = config.ProviderConfig{
		Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
		Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
	}

	view := NewCascadeCRUD(cfg).View()
	if view.Mode != CascadeModeHub {
		t.Fatalf("mode = %q, want hub", view.Mode)
	}
	if view.Hub == nil {
		t.Fatal("hub view must not be nil")
	}
	if view.Hub.ProviderName != "corp-dev" {
		t.Errorf("hub provider name = %q, want corp-dev", view.Hub.ProviderName)
	}
	if view.Hub.Token != "hub-secret" {
		t.Errorf("hub token = %q, want hub-secret", view.Hub.Token)
	}
	if view.Spoke != nil {
		t.Errorf("spoke view must be nil, got %+v", view.Spoke)
	}
	if got, want := view.Providers, []string{"openai"}; !reflect.DeepEqual(got, want) {
		t.Errorf("providers = %v, want %v", got, want)
	}
}

func TestCascadeCRUD_UpdateHub(t *testing.T) {
	cfg := cascadeTestConfig()
	crud := NewCascadeCRUD(cfg)

	if err := crud.Update(&CascadeConfigInput{
		Mode: CascadeModeHub,
		Hub:  &CascadeHubInput{ProviderName: "corp-dev", Token: "hub-secret"},
	}); err != nil {
		t.Fatalf("update hub: %v", err)
	}

	p, ok := cfg.Providers.Items["corp-dev"]
	if !ok {
		t.Fatal("hub provider not created")
	}
	if p.Cascade == nil || !p.Cascade.Enabled || p.Cascade.Token != "hub-secret" {
		t.Fatalf("cascade config = %+v, want enabled with token", p.Cascade)
	}
	wantProtocols := []string{"openai.chat", "openai.responses", "anthropic.messages"}
	if !reflect.DeepEqual(p.Protocols, wantProtocols) {
		t.Errorf("protocols = %v, want %v", p.Protocols, wantProtocols)
	}
	if p.Endpoint != "" || len(p.Endpoints) != 0 {
		t.Errorf("hub provider must have no endpoint, got endpoint=%q endpoints=%v", p.Endpoint, p.Endpoints)
	}
	if cfg.Cascade != nil {
		t.Errorf("top-level cascade must be cleared, got %+v", cfg.Cascade)
	}
}

func TestCascadeCRUD_UpdateSpoke(t *testing.T) {
	cfg := cascadeTestConfig()
	crud := NewCascadeCRUD(cfg)

	if err := crud.Update(&CascadeConfigInput{
		Mode:  CascadeModeSpoke,
		Spoke: &CascadeSpokeInput{Hub: "https://api.example.com", Token: "spoke-secret", Peer: "openai"},
	}); err != nil {
		t.Fatalf("update spoke: %v", err)
	}

	if cfg.Cascade == nil {
		t.Fatal("top-level cascade must be set")
	}
	if cfg.Cascade.Hub != "https://api.example.com" || cfg.Cascade.Token != "spoke-secret" || cfg.Cascade.Peer != "openai" {
		t.Errorf("spoke cascade = %+v", cfg.Cascade)
	}
	if _, ok := cfg.Providers.Items["openai"]; !ok {
		t.Error("peer provider must remain")
	}
}

func TestCascadeCRUD_UpdateSpoke_RejectsBadHub(t *testing.T) {
	cfg := cascadeTestConfig()
	crud := NewCascadeCRUD(cfg)

	err := crud.Update(&CascadeConfigInput{
		Mode:  CascadeModeSpoke,
		Spoke: &CascadeSpokeInput{Hub: "wss://api.example.com/cascade", Token: "spoke-secret", Peer: "openai"},
	})
	if err == nil {
		t.Fatal("expected error for wss hub origin")
	}
	if e, ok := err.(*Error); !ok || e.Field != "spoke.hub" {
		t.Fatalf("error = %v, want spoke.hub field error", err)
	}
}

func TestCascadeCRUD_UpdateDisabled_ClearsBothRoles(t *testing.T) {
	cfg := cascadeTestConfig()
	cfg.Providers.Items["corp-dev"] = config.ProviderConfig{
		Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
		Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
	}
	cfg.Cascade = &config.SpokeCascadeConfig{Hub: "https://api.example.com", Token: "spoke-secret", Peer: "openai"}

	if err := NewCascadeCRUD(cfg).Update(&CascadeConfigInput{Mode: CascadeModeDisabled}); err != nil {
		t.Fatalf("update disabled: %v", err)
	}
	if cfg.Cascade != nil {
		t.Errorf("top-level cascade must be removed, got %+v", cfg.Cascade)
	}
	if _, ok := cfg.Providers.Items["corp-dev"]; ok {
		t.Error("cascade-only hub provider must be removed after disabling")
	}
}

func TestCascadeCRUD_UpdateHub_RejectsExistingHTTPProvider(t *testing.T) {
	cfg := cascadeTestConfig()
	openai := cfg.Providers.Items["openai"]

	err := NewCascadeCRUD(cfg).Update(&CascadeConfigInput{
		Mode: CascadeModeHub,
		Hub:  &CascadeHubInput{ProviderName: "openai", Token: "hub-secret"},
	})
	if err == nil {
		t.Fatal("expected conflict when hub name matches an existing HTTP provider")
	}
	e, ok := err.(*Error)
	if !ok || e.Code != ErrCodeConflict || e.Field != "hub.provider_name" {
		t.Fatalf("error = %v, want conflict hub.provider_name", err)
	}
	if !strings.Contains(e.Message, "openai") {
		t.Fatalf("error = %v, want existing provider name", err)
	}

	got := cfg.Providers.Items["openai"]
	if !reflect.DeepEqual(got, openai) {
		t.Fatalf("existing HTTP provider was mutated: got %+v, want %+v", got, openai)
	}
	if cfg.Cascade != nil {
		t.Errorf("top-level cascade must stay unset, got %+v", cfg.Cascade)
	}
}

func TestCascadeCRUD_UpdateHub_RejectsExistingRemoteBridgeProvider(t *testing.T) {
	cfg := cascadeTestConfig()
	cfg.Providers.Items["xai"] = config.ProviderConfig{
		Protocols: []string{"openai.chat"},
		RemoteBridge: &config.RemoteBridgeConfig{
			Enabled:  true,
			Provider: "xai-oauth",
			Token:    "bridge-secret",
		},
	}
	before := cfg.Providers.Items["xai"]

	err := NewCascadeCRUD(cfg).Update(&CascadeConfigInput{
		Mode: CascadeModeHub,
		Hub:  &CascadeHubInput{ProviderName: "xai", Token: "hub-secret"},
	})
	if err == nil {
		t.Fatal("expected conflict when hub name matches an existing remote_bridge provider")
	}
	e, ok := err.(*Error)
	if !ok || e.Code != ErrCodeConflict || e.Field != "hub.provider_name" {
		t.Fatalf("error = %v, want conflict hub.provider_name", err)
	}

	got := cfg.Providers.Items["xai"]
	if !reflect.DeepEqual(got, before) {
		t.Fatalf("existing remote_bridge provider was mutated: got %+v, want %+v", got, before)
	}
}

func TestCascadeCRUD_UpdateHub_RejectsSwitchingToExistingProvider(t *testing.T) {
	cfg := cascadeTestConfig()
	cfg.Providers.Items["corp-dev"] = config.ProviderConfig{
		Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
		Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
	}
	openai := cfg.Providers.Items["openai"]

	err := NewCascadeCRUD(cfg).Update(&CascadeConfigInput{
		Mode: CascadeModeHub,
		Hub:  &CascadeHubInput{ProviderName: "openai", Token: "new-secret"},
	})
	if err == nil {
		t.Fatal("expected conflict when switching hub identity onto an existing HTTP provider")
	}
	e, ok := err.(*Error)
	if !ok || e.Code != ErrCodeConflict || e.Field != "hub.provider_name" {
		t.Fatalf("error = %v, want conflict hub.provider_name", err)
	}

	if got := cfg.Providers.Items["openai"]; !reflect.DeepEqual(got, openai) {
		t.Fatalf("existing HTTP provider was mutated: got %+v, want %+v", got, openai)
	}
	hub := cfg.Providers.Items["corp-dev"]
	if hub.Cascade == nil || !hub.Cascade.Enabled || hub.Cascade.Token != "hub-secret" {
		t.Fatalf("previous hub must remain unchanged, got %+v", hub.Cascade)
	}
}

func TestCascadeCRUD_UpdateHub_RequiresToken(t *testing.T) {
	cfg := cascadeTestConfig()
	err := NewCascadeCRUD(cfg).Update(&CascadeConfigInput{
		Mode: CascadeModeHub,
		Hub:  &CascadeHubInput{ProviderName: "corp-dev"},
	})
	if err == nil {
		t.Fatal("expected error when creating hub without token")
	}
	if e, ok := err.(*Error); !ok || e.Field != "hub.token" {
		t.Fatalf("error = %v, want hub.token field error", err)
	}
}

func TestCascadeCRUD_UpdateSpoke_RequiresToken(t *testing.T) {
	cfg := cascadeTestConfig()
	err := NewCascadeCRUD(cfg).Update(&CascadeConfigInput{
		Mode:  CascadeModeSpoke,
		Spoke: &CascadeSpokeInput{Hub: "https://api.example.com", Peer: "openai"},
	})
	if err == nil {
		t.Fatal("expected error when creating spoke without token")
	}
	if e, ok := err.(*Error); !ok || e.Field != "spoke.token" {
		t.Fatalf("error = %v, want spoke.token field error", err)
	}
}

func TestCascadeCRUD_UpdateSpoke_RejectsHubPeer(t *testing.T) {
	cfg := cascadeTestConfig()
	cfg.Providers.Items["corp-dev"] = config.ProviderConfig{
		Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
		Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
	}
	err := NewCascadeCRUD(cfg).Update(&CascadeConfigInput{
		Mode:  CascadeModeSpoke,
		Spoke: &CascadeSpokeInput{Hub: "https://api.example.com", Token: "spoke-secret", Peer: "corp-dev"},
	})
	if err == nil {
		t.Fatal("expected error when hub provider is used as peer")
	}
	if e, ok := err.(*Error); !ok || e.Field != "spoke.peer" {
		t.Fatalf("error = %v, want spoke.peer field error", err)
	}
}

func TestCascadeCRUD_UpdateSpoke_RejectsMissingPeer(t *testing.T) {
	cfg := cascadeTestConfig()
	err := NewCascadeCRUD(cfg).Update(&CascadeConfigInput{
		Mode:  CascadeModeSpoke,
		Spoke: &CascadeSpokeInput{Hub: "https://api.example.com", Token: "spoke-secret", Peer: "missing"},
	})
	if err == nil {
		t.Fatal("expected error for missing peer")
	}
	if e, ok := err.(*Error); !ok || e.Code != ErrCodeNotFound || e.Field != "spoke.peer" {
		t.Fatalf("error = %v, want not_found spoke.peer", err)
	}
}

func TestCascadeCRUD_UpdateSpoke_ClearsHubProvider(t *testing.T) {
	cfg := cascadeTestConfig()
	cfg.Providers.Items["corp-dev"] = config.ProviderConfig{
		Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
		Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
	}
	if err := NewCascadeCRUD(cfg).Update(&CascadeConfigInput{
		Mode:  CascadeModeSpoke,
		Spoke: &CascadeSpokeInput{Hub: "https://api.example.com", Token: "spoke-secret", Peer: "openai"},
	}); err != nil {
		t.Fatalf("update spoke: %v", err)
	}
	if _, ok := cfg.Providers.Items["corp-dev"]; ok {
		t.Error("cascade-only hub provider must be removed when switching to spoke")
	}
	if cfg.Cascade == nil || cfg.Cascade.Peer != "openai" {
		t.Fatalf("spoke cascade = %+v", cfg.Cascade)
	}
}

func TestCascadeCRUD_View_SpokeAndDisabled(t *testing.T) {
	cfg := cascadeTestConfig()
	cfg.Cascade = &config.SpokeCascadeConfig{Hub: "https://api.example.com", Token: "spoke-secret", Peer: "openai"}
	view := NewCascadeCRUD(cfg).View()
	if view.Mode != CascadeModeSpoke {
		t.Fatalf("mode = %q, want spoke", view.Mode)
	}
	if view.Spoke == nil || view.Spoke.Hub != "https://api.example.com" || view.Spoke.Token != "spoke-secret" || view.Spoke.Peer != "openai" {
		t.Fatalf("spoke view = %+v", view.Spoke)
	}
	if view.Hub != nil {
		t.Fatalf("hub view must be nil, got %+v", view.Hub)
	}

	disabled := NewCascadeCRUD(cascadeTestConfig()).View()
	if disabled.Mode != CascadeModeDisabled || disabled.Hub != nil || disabled.Spoke != nil {
		t.Fatalf("disabled view = %+v", disabled)
	}
}

func TestCascadeCRUD_Update_InvalidMode(t *testing.T) {
	cfg := cascadeTestConfig()
	err := NewCascadeCRUD(cfg).Update(&CascadeConfigInput{Mode: "bogus"})
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if e, ok := err.(*Error); !ok || e.Field != "mode" {
		t.Fatalf("error = %v, want mode field error", err)
	}
}

func TestCascadeCRUD_UpdateHub_RequiresTokenWhenEditing(t *testing.T) {
	cfg := cascadeTestConfig()
	cfg.Providers.Items["corp-dev"] = config.ProviderConfig{
		Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
		Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "existing-secret"},
	}
	crud := NewCascadeCRUD(cfg)

	err := crud.Update(&CascadeConfigInput{
		Mode: CascadeModeHub,
		Hub:  &CascadeHubInput{ProviderName: "corp-dev"},
	})
	if err == nil {
		t.Fatal("expected error when editing hub without token")
	}
	if e, ok := err.(*Error); !ok || e.Field != "hub.token" {
		t.Fatalf("error = %v, want hub.token field error", err)
	}

	p := cfg.Providers.Items["corp-dev"]
	if p.Cascade == nil || p.Cascade.Token != "existing-secret" {
		t.Fatalf("cascade = %+v, want unchanged token", p.Cascade)
	}
}

func TestCascadeCRUD_UpdateDisabled_RejectsReferencedHubProvider(t *testing.T) {
	cfg := cascadeTestConfig()
	cfg.Providers.Items["corp-dev"] = config.ProviderConfig{
		Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
		Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
	}
	cfg.ModelGroups = append(cfg.ModelGroups, config.ModelGroupConfig{
		Name:   "corp-dev-chat",
		Models: config.ModelEntries{{Model: "corp-dev/local-gpt", Weight: 1}},
	})

	err := NewCascadeCRUD(cfg).Update(&CascadeConfigInput{Mode: CascadeModeDisabled})
	if err == nil {
		t.Fatal("expected conflict error for referenced hub provider")
	}
	e, ok := err.(*Error)
	if !ok || e.Code != ErrCodeConflict {
		t.Fatalf("error = %v, want conflict", err)
	}
	if !strings.Contains(e.Message, "corp-dev-chat") {
		t.Fatalf("error = %v, want referencing group name", err)
	}

	// Draft must remain unchanged so the operator can fix the model groups first.
	if p := cfg.Providers.Items["corp-dev"]; p.Cascade == nil || !p.Cascade.Enabled {
		t.Fatal("referenced hub provider must remain cascade-enabled after rejected transition")
	}
}
