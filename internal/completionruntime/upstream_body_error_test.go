package completionruntime

import (
	"context"
	"net/http"
	"testing"

	"ds2api/internal/promptcompat"
	"ds2api/internal/sse"
)

func mutedBodyResponse() *http.Response {
	resp := sseHTTPResponse(http.StatusOK, `{"code":0,"msg":"","data":{"biz_code":5,"biz_msg":"user is muted","biz_data":{"is_muted":1,"mute_until":1790939390.395}}}`)
	resp.Header.Set("Content-Type", "application/json")
	return resp
}

func invalidPowBodyResponse() *http.Response {
	resp := sseHTTPResponse(http.StatusOK, `{"code":40301,"msg":"INVALID_POW_RESPONSE","data":null}`)
	resp.Header.Set("Content-Type", "application/json")
	return resp
}

// TestFailurePolicyHandleUpstreamBodyErrorMutedEvictsAndSwitches locks in the
// mute handling: a "user is muted" JSON envelope evicts the account with
// reason "banned" (persisted ban state, so the unban monitor auto-recovers it
// after the mute expires) and switches the lease — without touching the
// error budget.
func TestFailurePolicyHandleUpstreamBodyErrorMutedEvictsAndSwitches(t *testing.T) {
	a, logins, store := newManagedTestAuth(t)
	policy := NewFailurePolicy(a)

	if got := policy.HandleUpstreamBodyError(context.Background(), sse.UpstreamCodeAccountMuted, 1790939390.395); got != FailureActionSwitchedAccount {
		t.Fatalf("muted account: expected switch, got %v", got)
	}
	if a.AccountID == "acc1@test.com" {
		t.Fatalf("expected lease switched off the muted account, got %q", a.AccountID)
	}
	switchedAccount := a.AccountID

	acc, ok := store.FindAccount("acc1@test.com")
	if !ok {
		t.Fatal("expected acc1 to exist")
	}
	if acc.IsEnabled() {
		t.Fatal("expected muted acc1 to be disabled")
	}
	if acc.DisabledReason != "banned" {
		t.Fatalf("expected disabled_reason banned, got %q", acc.DisabledReason)
	}
	if acc.BanIsMuted != 1 {
		t.Fatalf("expected ban_is_muted=1, got %d", acc.BanIsMuted)
	}
	if acc.BanMuteUntil != 1790939390.395 {
		t.Fatalf("expected ban_mute_until=1790939390.395, got %v", acc.BanMuteUntil)
	}

	// One-shot: a second typed mute error in the same request must fail
	// instead of switching again.
	if got := policy.HandleUpstreamBodyError(context.Background(), sse.UpstreamCodeAccountMuted, 1790939390.395); got != FailureActionFail {
		t.Fatalf("second mute in one request: expected fail, got %v", got)
	}
	if a.AccountID != switchedAccount {
		t.Fatalf("lease must stay on %q, got %q", switchedAccount, a.AccountID)
	}
	// Login sequence: acc1 bootstrap (determine), switched-account bootstrap.
	wantLogins := []string{"acc1@test.com", switchedAccount}
	if len(*logins) != len(wantLogins) {
		t.Fatalf("login sequence mismatch: got %v want %v", *logins, wantLogins)
	}
	for i, want := range wantLogins {
		if (*logins)[i] != want {
			t.Fatalf("login %d = %q, want %q (all=%v)", i, (*logins)[i], want, *logins)
		}
	}
}

// TestFailurePolicyHandleUpstreamBodyErrorPowRetryThenLadder: the first
// INVALID_POW_RESPONSE retries the same account with a fresh PoW; a repeat
// falls through to the one-shot upstream-unavailable ladder.
func TestFailurePolicyHandleUpstreamBodyErrorPowRetryThenLadder(t *testing.T) {
	a, _, _ := newManagedTestAuth(t)
	policy := NewFailurePolicy(a)

	if got := policy.HandleUpstreamBodyError(context.Background(), sse.UpstreamCodeInvalidPow, 0); got != FailureActionRetrySameAccount {
		t.Fatalf("first pow rejection: expected retry-same-account, got %v", got)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("pow retry must keep the lease on acc1, got %q", a.AccountID)
	}

	// Repeat: the upstream-unavailable ladder re-checks the ban first, which
	// refreshes the token in place.
	if got := policy.HandleUpstreamBodyError(context.Background(), sse.UpstreamCodeInvalidPow, 0); got != FailureActionRetrySameAccount {
		t.Fatalf("second pow rejection: expected retry-same-account via re-check, got %v", got)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("ladder re-check must keep the lease on acc1, got %q", a.AccountID)
	}
}

