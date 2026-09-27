package auth

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ds2api/internal/account"
	"ds2api/internal/config"
)

// newKeeperEnv builds a resolver over an env-backed store whose account
// tokens are stripped at load (see config store loadConfig), so tests seed
// tokens explicitly via UpdateAccountToken.
func newKeeperEnv(t *testing.T, accountsJSON string) (*Resolver, *config.Store) {
	t.Helper()
	t.Setenv("DS2API_CONFIG_JSON", `{"keys":["managed-key"],"accounts":`+accountsJSON+`}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	return NewResolver(store, pool, func(_ context.Context, _ config.Account) (string, error) {
		return "fresh-token", nil
	}), store
}

func TestEnsurePoolTokensProvisionsMissingToken(t *testing.T) {
	r, store := newKeeperEnv(t, `[{"email":"a@example.com","password":"pwd"}]`)
	var loginCalls atomic.Int32
	r.Login = func(_ context.Context, _ config.Account) (string, error) {
		loginCalls.Add(1)
		return "fresh-token", nil
	}

	r.ensurePoolTokens(context.Background())

	if got := loginCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 login, got %d", got)
	}
	acc, ok := store.FindAccount("a@example.com")
	if !ok {
		t.Fatal("account missing from store")
	}
	if acc.Token != "fresh-token" {
		t.Fatalf("expected persisted token, got %q", acc.Token)
	}
}

func TestEnsurePoolTokensSkipsFreshlyRefreshedToken(t *testing.T) {
	r, store := newKeeperEnv(t, `[{"email":"a@example.com","password":"pwd"}]`)
	if err := store.UpdateAccountToken("a@example.com", "seeded-token"); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	r.markTokenRefreshedNow("a@example.com")
	var loginCalls, probeCalls atomic.Int32
	r.Login = func(_ context.Context, _ config.Account) (string, error) {
		loginCalls.Add(1)
		return "fresh-token", nil
	}
	r.TokenProbe = func(_ context.Context, _ string) error {
		probeCalls.Add(1)
		return nil
	}

	r.ensurePoolTokens(context.Background())

	if got := loginCalls.Load(); got != 0 {
		t.Fatalf("expected no login for fresh token, got %d", got)
	}
	if got := probeCalls.Load(); got != 0 {
		t.Fatalf("expected no probe for fresh token, got %d", got)
	}
}

func TestEnsurePoolTokensProbeOKSkipsLogin(t *testing.T) {
	r, store := newKeeperEnv(t, `[{"email":"a@example.com","password":"pwd"}]`)
	if err := store.UpdateAccountToken("a@example.com", "seeded-token"); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	var loginCalls, probeCalls atomic.Int32
	r.Login = func(_ context.Context, _ config.Account) (string, error) {
		loginCalls.Add(1)
		return "fresh-token", nil
	}
	r.TokenProbe = func(_ context.Context, token string) error {
		probeCalls.Add(1)
		if token != "seeded-token" {
			t.Fatalf("probe saw unexpected token %q", token)
		}
		return nil
	}

	r.ensurePoolTokens(context.Background())

	if got := probeCalls.Load(); got != 1 {
		t.Fatalf("expected 1 probe, got %d", got)
	}
	if got := loginCalls.Load(); got != 0 {
		t.Fatalf("expected no login when probe passes, got %d", got)
	}
}

func TestEnsurePoolTokensReloginsAfterRepeatedProbeRejection(t *testing.T) {
	r, store := newKeeperEnv(t, `[{"email":"a@example.com","password":"pwd"}]`)
	if err := store.UpdateAccountToken("a@example.com", "stale-token"); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	var loginCalls atomic.Int32
	r.Login = func(_ context.Context, _ config.Account) (string, error) {
		loginCalls.Add(1)
		return "fresh-token", nil
	}
	r.TokenProbe = func(_ context.Context, _ string) error {
		return errors.New("request failed: status=401")
	}

	// First rejection is tolerated (may be a transient network error).
	r.ensurePoolTokens(context.Background())
	if got := loginCalls.Load(); got != 0 {
		t.Fatalf("expected no login after first probe rejection, got %d", got)
	}
	// Second consecutive rejection treats the token as stale → relogin.
	r.ensurePoolTokens(context.Background())
	if got := loginCalls.Load(); got != 1 {
		t.Fatalf("expected relogin after repeated rejection, got %d", got)
	}
	acc, ok := store.FindAccount("a@example.com")
	if !ok || acc.Token != "fresh-token" {
		t.Fatalf("expected relogin token persisted, got %q", acc.Token)
	}
	// The relogin marked the token fresh: no further probe or login.
	r.ensurePoolTokens(context.Background())
	if got := loginCalls.Load(); got != 1 {
		t.Fatalf("expected no extra login after successful relogin, got %d", got)
	}
}

func TestEnsurePoolTokensThrottleIsNotAFailure(t *testing.T) {
	r, _ := newKeeperEnv(t, `[{"email":"a@example.com","password":"pwd"}]`)
	var loginCalls atomic.Int32
	r.Login = func(_ context.Context, _ config.Account) (string, error) {
		loginCalls.Add(1)
		return "", ErrLoginThrottled
	}

	// Throttled logins retry every sweep and never pause the account.
	for i := 0; i < keeperFailureThreshold+2; i++ {
		r.ensurePoolTokens(context.Background())
	}
	if got := loginCalls.Load(); got != int32(keeperFailureThreshold+2) {
		t.Fatalf("expected a login attempt every sweep while throttled, got %d", got)
	}
	if got := r.keeperFailureCount("a@example.com"); got != 0 {
		t.Fatalf("throttling must not count as failure, counter=%d", got)
	}
}

func TestEnsurePoolTokensPausesAfterRepeatedLoginFailures(t *testing.T) {
	r, _ := newKeeperEnv(t, `[{"email":"a@example.com","password":"pwd"}]`)
	var loginCalls atomic.Int32
	r.Login = func(_ context.Context, _ config.Account) (string, error) {
		loginCalls.Add(1)
		return "", errors.New("login failed: bad password")
	}

	// Two attempts, then the account is paused.
	r.ensurePoolTokens(context.Background())
	r.ensurePoolTokens(context.Background())
	r.ensurePoolTokens(context.Background())
	if got := loginCalls.Load(); got != int32(keeperFailureThreshold) {
		t.Fatalf("expected %d attempts before pause, got %d", keeperFailureThreshold, got)
	}

	// After the pause window elapses the keeper tries again.
	r.mu.Lock()
	r.keeperLastFailedAt["a@example.com"] = time.Now().Add(-keeperFailurePause - time.Minute)
	r.mu.Unlock()
	r.ensurePoolTokens(context.Background())
	if got := loginCalls.Load(); got != int32(keeperFailureThreshold+1) {
		t.Fatalf("expected retry after pause window, got %d", got)
	}
}

func TestEnsurePoolTokensSkipsDisabledAndBanned(t *testing.T) {
	r, _ := newKeeperEnv(t, `[
		{"email":"ok@example.com","password":"pwd"},
		{"email":"off@example.com","password":"pwd","enabled":false},
		{"email":"banned@example.com","password":"pwd","ban_is_muted":1}
	]`)
	var loginCalls atomic.Int32
	r.Login = func(_ context.Context, acc config.Account) (string, error) {
		loginCalls.Add(1)
		if !strings.HasPrefix(acc.Email, "ok@") {
			t.Errorf("login called for ineligible account %q", acc.Email)
		}
		return "fresh-token", nil
	}

	r.ensurePoolTokens(context.Background())

	if got := loginCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 login for the eligible account, got %d", got)
	}
}

func TestStartPoolTokenKeeperDisabledViaEnv(t *testing.T) {
	t.Setenv("DS2API_TOKEN_KEEP_INTERVAL", "off")
	if envPoolTokenKeepInterval() != 0 {
		t.Fatal("off must disable the keeper")
	}
	t.Setenv("DS2API_TOKEN_KEEP_INTERVAL", "bogus")
	if envPoolTokenKeepInterval() != defaultPoolTokenKeepInterval {
		t.Fatal("invalid value must fall back to the default")
	}
	t.Setenv("DS2API_TOKEN_KEEP_INTERVAL", "90s")
	if got := envPoolTokenKeepInterval(); got != 90*time.Second {
		t.Fatalf("expected 90s, got %s", got)
	}
}
