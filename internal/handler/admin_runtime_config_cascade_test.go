package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func cascadeHandlerSetup(t *testing.T, cfg *config.Config) (*runtimeconfig.Manager, *gin.Engine) {
	t.Helper()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.GET("/admin/runtime-config/draft/cascade", h.GetCascade)
	r.PUT("/admin/runtime-config/draft/cascade", h.UpdateCascade)
	return mgr, r
}

func cascadeHubConfig() *config.Config {
	cfg := createBasicTestConfig()
	cfg.Providers.Items["corp-dev"] = config.ProviderConfig{
		Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
		Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
	}
	return cfg
}

func TestAdminRuntimeConfigHandler_GetCascade_ExposesToken(t *testing.T) {
	_, r := cascadeHandlerSetup(t, cascadeHubConfig())

	req := httptest.NewRequest(http.MethodGet, "/admin/runtime-config/draft/cascade", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	if !strings.Contains(w.Body.String(), "hub-secret") {
		t.Fatal("GET must expose the raw cascade token in plaintext")
	}

	var resp struct {
		Mode      string   `json:"mode"`
		Providers []string `json:"providers"`
		Hub       struct {
			ProviderName string `json:"provider_name"`
			Token        string `json:"token"`
		} `json:"hub"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "hub", resp.Mode)
	require.Equal(t, "corp-dev", resp.Hub.ProviderName)
	require.Equal(t, "hub-secret", resp.Hub.Token)
	for _, name := range resp.Providers {
		if name == "corp-dev" {
			t.Fatalf("providers must exclude the hub provider, got %v", resp.Providers)
		}
	}
}

func TestAdminRuntimeConfigHandler_UpdateCascade_HubRequiresToken(t *testing.T) {
	cfg := cascadeHubConfig()
	mgr, r := cascadeHandlerSetup(t, cfg)

	req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/cascade",
		strings.NewReader(`{"mode":"hub","hub":{"provider_name":"corp-dev"}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	var resp struct {
		Error struct {
			Field string `json:"field"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "hub.token", resp.Error.Field)

	p := mgr.GetDraft().Providers.Items["corp-dev"]
	require.NotNil(t, p.Cascade)
	require.Equal(t, "hub-secret", p.Cascade.Token)
}

func TestAdminRuntimeConfigHandler_UpdateCascade_Hub(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr, r := cascadeHandlerSetup(t, cfg)

	req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/cascade",
		strings.NewReader(`{"mode":"hub","hub":{"provider_name":"corp-dev","token":"hub-secret"}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	p, ok := mgr.GetDraft().Providers.Items["corp-dev"]
	require.True(t, ok, "hub provider must be created")
	require.NotNil(t, p.Cascade)
	require.True(t, p.Cascade.Enabled)
	require.Equal(t, "hub-secret", p.Cascade.Token)
	require.Equal(t, []string{"openai.chat", "openai.responses", "anthropic.messages"}, p.Protocols)
	require.Empty(t, p.Endpoint)
}

func TestAdminRuntimeConfigHandler_UpdateCascade_Spoke(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr, r := cascadeHandlerSetup(t, cfg)

	req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/cascade",
		strings.NewReader(`{"mode":"spoke","spoke":{"hub":"https://api.example.com","token":"spoke-secret","peer":"openai"}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	spoke := mgr.GetDraft().Cascade
	require.NotNil(t, spoke)
	require.Equal(t, "https://api.example.com", spoke.Hub)
	require.Equal(t, "spoke-secret", spoke.Token)
	require.Equal(t, "openai", spoke.Peer)
}

func TestAdminRuntimeConfigHandler_UpdateCascade_HubNameConflict(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr, r := cascadeHandlerSetup(t, cfg)
	openai := mgr.GetDraft().Providers.Items["openai"]

	req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/cascade",
		strings.NewReader(`{"mode":"hub","hub":{"provider_name":"openai","token":"hub-secret"}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusConflict, w.Code)

	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "conflict", resp.Error.Code)
	require.Equal(t, "hub.provider_name", resp.Error.Field)

	got := mgr.GetDraft().Providers.Items["openai"]
	require.Equal(t, openai, got, "existing HTTP provider must not be converted into a hub")
}

func TestAdminRuntimeConfigHandler_UpdateCascade_InvalidBody(t *testing.T) {
	_, r := cascadeHandlerSetup(t, createBasicTestConfig())

	req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/cascade", strings.NewReader(`{`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAdminRuntimeConfigHandler_UpdateCascade_BadMode(t *testing.T) {
	_, r := cascadeHandlerSetup(t, createBasicTestConfig())

	req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/cascade",
		strings.NewReader(`{"mode":"bogus"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	var resp struct {
		Error struct {
			Field string `json:"field"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "mode", resp.Error.Field)
}

func TestAdminRuntimeConfigHandler_UpdateCascade_PeerNotFound(t *testing.T) {
	_, r := cascadeHandlerSetup(t, createBasicTestConfig())

	req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/cascade",
		strings.NewReader(`{"mode":"spoke","spoke":{"hub":"https://api.example.com","token":"spoke-secret","peer":"missing"}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)

	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "not_found", resp.Error.Code)
	require.Equal(t, "spoke.peer", resp.Error.Field)
}
