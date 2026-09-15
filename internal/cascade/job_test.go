package cascade

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/gorilla/websocket"
)

func TestExecuteJob_Stream4xxReturnsJSONNotSSE(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startTestHub(t, hub)
	defer server.Close()

	conn := dialTestHub(t, server)
	defer conn.Close()
	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn)

	stubCtx, stubCancel := context.WithCancel(context.Background())
	stubDone := make(chan struct{})
	go func() {
		defer close(stubDone)
		stubSpokeStreamErrorLoop(stubCtx, t, conn)
	}()
	defer func() {
		stubCancel()
		<-stubDone
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := hub.ExecuteJob(ctx, JobRequest{
		ID:       "job-stream-err",
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
		t.Fatalf("content-type = %q, want non-SSE JSON error", resp.Header.Get("Content-Type"))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "model overloaded") {
		t.Fatalf("body = %s", body)
	}
}

func stubSpokeStreamErrorLoop(ctx context.Context, t *testing.T, conn *websocket.Conn) {
	t.Helper()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				continue
			}
			return
		}
		frame, err := DecodeFrame(data)
		if err != nil {
			continue
		}
		switch frame.Type {
		case FrameJob:
			errBody := `{"error":{"message":"model overloaded","type":"server_error"}}`
			writeFrame(t, conn, Frame{
				Type:   FrameResult,
				ID:     frame.ID,
				Chunk:  errBody,
				Final:  true,
				Status: http.StatusBadGateway,
			})
		case FramePing:
			writeFrame(t, conn, Frame{Type: FramePong})
		}
	}
}

func TestExecuteJob_StreamSSEBeforeFinal(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startTestHub(t, hub)
	defer server.Close()

	conn := dialTestHub(t, server)
	defer conn.Close()
	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn)

	jobReceived := make(chan Frame, 1)
	clientRead := make(chan struct{})
	stubCtx, stubCancel := context.WithCancel(context.Background())
	stubDone := make(chan struct{})
	go func() {
		defer close(stubDone)
		stubSpokeJobLoop(stubCtx, t, conn, jobReceived, clientRead)
	}()
	defer func() {
		stubCancel()
		<-stubDone
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := hub.ExecuteJob(ctx, JobRequest{
		ID:       "job-stream-1",
		Protocol: "openai.chat",
		Model:    "corp-dev/gpt-4o",
		Stream:   true,
		Body:     []byte(`{"model":"corp-dev/gpt-4o","stream":true,"messages":[]}`),
	})
	if err != nil {
		t.Fatalf("ExecuteJob: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", resp.Header.Get("Content-Type"))
	}

	go func() {
		scanner := bufio.NewScanner(resp.Body)
		if scanner.Scan() {
			close(clientRead)
		}
	}()

	select {
	case <-clientRead:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first SSE line before result-end")
	}

	select {
	case job := <-jobReceived:
		if job.ID != "job-stream-1" {
			t.Fatalf("job id = %q", job.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for job frame on spoke")
	}
}

func TestExecuteJob_ContextCancelSendsCancelFrame(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startTestHub(t, hub)
	defer server.Close()

	conn := dialTestHub(t, server)
	defer conn.Close()
	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn)

	cancelSeen := make(chan string, 1)
	jobSeen := make(chan struct{}, 1)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			frame, err := DecodeFrame(data)
			if err != nil {
				continue
			}
			switch frame.Type {
			case FrameJob:
				jobSeen <- struct{}{}
			case FrameCancel:
				cancelSeen <- frame.ID
			case FramePing:
				writeFrame(t, conn, Frame{Type: FramePong})
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var jobResp *http.Response
	go func() {
		defer close(done)
		resp, err := hub.ExecuteJob(ctx, JobRequest{
			ID:       "job-cancel-me",
			Protocol: "openai.chat",
			Model:    "corp-dev/gpt-4o",
			Stream:   true,
			Body:     []byte(`{"model":"corp-dev/gpt-4o","stream":true,"messages":[]}`),
		})
		if err == nil {
			jobResp = resp
		}
	}()

	select {
	case <-jobSeen:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for job frame")
	}
	cancel()

	select {
	case id := <-cancelSeen:
		if id != "job-cancel-me" {
			t.Fatalf("cancel id = %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for cancel frame")
	}
	if jobResp != nil && jobResp.Body != nil {
		jobResp.Body.Close()
	}
	<-done
}

func TestExecuteJob_NonStreamJSON(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startTestHub(t, hub)
	defer server.Close()

	conn := dialTestHub(t, server)
	defer conn.Close()
	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn)

	stubCtx, stubCancel := context.WithCancel(context.Background())
	stubDone := make(chan struct{})
	go func() {
		defer close(stubDone)
		stubSpokeJobLoop(stubCtx, t, conn, nil, nil)
	}()
	defer func() {
		stubCancel()
		<-stubDone
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := hub.ExecuteJob(ctx, JobRequest{
		ID:       "job-json-1",
		Protocol: "openai.chat",
		Model:    "corp-dev/gpt-4o",
		Stream:   false,
		Body:     []byte(`{"model":"corp-dev/gpt-4o","messages":[]}`),
	})
	if err != nil {
		t.Fatalf("ExecuteJob: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), `"object":"chat.completion"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestHubDisconnect_PinsThreeProtocolKeysUnhealthy(t *testing.T) {
	const shortCooldown = 50 * time.Millisecond
	checker := health.NewChecker(3, shortCooldown)
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	hub.SetHealthChecker(checker)

	server := startTestHub(t, hub)
	defer server.Close()

	conn := dialTestHub(t, server)
	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn)

	for _, proto := range CascadeOutboundProtocols {
		key := health.MakeHealthKey("corp-dev", proto)
		if !checker.IsHealthy(key) {
			t.Fatalf("expected healthy %q after connect", key)
		}
	}

	conn.Close()
	time.Sleep(100 * time.Millisecond)

	for _, proto := range CascadeOutboundProtocols {
		key := health.MakeHealthKey("corp-dev", proto)
		if checker.IsHealthy(key) {
			t.Fatalf("expected unhealthy %q after disconnect", key)
		}
	}

	// Wait past default checker cooldown while still disconnected; pinned keys must stay unhealthy.
	time.Sleep(shortCooldown + 50*time.Millisecond)
	for _, proto := range CascadeOutboundProtocols {
		key := health.MakeHealthKey("corp-dev", proto)
		if checker.IsHealthy(key) {
			t.Fatalf("expected still unhealthy %q after default cooldown elapsed", key)
		}
	}

	conn2 := dialTestHub(t, server)
	writeFrame(t, conn2, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn2)

	for _, proto := range CascadeOutboundProtocols {
		key := health.MakeHealthKey("corp-dev", proto)
		if !checker.IsHealthy(key) {
			t.Fatalf("expected healthy %q after reconnect before closing session", key)
		}
	}
	conn2.Close()
}

func TestHubClose_FailsJobsAndPinsHealth(t *testing.T) {
	checker := health.NewChecker(3, 30*time.Second)
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	hub.SetHealthChecker(checker)

	server := startTestHub(t, hub)
	defer server.Close()

	conn := dialTestHub(t, server)
	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn)

	for _, proto := range CascadeOutboundProtocols {
		key := health.MakeHealthKey("corp-dev", proto)
		if !checker.IsHealthy(key) {
			t.Fatalf("expected healthy %q after connect", key)
		}
	}

	hub.Close()

	for _, proto := range CascadeOutboundProtocols {
		key := health.MakeHealthKey("corp-dev", proto)
		if checker.IsHealthy(key) {
			t.Fatalf("expected unhealthy %q after Hub.Close", key)
		}
	}
}

func TestSetHealthChecker_WithSessionReportsHealthy(t *testing.T) {
	checker := health.NewChecker(3, 30*time.Second)
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})

	server := startTestHub(t, hub)
	defer server.Close()

	conn := dialTestHub(t, server)
	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn)

	hub.SetHealthChecker(checker)

	for _, proto := range CascadeOutboundProtocols {
		key := health.MakeHealthKey("corp-dev", proto)
		if !checker.IsHealthy(key) {
			t.Fatalf("expected healthy %q when session active", key)
		}
	}
	conn.Close()
}

