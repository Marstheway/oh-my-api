package cascade

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func ptrInt(v int) *int { return &v }

// waitHubContext 轮询等待 hub 会话元数据生效（Hub 异步处理帧）。
func waitHubContext(t *testing.T, hub *Hub, provider, model string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := hub.ContextLength(provider, model); ok && v == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	v, ok := hub.ContextLength(provider, model)
	t.Fatalf("corp-dev/%s = %d, ok=%v; want %d", model, v, ok, want)
}

// waitHubContextMiss 轮询等待 hub 会话元数据 miss（断开/清空/替换后）。
func waitHubContextMiss(t *testing.T, hub *Hub, provider, model string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := hub.ContextLength(provider, model); !ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	v, ok := hub.ContextLength(provider, model)
	t.Fatalf("%s/%s = %d, ok=%v; want miss", provider, model, v, ok)
}

func metadataSnapshotFrame(id string, models []MetadataModel) Frame {
	body, err := json.Marshal(models)
	if err != nil {
		panic(err)
	}
	return Frame{Type: FrameMetadataSnapshot, ID: id, Body: body}
}

func TestParseMetadataSnapshot_Valid(t *testing.T) {
	frame := metadataSnapshotFrame("snap-1", []MetadataModel{
		{Model: "local-gpt", ContextLength: ptrInt(128000)},
		{Model: "no-ctx"},
	})
	got, err := parseMetadataSnapshot(frame)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2", len(got))
	}
	if got["local-gpt"] != 128000 {
		t.Errorf("local-gpt = %d, want 128000", got["local-gpt"])
	}
	if got["no-ctx"] != 0 {
		t.Errorf("no-ctx = %d, want 0 (present without context)", got["no-ctx"])
	}
}

func TestParseMetadataSnapshot_EmptyIsLegal(t *testing.T) {
	frame := metadataSnapshotFrame("snap-empty", nil)
	got, err := parseMetadataSnapshot(frame)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("entries = %d, want 0 (empty snapshot clears)", len(got))
	}
}

