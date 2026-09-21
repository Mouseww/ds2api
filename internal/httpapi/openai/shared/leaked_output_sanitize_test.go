package shared

import (
	"strings"
	"testing"

	"ds2api/internal/sse"
)

func lineResultWithTextPart(text string) sse.LineResult {
	return sse.LineResult{Parts: []sse.ContentPart{{Type: "text", Text: text}}}
}

// These tests pin the visible-output sanitizer against the three leak classes
// reported in production:
//  1. thinking content leaking as visible text (reasoning history echo,
//     <think> block echo)
//  2. structural XML markers output directly (closing role tags such as
//     </Assistant>, </Tool>)
//  3. tool / operation info leaking as visible text (echoed <Tool>: role
//     blocks carrying tool-result payloads)

func TestSanitizeStripsClosingRoleTags(t *testing.T) {
	in := "answer text</Assistant>\n</Tool>\nmore"
	out := sanitizeLeakedOutput(in)
	if strings.Contains(out, "</Assistant>") || strings.Contains(out, "</Tool>") {
		t.Fatalf("expected closing role tags stripped, got %q", out)
	}
	if !strings.Contains(out, "answer text") || !strings.Contains(out, "more") {
		t.Fatalf("expected surrounding text kept, got %q", out)
	}
}

func TestSanitizeStripsReasoningHistoryBlockEntirely(t *testing.T) {
	in := "before\n[reasoning_content]\nthe model's hidden deliberation\n[/reasoning_content]\nafter"
	out := sanitizeLeakedOutput(in)
	if strings.Contains(out, "hidden deliberation") {
		t.Fatalf("expected echoed reasoning content removed, got %q", out)
	}
	if strings.Contains(out, "reasoning_content") {
		t.Fatalf("expected reasoning brackets removed, got %q", out)
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Fatalf("expected surrounding text kept, got %q", out)
	}
}

func TestSanitizeStripsThinkBlockContentEntirely(t *testing.T) {
	in := "a<think>\nsecret chain of thought\n</think>b"
	out := sanitizeLeakedOutput(in)
	if strings.Contains(out, "secret chain of thought") {
		t.Fatalf("expected think block content removed, got %q", out)
	}
	if strings.Contains(out, "think") {
		t.Fatalf("expected think tags removed, got %q", out)
	}
	if !strings.Contains(out, "a") || !strings.Contains(out, "b") {
		t.Fatalf("expected surrounding text kept, got %q", out)
	}
}

func TestSanitizeStripsEchoedRoleBlockContent(t *testing.T) {
	t.Run("tool block with closing tag resumes after it", func(t *testing.T) {
		in := "real answer\n<Tool>: <path>E:\\x\\README.MD</path> <type>file</type> <content>file body</content>\n</Tool>\nresumed answer"
		out := sanitizeLeakedOutput(in)
		if strings.Contains(out, "README.MD") || strings.Contains(out, "file body") || strings.Contains(out, "<path>") {
			t.Fatalf("expected echoed tool-result block removed, got %q", out)
		}
		if !strings.Contains(out, "real answer") || !strings.Contains(out, "resumed answer") {
			t.Fatalf("expected answer text kept, got %q", out)
		}
	})

	t.Run("user and system blocks are removed with their content", func(t *testing.T) {
		in := "answer\n<User>: next user message echo\n</User>\n<System>: next system echo\n</System>\nfinal"
		out := sanitizeLeakedOutput(in)
		if strings.Contains(out, "next user message echo") || strings.Contains(out, "next system echo") {
			t.Fatalf("expected echoed role blocks removed, got %q", out)
		}
		if !strings.Contains(out, "answer") || !strings.Contains(out, "final") {
			t.Fatalf("expected surrounding text kept, got %q", out)
		}
	})

	t.Run("consecutive unclosed role blocks suppress to the end", func(t *testing.T) {
		in := "answer\n<User>: echo one\n<System>: echo two\ntrailing text is echo too"
		out := sanitizeLeakedOutput(in)
		if strings.Contains(out, "echo one") || strings.Contains(out, "echo two") || strings.Contains(out, "trailing text") {
			t.Fatalf("expected unclosed role blocks to suppress through the end, got %q", out)
		}
		if !strings.Contains(out, "answer") {
			t.Fatalf("expected text before the blocks kept, got %q", out)
		}
	})

	t.Run("unclosed tool block suppresses to the end", func(t *testing.T) {
		in := "answer\n<Tool>: tool result payload without closer"
		out := sanitizeLeakedOutput(in)
		if strings.Contains(out, "tool result payload") {
			t.Fatalf("expected unclosed role block suppressed to end, got %q", out)
		}
		if !strings.Contains(out, "answer") {
			t.Fatalf("expected text before the block kept, got %q", out)
		}
	})

	t.Run("assistant marker keeps its content", func(t *testing.T) {
		in := "<Assistant>: here is the actual answer"
		out := sanitizeLeakedOutput(in)
		if !strings.Contains(out, "here is the actual answer") {
			t.Fatalf("expected content after assistant marker kept, got %q", out)
		}
		if strings.Contains(out, "<Assistant>") {
			t.Fatalf("expected assistant marker itself stripped, got %q", out)
		}
	})
}

func TestSanitizeKeepsPlainProseUntouched(t *testing.T) {
	plain := "普通文本 with <angle brackets> and 2 > 1 and [brackets] too"
	if out := sanitizeLeakedOutput(plain); out != plain {
		t.Fatalf("expected plain text unchanged, got %q", out)
	}
}

// TestStreamAccumulatorSuppressesRoleBlocksAcrossParts guards the streaming
// path: an echoed role block split across several SSE parts must be suppressed
// in full, and an <Assistant>: marker must resume visible output.
func TestStreamAccumulatorSuppressesRoleBlocksAcrossParts(t *testing.T) {
	acc := StreamAccumulator{}

	part := func(text string) string {
		res := acc.Apply(lineResultWithTextPart(text))
		visible := ""
		for _, p := range res.Parts {
			visible += p.VisibleText
		}
		return visible
	}

	if got := part("real answer\n"); got != "real answer\n" {
		t.Fatalf("expected first part visible, got %q", got)
	}
	// The echoed tool-result block spans multiple parts.
	if got := part("<Tool>: <path>E:\\x\\f</path>"); got != "" {
		t.Fatalf("expected block-opening part suppressed, got %q", got)
	}
	if got := part(" <content>file body</content>\n"); got != "" {
		t.Fatalf("expected block-middle part suppressed, got %q", got)
	}
	if got := part("</Tool>\n"); strings.TrimSpace(got) != "" {
		t.Fatalf("expected block-closing part suppressed, got %q", got)
	}
	if got := part("resumed answer"); got != "resumed answer" {
		t.Fatalf("expected output to resume after the block, got %q", got)
	}
	if got := acc.Text.String(); !strings.Contains(got, "real answer") || !strings.Contains(got, "resumed answer") || strings.Contains(got, "file body") {
		t.Fatalf("unexpected accumulated visible text: %q", got)
	}
}
