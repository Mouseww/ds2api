package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testShellMarker = "DS2API-SPA-SHELL-MARKER"

func TestIsAdminDocumentRequest(t *testing.T) {
	const browserAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"

	cases := []struct {
		name    string
		method  string
		target  string
		headers map[string]string
		want    bool
	}{
		{
			name:   "chrome refresh on accounts tab",
			method: http.MethodGet,
			target: "/admin/accounts",
			headers: map[string]string{
				"Accept":         browserAccept,
				"Sec-Fetch-Mode": "navigate",
				"Sec-Fetch-Dest": "document",
			},
			want: true,
		},
		{
			name:    "refresh on post-only test tab",
			method:  http.MethodGet,
			target:  "/admin/test",
			headers: map[string]string{"Accept": browserAccept, "Sec-Fetch-Mode": "navigate"},
			want:    true,
		},
		{
			name:    "refresh on bare admin root",
			method:  http.MethodGet,
			target:  "/admin",
			headers: map[string]string{"Accept": browserAccept, "Sec-Fetch-Mode": "navigate"},
			want:    true,
		},
		{
			name:    "legacy browser without fetch metadata",
			method:  http.MethodGet,
			target:  "/admin/settings",
			headers: map[string]string{"Accept": browserAccept},
			want:    true,
		},
		{
			name:   "spa fetch carrying bearer token",
			method: http.MethodGet,
			target: "/admin/accounts",
			headers: map[string]string{
				"Accept":         "*/*",
				"Sec-Fetch-Mode": "cors",
				"Authorization":  "Bearer token",
			},
			want: false,
		},
		{
			name:   "document headers but explicit credentials",
			method: http.MethodGet,
			target: "/admin/accounts",
			headers: map[string]string{
				"Accept":         browserAccept,
				"Sec-Fetch-Mode": "navigate",
				"Authorization":  "Bearer token",
			},
			want: false,
		},
		{
			name:    "plain api probe",
			method:  http.MethodGet,
			target:  "/admin/accounts",
			headers: map[string]string{"Accept": "*/*"},
			want:    false,
		},
		{
			name:    "script asset",
			method:  http.MethodGet,
			target:  "/admin/assets/index-BOaT4Ua0.js",
			headers: map[string]string{"Accept": "*/*", "Sec-Fetch-Mode": "no-cors"},
			want:    false,
		},
		{
			name:    "stylesheet asset",
			method:  http.MethodGet,
			target:  "/admin/assets/index-B7SDuWR_.css",
			headers: map[string]string{"Accept": "text/css,*/*;q=0.1", "Sec-Fetch-Mode": "no-cors"},
			want:    false,
		},
		{
			name:    "favicon asset",
			method:  http.MethodGet,
			target:  "/admin/ds2api-favicon.svg",
			headers: map[string]string{"Accept": "image/avif,image/webp,*/*", "Sec-Fetch-Mode": "no-cors"},
			want:    false,
		},
		{
			name:    "xmlhttprequest with html accept",
			method:  http.MethodGet,
			target:  "/admin/accounts",
			headers: map[string]string{"Accept": browserAccept, "X-Requested-With": "XMLHttpRequest"},
			want:    false,
		},
		{
			name:    "prefetch",
			method:  http.MethodGet,
			target:  "/admin/accounts",
			headers: map[string]string{"Accept": browserAccept, "Sec-Fetch-Mode": "no-cors"},
			want:    false,
		},
		{
			name:    "non get method",
			method:  http.MethodPost,
			target:  "/admin/login",
			headers: map[string]string{"Accept": browserAccept, "Sec-Fetch-Mode": "navigate"},
			want:    false,
		},
		{
			name:    "head probe",
			method:  http.MethodHead,
			target:  "/admin/accounts",
			headers: map[string]string{"Accept": browserAccept},
			want:    false,
		},
		{
			name:    "outside admin",
			method:  http.MethodGet,
			target:  "/v1/models",
			headers: map[string]string{"Accept": browserAccept, "Sec-Fetch-Mode": "navigate"},
			want:    false,
		},
		{
			name:    "api identifier with dot in last segment",
			method:  http.MethodGet,
			target:  "/admin/usage-stats/accounts/user@example.com",
			headers: map[string]string{"Accept": browserAccept, "Sec-Fetch-Mode": "navigate"},
			want:    false,
		},
		{
			name:    "no accept and no fetch metadata",
			method:  http.MethodGet,
			target:  "/admin/accounts",
			headers: map[string]string{},
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			for key, value := range tc.headers {
				req.Header.Set(key, value)
			}
			if got := isAdminDocumentRequest(req); got != tc.want {
				t.Fatalf("isAdminDocumentRequest(%s %s, %v) = %v, want %v",
					tc.method, tc.target, tc.headers, got, tc.want)
			}
		})
	}
}

func TestIsAdminDocumentRequestRejectsNilRequest(t *testing.T) {
	if isAdminDocumentRequest(nil) {
		t.Fatal("expected nil request to be rejected")
	}
}

func TestAdminDocumentShellServesShellForNavigations(t *testing.T) {
	staticDir := t.TempDir()
	shell := "<!doctype html><html><head><title>" + testShellMarker +
		"</title></head><body><div id=\"root\"></div></body></html>"
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte(shell), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	t.Setenv("DS2API_STATIC_ADMIN_DIR", staticDir)

	h := &Handler{StaticDir: staticDir}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/accounts", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	rec := httptest.NewRecorder()
	h.AdminDocumentShell(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("document navigation status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html prefix", ct)
	}
	if !strings.Contains(rec.Body.String(), testShellMarker) {
		t.Fatalf("body does not contain the SPA shell marker: %s", rec.Body.String())
	}
}

func TestAdminDocumentShellLetsAPICallsThrough(t *testing.T) {
	t.Setenv("DS2API_STATIC_ADMIN_DIR", t.TempDir())

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusUnauthorized)
	})
	h := &Handler{StaticDir: t.TempDir()}

	req := httptest.NewRequest(http.MethodGet, "/admin/accounts", nil)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	h.AdminDocumentShell(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected the API request to fall through to the next handler")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAdminDocumentShellHandlesNilHandler(t *testing.T) {
	var h *Handler
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	h.AdminDocumentShell(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/accounts", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}
