package usagestats

import "time"

// AccountWindowUsage returns each account's usage over the trailing window,
// which backs the configurable per-account quota. Windows that fit inside the
// per-account minute retention are summed from minute buckets (accurate to the
// minute); wider windows fall back to hour buckets, which can only overcount by
// up to one boundary hour — the conservative direction for a quota. Accounts
// without usage in the window are omitted.
func (s *Store) AccountWindowUsage(window time.Duration) map[string]AccountUsage {
	out := map[string]AccountUsage{}
	if s == nil || window <= 0 {
		return out
	}
	now := time.Now()
	from := now.Add(-window)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneIfDueLocked(now)

	for id, series := range s.accountSeries {
		if series == nil {
			continue
		}
		buckets, seconds := quotaWindowBuckets(series, window)
		agg := aggregateRange(buckets, seconds, from, now)
		if agg.Requests == 0 && agg.Total == 0 {
			continue
		}
		out[id] = AccountUsage{
			Requests:         agg.Requests,
			PromptTokens:     agg.Prompt,
			CompletionTokens: agg.Completion,
			TotalTokens:      agg.Total,
		}
	}
	return out
}

// quotaWindowBuckets selects the per-account bucket resolution for a quota
// window: minute buckets while the window fits inside their retention, hour
// buckets beyond that.
func quotaWindowBuckets(series *accountSeries, window time.Duration) (map[int64]*bucket, int64) {
	if window <= accountMinuteRetention {
		return series.Minutes, 60
	}
	return series.Hours, 3600
}
