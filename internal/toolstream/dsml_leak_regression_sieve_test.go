package toolstream

import (
	"strings"
	"testing"
)

// Byte-exact production leak samples: both blocks were emitted by the model as
// visible text instead of being executed as tool calls when a deployed instance
// ran parsing code that predated the calls-shorthand / repeated-separator
// support. They are locked here so any parser regression fails a test instead
// of reintroducing a production leak.
const verbatimLeakedCallsShorthandSample = "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"pwsh\">\n<｜｜DSML｜｜ parameter name=\"command\" string=\"true\"><![CDATA[cd E:\\projects\\ds2api; git status --porcelain | Select-Object -First 40; Write-Output '--- grep charts/dashboard ---'; Select-String -Path 'webui\\src\\**\\*.jsx','webui\\src\\**\\*.js' -Pattern 'Dashboard|charts|Sparkline|AreaLineChart|BarList' -SimpleMatch -List | Select-Object -ExpandProperty Path]]></｜｜DSML｜｜ parameter>\n<｜｜DSML｜｜ parameter name=\"description\" string=\"true\">Check git status and chart references</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n<｜｜DSML｜｜ invoke name=\"glob\">\n<｜｜DSML｜｜ parameter name=\"pattern\" string=\"true\">plans/refactor-line-gate-targets.txt</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n</｜｜DSML｜｜ calls>"

const verbatimLeakedHybridEPSECloseSample = "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"read\">\n<｜｜DSML｜｜ parameter name=\"file_path\"><![CDATA[internal/responsehistory/session.go]]></|EPSE|parameter>\n  </|EPSE|invoke>\n  <|EPSE|invoke name=\"read\">\n    <|EPSE|parameter name=\"file_path\"><![CDATA[internal/httpapi/openai/chat/handler_chat.go]]></|EPSE|parameter>\n  </|EPSE|invoke>\n  <|EPSE|invoke name=\"read\">\n    <|EPSE|parameter name=\"file_path\"><![CDATA[webui/src/i18n.jsx]]></|EPSE|parameter>\n  </|EPSE|invoke>\n</|EPSE|tool_calls>"

func TestSieveVerbatimLeakedSamplesAcrossChunkSizes(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		tools     []string
		wantCalls int
	}{
		{"calls_shorthand", verbatimLeakedCallsShorthandSample, []string{"pwsh", "glob"}, 2},
		{"hybrid_epse_close", verbatimLeakedHybridEPSECloseSample, []string{"read"}, 3},
	}
	for _, tc := range cases {
		for _, size := range []int{0, 1, 2, 3, 5, 7, 13, 64} {
			var state State
			var events []Event
			if size == 0 {
				events = append(events, ProcessChunk(&state, tc.text, tc.tools)...)
			} else {
				for i := 0; i < len(tc.text); i += size {
					end := i + size
					if end > len(tc.text) {
						end = len(tc.text)
					}
					events = append(events, ProcessChunk(&state, tc.text[i:end], tc.tools)...)
				}
			}
			events = append(events, Flush(&state, tc.tools)...)
			calls := 0
			var content strings.Builder
			for _, evt := range events {
				calls += len(evt.ToolCalls)
				content.WriteString(evt.Content)
			}
			if calls != tc.wantCalls {
				t.Fatalf("%s size=%d: expected %d tool calls, got %d", tc.name, size, tc.wantCalls, calls)
			}
			if leaked := content.String(); strings.Contains(leaked, "DSML") || strings.Contains(leaked, "EPSE") {
				t.Fatalf("%s size=%d: leaked tool markup as content: %q", tc.name, size, leaked)
			}
		}
	}
}

// orphanedDSMLToolCallSample is a byte-exact production leak: the model emitted
// a tool call whose opening wrapper was corrupted into a stray closing tag and
// whose invoke became a bare <Tool> tag, leaving only orphaned parameter and
// closing tags. The sieve must not stall on this (it must release it as text
// so the downstream output sanitizer can strip the markup), and it must not
// mis-detect it as an executable tool call.
const orphanedDSMLToolCallSample = "</|DSML|tool_calls><Tool>:echo hi\n" +
	"']]></|DSML|parameter>\n" +
	"<|DSML|parameter name=\"description\"><![CDATA[拉取服务器证据]]></|DSML|parameter>\n" +
	"<|DSML|parameter name=\"timeout\"><![CDATA[120000]]></|DSML|parameter>\n" +
	"</|DSML|parameter>\n" +
	"</|DSML|tool_calls>\n" +
	"</|DSML|tool_calls>"

func TestSieveOrphanedDSMLToolCallDoesNotStallOrExecute(t *testing.T) {
	for _, size := range []int{0, 1, 2, 3, 5, 7, 13, 64} {
		var state State
		var events []Event
		if size == 0 {
			events = append(events, ProcessChunk(&state, orphanedDSMLToolCallSample, []string{"Tool"})...)
		} else {
			for i := 0; i < len(orphanedDSMLToolCallSample); i += size {
				end := i + size
				if end > len(orphanedDSMLToolCallSample) {
					end = len(orphanedDSMLToolCallSample)
				}
				events = append(events, ProcessChunk(&state, orphanedDSMLToolCallSample[i:end], []string{"Tool"})...)
			}
		}
		events = append(events, Flush(&state, []string{"Tool"})...)

		calls := 0
		var content strings.Builder
		for _, evt := range events {
			calls += len(evt.ToolCalls)
			content.WriteString(evt.Content)
		}
		if calls != 0 {
			t.Fatalf("size=%d: malformed orphaned DSML must not execute as a tool call, got %d", size, calls)
		}
		if state.capturing {
			t.Fatalf("size=%d: sieve left capture open after Flush", size)
		}
		// The raw text must be released (not swallowed) so the downstream
		// sanitizer can strip the markup; but it must not be re-ordered.
		if got := content.String(); !strings.Contains(got, "echo hi") {
			t.Fatalf("size=%d: expected released text to preserve command body, got %q", size, got)
		}
	}
}
