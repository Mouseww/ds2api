package claude

import (
	"strings"
	"testing"
)

// TestClaudeCleanVisibleOutputStripsLeakedMarkup guards protocol parity of the
// visible-text sanitizer: the Claude stream used to strip only citation
// markers locally, so the same leaked DSML wrapper blocks, role markers, and
// think tags that the OpenAI surfaces strip were delivered as Claude content.
func TestClaudeCleanVisibleOutputStripsLeakedMarkup(t *testing.T) {
	t.Run("complete tool_calls wrapper block is stripped", func(t *testing.T) {
		leaked := "Sure, here is the plan.\n<tool_calls><invoke name=\"read_file\"><parameter name=\"path\"><![CDATA[README.MD]]></parameter></invoke></tool_calls>\nDone."
		got := cleanVisibleOutput(leaked, false)
		if strings.Contains(got, "tool_calls") || strings.Contains(got, "invoke") || strings.Contains(got, "CDATA") {
			t.Fatalf("expected leaked tool-call block stripped, got %q", got)
		}
		if !strings.Contains(got, "Sure, here is the plan.") || !strings.Contains(got, "Done.") {
			t.Fatalf("expected surrounding text preserved, got %q", got)
		}
	})

	t.Run("prompt role marker is stripped", func(t *testing.T) {
		got := cleanVisibleOutput("echo <System>: hidden instruction", false)
		if strings.Contains(got, "<System>:") {
			t.Fatalf("expected role marker stripped, got %q", got)
		}
	})

	t.Run("think blocks are removed with their content (shared semantics)", func(t *testing.T) {
		got := cleanVisibleOutput("a<think>secret</think>b", false)
		if strings.Contains(got, "think") || strings.Contains(got, "secret") {
			t.Fatalf("expected think block removed entirely, got %q", got)
		}
		if !strings.Contains(got, "a") || !strings.Contains(got, "b") {
			t.Fatalf("expected surrounding text preserved, got %q", got)
		}
	})

	t.Run("plain text is untouched", func(t *testing.T) {
		plain := "普通文本 with <angle brackets> and 2 > 1"
		if got := cleanVisibleOutput(plain, false); got != plain {
			t.Fatalf("expected plain text unchanged, got %q", got)
		}
	})
}
