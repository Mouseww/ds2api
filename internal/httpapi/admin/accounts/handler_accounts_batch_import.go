package accounts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"ds2api/internal/config"
	adminshared "ds2api/internal/httpapi/admin/shared"
)

const maxBatchImportAccounts = 5000

// batchImportRequest is the payload for POST /admin/accounts/batch-import.
type batchImportRequest struct {
	// Mode controls how collisions with existing accounts are handled.
	//   "skip"      (default): leave the existing account untouched.
	//   "overwrite": replace the existing account's fields with the imported values.
	//   "error"    : report the collision as an error entry; existing account untouched.
	Mode string `json:"mode"`
	// Accounts is the list of account objects to import. Each accepts:
	// name, remark, email, mobile, password, token, user_id, device_id,
	// x_device_id, proxy_id. At least one of email / mobile is required.
	Accounts []map[string]any `json:"accounts"`
}

// batchImportResultEntry is the per-account outcome.
type batchImportResultEntry struct {
	Index      int    `json:"index"`
	Identifier string `json:"identifier,omitempty"`
	Status     string `json:"status"` // created | updated | skipped | error
	Reason     string `json:"reason,omitempty"`
}

type batchImportResponse struct {
	Success       bool                     `json:"success"`
	Mode          string                   `json:"mode"`
	Created       int                      `json:"created"`
	Updated       int                      `json:"updated"`
	Skipped       int                      `json:"skipped"`
	Errors        int                      `json:"errors"`
	TotalAccounts int                      `json:"total_accounts"`
	Results       []batchImportResultEntry `json:"results"`
}

// batchImportAccounts handles POST /admin/accounts/batch-import.
//
// It is an admin-authenticated endpoint intended for external callers (scripts,
// migration tooling) to upload many accounts in one call, preserving credentials
// such as token / user_id / device_id / x_device_id.
func (h *Handler) batchImportAccounts(w http.ResponseWriter, r *http.Request) {
	var req batchImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid JSON body: " + err.Error()})
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "skip"
	}
	if mode != "skip" && mode != "overwrite" && mode != "error" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid mode: must be one of skip, overwrite, error"})
		return
	}
	if len(req.Accounts) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "accounts must be a non-empty array"})
		return
	}
	if len(req.Accounts) > maxBatchImportAccounts {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": fmt.Sprintf("accounts exceeds maximum of %d", maxBatchImportAccounts)})
		return
	}

	// Normalize + validate every entry up front, before touching the store.
	type preparedEntry struct {
		index int
		acc   config.Account
	}
	prepared := make([]preparedEntry, 0, len(req.Accounts))
	results := make([]batchImportResultEntry, 0, len(req.Accounts))
	for i, raw := range req.Accounts {
		acc := adminshared.NormalizeAccountForStorage(adminshared.ToAccount(raw))
		identifier := acc.Identifier()
		if identifier == "" {
			results = append(results, batchImportResultEntry{
				Index:  i,
				Status: "error",
				Reason: "email or mobile is required",
			})
			continue
		}
		// Optional proxy_id must reference an existing proxy when provided.
		if acc.ProxyID != "" {
			if _, ok := findProxyByID(h.Store.Snapshot(), acc.ProxyID); !ok {
				results = append(results, batchImportResultEntry{
					Index:      i,
					Identifier: identifier,
					Status:     "error",
					Reason:     "proxy_id not found",
				})
				continue
			}
		}
		prepared = append(prepared, preparedEntry{index: i, acc: acc})
	}

	updated := 0
	created := 0
	err := h.Store.Update(func(c *config.Config) error {
		// Re-read under the write lock: the proxy existence check above used a
		// snapshot, but accounts themselves must be mutated atomically here.
		existingByDedupe := make(map[string]int, len(c.Accounts))
		for i, acc := range c.Accounts {
			if key := adminshared.AccountDedupeKey(acc); key != "" {
				existingByDedupe[key] = i
			}
		}

		for _, p := range prepared {
			acc := p.acc
			identifier := acc.Identifier()
			dedupe := adminshared.AccountDedupeKey(acc)
			idx, exists := existingByDedupe[dedupe]
			if !exists {
				c.Accounts = append(c.Accounts, acc)
				existingByDedupe[dedupe] = len(c.Accounts) - 1
				results = append(results, batchImportResultEntry{
					Index:      p.index,
					Identifier: identifier,
					Status:     "created",
				})
				created++
				continue
			}

			switch mode {
			case "overwrite":
				existing := c.Accounts[idx]
				// Preserve server-managed runtime flags; only overwrite identity/credential fields.
				acc.Enabled = existing.Enabled
				acc.DisabledReason = existing.DisabledReason
				acc.BanIsMuted = existing.BanIsMuted
				acc.BanMuteUntil = existing.BanMuteUntil
				acc.BanStatus = existing.BanStatus
				c.Accounts[idx] = acc
				results = append(results, batchImportResultEntry{
					Index:      p.index,
					Identifier: identifier,
					Status:     "updated",
				})
				updated++
			case "error":
				results = append(results, batchImportResultEntry{
					Index:      p.index,
					Identifier: identifier,
					Status:     "error",
					Reason:     "account already exists",
				})
			default: // skip
				results = append(results, batchImportResultEntry{
					Index:      p.index,
					Identifier: identifier,
					Status:     "skipped",
					Reason:     "account already exists",
				})
			}
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}

	h.Pool.Reset()

	skipped := 0
	errCount := 0
	for _, r := range results {
		switch r.Status {
		case "skipped":
			skipped++
		case "error":
			errCount++
		}
	}

	writeJSON(w, http.StatusOK, batchImportResponse{
		Success:       errCount == 0,
		Mode:          mode,
		Created:       created,
		Updated:       updated,
		Skipped:       skipped,
		Errors:        errCount,
		TotalAccounts: len(h.Store.Snapshot().Accounts),
		Results:       results,
	})
}
