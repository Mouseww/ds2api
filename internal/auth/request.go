package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"ds2api/internal/account"
	"ds2api/internal/config"
)

type ctxKey string

const authCtxKey ctxKey = "auth_context"

var (
	ErrUnauthorized  = errors.New("unauthorized: missing auth token")
	ErrNoAccount     = errors.New("no accounts configured or all accounts are busy")
	errAccountBanned = errors.New("account is muted/banned by DeepSeek")
)

const (
	// banRecheckCooldown is the minimum interval between ban re-check logins
	// for the same account. This prevents login storms when a request is
	// repeatedly rejected.
	banRecheckCooldown = 60 * time.Second

	// banUnbanCheckInterval is how often the background monitor scans for
	// auto-disabled (banned) accounts whose mute timestamp has lapsed, so it
	// can re-login and re-enable any account whose ban was lifted.
	banUnbanCheckInterval = 60 * time.Second
)

type RequestAuth struct {
	UseConfigToken bool
	DeepSeekToken  string
	CallerID       string
	AccountID      string
	TargetAccount  string
	Account        config.Account
	TriedAccounts  map[string]bool
	resolver       *Resolver
}

type LoginFunc func(ctx context.Context, acc config.Account) (string, error)
type PostLoginFunc func(ctx context.Context, a *RequestAuth)

type Resolver struct {
	Store     *config.Store
	Pool      *account.Pool
	Login     LoginFunc
	PostLogin PostLoginFunc

	mu               sync.Mutex
	tokenRefreshedAt map[string]time.Time
	banRecheckedAt   map[string]time.Time
	loginFlights     map[string]*loginFlight
}

func NewResolver(store *config.Store, pool *account.Pool, login LoginFunc) *Resolver {
	return &Resolver{
		Store:            store,
		Pool:             pool,
		Login:            login,
		tokenRefreshedAt: map[string]time.Time{},
		banRecheckedAt:   map[string]time.Time{},
		loginFlights:     map[string]*loginFlight{},
	}
}

