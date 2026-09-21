package account

import (
	"context"

	"ds2api/internal/config"
)

func (p *Pool) Acquire(target string, exclude map[string]bool) (config.Account, bool) {
	usage := p.usageSnapshot()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.acquireLocked(target, normalizeExclude(exclude), usage)
}

func (p *Pool) AcquireWait(ctx context.Context, target string, exclude map[string]bool) (config.Account, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	exclude = normalizeExclude(exclude)
	for {
		if ctx.Err() != nil {
			return config.Account{}, false
		}
		usage := p.usageSnapshot()

		p.mu.Lock()
		if acc, ok := p.acquireLocked(target, exclude, usage); ok {
			p.mu.Unlock()
			return acc, true
		}
		if !p.canQueueLocked(target, exclude) {
			p.mu.Unlock()
			return config.Account{}, false
		}
		// Park on the shared broadcast channel: every release (and every
		// limit/queue change) closes it and installs a fresh one, so a waiter
		// that wakes up and loses the race for one account still observes a
		// slot released for any other account.
		p.waiters++
		wake := p.ensureWakeChLocked()
		p.mu.Unlock()

		select {
		case <-ctx.Done():
		case <-wake:
		}

		p.mu.Lock()
		p.waiters--
		p.mu.Unlock()

		if ctx.Err() != nil {
			return config.Account{}, false
		}
	}
}

func (p *Pool) acquireLocked(target string, exclude map[string]bool, usage map[string]DailyUsage) (config.Account, bool) {
	if target != "" {
		if exclude[target] || !p.canAcquireIDLocked(target) {
			return config.Account{}, false
		}
		acc, ok := p.store.FindAccount(target)
		if !ok || !acc.IsEnabled() || acc.IsBanned() {
			return config.Account{}, false
		}
		if p.accountDailyLimitedLocked(usage, target) {
			return config.Account{}, false
		}
		p.takeSlotLocked(target)
		p.bumpQueue(target)
		return acc, true
	}

	return p.tryAcquire(exclude, usage)
}

func (p *Pool) tryAcquire(exclude map[string]bool, usage map[string]DailyUsage) (config.Account, bool) {
	tokenLimit, requestLimit := p.dailyLimits()
	// Swap in an account that has not reached its daily budget as soon as an
	// active one exhausts either metric. The rebuild is driven by the snapshot
	// passed in, so no second provider read is needed here.
	p.promoteForDailyLimitsLocked(usage, tokenLimit, requestLimit)

	n := len(p.queue)
	if n == 0 {
		return config.Account{}, false
	}
	// Walk the queue as a ring starting at the round-robin cursor, so the
	// account that most recently took a slot is tried last without the O(n)
	// slice delete+append bumpQueue used to do.
	idx := p.queueCursor
	if idx < 0 || idx >= n {
		idx = 0
	}
	for i := 0; i < n; i++ {
		id := p.queue[idx]
		idx++
		if idx == n {
			idx = 0
		}
		if exclude[id] || !p.canAcquireIDLocked(id) {
			continue
		}
		if atDailyLimit(usage[id], tokenLimit, requestLimit) {
			continue
		}
		acc, ok := p.store.FindAccount(id)
		if !ok || !acc.IsEnabled() || acc.IsBanned() {
			continue
		}
		p.takeSlotLocked(id)
		p.bumpQueue(id)
		return acc, true
	}
	return config.Account{}, false
}

// bumpQueue advances the round-robin cursor past accountID so the account that
// just took a slot is tried last on the next acquire. It is O(1) (one map
// lookup plus a cursor move) and leaves p.queue itself untouched, which also
// keeps the queue order reported by Status stable. Callers must hold p.mu.
func (p *Pool) bumpQueue(accountID string) {
	n := len(p.queue)
	if n == 0 {
		return
	}
	pos, ok := p.queuePos[accountID]
	if !ok {
		return
	}
	p.queueCursor = (pos + 1) % n
}

func normalizeExclude(exclude map[string]bool) map[string]bool {
	if exclude == nil {
		return map[string]bool{}
	}
	return exclude
}
