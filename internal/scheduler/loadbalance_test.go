package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"strings"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestLoadBalanceStrategy_SingleProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"test": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Provider: providers["test"], UpstreamModel: "model", Request: req, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "test" {
		t.Errorf("winner = %q, want %q", result.Winner, "test")
	}

	if !h.IsHealthy(health.MakeHealthKey("test", "")) {
		t.Error("provider should be healthy after success")
	}
	if !h.IsHealthy(health.MakeHealthKey("test", "")) {
		t.Error("health key should be healthy after success")
	}
}

func TestLoadBalanceStrategy_NoHealthyProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"test": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)

	// 标记为不健康
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))

	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Provider: providers["test"], UpstreamModel: "model", Request: req, Weight: 1},
	}

	_, err := strategy.Execute(context.Background(), tasks)
	if err != ErrNoProviderAvailable {
		t.Errorf("error = %v, want %v", err, ErrNoProviderAvailable)
	}
}

func TestLoadBalanceStrategy_ProtocolGranularityIsolation(t *testing.T) {
	respSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"resp failed"}`))
	}))
	defer respSrv.Close()

	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chat-ok"}`))
	}))
	defer chatSrv.Close()

	providers := map[string]config.ProviderConfig{
		"token-hub": {
			Endpoint:  chatSrv.URL,
			APIKey:    "k",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	// Mark response channel unhealthy only.
	respKey := health.MakeHealthKey("token-hub", "openai.response")
	h.ReportFailure(respKey)
	h.ReportFailure(respKey)
	h.ReportFailure(respKey)

	respReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, respSrv.URL, nil)
	chatReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, chatSrv.URL, nil)

	tasks := []Task{
		{ProviderName: "token-hub", Provider: providers["token-hub"], UpstreamModel: "m", OutboundProtocol: "openai.response", Request: respReq, Weight: 1},
		{ProviderName: "token-hub", Provider: providers["token-hub"], UpstreamModel: "m", OutboundProtocol: "openai", Request: chatReq, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", result.Response.StatusCode)
	}
}

func TestLoadBalanceStrategy_ProbeReadFailureMarksProviderUnhealthy(t *testing.T) {
	rand.Seed(1)

	srv := newProbeReadFailureServer(t)
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"broken": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(1, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	_, err := strategy.Execute(context.Background(), []Task{{
		ProviderName:  "broken",
		Provider:      providers["broken"],
		UpstreamModel: "model",
		Request:       req,
		Weight:        1,
	}})
	if err == nil {
		t.Fatal("Execute should fail on probe read error")
	}

	if h.IsHealthy(health.MakeHealthKey("broken", "")) {
		t.Fatal("probe read failure should report provider failure")
	}
}

func TestLoadBalanceStrategy_FailoverToNext(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success"}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"fail": {
			Endpoint:  failSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, failSrv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)

	tasks := []Task{
		{ProviderName: "fail", Provider: providers["fail"], UpstreamModel: "model", Request: req1, Weight: 1},
		{ProviderName: "success", Provider: providers["success"], UpstreamModel: "model", Request: req2, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute should succeed after failover: %v", err)
	}
	defer result.Response.Body.Close()

	if !h.IsHealthy(health.MakeHealthKey("success", "")) {
		t.Error("success provider should be healthy")
	}
}

func TestLoadBalanceStrategy_WeightDistribution(t *testing.T) {
	countA := 0
	countB := 0

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		countA++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"a"}`))
	}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		countB++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"b"}`))
	}))
	defer srvB.Close()

	providers := map[string]config.ProviderConfig{
		"a": {
			Endpoint:  srvA.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"b": {
			Endpoint:  srvB.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	// 执行多次请求，验证权重分布（随机策略用大样本）
	for i := 0; i < 2000; i++ {
		reqA, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srvA.URL, nil)
		reqB, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srvB.URL, nil)

		tasks := []Task{
			{ProviderName: "a", Provider: providers["a"], UpstreamModel: "model", Request: reqA, Weight: 3},
			{ProviderName: "b", Provider: providers["b"], UpstreamModel: "model", Request: reqB, Weight: 1},
		}

		result, err := strategy.Execute(context.Background(), tasks)
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
		result.Response.Body.Close()
	}

	// 权重 3:1，a 理论值约 1500，容忍范围放宽避免随机抖动
	if countA < 1300 || countA > 1700 {
		t.Errorf("countA = %d, expect in [1300,1700] (weight 3)", countA)
	}
	if countB < 300 || countB > 700 {
		t.Errorf("countB = %d, expect in [300,700] (weight 1)", countB)
	}
}

