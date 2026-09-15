package cascade

import (
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Session represents one registered spoke connection on the hub.
type Session struct {
	provider string
	conn     *websocket.Conn

	mu     sync.Mutex
	closed bool
	done   chan struct{}

	jobsMu sync.Mutex
	jobs   map[string]*jobWaiter

	metadataMu sync.RWMutex
	metadata   map[string]int // spoke 模型名 → context_length（未携带长度时为 0）
}

func newSession(provider string, conn *websocket.Conn) *Session {
	return &Session{
		provider: provider,
		conn:     conn,
		done:     make(chan struct{}),
		jobs:     make(map[string]*jobWaiter),
	}
}

func (s *Session) ProviderName() string {
	return s.provider
}

func (s *Session) Conn() *websocket.Conn {
	return s.conn
}

func (s *Session) Done() <-chan struct{} {
	return s.done
}

func (s *Session) beginJob(id string, stream bool) *jobWaiter {
	waiter := newJobWaiter(stream)
	s.jobsMu.Lock()
	s.jobs[id] = waiter
	s.jobsMu.Unlock()
	return waiter
}

func (s *Session) endJob(id string) {
	s.jobsMu.Lock()
	delete(s.jobs, id)
	s.jobsMu.Unlock()
}

func (s *Session) cancelJob(id string) {
	s.jobsMu.Lock()
	waiter, ok := s.jobs[id]
	s.jobsMu.Unlock()
	if ok {
		waiter.fail(contextCanceledError())
	}
}

func (s *Session) deliverResult(frame Frame) {
	s.jobsMu.Lock()
	waiter, ok := s.jobs[frame.ID]
	s.jobsMu.Unlock()
	if !ok {
		return
	}
	waiter.appendChunk(frame.Chunk, frame.Final, frame.Status)
}

func (s *Session) deliverError(id string, err error) {
	if id == "" {
		return
	}
	s.jobsMu.Lock()
	waiter, ok := s.jobs[id]
	s.jobsMu.Unlock()
	if !ok {
		return
	}
	waiter.fail(err)
}

func (s *Session) failAllJobs(err error) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	for id, waiter := range s.jobs {
		waiter.fail(err)
		delete(s.jobs, id)
	}
}

// ReplaceMetadata 原子整体替换当前 session 的元数据快照（注册、重连或更新使用完整快照替换）。
func (s *Session) ReplaceMetadata(models map[string]int) {
	s.metadataMu.Lock()
	s.metadata = models
	s.metadataMu.Unlock()
}

// ClearMetadata 清空 session 元数据（session 关闭、替换或结束时调用，
// 不允许保留离线/旧 session 的数据）。
func (s *Session) ClearMetadata() {
	s.metadataMu.Lock()
	s.metadata = nil
	s.metadataMu.Unlock()
}

// MetadataContextLength 返回指定模型名在当前 session 元数据中的 context_length。
// 条目缺失或未携带有效上下文长度时返回 miss。
func (s *Session) MetadataContextLength(model string) (int, bool) {
	s.metadataMu.RLock()
	v, ok := s.metadata[model]
	s.metadataMu.RUnlock()
	if !ok || v <= 0 {
		return 0, false
	}
	return v, true
}

func (s *Session) WriteFrame(frame Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return websocket.ErrCloseSent
	}
	data, err := EncodeFrame(frame)
	if err != nil {
		return err
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(defaultWriteWait))
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *Session) SendJob(frame Frame) error {
	frame.Type = FrameJob
	return s.WriteFrame(frame)
}

func (s *Session) SendCancel(jobID string) error {
	return s.WriteFrame(Frame{Type: FrameCancel, ID: jobID})
}

func (s *Session) closeWithReason(reason string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	s.mu.Unlock()

	s.failAllJobs(errors.New("cascade session closed: " + reason))
	s.ClearMetadata()

	_ = s.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason),
		time.Now().Add(defaultWriteWait),
	)
	_ = s.conn.Close()
}

func contextCanceledError() error {
	return errors.New("cascade job canceled")
}
