package auth

import (
	"context"
	"testing"
	"time"

	"ds2api/internal/account"
	"ds2api/internal/config"
)

func TestReenableIfUnbanned_RestoresBannedAccount(t *testing.T) {
	// Setup: account is banned and auto-disabled.
	t.Setenv("DS2API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[{"email":"acc@example.com","password":"pwd"}]
	}`)
	store := config.LoadStore()
	if err := store.UpdateAccountBanStatus("acc@example.com", 1, 0, 3); err != nil {
		t.Fatalf("set ban status: %v", err)
	}
	if err := store.SetAccountEnabled("acc@example.com", false, "banned"); err != nil {
		t.Fatalf("disable account: %v", err)
	}
	// Re-login un-mutes the account.
	var loginCalled bool
	resolver := NewResolver(store, account.NewPool(store), func(_ context.Context, acc config.Account) (string, error) {
		loginCalled = true
		_ = store.UpdateAccountBanStatus(acc.Identifier(), 0, 0, 0)
		return "fresh-token", nil
	})

	acc := mustAccount(t, store, "acc@example.com")
	a := &RequestAuth{
		UseConfigToken: true,
		AccountID:      acc.Identifier(),
		Account:        acc,
		TriedAccounts:  map[string]bool{},
		resolver:       resolver,
	}

	if err := resolver.loginAndPersist(context.Background(), a); err != nil {
		t.Fatalf("loginAndPersist: %v", err)
	}
	if !loginCalled {
		t.Fatal("expected login to be called")
	}

	reloaded := mustAccount(t, store, "acc@example.com")
	if !reloaded.IsEnabled() {
		t.Fatal("expected account re-enabled after ban lifted")
	}
	if reloaded.DisabledReason != "" {
		t.Fatalf("expected cleared disabled reason, got %q", reloaded.DisabledReason)
	}
}

func TestReenableIfUnbanned_SkipsManualDisabled(t *testing.T) {
	// Manually disabled accounts should not be touched.
	t.Setenv("DS2API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[{"email":"acc@example.com","password":"pwd"}]
	}`)
	store := config.LoadStore()
	// Not banned, but disabled manually.
	if err := store.SetAccountEnabled("acc@example.com", false, "manual"); err != nil {
		t.Fatalf("disable account: %v", err)
	}

	var loginCalled bool
	resolver := NewResolver(store, account.NewPool(store), func(_ context.Context, acc config.Account) (string, error) {
		loginCalled = true
		return "fresh-token", nil
	})

	acc := mustAccount(t, store, "acc@example.com")
	a := &RequestAuth{
		UseConfigToken: true,
		AccountID:      acc.Identifier(),
		Account:        acc,
		TriedAccounts:  map[string]bool{},
		resolver:       resolver,
	}

	if err := resolver.loginAndPersist(context.Background(), a); err != nil {
		t.Fatalf("loginAndPersist: %v", err)
	}
	if !loginCalled {
		t.Fatal("expected login to be called")
	}

	reloaded := mustAccount(t, store, "acc@example.com")
	if reloaded.IsEnabled() {
		t.Fatal("manual-disabled account should stay disabled")
	}
	if reloaded.DisabledReason != "manual" {
		t.Fatalf("expected 'manual' reason, got %q", reloaded.DisabledReason)
	}
}