func TestLoadBalanceStrategy_WeightDistribution_FourProviders(t *testing.T) {
	counts := map[string]int{
		"coding-plan": 0,
		"token-hub":   0,
		"venus-51":    0,
		"venus-5":     0,
	}

	newServer := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counts[name]++
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"id":"ok"}`))
		}))
	}

	srvCP := newServer("coding-plan")
	defer srvCP.Close()
	srvTH := newServer("token-hub")
	defer srvTH.Close()
	srvV51 := newServer("venus-51")
	defer srvV51.Close()
	srvV5 := newServer("venus-5")
	defer srvV5.Close()

	providers := map[string]config.ProviderConfig{
		"coding-plan": {Endpoint: srvCP.URL, APIKey: "k", Protocols: []string{"openai"}, RateLimit: config.RateLimitConfig{QPM: 0}},
		"token-hub":   {Endpoint: srvTH.URL, APIKey: "k", Protocols: []string{"openai"}, RateLimit: config.RateLimitConfig{QPM: 0}},
		"venus-51":    {Endpoint: srvV51.URL, APIKey: "k", Protocols: []string{"openai"}, RateLimit: config.RateLimitConfig{QPM: 0}},
		"venus-5":     {Endpoint: srvV5.URL, APIKey: "k", Protocols: []string{"openai"}, RateLimit: config.RateLimitConfig{QPM: 0}},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	for i := 0; i < 4000; i++ {
		reqCP, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srvCP.URL, nil)
		reqTH, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srvTH.URL, nil)
		reqV51, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srvV51.URL, nil)
		reqV5, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srvV5.URL, nil)

		tasks := []Task{
			{ProviderName: "coding-plan", Provider: providers["coding-plan"], UpstreamModel: "m", Request: reqCP, Weight: 3},
			{ProviderName: "token-hub", Provider: providers["token-hub"], UpstreamModel: "m", Request: reqTH, Weight: 1},
			{ProviderName: "venus-51", Provider: providers["venus-51"], UpstreamModel: "m", Request: reqV51, Weight: 3},
			{ProviderName: "venus-5", Provider: providers["venus-5"], UpstreamModel: "m", Request: reqV5, Weight: 3},
		}

		result, err := strategy.Execute(context.Background(), tasks)
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
		result.Response.Body.Close()
	}

	// 理论比例 30%/10%/30%/30%，给出宽松区间避免偶发抖动
	if counts["coding-plan"] < 900 || counts["coding-plan"] > 1500 {
		t.Errorf("coding-plan = %d, expect in [900,1500]", counts["coding-plan"])
	}
	if counts["token-hub"] < 250 || counts["token-hub"] > 550 {
		t.Errorf("token-hub = %d, expect in [250,550]", counts["token-hub"])
	}
	if counts["venus-51"] < 900 || counts["venus-51"] > 1500 {
		t.Errorf("venus-51 = %d, expect in [900,1500]", counts["venus-51"])
	}
	if counts["venus-5"] < 900 || counts["venus-5"] > 1500 {
		t.Errorf("venus-5 = %d, expect in [900,1500]", counts["venus-5"])
	}
}

func TestLoadBalanceStrategy_AllRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"test": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 1},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	// 消耗限流令牌
	rl.Allow("test", "model")

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Provider: providers["test"], UpstreamModel: "model", Request: req, Weight: 1},
	}

	_, err := strategy.Execute(context.Background(), tasks)
	if err != ErrAllRateLimited {
		t.Errorf("error = %v, want %v", err, ErrAllRateLimited)
	}
}

func TestLoadBalanceStrategy_RateLimitShouldNotBurnUnselectedTokens(t *testing.T) {
	countA := 0
	countB := 0

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		countA++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"a"}`))
	}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		countB++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"b"}`))
	}))
	defer srvB.Close()

	providers := map[string]config.ProviderConfig{
		"a": {
			Endpoint: srvA.URL,
			APIKey:   "test-key",
			Protocols: []string{"openai"},
			UpstreamModels: []config.UpstreamModelConfig{
				{Model: "model", QPM: 60000},
			},
		},
		"b": {
			Endpoint: srvB.URL,
			APIKey:   "test-key",
			Protocols: []string{"openai"},
			UpstreamModels: []config.UpstreamModelConfig{
				{Model: "model", QPM: 60000},
			},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	for i := 0; i < 20; i++ {
		reqA, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srvA.URL, nil)
		reqB, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srvB.URL, nil)
		tasks := []Task{
			{ProviderName: "a", Provider: providers["a"], UpstreamModel: "model", Request: reqA, Weight: 1},
			{ProviderName: "b", Provider: providers["b"], UpstreamModel: "model", Request: reqB, Weight: 1},
		}

		result, err := strategy.Execute(context.Background(), tasks)
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
		result.Response.Body.Close()
	}

	if countA == 0 || countB == 0 {
		t.Fatalf("both providers should receive traffic, got a=%d b=%d", countA, countB)
	}
}

func TestLoadBalanceStrategy_NoTasks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{}
	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	_, err := strategy.Execute(context.Background(), nil)
	if err != ErrNoTasks {
		t.Errorf("error = %v, want %v", err, ErrNoTasks)
	}
}

func TestLoadBalanceStrategy_HealthReport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"test": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	// 先标记为不健康
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Provider: providers["test"], UpstreamModel: "model", Request: req, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	// 成功后应该重置健康状态
	if !h.IsHealthy(health.MakeHealthKey("test", "")) {
		t.Error("provider should be healthy after success")
	}
}

// TestLoadBalance_LogsProviderUpstreamPair 验证 load-balance 日志中使用 upstream_identity=provider/model，
// 而非裸 model 字段。
func TestLoadBalance_LogsProviderUpstreamPair(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"test-prov": {
			Endpoint:  srv.URL,
			APIKey:    "k",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	// 捕获 slog 输出
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	old := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(old)

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test-prov", Provider: providers["test-prov"], UpstreamModel: "my-upstream-model", Request: req, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	logs := buf.String()

	// upstream_identity 字段应存在且值为 provider/upstream_model
	if !strings.Contains(logs, `"upstream_identity"`) {
		t.Errorf("log output should contain upstream_identity field, got: %s", logs)
	}
	if !strings.Contains(logs, `"test-prov/my-upstream-model"`) {
		t.Errorf("log output should contain test-prov/my-upstream-model, got: %s", logs)
	}

	// 不应出现裸 model 字段（值为 upstreamModel 而不包含 provider 前缀）
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if v, ok := entry["model"]; ok {
			s, _ := v.(string)
			// model 字段的值若不含 / 则说明只有裸 upstream model
			if !strings.Contains(s, "/") {
				t.Errorf("log line contains bare model field without provider prefix: %q, line: %s", s, line)
			}
		}
	}
}

func TestLoadBalanceStrategy_ContentFilterRetriesToNextSuccess(t *testing.T) {
	rand.Seed(1)

	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft","choices":[{"message":{"role":"assistant","content":"I can't help with that request."},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	softReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	successReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: softReq, Weight: 1},
		{ProviderName: "success", Provider: providers["success"], UpstreamModel: "model", Request: successReq, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "success" {
		t.Fatalf("winner = %q, want %q", result.Winner, "success")
	}
	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
}

func TestLoadBalanceStrategy_StreamSoftFailureClosesAbandonedBodyOnSuccess(t *testing.T) {
	rand.Seed(1)

	softContinue := make(chan struct{}, 1)
	softClosed := make(chan struct{}, 1)
	defer func() {
		select {
		case softContinue <- struct{}{}:
		default:
		}
	}()

	softCalls := 0
	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		softCalls++
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}

		<-softContinue
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"late\"},\"finish_reason\":null}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}

		select {
		case <-r.Context().Done():
			softClosed <- struct{}{}
		case <-time.After(300 * time.Millisecond):
		}
	}))
	defer softSrv.Close()

	successCalls := 0
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		successCalls++
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	softReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, softSrv.URL, nil)
	successReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, successSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: softReq, Weight: 100},
		{ProviderName: "success", Provider: providers["success"], UpstreamModel: "model", Request: successReq, Weight: 1},
	}

	result, err := strategy.Execute(ctx, tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "success" {
		t.Fatalf("winner = %q, want %q", result.Winner, "success")
	}
	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
	if softCalls != 1 {
		t.Fatalf("soft provider called %d times, want 1", softCalls)
	}
	if successCalls != 1 {
		t.Fatalf("success provider called %d times, want 1", successCalls)
	}

	body := string(readSchedulerResponseBody(t, result.Response))
	if !strings.Contains(body, "\"content\":\"ok\"") {
		t.Fatalf("success stream body missing winning content: %q", body)
	}

	softContinue <- struct{}{}
	select {
	case <-softClosed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("abandoned soft-failure stream body was not closed")
	}
}

func TestLoadBalanceStrategy_SoftFailureReturnsLaterHardFailureAndKeepsHealth(t *testing.T) {
	rand.Seed(1)

	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft","choices":[{"message":{"role":"assistant","content":"I can't help with that request."},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv.Close()

	hardSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"upstream failed"}`))
	}))
	defer hardSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"hard": {
			Endpoint:  hardSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	softReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	hardReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, hardSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: softReq, Weight: 1},
		{ProviderName: "hard", Provider: providers["hard"], UpstreamModel: "model", Request: hardReq, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "hard" {
		t.Fatalf("winner = %q, want %q", result.Winner, "hard")
	}
	if result.FailureKind != FailureKindHard {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindHard)
	}
	if result.Response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", result.Response.StatusCode, http.StatusInternalServerError)
	}
	if !h.IsHealthy(health.MakeHealthKey("soft", "")) {
		t.Fatal("soft-failing provider should remain healthy")
	}
}

