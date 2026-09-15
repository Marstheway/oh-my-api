package scheduler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/health"
	"github.com/Marstheway/oh-my-api/internal/provider"
	"github.com/Marstheway/oh-my-api/internal/ratelimit"
)

func TestStickyStore_LookupMiss(t *testing.T) {
	store := NewStickyStore()
	now := time.Now()
	affinity := affinityKey("test-key", "test-group")

	candidate, ok := store.Lookup(affinity, now)
	if ok {
		t.Errorf("expected lookup miss, got candidate %s", candidate)
	}
}

func TestStickyStore_RememberAndLookup(t *testing.T) {
	store := NewStickyStore()
	now := time.Now()
	affinity := affinityKey("test-key", "test-group")
	candidate := "provider/model"

	store.Remember(affinity, candidate, now, 10*time.Minute)

	got, ok := store.Lookup(affinity, now)
	if !ok {
		t.Fatal("expected lookup hit, got miss")
	}
	if got != candidate {
		t.Errorf("expected candidate %s, got %s", candidate, got)
	}
}

func TestStickyStore_LookupExpired(t *testing.T) {
	store := NewStickyStore()
	now := time.Now()
	affinity := affinityKey("test-key", "test-group")
	candidate := "provider/model"

	store.Remember(affinity, candidate, now, 10*time.Minute)

	future := now.Add(15 * time.Minute)
	got, ok := store.Lookup(affinity, future)
	if ok {
		t.Errorf("expected lookup miss after expiry, got candidate %s", got)
	}
}

func TestStickyStore_PrunesExpiredRecordsOnLaterAccess(t *testing.T) {
	store := NewStickyStore()
	now := time.Now()
	expired := affinityKey("expired-key", "group")
	active := affinityKey("active-key", "group")

	store.Remember(expired, "provider/expired", now, time.Minute)
	store.Remember(active, "provider/active", now, 2*time.Hour)

	if _, ok := store.Lookup(active, now.Add(time.Minute+stickyPruneInterval)); !ok {
		t.Fatal("expected active record to remain available")
	}
	if _, ok := store.stickies[expired]; ok {
		t.Fatal("expected expired record to be pruned without a direct lookup")
	}
	if _, ok := store.stickies[active]; !ok {
		t.Fatal("expected active record to remain after pruning")
	}
}

func TestStickyStore_DifferentAffinities(t *testing.T) {
	store := NewStickyStore()
	now := time.Now()

	affinity1 := affinityKey("key1", "group")
	affinity2 := affinityKey("key2", "group")
	candidate1 := "provider1/model"
	candidate2 := "provider2/model"

	store.Remember(affinity1, candidate1, now, 10*time.Minute)
	store.Remember(affinity2, candidate2, now, 10*time.Minute)

	got1, ok1 := store.Lookup(affinity1, now)
	got2, ok2 := store.Lookup(affinity2, now)

	if !ok1 || got1 != candidate1 {
		t.Errorf("affinity1: expected %s, got %s (ok=%v)", candidate1, got1, ok1)
	}
	if !ok2 || got2 != candidate2 {
		t.Errorf("affinity2: expected %s, got %s (ok=%v)", candidate2, got2, ok2)
	}
}

func TestStickyStore_IncrSuccess(t *testing.T) {
	store := NewStickyStore()
	group := "test-group"
	c1 := "provider1/model"
	c2 := "provider2/model"

	store.IncrSuccess(group, c1)
	store.IncrSuccess(group, c1)
	store.IncrSuccess(group, c2)

	counts := store.GetSuccessCounts(group)
	if counts[c1] != 2 {
		t.Errorf("expected c1 count 2, got %d", counts[c1])
	}
	if counts[c2] != 1 {
		t.Errorf("expected c2 count 1, got %d", counts[c2])
	}
}

func TestStickyStore_PickDeficit_Basic(t *testing.T) {
	store := NewStickyStore()
	group := "test-group"
	c1 := "provider1/model"
	c2 := "provider2/model"

	store.IncrSuccess(group, c1)
	store.IncrSuccess(group, c1)
	store.IncrSuccess(group, c1)
	store.IncrSuccess(group, c2)

	candidates := []string{c1, c2}
	weights := map[string]int{c1: 3, c2: 1}

	// c1: 3/3 = 1.0, c2: 1/1 = 1.0，weight 更大选 c1
	picked := store.PickDeficit(group, candidates, weights)
	if picked != c1 {
		t.Errorf("expected c1 (higher weight breaks tie), got %s", picked)
	}
}

