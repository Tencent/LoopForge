import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

const run = {
  id: 'run-eval', status: 'succeeded', workflowId: 'sop',
  executionContext: { projectId: 'project' },
  workflowSnapshot: {
    id: 'sop', name: '交付流程',
    nodes: [{ id: 'verify', name: '验证交付', agentId: 'reviewer' }],
  },
};

function mount(app, selected = run) {
  app.evaluate(`{
    const run = ${JSON.stringify(selected)};
    state.selectedRun = run.id;
    state.activeRun = run;
    const host = document.querySelector('#runDetail');
    host.innerHTML = '<div data-run-pane="eval">' + runEvalPaneHTML(run) + '</div>';
    bindRunEvalPane(run, host);
  }`);
  return app.document.querySelector('[data-run-pane="eval"]');
}

test('evaluation follows server stages and ignores partial model output', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const running = { id: 'eval-1', runId: run.id, status: 'running', model: 'codex:model-x' };
  app.router.get('/api/runs/run-eval/eval', running);
  const pane = mount(app);
  await app.evaluate(`loadRunEval('${run.id}')`);
  assert.equal(pane.querySelector('[data-start-run-eval]').disabled, true);
  assert.equal(pane.querySelector('[aria-current="step"]').textContent.trim(), '等待执行');

  const [source] = app.window.EventSource.instances.filter(stream => stream.url.endsWith('/eval/stream'));
  source.emit({ stage: 'collecting' }, 'thought');
  source.emit({ stage: 'evaluating' }, 'thought');
  const status = pane.querySelector('.run-eval-live [role="status"]');
  assert.equal(pane.querySelector('[aria-current="step"]').textContent.trim(), '分析并评分');
  assert.equal(pane.querySelectorAll('.run-eval-steps .is-complete').length, 2);
  for (const text of ['can', 'use', 'descriptive', 'IDs']) source.emit({ kind: 'thinking', text }, 'thought');
  source.emit({ stage: 'collecting' }, 'thought'); // Replay after reconnect.
  assert.doesNotMatch(pane.textContent, /descriptive/);
  assert.match(status.textContent, /正在检查/);

  source.readyState = app.window.EventSource.CONNECTING;
  source.dispatchEvent(new app.window.Event('error'));
  assert.match(status.textContent, /正在重新连接/);
  source.readyState = app.window.EventSource.OPEN;
  source.dispatchEvent(new app.window.Event('open'));
  assert.match(status.textContent, /正在检查/);

  source.emit({ stage: 'validating' }, 'thought');
  assert.equal(pane.querySelector('[aria-current="step"]').textContent.trim(), '校验并保存');
  source.emit({ ...running, status: 'succeeded', score: 84, passed: true, result: { summary: '已完成验证。' } }, 'done');
  assert.equal(source.readyState, app.window.EventSource.CLOSED);
  assert.match(pane.querySelector('.run-eval-verdict').textContent, /84[\s\S]*已完成验证/);
  assert.equal(pane.querySelector('[data-start-run-eval]').textContent, '重新评测');
});

test('evaluation updates are scoped to the selected run', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const running = { id: 'eval-1', runId: run.id, status: 'running' };
  app.router.get('/api/runs/run-eval/eval', running);
  app.router.get('/api/runs/another/eval', { id: 'eval-2', runId: 'another', status: 'running' });

  mount(app);
  await app.evaluate(`loadRunEval('${run.id}')`);
  const originalSource = app.window.EventSource.instances.at(-1);
  const otherPane = mount(app, { ...run, id: 'another' });
  await app.evaluate(`loadRunEval('another')`);
  const currentSource = app.window.EventSource.instances.at(-1);
  assert.equal(originalSource.readyState, app.window.EventSource.CLOSED);
  originalSource.emit({ ...running, status: 'succeeded', score: 82 }, 'done');
  assert.equal(otherPane.querySelector('[aria-current="step"]').textContent.trim(), '等待执行');

  currentSource.emit({ error: '执行器不可用' }, 'error');
  assert.match(otherPane.querySelector('.run-eval-failed').textContent, /执行器不可用/);
  assert.equal(otherPane.querySelector('[data-start-run-eval]').disabled, false);
});

