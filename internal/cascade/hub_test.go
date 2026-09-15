package cascade

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func startTestHub(t *testing.T, hub *Hub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/cascade", hub.ServeHTTP)
	return httptest.NewServer(mux)
}

func dialTestHub(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	return dialTestHubWithToken(t, server, "hub-secret")
}

func dialTestHubWithToken(t *testing.T, server *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/cascade"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, AuthorizationHeaders(token))
	if err != nil {
		t.Fatalf("dial: %v (status=%v)", err, resp)
	}
	return conn
}

func readFrame(t *testing.T, conn *websocket.Conn) Frame {
	t.Helper()
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	frame, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return frame
}

func writeFrame(t *testing.T, conn *websocket.Conn, frame Frame) {
	t.Helper()
	data, err := EncodeFrame(frame)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestHub_ValidTokenRegisters(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startTestHub(t, hub)
	defer server.Close()

	conn := dialTestHub(t, server)
	defer conn.Close()

	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "hub-secret"})
	ack := readFrame(t, conn)
	if ack.Type != FrameRegisterAck {
		t.Fatalf("ack type = %q, want register_ack", ack.Type)
	}
	if ack.Provider != "corp-dev" {
		t.Fatalf("provider = %q, want corp-dev", ack.Provider)
	}

	session, ok := hub.Session()
	if !ok {
		t.Fatal("expected registered session")
	}
	if session.ProviderName() != "corp-dev" {
		t.Fatalf("session provider = %q", session.ProviderName())
	}
}

func TestHub_InvalidHandshakeTokenRejected(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startTestHub(t, hub)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/cascade"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, AuthorizationHeaders("wrong-token"))
	if err == nil {
		t.Fatal("expected handshake with wrong bearer token to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

func TestHub_RegisterTokenMismatchRejected(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startTestHub(t, hub)
	defer server.Close()

	conn := dialTestHub(t, server)
	defer conn.Close()

	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "wrong-token"})
	frame := readFrame(t, conn)
	if frame.Type != FrameError {
		t.Fatalf("frame type = %q, want error", frame.Type)
	}

	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected connection to close after invalid token")
	}
}

func TestHub_ReconnectReplacesOldSession(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startTestHub(t, hub)
	defer server.Close()

	conn1 := dialTestHub(t, server)
	writeFrame(t, conn1, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn1)

	conn2 := dialTestHub(t, server)
	writeFrame(t, conn2, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn2)

	conn1.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn1.ReadMessage(); err == nil {
		t.Fatal("expected first connection to be closed when replaced")
	}

	session, ok := hub.Session()
	if !ok {
		t.Fatal("expected active session")
	}
	if err := session.WriteFrame(Frame{Type: FramePing}); err != nil {
		t.Fatalf("write ping to active session: %v", err)
	}
	conn2.SetReadDeadline(time.Now().Add(time.Second))
	frame := readFrame(t, conn2)
	if frame.Type != FramePing {
		t.Fatalf("expected ping on second connection, got %q", frame.Type)
	}
}

func TestHub_UnconfiguredReturns503(t *testing.T) {
	hub := NewHub(HubConfig{})
	req := httptest.NewRequest(http.MethodGet, "/cascade", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	rec := httptest.NewRecorder()

	hub.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "404") {
		t.Fatalf("unexpected 404 body: %s", rec.Body.String())
	}
}

func TestHub_HeartbeatPingPong(t *testing.T) {
	hub := NewHub(HubConfig{
		ProviderName:    "corp-dev",
		Token:           "hub-secret",
		HeartbeatPeriod: 50 * time.Millisecond,
	})
	server := startTestHub(t, hub)
	defer server.Close()

	conn := dialTestHub(t, server)
	defer conn.Close()

	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn)

	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	frame := readFrame(t, conn)
	if frame.Type != FramePing {
		t.Fatalf("frame type = %q, want ping", frame.Type)
	}
	writeFrame(t, conn, Frame{Type: FramePong})
}

func TestHub_SendCancelRoundTrip(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startTestHub(t, hub)
	defer server.Close()

	conn := dialTestHub(t, server)
	defer conn.Close()

	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn)

	received := make(chan Frame, 1)
	go func() {
		received <- readFrame(t, conn)
	}()

	session, ok := hub.Session()
	if !ok {
		t.Fatal("expected registered session")
	}
	if err := session.SendCancel("job-42"); err != nil {
		t.Fatalf("SendCancel: %v", err)
	}

	select {
	case frame := <-received:
		if frame.Type != FrameCancel {
			t.Fatalf("frame type = %q, want cancel", frame.Type)
		}
		if frame.ID != "job-42" {
			t.Fatalf("frame id = %q, want job-42", frame.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for cancel frame")
	}
}

func TestHubFromConfig(t *testing.T) {
	hub, ok := HubFromProvider("corp-dev", "token-1")
	if !ok || !hub.Configured() {
		t.Fatal("expected configured hub")
	}
	if hub.ProviderName() != "corp-dev" {
		t.Fatalf("provider = %q", hub.ProviderName())
	}

	unconfigured := NewHub(HubConfig{})
	if unconfigured.Configured() {
		t.Fatal("expected empty hub to be unconfigured")
	}
}
