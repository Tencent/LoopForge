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
    assert.equal(el.textContent, 'API Key');
    assert.equal(el.classList.contains('missing'), false);
  }
});

test('runner card offers the short authentication labels with a separate explanation', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  renderCodeBuddyRow(app, {});
  const select = app.document.querySelector('[data-runner-auth="runner-cb|codex"]');
  assert.deepEqual([...select.options].map(option=>option.textContent), ['账号登录', 'API Key']);
  app.document.querySelector('[data-runner-auth-help]').click();
  assert.match(app.document.querySelector('#confirmMessage').textContent, /系统账号已登录的 CLI/);
  assert.match(app.document.querySelector('#confirmMessage').textContent, /当前发言人的 Key/);
});

test('runner card indicates a missing CodeBuddy key on its single-line label', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const el = renderCodeBuddyRow(app, { codeBuddyApiKeyConfigured: false, codeBuddyInternetEnvironment: 'ioa' });
  assert.equal(el.textContent, 'API Key');
  assert.match(el.getAttribute('title'), /个人配置/);
  assert.ok(el.classList.contains('missing'));
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
  app.router.get('/api/runners/runner-cb/maintenance', {status: 'available', current: '0.10.3', canUpdate: true});
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

test('CLI model menu uses the catalog matching the row authentication', async t => {
  const app = await loadApp();
  t.after(app.close);
  renderCodeBuddyRow(app, { codeBuddyApiKeyConfigured: true });
  app.router.get('/api/executor-models', call => call.search === '?builtin=1'
    ? { codex: { models: [{id:'built-in-codex',name:'Built-in Codex'}] } }
    : { codebuddy: {source:'personal',models:[{id:'gateway/model',name:'Personal model'}]} });
  const show = provider => {
    app.document.querySelector(`[data-runner-runtime-menu="runner-cb|${provider}"]`).click();
    [...app.document.querySelectorAll('.runner-runtime-menu button')].find(b => b.textContent.trim()==='模型列表').click();
  };
  show('codex');
  await settle();
  assert.match(app.document.querySelector('.runner-model-list').textContent, /built-in-codex/);
  assert.equal(app.document.querySelector('#confirmMessage').textContent, 'CLI 内置模型目录');
  app.document.querySelector('#confirmOkBtn').click();
  show('codebuddy');
  await settle();
  assert.match(app.document.querySelector('.runner-model-list').textContent, /Personal modelgateway\/model/);
  assert.equal(app.document.querySelector('#confirmMessage').textContent, '个人配置的模型目录');
  app.document.querySelector('#confirmOkBtn').click();
});

test('CLI update is available for managed installations and guides others locally', async t => {
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
  app.evaluate(`state.runners[0].runtimes[1].capabilities=['managed_update'];renderRunnersView();followRunnerUpdate=async()=>{}`);
  app.router.post('/api/runners/runner-cb/maintenance', {status:'updating'});
  update();
  await settle();
  const request=app.router.calls.find(c=>c.path.endsWith('/maintenance'));
  assert.equal(request.method,'POST');
  assert.equal(request.search,'?provider=codex');
});
