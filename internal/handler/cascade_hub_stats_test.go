package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const hubSpokeReply = "hello from spoke"

// startJobAnsweringSpoke 建立一条真实 Hub 会话，并应答所有 job frame：
// 流式 job 回 SSE chunk + final，非流式 job 回整包 chat completion。
func startJobAnsweringSpoke(t *testing.T, hub *cascade.Hub) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/cascade", hub.ServeHTTP)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/cascade", cascade.AuthorizationHeaders("hub-secret"))
	if err != nil {
		t.Fatalf("dial cascade hub: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	register, _ := cascade.EncodeFrame(cascade.Frame{Type: cascade.FrameRegister, Token: "hub-secret"})
	if err := conn.WriteMessage(websocket.TextMessage, register); err != nil {
		t.Fatalf("write register: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read register ack: %v", err)
	}

	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			frame, err := cascade.DecodeFrame(data)
			if err != nil || frame.Type != cascade.FrameJob {
				continue
			}
			if frame.Stream != nil && *frame.Stream {
				chunk := `data: {"id":"cc-1","object":"chat.completion.chunk","created":1,"model":"local-gpt",` +
					`"choices":[{"index":0,"delta":{"role":"assistant","content":"` + hubSpokeReply + `"},"finish_reason":null}]}` + "\n\n"
				writeJobFrame(conn, frame.ID, chunk, false, 0)
				writeJobFrame(conn, frame.ID, "data: [DONE]\n\n", true, http.StatusOK)
				continue
			}
			body := `{"id":"cc-1","object":"chat.completion","created":1,"model":"local-gpt",` +
				`"choices":[{"index":0,"message":{"role":"assistant","content":"` + hubSpokeReply + `"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`
			writeJobFrame(conn, frame.ID, body, true, http.StatusOK)
		}
	}()
}

func writeJobFrame(conn *websocket.Conn, id, chunk string, final bool, status int) {
	frame, _ := cascade.EncodeFrame(cascade.Frame{
		Type: cascade.FrameResult, ID: id, Chunk: chunk, Final: final, Status: status,
	})
	_ = conn.WriteMessage(websocket.TextMessage, frame)
}

// setupHubCascadeChatRouter 装配 Hub 侧测试环境：真实 Hub + Spoke 会话、resolver/scheduler/
// provider client 与 /v1/chat/completions 路由；hub 为 nil 时模拟 Spoke 会话缺席。
func setupHubCascadeChatRouter(t *testing.T, testCfg *config.Config, hub *cascade.Hub) *gin.Engine {
	t.Helper()
	testResolver, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	reg := cascade.NewHubRegistry()
	if hub != nil {
		reg.Set(hub)
	}

	oldCfg, oldResolver, oldSched, oldHubs := cfg, resolver, sched, cascadeHubs
	cfg, resolver = testCfg, testResolver
	SetCascadeHubs(reg)
	t.Cleanup(func() {
		cfg, resolver, sched, cascadeHubs = oldCfg, oldResolver, oldSched, oldHubs
	})

	client := provider.NewClient(testCfg.Providers.Items, 120*time.Second, 0, 0)
	client.SetCascadeHubRegistry(reg)
	sched = scheduler.New(ratelimit.NewManager(testCfg.Providers.Items), client, health.NewChecker(3, 30*time.Second), 500*time.Millisecond, 0, 0)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("key_name", "hub-key")
		c.Next()
	})
	router.POST("/v1/chat/completions", Chat)
	return router
}

func initHubCascadeStats(t *testing.T, name string) {
	t.Helper()
	if err := stats.Init(filepath.Join(t.TempDir(), name)); err != nil {
		t.Fatalf("Init stats error: %v", err)
	}
	t.Cleanup(stats.Reset)
	metrics.ResetForTest()
}

func hubCascadeConfig() *config.Config {
	return &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"corp-dev": {
				Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
				Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
			},
		}},
		ModelGroups: []config.ModelGroupConfig{{Name: "hub-model", Models: config.ModelEntries{{Model: "corp-dev/local-gpt"}}}},
	}
}

