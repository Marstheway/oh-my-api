package cascade

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultSpokeInitialBackoff  = time.Second
	defaultSpokeMaxBackoff      = 30 * time.Second
	defaultSpokeHeartbeatPeriod = 30 * time.Second
)

// SpokeConfig configures the spoke-side outbound cascade connector.
type SpokeConfig struct {
	URL             string
	Token           string
	HeartbeatPeriod time.Duration
	// MetadataProvider 返回当前完整元数据快照（public/hidden 入口及可选 context_length）。
	// 注册成功后以及本地元数据变化时由 publisher 调用；返回 nil 表示不发布。
	MetadataProvider func() []MetadataModel
}

// Spoke maintains an outbound WebSocket to the hub.
type Spoke struct {
	cfg     SpokeConfig
	handler JobHandler

	mu           sync.Mutex
	conn         *websocket.Conn
	registered   bool
	disconnected chan struct{}
	jobs         *spokeJobRegistry

	// metadataNotify 触发一次元数据重算/发布尝试（非阻塞；未连接时丢弃）。
	metadataNotify chan struct{}
	// metadataRejectCh 收到与最近发送快照同 ID 的 metadata rejection 时触发重试。
	metadataRejectCh chan struct{}
	// metadataRejectMu 保护 lastMetadataID（publisher 写入，readLoop 读取）。
	metadataRejectMu sync.Mutex
	lastMetadataID   string

	testDial           func(context.Context) error
	testBackoff        func(time.Duration)
	testMetadataDelays []time.Duration
	testMetadataNotify func() // 测试观察点：外部触发到达 publisher
	// testMetadataWriteHook 测试钩子：非 nil 时在 metadata snapshot 连接写入前调用，
	// 返回错误则模拟本次写失败（用于验证写失败 → 幂等关闭 → 重连/重发路径）。
	testMetadataWriteHook func() error
}

func NewSpoke(cfg SpokeConfig, handler JobHandler) *Spoke {
	return &Spoke{
		cfg:              cfg,
		handler:          handler,
		metadataNotify:   make(chan struct{}, 1),
		metadataRejectCh: make(chan struct{}, 1),
	}
}

// NotifyMetadataChanged 通知当前连接的 publisher 重算并去重发布元数据快照。
// 未连接时不缓存待发送增量；下次注册使用当时的完整快照。
func (s *Spoke) NotifyMetadataChanged() {
	if s.testMetadataNotify != nil {
		s.testMetadataNotify()
	}
	select {
	case s.metadataNotify <- struct{}{}:
	default:
	}
}

func (s *Spoke) Registered() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registered
}

func (s *Spoke) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registered = false
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

func (s *Spoke) heartbeatPeriod() time.Duration {
	if s.cfg.HeartbeatPeriod > 0 {
		return s.cfg.HeartbeatPeriod
	}
	return defaultSpokeHeartbeatPeriod
}

func (s *Spoke) Connect(ctx context.Context) error {
	if s.testDial != nil {
		return s.testDial(ctx)
	}

	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, s.cfg.URL, AuthorizationHeaders(s.cfg.Token))
	if err != nil {
		return fmt.Errorf("dial cascade hub: %w", err)
	}

	if err := s.register(conn); err != nil {
		_ = conn.Close()
		return err
	}

	disconnected := make(chan struct{})
	s.mu.Lock()
	s.conn = conn
	s.registered = true
	s.disconnected = disconnected
	s.mu.Unlock()

	// 共享连接写锁：job result / pong / cancel 与 metadata snapshot 的写入串行化。
	sc := newSpokeConn(conn)
	go s.readLoop(conn, disconnected, sc)
	go s.runMetadataPublisher(sc, disconnected)
	return nil
}

func (s *Spoke) register(conn *websocket.Conn) error {
	payload, err := EncodeFrame(Frame{Type: FrameRegister, Token: s.cfg.Token})
	if err != nil {
		return err
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		return err
	}

	_ = conn.SetReadDeadline(time.Now().Add(defaultRegisterTimeout))
	_, data, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read register ack: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})

	frame, err := DecodeFrame(data)
	if err != nil {
		return err
	}
	if frame.Type == FrameError {
		return errors.New(frame.Message)
	}
	if frame.Type != FrameRegisterAck {
		return fmt.Errorf("expected register_ack, got %q", frame.Type)
	}
	return nil
}

func (s *Spoke) readLoop(conn *websocket.Conn, disconnected chan struct{}, sc *spokeConn) {
	defer close(disconnected)

	loopCtx, loopCancel := context.WithCancel(context.Background())
	defer loopCancel()

	defer func() {
		if s.jobs != nil {
			s.jobs.cancelAll()
		}
		s.mu.Lock()
		if s.conn == conn {
			s.conn = nil
			s.registered = false
		}
		s.mu.Unlock()
	}()

	readWait := s.heartbeatPeriod() * 2
	for {
		select {
		case <-loopCtx.Done():
			return
		default:
		}

		if err := conn.SetReadDeadline(time.Now().Add(readWait)); err != nil {
			return
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		frame, err := DecodeFrame(data)
		if err != nil {
			continue
		}

		switch frame.Type {
		case FramePing:
			_ = sc.writeFrame(Frame{Type: FramePong})
		case FramePong:
			continue
		case FrameCancel:
			if s.jobs != nil {
				s.jobs.cancel(frame.ID)
			}
		case FrameJob:
			s.handleJobFrame(loopCtx, frame, sc)
		case FrameError:
			return
		case FrameMetadataReject:
			// 非致命：Hub 拒绝了同 ID 快照，保持连接并触发重试发布。
			s.handleMetadataReject(frame.ID)
		default:
			continue
		}
	}
}

// Run dials the hub and reconnects with exponential backoff until ctx is canceled.
func (s *Spoke) Run(ctx context.Context) error {
	backoff := defaultSpokeInitialBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := s.Connect(ctx)
		if err == nil {
			backoff = defaultSpokeInitialBackoff
			if err := s.waitUntilDisconnected(ctx); err != nil {
				return err
			}
			continue
		}

		if err := s.sleep(ctx, backoff); err != nil {
			return err
		}
		if backoff < defaultSpokeMaxBackoff {
			backoff *= 2
			if backoff > defaultSpokeMaxBackoff {
				backoff = defaultSpokeMaxBackoff
			}
		}
	}
}

func (s *Spoke) waitUntilDisconnected(ctx context.Context) error {
	s.mu.Lock()
	ch := s.disconnected
	s.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ch:
		return nil
	}
}

func (s *Spoke) sleep(ctx context.Context, d time.Duration) error {
	if s.testBackoff != nil {
		s.testBackoff(d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
