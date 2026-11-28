import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// The server accepts `rerun` only for a LOOP lane that has already run on the requirement.
// Every dispatch entry (issue modal, list trigger, drawer retrigger) must decide
// "first run vs rerun" for the LOOP being started, not for the requirement as a whole.
async function setup(t) {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    var base=state.data.workflows[0];
    state.data.workflows=[base,{...base,id:'flow-2',name:'Fix Flow'}];
    var lane={id:'issue-1',projectId:'project-1',number:7,kind:'requirement',source:'local',status:'blocked',title:'Lane switch',workflowId:'flow-1',repositoryIds:['repo-1']};
    state.data.issues=[lane];
    state.data.tasks=[
      {id:'task-old',issueId:'issue-1',workflowId:'flow-1',runId:'run-old',status:'succeeded',createdAt:'2026-10-20T10:00:00Z'},
      {id:'task-new',issueId:'issue-1',workflowId:'flow-1',runId:'run-new',status:'failed',createdAt:'2026-10-21T10:00:00Z'}];
    state.data.runs=[];
  `);
  // Background refreshes after a dispatch must see the same history.
  const tasks = JSON.parse(app.evaluate(`JSON.stringify(state.data.tasks)`));
  const issues = JSON.parse(app.evaluate(`JSON.stringify(state.data.issues)`));
  app.router.get('/api/tasks', () => tasks);
  app.router.get('/api/issues', () => issues);
  app.router.get('/api/runs', () => []);
  app.router.put('/api/issues', (call) => ({ status: 200, body: call.body }));
  app.router.post('/api/issues/issue-1/dispatch', (call) => ({ status: 201, body: { id: 'task-x', issueId: 'issue-1', workflowId: call.body.workflowId, status: 'running' } }));
  return app;
}
const dispatches = (app) => app.router.calls.filter((c) => c.method === 'POST' && c.path === '/api/issues/issue-1/dispatch');
const confirmOpen = (app) => app.document.querySelector('#confirmModal')?.classList.contains('open');

test('lane task helper returns the newest task of that LOOP only', async (t) => {
  const app = await setup(t);
  const out = JSON.parse(app.evaluate(`JSON.stringify({
    same:latestIssueLaneTask(state.data.issues[0],'flow-1')?.id,
    other:latestIssueLaneTask(state.data.issues[0],'flow-2'),
    blank:latestIssueLaneTask(state.data.issues[0],''),
    any:latestIssueTask(state.data.issues[0])?.id})`));
  assert.deepEqual(out, { same: 'task-new', other: null, blank: null, any: 'task-new' });
});

test('issue modal: switching to a LOOP that never ran starts it as a first run', async (t) => {
  const app = await setup(t);
  app.evaluate(`openIssue(state.data.issues[0])`);
  const form = app.document.querySelector('#issueForm');
  assert.ok([...form.elements.workflowId.options].some((o) => o.value === 'flow-2'));
  form.elements.workflowId.value = 'flow-2';
  app.document.querySelector('#dispatchIssue').click();
  await settle(6);
  assert.equal(confirmOpen(app), false, 'a new LOOP lane needs no rerun confirmation');
  const [call] = dispatches(app);
  assert.ok(call, 'dispatch was sent');
  assert.deepEqual(call.body, { rerun: false, workflowId: 'flow-2' });
});

test('issue modal: starting the LOOP that already ran asks first and sends rerun', async (t) => {
  const app = await setup(t);
  app.evaluate(`openIssue(state.data.issues[0])`);
  app.document.querySelector('#dispatchIssue').click();
  await settle(4);
  assert.equal(confirmOpen(app), true);
  assert.match(app.document.querySelector('#confirmModal').textContent, /已经处理过/);
  assert.equal(dispatches(app).length, 0, 'nothing is sent before confirming');
  app.document.querySelector('#confirmOkBtn').click();
  await settle(6);
  const [call] = dispatches(app);
  assert.deepEqual(call.body, { rerun: true, workflowId: 'flow-1' });
});

test('list trigger and drawer retrigger keep deciding rerun per lane', async (t) => {
  const app = await setup(t);
  await app.evaluate(`triggerIssue('issue-1','flow-2')`);
  await settle(6);
  assert.deepEqual(dispatches(app).at(-1).body, { rerun: false, workflowId: 'flow-2' });

  const retrigger = app.evaluate(`retriggerIssue('issue-1','flow-1')`);
  await settle(4);
  app.document.querySelector('#confirmOkBtn').click();
  await retrigger;
  await settle(6);
  assert.deepEqual(dispatches(app).at(-1).body, { rerun: true, workflowId: 'flow-1' });
});
