package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/metrics"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type recordingJobSink struct {
	chunks     []string
	final      bool
	errMsg     string
	statusCode int
}

func (s *recordingJobSink) SendResult(chunk string, final bool, statusCode int) error {
	if chunk != "" {
		s.chunks = append(s.chunks, chunk)
	}
	if final {
		s.final = true
		if statusCode > 0 {
			s.statusCode = statusCode
		}
	}
	return nil
}

func (s *recordingJobSink) SendError(message string) error {
	s.errMsg = message
	return nil
}

type orderJobSink struct {
	events []string
}

func (s *orderJobSink) SendResult(chunk string, final bool, statusCode int) error {
	if chunk != "" {
		s.events = append(s.events, "chunk")
	}
	if final {
		s.events = append(s.events, "final")
	}
	return nil
}

func (s *orderJobSink) SendError(message string) error {
	s.events = append(s.events, "error")
	return nil
}

func setupSpokeCascadeHandler(t *testing.T, providers map[string]config.ProviderConfig, groups []config.ModelGroupConfig, rules []config.RuleConfig, peer string) {
	t.Helper()
	// ExecuteCascadeJob 成功路径会写统计，测试需自包含初始化 recorder。
	testDBPath := filepath.Join(t.TempDir(), "cascade-job-stats.db")
	if err := stats.Init(testDBPath); err != nil {
		t.Fatalf("Init stats error: %v", err)
	}
	t.Cleanup(stats.Reset)

	cfg = &config.Config{
		Providers:   config.ProvidersConfig{Items: providers},
		ModelGroups: groups,
		Rules:       rules,
		Cascade: &config.SpokeCascadeConfig{
			Hub:   "https://hub.example",
			Token: "spoke-token",
			Peer:  peer,
		},
	}
	var err error
	resolver, err = model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	sched = scheduler.New(ratelimit.NewManager(providers), client, health.NewChecker(3, 30*time.Second), 500*time.Millisecond, 0, 0)
}

func TestCascadeJob_RejectsUnknownModel(t *testing.T) {
	setupSpokeCascadeHandler(t,
		map[string]config.ProviderConfig{
			"local": {Endpoint: "http://unused", APIKey: "k", Protocols: []string{"openai.chat"}},
		},
		[]config.ModelGroupConfig{{
			Name: "local-model", Models: config.ModelEntries{{Model: "local/gpt-4"}},
		}},
		nil,
		"",
	)

	sink := &recordingJobSink{}
	err := ExecuteCascadeJob(context.Background(), cascade.Frame{
		ID: "j1", Protocol: "openai.chat", Model: "missing-model",
		Body: json.RawMessage(`{"model":"missing-model","messages":[]}`),
	}, sink)
	if err != nil {
		t.Fatalf("ExecuteCascadeJob: %v", err)
	}
	if sink.errMsg == "" {
		t.Fatal("expected unknown model to be rejected")
	}
}

