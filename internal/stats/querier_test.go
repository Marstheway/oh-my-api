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