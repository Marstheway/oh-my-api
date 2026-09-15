package cascade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/gorilla/websocket"
)

const (
	defaultRegisterTimeout = 10 * time.Second
	defaultHeartbeatPeriod = 30 * time.Second
	defaultWriteWait       = 10 * time.Second
)

// HubConfig configures the hub-side cascade session acceptor.
type HubConfig struct {
	ProviderName    string
	Token           string
	RegisterTimeout time.Duration
	HeartbeatPeriod time.Duration
}

// Hub accepts spoke WebSocket connections and maintains the active session.
type Hub struct {
	cfg      HubConfig
	upgrader websocket.Upgrader
	health   *health.Checker

	mu      sync.RWMutex
	session *Session
}

func NewHub(cfg HubConfig) *Hub {
	if cfg.RegisterTimeout <= 0 {
		cfg.RegisterTimeout = defaultRegisterTimeout
	}
	if cfg.HeartbeatPeriod <= 0 {
		cfg.HeartbeatPeriod = defaultHeartbeatPeriod
	}
	return &Hub{
		cfg: cfg,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func (h *Hub) Configured() bool {
	return strings.TrimSpace(h.cfg.ProviderName) != "" && strings.TrimSpace(h.cfg.Token) != ""
}

func (h *Hub) ProviderName() string {
	return h.cfg.ProviderName
}

func (h *Hub) Session() (*Session, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.session == nil {
		return nil, false
	}
	return h.session, true
}

// ContextLength 查询当前活跃 session 元数据快照中 model 的 context_length。
// provider 必须匹配当前 hub 的 provider 名；无 session、无快照或未携带有效
// 上下文长度时返回 miss。无副作用，仅作只读补充来源。
func (h *Hub) ContextLength(provider, model string) (int, bool) {
	if provider != h.cfg.ProviderName {
		return 0, false
	}
	session, ok := h.Session()
	if !ok {
		return 0, false
	}
	return session.MetadataContextLength(model)
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.Configured() {
		http.Error(w, "cascade hub not configured", http.StatusServiceUnavailable)
		return
	}
	if !tokensEqual(requestBearerToken(r), h.cfg.Token) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Debug("cascade websocket upgrade failed", "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.RegisterTimeout)
	defer cancel()

	session, err := h.handleRegister(ctx, conn)
	if err != nil {
		_ = writeErrorAndClose(conn, err.Error())
		return
	}

	go h.runSession(session)
}

func (h *Hub) handleRegister(ctx context.Context, conn *websocket.Conn) (*Session, error) {
	if err := conn.SetReadDeadline(time.Now().Add(h.cfg.RegisterTimeout)); err != nil {
		return nil, err
	}

	_, data, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read register frame: %w", err)
	}

	frame, err := DecodeFrame(data)
	if err != nil {
		return nil, err
	}
	if frame.Type != FrameRegister {
		return nil, fmt.Errorf("first frame must be register, got %q", frame.Type)
	}
	if !tokensEqual(frame.Token, h.cfg.Token) {
		return nil, fmt.Errorf("invalid token")
	}

	ack, err := EncodeFrame(Frame{
		Type:     FrameRegisterAck,
		Provider: h.cfg.ProviderName,
	})
	if err != nil {
		return nil, err
	}
	if err := conn.WriteMessage(websocket.TextMessage, ack); err != nil {
		return nil, err
	}

	_ = conn.SetReadDeadline(time.Time{})

	session := newSession(h.cfg.ProviderName, conn)
	h.replaceSession(session)

	select {
	case <-ctx.Done():
		h.clearSession(session)
		return nil, ctx.Err()
	default:
	}

	return session, nil
}

func (h *Hub) SetHealthChecker(checker *health.Checker) {
	h.mu.Lock()
	h.health = checker
	session := h.session
	provider := h.cfg.ProviderName
	configured := h.Configured()
	h.mu.Unlock()
	if checker == nil || !configured {
		return
	}
	if session == nil {
		ReportSessionUnhealthy(checker, provider)
	} else {
		ReportSessionHealthy(checker, provider)
	}
}

// Close tears down the active spoke session, fails in-flight jobs, and pins health unhealthy.
func (h *Hub) Close() {
	h.mu.Lock()
	session := h.session
	h.session = nil
	checker := h.health
	provider := h.cfg.ProviderName
	h.mu.Unlock()

	if session != nil {
		session.closeWithReason("hub closed")
	}
	if checker != nil && provider != "" {
		ReportSessionUnhealthy(checker, provider)
	}
}

func (h *Hub) replaceSession(next *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.session != nil {
		h.session.closeWithReason("replaced by new connection")
	}
	h.session = next
	if h.health != nil {
		ReportSessionHealthy(h.health, h.cfg.ProviderName)
	}
}

func (h *Hub) clearSession(session *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.session == session {
		h.session = nil
		if h.health != nil {
			ReportSessionUnhealthy(h.health, h.cfg.ProviderName)
		}
	}
}

func (h *Hub) runSession(session *Session) {
	defer h.clearSession(session)
	defer session.closeWithReason("session ended")

	conn := session.Conn()
	stopHeartbeat := make(chan struct{})
	defer close(stopHeartbeat)

	go func() {
		ticker := time.NewTicker(h.cfg.HeartbeatPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-session.Done():
				return
			case <-ticker.C:
				if err := session.WriteFrame(Frame{Type: FramePing}); err != nil {
					return
				}
			}
		}
	}()

	for {
		select {
		case <-session.Done():
			return
		default:
		}

		if err := conn.SetReadDeadline(time.Now().Add(h.cfg.HeartbeatPeriod * 2)); err != nil {
			return
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		frame, err := DecodeFrame(data)
		if err != nil {
			_ = session.WriteFrame(Frame{Type: FrameError, Message: err.Error()})
			continue
		}

		switch frame.Type {
		case FramePong:
			continue
		case FramePing:
			if err := session.WriteFrame(Frame{Type: FramePong}); err != nil {
				return
			}
		case FrameCancel:
			continue
		case FrameResult:
			session.deliverResult(frame)
		case FrameError:
			if frame.ID != "" {
				session.deliverError(frame.ID, errors.New(frame.Message))
			}
		case FrameMetadataSnapshot:
			models, merr := parseMetadataSnapshot(frame)
			if merr != nil {
				// 非致命：回送同 ID 的 metadata rejection，保留上一份有效快照，
				// 不关闭会话（元数据语义错误不视为传输层故障）。
				if werr := session.WriteFrame(Frame{Type: FrameMetadataReject, ID: frame.ID, Message: merr.Error()}); werr != nil {
					return
				}
				continue
			}
			session.ReplaceMetadata(models)
		default:
			_ = session.WriteFrame(Frame{Type: FrameError, Message: fmt.Sprintf("unexpected frame type %q from spoke", frame.Type)})
		}
	}
}

func writeErrorAndClose(conn *websocket.Conn, message string) error {
	payload, err := EncodeFrame(Frame{Type: FrameError, Message: message})
	if err != nil {
		_ = conn.Close()
		return err
	}
	_ = conn.WriteMessage(websocket.TextMessage, payload)
	return conn.Close()
}
