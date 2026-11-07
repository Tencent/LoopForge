import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

test('changing repositories keeps the conclusion, flags it on the confirm page, and reclarifies with the new choice', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const $ = (s) => app.document.querySelector(s);
  const result = app.evaluate(`(()=>{
    state.data.repositories.push({id:'repo-2',projectId:'project-1',name:'API',defaultBranch:'main'});
    openRequirementWizard();
    $('#chatInput').value='Original need';
    chatHistory=[{role:'user',content:'Original need'},{role:'assistant',content:'{}'}];
    chatClarifySessionId='s-keep';
    const before=selectedReqRepositoryIds();
    setReqRepositoryIds(['repo-1']);
    state.reqClarifyRepoSnapshot=reqCurrentRepoSnapshot();
    state.chatClarifyResult={ready:true,summary:'Enough',suggestedTitle:'Need',questions:[]};
    goReqStep(3);
    const alertBefore=$('#reqRepoChangedAlert').hidden;
    setReqRepositoryIds(['repo-2']);
    return {before,alertBefore,history:chatHistory.length,ready:state.chatClarifyResult.ready,session:chatClarifySessionId,
      alertAfter:$('#reqRepoChangedAlert').hidden,text:$('#reqRepoChangedText').textContent};
  })()`);
  assert.equal(result.before.sort().join(','), 'repo-1,repo-2');
  assert.equal(result.alertBefore, true);
  assert.equal(result.history, 2, 'history survives a repository change');
  assert.equal(result.ready, true);
  assert.equal(result.session, 's-keep');
  assert.equal(result.alertAfter, false);
  assert.match(result.text, /澄清基于原仓库（atlas-web）/);

  app.router.post('/api/chat-clarify', { sessionId: 's-keep', parsed: { ready: true, summary: 'Reanalysed on API', questions: [] } });
  $('#reqReclarify').click();
  await settle(4);
  const call = app.router.calls.filter((c) => c.path === '/api/chat-clarify').at(-1);
  assert.deepEqual(call.body.repositoryIds, ['repo-2']);
  assert.equal(call.body.message, 'Original need');
  assert.equal(app.evaluate(`state.reqStep`), 2);
  assert.match($('#reqClarifyPanel').textContent, /Reanalysed on API/);
  $('#reqGoCreate').click();
  assert.equal($('#reqRepoChangedAlert').hidden, true, 'a fresh clarification clears the repository alert');
});

test('draft restore brings back the step, question index, answers, title and target', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const $ = (s) => app.document.querySelector(s);
  app.evaluate(`openRequirementWizard()`);
  const raw = {
    open: true, projectId: 'project-1', input: 'Restored text',
    history: [{ role: 'user', content: 'Restored text' }, { role: 'user', content: 'Scope?：Web' }],
    sessionId: 's1', clarificationStartedAt: '2026-01-01', jobId: '',
    clarifyResult: { ready: false, summary: 'Need scope', questions: [
      { question: 'Scope?', options: ['Web', 'API'] },
      { question: 'Users?', options: ['Staff', 'Guests'], multiSelect: true },
    ] },
    step: 2, reached: 3, clarifySkipped: false, questionIndex: 1, answers: { 1: { selected: [1], other: '' } },
    title: 'Saved title', titleEdited: true, target: 'local', repoSnapshot: { ids: ['repo-1'], baseBranch: '' },
    lastDescription: 'Restored text', repoIds: ['repo-1'], baseBranch: '', workflowId: 'flow-1',
  };
  app.evaluate(`applyRequirementComposerDraft(${JSON.stringify(raw)});persistRequirementComposer()`);
  assert.equal(app.evaluate(`state.reqStep`), 2);
  assert.ok(!app.document.querySelector('[data-req-pane="2"]').hidden);
  assert.match($('#reqClarifyPanel').textContent, /第 2 \/ 2 题/);
  assert.match($('#reqClarifyPanel').textContent, /可多选/);
  assert.equal($('[data-req-option="1"]').getAttribute('aria-checked'), 'true');
  assert.match($('#reqAnswerHistory').textContent, /Scope\?.*Web/s);
  assert.equal($('#chatInput').value, 'Restored text');
  assert.equal($('#reqTitle').value, 'Saved title');
  assert.equal($('[data-req-step="3"]').disabled, false, 'reached steps stay clickable');
  const saved = app.evaluate(`JSON.parse(sessionStorage.getItem('agentflow.requirementComposer'))`);
  assert.equal(saved.step, 2);
  assert.equal(saved.questionIndex, 1);
  assert.deepEqual(JSON.parse(JSON.stringify(saved.answers)), { 1: { selected: [1], other: '' } });
  assert.equal(saved.repoIds[0], 'repo-1');
  assert.equal(app.evaluate(`projectTAPDReady()`), false);

  // Multi-choice last question waits for 提交回答.
  $('[data-req-option="0"]').click();
  await sleep(300);
  assert.ok(!app.router.calls.some((c) => c.path === '/api/chat-clarify'));
  app.router.post('/api/chat-clarify', { sessionId: 's1', parsed: { ready: true, summary: 'Web for everyone', questions: [] } });
  $('#reqSubmitAnswers').click();
  await settle(4);
  const call = app.router.calls.find((c) => c.path === '/api/chat-clarify');
  assert.equal(call.body.message, 'Users?：Staff、Guests');

  // A ready conclusion accepts a supplement and re-analyses with it.
  const supplement = $('#reqSupplement');
  supplement.value = 'Also export CSV';
  $('#reqResubmit').click();
  await settle(4);
  assert.equal(app.router.calls.filter((c) => c.path === '/api/chat-clarify').at(-1).body.message, 'Also export CSV');
});

