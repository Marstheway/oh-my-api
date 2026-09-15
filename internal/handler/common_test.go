package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/config"
	errs "github.com/Marstheway/oh-my-api/internal/errors"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/gin-gonic/gin"
)

// newTestSchedulerWithChecker 构造带独立 health.Checker 的 scheduler，
// 供测试验证流中断上报后的摘除行为。
func newTestSchedulerWithChecker() (*scheduler.Scheduler, *health.Checker) {
	providers := map[string]config.ProviderConfig{
		"test": {
			Endpoint:  "http://127.0.0.1:1",
			APIKey:    "test-key",
			Protocols: []string{"openai.responses"},
		},
	}
	client := provider.NewClient(providers, 50*time.Millisecond, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	return scheduler.New(rl, client, h, 500*time.Millisecond, 0, 0), h
}

func TestHandleWriteResponseError_StreamTruncatedReportsHealth(t *testing.T) {
	oldSched := sched
	s, h := newTestSchedulerWithChecker()
	sched = s
	t.Cleanup(func() { sched = oldSched })

	key := health.MakeHealthKey("test", "openai.responses")

	// 前两次断流：尚未达到阈值，仍健康
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", nil)
		handleWriteResponseError(c, errs.ProtocolOpenAI, codec.ErrStreamTruncated, true, "test", "m", "openai.responses")
	}
	if !h.IsHealthy(key) {
		t.Fatalf("provider should be healthy after 2 stream truncations")
	}

	// 第三次断流：达到阈值，摘除 provider
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", nil)
	handleWriteResponseError(c, errs.ProtocolOpenAI, codec.ErrStreamTruncated, true, "test", "m", "openai.responses")
	if h.IsHealthy(key) {
		t.Fatalf("provider should be unhealthy after 3 stream truncations")
	}
}

func TestHandleWriteResponseError_ClientCancelDoesNotReportHealth(t *testing.T) {
	oldSched := sched
	s, h := newTestSchedulerWithChecker()
	sched = s
	t.Cleanup(func() { sched = oldSched })

	key := health.MakeHealthKey("test", "openai.responses")

	// 连续 3 次客户端取消：不应触发摘除（客户端取消不是上游故障）
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", nil)
		handleWriteResponseError(c, errs.ProtocolOpenAI, context.Canceled, true, "test", "m", "openai.responses")
	}
	if !h.IsHealthy(key) {
		t.Fatalf("client cancel should not report stream failure")
	}
}

func TestHandleWriteResponseError_ClientDisconnectedWriteFailureDoesNotReportHealth(t *testing.T) {
	oldSched := sched
	s, h := newTestSchedulerWithChecker()
	sched = s
	t.Cleanup(func() { sched = oldSched })

	key := health.MakeHealthKey("test", "openai.responses")
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequestWithContext(ctx, "POST", "/", nil)
		handleWriteResponseError(c, errs.ProtocolOpenAI, errors.New("write: broken pipe"), true, "test", "m", "openai.responses")
	}
	if !h.IsHealthy(key) {
		t.Fatalf("client-disconnected write failure should not report stream failure")
	}
}
