package auth

import (
	"context"
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
//   - stored token → validate it with the read-only TokenProbe. A single
//     rejection is tolerated (it may be a transient network error); after
//     keeperFailureThreshold consecutive rejections the token is treated
//     as stale and the account is re-logged-in.
//
// All logins go through the shared performLogin singleflight and the
// per-egress login gate, so the keeper can never stampede DeepSeek's login
// endpoint: it queues behind the same pacing as every other login source.
// Keeper login failures (bad password, captcha, upstream errors) are
// tolerated twice, then the account is paused for keeperFailurePause before
// the keeper tries again — a permanently broken account surfaces as a
// recurring warning instead of a login storm. ErrLoginThrottled and
// context cancellation are local/transient conditions and never count as
// failures. The keeper never evicts accounts on its own; eviction stays
// with the failure policy (refresh_failed / error_count) and ban detection.
//
// Operators control the cadence with DS2API_TOKEN_KEEP_INTERVAL (Go duration
// syntax, e.g. "5m", "30s"); "off", "disabled" or "0" disables the keeper.
var (
	// poolTokenKeepInterval is how often the keeper sweeps the active pool.
	// Parsed once at startup from DS2API_TOKEN_KEEP_INTERVAL; zero disables.
	poolTokenKeepInterval = envPoolTokenKeepInterval()

	// keeperFailurePause is how long the keeper stops trying an account
	// after keeperFailureThreshold consecutive failures. Tests shrink it.
	keeperFailurePause = time.Hour
)

// keeperFailureThreshold mirrors the error-count eviction rule: two
// consecutive keeper failures (probe rejections or login errors) before the
// keeper acts (relogin) or pauses (after a failed relogin).
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
		// Paused after repeated failures: wait out the pause window, then
		// start a fresh attempt cycle.
		if r.keeperFailureCount(id) >= keeperFailureThreshold {
			if time.Since(r.keeperLastFailedTime(id)) < keeperFailurePause {
				continue
			}
			r.resetKeeperFailures(id)
		}

		token := strings.TrimSpace(acc.Token)
		if token != "" && r.tokenRefreshedRecently(id) {
			continue // token obtained moments ago by traffic or the keeper
		}
		if token != "" && r.TokenProbe != nil {
			probeCtx := WithAuth(ctx, &RequestAuth{
				UseConfigToken: false,
				AccountID:      id,
				Account:        acc,
				DeepSeekToken:  token,
				TriedAccounts:  map[string]bool{},
			})
			if err := r.TokenProbe(probeCtx, token); err == nil {
				r.resetKeeperFailures(id)
				continue // stored token works
			}
			// Rejected once may be a transient network error; only treat the
			// token as stale after consecutive rejections.
			if r.bumpKeeperFailures(id) < keeperFailureThreshold {
				continue
			}
			config.Logger.Info("[pool_token_keeper] stored token rejected repeatedly, re-logging in",
				"account", id)
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
				r.bumpKeeperFailures(id)
				config.Logger.Warn("[pool_token_keeper] login failed",
					"account", id, "error", err)
			}
			continue
		}
		r.resetKeeperFailures(id)
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

// keeperFailureCount returns the account's consecutive keeper failures.
func (r *Resolver) keeperFailureCount(accountID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.keeperFailures[accountID]
}

// keeperLastFailedTime returns when the account last failed a keeper step
// (zero time if never).
func (r *Resolver) keeperLastFailedTime(accountID string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.keeperLastFailedAt[accountID]
}

// bumpKeeperFailures records one keeper failure and returns the new
// consecutive-failure count.
func (r *Resolver) bumpKeeperFailures(accountID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keeperFailures[accountID]++
	r.keeperLastFailedAt[accountID] = time.Now()
	return r.keeperFailures[accountID]
}

// resetKeeperFailures clears the failure state after any success (probe or
// login).
func (r *Resolver) resetKeeperFailures(accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keeperFailures, accountID)
	delete(r.keeperLastFailedAt, accountID)
}
