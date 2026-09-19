package usage

import "github.com/go-chi/chi/v5"

// RegisterRoutes mounts the usage statistics endpoints under the admin group.
// Both routes require admin authentication, which the caller applies.
func RegisterRoutes(r chi.Router, h *Handler) {
	r.Get("/usage-stats", h.getUsageStats)
	r.Delete("/usage-stats", h.resetUsageStats)
	r.Get("/usage-stats/accounts", h.getAccountUsage)
	r.Get("/usage-stats/accounts/{identifier}", h.getAccountUsageDetail)
}
