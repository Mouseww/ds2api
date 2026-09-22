package assistantturn

import (
	"strings"
	"testing"

	"ds2api/internal/httpapi/openai/shared"
	"ds2api/internal/toolcall"
)

// deadSubagentModeBSpecimen is the verbatim text block of a production
// failure: a coding agent's model (served through this API) tried to emit a
// grep tool call, but the DSML markup came out corrupted — fullwidth doubled
// ｜ pipes, a doubled parameter name attribute, a stray CDATA close marker,
// and no wrapper/invoke open tags at all. The sieve could not capture it, the
// response came back text-only, and the agent's turn ended mid-task.
const deadSubagentModeBSpecimen = "Now let me look at the turn finalization logic that decides empty-output retry.\n\n\n\n" +
	"<｜｜DSML｜｜ parameter name=\"parameter name=\"pattern\">func ShouldRetryEmptyOutput|func FinalizeTurn|func BuildTurnFromCollected]]</｜｜DSML｜｜ parameter>\n" +
	"<｜｜DSML｜｜ parameter name=\"path\">E:\\projects\\ds2api\\internal\\assistantturn</｜｜DSML｜｜ parameter>\n" +
	"</｜｜DSML｜｜ invoke>\n" +
	"</｜｜DSML｜｜ calls>"

func TestDetectMalformedToolCallAttempt(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"dead subagent mode B specimen (verbatim)", deadSubagentModeBSpecimen, true},
		{"fullwidth doubled pipes", "<｜｜DSML｜｜ parameter name=\"command\">ls</｜｜DSML｜｜ parameter>", true},
		{"fullwidth invoke", "<｜｜DSML｜｜ invoke name=\"Bash\">", true},
		{"fullwidth calls shorthand with invoke", "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"pwsh\">", true},
		{"halfwidth orphaned parameter", "<|DSML|parameter name=\"description\">x</|DSML|parameter>", true},
		{"space separator typo", "<|DSML parameter name=\"file_path\">/tmp/x</|DSML parameter>", true},
		{"collapsed tag", "<DSMLparameter name=\"todos\">x</DSMLparameter>", true},
		{"wrapper mention only stays prose", "the format is <|DSML|tool_calls> and </|DSML|tool_calls>", false},
		{"plain prose", "普通文本 with <angle brackets> and 2 > 1", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectMalformedToolCallAttempt(tc.in); got != tc.want {
				t.Fatalf("DetectMalformedToolCallAttempt(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestClassifyRetryKind(t *testing.T) {
	t.Run("text plus corrupted markup with no parsed calls is malformed", func(t *testing.T) {
		turn := Turn{Text: "Now let me look at the finalization logic.", RawText: deadSubagentModeBSpecimen}
		if got := ClassifyRetryKind(turn); got != RetryKindMalformedToolCall {
			t.Fatalf("expected RetryKindMalformedToolCall, got %v", got)
		}
	})
	t.Run("markup only with empty visible text is malformed, not empty", func(t *testing.T) {
		turn := Turn{RawText: "<｜｜DSML｜｜ parameter name=\"x\">v</｜｜DSML｜｜ parameter>"}
		if got := ClassifyRetryKind(turn); got != RetryKindMalformedToolCall {
			t.Fatalf("expected RetryKindMalformedToolCall to take priority, got %v", got)
		}
	})
	t.Run("empty output without markup is the classic empty kind", func(t *testing.T) {
		turn := Turn{Thinking: "reasoning only"}
		if got := ClassifyRetryKind(turn); got != RetryKindEmptyOutput {
			t.Fatalf("expected RetryKindEmptyOutput, got %v", got)
		}
	})
	t.Run("parsed tool calls never retry", func(t *testing.T) {
		turn := Turn{RawText: deadSubagentModeBSpecimen, ToolCalls: []toolcall.ParsedToolCall{{Name: "grep"}}}
		if got := ClassifyRetryKind(turn); got != RetryKindNone {
			t.Fatalf("expected RetryKindNone when calls parsed, got %v", got)
		}
	})
	t.Run("content filter never retries", func(t *testing.T) {
		turn := Turn{ContentFilter: true, RawText: deadSubagentModeBSpecimen}
		if got := ClassifyRetryKind(turn); got != RetryKindNone {
			t.Fatalf("expected RetryKindNone on content filter, got %v", got)
		}
	})
	t.Run("plain answer never retries", func(t *testing.T) {
		turn := Turn{Text: "here is the answer"}
		if got := ClassifyRetryKind(turn); got != RetryKindNone {
			t.Fatalf("expected RetryKindNone, got %v", got)
		}
	})
	t.Run("budget bounds the decision", func(t *testing.T) {
		turn := Turn{RawText: deadSubagentModeBSpecimen}
		if got := RetryKindForTurn(turn, 1, 1); got != RetryKindNone {
			t.Fatalf("expected RetryKindNone when attempts exhausted, got %v", got)
		}
		if got := RetryKindForTurn(turn, 0, 1); got != RetryKindMalformedToolCall {
			t.Fatalf("expected RetryKindMalformedToolCall within budget, got %v", got)
		}
	})
}

func TestMalformedSpecimenDoesNotSurviveSanitizerTags(t *testing.T) {
	// The Go visible-output sanitizer must at minimum strip the corrupted
	// tags from visible text (values may remain, matching the pinned
	// orphaned-DSML behavior). This documents that the Go path leaks less
	// than the Node path did before its strip was added.
	out := shared.CleanVisibleOutput(deadSubagentModeBSpecimen, false)
	for _, tag := range []string{"<｜｜DSML｜｜ parameter", "</｜｜DSML｜｜ parameter>", "</｜｜DSML｜｜ invoke>", "</｜｜DSML｜｜ calls>"} {
		if strings.Contains(out, tag) {
			t.Fatalf("corrupted DSML tag %q leaked into visible output: %q", tag, out)
		}
	}
}