test('composer consumes clarification SSE, counts tools, and exposes the repository warning', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const $ = (s) => app.document.querySelector(s);
  app.evaluate(`openRequirementWizard();$('#chatInput').value='Improve login';$('#chatInput').dispatchEvent(new Event('input'));goReqStep(2);setChatSendAnalyzing(true)`);
  assert.equal($('#reqThinking').hidden, false);
  assert.match($('#reqThinkingLabel').textContent, /分析师正在读代码/);
  const done = {
    sessionId: 'clarify-s1', startedAt: '2026-10-31T00:00:00Z',
    repository: { id: 'repo-1', name: 'atlas-web', analyzed: false, reason: '\u001b[31mclone failed\u001b[0m' },
    parsed: { ready: false, summary: 'Need details', questions: [{ question: 'Which users?', options: [{ label: 'Staff', recommended: true }, { label: 'Guests' }] }] },
  };
  const stream = new Response([
    'event: open\ndata: {"repository":{"name":"atlas-web","analyzed":false}}\n\n',
    'event: job\ndata: {"jobId":"job-1","startedAt":"2026-10-31T00:00:00Z"}\n\n',
    'event: thought\ndata: {"kind":"tool","tool":"Grep","detail":"auth"}\n\n',
    'event: thought\ndata: {"kind":"status","text":"Assessing scope"}\n\n',
    `event: done\ndata: ${JSON.stringify(done)}\n\n`,
  ].join(''), { headers: { 'Content-Type': 'text/event-stream' } });
  app.window.TextDecoder = TextDecoder;
  const streamed = await app.evaluate(`consumeClarifyStream`).call(null, stream);
  assert.match($('#reqTraceLabel').textContent, /已用 1 个工具/);
  assert.equal($('#reqTraceToggle').hidden, false);
  assert.match($('#reqThinkingStream').textContent, /未拉取到代码仓库/);
  $('#reqTraceToggle').click();
  assert.equal($('#reqThinkingStream').hidden, true);
  $('#reqTraceToggle').click();
  assert.equal($('#reqThinkingStream').hidden, false);
  app.evaluate(`applyChatClarifyOutcome(${JSON.stringify(streamed)})`);
  const panel = $('#reqClarifyPanel');
  assert.match(panel.textContent, /未能读取代码仓库/);
  assert.match(panel.textContent, /clone failed/);
  assert.ok(!panel.textContent.includes('\u001b'));
  assert.match(panel.textContent, /推荐/);
  assert.ok(panel.querySelector('#reqUseRecommended'), 'recommended options enable the shortcut');
  panel.querySelector('[data-req-option="0"]').click();
  assert.match(app.evaluate(`collectChatReply()`), /Staff/);
  assert.equal(app.evaluate(`chatClarifySessionId`), 'clarify-s1');
});

