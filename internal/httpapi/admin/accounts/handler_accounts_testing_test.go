package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ds2api/internal/auth"
	"ds2api/internal/config"
	dsclient "ds2api/internal/deepseek/client"
)

type testingDSMock struct {
	loginCalls                 int
	createSessionCalls         int
	createSessionError         error
	createSessionErrorOnce     bool
	getPowCalls                int
	callCompletionCalls        int
	deleteAllSessionsCalls     int
	deleteAllSessionsError     error
	deleteAllSessionsErrorOnce bool
}

func (m *testingDSMock) Login(_ context.Context, _ config.Account) (string, error) {
	m.loginCalls++
	return "new-token", nil
}

func (m *testingDSMock) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	m.createSessionCalls++
	if m.createSessionError != nil {
		err := m.createSessionError
		if m.createSessionErrorOnce {
			m.createSessionError = nil
		}
		return "", err
	}
	return "session-id", nil
}

func (m *testingDSMock) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	m.getPowCalls++
	return "", errors.New("should not call GetPow in this test")
}

func (m *testingDSMock) CallCompletion(_ context.Context, _ *auth.RequestAuth, _ map[string]any, _ string, _ int) (*http.Response, error) {
	m.callCompletionCalls++
	return nil, errors.New("should not call CallCompletion in this test")
}

func (m *testingDSMock) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	m.deleteAllSessionsCalls++
	if m.deleteAllSessionsError != nil {
		err := m.deleteAllSessionsError
		if m.deleteAllSessionsErrorOnce {
			m.deleteAllSessionsError = nil
		}
		return err
	}
	return nil
}

func (m *testingDSMock) GetSessionCountForToken(_ context.Context, _ string) (*dsclient.SessionStats, error) {
	return &dsclient.SessionStats{Success: true}, nil
}

func TestTestAccount_BatchModeOnlyCreatesSession(t *testing.T) {
	t.Setenv("DS2API_CONFIG_JSON", `{"accounts":[{"email":"batch@example.com","password":"pwd","token":""}]}`)
	store := config.LoadStore()
	ds := &testingDSMock{}
	h := &Handler{Store: store, DS: ds}
	acc, ok := store.FindAccount("batch@example.com")
	if !ok {
		t.Fatal("expected test account")
	}

	result := h.testAccount(context.Background(), acc, "deepseek-v4-flash", "")

	if ok, _ := result["success"].(bool); !ok {
		t.Fatalf("expected success=true, got %#v", result)
	}
	msg, _ := result["message"].(string)
	if !strings.Contains(msg, "Token 刷新成功") {
		t.Fatalf("expected session-only success message, got %q", msg)
	}
	if ds.loginCalls != 1 || ds.createSessionCalls != 1 {
		t.Fatalf("unexpected Login/CreateSession calls: login=%d createSession=%d", ds.loginCalls, ds.createSessionCalls)
	}
	if ds.getPowCalls != 0 || ds.callCompletionCalls != 0 {
		t.Fatalf("expected no completion flow calls, got getPow=%d callCompletion=%d", ds.getPowCalls, ds.callCompletionCalls)
	}
	updated, ok := store.FindAccount("batch@example.com")
	if !ok {
		t.Fatal("expected updated account")
	}
	if updated.Token != "new-token" {
		t.Fatalf("expected refreshed token to be persisted, got %q", updated.Token)
	}
	testStatus, ok := store.AccountTestStatus("batch@example.com")
	if !ok || testStatus != "ok" {
		t.Fatalf("expected runtime test status ok, got %q (ok=%v)", testStatus, ok)
	}
}

func TestDeleteAllSessions_RetryWithReloginOnDeleteFailure(t *testing.T) {
	t.Setenv("DS2API_CONFIG_JSON", `{"accounts":[{"email":"batch@example.com","password":"pwd","token":"expired-token"}]}`)
	store := config.LoadStore()
	ds := &testingDSMock{deleteAllSessionsError: errors.New("token expired"), deleteAllSessionsErrorOnce: true}
	h := &Handler{Store: store, DS: ds}

	req := httptest.NewRequest(http.MethodPost, "/delete-all", bytes.NewBufferString(`{"identifier":"batch@example.com"}`))
	rec := httptest.NewRecorder()
	h.deleteAllSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if ok, _ := resp["success"].(bool); !ok {
		t.Fatalf("expected success response, got %#v", resp)
	}
	if ds.loginCalls != 2 {
		t.Fatalf("expected initial login plus relogin, got %d", ds.loginCalls)
	}
	if ds.deleteAllSessionsCalls != 2 {
		t.Fatalf("expected delete called twice, got %d", ds.deleteAllSessionsCalls)
	}
	updated, ok := store.FindAccount("batch@example.com")
	if !ok {
		t.Fatal("expected account")
	}
	if updated.Token != "new-token" {
		t.Fatalf("expected refreshed token persisted, got %q", updated.Token)
	}
}

type completionPayloadDSMock struct {
	payload map[string]any
}

func (m *completionPayloadDSMock) Login(_ context.Context, _ config.Account) (string, error) {
	return "new-token", nil
}

