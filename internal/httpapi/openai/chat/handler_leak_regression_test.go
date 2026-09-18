package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// End-to-end guard: the full streaming handler must convert both production
// leak samples into structured tool_calls deltas with no markup in content.
func TestHandleStreamExecutesVerbatimLeakedSamplesAsToolCalls(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		tools []string
	}{
		{"calls_shorthand", verbatimLeakedCallsShorthandSample, []string{"pwsh", "glob"}},
		{"hybrid_epse_close", verbatimLeakedHybridEPSECloseSample, []string{"read"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var lines []string
			const step = 53
			for i := 0; i < len(tc.text); i += step {
				end := i + step
				if end > len(tc.text) {
					end = len(tc.text)
				}
				payload, err := json.Marshal(map[string]any{"p": "response/content", "v": tc.text[i:end]})
				if err != nil {
					t.Fatalf("marshal patch: %v", err)
				}
				lines = append(lines, "data: "+string(payload))
			}
			lines = append(lines, "data: [DONE]")
			resp := makeSSEHTTPResponse(lines...)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			h := &Handler{}
			h.handleStream(rec, req, resp, "cid-leak-regression", "deepseek-v4-pro", "prompt", 0, false, false, tc.tools, nil, nil)

			frames, done := parseSSEDataFrames(t, rec.Body.String())
			if !done {
				t.Fatalf("expected [DONE], body=%s", rec.Body.String())
			}
			if !streamHasToolCallsDelta(frames) {
				t.Fatalf("expected tool_calls delta, body=%s", rec.Body.String())
			}
			var content strings.Builder
			for _, frame := range frames {
				choices, _ := frame["choices"].([]any)
				for _, item := range choices {
					choice, _ := item.(map[string]any)
					delta, _ := choice["delta"].(map[string]any)
					content.WriteString(asString(delta["content"]))
				}
			}
			if leaked := content.String(); strings.Contains(leaked, "DSML") || strings.Contains(leaked, "EPSE") {
				t.Fatalf("leaked tool markup as content: %q", leaked)
			}
			if streamFinishReason(frames) != "tool_calls" {
				t.Fatalf("expected finish_reason=tool_calls, body=%s", rec.Body.String())
			}
		})
	}
}
