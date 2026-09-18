'use strict';
const path = require('path');
// tests/node/ -> repo root -> internal/js/helpers/stream-tool-sieve
const sievePath = path.resolve(__dirname, '..', '..', 'internal', 'js', 'helpers', 'stream-tool-sieve');
const {
  createToolSieveState,
  processToolSieveChunk,
  flushToolSieve,
  parseToolCallsDetailed,
} = require(sievePath);

const PIPE = '\uFF5C'; // fullwidth pipe ｜
const DPIPE = PIPE + PIPE; // double fullwidth pipe ｜｜

// The exact raw sample reported by the user (2024 drift): double fullwidth
// pipes after '<' and after DSML, 'calls' shorthand wrapper, string="true"/
// "false" parameter attributes, plain-text (non-CDATA) values with halfwidth
// pipes and nested quotes inside.
const userExact =
  '<' + DPIPE + 'DSML' + DPIPE + ' calls>\n' +
  '<' + DPIPE + 'DSML' + DPIPE + ' invoke name="Bash">\n' +
  '<' + DPIPE + 'DSML' + DPIPE + ' parameter name="command" string="true">ssh -o StrictHostKeyChecking=no root@192.168.31.133 \'docker ps --filter name=research-frontend --filter name=research-agent-runtime --format "{{.Names}}|{{.Status}}"\' 2>&1 | tail -10</' + DPIPE + 'DSML' + DPIPE + ' parameter>\n' +
  '<' + DPIPE + 'DSML' + DPIPE + ' parameter name="description" string="true">检查两个容器运行状态</' + DPIPE + 'DSML' + DPIPE + ' parameter>\n' +
  '<' + DPIPE + 'DSML' + DPIPE + ' parameter name="timeout" string="false">120000</' + DPIPE + 'DSML' + DPIPE + ' parameter>\n' +
  '</' + DPIPE + 'DSML' + DPIPE + ' invoke>\n' +
  '<' + DPIPE + 'DSML' + DPIPE + ' invoke name="Bash">\n' +
  '<' + DPIPE + 'DSML' + DPIPE + ' parameter name="command" string="true">ssh -o StrictHostKeyChecking=no root@192.168.31.133 \'echo "=== 新组件文案 ==="; docker exec research-frontend sh -c "grep -rl "AI 需要您的帮助" /app/.next 2>/dev/null | head -3"; echo "=== 旧弹窗文案(空=已清除) ==="; docker exec research-frontend sh -c "grep -rl "请选择或输入您的答案" /app/.next 2>/dev/null | head -3"; echo "=== 新交互文案 ==="; docker exec research-frontend sh -c "grep -rl "AI 会等你的回答再继续" /app/.next 2>/dev/null | head -3"\' 2>&1 | tail -20</' + DPIPE + 'DSML' + DPIPE + ' parameter>\n' +
  '<' + DPIPE + 'DSML' + DPIPE + ' parameter name="description" string="true">在生产构建产物中验证新旧组件文案</' + DPIPE + 'DSML' + DPIPE + ' parameter>\n' +
  '<' + DPIPE + 'DSML' + DPIPE + ' parameter name="timeout" string="false">120000</' + DPIPE + 'DSML' + DPIPE + ' parameter>\n' +
  '</' + DPIPE + 'DSML' + DPIPE + ' invoke>\n' +
  '</' + DPIPE + 'DSML' + DPIPE + ' calls>';

