package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
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
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h)

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
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)

	// 标记为不健康
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))
	h.ReportFailure(health.MakeHealthKey("test", ""))

	strategy := NewLoadBalanceStrategy(client, rl, h)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	tasks := []Task{
		{ProviderName: "test", Provider: providers["test"], UpstreamModel: "model", Request: req, Weight: 1},
	}

	_, err := strategy.Execute(context.Background(), tasks)
	if err != ErrNoHealthyProvider {
		t.Errorf("error = %v, want %v", err, ErrNoHealthyProvider)
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
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h)

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
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"success": {
			Endpoint:  successSrv.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h)

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
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
		"b": {
			Endpoint:  srvB.URL,
			APIKey:    "test-key",
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h)

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
		"coding-plan": {Endpoint: srvCP.URL, APIKey: "k", Protocol: "openai", RateLimit: config.RateLimitConfig{QPM: 0}},
		"token-hub":   {Endpoint: srvTH.URL, APIKey: "k", Protocol: "openai", RateLimit: config.RateLimitConfig{QPM: 0}},
		"venus-51":    {Endpoint: srvV51.URL, APIKey: "k", Protocol: "openai", RateLimit: config.RateLimitConfig{QPM: 0}},
		"venus-5":     {Endpoint: srvV5.URL, APIKey: "k", Protocol: "openai", RateLimit: config.RateLimitConfig{QPM: 0}},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h)

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
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 1},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h)

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
			Protocol: "openai",
			UpstreamModels: []config.UpstreamModelConfig{
				{Model: "model", QPM: 60000},
			},
		},
		"b": {
			Endpoint: srvB.URL,
			APIKey:   "test-key",
			Protocol: "openai",
			UpstreamModels: []config.UpstreamModelConfig{
				{Model: "model", QPM: 60000},
			},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h)

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
	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h)

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
			Protocol:  "openai",
			RateLimit: config.RateLimitConfig{QPM: 0},
		},
	}

	client := provider.NewClient(providers, "")
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	strategy := NewLoadBalanceStrategy(client, rl, h)

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
