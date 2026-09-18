package usagestats

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordAggregatesTotalsAndBreakdowns(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	now := time.Now()
	store.Record(Event{
		Time: now, Surface: SurfaceOpenAIChat, Model: "deepseek-v4-pro",
		AccountID: "acc-1", CallerID: "caller-1", StatusCode: 200,
		PromptTokens: 10, CompletionTokens: 5, ReasoningTokens: 2, TotalTokens: 15, ElapsedMs: 40,
	})
	store.Record(Event{
		Time: now, Surface: SurfaceOpenAIChat, Model: "deepseek-v4-pro",
		AccountID: "acc-2", CallerID: "caller-1", StatusCode: 500,
		PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3, ElapsedMs: 10,
	})

	snap := store.Query("1h")
	totals := snap.Totals
	if totals.Requests != 2 {
		t.Fatalf("requests = %d, want 2", totals.Requests)
	}
	if totals.Errors != 1 {
		t.Fatalf("errors = %d, want 1", totals.Errors)
	}
	if totals.SuccessRequests != 1 {
		t.Fatalf("success requests = %d, want 1", totals.SuccessRequests)
	}
	if totals.TotalTokens != 18 {
		t.Fatalf("total tokens = %d, want 18", totals.TotalTokens)
	}
	if totals.ReasoningTokens != 2 {
		t.Fatalf("reasoning tokens = %d, want 2", totals.ReasoningTokens)
	}
	if totals.AvgElapsedMs != 25 {
		t.Fatalf("avg elapsed = %d, want 25", totals.AvgElapsedMs)
	}
	if totals.MaxElapsedMs != 40 {
		t.Fatalf("max elapsed = %d, want 40", totals.MaxElapsedMs)
	}

	if len(snap.Models) != 1 || snap.Models[0].Key != "deepseek-v4-pro" || snap.Models[0].Requests != 2 {
		t.Fatalf("models = %+v, want one entry with 2 requests", snap.Models)
	}
	if len(snap.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(snap.Accounts))
	}
	if len(snap.Callers) != 1 || snap.Callers[0].Key != "caller-1" {
		t.Fatalf("callers = %+v, want caller-1", snap.Callers)
	}
	if len(snap.Surfaces) != 1 || snap.Surfaces[0].Key != SurfaceOpenAIChat {
		t.Fatalf("surfaces = %+v, want %s", snap.Surfaces, SurfaceOpenAIChat)
	}
}

func TestRecordUsesPlaceholderForMissingIdentity(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	store.Record(Event{Time: time.Now(), Surface: SurfaceOpenAIChat, StatusCode: 200})

	snap := store.Query("1h")
	if len(snap.Models) != 1 || snap.Models[0].Key != noneKey {
		t.Fatalf("models = %+v, want a single %q entry", snap.Models, noneKey)
	}
}

func TestBreakdownCardinalityIsCapped(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	overflow := 10
	for i := 0; i < maxBreakdownKeys+overflow; i++ {
		store.Record(Event{
			Time: time.Now(), Surface: SurfaceOpenAIChat,
			Model: fmt.Sprintf("model-%d", i), StatusCode: 200,
		})
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.models) > maxBreakdownKeys+1 {
		t.Fatalf("model keys = %d, want at most %d", len(store.models), maxBreakdownKeys+1)
	}
	if _, ok := store.models[otherKey]; !ok {
		t.Fatalf("expected overflow models under %q", otherKey)
	}
	if store.totals.Requests != int64(maxBreakdownKeys+overflow) {
		t.Fatalf("totals requests = %d, want %d", store.totals.Requests, maxBreakdownKeys+overflow)
	}
}

func TestRecordAfterCloseIsIgnored(t *testing.T) {
	store := NewStore("")
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	store.Record(Event{Time: time.Now(), Surface: SurfaceOpenAIChat, StatusCode: 200})
	if got := store.Query("1h").Totals.Requests; got != 0 {
		t.Fatalf("requests = %d, want 0 after close", got)
	}
}

func TestPruneDropsExpiredBucketsButKeepsTotals(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	store.Record(Event{
		Time: time.Now().Add(-72 * time.Hour), Surface: SurfaceOpenAIChat, StatusCode: 200,
	})
	// Query prunes against wall-clock time.
	_ = store.Query("1h")

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.minutes) != 0 {
		t.Fatalf("minute buckets = %d, want 0 after prune", len(store.minutes))
	}
	if len(store.seconds) != 0 {
		t.Fatalf("second buckets = %d, want 0 after prune", len(store.seconds))
	}
	if store.totals.Requests != 1 {
		t.Fatalf("totals requests = %d, want 1 (totals are cumulative)", store.totals.Requests)
	}
}

func TestResetClearsEverything(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	store.Record(Event{Time: time.Now(), Surface: SurfaceOpenAIChat, StatusCode: 200, TotalTokens: 9})
	if got := store.Query("1h").Totals.Requests; got != 1 {
		t.Fatalf("requests = %d, want 1 before reset", got)
	}
	if err := store.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	snap := store.Query("1h")
	if snap.Totals.Requests != 0 || snap.Summary.Requests != 0 {
		t.Fatalf("requests after reset: totals=%d summary=%d, want 0", snap.Totals.Requests, snap.Summary.Requests)
	}
	if len(snap.Models) != 0 || len(snap.Surfaces) != 0 {
		t.Fatalf("breakdowns after reset: models=%d surfaces=%d, want 0", len(snap.Models), len(snap.Surfaces))
	}
}

func TestPersistAndReloadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")

	store := NewStore(path)
	if err := store.Err(); err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	store.Record(Event{
		Time: time.Now(), Surface: SurfaceOpenAIChat, Model: "deepseek-v4-pro",
		AccountID: "acc-1", CallerID: "caller-1", StatusCode: 200,
		PromptTokens: 4, CompletionTokens: 6, ReasoningTokens: 2, TotalTokens: 10, ElapsedMs: 25,
	})
	store.Record(Event{
		Time: time.Now(), Surface: SurfaceClaudeMessages, Model: "deepseek-v4-pro", StatusCode: 500,
	})
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reloaded := NewStore(path)
	defer func() { _ = reloaded.Close() }()
	if err := reloaded.Err(); err != nil {
		t.Fatalf("reload error: %v", err)
	}

	snap := reloaded.Query("24h")
	if snap.Totals.Requests != 2 {
		t.Fatalf("reloaded requests = %d, want 2", snap.Totals.Requests)
	}
	if snap.Totals.Errors != 1 {
		t.Fatalf("reloaded errors = %d, want 1", snap.Totals.Errors)
	}
	if snap.Totals.TotalTokens != 10 {
		t.Fatalf("reloaded tokens = %d, want 10", snap.Totals.TotalTokens)
	}
	if snap.Totals.PromptTokens != 4 || snap.Totals.CompletionTokens != 6 || snap.Totals.ReasoningTokens != 2 {
		t.Fatalf("reloaded token split = %+v", snap.Totals)
	}
	if len(snap.Models) != 1 || snap.Models[0].Key != "deepseek-v4-pro" {
		t.Fatalf("reloaded models = %+v", snap.Models)
	}
	if len(snap.Surfaces) != 2 {
		t.Fatalf("reloaded surfaces = %d, want 2", len(snap.Surfaces))
	}
	if snap.Totals.TrackingSince == 0 {
		t.Fatal("expected a tracking start timestamp after reload")
	}
}

func TestInMemoryStoreHasNoPath(t *testing.T) {
	store := NewStore("   ")
	defer func() { _ = store.Close() }()
	if store.Path() != "" {
		t.Fatalf("path = %q, want empty", store.Path())
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush on in-memory store: %v", err)
	}
}

func TestUsageFromMapProtocolShapes(t *testing.T) {
	cases := []struct {
		name       string
		usage      map[string]any
		prompt     int
		completion int
		reasoning  int
		total      int
	}{
		{
			name: "openai chat",
			usage: map[string]any{
				"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150,
				"completion_tokens_details": map[string]any{"reasoning_tokens": 20},
			},
			prompt: 100, completion: 50, reasoning: 20, total: 150,
		},
		{
			name: "openai responses",
			usage: map[string]any{
				"input_tokens": 10, "output_tokens": 4, "total_tokens": 14,
			},
			prompt: 10, completion: 4, total: 14,
		},
		{
			name:   "claude messages without total",
			usage:  map[string]any{"input_tokens": 7, "output_tokens": 3},
			prompt: 7, completion: 3, total: 10,
		},
		{
			name: "gemini",
			usage: map[string]any{
				"promptTokenCount": 5, "candidatesTokenCount": 2,
				"totalTokenCount": 7, "thoughtsTokenCount": 1,
			},
			prompt: 5, completion: 2, reasoning: 1, total: 7,
		},
		{name: "empty", usage: nil},
	}

	for _, tc := range cases {
		got := UsageFromMap(tc.usage)
		if got.PromptTokens != tc.prompt || got.CompletionTokens != tc.completion ||
			got.ReasoningTokens != tc.reasoning || got.TotalTokens != tc.total {
			t.Fatalf("%s: got %+v, want prompt=%d completion=%d reasoning=%d total=%d",
				tc.name, got, tc.prompt, tc.completion, tc.reasoning, tc.total)
		}
	}
}

func TestIntFromAnyCoercions(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{nil, 0}, {7, 7}, {int64(9), 9}, {int32(3), 3},
		{float64(12.4), 12}, {float64(12.6), 13}, {float32(2.5), 3},
		{"12", 0}, {struct{}{}, 0},
	}
	for _, tc := range cases {
		if got := intFromAny(tc.in); got != tc.want {
			t.Fatalf("intFromAny(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestAnnotateWithoutRecorderReportsFalse(t *testing.T) {
	if Annotate(context.Background(), Usage{TotalTokens: 1}) {
		t.Fatal("expected false when no recorder is attached")
	}
	if _, ok := RecorderFromContext(nil); ok {
		t.Fatal("expected no recorder for a nil context")
	}
}

func TestAnnotateMergesPartialUpdates(t *testing.T) {
	recorder := &Recorder{}
	ctx := WithRecorder(context.Background(), recorder)

	if !Annotate(ctx, Usage{Model: "deepseek-v4-pro", AccountID: "acc-1", CallerID: "caller-1"}) {
		t.Fatal("expected the first annotation to succeed")
	}
	// A later partial annotation must not clobber identity with empty strings.
	AnnotateUsage(ctx, "", "", "", map[string]any{
		"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5,
	})

	usage := recorder.snapshot()
	if usage.Model != "deepseek-v4-pro" || usage.AccountID != "acc-1" || usage.CallerID != "caller-1" {
		t.Fatalf("identity was clobbered: %+v", usage)
	}
	if usage.PromptTokens != 3 || usage.CompletionTokens != 2 || usage.TotalTokens != 5 {
		t.Fatalf("tokens = %+v, want 3/2/5", usage)
	}
}
