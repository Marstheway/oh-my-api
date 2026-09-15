package handler

import (
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/Marstheway/oh-my-api/internal/stats"
	"github.com/gin-gonic/gin"
)

// dateRegex validates YYYY-MM-DD query params; compiled once at package init.
var dateRegex = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

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
	Total        *TotalStatsResponse       `json:"total"`
	ByKeys       []*KeyStatsResponse       `json:"by_key"`
	ByUserModels []*UserModelStatsResponse `json:"by_user_model"`
	ByProviders  []*ProviderStatsResponse  `json:"by_provider"`
	ByModels     []*ModelStatsResponse     `json:"by_model"`
	EarliestDate string                    `json:"earliest_date"`
	Since        string                    `json:"since"`
	Until        string                    `json:"until"`
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

// UserModelStatsResponse 表示按 user_model 分组的统计响应
type UserModelStatsResponse struct {
	UserModel    string `json:"user_model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	RequestCount int64  `json:"request_count"`
	LatencyMs    int64  `json:"latency_ms"`
}

// GetStats 返回所有维度的统计数据
func (h *AdminStatsHandler) GetStats(c *gin.Context) {
	since := c.Query("since")
	until := c.Query("until")

	// range 参数按服务端本地时区（部署环境时区）解析，优先于 since/until
	if rng := c.Query("range"); rng != "" {
		s, u, err := resolveDateRange(rng, time.Now())
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"code":    "bad_request",
					"message": err.Error(),
				},
			})
			return
		}
		since, until = s, u
	}

	// 验证日期格式
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

	// 查询按 user_model 分组统计
	byUserModels, err := h.querier.QueryByUserModels(since, until)
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
		ByUserModels: convertUserModelStats(byUserModels),
		ByProviders:  convertProviderOnlyStats(byProviderOnly),
		ByModels:     convertProviderModelStats(byProviderModel),
		EarliestDate: earliestDate,
		Since:        since,
		Until:        until,
	}

	c.JSON(http.StatusOK, resp)
}

// resolveDateRange 按服务端本地时区解析预设时间范围，返回闭区间 [since, until]（YYYY-MM-DD）。
// 服务端本地时区即部署环境时区（容器 TZ 或宿主机时区），部署时固定，作为统计口径的唯一权威来源。
func resolveDateRange(rng string, now time.Time) (since, until string, err error) {
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	until = today.Format("2006-01-02")

	switch rng {
	case "today":
		since = until
	case "week":
		// 使用日历减法而非 7*24h 绝对时长，避免 DST 切换导致日期偏移
		since = today.AddDate(0, 0, -7).Format("2006-01-02")
	case "month":
		// 与前端 new Date(y, m-1, d) 语义一致，跨月溢出时按日历 normalize
		since = today.AddDate(0, -1, 0).Format("2006-01-02")
	default:
		return "", "", fmt.Errorf("invalid range: %s", rng)
	}
	return since, until, nil
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

func convertUserModelStats(m map[string]*stats.UserModelStats) []*UserModelStatsResponse {
	result := make([]*UserModelStatsResponse, 0, len(m))
	for _, v := range m {
		if v != nil {
			result = append(result, &UserModelStatsResponse{
				UserModel:    v.UserModel,
				InputTokens:  v.InputTokens,
				OutputTokens: v.OutputTokens,
				RequestCount: v.RequestCount,
				LatencyMs:    v.LatencyMs,
			})
		}
	}
	return result
}