func (r *Resolver) Determine(req *http.Request) (*RequestAuth, error) {
	callerKey := extractCallerToken(req)
	if callerKey == "" {
		return nil, ErrUnauthorized
	}
	callerID := callerTokenID(callerKey)
	ctx := req.Context()
	if !r.Store.HasAPIKey(callerKey) {
		return &RequestAuth{
			UseConfigToken: false,
			DeepSeekToken:  callerKey,
			CallerID:       callerID,
			resolver:       r,
			TriedAccounts:  map[string]bool{},
		}, nil
	}
	target := strings.TrimSpace(req.Header.Get("X-Ds2-Target-Account"))
	a, err := r.acquireManagedRequestAuth(ctx, callerID, target)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (r *Resolver) acquireManagedRequestAuth(ctx context.Context, callerID, target string) (*RequestAuth, error) {
	tried := map[string]bool{}
	var lastEnsureErr error
	for {
		if target == "" && len(tried) >= len(r.Store.Accounts()) {
			if lastEnsureErr != nil {
				return nil, lastEnsureErr
			}
			return nil, ErrNoAccount
		}
		acc, ok := r.Pool.AcquireWait(ctx, target, tried)
		if !ok {
			if lastEnsureErr != nil {
				return nil, lastEnsureErr
			}
			return nil, ErrNoAccount
		}

		a := &RequestAuth{
			UseConfigToken: true,
			CallerID:       callerID,
			AccountID:      acc.Identifier(),
			TargetAccount:  target,
			Account:        acc,
			TriedAccounts:  tried,
			resolver:       r,
		}

		if err := r.ensureManagedToken(ctx, a); err != nil {
			lastEnsureErr = err
			tried[a.AccountID] = true
			r.Pool.Release(a.AccountID)
			if target != "" {
				return nil, err
			}
			continue
		}
		return a, nil
	}
}

// DetermineCaller resolves caller identity without acquiring any pooled account.
// Use this for local-cache lookup routes that only need tenant isolation.
func (r *Resolver) DetermineCaller(req *http.Request) (*RequestAuth, error) {
	callerKey := extractCallerToken(req)
	if callerKey == "" {
		return nil, ErrUnauthorized
	}
	callerID := callerTokenID(callerKey)
	a := &RequestAuth{
		UseConfigToken: false,
		CallerID:       callerID,
		resolver:       r,
		TriedAccounts:  map[string]bool{},
	}
	if r == nil || r.Store == nil || !r.Store.HasAPIKey(callerKey) {
		a.DeepSeekToken = callerKey
	}
	return a, nil
}

func WithAuth(ctx context.Context, a *RequestAuth) context.Context {
	return context.WithValue(ctx, authCtxKey, a)
}

func FromContext(ctx context.Context) (*RequestAuth, bool) {
	v := ctx.Value(authCtxKey)
	a, ok := v.(*RequestAuth)
	return a, ok
}

// loginFlight is one in-progress login for a single account. Concurrent
// callers park on done and apply the shared result instead of each hitting the
// DeepSeek login endpoint: concurrent 401 refreshes, ban re-checks, and the
// unban monitor would otherwise stampede logins for the same account.
type loginFlight struct {
	done chan struct{}
	res  loginFlightResult
}

type loginFlightResult struct {
	token  string
	banned bool
	err    error
}

// performLogin runs one login for accountID, deduplicating concurrent
// callers. The leader executes the login (and its persist / ban-reconcile /
// post-login side effects) exactly once; everyone else waits for the same
// result. A caller whose context is cancelled while waiting returns early
// without applying the result.
func (r *Resolver) performLogin(ctx context.Context, accountID string, acc config.Account) loginFlightResult {
	if strings.TrimSpace(accountID) == "" {
		// Degenerate caller: no account identity to dedupe on.
		return r.executeLogin(ctx, accountID, acc)
	}
	r.mu.Lock()
	if f, ok := r.loginFlights[accountID]; ok {
		r.mu.Unlock()
		select {
		case <-f.done:
			return f.res
		case <-ctx.Done():
			return loginFlightResult{err: ctx.Err()}
		}
	}
	f := &loginFlight{done: make(chan struct{})}
	r.loginFlights[accountID] = f
	r.mu.Unlock()

	f.res = r.executeLogin(ctx, accountID, acc)
	close(f.done)

	r.mu.Lock()
	delete(r.loginFlights, accountID)
	r.mu.Unlock()
	return f.res
}

// executeLogin performs the actual login and its once-per-login side
// effects: token persistence, ban reconciliation, and the PostLogin hook.
func (r *Resolver) executeLogin(ctx context.Context, accountID string, acc config.Account) loginFlightResult {
	token, err := r.Login(ctx, acc)
	if err != nil {
		return loginFlightResult{err: err}
	}
	r.markTokenRefreshedNow(accountID)
	// The client persisted fresh ban fields during Login. Reconcile ban state:
	// disable if newly banned, re-enable if ban has been lifted.
	if stored, ok := r.Store.FindAccount(accountID); ok {
		if stored.IsBanned() {
			r.disableBannedAccount(accountID, stored)
			return loginFlightResult{banned: true, err: errAccountBanned}
		}
		r.reenableIfUnbanned(accountID, stored)
	}
	if err := r.Store.UpdateAccountToken(accountID, token); err != nil {
		return loginFlightResult{err: err}
	}
	if r.PostLogin != nil {
		r.PostLogin(ctx, &RequestAuth{
			UseConfigToken: true,
			AccountID:      accountID,
			Account:        acc,
			DeepSeekToken:  token,
			TriedAccounts:  map[string]bool{},
			resolver:       r,
		})
	}
	return loginFlightResult{token: token}
}

func (r *Resolver) loginAndPersist(ctx context.Context, a *RequestAuth) error {
	res := r.performLogin(ctx, a.AccountID, a.Account)
	if res.banned {
		a.Account.Token = ""
		a.DeepSeekToken = ""
		return res.err
	}
	if res.err != nil {
		return res.err
	}
	a.Account.Token = res.token
	a.DeepSeekToken = res.token
	return nil
}

// RecheckBan re-logs-in to pull the latest ban status after a rejected request
// (captcha / 429 / auth failure). It returns whether the account was found
// banned (and thus auto-disabled) and whether a fresh token was obtained.
// Re-checks are rate-limited by banRecheckCooldown.
func (r *Resolver) RecheckBan(ctx context.Context, a *RequestAuth) (banned bool, refreshed bool) {
	if r == nil || a == nil || !a.UseConfigToken || a.AccountID == "" {
		return false, false
	}
	if !r.beginBanRecheck(a.AccountID) {
		return false, false
	}
	if err := r.loginAndPersist(ctx, a); err != nil {
		if errors.Is(err, errAccountBanned) {
			return true, false
		}
		config.Logger.Warn("[ban_recheck] login failed", "account", a.AccountID, "error", err)
		return false, false
	}
	return false, true
}

// disableBannedAccount marks the account disabled with reason "banned" and
// evicts it from the pool. The persisted token is intentionally preserved
// (per spec R3.4) so re-enabling the account later does not require a fresh
// credential; only the in-memory token is cleared by the caller.
func (r *Resolver) disableBannedAccount(accountID string, acc config.Account) {
	if r.Store != nil {
		_ = r.Store.Update(func(c *config.Config) error {
			for i := range c.Accounts {
				if c.Accounts[i].Identifier() != accountID {
					continue
				}
				disabled := false
				c.Accounts[i].Enabled = &disabled
				c.Accounts[i].DisabledReason = "banned"
				return nil
			}
			return nil
		})
	}
	if r.Pool != nil {
		r.Pool.RemoveAccount(accountID)
	}
	config.Logger.Warn(
		"[ban_detect] account banned and auto-disabled",
		"account", accountID,
		"ban_is_muted", acc.BanIsMuted,
		"ban_mute_until", acc.BanMuteUntil,
		"ban_status", acc.BanStatus,
	)
}

// reenableIfUnbanned restores an account that was auto-disabled with reason
// "banned" once a fresh login shows the mute is no longer active. Manual
// disables and already-enabled accounts are left untouched.
func (r *Resolver) reenableIfUnbanned(accountID string, acc config.Account) {
	if acc.IsEnabled() || acc.DisabledReason != "banned" {
		return
	}
	if r.Store != nil {
		_ = r.Store.Update(func(c *config.Config) error {
			for i := range c.Accounts {
				if c.Accounts[i].Identifier() != accountID {
					continue
				}
				enabled := true
				c.Accounts[i].Enabled = &enabled
				c.Accounts[i].DisabledReason = ""
				return nil
			}
			return nil
		})
	}
	if r.Pool != nil {
		r.Pool.Rebalance()
	}
	config.Logger.Info(
		"[ban_detect] account unbanned and re-enabled",
		"account", accountID,
		"ban_is_muted", acc.BanIsMuted,
		"ban_mute_until", acc.BanMuteUntil,
		"ban_status", acc.BanStatus,
	)
}

// StartUnbanMonitor launches a background loop that periodically re-logs-in
// for auto-disabled (banned) accounts whose mute timestamp has expired. When a
// login confirms the ban is lifted the account is re-enabled and returned to
// the rotation pool. The loop runs on banUnbanCheckInterval until ctx is done.
func (r *Resolver) StartUnbanMonitor(ctx context.Context) {
	if r == nil || r.Store == nil || r.Login == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(banUnbanCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.checkExpiredBans(ctx)
			}
		}
	}()
}

