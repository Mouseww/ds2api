package openai

import (
	"strings"
	"testing"
)

func TestSanitizeLeakedOutputRemovesEmptyJSONFence(t *testing.T) {
	raw := "before\n```json\n```\nafter"
	got := sanitizeLeakedOutput(raw)
	if got != "before\n\nafter" {
		t.Fatalf("unexpected sanitized empty json fence: %q", got)
	}
}

func TestSanitizeLeakedOutputRemovesLeakedWireToolCallAndResult(t *testing.T) {
	raw := "开始\n[{\"function\":{\"arguments\":\"{\\\"command\\\":\\\"java -version\\\"}\",\"name\":\"exec\"},\"id\":\"callb9a321\",\"type\":\"function\"}]< | Tool | >{\"content\":\"openjdk version 21\",\"tool_call_id\":\"callb9a321\"}\n结束"
	got := sanitizeLeakedOutput(raw)
	if got != "开始\n\n结束" {
		t.Fatalf("unexpected sanitize result for leaked wire format: %q", got)
	}
}

func TestSanitizeLeakedOutputRemovesStandaloneMetaMarkers(t *testing.T) {
	raw := "A<| end_of_sentence |><| Assistant |>B<| end_of_thinking |>C<|end▁of▁thinking|>D<|end▁of▁sentence|>E<| end_of_toolresults |>F<|end▁of▁instructions|>G"
	got := sanitizeLeakedOutput(raw)
	if got != "ABCDEFG" {
		t.Fatalf("unexpected sanitize result for meta markers: %q", got)
	}
}

func TestSanitizeLeakedOutputRemovesRoleMarkers(t *testing.T) {
	// The model echoes ds2api's own prompt role markers back into visible
	// output. The marker itself must be stripped while the trailing content
	// is preserved.
	raw := "<Tool>:=== 两份文档是否存在 ===\n" +
		"-rw-r--r-- 1 webber 197121 8833 QUESTION_TOOL_FRONTEND_CHANGES.md\n" +
		"<System>:system rule\n<User>:question\n<Assistant>:answer\n<Tool>:tool result"
	got := sanitizeLeakedOutput(raw)
	for _, marker := range []string{"<System>:", "<User>:", "<Assistant>:", "<Tool>:"} {
		if strings.Contains(got, marker) {
			t.Fatalf("role marker %q leaked: %q", marker, got)
		}
	}
	if !strings.Contains(got, "两份文档是否存在") || !strings.Contains(got, "QUESTION_TOOL_FRONTEND_CHANGES.md") {
		t.Fatalf("expected marker content to be preserved, got %q", got)
	}
	if !strings.Contains(got, "system rule") || !strings.Contains(got, "tool result") {
		t.Fatalf("expected role content to be preserved, got %q", got)
	}
}

func TestSanitizeLeakedOutputRemovesReasoningMarkers(t *testing.T) {
	raw := "prefix [reasoning_content]\ninternal reasoning\n[/reasoning_content] suffix"
	got := sanitizeLeakedOutput(raw)
	if strings.Contains(got, "reasoning_content") {
		t.Fatalf("reasoning marker leaked: %q", got)
	}
	if !strings.Contains(got, "prefix") || !strings.Contains(got, "suffix") || !strings.Contains(got, "internal reasoning") {
		t.Fatalf("expected reasoning-adjacent content to be preserved, got %q", got)
	}
}

func TestSanitizeLeakedOutputRemovesFullwidthDelimitedMetaMarkers(t *testing.T) {
	fw := "\uff5c"
	raw := "A<" + fw + "end▁of▁sentence" + fw + ">B<" + fw + " Assistant " + fw + ">C<" + fw + "end_of_toolresults" + fw + ">D"
	got := sanitizeLeakedOutput(raw)
	if got != "ABCD" {
		t.Fatalf("unexpected sanitize result for fullwidth-delimited meta markers: %q", got)
	}
}

func TestSanitizeLeakedOutputRemovesThinkAndBosMarkers(t *testing.T) {
	raw := "A<think>B</think>C<|begin▁of▁sentence|>D<| begin_of_sentence |>E<|begin_of_sentence|>F"
	got := sanitizeLeakedOutput(raw)
	if got != "ABCDEF" {
		t.Fatalf("unexpected sanitize result for think/BOS markers: %q", got)
	}
}

func TestSanitizeLeakedOutputRemovesThoughtMarkers(t *testing.T) {
	raw := "A<|▁of▁thought|>B<| of_thought |>C<| begin_of_thought |>D<| end_of_thought |>E"
	got := sanitizeLeakedOutput(raw)
	if got != "ABCDE" {
		t.Fatalf("unexpected sanitize result for leaked thought markers: %q", got)
	}
}

