package scheduler

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/gorilla/websocket"
)

func TestCascadeJobStream_4xxHubResponseNotSSE(t *testing.T) {
	hub := cascade.NewHub(cascade.HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startCascadeProbeHub(t, hub)
	defer server.Close()

	conn := dialCascadeProbeHub(t, server)
	writeCascadeProbeFrame(t, conn, cascade.Frame{Type: cascade.FrameRegister, Token: "hub-secret"})
	readCascadeProbeFrame(t, conn)

	go cascadeProbeStreamErrorSpoke(t, conn)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := hub.ExecuteJob(ctx, cascade.JobRequest{
		ID:       "job-stream-4xx",
		Protocol: "openai.chat",
		Model:    "corp-dev/gpt-4o",
		Stream:   true,
		Body:     []byte(`{"model":"corp-dev/gpt-4o","stream":true,"messages":[]}`),
	})
	if err != nil {
		t.Fatalf("ExecuteJob: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		t.Fatalf("content-type = %q, want JSON error not SSE", resp.Header.Get("Content-Type"))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "model overloaded") {
		t.Fatalf("body = %s", body)
	}
}

func cascadeProbeStreamErrorSpoke(t *testing.T, conn *websocket.Conn) {
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
			writeCascadeProbeFrame(t, conn, cascade.Frame{
				Type:   cascade.FrameResult,
				ID:     frame.ID,
				Chunk:  `{"error":{"message":"model overloaded"}}`,
				Final:  true,
				Status: http.StatusBadGateway,
			})
		case cascade.FramePing:
			writeCascadeProbeFrame(t, conn, cascade.Frame{Type: cascade.FramePong})
		}
	}
}

func TestCascadeJobStream_ProbeReadsFirstEventBeforeFinal(t *testing.T) {
	hub := cascade.NewHub(cascade.HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startCascadeProbeHub(t, hub)
	defer server.Close()

	conn := dialCascadeProbeHub(t, server)
	writeCascadeProbeFrame(t, conn, cascade.Frame{Type: cascade.FrameRegister, Token: "hub-secret"})
	readCascadeProbeFrame(t, conn)

	readAck := make(chan struct{})
	var readOnce sync.Once
	go cascadeProbeStubSpoke(t, conn, readAck)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := hub.ExecuteJob(ctx, cascade.JobRequest{
		ID:       "job-probe-1",
		Protocol: "openai.chat",
		Model:    "corp-dev/gpt-4o",
		Stream:   true,
		Body:     []byte(`{"model":"corp-dev/gpt-4o","stream":true,"messages":[]}`),
	})
	if err != nil {
		t.Fatalf("ExecuteJob: %v", err)
	}
	defer resp.Body.Close()

	kind, _, _, err := ProbeStreamPrefixForTest(resp, "openai.chat", 2*time.Second, 0, time.Now(), func() {
		readOnce.Do(func() { close(readAck) })
	})
	if err != nil {
		t.Fatalf("probeStreamPrefix: %v", err)
	}
	if kind != FailureKindSuccess {
		t.Fatalf("failure kind = %v, want success", kind)
	}
}

func TestCascadeJobStream_DisconnectBeforeChunk_ExecuteJobFails(t *testing.T) {
	hub := cascade.NewHub(cascade.HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startCascadeProbeHub(t, hub)
	defer server.Close()

	conn := dialCascadeProbeHub(t, server)
	writeCascadeProbeFrame(t, conn, cascade.Frame{Type: cascade.FrameRegister, Token: "hub-secret"})
	readCascadeProbeFrame(t, conn)

	jobSeen := make(chan struct{}, 1)
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
			if frame.Type == cascade.FrameJob {
				jobSeen <- struct{}{}
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := hub.ExecuteJob(ctx, cascade.JobRequest{
			ID:       "job-disconnect-1",
			Protocol: "openai.chat",
			Model:    "corp-dev/gpt-4o",
			Stream:   true,
			Body:     []byte(`{"model":"corp-dev/gpt-4o","stream":true,"messages":[]}`),
		})
		done <- err
	}()

	select {
	case <-jobSeen:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for job frame")
	}

	conn.Close()
	time.Sleep(50 * time.Millisecond)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ExecuteJob: want error when spoke disconnects before first chunk")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for ExecuteJob to fail after disconnect")
	}
}

func TestCascadeJobStream_PrefillTimeoutSendsCancel(t *testing.T) {
	hub := cascade.NewHub(cascade.HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startCascadeProbeHub(t, hub)
	defer server.Close()

	conn := dialCascadeProbeHub(t, server)
	writeCascadeProbeFrame(t, conn, cascade.Frame{Type: cascade.FrameRegister, Token: "hub-secret"})
	readCascadeProbeFrame(t, conn)

	cancelSeen := make(chan string, 1)
	jobSeen := make(chan struct{}, 1)
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
				jobSeen <- struct{}{}
			case cascade.FrameCancel:
				cancelSeen <- frame.ID
			case cascade.FramePing:
				writeCascadeProbeFrame(t, conn, cascade.Frame{Type: cascade.FramePong})
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := hub.ExecuteJob(ctx, cascade.JobRequest{
		ID:       "job-prefill-cancel",
		Protocol: "openai.chat",
		Model:    "corp-dev/gpt-4o",
		Stream:   true,
		Body:     []byte(`{"model":"corp-dev/gpt-4o","stream":true,"messages":[]}`),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ExecuteJob err = %v, want context deadline exceeded", err)
	}

	select {
	case <-jobSeen:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for job frame")
	}

	select {
	case id := <-cancelSeen:
		if id != "job-prefill-cancel" {
			t.Fatalf("cancel id = %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for cancel frame after waitFirst timeout")
	}
}

func startCascadeProbeHub(t *testing.T, hub *cascade.Hub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/cascade", hub.ServeHTTP)
	return httptest.NewServer(mux)
}

func dialCascadeProbeHub(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/cascade"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, cascade.AuthorizationHeaders("hub-secret"))
	if err != nil {
		t.Fatalf("dial: %v (status=%v)", err, resp)
	}
	return conn
}

func writeCascadeProbeFrame(t *testing.T, conn *websocket.Conn, frame cascade.Frame) {
	t.Helper()
	data, err := cascade.EncodeFrame(frame)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readCascadeProbeFrame(t *testing.T, conn *websocket.Conn) cascade.Frame {
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

func cascadeProbeStubSpoke(t *testing.T, conn *websocket.Conn, clientRead <-chan struct{}) {
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
			writeCascadeProbeFrame(t, conn, cascade.Frame{Type: cascade.FrameResult, ID: frame.ID, Chunk: "event: message\ndata: {}\n\n"})
			if clientRead != nil {
				select {
				case <-clientRead:
				case <-time.After(2 * time.Second):
					t.Error("timeout waiting for client to read first SSE chunk")
				}
			}
			writeCascadeProbeFrame(t, conn, cascade.Frame{Type: cascade.FrameResult, ID: frame.ID, Final: true})
		case cascade.FramePing:
			writeCascadeProbeFrame(t, conn, cascade.Frame{Type: cascade.FramePong})
		}
	}
}

// Ensure bufio can read first SSE line before final in isolation.
func TestCascadeJobStream_FirstLineBeforeFinal(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("event: message\ndata: {}\n\n"))
		_ = pw.Close()
	}()

	scanner := bufio.NewScanner(pr)
	if !scanner.Scan() {
		t.Fatal("expected first SSE line")
	}
	if !bytes.HasPrefix([]byte(scanner.Text()), []byte("event:")) {
		t.Fatalf("line = %q", scanner.Text())
	}
}
