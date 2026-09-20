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

// dailyLimitsLocked reports the configured per-account budgets, in raw tokens
// and requests. A zero budget disables that metric. Callers must hold p.mu.
func (p *Pool) dailyLimitsLocked() (tokenLimit, requestLimit int64) {
	if p.store == nil {
		return 0, 0
	}
	return p.store.RuntimeDailyTokenLimit(), int64(p.store.RuntimeDailyRequestLimit())
}

// dailyUsageLocked returns the cached per-account quota-window usage,
// refreshing it once the cache expired. Callers must hold p.mu.
func (p *Pool) dailyUsageLocked() map[string]DailyUsage {
	if p.dailyUsageProvider == nil {
		return nil
	}
	if p.dailyUsageCache == nil || time.Since(p.dailyUsageCachedAt) >= dailyUsageCacheTTL {
		p.dailyUsageCache = p.dailyUsageProvider()
		p.dailyUsageCachedAt = time.Now()
	}
	return p.dailyUsageCache
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
	tokenLimit, requestLimit := p.dailyLimitsLocked()
	if tokenLimit <= 0 && requestLimit <= 0 {
		return false
	}
	return atDailyLimit(usage[accountID], tokenLimit, requestLimit)
}

// promoteForDailyLimitsLocked rebuilds the active/standby split when a queued
// account has exhausted its quota, so an account that has not reached its
// limit takes its place. It reports whether a rebuild happened, which means
// the caller's usage snapshot is still valid but p.queue changed. Callers must
// hold p.mu.
func (p *Pool) promoteForDailyLimitsLocked(usage map[string]DailyUsage, tokenLimit, requestLimit int64) bool {
	if tokenLimit <= 0 && requestLimit <= 0 {
		return false
	}
	for _, id := range p.queue {
		if atDailyLimit(usage[id], tokenLimit, requestLimit) {
			p.rebuildQueueLocked()
			return true
		}
	}
	return false
}

// dailyLimitedCountLocked counts configured accounts that exhausted either
// quota budget inside the current window, for the queue status payload.
// Callers must hold p.mu.
func (p *Pool) dailyLimitedCountLocked() int {
	if p.store == nil {
		return 0
	}
	tokenLimit, requestLimit := p.dailyLimitsLocked()
	if tokenLimit <= 0 && requestLimit <= 0 {
		return 0
	}
	usage := p.dailyUsageLocked()
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
