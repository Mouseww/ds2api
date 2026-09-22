package client

import (
	"bytes"
	"context"
	"crypto/rand"
	dsprotocol "ds2api/internal/deepseek/protocol"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"ds2api/internal/auth"
	"ds2api/internal/config"
)

// loginDeviceRotationLimit bounds how many times Login regenerates the
// account device fingerprint after DeepSeek risk control rejects it
// (RISK_DEVICE_DETECTED). A flagged device_id poisons every later login for
// the account, so the only recovery is a fresh random fingerprint.
const loginDeviceRotationLimit = 2

// loginDeviceRotationPause spaces out fingerprint-rotation retries so a
// rejected login is not immediately followed by another one from the same
// source. Tests zero it out.
var loginDeviceRotationPause = time.Second

// loginServiceHTTPClient is the HTTP client used to call the browser-based
// login service for accounts whose login endpoint is protected by AWS WAF.
var loginServiceHTTPClient = &http.Client{Timeout: 35 * time.Second}

func (c *Client) Login(ctx context.Context, acc config.Account) (string, error) {
	// If a browser-based login service URL is configured, delegate the
	// WAF-protected login to it. The service performs a native form
	// submission in headless Chromium, which passes the AWS WAF challenge
	// that plain HTTP / fetch() requests cannot.
	if url := c.loginServiceURL(); url != "" {
		return c.loginWithService(ctx, url, acc)
	}

	// The x-device-id header needs a stable per-account UUID, generated once
	// and persisted alongside the body-level device_id.
	deviceUUID, err := c.ensureAccountDeviceUUID(acc)
	if err != nil {
		return "", err
	}
	acc.DeviceUUID = deviceUUID
	for rotation := 0; ; rotation++ {
		deviceID, err := c.ensureAccountDeviceID(acc)
		if err != nil {
			return "", err
		}
		acc.DeviceID = deviceID
		token, err := c.loginOnce(ctx, acc, deviceID)
		if err == nil {
			return token, nil
		}
		if rotation >= loginDeviceRotationLimit || !isRiskDeviceDetected(err) {
			return "", err
		}
		newDeviceID, rotErr := c.rotateAccountDeviceID(acc)
		if rotErr != nil {
			return "", rotErr
		}
		acc.DeviceID = newDeviceID
		config.Logger.Warn(
			"[login] device fingerprint flagged by risk control, rotated and retrying",
			"account", acc.Identifier(),
			"attempt", rotation+1,
			"old_error", err.Error(),
		)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(loginDeviceRotationPause):
		}
	}
}

// loginServiceURL returns the configured browser-based login service base URL
// (e.g. "http://127.0.0.1:8787"), or "" when not configured.
func (c *Client) loginServiceURL() string {
	if c == nil || c.Store == nil {
		return ""
	}
	return c.Store.Snapshot().LoginServiceURL
}

