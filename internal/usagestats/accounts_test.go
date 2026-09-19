package usagestats

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestQueryAccountsRanksAndScopesRange(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	now := time.Now()
	store.Record(Event{
		Time: now, Surface: SurfaceOpenAIChat, Model: "deepseek-v4-pro",
		AccountID: "acc-a", StatusCode: 200, PromptTokens: 10, TotalTokens: 200,
	})
	store.Record(Event{
		Time: now, Surface: SurfaceOpenAIChat, Model: "deepseek-v4-pro",
		AccountID: "acc-b", StatusCode: 200, TotalTokens: 60,
	})
	store.Record(Event{
		Time: now, Surface: SurfaceOpenAIChat, Model: "deepseek-v4-pro",
		AccountID: "acc-b", StatusCode: 500, TotalTokens: 40,
	})
	// Older than the 1h window but inside the 24h window.
	store.Record(Event{
		Time: now.Add(-3 * time.Hour), Surface: SurfaceOpenAIChat, Model: "deepseek-v4-pro",
		AccountID: "acc-a", StatusCode: 200, TotalTokens: 999,
	})

	hour := store.QueryAccounts("1h")
	if hour.Range != "1h" || hour.Bucket != "1m" {
		t.Fatalf("range/bucket = %q/%q, want 1h/1m", hour.Range, hour.Bucket)
	}
	if len(hour.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(hour.Accounts))
	}
	if hour.Accounts[0].AccountID != "acc-a" {
		t.Fatalf("top account = %q, want acc-a", hour.Accounts[0].AccountID)
	}
	if hour.Accounts[0].TotalTokens != 200 || hour.Accounts[0].Requests != 1 {
		t.Fatalf("acc-a hour stat = %+v", hour.Accounts[0])
	}
	if hour.Accounts[1].AccountID != "acc-b" || hour.Accounts[1].Requests != 2 || hour.Accounts[1].Errors != 1 {
		t.Fatalf("acc-b hour stat = %+v", hour.Accounts[1])
	}
	if hour.Accounts[1].SuccessRate != 0.5 {
		t.Fatalf("acc-b success rate = %v, want 0.5", hour.Accounts[1].SuccessRate)
	}
	if hour.Summary.Requests != 3 || hour.Attributed.Requests != 3 {
		t.Fatalf("hour summary=%d attributed=%d, want 3/3", hour.Summary.Requests, hour.Attributed.Requests)
	}
	if hour.Attributed.TotalTokens != 300 {
		t.Fatalf("attributed tokens = %d, want 300", hour.Attributed.TotalTokens)
	}

	day := store.QueryAccounts("24h")
	if len(day.Accounts) != 2 {
		t.Fatalf("24h accounts = %d, want 2", len(day.Accounts))
	}
	if day.Accounts[0].AccountID != "acc-a" || day.Accounts[0].TotalTokens != 1199 {
		t.Fatalf("24h top account = %+v, want acc-a with 1199 tokens", day.Accounts[0])
	}
	if day.Summary.Requests != 4 {
		t.Fatalf("24h summary requests = %d, want 4", day.Summary.Requests)
	}
}

// The per-account view must never silently blame a real account for traffic
// that failed before an account was acquired.
func TestQueryAccountsReportsAttributionGap(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	now := time.Now()
	store.Record(Event{
		Time: now, Surface: SurfaceOpenAIChat, Model: "deepseek-v4-pro",
		AccountID: "acc-a", StatusCode: 200, TotalTokens: 10,
	})
	store.Record(Event{Time: now, Surface: SurfaceOpenAIChat, Model: "deepseek-v4-pro", StatusCode: 502})

	snap := store.QueryAccounts("1h")
	if snap.Summary.Requests != 2 {
		t.Fatalf("summary requests = %d, want 2", snap.Summary.Requests)
	}
	if snap.Attributed.Requests != 1 {
		t.Fatalf("attributed requests = %d, want 1", snap.Attributed.Requests)
	}
	if len(snap.Accounts) != 1 || snap.Accounts[0].AccountID != "acc-a" {
		t.Fatalf("accounts = %+v, want only acc-a", snap.Accounts)
	}
	if snap.TrackingSince == 0 {
		t.Fatal("expected a tracking start timestamp")
	}
}