func TestLoadBalanceStrategy_SoftFailureWithRateLimitedCandidatesReturnsSoft(t *testing.T) {
	rand.Seed(1)

	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft","choices":[{"message":{"role":"assistant","content":"I can't help with that request."},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv.Close()

	rateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"late-success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer rateSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"rate-limited": {
			Endpoint:  rateSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 1},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	if !rl.Allow("rate-limited", "model") {
		t.Fatal("failed to exhaust rate-limited provider token")
	}

	softReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	rateReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, rateSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: softReq, Weight: 1},
		{ProviderName: "rate-limited", Provider: providers["rate-limited"], UpstreamModel: "model", Request: rateReq, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "soft" {
		t.Fatalf("winner = %q, want %q", result.Winner, "soft")
	}
	if result.FailureKind != FailureKindSoft {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSoft)
	}
}

func TestLoadBalanceStrategy_SoftFailureRemovesSelectedCandidateOnly(t *testing.T) {
	rand.Seed(1)

	softCalls := 0
	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		softCalls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft","choices":[{"message":{"role":"assistant","content":"blocked"},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv.Close()

	successCalls := 0
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		successCalls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"shared": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	successReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)
	softReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "shared", Provider: providers["shared"], UpstreamModel: "success-model", Request: successReq, Weight: 1},
		{ProviderName: "shared", Provider: providers["shared"], UpstreamModel: "soft-model", Request: softReq, Weight: 100},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
	if softCalls != 1 {
		t.Fatalf("soft candidate called %d times, want 1", softCalls)
	}
	if successCalls != 1 {
		t.Fatalf("success candidate called %d times, want 1", successCalls)
	}
}

