package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

type stubKeyProvider struct{}

func (stubKeyProvider) ActiveKeys() []config.KeyConfig { return nil }

func registryWithHub(h *cascade.Hub) *cascade.HubRegistry {
	reg := cascade.NewHubRegistry()
	reg.Set(h)
	return reg
}

func TestSetupCascade_UnconfiguredReturns503Not404(t *testing.T) {
	r := gin.New()
	r.NoRoute(func(c *gin.Context) { c.Status(404) })
	Setup(r, stubKeyProvider{}, cascade.NewHubRegistry())

	req := httptest.NewRequest(http.MethodGet, "/cascade", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "cascade hub not configured")
}

func TestSetupCascade_UnconfiguredHubReturns503(t *testing.T) {
	r := gin.New()
	r.NoRoute(func(c *gin.Context) { c.Status(404) })
	Setup(r, stubKeyProvider{}, registryWithHub(cascade.NewHub(cascade.HubConfig{})))

	req := httptest.NewRequest(http.MethodGet, "/cascade", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestSetupCascade_ConfiguredHubRouteRegistered(t *testing.T) {
	hub := cascade.NewHub(cascade.HubConfig{
		ProviderName: "corp-dev",
		Token:        "hub-secret",
	})

	r := gin.New()
	Setup(r, stubKeyProvider{}, registryWithHub(hub))
	server := httptest.NewServer(r)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/cascade"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, cascade.AuthorizationHeaders("hub-secret"))
	if err != nil {
		t.Fatalf("dial configured cascade route: %v (status=%v)", err, resp)
	}
	defer conn.Close()

	payload, err := cascade.EncodeFrame(cascade.Frame{Type: cascade.FrameRegister, Token: "hub-secret"})
	if err != nil {
		t.Fatalf("encode register: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write register: %v", err)
	}

	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read register ack: %v", err)
	}
	ack, err := cascade.DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode register ack: %v", err)
	}
	if ack.Type != cascade.FrameRegisterAck {
		t.Fatalf("ack type = %q, want register_ack", ack.Type)
	}
	if ack.Provider != "corp-dev" {
		t.Fatalf("provider = %q, want corp-dev", ack.Provider)
	}
}

func TestSetupCascade_RegistrySwapUpdatesRoute(t *testing.T) {
	reg := cascade.NewHubRegistry()
	r := gin.New()
	Setup(r, stubKeyProvider{}, reg)
	server := httptest.NewServer(r)
	defer server.Close()

	req := httptest.NewRequest(http.MethodGet, "/cascade", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial status = %d, want 503", w.Code)
	}

	hub := cascade.NewHub(cascade.HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	reg.Set(hub)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/cascade"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, cascade.AuthorizationHeaders("hub-secret"))
	if err != nil {
		t.Fatalf("dial after registry swap: %v", err)
	}
	conn.Close()
}

func TestSetupV1RoutesStillRegistered(t *testing.T) {
	r := gin.New()
	Setup(r, stubKeyProvider{}, cascade.NewHubRegistry())

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
