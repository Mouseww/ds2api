package shared

import (
	"regexp"
	"strings"

	"ds2api/internal/toolcall"
)

var emptyJSONFencePattern = regexp.MustCompile("(?is)```json\\s*```")
var leakedToolCallArrayPattern = regexp.MustCompile(`(?is)\[\{\s*"function"\s*:\s*\{[\s\S]*?\}\s*,\s*"id"\s*:\s*"call[^"]*"\s*,\s*"type"\s*:\s*"function"\s*}\]`)
var leakedToolResultBlobPattern = regexp.MustCompile(`(?is)<\s*\|\s*tool\s*\|\s*>\s*\{[\s\S]*?"tool_call_id"\s*:\s*"call[^"]*"\s*}`)

var leakedThinkTagPattern = regexp.MustCompile(`(?is)</?\s*think\s*>`)

// leakedBOSMarkerPattern matches DeepSeek BOS markers with halfwidth or
// legacy U+FF5C fullwidth delimiters:
//   - ASCII underscore: <|begin_of_sentence|>
//   - U+2581 variant:   <|begin▁of▁sentence|>
var leakedBOSMarkerPattern = regexp.MustCompile(`(?i)<[\|\x{ff5c}]\s*begin[_▁]of[_▁]sentence\s*[\|\x{ff5c}]>`)

// leakedThoughtMarkerPattern matches leaked thought control markers in both
// explicit and compact forms:
//   - ASCII underscore: <| of_thought |>, <| begin_of_thought |>
//   - U+2581 variant:   <|▁of▁thought|>, <|begin▁of▁thought|>
var leakedThoughtMarkerPattern = regexp.MustCompile(`(?i)<[\|\x{ff5c}]\s*(?:begin[_▁])?[_▁]*of[_▁]thought\s*[\|\x{ff5c}]>`)

// leakedMetaMarkerPattern matches the remaining DeepSeek special tokens with
// halfwidth or legacy U+FF5C fullwidth delimiters:
//   - ASCII underscore: <|end_of_sentence|>, <|end_of_toolresults|>, <|end_of_instructions|>
//   - U+2581 variant:   <|end▁of▁sentence|>, <|end▁of▁toolresults|>, <|end▁of▁instructions|>
var leakedMetaMarkerPattern = regexp.MustCompile(`(?i)<[\|\x{ff5c}]\s*(?:assistant|tool|end[_▁]of[_▁]sentence|end[_▁]of[_▁]thinking|end[_▁]of[_▁]thought|end[_▁]of[_▁]toolresults|end[_▁]of[_▁]instructions)\s*[\|\x{ff5c}]>`)

// leakedRoleMarkerPattern matches ds2api's own prompt role markers
// (<System>:, <User>:, <Assistant>:, <Tool>:) that the model echoes back in
// its visible output. The prompt layer injects these to delimit role blocks
// (internal/prompt/messages.go); they must never surface as API content.
var leakedRoleMarkerPattern = regexp.MustCompile(`(?i)<(?:System|User|Assistant|Tool)>:\s*`)

// leakedRoleClosingTagPattern matches the XML-style closing forms
// (</Assistant>, </Tool>, ...) the model emits when it keeps "closing" the
// transcript structure it saw in the prompt. Opening markers carry a colon;
// closing tags never do, which is why they need their own pattern.
var leakedRoleClosingTagPattern = regexp.MustCompile(`(?i)<\s*/\s*(?:System|User|Assistant|Tool)\s*>`)

// leakedRoleBlockMarkerPattern finds every role marker relevant for block
// suppression: opening markers with colon plus closing tags.
var leakedRoleBlockMarkerPattern = regexp.MustCompile(`(?i)<\s*/\s*(?:System|User|Assistant|Tool)\s*>|<\s*(?:System|User|Assistant|Tool)\s*>:`)

// leakedReasoningBlockPattern matches a complete echoed reasoning-history
// block. The prompt layer wraps assistant reasoning as
// [reasoning_content] ... [/reasoning_content] (internal/promptcompat);
// when the model echoes that structure into its visible output the whole
// block is leaked thinking, not an answer.
var leakedReasoningBlockPattern = regexp.MustCompile(`(?s)\[reasoning_content\][\s\S]*?\[/reasoning_content\]`)

// leakedThinkBlockPattern matches a complete echoed
// block in the content channel. The real thinking is carried in the
// reasoning channel; a think block in visible text is mis-channeled thinking
// and is removed with its content.
var leakedThinkBlockPattern = regexp.MustCompile(`(?is)<\s*think\s*>[\s\S]*?<\s*/\s*think\s*>`)