func TestExecuteJob_CanceledContextBeforeSendJob(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startTestHub(t, hub)
	defer server.Close()

	conn := dialTestHub(t, server)
	defer conn.Close()
	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := hub.ExecuteJob(ctx, JobRequest{
		ID:       "job-dead-ctx",
		Protocol: "openai.chat",
		Model:    "corp-dev/gpt-4o",
		Stream:   true,
		Body:     []byte(`{"model":"corp-dev/gpt-4o","stream":true,"messages":[]}`),
	})
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestSetHealthChecker_PinsUnhealthyBeforeSession(t *testing.T) {
	checker := health.NewChecker(3, 30*time.Second)
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	hub.SetHealthChecker(checker)

	for _, proto := range CascadeOutboundProtocols {
		key := health.MakeHealthKey("corp-dev", proto)
		if checker.IsHealthy(key) {
			t.Fatalf("expected unhealthy %q before any session", key)
		}
	}
}

func stubSpokeJobLoop(ctx context.Context, t *testing.T, conn *websocket.Conn, jobReceived chan Frame, clientRead <-chan struct{}) {
	t.Helper()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				continue
			}
			return
		}
		frame, err := DecodeFrame(data)
		if err != nil {
			continue
		}
		switch frame.Type {
		case FrameJob:
			if jobReceived != nil {
				select {
				case jobReceived <- frame:
				default:
				}
			}
			if frame.Stream != nil && *frame.Stream {
				writeFrame(t, conn, Frame{Type: FrameResult, ID: frame.ID, Chunk: "event: message\ndata: {\"x\":1}\n\n"})
				if clientRead != nil {
					select {
					case <-clientRead:
					case <-ctx.Done():
						return
					case <-time.After(2 * time.Second):
						if ctx.Err() != nil {
							return
						}
						t.Error("timeout waiting for client to read first SSE chunk")
					}
				}
				if ctx.Err() != nil {
					return
				}
				writeFrame(t, conn, Frame{Type: FrameResult, ID: frame.ID, Final: true})
			} else {
				writeFrame(t, conn, Frame{Type: FrameResult, ID: frame.ID, Chunk: `{"id":"1","object":"chat.completion","choices":[]}`, Final: true})
			}
		case FramePing:
			writeFrame(t, conn, Frame{Type: FramePong})
		}
	}
}
