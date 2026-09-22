'use strict';

// Mirrors the Go visible-output sanitizer
// (internal/httpapi/openai/shared/leaked_output_sanitize.go). The model can
// echo prompt structure into the content channel: role markers
// (<Tool>:, </Assistant>, ...), reasoning history blocks
// ([reasoning_content]...[/reasoning_content]), think blocks, and DeepSeek
// control tokens. This cleaner removes them from visible output.
//
// Role-block suppression is stateful across chunks: once an echoed
// <User>/<System>/<Tool> marker opens a block, following chunks stay
// invisible until the next role marker. An <Assistant> marker only loses the
// marker itself, because content after it is the model answering in its own
// voice.

const {
  findToolMarkupTagOutsideIgnored,
  findMatchingToolMarkupClose,
  sanitizeLooseCDATA,
} = require('../helpers/stream-tool-sieve/parse_payload');

const REASONING_BLOCK = /\[reasoning_content\][\s\S]*?\[\/reasoning_content\]/g;
const THINK_BLOCK = /<\s*think\s*>[\s\S]*?<\s*\/\s*think\s*>/gi;
const THINK_TAG = /<\/?\s*think\s*>/gi;
const ROLE_BLOCK_MARKER = /<\s*\/\s*(?:System|User|Assistant|Tool)\s*>|<\s*(?:System|User|Assistant|Tool)\s*>:/gi;
const ROLE_MARKER = /<(?:System|User|Assistant|Tool)>:\s*/gi;
const ROLE_CLOSING_TAG = /<\s*\/\s*(?:System|User|Assistant|Tool)\s*>/gi;
const REASONING_MARKER = /\[\/?reasoning_content\]/g;
const BOS_MARKER = /<[\|\uff5c]\s*begin[_\u2581]of[_\u2581]sentence\s*[\|\uff5c]>/gi;
const THOUGHT_MARKER = /<[\|\uff5c]\s*(?:begin[_\u2581])?[_\u2581]*of[_\u2581]thought\s*[\|\uff5c]>/gi;
const META_MARKER = /<[\|\uff5c]\s*(?:assistant|tool|end[_\u2581]of[_\u2581]sentence|end[_\u2581]of[_\u2581]thinking|end[_\u2581]of[_\u2581]thought|end[_\u2581]of[_\u2581]toolresults|end[_\u2581]of[_\u2581]instructions)\s*[\|\uff5c]>/gi;

function stripDanglingThinkSuffix(text) {
  const matches = [...text.matchAll(THINK_TAG)];
  if (matches.length === 0) {
    return text;
  }
  let depth = 0;
  let lastOpen = -1;
  for (const m of matches) {
    const tag = m[0].toLowerCase().replace(/[ \t]/g, '');
    if (tag.startsWith('</')) {
      if (depth > 0) {
        depth--;
        if (depth === 0) {
          lastOpen = -1;
        }
      }
      continue;
    }
    if (depth === 0) {
      lastOpen = m.index;
    }
    depth++;
  }
  if (depth === 0 || lastOpen < 0) {
    return text;
  }
  const prefix = text.slice(0, lastOpen);
  if (prefix.trim() === '') {
    return '';
  }
  return prefix;
}

function applyRoleBlockSuppression(text, inside) {
  if (!text) {
    return { text, inside };
  }
  const matches = [...text.matchAll(ROLE_BLOCK_MARKER)];
  if (matches.length === 0) {
    return inside ? { text: '', inside } : { text, inside };
  }
  let out = '';
  let pos = 0;
  let suppressed = inside;
  for (const m of matches) {
    const start = m.index;
    const end = start + m[0].length;
    if (!suppressed && start > pos) {
      out += text.slice(pos, start);
    }
    const lower = m[0].toLowerCase();
    if (lower.includes('/')) {
      // Closing role tag ends suppression; content after it resumes.
      suppressed = false;
    } else if (lower.includes('assistant')) {
      // The model prefixing its own answer with its role marker is answer
      // content, not leaked context.
      suppressed = false;
    } else {
      // <User>: / <System>: / <Tool>: open (or continue) a leaked block.
      suppressed = true;
    }
    pos = end;
  }
  if (!suppressed && pos < text.length) {
    out += text.slice(pos);
  }
  return { text: out, inside: suppressed };
}

