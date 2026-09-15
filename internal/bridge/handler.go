package bridge

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// ProviderRegistry 按 provider 类型管理 TokenProvider 实例。
// 通过注册表模式保留扩展位，第一版仅注册 xai-oauth。
type ProviderRegistry struct {
	providers map[string]TokenProvider
}

// NewProviderRegistry 创建一个 provider 注册表。
// 内部使用 bridge 默认的独立 xAI OAuth state provider（~/.oh-my-api/bridge/auth.json），
// 不读取任何外部 auth 文件路径。
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers: map[string]TokenProvider{
			"xai-oauth": NewXAIOAuthProvider(""),
		},
	}
}

// Get 按 provider 类型获取对应的 TokenProvider。
// 若该 provider 不受支持，返回错误。
func (r *ProviderRegistry) Get(providerType string) (TokenProvider, error) {
	if err := ValidateProvider(providerType); err != nil {
		return nil, err
	}
	return r.providers[providerType], nil
}

// Register 注入指定 provider 类型的 TokenProvider，覆盖默认实现。
// 仅用于测试；生产代码不调用。
func (r *ProviderRegistry) Register(providerType string, tp TokenProvider) {
	if r.providers == nil {
		r.providers = map[string]TokenProvider{}
	}
	r.providers[providerType] = tp
}

// BridgeHandler 承载 /v1/responses、/v1/chat/completions、/v1/models 和 /healthz 的 HTTP handler 逻辑。
type BridgeHandler struct {
	bridgeToken                  string
	allowUnauthenticatedLoopback bool
	registry                     *ProviderRegistry
}

// NewBridgeHandler 创建 bridge handler。
// provider registry 内部使用 bridge 默认独立 xAI OAuth state provider，
// 不依赖任何外部 auth 文件路径。
func NewBridgeHandler(bridgeToken string) *BridgeHandler {
	return &BridgeHandler{
		bridgeToken: bridgeToken,
		registry:    NewProviderRegistry(),
	}
}

// NewBridgeHandlerWithRegistry 创建 bridge handler 并注入自定义 provider registry。
// 仅用于测试；生产代码始终使用 NewBridgeHandler 的默认独立 state provider。
func NewBridgeHandlerWithRegistry(bridgeToken string, registry *ProviderRegistry) *BridgeHandler {
	return &BridgeHandler{
		bridgeToken: bridgeToken,
		registry:    registry,
	}
}

// HandleResponses 处理 POST /v1/responses 请求。
func (h *BridgeHandler) HandleResponses(w http.ResponseWriter, r *http.Request) {
	h.handleProxyPost(w, r, xaiResponsesURL)
}

// HandleChatCompletions 处理 POST /v1/chat/completions 请求，转发到 xAI 的
// OpenAI 兼容 Chat Completions 接口。鉴权、provider 头校验、Content-Type 校验、
// body 透传、OAuth 401 刷新重试与日志规则与 HandleResponses 一致。
func (h *BridgeHandler) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	h.handleProxyPost(w, r, xaiChatURL)
}

