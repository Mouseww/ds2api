// Package usagestats aggregates per-request API usage for the admin dashboard:
// request counts, token consumption, latency, and live RPM/TPM readings.
//
// The store is safe for concurrent use. It keeps cumulative totals plus
// time-bucketed counters (per second, minute, hour and day) and can persist a
// snapshot to disk so the dashboard survives restarts. Bucket boundaries are
// aligned to UTC epoch multiples, which keeps a rendered series stable across
// refreshes.
//
// Only the middleware decides which requests are counted; see ClassifyRequest.
// Token counts are supplied by the protocol handlers through Annotate, which
// merges into a per-request Recorder carried in the request context.
package usagestats

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

// FileVersion is the schema version of the persisted snapshot file.
const FileVersion = 1

const (
	// LiveWindowSeconds is the width of the live window kept at one-second
	// resolution. It bounds the rolling RPM/TPM reading.
	LiveWindowSeconds = 300

	// LiveSeriesStepSeconds is the resolution of the live series sent to the UI.
	LiveSeriesStepSeconds = 5

	// sampleWindowSeconds is the trailing window used for the current RPM/TPM.
	sampleWindowSeconds = 60

	secondRetention = LiveWindowSeconds * time.Second
	minuteRetention = 48 * time.Hour
	hourRetention   = 90 * 24 * time.Hour
	dayRetention    = 730 * 24 * time.Hour

	// maxBreakdownKeys bounds the cardinality of each breakdown map so a
	// misbehaving client cannot grow memory without limit.
	maxBreakdownKeys = 100

	// otherKey collects breakdown keys beyond maxBreakdownKeys.
	otherKey = "(other)"
	// noneKey labels events that carry no model/account/caller identity.
	noneKey = "(none)"

	defaultSaveInterval = 20 * time.Second
)

// Surface identifiers used by the middleware and rendered as breakdown keys.
const (
	SurfaceOpenAIChat       = "openai.chat"
	SurfaceOpenAIResponses  = "openai.responses"
	SurfaceOpenAIEmbeddings = "openai.embeddings"
	SurfaceOpenAIFiles      = "openai.files"
	SurfaceOpenAIModels     = "openai.models"
	SurfaceClaudeMessages   = "claude.messages"
	SurfaceClaudeModels     = "claude.models"
	SurfaceGeminiGenerate   = "gemini.generate"
	SurfaceOllama           = "ollama"
)

// Event is one completed API request.
type Event struct {
	Time             time.Time
	Surface          string
	Method           string
	Path             string
	Model            string
	AccountID        string
	CallerID         string
	StatusCode       int
	Stream           bool
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
	TotalTokens      int
	ElapsedMs        int64
}

// Failed reports whether the request finished with an error status.
func (e Event) Failed() bool { return e.StatusCode >= 400 }

// Usage is the token accounting a handler knows about its own request. It is
// merged into the per-request Recorder so the middleware can commit a single
// event once the handler returns.
type Usage struct {
	Model            string
	AccountID        string
	CallerID         string
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
	TotalTokens      int
	Stream           bool
}

// bucket holds additive counters for one time slice or one breakdown key.
type bucket struct {
	Requests   int64 `json:"requests"`
	Errors     int64 `json:"errors"`
	Prompt     int64 `json:"prompt_tokens"`
	Completion int64 `json:"completion_tokens"`
	Reasoning  int64 `json:"reasoning_tokens"`
	Total      int64 `json:"total_tokens"`
	ElapsedSum int64 `json:"elapsed_sum_ms"`
	ElapsedMax int64 `json:"elapsed_max_ms"`
}

// addEvent folds one request into the bucket.
func (b *bucket) addEvent(ev Event) {
	if b == nil {
		return
	}
	b.Requests++
	if ev.Failed() {
		b.Errors++
	}
	b.Prompt += int64(ev.PromptTokens)
	b.Completion += int64(ev.CompletionTokens)
	b.Reasoning += int64(ev.ReasoningTokens)
	b.Total += int64(ev.TotalTokens)
	b.ElapsedSum += ev.ElapsedMs
	if ev.ElapsedMs > b.ElapsedMax {
		b.ElapsedMax = ev.ElapsedMs
	}
}

// mergeInto adds b into dst.
func (b *bucket) mergeInto(dst *bucket) {
	if b == nil || dst == nil {
		return
	}
	dst.Requests += b.Requests
	dst.Errors += b.Errors
	dst.Prompt += b.Prompt
	dst.Completion += b.Completion
	dst.Reasoning += b.Reasoning
	dst.Total += b.Total
	dst.ElapsedSum += b.ElapsedSum
	if b.ElapsedMax > dst.ElapsedMax {
		dst.ElapsedMax = b.ElapsedMax
	}
}

// Point is one rendered series sample.
type Point struct {
	T                int64 `json:"t"`
	Requests         int64 `json:"requests"`
	Errors           int64 `json:"errors"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// Breakdown is one ranked row, grouped by model, surface, account or caller.
type Breakdown struct {
	Key              string `json:"key"`
	Requests         int64  `json:"requests"`
	Errors           int64  `json:"errors"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	ReasoningTokens  int64  `json:"reasoning_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
}

// Summary is the aggregate for one scope (the selected range or all time).
type Summary struct {
	Requests         int64   `json:"requests"`
	Errors           int64   `json:"errors"`
	SuccessRequests  int64   `json:"success_requests"`
	SuccessRate      float64 `json:"success_rate"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	AvgElapsedMs     int64   `json:"avg_elapsed_ms"`
	MaxElapsedMs     int64   `json:"max_elapsed_ms"`
	RPM              float64 `json:"rpm"`
	TPM              float64 `json:"tpm"`
	PeakRPM          int64   `json:"peak_rpm"`
	PeakTPM          int64   `json:"peak_tpm"`
	TrackingSince    int64   `json:"tracking_since"`
	GeneratedAt      int64   `json:"generated_at"`
}

