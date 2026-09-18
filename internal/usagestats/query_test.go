package usagestats

import (
	"testing"
	"time"
)

func TestQuerySeriesSumsEveryRequest(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	now := time.Now()
	for i := 0; i < 5; i++ {
		store.Record(Event{
			Time: now.Add(-time.Duration(i) * time.Minute), Surface: SurfaceOpenAIChat,
			StatusCode: 200, PromptTokens: 10, TotalTokens: 10,
		})
	}

	snap := store.Query("1h")
	var requests, tokens int64
	for _, point := range snap.Series {
		requests += point.Requests
		tokens += point.TotalTokens
	}
	if requests != 5 {
		t.Fatalf("series requests = %d, want 5", requests)
	}
	if tokens != 50 {
		t.Fatalf("series tokens = %d, want 50", tokens)
	}
	if snap.Bucket != "1m" {
		t.Fatalf("bucket = %q, want 1m", snap.Bucket)
	}
	if snap.BucketMs != int64(time.Minute/time.Millisecond) {
		t.Fatalf("bucket ms = %d, want %d", snap.BucketMs, int64(time.Minute/time.Millisecond))
	}
	if snap.Summary.Requests != 5 {
		t.Fatalf("summary requests = %d, want 5", snap.Summary.Requests)
	}
	if snap.Summary.TotalTokens != 50 {
		t.Fatalf("summary tokens = %d, want 50", snap.Summary.TotalTokens)
	}
}

func TestQueryLiveWindowRPMAndTPM(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	now := time.Now()
	for i := 0; i < 3; i++ {
		store.Record(Event{
			Time: now.Add(-time.Duration(i) * time.Second), Surface: SurfaceOpenAIChat,
			StatusCode: 200, TotalTokens: 100,
		})
	}

	snap := store.Query("1h")
	if snap.Live.RPM != 3 {
		t.Fatalf("live rpm = %v, want 3", snap.Live.RPM)
	}
	if snap.Live.TPM != 300 {
		t.Fatalf("live tpm = %v, want 300", snap.Live.TPM)
	}
	if snap.Live.WindowSeconds != LiveWindowSeconds {
		t.Fatalf("window seconds = %d, want %d", snap.Live.WindowSeconds, LiveWindowSeconds)
	}
	if snap.Summary.RPM != 3 {
		t.Fatalf("summary rpm = %v, want 3", snap.Summary.RPM)
	}

	var liveRequests int64
	for _, point := range snap.Live.Series {
		liveRequests += point.Requests
	}
	if liveRequests != 3 {
		t.Fatalf("live series requests = %d, want 3", liveRequests)
	}
}

func TestQueryPeakUsesMinuteBuckets(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	store.Record(Event{Time: time.Now(), Surface: SurfaceOpenAIChat, StatusCode: 200, TotalTokens: 10})

	snap := store.Query("1h")
	if snap.Summary.PeakRPM < 1 {
		t.Fatalf("peak rpm = %d, want >= 1", snap.Summary.PeakRPM)
	}
	if snap.Summary.PeakTPM < 10 {
		t.Fatalf("peak tpm = %d, want >= 10", snap.Summary.PeakTPM)
	}
}

func TestQueryScopesRangeAgainstTotals(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	// Older than the 1h window but inside the 24h window.
	store.Record(Event{
		Time: time.Now().Add(-3 * time.Hour), Surface: SurfaceOpenAIChat, StatusCode: 200, TotalTokens: 5,
	})

	hour := store.Query("1h")
	if hour.Summary.Requests != 0 {
		t.Fatalf("1h requests = %d, want 0", hour.Summary.Requests)
	}
	if hour.Totals.Requests != 1 {
		t.Fatalf("all-time requests = %d, want 1", hour.Totals.Requests)
	}

	day := store.Query("24h")
	if day.Summary.Requests != 1 {
		t.Fatalf("24h requests = %d, want 1", day.Summary.Requests)
	}
	if day.Bucket != "10m" {
		t.Fatalf("24h bucket = %q, want 10m", day.Bucket)
	}
}

func TestQueryReturnsZeroFilledSeries(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	snap := store.Query("1h")
	if len(snap.Series) != 61 {
		t.Fatalf("series length = %d, want 61 one-minute buckets", len(snap.Series))
	}
	for _, point := range snap.Series {
		if point.Requests != 0 || point.TotalTokens != 0 {
			t.Fatalf("expected a zero-filled series, got %+v", point)
		}
	}
}

func TestQueryUnknownRangeFallsBackToDefault(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	if got := store.Query("nonsense").Range; got != DefaultRange {
		t.Fatalf("range = %q, want %q", got, DefaultRange)
	}
}

func TestQueryRetentionIsReported(t *testing.T) {
	store := NewStore("")
	defer func() { _ = store.Close() }()

	retention := store.Query("1h").Retention
	if retention.LiveSeconds != LiveWindowSeconds {
		t.Fatalf("live seconds = %d, want %d", retention.LiveSeconds, LiveWindowSeconds)
	}
	if retention.MinuteHours != 48 {
		t.Fatalf("minute hours = %d, want 48", retention.MinuteHours)
	}
	if retention.HourDays != 90 {
		t.Fatalf("hour days = %d, want 90", retention.HourDays)
	}
	if retention.DayDays != 730 {
		t.Fatalf("day days = %d, want 730", retention.DayDays)
	}
}
