import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function describe(app, text) {
  const input = app.document.querySelector('#chatInput');
  input.value = text;
  input.dispatchEvent(new app.window.Event('input', { bubbles: true }));
}
const visible = (app, selector) => !app.document.querySelector(selector).hidden;
const paneShown = (app, n) => visible(app, `[data-req-pane="${n}"]`);

test('wizard step 1 gates actions on the description and skipping goes straight to an undispatched issue', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`openRequirementWizard()`);
  const $ = (s) => app.document.querySelector(s);
  assert.ok(paneShown(app, 1) && !paneShown(app, 2) && !paneShown(app, 3));
  assert.equal($('#chatInput').getAttribute('placeholder').startsWith('要做什么？例如：'), true);
  assert.equal($('#reqNext').disabled, true);
  assert.equal($('#reqSkip').disabled, true);
  assert.equal($('#reqNext').title, '先写一句需求');
  assert.equal($('[data-req-step="2"]').disabled, true);
  assert.match($('#reqRepoChipLabel').textContent, /分析仓库 atlas-web/);
  assert.ok(!$('#reqRepository').textContent.includes('已全部选择'));

  $('#reqRepoChipToggle').click();
  assert.equal($('#reqRepoPopover').hidden, false);
  assert.ok($('#reqRepoChip').classList.contains('is-open'));
  app.document.body.dispatchEvent(new app.window.Event('pointerdown', { bubbles: true }));
  assert.equal($('#reqRepoPopover').hidden, true);

  describe(app, 'Ship audit log\nKeep 90 days');
  assert.equal($('#reqNext').disabled, false);
  assert.equal($('#reqCharCount').textContent, '27 字');
  $('#reqSkip').click();
  assert.ok(paneShown(app, 3));
  assert.match($('#reqStepSkipTag').textContent, /已跳过/);
  assert.equal($('#reqTitle').value, 'Ship audit log');
  assert.equal($('#reqSummaryLabel').textContent, '需求描述');
  assert.match($('#reqOutcome').textContent, /跳过了澄清，创建后不会自动启动 SOP/);
  assert.equal($('#reqCreate').textContent.trim(), '创建需求');
  assert.equal($('#reqTargetTAPDHint').textContent, '未配置');
  assert.equal($('[data-req-target="tapd"]').disabled, true);
  assert.ok($('#reqConfirmRepoSlot').contains($('#reqRepository')), 'repository picker moves onto the confirm page');

  app.router.post('/api/issues', (call) => ({ status: 200, body: { id: 'issue-skip', ...call.body } }));
  $('#reqCreate').click();
  await settle(4);
  const create = app.router.calls.find((c) => c.path === '/api/issues');
  assert.equal(create.body.title, 'Ship audit log');
  assert.equal(create.body.description, 'Ship audit log\nKeep 90 days');
  assert.deepEqual(create.body.repositoryIds, ['repo-1']);
  assert.equal(create.body.workflowId, 'flow-1');
  assert.ok(!app.router.calls.some((c) => c.path.endsWith('/dispatch')));
  assert.ok(!app.router.calls.some((c) => c.path === '/api/chat-clarify'));
  assert.equal($('#requirementModal').classList.contains('open'), false);
  assert.match($('#toastContainer').textContent, /可在详情里触发 SOP/);
});