func TestStickyStore_PickDeficit_ChoosesLowerScore(t *testing.T) {
	store := NewStickyStore()
	group := "test-group"
	c1 := "provider1/model"
	c2 := "provider2/model"

	for i := 0; i < 5; i++ {
		store.IncrSuccess(group, c1)
	}
	store.IncrSuccess(group, c2)

	candidates := []string{c1, c2}
	weights := map[string]int{c1: 1, c2: 1}

	picked := store.PickDeficit(group, candidates, weights)
	if picked != c2 {
		t.Errorf("expected c2 (lower score), got %s", picked)
	}
}

func TestStickyStore_PickDeficit_WeightTieBreak(t *testing.T) {
	store := NewStickyStore()
	group := "test-group"
	c1 := "provider1/model"
	c2 := "provider2/model"

	store.IncrSuccess(group, c1)
	store.IncrSuccess(group, c2)

	candidates := []string{c1, c2}
	weights := map[string]int{c1: 1, c2: 3}

	picked := store.PickDeficit(group, candidates, weights)
	if picked != c2 {
		t.Errorf("expected c2 to be picked (higher weight), got %s", picked)
	}
}

func TestStickyStore_PickDeficit_OrderTieBreak(t *testing.T) {
	store := NewStickyStore()
	group := "test-group"
	c1 := "provider1/model"
	c2 := "provider2/model"

	candidates := []string{c1, c2}
	weights := map[string]int{c1: 1, c2: 1}

	picked := store.PickDeficit(group, candidates, weights)
	if picked != c1 {
		t.Errorf("expected c1 to be picked (first in order), got %s", picked)
	}
}

func TestStickyStore_RememberOverwrites(t *testing.T) {
	store := NewStickyStore()
	now := time.Now()
	affinity := affinityKey("key", "group")

	store.Remember(affinity, "provider1/model", now, 10*time.Minute)
	store.Remember(affinity, "provider2/model", now, 10*time.Minute)

	got, ok := store.Lookup(affinity, now)
	if !ok {
		t.Fatal("expected lookup hit")
	}
	if got != "provider2/model" {
		t.Errorf("expected provider2/model, got %s", got)
	}
}

func TestStickyStore_PickDeficit_ZeroWeight(t *testing.T) {
	store := NewStickyStore()
	group := "test-group"
	c1 := "provider1/model"
	c2 := "provider2/model"

	candidates := []string{c1, c2}
	weights := map[string]int{c1: 0, c2: 0}

	picked := store.PickDeficit(group, candidates, weights)
	if picked != c1 {
		t.Errorf("expected c1 to be picked, got %s", picked)
	}
}

func TestGetStickyStore_Singleton(t *testing.T) {
	resetStickyStore()
	s1 := GetStickyStore()
	s2 := GetStickyStore()
	if s1 != s2 {
		t.Error("expected same singleton instance")
	}
	resetStickyStore()
}

func TestParseStickyMeta(t *testing.T) {
	meta, err := ParseStickyMeta(false, "10m")
	if err != nil || meta != nil {
		t.Fatalf("disabled should return nil meta, got %+v err=%v", meta, err)
	}

	meta, err = ParseStickyMeta(true, "")
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || !meta.Enabled || meta.IdleTimeout != 10*time.Minute {
		t.Fatalf("expected default 10m, got %+v", meta)
	}

	meta, err = ParseStickyMeta(true, "30s")
	if err != nil || meta.IdleTimeout != 30*time.Second {
		t.Fatalf("expected 30s, got %+v err=%v", meta, err)
	}

	if _, err := ParseStickyMeta(true, "not-a-duration"); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err := ParseStickyMeta(true, "0s"); err == nil {
		t.Fatal("expected non-positive error")
	}
}

func TestPickStickyOrDeficit_HitAndMiss(t *testing.T) {
	store := NewStickyStore()
	now := time.Now()
	group := "g"
	keyName := "k"
	sticky := &StickyMeta{Enabled: true, IdleTimeout: 10 * time.Minute}
	remaining := []string{"a/m", "b/m"}
	weights := map[string]int{"a/m": 1, "b/m": 1}

	// miss → deficit 顺序选 a/m
	picked := pickStickyOrDeficit(store, group, keyName, sticky, now, remaining, weights)
	if picked != "a/m" {
		t.Fatalf("miss expected a/m, got %s", picked)
	}

	store.Remember(affinityKey(keyName, group), "b/m", now, sticky.IdleTimeout)
	picked = pickStickyOrDeficit(store, group, keyName, sticky, now, remaining, weights)
	if picked != "b/m" {
		t.Fatalf("hit expected b/m, got %s", picked)
	}

	// sticky 指向不在 remaining 的候选 → deficit
	picked = pickStickyOrDeficit(store, group, keyName, sticky, now, []string{"a/m"}, weights)
	if picked != "a/m" {
		t.Fatalf("stale sticky expected a/m, got %s", picked)
	}

	// key 空不查 sticky
	picked = pickStickyOrDeficit(store, group, "", sticky, now, remaining, weights)
	if picked != "a/m" {
		t.Fatalf("empty key expected deficit a/m, got %s", picked)
	}
}

