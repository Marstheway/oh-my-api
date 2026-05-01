package stats

import (
	"path/filepath"
	"testing"
)

func TestRecorderRecord(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := Init(dbPath); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Close()

	rec := GetRecorder()
	if rec == nil {
		t.Fatal("GetRecorder returned nil")
	}

	err := rec.Record("test-key", "openai", "gpt-4o", 100, 50, 150)
	if err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	// 验证数据写入
	var count int64
	err = db.QueryRow("SELECT COUNT(*) FROM daily_stats").Scan(&count)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}
}

func TestRecorderAccumulate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := Init(dbPath); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Close()

	rec := GetRecorder()

	// 同一天同一维度写入两次
	_ = rec.Record("test-key", "openai", "gpt-4o", 100, 50, 100)
	err := rec.Record("test-key", "openai", "gpt-4o", 200, 100, 200)
	if err != nil {
		t.Fatalf("second Record failed: %v", err)
	}

	var inputTokens, outputTokens, requestCount, latencyMs int64
	err = db.QueryRow(`
		SELECT input_tokens, output_tokens, request_count, latency_ms
		FROM daily_stats
		WHERE key_name = 'test-key'
	`).Scan(&inputTokens, &outputTokens, &requestCount, &latencyMs)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if inputTokens != 300 {
		t.Errorf("expected input_tokens 300, got %d", inputTokens)
	}
	if outputTokens != 150 {
		t.Errorf("expected output_tokens 150, got %d", outputTokens)
	}
	if requestCount != 2 {
		t.Errorf("expected request_count 2, got %d", requestCount)
	}
	if latencyMs != 300 {
		t.Errorf("expected latency_ms 300, got %d", latencyMs)
	}
}

func TestRecorderTodayDate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := Init(dbPath); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Close()

	rec := GetRecorder()
	_ = rec.Record("test-key", "openai", "gpt-4o", 100, 50, 150)

	var date string
	err := db.QueryRow("SELECT date FROM daily_stats").Scan(&date)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	today := timeNow().Format("2006-01-02")
	if date != today {
		t.Errorf("expected date %s, got %s", today, date)
	}
}