func postHubChat(t *testing.T, router *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

// TestChat_CascadeProviderRecordsHubStats 回归 Hub 侧统计：cascade provider 作为普通叶子
// 参与调度，请求成功后 Hub 必须按 key/provider/upstream/user_model 维度记账，
// 否则 Hub 的 dashboard 会漏掉所有下发给 Spoke 的流量。
func TestChat_CascadeProviderRecordsHubStats(t *testing.T) {
	initHubCascadeStats(t, "hub-cascade-stats.db")

	hub := cascadeHubFixture()
	startJobAnsweringSpoke(t, hub)
	router := setupHubCascadeChatRouter(t, hubCascadeConfig(), hub)

	w := postHubChat(t, router, `{"model":"hub-model","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), hubSpokeReply) {
		t.Fatalf("spoke payload not passed through: %s", w.Body.String())
	}

	assertHubCascadeStats(t, 1, 1)
}

// TestChat_CascadeProviderRecordsHubStatsStreaming 覆盖流式 job：Hub 侧返回 SSE 时同样
// 记 key/provider/model 维度，并累计输出 token。
func TestChat_CascadeProviderRecordsHubStatsStreaming(t *testing.T) {
	initHubCascadeStats(t, "hub-cascade-stream-stats.db")

	hub := cascadeHubFixture()
	startJobAnsweringSpoke(t, hub)
	router := setupHubCascadeChatRouter(t, hubCascadeConfig(), hub)

	w := postHubChat(t, router, `{"model":"hub-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), hubSpokeReply) || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("stream payload not passed through: %s", w.Body.String())
	}

	assertHubCascadeStats(t, 1, 1)
}

// TestChat_CascadeProviderUnreachableRecordsError 覆盖 Spoke 会话缺席：cascade 叶子不可
// 调度，请求失败但 Hub 仍必须留下 error 计数，且不得伪造成功统计。
func TestChat_CascadeProviderUnreachableRecordsError(t *testing.T) {
	initHubCascadeStats(t, "hub-cascade-error-stats.db")

	router := setupHubCascadeChatRouter(t, hubCascadeConfig(), nil)

	w := postHubChat(t, router, `{"model":"hub-model","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code == http.StatusOK {
		t.Fatalf("expected failure without spoke session, body = %s", w.Body.String())
	}

	keys, err := stats.GetQuerier().QueryByKeys("", "")
	if err != nil {
		t.Fatalf("QueryByKeys: %v", err)
	}
	if got := keys["hub-key"]; got != nil {
		t.Fatalf("failed request must not be recorded as success stats: %+v", got)
	}

	if n := testutil.ToFloat64(metrics.GetRequestTotal().WithLabelValues(
		"openai.chat", "", "", "", "hub-model", "hub-key", "error",
	)); n != 1 {
		t.Fatalf("request_total{status=error} = %v, want 1 (collected=%d)", n,
			testutil.CollectAndCount(metrics.GetRequestTotal(), "request_total"))
	}
}

// assertHubCascadeStats 断言 Hub 侧四个维度与 request_total 都记了成对的 cascade 流量。
func assertHubCascadeStats(t *testing.T, wantRequests int64, wantMetricLines int) {
	t.Helper()

	keys, err := stats.GetQuerier().QueryByKeys("", "")
	if err != nil {
		t.Fatalf("QueryByKeys: %v", err)
	}
	got, ok := keys["hub-key"]
	if !ok {
		t.Fatalf("hub-side client key not recorded, got: %v", keys)
	}
	if got.RequestCount != wantRequests {
		t.Fatalf("request_count = %d, want %d", got.RequestCount, wantRequests)
	}
	if got.InputTokens <= 0 {
		t.Fatalf("input_tokens = %d, want > 0", got.InputTokens)
	}
	if got.OutputTokens <= 0 {
		t.Fatalf("output_tokens = %d, want > 0", got.OutputTokens)
	}

	byProviderModel, err := stats.GetQuerier().QueryByProviders("", "")
	if err != nil {
		t.Fatalf("QueryByProviders: %v", err)
	}
	leaf, ok := byProviderModel["corp-dev/local-gpt"]
	if !ok {
		t.Fatalf("cascade provider leaf missing, got: %v", byProviderModel)
	}
	if leaf.RequestCount != wantRequests {
		t.Fatalf("leaf request_count = %d, want %d", leaf.RequestCount, wantRequests)
	}

	userModels, err := stats.GetQuerier().QueryByUserModels("", "")
	if err != nil {
		t.Fatalf("QueryByUserModels: %v", err)
	}
	if _, ok := userModels["hub-model"]; !ok {
		t.Fatalf("user_model dimension missing, got: %v", userModels)
	}

	if n := testutil.ToFloat64(metrics.GetRequestTotal().WithLabelValues(
		"openai.chat", "openai.chat", "corp-dev", "local-gpt", "hub-model", "hub-key", "success",
	)); n != float64(wantMetricLines) {
		t.Fatalf("request_total{provider=corp-dev,status=success} = %v, want %d", n, wantMetricLines)
	}
}
