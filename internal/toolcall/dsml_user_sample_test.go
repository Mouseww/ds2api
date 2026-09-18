package toolcall

import (
	"strings"
	"testing"
)

// TestUserExactDSMLSample verifies the exact raw sample reported by the user
// (double fullwidth pipes after '<' and after DSML, 'calls' shorthand wrapper,
// string="true"/"false" parameter attributes, plain-text non-CDATA values).
func TestUserExactDSMLSample(t *testing.T) {
	text := "<｜｜DSML｜｜ calls>\n" +
		"<｜｜DSML｜｜ invoke name=\"Bash\">\n" +
		"<｜｜DSML｜｜ parameter name=\"command\" string=\"true\">ssh -o StrictHostKeyChecking=no root@192.168.31.133 'docker ps --filter name=research-frontend --filter name=research-agent-runtime --format \"{{.Names}}|{{.Status}}\"' 2>&1 | tail -10</｜｜DSML｜｜ parameter>\n" +
		"<｜｜DSML｜｜ parameter name=\"description\" string=\"true\">检查两个容器运行状态</｜｜DSML｜｜ parameter>\n" +
		"<｜｜DSML｜｜ parameter name=\"timeout\" string=\"false\">120000</｜｜DSML｜｜ parameter>\n" +
		"</｜｜DSML｜｜ invoke>\n" +
		"<｜｜DSML｜｜ invoke name=\"Bash\">\n" +
		"<｜｜DSML｜｜ parameter name=\"command\" string=\"true\">ssh -o StrictHostKeyChecking=no root@192.168.31.133 'echo \"=== 新组件文案 ===\"; docker exec research-frontend sh -c \"grep -rl \"AI 需要您的帮助\" /app/.next 2>/dev/null | head -3\"; echo \"=== 旧弹窗文案(空=已清除) ===\"; docker exec research-frontend sh -c \"grep -rl \"请选择或输入您的答案\" /app/.next 2>/dev/null | head -3\"; echo \"=== 新交互文案 ===\"; docker exec research-frontend sh -c \"grep -rl \"AI 会等你的回答再继续\" /app/.next 2>/dev/null | head -3\"' 2>&1 | tail -20</｜｜DSML｜｜ parameter>\n" +
		"<｜｜DSML｜｜ parameter name=\"description\" string=\"true\">在生产构建产物中验证新旧组件文案</｜｜DSML｜｜ parameter>\n" +
		"<｜｜DSML｜｜ parameter name=\"timeout\" string=\"false\">120000</｜｜DSML｜｜ parameter>\n" +
		"</｜｜DSML｜｜ invoke>\n" +
		"</｜｜DSML｜｜ calls>"

	res := ParseToolCallsDetailed(text, []string{"Bash"})
	if len(res.Calls) != 2 {
		t.Fatalf("got %d calls, want 2; sawSyntax=%v calls=%+v", len(res.Calls), res.SawToolCallSyntax, res.Calls)
	}
	first := res.Calls[0]
	if first.Name != "Bash" {
		t.Fatalf("first call name = %q, want Bash", first.Name)
	}
	cmd, _ := first.Input["command"].(string)
	if !strings.Contains(cmd, "docker ps --filter name=research-frontend") || !strings.Contains(cmd, "tail -10") {
		t.Fatalf("command param mismatch: %q", cmd)
	}
	desc, _ := first.Input["description"].(string)
	if desc != "检查两个容器运行状态" {
		t.Fatalf("description param mismatch: %q", desc)
	}
	if _, ok := first.Input["timeout"]; !ok {
		t.Fatalf("timeout param missing: %+v", first.Input)
	}
	second := res.Calls[1]
	if second.Name != "Bash" {
		t.Fatalf("second call name = %q, want Bash", second.Name)
	}
	cmd2, _ := second.Input["command"].(string)
	if !strings.Contains(cmd2, "grep -rl") || !strings.Contains(cmd2, "AI 需要您的帮助") {
		t.Fatalf("second command param mismatch: %q", cmd2)
	}
}
