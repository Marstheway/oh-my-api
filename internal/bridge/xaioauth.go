package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// xaiOAuthClientID 固定使用 Hermes 基准版本的 xAI OAuth client ID。
	xaiOAuthClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	// xaiOAuthDiscoveryURL 固定使用 Hermes 基准版本的 discovery URL。
	xaiOAuthDiscoveryURL = "https://auth.x.ai/.well-known/openid-configuration"
	// xaiOAuthDeviceCodeURL 固定使用 Hermes 基准版本的设备码 URL。
	xaiOAuthDeviceCodeURL = "https://auth.x.ai/oauth2/device/code"
	// xaiOAuthScope 固定使用 Hermes 基准版本的 scope。
	xaiOAuthScope = "openid profile email offline_access grok-cli:access api:access"

	// fullLifetimeRefreshSkewSecs 长寿命/无法解析 token 的主动刷新窗口（对齐 Hermes XAI_ACCESS_TOKEN_REFRESH_SKEW_SECONDS）。
	fullLifetimeRefreshSkewSecs = 3600
	// shortLifetimeRefreshSkewSec 短寿命 token（剩余 ≤45min）的主动刷新窗口（对齐 Hermes 120s 规则）。
	shortLifetimeRefreshSkewSec = 120

	// xaiRefreshHTTPTimeout 刷新请求 HTTP 客户端超时。
	xaiRefreshHTTPTimeout = 20 * time.Second
	// xaiDiscoveryHTTPTimeout discovery 请求 HTTP 客户端超时。
	xaiDiscoveryHTTPTimeout = 15 * time.Second
	// slowDownMaxInterval 轮询 slow_down 后 interval 上限（对齐 Hermes）。
	slowDownMaxInterval = 30

	// defaultStateDirMode 状态目录权限（仅属主）。
	defaultStateDirMode = 0o700
	// defaultStateFileMode 状态文件权限（仅属主可读写）。
	defaultStateFileMode = 0o600
)

// TokenProvider 定义“按 provider 获取可用 access token”的接口。
// 当前仅 xai-oauth 实现此接口。
type TokenProvider interface {
	// GetAccessToken 返回当前可用的 access token。
	// 若 token 处于主动刷新窗口内，实现层自动执行刷新并持久化。
	// notified 表示本次请求是否触发了主动刷新（非 401 强制刷新）。
	GetAccessToken(ctx context.Context) (token string, notified bool, err error)

	// ForceRefresh 强制刷新 access token，忽略过期判断。
	// 用于上游返回 401 时的恢复路径。
	ForceRefresh(ctx context.Context) (string, error)
}

// LastAuthError 描述刷新失败后的安全状态快照。
// 不含任何敏感 token；relogin_required 决定是否引导用户重新登录。
type LastAuthError struct {
	Provider        string `json:"provider"`
	Code            string `json:"code"`
	Message         string `json:"message"`
	Reason          string `json:"reason"`
	ReloginRequired bool   `json:"relogin_required"`
	At              string `json:"at"`
}

// OAuthState 表示 bridge 独立的 xAI OAuth 状态文件结构。
// 该结构不读取、不导入、不兼容 Hermes auth.json 格式。
type OAuthState struct {
	// Path 是状态文件在磁盘上的位置（不序列化）。
	Path string `json:"-"`
	// AccessToken 是 xAI OAuth access token。
	AccessToken string `json:"access_token,omitempty"`
	// RefreshToken 是 xAI OAuth refresh token。
	RefreshToken string `json:"refresh_token,omitempty"`
	// IDToken 是 OIDC id token（可选）。
	IDToken string `json:"id_token,omitempty"`
	// TokenType 是 token 类型，默认 Bearer。
	TokenType string `json:"token_type,omitempty"`
	// ExpiresIn 是 access token 寿命（秒，可选）。
	ExpiresIn int `json:"expires_in,omitempty"`
	// LastRefresh 是上次成功刷新/登录的 UTC 时间。
	LastRefresh time.Time `json:"last_refresh,omitempty"`
	// TokenEndpoint 是 discovery 返回并经验证的 token endpoint。
	TokenEndpoint string `json:"token_endpoint,omitempty"`
	// Discovery 是缓存的 discovery（含 token_endpoint）；保留用于校验与重新校验。
	Discovery Discovery `json:"discovery,omitempty"`
	// LastAuthError 记录刷新失败的固定安全错误（可选）。
	LastAuthError *LastAuthError `json:"last_auth_error,omitempty"`
}

