package usage

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
)

// getAccountUsage returns the ranked per-account usage for one range.
//
// Query parameters:
//
//	range=1h|24h|7d|30d|90d|1y  (default 24h; unknown values fall back)
//
// Only requests that reached a concrete pooled account are listed; traffic that
// failed before account acquisition shows up in summary (all traffic) minus
// attributed (account traffic) instead of being blamed on a real account.
func (h *Handler) getAccountUsage(w http.ResponseWriter, r *http.Request) {
	store := h.UsageStats
	if store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": "usage statistics store is not configured",
		})
		return
	}
	rangeName := ""
	if r != nil && r.URL != nil {
		rangeName = strings.TrimSpace(r.URL.Query().Get("range"))
	}
	// Per-account counters move on every request, so never let a proxy cache them.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, store.QueryAccounts(rangeName))
}

// getAccountUsageDetail returns one account's range-scoped trend, token split
// and per-model / per-surface breakdown.
func (h *Handler) getAccountUsageDetail(w http.ResponseWriter, r *http.Request) {
	store := h.UsageStats
	if store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": "usage statistics store is not configured",
		})
		return
	}
	identifier := chi.URLParam(r, "identifier")
	if decoded, err := url.PathUnescape(identifier); err == nil {
		identifier = decoded
	}
	rangeName := ""
	if r != nil && r.URL != nil {
		rangeName = strings.TrimSpace(r.URL.Query().Get("range"))
	}
	detail, ok := store.QueryAccount(rangeName, identifier)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"detail": "no usage recorded for this account in the retained window",
		})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, detail)
}