// TestFailurePolicyHandleUpstreamBodyErrorGenericUsesLadder: a generic typed
// envelope goes straight to the one-shot upstream-unavailable ladder — the
// ladder runs at most once per request, so a second typed error fails.
func TestFailurePolicyHandleUpstreamBodyErrorGenericUsesLadder(t *testing.T) {
	a, _, _ := newManagedTestAuth(t)
	policy := NewFailurePolicy(a)

	if got := policy.HandleUpstreamBodyError(context.Background(), sse.UpstreamCodeError, 0); got != FailureActionRetrySameAccount {
		t.Fatalf("first generic body error: expected retry-same-account via re-check, got %v", got)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("ladder re-check must keep the lease on acc1, got %q", a.AccountID)
	}
	if got := policy.HandleUpstreamBodyError(context.Background(), sse.UpstreamCodeError, 0); got != FailureActionFail {
		t.Fatalf("second generic body error: expected fail (ladder is one-shot per request), got %v", got)
	}
}

func bodyErrorTestRequest() promptcompat.StandardRequest {
	return promptcompat.StandardRequest{
		Surface:         "test",
		ResponseModel:   "deepseek-v4-flash",
		PromptTokenText: "prompt",
		FinalPrompt:     "final prompt",
	}
}

// TestExecuteNonStreamWithRetryMutedBodySwitchesAccount: a mute envelope on
// the first completion switches to an alternate account and completes there,
// with the muted account evicted for the unban monitor to recover.
func TestExecuteNonStreamWithRetryMutedBodySwitchesAccount(t *testing.T) {
	a, _, store := newManagedTestAuth(t)
	ds := &fakeDeepSeekCaller{
		sessionByAccount: true,
		responses: []*http.Response{
			mutedBodyResponse(),
			sseHTTPResponse(http.StatusOK, `data: {"response_message_id":21,"p":"response/content","v":"ok from second account"}`),
		},
	}

	result, outErr := ExecuteNonStreamWithRetry(context.Background(), ds, a, bodyErrorTestRequest(), Options{RetryEnabled: true})
	if outErr != nil {
		t.Fatalf("unexpected output error after mute switch: %#v", outErr)
	}
	if result.Turn.Text != "ok from second account" {
		t.Fatalf("text mismatch after mute switch: %q", result.Turn.Text)
	}
	if a.AccountID == "acc1@test.com" {
		t.Fatalf("expected lease switched off the muted account, still on %q", a.AccountID)
	}
	if result.SessionID != "session-"+a.AccountID {
		t.Fatalf("expected session on switched account %q, got %q", a.AccountID, result.SessionID)
	}
	wantAccounts := []string{"acc1@test.com", a.AccountID}
	if len(ds.completionAccounts) != len(wantAccounts) {
		t.Fatalf("completion accounts mismatch: got %v want %v", ds.completionAccounts, wantAccounts)
	}
	for i, want := range wantAccounts {
		if ds.completionAccounts[i] != want {
			t.Fatalf("completion %d = %q want %q (all=%v)", i, ds.completionAccounts[i], want, ds.completionAccounts)
		}
	}
	// The switched retry must be a fresh completion: same prompt, no
	// empty-output suffix.
	if got := ds.payloads[1]["prompt"]; got != "final prompt" {
		t.Fatalf("switched retry prompt = %v, want the unchanged final prompt", got)
	}
	acc, ok := store.FindAccount("acc1@test.com")
	if !ok {
		t.Fatal("expected acc1 to exist")
	}
	if acc.IsEnabled() || acc.DisabledReason != "banned" || acc.BanMuteUntil != 1790939390.395 {
		t.Fatalf("expected acc1 evicted as banned with mute expiry, got enabled=%v reason=%q mute_until=%v", acc.IsEnabled(), acc.DisabledReason, acc.BanMuteUntil)
	}
}