test('composer surfaces failures, stops, and retries the clarification', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const $ = (s) => app.document.querySelector(s);
  app.evaluate(`openRequirementWizard();$('#chatInput').value='Add export';$('#chatInput').dispatchEvent(new Event('input'))`);
  app.router.post('/api/chat-clarify', { error: 'model unavailable' }, 503);
  $('#chatInput').dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true }));
  await settle(4);
  assert.equal(app.evaluate(`state.reqStep`), 2);
  assert.match($('#reqClarifyPanel').textContent, /分析失败：model unavailable/);
  assert.ok($('#chatClarifyRetry'));

  app.router.post('/api/chat-clarify', { sessionId: 'request-session', parsed: { ready: true, summary: 'Export CSV with headers', suggestedTitle: 'CSV export', questions: [] } });
  $('#chatClarifyRetry').click();
  await settle(4);
  const calls = app.router.calls.filter((c) => c.path === '/api/chat-clarify');
  assert.equal(calls.at(-1).body.message, 'Add export');
  assert.equal(app.evaluate(`chatHistory.filter(h=>h.role==='user').length`), 1, 'retry does not duplicate the description');
  assert.match($('#reqClarifyPanel').textContent, /Export CSV/);

  // Going back without changing the description does not re-run the analysis.
  $('#reqBack').click();
  assert.equal(app.evaluate(`state.reqStep`), 1);
  $('#reqNext').click();
  await settle(2);
  assert.equal(app.router.calls.filter((c) => c.path === '/api/chat-clarify').length, calls.length);
  // An edited description is sent as an update.
  $('#reqBack').click();
  $('#chatInput').value = 'Add export with BOM';
  $('#chatInput').dispatchEvent(new app.window.Event('input', { bubbles: true }));
  app.router.post('/api/chat-clarify', (call) => new Promise((_, reject) => {
    call.options.signal.addEventListener('abort', () => reject(new app.window.DOMException('aborted', 'AbortError')));
  }));
  $('#reqNext').click();
  await settle(2);
  assert.match(app.router.calls.filter((c) => c.path === '/api/chat-clarify').at(-1).body.message, /需求描述已更新：\nAdd export with BOM/);
  assert.equal($('#chatStop').hidden, false);
  assert.equal($('#reqGoCreate').hidden, true);
  app.evaluate(`abortChatClarify()`);
  await settle(4);
  assert.equal($('#chatStop').hidden, true);
  assert.match($('#reqClarifyPanel').textContent, /Export CSV/, 'stopping restores the previous conclusion');

  // Skipping from step 2 after a clarification still creates without dispatching.
  app.evaluate(`state.reqClarifySkipped=true;goReqStep(3)`);
  assert.match($('#reqOutcome').textContent, /跳过了澄清/);
  $('[data-req-step="2"]').click();
  assert.equal(app.evaluate(`state.reqClarifySkipped`), false);
  app.document.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  await settle(2);
  assert.equal($('#requirementModal').classList.contains('open'), false);
});

