'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const {
  createToolSieveState,
  processToolSieveChunk,
  flushToolSieve,
  parseToolCallsDetailed,
} = require('../../internal/js/helpers/stream-tool-sieve.js');

// Byte-exact production leak samples: both blocks were emitted by the model as
// visible text instead of being executed as tool calls when a deployed instance
// ran parsing code that predated the calls-shorthand / repeated-separator
// support. They are locked here so any parser regression fails a test instead
// of reintroducing a production leak.
const verbatimLeakedCallsShorthandSample = "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"pwsh\">\n<｜｜DSML｜｜ parameter name=\"command\" string=\"true\"><![CDATA[cd E:\\projects\\ds2api; git status --porcelain | Select-Object -First 40; Write-Output '--- grep charts/dashboard ---'; Select-String -Path 'webui\\src\\**\\*.jsx','webui\\src\\**\\*.js' -Pattern 'Dashboard|charts|Sparkline|AreaLineChart|BarList' -SimpleMatch -List | Select-Object -ExpandProperty Path]]></｜｜DSML｜｜ parameter>\n<｜｜DSML｜｜ parameter name=\"description\" string=\"true\">Check git status and chart references</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n<｜｜DSML｜｜ invoke name=\"glob\">\n<｜｜DSML｜｜ parameter name=\"pattern\" string=\"true\">plans/refactor-line-gate-targets.txt</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n</｜｜DSML｜｜ calls>";

// Hybrid marker-family sample: DSML openings closed by a foreign client-harness
// marker family (<|EPSE|...>), including a calls wrapper closed as
// </|EPSE|tool_calls>. Closing tags are matched by local name, so the mixed
// block still executes.
const verbatimLeakedHybridEPSECloseSample = "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"read\">\n<｜｜DSML｜｜ parameter name=\"file_path\"><![CDATA[internal/responsehistory/session.go]]></|EPSE|parameter>\n  </|EPSE|invoke>\n  <|EPSE|invoke name=\"read\">\n    <|EPSE|parameter name=\"file_path\"><![CDATA[internal/httpapi/openai/chat/handler_chat.go]]></|EPSE|parameter>\n  </|EPSE|invoke>\n  <|EPSE|invoke name=\"read\">\n    <|EPSE|parameter name=\"file_path\"><![CDATA[webui/src/i18n.jsx]]></|EPSE|parameter>\n  </|EPSE|invoke>\n</|EPSE|tool_calls>";

test('terminal parse executes verbatim leaked calls-shorthand sample (Go parity)', () => {
  const res = parseToolCallsDetailed(verbatimLeakedCallsShorthandSample, ['pwsh', 'glob']);
  assert.equal(res.calls.length, 2);
  assert.ok(res.sawToolCallSyntax);
  assert.equal(res.calls[0].name, 'pwsh');
  assert.equal(res.calls[0].input.command, "cd E:\\projects\\ds2api; git status --porcelain | Select-Object -First 40; Write-Output '--- grep charts/dashboard ---'; Select-String -Path 'webui\\src\\**\\*.jsx','webui\\src\\**\\*.js' -Pattern 'Dashboard|charts|Sparkline|AreaLineChart|BarList' -SimpleMatch -List | Select-Object -ExpandProperty Path");
  assert.equal(res.calls[0].input.description, "Check git status and chart references");
  assert.equal(res.calls[1].name, 'glob');
  assert.equal(res.calls[1].input.pattern, "plans/refactor-line-gate-targets.txt");
});

test('terminal parse executes verbatim leaked hybrid EPSE-close sample (Go parity)', () => {
  const res = parseToolCallsDetailed(verbatimLeakedHybridEPSECloseSample, ['read']);
  assert.equal(res.calls.length, 3);
  const wantPaths = [
    'internal/responsehistory/session.go',
    'internal/httpapi/openai/chat/handler_chat.go',
    'webui/src/i18n.jsx',
  ];
  res.calls.forEach((call, i) => {
    assert.equal(call.name, 'read');
    assert.equal(call.input.file_path, wantPaths[i]);
  });
});

