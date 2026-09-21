package account

// canQueueLocked reports whether one more acquirer may park while waiting for a
// free slot. Callers must hold p.mu.
func (p *Pool) canQueueLocked(target string, exclude map[string]bool) bool {
	if target != "" {
		if exclude[target] {
			return false
		}
		if _, ok := p.store.FindAccount(target); !ok {
			return false
		}
	}
	if p.maxQueueSize <= 0 {
		return false
	}
	return p.waiters < p.maxQueueSize
}

// notifyWaitersLocked broadcasts a capacity change to every parked acquirer by
// closing the channel they are blocked on and installing a fresh one.
//
// The pool used to keep a FIFO of per-waiter channels and hand each wakeup to a
// single waiter. That loses wakeups: the woken waiter may be waiting on a
// different, still-full target account, re-park itself without waking anyone
// else, and leave the slot that was just released unused until an unrelated
// release happens to wake another waiter. Broadcasting is the sync.Cond-style
// equivalent: every waiter re-checks the pool state under p.mu after each
// wakeup and re-parks only when it genuinely cannot make progress, so no
// release can go unnoticed.
//
// Callers must hold p.mu.
func (p *Pool) notifyWaitersLocked() {
	if p.waiters == 0 || p.wakeCh == nil {
		return
	}
	close(p.wakeCh)
	p.wakeCh = make(chan struct{})
}

// ensureWakeChLocked returns the channel parked acquirers block on, creating it
// on first use. Callers must hold p.mu.
func (p *Pool) ensureWakeChLocked() chan struct{} {
	if p.wakeCh == nil {
		p.wakeCh = make(chan struct{})
	}
	return p.wakeCh
}