func TestCascadeJob_RejectsInternalExposure(t *testing.T) {
	internal := config.ExposureInternal
	setupSpokeCascadeHandler(t,
		map[string]config.ProviderConfig{
			"local": {Endpoint: "http://unused", APIKey: "k", Protocols: []string{"openai.chat"}},
		},
		[]config.ModelGroupConfig{{
			Name: "backend-internal", Exposure: &internal,
			Models: config.ModelEntries{{Model: "local/gpt-4"}},
		}},
		nil,
		"",
	)

	sink := &recordingJobSink{}
	err := ExecuteCascadeJob(context.Background(), cascade.Frame{
		ID: "j-internal-group", Protocol: "openai.chat", Model: "backend-internal",
		Body: json.RawMessage(`{"model":"backend-internal","messages":[]}`),
	}, sink)
	if err != nil {
		t.Fatalf("ExecuteCascadeJob: %v", err)
	}
	if sink.errMsg == "" {
		t.Fatal("expected internal group to be rejected by external call resolution")
	}

	// hidden redirect 指向 internal group 是合法替代：hub 可经该入口调用。
	hidden := config.ExposureHidden
	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer localSrv.Close()

	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"local": {Endpoint: localSrv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{{
			Name: "backend-internal", Exposure: &internal,
			Models: config.ModelEntries{{Model: "local/gpt-4"}},
		}},
		Redirect: config.RedirectConfigs{{
			Source: "hub-entry", Target: "backend-internal", Exposure: &hidden,
		}},
		Cascade: &config.SpokeCascadeConfig{Hub: "https://hub.example", Token: "spoke-token"},
	}
	var err2 error
	resolver, err2 = model.NewResolver(testCfg)
	if err2 != nil {
		t.Fatalf("NewResolver: %v", err2)
	}
	cfg = testCfg
	client := provider.NewClient(testCfg.Providers.Items, 120*time.Second, 0, 0)
	sched = scheduler.New(ratelimit.NewManager(testCfg.Providers.Items), client, health.NewChecker(3, 30*time.Second), 500*time.Millisecond, 0, 0)

	okSink := &recordingJobSink{}
	if err := ExecuteCascadeJob(context.Background(), cascade.Frame{
		ID: "j-hidden-alias", Protocol: "openai.chat", Model: "hub-entry",
		Body: json.RawMessage(`{"model":"hub-entry","messages":[{"role":"user","content":"hi"}]}`),
	}, okSink); err != nil {
		t.Fatalf("ExecuteCascadeJob: %v", err)
	}
	if okSink.errMsg != "" {
		t.Fatalf("unexpected error for hidden redirect: %s", okSink.errMsg)
	}
	if !okSink.final {
		t.Fatal("expected successful final frame for hidden redirect")
	}
}

func TestCascadeJob_BodyModelCannotRedirect(t *testing.T) {
	var seenModel atomic.Value
	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req dto.ChatCompletionRequest
		_ = json.Unmarshal(body, &req)
		seenModel.Store(req.Model)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer localSrv.Close()

	setupSpokeCascadeHandler(t,
		map[string]config.ProviderConfig{
			"local": {Endpoint: localSrv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
		},
		[]config.ModelGroupConfig{{
			Name: "secure-model", Models: config.ModelEntries{{Model: "local/gpt-4"}},
		}},
		nil,
		"",
	)

	sink := &recordingJobSink{}
	body, _ := json.Marshal(dto.ChatCompletionRequest{
		Model:    "other-model",
		Messages: []dto.Message{{Role: "user", Content: "hi"}},
	})
	err := ExecuteCascadeJob(context.Background(), cascade.Frame{
		ID: "j2", Protocol: "openai.chat", Model: "secure-model", Body: body,
	}, sink)
	if err != nil {
		t.Fatalf("ExecuteCascadeJob: %v", err)
	}
	if sink.errMsg != "" {
		t.Fatalf("unexpected error: %s", sink.errMsg)
	}
	if got := seenModel.Load().(string); got != "gpt-4" {
		t.Fatalf("upstream model = %q, want gpt-4 from secure-model plan", got)
	}
}

func TestCascadeJob_SkipsPeerLeafNoHTTP(t *testing.T) {
	var peerHits int
	var localHits int
	peerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer peerSrv.Close()
	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localHits++
		if got := r.Header.Get(cascade.HopHeader); got != "spoke-token" {
			t.Errorf("hop header = %q, want spoke-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"local"}}]}`))
	}))
	defer localSrv.Close()

	setupSpokeCascadeHandler(t,
		map[string]config.ProviderConfig{
			"cloud-peer": {Endpoint: peerSrv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
			"local":      {Endpoint: localSrv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
		},
		[]config.ModelGroupConfig{{
			Name:   "job-model",
			Mode:   "failover",
			Models: config.ModelEntries{{Model: "cloud-peer/gpt-4"}, {Model: "local/gpt-4"}},
		}},
		nil,
		"cloud-peer",
	)

	sink := &recordingJobSink{}
	err := ExecuteCascadeJob(context.Background(), cascade.Frame{
		ID: "j3", Protocol: "openai.chat", Model: "job-model",
		Body: json.RawMessage(`{"model":"ignored","messages":[{"role":"user","content":"hi"}]}`),
	}, sink)
	if err != nil {
		t.Fatalf("ExecuteCascadeJob: %v", err)
	}
	if peerHits != 0 {
		t.Fatalf("peer hits = %d, want 0", peerHits)
	}
	if localHits != 1 {
		t.Fatalf("local hits = %d, want 1", localHits)
	}
}

func TestMaterialize_NoOriginStillSchedulesPeer(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"cloud-peer": {Endpoint: "http://peer", APIKey: "k", Protocols: []string{"openai.chat"}},
		"local":      {Endpoint: "http://local", APIKey: "k", Protocols: []string{"openai.chat"}},
	}
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{{
			Name:   "mac-model",
			Mode:   "failover",
			Models: config.ModelEntries{{Model: "cloud-peer/gpt-4"}, {Model: "local/gpt-4"}},
		}},
		Cascade: &config.SpokeCascadeConfig{Peer: "cloud-peer"},
	}
	r, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	plan, err := r.Resolve("mac-model")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	inboundCodec, _ := codec.Get(codec.FormatOpenAIChat)
	req := &dto.ChatCompletionRequest{Model: "mac-model", Messages: []dto.Message{{Role: "user", Content: "x"}}}
	root, err := materializePlan(context.Background(), plan.Plan, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        req,
		ModelGroup:    plan.ModelGroup,
		ClientModel:   "mac-model",
		CascadePeer:   "cloud-peer",
	})
	if err != nil {
		t.Fatalf("materializePlan: %v", err)
	}
	if len(root.Children) == 0 && len(root.Task.ProviderName) == 0 {
		// failover group becomes nested - check leaves
	}
	foundPeer := false
	walkRunNodes(root, func(n *scheduler.RunNode) {
		if n.IsLeaf && n.Task.ProviderName == "cloud-peer" && n.Unschedulable {
			t.Fatal("peer leaf must remain schedulable without cascade origin")
		}
		if n.IsLeaf && n.Task.ProviderName == "cloud-peer" {
			foundPeer = true
		}
	})
	if !foundPeer {
		t.Fatal("expected peer leaf in plan")
	}
}

