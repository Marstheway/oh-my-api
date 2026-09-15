package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeviceCodeResponse 是 xAI 设备码端点的响应。
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// LoginInfo 是登录层返回给调用方（CLI 层）的展示信息。
// 不含 token 本身；token 仅在轮询成功后才写入状态文件。
type LoginInfo struct {
	VerificationURL string
	UserCode        string
	ExpiresIn       int
	Interval        int
}

// Login 执行完整的 xAI 设备码登录流程：discovery → 申请设备码 → 轮询 → 写入独立状态文件。
// 它是 StartDeviceCode + PollDeviceToken + CompleteLogin 的便捷组合，供同步调用场景使用。
// CLI 层应使用拆分后的三个步骤，以便在设备码返回后立即展示并启动浏览器，再开始轮询。
func Login(ctx context.Context) (*LoginInfo, *OAuthState, error) {
	device, tokenEndpoint, err := StartDeviceCode(ctx)
	if err != nil {
		return nil, nil, err
	}
	info := &LoginInfo{
		VerificationURL: device.VerificationURIComplete,
		UserCode:        device.UserCode,
		ExpiresIn:       device.ExpiresIn,
		Interval:        device.Interval,
	}
	tokenResp, err := PollDeviceToken(ctx, tokenEndpoint, device)
	if err != nil {
		return info, nil, err
	}
	state, err := CompleteLogin(ctx, tokenResp, tokenEndpoint)
	if err != nil {
		return info, nil, err
	}
	return info, state, nil
}

// StartDeviceCode 执行 discovery 并申请设备码，返回设备码响应与已校验的 token endpoint。
// 不轮询、不写状态；调用方（CLI 层）可立即展示 verification URL/user code 并启动浏览器，
// 随后再调用 PollDeviceToken。verification_uri_complete 缺失时回退到 verification_uri。
func StartDeviceCode(ctx context.Context) (*DeviceCodeResponse, string, error) {
	discovery, err := fetchDiscovery(ctx, xaiDiscoveryHTTPTimeout)
	if err != nil {
		return nil, "", err
	}
	device, err := requestDeviceCode(ctx)
	if err != nil {
		return nil, "", err
	}
	// verification_uri_complete 优先；缺失时回退到 verification_uri（对齐 Hermes 回退行为）。
	if strings.TrimSpace(device.VerificationURIComplete) == "" {
		device.VerificationURIComplete = device.VerificationURI
	}
	return device, discovery.TokenEndpoint, nil
}

// CompleteLogin 将已轮询成功的 token 写入独立状态文件并返回持久化状态。
// 不读取或返回任何敏感 token 之外的内容；token 必须在调用前已通过轮询获得。
func CompleteLogin(ctx context.Context, token *DeviceTokenResult, tokenEndpoint string) (*OAuthState, error) {
	statePath, err := DefaultAuthStatePath()
	if err != nil {
		return nil, err
	}
	state := &OAuthState{
		Path:          statePath,
		AccessToken:   token.AccessToken,
		RefreshToken:  token.RefreshToken,
		IDToken:       token.IDToken,
		TokenType:     token.TokenType,
		ExpiresIn:     token.ExpiresIn,
		LastRefresh:   time.Now().UTC(),
		TokenEndpoint: tokenEndpoint,
		Discovery:     Discovery{TokenEndpoint: tokenEndpoint},
		LastAuthError: nil,
	}
	if strings.TrimSpace(state.TokenType) == "" {
		state.TokenType = "Bearer"
	}
	if err := writeOAuthState(state); err != nil {
		return nil, err
	}
	return state, nil
}