func (m *completionPayloadDSMock) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "session-id", nil
}

func (m *completionPayloadDSMock) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow-ok", nil
}

func (m *completionPayloadDSMock) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	m.payload = payload
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("data: {\"v\":\"ok\"}\n\ndata: [DONE]\n\n")),
	}, nil
}

func (m *completionPayloadDSMock) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func (m *completionPayloadDSMock) GetSessionCountForToken(_ context.Context, _ string) (*dsclient.SessionStats, error) {
	return &dsclient.SessionStats{Success: true}, nil
}

func TestTestAccount_MessageModeUsesExpertModelTypeForExpertModel(t *testing.T) {
	t.Setenv("DS2API_CONFIG_JSON", `{"accounts":[{"email":"batch@example.com","password":"pwd","token":"seed-token"}]}`)
	store := config.LoadStore()
	ds := &completionPayloadDSMock{}
	h := &Handler{Store: store, DS: ds}
	acc, ok := store.FindAccount("batch@example.com")
	if !ok {
		t.Fatal("expected test account")
	}

	result := h.testAccount(context.Background(), acc, "deepseek-v4-pro", "hello")

	if ok, _ := result["success"].(bool); !ok {
		t.Fatalf("expected success=true, got %#v", result)
	}
	if got := ds.payload["model_type"]; got != "expert" {
		t.Fatalf("expected model_type expert, got %#v", got)
	}
	if got := ds.payload["chat_session_id"]; got != "session-id" {
		t.Fatalf("unexpected chat_session_id: %#v", got)
	}
}

func TestTestAccount_MessageModeUsesVisionModelTypeForVisionModel(t *testing.T) {
	t.Setenv("DS2API_CONFIG_JSON", `{"accounts":[{"email":"batch@example.com","password":"pwd","token":"seed-token"}]}`)
	store := config.LoadStore()
	ds := &completionPayloadDSMock{}
	h := &Handler{Store: store, DS: ds}
	acc, ok := store.FindAccount("batch@example.com")
	if !ok {
		t.Fatal("expected test account")
	}

	result := h.testAccount(context.Background(), acc, "deepseek-v4-vision", "hello")

	if ok, _ := result["success"].(bool); !ok {
		t.Fatalf("expected success=true, got %#v", result)
	}
	if got := ds.payload["model_type"]; got != "vision" {
		t.Fatalf("expected model_type vision, got %#v", got)
	}
}

type stubPoolController struct {
	rebalances int
}

func (p *stubPoolController) Reset()                           {}
func (p *stubPoolController) Rebalance()                       { p.rebalances++ }
func (p *stubPoolController) Status() map[string]any           { return nil }
func (p *stubPoolController) ApplyRuntimeLimits(int, int, int) {}

func TestTestAccount_TokenFirstSkipsLoginWhenStoredTokenWorks(t *testing.T) {
	// Env-backed configs strip tokens at load (ClearAccountTokens); runtime
	// tokens enter the store via UpdateAccountToken, so seed one the same
	// way production does.
	t.Setenv("DS2API_CONFIG_JSON", `{"accounts":[{"email":"tok@example.com","password":"pwd"}]}`)
	store := config.LoadStore()
	if err := store.UpdateAccountToken("tok@example.com", "stored-token"); err != nil {
		t.Fatal(err)
	}
	ds := &testingDSMock{}
	h := &Handler{Store: store, DS: ds}
	acc, ok := store.FindAccount("tok@example.com")
	if !ok {
		t.Fatal("expected test account")
	}

	result := h.testAccount(context.Background(), acc, "deepseek-v4-flash", "")

	if ok, _ := result["success"].(bool); !ok {
		t.Fatalf("expected success=true, got %#v", result)
	}
	msg, _ := result["message"].(string)
	if !strings.Contains(msg, "Token 有效") {
		t.Fatalf("expected token-first success message, got %q", msg)
	}
	if ds.loginCalls != 0 {
		t.Fatalf("expected no login for a valid stored token, got %d login calls", ds.loginCalls)
	}
	if ds.createSessionCalls != 1 {
		t.Fatalf("expected exactly one CreateSession, got %d", ds.createSessionCalls)
	}
	updated, ok := store.FindAccount("tok@example.com")
	if !ok {
		t.Fatal("expected updated account")
	}
	if updated.Token != "stored-token" {
		t.Fatalf("stored token must not be overwritten by a token-first test, got %q", updated.Token)
	}
	testStatus, ok := store.AccountTestStatus("tok@example.com")
	if !ok || testStatus != "ok" {
		t.Fatalf("expected runtime test status ok, got %q (ok=%v)", testStatus, ok)
	}
}