func TestMaterialize_CascadeOriginSkipsPeer(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"cloud-peer": {Endpoint: "http://peer", APIKey: "k", Protocols: []string{"openai.chat"}},
		"local":      {Endpoint: "http://local", APIKey: "k", Protocols: []string{"openai.chat"}},
	}
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{{
			Name:   "job-model",
			Mode:   "failover",
			Models: config.ModelEntries{{Model: "cloud-peer/gpt-4"}, {Model: "local/gpt-4"}},
		}},
	}
	r, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	plan, err := r.Resolve("job-model")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	inboundCodec, _ := codec.Get(codec.FormatOpenAIChat)
	req := &dto.ChatCompletionRequest{Model: "job-model", Messages: []dto.Message{{Role: "user", Content: "x"}}}
	ctx := cascade.WithCascadeOrigin(context.Background())
	root, err := materializePlan(ctx, plan.Plan, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        req,
		ModelGroup:    plan.ModelGroup,
		ClientModel:   "job-model",
		CascadePeer:   "cloud-peer",
	})
	if err != nil {
		t.Fatalf("materializePlan: %v", err)
	}
	var peerUnsched bool
	walkRunNodes(root, func(n *scheduler.RunNode) {
		if n.IsLeaf && n.Task.ProviderName == "cloud-peer" {
			peerUnsched = n.Unschedulable
		}
	})
	if !peerUnsched {
		t.Fatal("peer leaf must be unschedulable under cascade origin")
	}
}

