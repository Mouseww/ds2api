package accounts

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"ds2api/internal/config"
	"ds2api/internal/usagestats"
)

// accountListItem couples a config account with the derived fields the list
// needs for filtering and sorting, so each derivation runs once per account.
type accountListItem struct {
	acc          config.Account
	identifier   string
	testStatus   string
	usage        usagestats.AccountUsage
	window       usagestats.AccountUsage
	inPool       bool
	dailyLimited bool
}

// triStateQuery reads a filter param that accepts "all" (default), "true" or
// "false" and reports the requested value. The second return is false when the
// filter is not active.
func triStateQuery(r *http.Request, key string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key))) {
	case "true", "1", "yes":
		return true, true
	case "false", "0", "no":
		return false, true
	}
	return false, false
}

// normalizeTestStatusFilter maps the test_status filter param to the stored
// status value. Empty (or "all") disables the filter; "unknown" selects
// accounts without a recorded test status.
func normalizeTestStatusFilter(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "all":
		return ""
	case "ok", "failed":
		return strings.ToLower(strings.TrimSpace(v))
	case "unknown", "none", "untested":
		return "unknown"
	}
	return ""
}

// activePoolMembers returns the identifiers currently in the pool's active
// queue. Accounts there are enabled and eligible for allocation.
func (h *Handler) activePoolMembers() map[string]bool {
	out := map[string]bool{}
	if h.Pool == nil {
		return out
	}
	status := h.Pool.Status()
	if status == nil {
		return out
	}
	members, ok := status["active_pool_accounts"].([]string)
	if !ok {
		return out
	}
	for _, id := range members {
		if id != "" {
			out[id] = true
		}
	}
	return out
}

func accountMatchesSearch(acc config.Account, q string) bool {
	return strings.Contains(strings.ToLower(acc.Identifier()), q) ||
		strings.Contains(strings.ToLower(acc.Name), q) ||
		strings.Contains(strings.ToLower(acc.Remark), q) ||
		strings.Contains(strings.ToLower(acc.Email), q) ||
		strings.Contains(strings.ToLower(acc.Mobile), q)
}

// testStatusRank orders test statuses for sorting: ok first, then failed,
// then unknown.
func testStatusRank(status string) int {
	switch status {
	case "ok":
		return 3
	case "failed":
		return 2
	}
	return 1
}

func (h *Handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	page := intFromQuery(r, "page", 1)
	pageSize := intFromQuery(r, "page_size", 10)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 1
	}
	if pageSize > 5000 {
		pageSize = 5000
	}
	snap := h.Store.Snapshot()

	quotaWindowHours := h.Store.RuntimeQuotaWindowHours()
	quotaWindow := time.Duration(quotaWindowHours) * time.Hour
	usageByAccount := map[string]usagestats.AccountUsage{}
	windowByAccount := map[string]usagestats.AccountUsage{}
	if h.UsageStats != nil {
		usageByAccount = h.UsageStats.AllAccountUsage()
		windowByAccount = h.UsageStats.AccountWindowUsage(quotaWindow)
	}
	// The global per-account quota is enforced by the pool over the configured
	// rolling window; the list only reports it so an operator can see which
	// accounts are near their budget.
	tokenLimit := snap.Runtime.DailyTokenLimit()
	requestLimit := int64(snap.Runtime.DailyRequestLimit)
	activePool := h.activePoolMembers()

	enabledFilter, filterByEnabled := triStateQuery(r, "enabled")
	testStatusFilter := normalizeTestStatusFilter(r.URL.Query().Get("test_status"))
	bannedFilter, filterByBanned := triStateQuery(r, "banned")
	dailyLimitedFilter, filterByDailyLimited := triStateQuery(r, "daily_limited")
	hasProxyFilter, filterByHasProxy := triStateQuery(r, "has_proxy")
	inPoolFilter, filterByInPool := triStateQuery(r, "in_pool")
	q := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("q")))

	// Accounts are listed newest-first; the default sort additionally moves
	// accounts that are enabled and in the active pool to the top.
	reversed := make([]config.Account, len(snap.Accounts))
	copy(reversed, snap.Accounts)
	reverseAccounts(reversed)

	items := make([]accountListItem, 0, len(reversed))
	for _, acc := range reversed {
		id := acc.Identifier()
		testStatus, _ := h.Store.AccountTestStatus(id)
		usage := usageByAccount[id]
		windowUsage := windowByAccount[id]
		entry := accountListItem{
			acc:          acc,
			identifier:   id,
			testStatus:   testStatus,
			usage:        usage,
			window:       windowUsage,
			inPool:       activePool[id],
			dailyLimited: dailyLimitReached(windowUsage, tokenLimit, requestLimit),
		}
		if q != "" && !accountMatchesSearch(acc, q) {
			continue
		}
		if filterByEnabled && acc.IsEnabled() != enabledFilter {
			continue
		}
		if testStatusFilter != "" {
			status := entry.testStatus
			if status == "" {
				status = "unknown"
			}
			if status != testStatusFilter {
				continue
			}
		}
		if filterByBanned && acc.IsBanned() != bannedFilter {
			continue
		}
		if filterByDailyLimited && entry.dailyLimited != dailyLimitedFilter {
			continue
		}
		if filterByHasProxy && (acc.ProxyID != "") != hasProxyFilter {
			continue
		}
		if filterByInPool && entry.inPool != inPoolFilter {
			continue
		}
		items = append(items, entry)
	}

	sortKey := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort")))
	sortOrder := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("order")))
	sortAccountListItems(items, sortKey, sortOrder)

	total := len(items)
	totalPages := 1
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	rendered := make([]map[string]any, 0, end-start)
	for _, entry := range items[start:end] {
		acc := entry.acc
		token := strings.TrimSpace(acc.Token)
		rendered = append(rendered, map[string]any{
			"identifier":              acc.Identifier(),
			"name":                    acc.Name,
			"remark":                  acc.Remark,
			"email":                   acc.Email,
			"mobile":                  acc.Mobile,
			"proxy_id":                acc.ProxyID,
			"has_password":            acc.Password != "",
			"has_token":               token != "",
			"token_preview":           maskSecretPreview(token),
			"test_status":             entry.testStatus,
			"enabled":                 acc.IsEnabled(),
			"disabled_reason":         acc.DisabledReason,
			"ban_is_muted":            acc.BanIsMuted,
			"ban_mute_until":          acc.BanMuteUntil,
			"ban_status":              acc.BanStatus,
			"in_pool":                 entry.inPool,
			"usage_requests":          entry.usage.Requests,
			"usage_prompt_tokens":     entry.usage.PromptTokens,
			"usage_completion_tokens": entry.usage.CompletionTokens,
			"usage_total_tokens":      entry.usage.TotalTokens,
			// The usage_today_* names are kept for API compatibility; since the
			// quota window became configurable they carry the rolling-window
			// usage rather than the calendar-day usage.
			"usage_today_requests": entry.window.Requests,
			"usage_today_tokens":   entry.window.TotalTokens,
			"daily_token_limit":    tokenLimit,
			"daily_request_limit":  requestLimit,
			"daily_limited":        entry.dailyLimited,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":              rendered,
		"total":              total,
		"page":               page,
		"page_size":          pageSize,
		"total_pages":        totalPages,
		"quota_window_hours": quotaWindowHours,
	})
}

