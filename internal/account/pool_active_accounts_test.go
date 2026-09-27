package account

import (
	"testing"

	"ds2api/internal/config"
)

func TestActiveAccountsReturnsQueueSnapshot(t *testing.T) {
	t.Setenv("DS2API_CONFIG_JSON", `{
		"accounts":[
			{"email":"a@example.com","password":"pwd"},
			{"email":"b@example.com","password":"pwd"}
		]
	}`)
	store := config.LoadStore()
	pool := NewPool(store)

	active := pool.ActiveAccounts()
	if len(active) != 2 {
		t.Fatalf("expected 2 active accounts, got %d (%v)", len(active), active)
	}

	// The caller owns the snapshot: mutating it must not affect the pool.
	active[0] = "mutated@example.com"
	again := pool.ActiveAccounts()
	if again[0] != "a@example.com" {
		t.Fatalf("snapshot must be a copy, got %q", again[0])
	}
}

func TestActiveAccountsExcludesStandbyAndDisabled(t *testing.T) {
	t.Setenv("DS2API_CONFIG_JSON", `{
		"accounts":[
			{"email":"a@example.com","password":"pwd"},
			{"email":"b@example.com","password":"pwd"},
			{"email":"off@example.com","password":"pwd","enabled":false}
		],
		"runtime": {"active_pool_size": 1}
	}`)
	store := config.LoadStore()
	pool := NewPool(store)

	active := pool.ActiveAccounts()
	if len(active) != 1 {
		t.Fatalf("expected 1 active account (active_pool_size=1), got %d (%v)", len(active), active)
	}
	if active[0] != "a@example.com" {
		t.Fatalf("expected a@example.com first, got %q", active[0])
	}
}