// malformedDSMLAttemptPattern matches DSML parameter/invoke tags in every
// corruption variant observed in production transcripts (fullwidth doubled
// ｜ pipes, halfwidth canonical, space separator, collapsed). Wrapper-only
// mentions do not match: prose naming the wrapper is documentation, not an
// attempt. Mirrors assistantturn.DetectMalformedToolCallAttempt in Go.
const malformedDSMLAttemptPattern = /[<][｜|]{0,2}DSML[｜|]{0,2}\s*(?:parameter|invoke)/;

// Detects a corrupted tool-call attempt in the RAW output text. Callers must
// combine this with "no tool calls were parsed": a complete block the sieve
// captured successfully is a working tool call, not a malformed attempt.
function detectMalformedDSMLToolAttempt(text) {
  if (!text) {
    return false;
  }
  return malformedDSMLAttemptPattern.test(text);
}

// doubledParameterNamePattern collapses the doubled parameter-name attribute
// the corrupted emission repeats (parameter name="parameter name="pattern").
// The doubled quote breaks quote-aware tag scanning, so it is collapsed
// before tool-markup stripping. Mirrors the Go sanitizer pre-pass.
const doubledParameterNamePattern = /(parameter\s+name=")+/gi;

// stripLeakedToolMarkup removes tool-call markup tags the sieve failed to
// capture, keeping the surrounding (and inner) text. Mirrors Go
// stripLeakedToolCallWrapperBlocks. Apply it to text AFTER the tool sieve so
// complete tool calls are still captured from the raw stream.
function stripLeakedToolMarkup(text) {
  if (!text) {
    return text;
  }
  let out = text.replace(doubledParameterNamePattern, 'parameter name="');
  let buf = '';
  let pos = 0;
  while (pos < out.length) {
    const tag = findToolMarkupTagOutsideIgnored(out, pos);
    if (!tag) {
      buf += out.slice(pos);
      break;
    }
    if (tag.start > pos) {
      buf += out.slice(pos, tag.start);
    }
    // A complete <tool_calls>...</tool_calls> block is stripped entirely.
    if (!tag.closing && tag.name === 'tool_calls') {
      const closeTag = findMatchingToolMarkupClose(out, tag);
      if (closeTag) {
        pos = closeTag.end + 1;
        continue;
      }
    }
    // Any other recognized tool-call markup tag is leaked markup: drop the
    // tag itself while keeping the surrounding text.
    pos = tag.end + 1;
  }
  return sanitizeLooseCDATA(buf);
}

function createVisibleTextCleaner() {
  let roleSuppressed = false;
  return {
    clean(text) {
      if (!text) {
        return text;
      }
      let out = text;
      // Echoed reasoning history and think blocks are removed with their
      // content: what sits between them is leaked thinking, never an answer.
      out = out.replace(REASONING_BLOCK, '');
      out = out.replace(THINK_BLOCK, '');
      out = stripDanglingThinkSuffix(out);
      out = out.replace(THINK_TAG, '');
      out = out.replace(BOS_MARKER, '');
      out = out.replace(THOUGHT_MARKER, '');
      out = out.replace(META_MARKER, '');
      const role = applyRoleBlockSuppression(out, roleSuppressed);
      roleSuppressed = role.inside;
      out = role.text;
      out = out.replace(ROLE_MARKER, '');
      out = out.replace(ROLE_CLOSING_TAG, '');
      out = out.replace(REASONING_MARKER, '');
      return out;
    },
    reset() {
      roleSuppressed = false;
    },
  };
}

module.exports = {
  createVisibleTextCleaner,
  applyRoleBlockSuppression,
  detectMalformedDSMLToolAttempt,
  stripLeakedToolMarkup,
};
