package webui

import (
	"net/http"
	"strings"
)

// adminRootPath is the URL prefix shared by the Admin API and the SPA.
const adminRootPath = "/admin"

// AdminDocumentShell serves the WebUI shell for browser document navigations
// (hard refreshes and deep links) under /admin/* before chi can dispatch the
// request to an Admin API route registered on the same path.
//
// Without this guard a refresh on e.g. /admin/accounts matched GET
// /admin/accounts, so the authenticated API handler answered 401 JSON. The
// browser could not have sent its Bearer token: a document navigation cannot
// carry an Authorization header. The session was never expired — the SPA shell
// was simply never served.
//
// Requests that carry credentials, ask for JSON, or fetch static assets fall
// through untouched, so the Admin API contract is unchanged.
func (h *Handler) AdminDocumentShell(next http.Handler) http.Handler {
	if h == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAdminDocumentRequest(r) {
			h.admin(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isAdminDocumentRequest reports whether the request is a browser document
// navigation for an /admin route, as opposed to an Admin API call or a static
// asset fetch.
func isAdminDocumentRequest(r *http.Request) bool {
	if r == nil || r.URL == nil || r.Method != http.MethodGet {
		return false
	}
	path := r.URL.Path
	if path != adminRootPath && !strings.HasPrefix(path, adminRootPath+"/") {
		return false
	}
	// A request carrying credentials is an API call, never a document
	// navigation: browsers cannot attach Authorization to a navigation.
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Requested-With")), "xmlhttprequest") {
		return false
	}
	// Static assets live under /admin/assets/... and always carry a file
	// extension; SPA routes never do.
	if hasFileExtension(path) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Mode"))) {
	case "navigate":
		return true
	case "":
		// Browsers predating Fetch Metadata still advertise text/html.
		return acceptsHTML(r.Header.Get("Accept"))
	default:
		// cors / same-origin / no-cors are fetch, XHR, prefetch or asset loads.
		return false
	}
}

// hasFileExtension reports whether the last path segment looks like a static
// file rather than an SPA route.
func hasFileExtension(path string) bool {
	trimmed := strings.TrimSuffix(path, "/")
	if idx := strings.LastIndexByte(trimmed, '/'); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	return strings.Contains(trimmed, ".")
}

// acceptsHTML reports whether the Accept header lists an HTML media type.
func acceptsHTML(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		mediaType := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if strings.EqualFold(mediaType, "text/html") || strings.EqualFold(mediaType, "application/xhtml+xml") {
			return true
		}
	}
	return false
}
