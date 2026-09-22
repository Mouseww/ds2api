package config

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
)

type Config struct {
	Keys              []string                `json:"keys,omitempty"`
	APIKeys           []APIKey                `json:"api_keys,omitempty"`
	Accounts          []Account               `json:"accounts,omitempty"`
	Proxies           []Proxy                 `json:"proxies,omitempty"`
	ModelAliases      map[string]string       `json:"model_aliases,omitempty"`
	Admin             AdminConfig             `json:"admin,omitempty"`
	Runtime           RuntimeConfig           `json:"runtime,omitempty"`
	Responses         ResponsesConfig         `json:"responses,omitempty"`
	Embeddings        EmbeddingsConfig        `json:"embeddings,omitempty"`
	AutoDelete        AutoDeleteConfig        `json:"auto_delete"`
	CurrentInputFile  CurrentInputFileConfig  `json:"current_input_file,omitempty"`
	ThinkingInjection ThinkingInjectionConfig `json:"thinking_injection,omitempty"`
	Vercel            VercelConfig            `json:"vercel,omitempty"`
	LoginServiceURL   string                  `json:"login_service_url,omitempty"`
	VercelSyncHash    string                  `json:"_vercel_sync_hash,omitempty"`
	VercelSyncTime    int64                   `json:"_vercel_sync_time,omitempty"`
	AdditionalFields  map[string]any          `json:"-"`
}

type Account struct {
	Name       string `json:"name,omitempty"`
	Remark     string `json:"remark,omitempty"`
	Email      string `json:"email,omitempty"`
	Mobile     string `json:"mobile,omitempty"`
	Password   string `json:"password,omitempty"`
	Token      string `json:"token,omitempty"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceUUID string `json:"device_uuid,omitempty"`
	ProxyID    string `json:"proxy_id,omitempty"`
	// Enabled controls whether the account may be allocated by the load pool.
	// A nil pointer means "enabled" for backward compatibility with older configs.
	Enabled *bool `json:"enabled,omitempty"`
	// DisabledReason records why the account was disabled: "" (none),
	// "banned" (auto-disabled by ban detection) or "manual".
	DisabledReason string `json:"disabled_reason,omitempty"`
	// Ban status, refreshed on each login / token refresh.
	BanIsMuted   int     `json:"ban_is_muted,omitempty"`   // 1 = muted (user.chat.is_muted)
	BanMuteUntil float64 `json:"ban_mute_until,omitempty"` // unix ts when mute expires (user.chat.mute_until)
	BanStatus    int     `json:"ban_status,omitempty"`     // account status code (user.status)
}

// IsEnabled reports whether the account is eligible for pool allocation.
func (a Account) IsEnabled() bool {
	return a.Enabled == nil || *a.Enabled
}

// IsBanned reports whether the account is currently muted/banned by DeepSeek.
func (a Account) IsBanned() bool {
	return a.BanIsMuted == 1
}

type APIKey struct {
	Key    string `json:"key"`
	Name   string `json:"name,omitempty"`
	Remark string `json:"remark,omitempty"`
}

type Proxy struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Type     string `json:"type,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

func NormalizeProxy(p Proxy) Proxy {
	p.ID = strings.TrimSpace(p.ID)
	p.Name = strings.TrimSpace(p.Name)
	p.Type = strings.ToLower(strings.TrimSpace(p.Type))
	p.Host = strings.TrimSpace(p.Host)
	p.Username = strings.TrimSpace(p.Username)
	p.Password = strings.TrimSpace(p.Password)
	if p.ID == "" {
		p.ID = StableProxyID(p)
	}
	if p.Name == "" && p.Host != "" && p.Port > 0 {
		p.Name = fmt.Sprintf("%s:%d", p.Host, p.Port)
	}
	return p
}

func StableProxyID(p Proxy) string {
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(p.Type)) + "|" + strings.ToLower(strings.TrimSpace(p.Host)) + "|" + fmt.Sprintf("%d", p.Port) + "|" + strings.TrimSpace(p.Username)))
	return "proxy_" + hex.EncodeToString(sum[:6])
}

func (c *Config) ClearAccountTokens() {
	if c == nil {
		return
	}
	for i := range c.Accounts {
		c.Accounts[i].Token = ""
	}
}

func (c *Config) NormalizeCredentials() {
	if c == nil {
		return
	}
	normalizedAPIKeys := normalizeAPIKeys(c.APIKeys)
	if len(normalizedAPIKeys) > 0 {
		c.APIKeys = normalizedAPIKeys
		c.Keys = apiKeysToStrings(c.APIKeys)
	} else {
		c.Keys = normalizeKeys(c.Keys)
		c.APIKeys = apiKeysFromStrings(c.Keys, nil)
	}

	for i := range c.Accounts {
		c.Accounts[i].Name = strings.TrimSpace(c.Accounts[i].Name)
		c.Accounts[i].Remark = strings.TrimSpace(c.Accounts[i].Remark)
		c.Accounts[i].DeviceID = strings.TrimSpace(c.Accounts[i].DeviceID)
		c.Accounts[i].DeviceUUID = strings.TrimSpace(c.Accounts[i].DeviceUUID)
	}

	c.Vercel = NormalizeVercelConfig(c.Vercel)
	c.LoginServiceURL = strings.TrimSpace(c.LoginServiceURL)
	c.normalizeModelAliases()
}

