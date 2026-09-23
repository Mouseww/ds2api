package completionruntime

import (
	"context"

	"ds2api/internal/auth"
	dsclient "ds2api/internal/deepseek/client"
)

// CALL-SITE MAP — where "failure → lease action" used to live before this
// module consolidated it (see docs/ban-detection-load-pool-spec.md R2.1):
//
//  1. internal/deepseek/client/client_auth.go — CreateSession and
//     GetPowForTarget retry loops called RecheckBan + RefreshToken +
//     SwitchAccount internally on captcha / 429 / auth failures.
//  2. internal/deepseek/client/client_upload.go — UploadFile called
//     RefreshToken + SwitchAccount (no RecheckBan) on auth failures.
//  3. internal/deepseek/client/client_session.go (GetSessionCount) and
//     client_session_delete.go (DeleteSession) — RefreshToken + SwitchAccount
//     (no RecheckBan) on auth failures.
//  4. internal/completionruntime/nonstream.go —
//     StartCompletionWithAccountFallback and ExecuteNonStreamStartedWithRetry
//     mapped 429 / captcha bodies to one SwitchAccount + fresh start
//     (no RecheckBan) via canRetryOnAlternateAccount.
//  5. internal/completionruntime/stream_retry.go — ExecuteStreamWithRetry did
//     the same for streams via canRetryOnAlternateAccount.
//  6. internal/httpapi/openai/chat/vercel_stream.go — handleVercelStreamSwitch
//     calls a.SwitchAccount directly (kept as-is; one switch per lease is now
//     enforced by the auth-layer guard).
//
// After the consolidation the DeepSeek client RPCs are policy-free: they
// return typed *dsclient.RequestFailure and never mutate the lease. This
// module is the single place that turns a failure into a lease action, using
// auth.Resolver.RecheckBan (cooldown-guarded), the singleflight RefreshToken
// and SwitchAccount — each at most once per request.

// FailureAction is the lease decision the shared failure policy made for one
// upstream failure.
type FailureAction int

const (
	// FailureActionFail means no lease action is possible or useful: the
	// caller must surface the failure.
	FailureActionFail FailureAction = iota
	// FailureActionRetrySameAccount means the token was refreshed in place
	// (ban re-check login or explicit refresh); retry on the same account.
	FailureActionRetrySameAccount
	// FailureActionSwitchedAccount means the lease moved to another pooled
	// account; retry once with the fresh lease.
	FailureActionSwitchedAccount
)

// FailurePolicy owns all failure → lease decisions for ONE request. Create a
// single policy per request (see Options.FailurePolicy) so that RecheckBan,
// RefreshToken and SwitchAccount each happen at most once across the whole
// start/retry lifecycle; the auth layer additionally guarantees at most one
// successful SwitchAccount per *auth.RequestAuth.
type FailurePolicy struct {
	a *auth.RequestAuth

	recheckDone bool
	refreshDone bool
}

// NewFailurePolicy returns a request-scoped failure policy for the given
// lease. A nil or non-managed (direct-token) lease never mutates.
func NewFailurePolicy(a *auth.RequestAuth) *FailurePolicy {
	return &FailurePolicy{a: a}
}

// Handle maps one typed upstream failure to a lease action:
//
//   - direct-token (non-managed) requests never mutate the lease and fail;
//   - captcha / rate-limit failures re-check the ban state (cooldown-guarded
//     login that also auto-disables banned accounts) and then switch;
//   - managed auth failures first re-check the ban (whose login refreshes the
//     token), then explicitly refresh the token, and only then switch;
//   - anything else fails without touching the lease.
//
// Handle returns FailureActionFail for a nil policy or nil failure.
func (p *FailurePolicy) Handle(ctx context.Context, failure *dsclient.RequestFailure) FailureAction {
	if p == nil || p.a == nil || !p.a.UseConfigToken || failure == nil {
		return FailureActionFail
	}
	switch failure.Kind {
	case dsclient.FailureCaptchaRequired, dsclient.FailureRateLimited:
		p.recheckOnce(ctx)
		return p.switchOnce(ctx)
	case dsclient.FailureManagedUnauthorized:
		if !p.recheckDone {
			p.recheckDone = true
			if _, refreshed := p.a.RecheckBan(ctx); refreshed {
				// The re-check login already refreshed the token in place.
				return FailureActionRetrySameAccount
			}
		}
		if !p.refreshDone {
			p.refreshDone = true
			if p.a.RefreshToken(ctx) {
				return FailureActionRetrySameAccount
			}
		}
		return p.switchOnce(ctx)
	default:
		return FailureActionFail
	}
}

// HandleUpstreamUnavailable maps an "upstream returned no output" completion
// result (assistantturn code "upstream_unavailable", not a typed
// *RequestFailure) to a lease action. A stale token can route a managed
// account to a degraded backend that returns empty completions, so the same
// recovery ladder used for managed auth failures applies: ban re-check (its
// login refreshes the token), explicit token refresh, then switch. It shares
// the recheckDone/refreshDone budgets with Handle so the ladder never runs
// twice for one request.
func (p *FailurePolicy) HandleUpstreamUnavailable(ctx context.Context) FailureAction {
	if p == nil || p.a == nil || !p.a.UseConfigToken {
		return FailureActionFail
	}
	if !p.recheckDone {
		p.recheckDone = true
		if _, refreshed := p.a.RecheckBan(ctx); refreshed {
			return FailureActionRetrySameAccount
		}
	}
	if !p.refreshDone {
		p.refreshDone = true
		if p.a.RefreshToken(ctx) {
			return FailureActionRetrySameAccount
		}
	}
	return p.switchOnce(ctx)
}

func (p *FailurePolicy) recheckOnce(ctx context.Context) {
	if p.recheckDone {
		return
	}
	p.recheckDone = true
	p.a.RecheckBan(ctx)
}

func (p *FailurePolicy) switchOnce(ctx context.Context) FailureAction {
	if p.a.SwitchAccount(ctx) {
		return FailureActionSwitchedAccount
	}
	return FailureActionFail
}

// HandleUpstreamFailure is the one-shot form of FailurePolicy.Handle for
// callers that do not keep request-scoped policy state.
func HandleUpstreamFailure(ctx context.Context, a *auth.RequestAuth, failure *dsclient.RequestFailure) FailureAction {
	return NewFailurePolicy(a).Handle(ctx, failure)
}

// RateLimitedFailure builds the synthetic failure used when the completion
// HTTP response itself is rejected with 429 or a captcha body (no typed
// RequestFailure exists on that path).
func RateLimitedFailure(op string) *dsclient.RequestFailure {
	return &dsclient.RequestFailure{Op: op, Kind: dsclient.FailureRateLimited, Message: "account rate-limited"}
}