// defaultOrderForSortKey picks the natural direction for a sort key when the
// caller does not pass an explicit order.
func defaultOrderForSortKey(key string) string {
	switch key {
	case "name", "identifier":
		return "asc"
	default:
		return "desc"
	}
}

// sortAccountListItems orders items for the response. The default key (and
// any unrecognized key) keeps the newest-first order but partitions accounts
// that are enabled and currently in the active pool ahead of the rest.
// Custom keys sort by the requested field; "asc"/"desc" overrides the per-key
// default direction.
func sortAccountListItems(items []accountListItem, key, order string) {
	if order != "asc" && order != "desc" {
		order = defaultOrderForSortKey(key)
	}
	desc := order == "desc"
	switch key {
	case "usage_tokens":
		sortByInt64(items, desc, func(it accountListItem) int64 { return it.usage.TotalTokens })
	case "usage_requests":
		sortByInt64(items, desc, func(it accountListItem) int64 { return it.usage.Requests })
	case "usage_today_tokens":
		sortByInt64(items, desc, func(it accountListItem) int64 { return it.window.TotalTokens })
	case "usage_today_requests":
		sortByInt64(items, desc, func(it accountListItem) int64 { return it.window.Requests })
	case "test_status":
		sortByInt64(items, desc, func(it accountListItem) int64 { return int64(testStatusRank(it.testStatus)) })
	case "enabled":
		sortByInt64(items, desc, func(it accountListItem) int64 { return boolRank(it.acc.IsEnabled()) })
	case "name":
		sortByString(items, desc, func(it accountListItem) string {
			if name := strings.TrimSpace(it.acc.Name); name != "" {
				return name
			}
			return it.identifier
		})
	case "identifier":
		sortByString(items, desc, func(it accountListItem) string { return it.identifier })
	default:
		// Pool membership already implies the account is enabled.
		sort.SliceStable(items, func(i, j int) bool {
			return items[i].inPool && !items[j].inPool
		})
	}
}

func boolRank(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func sortByInt64(items []accountListItem, desc bool, value func(accountListItem) int64) {
	sort.SliceStable(items, func(i, j int) bool {
		vi, vj := value(items[i]), value(items[j])
		if vi == vj {
			return false
		}
		if desc {
			return vi > vj
		}
		return vi < vj
	})
}

func sortByString(items []accountListItem, desc bool, value func(accountListItem) string) {
	sort.SliceStable(items, func(i, j int) bool {
		vi, vj := value(items[i]), value(items[j])
		if vi == vj {
			return false
		}
		if desc {
			return vi > vj
		}
		return vi < vj
	})
}
