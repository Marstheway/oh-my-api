package provider

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/gorilla/websocket"
)

type panicTransport struct {
	t *testing.T
}

func (p panicTransport) RoundTrip(*http.Request) (*http.Response, error) {
	p.t.Fatal("HTTP dial must not be used for cascade.enabled provider")
	return nil, nil
}

func startCascadeTestHub(t *testing.T, hub *cascade.Hub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/cascade", hub.ServeHTTP)
	return httptest.NewServer(mux)
}

func dialCascadeTestHub(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/cascade"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, cascade.AuthorizationHeaders("hub-secret"))
	if err != nil {
		t.Fatalf("dial: %v (status=%v)", err, resp)
	}
	return conn
}

func writeCascadeFrame(t *testing.T, conn *websocket.Conn, frame cascade.Frame) {
	t.Helper()
	data, err := cascade.EncodeFrame(frame)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readCascadeFrame(t *testing.T, conn *websocket.Conn) cascade.Frame {
	t.Helper()
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	frame, err := cascade.DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return frame
}

func cascadeStubSpoke(t *testing.T, conn *websocket.Conn) {
	t.Helper()
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
			if frame.Stream != nil && *frame.Stream {
				writeCascadeFrame(t, conn, cascade.Frame{Type: cascade.FrameResult, ID: frame.ID, Chunk: "event: message\ndata: {}\n\n"})
			}
			writeCascadeFrame(t, conn, cascade.Frame{Type: cascade.FrameResult, ID: frame.ID, Final: true})
		case cascade.FramePing:
			writeCascadeFrame(t, conn, cascade.Frame{Type: cascade.FramePong})
		}
	}
}

func TestDoWithMeta_CascadeProvider_DoesNotDialHTTP(t *testing.T) {
	hub := cascade.NewHub(cascade.HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startCascadeTestHub(t, hub)
	defer server.Close()

	conn := dialCascadeTestHub(t, server)
	writeCascadeFrame(t, conn, cascade.Frame{Type: cascade.FrameRegister, Token: "hub-secret"})
	readCascadeFrame(t, conn)
	go cascadeStubSpoke(t, conn)

	providers := map[string]config.ProviderConfig{
		"corp-dev": {
			Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
			Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
		},
	}
	client := NewClient(providers, 5*time.Second, 0, 0)
	reg := cascade.NewHubRegistry()
	reg.Set(hub)
	client.SetCascadeHubRegistry(reg)
	client.SetTransport("corp-dev", panicTransport{t: t})

	body := []byte(`{"model":"corp-dev/gpt-4o","stream":true,"messages":[]}`)
	req, _ := http.NewRequest(http.MethodPost, "http://cascade.local/v1/chat/completions", io.NopCloser(bytes.NewReader(body)))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	resp, err := client.DoWithMeta("corp-dev", RequestMeta{
		UpstreamModel:    "corp-dev/gpt-4o",
		OutboundProtocol: "openai.chat",
	}, req)
	if err != nil {
		t.Fatalf("DoWithMeta: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q", resp.Header.Get("Content-Type"))
	}
}

func TestDoWithMeta_CascadeProvider_NoSessionReturnsError(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"corp-dev": {
			Cascade: &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
		},
	}
	client := NewClient(providers, 5*time.Second, 0, 0)
	reg := cascade.NewHubRegistry()
	reg.Set(cascade.NewHub(cascade.HubConfig{ProviderName: "corp-dev", Token: "hub-secret"}))
	client.SetCascadeHubRegistry(reg)
	client.SetTransport("corp-dev", panicTransport{t: t})

	req, _ := http.NewRequest(http.MethodPost, "http://cascade.local/v1/chat/completions", strings.NewReader(`{}`))
	_, err := client.DoWithMeta("corp-dev", RequestMeta{
		UpstreamModel:    "corp-dev/gpt-4o",
		OutboundProtocol: "openai.chat",
	}, req)
	if err == nil {
		t.Fatal("expected error without cascade session")
	}
	if !strings.Contains(err.Error(), "session") {
		t.Fatalf("error = %v, want session unavailable", err)
	}
	if client.CascadeReady("corp-dev") {
		t.Fatal("expected cascade provider not ready without session")
	}
}

