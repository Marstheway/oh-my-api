package scheduler

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	streamProbeWindow         = 500 * time.Millisecond
	streamProbeMaxPrefixBytes = 32 * 1024
)

type streamProbeState struct {
	mu       sync.Mutex
	prefix   bytes.Buffer
	released bool
	aborted  bool
}

func (s *streamProbeState) captureBytes(p []byte) (released bool, aborted bool, prefixLen int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.aborted {
		return false, true, s.prefix.Len()
	}
	if s.released {
		return true, false, s.prefix.Len()
	}

	s.prefix.Write(p)
	return false, false, s.prefix.Len()
}

func (s *streamProbeState) releasePrefix() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.released = true
	prefix := make([]byte, s.prefix.Len())
	copy(prefix, s.prefix.Bytes())
	return prefix
}

func (s *streamProbeState) abortProbe() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aborted = true
}

func probeStreamPrefix(resp *http.Response, protocol string, prefillTimeout time.Duration, streamIdleTimeout time.Duration) (FailureKind, string, time.Duration, error) {
	if resp == nil || resp.Body == nil {
		return FailureKindSuccess, "", 0, nil
	}

	probeStart := time.Now()
	var streamTTFT time.Duration

	originalBody := resp.Body
	pipeReader, pipeWriter := io.Pipe()
	state := &streamProbeState{}
	events := make(chan string, 16)
	done := make(chan error, 1)
	stopClassification := make(chan struct{})
	prefixLimitReached := make(chan struct{}, 1)

	closeClassification := sync.OnceFunc(func() {
		close(stopClassification)
	})

	go func() {
		done <- pumpProbeStream(originalBody, pipeWriter, state, events, stopClassification, prefixLimitReached)
	}()

	release := func() {
		closeClassification()
		prefix := state.releasePrefix()
		tail := io.ReadCloser(pipeReader)
		if streamIdleTimeout > 0 {
			tail = &idleTimeoutReader{r: pipeReader, timeout: streamIdleTimeout}
		}
		resp.Body = &prefixedReadCloser{
			prefix:   bytes.NewReader(prefix),
			tail:     tail,
			original: originalBody,
			state:    state,
		}
	}

	var sseEvents []string
	drainReadyEvents := func() (FailureKind, string, bool, error) {
		for {
			select {
			case event, ok := <-events:
				if !ok {
					err := <-done
					if err != nil {
						_ = pipeReader.Close()
						return FailureKindSuccess, "", true, err
					}
					kind, reason := classifyResponse(protocol, nil, sseEvents)
					return kind, reason, true, nil
				}

				sseEvents = append(sseEvents, event)
				kind, reason := classifyResponse(protocol, nil, sseEvents)
				if kind == FailureKindSoft {
					return kind, reason, false, nil
				}
			default:
				return FailureKindSuccess, "", false, nil
			}
		}
	}

	prefillTimer := time.NewTimer(prefillTimeout)
	defer prefillTimer.Stop()
	var probeTimer *time.Timer
	var probeTimerC <-chan time.Time
	startProbeTimer := func() {
		if probeTimer != nil {
			return
		}
		probeTimer = time.NewTimer(streamProbeWindow)
		probeTimerC = probeTimer.C
	}
	stopPrefillTimer := func() {
		if !prefillTimer.Stop() {
			select {
			case <-prefillTimer.C:
			default:
			}
		}
	}
	defer func() {
		if probeTimer != nil {
			probeTimer.Stop()
		}
	}()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				err := <-done
				if err != nil {
					_ = pipeReader.Close()
					return FailureKindSuccess, "", streamTTFT, err
				}

				kind, reason := classifyResponse(protocol, nil, sseEvents)
				release()
				return kind, reason, streamTTFT, nil
			}

			sseEvents = append(sseEvents, event)
			kind, reason := classifyResponse(protocol, nil, sseEvents)
			if kind == FailureKindSoft {
				release()
				return kind, reason, streamTTFT, nil
			}
			if len(sseEvents) == 1 {
				streamTTFT = time.Since(probeStart)
				stopPrefillTimer()
				startProbeTimer()
			}
		case <-prefixLimitReached:
			kind, reason, _, err := drainReadyEvents()
			if err != nil {
				return FailureKindSuccess, "", streamTTFT, err
			}
			if len(sseEvents) > 0 {
				release()
				return kind, reason, streamTTFT, nil
			}
			if kind == FailureKindSoft {
				release()
				return kind, reason, streamTTFT, nil
			}
			continue
		case <-prefillTimer.C:
			// 如果在 prefill timeout 内没有收到任何 SSE 事件，返回 ErrPrefillTimeout
			if len(sseEvents) == 0 {
				_ = pipeReader.Close()
				return FailureKindSuccess, "", 0, ErrPrefillTimeout
			}
			kind, reason, _, err := drainReadyEvents()
			if err != nil {
				return FailureKindSuccess, "", streamTTFT, err
			}
			release()
			return kind, reason, streamTTFT, nil
		case <-probeTimerC:
			kind, reason, _, err := drainReadyEvents()
			if err != nil {
				return FailureKindSuccess, "", streamTTFT, err
			}
			release()
			return kind, reason, streamTTFT, nil
		}
	}
}