func TestMaterialize_HopSkipsCascadeEnabledLeaves(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"corp-dev": {
			Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
			Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
		},
		"cloud-provider": {
			Endpoint:  "http://cloud",
			APIKey:    "k",
			Protocols: []string{"openai.chat"},
		},
	}
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{{
			Name:   "hub-model",
			Mode:   "failover",
			Models: config.ModelEntries{{Model: "corp-dev/offered"}, {Model: "cloud-provider/gpt-4"}},
		}},
	}
	r, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	plan, err := r.Resolve("hub-model")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	inboundCodec, _ := codec.Get(codec.FormatOpenAIChat)
	req := &dto.ChatCompletionRequest{Model: "hub-model", Messages: []dto.Message{{Role: "user", Content: "x"}}}
	ctx := cascade.WithCascadeHop(context.Background(), "hub-secret")
	root, err := materializePlan(ctx, plan.Plan, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        req,
		ModelGroup:    plan.ModelGroup,
		ClientModel:   "hub-model",
	})
	if err != nil {
		t.Fatalf("materializePlan: %v", err)
	}

	var cascadeUnsched, cloudSched bool
	walkRunNodes(root, func(n *scheduler.RunNode) {
		if !n.IsLeaf {
			return
		}
		switch n.Task.ProviderName {
		case "corp-dev":
			cascadeUnsched = n.Unschedulable
		case "cloud-provider":
			cloudSched = !n.Unschedulable
		}
	})
	if !cascadeUnsched {
		t.Fatal("corp-dev cascade leaf must be unschedulable when hop is present")
	}
	if !cloudSched {
		t.Fatal("cloud-provider leaf must remain schedulable when hop skips cascade")
	}
}

func TestMaterialize_ForgedHopDoesNotSkipCascade(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"corp-dev": {
			Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
			Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
		},
	}
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{{
			Name:   "hub-model",
			Mode:   "failover",
			Models: config.ModelEntries{{Model: "corp-dev/offered"}},
		}},
	}
	r, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	plan, err := r.Resolve("hub-model")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	inboundCodec, _ := codec.Get(codec.FormatOpenAIChat)
	req := &dto.ChatCompletionRequest{Model: "hub-model", Messages: []dto.Message{{Role: "user", Content: "x"}}}
	ctx := cascade.WithCascadeHop(context.Background(), "loop-guard")
	root, err := materializePlan(ctx, plan.Plan, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        req,
		ModelGroup:    plan.ModelGroup,
		ClientModel:   "hub-model",
	})
	if err != nil {
		t.Fatalf("materializePlan: %v", err)
	}
	var cascadeUnsched bool
	walkRunNodes(root, func(n *scheduler.RunNode) {
		if n.IsLeaf && n.Task.ProviderName == "corp-dev" {
			cascadeUnsched = n.Unschedulable
		}
	})
	if cascadeUnsched {
		t.Fatal("forged hop must not unschedulable cascade leaves")
	}
}

func TestCascadeJob_InternalExposureAliasRejected(t *testing.T) {
	internal := config.ExposureInternal
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"local": {Endpoint: "http://unused", APIKey: "k", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{{
			Name: "backend-internal", Exposure: &internal,
			Models: config.ModelEntries{{Model: "local/gpt-4"}},
		}},
		Redirect: config.RedirectConfigs{{
			Source: "hub-alias", Target: "backend-internal", Exposure: &internal,
		}},
		Cascade: &config.SpokeCascadeConfig{
			Hub: "https://hub.example", Token: "spoke-token", Peer: "cloud-peer",
		},
	}
	var err error
	resolver, err = model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	cfg = testCfg
	client := provider.NewClient(testCfg.Providers.Items, 120*time.Second, 0, 0)
	sched = scheduler.New(ratelimit.NewManager(testCfg.Providers.Items), client, health.NewChecker(3, 30*time.Second), 500*time.Millisecond, 0, 0)

	sink := &recordingJobSink{}
	err = ExecuteCascadeJob(context.Background(), cascade.Frame{
		ID: "j-internal", Protocol: "openai.chat", Model: "hub-alias",
		Body: json.RawMessage(`{"model":"ignored","messages":[{"role":"user","content":"hi"}]}`),
	}, sink)
	if err != nil {
		t.Fatalf("ExecuteCascadeJob: %v", err)
	}
	// internal redirect 不得通过 cascade job 绕过 exposure 直调限制。
	if sink.errMsg == "" {
		t.Fatal("expected internal redirect alias to be rejected")
	}
}

