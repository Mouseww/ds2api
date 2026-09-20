package usagestats

import (
	"testing"
	"time"
)

func TestAccountWindowUsageSumsOnlyRecentTraffic(t *testing.T) {
	store := NewStore("")
	now := time.Now()
	store.Record(Event{Time: now.Add(-3 * time.Hour), Model: "m", AccountID: "acc-a", TotalTokens: 300})
	store.Record(Event{Time: now.Add(-30 * time.Minute), Model: "m", AccountID: "acc-a", TotalTokens: 50})

	got := store.AccountWindowUsage(time.Hour)
	usage, ok := got["acc-a"]
	if !ok {
		t.Fatalf("expected acc-a in window usage, got %v", got)
	}
	if usage.TotalTokens != 50 {
		t.Fatalf("expected only the in-window tokens, got %d", usage.TotalTokens)
	}
	if usage.Requests != 1 {
		t.Fatalf("expected only the in-window request, got %d", usage.Requests)
	}

	all := store.AccountWindowUsage(6 * time.Hour)
	if all["acc-a"].TotalTokens != 350 {
		t.Fatalf("expected the wider window to include old usage, got %d", all["acc-a"].TotalTokens)
	}
}

func TestAccountWindowUsageOmitsIdleAccounts(t *testing.T) {
	store := NewStore("")
	store.Record(Event{Time: time.Now(), Model: "m", AccountID: "acc-a", TotalTokens: 10})

	got := store.AccountWindowUsage(time.Hour)
	if _, ok := got["acc-b"]; ok {
		t.Fatalf("expected acc-b omitted, got %v", got)
	}
	if len(got) != 1 {
		t.Fatalf("expected a single account, got %v", got)
	}
}

func TestAccountWindowUsageFallsBackToHourBuckets(t *testing.T) {
	store := NewStore("")
	now := time.Now()
	// 40h-old traffic is outside both the 36h window and the minute
	// retention, so the hour-bucket fallback must still see the 20h-old
	// traffic it keeps.
	store.Record(Event{Time: now.Add(-40 * time.Hour), Model: "m", AccountID: "acc-a", TotalTokens: 400})
	store.Record(Event{Time: now.Add(-20 * time.Hour), Model: "m", AccountID: "acc-a", TotalTokens: 20})

	got := store.AccountWindowUsage(36 * time.Hour)
	usage, ok := got["acc-a"]
	if !ok {
		t.Fatalf("expected acc-a in window usage, got %v", got)
	}
	if usage.TotalTokens != 20 {
		t.Fatalf("expected only the in-window tokens, got %d", usage.TotalTokens)
	}
}

func TestAccountWindowUsageRejectsInvalidWindow(t *testing.T) {
	store := NewStore("")
	store.Record(Event{Time: time.Now(), Model: "m", AccountID: "acc-a", TotalTokens: 10})
	if got := store.AccountWindowUsage(0); len(got) != 0 {
		t.Fatalf("expected empty result for zero window, got %v", got)
	}
	if got := (*Store)(nil).AccountWindowUsage(time.Hour); len(got) != 0 {
		t.Fatalf("expected empty result for nil store, got %v", got)
	}
}
