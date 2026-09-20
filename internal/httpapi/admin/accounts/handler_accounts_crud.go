package accounts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"ds2api/internal/config"
	"ds2api/internal/usagestats"
)

func (h *Handler) addAccount(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	acc := toAccount(req)
	if acc.Identifier() == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "需要 email 或 mobile"})
		return
	}
	err := h.Store.Update(func(c *config.Config) error {
		if acc.ProxyID != "" {
			if _, ok := findProxyByID(*c, acc.ProxyID); !ok {
				return fmt.Errorf("代理不存在")
			}
		}
		mobileKey := config.CanonicalMobileKey(acc.Mobile)
		for _, a := range c.Accounts {
			if acc.Email != "" && a.Email == acc.Email {
				return fmt.Errorf("邮箱已存在")
			}
			if mobileKey != "" && config.CanonicalMobileKey(a.Mobile) == mobileKey {
				return fmt.Errorf("手机号已存在")
			}
		}
		c.Accounts = append(c.Accounts, acc)
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "total_accounts": len(h.Store.Snapshot().Accounts)})
}

func (h *Handler) updateAccount(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if decoded, err := url.PathUnescape(identifier); err == nil {
		identifier = decoded
	}

	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid json"})
		return
	}
	name, nameOK := fieldStringOptional(req, "name")
	remark, remarkOK := fieldStringOptional(req, "remark")
	enabledVal, enabledOK := fieldBool(req, "enabled")

	err := h.Store.Update(func(c *config.Config) error {
		for i, acc := range c.Accounts {
			if !accountMatchesIdentifier(acc, identifier) {
				continue
			}
			if nameOK {
				c.Accounts[i].Name = name
			}
			if remarkOK {
				c.Accounts[i].Remark = remark
			}
			if enabledOK {
				c.Accounts[i].Enabled = &enabledVal
				if enabledVal {
					c.Accounts[i].DisabledReason = ""
				} else {
					c.Accounts[i].DisabledReason = "manual"
				}
			}
			return nil
		}
		return newRequestError("账号不存在")
	})
	if err != nil {
		if detail, ok := requestErrorDetail(err); ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": detail})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if enabledOK {
		h.Pool.Rebalance()
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "total_accounts": len(h.Store.Snapshot().Accounts)})
}

func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if decoded, err := url.PathUnescape(identifier); err == nil {
		identifier = decoded
	}
	err := h.Store.Update(func(c *config.Config) error {
		idx := -1
		for i, a := range c.Accounts {
			if accountMatchesIdentifier(a, identifier) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("账号不存在")
		}
		c.Accounts = append(c.Accounts[:idx], c.Accounts[idx+1:]...)
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "total_accounts": len(h.Store.Snapshot().Accounts)})
}

func fieldBool(m map[string]any, key string) (bool, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return false, false
	}
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		return strings.ToLower(strings.TrimSpace(x)) == "true", true
	default:
		return false, false
	}
}

// dailyLimitReached reports whether the quota-window usage has hit either
// configured per-account budget. A zero limit disables that metric.
func dailyLimitReached(windowUsage usagestats.AccountUsage, tokenLimit, requestLimit int64) bool {
	if tokenLimit > 0 && windowUsage.TotalTokens >= tokenLimit {
		return true
	}
	if requestLimit > 0 && windowUsage.Requests >= requestLimit {
		return true
	}
	return false
}
