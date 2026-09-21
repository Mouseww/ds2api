package account

import (
	"sort"
	"sync"
	"time"

	"ds2api/internal/config"
)

type Pool struct {
	store                  *config.Store
	mu                     sync.Mutex
	queue                  []string
	queuePos               map[string]int
	queueCursor            int
	standby                []string
	inUse                  map[string]int
	totalInUse             int
	waiters                int
	wakeCh                 chan struct{}
	maxInflightPerAccount  int
	recommendedConcurrency int
	maxQueueSize           int
	globalMaxInflight      int
	activePoolSize         int

	// Quota enforcement: the provider reports per-account usage for the
	// configured rolling window, cached briefly so the acquire path stays
	// cheap. The provider is always invoked without p.mu held; dailyUsageMu
	// serializes refreshes so an expired cache triggers a single read.
	dailyUsageProvider DailyUsageProvider
	dailyUsageMu       sync.Mutex
	dailyUsageCache    map[string]DailyUsage
	dailyUsageCachedAt time.Time
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
	usage := p.usageSnapshot()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rebuildLocked(usage)
	p.inUse = map[string]int{}
	p.totalInUse = 0
	p.notifyWaitersLocked()
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
	usage := p.usageSnapshot()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rebuildLocked(usage)
	p.notifyWaitersLocked()
}

// RemoveAccount evicts a single account from the active set immediately. It is
// used by ban detection to stop allocating new requests to a banned account
// while allowing its in-flight requests to finish and release naturally.
func (p *Pool) RemoveAccount(accountID string) {
	if accountID == "" {
		return
	}
	usage := p.usageSnapshot()
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
	p.rebuildLocked(usage)
	p.notifyWaitersLocked()
}

// rebuildLocked recomputes the active queue and derived concurrency limits.
// The usage snapshot must be taken before p.mu is acquired (usageSnapshot
// briefly takes p.mu itself), which is why callers capture it first and pass
// it in. It must be called with p.mu held.
func (p *Pool) rebuildLocked(usage map[string]DailyUsage) {
	p.rebuildQueueLocked(usage)
	p.recomputeLimitsLocked()
}

// rebuildQueueLocked selects eligible accounts (enabled && not banned) in the
// existing stable token-first order, keeps the first activePoolSize of them as
// the active queue and tracks the rest as standby. It also rebuilds the
// queuePos index that bumpQueue uses for its O(1) round-robin cursor move.
// Must be called with p.mu.
func (p *Pool) rebuildQueueLocked(usage map[string]DailyUsage) {
	p.activePoolSize = 0
	if p.store != nil {
		p.activePoolSize = p.store.RuntimeActivePoolSize()
	}
	candidates := p.eligibleAccountsLocked(usage)
	n := len(candidates)
	if p.activePoolSize > 0 && p.activePoolSize < n {
		n = p.activePoolSize
	}
	queue := make([]string, 0, n)
	standby := make([]string, 0, len(candidates)-n)
	queuePos := make(map[string]int, n)
	for _, acc := range candidates {
		id := acc.Identifier()
		if id == "" {
			continue
		}
		if len(queue) < n {
			queuePos[id] = len(queue)
			queue = append(queue, id)
		} else {
			standby = append(standby, id)
		}
	}
	p.queue = queue
	p.queuePos = queuePos
	p.standby = standby
	if p.queueCursor < 0 || p.queueCursor >= len(queue) {
		p.queueCursor = 0
	}
}

// eligibleAccountsLocked returns enabled, non-banned accounts that have not
// exhausted their quota inside the configured window, in the existing stable
// token-first ordering. Excluding over-quota accounts here is what makes a
// standby account take the place of an active one that reached its limit.
// The usage snapshot is supplied by the caller so the provider is never read
// under p.mu. Must be called with p.mu held.
func (p *Pool) eligibleAccountsLocked(usage map[string]DailyUsage) []config.Account {
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
		if p.accountDailyLimitedLocked(usage, acc.Identifier()) {
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

// Release returns one in-flight slot for accountID. It goes through
// releaseSlotLocked so the per-account counter and the O(1) global total
// (totalInUse) can never drift apart, and broadcasts to parked acquirers so
// any waiter that can now make progress wakes up.
func (p *Pool) Release(accountID string) {
	if accountID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.releaseSlotLocked(accountID) {
		p.notifyWaitersLocked()
	}
}

func (p *Pool) Status() map[string]any {
	// The quota snapshot is read before p.mu: usageSnapshot takes p.mu
	// internally, so reading it under Status' own lock would deadlock.
	usage := p.quotaUsageSnapshot()
	p.mu.Lock()
	defer p.mu.Unlock()
	available := make([]string, 0, len(p.queue))
	inUseAccounts := make([]string, 0, len(p.inUse))
	activePoolAccounts := make([]string, len(p.queue))
	copy(activePoolAccounts, p.queue)
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
		"active_pool_accounts":     activePoolAccounts,
		"max_inflight_per_account": p.maxInflightPerAccount,
		"global_max_inflight":      p.globalMaxInflight,
		"recommended_concurrency":  p.recommendedConcurrency,
		"waiting":                  p.waiters,
		"max_queue_size":           p.maxQueueSize,
		"active_pool_size":         p.activePoolSize,
		"active_pool_count":        len(p.queue),
		"standby_count":            len(p.standby),
		"banned_count":             bannedCount,
		"disabled_count":           disabledCount,
		"daily_limited_count":      p.dailyLimitedCountLocked(usage),
	}
}