// TestExecuteNonStreamWithRetryMutedBodyTerminalAfterSwitch: when the mute
// follows to the switched account too, the typed error surfaces truthfully
// instead of degrading into the generic empty-output classification.
func TestExecuteNonStreamWithRetryMutedBodyTerminalAfterSwitch(t *testing.T) {
	a, _, _ := newManagedTestAuth(t)
	ds := &fakeDeepSeekCaller{
		sessionByAccount: true,
		responses: []*http.Response{
			mutedBodyResponse(),
			mutedBodyResponse(),
		},
	}

	result, outErr := ExecuteNonStreamWithRetry(context.Background(), ds, a, bodyErrorTestRequest(), Options{RetryEnabled: true})
	if outErr == nil {
		t.Fatalf("expected typed mute error, got result %#v", result)
	}
	if outErr.Code != sse.UpstreamCodeAccountMuted {
		t.Fatalf("code = %q, want %q", outErr.Code, sse.UpstreamCodeAccountMuted)
	}
	if outErr.Status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", outErr.Status)
	}
	if outErr.MuteUntil != 1790939390.395 {
		t.Fatalf("mute_until = %v, want 1790939390.395", outErr.MuteUntil)
	}
	if len(ds.completionAccounts) != 2 {
		t.Fatalf("expected exactly two completions (muted, switched muted), got %v", ds.completionAccounts)
	}
}

// TestExecuteNonStreamWithRetryInvalidPowRetriesSameAccount: an
// INVALID_POW_RESPONSE envelope retries the same account with a fresh PoW and
// the unchanged payload — no account switch, no empty-output suffix.
func TestExecuteNonStreamWithRetryInvalidPowRetriesSameAccount(t *testing.T) {
	a, _, store := newManagedTestAuth(t)
	ds := &fakeDeepSeekCaller{
		sessionByAccount: true,
		responses: []*http.Response{
			invalidPowBodyResponse(),
			sseHTTPResponse(http.StatusOK, `data: {"response_message_id":21,"p":"response/content","v":"ok after fresh pow"}`),
		},
	}

	result, outErr := ExecuteNonStreamWithRetry(context.Background(), ds, a, bodyErrorTestRequest(), Options{RetryEnabled: true})
	if outErr != nil {
		t.Fatalf("unexpected output error after fresh pow: %#v", outErr)
	}
	if result.Turn.Text != "ok after fresh pow" {
		t.Fatalf("text mismatch after fresh pow: %q", result.Turn.Text)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("pow retry must stay on acc1, got %q", a.AccountID)
	}
	wantAccounts := []string{"acc1@test.com", "acc1@test.com"}
	if len(ds.completionAccounts) != len(wantAccounts) {
		t.Fatalf("completion accounts mismatch: got %v want %v", ds.completionAccounts, wantAccounts)
	}
	for i, want := range wantAccounts {
		if ds.completionAccounts[i] != want {
			t.Fatalf("completion %d = %q want %q (all=%v)", i, ds.completionAccounts[i], want, ds.completionAccounts)
		}
	}
	if got := ds.payloads[1]["prompt"]; got != "final prompt" {
		t.Fatalf("pow retry prompt = %v, want the unchanged final prompt", got)
	}
	// The standard payload always carries parent_message_id (nil for a fresh
	// completion); the pow retry must keep it nil, not chain a parent.
	if got := ds.payloads[1]["parent_message_id"]; got != nil {
		t.Fatalf("pow retry must not set parent_message_id, got %v", got)
	}
	acc, ok := store.FindAccount("acc1@test.com")
	if !ok {
		t.Fatal("expected acc1 to exist")
	}
	if !acc.IsEnabled() {
		t.Fatalf("a single pow rejection must not evict the account, reason=%q", acc.DisabledReason)
	}
}

// TestExecuteStreamWithRetryMutedBodySwitchesAccount is the stream-loop
// counterpart: the typed envelope is sniffed before the surface consumes the
// response, so the client never sees the generic empty-output chunk.
func TestExecuteStreamWithRetryMutedBodySwitchesAccount(t *testing.T) {
	a, _, _ := newManagedTestAuth(t)
	ds := &fakeDeepSeekCaller{
		sessionByAccount: true,
		responses: []*http.Response{
			sseHTTPResponse(http.StatusOK, `data: {"response_message_id":21,"p":"response/content","v":"ok from second account"}`),
		},
	}
	initial := mutedBodyResponse()
	payload := map[string]any{"prompt": "original prompt", "chat_session_id": "session-acc1@test.com"}
	consumeCalls := 0
	retryFailures := 0
	accountSwitches := []string{}

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
			consumeCalls++
			return true, false
		},
		OnRetryFailure: func(status int, message, code string) {
			retryFailures++
		},
		OnAccountSwitch: func(sessionID string) {
			accountSwitches = append(accountSwitches, sessionID)
		},
	})

	if consumeCalls != 1 {
		t.Fatalf("expected exactly one consume attempt (the healthy retry), got %d", consumeCalls)
	}
	if retryFailures != 0 {
		t.Fatalf("expected no retry failure, got %d", retryFailures)
	}
	if a.AccountID == "acc1@test.com" {
		t.Fatalf("expected lease switched off the muted account, still on %q", a.AccountID)
	}
	if len(accountSwitches) != 1 || accountSwitches[0] != "session-"+a.AccountID {
		t.Fatalf("expected one account switch to the new account session, got %v (lease=%q)", accountSwitches, a.AccountID)
	}
	if len(ds.completionAccounts) != 1 || ds.completionAccounts[0] != a.AccountID {
		t.Fatalf("expected the retry completion on the switched account, got %v", ds.completionAccounts)
	}
}