func TestLoadBalanceStrategy_ProviderUnhealthyRemovesSiblingTasksInSameRequest(t *testing.T) {
	failingCalls := 0
	failingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failingCalls++
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"upstream failed"}`))
	}))
	defer failingSrv.Close()

	siblingCalls := 0
	siblingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		siblingCalls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"should-not-run"}`))
	}))
	defer siblingSrv.Close()

	providers := map[string]config.ProviderConfig{
		"shared": {
			Endpoint:  failingSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(1, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	failingReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, failingSrv.URL, nil)
	siblingReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, siblingSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "shared", Provider: providers["shared"], UpstreamModel: "failing-model", Request: failingReq, Weight: 100},
		{ProviderName: "shared", Provider: providers["shared"], UpstreamModel: "sibling-model", Request: siblingReq, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindHard {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindHard)
	}
	if result.Response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", result.Response.StatusCode, http.StatusInternalServerError)
	}
	if failingCalls != 1 {
		t.Fatalf("failing candidate called %d times, want 1", failingCalls)
	}
	if siblingCalls != 0 {
		t.Fatalf("sibling candidate called %d times, want 0", siblingCalls)
	}
	if h.IsHealthy(health.MakeHealthKey("shared", "")) {
		t.Fatal("provider should be unhealthy after first 5xx at threshold 1")
	}
}