func TestQueryAccountDetailRendersTrendAndBreakdowns(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	now := time.Now()
	store.Record(Event{
		Time: now, Surface: SurfaceOpenAIChat, Model: "deepseek-v4-pro",
		AccountID: "acc-a", StatusCode: 200, Stream: true, TotalTokens: 30,
	})
	store.Record(Event{
		Time: now, Surface: SurfaceClaudeMessages, Model: "deepseek-v4-pro",
		AccountID: "acc-a", StatusCode: 200, TotalTokens: 20,
	})

	detail, ok := store.QueryAccount("1h", "acc-a")
	if !ok {
		t.Fatal("expected acc-a to be found")
	}
	if detail.Account.AccountID != "acc-a" || detail.Account.Requests != 2 || detail.Account.TotalTokens != 50 {
		t.Fatalf("range stat = %+v", detail.Account)
	}
	if detail.Account.StreamRequests != 1 {
		t.Fatalf("range stream requests = %d, want 1", detail.Account.StreamRequests)
	}
	if detail.Totals.Requests != 2 || detail.Totals.TotalTokens != 50 || detail.Totals.StreamRequests != 1 {
		t.Fatalf("totals = %+v", detail.Totals)
	}
	if detail.Account.FirstSeenAt == 0 || detail.Account.LastSeenAt == 0 {
		t.Fatalf("expected first/last seen timestamps, got %+v", detail.Account)
	}
	if len(detail.Series) != 61 {
		t.Fatalf("series length = %d, want 61 one-minute buckets", len(detail.Series))
	}
	var seriesRequests int64
	for _, point := range detail.Series {
		seriesRequests += point.Requests
	}
	if seriesRequests != 2 {
		t.Fatalf("series requests = %d, want 2", seriesRequests)
	}
	if len(detail.Models) != 1 || detail.Models[0].Key != "deepseek-v4-pro" || detail.Models[0].Requests != 2 {
		t.Fatalf("models = %+v", detail.Models)
	}
	if len(detail.Surfaces) != 2 {
		t.Fatalf("surfaces = %d, want 2", len(detail.Surfaces))
	}
	if detail.Bucket != "1m" || detail.BucketMs != int64(time.Minute/time.Millisecond) {
		t.Fatalf("bucket = %q/%d, want 1m/%d", detail.Bucket, detail.BucketMs, int64(time.Minute/time.Millisecond))
	}
}

func TestQueryAccountUnknownOrBlankReportsFalse(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	if _, ok := store.QueryAccount("1h", "missing"); ok {
		t.Fatal("expected an unknown account to report false")
	}
	if _, ok := store.QueryAccount("1h", "   "); ok {
		t.Fatal("expected a blank account to report false")
	}
}

func TestAccountSeriesIgnoresUnattributedRequests(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	store.Record(Event{Time: time.Now(), Surface: SurfaceOpenAIChat, StatusCode: 200})

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.accountSeries) != 0 {
		t.Fatalf("account series = %d, want 0 for unattributed traffic", len(store.accountSeries))
	}
}

func TestAccountSeriesCardinalityIsCapped(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	now := time.Now()
	for i := 0; i < maxTrackedAccounts+7; i++ {
		store.Record(Event{
			Time: now, Surface: SurfaceOpenAIChat,
			AccountID: fmt.Sprintf("acct-%d", i), StatusCode: 200,
		})
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.accountSeries) > maxTrackedAccounts+1 {
		t.Fatalf("account series = %d, want at most %d", len(store.accountSeries), maxTrackedAccounts+1)
	}
	if _, ok := store.accountSeries[otherKey]; !ok {
		t.Fatalf("expected overflow accounts to fold into %q", otherKey)
	}
	if store.totals.Requests != int64(maxTrackedAccounts+7) {
		t.Fatalf("totals requests = %d, want %d", store.totals.Requests, maxTrackedAccounts+7)
	}
}