// leakedReasoningMarkerPattern matches the reasoning history brackets that the
// prompt layer uses to wrap assistant reasoning ([reasoning_content] ...
// [/reasoning_content]). The real reasoning content is carried in the
// reasoning/thinking channel, so these brackets in visible text are leaked
// prompt markup.
var leakedReasoningMarkerPattern = regexp.MustCompile(`\[/?reasoning_content\]`)

// leakedAgentXMLBlockPatterns catch agent-style XML blocks that leak through
// when the sieve fails to capture them. These are applied only to complete
// wrapper blocks so standalone "<result>" examples in normal output remain
// untouched.
var leakedAgentXMLBlockPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<attempt_completion\b[^>]*>(.*?)</attempt_completion>`),
	regexp.MustCompile(`(?is)<ask_followup_question\b[^>]*>(.*?)</ask_followup_question>`),
	regexp.MustCompile(`(?is)<new_task\b[^>]*>(.*?)</new_task>`),
}

var leakedAgentWrapperTagPattern = regexp.MustCompile(`(?is)</?(?:attempt_completion|ask_followup_question|new_task)\b[^>]*>`)
var leakedAgentWrapperPlusResultOpenPattern = regexp.MustCompile(`(?is)<(?:attempt_completion|ask_followup_question|new_task)\b[^>]*>\s*<result>`)
var leakedAgentResultPlusWrapperClosePattern = regexp.MustCompile(`(?is)</result>\s*</(?:attempt_completion|ask_followup_question|new_task)\b[^>]*>`)
var leakedAgentResultTagPattern = regexp.MustCompile(`(?is)</?result>`)

func sanitizeLeakedOutput(text string) string {
	if text == "" {
		return text
	}
	out := emptyJSONFencePattern.ReplaceAllString(text, "")
	out = leakedToolCallArrayPattern.ReplaceAllString(out, "")
	out = leakedToolResultBlobPattern.ReplaceAllString(out, "")
	// Echoed reasoning history and think blocks are removed with their
	// content: the brackets/tags alone are prompt structure, and what sits
	// between them is leaked thinking, never part of the answer.
	out = leakedReasoningBlockPattern.ReplaceAllString(out, "")
	out = leakedThinkBlockPattern.ReplaceAllString(out, "")
	out = stripDanglingThinkSuffix(out)
	out = leakedThinkTagPattern.ReplaceAllString(out, "")
	out = leakedBOSMarkerPattern.ReplaceAllString(out, "")
	out = leakedThoughtMarkerPattern.ReplaceAllString(out, "")
	out = leakedMetaMarkerPattern.ReplaceAllString(out, "")
	// Echoed role blocks: a <User>/<System>/<Tool> marker in visible output
	// means the model is replaying prompt structure, so the marker AND the
	// content that follows (until the next role marker) are dropped.
	out, _ = applyRoleBlockSuppression(out, false)
	out = leakedRoleMarkerPattern.ReplaceAllString(out, "")
	out = leakedRoleClosingTagPattern.ReplaceAllString(out, "")
	out = leakedReasoningMarkerPattern.ReplaceAllString(out, "")
	out = stripLeakedToolCallWrapperBlocks(out)
	out = sanitizeLeakedAgentXMLBlocks(out)
	return out
}

// applyRoleBlockSuppression removes echoed role-marker blocks and reports
// whether suppression is still active at the end of text. The inside
// parameter carries suppression state across streaming parts: once a
// <User>:/<System>:/<Tool>: marker opens a block, everything until the next
// role marker (opening or closing) is leaked context, not the model's
// answer. An <Assistant>: marker only loses the marker itself, because
// content after it is the model answering in its own voice.
func applyRoleBlockSuppression(text string, inside bool) (string, bool) {
	if text == "" {
		return text, inside
	}
	matches := leakedRoleBlockMarkerPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		if inside {
			return "", true
		}
		return text, false
	}
	var b strings.Builder
	pos := 0
	suppressed := inside
	for _, m := range matches {
		start, end := m[0], m[1]
		if !suppressed && start > pos {
			b.WriteString(text[pos:start])
		}
		marker := text[start:end]
		lower := strings.ToLower(marker)
		switch {
		case strings.Contains(lower, "/"):
			// Closing role tag ends suppression; content after it resumes.
			suppressed = false
		case strings.Contains(lower, "assistant"):
			// The model prefixing its own answer with its role marker is
			// answer content, not leaked context.
			suppressed = false
		default:
			// <User>: / <System>: / <Tool>: open (or continue) a leaked
			// context block.
			suppressed = true
		}
		pos = end
	}
	if !suppressed && pos < len(text) {
		b.WriteString(text[pos:])
	}
	return b.String(), suppressed
}

// doubledParameterNamePattern collapses the doubled parameter-name attribute
// the corrupted DSML emission repeats (parameter name="parameter
// name="pattern"). The doubled quote breaks quote-aware tag scanning, so it
// is collapsed before tool-markup stripping.
var doubledParameterNamePattern = regexp.MustCompile(`(?i)(parameter\s+name=")+`)

func stripLeakedToolCallWrapperBlocks(text string) string {
	if text == "" {
		return text
	}
	text = doubledParameterNamePattern.ReplaceAllString(text, `parameter name="`)
	var b strings.Builder
	pos := 0
	for pos < len(text) {
		tag, ok := toolcall.FindToolMarkupTagOutsideIgnored(text, pos)
		if !ok {
			b.WriteString(text[pos:])
			break
		}
		if tag.Start > pos {
			b.WriteString(text[pos:tag.Start])
		}
		// A complete <tool_calls>...</tool_calls> block is stripped entirely.
		if !tag.Closing && tag.Name == "tool_calls" {
			if closeTag, ok := toolcall.FindMatchingToolMarkupClose(text, tag); ok {
				pos = closeTag.End + 1
				continue
			}
		}
		// Any other recognized tool-call markup tag is leaked markup that the
		// stream sieve failed to capture: a stray closing wrapper, an orphaned
		// parameter/invoke tag, or an unclosed wrapper opening. Drop the tag
		// itself while keeping the surrounding text.
		pos = tag.End + 1
	}
	return stripLeakedCDATAMarkers(b.String())
}

// leakedCDATAOpenPattern and leakedCDATAClosePattern strip the CDATA wrappers
// that remain when an orphaned parameter tag is dropped. The content inside
// the CDATA is preserved; only the <![CDATA[ ... ]]> delimiters are removed.
var leakedCDATAOpenPattern = regexp.MustCompile(`<!\[CDATA\[`)
var leakedCDATAClosePattern = regexp.MustCompile(`\]\]>`)

func stripLeakedCDATAMarkers(text string) string {
	if text == "" {
		return text
	}
	if !strings.Contains(text, "CDATA") && !strings.Contains(text, "]]>") {
		return text
	}
	out := leakedCDATAOpenPattern.ReplaceAllString(text, "")
	return leakedCDATAClosePattern.ReplaceAllString(out, "")
}

func stripDanglingThinkSuffix(text string) string {
	matches := leakedThinkTagPattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	depth := 0
	lastOpen := -1
	for _, loc := range matches {
		tag := strings.ToLower(text[loc[0]:loc[1]])
		compact := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(tag), " ", ""), "\t", "")
		if strings.HasPrefix(compact, "</") {
			if depth > 0 {
				depth--
				if depth == 0 {
					lastOpen = -1
				}
			}
			continue
		}
		if depth == 0 {
			lastOpen = loc[0]
		}
		depth++
	}
	if depth == 0 || lastOpen < 0 {
		return text
	}
	prefix := text[:lastOpen]
	if strings.TrimSpace(prefix) == "" {
		return ""
	}
	return prefix
}

