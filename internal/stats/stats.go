package stats

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Marstheway/oh-my-api/internal/migrate"
	_ "modernc.org/sqlite"
)

type DailyStats struct {
	Date          string
	KeyName       string
	ProviderName  string
	UpstreamModel string
	InputTokens   int64
	OutputTokens  int64
	RequestCount  int64
}

type TotalStats struct {
	InputTokens   int64
	OutputTokens  int64
	RequestCount  int64
	LatencyMs     int64
}

type KeyStats struct {
	KeyName      string
	InputTokens  int64
	OutputTokens int64
	RequestCount int64
	LatencyMs    int64
}

type ProviderStats struct {
	ProviderName  string
	UpstreamModel string
	InputTokens   int64
	OutputTokens  int64
	RequestCount  int64
	LatencyMs     int64
}

type ProviderOnlyStats struct {
	ProviderName string
	InputTokens  int64
	OutputTokens int64
	RequestCount int64
	LatencyMs    int64
}

type Recorder interface {
	Record(keyName, providerName, upstreamModel string, inputTokens, outputTokens int, latencyMs int64) error
}

type Querier interface {
	QueryTotal(since, until string) (*TotalStats, error)
	QueryByKeys(since, until string) (map[string]*KeyStats, error)
	QueryByProviders(since, until string) (map[string]*ProviderStats, error)
	QueryByProviderOnly(since, until string) (map[string]*ProviderOnlyStats, error)
	QueryEarliestDate() (string, error)
}

var (
	db      *sql.DB
	recorder Recorder
	querier  Querier
)

func GetRecorder() Recorder {
	return recorder
}

func GetQuerier() Querier {
	return querier
}

func Init(dbPath string) error {
	if dbPath == "" {
		dbPath = "./data/oh-my-api.db"
	}

	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}

	var err error
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	if err := applyPragmas(); err != nil {
		return fmt.Errorf("apply pragmas: %w", err)
	}

	if err := createTable(); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	if err := migrate.Run(dbPath); err != nil {
		return fmt.Errorf("migration: %w", err)
	}

	recorder = &sqliteRecorder{db: db}
	querier = &sqliteQuerier{db: db}

	return nil
}

func Close() error {
	if db != nil {
		return db.Close()
	}
	return nil
}

// Reset 清空全局状态，用于测试隔离
func Reset() {
	if db != nil {
		db.Close()
		db = nil
	}
	recorder = nil
	querier = nil
}

func applyPragmas() error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return err
		}
	}
	return nil
}