func TestParseMetadataSnapshot_RejectsInvalid(t *testing.T) {
	tests := []struct {
		name   string
		models []MetadataModel
	}{
		{name: "empty model name", models: []MetadataModel{{Model: ""}}},
		{name: "duplicate model", models: []MetadataModel{{Model: "a"}, {Model: "a"}}},
		{name: "zero context", models: []MetadataModel{{Model: "a", ContextLength: ptrInt(0)}}},
		{name: "negative context", models: []MetadataModel{{Model: "a", ContextLength: ptrInt(-5)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseMetadataSnapshot(metadataSnapshotFrame("snap-x", tt.models))
			if err == nil {
				t.Fatalf("expected rejection for %q", tt.name)
			}
		})
	}
}

func TestParseMetadataSnapshot_RejectsOverLimit(t *testing.T) {
	long := strings.Repeat("a", MaxMetadataModelNameBytes+1)
	_, err := parseMetadataSnapshot(metadataSnapshotFrame("snap-x", []MetadataModel{{Model: long}}))
	if err == nil {
		t.Fatal("expected rejection for oversized model name")
	}

	models := make([]MetadataModel, MaxMetadataSnapshotEntries+1)
	for i := range models {
		models[i] = MetadataModel{Model: strings.Repeat("x", 8) + string(rune('a'+i%26)) + string(rune('0'+i%10))}
	}
	_, err = parseMetadataSnapshot(metadataSnapshotFrame("snap-x", models))
	if err == nil {
		t.Fatal("expected rejection for over-limit snapshot")
	}
}

func TestParseMetadataSnapshot_RejectsEmptyID(t *testing.T) {
	frame := metadataSnapshotFrame("", []MetadataModel{{Model: "a"}})
	_, err := parseMetadataSnapshot(frame)
	if err == nil {
		t.Fatal("expected rejection for empty snapshot id")
	}
}

func TestParseMetadataSnapshot_RejectsMalformedBody(t *testing.T) {
	frame := Frame{Type: FrameMetadataSnapshot, ID: "snap-x", Body: json.RawMessage(`{invalid`)}
	_, err := parseMetadataSnapshot(frame)
	if err == nil {
		t.Fatal("expected rejection for malformed body")
	}
}

func TestEncodeDecodeMetadataFrames(t *testing.T) {
	body, err := json.Marshal([]MetadataModel{{Model: "local-gpt", ContextLength: ptrInt(128000)}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	original := Frame{Type: FrameMetadataSnapshot, ID: "snap-1", Body: body}
	data, err := EncodeFrame(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Type != FrameMetadataSnapshot || decoded.ID != "snap-1" {
		t.Fatalf("decoded = %+v", decoded)
	}

	reject := Frame{Type: FrameMetadataReject, ID: "snap-1", Message: "bad"}
	data, err = EncodeFrame(reject)
	if err != nil {
		t.Fatalf("encode reject: %v", err)
	}
	decoded, err = DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode reject: %v", err)
	}
	if decoded.Type != FrameMetadataReject || decoded.ID != "snap-1" || decoded.Message != "bad" {
		t.Fatalf("decoded reject = %+v", decoded)
	}
}

// startMetadataHubSession 启动真实 Hub 并注册一个带指定元数据快照的 spoke 连接。
// 返回 spoke 侧 client conn（后续可用 writeFrame 继续向 hub 写入帧）。
func startMetadataHubSession(t *testing.T, hub *Hub, models []MetadataModel) *websocket.Conn {
	t.Helper()
	server := startTestHub(t, hub)
	conn := dialTestHub(t, server)
	t.Cleanup(func() { _ = conn.Close() })
	t.Cleanup(server.Close)

	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "hub-secret"})
	ack := readFrame(t, conn)
	if ack.Type != FrameRegisterAck {
		t.Fatalf("ack type = %q", ack.Type)
	}
	writeFrame(t, conn, metadataSnapshotFrame("snap-1", models))

	// Hub 异步处理快照：等待元数据生效后再返回。
	if len(models) > 0 {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, ok := hub.ContextLength("corp-dev", models[0].Model); ok {
				return conn
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("metadata snapshot for %q not applied by hub", models[0].Model)
	}
	return conn
}

func TestHub_MetadataSnapshotLookup(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	startMetadataHubSession(t, hub, []MetadataModel{
		{Model: "local-gpt", ContextLength: ptrInt(128000)},
		{Model: "no-ctx"},
	})

	waitHubContext(t, hub, "corp-dev", "local-gpt", 128000)
	if _, ok := hub.ContextLength("corp-dev", "no-ctx"); ok {
		t.Fatal("no-ctx must miss (no context_length)")
	}
	if _, ok := hub.ContextLength("corp-dev", "missing"); ok {
		t.Fatal("missing model must miss")
	}
	// provider 不匹配当前 hub 时一律 miss。
	if _, ok := hub.ContextLength("other-provider", "local-gpt"); ok {
		t.Fatal("non-matching provider must miss")
	}
}

func TestHub_InvalidSnapshotKeepsPreviousAndRejects(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startTestHub(t, hub)
	defer server.Close()

	conn := dialTestHub(t, server)
	defer conn.Close()
	writeFrame(t, conn, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn)

	// 第一份有效快照
	writeFrame(t, conn, metadataSnapshotFrame("snap-good", []MetadataModel{{Model: "local-gpt", ContextLength: ptrInt(128000)}}))
	waitHubContext(t, hub, "corp-dev", "local-gpt", 128000)

	// 非法快照：整帧拒绝，保留上一份有效快照，并回送同 ID rejection。
	writeFrame(t, conn, metadataSnapshotFrame("snap-bad", []MetadataModel{{Model: "dup"}, {Model: "dup"}}))
	reject := readFrame(t, conn)
	if reject.Type != FrameMetadataReject {
		t.Fatalf("frame type = %q, want metadata_reject", reject.Type)
	}
	if reject.ID != "snap-bad" {
		t.Fatalf("reject id = %q, want snap-bad", reject.ID)
	}
	if v, ok := hub.ContextLength("corp-dev", "local-gpt"); !ok || v != 128000 {
		t.Fatalf("previous snapshot must be kept: %d, %v", v, ok)
	}
}

func TestHub_EmptySnapshotClears(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	conn := startMetadataHubSession(t, hub, []MetadataModel{{Model: "local-gpt", ContextLength: ptrInt(128000)}})
	waitHubContext(t, hub, "corp-dev", "local-gpt", 128000)

	// 空快照是唯一合法清空方式。
	writeFrame(t, conn, metadataSnapshotFrame("snap-empty", nil))
	waitHubContextMiss(t, hub, "corp-dev", "local-gpt")
}

func TestHub_MetadataClearedOnSessionReplacementAndClose(t *testing.T) {
	hub := NewHub(HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	server := startTestHub(t, hub)
	defer server.Close()

	conn1 := dialTestHub(t, server)
	writeFrame(t, conn1, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn1)
	writeFrame(t, conn1, metadataSnapshotFrame("snap-1", []MetadataModel{{Model: "local-gpt", ContextLength: ptrInt(128000)}}))
	waitHubContext(t, hub, "corp-dev", "local-gpt", 128000)

	// 新 session 替换旧 session：旧 session 元数据不得保留。
	conn2 := dialTestHub(t, server)
	writeFrame(t, conn2, Frame{Type: FrameRegister, Token: "hub-secret"})
	readFrame(t, conn2)
	waitHubContextMiss(t, hub, "corp-dev", "local-gpt")
	writeFrame(t, conn2, metadataSnapshotFrame("snap-2", []MetadataModel{{Model: "local-gpt", ContextLength: ptrInt(64000)}}))
	waitHubContext(t, hub, "corp-dev", "local-gpt", 64000)

	// 关闭 session：元数据随之清除。
	conn2.Close()
	waitHubContextMiss(t, hub, "corp-dev", "local-gpt")
}
