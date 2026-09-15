package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminStats_NoDateFilter(t *testing.T) {
	// Initialize test database
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"
	err := stats.Init(dbPath)
	require.NoError(t, err)
	defer stats.Reset()

	// Insert test data
	recorder := stats.GetRecorder()
	err = recorder.Record("key1", "openai", "gpt-4", 100, 50, 1000)
	require.NoError(t, err)
	err = recorder.Record("key2", "anthropic", "claude-3", 200, 100, 2000)
	require.NoError(t, err)
	err = recorder.RecordUserModel("gpt-4o", 100, 50, 1000)
	require.NoError(t, err)
	err = recorder.RecordUserModel("claude-sonnet", 200, 100, 2000)
	require.NoError(t, err)

	// Create handler
	querier := stats.GetQuerier()
	h := NewAdminStatsHandler(querier)

	// Setup router
	r := gin.New()
	r.GET("/admin/stats", h.GetStats)

	// Make request
	req := httptest.NewRequest("GET", "/admin/stats", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Verify response
	assert.Equal(t, http.StatusOK, w.Code)

	var resp StatsResponse
	err = parseJSONResponse(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	// Verify total stats
	require.NotNil(t, resp.Total)
	assert.Equal(t, int64(300), resp.Total.InputTokens)
	assert.Equal(t, int64(150), resp.Total.OutputTokens)
	assert.Equal(t, int64(2), resp.Total.RequestCount)
	assert.Equal(t, int64(3000), resp.Total.LatencyMs)

	// Verify by_keys
	assert.Len(t, resp.ByKeys, 2)
	keyNames := make([]string, len(resp.ByKeys))
	for i, k := range resp.ByKeys {
		keyNames[i] = k.KeyName
	}
	assert.Contains(t, keyNames, "key1")
	assert.Contains(t, keyNames, "key2")

	// Verify by_providers
	assert.Len(t, resp.ByProviders, 2)
	providerNames := make([]string, len(resp.ByProviders))
	for i, p := range resp.ByProviders {
		providerNames[i] = p.ProviderName
	}
	assert.Contains(t, providerNames, "openai")
	assert.Contains(t, providerNames, "anthropic")

	// Verify earliest_date
	assert.NotEmpty(t, resp.EarliestDate)

	// Verify by_user_model
	assert.Len(t, resp.ByUserModels, 2)
	userModelNames := make([]string, len(resp.ByUserModels))
	for i, m := range resp.ByUserModels {
		userModelNames[i] = m.UserModel
	}
	assert.Contains(t, userModelNames, "gpt-4o")
	assert.Contains(t, userModelNames, "claude-sonnet")
}

func TestAdminStats_ValidDateRange(t *testing.T) {
	// Initialize test database
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"
	err := stats.Init(dbPath)
	require.NoError(t, err)
	defer stats.Reset()

	// Insert test data
	recorder := stats.GetRecorder()
	err = recorder.Record("key1", "openai", "gpt-4", 100, 50, 1000)
	require.NoError(t, err)

	// Create handler
	querier := stats.GetQuerier()
	h := NewAdminStatsHandler(querier)

	// Setup router
	r := gin.New()
	r.GET("/admin/stats", h.GetStats)

	// Make request with date range
	req := httptest.NewRequest("GET", "/admin/stats?since=2026-01-01&until=2026-12-31", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Verify response
	assert.Equal(t, http.StatusOK, w.Code)

	var resp StatsResponse
	err = parseJSONResponse(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	// Verify total stats
	require.NotNil(t, resp.Total)
	assert.Equal(t, int64(100), resp.Total.InputTokens)
	assert.Equal(t, int64(50), resp.Total.OutputTokens)
	assert.Equal(t, int64(1), resp.Total.RequestCount)
	assert.Equal(t, int64(1000), resp.Total.LatencyMs)
}

func TestAdminStats_InvalidDateFormat(t *testing.T) {
	// Initialize test database
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"
	err := stats.Init(dbPath)
	require.NoError(t, err)
	defer stats.Reset()

	// Create handler
	querier := stats.GetQuerier()
	h := NewAdminStatsHandler(querier)

	// Setup router
	r := gin.New()
	r.GET("/admin/stats", h.GetStats)

	// Test invalid 'since' format
	req := httptest.NewRequest("GET", "/admin/stats?since=invalid-date", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Test invalid 'until' format
	req = httptest.NewRequest("GET", "/admin/stats?until=2026/01/01", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAdminStats_EmptyDatabase(t *testing.T) {
	// Initialize test database with no data
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"
	err := stats.Init(dbPath)
	require.NoError(t, err)
	defer stats.Reset()

	// Create handler
	querier := stats.GetQuerier()
	h := NewAdminStatsHandler(querier)

	// Setup router
	r := gin.New()
	r.GET("/admin/stats", h.GetStats)

	// Make request
	req := httptest.NewRequest("GET", "/admin/stats", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Verify response
	assert.Equal(t, http.StatusOK, w.Code)

	var resp StatsResponse
	err = parseJSONResponse(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	// Verify zero values for empty database
	require.NotNil(t, resp.Total)
	assert.Equal(t, int64(0), resp.Total.InputTokens)
	assert.Equal(t, int64(0), resp.Total.OutputTokens)
	assert.Equal(t, int64(0), resp.Total.RequestCount)
	assert.Equal(t, int64(0), resp.Total.LatencyMs)

	// Empty maps
	assert.Empty(t, resp.ByKeys)
	assert.Empty(t, resp.ByUserModels)
	assert.Empty(t, resp.ByProviders)
	assert.Empty(t, resp.ByModels)

	// Empty earliest date
	assert.Empty(t, resp.EarliestDate)
}

func TestResolveDateRange(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)

	tests := []struct {
		name      string
		rng       string
		now       time.Time
		wantSince string
		wantUntil string
		wantErr   bool
	}{
		{
			name:      "today 白天",
			rng:       "today",
			now:       time.Date(2026, 8, 3, 15, 30, 0, 0, cst),
			wantSince: "2026-08-03",
			wantUntil: "2026-08-03",
		},
		{
			name:      "today 凌晨窗口不偏移",
			rng:       "today",
			now:       time.Date(2026, 8, 3, 1, 0, 0, 0, cst),
			wantSince: "2026-08-03",
			wantUntil: "2026-08-03",
		},
		{
			name:      "week",
			rng:       "week",
			now:       time.Date(2026, 8, 3, 15, 30, 0, 0, cst),
			wantSince: "2026-07-27",
			wantUntil: "2026-08-03",
		},
		{
			name:      "month",
			rng:       "month",
			now:       time.Date(2026, 8, 3, 15, 30, 0, 0, cst),
			wantSince: "2026-07-03",
			wantUntil: "2026-08-03",
		},
		{
			name:      "month 跨月溢出 normalize",
			rng:       "month",
			now:       time.Date(2026, 3, 31, 12, 0, 0, 0, cst),
			wantSince: "2026-03-03",
			wantUntil: "2026-03-31",
		},
		{
			name:      "week 跨年",
			rng:       "week",
			now:       time.Date(2026, 1, 15, 12, 0, 0, 0, cst),
			wantSince: "2026-01-08",
			wantUntil: "2026-01-15",
		},
		{
			name:      "month 跨年",
			rng:       "month",
			now:       time.Date(2026, 1, 15, 12, 0, 0, 0, cst),
			wantSince: "2025-12-15",
			wantUntil: "2026-01-15",
		},
		{
			name:    "非法 range",
			rng:     "all",
			now:     time.Date(2026, 8, 3, 12, 0, 0, 0, cst),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			since, until, err := resolveDateRange(tt.rng, tt.now)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSince, since)
			assert.Equal(t, tt.wantUntil, until)
		})
	}
}

func TestAdminStats_ValidRange(t *testing.T) {
	// Initialize test database
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"
	err := stats.Init(dbPath)
	require.NoError(t, err)
	defer stats.Reset()

	recorder := stats.GetRecorder()
	err = recorder.Record("key1", "openai", "gpt-4", 100, 50, 1000)
	require.NoError(t, err)

	h := NewAdminStatsHandler(stats.GetQuerier())
	r := gin.New()
	r.GET("/admin/stats", h.GetStats)

	today := time.Now().Format("2006-01-02")

	// range=today 返回服务端本地时区的今天
	req := httptest.NewRequest("GET", "/admin/stats?range=today", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp StatsResponse
	err = parseJSONResponse(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, today, resp.Since)
	assert.Equal(t, today, resp.Until)

	// range 优先于 since/until
	req = httptest.NewRequest("GET", "/admin/stats?range=today&since=2026-01-01&until=2026-12-31", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	resp = StatsResponse{}
	err = parseJSONResponse(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, today, resp.Since)
	assert.Equal(t, today, resp.Until)

	// 非法 range 返回 400
	req = httptest.NewRequest("GET", "/admin/stats?range=all", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func parseJSONResponse(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