func TestTestAccount_TokenFirstFallsBackToLoginWhenStoredTokenRejected(t *testing.T) {
	t.Setenv("DS2API_CONFIG_JSON", `{"accounts":[{"email":"tok@example.com","password":"pwd"}]}`)
	store := config.LoadStore()
	if err := store.UpdateAccountToken("tok@example.com", "expired-token"); err != nil {
		t.Fatal(err)
	}
	ds := &testingDSMock{createSessionError: errors.New("401 unauthorized"), createSessionErrorOnce: true}
	h := &Handler{Store: store, DS: ds}
	acc, ok := store.FindAccount("tok@example.com")
	if !ok {
		t.Fatal("expected test account")
	}

	result := h.testAccount(context.Background(), acc, "deepseek-v4-flash", "")

	if ok, _ := result["success"].(bool); !ok {
		t.Fatalf("expected success=true, got %#v", result)
	}
	msg, _ := result["message"].(string)
	if !strings.Contains(msg, "Token 刷新成功") {
		t.Fatalf("expected refresh success message after fallback, got %q", msg)
	}
	if ds.loginCalls != 1 {
		t.Fatalf("expected exactly one login after stored-token rejection, got %d", ds.loginCalls)
	}
	if ds.createSessionCalls != 2 {
		t.Fatalf("expected rejected token-first attempt plus post-login session, got %d CreateSession calls", ds.createSessionCalls)
	}
	updated, ok := store.FindAccount("tok@example.com")
	if !ok {
		t.Fatal("expected updated account")
	}
	if updated.Token != "new-token" {
		t.Fatalf("expected refreshed token to be persisted, got %q", updated.Token)
	}
}

func TestTestAccount_BannedAccountSkipsTokenFirst(t *testing.T) {
	t.Setenv("DS2API_CONFIG_JSON", `{"accounts":[{"email":"banned@example.com","password":"pwd"}]}`)
	store := config.LoadStore()
	if err := store.UpdateAccountToken("banned@example.com", "stored-token"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAccountBanStatus("banned@example.com", 1, 1790783711, 0); err != nil {
		t.Fatal(err)
	}
	ds := &testingDSMock{}
	pool := &stubPoolController{}
	h := &Handler{Store: store, DS: ds, Pool: pool}
	acc, ok := store.FindAccount("banned@example.com")
	if !ok {
		t.Fatal("expected test account")
	}

	result := h.testAccount(context.Background(), acc, "deepseek-v4-flash", "")

	if isBanned, _ := result["banned"].(bool); !isBanned {
		t.Fatalf("expected banned result, got %#v", result)
	}
	if ok, _ := result["success"].(bool); ok {
		t.Fatalf("banned account must not report success, got %#v", result)
	}
	msg, _ := result["message"].(string)
	if !strings.Contains(msg, "封禁") {
		t.Fatalf("expected ban message, got %q", msg)
	}
	// Banned accounts keep the login path so the ban state gets refreshed;
	// the stored token must not be trusted for them.
	if ds.loginCalls != 1 {
		t.Fatalf("expected login path for banned account, got %d login calls", ds.loginCalls)
	}
	if ds.createSessionCalls != 0 {
		t.Fatalf("banned account must not create sessions, got %d CreateSession calls", ds.createSessionCalls)
	}
	if pool.rebalances == 0 {
		t.Fatal("expected pool rebalance for banned account")
	}
}

func TestRunAccountTestsConcurrentlyGroupsByEgress(t *testing.T) {
	accounts := []config.Account{
		{Email: "p1a@example.com", ProxyID: "p1"},
		{Email: "direct@example.com"},
		{Email: "p1b@example.com", ProxyID: "p1"},
		{Email: "p2a@example.com", ProxyID: "p2"},
	}
	entered := make(chan string, 4)
	release := make(chan struct{})
	done := make(chan []map[string]any, 1)
	go func() {
		done <- runAccountTestsConcurrently(accounts, 8, func(_ int, acc config.Account) map[string]any {
			key := acc.ProxyID
			if key == "" {
				key = "direct"
			}
			entered <- key
			<-release
			return map[string]any{"account": acc.Email}
		})
	}()

	// Exactly one test per egress group may run at a time: the three group
	// leaders (p1, direct, p2) enter together; p1's second account must wait.
	leaders := make(map[string]bool)
	for i := 0; i < 3; i++ {
		select {
		case key := <-entered:
			if leaders[key] {
				t.Fatalf("two concurrent tests on egress %s", key)
			}
			leaders[key] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("expected 3 parallel group leaders, got %v", leaders)
		}
	}
	for _, want := range []string{"p1", "direct", "p2"} {
		if !leaders[want] {
			t.Fatalf("missing parallel group leader %s, got %v", want, leaders)
		}
	}
	select {
	case key := <-entered:
		t.Fatalf("unexpected 4th concurrent test on egress %s: same-egress tests must be sequential", key)
	default:
	}

	close(release)
	select {
	case key := <-entered:
		if key != "p1" {
			t.Fatalf("expected p1's second account after release, got %s", key)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expected p1's second account to run after release")
	}

	results := <-done
	wantOrder := []string{"p1a@example.com", "direct@example.com", "p1b@example.com", "p2a@example.com"}
	for i, want := range wantOrder {
		if got, _ := results[i]["account"].(string); got != want {
			t.Fatalf("result order not preserved: idx %d = %q, want %q", i, got, want)
		}
	}
}
