package toolstream

import (
	"strings"
	"testing"
)

// userExactDSMLSample is the verbatim sample reported by the user: double
// fullwidth pipes after '<' and after DSML, 'calls' shorthand wrapper,
// string="true"/"false" parameter attributes, plain-text (non-CDATA) values.
const userExactDSMLSample = "<｜｜DSML｜｜ calls>\n" +
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

func runUserSampleSieve(t *testing.T, chunkSize int) {
	t.Helper()
	var state State
	var events []Event
	for i := 0; i < len(userExactDSMLSample); i += chunkSize {
		end := i + chunkSize
		if end > len(userExactDSMLSample) {
			end = len(userExactDSMLSample)
		}
		events = append(events, ProcessChunk(&state, userExactDSMLSample[i:end], []string{"Bash"})...)
	}
	events = append(events, Flush(&state, []string{"Bash"})...)

	var textContent strings.Builder
	var toolCalls int
	for _, evt := range events {
		if evt.Content != "" {
			textContent.WriteString(evt.Content)
		}
		toolCalls += len(evt.ToolCalls)
	}

	if toolCalls != 2 {
		t.Fatalf("expected two tool calls, got %d events=%#v", toolCalls, events)
	}
	leaked := textContent.String()
	if strings.Contains(leaked, "DSML") || strings.Contains(leaked, "Bash") || strings.Contains(leaked, "ssh ") {
		t.Fatalf("user sample tool call leaked to text: %q", leaked)
	}
	for _, evt := range events {
		for _, call := range evt.ToolCalls {
			cmd, _ := call.Input["command"].(string)
			if !strings.Contains(cmd, "docker") {
				t.Fatalf("command param mismatch: %q", cmd)
			}
			if _, ok := call.Input["description"]; !ok {
				t.Fatalf("description param missing: %+v", call.Input)
			}
			if _, ok := call.Input["timeout"]; !ok {
				t.Fatalf("timeout param missing: %+v", call.Input)
			}
		}
	}
}

func TestSieveUserExactDSMLSampleWholeChunk(t *testing.T) {
	runUserSampleSieve(t, len(userExactDSMLSample))
}

func TestSieveUserExactDSMLSampleTinyChunks(t *testing.T) {
	runUserSampleSieve(t, 3)
}

func TestSieveUserExactDSMLSampleRuneChunks(t *testing.T) {
	runUserSampleSieve(t, 1)
}
