package usagestats

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyRequest(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   string
		count  bool
	}{
		{http.MethodPost, "/v1/chat/completions", SurfaceOpenAIChat, true},
		{http.MethodPost, "/chat/completions", SurfaceOpenAIChat, true},
		{http.MethodPost, "/v1/responses", SurfaceOpenAIResponses, true},
		{http.MethodGet, "/v1/responses/resp_1", SurfaceOpenAIResponses, true},
		{http.MethodPost, "/responses", SurfaceOpenAIResponses, true},
		{http.MethodPost, "/v1/embeddings", SurfaceOpenAIEmbeddings, true},
		{http.MethodPost, "/embeddings", SurfaceOpenAIEmbeddings, true},
		{http.MethodPost, "/v1/files", SurfaceOpenAIFiles, true},
		{http.MethodGet, "/v1/files/file_1", SurfaceOpenAIFiles, true},
		{http.MethodGet, "/v1/models", SurfaceOpenAIModels, true},
		{http.MethodGet, "/v1/models/deepseek-v4-pro", SurfaceOpenAIModels, true},
		{http.MethodPost, "/v1beta/models/gemini-2.0:generateContent", SurfaceGeminiGenerate, true},
		{http.MethodPost, "/v1beta/models/gemini-2.0:streamGenerateContent", SurfaceGeminiGenerate, true},
		{http.MethodPost, "/v1/models/deepseek-v4-pro:generateContent", SurfaceGeminiGenerate, true},
		{http.MethodPost, "/anthropic/v1/messages", SurfaceClaudeMessages, true},
		{http.MethodPost, "/v1/messages", SurfaceClaudeMessages, true},
		{http.MethodPost, "/messages", SurfaceClaudeMessages, true},
		{http.MethodPost, "/v1/messages/count_tokens", SurfaceClaudeMessages, true},
		{http.MethodGet, "/anthropic/v1/models", SurfaceClaudeModels, true},
		{http.MethodGet, "/api/tags", SurfaceOllama, true},
		{http.MethodGet, "/api/version", SurfaceOllama, true},

		{http.MethodGet, "/healthz", "", false},
		{http.MethodGet, "/readyz", "", false},
		{http.MethodGet, "/admin/usage-stats", "", false},
		{http.MethodGet, "/admin", "", false},
		{http.MethodOptions, "/v1/chat/completions", "", false},
		{http.MethodGet, "/assets/index.js", "", false},
		{http.MethodGet, "/", "", false},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		got, ok := ClassifyRequest(req)
		if ok != tc.count {
			t.Fatalf("%s %s: counted = %v, want %v", tc.method, tc.path, ok, tc.count)
		}
		if tc.count && got != tc.want {
			t.Fatalf("%s %s: surface = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestClassifyRequestNilInputs(t *testing.T) {
	if _, ok := ClassifyRequest(nil); ok {
		t.Fatal("expected nil request to be skipped")
	}
}

func TestSupportedRanges(t *testing.T) {
	ranges := SupportedRanges()
	if len(ranges) != len(rangeSpecs) {
		t.Fatalf("range count = %d, want %d", len(ranges), len(rangeSpecs))
	}
	if ranges[0] != "1h" {
		t.Fatalf("first range = %q, want 1h", ranges[0])
	}
}

func TestLookupRangeFallsBackToDefault(t *testing.T) {
	spec := lookupRange("bogus")
	if spec.name != DefaultRange {
		t.Fatalf("range = %q, want %q", spec.name, DefaultRange)
	}
	if got := lookupRange("7D"); got.name != "7d" {
		t.Fatalf("range = %q, want 7d", got.name)
	}
}

func TestStoreErrOnCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	store := NewStore(path)
	defer func() { _ = store.Close() }()
	if store.Err() == nil {
		t.Fatal("expected a load error for corrupt usage stats file")
	}
}
