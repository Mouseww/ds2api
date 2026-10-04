package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"ds2api/internal/config"
)

// The pool token keeper is a background monitor that guarantees every
// account in the ACTIVE pool carries a usable DeepSeek token, so the first
// request routed to an account never has to pay a full (gate-paced) login
// and cannot fail for lack of credentials.
//
// Every poolTokenKeepInterval it walks the active queue and, per account:
//
//   - no stored token → log in once and persist it (provisioning);
//   - stored token, refreshed recently (by traffic or the keeper itself) →
//     skip: it is fresh by construction;
//   - stored token whose JWT exp claim has already passed (or passes
//     within one sweep interval) → refresh it right away, without probing:
//     the token itself declares it stale, so a probe would only burn a
//     round trip — under the default cadence a pooled token never expires
//     in service;
//   - other stored token → validate it with the read-only TokenProbe. A
//     single rejection is tolerated (it may be a transient network error);
//     after keeperFailureThreshold consecutive rejections the token is
//     treated as stale and the account is re-logged-in.
//
// All logins go through the shared performLogin singleflight and the
// per-egress login gate, so the keeper can never stampede DeepSeek's login
// endpoint: it queues behind the same pacing as every other login source.
// A token refresh that fails keeperFailureThreshold times in a row (bad
// password, deleted account, persistent upstream rejection) evicts the
// account from the active pool with reason "refresh_failed": the account
// is auto-disabled, a standby account is promoted to take over its
// traffic, and it returns only via manual re-enable. ErrLoginThrottled
// and context cancellation are local/transient conditions and never count
// as failures.
//
// Operators control the cadence with DS2API_TOKEN_KEEP_INTERVAL (Go duration
// syntax, e.g. "5m", "30s"); "off", "disabled" or "0" disables the keeper.
var (
	// poolTokenKeepInterval is how often the keeper sweeps the active pool.
	// Parsed once at startup from DS2API_TOKEN_KEEP_INTERVAL; zero disables.
	poolTokenKeepInterval = envPoolTokenKeepInterval()
)

// keeperFailureThreshold is the two-strike rule shared by both keeper
// failure kinds: two consecutive probe rejections declare a stored token
// stale (triggering a relogin), and two consecutive refresh (login)
// failures evict the account from the active pool (reason
// "refresh_failed").
const keeperFailureThreshold = 2

// defaultPoolTokenKeepInterval is the default sweep cadence.
const defaultPoolTokenKeepInterval = 5 * time.Minute

// envPoolTokenKeepInterval parses DS2API_TOKEN_KEEP_INTERVAL, falling back
// to the default (with a warning) when unset or malformed. The values
// "off", "disabled" and "0" (and "0s") disable the keeper entirely.
func envPoolTokenKeepInterval() time.Duration {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("DS2API_TOKEN_KEEP_INTERVAL")))
	if raw == "" {
		return defaultPoolTokenKeepInterval
	}
	if raw == "off" || raw == "disabled" || raw == "0" || raw == "0s" {
		config.Logger.Info("[pool_token_keeper] disabled via DS2API_TOKEN_KEEP_INTERVAL")
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		config.Logger.Warn("[pool_token_keeper] ignoring invalid env value, using default",
			"env", "DS2API_TOKEN_KEEP_INTERVAL", "value", raw,
			"default", defaultPoolTokenKeepInterval.String())
		return defaultPoolTokenKeepInterval
	}
	return d
}

// StartPoolTokenKeeper launches the background sweep. It runs one pass
// immediately (a freshly started server provisions its active pool without
// waiting a full interval) and then repeats every poolTokenKeepInterval.
// Nil-safe and a no-op when the keeper is disabled via env.
func (r *Resolver) StartPoolTokenKeeper(ctx context.Context) {
	if r == nil || r.Store == nil || r.Pool == nil || r.Login == nil {
		return
	}
	if poolTokenKeepInterval <= 0 {
		return
	}
	go func() {
		r.ensurePoolTokens(ctx)
		ticker := time.NewTicker(poolTokenKeepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.ensurePoolTokens(ctx)
			}
		}
	}()
}

// tokenExpiry decodes the expiry time embedded in a DeepSeek session token.
// DeepSeek tokens are JWTs: the middle base64url segment carries a JSON
// payload whose "exp" claim (unix seconds) states when the token lapses.
// The signature is not verified — only the expiry timestamp is needed, and
// the token's authenticity was established by the login that produced it.
// Tokens that are not decodable JWTs, or carry no positive numeric exp,
// return ok=false and fall back to probe-based staleness detection.
func tokenExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, false
	}
	if claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(claims.Exp), 0), true
}

// tokenExpiringSoon reports whether the token's JWT exp claim has already
// passed or passes within margin. Tokens without a decodable exp claim are
// never expiring soon — their staleness is detected by the TokenProbe.
func tokenExpiringSoon(token string, margin time.Duration) bool {
	exp, ok := tokenExpiry(token)
	if !ok {
		return false
	}
	return !exp.After(time.Now().Add(margin))
}

