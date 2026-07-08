package stats

import (
	"testing"
)

func TestQuerierQueryTotal(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	if err := Init(dbPath); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Close()

	// 写入测试数据
	_ = GetRecorder().Record("alice", "openai", "gpt-4o", 100, 50, 150)
	_ = GetRecorder().Record("alice", "openai", "gpt-4o", 200, 100, 200)
	_ = GetRecorder().Record("bob", "anthropic", "claude-3", 300, 150, 300)

	q := GetQuerier()
	total, err := q.QueryTotal("", "")
	if err != nil {
		t.Fatalf("QueryTotal failed: %v", err)
	}

	if total.InputTokens != 600 {
		t.Errorf("expected InputTokens 600, got %d", total.InputTokens)
	}
	if total.OutputTokens != 300 {
		t.Errorf("expected OutputTokens 300, got %d", total.OutputTokens)
	}
	if total.RequestCount != 3 {
		t.Errorf("expected RequestCount 3, got %d", total.RequestCount)
	}
	if total.LatencyMs != 650 {
		t.Errorf("expected LatencyMs 650, got %d", total.LatencyMs)
	}
}

func TestQuerierQueryByKeys(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	if err := Init(dbPath); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Close()

	_ = GetRecorder().Record("alice", "openai", "gpt-4o", 100, 50, 100)
	_ = GetRecorder().Record("bob", "openai", "gpt-4o", 200, 100, 200)

	q := GetQuerier()
	keys, err := q.QueryByKeys("", "")
	if err != nil {
		t.Fatalf("QueryByKeys failed: %v", err)
	}

	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}

	if keys["alice"].InputTokens != 100 {
		t.Errorf("expected alice InputTokens 100, got %d", keys["alice"].InputTokens)
	}
	if keys["bob"].OutputTokens != 100 {
		t.Errorf("expected bob OutputTokens 100, got %d", keys["bob"].OutputTokens)
	}
	if keys["alice"].LatencyMs != 100 {
		t.Errorf("expected alice LatencyMs 100, got %d", keys["alice"].LatencyMs)
	}
}

func TestQuerierQueryByProviders(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	if err := Init(dbPath); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Close()

	_ = GetRecorder().Record("alice", "openai", "gpt-4o", 100, 50, 100)
	_ = GetRecorder().Record("bob", "openai", "gpt-4o-mini", 200, 100, 200)
	_ = GetRecorder().Record("alice", "anthropic", "claude-3", 300, 150, 300)

	q := GetQuerier()
	providers, err := q.QueryByProviders("", "")
	if err != nil {
		t.Fatalf("QueryByProviders failed: %v", err)
	}

	if len(providers) != 3 {
		t.Fatalf("expected 3 provider/model combos, got %d", len(providers))
	}

	key := "openai/gpt-4o"
	if providers[key].InputTokens != 100 {
		t.Errorf("expected %s InputTokens 100, got %d", key, providers[key].InputTokens)
	}
	if providers[key].LatencyMs != 100 {
		t.Errorf("expected %s LatencyMs 100, got %d", key, providers[key].LatencyMs)
	}
}

// TestQueryByProviders_PreservesProviderUpstreamDimensions 验证 QueryByProviders 以 provider/upstream_model
// 为维度聚合，而非以 alias 为维度，从而确认 stats 存储不受 alias 污染。
func TestQueryByProviders_PreservesProviderUpstreamDimensions(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	if err := Init(dbPath); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Close()

	// 模拟两次请求：同一 provider/upstream_model 组合，存入真实 winner 信息
	_ = GetRecorder().Record("alice", "openai", "gpt-4o", 100, 50, 100)
	_ = GetRecorder().Record("bob", "openai", "gpt-4o", 200, 100, 200)

	q := GetQuerier()
	providers, err := q.QueryByProviders("", "")
	if err != nil {
		t.Fatalf("QueryByProviders failed: %v", err)
	}

	// provider/upstream_model 维度应单独存在
	key := "openai/gpt-4o"
	if _, ok := providers[key]; !ok {
		t.Fatalf("expected key %q in providers result, got keys: %v", key, providerKeys(providers))
	}

	if providers[key].RequestCount != 2 {
		t.Errorf("expected RequestCount 2, got %d", providers[key].RequestCount)
	}

	// 不应存在以 alias 为 key 的条目（alias 不应出现在 upstream_model 维度）
	const fakeAlias = "openai/my-alias"
	if _, ok := providers[fakeAlias]; ok {
		t.Errorf("alias %q should not appear in provider stats dimensions", fakeAlias)
	}
}

func providerKeys(m map[string]*ProviderStats) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
