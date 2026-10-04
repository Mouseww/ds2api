package accounts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func postBatchImport(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Post("/admin/accounts/batch-import", h.batchImportAccounts)
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/batch-import", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func decodeBatchImportResponse(t *testing.T, rec *httptest.ResponseRecorder) batchImportResponse {
	t.Helper()
	var out batchImportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response not json: %v body=%s", err, rec.Body.String())
	}
	return out
}

func TestBatchImportCreatesNewAccountsWithCredentials(t *testing.T) {
	h := newAdminTestHandler(t, `{"accounts":[]}`)
	rec := postBatchImport(t, h, `{
		"accounts":[
			{
				"email":"weiwebber5+emptop8d2c40@gmail.com",
				"password":"lili1835",
				"token":"tok-A",
				"user_id":"uid-A",
				"device_id":"dev-A",
				"x_device_id":"uuid-A"
			},
			{
				"mobile":"13800138000",
				"password":"pwd2"
			}
		]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	out := decodeBatchImportResponse(t, rec)
	if !out.Success {
		t.Fatalf("expected success, got %+v", out)
	}
	if out.Created != 2 || out.Updated != 0 || out.Skipped != 0 || out.Errors != 0 {
		t.Fatalf("unexpected counts: %+v", out)
	}
	if out.TotalAccounts != 2 {
		t.Fatalf("total_accounts = %d, want 2", out.TotalAccounts)
	}

	accounts := h.Store.Snapshot().Accounts
	if len(accounts) != 2 {
		t.Fatalf("store has %d accounts, want 2", len(accounts))
	}
	a := accounts[0]
	if a.Token != "tok-A" || a.UserID != "uid-A" || a.DeviceID != "dev-A" || a.DeviceUUID != "uuid-A" {
		t.Fatalf("credentials not preserved: %+v", a)
	}
	if a.Email != "weiwebber5+emptop8d2c40@gmail.com" {
		t.Fatalf("email not preserved: %q", a.Email)
	}
}

func TestBatchImportSkipModeLeavesExistingUntouched(t *testing.T) {
	// NOTE: env-load strips tokens (ClearAccountTokens at store.go:67), so the
	// "original" account loaded from env has Token="". What skip mode must
	// preserve is the original *stored* state — here, Password="old" — and it
	// must NOT apply the new token/password from the import payload.
	h := newAdminTestHandler(t, `{"accounts":[{"email":"u@example.com","password":"old","token":"tok-old"}]}`)
	rec := postBatchImport(t, h, `{
		"mode":"skip",
		"accounts":[
			{"email":"u@example.com","password":"new","token":"tok-new"},
			{"email":"v@example.com","password":"pwd","token":"tok-v"}
		]
	}`)
	out := decodeBatchImportResponse(t, rec)
	if out.Created != 1 || out.Skipped != 1 || out.Updated != 0 || out.Errors != 0 {
		t.Fatalf("unexpected counts: %+v", out)
	}
	accounts := h.Store.Snapshot().Accounts
	if len(accounts) != 2 {
		t.Fatalf("store has %d accounts, want 2", len(accounts))
	}
	// Original account: must NOT have picked up the new password/token from import.
	if accounts[0].Password != "old" {
		t.Fatalf("skip mode applied new password: %+v", accounts[0])
	}
	if accounts[0].Token == "tok-new" {
		t.Fatalf("skip mode applied new token: %+v", accounts[0])
	}
	// New one added with credentials intact (import path does NOT strip).
	if accounts[1].Email != "v@example.com" || accounts[1].Token != "tok-v" {
		t.Fatalf("new account not added: %+v", accounts[1])
	}
}

func TestBatchImportOverwriteModeReplacesCredentialsButPreservesRuntime(t *testing.T) {
	h := newAdminTestHandler(t, `{"accounts":[{
		"email":"u@example.com",
		"password":"old",
		"token":"tok-old",
		"disabled_reason":"banned",
		"ban_is_muted":1
	}]}`)
	rec := postBatchImport(t, h, `{
		"mode":"overwrite",
		"accounts":[
			{"email":"u@example.com","password":"new","token":"tok-new","device_id":"dev-new"}
		]
	}`)
	out := decodeBatchImportResponse(t, rec)
	if out.Updated != 1 || out.Created != 0 || out.Errors != 0 {
		t.Fatalf("unexpected counts: %+v", out)
	}
	a := h.Store.Snapshot().Accounts[0]
	if a.Token != "tok-new" || a.Password != "new" || a.DeviceID != "dev-new" {
		t.Fatalf("overwrite did not replace credentials: %+v", a)
	}
	// Runtime state preserved
	if a.DisabledReason != "banned" || a.BanIsMuted != 1 {
		t.Fatalf("overwrite clobbered runtime flags: %+v", a)
	}
}

func TestBatchImportErrorModeReportsCollision(t *testing.T) {
	h := newAdminTestHandler(t, `{"accounts":[{"email":"u@example.com","password":"old"}]}`)
	rec := postBatchImport(t, h, `{
		"mode":"error",
		"accounts":[
			{"email":"u@example.com","password":"new"},
			{"email":"v@example.com","password":"pwd"}
		]
	}`)
	out := decodeBatchImportResponse(t, rec)
	if out.Success {
		t.Fatalf("expected success=false, got %+v", out)
	}
	if out.Created != 1 || out.Errors != 1 {
		t.Fatalf("unexpected counts: %+v", out)
	}
	// Existing untouched
	a := h.Store.Snapshot().Accounts[0]
	if a.Password != "old" {
		t.Fatalf("error mode mutated existing: %+v", a)
	}
}

func TestBatchImportValidationErrors(t *testing.T) {
	h := newAdminTestHandler(t, `{"accounts":[]}`)

	// Missing identifier
	rec := postBatchImport(t, h, `{"accounts":[{"password":"pwd"}]}`)
	out := decodeBatchImportResponse(t, rec)
	if out.Errors != 1 || out.Created != 0 {
		t.Fatalf("expected 1 error, got %+v", out)
	}
	if out.Results[0].Reason == "" {
		t.Fatalf("expected reason for missing identifier: %+v", out.Results[0])
	}

	// Empty accounts
	rec = postBatchImport(t, h, `{"accounts":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty accounts, got %d", rec.Code)
	}

	// Invalid mode
	rec = postBatchImport(t, h, `{"mode":"bad","accounts":[{"email":"a@b.com"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid mode, got %d", rec.Code)
	}

	// Invalid JSON
	rec = postBatchImport(t, h, `{not-json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", rec.Code)
	}
}

func TestBatchImportProxyIDMustExist(t *testing.T) {
	h := newAdminTestHandler(t, `{"accounts":[],"proxies":[{"id":"p1","type":"socks5","host":"127.0.0.1","port":8080}]}`)
	rec := postBatchImport(t, h, `{
		"accounts":[
			{"email":"a@b.com","password":"pwd","proxy_id":"p1"},
			{"email":"c@d.com","password":"pwd","proxy_id":"no-such"}
		]
	}`)
	out := decodeBatchImportResponse(t, rec)
	if out.Created != 1 || out.Errors != 1 {
		t.Fatalf("unexpected counts: %+v", out)
	}
	accounts := h.Store.Snapshot().Accounts
	if len(accounts) != 1 || accounts[0].ProxyID != "p1" {
		t.Fatalf("proxy attach failed: %+v", accounts)
	}
}

func TestBatchImportWithinPayloadDedupe(t *testing.T) {
	h := newAdminTestHandler(t, `{"accounts":[]}`)
	rec := postBatchImport(t, h, `{
		"accounts":[
			{"email":"u@example.com","password":"pwd1"},
			{"email":"u@example.com","password":"pwd2"}
		]
	}`)
	out := decodeBatchImportResponse(t, rec)
	// First creates, second hits the dedupe key and (default skip mode) skips.
	if out.Created != 1 || out.Skipped != 1 {
		t.Fatalf("unexpected counts: %+v", out)
	}
	if got := len(h.Store.Snapshot().Accounts); got != 1 {
		t.Fatalf("store has %d accounts, want 1", got)
	}
}

func TestBatchImportPreservesCredentialsEndToEnd(t *testing.T) {
	// Regression: user's real payload shape from issue.
	h := newAdminTestHandler(t, `{"accounts":[]}`)
	rec := postBatchImport(t, h, `{
		"accounts":[{
			"email":"weiwebber5+emptop8d2c40@gmail.com",
			"password":"lili1835",
			"token":"R3dc+TP1l5vV5ixP4vwO/KnREioKuORNpW18ZPkLl9hkBGmCzpCd/UvIzZYUOvEt",
			"user_id":"9c7377d8-90a0-44b8-800a-9a4879ac09fc",
			"device_id":"BRfj6WYam+tTv9P138GtP7X2yRJLiSHuJ1oZCeNudcP4qPZnC5AOVzZXEhjmG5FkF8EAdKA2OJM8o2kiqGtZWpA==",
			"x_device_id":"2ffda7a9-9006-4dbe-ac49-f49825c47726"
		}]
	}`)
	out := decodeBatchImportResponse(t, rec)
	if !out.Success || out.Created != 1 {
		t.Fatalf("expected created=1 success, got %+v", out)
	}
	a := h.Store.Snapshot().Accounts[0]
	if a.Token != "R3dc+TP1l5vV5ixP4vwO/KnREioKuORNpW18ZPkLl9hkBGmCzpCd/UvIzZYUOvEt" {
		t.Fatalf("token mismatch: %q", a.Token)
	}
	if a.UserID != "9c7377d8-90a0-44b8-800a-9a4879ac09fc" {
		t.Fatalf("user_id mismatch: %q", a.UserID)
	}
	if a.DeviceID != "BRfj6WYam+tTv9P138GtP7X2yRJLiSHuJ1oZCeNudcP4qPZnC5AOVzZXEhjmG5FkF8EAdKA2OJM8o2kiqGtZWpA==" {
		t.Fatalf("device_id mismatch: %q", a.DeviceID)
	}
	if a.DeviceUUID != "2ffda7a9-9006-4dbe-ac49-f49825c47726" {
		t.Fatalf("x_device_id mismatch: %q", a.DeviceUUID)
	}
}
