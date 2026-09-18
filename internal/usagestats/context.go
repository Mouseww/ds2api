package usagestats

import (
	"context"
	"strings"
	"sync"
)

// recorderCtxKey is the private context key for the per-request Recorder.
type recorderCtxKey struct{}

// Recorder accumulates per-request annotations until the middleware commits
// the resulting event. Every method is safe for concurrent use.
type Recorder struct {
	mu    sync.Mutex
	usage Usage
}

// annotate merges non-zero fields from u into the recorder.
func (r *Recorder) annotate(u Usage) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if model := strings.TrimSpace(u.Model); model != "" {
		r.usage.Model = model
	}
	if account := strings.TrimSpace(u.AccountID); account != "" {
		r.usage.AccountID = account
	}
	if caller := strings.TrimSpace(u.CallerID); caller != "" {
		r.usage.CallerID = caller
	}
	if u.PromptTokens != 0 {
		r.usage.PromptTokens = u.PromptTokens
	}
	if u.CompletionTokens != 0 {
		r.usage.CompletionTokens = u.CompletionTokens
	}
	if u.ReasoningTokens != 0 {
		r.usage.ReasoningTokens = u.ReasoningTokens
	}
	if u.TotalTokens != 0 {
		r.usage.TotalTokens = u.TotalTokens
	}
	if u.Stream {
		r.usage.Stream = true
	}
}

// snapshot copies the accumulated usage.
func (r *Recorder) snapshot() Usage {
	if r == nil {
		return Usage{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.usage
}

// WithRecorder attaches a recorder to ctx. Only the middleware needs this.
func WithRecorder(ctx context.Context, recorder *Recorder) context.Context {
	if ctx == nil || recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, recorderCtxKey{}, recorder)
}

// RecorderFromContext returns the recorder attached to ctx, if any.
func RecorderFromContext(ctx context.Context) (*Recorder, bool) {
	if ctx == nil {
		return nil, false
	}
	recorder, ok := ctx.Value(recorderCtxKey{}).(*Recorder)
	if !ok || recorder == nil {
		return nil, false
	}
	return recorder, true
}

// Annotate merges handler-side usage knowledge into the request recorder. It
// reports false when the request was not instrumented, so callers can invoke it
// unconditionally.
func Annotate(ctx context.Context, usage Usage) bool {
	recorder, ok := RecorderFromContext(ctx)
	if !ok {
		return false
	}
	recorder.annotate(usage)
	return true
}

// AnnotateUsage merges token counts extracted from a protocol-specific usage
// object, together with the identity fields the handler already knows.
func AnnotateUsage(ctx context.Context, model, accountID, callerID string, usageMap map[string]any) bool {
	usage := UsageFromMap(usageMap)
	usage.Model = model
	usage.AccountID = accountID
	usage.CallerID = callerID
	return Annotate(ctx, usage)
}
