import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

function describe(app, text) {
  const input = app.document.querySelector('#chatInput');
  input.value = text;
  input.dispatchEvent(new app.window.Event('input', { bubbles: true }));
}

test('the composer gates on the description and creates an undispatched issue without clarifying', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`openRequirementWizard()`);
  const $ = (s) => app.document.querySelector(s);
  assert.equal($('#chatInput').getAttribute('placeholder').startsWith('要做什么？例如：'), true);
  assert.equal($('#reqSteps'), null, 'there is no step bar any more');
  assert.equal($('#reqCreate').disabled, true);
  assert.equal($('#reqCreateChat').disabled, true);
  assert.equal($('#reqCreate').title, '先写一句需求');
  assert.match($('#reqRepoChipLabel').textContent, /仓库 atlas-web/);
  assert.ok(!$('#reqRepository').textContent.includes('已全部选择'));
  assert.equal($('#reqTargetTAPDHint').textContent, '未配置');
  assert.equal($('[data-req-target="tapd"]').disabled, true);

  $('#reqRepoChipToggle').click();
  assert.equal($('#reqRepoPopover').hidden, false);
  assert.ok($('#reqRepoChip').classList.contains('is-open'));
  app.document.body.dispatchEvent(new app.window.Event('pointerdown', { bubbles: true }));
  assert.equal($('#reqRepoPopover').hidden, true);

  describe(app, 'Ship audit log\nKeep 90 days');
  assert.equal($('#reqCreate').disabled, false);
  assert.equal($('#reqCreateChat').disabled, false);
  assert.equal($('#reqCharCount').textContent, '27 字');
  assert.match($('#reqComposerHint').textContent, /去对话里把需求聊清楚/);
  assert.equal($('[data-req-delivery="sop"]').getAttribute('aria-checked'), 'true', 'SOP delivery is the default');
  assert.equal($('#reqWorkflowField').hidden, false);
  const workflowId = $('#reqWorkflow').value;
  assert.ok(workflowId, 'the project SOP is preselected');

  app.router.post('/api/issues', (call) => ({ status: 200, body: { id: 'issue-direct', ...call.body } }));
  $('#reqCreate').click();
  await settle(4);
  const create = app.router.calls.find((c) => c.path === '/api/issues');
  assert.equal(create.body.title, 'Ship audit log');
  assert.equal(create.body.description, 'Ship audit log\nKeep 90 days');
  assert.deepEqual(create.body.repositoryIds, ['repo-1']);
  assert.equal(create.body.workflowId, workflowId);
  assert.equal(create.body.sopMode, '');
  assert.equal(create.body.clarification, undefined);
  assert.ok(!app.router.calls.some((c) => c.path.endsWith('/dispatch')));
  assert.ok(!app.router.calls.some((c) => c.path.includes('chat-clarify')));
  assert.equal($('#requirementModal').classList.contains('open'), false);
  assert.match($('#toastContainer').textContent, /可在详情里触发 SOP/);
  assert.ok(app.evaluate(`state.data.issues.some(issue=>issue.id==='issue-direct')`));
});

