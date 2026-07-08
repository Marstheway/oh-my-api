package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	assert.Empty(t, resp.ByProviders)
	assert.Empty(t, resp.ByModels)

	// Empty earliest date
	assert.Empty(t, resp.EarliestDate)
}

func parseJSONResponse(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}