// Discovery 包含 OIDC discovery 端点信息。
type Discovery struct {
	TokenEndpoint         string `json:"token_endpoint,omitempty"`
	AuthorizationEndpoint string `json:"authorization_endpoint,omitempty"`
}

// DefaultAuthStatePath 返回 bridge 独立的 OAuth 状态文件路径：
// ~/.oh-my-api/bridge/auth.json。该路径固定，不读取 Hermes 任何文件。
func DefaultAuthStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("cannot determine home directory for auth state")
	}
	return filepath.Join(home, ".oh-my-api", "bridge", "auth.json"), nil
}

// XAIOAuthProvider 实现 xAI OAuth 的独立凭证读取与刷新。
// 状态唯一存放在独立的 bridge 状态文件，不依赖 Hermes。
type XAIOAuthProvider struct {
	statePath string
	mu        sync.Mutex // 进程内串行读-判定-刷新-写，避免 token 轮换损坏状态
}

// NewXAIOAuthProvider 创建 xAI OAuth 凭证读取器。
// path 为空时使用默认独立状态路径（~/.oh-my-api/bridge/auth.json）。
func NewXAIOAuthProvider(path string) *XAIOAuthProvider {
	if strings.TrimSpace(path) == "" {
		if def, err := DefaultAuthStatePath(); err == nil {
			path = def
		}
	}
	return &XAIOAuthProvider{statePath: path}
}

// GetAccessToken 返回当前可用的 xAI access token。
// 若 token 处于 Hermes 定义的主动刷新窗口内，自动执行刷新并回写状态文件。
// 读-判窗口-刷新-写回在进程内互斥锁内完成。
func (p *XAIOAuthProvider) GetAccessToken(ctx context.Context) (string, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	state, err := p.loadStateLocked()
	if err != nil {
		return "", false, err
	}
	if state.AccessToken == "" && state.RefreshToken == "" {
		return "", false, fmt.Errorf("xai-oauth: no access token or refresh token available")
	}
	if state.AccessToken == "" && state.RefreshToken != "" {
		tok, err := p.refreshAndWriteLocked(ctx, state)
		return tok, true, err
	}
	if p.needsRefresh(state) {
		// 重新加载最新状态：若持有锁期间其他 goroutine 已完成刷新，
		// 则直接复用新鲜 token，避免重复刷新（并发去重）。
		if fresh, lerr := p.loadStateLocked(); lerr == nil {
			if !p.needsRefresh(fresh) && strings.TrimSpace(fresh.AccessToken) != "" {
				return fresh.AccessToken, false, nil
			}
			state = fresh
		}
		tok, err := p.refreshAndWriteLocked(ctx, state)
		return tok, true, err
	}
	return state.AccessToken, false, nil
}

// ForceRefresh 强制刷新 access token，忽略过期判断。
// 401 恢复路径保持真强制刷新。
func (p *XAIOAuthProvider) ForceRefresh(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	state, err := p.loadStateLocked()
	if err != nil {
		return "", err
	}
	if state.RefreshToken == "" {
		return "", fmt.Errorf("xai-oauth: no refresh token available for forced refresh")
	}
	return p.refreshAndWriteLocked(ctx, state)
}

// loadStateLocked 在持有 p.mu 时读取并校验状态文件。
func (p *XAIOAuthProvider) loadStateLocked() (*OAuthState, error) {
	if strings.TrimSpace(p.statePath) == "" {
		return nil, fmt.Errorf("xai-oauth: auth state path is empty")
	}
	state, err := readOAuthState(p.statePath)
	if err != nil {
		return nil, err
	}
	return state, nil
}

// needsRefresh 判断 xAI access token 是否需要主动刷新。
// 根据 access token JWT exp 动态选择刷新窗口（对齐 Hermes _xai_proactive_refresh_skew_seconds）：
//   - 短寿命 token（剩余 ≤45min）：使用 120s 窗口
//   - 长寿命 token、缺失 exp 或 JWT 解析失败：使用 3600s 窗口
//
// 若基础信息缺失（expires_in 为 0 或 last_refresh 为零），返回 false，
// 让调用方先尝试使用现有 token，再根据上游 401 响应决定是否刷新。
func (p *XAIOAuthProvider) needsRefresh(state *OAuthState) bool {
	if state.ExpiresIn <= 0 {
		return false
	}
	if state.LastRefresh.IsZero() {
		return false
	}

	skew := proactiveRefreshSkewSeconds(state.AccessToken)
	// 当 access token 缺失时按短寿命处理（对齐 Hermes 对空 token 的 3600s 保守处理）。
	if state.AccessToken == "" && expiryLooksShortLived(state.ExpiresIn) {
		skew = shortLifetimeRefreshSkewSec
	}
	expiresAt := state.LastRefresh.Add(time.Duration(state.ExpiresIn) * time.Second)
	return time.Now().After(expiresAt.Add(-time.Duration(skew) * time.Second))
}

