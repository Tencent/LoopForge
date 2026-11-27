import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// A requirement created with 不使用 LOOP has no LOOP to trigger: the list and
// the drawer route it to a conversation, and a LOOP is only run once the user
// explicitly picks one.
test('no-LOOP requirements go to the chat and run a LOOP only when one is picked', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = JSON.parse(app.evaluate(`
    var pid=state.projectFilter||state.data.projects[0].id;
    var noSOPIssue={id:'iss-nosop',projectId:pid,number:61,kind:'requirement',source:'local',status:'backlog',title:'不走 LOOP',sopMode:'none',automationState:'ignored'};
    state.data.issues=[noSOPIssue];state.data.tasks=[];state.data.runs=[];
    JSON.stringify({sop:issueSOPLabel(noSOPIssue),fallback:defaultSOPForIssue(noSOPIssue),actions:issueActionButtons(noSOPIssue),trigger:triggerSOPButton(noSOPIssue),pane:sopPane(noSOPIssue,{issue:noSOPIssue,run:null,requirement:null})});
  `));
  assert.equal(out.sop, '不使用 LOOP');
  assert.equal(out.fallback, '', 'no project LOOP is filled in');
  assert.match(out.actions, /data-chat-issue="iss-nosop"[^>]*>去对话/);
  assert.doesNotMatch(out.actions, /data-trigger-issue/);
  assert.match(out.trigger, /data-chat-issue="iss-nosop"/);
  assert.match(out.pane, /这条需求不走 LOOP/);
  assert.match(out.pane, /data-no-sop-workflow/);

  const $ = (s) => app.document.querySelector(s);
  await app.evaluate(`triggerIssue('iss-nosop')`);
  await settle(2);
  assert.ok(!app.router.calls.some((c) => c.path.endsWith('/dispatch')), 'triggering without a picked LOOP does not dispatch');
  assert.match($('#toastContainer').textContent, /请先选择要运行的 LOOP/);

  app.evaluate(`state.issueDetailData={issue:state.data.issues[0],run:null,requirement:null};state.issueDetailTab='sop';renderIssueDetailTab()`);
  const workflowId = $('[data-no-sop-workflow]').value;
  assert.ok(workflowId);
  app.router.post('/api/issues/iss-nosop/dispatch', (call) => ({ status: 201, body: { id: 'task-nosop', issueId: 'iss-nosop', workflowId: call.body.workflowId } }));
  $('[data-no-sop-run]').click();
  await settle(4);
  const dispatch = app.router.calls.find((c) => c.path === '/api/issues/iss-nosop/dispatch');
  assert.equal(dispatch.body.workflowId, workflowId);
  const issue = JSON.parse(app.evaluate(`JSON.stringify(noSOPIssue)`));
  assert.equal(issue.sopMode, '');
  assert.equal(issue.workflowId, workflowId);

  app.evaluate(`switchView('issues');openIssueInChatById('missing')`);
  assert.match($('#toastContainer').textContent, /未找到工作项/);
  app.evaluate(`state.data.issues=[noSOPIssue];openIssueInChatById('iss-nosop')`);
  assert.equal(app.evaluate(`activeViewName()`), 'collaboration');
});

// The edit modal echoes the stored sopMode; the server keeps an echoed "none"
// and drops the LOOP, so picking a LOOP here must send sopMode=sop. The empty
// choice of a no-LOOP requirement runs nothing, so it must not read as the
// default flow.
test('editing a no-LOOP requirement binds the LOOP the user picks', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const $ = (s) => app.document.querySelector(s);
  const putBodies = [];
  app.router.put('/api/issues', (call) => { putBodies.push(call.body); return { ...call.body, sopMode: call.body.sopMode === 'sop' ? '' : call.body.sopMode }; });
  app.router.post('/api/issues/iss-edit-nosop/dispatch', (call) => ({ status: 201, body: { id: 'task-edit', issueId: 'iss-edit-nosop', workflowId: call.body.workflowId } }));
  const open = () => app.evaluate(`
    var pid=state.projectFilter||state.data.projects[0].id;
    state.data.issues=[{id:'iss-edit-nosop',projectId:pid,number:62,kind:'requirement',source:'local',status:'backlog',priority:'medium',title:'编辑不走 LOOP',sopMode:'none',automationState:'ignored'}];
    state.data.tasks=[];openIssue(state.data.issues[0]);
  `);
  const save = async () => { await app.evaluate(`saveIssue({preventDefault(){},currentTarget:document.querySelector('#issueForm')})`); await settle(3); };

  open();
  const select = $('#issueForm [name="workflowId"]');
  assert.equal(select.value, '');
  assert.equal(select.options[select.selectedIndex].textContent, '不使用 LOOP');
  const workflowId = [...select.options].map((o) => o.value).find(Boolean);
  assert.ok(workflowId, 'the project has a runnable LOOP to pick');

  $('#issueForm [name="priority"]').value = 'high';
  await save();
  assert.equal(putBodies.at(-1).sopMode, 'none', 'an untouched choice keeps the requirement without a LOOP');
  assert.equal(putBodies.at(-1).workflowId, '');
  assert.equal(putBodies.at(-1).automationState, 'ignored');

  open();
  $('#issueForm [name="workflowId"]').value = workflowId;
  await save();
  assert.equal(putBodies.at(-1).sopMode, 'sop', 'picking a LOOP switches the requirement to LOOP delivery');
  assert.equal(putBodies.at(-1).workflowId, workflowId);
  assert.equal(putBodies.at(-1).automationState, '', 'the server restores scheduled claims the no-LOOP mode had turned off');

  open();
  $('#issueForm [name="workflowId"]').value = workflowId;
  const ignore = $('#issueForm [name="automationIgnored"]');
  ignore.checked = true;
  ignore.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await save();
  assert.equal(putBodies.at(-1).automationState, 'ignored', 'an explicit 定时忽略 choice is kept');

  open();
  const before = app.router.calls.length;
  await app.evaluate(`dispatchIssue()`);
  await settle(2);
  assert.ok(!app.router.calls.slice(before).some((c) => c.path.endsWith('/dispatch') || c.method === 'PUT'), 'starting without a picked LOOP neither saves nor dispatches');
  assert.match($('#toastContainer').textContent, /请先选择要运行的 LOOP/);

  open();
  $('#issueForm [name="workflowId"]').value = workflowId;
  await app.evaluate(`dispatchIssue()`);
  await settle(4);
  assert.equal(putBodies.at(-1).sopMode, 'sop');
  const dispatch = app.router.calls.filter((c) => c.path === '/api/issues/iss-edit-nosop/dispatch').at(-1);
  assert.equal(dispatch.body.workflowId, workflowId, 'the picked LOOP is the one that runs');
});

test('the edit modal keeps the default-flow wording for LOOP requirements', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    var pid=state.projectFilter||state.data.projects[0].id;
    openIssue({id:'iss-loop',projectId:pid,number:63,kind:'requirement',source:'local',status:'backlog',priority:'medium',title:'走 LOOP'});
  `);
  const select = app.document.querySelector('#issueForm [name="workflowId"]');
  assert.equal(select.options[0].textContent, '使用来源默认流程');
  app.evaluate(`openIssue(null)`);
  assert.equal(select.options[0].textContent, '使用来源默认流程');
});