test('a stream snapshot from a replica without the job keeps the evaluation running', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const running = { id: 'eval-1', runId: run.id, status: 'running' };
  app.router.get('/api/runs/run-eval/eval', running);
  const pane = mount(app);
  await app.evaluate(`loadRunEval('${run.id}')`);
  const source = app.window.EventSource.instances.at(-1);

  source.emit(running, 'done');
  assert.equal(pane.querySelector('.run-eval-failed'), null);
  assert.equal(pane.querySelector('[data-start-run-eval]').disabled, true);
  assert.notEqual(source.readyState, app.window.EventSource.CLOSED);
  source.emit({ ...running, status: 'succeeded', score: 84, result: { summary: '评测已完成。' } }, 'done');
  assert.match(pane.querySelector('.run-eval-verdict').textContent, /评测已完成/);
});

test('recommendations preview the actual change and show the server writeback outcome', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const prompt = '交付前验证产物，并附上 <完成证据>。';
  const recommendation = {
    id: 'append', action: 'append_task_prompt', target: 'agent', nodeId: 'verify',
    set: { instructionsDelta: prompt },
  };
  const report = {
    id: 'eval-1', runId: run.id, status: 'succeeded', score: 67, passed: false,
    facts: { editableCatalog: { agents: [{ id: 'reviewer', name: '审查角色', shared: true }] } },
    result: {
      summary: '交付证据不足。',
      dimensions: { delivery: 50, tokens: 88 },
      findings: [
        { severity: 'info', dimension: 'tokens', message: '缓存可用。' },
        { severity: 'error', dimension: 'delivery', message: '缺少验证证据。', evidence: 'seq 42: <missing>' },
      ],
      recommendations: [
        recommendation,
        { id: 'limit', action: 'workflow.update_node', nodeId: 'verify', set: { maxReworks: 0 } },
        { id: 'partial', action: 'append_instructions', target: 'agent', targetId: 'reviewer' },
      ],
    },
  };
  const resultWith = (patch) => ({
    ...report, result: { ...report.result, recommendations: [
      { ...recommendation, ...patch }, ...report.result.recommendations.slice(1),
    ] },
  });
  app.router.get('/api/runs/run-eval/eval', report);
  app.router.post('/api/runs/run-eval/eval/apply', resultWith({ applyError: '该角色已被移除' }));
  const pane = mount(app);
  await app.evaluate(`loadRunEval('${run.id}')`);

  const firstFinding = pane.querySelectorAll('.run-eval-list')[0].firstElementChild;
  assert.match(firstFinding.textContent, /交付完整/);
  assert.equal(firstFinding.querySelector('details').open, false);
  assert.equal(firstFinding.querySelector('pre').textContent, 'seq 42: <missing>');
  assert.match(pane.querySelector('.run-eval-verdict').textContent, /未达标/);

  const row = (id) => pane.querySelector(`[data-eval-recommendation="${id}"]`);
  assert.match(row('append').textContent, /追加角色任务说明[\s\S]*审查角色[\s\S]*验证交付/);
  assert.equal(row('append').querySelector('pre').textContent, prompt);
  assert.match(row('limit').querySelector('dl').textContent, /最大返工次数0/);
  assert.match(row('partial').textContent, /仅供参考/);
  assert.equal(row('partial').querySelector('[data-apply-eval]'), null);

  row('append').querySelector('details').open = true;
  app.evaluate(`window.afConfirm = async () => false`);
  await row('append').querySelector('[data-apply-eval]').onclick();
  const calls = () => app.router.calls.filter(call => call.path.endsWith('/eval/apply'));
  assert.equal(calls().length, 0);

  app.evaluate(`window.afConfirm = async (options) => { window.lastEvalConfirmation = options; return true; }`);
  await row('append').querySelector('[data-apply-eval]').onclick();
  assert.match(app.window.lastEvalConfirmation.message, /角色 · 审查角色[\s\S]*项目的评测副本/);
  assert.deepEqual(calls()[0].body, { evalId: 'eval-1', recommendationIds: ['append'] });
  assert.match(row('append').querySelector('[role="alert"]').textContent, /该角色已被移除/);
  assert.equal(row('append').querySelector('details').open, true);
  assert.doesNotMatch(app.document.body.textContent, /建议已回写/);

  app.router.post('/api/runs/run-eval/eval/apply', resultWith({ status: 'applied', appliedTargetId: 'reviewer-project' }));
  app.router.get('/api/agents', [{ id: 'reviewer-project', name: '审查角色（评测副本）' }]);
  await row('append').querySelector('[data-apply-eval]').onclick();
  assert.match(row('append').textContent, /已回写[\s\S]*审查角色（评测副本）/);
  assert.equal(row('append').querySelector('[data-open-eval-target]').dataset.openEvalId, 'reviewer-project');
  assert.equal(row('append').querySelector('[data-apply-eval]'), null);
});

