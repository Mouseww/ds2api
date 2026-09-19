package accounts

import (
	"ds2api/internal/config"
)

// applyLoginBanState inspects the ban fields a token refresh just persisted and
// reacts to a banned account: it is auto-disabled (reason "banned") and evicted
// from the rotation pool so no further request is allocated to it.
//
// It reports the refreshed account and whether it is banned. Callers use the
// return value to render the test result as a warning instead of a success.
func (h *Handler) applyLoginBanState(identifier string) (config.Account, bool) {
	acc, ok := h.Store.FindAccount(identifier)
	if !ok {
		return config.Account{}, false
	}
	if !acc.IsBanned() {
		return acc, false
	}
	if acc.IsEnabled() {
		if err := h.Store.SetAccountEnabled(identifier, false, "banned"); err != nil {
			config.Logger.Warn(
				"[accounts] auto-disable banned account failed",
				"account", identifier,
				"error", err,
			)
		}
	}
	// Rebalance even when the account was already disabled: a stale queue entry
	// must not keep receiving requests.
	h.Pool.Rebalance()
	config.Logger.Info(
		"[accounts] banned account auto-disabled after token refresh",
		"account", identifier,
		"ban_is_muted", acc.BanIsMuted,
		"ban_mute_until", acc.BanMuteUntil,
		"ban_status", acc.BanStatus,
	)
	return acc, true
}

// markBannedResult fills the test payload for an account whose token refresh
// succeeded but which DeepSeek reports as banned. The result is deliberately
// not a success: the account is unusable until it is unbanned and re-enabled.
func markBannedResult(result map[string]any, acc config.Account, responseTimeMs int) {
	result["success"] = false
	result["banned"] = true
	result["ban_is_muted"] = acc.BanIsMuted
	result["ban_mute_until"] = acc.BanMuteUntil
	result["ban_status"] = acc.BanStatus
	result["response_time"] = responseTimeMs
	result["message"] = "账号已被封禁（muted），已自动禁用并移出轮换池"
}