func TestLoadBalanceStrategy_ProviderUnhealthyRemovesInterleavedSiblingTasksInSameRequest(t *testing.T) {
	failingCalls := 0
	failingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failingCalls++
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"upstream failed"}`))
	}))
	defer failingSrv.Close()

	otherCalls := 0
	otherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherCalls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"other-success"}`))
	}))
	defer otherSrv.Close()

	siblingCalls := 0
	siblingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		siblingCalls++
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"shared-sibling-should-not-run"}`))
	}))
	defer siblingSrv.Close()

	providers := map[string]config.ProviderConfig{
		"shared": {
			Endpoint:  failingSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"other-provider": {
			Endpoint:  otherSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(1, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	failingReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, failingSrv.URL, nil)
	otherReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, otherSrv.URL, nil)
	siblingReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, siblingSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "shared", Provider: providers["shared"], UpstreamModel: "failing-model", OutboundProtocol: "openai", Request: failingReq, Weight: 100},
		{ProviderName: "other-provider", Provider: providers["other-provider"], UpstreamModel: "other-model", OutboundProtocol: "openai", Request: otherReq, Weight: 1},
		{ProviderName: "shared", Provider: providers["shared"], UpstreamModel: "sibling-model", OutboundProtocol: "openai", Request: siblingReq, Weight: 100},
	}

	selector := newWeightedSelector(tasks, func(n int) int { return 0 })
	selected := selector.Select()
	if selected == nil {
		t.Fatal("expected a selected task")
	}
	if selected.ProviderName != "shared" || selected.UpstreamModel != "failing-model" {
		t.Fatalf("selected = %s/%s, want shared/failing-model", selected.ProviderName, selected.UpstreamModel)
	}
	selectedTask := *selected

	result, err := strategy.executeTask(context.Background(), &selectedTask)
	if err != nil {
		t.Fatalf("executeTask failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindHard {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindHard)
	}

	selector.RemoveTask(selected)
	strategy.removeUnhealthySiblingTasks(selector, &selectedTask)

	if selector.Len() != 1 {
		t.Fatalf("remaining candidates = %d, want 1", selector.Len())
	}

	remaining := selector.Select()
	if remaining == nil {
		t.Fatal("expected remaining task")
	}
	if remaining.ProviderName != "other-provider" {
		t.Fatalf("remaining provider = %q, want %q", remaining.ProviderName, "other-provider")
	}

	otherResult, err := strategy.executeTask(context.Background(), remaining)
	if err != nil {
		t.Fatalf("other provider executeTask failed: %v", err)
	}
	defer otherResult.Response.Body.Close()

	if otherResult.FailureKind != FailureKindSuccess {
		t.Fatalf("other provider FailureKind = %q, want %q", otherResult.FailureKind, FailureKindSuccess)
	}

	if failingCalls != 1 {
		t.Fatalf("failing candidate called %d times, want 1", failingCalls)
	}
	if otherCalls != 1 {
		t.Fatalf("other candidate called %d times, want 1", otherCalls)
	}
	if siblingCalls != 0 {
		t.Fatalf("sibling candidate called %d times, want 0", siblingCalls)
	}
	if h.IsHealthy(health.MakeHealthKey("shared", "openai")) {
		t.Fatal("provider should be unhealthy after first 5xx at threshold 1")
	}
}

func TestLoadBalanceStrategy_SoftFailureReturnsLastSoftFailureResponse(t *testing.T) {
	callOrder := make([]string, 0, 2)
	softSrv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callOrder = append(callOrder, "soft-1")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft-1","choices":[{"message":{"role":"assistant","content":"blocked-1"},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv1.Close()

	softSrv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callOrder = append(callOrder, "soft-2")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft-2","choices":[{"message":{"role":"assistant","content":"blocked-2"},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv2.Close()

	providers := map[string]config.ProviderConfig{
		"soft-1": {
			Endpoint:  softSrv1.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"soft-2": {
			Endpoint:  softSrv2.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv1.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv2.URL, nil)
	tasks := []Task{
		{ProviderName: "soft-1", Provider: providers["soft-1"], UpstreamModel: "model", Request: req1, Weight: 1},
		{ProviderName: "soft-2", Provider: providers["soft-2"], UpstreamModel: "model", Request: req2, Weight: 100},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if len(callOrder) != 2 {
		t.Fatalf("call order len = %d, want 2", len(callOrder))
	}
	if result.Winner != callOrder[1] {
		t.Fatalf("winner = %q, want last soft-failure provider %q", result.Winner, callOrder[1])
	}
	if result.FailureKind != FailureKindSoft {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSoft)
	}
}

func TestLoadBalanceStrategy_SoftFailureDoesNotResetOrIncreaseHealthCount(t *testing.T) {
	rand.Seed(1)

	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft","choices":[{"message":{"role":"assistant","content":"blocked"},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv.Close()

	hardSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"upstream failed"}`))
	}))
	defer hardSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft-neutral": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)
	healthKey := health.MakeHealthKey("soft-neutral", "")

	h.ReportFailure(healthKey)

	softReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	softResult, err := strategy.Execute(context.Background(), []Task{{ProviderName: "soft-neutral", Provider: providers["soft-neutral"], UpstreamModel: "model", Request: softReq, Weight: 1}})
	if err != nil {
		t.Fatalf("soft Execute failed: %v", err)
	}
	softResult.Response.Body.Close()

	if !h.IsHealthy(healthKey) {
		t.Fatal("soft failure should not make provider unhealthy")
	}

	hardReq1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, hardSrv.URL, nil)
	hardResult1, err := strategy.Execute(context.Background(), []Task{{ProviderName: "soft-neutral", Provider: providers["soft-neutral"], UpstreamModel: "model", Request: hardReq1, Weight: 1}})
	if err != nil {
		t.Fatalf("first hard Execute failed: %v", err)
	}
	hardResult1.Response.Body.Close()

	if !h.IsHealthy(healthKey) {
		t.Fatal("soft failure should not add or reset failure count before threshold")
	}

	hardReq2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, hardSrv.URL, nil)
	hardResult2, err := strategy.Execute(context.Background(), []Task{{ProviderName: "soft-neutral", Provider: providers["soft-neutral"], UpstreamModel: "model", Request: hardReq2, Weight: 1}})
	if err != nil {
		t.Fatalf("second hard Execute failed: %v", err)
	}
	hardResult2.Response.Body.Close()

	if h.IsHealthy(healthKey) {
		t.Fatal("provider should become unhealthy exactly after the second real failure")
	}
}