// checkExpiredBans scans every account that was auto-disabled with reason
// "banned" and whose mute timestamp has lapsed. Each eligible account is
// logged-in to refresh ban state; if the mute is lifted the account is
// re-enabled automatically.
func (r *Resolver) checkExpiredBans(ctx context.Context) {
	if r == nil || r.Store == nil || r.Login == nil {
		return
	}
	nowUnix := float64(time.Now().Unix())
	for _, acc := range r.Store.Accounts() {
		if acc.IsEnabled() || acc.DisabledReason != "banned" {
			continue
		}
		if !acc.IsBanned() {
			// Stale state: disabled for ban but not actually banned.
			// Restore defensively.
			r.reenableIfUnbanned(acc.Identifier(), acc)
			continue
		}
		if acc.BanMuteUntil <= 0 || nowUnix < acc.BanMuteUntil {
			continue // no known expiry, or not yet expired
		}
		// Mute has expired: re-login to refresh ban status. loginAndPersist
		// will call reenableIfUnbanned if the ban was lifted.
		a := &RequestAuth{
			UseConfigToken: true,
			AccountID:      acc.Identifier(),
			Account:        acc,
			TriedAccounts:  map[string]bool{},
			resolver:       r,
		}
		if err := r.loginAndPersist(ctx, a); err != nil {
			if errors.Is(err, errAccountBanned) {
				config.Logger.Debug("[ban_unban_monitor] account still banned after mute expiry", "account", acc.Identifier())
			} else {
				config.Logger.Warn("[ban_unban_monitor] re-check login failed", "account", acc.Identifier(), "error", err)
			}
		}
	}
}

