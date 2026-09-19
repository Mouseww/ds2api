package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const adminShellMarker = "DS2API-ADMIN-SHELL-MARKER"

// TestAdminDocumentNavigationServesShellWithoutBreakingAPI pins the behaviour
// behind "the session looks expired after refreshing on /admin".
//
// A hard refresh is a document navigation: the browser requests
// /admin/accounts with Accept: text/html and Sec-Fetch-Mode: navigate, and it
// cannot attach an Authorization header. Those paths are also Admin API routes
// (GET /admin/accounts), so the request used to reach the authenticated handler
// and answer 401 JSON even though the stored token was still valid. The router
// now serves the SPA shell for such navigations while every genuine API call
// keeps hitting the authenticated handler.
func TestAdminDocumentNavigationServesShellWithoutBreakingAPI(t *testing.T) {
	staticDir := t.TempDir()
	shell := "<!doctype html><html><head><title>DS2API</title></head><body><div id=\"root\">" +
		adminShellMarker + "</div></body></html>"
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte(shell), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}

	t.Setenv("DS2API_STATIC_ADMIN_DIR", staticDir)
	t.Setenv("DS2API_CONFIG_JSON", `{"keys":["k1"],"accounts":[{"email":"u@example.com","password":"p"}]}`)
	t.Setenv("DS2API_ENV_WRITEBACK", "0")

	app, err := NewApp()
	if err != nil {
		t.Fatalf("NewApp() error: %v", err)
	}

	const browserAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

	cases := []struct {
		name       string
		method     string
		target     string
		headers    map[string]string
		wantStatus int
		wantShell  bool
	}{
		{
			// GET /admin/accounts is both the SPA "accounts" tab and an Admin
			// API route: the refresh must render the panel, not 401.
			name:   "refresh on accounts tab serves the SPA shell",
			method: http.MethodGet,
			target: "/admin/accounts",
			headers: map[string]string{
				"Accept":         browserAccept,
				"Sec-Fetch-Mode": "navigate",
				"Sec-Fetch-Dest": "document",
			},
			wantStatus: http.StatusOK,
			wantShell:  true,
		},
		{
			name:   "deep link on settings tab serves the SPA shell",
			method: http.MethodGet,
			target: "/admin/settings",
			headers: map[string]string{
				"Accept":         "text/html,application/xhtml+xml",
				"Sec-Fetch-Mode": "navigate",
			},
			wantStatus: http.StatusOK,
			wantShell:  true,
		},
		{
			// The import tab has no GET API route at all, yet it is still a
			// SPA route the user can refresh on.
			name:   "refresh on post-only import tab serves the SPA shell",
			method: http.MethodGet,
			target: "/admin/import",
			headers: map[string]string{
				"Accept":         browserAccept,
				"Sec-Fetch-Mode": "navigate",
			},
			wantStatus: http.StatusOK,
			wantShell:  true,
		},
		{
			// Browsers predating Fetch Metadata send only Accept.
			name:   "legacy browser refresh serves the SPA shell",
			method: http.MethodGet,
			target: "/admin/proxies",
			headers: map[string]string{
				"Accept": browserAccept,
			},
			wantStatus: http.StatusOK,
			wantShell:  true,
		},
		{
			name:       "admin API call still requires auth",
			method:     http.MethodGet,
			target:     "/admin/accounts",
			headers:    map[string]string{"Accept": "application/json"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "plain api probe still requires auth",
			method:     http.MethodGet,
			target:     "/admin/config",
			headers:    map[string]string{"Accept": "*/*"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			// A document navigation cannot carry credentials; if credentials
			// are present the request is an API call and must be authorized.
			name:   "document headers with credentials still require auth",
			method: http.MethodGet,
			target: "/admin/accounts",
			headers: map[string]string{
				"Accept":         browserAccept,
				"Sec-Fetch-Mode": "navigate",
				"Authorization":  "Bearer not-a-real-token",
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:   "write method is never served the shell",
			method: http.MethodPost,
			target: "/admin/import",
			headers: map[string]string{
				"Accept":         browserAccept,
				"Sec-Fetch-Mode": "navigate",
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:   "missing static asset is not swallowed by the shell",
			method: http.MethodGet,
			target: "/admin/assets/index-doesnotexist.js",
			headers: map[string]string{
				"Accept":         "*/*",
				"Sec-Fetch-Mode": "no-cors",
			},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			for key, value := range tc.headers {
				req.Header.Set(key, value)
			}
			rec := httptest.NewRecorder()
			app.Router.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("%s %s status = %d, want %d (body=%s)",
					tc.method, tc.target, rec.Code, tc.wantStatus, rec.Body.String())
			}
			hasShell := strings.Contains(rec.Body.String(), adminShellMarker)
			if hasShell != tc.wantShell {
				t.Fatalf("%s %s shell marker present = %v, want %v (body=%s)",
					tc.method, tc.target, hasShell, tc.wantShell, rec.Body.String())
			}
		})
	}
}