test('restoring an interrupted draft reattaches the clarification stream and repository controls stay in sync', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const $ = (s) => app.document.querySelector(s);
  app.window.TextDecoder = TextDecoder;
  const draft = {
    open: true, projectId: 'project-1', input: 'Resume me', history: [{ role: 'user', content: 'Resume me' }],
    sessionId: 's-resume', jobId: 'job-9', interrupted: true, step: 2, reached: 2, repoIds: ['repo-1'], workflowId: 'flow-1',
  };
  const done = { sessionId: 's-resume', parsed: { ready: false, summary: 'Resumed', questions: [
    { question: 'Scope?', options: [{ label: 'Web', recommended: true }, { label: 'API' }] },
    { question: 'Users?', options: [{ label: 'Staff', recommended: true }, { label: 'Guests', recommended: true }], multiSelect: true },
  ] } };
  app.router.get('/api/chat-clarify?jobId=job-9', { status: 200, headers: { 'content-type': 'text/event-stream' },
    body: `event: thought\ndata: {"kind":"tool","tool":"Read","detail":"a.go"}\n\nevent: done\ndata: ${JSON.stringify(done)}\n\n` });
  app.router.get('/api/chat-clarify?jobId=job-gone', { status: 404, body: { error: 'gone' } });
  const fetchWithBody = app.window.fetch;
  app.window.fetch = async (input, init) => {
    const resp = await fetchWithBody(input, init);
    if (String(input).includes('jobId=job-9')) {
      const text = await resp.text();
      return new Response(text, { status: 200, headers: { 'Content-Type': 'text/event-stream' } });
    }
    return resp;
  };
  app.evaluate(`sessionStorage.setItem('agentflow.requirementComposer',${JSON.stringify(JSON.stringify(draft))})`);
  assert.equal(app.evaluate(`restoreRequirementComposer()`), true);
  await settle(6);
  assert.ok($('#requirementModal').classList.contains('open'));
  assert.match($('#toastContainer').textContent, /正在接上分析/);
  assert.match($('#reqClarifyPanel').textContent, /Resumed/);
  assert.equal(app.evaluate(`chatClarifyJobId`), '');

  app.evaluate(`useRecommendedAnswers()`);
  assert.equal(app.evaluate(`collectChatReply()`), 'Scope?：Web\nUsers?：Staff、Guests');
  app.evaluate(`clearTimeout(reqAdvanceTimer)`);

  app.evaluate(`chatAbortController=null;resumeChatClarify('job-gone')`);
  await settle(4);
  assert.match($('#reqClarifyPanel').textContent, /Resumed/, 'an expired job keeps the last conclusion');
  app.evaluate(`sessionStorage.setItem('agentflow.requirementComposer',JSON.stringify({open:true,input:'x',interrupted:true}))`);
  app.evaluate(`$('#requirementModal').classList.remove('open');restoreRequirementComposer()`);
  assert.match($('#toastContainer').textContent, /进行中的分析已中断/);
  assert.equal(app.evaluate(`requirementComposerDraftUseful(null)`), false);

  // Repository picker: add through the select, edit the branch, remove again.
  app.evaluate(`state.data.repositories.push({id:'repo-2',projectId:'project-1',name:'API',defaultBranch:'main'});goReqStep(1);setReqRepositoryIds(['repo-1'])`);
  const add = $('#reqRepository select');
  add.value = 'repo-2';
  add.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  assert.deepEqual([...app.evaluate(`selectedReqRepositoryIds()`)], ['repo-1', 'repo-2']);
  $('[data-remove-req-repo="repo-2"]').click();
  assert.deepEqual([...app.evaluate(`selectedReqRepositoryIds()`)], ['repo-1']);
  $('#reqBaseBranch').value = 'feature/x';
  $('#reqBaseBranch').dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.match($('#reqRepoChipLabel').textContent, /feature\/x/);
  $('#reqRepoChipToggle').click();
  app.document.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  assert.equal($('#reqRepoPopover').hidden, true, 'Escape closes the repository popover first');
  assert.ok($('#requirementModal').classList.contains('open'));
  app.evaluate(`setReqTarget('tapd')`);
  assert.equal(app.evaluate(`state.reqTarget`), 'local', 'TAPD cannot be chosen when unconfigured');
  assert.equal(app.evaluate(`issueCanPushTAPD(state.data.issues[0])`), false);
  app.evaluate(`state.data.repositories=[];renderReqContext();renderReqFooter()`);
  assert.equal($('#reqRepoChip').hidden, true);
  assert.match($('#reqComposerHint').textContent, /还没绑定仓库/);
});

test('pushing an existing issue to TAPD from the detail page updates it and reports failures', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const $ = (s) => app.document.querySelector(s);
  app.evaluate(`pushIssueToTAPDFromDetail('missing')`);
  await settle(2);
  assert.match($('#toastContainer').textContent, /未找到工作项/);
  await app.evaluate(`pushIssueToTAPDFromDetail('issue-1')`);
  assert.match($('#toastContainer').textContent, /未配置 TAPD/);
  app.evaluate(`Object.assign(state.data.projects[0],{tapdWorkspaceId:'ws',tapdIterationId:'it'});state.data.syncConfig={tapdMcpUrl:'https://tapd.example/mcp'}`);
  assert.equal(app.evaluate(`issueCanPushTAPD(state.data.issues[0])`), true);
  app.router.post('/api/sync/create', (call) => ({ status: 200, body: { ...app.evaluate(`state.data.issues[0]`), id: call.body.issueId, externalRef: { externalId: 'T-42' } } }));
  await app.evaluate(`pushIssueToTAPDFromDetail('issue-1')`);
  assert.match($('#toastContainer').textContent, /已在 TAPD 创建需求 T-42/);
  app.router.post('/api/sync/create', { error: 'tapd down' }, 502);
  await app.evaluate(`pushIssueToTAPDFromDetail('issue-1')`);
  assert.match($('#toastContainer').textContent, /tapd down/);
});