func createTable() error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS daily_stats (
			date TEXT NOT NULL,
			key_name TEXT NOT NULL,
			provider_name TEXT NOT NULL,
			upstream_model TEXT NOT NULL,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			request_count INTEGER DEFAULT 0,
			PRIMARY KEY (date, key_name, provider_name, upstream_model)
		);
		CREATE INDEX IF NOT EXISTS idx_date ON daily_stats(date);
	`)
	return err
}

// timeNow 可在测试中被替换
var timeNow = time.Now

// sqliteRecorder 是 Recorder 接口的空实现
type sqliteRecorder struct {
	db *sql.DB
}

func (r *sqliteRecorder) Record(keyName, providerName, upstreamModel string, inputTokens, outputTokens int, latencyMs int64) error {
	date := timeNow().Format("2006-01-02")

	_, err := r.db.Exec(`
		INSERT INTO daily_stats (date, key_name, provider_name, upstream_model, input_tokens, output_tokens, request_count, latency_ms)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?)
		ON CONFLICT(date, key_name, provider_name, upstream_model) DO UPDATE SET
			input_tokens = input_tokens + excluded.input_tokens,
			output_tokens = output_tokens + excluded.output_tokens,
			request_count = request_count + 1,
			latency_ms = latency_ms + excluded.latency_ms
	`, date, keyName, providerName, upstreamModel, inputTokens, outputTokens, latencyMs)

	return err
}

// sqliteQuerier 是 Querier 接口的空实现
type sqliteQuerier struct {
	db *sql.DB
}

func (q *sqliteQuerier) QueryTotal(since, until string) (*TotalStats, error) {
	query := "SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(request_count), 0), COALESCE(SUM(latency_ms), 0) FROM daily_stats"
	args := []any{}

	if since != "" || until != "" {
		query += " WHERE 1=1"
		if since != "" {
			query += " AND date >= ?"
			args = append(args, since)
		}
		if until != "" {
			query += " AND date <= ?"
			args = append(args, until)
		}
	}

	var inputTokens, outputTokens, requestCount, latencyMs int64
	err := q.db.QueryRow(query, args...).Scan(&inputTokens, &outputTokens, &requestCount, &latencyMs)
	if err != nil {
		return nil, err
	}

	return &TotalStats{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		RequestCount: requestCount,
		LatencyMs:    latencyMs,
	}, nil
}

func (q *sqliteQuerier) QueryByKeys(since, until string) (map[string]*KeyStats, error) {
	query := "SELECT key_name, COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(request_count), 0), COALESCE(SUM(latency_ms), 0) FROM daily_stats"
	args := []any{}

	if since != "" || until != "" {
		query += " WHERE 1=1"
		if since != "" {
			query += " AND date >= ?"
			args = append(args, since)
		}
		if until != "" {
			query += " AND date <= ?"
			args = append(args, until)
		}
	}

	query += " GROUP BY key_name"

	rows, err := q.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*KeyStats)
	for rows.Next() {
		var keyName string
		var inputTokens, outputTokens, requestCount, latencyMs int64
		if err := rows.Scan(&keyName, &inputTokens, &outputTokens, &requestCount, &latencyMs); err != nil {
			return nil, err
		}
		result[keyName] = &KeyStats{
			KeyName:      keyName,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			RequestCount: requestCount,
			LatencyMs:    latencyMs,
		}
	}

	return result, rows.Err()
}

func (q *sqliteQuerier) QueryByProviders(since, until string) (map[string]*ProviderStats, error) {
	query := "SELECT provider_name, upstream_model, COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(request_count), 0), COALESCE(SUM(latency_ms), 0) FROM daily_stats"
	args := []any{}

	if since != "" || until != "" {
		query += " WHERE 1=1"
		if since != "" {
			query += " AND date >= ?"
			args = append(args, since)
		}
		if until != "" {
			query += " AND date <= ?"
			args = append(args, until)
		}
	}

	query += " GROUP BY provider_name, upstream_model"

	rows, err := q.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*ProviderStats)
	for rows.Next() {
		var providerName, upstreamModel string
		var inputTokens, outputTokens, requestCount, latencyMs int64
		if err := rows.Scan(&providerName, &upstreamModel, &inputTokens, &outputTokens, &requestCount, &latencyMs); err != nil {
			return nil, err
		}
		key := providerName + "/" + upstreamModel
		result[key] = &ProviderStats{
			ProviderName:  providerName,
			UpstreamModel: upstreamModel,
			InputTokens:   inputTokens,
			OutputTokens:  outputTokens,
			RequestCount:  requestCount,
			LatencyMs:     latencyMs,
		}
	}

	return result, rows.Err()
}

func (q *sqliteQuerier) QueryByProviderOnly(since, until string) (map[string]*ProviderOnlyStats, error) {
	query := "SELECT provider_name, COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(request_count), 0), COALESCE(SUM(latency_ms), 0) FROM daily_stats"
	args := []any{}

	if since != "" || until != "" {
		query += " WHERE 1=1"
		if since != "" {
			query += " AND date >= ?"
			args = append(args, since)
		}
		if until != "" {
			query += " AND date <= ?"
			args = append(args, until)
		}
	}

	query += " GROUP BY provider_name"

	rows, err := q.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*ProviderOnlyStats)
	for rows.Next() {
		var providerName string
		var inputTokens, outputTokens, requestCount, latencyMs int64
		if err := rows.Scan(&providerName, &inputTokens, &outputTokens, &requestCount, &latencyMs); err != nil {
			return nil, err
		}
		result[providerName] = &ProviderOnlyStats{
			ProviderName: providerName,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			RequestCount: requestCount,
			LatencyMs:    latencyMs,
		}
	}

	return result, rows.Err()
}

func (q *sqliteQuerier) QueryEarliestDate() (string, error) {
	var date sql.NullString
	err := q.db.QueryRow("SELECT MIN(date) FROM daily_stats").Scan(&date)
	if err != nil {
		return "", err
	}
	return date.String, nil
}

func ClearStats() error {
	if db == nil {
		return fmt.Errorf("database not initialized")
	}
	_, err := db.Exec("DELETE FROM daily_stats")
	return err
}