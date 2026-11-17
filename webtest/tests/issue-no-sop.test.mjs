import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// A requirement created with 不使用 SOP has no SOP to trigger: the list and
// the drawer route it to a conversation, and an SOP is only run once the user
// explicitly picks one.
test('no-SOP requirements go to the chat and run an SOP only when one is picked', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = JSON.parse(app.evaluate(`
    var pid=state.projectFilter||state.data.projects[0].id;
    var noSOPIssue={id:'iss-nosop',projectId:pid,number:61,kind:'requirement',source:'local',status:'backlog',title:'不走 SOP',sopMode:'none',automationState:'ignored'};
    state.data.issues=[noSOPIssue];state.data.tasks=[];state.data.runs=[];
    JSON.stringify({sop:issueSOPLabel(noSOPIssue),fallback:defaultSOPForIssue(noSOPIssue),actions:issueActionButtons(noSOPIssue),trigger:triggerSOPButton(noSOPIssue),pane:sopPane(noSOPIssue,{issue:noSOPIssue,run:null,requirement:null})});
  `));
  assert.equal(out.sop, '不使用 SOP');
  assert.equal(out.fallback, '', 'no project SOP is filled in');
  assert.match(out.actions, /data-chat-issue="iss-nosop"[^>]*>去对话/);
  assert.doesNotMatch(out.actions, /data-trigger-issue/);
  assert.match(out.trigger, /data-chat-issue="iss-nosop"/);
  assert.match(out.pane, /这条需求不走 SOP/);
  assert.match(out.pane, /data-no-sop-workflow/);

  const $ = (s) => app.document.querySelector(s);
  await app.evaluate(`triggerIssue('iss-nosop')`);
  await settle(2);
  assert.ok(!app.router.calls.some((c) => c.path.endsWith('/dispatch')), 'triggering without a picked SOP does not dispatch');
  assert.match($('#toastContainer').textContent, /请先选择要运行的 SOP/);

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
