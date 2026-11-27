import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

for (const entry of ['showRunDetail', 'selectRun']) {
for (const scenario of [
  { name: 'personal machine', runners: [{ id: 'runner-personal', name: '<Build machine>', ownerUserId: 42, runLimit: 3 }], label: '执行机器 <Build machine> · 本机最多 3 条', html: '执行机器 &lt;Build machine&gt; · 本机最多 3 条' },
  { name: 'unavailable runner endpoint', runners: { status: 403, body: { error: 'Forbidden' } }, label: '执行机器 personal' },
  { name: 'cloud pool', runners: [], pool: 'cloud', label: '云端执行池' },
]) {
  test(`${entry} resolves ${scenario.name} without visiting the runners page`, async (t) => {
    const app = await loadApp();
    t.after(app.close);
    const run = {
      ...app.evaluate('structuredClone(state.data.runs[0])'), status: 'succeeded',
      workflowSnapshot: app.evaluate('structuredClone(state.data.workflows[0])'),
      executionContext: { executionMode: 'runner', executionRunnerId: 'runner-personal', executionPool: scenario.pool },
    };
    const { id, name } = run.workflowSnapshot;
    const summary = { ...run, workflowSnapshot: { id, name, nodes: null } };
    app.router.get('/api/runs/run-1', run);
    app.router.get('/api/runners?mine=1', scenario.runners);
    app.evaluate(`state.runners=[];state.data.runs=[${JSON.stringify(summary)}];${entry}('run-1')`);
    await settle(8);
    assert.ok(app.router.calls.some((call) => call.path === '/api/runners' && call.search === '?mine=1'));
    assert.ok(app.evaluate('runExecutionChipHTML(state.activeRun)').includes(scenario.html || scenario.label));
    assert.equal(app.document.querySelector('#runsDetailSection [data-run-execution-chip]').textContent, scenario.label);
    assert.equal(app.router.calls.filter((call) => call.path === '/api/runners/bind-codes').length, 0,
      'viewing a run must not create a binding code');
  });
}
}

test('non-runner run detail does not fetch machines', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const run = { ...app.evaluate('structuredClone(state.data.runs[0])'), status: 'succeeded', executionContext: {} };
  app.router.get('/api/runs/run-1', run);
  const before = app.router.calls.filter((call) => call.path === '/api/runners').length;
  app.evaluate(`state.data.runs=[${JSON.stringify(run)}];showRunDetail('run-1')`);
  await settle(8);
  assert.equal(app.router.calls.filter((call) => call.path === '/api/runners').length, before);
});
