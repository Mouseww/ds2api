package usagestats

import (
	"sort"
	"strings"
	"time"
)

// DefaultRange is used when the caller does not ask for a specific range.
const DefaultRange = "24h"

// rangeSpec maps a user-facing range onto a source bucket and a render step.
type rangeSpec struct {
	name   string
	bucket string
	window time.Duration
	source string
	step   time.Duration
}

// rangeSpecs is ordered from the shortest to the longest range.
var rangeSpecs = []rangeSpec{
	{name: "1h", bucket: "1m", window: time.Hour, source: "minute", step: time.Minute},
	{name: "24h", bucket: "10m", window: 24 * time.Hour, source: "minute", step: 10 * time.Minute},
	{name: "7d", bucket: "1h", window: 7 * 24 * time.Hour, source: "hour", step: time.Hour},
	{name: "30d", bucket: "6h", window: 30 * 24 * time.Hour, source: "hour", step: 6 * time.Hour},
	{name: "90d", bucket: "1d", window: 90 * 24 * time.Hour, source: "day", step: 24 * time.Hour},
	{name: "1y", bucket: "7d", window: 365 * 24 * time.Hour, source: "day", step: 7 * 24 * time.Hour},
}

// SupportedRanges lists the accepted range identifiers, shortest first.
func SupportedRanges() []string {
	out := make([]string, 0, len(rangeSpecs))
	for _, spec := range rangeSpecs {
		out = append(out, spec.name)
	}
	return out
}

// lookupRange resolves a range name, falling back to DefaultRange.
func lookupRange(name string) rangeSpec {
	wanted := strings.TrimSpace(strings.ToLower(name))
	for _, spec := range rangeSpecs {
		if spec.name == wanted {
			return spec
		}
	}
	for _, spec := range rangeSpecs {
		if spec.name == DefaultRange {
			return spec
		}
	}
	return rangeSpecs[0]
}