func TestRecordStickySuccess(t *testing.T) {
	store := NewStickyStore()
	now := time.Now()
	recordStickySuccess(store, "g", "k", "p/m", time.Minute, now)
	if store.GetSuccessCounts("g")["p/m"] != 1 {
		t.Fatal("expected incr")
	}
	got, ok := store.Lookup(affinityKey("k", "g"), now)
	if !ok || got != "p/m" {
		t.Fatalf("expected remember, got %s ok=%v", got, ok)
	}

	// 无 key：只计数不写 sticky
	store2 := NewStickyStore()
	recordStickySuccess(store2, "g", "", "p/m", time.Minute, now)
	if store2.GetSuccessCounts("g")["p/m"] != 1 {
		t.Fatal("expected incr without key")
	}
	if _, ok := store2.Lookup(affinityKey("", "g"), now); ok {
		t.Fatal("should not remember without key")
	}
}

func okChatBody() string {
	return `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`
}

func TestExecuteSticky_HitSameCandidate(t *testing.T) {
	resetStickyStore()
	defer resetStickyStore()

	aSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okChatBody())
	}))
	defer aSrv.Close()
	bSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okChatBody())
	}))
	defer bSrv.Close()

	providers := map[string]config.ProviderConfig{
		"prov-a": {Endpoint: aSrv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
		"prov-b": {Endpoint: bSrv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
	}
	client := provider.NewClient(providers, 50*time.Millisecond, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h, 500*time.Millisecond, 0, 0)

	meta := &StickyMeta{Enabled: true, IdleTimeout: 10 * time.Minute}
	group := "sticky-g"
	mk := func(name, url string, w int) Task {
		req, _ := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", nil)
		return Task{ProviderName: name, UpstreamModel: "m", Weight: w, OutboundProtocol: "openai.chat", Request: req}
	}
	tasks := []Task{mk("prov-a", aSrv.URL, 1), mk("prov-b", bSrv.URL, 3)}

	ctx := WithKeyName(context.Background(), "user-1")
	// 抬高 a 的成功计数，使 miss 时 deficit 偏向 b；随后 sticky 应粘住 b
	GetStickyStore().IncrSuccess(group, "prov-a/m")
	GetStickyStore().IncrSuccess(group, "prov-a/m")

	r1, err := sched.ExecuteWithSticky(ctx, "load-balance", group, meta, cloneTasks(tasks))
	if err != nil || r1 == nil || r1.FailureKind != FailureKindSuccess {
		t.Fatalf("first request: err=%v res=%v", err, r1)
	}
	first := r1.Winner
	r2, err := sched.ExecuteWithSticky(ctx, "load-balance", group, meta, cloneTasks(tasks))
	if err != nil || r2 == nil || r2.FailureKind != FailureKindSuccess {
		t.Fatalf("second request: err=%v res=%v", err, r2)
	}
	if r2.Winner != first {
		t.Fatalf("sticky hit expected same winner %s, got %s", first, r2.Winner)
	}
}

func cloneTasks(in []Task) []Task {
	out := make([]Task, len(in))
	for i, t := range in {
		out[i] = t
		if t.Request != nil {
			req2 := t.Request.Clone(context.Background())
			out[i].Request = req2
		}
	}
	return out
}

func TestExecuteSticky_FailThroughAndRecordWinner(t *testing.T) {
	resetStickyStore()
	defer resetStickyStore()

	aSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"fail"}`)
	}))
	defer aSrv.Close()
	bSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okChatBody())
	}))
	defer bSrv.Close()

	providers := map[string]config.ProviderConfig{
		"prov-a": {Endpoint: aSrv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
		"prov-b": {Endpoint: bSrv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
	}
	client := provider.NewClient(providers, 50*time.Millisecond, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(100, 30*time.Second) // 高阈值，避免首失败即摘除影响其它断言外逻辑
	sched := New(rl, client, h, 500*time.Millisecond, 0, 0)

	group := "g-fail"
	meta := &StickyMeta{Enabled: true, IdleTimeout: 10 * time.Minute}
	// 预置 sticky 指向会失败的 a
	GetStickyStore().Remember(affinityKey("user-1", group), "prov-a/m", time.Now(), meta.IdleTimeout)

	mk := func(name, url string) Task {
		req, _ := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", nil)
		return Task{ProviderName: name, UpstreamModel: "m", Weight: 1, OutboundProtocol: "openai.chat", Request: req}
	}
	tasks := []Task{mk("prov-a", aSrv.URL), mk("prov-b", bSrv.URL)}

	ctx := WithKeyName(context.Background(), "user-1")
	res, err := sched.ExecuteWithSticky(ctx, "load-balance", group, meta, tasks)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.FailureKind != FailureKindSuccess || res.Winner != "prov-b" {
		t.Fatalf("expected prov-b success, got winner=%s kind=%v", res.Winner, res.FailureKind)
	}
	// sticky 应覆盖为 winner
	got, ok := GetStickyStore().Lookup(affinityKey("user-1", group), time.Now())
	if !ok || got != "prov-b/m" {
		t.Fatalf("expected sticky prov-b/m, got %s ok=%v", got, ok)
	}
	if GetStickyStore().GetSuccessCounts(group)["prov-b/m"] != 1 {
		t.Fatalf("expected success count on winner")
	}
	if GetStickyStore().GetSuccessCounts(group)["prov-a/m"] != 0 {
		t.Fatalf("failed candidate must not get success count")
	}
}

func TestExecuteSticky_HitAlsoIncrSuccess(t *testing.T) {
	resetStickyStore()
	defer resetStickyStore()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okChatBody())
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"only": {Endpoint: srv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
	}
	client := provider.NewClient(providers, 50*time.Millisecond, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h, 500*time.Millisecond, 0, 0)

	group := "g-hit"
	meta := &StickyMeta{Enabled: true, IdleTimeout: time.Hour}
	GetStickyStore().Remember(affinityKey("u", group), "only/m", time.Now(), meta.IdleTimeout)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", nil)
	task := Task{ProviderName: "only", UpstreamModel: "m", Weight: 1, OutboundProtocol: "openai.chat", Request: req}

	ctx := WithKeyName(context.Background(), "u")
	if _, err := sched.ExecuteWithSticky(ctx, "load-balance", group, meta, []Task{task}); err != nil {
		t.Fatal(err)
	}
	if GetStickyStore().GetSuccessCounts(group)["only/m"] != 1 {
		t.Fatal("sticky hit success must IncrSuccess")
	}
}

func TestExecuteSticky_SingleAllowWithQPM1(t *testing.T) {
	resetStickyStore()
	defer resetStickyStore()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okChatBody())
	}))
	defer srv.Close()

	// QPM=1：若双重 Allow，第二次会限流导致失败
	providers := map[string]config.ProviderConfig{
		"limited": {Endpoint: srv.URL, APIKey: "k", Protocols: []string{"openai.chat"}, RateLimit: config.RateLimitConfig{QPM: 1}},
	}
	client := provider.NewClient(providers, 50*time.Millisecond, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h, 500*time.Millisecond, 0, 0)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", nil)
	task := Task{ProviderName: "limited", UpstreamModel: "m", Weight: 1, OutboundProtocol: "openai.chat", Request: req}
	meta := &StickyMeta{Enabled: true, IdleTimeout: time.Minute}
	ctx := WithKeyName(context.Background(), "u")

	res, err := sched.ExecuteWithSticky(ctx, "load-balance", "g", meta, []Task{task})
	if err != nil {
		t.Fatalf("expected success with single Allow, err=%v", err)
	}
	if res.FailureKind != FailureKindSuccess {
		t.Fatalf("expected success, kind=%v reason=%s", res.FailureKind, res.FailureReason)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", hits.Load())
	}
}

func TestExecuteSticky_EmptyKeyStillIncr(t *testing.T) {
	resetStickyStore()
	defer resetStickyStore()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okChatBody())
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"p": {Endpoint: srv.URL, APIKey: "k", Protocols: []string{"openai.chat"}},
	}
	client := provider.NewClient(providers, 50*time.Millisecond, 0, 0)
	rl := ratelimit.NewManager(providers)
	h := health.NewChecker(3, 30*time.Second)
	sched := New(rl, client, h, 500*time.Millisecond, 0, 0)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", nil)
	task := Task{ProviderName: "p", UpstreamModel: "m", Weight: 1, OutboundProtocol: "openai.chat", Request: req}
	meta := &StickyMeta{Enabled: true, IdleTimeout: time.Minute}

	// 无 key_name
	res, err := sched.ExecuteWithSticky(context.Background(), "load-balance", "g", meta, []Task{task})
	if err != nil || res.FailureKind != FailureKindSuccess {
		t.Fatalf("err=%v res=%v", err, res)
	}
	if GetStickyStore().GetSuccessCounts("g")["p/m"] != 1 {
		t.Fatal("empty key must still IncrSuccess")
	}
	if _, ok := GetStickyStore().Lookup(affinityKey("", "g"), time.Now()); ok {
		t.Fatal("empty key must not Remember")
	}
}