func TestSanitizeLeakedOutputRemovesFullwidthDelimitedBosAndThoughtMarkers(t *testing.T) {
	fw := "\uff5c"
	raw := "A<" + fw + "begin▁of▁sentence" + fw + ">B<" + fw + "▁of▁thought" + fw + ">C<" + fw + " begin_of_thought " + fw + ">D"
	got := sanitizeLeakedOutput(raw)
	if got != "ABCD" {
		t.Fatalf("unexpected sanitize result for fullwidth-delimited BOS/thought markers: %q", got)
	}
}

func TestSanitizeLeakedOutputRemovesDanglingThinkBlock(t *testing.T) {
	raw := "Answer prefix<think>internal reasoning that never closes"
	got := sanitizeLeakedOutput(raw)
	if got != "Answer prefix" {
		t.Fatalf("unexpected sanitize result for dangling think block: %q", got)
	}
}

func TestSanitizeLeakedOutputRemovesCompleteDSMLToolCallWrapper(t *testing.T) {
	raw := "前置文本\n<|DSML|tool_calls>\n<|DSML|invoke name=\"Bash\">\n<|DSML|parameter name=\"command\"></|DSML|parameter>\n</|DSML|invoke>\n</|DSML|tool_calls>\n后置文本"
	got := sanitizeLeakedOutput(raw)
	if got != "前置文本\n\n后置文本" {
		t.Fatalf("unexpected sanitize result for leaked dsml wrapper: %q", got)
	}
}

func TestSanitizeLeakedOutputStripsOrphanedDSMLToolCallMarkup(t *testing.T) {
	// A malformed tool call the model emitted: a stray closing wrapper, a bare
	// tool-name tag, orphaned parameter tags and dangling CDATA markers. None
	// of it forms a complete <tool_calls> block, so the sieve passes it through
	// as text and this sanitizer is the last line of defense against the markup
	// becoming visible to the user.
	raw := "</|DSML|tool_calls><Tool>:echo hi\n" +
		"']]></|DSML|parameter>\n" +
		"<|DSML|parameter name=\"description\"><![CDATA[拉取服务器证据]]></|DSML|parameter>\n" +
		"<|DSML|parameter name=\"timeout\"><![CDATA[120000]]></|DSML|parameter>\n" +
		"</|DSML|parameter>\n" +
		"</|DSML|tool_calls>\n" +
		"</|DSML|tool_calls>"
	got := sanitizeLeakedOutput(raw)
	if strings.Contains(got, "DSML") {
		t.Fatalf("orphaned DSML markup leaked: %q", got)
	}
	if strings.Contains(got, "CDATA") || strings.Contains(got, "]]>") {
		t.Fatalf("CDATA markers leaked: %q", got)
	}
	if !strings.Contains(got, "echo hi") {
		t.Fatalf("expected command text to be preserved, got %q", got)
	}
}

func TestSanitizeLeakedOutputRemovesAgentXMLLeaks(t *testing.T) {
	raw := "Done.<attempt_completion><result>Some final answer</result></attempt_completion>"
	got := sanitizeLeakedOutput(raw)
	if got != "Done.Some final answer" {
		t.Fatalf("unexpected sanitize result for agent XML leak: %q", got)
	}
}

func TestSanitizeLeakedOutputPreservesStandaloneResultTags(t *testing.T) {
	raw := "Example XML: <result>value</result>"
	got := sanitizeLeakedOutput(raw)
	if got != raw {
		t.Fatalf("unexpected sanitize result for standalone result tag: %q", got)
	}
}

func TestSanitizeLeakedOutputRemovesDanglingAgentXMLOpeningTags(t *testing.T) {
	raw := "Done.<attempt_completion><result>Some final answer"
	got := sanitizeLeakedOutput(raw)
	if got != "Done.Some final answer" {
		t.Fatalf("unexpected sanitize result for dangling opening tags: %q", got)
	}
}

func TestSanitizeLeakedOutputRemovesDanglingAgentXMLClosingTags(t *testing.T) {
	raw := "Done.Some final answer</result></attempt_completion>"
	got := sanitizeLeakedOutput(raw)
	if got != "Done.Some final answer" {
		t.Fatalf("unexpected sanitize result for dangling closing tags: %q", got)
	}
}

func TestSanitizeLeakedOutputPreservesUnrelatedResultTagsWhenWrapperLeaks(t *testing.T) {
	raw := "Done.<attempt_completion><result>Some final answer\nExample XML: <result>value</result>"
	got := sanitizeLeakedOutput(raw)
	want := "Done.Some final answer\nExample XML: <result>value</result>"
	if got != want {
		t.Fatalf("unexpected sanitize result for mixed leaked wrapper + xml example: %q", got)
	}
}
