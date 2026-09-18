package usagestats

import (
	"net/http"
	"strings"
)

// ClassifyRequest maps a request onto a usage surface. It reports false for
// requests that must not be counted, such as admin API traffic, CORS
// preflights, health probes, static assets and unknown paths.
func ClassifyRequest(r *http.Request) (string, bool) {
	if r == nil || r.URL == nil {
		return "", false
	}
	// CORS preflights are handled by the router and carry no usage.
	if r.Method == http.MethodOptions {
		return "", false
	}
	path := strings.TrimSpace(r.URL.Path)
	if path == "" {
		return "", false
	}
	switch path {
	case "/healthz", "/readyz":
		return "", false
	}
	// Admin traffic drives the dashboard itself; counting it would make every
	// number self-referential.
	if path == "/admin" || strings.HasPrefix(path, "/admin/") {
		return "", false
	}
	// The Vercel Node bridge calls back into the Go surface with internal
	// sub-requests; counting them would multiply a single user request.
	if hasInternalStreamParam(r) {
		return "", false
	}

	lower := strings.ToLower(path)

	// Gemini generateContent aliases live under /v1beta/models/... and under
	// /v1/models/{model}:generateContent, so they must be matched before the
	// OpenAI model-listing routes. ":streamGenerateContent" does not contain
	// ":generateContent", so both method suffixes are checked.
	if strings.Contains(lower, ":generatecontent") || strings.Contains(lower, ":streamgeneratecontent") {
		return SurfaceGeminiGenerate, true
	}

	switch {
	case lower == "/v1/chat/completions" || lower == "/chat/completions":
		return SurfaceOpenAIChat, true
	case lower == "/v1/responses" || lower == "/responses" ||
		strings.HasPrefix(lower, "/v1/responses/") || strings.HasPrefix(lower, "/responses/"):
		return SurfaceOpenAIResponses, true
	case lower == "/v1/embeddings" || lower == "/embeddings":
		return SurfaceOpenAIEmbeddings, true
	case lower == "/v1/files" || lower == "/files" ||
		strings.HasPrefix(lower, "/v1/files/") || strings.HasPrefix(lower, "/files/"):
		return SurfaceOpenAIFiles, true
	case lower == "/v1/models" || lower == "/models" ||
		strings.HasPrefix(lower, "/v1/models/") || strings.HasPrefix(lower, "/models/"):
		return SurfaceOpenAIModels, true
	case strings.HasPrefix(lower, "/anthropic/v1/messages") ||
		strings.HasPrefix(lower, "/v1/messages") ||
		strings.HasPrefix(lower, "/messages"):
		return SurfaceClaudeMessages, true
	case lower == "/anthropic/v1/models" || strings.HasPrefix(lower, "/anthropic/v1/models/"):
		return SurfaceClaudeModels, true
	case lower == "/api/version" || lower == "/api/tags" || lower == "/api/show":
		return SurfaceOllama, true
	}
	return "", false
}

// internalStreamParams are the query flags the Vercel Node bridge uses for its
// internal prepare/pow/switch/release sub-requests.
var internalStreamParams = []string{
	"__stream_prepare",
	"__stream_release",
	"__stream_pow",
	"__stream_switch",
}

// hasInternalStreamParam reports whether the request is one of the bridge's
// internal sub-requests rather than a client-facing call.
func hasInternalStreamParam(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	query := r.URL.Query()
	for _, name := range internalStreamParams {
		if strings.TrimSpace(query.Get(name)) == "1" {
			return true
		}
	}
	return false
}
