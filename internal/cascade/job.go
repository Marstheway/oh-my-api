package cascade

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// JobRequest is a hub-side cascade job dispatched to the connected spoke.
type JobRequest struct {
	ID       string
	Protocol string
	Model    string
	Stream   bool
	Body     []byte
	Hop      string
}

type jobWaiter struct {
	stream     bool
	pr         *io.PipeReader
	pw         *io.PipeWriter
	buf        bytes.Buffer
	done       chan struct{}
	firstReady chan struct{}
	firstOnce  sync.Once
	err        error
	statusCode int
	mu         sync.Mutex
}

func newJobWaiter(stream bool) *jobWaiter {
	w := &jobWaiter{
		stream:     stream,
		done:       make(chan struct{}),
		firstReady: make(chan struct{}),
	}
	if stream {
		w.pr, w.pw = io.Pipe()
	}
	return w
}

func (w *jobWaiter) isFinished() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

func (w *jobWaiter) appendChunk(chunk string, final bool, statusCode int) {
	var toWrite []byte
	w.mu.Lock()
	if w.err != nil {
		w.mu.Unlock()
		return
	}
	streamUpstreamError := w.stream && final && statusCode >= http.StatusBadRequest
	if chunk != "" {
		if streamUpstreamError || !w.stream || w.pw == nil {
			w.buf.WriteString(chunk)
		} else {
			toWrite = []byte(chunk)
		}
	}
	if final && statusCode > 0 {
		w.statusCode = statusCode
	}
	shouldFinal := final
	w.mu.Unlock()

	w.firstOnce.Do(func() { close(w.firstReady) })

	if len(toWrite) > 0 {
		w.mu.Lock()
		dead := w.err != nil
		pw := w.pw
		w.mu.Unlock()
		if !dead && pw != nil {
			_, _ = pw.Write(toWrite)
		}
	}

	if shouldFinal {
		w.mu.Lock()
		if w.err == nil {
			w.finishLocked(nil)
		}
		w.mu.Unlock()
	}
}

func (w *jobWaiter) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.finishLocked(err)
}

func (w *jobWaiter) finishLocked(err error) {
	select {
	case <-w.done:
		return
	default:
	}

	if w.err == nil && err != nil {
		w.err = err
	}
	if w.pw != nil {
		if w.err != nil {
			_ = w.pw.CloseWithError(w.err)
		} else {
			_ = w.pw.Close()
		}
	}
	close(w.done)
}

func (w *jobWaiter) wait(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-w.done:
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.err != nil {
			return nil, w.err
		}
		return append([]byte(nil), w.buf.Bytes()...), nil
	}
}

func (w *jobWaiter) waitFirst(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.firstReady:
		return nil
	case <-w.done:
		w.mu.Lock()
		err := w.err
		w.mu.Unlock()
		if err != nil {
			return err
		}
		return nil
	}
}

func (w *jobWaiter) responseStatus() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.statusCode > 0 {
		return w.statusCode
	}
	return http.StatusOK
}

// ExecuteJob sends a job frame to the active spoke session and assembles the HTTP response.
func (h *Hub) ExecuteJob(ctx context.Context, req JobRequest) (*http.Response, error) {
	if req.ID == "" {
		return nil, fmt.Errorf("cascade job id must not be empty")
	}

	session, ok := h.Session()
	if !ok {
		return nil, fmt.Errorf("cascade session not connected")
	}

	waiter := session.beginJob(req.ID, req.Stream)
	if err := ctx.Err(); err != nil {
		session.endJob(req.ID)
		return nil, err
	}

	stopCancel := context.AfterFunc(ctx, func() {
		if !waiter.isFinished() {
			_ = session.SendCancel(req.ID)
			session.cancelJob(req.ID)
		}
	})

	stream := req.Stream
	jobFrame := Frame{
		ID:       req.ID,
		Protocol: req.Protocol,
		Model:    req.Model,
		Stream:   &stream,
		Body:     json.RawMessage(req.Body),
		Hop:      req.Hop,
	}
	if err := session.SendJob(jobFrame); err != nil {
		stopCancel()
		session.endJob(req.ID)
		return nil, fmt.Errorf("send cascade job: %w", err)
	}

	if req.Stream {
		if err := waiter.waitFirst(ctx); err != nil {
			stopCancel()
			session.endJob(req.ID)
			return nil, err
		}
		if statusCode := waiter.responseStatus(); statusCode >= http.StatusBadRequest {
			defer stopCancel()
			defer session.endJob(req.ID)
			body, err := waiter.wait(ctx)
			if err != nil {
				return nil, err
			}
			return &http.Response{
				StatusCode: statusCode,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewReader(body)),
			}, nil
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: &jobStreamBody{
				PipeReader: waiter.pr,
				session:    session,
				jobID:      req.ID,
				waiter:     waiter,
				stopCancel: stopCancel,
			},
		}, nil
	}

	defer stopCancel()
	defer session.endJob(req.ID)

	body, err := waiter.wait(ctx)
	if err != nil {
		return nil, err
	}
	statusCode := http.StatusOK
	waiter.mu.Lock()
	if waiter.statusCode > 0 {
		statusCode = waiter.statusCode
	}
	waiter.mu.Unlock()
	return &http.Response{
		StatusCode: statusCode,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(bytes.NewReader(body)),
	}, nil
}

type jobStreamBody struct {
	*io.PipeReader
	session    *Session
	jobID      string
	waiter     *jobWaiter
	stopCancel func() bool
}

func (b *jobStreamBody) Close() error {
	if b.waiter != nil && !b.waiter.isFinished() {
		if b.session != nil {
			_ = b.session.SendCancel(b.jobID)
			b.session.cancelJob(b.jobID)
		}
	}
	if b.stopCancel != nil {
		b.stopCancel()
	}
	if b.session != nil {
		b.session.endJob(b.jobID)
	}
	return b.PipeReader.Close()
}