func pumpProbeStream(originalBody io.ReadCloser, pipeWriter *io.PipeWriter, state *streamProbeState, events chan<- string, stopClassification <-chan struct{}, prefixLimitReached chan<- struct{}) error {
	defer close(events)
	defer originalBody.Close()

	reader := bufio.NewReader(originalBody)
	var dataLines []string
	prefixLimitSignaled := false
	prefixLimitPending := false

	for {
		lineBytes, err := reader.ReadBytes('\n')
		if len(lineBytes) > 0 {
			released, aborted, prefixLen := state.captureBytes(lineBytes)
			if aborted {
				_ = pipeWriter.Close()
				return nil
			}

			if released {
				if _, writeErr := pipeWriter.Write(lineBytes); writeErr != nil {
					_ = pipeWriter.CloseWithError(writeErr)
					return nil
				}
			} else {
				if !prefixLimitSignaled && prefixLen >= streamProbeMaxPrefixBytes {
					prefixLimitPending = true
				}

				line := strings.TrimRight(string(lineBytes), "\r\n")
				switch {
				case line == "":
					if len(dataLines) > 0 {
						payload := strings.Join(dataLines, "\n")
						select {
						case events <- payload:
						case <-stopClassification:
						}
						dataLines = nil
					}
				case strings.HasPrefix(line, "data:"):
					data := strings.TrimPrefix(line, "data:")
					if strings.HasPrefix(data, " ") {
						data = data[1:]
					}
					dataLines = append(dataLines, data)
				}

				if prefixLimitPending && !prefixLimitSignaled && len(dataLines) == 0 {
					prefixLimitSignaled = true
					select {
					case prefixLimitReached <- struct{}{}:
					default:
					}
				}
			}
		}

		if err != nil {
			if err == io.EOF {
				_ = pipeWriter.Close()
				return nil
			}
			_ = pipeWriter.CloseWithError(err)
			return err
		}
	}
}

type prefixedReadCloser struct {
	prefix    *bytes.Reader
	tail      io.ReadCloser
	original  io.Closer
	state     *streamProbeState
	closeOnce sync.Once
}

func (r *prefixedReadCloser) Read(p []byte) (int, error) {
	if r.prefix != nil {
		n, err := r.prefix.Read(p)
		if n > 0 {
			if err == io.EOF {
				return n, nil
			}
			return n, err
		}
		if err != nil && err != io.EOF {
			return 0, err
		}
	}
	if r.tail == nil {
		return 0, io.EOF
	}
	return r.tail.Read(p)
}

func (r *prefixedReadCloser) Close() error {
	var closeErr error
	r.closeOnce.Do(func() {
		if r.state != nil {
			r.state.abortProbe()
		}
		if r.tail != nil {
			if err := r.tail.Close(); err != nil {
				closeErr = err
			}
		}
		if r.original != nil {
			if err := r.original.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
	})
	return closeErr
}

// idleTimeoutReader 包装 io.ReadCloser，在每次 Read 时设置超时。
// 若在 timeout 内无数据到达，返回超时错误。
// 不适用于高频小包读取场景（每次 Read 会启动一个 goroutine），
// 但对于流式 SSE 读取，Read 频率低，goroutine 开销可接受。
type idleTimeoutReader struct {
	r       io.ReadCloser
	timeout time.Duration
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := r.r.Read(p)
		ch <- result{n, err}
	}()

	timer := time.NewTimer(r.timeout)
	defer timer.Stop()

	select {
	case res := <-ch:
		return res.n, res.err
	case <-timer.C:
		return 0, fmt.Errorf("stream idle timeout: no data for %v", r.timeout)
	}
}

func (r *idleTimeoutReader) Close() error {
	return r.r.Close()
}
