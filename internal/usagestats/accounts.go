package usagestats

import (
	"sort"
	"strings"
	"time"
)

// maxTrackedAccounts bounds how many per-account series the store keeps, so a
// runaway account list cannot grow the persisted snapshot without limit.
// Accounts beyond it fold into "(other)". The cap is slightly above the generic
// breakdown cap because the account pool is the primary thing this surface
// monitors and folding a real account into "(other)" is worse than folding a
// model name.
const maxTrackedAccounts = 128

// Per-account retention is deliberately tighter than the global bucket
// retention. The snapshot is persisted as a single JSON document, so the
// per-account series must stay bounded: the bucket maps only ever hold the
// minutes/hours/days that fall inside their window, so an account's map size is
// capped by the window length rather than by its traffic. Each window is sized
// to comfortably exceed the widest dashboard range that reads that resolution:
//
//	minute -> 1h and 24h ranges
//	hour   -> 7d and 30d ranges
//	day    -> 90d and 1y ranges
//
// Maps only ever hold minutes that actually carried a request, so an idle
// account costs nothing and the realistic footprint stays far below the
// worst-case bound of maxTrackedAccounts x (window length in buckets).
const (
	accountMinuteRetention = 30 * time.Hour
	accountHourRetention   = 32 * 24 * time.Hour
	accountDayRetention    = 400 * 24 * time.Hour
)

// accountSeries is the rolling per-account state. It mirrors the global store:
// one cumulative bucket, first/last activity timestamps, the minute/hour/day
// resolutions every dashboard range is rendered from, and per-account model and
// surface breakdowns.
type accountSeries struct {
	Total    bucket             `json:"total"`
	FirstAt  int64              `json:"first_at"`
	LastAt   int64              `json:"last_at"`
	Minutes  map[int64]*bucket  `json:"minutes,omitempty"`
	Hours    map[int64]*bucket  `json:"hours,omitempty"`
	Days     map[int64]*bucket  `json:"days,omitempty"`
	Models   map[string]*bucket `json:"models,omitempty"`
	Surfaces map[string]*bucket `json:"surfaces,omitempty"`
}

// newAccountSeries allocates an empty series with live maps.
func newAccountSeries() *accountSeries {
	return &accountSeries{
		Minutes:  map[int64]*bucket{},
		Hours:    map[int64]*bucket{},
		Days:     map[int64]*bucket{},
		Models:   map[string]*bucket{},
		Surfaces: map[string]*bucket{},
	}
}

// addEvent folds one request into the account series.
func (a *accountSeries) addEvent(ev Event, now time.Time) {
	if a == nil {
		return
	}
	a.Total.addEvent(ev)
	at := now.UnixMilli()
	if a.FirstAt == 0 || at < a.FirstAt {
		a.FirstAt = at
	}
	if at > a.LastAt {
		a.LastAt = at
	}
	addBucket(a.Minutes, now.Unix()/60, ev)
	addBucket(a.Hours, now.Unix()/3600, ev)
	addBucket(a.Days, now.Unix()/86400, ev)
	bumpBreakdown(a.Models, ev.Model, ev)
	bumpBreakdown(a.Surfaces, ev.Surface, ev)
}

// prune drops time buckets that fell out of their retention window.
func (a *accountSeries) prune(now time.Time) {
	if a == nil {
		return
	}
	pruneBucketMap(a.Minutes, now.Add(-accountMinuteRetention).Unix()/60)
	pruneBucketMap(a.Hours, now.Add(-accountHourRetention).Unix()/3600)
	pruneBucketMap(a.Days, now.Add(-accountDayRetention).Unix()/86400)
}

// adopt rebuilds a decoded series with freshly allocated maps, dropping nil
// entries so a truncated snapshot cannot panic a later record.
func (a *accountSeries) adopt() *accountSeries {
	if a == nil {
		return nil
	}
	return &accountSeries{
		Total:    a.Total,
		FirstAt:  a.FirstAt,
		LastAt:   a.LastAt,
		Minutes:  adoptBuckets(a.Minutes),
		Hours:    adoptBuckets(a.Hours),
		Days:     adoptBuckets(a.Days),
		Models:   adoptBreakdowns(a.Models),
		Surfaces: adoptBreakdowns(a.Surfaces),
	}
}

