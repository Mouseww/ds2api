'use strict';

// Mirrors the Go sanitizer tests in
// internal/httpapi/openai/shared/leaked_output_sanitize_test.go: the model
// echoing prompt structure (role markers, reasoning history, think blocks)
// must never reach the client as visible content.

const test = require('node:test');
const assert = require('node:assert/strict');

const {
  createVisibleTextCleaner,
  applyRoleBlockSuppression,
  detectMalformedDSMLToolAttempt,
  stripLeakedToolMarkup,
} = require('../../internal/js/chat-stream/visible_clean');

// Verbatim text block of a production failure: a coding agent's model tried
// to emit a grep tool call, but the DSML markup came out corrupted —
// fullwidth doubled ｜ pipes, a doubled parameter-name attribute, a stray
// CDATA close marker, and no wrapper/invoke open tags.
const deadSubagentSpecimen = 'Now let me look at the turn finalization logic that decides empty-output retry.\n\n\n\n' +
  '<｜｜DSML｜｜ parameter name="parameter name="pattern">func ShouldRetryEmptyOutput|func FinalizeTurn|func BuildTurnFromCollected]]</｜｜DSML｜｜ parameter>\n' +
  '<｜｜DSML｜｜ parameter name="path">E:\\projects\\ds2api\\internal\\assistantturn</｜｜DSML｜｜ parameter>\n' +
  '</｜｜DSML｜｜ invoke>\n' +
  '</｜｜DSML｜｜ calls>';

function cleanOnce(text) {
  return createVisibleTextCleaner().clean(text);
}

test('visible cleaner strips closing role tags', () => {
  const got = cleanOnce('answer text</Assistant>\n</Tool>\nmore');
  assert.ok(!got.includes('</Assistant>'), `closing assistant tag leaked: ${got}`);
  assert.ok(!got.includes('</Tool>'), `closing tool tag leaked: ${got}`);
  assert.ok(got.includes('answer text') && got.includes('more'), `surrounding text lost: ${got}`);
});

test('visible cleaner removes echoed reasoning history entirely', () => {
  const got = cleanOnce('before\n[reasoning_content]\nhidden deliberation\n[/reasoning_content]\nafter');
  assert.ok(!got.includes('hidden deliberation'), `reasoning content leaked: ${got}`);
  assert.ok(!got.includes('reasoning_content'), `reasoning brackets leaked: ${got}`);
  assert.ok(got.includes('before') && got.includes('after'), `surrounding text lost: ${got}`);
});

test('visible cleaner removes echoed think blocks entirely', () => {
  const got = cleanOnce('a<think>\nsecret chain of thought\n</think>b');
  assert.ok(!got.includes('secret chain'), `think content leaked: ${got}`);
  assert.ok(!got.toLowerCase().includes('think'), `think tags leaked: ${got}`);
  assert.ok(got.includes('a') && got.includes('b'), `surrounding text lost: ${got}`);
});

test('visible cleaner suppresses echoed role blocks with their content', () => {
  const got = cleanOnce(
    'real answer\n<Tool>: <path>E:\\x\\README.MD</path> <content>file body</content>\n</Tool>\nresumed answer',
  );
  assert.ok(!got.includes('README.MD') && !got.includes('file body'), `tool echo leaked: ${got}`);
  assert.ok(got.includes('real answer') && got.includes('resumed answer'), `answer lost: ${got}`);
});

test('visible cleaner keeps content after an assistant marker', () => {
  const got = cleanOnce('<Assistant>: here is the actual answer');
  assert.ok(got.includes('here is the actual answer'), `answer lost: ${got}`);
  assert.ok(!got.includes('<Assistant>'), `marker leaked: ${got}`);
});

test('visible cleaner suppresses unclosed role blocks to the end', () => {
  const got = cleanOnce('answer\n<Tool>: tool result payload without closer');
  assert.ok(!got.includes('tool result payload'), `unclosed block leaked: ${got}`);
  assert.ok(got.includes('answer'), `answer lost: ${got}`);
});

test('visible cleaner leaves plain prose untouched', () => {
  const plain = '普通文本 with <angle brackets> and 2 > 1 and [brackets] too';
  assert.equal(cleanOnce(plain), plain);
});

test('role block suppression carries across chunks', () => {
  const cleaner = createVisibleTextCleaner();
  assert.equal(cleaner.clean('real answer\n'), 'real answer\n');
  assert.equal(cleaner.clean('<Tool>: <path>E:\\x\\f</path>'), '');
  assert.equal(cleaner.clean(' <content>file body</content>\n'), '');
  assert.equal(cleaner.clean('</Tool>\n').trim(), '');
  assert.equal(cleaner.clean('resumed answer'), 'resumed answer');
});

test('role block suppression resets between attempts', () => {
  const cleaner = createVisibleTextCleaner();
  assert.equal(cleaner.clean('<Tool>: echo'), '');
  cleaner.reset();
  assert.equal(cleaner.clean('fresh attempt answer'), 'fresh attempt answer');
});

test('applyRoleBlockSuppression is exported for parity checks', () => {
  const res = applyRoleBlockSuppression('a<User>: echo', false);
  assert.equal(res.text, 'a');
  assert.equal(res.inside, true);
});

test('detectMalformedDSMLToolAttempt matches every corruption variant', () => {
  assert.equal(detectMalformedDSMLToolAttempt(deadSubagentSpecimen), true, 'verbatim specimen');
  assert.equal(detectMalformedDSMLToolAttempt('<｜｜DSML｜｜ parameter name="command">ls</｜｜DSML｜｜ parameter>'), true, 'fullwidth pipes');
  assert.equal(detectMalformedDSMLToolAttempt('<｜｜DSML｜｜ invoke name="Bash">'), true, 'fullwidth invoke');
  assert.equal(detectMalformedDSMLToolAttempt('<|DSML|parameter name="x">v</|DSML|parameter>'), true, 'halfwidth orphaned');
  assert.equal(detectMalformedDSMLToolAttempt('<|DSML parameter name="file_path">/x</|DSML parameter>'), true, 'space separator');
  assert.equal(detectMalformedDSMLToolAttempt('<DSMLparameter name="todos">x</DSMLparameter>'), true, 'collapsed');
  assert.equal(detectMalformedDSMLToolAttempt('the format is <|DSML|tool_calls> and </|DSML|tool_calls>'), false, 'wrapper mention stays prose');
  assert.equal(detectMalformedDSMLToolAttempt('普通文本 with <angle brackets>'), false, 'plain prose');
  assert.equal(detectMalformedDSMLToolAttempt(''), false, 'empty');
});

test('stripLeakedToolMarkup removes corrupted DSML tags from visible text', () => {
  const out = stripLeakedToolMarkup(deadSubagentSpecimen);
  for (const tag of ['<｜｜DSML｜｜ parameter', '</｜｜DSML｜｜ parameter>', '</｜｜DSML｜｜ invoke>', '</｜｜DSML｜｜ calls>', 'DSML']) {
    assert.ok(!out.includes(tag), `corrupted markup leaked: ${tag} in ${out}`);
  }
  assert.ok(out.includes('Now let me look at the turn finalization logic'), `answer text lost: ${out}`);
});

test('stripLeakedToolMarkup keeps prose mentions inside code spans', () => {
  const prose = 'use `grep -E "a|b"` and see `<|DSML|tool_calls>` docs';
  assert.equal(stripLeakedToolMarkup(prose), prose);
});