// TestExecuteStreamWithRetryInvalidPowRetriesSameAccount: the stream loop
// regenerates the PoW and retries the same account with the unchanged
// payload.
func TestExecuteStreamWithRetryInvalidPowRetriesSameAccount(t *testing.T) {
	a, _, _ := newManagedTestAuth(t)
	ds := &fakeDeepSeekCaller{
		sessionByAccount: true,
		responses: []*http.Response{
			sseHTTPResponse(http.StatusOK, `data: {"response_message_id":21,"p":"response/content","v":"ok after fresh pow"}`),
		},
	}
	initial := invalidPowBodyResponse()
	payload := map[string]any{"prompt": "original prompt", "chat_session_id": "session-acc1@test.com"}
	consumeCalls := 0
	retryFailures := 0

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
			consumeCalls++
			return true, false
		},
		OnRetryFailure: func(status int, message, code string) {
			retryFailures++
		},
	})

	if consumeCalls != 1 {
		t.Fatalf("expected exactly one consume attempt (the healthy retry), got %d", consumeCalls)
	}
	if retryFailures != 0 {
		t.Fatalf("expected no retry failure, got %d", retryFailures)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("pow retry must stay on acc1, got %q", a.AccountID)
	}
	// The initial response bypasses the fake caller; only the fresh-PoW retry
	// goes through CallCompletion.
	if len(ds.completionAccounts) != 1 || ds.completionAccounts[0] != "acc1@test.com" {
		t.Fatalf("expected the pow retry completion on acc1, got %v", ds.completionAccounts)
	}
	if got := ds.payloads[0]["prompt"]; got != "original prompt" {
		t.Fatalf("pow retry prompt = %v, want the unchanged original prompt", got)
	}
	// The standard payload always carries parent_message_id (nil for a fresh
	// completion); the pow retry must keep it nil, not chain a parent.
	if got := ds.payloads[0]["parent_message_id"]; got != nil {
		t.Fatalf("pow retry must not set parent_message_id, got %v", got)
	}
}

// TestExecuteStreamWithRetryMutedBodyTerminalReportsTypedError: when the
// mute follows to the switched account too, the typed envelope is sniffed
// before the surface consumes it, and the real status/code reach the client
// through OnRetryFailure instead of a generic empty-output chunk.
func TestExecuteStreamWithRetryMutedBodyTerminalReportsTypedError(t *testing.T) {
	a, _, _ := newManagedTestAuth(t)
	ds := &fakeDeepSeekCaller{
		sessionByAccount: true,
		responses: []*http.Response{
			mutedBodyResponse(),
		},
	}
	initial := mutedBodyResponse()
	payload := map[string]any{"prompt": "original prompt", "chat_session_id": "session-acc1@test.com"}
	consumeCalls := 0
	var failureStatus int
	var failureMessage, failureCode string

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
			consumeCalls++
			return true, false
		},
		OnRetryFailure: func(status int, message, code string) {
			failureStatus, failureMessage, failureCode = status, message, code
		},
	})

	if consumeCalls != 0 {
		t.Fatalf("expected no consume attempt (both responses were mute envelopes), got %d", consumeCalls)
	}
	if failureCode != sse.UpstreamCodeAccountMuted {
		t.Fatalf("failure code = %q, want %q", failureCode, sse.UpstreamCodeAccountMuted)
	}
	if failureStatus != http.StatusForbidden {
		t.Fatalf("failure status = %d, want 403", failureStatus)
	}
	if failureMessage == "" {
		t.Fatal("expected a typed failure message")
	}
	if a.AccountID == "acc1@test.com" {
		t.Fatalf("expected the lease to have switched before the terminal mute, still on %q", a.AccountID)
	}
}