// loginWithService delegates the WAF-protected login to a browser-based login
// service. The service performs a native form submission in headless Chromium
// and returns the session token.
func (c *Client) loginWithService(ctx context.Context, serviceURL string, acc config.Account) (string, error) {
	payload := map[string]string{}
	if email := strings.TrimSpace(acc.Email); email != "" {
		payload["email"] = email
	} else if mobile := strings.TrimSpace(acc.Mobile); mobile != "" {
		payload["mobile"] = mobile
	} else {
		return "", errors.New("missing email/mobile")
	}
	payload["password"] = strings.TrimSpace(acc.Password)

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serviceURL+"/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := loginServiceHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("login service unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("login service read error: %w", err)
	}

	var result struct {
		Success bool   `json:"success"`
		Token   string `json:"token"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("login service bad response: %w", err)
	}
	if !result.Success || result.Token == "" {
		msg := result.Error
		if msg == "" {
			msg = "unknown error"
		}
		return "", fmt.Errorf("login service: %s", msg)
	}
	return result.Token, nil
}

// loginHeaders returns a copy of the shared BaseHeaders with per-account
// device characteristics: the x-device-id UUID and a timezone offset derived
// from the account identifier.
func loginHeaders(acc config.Account) map[string]string {
	headers := make(map[string]string, len(dsprotocol.BaseHeaders)+2)
	for k, v := range dsprotocol.BaseHeaders {
		headers[k] = v
	}
	headers["x-client-timezone-offset"] = strconv.Itoa(accountTimezoneOffset(acc.Identifier()))
	if uuid := strings.TrimSpace(acc.DeviceUUID); uuid != "" {
		headers["x-device-id"] = uuid
	}
	return headers
}

// accountTimezoneOffset returns a stable timezone offset (in seconds) derived
// from the account identifier hash. The same account always maps to the same
// offset; different accounts spread across a small set of realistic values so
// they are less likely to be recognized as clones of a single device.
func accountTimezoneOffset(identifier string) int {
	if identifier == "" {
		return 28800 // UTC+8 default
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(identifier))
	offsets := []int{
		28800,  // UTC+8 Beijing
		32400,  // UTC+9
		36000,  // UTC+10
		25200,  // UTC+7
		-14400, // UTC-4
		-18000, // UTC-5
		-25200, // UTC-7
		0,      // UTC
	}
	return offsets[int(h.Sum32())%len(offsets)]
}

// loginOnce performs a single login attempt with the given device fingerprint.
func (c *Client) loginOnce(ctx context.Context, acc config.Account, deviceID string) (string, error) {
	clients := c.requestClientsForAccount(acc)
	payload := map[string]any{
		"email":     "",
		"mobile":    "",
		"password":  strings.TrimSpace(acc.Password),
		"area_code": "",
		"device_id": deviceID,
		"os":        "web",
	}
	if email := strings.TrimSpace(acc.Email); email != "" {
		payload["email"] = email
	} else if mobile := strings.TrimSpace(acc.Mobile); mobile != "" {
		loginMobile, areaCode := normalizeMobileForLogin(mobile)
		payload["mobile"] = loginMobile
		payload["area_code"] = areaCode
	} else {
		return "", errors.New("missing email/mobile")
	}
	resp, err := c.postJSON(ctx, clients.regular, clients.fallback, dsprotocol.DeepSeekLoginURL, loginHeaders(acc), payload)
	if err != nil {
		return "", err
	}
	code := intFrom(resp["code"])
	if code != 0 {
		return "", fmt.Errorf("login failed: %v", resp["msg"])
	}
	data, _ := resp["data"].(map[string]any)
	if intFrom(data["biz_code"]) != 0 {
		return "", fmt.Errorf("login failed: %v", data["biz_msg"])
	}
	bizData, _ := data["biz_data"].(map[string]any)
	user, _ := bizData["user"].(map[string]any)
	token, _ := user["token"].(string)
	if strings.TrimSpace(token) == "" {
		return "", errors.New("missing login token")
	}
	logLoginBizData(acc, bizData)
	c.updateAccountBanStatus(acc, user)
	ssoID, _ := user["id"].(string)
	loginAuth := &auth.RequestAuth{
		UseConfigToken: true,
		DeepSeekToken:  token,
		AccountID:      acc.Identifier(),
		Account:        acc,
	}
	auth.WithAuth(ctx, loginAuth)
	c.reportClientSettingsAfterLogin(ctx, loginAuth, ssoID)
	return token, nil
}

// logLoginBizData dumps the login response biz_data (secrets masked) so we can
// identify the ban/status fields DeepSeek returns during token refresh.
func logLoginBizData(acc config.Account, bizData map[string]any) {
	b, err := json.Marshal(maskSecretValues(bizData))
	if err != nil {
		b = []byte(fmt.Sprintf("%#v", bizData))
	}
	config.Logger.Info("[login] biz_data dump", "account", acc.Identifier(), "biz_data", string(b))
}

func maskSecretValues(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if isSecretKey(k) {
				out[k] = maskTokenValue(val)
			} else {
				out[k] = maskSecretValues(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = maskSecretValues(val)
		}
		return out
	default:
		return v
	}
}

func isSecretKey(k string) bool {
	lk := strings.ToLower(k)
	return lk == "token" ||
		lk == "jwt" ||
		lk == "password" ||
		lk == "secret" ||
		strings.Contains(lk, "token")
}

func maskTokenValue(v any) string {
	s, _ := v.(string)
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "***" + s[len(s)-4:]
}

func (c *Client) updateAccountBanStatus(acc config.Account, user map[string]any) {
	if c == nil || c.Store == nil || user == nil {
		return
	}
	isMuted, muteUntil, status := extractBanFields(user)
	if err := c.Store.UpdateAccountBanStatus(acc.Identifier(), isMuted, muteUntil, status); err != nil {
		config.Logger.Warn("[login] persist ban status failed", "account", acc.Identifier(), "error", err)
	}
}

func extractBanFields(user map[string]any) (isMuted int, muteUntil float64, status int) {
	if chat, ok := user["chat"].(map[string]any); ok {
		isMuted = intFrom(chat["is_muted"])
		if v, ok := chat["mute_until"].(float64); ok {
			muteUntil = v
		}
	}
	status = intFrom(user["status"])
	return
}

func (c *Client) ensureAccountDeviceID(acc config.Account) (string, error) {
	deviceID := strings.TrimSpace(acc.DeviceID)
	if deviceID != "" {
		return deviceID, nil
	}
	deviceID, err := createRandomDeviceID()
	if err != nil {
		return "", err
	}
	if err := c.persistAccountDeviceID(acc, deviceID); err != nil {
		return "", err
	}
	return deviceID, nil
}

// rotateAccountDeviceID replaces the account's persisted device fingerprint
// with a fresh random one and returns it. Used when risk control has flagged
// the previous fingerprint (RISK_DEVICE_DETECTED).
func (c *Client) rotateAccountDeviceID(acc config.Account) (string, error) {
	deviceID, err := createRandomDeviceID()
	if err != nil {
		return "", err
	}
	if err := c.persistAccountDeviceID(acc, deviceID); err != nil {
		return "", err
	}
	return deviceID, nil
}

// persistAccountField writes a field on the account identified by
// acc.Identifier() through the config store. Without a store or account
// identity the call is a no-op.
func (c *Client) persistAccountField(acc config.Account, set func(*config.Account)) error {
	if c == nil || c.Store == nil {
		return nil
	}
	identifier := acc.Identifier()
	if identifier == "" {
		return nil
	}
	return c.Store.Update(func(cfg *config.Config) error {
		for i := range cfg.Accounts {
			if cfg.Accounts[i].Identifier() == identifier {
				set(&cfg.Accounts[i])
				return nil
			}
		}
		return errors.New("account not found")
	})
}

// persistAccountDeviceID stores deviceID on the account so later logins reuse
// the same body-level device fingerprint.
func (c *Client) persistAccountDeviceID(acc config.Account, deviceID string) error {
	return c.persistAccountField(acc, func(a *config.Account) { a.DeviceID = deviceID })
}

// persistAccountDeviceUUID stores the UUID on the account so later logins
// reuse the same x-device-id header value.
func (c *Client) persistAccountDeviceUUID(acc config.Account, uuid string) error {
	return c.persistAccountField(acc, func(a *config.Account) { a.DeviceUUID = uuid })
}

// ensureAccountDeviceUUID returns the account's existing DeviceUUID or
// generates and persists a new random UUID v4.
func (c *Client) ensureAccountDeviceUUID(acc config.Account) (string, error) {
	uuid := strings.TrimSpace(acc.DeviceUUID)
	if uuid != "" {
		return uuid, nil
	}
	var err error
	uuid, err = createRandomDeviceUUID()
	if err != nil {
		return "", err
	}
	if err := c.persistAccountDeviceUUID(acc, uuid); err != nil {
		return "", err
	}
	return uuid, nil
}

// createRandomDeviceUUID generates a random UUID v4 string.
func createRandomDeviceUUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

// isRiskDeviceDetected reports whether a login error is DeepSeek risk control
// rejecting the device fingerprint (RISK_DEVICE_DETECTED). The flagged
// device_id stays rejected for later logins, so Login responds by rotating
// to a fresh random fingerprint.
func isRiskDeviceDetected(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "RISK_DEVICE_DETECTED")
}

func createRandomDeviceID() (string, error) {
	buf := make([]byte, 64)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "B" + base64.StdEncoding.EncodeToString(buf), nil
}

func (c *Client) reportClientSettingsAfterLogin(ctx context.Context, a *auth.RequestAuth, ssoID string) {
	if c == nil || a == nil || strings.TrimSpace(a.DeepSeekToken) == "" || strings.TrimSpace(a.Account.DeviceID) == "" {
		return
	}
	if err := c.ReportClientSettings(ctx, a, ssoID); err != nil {
		config.Logger.Warn("[client_settings] report after login failed", "account", a.AccountID, "error", err)
	}
}

// CreateSession creates one DeepSeek chat session. It is policy-free: on a
// captcha / rate-limit / auth rejection it returns a typed *RequestFailure
// and never mutates the lease (no RecheckBan / RefreshToken / SwitchAccount);
// the shared failure policy in internal/completionruntime decides what
// happens to the lease. Network errors and unknown server failures are
// retried on the same account up to maxAttempts.
func (c *Client) CreateSession(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error) {
	if maxAttempts <= 0 {
		maxAttempts = c.maxRetries
	}
	clients := c.requestClientsForAuth(ctx, a)
	attempts := 0
	for attempts < maxAttempts {
		headers := c.authHeaders(a.DeepSeekToken)
		resp, status, err := c.postJSONWithStatus(ctx, clients.regular, clients.fallback, dsprotocol.DeepSeekCreateSessionURL, headers, map[string]any{})
		if err != nil {
			config.Logger.Warn("[create_session] request error", "error", err, "account", a.AccountID)
			attempts++
			continue
		}
		code, bizCode, msg, bizMsg := extractResponseStatus(resp)
		if status == http.StatusOK && code == 0 && bizCode == 0 {
			sessionID := extractCreateSessionID(resp)
			if sessionID != "" {
				return sessionID, nil
			}
		}
		if ch := DetectCaptchaChallenge(resp); ch != nil {
			config.Logger.Warn("[create_session] captcha challenge detected, account should be cooled down", "account", a.AccountID, "instruction", ch.Instruction, "image_url", ch.ImageURL, "rid", ch.Rid)
			return "", &RequestFailure{Op: "create session", Kind: FailureCaptchaRequired, Message: failureMessage(msg, bizMsg, "captcha challenge required")}
		}
		config.Logger.Warn("[create_session] failed", "status", status, "code", code, "biz_code", bizCode, "msg", msg, "biz_msg", bizMsg, "use_config_token", a.UseConfigToken, "account", a.AccountID)
		if failure := classifyResponseFailure("create session", status, code, bizCode, msg, bizMsg, a.UseConfigToken); failure != nil {
			return "", failure
		}
		attempts++
	}
	return "", errors.New("create session failed")
}

// GetPow fetches and solves the PoW challenge for the completion target path.
func (c *Client) GetPow(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error) {
	return c.GetPowForTarget(ctx, a, dsprotocol.DeepSeekCompletionTargetPath, maxAttempts)
}

// GetPowForTarget is policy-free like CreateSession: captcha / rate-limit /
// auth rejections return a typed *RequestFailure for the shared failure
// policy, while network errors and unknown failures retry on the same
// account up to maxAttempts.
func (c *Client) GetPowForTarget(ctx context.Context, a *auth.RequestAuth, targetPath string, maxAttempts int) (string, error) {
	if maxAttempts <= 0 {
		maxAttempts = c.maxRetries
	}
	targetPath = strings.TrimSpace(targetPath)
	if targetPath == "" {
		targetPath = dsprotocol.DeepSeekCompletionTargetPath
	}
	clients := c.requestClientsForAuth(ctx, a)
	attempts := 0
	for attempts < maxAttempts {
		if c.powCache != nil {
			if cachedChallenge, ok := c.powCache.get(a.AccountID, targetPath); ok {
				answer, err := ComputePow(ctx, cachedChallenge)
				if err != nil {
					attempts++
					continue
				}
				c.prefetchPowChallenge(a, targetPath)
				return BuildPowHeader(cachedChallenge, answer)
			}
		}
		headers := c.authHeaders(a.DeepSeekToken)
		resp, status, err := c.postJSONWithStatus(ctx, clients.regular, clients.fallback, dsprotocol.DeepSeekCreatePowURL, headers, map[string]any{"target_path": targetPath})
		if err != nil {
			config.Logger.Warn("[get_pow] request error", "error", err, "account", a.AccountID, "target_path", targetPath)
			attempts++
			continue
		}
		code, bizCode, msg, bizMsg := extractResponseStatus(resp)
		if status == http.StatusOK && code == 0 && bizCode == 0 {
			data, _ := resp["data"].(map[string]any)
			bizData, _ := data["biz_data"].(map[string]any)
			challenge, _ := bizData["challenge"].(map[string]any)
			answer, err := ComputePow(ctx, challenge)
			if err != nil {
				attempts++
				continue
			}
			c.prefetchPowChallenge(a, targetPath)
			return BuildPowHeader(challenge, answer)
		}
		if ch := DetectCaptchaChallenge(resp); ch != nil {
			config.Logger.Warn("[get_pow] captcha challenge detected, account should be cooled down", "account", a.AccountID, "target_path", targetPath, "instruction", ch.Instruction, "image_url", ch.ImageURL, "rid", ch.Rid)
			return "", &RequestFailure{Op: "get pow", Kind: FailureCaptchaRequired, Message: failureMessage(msg, bizMsg, "captcha challenge required")}
		}
		config.Logger.Warn("[get_pow] failed", "status", status, "code", code, "biz_code", bizCode, "msg", msg, "biz_msg", bizMsg, "use_config_token", a.UseConfigToken, "account", a.AccountID, "target_path", targetPath)
		if failure := classifyResponseFailure("get pow", status, code, bizCode, msg, bizMsg, a.UseConfigToken); failure != nil {
			return "", failure
		}
		attempts++
	}
	return "", errors.New("get pow failed")
}

func (c *Client) authHeaders(token string) map[string]string {
	headers := make(map[string]string, len(dsprotocol.BaseHeaders)+1)
	for k, v := range dsprotocol.BaseHeaders {
		headers[k] = v
	}
	headers["authorization"] = "Bearer " + token
	return headers
}

func isTokenInvalid(status int, code int, bizCode int, msg string, bizMsg string) bool {
	msg = strings.ToLower(strings.TrimSpace(msg) + " " + strings.TrimSpace(bizMsg))
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return true
	}
	if code == 40001 || code == 40002 || code == 40003 || bizCode == 40001 || bizCode == 40002 || bizCode == 40003 {
		return true
	}
	return strings.Contains(msg, "token") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "expired") ||
		strings.Contains(msg, "not login") ||
		strings.Contains(msg, "login required") ||
		strings.Contains(msg, "invalid jwt")
}

// classifyResponseFailure maps a rejected DeepSeek response to the typed
// RequestFailure that the shared failure policy (internal/completionruntime)
// acts on. Auth-indicative failures become managed/direct unauthorized,
// HTTP 429 becomes rate-limited. It returns nil for failures the policy
// cannot act on (unknown server errors), which stay in the RPC's own
// same-account transport retry loop.
func classifyResponseFailure(op string, status int, code int, bizCode int, msg string, bizMsg string, useConfigToken bool) *RequestFailure {
	if isTokenInvalid(status, code, bizCode, msg, bizMsg) || isAuthIndicativeBizFailure(msg, bizMsg) {
		return &RequestFailure{Op: op, Kind: authFailureKind(useConfigToken), Message: failureMessage(msg, bizMsg, op+" failed")}
	}
	if status == http.StatusTooManyRequests {
		return &RequestFailure{Op: op, Kind: FailureRateLimited, Message: failureMessage(msg, bizMsg, op+" failed")}
	}
	return nil
}

func isAuthIndicativeBizFailure(msg string, bizMsg string) bool {
	combined := strings.ToLower(strings.TrimSpace(msg) + " " + strings.TrimSpace(bizMsg))
	authKeywords := []string{
		"auth",
		"authorization",
		"credential",
		"expired",
		"invalid jwt",
		"jwt",
		"login",
		"not login",
		"session expired",
		"token",
		"unauthorized",
		"登录",
		"未登录",
		"认证",
		"凭证",
		"会话过期",
		"令牌",
	}
	for _, keyword := range authKeywords {
		if strings.Contains(combined, keyword) {
			return true
		}
	}
	return false
}

func authFailureKind(useConfigToken bool) FailureKind {
	if useConfigToken {
		return FailureManagedUnauthorized
	}
	return FailureDirectUnauthorized
}

func failureMessage(msg string, bizMsg string, fallback string) string {
	if trimmed := strings.TrimSpace(bizMsg); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(msg); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(fallback)
}

// DeepSeek has returned create-session ids in both biz_data.id and
// biz_data.chat_session.id across observed response variants; accept either.
func extractCreateSessionID(resp map[string]any) string {
	data, _ := resp["data"].(map[string]any)
	bizData, _ := data["biz_data"].(map[string]any)
	if sessionID, _ := bizData["id"].(string); strings.TrimSpace(sessionID) != "" {
		return strings.TrimSpace(sessionID)
	}
	if chatSession, ok := bizData["chat_session"].(map[string]any); ok {
		if sessionID, _ := chatSession["id"].(string); strings.TrimSpace(sessionID) != "" {
			return strings.TrimSpace(sessionID)
		}
	}
	return ""
}

func extractResponseStatus(resp map[string]any) (code int, bizCode int, msg string, bizMsg string) {
	code = intFrom(resp["code"])
	msg, _ = resp["msg"].(string)
	data, _ := resp["data"].(map[string]any)
	bizCode = intFrom(data["biz_code"])
	bizMsg, _ = data["biz_msg"].(string)
	if strings.TrimSpace(bizMsg) == "" {
		if bizData, ok := data["biz_data"].(map[string]any); ok {
			bizMsg, _ = bizData["msg"].(string)
		}
	}
	return code, bizCode, msg, bizMsg
}

func normalizeMobileForLogin(raw string) (mobile string, areaCode string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", ""
	}
	hasPlus := strings.HasPrefix(s, "+")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if digits == "" {
		return "", ""
	}
	if (hasPlus || strings.HasPrefix(digits, "86")) && strings.HasPrefix(digits, "86") && len(digits) == 13 {
		return digits[2:], "+86"
	}
	return digits, "+86"
}