// discoveryDoc 是 OIDC discovery 文档的精简表示。
type discoveryDoc struct {
	TokenEndpoint         string `json:"token_endpoint"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
}

// fetchDiscovery 获取并校验 discovery，返回经验证的端点。
// 通过包级 xaiHTTPClient 发起请求（测试可注入 transport 截获固定 discovery URL）。
func fetchDiscovery(ctx context.Context, timeout time.Duration) (*discoveryDoc, error) {
	client := xaiHTTPClient
	if timeout > 0 {
		client = &http.Client{Timeout: timeout, Transport: xaiHTTPClient.Transport}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xaiOAuthDiscoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("xai-oauth: cannot create discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai-oauth: discovery failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xai-oauth: discovery returned status %d", resp.StatusCode)
	}
	var doc discoveryDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("xai-oauth: discovery returned invalid JSON: %w", err)
	}
	if strings.TrimSpace(doc.TokenEndpoint) == "" || strings.TrimSpace(doc.AuthorizationEndpoint) == "" {
		return nil, fmt.Errorf("xai-oauth: discovery missing required endpoints")
	}
	// 两个端点都必须 HTTPS 且位于 x.ai 或 *.x.ai（对齐 Hermes 校验）。
	if err := validateXAIEndpoint(doc.TokenEndpoint); err != nil {
		return nil, err
	}
	if err := validateXAIEndpoint(doc.AuthorizationEndpoint); err != nil {
		return nil, err
	}
	return &doc, nil
}

// validateXAIEndpoint 校验任意 xAI OIDC 端点必须为 HTTPS 且主机在 x.ai 或 *.x.ai。
// 对齐 Hermes _xai_validate_oauth_endpoint，无条件拒绝非受信端点，避免凭据泄漏。
func validateXAIEndpoint(endpoint string) error {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("xai-oauth: invalid endpoint %q", endpoint)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("xai-oauth: endpoint must use https: %q", endpoint)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host != "x.ai" && !strings.HasSuffix(host, ".x.ai") {
		return fmt.Errorf("xai-oauth: endpoint host %q is not on x.ai", host)
	}
	return nil
}

// requestDeviceCode 向固定设备码 URL 申请设备码。
// 通过包级 xaiHTTPClient 发起请求（测试可注入 transport 截获固定设备码 URL）。
func requestDeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	client := xaiHTTPClient
	form := url.Values{}
	form.Set("client_id", xaiOAuthClientID)
	form.Set("scope", xaiOAuthScope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, xaiOAuthDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("xai-oauth: cannot create device-code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai-oauth: device-code request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 不回显上游原始 body，仅用固定安全信息表达失败。
		return nil, fmt.Errorf("xai-oauth: device-code request failed (HTTP %d)", resp.StatusCode)
	}
	var dc DeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return nil, fmt.Errorf("xai-oauth: device-code response invalid JSON: %w", err)
	}
	if strings.TrimSpace(dc.DeviceCode) == "" ||
		strings.TrimSpace(dc.UserCode) == "" ||
		strings.TrimSpace(dc.VerificationURI) == "" ||
		dc.ExpiresIn <= 0 ||
		dc.Interval <= 0 {
		return nil, fmt.Errorf("xai-oauth: device-code response missing required fields")
	}
	return &dc, nil
}

// DeviceTokenResult 是设备码轮询成功后返回的 token 结果。
// 导出以便 CLI 层在 CompleteLogin 前持有并传递，避免暴露写状态细节。
type DeviceTokenResult struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
}

// pollWaiter 是轮询等待原语；默认实现用 time.After 并监听 ctx.Done，
// 测试可替换为即时返回的实现以避免真实睡眠。
var pollWaiter = func(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// PollDeviceToken 按 Hermes 规则轮询 token endpoint 直到成功或终止。
// 轮询间隔等待通过 pollWaiter 完成，可被 ctx 取消立即中断。
// 等待时长严格取 min(interval, 距 deadline 剩余)，不会越过 deadline；
// 到达 deadline 或 ctx 取消时立即退出，不再发起多余请求。
func PollDeviceToken(ctx context.Context, tokenEndpoint string, device *DeviceCodeResponse) (*DeviceTokenResult, error) {
	if err := validateXAITokenEndpoint(tokenEndpoint); err != nil {
		return nil, err
	}
	client := xaiHTTPClient

	deadline := time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)
	interval := device.Interval
	if interval < 1 {
		interval = 1
	}

	// firstPoll 标记是否已完成首次轮询：严格复制 Hermes，第一次 token poll
	// 立即发起，不在请求前等待；仅当收到 authorization_pending/slow_down 后才等待。
	firstPoll := true

	for {
		// 首次轮询无需等待，直接发起请求。
		if !firstPoll {
			// 不越过 deadline：剩余时间耗尽立即退出，不再轮询。
			if remaining := time.Until(deadline); remaining <= 0 {
				return nil, fmt.Errorf("xai-oauth: timed out waiting for device authorization")
			}

			// 等待 min(interval, 剩余)，避免等待越过 deadline。
			wait := time.Duration(interval) * time.Second
			if remaining := time.Until(deadline); wait > remaining {
				wait = remaining
			}
			pollWaiter(ctx, wait)

			// pollWaiter 可能因 ctx 取消返回；取消优先于 deadline 判定。
			if ctx.Err() != nil {
				return nil, fmt.Errorf("xai-oauth: device-code polling cancelled: %w", ctx.Err())
			}
			// 等待结束后若已越过 deadline，直接超时退出，不发起多余请求。
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("xai-oauth: timed out waiting for device authorization")
			}
		}

		form := url.Values{}
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		form.Set("client_id", xaiOAuthClientID)
		form.Set("device_code", device.DeviceCode)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, fmt.Errorf("xai-oauth: cannot create poll request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("xai-oauth: device-code poll failed: %w", err)
		}

		if resp.StatusCode == http.StatusOK {
			var tok DeviceTokenResult
			decErr := json.NewDecoder(resp.Body).Decode(&tok)
			resp.Body.Close()
			if decErr != nil {
				return nil, fmt.Errorf("xai-oauth: device token response invalid: %w", decErr)
			}
			if strings.TrimSpace(tok.AccessToken) == "" {
				return nil, fmt.Errorf("xai-oauth: device token response missing access_token")
			}
			if strings.TrimSpace(tok.RefreshToken) == "" {
				return nil, fmt.Errorf("xai-oauth: device token response missing refresh_token")
			}
			return &tok, nil
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		var errResp struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &errResp)

		// 首次轮询已发起且未成功（任何非 200 响应），后续轮询前需等待。
		// authorization_pending/slow_down 走 continue 进入下一轮循环并按 interval 等待；
		// 终止错误或非 JSON 错误直接返回，firstPoll 标记不再生效。
		firstPoll = false

		switch errResp.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval++
			if interval > slowDownMaxInterval {
				interval = slowDownMaxInterval
			}
			continue
		case "":
			// 非 JSON 错误：以固定安全信息失败，不回显原始 body。
			return nil, fmt.Errorf("xai-oauth: device-code poll failed (HTTP %d)", resp.StatusCode)
		default:
			// 终止 OAuth error：用固定安全信息表达，不回显上游描述。
			return nil, fmt.Errorf("xai-oauth: device-code poll failed with error %q", errResp.Error)
		}
	}
}
