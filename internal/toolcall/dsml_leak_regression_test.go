package toolcall

import "testing"

// Byte-exact production leak samples: both blocks were emitted by the model as
// visible text instead of being executed as tool calls when a deployed instance
// ran parsing code that predated the calls-shorthand / repeated-separator
// support. They are locked here so any parser regression fails a test instead
// of reintroducing a production leak.
const verbatimLeakedCallsShorthandSample = "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"pwsh\">\n<｜｜DSML｜｜ parameter name=\"command\" string=\"true\"><![CDATA[cd E:\\projects\\ds2api; git status --porcelain | Select-Object -First 40; Write-Output '--- grep charts/dashboard ---'; Select-String -Path 'webui\\src\\**\\*.jsx','webui\\src\\**\\*.js' -Pattern 'Dashboard|charts|Sparkline|AreaLineChart|BarList' -SimpleMatch -List | Select-Object -ExpandProperty Path]]></｜｜DSML｜｜ parameter>\n<｜｜DSML｜｜ parameter name=\"description\" string=\"true\">Check git status and chart references</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n<｜｜DSML｜｜ invoke name=\"glob\">\n<｜｜DSML｜｜ parameter name=\"pattern\" string=\"true\">plans/refactor-line-gate-targets.txt</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n</｜｜DSML｜｜ calls>"

// Hybrid marker-family sample: DSML openings closed by a foreign client-harness
// marker family (<|EPSE|...>), including a `calls` wrapper closed as
// </|EPSE|tool_calls>. Closing tags are matched by local name, so the mixed
// block still executes.
const verbatimLeakedHybridEPSECloseSample = "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"read\">\n<｜｜DSML｜｜ parameter name=\"file_path\"><![CDATA[internal/responsehistory/session.go]]></|EPSE|parameter>\n  </|EPSE|invoke>\n  <|EPSE|invoke name=\"read\">\n    <|EPSE|parameter name=\"file_path\"><![CDATA[internal/httpapi/openai/chat/handler_chat.go]]></|EPSE|parameter>\n  </|EPSE|invoke>\n  <|EPSE|invoke name=\"read\">\n    <|EPSE|parameter name=\"file_path\"><![CDATA[webui/src/i18n.jsx]]></|EPSE|parameter>\n  </|EPSE|invoke>\n</|EPSE|tool_calls>"

func TestParseToolCallsVerbatimLeakedCallsShorthandSample(t *testing.T) {
	res := ParseToolCallsDetailed(verbatimLeakedCallsShorthandSample, []string{"pwsh", "glob"})
	if len(res.Calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d (%+v)", len(res.Calls), res)
	}
	if !res.SawToolCallSyntax {
		t.Fatal("expected tool-call syntax detection")
	}
	if res.Calls[0].Name != "pwsh" {
		t.Fatalf("expected first call pwsh, got %q", res.Calls[0].Name)
	}
	if got := res.Calls[0].Input["command"]; got != "cd E:\\projects\\ds2api; git status --porcelain | Select-Object -First 40; Write-Output '--- grep charts/dashboard ---'; Select-String -Path 'webui\\src\\**\\*.jsx','webui\\src\\**\\*.js' -Pattern 'Dashboard|charts|Sparkline|AreaLineChart|BarList' -SimpleMatch -List | Select-Object -ExpandProperty Path" {
		t.Fatalf("unexpected pwsh command: %q", got)
	}
	if got := res.Calls[0].Input["description"]; got != "Check git status and chart references" {
		t.Fatalf("unexpected pwsh description: %q", got)
	}
	if res.Calls[1].Name != "glob" {
		t.Fatalf("expected second call glob, got %q", res.Calls[1].Name)
	}
	if got := res.Calls[1].Input["pattern"]; got != "plans/refactor-line-gate-targets.txt" {
		t.Fatalf("unexpected glob pattern: %q", got)
	}
}

func TestParseToolCallsVerbatimLeakedHybridEPSECloseSample(t *testing.T) {
	res := ParseToolCallsDetailed(verbatimLeakedHybridEPSECloseSample, []string{"read"})
	if len(res.Calls) != 3 {
		t.Fatalf("expected 3 tool calls, got %d (%+v)", len(res.Calls), res)
	}
	wantPaths := []string{
		"internal/responsehistory/session.go",
		"internal/httpapi/openai/chat/handler_chat.go",
		"webui/src/i18n.jsx",
	}
	for i, want := range wantPaths {
		if res.Calls[i].Name != "read" {
			t.Fatalf("call %d: expected read, got %q", i, res.Calls[i].Name)
		}
		if got := res.Calls[i].Input["file_path"]; got != want {
			t.Fatalf("call %d: expected file_path %q, got %q", i, want, got)
		}
	}
}
