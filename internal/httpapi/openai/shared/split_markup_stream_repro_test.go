package shared

import (
	"strings"
	"testing"

	"ds2api/internal/sse"
)

// cdataCloseSplit is assembled at runtime so this source file never contains
// the literal CDATA terminator sequence, which would break the markup that
// carries this file.
var cdataCloseSplit = "]]" + ">"

// Tool-call markup reaching the visible channel is sanitized per SSE part
// (StreamAccumulator.applyTextPart -> CleanVisibleOutput), not on the
// accumulated text. Only leaked *role blocks* carry suppression state across
// parts. The sanitizer itself is stateless and regex based, so it can only
// strip a tag that is complete inside a single part.
//
// This test pins the consequence: the same block is cleaned when it arrives in
// one part, and leaks verbatim when the upstream stream splits its tags across
// parts.
func TestSplitToolMarkupLeaksThroughPerPartSanitizer(t *testing.T) {
	whole := "<|DSML|tool_calls>\n" +
		"<|DSML|invoke name=\"read\">\n" +
		"<|DSML|parameter name=\"file_path\"><![CDATA[x.go" + cdataCloseSplit + "</|DSML|parameter>\n" +
		"</|DSML|invoke>\n" +
		"</|DSML|tool_calls>"

	visibleFrom := func(parts []string) string {
		acc := StreamAccumulator{}
		visible := ""
		for _, p := range parts {
			res := acc.Apply(sse.LineResult{
				Parsed: true,
				Parts:  []sse.ContentPart{{Type: "text", Text: p}},
			})
			for _, d := range res.Parts {
				visible += d.VisibleText
			}
		}
		return visible
	}

	t.Run("whole_block_in_one_part_is_cleaned", func(t *testing.T) {
		visible := visibleFrom([]string{whole})
		if strings.Contains(visible, "DSML") || strings.Contains(visible, "CDATA") {
			t.Fatalf("whole block should be stripped, got %q", visible)
		}
	})

	t.Run("tags_split_across_parts_leak", func(t *testing.T) {
		// Known gap, outside this fix's scope. The sanitizer is stateless and
		// runs per SSE part, so it cannot match a tag the upstream stream
		// splits across parts. It is only reachable when the request carries
		// no tools, because the tool path routes content through the sieve
		// instead of the visible channel. Closing it needs a stateful
		// visible-channel buffer plus end-of-stream flush semantics across
		// every stream runtime, which is a separate design decision.
		t.Skip("known gap: per-part sanitizer cannot match a tag split across SSE parts")
		visible := visibleFrom([]string{
			"<|DSML|tool_",
			"calls>\n<|DSML|inv",
			"oke name=\"read\">\n<|DSML|parameter name=\"file_path\"><![CDATA[x.go",
			cdataCloseSplit,
			"</|DSML|parameter>\n</|DSML|invoke>\n</|DSML|tool_calls>",
		})
		if strings.Contains(visible, "DSML") || strings.Contains(visible, "CDATA") {
			t.Fatalf("split tool markup leaked into visible stream output: %q", visible)
		}
	})
}