// Each resolution has its own window, so an old event must drop out of the
// finer maps while the coarser one keeps it — and the cumulative totals must
// never be touched by a prune.
func TestAccountSeriesPruneRespectsPerResolutionWindows(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	// 60 days old: past both the minute (30h) and hour (32d) windows, still
	// inside the day (400d) window.
	store.Record(Event{
		Time: time.Now().Add(-60 * 24 * time.Hour), Surface: SurfaceOpenAIChat,
		AccountID: "acc-a", StatusCode: 200, TotalTokens: 5,
	})
	// Query prunes against wall-clock time.
	_ = store.Query("1h")

	store.mu.Lock()
	defer store.mu.Unlock()
	series := store.accountSeries["acc-a"]
	if series == nil {
		t.Fatal("expected the acc-a series to survive the prune")
	}
	if len(series.Minutes) != 0 {
		t.Fatalf("minute buckets = %d, want 0 after prune", len(series.Minutes))
	}
	if len(series.Hours) != 0 {
		t.Fatalf("hour buckets = %d, want 0 after prune", len(series.Hours))
	}
	if len(series.Days) != 1 {
		t.Fatalf("day buckets = %d, want 1 to survive inside the day window", len(series.Days))
	}
	if series.Total.Requests != 1 || series.Total.Total != 5 {
		t.Fatalf("cumulative totals = %+v, want 1 request / 5 tokens", series.Total)
	}
	if series.FirstAt == 0 || series.LastAt == 0 {
		t.Fatalf("expected first/last seen to survive the prune, got %+v", series)
	}
}

func TestResetClearsAccountSeries(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	store.Record(Event{Time: time.Now(), Surface: SurfaceOpenAIChat, AccountID: "acc-a", StatusCode: 200})
	if err := store.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, ok := store.QueryAccount("1h", "acc-a"); ok {
		t.Fatal("expected reset to clear the per-account series")
	}
	if got := len(store.QueryAccounts("1h").Accounts); got != 0 {
		t.Fatalf("ranked accounts after reset = %d, want 0", got)
	}
}

func TestAccountSeriesPersistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")

	store := NewStore(path)
	store.Record(Event{
		Time: time.Now(), Surface: SurfaceOpenAIChat, Model: "deepseek-v4-pro",
		AccountID: "acc-a", StatusCode: 200,
		PromptTokens: 4, CompletionTokens: 6, ReasoningTokens: 2, TotalTokens: 10,
	})
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reloaded := NewStore(path)
	defer func() { _ = reloaded.Close() }()
	if err := reloaded.Err(); err != nil {
		t.Fatalf("reload error: %v", err)
	}

	detail, ok := reloaded.QueryAccount("24h", "acc-a")
	if !ok {
		t.Fatal("expected acc-a to survive the reload")
	}
	if detail.Account.Requests != 1 || detail.Account.TotalTokens != 10 {
		t.Fatalf("reloaded range stat = %+v", detail.Account)
	}
	if detail.Totals.PromptTokens != 4 || detail.Totals.CompletionTokens != 6 || detail.Totals.ReasoningTokens != 2 {
		t.Fatalf("reloaded totals = %+v", detail.Totals)
	}
	if len(detail.Models) != 1 || detail.Models[0].Key != "deepseek-v4-pro" {
		t.Fatalf("reloaded models = %+v", detail.Models)
	}

	ranked := reloaded.QueryAccounts("24h")
	if len(ranked.Accounts) != 1 || ranked.Accounts[0].AccountID != "acc-a" {
		t.Fatalf("reloaded ranked accounts = %+v", ranked.Accounts)
	}
	if ranked.Accounts[0].TotalTokens != 10 {
		t.Fatalf("reloaded ranked tokens = %d, want 10", ranked.Accounts[0].TotalTokens)
	}
}