test('创建并去对话澄清 creates the issue and opens a new chat with the requirement @-mentioned', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const $ = (s) => app.document.querySelector(s);
  app.evaluate(`openRequirementWizard()`);
  describe(app, 'Add CSV export');
  $('[data-req-delivery="none"]').click();
  assert.equal($('[data-req-delivery="none"]').getAttribute('aria-checked'), 'true');
  assert.equal($('#reqWorkflowField').hidden, true, 'the SOP picker hides when no SOP is used');
  assert.match($('#reqComposerHint').textContent, /定时任务也不会自动认领/);
  app.router.post('/api/issues', (call) => ({ status: 200, body: { id: 'issue-chat', ...call.body } }));
  $('#reqCreateChat').click();
  await settle(4);
  const create = app.router.calls.find((c) => c.path === '/api/issues');
  assert.equal(create.body.title, 'Add CSV export');
  assert.equal(create.body.sopMode, 'none');
  assert.equal(create.body.workflowId, '');
  assert.equal(create.body.playbookId, '');
  assert.ok(!app.router.calls.some((c) => c.path.endsWith('/dispatch')), 'the chat handoff does not start an SOP');
  assert.equal($('#requirementModal').classList.contains('open'), false);
  assert.match($('#toastContainer').textContent, /在对话里继续澄清/);
  assert.equal(app.evaluate(`activeViewName()`), 'collaboration');
  assert.equal(app.evaluate(`localStorage.getItem('agentflow.activeProject')`), 'project-1');

  // The chat frame is mounted but not ready yet: the mention waits for it.
  const frame = $('#chatNextFrame');
  const posted = [];
  frame.contentWindow.postMessage = (message) => posted.push(JSON.parse(JSON.stringify(message)));
  assert.deepEqual(JSON.parse(JSON.stringify(app.evaluate(`pendingChatIssueMention`))), { issueId: 'issue-chat', projectId: 'project-1' });
  app.window.dispatchEvent(new app.window.MessageEvent('message', {
    data: { type: 'agentflow-chat-ready' }, origin: app.window.location.origin, source: frame.contentWindow,
  }));
  assert.deepEqual(posted[0], { type: 'agentflow-chat-mention-issue', issueId: 'issue-chat', projectId: 'project-1' });
  assert.equal(posted[1]?.type, 'agentflow-project-context', 'the project context follows the mention');
  assert.equal(app.evaluate(`pendingChatIssueMention`), null);

  // A ready chat receives the next requirement at once, without reloading.
  posted.length = 0;
  const src = frame.src;
  app.evaluate(`switchView('issues');openIssueInChat({id:'issue-next',projectId:'project-1'})`);
  assert.deepEqual(posted[0], { type: 'agentflow-chat-mention-issue', issueId: 'issue-next', projectId: 'project-1' });
  assert.equal(frame.src, src);
  assert.equal(app.evaluate(`activeViewName()`), 'collaboration');

  // A chat that was never mounted loads with the requirement in its URL.
  app.evaluate(`switchView('issues');$('#chatNextFrame').dataset.src='';openIssueInChat({id:'issue-cold',projectId:'project-1'})`);
  const cold = new URL(frame.src, 'http://localhost');
  assert.equal(cold.pathname, '/chatbot/');
  assert.equal(cold.searchParams.get('mentionIssue'), 'issue-cold');
  assert.equal(cold.searchParams.get('projectId'), 'project-1');
  assert.equal(app.evaluate(`pendingChatIssueMention`), null);
});

test('Ctrl+Enter hands the requirement to the chat and TAPD sync failures are reported', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const $ = (s) => app.document.querySelector(s);
  app.evaluate(`openRequirementWizard()`);
  describe(app, 'Keyboard request');
  app.router.post('/api/issues', (call) => ({ status: 200, body: { id: 'issue-key', ...call.body } }));
  $('#chatInput').dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true }));
  await settle(4);
  assert.ok(app.router.calls.some((c) => c.path === '/api/issues' && c.body.description === 'Keyboard request'));
  assert.equal(app.evaluate(`pendingChatIssueMention?.issueId`), 'issue-key');

  app.evaluate(`
    Object.assign(currentProject(),{tapdWorkspaceId:'ws',tapdIterationId:'it'});
    state.data.syncConfig={...(state.data.syncConfig||{}),tapdMcpUrl:'https://tapd.example/mcp'};
    openRequirementWizard();
  `);
  describe(app, 'TAPD task');
  assert.equal($('[data-req-target="tapd"]').disabled, false);
  $('[data-req-target="tapd"]').click();
  assert.equal($('[data-req-target="tapd"]').getAttribute('aria-checked'), 'true');
  await app.evaluate(`createIssueFromChat('invalid')`);
  assert.match($('#toastContainer').textContent, /请选择平台或 TAPD/);
  app.router.post('/api/issues', (call) => ({ status: 200, body: { id: 'issue-tapd', ...call.body } }));
  app.router.post('/api/sync/create', { error: 'tapd down' }, 502);
  $('#reqCreate').click();
  await settle(4);
  assert.ok(app.router.calls.some((c) => c.path === '/api/sync/create' && c.body.issueId === 'issue-tapd'));
  assert.match($('#toastContainer').textContent, /TAPD 同步失败/);
  assert.equal($('#requirementModal').classList.contains('open'), false);
});