func TestCheckExpiredBans_ReenablesWhenMuteExpired(t *testing.T) {
	// Account banned+disabled with mute_until in the past.
	t.Setenv("DS2API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[{"email":"acc@example.com","password":"pwd"}]
	}`)
	store := config.LoadStore()
	if err := store.UpdateAccountBanStatus("acc@example.com", 1, float64(time.Now().Add(-1*time.Hour).Unix()), 3); err != nil {
		t.Fatalf("set ban status: %v", err)
	}
	if err := store.SetAccountEnabled("acc@example.com", false, "banned"); err != nil {
		t.Fatalf("disable account: %v", err)
	}

	var loginCalled bool
	resolver := NewResolver(store, account.NewPool(store), func(_ context.Context, acc config.Account) (string, error) {
		loginCalled = true
		_ = store.UpdateAccountBanStatus(acc.Identifier(), 0, 0, 0)
		return "fresh-token", nil
	})

	resolver.checkExpiredBans(context.Background())

	if !loginCalled {
		t.Fatal("expected login to be triggered for expired mute")
	}

	reloaded := mustAccount(t, store, "acc@example.com")
	if !reloaded.IsEnabled() {
		t.Fatal("expected account re-enabled after mute expired and ban lifted")
	}
}

func TestCheckExpiredBans_SkipsFutureMute(t *testing.T) {
	// Account banned+disabled with mute_until in the future.
	t.Setenv("DS2API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[{"email":"acc@example.com","password":"pwd"}]
	}`)
	store := config.LoadStore()
	if err := store.UpdateAccountBanStatus("acc@example.com", 1, float64(time.Now().Add(2*time.Hour).Unix()), 3); err != nil {
		t.Fatalf("set ban status: %v", err)
	}
	if err := store.SetAccountEnabled("acc@example.com", false, "banned"); err != nil {
		t.Fatalf("disable account: %v", err)
	}

	var loginCalled bool
	resolver := NewResolver(store, account.NewPool(store), func(_ context.Context, _ config.Account) (string, error) {
		loginCalled = true
		return "fresh-token", nil
	})

	resolver.checkExpiredBans(context.Background())

	if loginCalled {
		t.Fatal("login should not be called for future mute")
	}
}

func TestCheckExpiredBans_ReenablesStaleBannedState(t *testing.T) {
	// Disabled with reason "banned" but IsBanned()==false (stale state).
	t.Setenv("DS2API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[{"email":"acc@example.com","password":"pwd"}]
	}`)
	store := config.LoadStore()
	// Not actually banned (is_muted=0).
	if err := store.UpdateAccountBanStatus("acc@example.com", 0, 0, 0); err != nil {
		t.Fatalf("set ban status: %v", err)
	}
	if err := store.SetAccountEnabled("acc@example.com", false, "banned"); err != nil {
		t.Fatalf("disable account: %v", err)
	}

	resolver := NewResolver(store, account.NewPool(store), func(_ context.Context, _ config.Account) (string, error) {
		return "fresh-token", nil
	})

	resolver.checkExpiredBans(context.Background())

	reloaded := mustAccount(t, store, "acc@example.com")
	if !reloaded.IsEnabled() {
		t.Fatal("stale-banned account should be re-enabled")
	}
}

func TestCheckExpiredBans_StillBannedAccount(t *testing.T) {
	// Account banned+disabled with expired mute, but login shows still banned.
	t.Setenv("DS2API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[{"email":"acc@example.com","password":"pwd"}]
	}`)
	store := config.LoadStore()
	if err := store.UpdateAccountBanStatus("acc@example.com", 1, float64(time.Now().Add(-1*time.Hour).Unix()), 3); err != nil {
		t.Fatalf("set ban status: %v", err)
	}
	if err := store.SetAccountEnabled("acc@example.com", false, "banned"); err != nil {
		t.Fatalf("disable account: %v", err)
	}

	var loginCalled bool
	resolver := NewResolver(store, account.NewPool(store), func(_ context.Context, _ config.Account) (string, error) {
		loginCalled = true
		// Login still reports banned (is_muted=1).
		_ = store.UpdateAccountBanStatus("acc@example.com", 1, float64(time.Now().Add(2*time.Hour).Unix()), 3)
		return "fresh-token", nil
	})

	resolver.checkExpiredBans(context.Background())

	if !loginCalled {
		t.Fatal("expected login to be triggered even if still banned")
	}

	reloaded := mustAccount(t, store, "acc@example.com")
	if reloaded.IsEnabled() {
		t.Fatal("account should stay disabled while still banned")
	}
}

func mustAccount(t *testing.T, store *config.Store, identifier string) config.Account {
	t.Helper()
	acc, ok := store.FindAccount(identifier)
	if !ok {
		t.Fatalf("account %q not found", identifier)
	}
	return acc
}