func walkRunNodes(n *scheduler.RunNode, fn func(*scheduler.RunNode)) {
	if n == nil {
		return
	}
	fn(n)
	for _, ch := range n.Children {
		walkRunNodes(ch, fn)
	}
}

func TestChat_CascadeHopSkipsCascadeProvider(t *testing.T) {
	var cloudHits int
	cloudSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"cloud"}}]}`))
	}))
	defer cloudSrv.Close()

	hub := cascade.NewHub(cascade.HubConfig{ProviderName: "corp-dev", Token: "hub-secret"})
	providers := map[string]config.ProviderConfig{
		"corp-dev": {
			Protocols: []string{"openai.chat", "openai.responses", "anthropic.messages"},
			Cascade:   &config.ProviderCascadeConfig{Enabled: true, Token: "hub-secret"},
		},
		"cloud-provider": {
			Endpoint:  cloudSrv.URL,
			APIKey:    "k",
			Protocols: []string{"openai.chat"},
		},
	}
	testCfg := &config.Config{
		Providers: config.ProvidersConfig{Items: providers},
		ModelGroups: []config.ModelGroupConfig{{
			Name:   "hub-model",
			Mode:   "failover",
			Models: config.ModelEntries{{Model: "corp-dev/offered"}, {Model: "cloud-provider/gpt-4"}},
		}},
	}
	r, err := model.NewResolver(testCfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	client := provider.NewClient(providers, 120*time.Second, 0, 0)
	registry := cascade.NewHubRegistry()
	registry.Set(hub)
	client.SetCascadeHubRegistry(registry)
	sched = scheduler.New(ratelimit.NewManager(providers), client, health.NewChecker(3, 30*time.Second), 500*time.Millisecond, 0, 0)
	cfg = testCfg
	resolver = r

	router := gin.New()
	router.POST("/v1/chat/completions", Chat)

	body, _ := json.Marshal(dto.ChatCompletionRequest{
		Model:    "hub-model",
		Messages: []dto.Message{{Role: "user", Content: "hi"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(cascade.HopHeader, "hub-secret")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if cloudHits != 1 {
		t.Fatalf("cloud hits = %d, want 1 (cascade leaf skipped)", cloudHits)
	}
}

func TestCascadeJob_Stream2xxFirstChunkBeforeFinal(t *testing.T) {
	chatSSE := "" +
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: [DONE]\n\n"

	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(chatSSE))
	}))
	defer localSrv.Close()

	setupSpokeCascadeHandler(t,
		map[string]config.ProviderConfig{
			"local": {Endpoint: localSrv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
		},
		[]config.ModelGroupConfig{{
			Name: "stream-model", Models: config.ModelEntries{{Model: "local/gpt-4"}},
		}},
		nil,
		"",
	)

	stream := true
	sink := &orderJobSink{}
	err := ExecuteCascadeJob(context.Background(), cascade.Frame{
		ID: "j-stream-order", Protocol: "openai.chat", Model: "stream-model", Stream: &stream,
		Body: json.RawMessage(`{"model":"stream-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, sink)
	if err != nil {
		t.Fatalf("ExecuteCascadeJob: %v", err)
	}
	finalIdx := -1
	chunkIdx := -1
	for i, ev := range sink.events {
		switch ev {
		case "final":
			finalIdx = i
		case "chunk":
			if chunkIdx < 0 {
				chunkIdx = i
			}
		}
	}
	if chunkIdx < 0 {
		t.Fatalf("events = %v, want at least one chunk before final", sink.events)
	}
	if finalIdx < 0 {
		t.Fatalf("events = %v, want final", sink.events)
	}
	if chunkIdx > finalIdx {
		t.Fatalf("events = %v, want chunk before final", sink.events)
	}
}