// DropInvalidAccounts removes accounts that cannot be addressed by admin APIs
// (no email and no normalizable mobile). This prevents legacy token-only
// records from becoming orphaned empty entries after token stripping.
func (c *Config) DropInvalidAccounts() {
	if c == nil || len(c.Accounts) == 0 {
		return
	}
	kept := make([]Account, 0, len(c.Accounts))
	for _, acc := range c.Accounts {
		if acc.Identifier() == "" {
			continue
		}
		kept = append(kept, acc)
	}
	c.Accounts = kept
}

func (c *Config) normalizeModelAliases() {
	if c == nil {
		return
	}

	aliases := map[string]string{}
	for k, v := range c.ModelAliases {
		key := strings.TrimSpace(lower(k))
		val := strings.TrimSpace(lower(v))
		if key == "" || val == "" {
			continue
		}
		aliases[key] = val
	}
	if len(aliases) == 0 {
		c.ModelAliases = nil
	} else {
		c.ModelAliases = aliases
	}
}

type AdminConfig struct {
	PasswordHash      string `json:"password_hash,omitempty"`
	JWTExpireHours    int    `json:"jwt_expire_hours,omitempty"`
	JWTValidAfterUnix int64  `json:"jwt_valid_after_unix,omitempty"`
}

type RuntimeConfig struct {
	AccountMaxInflight        int `json:"account_max_inflight,omitempty"`
	AccountMaxQueue           int `json:"account_max_queue,omitempty"`
	GlobalMaxInflight         int `json:"global_max_inflight,omitempty"`
	TokenRefreshIntervalHours int `json:"token_refresh_interval_hours,omitempty"`
	// ActivePoolSize is the target number of simultaneously-active accounts in
	// the load pool. 0 means "use all eligible accounts" (the default).
	ActivePoolSize int `json:"active_pool_size,omitempty"`
	// DailyTokenLimitM is the global per-account token budget, expressed
	// in millions of tokens (m). 0 disables the token metric.
	DailyTokenLimitM int `json:"daily_token_limit_m,omitempty"`
	// DailyRequestLimit is the global per-account request budget.
	// 0 disables the request metric.
	DailyRequestLimit int `json:"daily_request_limit,omitempty"`
	// QuotaWindowHours is the rolling window, in hours, over which the
	// per-account token/request budgets are counted. 0 means the 24-hour
	// default. Usage that slides out of the window stops counting, so an
	// account that hit its budget re-enters the rotation pool automatically.
	QuotaWindowHours int `json:"quota_window_hours,omitempty"`
	// AutoContinueFix controls the fix for premature conversation stops: when
	// enabled, an explicit auto_continue flag overrides a FINISHED status that
	// arrived in the same frame, so ds2api keeps pulling continuation rounds.
	// A nil pointer means "enabled" for backward compatibility.
	AutoContinueFix *bool `json:"auto_continue_fix,omitempty"`
	// StripMaxTokens controls whether max_tokens / max_completion_tokens are
	// stripped from the upstream completion payload so a downstream token cap
	// cannot truncate the DeepSeek response mid-sentence. A nil pointer means
	// "enabled" for backward compatibility.
	StripMaxTokens *bool `json:"strip_max_tokens,omitempty"`
}

// DailyTokenLimit returns the per-account daily token budget in raw tokens.
// It returns 0 when the metric is disabled.
func (r RuntimeConfig) DailyTokenLimit() int64 {
	if r.DailyTokenLimitM <= 0 {
		return 0
	}
	return int64(r.DailyTokenLimitM) * 1_000_000
}

type ResponsesConfig struct {
	StoreTTLSeconds int `json:"store_ttl_seconds,omitempty"`
}

type EmbeddingsConfig struct {
	Provider string `json:"provider,omitempty"`
}

type AutoDeleteConfig struct {
	Mode     string `json:"mode,omitempty"`
	Sessions bool   `json:"sessions,omitempty"`
}

type CurrentInputFileConfig struct {
	Enabled  *bool `json:"enabled,omitempty"`
	MinChars int   `json:"min_chars,omitempty"`
}

type ThinkingInjectionConfig struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}

type VercelConfig struct {
	Token     string `json:"token,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	TeamID    string `json:"team_id,omitempty"`
}

func NormalizeVercelConfig(v VercelConfig) VercelConfig {
	return VercelConfig{
		Token:     strings.TrimSpace(v.Token),
		ProjectID: strings.TrimSpace(v.ProjectID),
		TeamID:    strings.TrimSpace(v.TeamID),
	}
}

func (c *Config) ClearVercelCredentials() {
	if c == nil {
		return
	}
	c.Vercel = VercelConfig{}
}