// LiveWindow is the rolling RPM/TPM reading plus a short live series.
type LiveWindow struct {
	WindowSeconds int     `json:"window_seconds"`
	RPM           float64 `json:"rpm"`
	TPM           float64 `json:"tpm"`
	Requests      int64   `json:"requests"`
	TotalTokens   int64   `json:"total_tokens"`
	Series        []Point `json:"series"`
}

// Retention describes how long each bucket resolution is kept.
type Retention struct {
	LiveSeconds int `json:"live_seconds"`
	MinuteHours int `json:"minute_hours"`
	HourDays    int `json:"hour_days"`
	DayDays     int `json:"day_days"`
}

// Snapshot is the dashboard payload for one selected range.
type Snapshot struct {
	GeneratedAt int64       `json:"generated_at"`
	Range       string      `json:"range"`
	Bucket      string      `json:"bucket"`
	BucketMs    int64       `json:"bucket_ms"`
	Summary     Summary     `json:"summary"`
	Totals      Summary     `json:"totals"`
	Series      []Point     `json:"series"`
	Models      []Breakdown `json:"models"`
	Surfaces    []Breakdown `json:"surfaces"`
	Accounts    []Breakdown `json:"accounts"`
	Callers     []Breakdown `json:"callers"`
	Live        LiveWindow  `json:"live"`
	Retention   Retention   `json:"retention"`
	Path        string      `json:"path,omitempty"`
}

// summarize converts a bucket into a Summary.
func (b bucket) summarize(now, trackingSince time.Time) Summary {
	out := Summary{
		Requests:         b.Requests,
		Errors:           b.Errors,
		PromptTokens:     b.Prompt,
		CompletionTokens: b.Completion,
		ReasoningTokens:  b.Reasoning,
		TotalTokens:      b.Total,
		MaxElapsedMs:     b.ElapsedMax,
		TrackingSince:    trackingSince.UnixMilli(),
		GeneratedAt:      now.UnixMilli(),
	}
	out.SuccessRequests = b.Requests - b.Errors
	if out.SuccessRequests < 0 {
		out.SuccessRequests = 0
	}
	if b.Requests > 0 {
		out.SuccessRate = float64(out.SuccessRequests) / float64(b.Requests)
		out.AvgElapsedMs = b.ElapsedSum / b.Requests
	}
	return out
}

// alignDown rounds v down to the nearest multiple of step.
func alignDown(v, step int64) int64 {
	if step <= 0 {
		return v
	}
	remainder := v % step
	if remainder < 0 {
		remainder += step
	}
	return v - remainder
}

// normalizeKey maps blank identity strings onto a stable placeholder.
func normalizeKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return noneKey
	}
	return key
}

// intFromAny coerces the numeric shapes that appear in JSON-decoded usage maps.
func intFromAny(v any) int {
	switch x := v.(type) {
	case nil:
		return 0
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float32:
		return int(math.Round(float64(x)))
	case float64:
		return int(math.Round(x))
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n)
		}
		if f, err := x.Float64(); err == nil {
			return int(math.Round(f))
		}
		return 0
	default:
		return 0
	}
}

// UsageFromMap reads token counts out of the protocol-specific usage objects
// that ds2api renders (OpenAI chat/responses, Claude, Gemini). Missing fields
// count as zero, and a missing total is derived from prompt + completion.
//
// Note that this project treats reasoning tokens as a subset of the completion
// tokens, so no double counting happens here.
func UsageFromMap(m map[string]any) Usage {
	if len(m) == 0 {
		return Usage{}
	}
	prompt := firstInt(m, "prompt_tokens", "input_tokens", "promptTokenCount")
	completion := firstInt(m, "completion_tokens", "output_tokens", "candidatesTokenCount")
	reasoning := firstInt(m, "reasoning_tokens", "thoughtsTokenCount")
	if reasoning == 0 {
		reasoning = nestedInt(m, "completion_tokens_details", "reasoning_tokens")
	}
	if reasoning == 0 {
		reasoning = nestedInt(m, "output_tokens_details", "reasoning_tokens")
	}
	total := firstInt(m, "total_tokens", "totalTokenCount")
	if total == 0 {
		total = prompt + completion
	}
	return Usage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		ReasoningTokens:  reasoning,
		TotalTokens:      total,
	}
}

// firstInt returns the first non-zero value found under the given keys.
func firstInt(m map[string]any, keys ...string) int {
	for _, key := range keys {
		value, ok := m[key]
		if !ok {
			continue
		}
		if n := intFromAny(value); n != 0 {
			return n
		}
	}
	return 0
}

// nestedInt reads m[outer][inner] as an integer.
func nestedInt(m map[string]any, outer, inner string) int {
	value, ok := m[outer]
	if !ok {
		return 0
	}
	nested, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	return intFromAny(nested[inner])
}
