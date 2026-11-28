// The operator's 补充上下文 reaches the reworked node exactly once: the
// workbench sends reason and context separately and the server composes them
// (engine.composeHumanFeedback), so the client must not pre-merge them.
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert/strict');
function localBundle() {
  const page = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  const modules = [...page.matchAll(/<script src="\/(app\.js|app\/[^"?]+\.js)(?:\?[^"]*)?"><\/script>/g)];
  return modules.map(([, file]) => fs.readFileSync(path.join(__dirname, file), 'utf8')).join('\n');
}
const bundle = process.argv[2] ? fs.readFileSync(process.argv[2], 'utf8') : localBundle();
function source(name) {
  const match = new RegExp('^(?:async )?function ' + name + '\\(', 'm').exec(bundle);
  assert(match, name);
  const tail = bundle.slice(match.index);
  const next = /\n(?:async )?function |\n(?:let|const) [A-Za-z]|\n\/\/ /.exec(tail);
  return next ? tail.slice(0, next.index) : tail;
}
function load(context, names) {
  vm.createContext(context);
  for (const name of names) vm.runInContext(source(name), context);
  return context;
}
async function main() {
  const ctx = '请把错误信息改成中文';
  const helpers = load({}, ['withHumanContext']);
  assert.deepEqual({ ...helpers.withHumanContext({ nodeId: 'n', reason: '验证未通过', context: `  ${ctx}\n` }) }, { nodeId: 'n', reason: '验证未通过', context: ctx });
  const plain = { nodeId: 'n', reason: '验证未通过', context: '  ' };
  assert.equal(helpers.withHumanContext(plain), plain);

  // Every workbench action that carries the context field: reject, retry from
  // the approval card, retry from the failure banner, approve and pass.
  const bodies = [];
  const control = load({
    state: { data: { runs: [], issues: [] }, drawerOpen: false },
    cachedRun: () => ({ status: 'waiting' }),
    readHumanContext: () => ctx,
    afPrompt: async () => '验证未通过，请上游修复后重新提交',
    api: async (url, opts) => { bodies.push([url, JSON.parse(opts.body)]); return {}; },
    requestRunActivation: async (id, action, body) => { bodies.push([`/api/runs/${id}/${action}`, body]); return {}; },
    syncRunControlState: async () => ({}), runControlToast: () => '', toast: () => {},
    renderApprovals: async () => {}, refreshIssues: async () => {}, refreshRuns: async () => {},
    issueIdForRun: () => '', issueExecutionBlocked: () => '', runRetryNeedsContext: () => false,
    openIssueDetail: () => {}, lfT: s => s, isScriptWorkflowNode: () => true, afConfirm: async () => true, startLiveRun: () => {},
  }, ['withHumanContext', 'queuedRunActionBlocked', 'turnsExhausted', 'rejectRun', 'retryWaitingNode', 'resumeRun', 'approveRun', 'passFailedNode']);
  const el = { dataset: { target: 'implement', targetName: '开发实现' } };
  await control.rejectRun('r', 'accept', el);
  await control.retryWaitingNode('r', 'accept', el);
  control.state.data.runs = [{ id: 'r', status: 'failed' }];
  control.cachedRun = () => ({ id: 'r', status: 'failed' });
  await control.resumeRun('r', el);
  await control.approveRun('r', 'accept', el);
  await control.passFailedNode('r', 'test', el);
  assert.equal(bodies.length, 5, JSON.stringify(bodies));
  for (const [url, body] of bodies) {
    assert.equal(body.context, ctx, url);
    assert.ok(!String(body.reason || '').includes(ctx), `${url} repeats the context in reason: ${body.reason}`);
  }
  assert.equal(bodies[0][1].reason, '验证未通过，请上游修复后重新提交');
  assert.equal(bodies[1][1].reason, '人工确认重试本节点');
  assert.equal(bodies[2][1].reason, '从中断处继续');
}
main().catch(err => { console.error(err); process.exit(1); });
