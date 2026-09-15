package cascade

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/config"
)

const HopHeader = "X-Oh-My-API-Cascade-Hop"

type FrameType string

const (
	FrameRegister         FrameType = "register"
	FrameRegisterAck      FrameType = "register_ack"
	FramePing             FrameType = "ping"
	FramePong             FrameType = "pong"
	FrameJob              FrameType = "job"
	FrameResult           FrameType = "result"
	FrameError            FrameType = "error"
	FrameCancel           FrameType = "cancel"
	FrameMetadataSnapshot FrameType = "metadata_snapshot"
	FrameMetadataReject   FrameType = "metadata_reject"
)

// 协议资源上限：metadata snapshot 最多 MaxMetadataSnapshotEntries 条、模型名最多
// MaxMetadataModelNameBytes UTF-8 字节。上限常量由 internal/config 定义——
// Spoke 配置校验（public/hidden 可调用入口集合）与 metadata frame 全帧语义校验
// 共用同一来源。不得设置 Cascade 连接级 read limit：它同时限制既有 job/result/cancel
// frame，本轮保持其既有大小语义。
const (
	MaxMetadataSnapshotEntries = config.MaxMetadataSnapshotEntries
	MaxMetadataModelNameBytes  = config.MaxMetadataModelNameBytes
)

// MetadataModel 是 Spoke→Hub metadata snapshot 的单条模型元数据。
// Model 为 Spoke 可外部直调的 public/hidden 入口名；ContextLength 可选，
// 省略表示该入口尚未获知有效上下文长度。
type MetadataModel struct {
	Model         string `json:"model"`
	ContextLength *int   `json:"context_length,omitempty"`
}

// Frame is the JSON application protocol exchanged over the cascade WebSocket.
type Frame struct {
	Type     FrameType       `json:"type"`
	Token    string          `json:"token,omitempty"`
	Provider string          `json:"provider,omitempty"`
	ID       string          `json:"id,omitempty"`
	Protocol string          `json:"protocol,omitempty"`
	Model    string          `json:"model,omitempty"`
	Stream   *bool           `json:"stream,omitempty"`
	Body     json.RawMessage `json:"body,omitempty"`
	Hop      string          `json:"hop,omitempty"`
	Chunk    string          `json:"chunk,omitempty"`
	Final    bool            `json:"final,omitempty"`
	Status   int             `json:"status,omitempty"`
	Message  string          `json:"message,omitempty"`
}

func EncodeFrame(frame Frame) ([]byte, error) {
	if err := validateFrameType(frame.Type); err != nil {
		return nil, err
	}
	return json.Marshal(frame)
}

func DecodeFrame(data []byte) (Frame, error) {
	var frame Frame
	if err := json.Unmarshal(data, &frame); err != nil {
		return Frame{}, fmt.Errorf("decode cascade frame: %w", err)
	}
	if err := validateFrameType(frame.Type); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

func validateFrameType(frameType FrameType) error {
	if frameType == "" {
		return fmt.Errorf("cascade frame type must not be empty")
	}
	switch frameType {
	case FrameRegister, FrameRegisterAck, FramePing, FramePong, FrameJob, FrameResult, FrameError, FrameCancel, FrameMetadataSnapshot, FrameMetadataReject:
		return nil
	default:
		return fmt.Errorf("unknown cascade frame type %q", frameType)
	}
}

func normalizeToken(token string) string {
	return strings.TrimSpace(token)
}

func tokensEqual(got, want string) bool {
	got = normalizeToken(got)
	want = normalizeToken(want)
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