// ensurePoolTokens walks the active pool once and makes sure every account
// in it has a usable token. Sequential by design: logins are paced by the
// per-egress gate anyway, and a quiet background sweep never competes with
// itself.
func (r *Resolver) ensurePoolTokens(ctx context.Context) {
	for _, id := range r.Pool.ActiveAccounts() {
		if ctx.Err() != nil {
			return
		}
		acc, ok := r.Store.FindAccount(id)
		if !ok || !acc.IsEnabled() || acc.IsBanned() {
			continue
		}

		token := strings.TrimSpace(acc.Token)
		if token != "" && r.tokenRefreshedRecently(id) {
			continue // token obtained moments ago by traffic or the keeper
		}

		// Timely refresh: a token whose exp has passed (or passes within
		// one sweep interval) is refreshed without probing — the token
		// itself already declares it stale, so probing it would only burn
		// a round trip.
		needsRefresh := token == "" || tokenExpiringSoon(token, poolTokenKeepInterval)

		if !needsRefresh && r.TokenProbe != nil {
			probeCtx := WithAuth(ctx, &RequestAuth{
				UseConfigToken: false,
				AccountID:      id,
				Account:        acc,
				DeepSeekToken:  token,
				TriedAccounts:  map[string]bool{},
			})
			if err := r.TokenProbe(probeCtx, token); err == nil {
				// A passing probe also proves the account itself healthy,
				// so an earlier refresh failure was transient: reset both
				// failure streaks.
				r.resetKeeperFailures(id)
				r.resetProbeFailures(id)
				continue // stored token works
			}
			// Rejected once may be a transient network error; only treat
			// the token as stale after consecutive rejections.
			if r.bumpProbeFailures(id) < keeperFailureThreshold {
				continue
			}
			config.Logger.Info("[pool_token_keeper] stored token rejected repeatedly, re-logging in",
				"account", id)
			needsRefresh = true
		}

		if !needsRefresh {
			continue
		}

		if err := r.loginAndPersist(ctx, &RequestAuth{
			UseConfigToken: true,
			AccountID:      id,
			Account:        acc,
			TriedAccounts:  map[string]bool{},
			resolver:       r,
		}); err != nil {
			switch {
			case errors.Is(err, ErrLoginThrottled):
				// Local gate pacing or risk cooldown: retry next sweep
				// without penalty.
				config.Logger.Debug("[pool_token_keeper] login throttled, will retry",
					"account", id)
			case errors.Is(err, errAccountBanned):
				// executeLogin already disabled and evicted the account; it
				// leaves the active queue on the next rebuild.
				config.Logger.Debug("[pool_token_keeper] account banned during relogin",
					"account", id)
			case errors.Is(err, context.Canceled) || ctx.Err() != nil:
				// Shutdown: stop the sweep without penalty.
				return
			default:
				// Two consecutive refresh failures mean the account can no
				// longer authenticate itself (bad password, deleted
				// account, persistent upstream rejection): evict it from
				// the active pool so a standby account takes over its
				// traffic. The account is auto-disabled with reason
				// "refresh_failed" and returns only via manual re-enable.
				if r.bumpKeeperFailures(id) >= keeperFailureThreshold {
					config.Logger.Warn("[pool_token_keeper] token refresh failed twice consecutively, evicting account",
						"account", id)
					r.evictAccountFromPool(id, "refresh_failed")
					r.resetKeeperFailures(id)
					r.resetProbeFailures(id)
				} else {
					config.Logger.Warn("[pool_token_keeper] login failed",
						"account", id, "error", err)
				}
			}
			continue
		}
		r.resetKeeperFailures(id)
		r.resetProbeFailures(id)
		config.Logger.Info("[pool_token_keeper] token ready",
			"account", id, "had_token", token != "")
	}
}

// tokenRefreshedRecently reports whether the account's token was refreshed
// within the current keep interval (by traffic-driven login, the unban
// monitor, or the keeper itself). Such a token is fresh by construction and
// is not probed again this sweep.
func (r *Resolver) tokenRefreshedRecently(accountID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	last, ok := r.tokenRefreshedAt[accountID]
	return ok && !last.IsZero() && time.Since(last) < poolTokenKeepInterval
}

// keeperFailureCount returns the account's consecutive refresh (login)
// failures.
func (r *Resolver) keeperFailureCount(accountID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.keeperFailures[accountID]
}

// bumpKeeperFailures records one refresh (login) failure and returns the
// new consecutive-failure count.
func (r *Resolver) bumpKeeperFailures(accountID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keeperFailures[accountID]++
	return r.keeperFailures[accountID]
}

// resetKeeperFailures clears the refresh-failure streak after a successful
// login (or a passing probe).
func (r *Resolver) resetKeeperFailures(accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keeperFailures, accountID)
}

// bumpProbeFailures records one probe rejection and returns the new
// consecutive-rejection count.
func (r *Resolver) bumpProbeFailures(accountID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keeperProbeFailures[accountID]++
	return r.keeperProbeFailures[accountID]
}

// resetProbeFailures clears the probe-rejection streak after a passing
// probe or a successful relogin.
func (r *Resolver) resetProbeFailures(accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keeperProbeFailures, accountID)
}