// proactiveRefreshSkewSeconds 根据 access token JWT exp 动态计算刷新窗口。
// 对齐 Hermes _xai_proactive_refresh_skew_seconds：
//   - 短寿命 token（剩余 ≤45min）：120s
//   - 长寿命 token 或 JWT 解析失败：3600s
func proactiveRefreshSkewSeconds(accessToken string) int {
	if accessToken == "" || !strings.Contains(accessToken, ".") {
		return fullLifetimeRefreshSkewSecs
	}
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return fullLifetimeRefreshSkewSecs
	}
	payloadB64 := parts[1]
	payloadB64 += strings.Repeat("=", (4-len(payloadB64)%4)%4)

	payload, err := base64.URLEncoding.DecodeString(payloadB64)
	if err != nil {
		return fullLifetimeRefreshSkewSecs
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return fullLifetimeRefreshSkewSecs
	}
	if claims.Exp <= 0 {
		return fullLifetimeRefreshSkewSecs
	}
	remaining := claims.Exp - float64(time.Now().Unix())
	if remaining <= 0 {
		return fullLifetimeRefreshSkewSecs
	}
	if remaining <= 45*60 {
		return shortLifetimeRefreshSkewSec
	}
	return fullLifetimeRefreshSkewSecs
}

func expiryLooksShortLived(expiresIn int) bool {
	return expiresIn > 0 && expiresIn <= 45*60
}

// refreshAndWriteLocked 向 token endpoint 发起刷新请求，并原子回写状态。
// 调用方必须已持有 p.mu。
func (p *XAIOAuthProvider) refreshAndWriteLocked(ctx context.Context, state *OAuthState) (string, error) {
	endpoint := strings.TrimSpace(state.TokenEndpoint)
	if endpoint == "" && state.Discovery.TokenEndpoint != "" {
		endpoint = strings.TrimSpace(state.Discovery.TokenEndpoint)
	}
	if endpoint != "" {
		if err := validateXAITokenEndpoint(endpoint); err != nil {
			return "", err
		}
	} else {
		// token endpoint 缺失时重新 discovery（经验证返回）。
		disc, err := discoverXAITokenEndpoint(p.httpClient())
		if err != nil {
			return "", err
		}
		endpoint = disc
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", state.RefreshToken)
	form.Set("client_id", xaiOAuthClientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("xai-oauth: cannot create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("xai-oauth: refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 不读取或传播上游原始 body；只依据状态码走固定安全错误路径。
		return "", p.handleRefreshError(ctx, state, resp.StatusCode)
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("xai-oauth: cannot parse refresh response: %w", err)
	}
	if strings.TrimSpace(result.AccessToken) == "" {
		return "", p.handleRefreshError(ctx, state, http.StatusBadRequest)
	}

	// 成功：保留未返回的旧 refresh token，更新其他字段，清除 last_auth_error，原子写入。
	state.AccessToken = result.AccessToken
	if strings.TrimSpace(result.RefreshToken) != "" {
		state.RefreshToken = result.RefreshToken
	}
	if strings.TrimSpace(result.IDToken) != "" {
		state.IDToken = result.IDToken
	}
	if result.ExpiresIn > 0 {
		state.ExpiresIn = result.ExpiresIn
	}
	if strings.TrimSpace(result.TokenType) != "" {
		state.TokenType = result.TokenType
	}
	state.TokenEndpoint = endpoint
	state.LastRefresh = time.Now().UTC()
	state.LastAuthError = nil

	if err := writeOAuthState(state); err != nil {
		return result.AccessToken, fmt.Errorf("xai-oauth: token refreshed but failed to persist: %w", err)
	}
	return result.AccessToken, nil
}

// handleRefreshError 处理刷新返回的非 200 或缺失 access token。
// 对齐 Hermes：400/401/403 为终止错误，清除本地 token 并写入 last_auth_error；
// 其他错误保留现有状态并返回失败。所有 message 均为固定安全字符串，
// 不持久化或返回任何上游原始响应 body/detail。
func (p *XAIOAuthProvider) handleRefreshError(ctx context.Context, state *OAuthState, statusCode int) error {
	if statusCode == http.StatusBadRequest || statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		var code, message string
		switch statusCode {
		case http.StatusBadRequest:
			code = "xai_refresh_failed"
			message = "xAI OAuth refresh rejected the refresh token (HTTP 400); re-authentication required"
		case http.StatusUnauthorized:
			code = "xai_refresh_failed"
			message = "xAI OAuth refresh rejected the refresh token (HTTP 401); re-authentication required"
		case http.StatusForbidden:
			code = "xai_oauth_tier_denied"
			message = "xAI OAuth/API access denied by xAI (HTTP 403 tier/entitlement gate); re-logging in will not change this"
		}
		state.AccessToken = ""
		state.RefreshToken = ""
		state.LastAuthError = &LastAuthError{
			Provider:        "xai-oauth",
			Code:            code,
			Message:         message,
			Reason:          "runtime_refresh_failure",
			ReloginRequired: statusCode != http.StatusForbidden,
			At:              time.Now().UTC().Format(time.RFC3339),
		}
		_ = writeOAuthState(state)
		return fmt.Errorf("xai-oauth: %s", message)
	}
	return fmt.Errorf("xai-oauth: token refresh failed with HTTP %d", statusCode)
}

// httpClient 返回生产 HTTP 客户端；测试通过包内 transport 注入覆写此行为。
func (p *XAIOAuthProvider) httpClient() *http.Client {
	return xaiHTTPClient
}

// xaiHTTPClient 是生产 OAuth HTTP 客户端；测试可临时替换其 Transport。
var xaiHTTPClient = &http.Client{Timeout: xaiRefreshHTTPTimeout}

// readOAuthState 读取、校验并解析独立状态文件。
// 读取时逐级以 Lstat 校验状态目录链与状态文件本身的安全属性。
func readOAuthState(path string) (*OAuthState, error) {
	if err := checkStateFileSafe(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read auth state: %w", err)
	}
	var state OAuthState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("cannot parse auth state: %w", err)
	}
	state.Path = path
	if state.TokenType == "" {
		state.TokenType = "Bearer"
	}
	return &state, nil
}

