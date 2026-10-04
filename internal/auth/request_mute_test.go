package auth

import (
	"context"
	"testing"

	"ds2api/internal/config"
)

// TestMarkAccountMutedEvictsAsBannedWithExpiry guards the completion-time
// mute handling: marking an account muted persists the ban state (so the
// unban monitor can auto-recover it after the mute expires), disables it with
// reason "banned", and removes it from the pool — while a direct-token
// request never touches account state.
func TestMarkAccountMutedEvictsAsBannedWithExpiry(t *testing.T) {
	resolver, store, pool := newEvictTestResolver(t, func(_ context.Context, _ config.Account) (string, error) {
		return "fresh-token", nil
	})
	a := &RequestAuth{UseConfigToken: true, AccountID: "acc1@example.com", resolver: resolver}

	a.MarkAccountMuted(1790939390.395)

	acc, ok := store.FindAccount("acc1@example.com")
	if !ok {
		t.Fatal("expected account to exist")
	}
	if acc.IsEnabled() {
		t.Fatal("expected muted account to be disabled")
	}
	if acc.DisabledReason != "banned" {
		t.Fatalf("expected disabled_reason banned, got %q", acc.DisabledReason)
	}
	if acc.BanIsMuted != 1 {
		t.Fatalf("expected ban_is_muted=1, got %d", acc.BanIsMuted)
	}
	if acc.BanMuteUntil != 1790939390.395 {
		t.Fatalf("expected ban_mute_until=1790939390.395, got %v", acc.BanMuteUntil)
	}
	if acc.Token != "stored-token-acc1@example.com" {
		t.Fatalf("expected stored token to survive the mute eviction, got %q", acc.Token)
	}
	if poolMembers(t, pool)["acc1@example.com"] {
		t.Fatal("expected muted account to be removed from the pool")
	}
	if !poolMembers(t, pool)["acc2@example.com"] {
		t.Fatal("expected the other account to stay pooled")
	}

	// Idempotent: marking an already-disabled account must not overwrite the
	// original disabled state.
	a.MarkAccountMuted(111)
	if acc, _ := store.FindAccount("acc1@example.com"); acc.BanMuteUntil != 1790939390.395 {
		t.Fatalf("re-marking a disabled account must not change state, got mute_until=%v", acc.BanMuteUntil)
	}
}

func TestMarkAccountMutedDirectTokenNoop(t *testing.T) {
	resolver, _, _ := newEvictTestResolver(t, func(_ context.Context, _ config.Account) (string, error) {
		return "fresh-token", nil
	})
	a := &RequestAuth{UseConfigToken: false, DeepSeekToken: "direct-token", resolver: resolver}

	a.MarkAccountMuted(1790939390.395)

	if a.AccountID != "" {
		t.Fatalf("direct-token request must not touch account state, got %q", a.AccountID)
	}
}