// handleProxyPost 是 Responses 与 Chat Completions 共用的受保护 POST 处理流程：
// 鉴权、provider 类型头校验、JSON Content-Type 校验、原始 body 读取、OAuth token 获取、
// 上游 401 后仅一次强制刷新重试，以及响应头/状态/body 透传与安全日志。
// targetURL 为实际转发的 xAI 接口地址。不解析或重写请求与响应内容。
func (h *BridgeHandler) handleProxyPost(w http.ResponseWriter, r *http.Request, targetURL string) {
	start := time.Now()
	requestID := newRequestID()
	logger := slog.With("request_id", requestID)

	// 1. 校验 bridge bearer token
	if !h.authenticate(r) {
		writeError(w, http.StatusUnauthorized, "bridge: invalid bearer token")
		logger.Warn("bridge: auth failed", "path", r.URL.Path)
		return
	}

	// 2. 读取并校验 provider 类型头
	providerType := strings.TrimSpace(r.Header.Get("X-Oh-My-API-Bridge-Provider"))
	if providerType == "" {
		writeError(w, http.StatusBadRequest, "bridge: missing X-Oh-My-API-Bridge-Provider header")
		logger.Warn("bridge: missing provider type header", "path", r.URL.Path)
		return
	}

	tokenProvider, err := h.registry.Get(providerType)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("bridge: %v", err))
		logger.Warn("bridge: unsupported provider type", "path", r.URL.Path, "provider", providerType)
		return
	}

	// 3. 校验 Content-Type：只接受 application/json 或无显式 content-type
	contentType := r.Header.Get("Content-Type")
	if contentType != "" && !strings.HasPrefix(contentType, "application/json") {
		writeError(w, http.StatusBadRequest, "bridge: only application/json is accepted")
		logger.Warn("bridge: unsupported content type", "path", r.URL.Path, "content_type", contentType)
		return
	}

	// 4. 读取原始 body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bridge: cannot read request body")
		logger.Warn("bridge: body read error", "path", r.URL.Path)
		return
	}

	// 5. 获取 access token（自动处理过期刷新）
	ctx := r.Context()
	accessToken, proactiveRefresh, err := tokenProvider.GetAccessToken(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "bridge: cannot obtain access token")
		logger.Error("bridge: token error", "path", r.URL.Path, "error", tokenErrorSafe(err))
		return
	}
	refreshed := proactiveRefresh

	// 6. 向 xAI 发起请求
	resp, proxyErr := proxyToXAI(ctx, targetURL, bytes.NewReader(bodyBytes), accessToken)

	// 7. 若上游返回 401 且未强制刷新过，执行一次强制刷新并重试
	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		logger.Info("bridge: upstream 401, attempting forced refresh and retry", "provider", providerType)

		newToken, refreshErr := tokenProvider.ForceRefresh(ctx)
		if refreshErr != nil {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("bridge: token refresh failed after upstream 401: %v", refreshErr))
			logger.Error("bridge: forced refresh failed", "provider", providerType, "error", tokenErrorSafe(refreshErr))
			return
		}

		resp, proxyErr = proxyToXAI(ctx, targetURL, bytes.NewReader(bodyBytes), newToken)
		refreshed = true
	}

	// 8. 透传上游响应
	statusCode := http.StatusBadGateway
	logLevel := slog.LevelError

	if resp != nil {
		defer resp.Body.Close()
		statusCode = resp.StatusCode
		copyUpstreamHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)

		_, copyErr := io.Copy(w, resp.Body)
		if copyErr != nil {
			logger.Error("bridge: response copy error", "path", r.URL.Path, "error", copyErr)
		}

		if statusCode < 400 {
			logLevel = slog.LevelInfo
		} else {
			logLevel = slog.LevelWarn
		}
	} else {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("upstream request failed: %v", proxyErr))
	}

	logger.Log(ctx, logLevel, "request",
		"provider", providerType,
		"path", r.URL.Path,
		"status", statusCode,
		"duration_ms", time.Since(start).Milliseconds(),
		"refreshed", refreshed,
		"body_bytes", len(bodyBytes),
	)
}