func TestDo_CascadeProvider_RefusesHTTPDial(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"corp-dev": {
			Cascade: &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
		},
	}
	client := NewClient(providers, 5*time.Second, 0, 0)
	client.SetTransport("corp-dev", panicTransport{t: t})
	req, _ := http.NewRequest(http.MethodPost, "http://cascade.local/v1/chat/completions", strings.NewReader(`{}`))
	_, err := client.Do("corp-dev", req)
	if err == nil {
		t.Fatal("expected HTTP dial to be rejected for cascade provider")
	}
	if !strings.Contains(err.Error(), "does not support HTTP dial") {
		t.Fatalf("error = %v", err)
	}
}

func TestDoWithMeta_CascadeProvider_SkipsResponsesCompat(t *testing.T) {
	hub := cascade.NewHub(cascade.HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startCascadeTestHub(t, hub)
	defer server.Close()

	conn := dialCascadeTestHub(t, server)
	writeCascadeFrame(t, conn, cascade.Frame{Type: cascade.FrameRegister, Token: "hub-secret"})
	readCascadeFrame(t, conn)
	go cascadeStubSpoke(t, conn)

	providers := map[string]config.ProviderConfig{
		"corp-dev": {
			Protocols: []string{"openai.responses"},
			Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
		},
	}
	client := NewClient(providers, 5*time.Second, 0, 0)
	reg := cascade.NewHubRegistry()
	reg.Set(hub)
	client.SetCascadeHubRegistry(reg)
	client.SetTransport("corp-dev", panicTransport{t: t})

	body := []byte(`{"model":"corp-dev/gpt-4o","input":[],"tool_choice":{"type":"function","name":"x"}}`)
	req, _ := http.NewRequest(http.MethodPost, "http://cascade.local/v1/responses", io.NopCloser(bytes.NewReader(body)))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	resp, err := client.DoWithMeta("corp-dev", RequestMeta{
		UpstreamModel:    "corp-dev/gpt-4o",
		OutboundProtocol: "openai.responses",
	}, req)
	if err != nil {
		t.Fatalf("DoWithMeta: %v", err)
	}
	resp.Body.Close()
}

func TestDoWithMeta_CascadeProvider_RegistrySwapVisible(t *testing.T) {
	hub1 := cascade.NewHub(cascade.HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server1 := startCascadeTestHub(t, hub1)
	defer server1.Close()

	conn1 := dialCascadeTestHub(t, server1)
	writeCascadeFrame(t, conn1, cascade.Frame{Type: cascade.FrameRegister, Token: "hub-secret"})
	readCascadeFrame(t, conn1)
	go cascadeStubSpoke(t, conn1)

	hub2 := cascade.NewHub(cascade.HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server2 := startCascadeTestHub(t, hub2)
	defer server2.Close()

	conn2 := dialCascadeTestHub(t, server2)
	writeCascadeFrame(t, conn2, cascade.Frame{Type: cascade.FrameRegister, Token: "hub-secret"})
	readCascadeFrame(t, conn2)
	go cascadeStubSpoke(t, conn2)

	providers := map[string]config.ProviderConfig{
		"corp-dev": {
			Protocols: []string{"openai.chat"},
			Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
		},
	}
	reg := cascade.NewHubRegistry()
	reg.Set(hub1)
	client := NewClient(providers, 5*time.Second, 0, 0)
	client.SetCascadeHubRegistry(reg)
	client.SetTransport("corp-dev", panicTransport{t: t})

	body := []byte(`{"model":"corp-dev/gpt-4o","messages":[]}`)
	req, _ := http.NewRequest(http.MethodPost, "http://cascade.local/v1/chat/completions", bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	if _, err := client.DoWithMeta("corp-dev", RequestMeta{
		UpstreamModel:    "corp-dev/gpt-4o",
		OutboundProtocol: "openai.chat",
	}, req); err != nil {
		t.Fatalf("DoWithMeta with hub1: %v", err)
	}

	reg.Set(hub2)
	req2, _ := http.NewRequest(http.MethodPost, "http://cascade.local/v1/chat/completions", bytes.NewReader(body))
	req2.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	if _, err := client.DoWithMeta("corp-dev", RequestMeta{
		UpstreamModel:    "corp-dev/gpt-4o",
		OutboundProtocol: "openai.chat",
	}, req2); err != nil {
		t.Fatalf("DoWithMeta with hub2 after registry swap: %v", err)
	}
}
