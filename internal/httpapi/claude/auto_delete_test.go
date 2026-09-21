package claude

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ds2api/internal/auth"
	"ds2api/internal/chathistory"
	dsclient "ds2api/internal/deepseek/client"
)

type claudeAutoDeleteStore struct {
	mode string
}

func (claudeAutoDeleteStore) ModelAliases() map[string]string { return nil }
func (claudeAutoDeleteStore) CurrentInputFileEnabled() bool   { return false }
func (claudeAutoDeleteStore) CurrentInputFileMinChars() int   { return 0 }
func (s claudeAutoDeleteStore) AutoDeleteMode() string        { return s.mode }

type claudeAutoDeleteDS struct {
	deletedSessions []string
	deletedAll      bool
}

func (d *claudeAutoDeleteDS) CreateSession(context.Context, *auth.RequestAuth, int) (string, error) {
	return "session-id", nil
}
func (d *claudeAutoDeleteDS) GetPow(context.Context, *auth.RequestAuth, int) (string, error) {
	return "pow", nil
}
func (d *claudeAutoDeleteDS) UploadFile(context.Context, *auth.RequestAuth, dsclient.UploadFileRequest, int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-id"}, nil
}
func (d *claudeAutoDeleteDS) CallCompletion(context.Context, *auth.RequestAuth, map[string]any, string, int) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("data: {\"p\":\"response/content\",\"v\":\"ok\"}\n")),
	}, nil
}
func (d *claudeAutoDeleteDS) DeleteSessionForToken(_ context.Context, _ string, sessionID string) (*dsclient.DeleteSessionResult, error) {
	d.deletedSessions = append(d.deletedSessions, sessionID)
	return &dsclient.DeleteSessionResult{}, nil
}
func (d *claudeAutoDeleteDS) DeleteAllSessionsForToken(context.Context, string) error {
	d.deletedAll = true
	return nil
}

// TestClaudeDirectAutoDeletesUpstreamSession guards the shared session
// cleanup: a completed Claude request must honor auto_delete_sessions the same
// way the OpenAI chat surface does. Claude used to never delete the upstream
// DeepSeek session, leaking one session per request on the account.
func TestClaudeDirectAutoDeletesUpstreamSession(t *testing.T) {
	t.Run("single mode deletes the request session", func(t *testing.T) {
		ds := &claudeAutoDeleteDS{}
		h := &Handler{
			Store:       claudeAutoDeleteStore{mode: "single"},
			Auth:        claudeCurrentInputAuth{},
			DS:          ds,
			ChatHistory: chathistory.New(filepath.Join(t.TempDir(), "history.json")),
		}
		reqBody := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello"}],"max_tokens":1024}`
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		h.Messages(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
		}
		if len(ds.deletedSessions) != 1 || ds.deletedSessions[0] != "session-id" {
			t.Fatalf("expected upstream session-id to be deleted, got %v", ds.deletedSessions)
		}
		if ds.deletedAll {
			t.Fatal("expected single mode to not delete all sessions")
		}
	})

	t.Run("none mode keeps the session", func(t *testing.T) {
		ds := &claudeAutoDeleteDS{}
		h := &Handler{
			Store:       claudeAutoDeleteStore{mode: "none"},
			Auth:        claudeCurrentInputAuth{},
			DS:          ds,
			ChatHistory: chathistory.New(filepath.Join(t.TempDir(), "history.json")),
		}
		reqBody := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello"}],"max_tokens":1024}`
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		h.Messages(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
		}
		if len(ds.deletedSessions) != 0 || ds.deletedAll {
			t.Fatal("expected none mode to keep upstream sessions")
		}
	})
}
