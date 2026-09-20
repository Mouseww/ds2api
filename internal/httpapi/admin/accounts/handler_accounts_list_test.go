package accounts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ds2api/internal/usagestats"
)

func listAccountIdentifiers(t *testing.T, h *Handler, query string) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/accounts"+query, nil)
	rec := httptest.NewRecorder()
	h.listAccounts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			Identifier string `json:"identifier"`
			InPool     bool   `json:"in_pool"`
			Enabled    bool   `json:"enabled"`
			TestStatus string `json:"test_status"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	out := make([]string, 0, len(payload.Items))
	for _, item := range payload.Items {
		out = append(out, item.Identifier)
	}
	return out
}

func listAccountFlags(t *testing.T, h *Handler, query string) map[string]bool {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/accounts"+query, nil)
	rec := httptest.NewRecorder()
	h.listAccounts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			Identifier string `json:"identifier"`
			InPool     bool   `json:"in_pool"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	flags := map[string]bool{}
	for _, item := range payload.Items {
		flags[item.Identifier] = item.InPool
	}
	return flags
}

func assertIdentifiers(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("unexpected identifiers: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected identifiers: got %v want %v", got, want)
		}
	}
}

// The default sort partitions accounts that are enabled and in the active pool
// ahead of everything else while preserving the newest-first order inside each
// group.
func TestListAccountsSortsActivePoolFirst(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"runtime": {"active_pool_size": 2},
		"accounts":[
			{"email":"pool1@example.com","password":"p"},
			{"email":"pool2@example.com","password":"p"},
			{"email":"standby1@example.com","password":"p"},
			{"email":"standby2@example.com","password":"p"},
			{"email":"disabled@example.com","password":"p","enabled":false},
			{"email":"banned@example.com","password":"p","ban_is_muted":1}
		]
	}`)

	got := listAccountIdentifiers(t, h, "?page=1&page_size=10")
	assertIdentifiers(t, got, []string{
		"pool2@example.com",
		"pool1@example.com",
		"banned@example.com",
		"disabled@example.com",
		"standby2@example.com",
		"standby1@example.com",
	})

	flags := listAccountFlags(t, h, "?page=1&page_size=10")
	if !flags["pool1@example.com"] || !flags["pool2@example.com"] {
		t.Fatalf("expected pool members flagged in_pool, got %v", flags)
	}
	for _, id := range []string{"standby1@example.com", "standby2@example.com", "disabled@example.com", "banned@example.com"} {
		if flags[id] {
			t.Fatalf("expected %s not in pool, got %v", id, flags)
		}
	}
}

func TestListAccountsFilterByEnabled(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[
			{"email":"on@example.com","password":"p"},
			{"email":"off@example.com","password":"p","enabled":false}
		]
	}`)

	got := listAccountIdentifiers(t, h, "?page=1&page_size=10&enabled=false")
	assertIdentifiers(t, got, []string{"off@example.com"})

	got = listAccountIdentifiers(t, h, "?page=1&page_size=10&enabled=true")
	assertIdentifiers(t, got, []string{"on@example.com"})

	got = listAccountIdentifiers(t, h, "?page=1&page_size=10&enabled=all")
	assertIdentifiers(t, got, []string{"on@example.com", "off@example.com"})
}

func TestListAccountsFilterByTestStatus(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[
			{"email":"good@example.com","password":"p"},
			{"email":"bad@example.com","password":"p"},
			{"email":"fresh@example.com","password":"p"}
		]
	}`)
	if err := h.Store.UpdateAccountTestStatus("good@example.com", "ok"); err != nil {
		t.Fatalf("seed test status: %v", err)
	}
	if err := h.Store.UpdateAccountTestStatus("bad@example.com", "failed"); err != nil {
		t.Fatalf("seed test status: %v", err)
	}

	got := listAccountIdentifiers(t, h, "?page=1&page_size=10&test_status=ok")
	assertIdentifiers(t, got, []string{"good@example.com"})

	got = listAccountIdentifiers(t, h, "?page=1&page_size=10&test_status=failed")
	assertIdentifiers(t, got, []string{"bad@example.com"})

	got = listAccountIdentifiers(t, h, "?page=1&page_size=10&test_status=unknown")
	assertIdentifiers(t, got, []string{"fresh@example.com"})
}

func TestListAccountsFilterByBannedAndInPool(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"runtime": {"active_pool_size": 1},
		"accounts":[
			{"email":"active@example.com","password":"p"},
			{"email":"standby@example.com","password":"p"},
			{"email":"banned@example.com","password":"p","ban_is_muted":1}
		]
	}`)

	got := listAccountIdentifiers(t, h, "?page=1&page_size=10&banned=true")
	assertIdentifiers(t, got, []string{"banned@example.com"})

	got = listAccountIdentifiers(t, h, "?page=1&page_size=10&in_pool=true")
	assertIdentifiers(t, got, []string{"active@example.com"})

	got = listAccountIdentifiers(t, h, "?page=1&page_size=10&in_pool=false")
	assertIdentifiers(t, got, []string{"banned@example.com", "standby@example.com"})
}