// writeOAuthState 原子地将独立状态写入磁盘：逐级校验目录链安全、创建 0700
// 目录、0600 临时文件后 rename。已有目录若权限不安全会被拒绝，不会因
// MkdirAll 静默接受。
func writeOAuthState(state *OAuthState) error {
	path := strings.TrimSpace(state.Path)
	if path == "" {
		return fmt.Errorf("auth state path is empty")
	}
	dir := filepath.Dir(path)
	if err := ensureStateDirSafe(dir, true); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot marshal auth state: %w", err)
	}
	tmpPath := filepath.Join(
		dir,
		fmt.Sprintf("%s.tmp.%d.%d", filepath.Base(path), os.Getpid(), time.Now().UnixNano()),
	)
	if err := os.WriteFile(tmpPath, data, defaultStateFileMode); err != nil {
		return fmt.Errorf("cannot write temp auth state: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("cannot persist auth state: %w", err)
	}
	return nil
}

// ensureStateDirSafe 逐级（从 home 下首个组件到状态目录）以 Lstat 校验目录链：
// 拒绝任意符号链接、非目录以及 group/other 可写目录。当 create 为 true 时，
// 缺失的中间目录以 0700 创建；为 false 时缺失即报错（读取/health 场景不创建目录）。
// 已存在目录若权限不安全则无论 create 均直接拒绝，不再静默接受。
func ensureStateDirSafe(dir string, create bool) error {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return fmt.Errorf("cannot determine home directory for auth state")
	}
	home = filepath.Clean(home)
	target := filepath.Clean(dir)
	if !strings.HasPrefix(target, home+string(os.PathSeparator)) && target != home {
		return fmt.Errorf("auth state directory %q is outside home %q", target, home)
	}

	// 从 home 起逐级构建并校验。
	rel, err := filepath.Rel(home, target)
	if err != nil {
		return fmt.Errorf("auth state directory %q is not under home: %w", target, err)
	}
	// 状态目录即为 HOME 本身时，仍需校验 HOME 的安全属性。
	if rel == "." || rel == "" {
		return checkDirComponentSafe(home, false)
	}
	current := home
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		if err := checkDirComponentSafe(current, create); err != nil {
			return err
		}
	}
	return nil
}

