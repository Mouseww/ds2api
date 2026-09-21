package completionruntime

import (
	"context"
	"net/http"

	"ds2api/internal/assistantturn"
	"ds2api/internal/auth"
	"ds2api/internal/promptcompat"
)

// PrepareCompletion runs the shared completion start sequence — current-input
// file preparation, DeepSeek session creation, PoW fetch and completion
// payload assembly — without issuing the completion call itself. It is the
// exact prefix of StartCompletion, including the shared failure policy: a
// refreshed token retries the same account, a switched lease restarts the
// sequence on the new account (re-uploading the current-input file for it),
// so protocol surfaces that only need a prepared session/payload (for example
// the Vercel Node stream bridge, which performs the streaming call from Node)
// get the same transient-failure recovery as every other surface.
//
// The returned StartResult.Response is always nil because no completion call
// is made; StartCompletion fills it in on top of this same sequence.
func PrepareCompletion(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options) (StartResult, *assistantturn.OutputError) {
	policy := opts.failurePolicyOrDefault(a)
	opts.FailurePolicy = policy
	return prepareCompletionForPolicy(ctx, ds, a, stdReq, opts, policy)
}

// prepareCompletionForPolicy is the policy-driven start sequence shared by
// StartCompletion and PrepareCompletion: current-input file → CreateSession →
// GetPow → payload. Every step failure is routed through the shared failure
// policy (see failure_policy.go); when the policy cannot recover, the
// original output-error mapping is returned unchanged.
func prepareCompletionForPolicy(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options, policy *FailurePolicy) (StartResult, *assistantturn.OutputError) {
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	result := StartResult{Request: stdReq}
	for {
		prepared, uploadFailure, uploadOutErr := prepareCurrentInputFileForPolicy(ctx, ds, a, result.Request, opts)
		result.Request = prepared
		if uploadOutErr != nil {
			if retryStartAfterPolicyAction(ctx, ds, a, &result, opts, policy, uploadFailure) {
				continue
			}
			return result, uploadOutErr
		}
		sessionID, err := ds.CreateSession(ctx, a, maxAttempts)
		if err != nil {
			if retryStartAfterPolicyAction(ctx, ds, a, &result, opts, policy, requestFailureFromError(err)) {
				continue
			}
			return result, authOutputError(a)
		}
		// Preserve the session id on the PoW-failure return path: callers
		// (and the Vercel prepare contract) report which session was created
		// even when the PoW fetch fails.
		result.SessionID = sessionID
		pow, err := ds.GetPow(ctx, a, maxAttempts)
		if err != nil {
			if retryStartAfterPolicyAction(ctx, ds, a, &result, opts, policy, requestFailureFromError(err)) {
				continue
			}
			return result, &assistantturn.OutputError{Status: http.StatusUnauthorized, Message: "Failed to get PoW (invalid token or unknown error).", Code: "error"}
		}
		payload := result.Request.CompletionPayload(sessionID)
		return StartResult{SessionID: sessionID, Payload: payload, Pow: pow, Request: result.Request}, nil
	}
}
