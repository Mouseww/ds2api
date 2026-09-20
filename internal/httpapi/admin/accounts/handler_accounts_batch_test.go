package accounts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func callBatchEndpoint(t *testing.T, h *Handler, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := chi.NewRouter()
	RegisterRoutes(r, h)
	req := adminReq(http.MethodPost, path, []byte(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	payload := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response failed: %v (body=%s)", err, rec.Body.String())
		}
	}
	return rec, payload
}

func TestBatchDeleteAccountsRemovesListedAccounts(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[
			{"email":"keep@example.com","password":"p"},
			{"email":"drop1@example.com","password":"p"},
			{"email":"drop2@example.com","mobile":"13800138000","password":"p"}
		]
	}`)

	rec, payload := callBatchEndpoint(t, h, "/accounts/batch-delete",
		`{"identifiers":["drop1@example.com","13800138000","ghost@example.com"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if deleted, _ := payload["deleted"].(float64); deleted != 2 {
		t.Fatalf("expected 2 deleted, got %v", payload["deleted"])
	}
	missing, _ := payload["missing"].([]any)
	if len(missing) != 1 || missing[0] != "ghost@example.com" {
		t.Fatalf("expected ghost reported missing, got %v", payload["missing"])
	}
	accounts := h.Store.Accounts()
	if len(accounts) != 1 || accounts[0].Email != "keep@example.com" {
		t.Fatalf("unexpected remaining accounts: %#v", accounts)
	}
}

func TestBatchDeleteAccountsRejectsEmptyList(t *testing.T) {
	h := newAdminTestHandler(t, `{"accounts":[{"email":"a@example.com","password":"p"}]}`)

	rec, _ := callBatchEndpoint(t, h, "/accounts/batch-delete", `{"identifiers":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty batch, got %d", rec.Code)
	}

	rec, _ = callBatchEndpoint(t, h, "/accounts/batch-delete", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid json, got %d", rec.Code)
	}
}

func TestBatchUpdateStatusTogglesAccounts(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[
			{"email":"one@example.com","password":"p"},
			{"email":"two@example.com","password":"p","enabled":false,"disabled_reason":"manual"},
			{"email":"three@example.com","password":"p"}
		]
	}`)

	rec, payload := callBatchEndpoint(t, h, "/accounts/batch-status",
		`{"identifiers":["one@example.com","two@example.com","missing@example.com"],"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if updated, _ := payload["updated"].(float64); updated != 2 {
		t.Fatalf("expected 2 updated, got %v", payload["updated"])
	}
	for _, acc := range h.Store.Accounts() {
		if acc.Email == "three@example.com" && !acc.IsEnabled() {
			t.Fatalf("three should stay enabled: %#v", acc)
		}
		if (acc.Email == "one@example.com" || acc.Email == "two@example.com") && acc.IsEnabled() {
			t.Fatalf("expected disabled after batch: %#v", acc)
		}
		if (acc.Email == "one@example.com" || acc.Email == "two@example.com") && acc.DisabledReason != "manual" {
			t.Fatalf("expected manual disabled reason: %#v", acc)
		}
	}

	rec, payload = callBatchEndpoint(t, h, "/accounts/batch-status",
		`{"identifiers":["one@example.com","two@example.com"],"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if updated, _ := payload["updated"].(float64); updated != 2 {
		t.Fatalf("expected 2 re-enabled, got %v", payload["updated"])
	}
	for _, acc := range h.Store.Accounts() {
		if !acc.IsEnabled() || acc.DisabledReason != "" {
			t.Fatalf("expected all enabled without reason: %#v", acc)
		}
	}
}

func TestBatchUpdateStatusRequiresEnabledField(t *testing.T) {
	h := newAdminTestHandler(t, `{"accounts":[{"email":"a@example.com","password":"p"}]}`)

	rec, _ := callBatchEndpoint(t, h, "/accounts/batch-status", `{"identifiers":["a@example.com"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without enabled field, got %d", rec.Code)
	}
}

func TestBatchUpdateProxyBindsAndUnbinds(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[
			{"email":"one@example.com","password":"p"},
			{"email":"two@example.com","password":"p"}
		],
		"proxies":[{"id":"proxy_1","type":"socks5","host":"127.0.0.1","port":1080}]
	}`)

	rec, payload := callBatchEndpoint(t, h, "/accounts/batch-proxy",
		`{"identifiers":["one@example.com","two@example.com"],"proxy_id":"proxy_1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if updated, _ := payload["updated"].(float64); updated != 2 {
		t.Fatalf("expected 2 updated, got %v", payload["updated"])
	}
	for _, acc := range h.Store.Accounts() {
		if acc.ProxyID != "proxy_1" {
			t.Fatalf("expected proxy bound: %#v", acc)
		}
	}

	rec, _ = callBatchEndpoint(t, h, "/accounts/batch-proxy",
		`{"identifiers":["one@example.com","two@example.com"],"proxy_id":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	for _, acc := range h.Store.Accounts() {
		if acc.ProxyID != "" {
			t.Fatalf("expected proxy unbound: %#v", acc)
		}
	}
}

func TestBatchUpdateProxyRejectsUnknownProxy(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[{"email":"one@example.com","password":"p"}]
	}`)

	rec, payload := callBatchEndpoint(t, h, "/accounts/batch-proxy",
		`{"identifiers":["one@example.com"],"proxy_id":"ghost"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown proxy, got %d", rec.Code)
	}
	if detail, _ := payload["detail"].(string); !strings.Contains(detail, "代理不存在") {
		t.Fatalf("expected proxy-missing detail, got %v", payload["detail"])
	}
	if acc := h.Store.Accounts()[0]; acc.ProxyID != "" {
		t.Fatalf("expected no partial mutation, got %#v", acc)
	}
}
