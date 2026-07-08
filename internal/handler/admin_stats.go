package handler

import (
	"net/http"
	"regexp"

	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/gin-gonic/gin"
)

// AdminStatsHandler 处理 /admin/stats 端点
type AdminStatsHandler struct {
	querier stats.Querier
}

// NewAdminStatsHandler 创建 handler
func NewAdminStatsHandler(querier stats.Querier) *AdminStatsHandler {
	return &AdminStatsHandler{
		querier: querier,
	}
}

// StatsResponse 表示统计 API 响应
type StatsResponse struct {
	Total        *TotalStatsResponse          `json:"total"`
	ByKeys       []*KeyStatsResponse          `json:"by_key"`
	ByProviders  []*ProviderStatsResponse     `json:"by_provider"`
	ByModels     []*ModelStatsResponse        `json:"by_model"`
	EarliestDate string                       `json:"earliest_date"`
}

// TotalStatsResponse 表示总计统计响应
type TotalStatsResponse struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	RequestCount int64 `json:"request_count"`
	LatencyMs    int64 `json:"latency_ms"`
}

// KeyStatsResponse 表示按 key 分组的统计响应
type KeyStatsResponse struct {
	KeyName      string `json:"key_name"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	RequestCount int64  `json:"request_count"`
	LatencyMs    int64  `json:"latency_ms"`
}

// ProviderStatsResponse 表示按 provider+model 分组的统计响应
type ProviderStatsResponse struct {
	ProviderName  string `json:"provider_name"`
	UpstreamModel string `json:"upstream_model"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	RequestCount  int64  `json:"request_count"`
	LatencyMs     int64  `json:"latency_ms"`
}

// ProviderOnlyStatsResponse 表示按 provider 分组的统计响应
type ProviderOnlyStatsResponse struct {
	ProviderName string `json:"provider_name"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	RequestCount int64  `json:"request_count"`
	LatencyMs    int64  `json:"latency_ms"`
}

// ModelStatsResponse 表示按 model 分组的统计响应
type ModelStatsResponse struct {
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	RequestCount int64  `json:"request_count"`
	LatencyMs    int64  `json:"latency_ms"`
}

// GetStats 返回所有维度的统计数据
func (h *AdminStatsHandler) GetStats(c *gin.Context) {
	since := c.Query("since")
	until := c.Query("until")

	// 验证日期格式
	dateRegex := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	if since != "" && !dateRegex.MatchString(since) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid date format",
			},
		})
		return
	}
	if until != "" && !dateRegex.MatchString(until) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "bad_request",
				"message": "invalid date format",
			},
		})
		return
	}

	// 查询总计统计
	total, err := h.querier.QueryTotal(since, until)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "internal_error",
				"message": err.Error(),
			},
		})
		return
	}

	// 查询按 key 分组统计
	byKeys, err := h.querier.QueryByKeys(since, until)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "internal_error",
				"message": err.Error(),
			},
		})
		return
	}

	// 查询按 provider 分组统计
	byProviderOnly, err := h.querier.QueryByProviderOnly(since, until)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "internal_error",
				"message": err.Error(),
			},
		})
		return
	}

	// 查询按 provider+model 分组统计
	byProviderModel, err := h.querier.QueryByProviders(since, until)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "internal_error",
				"message": err.Error(),
			},
		})
		return
	}

	// 查询最早日期
	earliestDate, err := h.querier.QueryEarliestDate()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "internal_error",
				"message": err.Error(),
			},
		})
		return
	}

	// 构建响应
	resp := StatsResponse{
		Total:        convertTotalStats(total),
		ByKeys:       convertKeyStats(byKeys),
		ByProviders:  convertProviderOnlyStats(byProviderOnly),
		ByModels:     convertProviderModelStats(byProviderModel),
		EarliestDate: earliestDate,
	}

	c.JSON(http.StatusOK, resp)
}

func convertTotalStats(s *stats.TotalStats) *TotalStatsResponse {
	if s == nil {
		return &TotalStatsResponse{}
	}
	return &TotalStatsResponse{
		InputTokens:  s.InputTokens,
		OutputTokens: s.OutputTokens,
		RequestCount: s.RequestCount,
		LatencyMs:    s.LatencyMs,
	}
}

func convertKeyStats(m map[string]*stats.KeyStats) []*KeyStatsResponse {
	result := make([]*KeyStatsResponse, 0, len(m))
	for _, v := range m {
		if v != nil {
			result = append(result, &KeyStatsResponse{
				KeyName:      v.KeyName,
				InputTokens:  v.InputTokens,
				OutputTokens: v.OutputTokens,
				RequestCount: v.RequestCount,
				LatencyMs:    v.LatencyMs,
			})
		}
	}
	return result
}

func convertProviderOnlyStats(m map[string]*stats.ProviderOnlyStats) []*ProviderStatsResponse {
	result := make([]*ProviderStatsResponse, 0, len(m))
	for _, v := range m {
		if v != nil {
			result = append(result, &ProviderStatsResponse{
				ProviderName:  v.ProviderName,
				UpstreamModel: "",
				InputTokens:   v.InputTokens,
				OutputTokens:  v.OutputTokens,
				RequestCount:  v.RequestCount,
				LatencyMs:     v.LatencyMs,
			})
		}
	}
	return result
}

func convertProviderModelStats(m map[string]*stats.ProviderStats) []*ModelStatsResponse {
	result := make([]*ModelStatsResponse, 0, len(m))
	for _, v := range m {
		if v != nil {
			result = append(result, &ModelStatsResponse{
				Model:        v.ProviderName + "/" + v.UpstreamModel,
				InputTokens:  v.InputTokens,
				OutputTokens: v.OutputTokens,
				RequestCount: v.RequestCount,
				LatencyMs:    v.LatencyMs,
			})
		}
	}
	return result
}