func TestListAccountsFilterByProxyAndDailyLimit(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"runtime": {"daily_token_limit_m": 1},
		"accounts":[
			{"email":"proxied@example.com","password":"p","proxy_id":"proxy_1"},
			{"email":"bare@example.com","password":"p"}
		],
		"proxies":[{"id":"proxy_1","type":"socks5","host":"127.0.0.1","port":1080}]
	}`)
	h.UsageStats = usagestats.NewStore("")
	h.UsageStats.Record(usagestats.Event{
		Time:        time.Now(),
		Model:       "deepseek-v4-flash",
		AccountID:   "bare@example.com",
		TotalTokens: 2_000_000,
	})

	got := listAccountIdentifiers(t, h, "?page=1&page_size=10&has_proxy=true")
	assertIdentifiers(t, got, []string{"proxied@example.com"})

	got = listAccountIdentifiers(t, h, "?page=1&page_size=10&daily_limited=true")
	assertIdentifiers(t, got, []string{"bare@example.com"})
}

func TestListAccountsSortByUsage(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[
			{"email":"a@example.com","password":"p"},
			{"email":"b@example.com","password":"p"},
			{"email":"c@example.com","password":"p"}
		]
	}`)
	h.UsageStats = usagestats.NewStore("")
	for i, id := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		for range i + 1 {
			h.UsageStats.Record(usagestats.Event{
				Time:        time.Now(),
				Model:       "deepseek-v4-flash",
				AccountID:   id,
				TotalTokens: 100,
			})
		}
	}

	got := listAccountIdentifiers(t, h, "?page=1&page_size=10&sort=usage_tokens")
	assertIdentifiers(t, got, []string{"c@example.com", "b@example.com", "a@example.com"})

	got = listAccountIdentifiers(t, h, "?page=1&page_size=10&sort=usage_tokens&order=asc")
	assertIdentifiers(t, got, []string{"a@example.com", "b@example.com", "c@example.com"})

	got = listAccountIdentifiers(t, h, "?page=1&page_size=10&sort=usage_requests&order=asc")
	assertIdentifiers(t, got, []string{"a@example.com", "b@example.com", "c@example.com"})
}

func TestListAccountsSortByNameAndIdentifier(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[
			{"email":"zeta@example.com","password":"p","name":"Bob"},
			{"email":"alpha@example.com","password":"p","name":"Alice"},
			{"email":"mid@example.com","password":"p"}
		]
	}`)

	got := listAccountIdentifiers(t, h, "?page=1&page_size=10&sort=name")
	// Unnamed accounts fall back to their identifier when sorting by name.
	assertIdentifiers(t, got, []string{"alpha@example.com", "zeta@example.com", "mid@example.com"})

	got = listAccountIdentifiers(t, h, "?page=1&page_size=10&sort=identifier&order=desc")
	assertIdentifiers(t, got, []string{"zeta@example.com", "mid@example.com", "alpha@example.com"})
}

func TestListAccountsSortByTestStatus(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[
			{"email":"good@example.com","password":"p"},
			{"email":"bad@example.com","password":"p"},
			{"email":"fresh@example.com","password":"p"}
		]
	}`)
	if err := h.Store.UpdateAccountTestStatus("good@example.com", "ok"); err != nil {
		t.Fatalf("seed test status: %v", err)
	}
	if err := h.Store.UpdateAccountTestStatus("bad@example.com", "failed"); err != nil {
		t.Fatalf("seed test status: %v", err)
	}

	got := listAccountIdentifiers(t, h, "?page=1&page_size=10&sort=test_status")
	assertIdentifiers(t, got, []string{"good@example.com", "bad@example.com", "fresh@example.com"})
}

func TestListAccountsFilterCombinesWithSearch(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[
			{"email":"one@example.com","password":"p"},
			{"email":"two@example.com","password":"p","enabled":false},
			{"email":"three@example.com","password":"p","enabled":false}
		]
	}`)

	got := listAccountIdentifiers(t, h, "?page=1&page_size=10&q=two&enabled=false")
	assertIdentifiers(t, got, []string{"two@example.com"})
}

// The quota is counted over the configured rolling window, so usage that slid
// out of the window no longer limits the account.
func TestListAccountsQuotaWindowExcludesOldUsage(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"runtime": {"daily_request_limit": 2, "quota_window_hours": 1},
		"accounts":[
			{"email":"stale@example.com","password":"p"},
			{"email":"fresh@example.com","password":"p"}
		]
	}`)
	h.UsageStats = usagestats.NewStore("")
	now := time.Now()
	for range 2 {
		h.UsageStats.Record(usagestats.Event{
			Time:      now.Add(-2 * time.Hour),
			Model:     "deepseek-v4-flash",
			AccountID: "stale@example.com",
		})
	}
	for range 2 {
		h.UsageStats.Record(usagestats.Event{
			Time:      now.Add(-10 * time.Minute),
			Model:     "deepseek-v4-flash",
			AccountID: "fresh@example.com",
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/accounts?page=1&page_size=10", nil)
	rec := httptest.NewRecorder()
	h.listAccounts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		QuotaWindowHours int `json:"quota_window_hours"`
		Items            []struct {
			Identifier string `json:"identifier"`
			Requests   int    `json:"usage_today_requests"`
			Limited    bool   `json:"daily_limited"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if payload.QuotaWindowHours != 1 {
		t.Fatalf("expected quota_window_hours=1, got %d", payload.QuotaWindowHours)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(payload.Items))
	}
	for _, item := range payload.Items {
		switch item.Identifier {
		case "stale@example.com":
			if item.Requests != 0 || item.Limited {
				t.Fatalf("expected stale usage outside the window, got requests=%d limited=%v", item.Requests, item.Limited)
			}
		case "fresh@example.com":
			if item.Requests != 2 || !item.Limited {
				t.Fatalf("expected fresh usage inside the window, got requests=%d limited=%v", item.Requests, item.Limited)
			}
		default:
			t.Fatalf("unexpected item: %s", item.Identifier)
		}
	}
}
