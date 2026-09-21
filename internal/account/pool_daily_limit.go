package account

import (
	"time"
)

// DailyUsage is one account's usage inside the configured quota window.
type DailyUsage struct {
	Requests    int64
	TotalTokens int64
}

// DailyUsageProvider supplies per-account quota-window usage to the pool. The
// server wiring adapts the usage-stats store into this shape, which keeps the
// account package independent of the analytics implementation. The window
// length is decided by the provider (runtime.quota_window_hours), so the pool
// itself stays window-agnostic.
type DailyUsageProvider func() map[string]DailyUsage

// dailyUsageCacheTTL bounds how often the pool re-reads the provider. The
// window counters only move when a request completes (or usage slides out of
// the window), so a short cache keeps the acquire path cheap without
// meaningfully delaying eviction of an account that just crossed its budget.
const dailyUsageCacheTTL = 5 * time.Second

// SetDailyUsageProvider installs the usage source backing the global
// per-account quotas. Passing nil disables the quota check.
func (p *Pool) SetDailyUsageProvider(provider DailyUsageProvider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dailyUsageProvider = provider
	p.dailyUsageCache = nil
	p.dailyUsageCachedAt = time.Time{}
}

// dailyLimits reports the configured per-account budgets, in raw tokens and
// requests. A zero budget disables that metric. It only reads the immutable
// store handle and the store's own snapshot, so callers may invoke it with or
// without p.mu held.
func (p *Pool) dailyLimits() (tokenLimit, requestLimit int64) {
	if p.store == nil {
		return 0, 0
	}
	return p.store.RuntimeDailyTokenLimit(), int64(p.store.RuntimeDailyRequestLimit())
}

// usageSnapshot returns the per-account quota-window usage, refreshing the
// cached snapshot once it expired.
//
// The provider is deliberately never invoked while p.mu is held: it reads the
// usage-stats store, which takes its own lock, so calling it under p.mu nested
// the pool and store locks and let a slow provider stall every pool operation,
// including Release. Refreshes are serialized by dailyUsageMu, which is only
// ever taken without p.mu, so an expired cache triggers a single provider read
// instead of one read per waiting acquirer.
func (p *Pool) usageSnapshot() map[string]DailyUsage {
	p.mu.Lock()
	provider := p.dailyUsageProvider
	cache := p.dailyUsageCache
	cachedAt := p.dailyUsageCachedAt
	p.mu.Unlock()

	if provider == nil {
		return nil
	}
	if cache != nil && time.Since(cachedAt) < dailyUsageCacheTTL {
		return cache
	}

	p.dailyUsageMu.Lock()
	defer p.dailyUsageMu.Unlock()

	// Another goroutine may have refreshed the cache while we waited for the
	// refresh lock, so re-check before reading the provider again.
	p.mu.Lock()
	provider = p.dailyUsageProvider
	cache = p.dailyUsageCache
	cachedAt = p.dailyUsageCachedAt
	p.mu.Unlock()
	if provider == nil {
		return nil
	}
	if cache != nil && time.Since(cachedAt) < dailyUsageCacheTTL {
		return cache
	}

	usage := provider()

	p.mu.Lock()
	p.dailyUsageCache = usage
	p.dailyUsageCachedAt = time.Now()
	p.mu.Unlock()
	return usage
}

// quotaUsageSnapshot returns the usage snapshot only when per-account quota
// budgets are configured, so a pool running with quotas disabled never touches
// the usage-stats store just to render a status payload.
func (p *Pool) quotaUsageSnapshot() map[string]DailyUsage {
	tokenLimit, requestLimit := p.dailyLimits()
	if tokenLimit <= 0 && requestLimit <= 0 {
		return nil
	}
	return p.usageSnapshot()
}

// atDailyLimit reports whether usage has reached either configured budget.
func atDailyLimit(usage DailyUsage, tokenLimit, requestLimit int64) bool {
	if tokenLimit > 0 && usage.TotalTokens >= tokenLimit {
		return true
	}
	if requestLimit > 0 && usage.Requests >= requestLimit {
		return true
	}
	return false
}

// accountDailyLimitedLocked reports whether accountID exhausted its quota
// budget inside the supplied usage snapshot. Callers must hold p.mu.
func (p *Pool) accountDailyLimitedLocked(usage map[string]DailyUsage, accountID string) bool {
	if accountID == "" {
		return false
	}
	tokenLimit, requestLimit := p.dailyLimits()
	if tokenLimit <= 0 && requestLimit <= 0 {
		return false
	}
	return atDailyLimit(usage[accountID], tokenLimit, requestLimit)
}

// promoteForDailyLimitsLocked rebuilds the active/standby split when a queued
// account has exhausted its quota, so an account that has not reached its
// limit takes its place. It reports whether a rebuild happened, which means
// p.queue changed; the caller's usage snapshot stays valid because the rebuild
// is driven by that very snapshot. Callers must hold p.mu.
func (p *Pool) promoteForDailyLimitsLocked(usage map[string]DailyUsage, tokenLimit, requestLimit int64) bool {
	if tokenLimit <= 0 && requestLimit <= 0 {
		return false
	}
	for _, id := range p.queue {
		if atDailyLimit(usage[id], tokenLimit, requestLimit) {
			p.rebuildQueueLocked(usage)
			return true
		}
	}
	return false
}

// dailyLimitedCountLocked counts configured accounts that exhausted either
// quota budget inside the supplied usage snapshot, for the queue status
// payload. Callers must hold p.mu.
func (p *Pool) dailyLimitedCountLocked(usage map[string]DailyUsage) int {
	if p.store == nil {
		return 0
	}
	tokenLimit, requestLimit := p.dailyLimits()
	if tokenLimit <= 0 && requestLimit <= 0 {
		return 0
	}
	count := 0
	for _, acc := range p.store.Accounts() {
		id := acc.Identifier()
		if id == "" {
			continue
		}
		if atDailyLimit(usage[id], tokenLimit, requestLimit) {
			count++
		}
	}
	return count
}