func TestCascadeJob_StreamRequestUpstream4xxNonStreamResult(t *testing.T) {
	errBody := `{"error":{"message":"model overloaded","type":"server_error"}}`
	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(errBody))
	}))
	defer localSrv.Close()

	setupSpokeCascadeHandler(t,
		map[string]config.ProviderConfig{
			"local": {Endpoint: localSrv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
		},
		[]config.ModelGroupConfig{{
			Name: "err-model", Models: config.ModelEntries{{Model: "local/gpt-4"}},
		}},
		nil,
		"",
	)

	stream := true
	sink := &orderJobSink{}
	err := ExecuteCascadeJob(context.Background(), cascade.Frame{
		ID: "j-stream-err", Protocol: "openai.chat", Model: "err-model", Stream: &stream,
		Body: json.RawMessage(`{"model":"err-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, sink)
	if err != nil {
		t.Fatalf("ExecuteCascadeJob: %v", err)
	}
	for _, ev := range sink.events {
		if ev == "error" {
			t.Fatalf("events = %v, want mapped result not job error", sink.events)
		}
	}
	if len(sink.events) != 2 || sink.events[0] != "chunk" || sink.events[1] != "final" {
		t.Fatalf("events = %v, want single non-stream error body then final", sink.events)
	}
}

func TestWriteCascadeJobUpstreamErrorBody(t *testing.T) {
	sink := &recordingJobSink{}
	body := []byte(`{"error":{"message":"model overloaded"}}`)
	if err := writeCascadeJobUpstreamErrorBody(sink, false, http.StatusBadGateway, body); err != nil {
		t.Fatalf("writeCascadeJobUpstreamErrorBody: %v", err)
	}
	out := strings.Join(sink.chunks, "")
	if !strings.Contains(out, "model overloaded") {
		t.Fatalf("body = %q", out)
	}
	if sink.statusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", sink.statusCode)
	}
}

func TestCascadeJob_UpstreamErrorReturnsResultNotJobError(t *testing.T) {
	errBody := `{"error":{"message":"model overloaded","type":"server_error"}}`
	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(errBody))
	}))
	defer localSrv.Close()

	setupSpokeCascadeHandler(t,
		map[string]config.ProviderConfig{
			"local": {Endpoint: localSrv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
		},
		[]config.ModelGroupConfig{{
			Name: "err-model", Models: config.ModelEntries{{Model: "local/gpt-4"}},
		}},
		nil,
		"",
	)

	sink := &recordingJobSink{}
	err := ExecuteCascadeJob(context.Background(), cascade.Frame{
		ID: "j-err", Protocol: "openai.chat", Model: "err-model",
		Body: json.RawMessage(`{"model":"ignored","messages":[{"role":"user","content":"hi"}]}`),
	}, sink)
	if err != nil {
		t.Fatalf("ExecuteCascadeJob: %v", err)
	}
	if sink.errMsg != "" {
		t.Fatalf("unexpected job error frame: %q", sink.errMsg)
	}
	if !sink.final {
		t.Fatal("expected final result frame")
	}
	if sink.statusCode != http.StatusBadGateway {
		t.Fatalf("statusCode = %d, want %d", sink.statusCode, http.StatusBadGateway)
	}
	out := strings.Join(sink.chunks, "")
	if !strings.Contains(out, "model overloaded") {
		t.Fatalf("result body = %q, want upstream error payload", out)
	}
}

func TestCascadeJob_ReturnPathChatSSENotResponses(t *testing.T) {
	responsesSSE := "" +
		"event: response.created\n" +
		`data: {"type":"response.created"}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed"}` + "\n\n"

	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responsesSSE))
	}))
	defer localSrv.Close()

	setupSpokeCascadeHandler(t,
		map[string]config.ProviderConfig{
			"local": {
				Endpoint:  localSrv.URL,
				APIKey:    "k",
				Protocols: []string{"openai.chat", "openai.responses"},
				Endpoints: []config.EndpointConfig{
					{URL: localSrv.URL, Protocols: []string{"openai.responses"}},
				},
			},
		},
		[]config.ModelGroupConfig{{
			Name: "stream-model", Models: config.ModelEntries{{Model: "local/gpt-4"}},
		}},
		[]config.RuleConfig{{
			Match:  config.RuleMatch{UpstreamModel: &config.RuleCondition{Op: "equals", Value: "local/gpt-4"}},
			Action: config.RuleAction{Protocol: "openai.responses"},
		}},
		"",
	)

	stream := true
	sink := &recordingJobSink{}
	err := ExecuteCascadeJob(context.Background(), cascade.Frame{
		ID: "j-stream", Protocol: "openai.chat", Model: "stream-model", Stream: &stream,
		Body: json.RawMessage(`{"model":"wrong","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, sink)
	if err != nil {
		t.Fatalf("ExecuteCascadeJob: %v", err)
	}
	if sink.errMsg != "" {
		t.Fatalf("unexpected error: %s", sink.errMsg)
	}
	if !sink.final {
		t.Fatal("expected final result frame")
	}
	out := strings.Join(sink.chunks, "")
	if strings.Contains(out, "response.output_text") {
		t.Fatalf("output still responses-shaped: %s", out)
	}
	if !strings.Contains(out, "data:") || !strings.Contains(out, "choices") {
		t.Fatalf("output not chat SSE shaped: %s", out)
	}
}

func TestCascadeJob_RecordsStatsWithCascadeKey(t *testing.T) {
	metrics.ResetForTest()

	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"1","object":"chat.completion","model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"hello there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`))
	}))
	defer localSrv.Close()

	setupSpokeCascadeHandler(t,
		map[string]config.ProviderConfig{
			"local": {Endpoint: localSrv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
		},
		[]config.ModelGroupConfig{{
			Name: "local-model", Models: config.ModelEntries{{Model: "local/gpt-4"}},
		}},
		nil,
		"olife",
	)

	sink := &recordingJobSink{}
	if err := ExecuteCascadeJob(context.Background(), cascade.Frame{
		ID: "j-stats", Protocol: "openai.chat", Model: "local-model",
		Body: json.RawMessage(`{"model":"local-model","messages":[{"role":"user","content":"hi"}]}`),
	}, sink); err != nil {
		t.Fatalf("ExecuteCascadeJob: %v", err)
	}
	if sink.errMsg != "" {
		t.Fatalf("unexpected error: %s", sink.errMsg)
	}

	keys, err := stats.GetQuerier().QueryByKeys("", "")
	if err != nil {
		t.Fatalf("QueryByKeys: %v", err)
	}
	got, ok := keys["cascade:olife"]
	if !ok {
		t.Fatalf("cascade job not recorded under cascade:olife key, got keys: %v", keys)
	}
	if got.RequestCount != 1 {
		t.Fatalf("request_count = %d, want 1", got.RequestCount)
	}
	if got.InputTokens <= 0 {
		t.Fatalf("input_tokens = %d, want > 0", got.InputTokens)
	}
	if _, ok := keys["grok-build"]; ok {
		t.Fatal("cascade job must not be attributed to a hub-side key")
	}

	byProvider, err := stats.GetQuerier().QueryByProviderOnly("", "")
	if err != nil {
		t.Fatalf("QueryByProviderOnly: %v", err)
	}
	if _, ok := byProvider["local"]; !ok {
		t.Fatalf("provider dimension missing, got: %v", byProvider)
	}

	userModels, err := stats.GetQuerier().QueryByUserModels("", "")
	if err != nil {
		t.Fatalf("QueryByUserModels: %v", err)
	}
	if _, ok := userModels["local-model"]; !ok {
		t.Fatalf("user_model dimension missing, got: %v", userModels)
	}

	if n := testutil.ToFloat64(metrics.GetRequestTotal().WithLabelValues(
		"openai.chat", "openai.chat", "local", "gpt-4", "local-model", "cascade:olife", "success",
	)); n != 1 {
		t.Fatalf("request_total{key=cascade:olife,status=success} = %v, want 1", n)
	}
}