// HandleModels 处理受保护的 GET /v1/models 请求，向 xAI 模型列表接口发起
// 无 body GET 并透传上游响应。鉴权、provider 类型校验与 401 刷新重试规则
// 与 HandleResponses 一致。
func (h *BridgeHandler) HandleModels(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := newRequestID()
	logger := slog.With("request_id", requestID)

	// 1. 校验 bridge bearer token
	if !h.authenticate(r) {
		writeError(w, http.StatusUnauthorized, "bridge: invalid bearer token")
		logger.Warn("bridge: auth failed", "path", r.URL.Path)
		return
	}

	// 2. 读取并校验 provider 类型头
	providerType := strings.TrimSpace(r.Header.Get("X-Oh-My-API-Bridge-Provider"))
	if providerType == "" {
		writeError(w, http.StatusBadRequest, "bridge: missing X-Oh-My-API-Bridge-Provider header")
		logger.Warn("bridge: missing provider type header", "path", r.URL.Path)
		return
	}

	tokenProvider, err := h.registry.Get(providerType)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("bridge: %v", err))
		logger.Warn("bridge: unsupported provider type", "path", r.URL.Path, "provider", providerType)
		return
	}

	// 3. 获取 access token
	ctx := r.Context()
	accessToken, _, err := tokenProvider.GetAccessToken(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "bridge: cannot obtain access token")
		logger.Error("bridge: token error", "path", r.URL.Path, "error", tokenErrorSafe(err))
		return
	}

	// 4. 向 xAI 模型列表接口发起无 body GET
	resp, proxyErr := proxyModelsToXAI(ctx, accessToken)

	// 5. 若上游返回 401，执行一次强制刷新并重试
	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		logger.Info("bridge: upstream 401, attempting forced refresh and retry", "provider", providerType)

		newToken, refreshErr := tokenProvider.ForceRefresh(ctx)
		if refreshErr != nil {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("bridge: token refresh failed after upstream 401: %v", refreshErr))
			logger.Error("bridge: forced refresh failed", "provider", providerType, "error", tokenErrorSafe(refreshErr))
			return
		}

		resp, proxyErr = proxyModelsToXAI(ctx, newToken)
	}

	// 6. 透传上游响应；无上游响应时返回本地 502
	statusCode := http.StatusBadGateway
	logLevel := slog.LevelError

	if resp != nil {
		defer resp.Body.Close()
		statusCode = resp.StatusCode
		copyUpstreamHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)

		_, copyErr := io.Copy(w, resp.Body)
		if copyErr != nil {
			logger.Error("bridge: response copy error", "path", r.URL.Path, "error", copyErr)
		}

		if statusCode < 400 {
			logLevel = slog.LevelInfo
		} else {
			logLevel = slog.LevelWarn
		}
	} else {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("upstream request failed: %v", proxyErr))
	}

	logger.Log(ctx, logLevel, "request",
		"provider", providerType,
		"path", r.URL.Path,
		"status", statusCode,
		"duration_ms", time.Since(start).Milliseconds(),
	)
}

// HandleHealthz 处理 GET /healthz 请求。
// 返回进程存活状态、auth.json 可读性及 xai-oauth 凭证块存在性。
// 健康检查不做网络刷新。
func (h *BridgeHandler) HandleHealthz(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"status": "ok",
	}

	w.Header().Set("Content-Type", "application/json")

	// 仅做本地状态校验，不进行网络 refresh；路径为空时使用默认独立状态路径。
	authErr := AuthJSONReadable("")
	if authErr != nil {
		status["status"] = "degraded"
		status["auth_json_error"] = authErr.Error()
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	json.NewEncoder(w).Encode(status)
}

// authenticate 校验请求中的 bearer token 是否匹配。
// 使用 constant-time 比较防止 timing 攻击。
func (h *BridgeHandler) authenticate(r *http.Request) bool {
	if h.allowUnauthenticatedLoopback {
		return true
	}
	token := extractBearerToken(r)
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(h.bridgeToken)) == 1
}

// extractBearerToken 从 Authorization header 中提取 bearer token。
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(auth) < len(prefix) {
		return ""
	}
	if !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return auth[len(prefix):]
}

// writeError 写入一个标准 JSON 错误响应。
func writeError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// tokenErrorSafe 将 token 相关错误消息中可能出现的敏感信息脱敏后返回。
func tokenErrorSafe(err error) string {
	msg := err.Error()
	// 移除 "bearer ", "access_token", "refresh_token" 等可能跟在实际值后面的片段
	// 简化处理：只返回错误类型，不返回可能包含敏感值的完整消息
	if strings.Contains(msg, "access_token") || strings.Contains(msg, "refresh_token") {
		return "credential error (details redacted)"
	}
	// 如果错误涉及 401 body（上游可能返回 token），截断
	if len(msg) > 200 {
		return msg[:200] + "..."
	}
	return msg
}

// newRequestID 生成一个用于日志追踪的随机请求 ID。
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 极端情况下 fallback 到时间戳
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
