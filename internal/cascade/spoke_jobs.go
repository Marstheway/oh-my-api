package cascade

import (
	"context"
	"sync"
)

type connJobSinkWithID struct {
	sc *spokeConn
	id string
}

func (s *connJobSinkWithID) SendResult(chunk string, final bool, statusCode int) error {
	frame := Frame{Type: FrameResult, ID: s.id, Chunk: chunk, Final: final}
	if final && statusCode > 0 {
		frame.Status = statusCode
	}
	return s.sc.writeFrame(frame)
}

func (s *connJobSinkWithID) SendError(message string) error {
	return s.sc.writeFrame(Frame{Type: FrameError, ID: s.id, Message: message})
}

type spokeJobRegistry struct {
	mu   sync.Mutex
	jobs map[string]context.CancelFunc
}

func newSpokeJobRegistry() *spokeJobRegistry {
	return &spokeJobRegistry{jobs: make(map[string]context.CancelFunc)}
}

func (r *spokeJobRegistry) start(id string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[id] = cancel
}

func (r *spokeJobRegistry) finish(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.jobs, id)
}

func (r *spokeJobRegistry) cancel(id string) {
	r.mu.Lock()
	cancel, ok := r.jobs[id]
	if ok {
		delete(r.jobs, id)
	}
	r.mu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
}

func (r *spokeJobRegistry) cancelAll() {
	r.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(r.jobs))
	for _, cancel := range r.jobs {
		cancels = append(cancels, cancel)
	}
	r.jobs = make(map[string]context.CancelFunc)
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *Spoke) handleJobFrame(parent context.Context, frame Frame, sc *spokeConn) {
	if s.handler == nil {
		_ = sc.writeFrame(Frame{Type: FrameError, ID: frame.ID, Message: "cascade job handler not configured"})
		return
	}
	if frame.ID == "" {
		_ = sc.writeFrame(Frame{Type: FrameError, Message: "cascade job id must not be empty"})
		return
	}

	jobCtx, cancel := context.WithCancel(parent)
	if s.jobs == nil {
		s.jobs = newSpokeJobRegistry()
	}
	s.jobs.start(frame.ID, cancel)

	sink := &connJobSinkWithID{sc: sc, id: frame.ID}
	go func() {
		defer s.jobs.finish(frame.ID)
		defer cancel()
		_ = s.handler(jobCtx, frame, sink)
	}()
}
