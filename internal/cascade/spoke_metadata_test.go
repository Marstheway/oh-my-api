package cascade

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// spokeHubStub 是面向 spoke 发布测试的最小 hub 桩：
// 接受注册后收集 spoke 发来的帧，并支持回送 metadata rejection。
type spokeHubStub struct {
	server *httptest.Server
	frames chan Frame

	mu        sync.Mutex
	conn      *websocket.Conn
	connCount int
}

func newSpokeHubStub(t *testing.T) *spokeHubStub {
	t.Helper()
	stub := &spokeHubStub{frames: make(chan Frame, 64)}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		stub.mu.Lock()
		stub.conn = conn
		stub.connCount++
		stub.mu.Unlock()

		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		frame, err := DecodeFrame(data)
		if err != nil || frame.Type != FrameRegister {
			return
		}
		ack, _ := EncodeFrame(Frame{Type: FrameRegisterAck, Provider: "corp-dev"})
		_ = conn.WriteMessage(websocket.TextMessage, ack)

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
			case FrameMetadataSnapshot:
				stub.frames <- frame
			case FramePing:
				payload, _ := EncodeFrame(Frame{Type: FramePong})
				_ = conn.WriteMessage(websocket.TextMessage, payload)
			}
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *spokeHubStub) wsURL(t *testing.T) string {
	t.Helper()
	return "ws" + strings.TrimPrefix(s.server.URL, "http") + "/cascade"
}

func (s *spokeHubStub) reject(id string) {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return
	}
	payload, _ := EncodeFrame(Frame{Type: FrameMetadataReject, ID: id, Message: "rejected"})
	_ = conn.WriteMessage(websocket.TextMessage, payload)
}

func (s *spokeHubStub) closeConn() {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// nextSnapshot 读取下一个 metadata snapshot 帧并解析其模型列表。
func (s *spokeHubStub) nextSnapshot(t *testing.T, timeout time.Duration) []MetadataModel {
	t.Helper()
	select {
	case frame := <-s.frames:
		if frame.Type != FrameMetadataSnapshot {
			t.Fatalf("frame type = %q, want metadata_snapshot", frame.Type)
		}
		var models []MetadataModel
		if err := json.Unmarshal(frame.Body, &models); err != nil {
			t.Fatalf("unmarshal snapshot body: %v", err)
		}
		return models
	case <-time.After(timeout):
		t.Fatal("timeout waiting for metadata snapshot")
		return nil
	}
}

func (s *spokeHubStub) assertNoSnapshot(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case frame := <-s.frames:
		t.Fatalf("unexpected frame %q after dedup", frame.Type)
	case <-time.After(timeout):
	}
}