test('streaming sieve captures verbatim leaked samples across chunk sizes (Go parity)', () => {
  const cases = [
    { name: 'calls-shorthand', text: verbatimLeakedCallsShorthandSample, tools: ['pwsh', 'glob'], wantCalls: 2 },
    { name: 'hybrid-epse-close', text: verbatimLeakedHybridEPSECloseSample, tools: ['read'], wantCalls: 3 },
  ];
  for (const tc of cases) {
    for (const size of [0, 1, 2, 3, 5, 7, 13, 64]) {
      const state = createToolSieveState();
      const events = [];
      if (size === 0) {
        events.push(...processToolSieveChunk(state, tc.text, tc.tools));
      } else {
        for (let i = 0; i < tc.text.length; i += size) {
          events.push(...processToolSieveChunk(state, tc.text.slice(i, i + size), tc.tools));
        }
      }
      events.push(...flushToolSieve(state, tc.tools));
      const calls = events
        .filter((evt) => evt.type === 'tool_calls')
        .reduce((sum, evt) => sum + (Array.isArray(evt.calls) ? evt.calls.length : 0), 0);
      assert.equal(calls, tc.wantCalls, tc.name + ' size=' + size);
      const text = events
        .filter((evt) => evt.type === 'text')
        .map((evt) => evt.text || '')
        .join('');
      assert.ok(!text.includes('DSML') && !text.includes('EPSE'), tc.name + ' size=' + size + ' leaked: ' + text);
    }
  }
});

// Byte-exact production leak where the model emitted a malformed tool call:
// a stray closing </|DSML|tool_calls>, a bare <Tool> tag as pretend invoke,
// orphaned DSML parameter tags, and duplicated closing wrapper tags. None of
// this forms a complete block, so the sieve must NOT treat it as a tool call
// and must release it as text so the downstream sanitizer can strip the
// markup.
const orphanedDSMLToolCallSample =
  "</|DSML|tool_calls><Tool>:echo hi\n" +
  "']]></|DSML|parameter>\n" +
  "<|DSML|parameter name=\"description\"><![CDATA[拉取服务器证据]]></|DSML|parameter>\n" +
  "<|DSML|parameter name=\"timeout\"><![CDATA[120000]]></|DSML|parameter>\n" +
  "</|DSML|parameter>\n" +
  "</|DSML|tool_calls>\n" +
  "</|DSML|tool_calls>";

test('streaming sieve releases orphaned DSML markup as text (does not stall or execute)', () => {
  const sizes = [0, 1, 2, 3, 5, 7, 13, 64];
  for (const size of sizes) {
    const state = createToolSieveState();
    const events = [];
    if (size === 0) {
      events.push(...processToolSieveChunk(state, orphanedDSMLToolCallSample, ['Tool']));
    } else {
      for (let i = 0; i < orphanedDSMLToolCallSample.length; i += size) {
        events.push(...processToolSieveChunk(state, orphanedDSMLToolCallSample.slice(i, i + size), ['Tool']));
      }
    }
    events.push(...flushToolSieve(state, ['Tool']));
    const calls = events
      .filter((evt) => evt.type === 'tool_calls')
      .reduce((sum, evt) => sum + (Array.isArray(evt.calls) ? evt.calls.length : 0), 0);
    assert.equal(calls, 0, 'orphaned DSML size=' + size + ' must not execute as tool call');
    assert.ok(!state.capturing, 'orphaned DSML size=' + size + ' must not leave capture open');
    const text = events
      .filter((evt) => evt.type === 'text')
      .map((evt) => evt.text || '')
      .join('');
    assert.ok(text.includes('echo hi'), 'orphaned DSML size=' + size + ' must preserve command body, got: ' + text);
  }
});
