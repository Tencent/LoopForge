import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

function renderCodeBuddyRow(app, user) {
  const runner = {
    id: 'runner-cb', name: 'Laptop', ownerUserId: 1, status: 'online', os: 'linux', arch: 'amd64',
    lastSeenAt: new Date().toISOString(),
    runtimes: [
      { provider: 'codebuddy', status: 'online', authMode: 'personal', version: '2.0' },
      { provider: 'codex', status: 'online', authMode: 'local', version: '1.0' },
    ],
  };
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    state.data.users=[${JSON.stringify({ id: 1, loginName: 'owner', ...user })}];
    state.runners=[${JSON.stringify(runner)}];state.defaultRunnerId='';state.cloudPool={configured:true,online:1};
    renderRunnersView();
  `);
  const list = app.document.querySelector('#runnersList');
  const fixed = list.querySelectorAll('.runner-auth-fixed');
  assert.equal(fixed.length, 1, 'only the CodeBuddy row has a fixed credential label');
  assert.ok(list.querySelector('[data-runner-auth="runner-cb|codex"]'), 'Codex keeps its auth-source select');
  return fixed[0];
}

test('runner card uses the same short CodeBuddy authentication label in all network environments', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  for (const value of ['', 'internal', 'ioa', 'china<b>']) {
    const el = renderCodeBuddyRow(app, { codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: value });
    assert.equal(el.firstElementChild.textContent, 'API Key');
    assert.equal(el.hasAttribute('title'), false);
  }
});

test('runner card offers the short authentication labels with a separate explanation', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  renderCodeBuddyRow(app, {});
  const select = app.document.querySelector('[data-runner-auth="runner-cb|codex"]');
  assert.deepEqual([...select.options].map(option=>option.textContent), ['账号登录', 'API Key']);
  app.document.querySelector('[data-runner-auth-help]').click();
  assert.match(app.document.querySelector('#confirmMessage').textContent, /Claude Code、Codex 和 Antigravity.*已有登录.*native、Docker 沙箱/);
  assert.match(app.document.querySelector('#confirmMessage').textContent, /当前发言人的 Key/);
  assert.match(app.document.querySelector('#confirmMessage').textContent, /Gemini API Key 按 API 单独计费/);
});

test('runner card indicates a missing CodeBuddy key on its single-line label', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const el = renderCodeBuddyRow(app, { codeBuddyApiKeyConfigured: false, codeBuddyInternetEnvironment: 'ioa' });
  assert.equal(el.firstElementChild.textContent, 'API Key');
  assert.match(el.getAttribute('title'), /个人配置/);
  assert.equal(el.classList.contains('missing'), false);
});

test('Claude Code, Codex and Antigravity offer account login and API Key rows explain their credentials', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  renderCodeBuddyRow(app, {});
  app.evaluate(`
    state.runners[0].runtimes.push(
      {provider:'claude-code',authMode:'local',status:'online'},
      {provider:'cursor',authMode:'personal',status:'online'},
      {provider:'agy',authMode:'personal',status:'online'},
      {provider:'zcode',authMode:'personal',status:'online'}
    );
    renderRunnersView();
  `);
  const choices = [...app.document.querySelectorAll('[data-runner-auth]')];
  assert.deepEqual(choices.map(select => select.dataset.runnerAuth), ['runner-cb|codex', 'runner-cb|claude-code', 'runner-cb|agy']);
  assert.equal(app.document.querySelector('.runner-runtime-icon.agy img')?.getAttribute('src'), '/images/credentials/antigravity.svg');
  for (const select of choices) {
    assert.deepEqual([...select.options].map(option => option.textContent), ['账号登录', 'API Key']);
  }
  const help = [...app.document.querySelectorAll('[data-runner-key-help]')];
  assert.deepEqual(help.map(button => button.dataset.runnerKeyHelp), ['codebuddy', 'cursor', 'zcode']);
  const commonHelp = /支持 API Key.*订阅套餐.*计入对应的套餐额度.*个人配置/;
  const providerHelp = {
    zcode: /GLM Coding Plan.*订阅额度.*Start Plan.*无法通过 API Key 使用/,
  };
  for (const button of help) {
    assert.equal(button.parentElement.firstElementChild.textContent, 'API Key');
    const tooltip = providerHelp[button.dataset.runnerKeyHelp] || commonHelp;
    assert.match(button.title, tooltip);
    button.click();
    assert.match(app.document.querySelector('#confirmMessage').textContent, tooltip);
    app.document.querySelector('#confirmOkBtn').click();
    await settle();
  }
});

test('testing a CLI sends its machine and provider and shows the result', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  renderCodeBuddyRow(app, {});
  let finish;
  app.router.post('/api/runners/runtimes/test', () => new Promise(resolve => { finish = resolve; }));
  const trigger = () => app.document.querySelector('[data-runner-runtime-menu="runner-cb|codex"]');
  trigger().click();
  const items = [...app.document.querySelectorAll('.runner-runtime-menu [role="menuitem"]')];
  assert.deepEqual(items.map(item => item.textContent.trim()), ['测试', '更新', '模型列表']);
  assert.equal(items[0], app.document.activeElement);
  items[0].click();
  assert.equal(trigger().getAttribute('aria-busy'), 'true');
  assert.ok(app.document.querySelector('img[src="/images/credentials/codex.svg"]'));
  const request = app.router.calls.find(c => c.path === '/api/runners/runtimes/test');
  assert.deepEqual(request.body, { runnerId: 'runner-cb', provider: 'codex' });
  finish({ ok: true });
  await settle();
  assert.equal(trigger().getAttribute('aria-busy'), 'false');
  assert.match(app.document.querySelector('#toastContainer').textContent, /Codex 测试通过/);
});

test('Runner update checks first and applies after the user chooses to update', async t => {
  const app = await loadApp();
  t.after(app.close);
  renderCodeBuddyRow(app, {});
  app.router.get('/api/runners/runner-cb/maintenance', {status: 'available', current: '0.10.2', canUpdate: true});
  app.router.post('/api/runners/runner-cb/maintenance', {status: 'updating'});
  app.evaluate(`followRunnerUpdate=async()=>{}`);
  app.document.querySelector('[data-runner-check-update]').click();
  await settle();
  assert.equal(app.router.calls.filter(c => c.method === 'POST' && c.path.endsWith('/maintenance')).length, 0);
  assert.equal(app.document.querySelector('#confirmOkBtn').textContent, '立即更新');
  app.document.querySelector('#confirmOkBtn').click();
  await settle();
  assert.equal(app.router.calls.filter(c => c.method === 'POST' && c.path.endsWith('/maintenance')).length, 1);
});

test('CLI model editor uses the selected auth source and saves the personal catalog', async t => {
  const app = await loadApp();
  t.after(app.close);
  const model = {id:'gateway/model',name:'Personal model'};
  renderCodeBuddyRow(app, { codeBuddyApiKeyConfigured: true, codeBuddyModels: [model] });
  app.router.post('/api/runners/runner-cb/models/probe', {
    source:'runner', models:[{id:'codex/account-model',name:'Account model'}],
  });
  app.router.post('/api/users/1/cli-models/probe', {
    source:'official', models:[model,{id:'another-model',name:'Another model'}],
  });
  app.router.put('/api/users/1/credentials', ({body}) => ({
    id:1, codeBuddyApiKeyConfigured:true, codeBuddyModels:[model], ...body,
  }));
  const show = provider => {
    app.document.querySelector(`[data-runner-runtime-menu="runner-cb|${provider}"]`).click();
    [...app.document.querySelectorAll('.runner-runtime-menu button')].find(b => b.textContent.trim()==='模型列表').click();
  };
  const picker = action => app.document.querySelector(`.runner-model-editor [data-catalog-act="${action}"]`);
  const modelIDs = () => [...app.document.querySelectorAll('.runner-model-editor [data-catalog-field="id"]')].map(input => input.value);
  show('codex');
  assert.match(app.document.querySelector('#confirmMessage').textContent, /个人配置共用.*机器的账号登录/);
  picker('probe').click();
  await settle();
  assert.deepEqual(app.router.calls.find(c => c.path.endsWith('/models/probe')).body, {provider:'codex'});
  picker('probe-apply').click();
  assert.deepEqual(modelIDs(), ['codex/account-model']);
  app.document.querySelector('#confirmOkBtn').click();
  await settle();
  const saved = {id:'codex/account-model',name:'Account model'};
  assert.deepEqual(app.router.calls.find(c => c.method==='PUT').body, {codexApiKey:'',codexModels:[saved]});
  show('codex');
  assert.deepEqual(modelIDs(), [saved.id]);
  app.document.querySelector('#confirmCancelBtn').click();

  show('codebuddy');
  assert.match(app.document.querySelector('#confirmMessage').textContent, /个人配置共用.*API Key/);
  assert.deepEqual(modelIDs(), [model.id]);
  picker('probe').click();
  await settle();
  assert.match(app.document.querySelector('.runner-model-editor').textContent, /来自官方服务/);
  assert.deepEqual(app.router.calls.find(c => c.path.endsWith('/cli-models/probe')).body, {engine:'codebuddy',apiKey:''});
  app.document.querySelector('#confirmCancelBtn').click();
});

test('CLI update runs the Runner action for native updates and guides unsupported installations locally', async t => {
  const app = await loadApp();
  t.after(app.close);
  renderCodeBuddyRow(app, {});
  const update = () => {
    app.document.querySelector('[data-runner-runtime-menu="runner-cb|codex"]').click();
    [...app.document.querySelectorAll('.runner-runtime-menu button')].find(b => b.textContent.trim()==='更新').click();
  };
  update();
  assert.match(app.document.querySelector('#confirmMessage').textContent,/原来的安装方式/);
  assert.equal(app.router.calls.filter(c => c.path.endsWith('/maintenance')).length,0);
  app.document.querySelector('#confirmOkBtn').click();
  app.evaluate(`state.runners[0].runtimes[1].capabilities=['native_update'];renderRunnersView();followRunnerUpdate=async()=>{}`);
  app.router.post('/api/runners/runner-cb/maintenance', {status:'updating'});
  update();
  await settle();
  const request=app.router.calls.find(c=>c.path.endsWith('/maintenance'));
  assert.equal(request.method,'POST');
  assert.equal(request.search,'?provider=codex');
});
