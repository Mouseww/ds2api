package assistantturn

import (
	"regexp"
	"strings"
)

// RetryKind classifies why a completed attempt with no tool calls should be
// retried. The malformed-tool-call kind exists because the model sometimes
// emits its tool call with corrupted DSML markup (fullwidth ｜ pipes, doubled
// parameter names, missing wrapper tags) after being primed by DSML-heavy
// prompt content; the sieve cannot capture the corrupted shape, so the
// response would otherwise come back as text-only and the caller's agent
// loop ends the turn even though the model clearly intended to call a tool.
type RetryKind int

const (
	// RetryKindNone means the attempt should be returned as-is.
	RetryKindNone RetryKind = iota
	// RetryKindEmptyOutput is the classic empty-output retry: no visible
	// text, no tool calls, no content filter.
	RetryKindEmptyOutput
	// RetryKindMalformedToolCall means the raw output contains DSML
	// tool-call markup that did not parse into a tool call: the model
	// attempted a tool call and the emission was corrupted.
	RetryKindMalformedToolCall
)

// malformedToolCallAttemptPattern matches DSML parameter/invoke tags in every
// corruption variant observed in production transcripts:
//   - halfwidth canonical:  <|DSML|parameter name="...">
//   - fullwidth doubled:   <｜｜DSML｜｜ parameter name="...">
//   - space separator:     <|DSML parameter name="...">
//   - collapsed:           <DSMLparameter name="...">
//
// Wrapper-only mentions (<|DSML|tool_calls>) do not match: prose that names
// the wrapper without parameter/invoke tags is documentation, not an attempt.
var malformedToolCallAttemptPattern = regexp.MustCompile(`<[\x{ff5c}|]{0,2}DSML[\x{ff5c}|]{0,2}\s*(?:parameter|invoke)`)

// DetectMalformedToolCallAttempt reports whether the raw output contains DSML
// parameter/invoke markup in any observed variant. Callers must combine this
// with "no tool calls were parsed": a complete (even corrupted-looking) block
// that the sieve captured successfully is a working tool call, not a
// malformed attempt.
func DetectMalformedToolCallAttempt(rawText string) bool {
	if rawText == "" {
		return false
	}
	return malformedToolCallAttemptPattern.MatchString(rawText)
}

// ClassifyRetryKind classifies one completed turn (no attempt bound) by why it
// should be retried. A malformed tool-call attempt takes priority over the
// empty-output kind: the corrective suffix teaches the exact DSML format,
// which also covers the markup-only case where visible text is empty.
func ClassifyRetryKind(turn Turn) RetryKind {
	if turn.ContentFilter || len(turn.ToolCalls) > 0 {
		return RetryKindNone
	}
	if DetectMalformedToolCallAttempt(turn.RawText) {
		return RetryKindMalformedToolCall
	}
	if strings.TrimSpace(turn.Text) == "" {
		return RetryKindEmptyOutput
	}
	return RetryKindNone
}

// RetryKindForTurn bounds ClassifyRetryKind by the retry budget.
func RetryKindForTurn(turn Turn, attempts, maxAttempts int) RetryKind {
	if attempts >= maxAttempts {
		return RetryKindNone
	}
	return ClassifyRetryKind(turn)
}

// String names the kind for retry logs.
func (k RetryKind) String() string {
	switch k {
	case RetryKindEmptyOutput:
		return "empty_output"
	case RetryKindMalformedToolCall:
		return "malformed_tool_call"
	default:
		return "none"
	}
}