test('recommendations whose targetId names a node show the LOOP or role the server writes', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const report = {
    id: 'eval-node-target', runId: run.id, status: 'succeeded', score: 54, passed: false,
    facts: { editableCatalog: { agents: [{ id: 'reviewer', name: '审查角色' }] } },
    result: {
      recommendations: [
        { id: 'retry', target: 'workflow', action: 'update_node', nodeId: 'verify', targetId: 'verify', set: { maxAttempts: 3 } },
        { id: 'prompt', target: 'agent', action: 'append_task_prompt', nodeId: 'verify', targetId: 'verify', text: '附上验证证据。' },
      ],
    },
  };
  app.router.get('/api/runs/run-eval/eval', report);
  app.router.post('/api/runs/run-eval/eval/apply', report);
  const pane = mount(app);
  await app.evaluate(`loadRunEval('${run.id}')`);
  const row = (id) => pane.querySelector(`[data-eval-recommendation="${id}"]`);
  assert.match(row('retry').querySelector('.run-eval-target').textContent, /LOOP\s*交付流程/);
  assert.match(row('prompt').querySelector('.run-eval-target').textContent, /角色\s*审查角色/);

  app.evaluate(`window.afConfirm = async (options) => { window.lastEvalConfirmation = options; return true; }`);
  await row('retry').querySelector('[data-apply-eval]').onclick();
  assert.match(app.window.lastEvalConfirmation.message, /LOOP · 交付流程/);
});

test('a rejected start follows the evaluation that is already running', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/assistant/models', {
    engines: [{ id: 'codex', name: 'Codex', personal: true }],
    models: [{ id: 'codex:model-x', name: 'Model X', engine: 'codex' }],
  });
  app.router.get('/api/runs/run-eval/eval', { id: 'eval-old', runId: run.id, status: 'succeeded', score: 80, result: {} });
  const pane = mount(app);
  await app.evaluate(`loadRunEval('${run.id}')`);

  // Another tab started an evaluation that this page has not seen yet.
  app.router.post('/api/runs/run-eval/eval', { status: 409, body: { error: '这次运行已有评测在进行' } });
  app.router.get('/api/runs/run-eval/eval', { id: 'eval-new', runId: run.id, status: 'running' });
  await app.evaluate(`startRunEval(state.activeRun)`);
  assert.equal(pane.querySelector('[data-start-run-eval]').disabled, true);
  assert.equal(pane.querySelector('[aria-current="step"]').textContent.trim(), '等待执行');
  assert.ok(app.window.EventSource.instances.some(stream => stream.url.endsWith('/eval/stream') && stream.readyState !== 2));
});

test('the gate preview translates its yes and no values without a dictionary entry', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const preview = () => app.evaluate(`runEvalChange({
    gate: { id: 'unit', name: '单元测试', mode: 'mr', required: false },
  }, 'agent.upsert_gate')`);
  const required = () => preview().fields.find(([label]) => label === '是否必需')[1];
  assert.equal(required(), '否');
  app.evaluate('window.lfT = (_zh, en) => en');
  assert.equal(required(), 'No');
  assert.equal(app.window.LF_I18N_EN['否'], undefined); // A typed 「否」 must not be rewritten.
});
