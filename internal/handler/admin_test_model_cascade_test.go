package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func cascadeProviderConfig() *config.Config {
	cfg := createBasicTestConfig()
	cfg.Providers.Items["devcloud"] = config.ProviderConfig{
		Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
		Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
	}
	return cfg
}

func postTestModel(t *testing.T, h *AdminRuntimeConfigHandler, providerName, model string) (int, testModelResponse) {
	t.Helper()
	r := gin.New()
	r.POST("/admin/runtime-config/test-model", h.TestModel)

	body, err := json.Marshal(testModelRequest{Provider: providerName, Model: model})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/admin/runtime-config/test-model", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp testModelResponse
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	}
	return w.Code, resp
}

func startAdminCascadeHub(t *testing.T, hub *cascade.Hub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/cascade", hub.ServeHTTP)
	return httptest.NewServer(mux)
}

func dialAdminCascadeHub(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/cascade"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, cascade.AuthorizationHeaders("hub-secret"))
	if err != nil {
		t.Fatalf("dial: %v (status=%v)", err, resp)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func writeAdminCascadeFrame(t *testing.T, conn *websocket.Conn, frame cascade.Frame) {
	t.Helper()
	data, err := cascade.EncodeFrame(frame)
	require.NoError(t, err)
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, data))
}

func readAdminCascadeFrame(t *testing.T, conn *websocket.Conn) cascade.Frame {
	t.Helper()
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)
	frame, err := cascade.DecodeFrame(data)
	require.NoError(t, err)
	return frame
}

func registerAdminCascadeSpoke(t *testing.T, hub *cascade.Hub, handleJob func(cascade.Frame, *websocket.Conn)) {
	t.Helper()
	server := startAdminCascadeHub(t, hub)
	t.Cleanup(server.Close)
	conn := dialAdminCascadeHub(t, server)
	writeAdminCascadeFrame(t, conn, cascade.Frame{Type: cascade.FrameRegister, Token: "hub-secret"})
	ack := readAdminCascadeFrame(t, conn)
	if ack.Type != cascade.FrameRegisterAck {
		t.Fatalf("ack type = %q", ack.Type)
	}
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			frame, err := cascade.DecodeFrame(data)
			if err != nil {
				continue
			}
			switch frame.Type {
			case cascade.FrameJob:
				if handleJob != nil {
					handleJob(frame, conn)
				}
			case cascade.FramePing:
				payload, encErr := cascade.EncodeFrame(cascade.Frame{Type: cascade.FramePong})
				if encErr == nil {
					_ = conn.WriteMessage(websocket.TextMessage, payload)
				}
			}
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := hub.Session(); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("spoke session did not register")
}

func TestTestModel_CascadeHubNotConfigured(t *testing.T) {
	mgr := createInMemoryManager(t, cascadeProviderConfig())
	h := NewAdminRuntimeConfigHandler(mgr)

	code, resp := postTestModel(t, h, "devcloud", "gpt-5.6-luna")
	require.Equal(t, http.StatusOK, code)
	require.False(t, resp.Success)
	require.Equal(t, "cascade hub not configured", resp.Error)
}

func TestTestModel_CascadeSpokeNotConnected(t *testing.T) {
	mgr := createInMemoryManager(t, cascadeProviderConfig())
	h := NewAdminRuntimeConfigHandler(mgr)
	reg := cascade.NewHubRegistry()
	reg.Set(cascade.NewHub(cascade.HubConfig{ProviderName: "devcloud", Token: "hub-secret"}))
	h.SetCascadeHubs(reg)

	code, resp := postTestModel(t, h, "devcloud", "gpt-5.6-luna")
	require.Equal(t, http.StatusOK, code)
	require.False(t, resp.Success)
	require.Equal(t, "cascade spoke not connected", resp.Error)
}

func TestTestModel_CascadeJobSuccess(t *testing.T) {
	mgr := createInMemoryManager(t, cascadeProviderConfig())
	h := NewAdminRuntimeConfigHandler(mgr)
	hub := cascade.NewHub(cascade.HubConfig{ProviderName: "devcloud", Token: "hub-secret"})
	reg := cascade.NewHubRegistry()
	reg.Set(hub)
	h.SetCascadeHubs(reg)

	gotJob := make(chan cascade.Frame, 1)
	registerAdminCascadeSpoke(t, hub, func(frame cascade.Frame, conn *websocket.Conn) {
		select {
		case gotJob <- frame:
		default:
		}
		chunk, _ := cascade.EncodeFrame(cascade.Frame{
			Type:  cascade.FrameResult,
			ID:    frame.ID,
			Chunk: "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n",
		})
		_ = conn.WriteMessage(websocket.TextMessage, chunk)
		final, _ := cascade.EncodeFrame(cascade.Frame{Type: cascade.FrameResult, ID: frame.ID, Final: true})
		_ = conn.WriteMessage(websocket.TextMessage, final)
	})

	code, resp := postTestModel(t, h, "devcloud", "gpt-5.6-luna")
	require.Equal(t, http.StatusOK, code)
	require.True(t, resp.Success, "error=%s", resp.Error)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	select {
	case job := <-gotJob:
		require.Equal(t, "gpt-5.6-luna", job.Model)
		require.Equal(t, "openai.chat", job.Protocol)
		require.NotNil(t, job.Stream)
		require.True(t, *job.Stream)
	case <-time.After(time.Second):
		t.Fatal("did not observe cascade job frame")
	}
}

func TestTestModel_CascadeJobErrorSurfaced(t *testing.T) {
	mgr := createInMemoryManager(t, cascadeProviderConfig())
	h := NewAdminRuntimeConfigHandler(mgr)
	hub := cascade.NewHub(cascade.HubConfig{ProviderName: "devcloud", Token: "hub-secret"})
	reg := cascade.NewHubRegistry()
	reg.Set(hub)
	h.SetCascadeHubs(reg)

	registerAdminCascadeSpoke(t, hub, func(frame cascade.Frame, conn *websocket.Conn) {
		payload, err := cascade.EncodeFrame(cascade.Frame{
			Type:    cascade.FrameError,
			ID:      frame.ID,
			Message: "model \"gpt-5.6-luna\" not found on spoke",
		})
		if err == nil {
			_ = conn.WriteMessage(websocket.TextMessage, payload)
		}
	})

	code, resp := postTestModel(t, h, "devcloud", "gpt-5.6-luna")
	require.Equal(t, http.StatusOK, code)
	require.False(t, resp.Success)
	require.Contains(t, resp.Error, "not found on spoke")
}