func sanitizeLeakedAgentXMLBlocks(text string) string {
	out := text
	for _, pattern := range leakedAgentXMLBlockPatterns {
		out = pattern.ReplaceAllStringFunc(out, func(match string) string {
			submatches := pattern.FindStringSubmatch(match)
			if len(submatches) < 2 {
				return match
			}
			// Preserve the inner text so leaked agent instructions do not erase
			// the actual answer, but strip the wrapper/result markup itself.
			return leakedAgentResultTagPattern.ReplaceAllString(submatches[1], "")
		})
	}
	// Fallback for truncated output streams: strip any dangling wrapper tags
	// that were not part of a complete block replacement. If we detect leaked
	// wrapper tags, strip only adjacent <result> tags to avoid exposing agent
	// markup without altering unrelated user-visible <result> examples.
	if leakedAgentWrapperTagPattern.MatchString(out) {
		out = leakedAgentWrapperPlusResultOpenPattern.ReplaceAllStringFunc(out, func(match string) string {
			return leakedAgentResultTagPattern.ReplaceAllString(match, "")
		})
		out = leakedAgentResultPlusWrapperClosePattern.ReplaceAllStringFunc(out, func(match string) string {
			return leakedAgentResultTagPattern.ReplaceAllString(match, "")
		})
		out = leakedAgentWrapperTagPattern.ReplaceAllString(out, "")
	}
	return out
}
