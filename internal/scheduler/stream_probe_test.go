package scheduler

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProbeStreamPrefix_PrefillTimeout_ZeroEvents(t *testing.T) {
	// 构造一个会等待的流，在 prefill timeout 内不发送任何事件
	// 使用 pipe 来模拟一个慢速的流式响应
	reader, writer := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       reader,
	}

	// 在另一个 goroutine 中延迟发送事件（在 prefill timeout 之后）
	go func() {
		time.Sleep(200 * time.Millisecond) // 延迟发送，确保在 prefill timeout 之后
		writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n"))
		writer.Close()
	}()

	// 使用极短的 prefillTimeout (50ms)，应该在事件发送前超时
	kind, reason, _, err := probeStreamPrefix(resp, "openai", 50*time.Millisecond, 0)

	if err != ErrPrefillTimeout {
		t.Errorf("error = %v, want %v", err, ErrPrefillTimeout)
	}
	if kind != FailureKindSuccess {
		t.Errorf("FailureKind = %q, want %q", kind, FailureKindSuccess)
	}
	if reason != "" {
		t.Errorf("FailureReason = %q, want empty", reason)
	}
}

func TestProbeStreamPrefix_PrefillTimeout_HasEvents(t *testing.T) {
	// 构造一个 SSE 流式 response，在 prefill timeout 内发送正常的 token 事件
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}\n\n" +
		"data: [DONE]\n\n"
	resp := newSchedulerHTTPResponse(http.StatusOK, "text/event-stream", body)

	// 使用充足的 prefillTimeout (5s)，确保能收到事件
	kind, reason, _, err := probeStreamPrefix(resp, "openai", 5*time.Second, 0)
	if err != nil {
		t.Fatalf("probeStreamPrefix failed: %v", err)
	}
	defer resp.Body.Close()

	if kind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", kind, FailureKindSuccess)
	}
	if reason != "" {
		t.Fatalf("FailureReason = %q, want empty", reason)
	}

	// 验证 prefixedReadCloser 正常工作
	releasedBody := readSchedulerResponseBody(t, resp)
	if string(releasedBody) != body {
		t.Fatalf("released body mismatch: got %q want %q", string(releasedBody), body)
	}
}

func TestProbeStreamPrefix_PrefillTimeoutLongerThanProbeWindow_KeepsWaiting(t *testing.T) {
	reader, writer := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       reader,
	}

	body := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n"
	start := time.Now()
	go func() {
		time.Sleep(700 * time.Millisecond)
		_, _ = writer.Write([]byte(body))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
		_ = writer.Close()
	}()

	kind, reason, _, err := probeStreamPrefix(resp, "openai", 2*time.Second, 0)
	if err != nil {
		t.Fatalf("probeStreamPrefix failed: %v", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	if elapsed < 650*time.Millisecond {
		t.Fatalf("probe returned too early: %v", elapsed)
	}
	if kind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", kind, FailureKindSuccess)
	}
	if reason != "" {
		t.Fatalf("FailureReason = %q, want empty", reason)
	}

	releasedBody := readSchedulerResponseBody(t, resp)
	wantBody := body + "data: [DONE]\n\n"
	if string(releasedBody) != wantBody {
		t.Fatalf("released body mismatch: got %q want %q", string(releasedBody), wantBody)
	}
}

func TestProbeStreamPrefix_ProbeWindowStillDetectsSoftFailure(t *testing.T) {
	payload := "{\"id\":\"" + strings.Repeat("a", streamProbeMaxPrefixBytes) + "\",\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}"
	body := "data: " + payload + "\n\n"
	resp := newSchedulerHTTPResponse(http.StatusOK, "text/event-stream", body)

	kind, reason, _, err := probeStreamPrefix(resp, "openai", 5*time.Second, 0)
	if err != nil {
		resp.Body.Close()
		t.Fatalf("probeStreamPrefix failed: %v", err)
	}
	defer resp.Body.Close()

	if kind != FailureKindSoft {
		t.Fatalf("FailureKind = %q, want %q", kind, FailureKindSoft)
	}
	if reason != failureReasonContentFilterFinish {
		t.Fatalf("FailureReason = %q, want %q", reason, failureReasonContentFilterFinish)
	}

	releasedBody := readSchedulerResponseBody(t, resp)
	if string(releasedBody) != body {
		t.Fatalf("released body mismatch: got %q want %q", string(releasedBody), body)
	}
}

func TestIdleTimeoutReader_NormalRead(t *testing.T) {
	reader, writer := io.Pipe()
	r := &idleTimeoutReader{r: reader, timeout: 200 * time.Millisecond}

	go func() {
		time.Sleep(50 * time.Millisecond)
		writer.Write([]byte("hello"))
		writer.Close()
	}()

	buf := make([]byte, 1024)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 || string(buf[:n]) != "hello" {
		t.Fatalf("unexpected data: n=%d got=%q", n, string(buf[:n]))
	}
}

func TestIdleTimeoutReader_Timeout(t *testing.T) {
	reader, writer := io.Pipe()
	r := &idleTimeoutReader{r: reader, timeout: 50 * time.Millisecond}

	// writer 侧不写任何数据，触发 idle timeout
	defer writer.Close()

	buf := make([]byte, 1024)
	_, err := r.Read(buf)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "stream idle timeout") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestIdleTimeoutReader_Close(t *testing.T) {
	reader, writer := io.Pipe()
	r := &idleTimeoutReader{r: reader, timeout: time.Second}

	writer.Close()
	r.Close()

	buf := make([]byte, 1024)
	_, err := r.Read(buf)
	if err == nil {
		t.Fatal("expected error after close, got nil")
	}
}