const cases = [
  {
    name: 'canonical',
    text: '<' + PIPE + 'DSML' + PIPE + 'tool_calls><' + PIPE + 'DSML' + PIPE + 'invoke name="run_code"><' + PIPE + 'DSML' + PIPE + 'parameter name="code"><![CDATA[echo hi]]' + '></' + PIPE + 'DSML' + PIPE + 'parameter></' + PIPE + 'DSML' + PIPE + 'invoke></' + PIPE + 'DSML' + PIPE + 'tool_calls>',
    wantCalls: 1,
  },
  {
    name: 'doublepipe_calls',
    text: '<' + PIPE + 'DSML' + PIPE + PIPE + ' calls><' + PIPE + 'DSML' + PIPE + PIPE + ' invoke name="run_code"><' + PIPE + 'DSML' + PIPE + PIPE + ' parameter name="code"><![CDATA[echo hi]]' + '></' + PIPE + 'DSML' + PIPE + 'parameter></' + PIPE + 'DSML' + PIPE + 'invoke></' + PIPE + 'DSML' + PIPE + 'tool_calls>',
    wantCalls: 1,
  },
  {
    name: 'doublepipe_invoke_only',
    text: '<' + PIPE + 'DSML' + PIPE + 'tool_calls><' + PIPE + 'DSML' + PIPE + PIPE + ' invoke name="run_code"><' + PIPE + 'DSML' + PIPE + 'parameter name="code"><![CDATA[echo hi]]' + '></' + PIPE + 'DSML' + PIPE + 'parameter></' + PIPE + 'DSML' + PIPE + 'invoke></' + PIPE + 'DSML' + PIPE + 'tool_calls>',
    wantCalls: 1,
  },
  {
    name: 'doublepipe_parameter_string_attr',
    text: '<' + PIPE + 'DSML' + PIPE + 'tool_calls><' + PIPE + 'DSML' + PIPE + 'invoke name="run_code"><' + PIPE + 'DSML' + PIPE + PIPE + ' parameter name="code" string="true"><![CDATA[echo hi]]' + '></' + PIPE + 'DSML' + PIPE + 'parameter></' + PIPE + 'DSML' + PIPE + 'invoke></' + PIPE + 'DSML' + PIPE + 'tool_calls>',
    wantCalls: 1,
  },
  {
    name: 'user_exact_doublepipe_calls_string_attr',
    text: userExact,
    wantCalls: 2,
  },
  {
    // 'calls' is too generic to validate as a bare markup prefix: prose that
    // merely mentions <calls must stay plain text (Go/Node parity guard).
    name: 'plain_calls_angle_stays_text',
    text: 'The gateway exposes <calls and <invokes as internal helpers, nothing else.',
    wantCalls: 0,
  },
  {
    // Bare hyphenated lookalike must stay ignored: 'calls' must not be
    // accepted as a local name after the arbitrary prefix 'tool-' without
    // DSML evidence (Go/Node parity guard).
    name: 'bare_hyphenated_lookalike_stays_text',
    text: '<tool-calls><invoke name="Bash"><parameter name="command">pwd</parameter></invoke></tool-calls>',
    wantCalls: 0,
  },
];

const toolNames = ['run_code'];
let allPass = true;

function countCalls(events) {
  return events
    .filter((e) => e.type === 'tool_calls')
    .reduce((sum, e) => sum + (Array.isArray(e.calls) ? e.calls.length : 0), 0);
}

console.log('=== JS Terminal Parse (parseToolCallsDetailed) ===');
for (const tc of cases) {
  const res = parseToolCallsDetailed(tc.text, toolNames);
  const got = res.calls.length;
  const status = got === tc.wantCalls ? 'OK' : 'FAIL';
  if (status === 'FAIL') allPass = false;
  console.log('[' + status + '] ' + tc.name.padEnd(36) + ' got=' + got + ' want=' + tc.wantCalls + ' sawSyntax=' + res.sawToolCallSyntax + ' calls=' + JSON.stringify(res.calls));
}

console.log('');
console.log('=== JS Streaming Sieve (single chunk + flush) ===');
for (const tc of cases) {
  const state = createToolSieveState();
  const events = processToolSieveChunk(state, tc.text, toolNames);
  const flushEvents = flushToolSieve(state, toolNames);
  const allEvents = events.concat(flushEvents);
  const totalCalls = countCalls(allEvents);
  const status = totalCalls === tc.wantCalls ? 'OK' : 'FAIL';
  if (status === 'FAIL') allPass = false;
  console.log('[' + status + '] ' + tc.name.padEnd(36) + ' got=' + totalCalls + ' want=' + tc.wantCalls + ' eventTypes=' + JSON.stringify(allEvents.map((e) => e.type)));
}

console.log('');
console.log('=== JS Streaming Sieve (chunked 10 chars + flush) ===');
for (const tc of cases) {
  const state = createToolSieveState();
  const allEvents = [];
  for (let i = 0; i < tc.text.length; i += 10) {
    allEvents.push(...processToolSieveChunk(state, tc.text.slice(i, i + 10), toolNames));
  }
  allEvents.push(...flushToolSieve(state, toolNames));
  const totalCalls = countCalls(allEvents);
  const status = totalCalls === tc.wantCalls ? 'OK' : 'FAIL';
  if (status === 'FAIL') allPass = false;
  console.log('[' + status + '] ' + tc.name.padEnd(36) + ' got=' + totalCalls + ' want=' + tc.wantCalls + ' eventTypes=' + JSON.stringify(allEvents.map((e) => e.type)));
}

console.log('');
console.log(allPass ? 'ALL PASS' : 'SOME FAILED');
process.exit(allPass ? 0 : 1);