func (r *Resolver) beginBanRecheck(accountID string) bool {
	if strings.TrimSpace(accountID) == "" {
		return false
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	last, ok := r.banRecheckedAt[accountID]
	if ok && now.Sub(last) < banRecheckCooldown {
		return false
	}
	r.banRecheckedAt[accountID] = now
	return true
}

func (r *Resolver) RefreshToken(ctx context.Context, a *RequestAuth) bool {
	if !a.UseConfigToken || a.AccountID == "" {
		return false
	}
	// The stored token is intentionally NOT blanked first: loginAndPersist
	// overwrites it on success, and a failed login must leave the previous
	// credential in place instead of forcing every later request into its own
	// login attempt. Concurrent refreshes are deduped by the login flight.
	if err := r.loginAndPersist(ctx, a); err != nil {
		config.Logger.Error("[refresh_token] failed", "account", a.AccountID, "error", err)
		return false
	}
	return true
}

func (r *Resolver) MarkTokenInvalid(a *RequestAuth) {
	if !a.UseConfigToken || a.AccountID == "" {
		return
	}
	a.Account.Token = ""
	a.DeepSeekToken = ""
	r.clearTokenRefreshMark(a.AccountID)
	_ = r.Store.UpdateAccountToken(a.AccountID, "")
}

func (r *Resolver) SwitchAccount(ctx context.Context, a *RequestAuth) bool {
	if !a.UseConfigToken {
		return false
	}
	if strings.TrimSpace(a.TargetAccount) != "" {
		return false
	}
	if a.TriedAccounts == nil {
		a.TriedAccounts = map[string]bool{}
	}
	if a.AccountID != "" {
		a.TriedAccounts[a.AccountID] = true
		r.Pool.Release(a.AccountID)
	}
	for {
		acc, ok := r.Pool.Acquire("", a.TriedAccounts)
		if !ok {
			return false
		}
		a.Account = acc
		a.AccountID = acc.Identifier()
		if err := r.ensureManagedToken(ctx, a); err != nil {
			a.TriedAccounts[a.AccountID] = true
			r.Pool.Release(a.AccountID)
			continue
		}
		return true
	}
}

func (a *RequestAuth) SwitchAccount(ctx context.Context) bool {
	if a == nil || a.resolver == nil {
		return false
	}
	return a.resolver.SwitchAccount(ctx, a)
}

func (r *Resolver) Release(a *RequestAuth) {
	if a == nil || !a.UseConfigToken || a.AccountID == "" {
		return
	}
	r.Pool.Release(a.AccountID)
}

func extractCallerToken(req *http.Request) string {
	authHeader := strings.TrimSpace(req.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		token := strings.TrimSpace(authHeader[7:])
		if token != "" {
			return token
		}
	}
	if key := strings.TrimSpace(req.Header.Get("x-api-key")); key != "" {
		return key
	}
	// Gemini/Google clients commonly send API key via x-goog-api-key.
	if key := strings.TrimSpace(req.Header.Get("x-goog-api-key")); key != "" {
		return key
	}
	// Gemini AI Studio compatibility: allow query key fallback only when no
	// header-based credential is present.
	if key := strings.TrimSpace(req.URL.Query().Get("key")); key != "" {
		return key
	}
	return strings.TrimSpace(req.URL.Query().Get("api_key"))
}

func callerTokenID(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return "caller:" + hex.EncodeToString(sum[:8])
}

func (r *Resolver) ensureManagedToken(ctx context.Context, a *RequestAuth) error {
	if strings.TrimSpace(a.Account.Token) == "" {
		return r.loginAndPersist(ctx, a)
	}
	if r.shouldForceRefresh(a.AccountID) {
		if err := r.loginAndPersist(ctx, a); err != nil {
			return err
		}
		return nil
	}
	a.DeepSeekToken = a.Account.Token
	return nil
}

func (r *Resolver) shouldForceRefresh(accountID string) bool {
	if r == nil || r.Store == nil {
		return false
	}
	if strings.TrimSpace(accountID) == "" {
		return false
	}
	intervalHours := r.Store.RuntimeTokenRefreshIntervalHours()
	if intervalHours <= 0 {
		return false
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	last, ok := r.tokenRefreshedAt[accountID]
	if !ok || last.IsZero() {
		r.tokenRefreshedAt[accountID] = now
		return false
	}
	return now.Sub(last) >= time.Duration(intervalHours)*time.Hour
}

func (r *Resolver) markTokenRefreshedNow(accountID string) {
	if strings.TrimSpace(accountID) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokenRefreshedAt[accountID] = time.Now()
}

func (r *Resolver) clearTokenRefreshMark(accountID string) {
	if strings.TrimSpace(accountID) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tokenRefreshedAt, accountID)
}
