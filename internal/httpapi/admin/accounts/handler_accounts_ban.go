package accounts

import (
	"ds2api/internal/config"
)

// applyLoginBanState inspects the ban fields a token refresh just persisted and
// reconciles the account's enabled flag: a banned account is auto-disabled
// (reason "banned") and evicted from the rotation pool, while a previously
// banned account whose mute has lapsed is re-enabled.
//
// It reports the refreshed account and whether it is banned. Callers use the
// return value to render the test result as a warning instead of a success.
func (h *Handler) applyLoginBanState(identifier string) (config.Account, bool) {
	acc, ok := h.Store.FindAccount(identifier)
	if !ok {
		return config.Account{}, false
	}
	if !acc.IsBanned() {
		// Ban lifted: restore an account that was auto-disabled by ban
		// detection. Manual disables and already-enabled accounts are skipped.
		if !acc.IsEnabled() && acc.DisabledReason == "banned" {
			if err := h.Store.SetAccountEnabled(identifier, true, ""); err != nil {
				config.Logger.Warn(
					"[accounts] re-enable unbanned account failed",
					"account", identifier,
					"error", err,
				)
			} else {
				h.Pool.Rebalance()
				config.Logger.Info(
					"[accounts] unbanned account re-enabled after token refresh",
					"account", identifier,
					"ban_is_muted", acc.BanIsMuted,
					"ban_mute_until", acc.BanMuteUntil,
					"ban_status", acc.BanStatus,
				)
			}
		}
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
