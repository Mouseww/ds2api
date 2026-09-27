package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"ds2api/internal/account"
	"ds2api/internal/config"
)

// newEvictTestResolver builds a two-account store+pool+resolver. Both
// accounts get a stored token so nothing here triggers a login unless a test
// explicitly refreshes.
func newEvictTestResolver(t *testing.T, login LoginFunc) (*Resolver, *config.Store, *account.Pool) {
	t.Helper()
	t.Setenv("DS2API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd1"},
			{"email":"acc2@example.com","password":"pwd2"}
		]
	}`)
	store := config.LoadStore()
	// Env-sourced configs have account tokens cleared on load; put them back so
	// the accounts are poolable without logging in.
	if err := store.Update(func(c *config.Config) error {
		for i := range c.Accounts {
			c.Accounts[i].Token = "stored-token-" + c.Accounts[i].Email
		}
		return nil
	}); err != nil {
		t.Fatalf("seeding stored tokens failed: %v", err)
	}
	pool := account.NewPool(store)
	return NewResolver(store, pool, login), store, pool
}

func poolMembers(t *testing.T, pool *account.Pool) map[string]bool {
	t.Helper()
	status := pool.Status()
	members := map[string]bool{}
	for _, id := range status["active_pool_accounts"].([]string) {
		members[id] = true
	}
	return members
}

// TestRefreshTokenFailureEvictsAccountFromPool guards eviction rule 1: a
// failed token refresh must auto-disable the account with reason
// "refresh_failed" and drop it from the pool, while preserving the stored
// credential and leaving other accounts untouched.
func TestRefreshTokenFailureEvictsAccountFromPool(t *testing.T) {
	resolver, store, pool := newEvictTestResolver(t, func(_ context.Context, _ config.Account) (string, error) {
		return "", errors.New("login upstream unavailable")
	})
	a := &RequestAuth{UseConfigToken: true, AccountID: "acc1@example.com", resolver: resolver}

	if resolver.RefreshToken(context.Background(), a) {
		t.Fatal("expected refresh to fail when login fails")
	}

	acc, ok := store.FindAccount("acc1@example.com")
	if !ok {
		t.Fatal("expected account to exist")
	}
	if acc.IsEnabled() {
		t.Fatal("expected account to be disabled after a failed refresh")
	}
	if acc.DisabledReason != "refresh_failed" {
		t.Fatalf("expected disabled_reason refresh_failed, got %q", acc.DisabledReason)
	}
	if acc.Token != "stored-token-acc1@example.com" {
		t.Fatalf("expected stored token to survive eviction, got %q", acc.Token)
	}
	if poolMembers(t, pool)["acc1@example.com"] {
		t.Fatal("expected evicted account to be removed from the pool")
	}
	if !poolMembers(t, pool)["acc2@example.com"] {
		t.Fatal("expected the other account to stay in the pool")
	}
}

// TestRefreshTokenContextCancelDoesNotEvict guards the cancellation exemption
// of eviction rule 1: a cancelled caller of a singleflight login must not
// evict the account, because the leader's login may still succeed.
func TestRefreshTokenContextCancelDoesNotEvict(t *testing.T) {
	resolver, store, pool := newEvictTestResolver(t, func(ctx context.Context, _ config.Account) (string, error) {
		return "", ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &RequestAuth{UseConfigToken: true, AccountID: "acc1@example.com", resolver: resolver}

	if resolver.RefreshToken(ctx, a) {
		t.Fatal("expected refresh to fail on a cancelled context")
	}

	acc, ok := store.FindAccount("acc1@example.com")
	if !ok {
		t.Fatal("expected account to exist")
	}
	if !acc.IsEnabled() {
		t.Fatal("expected cancelled refresh not to evict the account")
	}
	if acc.DisabledReason != "" {
		t.Fatalf("expected no disabled_reason, got %q", acc.DisabledReason)
	}
	if !poolMembers(t, pool)["acc1@example.com"] {
		t.Fatal("expected account to stay in the pool")
	}
}

// TestRefreshTokenThrottleDoesNotEvict guards the throttle exemption of
// eviction rule 1: a login rejected by the local per-egress rate gate
// (ErrLoginThrottled) is transient and local — the account is healthy — so
// it must not evict the account or drop it from the pool.
func TestRefreshTokenThrottleDoesNotEvict(t *testing.T) {
	resolver, store, pool := newEvictTestResolver(t, func(_ context.Context, _ config.Account) (string, error) {
		return "", fmt.Errorf("egress proxy-1 in risk-control cooldown, retry in 9m59s: %w", ErrLoginThrottled)
	})
	a := &RequestAuth{UseConfigToken: true, AccountID: "acc1@example.com", resolver: resolver}

	if resolver.RefreshToken(context.Background(), a) {
		t.Fatal("expected refresh to fail while throttled")
	}

	acc, ok := store.FindAccount("acc1@example.com")
	if !ok {
		t.Fatal("expected account to exist")
	}
	if !acc.IsEnabled() {
		t.Fatal("expected throttled refresh not to evict the account")
	}
	if acc.DisabledReason != "" {
		t.Fatalf("expected no disabled_reason, got %q", acc.DisabledReason)
	}
	if !poolMembers(t, pool)["acc1@example.com"] {
		t.Fatal("expected account to stay in the pool")
	}
}

// TestRefreshTokenBanKeepsBannedReason guards reason priority: a login that
// succeeds but finds the account banned disables it through the ban path, and
// the refresh-failure eviction must not overwrite the "banned" reason.
func TestRefreshTokenBanKeepsBannedReason(t *testing.T) {
	resolver, store, _ := newEvictTestResolver(t, func(_ context.Context, _ config.Account) (string, error) {
		return "fresh-token", nil
	})
	// Seed the ban fields the real client persists during Login.
	if err := store.Update(func(c *config.Config) error {
		c.Accounts[0].BanIsMuted = 1
		return nil
	}); err != nil {
		t.Fatalf("seeding ban state failed: %v", err)
	}

	a := &RequestAuth{UseConfigToken: true, AccountID: "acc1@example.com", resolver: resolver}
	if resolver.RefreshToken(context.Background(), a) {
		t.Fatal("expected refresh to fail when the account is banned")
	}

	acc, ok := store.FindAccount("acc1@example.com")
	if !ok {
		t.Fatal("expected account to exist")
	}
	if acc.IsEnabled() {
		t.Fatal("expected banned account to be disabled")
	}
	if acc.DisabledReason != "banned" {
		t.Fatalf("expected disabled_reason banned, got %q", acc.DisabledReason)
	}
}

// TestNoteAccountErrorEvictsAtThreshold guards eviction rule 2: one upstream
// error is tolerated, the second evicts the account with reason
// "error_count", and a manual re-enable starts a fresh slate.
func TestNoteAccountErrorEvictsAtThreshold(t *testing.T) {
	resolver, store, pool := newEvictTestResolver(t, nil)

	resolver.NoteAccountError("acc1@example.com")
	acc, ok := store.FindAccount("acc1@example.com")
	if !ok {
		t.Fatal("expected account to exist")
	}
	if !acc.IsEnabled() {
		t.Fatal("expected a single error not to evict the account")
	}
	if !poolMembers(t, pool)["acc1@example.com"] {
		t.Fatal("expected account to stay in the pool after one error")
	}

	resolver.NoteAccountError("acc1@example.com")
	acc, _ = store.FindAccount("acc1@example.com")
	if acc.IsEnabled() {
		t.Fatal("expected two errors to evict the account")
	}
	if acc.DisabledReason != "error_count" {
		t.Fatalf("expected disabled_reason error_count, got %q", acc.DisabledReason)
	}
	if poolMembers(t, pool)["acc1@example.com"] {
		t.Fatal("expected evicted account to be removed from the pool")
	}

	// Manual re-enable clears the counter: one more error must not evict.
	if err := store.SetAccountEnabled("acc1@example.com", true, ""); err != nil {
		t.Fatalf("re-enabling account failed: %v", err)
	}
	pool.Rebalance()
	resolver.NoteAccountError("acc1@example.com")
	acc, _ = store.FindAccount("acc1@example.com")
	if !acc.IsEnabled() {
		t.Fatal("expected fresh error slate after manual re-enable")
	}
}

// TestNoteAccountErrorSkipsDisabledAccount guards the zombie-error exemption:
// errors observed on an already-disabled account must not count (and must not
// overwrite the original disable reason).
func TestNoteAccountErrorSkipsDisabledAccount(t *testing.T) {
	resolver, store, _ := newEvictTestResolver(t, nil)
	if err := store.SetAccountEnabled("acc1@example.com", false, "manual"); err != nil {
		t.Fatalf("disabling account failed: %v", err)
	}

	resolver.NoteAccountError("acc1@example.com")
	resolver.NoteAccountError("acc1@example.com")

	acc, ok := store.FindAccount("acc1@example.com")
	if !ok {
		t.Fatal("expected account to exist")
	}
	if acc.IsEnabled() {
		t.Fatal("expected account to stay disabled")
	}
	if acc.DisabledReason != "manual" {
		t.Fatalf("expected original reason manual to be preserved, got %q", acc.DisabledReason)
	}
}
