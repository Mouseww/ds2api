package completionruntime

import (
	"context"
	"net/http"
	"testing"

	"ds2api/internal/account"
	"ds2api/internal/auth"
	"ds2api/internal/config"
	dsclient "ds2api/internal/deepseek/client"
	"ds2api/internal/promptcompat"
)

// newManagedTestAuth returns a request lease on the first of three managed
// accounts plus a login recorder used to observe re-check / refresh /
// switch-bootstrap logins. (Env-backed configs clear stored tokens, so the
// initial Determine and every post-switch token bootstrap perform a login.)
func newManagedTestAuth(t *testing.T) (*auth.RequestAuth, *[]string) {
	t.Helper()
	t.Setenv("DS2API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@test.com","password":"pwd"},
			{"email":"acc2@test.com","password":"pwd"},
			{"email":"acc3@test.com","password":"pwd"}
		]
	}`)
	store := config.LoadStore()
	logins := &[]string{}
	resolver := auth.NewResolver(store, account.NewPool(store), func(_ context.Context, acc config.Account) (string, error) {
		*logins = append(*logins, acc.Identifier())
		return "fresh-" + acc.Identifier(), nil
	})
	req, _ := http.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer managed-key")
	a, err := resolver.Determine(req)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	t.Cleanup(func() { resolver.Release(a) })
	return a, logins
}

func TestFailurePolicyCaptchaRechecksBanAndSwitchesExactlyOnce(t *testing.T) {
	a, logins := newManagedTestAuth(t)
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("expected initial lease on acc1, got %q", a.AccountID)
	}
	policy := NewFailurePolicy(a)

	first := policy.Handle(context.Background(), &dsclient.RequestFailure{Op: "create session", Kind: dsclient.FailureCaptchaRequired, Message: "captcha challenge required"})
	if first != FailureActionSwitchedAccount {
		t.Fatalf("captcha on managed account: expected switch, got %v", first)
	}
	if a.AccountID != "acc2@test.com" {
		t.Fatalf("expected lease switched to acc2, got %q", a.AccountID)
	}
	wantLogins := []string{"acc1@test.com", "acc1@test.com", "acc2@test.com"}
	if len(*logins) != len(wantLogins) {
		t.Fatalf("login sequence mismatch: got %v want %v", *logins, wantLogins)
	}
	for i, want := range wantLogins {
		if (*logins)[i] != want {
			t.Fatalf("login %d = %q want %q (all=%v)", i, (*logins)[i], want, *logins)
		}
	}

	// A second failure in the same request must not burn a third account:
	// the lease already switched once (double-switch guard).
	second := policy.Handle(context.Background(), &dsclient.RequestFailure{Op: "completion", Kind: dsclient.FailureCaptchaRequired, Message: "captcha challenge required"})
	if second != FailureActionFail {
		t.Fatalf("second captcha in one request: expected fail, got %v", second)
	}
	if a.AccountID != "acc2@test.com" {
		t.Fatalf("lease must stay on acc2 after the single switch, got %q", a.AccountID)
	}
	if len(*logins) != len(wantLogins) {
		t.Fatalf("second handle must not trigger any further login, got %v", *logins)
	}
}

func TestFailurePolicyManagedUnauthorizedLadder(t *testing.T) {
	a, logins := newManagedTestAuth(t)
	policy := NewFailurePolicy(a)
	failure := &dsclient.RequestFailure{Op: "create session", Kind: dsclient.FailureManagedUnauthorized, Message: "token expired"}

	// 1st: the ban re-check login refreshes the token in place.
	if got := policy.Handle(context.Background(), failure); got != FailureActionRetrySameAccount {
		t.Fatalf("first auth failure: expected retry-same-account, got %v", got)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("first auth failure must keep the lease on acc1, got %q", a.AccountID)
	}

	// 2nd: re-check already done; explicit singleflight refresh succeeds.
	if got := policy.Handle(context.Background(), failure); got != FailureActionRetrySameAccount {
		t.Fatalf("second auth failure: expected retry-same-account via refresh, got %v", got)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("second auth failure must keep the lease on acc1, got %q", a.AccountID)
	}

	// 3rd: refresh budget spent; switch to the next pooled account.
	if got := policy.Handle(context.Background(), failure); got != FailureActionSwitchedAccount {
		t.Fatalf("third auth failure: expected switch, got %v", got)
	}
	if a.AccountID != "acc2@test.com" {
		t.Fatalf("third auth failure must switch to acc2, got %q", a.AccountID)
	}

	// 4th: every lease action is spent; the request must fail.
	if got := policy.Handle(context.Background(), failure); got != FailureActionFail {
		t.Fatalf("fourth auth failure: expected fail, got %v", got)
	}
	if a.AccountID != "acc2@test.com" {
		t.Fatalf("lease must stay on acc2, got %q", a.AccountID)
	}

	// Login sequence: acc1 bootstrap (determine), acc1 re-check, acc1 refresh,
	// acc2 switch bootstrap — and nothing after the guard kicks in.
	wantLogins := []string{"acc1@test.com", "acc1@test.com", "acc1@test.com", "acc2@test.com"}
	if len(*logins) != len(wantLogins) {
		t.Fatalf("login sequence mismatch: got %v want %v", *logins, wantLogins)
	}
	for i, want := range wantLogins {
		if (*logins)[i] != want {
			t.Fatalf("login %d = %q want %q (all=%v)", i, (*logins)[i], want, *logins)
		}
	}
}

func TestFailurePolicyUnknownFailureNeverTouchesLease(t *testing.T) {
	a, logins := newManagedTestAuth(t)
	policy := NewFailurePolicy(a)

	if got := policy.Handle(context.Background(), &dsclient.RequestFailure{Op: "create session", Kind: dsclient.FailureUnknown, Message: "boom"}); got != FailureActionFail {
		t.Fatalf("unknown failure: expected fail, got %v", got)
	}
	if got := policy.Handle(context.Background(), nil); got != FailureActionFail {
		t.Fatalf("nil failure: expected fail, got %v", got)
	}
	if a.AccountID != "acc1@test.com" || len(*logins) != 1 {
		t.Fatalf("unknown failures must not mutate the lease, account=%q logins=%v", a.AccountID, *logins)
	}
}

func TestStartCompletionRoutesCreateSessionCaptchaThroughPolicy(t *testing.T) {
	a, logins := newManagedTestAuth(t)
	ds := &fakeDeepSeekCaller{
		sessionByAccount: true,
		createSessionErrs: []error{
			&dsclient.RequestFailure{Op: "create session", Kind: dsclient.FailureCaptchaRequired, Message: "captcha challenge required"},
		},
		responses: []*http.Response{sseHTTPResponse(http.StatusOK, `data: {"p":"response/content","v":"ok"}`)},
	}
	stdReq := promptcompat.StandardRequest{
		Surface:         "test",
		ResponseModel:   "deepseek-v4-flash",
		PromptTokenText: "prompt",
		FinalPrompt:     "final prompt",
	}

	start, outErr := StartCompletion(context.Background(), ds, a, stdReq, Options{})
	if outErr != nil {
		t.Fatalf("unexpected output error after policy switch: %#v", outErr)
	}
	if start.SessionID != "session-acc2@test.com" {
		t.Fatalf("expected session on switched account, got %q", start.SessionID)
	}
	wantCreate := []string{"acc1@test.com", "acc2@test.com"}
	if len(ds.createSessionCalls) != len(wantCreate) {
		t.Fatalf("create session accounts mismatch: got %v want %v", ds.createSessionCalls, wantCreate)
	}
	for i, want := range wantCreate {
		if ds.createSessionCalls[i] != want {
			t.Fatalf("create session %d = %q want %q (all=%v)", i, ds.createSessionCalls[i], want, ds.createSessionCalls)
		}
	}
	// Login sequence: acc1 bootstrap, acc1 ban re-check, acc2 switch bootstrap.
	wantLogins := []string{"acc1@test.com", "acc1@test.com", "acc2@test.com"}
	if len(*logins) != len(wantLogins) {
		t.Fatalf("login sequence mismatch: got %v want %v", *logins, wantLogins)
	}
}

func TestStartCompletionUploadAuthFailureRetriesSameAccountThroughPolicy(t *testing.T) {
	a, logins := newManagedTestAuth(t)
	ds := &fakeDeepSeekCaller{
		sessionByAccount: true,
		uploadErrs: []error{
			&dsclient.RequestFailure{Op: "upload file", Kind: dsclient.FailureManagedUnauthorized, Message: "token expired"},
		},
		responses: []*http.Response{sseHTTPResponse(http.StatusOK, `data: {"p":"response/content","v":"ok"}`)},
	}
	stdReq := promptcompat.StandardRequest{
		Surface:         "test",
		ResponseModel:   "deepseek-v4-flash",
		PromptTokenText: "large current input",
		FinalPrompt:     "large current input",
		Messages: []any{
			map[string]any{"role": "user", "content": "large current input"},
		},
	}

	start, outErr := StartCompletion(context.Background(), ds, a, stdReq, Options{CurrentInputFile: currentInputRuntimeConfig{}})
	if outErr != nil {
		t.Fatalf("unexpected output error after policy refresh: %#v", outErr)
	}
	if start.SessionID != "session-acc1@test.com" {
		t.Fatalf("expected session on refreshed same account, got %q", start.SessionID)
	}
	if len(ds.uploads) != 2 {
		t.Fatalf("expected upload retried once on the same account, got %d uploads", len(ds.uploads))
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("upload auth failure must be retried on the same account, lease moved to %q", a.AccountID)
	}
	// Login sequence: acc1 bootstrap, acc1 ban re-check (refreshed the token).
	wantLogins := []string{"acc1@test.com", "acc1@test.com"}
	if len(*logins) != len(wantLogins) {
		t.Fatalf("login sequence mismatch: got %v want %v", *logins, wantLogins)
	}
}

// TestExecuteNonStreamWithRetrySwitchesExactlyOncePerRequest is the
// double-switch regression test. Before the consolidation the DeepSeek client
// already switched accounts during CreateSession/GetPow, and the completion
// runtime switched AGAIN on a later 429 — two different pooled accounts
// burned for one request. Now the client is policy-free and the shared
// policy (plus the auth-layer switch guard) allows exactly one switch.
func TestExecuteNonStreamWithRetrySwitchesExactlyOncePerRequest(t *testing.T) {
	a, logins := newManagedTestAuth(t)
	ds := &fakeDeepSeekCaller{
		sessionByAccount: true,
		createSessionErrs: []error{
			&dsclient.RequestFailure{Op: "create session", Kind: dsclient.FailureCaptchaRequired, Message: "captcha challenge required"},
		},
		responses: []*http.Response{
			sseHTTPResponse(http.StatusTooManyRequests, `{"code":429,"msg":"too many requests"}`),
		},
	}
	stdReq := promptcompat.StandardRequest{
		Surface:         "test",
		ResponseModel:   "deepseek-v4-flash",
		PromptTokenText: "prompt",
		FinalPrompt:     "final prompt",
	}

	result, outErr := ExecuteNonStreamWithRetry(context.Background(), ds, a, stdReq, Options{RetryEnabled: true})
	if outErr == nil {
		t.Fatalf("expected final 429 after the single switch was spent, got result %#v", result)
	}
	if outErr.Status != http.StatusTooManyRequests {
		t.Fatalf("expected 429 output error, got %#v", outErr)
	}

	// Exactly one switch: acc1 (captcha during start) → acc2; the later 429
	// must not reach acc3.
	wantCreate := []string{"acc1@test.com", "acc2@test.com"}
	if len(ds.createSessionCalls) != len(wantCreate) {
		t.Fatalf("create session accounts mismatch: got %v want %v", ds.createSessionCalls, wantCreate)
	}
	for i, want := range wantCreate {
		if ds.createSessionCalls[i] != want {
			t.Fatalf("create session %d = %q want %q (all=%v)", i, ds.createSessionCalls[i], want, ds.createSessionCalls)
		}
	}
	if len(ds.completionAccounts) != 1 || ds.completionAccounts[0] != "acc2@test.com" {
		t.Fatalf("expected exactly one completion on the switched account, got %v", ds.completionAccounts)
	}
	// Login sequence: acc1 bootstrap, acc1 ban re-check, acc2 switch bootstrap.
	// The later 429 re-check is skipped (already done for this request) and no
	// third account is bootstrapped.
	wantLogins := []string{"acc1@test.com", "acc1@test.com", "acc2@test.com"}
	if len(*logins) != len(wantLogins) {
		t.Fatalf("login sequence mismatch: got %v want %v", *logins, wantLogins)
	}
	for i, want := range wantLogins {
		if (*logins)[i] != want {
			t.Fatalf("login %d = %q want %q (all=%v)", i, (*logins)[i], want, *logins)
		}
	}
}

// TestExecuteNonStreamWithRetryRechecksBanOnInitial429 locks in the missed
// ban-recheck fix: the completion runtime's 429 switch path never called
// RecheckBan before the consolidation (only the client's session/pow loops
// did). Now every 429-mapped switch re-checks the rate-limited account first.
func TestExecuteNonStreamWithRetryRechecksBanOnInitial429(t *testing.T) {
	a, logins := newManagedTestAuth(t)
	ds := &fakeDeepSeekCaller{
		sessionByAccount: true,
		responses: []*http.Response{
			sseHTTPResponse(http.StatusTooManyRequests, `{"code":429,"msg":"too many requests"}`),
			sseHTTPResponse(http.StatusOK, `data: {"response_message_id":21,"p":"response/content","v":"ok from second account"}`),
		},
	}
	stdReq := promptcompat.StandardRequest{
		Surface:         "test",
		ResponseModel:   "deepseek-v4-flash",
		PromptTokenText: "prompt",
		FinalPrompt:     "final prompt",
	}

	result, outErr := ExecuteNonStreamWithRetry(context.Background(), ds, a, stdReq, Options{RetryEnabled: true})
	if outErr != nil {
		t.Fatalf("unexpected output error after 429 switch: %#v", outErr)
	}
	if result.Turn.Text != "ok from second account" {
		t.Fatalf("text mismatch after 429 switch: %q", result.Turn.Text)
	}
	// Login sequence: acc1 bootstrap, acc1 ban re-check (the 429 trigger),
	// acc2 switch bootstrap.
	wantLogins := []string{"acc1@test.com", "acc1@test.com", "acc2@test.com"}
	if len(*logins) != len(wantLogins) {
		t.Fatalf("login sequence mismatch: got %v want %v", *logins, wantLogins)
	}
	for i, want := range wantLogins {
		if (*logins)[i] != want {
			t.Fatalf("login %d = %q want %q (all=%v)", i, (*logins)[i], want, *logins)
		}
	}
}

// TestExecuteStreamWithRetryRechecksBanOnSwitch locks in the missed
// ban-recheck fix for the stream retry loop: its 429/captcha switch path now
// re-checks the rate-limited account through the shared policy before
// switching.
func TestExecuteStreamWithRetryRechecksBanOnSwitch(t *testing.T) {
	a, logins := newManagedTestAuth(t)
	ds := &fakeDeepSeekCaller{
		sessionByAccount: true,
		responses: []*http.Response{
			sseHTTPResponse(http.StatusOK, `data: {"response_message_id":12,"p":"response/thinking_content","v":"retry empty"}`),
			sseHTTPResponse(http.StatusOK, `data: {"response_message_id":21,"p":"response/content","v":"ok from second account"}`),
		},
	}
	initial := sseHTTPResponse(http.StatusOK, `data: {"response_message_id":11,"p":"response/thinking_content","v":"first empty"}`)
	payload := map[string]any{"prompt": "original prompt", "chat_session_id": "session-acc1@test.com"}
	attemptsSeen := 0

	ExecuteStreamWithRetry(context.Background(), ds, a, initial, payload, "pow", StreamRetryOptions{
		Surface:          "test.stream",
		Stream:           true,
		RetryEnabled:     true,
		RetryMaxAttempts: 1,
		UsagePrompt:      "original prompt",
	}, StreamRetryHooks{
		ConsumeAttempt: func(resp *http.Response, allowDeferEmpty bool) (bool, bool) {
			defer func() {
				if err := resp.Body.Close(); err != nil {
					t.Fatalf("close failed: %v", err)
				}
			}()
			attemptsSeen++
			if attemptsSeen >= 3 {
				return true, false
			}
			return false, allowDeferEmpty
		},
	})

	if attemptsSeen != 3 {
		t.Fatalf("expected three stream attempts, got %d", attemptsSeen)
	}
	// Login sequence: acc1 bootstrap, acc1 ban re-check (429 trigger), acc2
	// switch bootstrap.
	wantLogins := []string{"acc1@test.com", "acc1@test.com", "acc2@test.com"}
	if len(*logins) != len(wantLogins) {
		t.Fatalf("login sequence mismatch: got %v want %v", *logins, wantLogins)
	}
	for i, want := range wantLogins {
		if (*logins)[i] != want {
			t.Fatalf("login %d = %q want %q (all=%v)", i, (*logins)[i], want, *logins)
		}
	}
}

func TestFailurePolicyDirectTokenNeverMutatesLease(t *testing.T) {
	a := &auth.RequestAuth{UseConfigToken: false, DeepSeekToken: "direct-token"}
	policy := NewFailurePolicy(a)

	failures := []*dsclient.RequestFailure{
		{Op: "create session", Kind: dsclient.FailureCaptchaRequired, Message: "captcha challenge required"},
		{Op: "completion", Kind: dsclient.FailureRateLimited, Message: "too many requests"},
		{Op: "create session", Kind: dsclient.FailureDirectUnauthorized, Message: "unauthorized"},
	}
	for _, failure := range failures {
		if got := policy.Handle(context.Background(), failure); got != FailureActionFail {
			t.Fatalf("direct-token request with %s failure: expected FailureActionFail, got %v", failure.Kind, got)
		}
	}
	if a.AccountID != "" {
		t.Fatalf("direct-token request must not touch account state, got %q", a.AccountID)
	}
}