// TestLoadBalanceStrategy_AttemptMetrics_HardFailureThenSuccess 验证硬失败后切换到成功的 attempt 指标
func TestLoadBalanceStrategy_AttemptMetrics_HardFailureThenSuccess(t *testing.T) {
	metrics.ResetForTest()

	var requestCount int32

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&requestCount, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"upstream failed"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer failSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&requestCount, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"upstream failed"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"fail": {
			Endpoint:  failSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, failSrv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)

	tasks := []Task{
		{ProviderName: "fail", Provider: providers["fail"], UpstreamModel: "fail-model", Request: req1, Weight: 100},
		{ProviderName: "success", Provider: providers["success"], UpstreamModel: "success-model", Request: req2, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute should succeed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "success" {
		t.Fatalf("winner = %q, want %q", result.Winner, "success")
	}

	// 无论随机先选中哪个 provider，第一次 attempt 都应硬失败，第二次 attempt 都应成功。
	hardFailCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"loadbalance", "fail", "fail-model", "", "openai", "hard_failure", "http_status", "500",
	)) + testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"loadbalance", "success", "success-model", "", "openai", "hard_failure", "http_status", "500",
	))
	if hardFailCount != 1 {
		t.Errorf("expected exactly 1 hard_failure attempt, got %f", hardFailCount)
	}

	successCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"loadbalance", "fail", "fail-model", "", "openai", "success", "", "",
	)) + testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"loadbalance", "success", "success-model", "", "openai", "success", "", "",
	))
	if successCount != 1 {
		t.Errorf("expected exactly 1 success attempt, got %f", successCount)
	}
}

