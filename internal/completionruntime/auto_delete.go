package completionruntime

import (
	"context"
	"time"

	"ds2api/internal/auth"
	"ds2api/internal/config"
	dsclient "ds2api/internal/deepseek/client"
)

// SessionDeleter is the upstream surface the shared auto-delete cleanup needs.
// It is satisfied by the DeepSeek client and by the OpenAI shared DeepSeekCaller
// interface.
type SessionDeleter interface {
	DeleteSessionForToken(ctx context.Context, token string, sessionID string) (*dsclient.DeleteSessionResult, error)
	DeleteAllSessionsForToken(ctx context.Context, token string) error
}

// AutoDeleteModeReader supplies the runtime auto_delete_sessions preference.
type AutoDeleteModeReader interface {
	AutoDeleteMode() string
}

// AutoDeleteRemoteSession deletes the upstream DeepSeek session(s) used by one
// request according to the configured auto_delete_sessions mode. It is the
// shared cleanup path for every protocol surface (chat, responses, Claude,
// Gemini): "single" removes the session created for this request, "all"
// clears every session on the leased account.
//
// Failures are logged, never propagated: cleanup must not turn an already
// delivered response into an error. The delete runs on a detached context so a
// cancelled client disconnect does not abort it mid-flight.
func AutoDeleteRemoteSession(ctx context.Context, ds SessionDeleter, store AutoDeleteModeReader, a *auth.RequestAuth, sessionID string) {
	if ds == nil || store == nil || a == nil {
		return
	}
	mode := store.AutoDeleteMode()
	if mode == "" || mode == "none" || a.DeepSeekToken == "" {
		return
	}

	deleteBaseCtx := context.WithoutCancel(ctx)
	deleteCtx, cancel := context.WithTimeout(deleteBaseCtx, 10*time.Second)
	defer cancel()

	switch mode {
	case "single":
		if sessionID == "" {
			config.Logger.Warn("[auto_delete_sessions] skipped single-session delete because session_id is empty", "account", a.AccountID)
			return
		}
		if _, err := ds.DeleteSessionForToken(deleteCtx, a.DeepSeekToken, sessionID); err != nil {
			config.Logger.Warn("[auto_delete_sessions] failed", "account", a.AccountID, "mode", mode, "session_id", sessionID, "error", err)
			return
		}
		config.Logger.Debug("[auto_delete_sessions] success", "account", a.AccountID, "mode", mode, "session_id", sessionID)
	case "all":
		if err := ds.DeleteAllSessionsForToken(deleteCtx, a.DeepSeekToken); err != nil {
			config.Logger.Warn("[auto_delete_sessions] failed", "account", a.AccountID, "mode", mode, "error", err)
			return
		}
		config.Logger.Debug("[auto_delete_sessions] success", "account", a.AccountID, "mode", mode)
	default:
		config.Logger.Warn("[auto_delete_sessions] unknown mode", "account", a.AccountID, "mode", mode)
	}
}
