package account

import (
	"sort"
	"sync"

	"ds2api/internal/config"
)

type Pool struct {
	store                  *config.Store
	mu                     sync.Mutex
	queue                  []string
	standby                []string
	inUse                  map[string]int
	waiters                []chan struct{}
	maxInflightPerAccount  int
	recommendedConcurrency int
	maxQueueSize           int
	globalMaxInflight      int
	activePoolSize         int
}

func NewPool(store *config.Store) *Pool {
	maxPer := 2
	if store != nil {
		maxPer = store.RuntimeAccountMaxInflight()
	}
	p := &Pool{
		store:                 store,
		inUse:                 map[string]int{},
		maxInflightPerAccount: maxPer,
	}
	p.Reset()
	return p
}

// Reset rebuilds the active queue from the store and resets all runtime
// accounting. It is used on startup and after account add/delete.
func (p *Pool) Reset() {
	if p.store != nil {
		p.maxInflightPerAccount = p.store.RuntimeAccountMaxInflight()
	} else {
		p.maxInflightPerAccount = maxInflightFromEnv()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rebuildLocked()
	p.drainWaitersLocked()
	p.inUse = map[string]int{}
	config.Logger.Info(
		"[init_account_queue] initialized",
		"total", len(p.queue),
		"active_pool_size", p.activePoolSize,
		"standby", len(p.standby),
		"max_inflight_per_account", p.maxInflightPerAccount,
		"global_max_inflight", p.globalMaxInflight,
		"recommended_concurrency", p.recommendedConcurrency,
		"max_queue_size", p.maxQueueSize,
	)
}

// Rebalance recomputes the active/standby split after a runtime change
// (enable/disable toggle, ban eviction, active_pool_size change) without
// dropping in-flight accounting. Waiting acquirers are re-notified.
func (p *Pool) Rebalance() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rebuildLocked()
	p.drainWaitersLocked()
}

// RemoveAccount evicts a single account from the active set immediately. It is
// used by ban detection to stop allocating new requests to a banned account
// while allowing its in-flight requests to finish and release naturally.
func (p *Pool) RemoveAccount(accountID string) {
	if accountID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// Sanity check: this is only meaningful once the store has already
	// marked the account disabled/banned. If not, rebuilding is a no-op
	// (the account would simply be re-selected), so surface a warning
	// instead of silently rebuilding.
	if p.store != nil {
		if acc, ok := p.store.FindAccount(accountID); ok && acc.IsEnabled() && !acc.IsBanned() {
			config.Logger.Warn(
				"[pool] RemoveAccount called for an account that is still enabled",
				"account", accountID,
			)
		}
	}
	p.rebuildLocked()
	p.drainWaitersLocked()
}

// rebuildLocked recomputes the active queue and derived concurrency limits.
// It must be called with p.mu held.
func (p *Pool) rebuildLocked() {
	p.rebuildQueueLocked()
	p.recomputeLimitsLocked()
}

// rebuildQueueLocked selects eligible accounts (enabled && not banned) in the
// existing stable token-first order, keeps the first activePoolSize of them as
// the active queue and tracks the rest as standby. Must be called with p.mu.
func (p *Pool) rebuildQueueLocked() {
	p.activePoolSize = 0
	if p.store != nil {
		p.activePoolSize = p.store.RuntimeActivePoolSize()
	}
	candidates := p.eligibleAccountsLocked()
	n := len(candidates)
	if p.activePoolSize > 0 && p.activePoolSize < n {
		n = p.activePoolSize
	}
	queue := make([]string, 0, n)
	standby := make([]string, 0, len(candidates)-n)
	for i, acc := range candidates {
		id := acc.Identifier()
		if id == "" {
			continue
		}
		if i < n {
			queue = append(queue, id)
		} else {
			standby = append(standby, id)
		}
	}
	p.queue = queue
	p.standby = standby
}

// eligibleAccountsLocked returns enabled, non-banned accounts in the existing
// stable token-first ordering. Must be called with p.mu held.
func (p *Pool) eligibleAccountsLocked() []config.Account {
	var accounts []config.Account
	if p.store != nil {
		accounts = p.store.Accounts()
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		iHas := accounts[i].Token != ""
		jHas := accounts[j].Token != ""
		if iHas == jHas {
			return i < j
		}
		return iHas
	})
	out := make([]config.Account, 0, len(accounts))
	for _, acc := range accounts {
		if acc.Identifier() == "" || !acc.IsEnabled() || acc.IsBanned() {
			continue
		}
		out = append(out, acc)
	}
	return out
}

// recomputeLimitsLocked derives the concurrency limits from the active queue
// size. Must be called with p.mu held.
func (p *Pool) recomputeLimitsLocked() {
	recommended := defaultRecommendedConcurrency(len(p.queue), p.maxInflightPerAccount)
	queueLimit := maxQueueFromEnv(recommended)
	globalLimit := recommended
	if p.store != nil {
		queueLimit = p.store.RuntimeAccountMaxQueue(recommended)
		globalLimit = p.store.RuntimeGlobalMaxInflight(recommended)
	}
	p.recommendedConcurrency = recommended
	p.maxQueueSize = queueLimit
	p.globalMaxInflight = globalLimit
}

func (p *Pool) Release(accountID string) {
	if accountID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	count := p.inUse[accountID]
	if count <= 0 {
		return
	}
	if count == 1 {
		delete(p.inUse, accountID)
		p.notifyWaiterLocked()
		return
	}
	p.inUse[accountID] = count - 1
	p.notifyWaiterLocked()
}

func (p *Pool) Status() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	available := make([]string, 0, len(p.queue))
	inUseAccounts := make([]string, 0, len(p.inUse))
	inUseSlots := 0
	for _, id := range p.queue {
		if p.inUse[id] < p.maxInflightPerAccount {
			available = append(available, id)
		}
	}
	for id, count := range p.inUse {
		if count > 0 {
			inUseAccounts = append(inUseAccounts, id)
			inUseSlots += count
		}
	}
	sort.Strings(inUseAccounts)

	bannedCount := 0
	disabledCount := 0
	total := 0
	if p.store != nil {
		for _, acc := range p.store.Accounts() {
			total++
			if acc.IsBanned() {
				bannedCount++
			}
			if !acc.IsEnabled() {
				disabledCount++
			}
		}
	}
	return map[string]any{
		"available":                len(available),
		"in_use":                   inUseSlots,
		"total":                    total,
		"available_accounts":       available,
		"in_use_accounts":          inUseAccounts,
		"max_inflight_per_account": p.maxInflightPerAccount,
		"global_max_inflight":      p.globalMaxInflight,
		"recommended_concurrency":  p.recommendedConcurrency,
		"waiting":                  len(p.waiters),
		"max_queue_size":           p.maxQueueSize,
		"active_pool_size":         p.activePoolSize,
		"active_pool_count":        len(p.queue),
		"standby_count":            len(p.standby),
		"banned_count":             bannedCount,
		"disabled_count":           disabledCount,
	}
}