// checkDirComponentSafe 以 Lstat 校验单个目录组件：缺失时若 create 为 true
// 则创建为 0700，否则报错；已存在则拒绝符号链接/非目录/group-other 可写；
// 创建后再次校验实际属性。读取与写入共用此规则，仅 create 行为不同。
func checkDirComponentSafe(path string, create bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if !create {
			return fmt.Errorf("auth state directory %q does not exist", path)
		}
		if mkErr := os.Mkdir(path, defaultStateDirMode); mkErr != nil {
			return fmt.Errorf("cannot create auth state directory %q: %w", path, mkErr)
		}
		return checkDirComponentSafe(path, create)
	}
	if err != nil {
		return fmt.Errorf("cannot stat auth state directory %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("auth state directory %q is a symlink; refusing to use it", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("auth state path component %q is not a directory", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("auth state directory %q is group/other writable; refusing to use it", path)
	}
	return nil
}

// checkStateFileSafe 校验独立状态文件及其目录链的安全属性：
//  1. 从 home 到状态目录逐级 Lstat 校验（复用 ensureStateDirSafe），
//     拒绝任意中间父目录（如 .oh-my-api、.oh-my-api/bridge）的符号链接、
//     非目录或 group/other 可写。
//  2. 校验状态文件本体：普通文件、非符号链接、无 group/other 权限。
//
// 读取、health 与写入使用同一套规则（ensureStateDirSafe + 文件本体校验），
// 保证各路径对目录链的安全判定一致。
func checkStateFileSafe(path string) error {
	dir := filepath.Dir(path)
	if err := ensureStateDirSafe(dir, false); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("cannot stat auth state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("auth state is a symlink; refusing to use it")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("auth state is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("auth state has group/other permissions; refusing to use it")
	}
	return nil
}

// AuthJSONReadable 检查独立状态文件是否可读、安全且包含可用的 xai-oauth 凭证。
// 复用与 readOAuthState 相同的安全校验，使后续 health 检查可以安全使用它。
// 只要有 access_token 或 refresh_token 任一即视为可用（token 缺失可通过刷新恢复）。
// 用于 /healthz 健康检查，不执行网络刷新，且不返回任何 token 内容。
func AuthJSONReadable(path string) error {
	if strings.TrimSpace(path) == "" {
		def, err := DefaultAuthStatePath()
		if err != nil {
			return err
		}
		path = def
	}
	state, err := readOAuthState(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(state.AccessToken) != "" || strings.TrimSpace(state.RefreshToken) != "" {
		return nil
	}
	return fmt.Errorf("auth state: no usable xai-oauth credentials")
}

// validateXAITokenEndpoint 校验 token endpoint 必须为 HTTPS 且主机在 x.ai 或 *.x.ai。
// 对齐 Hermes _xai_validate_oauth_endpoint，无条件拒绝非受信端点，避免 bearer 泄漏。
func validateXAITokenEndpoint(endpoint string) error {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("xai-oauth: invalid token endpoint")
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("xai-oauth: token endpoint must use https")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host != "x.ai" && !strings.HasSuffix(host, ".x.ai") {
		return fmt.Errorf("xai-oauth: token endpoint host %q is not on x.ai", host)
	}
	return nil
}

// discoverXAITokenEndpoint 获取并校验 discovery，返回经验证的 token endpoint。
// 始终使用固定的生产 discovery URL；测试只能注入 transport 截获该固定 URL。
func discoverXAITokenEndpoint(client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, xaiOAuthDiscoveryURL, nil)
	if err != nil {
		return "", fmt.Errorf("xai-oauth: cannot create discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("xai-oauth: discovery failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("xai-oauth: discovery returned status %d", resp.StatusCode)
	}
	var payload struct {
		TokenEndpoint         string `json:"token_endpoint"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("xai-oauth: discovery returned invalid JSON: %w", err)
	}
	if strings.TrimSpace(payload.TokenEndpoint) == "" {
		return "", fmt.Errorf("xai-oauth: discovery missing token_endpoint")
	}
	if strings.TrimSpace(payload.AuthorizationEndpoint) == "" {
		return "", fmt.Errorf("xai-oauth: discovery missing authorization_endpoint")
	}
	if err := validateXAIEndpoint(payload.TokenEndpoint); err != nil {
		return "", err
	}
	if err := validateXAIEndpoint(payload.AuthorizationEndpoint); err != nil {
		return "", err
	}
	return payload.TokenEndpoint, nil
}