func TestSpoke_PublishesMetadataAfterRegistration(t *testing.T) {
	stub := newSpokeHubStub(t)
	spoke := NewSpoke(SpokeConfig{
		URL:   stub.wsURL(t),
		Token: "hub-secret",
		MetadataProvider: func() []MetadataModel {
			return []MetadataModel{{Model: "local-gpt", ContextLength: ptrInt(128000)}}
		},
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := spoke.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer spoke.Close()

	models := stub.nextSnapshot(t, time.Second)
	if len(models) != 1 || models[0].Model != "local-gpt" {
		t.Fatalf("snapshot = %+v, want [local-gpt]", models)
	}
	if models[0].ContextLength == nil || *models[0].ContextLength != 128000 {
		t.Fatalf("context_length = %v, want 128000", models[0].ContextLength)
	}
}

func TestSpoke_MetadataDedupAndChangeTrigger(t *testing.T) {
	stub := newSpokeHubStub(t)
	holder := &modelsHolder{models: []MetadataModel{{Model: "local-gpt", ContextLength: ptrInt(128000)}}}
	spoke := NewSpoke(SpokeConfig{
		URL:              stub.wsURL(t),
		Token:            "hub-secret",
		MetadataProvider: holder.get,
	}, nil)
	spoke.testMetadataDelays = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := spoke.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer spoke.Close()

	models := stub.nextSnapshot(t, time.Second)
	if len(models) != 1 || models[0].Model != "local-gpt" {
		t.Fatalf("snapshot = %+v", models)
	}

	// 内容未变化：通知不产生新的发布。
	spoke.NotifyMetadataChanged()
	stub.assertNoSnapshot(t, 100*time.Millisecond)

	// 内容变化：立即发布新快照。
	holder.set([]MetadataModel{{Model: "local-gpt", ContextLength: ptrInt(64000)}, {Model: "new-model"}})
	spoke.NotifyMetadataChanged()
	models = stub.nextSnapshot(t, time.Second)
	if len(models) != 2 || models[0].Model != "local-gpt" {
		t.Fatalf("snapshot = %+v, want updated entries", models)
	}

	// 再次未变化：去重。
	spoke.NotifyMetadataChanged()
	stub.assertNoSnapshot(t, 100*time.Millisecond)
}

func TestSpoke_MetadataRejectRetriesThenWaits(t *testing.T) {
	stub := newSpokeHubStub(t)
	spoke := NewSpoke(SpokeConfig{
		URL:   stub.wsURL(t),
		Token: "hub-secret",
		MetadataProvider: func() []MetadataModel {
			return []MetadataModel{{Model: "local-gpt", ContextLength: ptrInt(128000)}}
		},
	}, nil)
	spoke.testMetadataDelays = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := spoke.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer spoke.Close()

	// 每次收到快照都拒绝；spoke 保持连接并依次按 1s/2s/4s（测试加速）重试三次后耗尽。
	var lastID string
	for i := 0; i < 4; i++ {
		select {
		case frame := <-stub.frames:
			if frame.Type != FrameMetadataSnapshot {
				t.Fatalf("frame type = %q", frame.Type)
			}
			lastID = frame.ID
			stub.reject(frame.ID)
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for snapshot attempt %d", i+1)
		}
	}
	// 重试耗尽：等待下一次触发，不再发送。
	select {
	case frame := <-stub.frames:
		t.Fatalf("unexpected snapshot %q after retries exhausted", frame.ID)
	case <-time.After(150 * time.Millisecond):
	}

	// 新的变化触发恢复发布。
	spoke.NotifyMetadataChanged()
	select {
	case frame := <-stub.frames:
		if frame.Type != FrameMetadataSnapshot {
			t.Fatalf("frame type = %q", frame.Type)
		}
		_ = frame.ID
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for snapshot after new trigger")
	}
	_ = lastID
}

func TestSpoke_ReconnectRepublishesFullSnapshot(t *testing.T) {
	stub := newSpokeHubStub(t)
	spoke := NewSpoke(SpokeConfig{
		URL:   stub.wsURL(t),
		Token: "hub-secret",
		MetadataProvider: func() []MetadataModel {
			return []MetadataModel{{Model: "local-gpt", ContextLength: ptrInt(128000)}}
		},
	}, nil)
	spoke.testBackoff = func(time.Duration) {}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := spoke.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer spoke.Close()
	stub.nextSnapshot(t, time.Second)

	// 断开当前连接；重新 Connect 后对新 session 必重新发送完整快照。
	stub.closeConn()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !spoke.Registered() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := spoke.Connect(ctx); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	models := stub.nextSnapshot(t, time.Second)
	if len(models) != 1 || models[0].Model != "local-gpt" {
		t.Fatalf("snapshot after reconnect = %+v", models)
	}
}

// TestSpoke_MetadataWriteFailureClosesAndReconnects 验证：连接级写失败（即使读取侧仍存活）
// 不参与同连接重试，而是幂等关闭当前 WebSocket，使 read loop 清理并触发 Spoke.Run 重连，
// 重连后的新 session 重新发送完整快照。
func TestSpoke_MetadataWriteFailureClosesAndReconnects(t *testing.T) {
	stub := newSpokeHubStub(t)
	holder := &modelsHolder{models: []MetadataModel{{Model: "local-gpt", ContextLength: ptrInt(128000)}}}

	// 仅第二次快照写入注入写失败；重连后的第三次写入恢复正常。
	writes := 0
	spoke := NewSpoke(SpokeConfig{
		URL:              stub.wsURL(t),
		Token:            "hub-secret",
		MetadataProvider: holder.get,
	}, nil)
	spoke.testBackoff = func(time.Duration) {}
	spoke.testMetadataDelays = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	spoke.testMetadataWriteHook = func() error {
		writes++
		if writes == 2 {
			return errors.New("injected metadata write failure")
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		_ = spoke.Run(ctx)
	}()
	defer spoke.Close()

	// 首次注册：完整快照送达。
	models := stub.nextSnapshot(t, 2*time.Second)
	if len(models) != 1 || models[0].Model != "local-gpt" {
		t.Fatalf("initial snapshot = %+v", models)
	}

	// 内容变化触发发布；第二次写失败 → publisher 幂等关闭连接 → 重连重发完整快照。
	holder.set([]MetadataModel{{Model: "local-gpt", ContextLength: ptrInt(64000)}, {Model: "new-model"}})
	spoke.NotifyMetadataChanged()

	models = stub.nextSnapshot(t, 3*time.Second)
	if len(models) != 2 || models[0].Model != "local-gpt" {
		t.Fatalf("snapshot after reconnect = %+v, want updated entries", models)
	}

	// 必须确实发生了连接关闭与重连（而非同连接重试）。
	deadline := time.Now().Add(2 * time.Second)
	reconnected := false
	for time.Now().Before(deadline) {
		stub.mu.Lock()
		c := stub.connCount
		stub.mu.Unlock()
		if c >= 2 {
			reconnected = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !reconnected {
		t.Fatal("write failure must close the connection and reconnect, not retry on the same connection")
	}
}

func TestSpoke_MetadataNotifyBeforeConnectIsDropped(t *testing.T) {
	stub := newSpokeHubStub(t)
	holder := &modelsHolder{models: []MetadataModel{{Model: "a"}}}
	spoke := NewSpoke(SpokeConfig{
		URL:              stub.wsURL(t),
		Token:            "hub-secret",
		MetadataProvider: holder.get,
	}, nil)

	// 未连接时通知不缓存；连接后使用当时完整快照。
	spoke.NotifyMetadataChanged()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := spoke.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer spoke.Close()

	models := stub.nextSnapshot(t, time.Second)
	if len(models) != 1 || models[0].Model != "a" {
		t.Fatalf("snapshot = %+v, want [a] from current state", models)
	}
}

// modelsHolder 供测试并发更新 MetadataProvider 返回的快照（publisher 在后台 goroutine 读取）。
type modelsHolder struct {
	mu     sync.Mutex
	models []MetadataModel
}

func (h *modelsHolder) get() []MetadataModel {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.models
}

func (h *modelsHolder) set(models []MetadataModel) {
	h.mu.Lock()
	h.models = models
	h.mu.Unlock()
}
