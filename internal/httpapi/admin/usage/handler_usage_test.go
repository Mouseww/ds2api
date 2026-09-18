package usage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ds2api/internal/usagestats"
)

func newTestStore(t *testing.T) *usagestats.Store {
	t.Helper()
	store := usagestats.NewStore("")
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close usage store: %v", err)
		}
	})
	return store
}

// recordOneRequest populates the store through the public middleware so the
// test does not depend on the internal Event field layout.
func recordOneRequest(t *testing.T, store *usagestats.Store) {
	t.Helper()
	handler := usagestats.Middleware(store)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
}

func TestGetUsageStatsWithoutStoreReturns503(t *testing.T) {
	handler := &Handler{}
	rec := httptest.NewRecorder()
	handler.getUsageStats(rec, httptest.NewRequest(http.MethodGet, "/admin/usage-stats", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestGetUsageStatsServesRequestedRange(t *testing.T) {
	store := newTestStore(t)
	recordOneRequest(t, store)

	handler := &Handler{UsageStats: store}
	rec := httptest.NewRecorder()
	handler.getUsageStats(rec, httptest.NewRequest(http.MethodGet, "/admin/usage-stats?range=7d", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}

	var snapshot struct {
		Range   string `json:"range"`
		Bucket  string `json:"bucket"`
		Summary struct {
			Requests int64 `json:"requests"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.Range != "7d" {
		t.Fatalf("range = %q, want %q", snapshot.Range, "7d")
	}
	if snapshot.Bucket == "" {
		t.Fatal("expected a non-empty bucket label")
	}
	if snapshot.Summary.Requests != 1 {
		t.Fatalf("summary requests = %d, want 1", snapshot.Summary.Requests)
	}
}

func TestGetUsageStatsFallsBackForUnknownRange(t *testing.T) {
	store := newTestStore(t)

	handler := &Handler{UsageStats: store}
	rec := httptest.NewRecorder()
	handler.getUsageStats(rec, httptest.NewRequest(http.MethodGet, "/admin/usage-stats?range=bogus", nil))

	var snapshot struct {
		Range string `json:"range"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.Range != usagestats.DefaultRange {
		t.Fatalf("range = %q, want %q", snapshot.Range, usagestats.DefaultRange)
	}
}

func TestResetUsageStatsClearsCounters(t *testing.T) {
	store := newTestStore(t)
	recordOneRequest(t, store)

	handler := &Handler{UsageStats: store}
	rec := httptest.NewRecorder()
	handler.resetUsageStats(rec, httptest.NewRequest(http.MethodDelete, "/admin/usage-stats", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := store.Query(usagestats.DefaultRange).Summary.Requests; got != 0 {
		t.Fatalf("requests after reset = %d, want 0", got)
	}
}

func TestResetUsageStatsWithoutStoreReturns503(t *testing.T) {
	handler := &Handler{}
	rec := httptest.NewRecorder()
	handler.resetUsageStats(rec, httptest.NewRequest(http.MethodDelete, "/admin/usage-stats", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
