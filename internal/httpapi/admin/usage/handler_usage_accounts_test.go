package usage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"ds2api/internal/usagestats"
)

// recordAccountRequest drives the public middleware so the test exercises the
// same attribution path production traffic takes.
func recordAccountRequest(t *testing.T, store *usagestats.Store, accountID string, status int) {
	t.Helper()
	handler := usagestats.Middleware(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		usagestats.Annotate(r.Context(), usagestats.Usage{
			Model:            "deepseek-v4-pro",
			AccountID:        accountID,
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		})
		w.WriteHeader(status)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
}

func accountUsageRouter(h *Handler) chi.Router {
	router := chi.NewRouter()
	router.Get("/admin/usage-stats/accounts", h.getAccountUsage)
	router.Get("/admin/usage-stats/accounts/{identifier}", h.getAccountUsageDetail)
	return router
}

func TestGetAccountUsageWithoutStoreReturns503(t *testing.T) {
	handler := &Handler{}
	rec := httptest.NewRecorder()
	handler.getAccountUsage(rec, httptest.NewRequest(http.MethodGet, "/admin/usage-stats/accounts", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestGetAccountUsageListsRankedAccounts(t *testing.T) {
	store := newTestStore(t)
	recordAccountRequest(t, store, "acc-a", http.StatusOK)
	recordAccountRequest(t, store, "acc-a", http.StatusOK)
	recordAccountRequest(t, store, "acc-b", http.StatusInternalServerError)

	handler := &Handler{UsageStats: store}
	rec := httptest.NewRecorder()
	handler.getAccountUsage(rec, httptest.NewRequest(http.MethodGet, "/admin/usage-stats/accounts?range=24h", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}

	var payload struct {
		Range   string `json:"range"`
		Bucket  string `json:"bucket"`
		Summary struct {
			Requests int64 `json:"requests"`
		} `json:"summary"`
		Attributed struct {
			Requests int64 `json:"requests"`
		} `json:"attributed"`
		Accounts []struct {
			AccountID   string  `json:"account_id"`
			Requests    int64   `json:"requests"`
			Errors      int64   `json:"errors"`
			TotalTokens int64   `json:"total_tokens"`
			SuccessRate float64 `json:"success_rate"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Range != "24h" || payload.Bucket == "" {
		t.Fatalf("range/bucket = %q/%q", payload.Range, payload.Bucket)
	}
	if payload.Summary.Requests != 3 || payload.Attributed.Requests != 3 {
		t.Fatalf("summary=%d attributed=%d, want 3/3", payload.Summary.Requests, payload.Attributed.Requests)
	}
	if len(payload.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(payload.Accounts))
	}
	if payload.Accounts[0].AccountID != "acc-a" || payload.Accounts[0].Requests != 2 {
		t.Fatalf("top account = %+v, want acc-a with 2 requests", payload.Accounts[0])
	}
	if payload.Accounts[1].AccountID != "acc-b" || payload.Accounts[1].Errors != 1 {
		t.Fatalf("second account = %+v, want acc-b with 1 error", payload.Accounts[1])
	}
	if payload.Accounts[1].SuccessRate != 0 {
		t.Fatalf("acc-b success rate = %v, want 0", payload.Accounts[1].SuccessRate)
	}
}

func TestGetAccountUsageDetailServesTrend(t *testing.T) {
	store := newTestStore(t)
	recordAccountRequest(t, store, "acc-a", http.StatusOK)

	handler := &Handler{UsageStats: store}
	rec := httptest.NewRecorder()
	accountUsageRouter(handler).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/admin/usage-stats/accounts/acc-a?range=1h", nil),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}

	var detail struct {
		Range   string `json:"range"`
		Bucket  string `json:"bucket"`
		Account struct {
			AccountID string `json:"account_id"`
			Requests  int64  `json:"requests"`
		} `json:"account"`
		Totals struct {
			Requests int64 `json:"requests"`
		} `json:"totals"`
		Series []struct {
			Requests int64 `json:"requests"`
		} `json:"series"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Range != "1h" || detail.Bucket != "1m" {
		t.Fatalf("range/bucket = %q/%q, want 1h/1m", detail.Range, detail.Bucket)
	}
	if detail.Account.AccountID != "acc-a" || detail.Account.Requests != 1 {
		t.Fatalf("account = %+v, want acc-a with 1 request", detail.Account)
	}
	if detail.Totals.Requests != 1 {
		t.Fatalf("totals requests = %d, want 1", detail.Totals.Requests)
	}
	var seriesRequests int64
	for _, point := range detail.Series {
		seriesRequests += point.Requests
	}
	if seriesRequests != 1 {
		t.Fatalf("series requests = %d, want 1", seriesRequests)
	}
}

func TestGetAccountUsageDetailUnknownReturns404(t *testing.T) {
	store := newTestStore(t)

	handler := &Handler{UsageStats: store}
	rec := httptest.NewRecorder()
	accountUsageRouter(handler).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/admin/usage-stats/accounts/missing", nil),
	)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestGetAccountUsageDetailWithoutStoreReturns503(t *testing.T) {
	handler := &Handler{}
	rec := httptest.NewRecorder()
	accountUsageRouter(handler).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/admin/usage-stats/accounts/acc-a", nil),
	)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// Account identifiers are email addresses or mobile numbers, so the path
// segment carries characters that must survive URL decoding intact.
func TestGetAccountUsageDetailDecodesEmailIdentifier(t *testing.T) {
	store := newTestStore(t)
	recordAccountRequest(t, store, "user@example.com", http.StatusOK)

	handler := &Handler{UsageStats: store}
	rec := httptest.NewRecorder()
	accountUsageRouter(handler).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/admin/usage-stats/accounts/user@example.com", nil),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var detail struct {
		Account struct {
			AccountID string `json:"account_id"`
		} `json:"account"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Account.AccountID != "user@example.com" {
		t.Fatalf("account_id = %q, want user@example.com", detail.Account.AccountID)
	}
}
