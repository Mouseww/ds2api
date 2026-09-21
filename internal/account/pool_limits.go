package account

import (
	"os"
	"strconv"
	"strings"
)

func (p *Pool) ApplyRuntimeLimits(maxInflightPerAccount, maxQueueSize, globalMaxInflight int) {
	if maxInflightPerAccount <= 0 {
		maxInflightPerAccount = 1
	}
	if maxQueueSize < 0 {
		maxQueueSize = 0
	}
	if globalMaxInflight <= 0 {
		globalMaxInflight = maxInflightPerAccount * len(p.store.Accounts())
		if globalMaxInflight <= 0 {
			globalMaxInflight = maxInflightPerAccount
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxInflightPerAccount = maxInflightPerAccount
	p.maxQueueSize = maxQueueSize
	p.globalMaxInflight = globalMaxInflight
	p.recommendedConcurrency = defaultRecommendedConcurrency(len(p.queue), p.maxInflightPerAccount)
	p.notifyWaitersLocked()
}

func maxInflightFromEnv() int {
	if raw := strings.TrimSpace(os.Getenv("DS2API_ACCOUNT_MAX_INFLIGHT")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return 2
}

func defaultRecommendedConcurrency(accountCount, maxInflightPerAccount int) int {
	if accountCount <= 0 {
		return 0
	}
	if maxInflightPerAccount <= 0 {
		maxInflightPerAccount = 2
	}
	return accountCount * maxInflightPerAccount
}

func maxQueueFromEnv(defaultSize int) int {
	if raw := strings.TrimSpace(os.Getenv("DS2API_ACCOUNT_MAX_QUEUE")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			return n
		}
	}
	if defaultSize < 0 {
		return 0
	}
	return defaultSize
}

// canAcquireIDLocked reports whether accountID has a free per-account slot and
// the pool still has global capacity. Both checks are O(1): the global total is
// maintained incrementally in p.totalInUse instead of being summed from p.inUse
// for every candidate account, which made the acquire path O(n²) in the number
// of accounts. Callers must hold p.mu.
func (p *Pool) canAcquireIDLocked(accountID string) bool {
	if accountID == "" {
		return false
	}
	if p.inUse[accountID] >= p.maxInflightPerAccount {
		return false
	}
	if p.globalMaxInflight > 0 && p.totalInUse >= p.globalMaxInflight {
		return false
	}
	return true
}

// takeSlotLocked records one in-flight request for accountID and keeps the O(1)
// global total in sync with p.inUse. Callers must hold p.mu.
func (p *Pool) takeSlotLocked(accountID string) {
	p.inUse[accountID]++
	p.totalInUse++
}

// releaseSlotLocked gives one in-flight slot back, reporting whether accountID
// had a slot to release. The global total is decremented next to the
// per-account counter so the two can never drift apart. Callers must hold p.mu.
func (p *Pool) releaseSlotLocked(accountID string) bool {
	count := p.inUse[accountID]
	if count <= 0 {
		return false
	}
	if count == 1 {
		delete(p.inUse, accountID)
	} else {
		p.inUse[accountID] = count - 1
	}
	p.totalInUse--
	return true
}