// TestLoadBalanceStrategy_AttemptMetrics_SoftFailure 验证软失败的 attempt 指标
func TestLoadBalanceStrategy_AttemptMetrics_SoftFailure(t *testing.T) {
	metrics.ResetForTest()

	softSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"soft","choices":[{"message":{"role":"assistant","content":"I can't help with that request."},"finish_reason":"content_filter"}]}`))
	}))
	defer softSrv.Close()

	providers := map[string]config.ProviderConfig{
		"soft": {
			Endpoint:  softSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, softSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "soft", Provider: providers["soft"], UpstreamModel: "model", Request: req, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer result.Response.Body.Close()

	if result.FailureKind != FailureKindSoft {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSoft)
	}

	// 验证软失败 attempt 指标
	softCount := testutil.ToFloat64(metrics.GetProviderAttemptTotal().WithLabelValues(
		"loadbalance", "soft", "model", "", "openai", "soft_failure", "content_filter_finish_reason", "200",
	))
	if softCount != 1 {
		t.Errorf("expected 1 soft_failure attempt, got %f", softCount)
	}
}

// TestLoadBalanceStrategy_TokenHubQuotaError_FiltersUnhealthyProvider 验证 load-balance 模式下
// TokenHub provider 被标记为不健康后，filterHealthy 将其排除
func TestLoadBalanceStrategy_TokenHubQuotaError_FiltersUnhealthyProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"token-hub": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"fallback-prov": {
			Endpoint:  srv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)

	// 模拟 TokenHub 额度错误后标记为 1 小时不健康
	healthKey := health.MakeHealthKey("token-hub", "openai")
	h.MarkUnhealthyFor(healthKey, 1*time.Hour)

	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "token-hub", Provider: providers["token-hub"], OutboundProtocol: "openai", UpstreamModel: "model", Request: req, Weight: 10},
		{ProviderName: "fallback-prov", Provider: providers["fallback-prov"], OutboundProtocol: "openai", UpstreamModel: "model", Request: req, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute should succeed via fallback provider: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "fallback-prov" {
		t.Fatalf("winner = %q, want %q (token-hub should be filtered out as unhealthy)", result.Winner, "fallback-prov")
	}
	if result.FailureKind != FailureKindSuccess {
		t.Fatalf("FailureKind = %q, want %q", result.FailureKind, FailureKindSuccess)
	}
}

// TestLoadBalanceStrategy_AllHealthyProvidersDisabled 验证所有健康的 provider
// 均处于禁用时段时，loadbalance 返回 ErrNoProviderAvailable。
func TestLoadBalanceStrategy_AllHealthyProvidersDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"disabled-1": {
			Endpoint:           srv.URL,
			APIKey:             "test-key",
			Protocols: []string{"openai"},
			RateLimit:          config.RateLimitConfig{QPM: 0},
			DisabledTimeRanges: []string{"00:00-24:00"},
		},
		"disabled-2": {
			Endpoint:           srv.URL,
			APIKey:             "test-key",
			Protocols: []string{"openai"},
			RateLimit:          config.RateLimitConfig{QPM: 0},
			DisabledTimeRanges: []string{"00:00-24:00"},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "disabled-1", Provider: providers["disabled-1"], UpstreamModel: "model", Request: req, Weight: 1},
		{ProviderName: "disabled-2", Provider: providers["disabled-2"], UpstreamModel: "model", Request: req, Weight: 1},
	}

	_, err := strategy.Execute(context.Background(), tasks)
	if err != ErrNoProviderAvailable {
		t.Errorf("error = %v, want %v", err, ErrNoProviderAvailable)
	}
}

// TestLoadBalanceStrategy_FilterDisabledProvider 验证 loadbalance 的 filterHealthy
// 能正确过滤处于禁用时段的 provider，仅选择未禁用的健康 provider。
func TestLoadBalanceStrategy_FilterDisabledProvider(t *testing.T) {
	disabledSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"disabled"}`))
	}))
	defer disabledSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success"}`))
	}))
	defer successSrv.Close()

	providers := map[string]config.ProviderConfig{
		"disabled-prov": {
			Endpoint:           disabledSrv.URL,
			APIKey:             "test-key",
			Protocols: []string{"openai"},
			RateLimit:          config.RateLimitConfig{QPM: 0},
			DisabledTimeRanges: []string{"00:00-24:00"},
		},
		"enabled-prov": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocols: []string{"openai"},
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, 120*time.Second, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h, 500*time.Millisecond, 0)

	disabledReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, disabledSrv.URL, nil)
	successReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, successSrv.URL, nil)
	tasks := []Task{
		{ProviderName: "disabled-prov", Provider: providers["disabled-prov"], UpstreamModel: "model", Request: disabledReq, Weight: 100},
		{ProviderName: "enabled-prov", Provider: providers["enabled-prov"], UpstreamModel: "model", Request: successReq, Weight: 1},
	}

	result, err := strategy.Execute(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Execute should succeed with non-disabled provider: %v", err)
	}
	defer result.Response.Body.Close()

	if result.Winner != "enabled-prov" {
		t.Errorf("winner = %q, want %q (disabled provider should be filtered out)", result.Winner, "enabled-prov")
	}
}
