package responsehistory

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"ds2api/internal/auth"
	"ds2api/internal/chathistory"
	"ds2api/internal/promptcompat"
	"ds2api/internal/usagestats"
)

// TestSessionAccountsUsageWhenChatHistoryDisabled proves the admin dashboard
// still sees tokens for surfaces whose chat history is switched off.
//
// The session is the single place where a protocol surface reports its final
// usage, so it must stay alive and annotate the request recorder even when
// persistence is disabled. Routing usage through the history store instead
// would silently blind the dashboard whenever chat history is off.
func TestSessionAccountsUsageWhenChatHistoryDisabled(t *testing.T) {
	usageStore := usagestats.NewStore("")
	defer func() {
		if err := usageStore.Close(); err != nil {
			t.Fatalf("close usage store: %v", err)
		}
	}()

	historyStore := chathistory.New(filepath.Join(t.TempDir(), "chat_history.json"))
	if _, err := historyStore.SetLimit(chathistory.DisabledLimit); err != nil {
		t.Fatalf("disable chat history: %v", err)
	}
	if historyStore.Enabled() {
		t.Fatal("expected chat history to be disabled")
	}

	handler := usagestats.Middleware(usageStore)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session := Start(StartParams{
			Store:   historyStore,
			Request: r,
			Auth:    &auth.RequestAuth{CallerID: "caller-1", AccountID: "account-1"},
			Surface: usagestats.SurfaceClaudeMessages,
			Standard: promptcompat.StandardRequest{
				ResponseModel: "deepseek-chat",
			},
		})
		if session == nil {
			t.Fatal("expected a session even when chat history is disabled")
		}
		session.Success(http.StatusOK, "", "hello", "stop", map[string]any{
			"input_tokens":  4,
			"output_tokens": 6,
			"total_tokens":  10,
		})
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil))

	snapshot := usageStore.Query("1h")
	if snapshot.Summary.Requests != 1 {
		t.Fatalf("requests = %d, want 1", snapshot.Summary.Requests)
	}
	if snapshot.Summary.PromptTokens != 4 || snapshot.Summary.CompletionTokens != 6 || snapshot.Summary.TotalTokens != 10 {
		t.Fatalf("tokens = %d/%d/%d, want 4/6/10",
			snapshot.Summary.PromptTokens, snapshot.Summary.CompletionTokens, snapshot.Summary.TotalTokens)
	}
	if len(snapshot.Models) != 1 || snapshot.Models[0].Key != "deepseek-chat" {
		t.Fatalf("model breakdown = %+v, want a single deepseek-chat entry", snapshot.Models)
	}
	if len(snapshot.Surfaces) != 1 || snapshot.Surfaces[0].Key != usagestats.SurfaceClaudeMessages {
		t.Fatalf("surface breakdown = %+v, want a single %s entry", snapshot.Surfaces, usagestats.SurfaceClaudeMessages)
	}
	if len(snapshot.Accounts) != 1 || snapshot.Accounts[0].Key != "account-1" {
		t.Fatalf("account breakdown = %+v, want a single account-1 entry", snapshot.Accounts)
	}
}
