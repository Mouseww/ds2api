package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
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

// testJWT builds a fake DeepSeek token: a three-segment JWT whose payload
// carries only the exp claim. The signature segment is opaque filler.
func testJWT(t *testing.T, expiry time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"exp":` + strconv.FormatInt(expiry.Unix(), 10) + `}`))
	return header + "." + payload + ".sig"
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
	r, store := newKeeperEnv(t, `[{"email":"a@example.com","password":"pwd"}]`)
	var loginCalls atomic.Int32
	r.Login = func(_ context.Context, _ config.Account) (string, error) {
		loginCalls.Add(1)
		return "", ErrLoginThrottled
	}

	// Throttled logins retry every sweep and never evict the account.
	for i := 0; i < keeperFailureThreshold+2; i++ {
		r.ensurePoolTokens(context.Background())
	}
	if got := loginCalls.Load(); got != int32(keeperFailureThreshold+2) {
		t.Fatalf("expected a login attempt every sweep while throttled, got %d", got)
	}
	if got := r.keeperFailureCount("a@example.com"); got != 0 {
		t.Fatalf("throttling must not count as failure, counter=%d", got)
	}
	acc, ok := store.FindAccount("a@example.com")
	if !ok || !acc.IsEnabled() {
		t.Fatal("throttled logins must not evict the account")
	}
}

func TestEnsurePoolTokensEvictsAfterRepeatedLoginFailures(t *testing.T) {
	r, store := newKeeperEnv(t, `[{"email":"a@example.com","password":"pwd"}]`)
	var loginCalls atomic.Int32
	r.Login = func(_ context.Context, _ config.Account) (string, error) {
		loginCalls.Add(1)
		return "", errors.New("login failed: bad password")
	}

	// First refresh failure is tolerated: the account stays in the pool.
	r.ensurePoolTokens(context.Background())
	if got := loginCalls.Load(); got != 1 {
		t.Fatalf("expected 1 login attempt, got %d", got)
	}
	acc, ok := store.FindAccount("a@example.com")
	if !ok || !acc.IsEnabled() {
		t.Fatal("account must stay enabled after a single refresh failure")
	}

	// Second consecutive failure evicts the account from the active pool.
	r.ensurePoolTokens(context.Background())
	if got := loginCalls.Load(); got != 2 {
		t.Fatalf("expected 2 login attempts, got %d", got)
	}
	acc, ok = store.FindAccount("a@example.com")
	if !ok {
		t.Fatal("account missing from store")
	}
	if acc.IsEnabled() {
		t.Fatal("account must be disabled after two consecutive refresh failures")
	}
	if acc.DisabledReason != "refresh_failed" {
		t.Fatalf("expected disabled_reason=refresh_failed, got %q", acc.DisabledReason)
	}
	if active := r.Pool.ActiveAccounts(); len(active) != 0 {
		t.Fatalf("evicted account must leave the active pool, got %v", active)
	}

	// Evicted (disabled) accounts are not retried by later sweeps.
	r.ensurePoolTokens(context.Background())
	if got := loginCalls.Load(); got != 2 {
		t.Fatalf("expected no further login attempts after eviction, got %d", got)
	}
}

func TestEnsurePoolTokensEvictionPromotesStandby(t *testing.T) {
	t.Setenv("DS2API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"a@example.com","password":"pwd"},
			{"email":"b@example.com","password":"pwd"}
		],
		"runtime": {"active_pool_size": 1}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	r := NewResolver(store, pool, func(_ context.Context, _ config.Account) (string, error) {
		return "", errors.New("login failed: bad password")
	})

	if active := pool.ActiveAccounts(); len(active) != 1 || active[0] != "a@example.com" {
		t.Fatalf("expected a@example.com active, got %v", active)
	}

	// Two consecutive refresh failures evict the active account; the
	// standby account is promoted to take over its traffic.
	r.ensurePoolTokens(context.Background())
	r.ensurePoolTokens(context.Background())

	if active := pool.ActiveAccounts(); len(active) != 1 || active[0] != "b@example.com" {
		t.Fatalf("expected standby b@example.com promoted after eviction, got %v", active)
	}
	acc, ok := store.FindAccount("a@example.com")
	if !ok || acc.IsEnabled() || acc.DisabledReason != "refresh_failed" {
		t.Fatalf("expected a@example.com disabled with refresh_failed, got %+v", acc)
	}
}

func TestEnsurePoolTokensRefreshesExpiredTokenWithoutProbe(t *testing.T) {
	r, store := newKeeperEnv(t, `[{"email":"a@example.com","password":"pwd"}]`)
	expired := testJWT(t, time.Now().Add(-time.Hour))
	if err := store.UpdateAccountToken("a@example.com", expired); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	var loginCalls, probeCalls atomic.Int32
	r.Login = func(_ context.Context, _ config.Account) (string, error) {
		loginCalls.Add(1)
		return testJWT(t, time.Now().Add(24*time.Hour)), nil
	}
	r.TokenProbe = func(_ context.Context, _ string) error {
		probeCalls.Add(1)
		return nil
	}

	r.ensurePoolTokens(context.Background())

	if got := probeCalls.Load(); got != 0 {
		t.Fatalf("expired token must not be probed, got %d probes", got)
	}
	if got := loginCalls.Load(); got != 1 {
		t.Fatalf("expected immediate refresh for expired token, got %d logins", got)
	}
	acc, _ := store.FindAccount("a@example.com")
	if acc.Token == expired {
		t.Fatal("expected refreshed token to be persisted")
	}
}

func TestEnsurePoolTokensRefreshesTokenExpiringWithinSweep(t *testing.T) {
	r, store := newKeeperEnv(t, `[{"email":"a@example.com","password":"pwd"}]`)
	// Expires before the next sweep (default keep interval 5m): refreshing
	// now guarantees the pooled token never expires in service.
	expiring := testJWT(t, time.Now().Add(time.Minute))
	if err := store.UpdateAccountToken("a@example.com", expiring); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	var loginCalls, probeCalls atomic.Int32
	r.Login = func(_ context.Context, _ config.Account) (string, error) {
		loginCalls.Add(1)
		return testJWT(t, time.Now().Add(24*time.Hour)), nil
	}
	r.TokenProbe = func(_ context.Context, _ string) error {
		probeCalls.Add(1)
		return nil
	}

	r.ensurePoolTokens(context.Background())

	if got := probeCalls.Load(); got != 0 {
		t.Fatalf("soon-expiring token must not be probed, got %d probes", got)
	}
	if got := loginCalls.Load(); got != 1 {
		t.Fatalf("expected refresh for soon-expiring token, got %d logins", got)
	}
}

func TestEnsurePoolTokensProbesTokenWithDistantExpiry(t *testing.T) {
	r, store := newKeeperEnv(t, `[{"email":"a@example.com","password":"pwd"}]`)
	if err := store.UpdateAccountToken("a@example.com", testJWT(t, time.Now().Add(24*time.Hour))); err != nil {
		t.Fatalf("seed token: %v", err)
	}
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

	if got := probeCalls.Load(); got != 1 {
		t.Fatalf("expected 1 probe for distant-expiry token, got %d", got)
	}
	if got := loginCalls.Load(); got != 0 {
		t.Fatalf("expected no login while probe passes, got %d", got)
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

func TestTokenExpiry(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	got, ok := tokenExpiry(testJWT(t, time.Unix(exp, 0)))
	if !ok || got.Unix() != exp {
		t.Fatalf("expected exp %d, got %v (ok=%v)", exp, got, ok)
	}

	if _, ok := tokenExpiry("not-a-jwt"); ok {
		t.Fatal("non-JWT token must not decode")
	}
	if _, ok := tokenExpiry(""); ok {
		t.Fatal("empty token must not decode")
	}
	if _, ok := tokenExpiry("a." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`)) + ".b"); ok {
		t.Fatal("JWT without exp must not decode")
	}
	if _, ok := tokenExpiry("a." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":"123"}`)) + ".b"); ok {
		t.Fatal("string exp must not decode")
	}
	if _, ok := tokenExpiry("a." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":0}`)) + ".b"); ok {
		t.Fatal("zero exp must not decode")
	}
	if _, ok := tokenExpiry("a." + base64.RawURLEncoding.EncodeToString([]byte(`not json`)) + ".b"); ok {
		t.Fatal("non-JSON payload must not decode")
	}
}
