package completionruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"ds2api/internal/assistantturn"
	"ds2api/internal/auth"
	"ds2api/internal/config"
	dsclient "ds2api/internal/deepseek/client"
	"ds2api/internal/httpapi/openai/history"
	"ds2api/internal/httpapi/openai/shared"
	"ds2api/internal/promptcompat"
	"ds2api/internal/sse"
)

type DeepSeekCaller interface {
	CreateSession(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
	GetPow(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
	UploadFile(ctx context.Context, a *auth.RequestAuth, req dsclient.UploadFileRequest, maxAttempts int) (*dsclient.UploadFileResult, error)
	CallCompletion(ctx context.Context, a *auth.RequestAuth, payload map[string]any, powResp string, maxAttempts int) (*http.Response, error)
}

type Options struct {
	StripReferenceMarkers bool
	MaxAttempts           int
	RetryEnabled          bool
	RetryMaxAttempts      int
	CurrentInputFile      history.CurrentInputConfigReader

	// FailurePolicy owns every failure → lease decision for one request
	// (re-check ban / refresh token / switch account, each at most once).
	// When nil, the entry points create a fresh per-request policy; callers
	// that drive several phases of the same request pass one shared policy
	// so the budgets are not reset between phases.
	FailurePolicy *FailurePolicy
}

// failurePolicyOrDefault returns the request-scoped failure policy, creating
// a fresh one bound to the lease when the caller did not provide any.
func (o Options) failurePolicyOrDefault(a *auth.RequestAuth) *FailurePolicy {
	if o.FailurePolicy != nil {
		return o.FailurePolicy
	}
	return NewFailurePolicy(a)
}

type NonStreamResult struct {
	SessionID string
	Payload   map[string]any
	Turn      assistantturn.Turn
	Attempts  int
}

type StartResult struct {
	SessionID string
	Payload   map[string]any
	Pow       string
	Response  *http.Response
	Request   promptcompat.StandardRequest
}

// StartCompletion prepares and starts one completion attempt. The prepare
// sequence (current-input upload, session creation, PoW) is shared with
// PrepareCompletion and routed through the shared failure policy (see
// failure_policy.go): a refreshed token retries the same account, a switched
// lease restarts the sequence on the new account (re-uploading the
// current-input file for it), and everything else maps to the same output
// errors as before the consolidation.
func StartCompletion(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options) (StartResult, *assistantturn.OutputError) {
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	policy := opts.failurePolicyOrDefault(a)
	opts.FailurePolicy = policy
	result, outErr := prepareCompletionForPolicy(ctx, ds, a, stdReq, opts, policy)
	if outErr != nil {
		return result, outErr
	}
	resp, err := ds.CallCompletion(ctx, a, result.Payload, result.Pow, maxAttempts)
	if err != nil {
		return StartResult{SessionID: result.SessionID, Payload: result.Payload, Pow: result.Pow, Request: result.Request}, &assistantturn.OutputError{Status: http.StatusInternalServerError, Message: "Failed to get completion.", Code: "error"}
	}
	result.Response = resp
	return result, nil
}

// retryStartAfterPolicyAction applies the shared failure policy to one start
// step failure. It reports whether the sequence should restart (token
// refreshed on the same account, or lease switched); when the lease switched
// it re-uploads the applied current-input file for the new account first.
func retryStartAfterPolicyAction(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, result *StartResult, opts Options, policy *FailurePolicy, failure *dsclient.RequestFailure) bool {
	switch policy.Handle(ctx, failure) {
	case FailureActionRetrySameAccount:
		return true
	case FailureActionSwitchedAccount:
		reuploaded, prepErr := reuploadCurrentInputFileForAccount(ctx, ds, a, result.Request, opts)
		if prepErr != nil {
			return false
		}
		result.Request = reuploaded
		return true
	default:
		return false
	}
}

// requestFailureFromError extracts the typed *RequestFailure carried by a
// client RPC error, or nil when the error has no policy-relevant kind.
func requestFailureFromError(err error) *dsclient.RequestFailure {
	var failure *dsclient.RequestFailure
	if errors.As(err, &failure) {
		return failure
	}
	return nil
}

// prepareCurrentInputFileForPolicy is the kind-preserving variant of
// prepareCurrentInputFile used by the policy-driven start sequence: it also
// returns the typed *RequestFailure (when the upload failed with one) so the
// shared failure policy can decide the lease action.
func prepareCurrentInputFileForPolicy(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options) (promptcompat.StandardRequest, *dsclient.RequestFailure, *assistantturn.OutputError) {
	if opts.CurrentInputFile == nil || stdReq.CurrentInputFileApplied {
		return stdReq, nil, nil
	}
	out, err := (history.Service{Store: opts.CurrentInputFile, DS: ds}).ApplyCurrentInputFile(ctx, a, stdReq)
	if err != nil {
		status, message := history.MapError(err)
		return out, requestFailureFromError(err), &assistantturn.OutputError{Status: status, Message: message, Code: "error"}
	}
	return out, nil, nil
}

// StartCompletionWithAccountFallback is like StartCompletion but retries on
// another pooled account when the initial attempt fails with a retryable
// condition (429 / captcha challenge). The switch itself goes through the
// shared failure policy (ban re-check first, at most one switch per request).
// Non-retryable failures are returned unchanged, and the response body (when
// present) is preserved for callers that still need to render the error.
func StartCompletionWithAccountFallback(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options) (StartResult, *assistantturn.OutputError) {
	policy := opts.failurePolicyOrDefault(a)
	opts.FailurePolicy = policy
	start, outErr := StartCompletion(ctx, ds, a, stdReq, opts)

	retryable := false
	if outErr != nil {
		retryable = outErr.Status == http.StatusTooManyRequests
	} else if start.Response != nil && start.Response.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(start.Response.Body)
		if readErr != nil {
			config.Logger.Warn("[completion_runtime_account_switch_retry] reading initial error body failed", "surface", stdReq.Surface, "error", readErr)
		}
		if closeErr := start.Response.Body.Close(); closeErr != nil {
			config.Logger.Warn("[completion_runtime_account_switch_retry] closing initial error body failed", "surface", stdReq.Surface, "error", closeErr)
		}
		retryable = start.Response.StatusCode == http.StatusTooManyRequests || tryDetectCaptchaFromBody(body) != ""
		// Restore the body so callers can still render the failure.
		start.Response.Body = io.NopCloser(bytes.NewReader(body))
	}

	if !retryable || !opts.RetryEnabled || a == nil || !a.UseConfigToken {
		return start, outErr
	}

	var switchAttempted bool
	if !switchForRateLimited(ctx, policy, opts.RetryEnabled, &switchAttempted) {
		return start, outErr
	}
	config.Logger.Info("[completion_runtime_account_switch_retry] retrying start on alternate account", "surface", stdReq.Surface, "stream", stdReq.Stream)
	return StartCompletion(ctx, ds, a, stdReq, opts)
}

func prepareCurrentInputFile(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options) (promptcompat.StandardRequest, *assistantturn.OutputError) {
	if opts.CurrentInputFile == nil || stdReq.CurrentInputFileApplied {
		return stdReq, nil
	}
	out, err := (history.Service{Store: opts.CurrentInputFile, DS: ds}).ApplyCurrentInputFile(ctx, a, stdReq)
	if err != nil {
		status, message := history.MapError(err)
		return out, &assistantturn.OutputError{Status: status, Message: message, Code: "error"}
	}
	return out, nil
}

func ExecuteNonStreamWithRetry(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options) (NonStreamResult, *assistantturn.OutputError) {
	// One shared failure policy per request: the start phase and the
	// collect/retry phase share the re-check / refresh / switch budgets.
	opts.FailurePolicy = opts.failurePolicyOrDefault(a)
	start, startErr := StartCompletionWithAccountFallback(ctx, ds, a, stdReq, opts)
	if startErr != nil {
		return NonStreamResult{SessionID: start.SessionID, Payload: start.Payload}, startErr
	}
	return ExecuteNonStreamStartedWithRetry(ctx, ds, a, start, opts)
}

func ExecuteNonStreamStartedWithRetry(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, start StartResult, opts Options) (NonStreamResult, *assistantturn.OutputError) {
	stdReq := start.Request
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	policy := opts.failurePolicyOrDefault(a)
	sessionID := start.SessionID
	payload := start.Payload
	pow := start.Pow

	attempts := 0
	accountSwitchAttempted := false
	upstreamUnavailableAttempted := false
	currentResp := start.Response
	usagePrompt := stdReq.PromptTokenText
	accumulatedThinking := ""
	accumulatedRawThinking := ""
	accumulatedToolDetectionThinking := ""
	for {
		turn, outErr := collectAttempt(currentResp, stdReq, usagePrompt, opts)
		if outErr != nil {
			if canRetryOnAlternateAccount(ctx, policy, outErr, opts.RetryEnabled, &accountSwitchAttempted) {
				switched, switchErr := startStandardCompletionOnAlternateAccount(ctx, ds, a, stdReq, opts, maxAttempts)
				if switchErr != nil {
					return NonStreamResult{SessionID: sessionID, Payload: payload, Attempts: attempts}, switchErr
				}
				if switched.Response != nil {
					config.Logger.Info("[completion_runtime_account_switch_retry] retrying after 429", "surface", stdReq.Surface, "stream", false, "account", a.AccountID)
					sessionID = switched.SessionID
					payload = switched.Payload
					pow = switched.Pow
					currentResp = switched.Response
					usagePrompt = stdReq.PromptTokenText
					accumulatedThinking = ""
					accumulatedRawThinking = ""
					accumulatedToolDetectionThinking = ""
					continue
				}
			}
			return NonStreamResult{SessionID: sessionID, Payload: payload, Attempts: attempts}, outErr
		}
		accumulatedThinking += sse.TrimContinuationOverlap(accumulatedThinking, turn.Thinking)
		accumulatedRawThinking += sse.TrimContinuationOverlap(accumulatedRawThinking, turn.RawThinking)
		accumulatedToolDetectionThinking += sse.TrimContinuationOverlap(accumulatedToolDetectionThinking, turn.DetectionThinking)
		turn.Thinking = accumulatedThinking
		turn.RawThinking = accumulatedRawThinking
		turn.DetectionThinking = accumulatedToolDetectionThinking
		turn = assistantturn.BuildTurnFromCollected(sse.CollectResult{
			Text:                  turn.RawText,
			Thinking:              turn.RawThinking,
			ToolDetectionThinking: turn.DetectionThinking,
			ContentFilter:         turn.ContentFilter,
			CitationLinks:         turn.CitationLinks,
			ResponseMessageID:     turn.ResponseMessageID,
		}, buildOptions(stdReq, usagePrompt, opts))

		retryMax := opts.RetryMaxAttempts
		if retryMax <= 0 {
			retryMax = shared.EmptyOutputRetryMaxAttempts()
		}
		// A malformed tool-call attempt (corrupted DSML markup that parsed
		// into no tool call) retries with a corrective suffix that teaches
		// the exact format, even when visible text exists: the model
		// intended to call a tool, and a text-only response would end the
		// caller's agent turn mid-task.
		retryKind := assistantturn.RetryKindForTurn(turn, attempts, retryMax)
		if !opts.RetryEnabled || retryKind == assistantturn.RetryKindNone {
			if canRetryOnAlternateAccount(ctx, policy, turn.Error, opts.RetryEnabled, &accountSwitchAttempted) {
				switched, switchErr := startStandardCompletionOnAlternateAccount(ctx, ds, a, stdReq, opts, maxAttempts)
				if switchErr != nil {
					return NonStreamResult{SessionID: sessionID, Payload: payload, Turn: turn, Attempts: attempts}, switchErr
				}
				if switched.Response != nil {
					config.Logger.Info("[completion_runtime_account_switch_retry] retrying after 429", "surface", stdReq.Surface, "stream", false, "account", a.AccountID)
					sessionID = switched.SessionID
					payload = switched.Payload
					pow = switched.Pow
					currentResp = switched.Response
					usagePrompt = stdReq.PromptTokenText
					accumulatedThinking = ""
					accumulatedRawThinking = ""
					accumulatedToolDetectionThinking = ""
					continue
				}
			}
			if canRetryOnUpstreamUnavailable(ctx, policy, turn.Error, opts.RetryEnabled, &upstreamUnavailableAttempted) {
				switched, switchErr := startStandardCompletionOnAlternateAccount(ctx, ds, a, stdReq, opts, maxAttempts)
				if switchErr != nil {
					return NonStreamResult{SessionID: sessionID, Payload: payload, Turn: turn, Attempts: attempts}, switchErr
				}
				if switched.Response != nil {
					config.Logger.Info("[completion_runtime_upstream_unavailable_retry] retrying after upstream unavailable", "surface", stdReq.Surface, "stream", false, "account", a.AccountID)
					sessionID = switched.SessionID
					payload = switched.Payload
					pow = switched.Pow
					currentResp = switched.Response
					usagePrompt = stdReq.PromptTokenText
					accumulatedThinking = ""
					accumulatedRawThinking = ""
					accumulatedToolDetectionThinking = ""
					continue
				}
			}
			return NonStreamResult{SessionID: sessionID, Payload: payload, Turn: turn, Attempts: attempts}, turn.Error
		}

		attempts++
		config.Logger.Info("[completion_runtime_empty_retry] attempting synthetic retry", "surface", stdReq.Surface, "stream", false, "retry_attempt", attempts, "retry_kind", retryKind.String(), "parent_message_id", turn.ResponseMessageID)
		retryPow, powErr := ds.GetPow(ctx, a, maxAttempts)
		if powErr != nil {
			config.Logger.Warn("[completion_runtime_empty_retry] retry PoW fetch failed, falling back to original PoW", "surface", stdReq.Surface, "retry_attempt", attempts, "error", powErr)
			retryPow = pow
		}
		retryPayload := shared.ClonePayloadForEmptyOutputRetry(payload, turn.ResponseMessageID)
		if retryKind == assistantturn.RetryKindMalformedToolCall {
			retryPayload = shared.ClonePayloadForMalformedToolCallRetry(payload, turn.ResponseMessageID)
		}
		nextResp, err := ds.CallCompletion(ctx, a, retryPayload, retryPow, maxAttempts)
		if err != nil {
			return NonStreamResult{SessionID: sessionID, Payload: payload, Turn: turn, Attempts: attempts}, &assistantturn.OutputError{Status: http.StatusInternalServerError, Message: "Failed to get completion.", Code: "error"}
		}
		if retryKind == assistantturn.RetryKindMalformedToolCall {
			usagePrompt = shared.UsagePromptWithMalformedToolCallRetry(stdReq.PromptTokenText, attempts)
		} else {
			usagePrompt = shared.UsagePromptWithEmptyOutputRetry(usagePrompt, attempts)
		}
		currentResp = nextResp
	}
}

// canRetryOnAlternateAccount reports whether a 429-mapped rejection should
// retry on another pooled account, applying the shared failure policy: the
// rate-limited account gets a cooldown-guarded ban re-check first, then the
// guarded one-switch-per-request lease switch. attempted is set on the first
// call regardless of outcome (one-shot semantics).
func canRetryOnAlternateAccount(ctx context.Context, policy *FailurePolicy, outErr *assistantturn.OutputError, retryEnabled bool, attempted *bool) bool {
	if outErr == nil || outErr.Status != http.StatusTooManyRequests {
		return false
	}
	return switchForRateLimited(ctx, policy, retryEnabled, attempted)
}

// switchForRateLimited applies the shared failure policy to a 429 / captcha
// rejection of the completion response itself and reports whether the lease
// was switched for a fresh retry. Direct-token requests never switch (the
// policy fails them without touching the lease).
func switchForRateLimited(ctx context.Context, policy *FailurePolicy, retryEnabled bool, attempted *bool) bool {
	if !retryEnabled || attempted == nil || *attempted {
		return false
	}
	*attempted = true
	return policy.Handle(ctx, RateLimitedFailure("completion")) == FailureActionSwitchedAccount
}

// canRetryOnUpstreamUnavailable reports whether an "upstream returned no
// output" completion result should recover by refreshing the token (or
// switching accounts). It applies the failure policy's
// HandleUpstreamUnavailable recovery ladder — RecheckBan → RefreshToken →
// SwitchAccount — each at most once per request. attempted is set on the
// first call regardless of outcome (one-shot semantics).
func canRetryOnUpstreamUnavailable(ctx context.Context, policy *FailurePolicy, outErr *assistantturn.OutputError, retryEnabled bool, attempted *bool) bool {
	if outErr == nil || outErr.Code != "upstream_unavailable" {
		return false
	}
	if !retryEnabled || attempted == nil || *attempted {
		return false
	}
	*attempted = true
	return policy.HandleUpstreamUnavailable(ctx) != FailureActionFail
}

func startStandardCompletionOnAlternateAccount(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options, maxAttempts int) (StartResult, *assistantturn.OutputError) {
	var prepErr *assistantturn.OutputError
	stdReq, prepErr = reuploadCurrentInputFileForAccount(ctx, ds, a, stdReq, opts)
	if prepErr != nil {
		return StartResult{Request: stdReq}, prepErr
	}
	sessionID, err := ds.CreateSession(ctx, a, maxAttempts)
	if err != nil {
		return StartResult{}, authOutputError(a)
	}
	pow, err := ds.GetPow(ctx, a, maxAttempts)
	if err != nil {
		return StartResult{SessionID: sessionID}, &assistantturn.OutputError{Status: http.StatusUnauthorized, Message: "Failed to get PoW (invalid token or unknown error).", Code: "error"}
	}
	payload := stdReq.CompletionPayload(sessionID)
	resp, err := ds.CallCompletion(ctx, a, payload, pow, maxAttempts)
	if err != nil {
		return StartResult{SessionID: sessionID, Payload: payload, Pow: pow}, &assistantturn.OutputError{Status: http.StatusInternalServerError, Message: "Failed to get completion.", Code: "error"}
	}
	return StartResult{SessionID: sessionID, Payload: payload, Pow: pow, Response: resp, Request: stdReq}, nil
}

func reuploadCurrentInputFileForAccount(ctx context.Context, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, opts Options) (promptcompat.StandardRequest, *assistantturn.OutputError) {
	if opts.CurrentInputFile == nil || !stdReq.CurrentInputFileApplied {
		return stdReq, nil
	}
	out, err := (history.Service{Store: opts.CurrentInputFile, DS: ds}).ReuploadAppliedCurrentInputFile(ctx, a, stdReq)
	if err != nil {
		status, message := history.MapError(err)
		return out, &assistantturn.OutputError{Status: status, Message: message, Code: "error"}
	}
	return out, nil
}

func collectAttempt(resp *http.Response, stdReq promptcompat.StandardRequest, usagePrompt string, opts Options) (assistantturn.Turn, *assistantturn.OutputError) {
	defer func() {
		if err := resp.Body.Close(); err != nil {
			config.Logger.Warn("[completion_runtime] response body close failed", "surface", stdReq.Surface, "error", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		if captchaBody := tryDetectCaptchaFromBody(body); captchaBody != "" {
			config.Logger.Warn("[completion_runtime] captcha challenge detected, mapping to 429 for account switch", "surface", stdReq.Surface, "detail", captchaBody)
			return assistantturn.Turn{}, &assistantturn.OutputError{Status: http.StatusTooManyRequests, Message: "Captcha challenge detected, account may be rate-limited.", Code: "captcha_required"}
		}
		return assistantturn.Turn{}, &assistantturn.OutputError{Status: resp.StatusCode, Message: message, Code: "error"}
	}
	result := sse.CollectStream(resp, stdReq.Thinking, false)
	return assistantturn.BuildTurnFromCollected(result, buildOptions(stdReq, usagePrompt, opts)), nil
}

func buildOptions(stdReq promptcompat.StandardRequest, prompt string, opts Options) assistantturn.BuildOptions {
	return assistantturn.BuildOptions{
		Model:                 stdReq.ResponseModel,
		Prompt:                prompt,
		RefFileTokens:         stdReq.RefFileTokens,
		SearchEnabled:         stdReq.Search,
		StripReferenceMarkers: opts.StripReferenceMarkers,
		ToolNames:             stdReq.ToolNames,
		ToolsRaw:              stdReq.ToolsRaw,
		ToolChoice:            stdReq.ToolChoice,
	}
}

func authOutputError(a *auth.RequestAuth) *assistantturn.OutputError {
	if a != nil && a.UseConfigToken {
		// The shared failure policy already exhausted RecheckBan →
		// RefreshToken → SwitchAccount before this fallback is reached. Clear
		// the stored token so the next request that leases this account forces
		// a fresh login instead of retrying the same invalid credential.
		a.MarkTokenInvalid()
		return &assistantturn.OutputError{Status: http.StatusUnauthorized, Message: "Account token is invalid. Please re-login the account in admin.", Code: "error"}
	}
	return &assistantturn.OutputError{Status: http.StatusUnauthorized, Message: "Invalid token. If this should be a DS2API key, add it to config.keys first.", Code: "error"}
}

func Errorf(status int, format string, args ...any) *assistantturn.OutputError {
	return &assistantturn.OutputError{Status: status, Message: fmt.Sprintf(format, args...), Code: "error"}
}