// adoptAccountSeries rebuilds the decoded per-account map, dropping nil entries.
func adoptAccountSeries(in map[string]*accountSeries) map[string]*accountSeries {
	out := make(map[string]*accountSeries, len(in))
	for key, value := range in {
		if adopted := value.adopt(); adopted != nil {
			out[key] = adopted
		}
	}
	return out
}

// recordAccountLocked folds an event into the per-account series. Callers must
// hold s.mu.
func (s *Store) recordAccountLocked(ev Event, now time.Time) {
	id := strings.TrimSpace(ev.AccountID)
	// Requests that never reached an account (missing credentials, no pooled
	// capacity) are visible in the global totals but are not account usage.
	if id == "" {
		return
	}
	key := id
	series, ok := s.accountSeries[key]
	if !ok {
		if len(s.accountSeries) >= maxTrackedAccounts {
			key = otherKey
			series, ok = s.accountSeries[key]
		}
		if !ok {
			series = newAccountSeries()
			s.accountSeries[key] = series
		}
	}
	series.addEvent(ev, now)
}

// AccountStat is one account's aggregate for a single scope: the selected range
// for list rows, or all time for the detail payload's Totals section.
type AccountStat struct {
	AccountID        string  `json:"account_id"`
	Requests         int64   `json:"requests"`
	Errors           int64   `json:"errors"`
	SuccessRequests  int64   `json:"success_requests"`
	SuccessRate      float64 `json:"success_rate"`
	StreamRequests   int64   `json:"stream_requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	AvgElapsedMs     int64   `json:"avg_elapsed_ms"`
	MaxElapsedMs     int64   `json:"max_elapsed_ms"`
	AvgRPM           float64 `json:"avg_rpm"`
	AvgTPM           float64 `json:"avg_tpm"`
	FirstSeenAt      int64   `json:"first_seen_at"`
	LastSeenAt       int64   `json:"last_seen_at"`
}

// AccountUsageSnapshot is the ranked per-account payload for one range.
//
// Summary describes all traffic in the range; Attributed describes only the
// share that reached a concrete account. The two differ by requests that failed
// before an account was acquired, which is how an attribution gap becomes
// visible instead of silently skewing the per-account numbers.
type AccountUsageSnapshot struct {
	GeneratedAt   int64         `json:"generated_at"`
	Range         string        `json:"range"`
	Bucket        string        `json:"bucket"`
	BucketMs      int64         `json:"bucket_ms"`
	Summary       Summary       `json:"summary"`
	Attributed    Summary       `json:"attributed"`
	Accounts      []AccountStat `json:"accounts"`
	TrackingSince int64         `json:"tracking_since"`
}

// AccountDetail is the drill-down payload for a single account.
type AccountDetail struct {
	GeneratedAt int64       `json:"generated_at"`
	Range       string      `json:"range"`
	Bucket      string      `json:"bucket"`
	BucketMs    int64       `json:"bucket_ms"`
	Account     AccountStat `json:"account"`
	Totals      AccountStat `json:"totals"`
	Series      []Point     `json:"series"`
	Models      []Breakdown `json:"models"`
	Surfaces    []Breakdown `json:"surfaces"`
}

// accountStatFrom renders a bucket as an AccountStat. window is the averaging
// window for the RPM/TPM columns; pass 0 to leave them unset (all-time scopes).
func accountStatFrom(id string, series *accountSeries, agg bucket, window time.Duration) AccountStat {
	stat := AccountStat{
		AccountID:        id,
		Requests:         agg.Requests,
		Errors:           agg.Errors,
		StreamRequests:   agg.Streams,
		PromptTokens:     agg.Prompt,
		CompletionTokens: agg.Completion,
		ReasoningTokens:  agg.Reasoning,
		TotalTokens:      agg.Total,
		MaxElapsedMs:     agg.ElapsedMax,
	}
	stat.SuccessRequests = agg.Requests - agg.Errors
	if stat.SuccessRequests < 0 {
		stat.SuccessRequests = 0
	}
	if agg.Requests > 0 {
		stat.SuccessRate = float64(stat.SuccessRequests) / float64(agg.Requests)
		stat.AvgElapsedMs = agg.ElapsedSum / agg.Requests
	}
	if minutes := window.Minutes(); minutes >= 1 {
		stat.AvgRPM = float64(agg.Requests) / minutes
		stat.AvgTPM = float64(agg.Total) / minutes
	}
	if series != nil {
		stat.FirstSeenAt = series.FirstAt
		stat.LastSeenAt = series.LastAt
	}
	return stat
}

// seriesBuckets selects the per-account bucket map backing a range. Per-account
// second resolution is not kept; minute buckets are the finest per-account
// series and cover every sub-hour range.
func seriesBuckets(series *accountSeries, source string) (map[int64]*bucket, int64) {
	if series == nil {
		return nil, 60
	}
	switch source {
	case "hour":
		return series.Hours, 3600
	case "day":
		return series.Days, 86400
	default:
		return series.Minutes, 60
	}
}

// QueryAccounts renders the ranked per-account usage for one range.
func (s *Store) QueryAccounts(rangeName string) AccountUsageSnapshot {
	spec := lookupRange(rangeName)
	now := time.Now()
	out := AccountUsageSnapshot{
		GeneratedAt: now.UnixMilli(),
		Range:       spec.name,
		Bucket:      spec.bucket,
		BucketMs:    spec.step.Milliseconds(),
		Accounts:    []AccountStat{},
	}
	if s == nil {
		return out
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneIfDueLocked(now)

	from := now.Add(-spec.window)
	source, sourceSeconds := s.sourceBucketsLocked(spec.source)
	summary := aggregateRange(source, sourceSeconds, from, now).summarize(now, s.trackingSince)

	var attributed bucket
	for id, series := range s.accountSeries {
		if series == nil {
			continue
		}
		buckets, seconds := seriesBuckets(series, spec.source)
		agg := aggregateRange(buckets, seconds, from, now)
		if agg.Requests == 0 {
			continue
		}
		agg.mergeInto(&attributed)
		out.Accounts = append(out.Accounts, accountStatFrom(id, series, agg, spec.window))
	}

	sort.Slice(out.Accounts, func(i, j int) bool {
		if out.Accounts[i].TotalTokens != out.Accounts[j].TotalTokens {
			return out.Accounts[i].TotalTokens > out.Accounts[j].TotalTokens
		}
		if out.Accounts[i].Requests != out.Accounts[j].Requests {
			return out.Accounts[i].Requests > out.Accounts[j].Requests
		}
		return out.Accounts[i].AccountID < out.Accounts[j].AccountID
	})

	out.Summary = summary
	out.Attributed = attributed.summarize(now, s.trackingSince)
	if minutes := spec.window.Minutes(); minutes >= 1 {
		out.Attributed.RPM = float64(out.Attributed.Requests) / minutes
		out.Attributed.TPM = float64(out.Attributed.TotalTokens) / minutes
	}
	out.TrackingSince = s.trackingSince.UnixMilli()
	return out
}

// QueryAccount renders the drill-down payload for a single account. It reports
// false when the account has no recorded usage yet.
func (s *Store) QueryAccount(rangeName, accountID string) (AccountDetail, bool) {
	spec := lookupRange(rangeName)
	now := time.Now()
	key := strings.TrimSpace(accountID)
	if s == nil || key == "" {
		return AccountDetail{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneIfDueLocked(now)

	series, ok := s.accountSeries[key]
	if !ok || series == nil {
		return AccountDetail{}, false
	}

	from := now.Add(-spec.window)
	buckets, seconds := seriesBuckets(series, spec.source)
	agg := aggregateRange(buckets, seconds, from, now)

	detail := AccountDetail{
		GeneratedAt: now.UnixMilli(),
		Range:       spec.name,
		Bucket:      spec.bucket,
		BucketMs:    spec.step.Milliseconds(),
		Account:     accountStatFrom(key, series, agg, spec.window),
		Totals:      accountStatFrom(key, series, series.Total, 0),
		Series:      buildSeries(buckets, seconds, int64(spec.step/time.Second), from, now),
		Models:      rankedBreakdowns(series.Models),
		Surfaces:    rankedBreakdowns(series.Surfaces),
	}
	return detail, true
}
