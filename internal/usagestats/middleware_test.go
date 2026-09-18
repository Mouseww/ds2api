package usagestats

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddlewareRecordsAnnotatedUsage(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	handler := Middleware(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Annotate(r.Context(), Usage{
			Model: "deepseek-v4-pro", AccountID: "acc-1", CallerID: "caller-1",
			PromptTokens: 12, CompletionTokens: 8, ReasoningTokens: 3, TotalTokens: 20,
		})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	snap := store.Query("1h")
	if snap.Summary.Requests != 1 {
		t.Fatalf("requests = %d, want 1", snap.Summary.Requests)
	}
	if snap.Summary.TotalTokens != 20 || snap.Summary.PromptTokens != 12 {
		t.Fatalf("tokens = %+v, want 20 total / 12 prompt", snap.Summary)
	}
	if len(snap.Models) != 1 || snap.Models[0].Key != "deepseek-v4-pro" {
		t.Fatalf("models = %+v", snap.Models)
	}
	if len(snap.Accounts) != 1 || snap.Accounts[0].Key != "acc-1" {
		t.Fatalf("accounts = %+v", snap.Accounts)
	}
	if len(snap.Surfaces) != 1 || snap.Surfaces[0].Key != SurfaceOpenAIChat {
		t.Fatalf("surfaces = %+v", snap.Surfaces)
	}
	if snap.Totals.MaxElapsedMs < 0 {
		t.Fatalf("max elapsed = %d, want a non-negative duration", snap.Totals.MaxElapsedMs)
	}
}

func TestMiddlewareCapturesErrorStatus(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	handler := Middleware(store)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	snap := store.Query("1h")
	if snap.Summary.Requests != 1 || snap.Summary.Errors != 1 {
		t.Fatalf("requests=%d errors=%d, want 1/1", snap.Summary.Requests, snap.Summary.Errors)
	}
	if snap.Summary.SuccessRate != 0 {
		t.Fatalf("success rate = %v, want 0", snap.Summary.SuccessRate)
	}
}

func TestMiddlewareSkipsUncountedRequests(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	handler := Middleware(store)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/admin/usage-stats"},
		{http.MethodOptions, "/v1/chat/completions"},
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/assets/index.js"},
	}
	for _, tc := range cases {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.path, nil))
	}

	if got := store.Query("1h").Totals.Requests; got != 0 {
		t.Fatalf("requests = %d, want 0 for uncounted paths", got)
	}
}

func TestMiddlewareWithNilStorePassesThrough(t *testing.T) {
	called := false
	handler := Middleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if !called {
		t.Fatal("expected the wrapped handler to run with a nil store")
	}
}

func TestStatusWriterPreservesFlusherAndUnwrap(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &statusWriter{ResponseWriter: recorder, status: http.StatusOK}

	var asFlusher http.Flusher = writer
	asFlusher.Flush()
	if !recorder.Flushed {
		t.Fatal("expected Flush to reach the underlying recorder")
	}
	if writer.Unwrap() != recorder {
		t.Fatal("expected Unwrap to return the original writer")
	}

	writer.WriteHeader(http.StatusTeapot)
	writer.WriteHeader(http.StatusOK) // must not overwrite the first status
	if got := writer.statusCode(); got != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", got, http.StatusTeapot)
	}
}

func TestStatusWriterDefaultsToOKOnWrite(t *testing.T) {
	writer := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	if _, err := writer.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := writer.statusCode(); got != http.StatusOK {
		t.Fatalf("status = %d, want %d", got, http.StatusOK)
	}
}