// Query renders the dashboard snapshot for one range.
func (s *Store) Query(rangeName string) Snapshot {
	spec := lookupRange(rangeName)
	now := time.Now()

	if s == nil {
		return Snapshot{
			GeneratedAt: now.UnixMilli(),
			Range:       spec.name,
			Bucket:      spec.bucket,
			BucketMs:    spec.step.Milliseconds(),
			Series:      []Point{},
			Models:      []Breakdown{},
			Surfaces:    []Breakdown{},
			Accounts:    []Breakdown{},
			Callers:     []Breakdown{},
			Live:        LiveWindow{WindowSeconds: LiveWindowSeconds, Series: []Point{}},
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lastPrune.IsZero() || now.Sub(s.lastPrune) >= time.Minute {
		s.pruneLocked(now)
		s.lastPrune = now
	}

	from := now.Add(-spec.window)
	source, sourceSeconds := s.sourceBucketsLocked(spec.source)

	live := s.liveLocked(now)
	peakRPM, peakTPM := s.peakLocked(now)

	summary := aggregateRange(source, sourceSeconds, from, now).summarize(now, s.trackingSince)
	summary.RPM = live.RPM
	summary.TPM = live.TPM
	summary.PeakRPM = peakRPM
	summary.PeakTPM = peakTPM

	totals := s.totals.summarize(now, s.trackingSince)
	totals.RPM = live.RPM
	totals.TPM = live.TPM
	totals.PeakRPM = peakRPM
	totals.PeakTPM = peakTPM

	return Snapshot{
		GeneratedAt: now.UnixMilli(),
		Range:       spec.name,
		Bucket:      spec.bucket,
		BucketMs:    spec.step.Milliseconds(),
		Summary:     summary,
		Totals:      totals,
		Series:      buildSeries(source, sourceSeconds, int64(spec.step/time.Second), from, now),
		Models:      rankedBreakdowns(s.models),
		Surfaces:    rankedBreakdowns(s.surfaces),
		Accounts:    rankedBreakdowns(s.accounts),
		Callers:     rankedBreakdowns(s.callers),
		Live:        live,
		Retention: Retention{
			LiveSeconds: int(secondRetention / time.Second),
			MinuteHours: int(minuteRetention / time.Hour),
			HourDays:    int(hourRetention / (24 * time.Hour)),
			DayDays:     int(dayRetention / (24 * time.Hour)),
		},
		Path: s.path,
	}
}

// sourceBucketsLocked selects the bucket map backing a range.
func (s *Store) sourceBucketsLocked(source string) (map[int64]*bucket, int64) {
	switch source {
	case "second":
		return s.seconds, 1
	case "hour":
		return s.hours, 3600
	case "day":
		return s.days, 86400
	default:
		return s.minutes, 60
	}
}

// aggregateRange sums every source bucket inside [from, now].
func aggregateRange(source map[int64]*bucket, sourceSeconds int64, from, now time.Time) bucket {
	var out bucket
	if sourceSeconds <= 0 {
		return out
	}
	fromKey := from.Unix() / sourceSeconds
	nowKey := now.Unix() / sourceSeconds
	for key, value := range source {
		if value == nil || key < fromKey || key > nowKey {
			continue
		}
		value.mergeInto(&out)
	}
	return out
}

// buildSeries renders a zero-filled series at the requested step.
//
// Bins are aligned to epoch multiples of step, so consecutive refreshes keep
// the same bin boundaries and the chart does not shimmer.
func buildSeries(source map[int64]*bucket, sourceSeconds, stepSeconds int64, from, now time.Time) []Point {
	if sourceSeconds <= 0 {
		sourceSeconds = 60
	}
	if stepSeconds <= 0 {
		stepSeconds = sourceSeconds
	}
	lastBin := alignDown(now.Unix(), stepSeconds)
	firstBin := alignDown(from.Unix(), stepSeconds)
	if firstBin > lastBin {
		firstBin = lastBin
	}
	count := int((lastBin-firstBin)/stepSeconds) + 1
	if count <= 0 {
		return []Point{}
	}

	bins := make([]bucket, count)
	index := make(map[int64]int, count)
	for i := 0; i < count; i++ {
		index[firstBin+int64(i)*stepSeconds] = i
	}
	for key, value := range source {
		if value == nil {
			continue
		}
		at := key * sourceSeconds
		if at < firstBin || at > lastBin {
			continue
		}
		if i, ok := index[alignDown(at, stepSeconds)]; ok {
			value.mergeInto(&bins[i])
		}
	}

	points := make([]Point, count)
	for i := range bins {
		points[i] = Point{
			T:                (firstBin + int64(i)*stepSeconds) * 1000,
			Requests:         bins[i].Requests,
			Errors:           bins[i].Errors,
			PromptTokens:     bins[i].Prompt,
			CompletionTokens: bins[i].Completion,
			ReasoningTokens:  bins[i].Reasoning,
			TotalTokens:      bins[i].Total,
		}
	}
	return points
}

// liveLocked renders the rolling RPM/TPM reading and a coarse live series.
func (s *Store) liveLocked(now time.Time) LiveWindow {
	live := LiveWindow{
		WindowSeconds: LiveWindowSeconds,
		Series:        []Point{},
	}
	lastSecond := now.Unix()
	firstSecond := lastSecond - int64(LiveWindowSeconds) + 1

	// RPM/TPM are the totals accumulated over the trailing sample window.
	for second := lastSecond - int64(sampleWindowSeconds) + 1; second <= lastSecond; second++ {
		value := s.seconds[second]
		if value == nil {
			continue
		}
		live.Requests += value.Requests
		live.TotalTokens += value.Total
	}
	live.RPM = float64(live.Requests)
	live.TPM = float64(live.TotalTokens)

	step := int64(LiveSeriesStepSeconds)
	if step <= 0 {
		step = 1
	}
	firstBin := alignDown(firstSecond, step)
	lastBin := alignDown(lastSecond, step)
	count := int((lastBin-firstBin)/step) + 1
	if count <= 0 {
		return live
	}

	bins := make([]bucket, count)
	index := make(map[int64]int, count)
	for i := 0; i < count; i++ {
		index[firstBin+int64(i)*step] = i
	}
	for second := firstSecond; second <= lastSecond; second++ {
		value := s.seconds[second]
		if value == nil {
			continue
		}
		if i, ok := index[alignDown(second, step)]; ok {
			value.mergeInto(&bins[i])
		}
	}

	points := make([]Point, count)
	for i := range bins {
		points[i] = Point{
			T:                (firstBin + int64(i)*step) * 1000,
			Requests:         bins[i].Requests,
			Errors:           bins[i].Errors,
			PromptTokens:     bins[i].Prompt,
			CompletionTokens: bins[i].Completion,
			ReasoningTokens:  bins[i].Reasoning,
			TotalTokens:      bins[i].Total,
		}
	}
	live.Series = points
	return live
}

// peakLocked reports the busiest single minute still retained.
func (s *Store) peakLocked(now time.Time) (int64, int64) {
	var peakRPM, peakTPM int64
	minKey := now.Add(-minuteRetention).Unix() / 60
	for key, value := range s.minutes {
		if value == nil || key < minKey {
			continue
		}
		if value.Requests > peakRPM {
			peakRPM = value.Requests
		}
		if value.Total > peakTPM {
			peakTPM = value.Total
		}
	}
	return peakRPM, peakTPM
}

// AccountUsage is the all-time usage accumulated for a single account.
type AccountUsage struct {
	Requests         int64 `json:"requests"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// AllAccountUsage returns all-time usage for every tracked account in one lock
// acquisition, so the admin account list can annotate each row cheaply.
func (s *Store) AllAccountUsage() map[string]AccountUsage {
	out := map[string]AccountUsage{}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, b := range s.accounts {
		if b == nil {
			continue
		}
		out[id] = AccountUsage{
			Requests:         b.Requests,
			PromptTokens:     b.Prompt,
			CompletionTokens: b.Completion,
			TotalTokens:      b.Total,
		}
	}
	return out
}

// rankedBreakdowns flattens a breakdown map, busiest first.
func rankedBreakdowns(m map[string]*bucket) []Breakdown {
	out := make([]Breakdown, 0, len(m))
	for key, value := range m {
		if value == nil {
			continue
		}
		out = append(out, Breakdown{
			Key:              key,
			Requests:         value.Requests,
			Errors:           value.Errors,
			PromptTokens:     value.Prompt,
			CompletionTokens: value.Completion,
			ReasoningTokens:  value.Reasoning,
			TotalTokens:      value.Total,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalTokens != out[j].TotalTokens {
			return out[i].TotalTokens > out[j].TotalTokens
		}
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Key < out[j].Key
	})
	return out
}
