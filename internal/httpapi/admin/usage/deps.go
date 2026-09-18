package usage

import (
	adminshared "ds2api/internal/httpapi/admin/shared"
	"ds2api/internal/usagestats"
)

// Handler exposes the aggregated API usage statistics that back the admin
// dashboard. The store is owned by the server, so a nil store means the feature
// is not configured and every route answers 503.
type Handler struct {
	UsageStats *usagestats.Store
}

var writeJSON = adminshared.WriteJSON
