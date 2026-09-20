package accounts

import (
	"encoding/json"
	"net/http"
	"strings"

	"ds2api/internal/config"
)

// maxBatchIdentifiers caps one batch request so a typo cannot lock the store
// for an unbounded mutation.
const maxBatchIdentifiers = 5000

type batchRequest struct {
	Identifiers []string `json:"identifiers"`
	Enabled     *bool    `json:"enabled"`
	ProxyID     *string  `json:"proxy_id"`
}

// decodeBatchRequest parses the shared batch payload and returns the
// de-duplicated identifier list. The final return is false on invalid JSON.
func decodeBatchRequest(r *http.Request) (batchRequest, []string, bool) {
	var req batchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, nil, false
	}
	ids := make([]string, 0, len(req.Identifiers))
	seen := make(map[string]bool, len(req.Identifiers))
	for _, raw := range req.Identifiers {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return req, ids, true
}

func rejectInvalidBatch(w http.ResponseWriter, ids []string) bool {
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "需要至少一个账号标识"})
		return true
	}
	if len(ids) > maxBatchIdentifiers {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "批量操作账号数量超出上限"})
		return true
	}
	return false
}

// batchIdentifierIndex indexes every way an account can be addressed (email,
// canonical mobile key, synthetic identifier) for O(1) batch matching.
type batchIdentifierIndex struct {
	requested map[string]bool
}

func newBatchIdentifierIndex(ids []string) batchIdentifierIndex {
	requested := make(map[string]bool, len(ids)*2)
	for _, id := range ids {
		requested[id] = true
		if key := config.CanonicalMobileKey(id); key != "" {
			requested[key] = true
		}
	}
	return batchIdentifierIndex{requested: requested}
}

func (b batchIdentifierIndex) matches(acc config.Account) bool {
	if acc.Email != "" && b.requested[acc.Email] {
		return true
	}
	if key := config.CanonicalMobileKey(acc.Mobile); key != "" && b.requested[key] {
		return true
	}
	if id := acc.Identifier(); id != "" && b.requested[id] {
		return true
	}
	return false
}

// missing returns the requested identifiers that no account matched, in
// request order.
func (b batchIdentifierIndex) missing(ids []string, accountKeys map[string]bool) []string {
	var missing []string
	for _, id := range ids {
		if accountKeys[id] {
			continue
		}
		if key := config.CanonicalMobileKey(id); key != "" && accountKeys[key] {
			continue
		}
		missing = append(missing, id)
	}
	return missing
}

// accountAddressKeys indexes every addressable key of the configured accounts.
func accountAddressKeys(accounts []config.Account) map[string]bool {
	keys := make(map[string]bool, len(accounts)*3)
	for _, acc := range accounts {
		if acc.Email != "" {
			keys[acc.Email] = true
		}
		if key := config.CanonicalMobileKey(acc.Mobile); key != "" {
			keys[key] = true
		}
		if id := acc.Identifier(); id != "" {
			keys[id] = true
		}
	}
	return keys
}

// batchDeleteAccounts removes every listed account in one store mutation.
func (h *Handler) batchDeleteAccounts(w http.ResponseWriter, r *http.Request) {
	_, ids, ok := decodeBatchRequest(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid json"})
		return
	}
	if rejectInvalidBatch(w, ids) {
		return
	}
	index := newBatchIdentifierIndex(ids)
	var deleted int
	var missing []string
	err := h.Store.Update(func(c *config.Config) error {
		// Snapshot the addressable keys before removal so matched accounts are
		// not reported as missing.
		keys := accountAddressKeys(c.Accounts)
		kept := make([]config.Account, 0, len(c.Accounts))
		for _, acc := range c.Accounts {
			if index.matches(acc) {
				deleted++
				continue
			}
			kept = append(kept, acc)
		}
		missing = index.missing(ids, keys)
		c.Accounts = kept
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"deleted":        deleted,
		"missing":        missing,
		"total_accounts": len(h.Store.Snapshot().Accounts),
	})
}

// batchUpdateStatus enables or disables every listed account in one store
// mutation, mirroring the single-account PUT semantics.
func (h *Handler) batchUpdateStatus(w http.ResponseWriter, r *http.Request) {
	req, ids, ok := decodeBatchRequest(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid json"})
		return
	}
	if rejectInvalidBatch(w, ids) {
		return
	}
	if req.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "需要 enabled 字段"})
		return
	}
	enabled := *req.Enabled
	index := newBatchIdentifierIndex(ids)
	var updated int
	var missing []string
	err := h.Store.Update(func(c *config.Config) error {
		for i, acc := range c.Accounts {
			if !index.matches(acc) {
				continue
			}
			c.Accounts[i].Enabled = &enabled
			if enabled {
				c.Accounts[i].DisabledReason = ""
			} else {
				c.Accounts[i].DisabledReason = "manual"
			}
			updated++
		}
		missing = index.missing(ids, accountAddressKeys(c.Accounts))
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Rebalance()
	writeJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"updated":        updated,
		"missing":        missing,
		"total_accounts": len(h.Store.Snapshot().Accounts),
	})
}

// batchUpdateProxy binds (or unbinds, with an empty proxy_id) one proxy to
// every listed account in one store mutation.
func (h *Handler) batchUpdateProxy(w http.ResponseWriter, r *http.Request) {
	req, ids, ok := decodeBatchRequest(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid json"})
		return
	}
	if rejectInvalidBatch(w, ids) {
		return
	}
	if req.ProxyID == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "需要 proxy_id 字段"})
		return
	}
	proxyID := strings.TrimSpace(*req.ProxyID)
	index := newBatchIdentifierIndex(ids)
	var updated int
	var missing []string
	err := h.Store.Update(func(c *config.Config) error {
		if proxyID != "" {
			if _, ok := findProxyByID(*c, proxyID); !ok {
				return newRequestError("代理不存在")
			}
		}
		for i, acc := range c.Accounts {
			if !index.matches(acc) {
				continue
			}
			c.Accounts[i].ProxyID = proxyID
			updated++
		}
		missing = index.missing(ids, accountAddressKeys(c.Accounts))
		if err := config.ValidateProxyConfig(c.Proxies); err != nil {
			return err
		}
		return config.ValidateAccountProxyReferences(c.Accounts, c.Proxies)
	})
	if err != nil {
		if detail, ok := requestErrorDetail(err); ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": detail})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"updated":        updated,
		"missing":        missing,
		"proxy_id":       proxyID,
		"total_accounts": len(h.Store.Snapshot().Accounts),
	})
}
