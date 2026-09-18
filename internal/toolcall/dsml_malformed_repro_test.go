package toolcall

import "testing"

func TestDSMLMalformedVariantsRepro(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantCalls int
	}{
		{
			name:      "canonical",
			text:      "<｜DSML｜tool_calls><｜DSML｜invoke name=\"run_code\"><｜DSML｜parameter name=\"code\"><![CDATA[echo hi]]></｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>",
			wantCalls: 1,
		},
		{
			name:      "doublepipe_calls",
			text:      "<｜DSML｜｜ calls><｜DSML｜｜ invoke name=\"run_code\"><｜DSML｜｜ parameter name=\"code\"><![CDATA[echo hi]]></｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>",
			wantCalls: 1,
		},
		{
			name:      "doublepipe_invoke_only",
			text:      "<｜DSML｜tool_calls><｜DSML｜｜ invoke name=\"run_code\"><｜DSML｜parameter name=\"code\"><![CDATA[echo hi]]></｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>",
			wantCalls: 1,
		},
		{
			name:      "doublepipe_parameter_string_attr",
			text:      "<｜DSML｜tool_calls><｜DSML｜invoke name=\"run_code\"><｜DSML｜｜ parameter name=\"code\" string=\"true\"><![CDATA[echo hi]]></｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>",
			wantCalls: 1,
		},
		{
			// 'calls' is too generic to validate as a bare markup prefix:
			// prose that merely mentions <calls must stay plain text.
			name:      "plain_calls_angle_stays_text",
			text:      "The gateway exposes <calls and <invokes as internal helpers, nothing else.",
			wantCalls: 0,
		},
	}

	for _, tc := range cases {
		res := ParseToolCallsDetailed(tc.text, []string{"run_code"})
		got := len(res.Calls)
		status := "OK"
		if got != tc.wantCalls {
			status = "FAIL"
		}
		t.Logf("[%s] %-34s got=%d want=%d sawSyntax=%v calls=%+v", status, tc.name, got, tc.wantCalls, res.SawToolCallSyntax, res.Calls)
		if got != tc.wantCalls {
			t.Errorf("%s: got %d calls, want %d", tc.name, got, tc.wantCalls)
		}
	}
}
