import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('boots with platform-shaped data and renders every routed view', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  assert.equal(app.evaluate('state.data.projects[0].name'), 'Atlas');
  assert.equal(app.evaluate('state.currentUser.username'), 'tester');

  const views = [
    'collaboration', 'issues', 'runs', 'approvals', 'sops', 'flow', 'agents', 'mcp',
    'rules', 'skills', 'dockerfiles', 'environments', 'sandboxes', 'observability',
    'projects', 'platform', 'runners', 'settings', 'guide', 'bots', 'aiSchedules',
  ];
  for (const view of views) {
    app.evaluate(`switchView(${JSON.stringify(view)})`);
    await settle(2);
  }
  assert.ok(app.router.calls.some((call) => call.path === '/api/bootstrap'));
  assert.ok(app.document.querySelectorAll('.view').length > 10);
  assert.equal(app.document.querySelector('#pageTitle').textContent, '定时任务');
  for (const removed of ['knowledge', 'metrics', 'businessDomains', 'businessDomainManagement']) {
    assert.equal(app.document.querySelector(`#${removed}View`), null, `${removed} was retired`);
    assert.equal(app.document.querySelector(`[data-view="${removed}"]`), null, `${removed} has no nav entry`);
  }
});

test('hash routing opens run, flow, project, agent, and asset details', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  for (const hash of [
    '#/runs/run-1', '#/flow/flow-1', '#/projects/project-1/repos',
    '#/agents/agent-1', '#/rules/rule-1', '#/skills/new', '#/collaboration/session-1',
  ]) {
    app.window.location.hash = hash;
    app.window.dispatchEvent(new app.window.HashChangeEvent('hashchange'));
    await settle(3);
  }
  assert.match(app.window.location.hash, /^#\/collaboration/);
  assert.ok(app.router.calls.some((call) => call.path.includes('/api/agents/agent-1')));
});

test('run status filter buttons switch the active filter', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`switchView('runs')`);
  await settle(2);
  for (const filter of ['failed', 'succeeded', 'all']) {
    app.document.querySelector(`#runFilters [data-run-filter="${filter}"]`).click();
    await settle(2);
    assert.equal(app.evaluate('state.runFilter'), filter);
    assert.ok(app.document.querySelector(`#runFilters [data-run-filter="${filter}"]`).classList.contains('active'), filter);
  }
});