test('wizard clarifies one question at a time, auto-submits the last answer, and dispatches a ready requirement', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const $ = (s) => app.document.querySelector(s);
  app.evaluate(`openRequirementWizard()`);
  const replies = [
    { sessionId: 'clarify-1', startedAt: '2026-10-31T00:00:00Z', parsed: {
      ready: false, summary: 'Need audience and checks',
      questions: [
        { id: 'q1', question: 'Which users?', options: ['Employees', 'Guests'] },
        { id: 'q2', question: 'Which checks?', options: ['Unit', 'E2E'] },
      ],
      assessment: { dimensions: { goal: 70 }, missing: ['acceptance'] } } },
    { sessionId: 'clarify-1', parsed: { ready: true, summary: 'Employees only, unit tests.', suggestedTitle: 'Audit log', questions: [],
      assessment: { dimensions: { goal: 95 }, missing: [] } } },
  ];
  let round = 0;
  app.router.post('/api/chat-clarify', () => replies[Math.min(round++, replies.length - 1)]);
  describe(app, 'Ship audit log');
  $('#reqNext').click();
  await settle(4);
  assert.ok(paneShown(app, 2));
  const panel = $('#reqClarifyPanel');
  assert.match(panel.textContent, /第 1 \/ 2 题/);
  assert.equal(panel.querySelector('[data-req-option="0"]').disabled, false, 'options are clickable once the analysis finishes');
  assert.match(panel.textContent, /还有 2 个问题/);
  assert.match(panel.textContent, /分析师的理解/);
  assert.ok(!/推荐/.test(panel.textContent), 'no recommendation without recommended options');
  assert.ok(!/第 \d+ 轮/.test(panel.textContent));
  assert.equal($('#reqGoCreate').textContent.trim(), '直接去创建');
  assert.equal(app.router.calls.filter((c) => c.path === '/api/chat-clarify')[0].body.message, 'Ship audit log');

  panel.querySelector('#reqNextQuestion').click();
  assert.match(panel.textContent, /第 2 \/ 2 题/);
  panel.querySelector('#reqSubmitAnswers').click();
  assert.match(panel.textContent, /至少回答一题，或点「直接去创建」/);
  assert.equal(app.router.calls.filter((c) => c.path === '/api/chat-clarify').length, 1);
  panel.querySelector('#reqPrevQuestion').click();
  assert.match(panel.textContent, /第 1 \/ 2 题/);

  panel.querySelector('[data-req-option="0"]').click();
  await sleep(300);
  assert.match(panel.textContent, /第 2 \/ 2 题/);
  panel.querySelector('[data-req-option="0"]').click();
  await sleep(300);
  await settle(4);
  const second = app.router.calls.filter((c) => c.path === '/api/chat-clarify')[1];
  assert.equal(second.body.message, 'Which users?：Employees\nWhich checks?：Unit');
  assert.equal(second.body.sessionId, 'clarify-1');
  assert.match(panel.textContent, /已问清，可以创建/);
  assert.doesNotMatch(panel.textContent, /查看完整度详情|准入线/);
  assert.match($('#reqAnswerHistory').textContent, /你的回答.*Which users.*Employees/s);
  assert.equal($('#reqGoCreate').textContent.trim(), '下一步：确认创建');

  $('#reqGoCreate').click();
  assert.ok(paneShown(app, 3));
  assert.equal($('#reqTitle').value, 'Audit log');
  assert.equal($('#reqSummaryLabel').textContent, '需求结论');
  assert.match($('#reqOutcome').textContent, /自动启动/);
  assert.equal($('#reqCreate').textContent.trim(), '创建并启动 SOP');
  assert.equal($('#reqRepoChangedAlert').hidden, true);
  $('#reqBack').click();
  assert.ok(paneShown(app, 2));
  $('[data-req-step="3"]').click();
  assert.ok(paneShown(app, 3));

  app.router.post('/api/issues', (call) => ({ status: 200, body: { id: 'issue-ready', ...call.body, clarification: { status: 'ready', passed: true, gated: true, questions: [] } } }));
  app.router.post('/api/issues/issue-ready/dispatch', { id: 'task-ready', issueId: 'issue-ready', runId: 'run-ready', workflowId: 'flow-1' });
  app.router.get('/api/runs/run-ready', { id: 'run-ready', projectId: 'project-1', workflowId: 'flow-1', status: 'queued', nodes: {} });
  $('#reqCreate').click();
  await settle(6);
  const create = app.router.calls.find((c) => c.path === '/api/issues');
  assert.equal(create.body.title, 'Audit log');
  assert.equal(create.body.clarification.status, 'ready');
  assert.ok(app.router.calls.some((c) => c.path === '/api/issues/issue-ready/dispatch'));
  assert.ok(app.evaluate(`state.data.issues.some(issue=>issue.id==='issue-ready')`));
});

test('wizard creates a not-ready requirement without dispatching and handles TAPD sync failure', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const $ = (s) => app.document.querySelector(s);
  app.evaluate(`openRequirementWizard()`);
  app.router.post('/api/chat-clarify', { sessionId: 's-partial', parsed: { ready: false, summary: 'Current conclusion',
    questions: [{ question: 'Acceptance?', options: [], freeText: true }] } });
  describe(app, 'Complex request');
  $('#reqNext').click();
  await settle(4);
  assert.match($('#reqClarifyPanel').textContent, /请填写回答/);
  const other = $('#reqQuestionOther');
  other.value = 'Must pass review';
  other.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(app.evaluate(`collectChatReply()`), 'Acceptance?：Must pass review');
  $('#reqGoCreate').click();
  assert.match($('#reqOutcome').textContent, /澄清未完成，不会自动启动 SOP/);
  $('#reqTitle').value = '';
  $('#reqTitle').dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal($('#reqCreate').disabled, true);
  $('#reqTitle').value = 'Complex';
  $('#reqTitle').dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal($('#reqCreate').disabled, false);
  app.router.post('/api/issues', (call) => ({ status: 200, body: { id: 'issue-partial', ...call.body } }));
  $('#reqCreate').click();
  await settle(4);
  const partial = app.router.calls.filter((c) => c.path === '/api/issues').at(-1);
  assert.equal(partial.body.title, 'Complex');
  assert.equal(partial.body.clarification.status, 'in_progress');
  assert.equal(partial.body.clarification.passed, undefined);
  assert.ok(!app.router.calls.some((c) => c.path.endsWith('/dispatch')));
  assert.match($('#toastContainer').textContent, /澄清尚未完成/);

  app.evaluate(`
    Object.assign(currentProject(),{tapdWorkspaceId:'ws',tapdIterationId:'it'});
    state.data.syncConfig={...(state.data.syncConfig||{}),tapdMcpUrl:'https://tapd.example/mcp'};
    openRequirementWizard();
  `);
  describe(app, 'TAPD task');
  $('#reqSkip').click();
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
});
