package toolstream_test

import (
	"fmt"
	"strings"
	"testing"

	"ds2api/internal/httpapi/openai/shared"
	"ds2api/internal/toolstream"
)

// cdataClose is assembled at runtime so this source file never contains the
// literal CDATA terminator sequence, which would otherwise break the markup
// that carries this file.
var cdataClose = "]]" + ">"

// sessionLeakSamples are malformed DSML emission shapes taken from a real
// ds2api-fronted session where the user reported "tool-call markup shows up as
// assistant body text".
//
// Two properties are checked for every shape, at several chunk sizes:
//
//  1. the shape must be recovered into a tool call (wantCalls), and
//  2. no tool markup may survive into the visible text.
//
// Chunk size matters: production is a streaming SSE path, so the sieve sees
// small, network-determined fragments rather than whole blocks. A shape that
// parses when handed over in one piece but leaks when streamed is a real
// production defect, not a test artifact.
var sessionLeakSamples = []struct {
	name      string
	text      string
	tools     []string
	wantCalls int
}{
	{
		name: "baseline_well_formed",
		text: "<|DSML|tool_calls>\n" +
			"<|DSML|invoke name=\"read\">\n" +
			"<|DSML|parameter name=\"file_path\"><![CDATA[internal/store.go" + cdataClose + "</|DSML|parameter>\n" +
			"</|DSML|invoke>\n" +
			"</|DSML|tool_calls>",
		tools:     []string{"read"},
		wantCalls: 1,
	},
	{
		name: "unclosed_cdata_no_terminator",
		text: "<|DSML|tool_calls>\n" +
			"<|DSML|invoke name=\"read\">\n" +
			"<|DSML|parameter name=\"file_path\"><![CDATA[internal/store.go</|DSML|parameter>\n" +
			"</|DSML|invoke>\n" +
			"</|DSML|tool_calls>",
		tools:     []string{"read"},
		wantCalls: 1,
	},
	{
		name: "plain_value_without_cdata",
		text: "<|DSML|tool_calls>\n" +
			"<|DSML|invoke name=\"read\">\n" +
			"<|DSML|parameter name=\"file_path\">internal/store.go</|DSML|parameter>\n" +
			"</|DSML|invoke>\n" +
			"</|DSML|tool_calls>",
		tools:     []string{"read"},
		wantCalls: 1,
	},
	{
		name: "unclosed_cdata_swallows_next_parameter",
		text: "<|DSML|tool_calls>\n" +
			"<|DSML|invoke name=\"pwsh\">\n" +
			"<|DSML|parameter name=\"command\"><![CDATA[Get-ChildItem -Force</|DSML|parameter>\n" +
			"<|DSML|parameter name=\"description\"><![CDATA[list files" + cdataClose + "</|DSML|parameter>\n" +
			"</|DSML|invoke>\n" +
			"</|DSML|tool_calls>",
		tools:     []string{"pwsh"},
		wantCalls: 1,
	},
	{
		name: "doubled_parameter_name_attribute",
		text: "<|DSML|tool_calls>\n" +
			"<|DSML|invoke name=\"read\">\n" +
			"<|DSML|parameter name=\"parameter name=\"file_path\"><![CDATA[x.go" + cdataClose + "</|DSML|parameter>\n" +
			"</|DSML|invoke>\n" +
			"</|DSML|tool_calls>",
		tools:     []string{"read"},
		wantCalls: 1,
	},
	{
		name: "wrapper_close_missing",
		text: "<|DSML|tool_calls>\n" +
			"<|DSML|invoke name=\"read\">\n" +
			"<|DSML|parameter name=\"file_path\"><![CDATA[x.go" + cdataClose + "</|DSML|parameter>\n" +
			"</|DSML|invoke>",
		tools:     []string{"read"},
		wantCalls: 1,
	},
	{
		name: "bare_invoke_with_wrapper_close",
		text: "<|DSML|invoke name=\"read\">\n" +
			"<|DSML|parameter name=\"file_path\"><![CDATA[x.go" + cdataClose + "</|DSML|parameter>\n" +
			"</|DSML|invoke>\n" +
			"</|DSML|tool_calls>",
		tools:     []string{"read"},
		wantCalls: 1,
	},
	{
		name: "orphaned_stray_close_then_parameters",
		text: "</|DSML|tool_calls><Tool>:echo hi\n" +
			"']]" + "></|DSML|parameter>\n" +
			"<|DSML|parameter name=\"description\"><![CDATA[pull evidence" + cdataClose + "</|DSML|parameter>\n" +
			"</|DSML|invoke>\n" +
			"</|DSML|tool_calls>",
		tools: []string{"Tool"},
		// By design: an unreconstructable block must never execute.
		wantCalls: 0,
	},
}

func TestSessionMalformedDSMLLeakRepro(t *testing.T) {
	for _, tc := range sessionLeakSamples {
		for _, size := range []int{0, 1, 7} {
			t.Run(fmt.Sprintf("%s/size=%d", tc.name, size), func(t *testing.T) {
				var state toolstream.State
				var events []toolstream.Event
				if size == 0 {
					events = append(events, toolstream.ProcessChunk(&state, tc.text, tc.tools)...)
				} else {
					for i := 0; i < len(tc.text); i += size {
						end := i + size
						if end > len(tc.text) {
							end = len(tc.text)
						}
						events = append(events, toolstream.ProcessChunk(&state, tc.text[i:end], tc.tools)...)
					}
				}
				events = append(events, toolstream.Flush(&state, tc.tools)...)

				calls := 0
				var raw strings.Builder
				for _, evt := range events {
					calls += len(evt.ToolCalls)
					raw.WriteString(evt.Content)
				}
				visible := shared.CleanVisibleOutput(raw.String(), false)

				if calls != tc.wantCalls {
					t.Errorf("got %d tool calls, want %d (raw released: %q, visible: %q)",
						calls, tc.wantCalls, raw.String(), visible)
				}
				if strings.Contains(visible, "DSML") || strings.Contains(visible, "CDATA") {
					t.Errorf("tool markup reached visible output: %q", visible)
				}
			})
		}
	}
}
