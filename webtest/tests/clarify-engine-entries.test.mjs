import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// Task 8: 两处澄清入口 + 个人设置默认。这些测试跑的是真实 SPA：向导和详情抽屉必须把
// 「能跑的框架」问出来并把它发出去，设置页那份默认要走凭据接口保存，而且失效时要说清楚。

const engineEngines = [
  { id: 'codebuddy', name: 'CodeBuddy', configured: true, available: true, reason: '', credential: 'codeBuddyApiKey', models: [{ id: 'glm-a', name: 'GLM A' }] },
  { id: 'codex', name: 'Codex', configured: true, available: true, reason: '', credential: 'codexApiKey', models: [{ id: 'codex:gpt-5.5', name: 'GPT 5.5', engine: 'codex' }] },
  { id: 'claude-code', name: 'Claude Code', configured: false, available: false, reason: '请先在个人配置中配置 Claude Code API Key 后再使用 Claude Code 模型', credential: 'claudeCodeApiKey', models: [] },
];

test('the new-requirement wizard asks which frameworks the project owner can run and clarifies on the effective one', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const me = Number(app.evaluate('Number(state.currentUser?.id)||0'));
  app.router.get(/^\/api\/clarify-engines/, {
    responsibleUserId: me, responsibleUserName: '我', default: 'codex:gpt-5.5', defaultStale: false, engines: engineEngines,
  });
  app.router.post('/api/chat-clarify', { sessionId: 's-eng', parsed: { ready: true, summary: '按 Codex 澄清完成', questions: [] } });

  await app.evaluate('openRequirementWizard()');
  await settle(4);
  const asked = app.router.calls.find(c => c.path === '/api/clarify-engines');
  assert.ok(asked, '向导要问一次框架可用性');
  assert.match(asked.search, /projectId=/, '没有单就先按项目问');
  // 选择器绑定的模型就是接口给的默认；标签文案由共享模型目录决定，这里只保证不再是占位符。
  assert.equal(app.document.querySelector('#reqClarifyModelPicker [data-clarify-model-value]').value, 'codex:gpt-5.5', '选择器绑定的就是真正会用的那个框架');
  assert.notEqual(app.document.querySelector('#reqClarifyModelPicker [data-model-label]').textContent.trim(), '选择模型');

  app.evaluate(`document.querySelector('#chatInput').value='导出 CSV 报表'`);
  await app.evaluate('sendChatClarify(false)');
  await settle(6);
  const call = app.router.calls.filter(c => c.path === '/api/chat-clarify').at(-1);
  assert.equal(call.body.model, 'codex:gpt-5.5', '发出去的就是界面上显示的那个框架');
  assert.equal(app.evaluate(`localStorage.getItem('agentflow.clarifyModel')`), null, '没动手就不该写浏览器偏好');
});

test('the issue drawer keeps the framework this item started with and names whose credentials run it', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false}`);
  app.router.get(/^\/api\/clarify-engines/, {
    responsibleUserId: 7, responsibleUserName: '责任人丁', default: 'glm-a', engines: engineEngines,
  });
  const issue = { ...app.evaluate('structuredClone(state.data.issues[0])'), id: 'issue-1',
    clarification: { status: 'in_progress', modelId: 'codex:gpt-5.5', sessionId: 'af-old', questions: [{ question: 'Browser?', options: ['Chrome', 'Safari'] }] } };
  app.router.post('/api/issues/issue-1/chat-clarify', {
    issue: { ...issue, clarification: { ...issue.clarification, status: 'ready', summary: 'Clarified' } }, ready: true,
  });
  app.evaluate(`state.data.issues=[${JSON.stringify(issue)}];
    state.issueDetailData={issue:state.data.issues[0],comments:[],run:null,task:null,requirement:null};
    state.issueDetailTab='clarify';state.drawerOpen=true;renderIssueDetailTab()`);
  await new Promise(resolve => setTimeout(resolve, 120));
  await settle(4);

  const pane = app.document.querySelector('.clarify-workspace');
  assert.match(pane.textContent, /澄清将使用这位责任人的凭据/, '责任人不是自己要说明用的是谁的凭据');
  assert.match(pane.textContent, /责任人丁/);
  assert.equal(pane.querySelector('[data-clarify-model-value]').value, 'codex:gpt-5.5', '抽屉里的选择器显示这张单记住的框架');

  await app.evaluate(`sendIssueClarification('Safari 也要')`);
  await settle(4);
  const call = app.router.calls.filter(c => c.path === '/api/issues/issue-1/chat-clarify').at(-1);
  assert.equal(call.body.model, 'codex:gpt-5.5', '继续澄清必须还用同一框架，否则接不上它自己的会话');
});

test('personal settings keep a default clarification framework and save it through the credentials API', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false,clarifyModel:''};state.data.users[0]=state.currentUser`);
  app.router.get('/api/assistant/models', {
    default: 'default',
    models: [{ id: 'default', name: 'CLI 默认模型', family: 'other' }, { id: 'codex:gpt-5.5', name: 'GPT 5.5', family: 'gpt', engine: 'codex' }],
    engines: [
      { id: 'codebuddy', name: 'CodeBuddy', configured: true, credential: 'codeBuddyApiKey' },
      { id: 'codex', name: 'Codex', configured: true, credential: 'codexApiKey' },
      { id: 'claude-code', name: 'Claude Code', configured: false, credential: 'claudeCodeApiKey' },
    ],
  });
  app.router.put('/api/users/1/credentials', { id: 1, clarifyModel: 'codex:gpt-5.5', codexApiKeyConfigured: true });
  await app.evaluate('renderPersonalSettings()');

  const row = () => app.document.querySelector('[data-clarify-pref]');
  assert.ok(row(), '「模型与执行」组顶部要有需求澄清默认行');
  assert.match(row().textContent, /需求澄清 · 默认框架与模型/);
  row().querySelector('[data-model-toggle]').click();
  await settle(4);
  const options = [...row().querySelectorAll('.ai-model-option')];
  const codex = options.find(option => option.dataset.id === 'codex:gpt-5.5');
  assert.ok(codex && !codex.disabled, '配了 Key 的框架可以选为默认');
  assert.ok(options.some(option => option.disabled && option.dataset.id.startsWith('claude-code:')) || !options.some(option => option.dataset.id.startsWith('claude-code:')), '没配 Key 的框架整组点不动');
  codex.click();
  await settle(6);
  const put = app.router.calls.filter(c => c.method === 'PUT' && c.path === '/api/users/1/credentials').at(-1);
  assert.ok(put, '默认框架走凭据接口保存');
  assert.deepEqual(put.body.clarifyModel, 'codex:gpt-5.5');

  // 默认指向一个已经不配的框架时，要说「已失效」，不能假装它还在用。
  app.evaluate(`state.currentUser={...state.currentUser,clarifyModel:'claude-code:claude-opus-5'};state.data.users[0]=state.currentUser;renderCredentialPanel()`);
  await settle(3);
  assert.match(row().textContent, /已失效，当前用 CodeBuddy/, '失效的默认值要显式标出来');
});
