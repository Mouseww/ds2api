package usage

import (
	"net/http"
	"strings"
)

// getUsageStats returns the aggregated dashboard snapshot for one range.
//
// Query parameters:
//
//	range=1h|24h|7d|30d|90d|1y  (default 24h; unknown values fall back)
func (h *Handler) getUsageStats(w http.ResponseWriter, r *http.Request) {
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
	// Usage numbers change on every request, so never let a proxy cache them.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, store.Query(rangeName))
}

// resetUsageStats clears every counter and restarts the tracking window.
func (h *Handler) resetUsageStats(w http.ResponseWriter, _ *http.Request) {
	store := h.UsageStats
	if store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": "usage statistics store is not configured",
		})
		return
	}
	if err := store.Reset(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
