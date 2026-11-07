import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('platform admin panel loads metrics, searches, filters, grants, revokes, and navigates', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.currentUser={...state.data.users[0],isGuest:false,isPlatformAdmin:true}`);
  const initial = [
    { userId: 1, loginName: 'example-maintainer', chineseName: '', source: 'builtin', revocable: false },
    { userId: 2, loginName: 'reviewer', chineseName: '审阅者', source: 'granted', revocable: true },
  ];
  app.router.get('/api/platform/admins', initial);
  app.router.put('/api/platform/admins', (call) => call.body.role === 'admin'
    ? [...initial, { loginName: call.body.loginName, source: 'granted', revocable: true }]
    : initial.filter((row) => row.loginName !== call.body.loginName));
  app.evaluate(`renderPlatformAdminPanel()`);
  await settle(8);
  const host = app.document.querySelector('#platformView');
  assert.match(host.textContent, /示例用户|审阅者|2 位管理员/);
  const search = host.querySelector('#platformAdminSearch');
  search.value = '不存在';
  search.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.match(host.textContent, /未找到/);
  search.value = '';
  search.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  host.querySelector('[data-source-filter="builtin"]')?.click();
  host.querySelector('[data-source-filter="granted"]')?.click();
  host.querySelector('[data-source-filter="all"]')?.click();

  const form = host.querySelector('#platformAdminForm');
  form.elements.loginName.value = 'newadmin';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(6);
  assert.match(host.textContent, /newadmin/);

  host.querySelector('[data-revoke-admin="reviewer"]')?.click();
  await settle();
  app.document.querySelector('#confirmOkBtn').click();
  await settle(6);
  assert.ok(app.router.calls.filter((call) => call.path === '/api/platform/admins' && call.method === 'PUT').length >= 2);
  host.querySelector('[data-platform-nav="agents"]')?.click();
  assert.equal(app.evaluate(`state.currentView`), 'agents');
});

test('platform admins create accounts and reset passwords from the page', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.currentUser={...state.data.users[0],isGuest:false,isPlatformAdmin:true}`);
  const accounts = [
    { userId: 1, loginName: 'owner', admin: true, password: true, codeBuddyApiKeyConfigured: true },
    { userId: 2, loginName: 'octocat', admin: false, password: false, codeBuddyApiKeyConfigured: false },
  ];
  app.router.get('/api/platform/admins', []);
  app.router.get('/api/platform/accounts', { accounts, signupEnabled: false });
  app.router.post('/api/platform/accounts', (call) => ({ status: 201, body: { userId: 3, loginName: call.body.loginName, admin: call.body.admin, password: true, codeBuddyApiKeyConfigured: false } }));
  app.router.put('/api/platform/accounts/3/password', { userId: 3, loginName: 'alice', password: true });
  app.evaluate(`renderPlatformAdminPanel()`);
  await settle(8);
  const card = app.document.querySelector('#platformAccountsCard');
  assert.match(card.textContent, /自助注册已关闭/);
  assert.match(card.textContent, /共 2 个账号/);
  assert.ok(card.querySelector('[data-reset-account="1"]'), 'password accounts can be reset');
  assert.ok(!card.querySelector('[data-reset-account="2"]'), 'GitHub accounts have no platform password');
  assert.match(card.querySelector('[data-account-row="2"]').textContent, /无平台密码/);

  const form = card.querySelector('#platformAccountForm');
  form.elements.loginName.value = 'x';
  form.elements.password.value = 'secret-123';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(4);
  assert.equal(app.router.calls.filter((call) => call.method === 'POST').length, 0, 'an invalid login is rejected before sending');

  form.elements.loginName.value = 'alice';
  card.querySelector('[data-account-generate]').click();
  const generated = form.elements.password.value;
  assert.match(generated, /^[A-Za-z0-9]{14}$/);
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(6);
  const created = app.router.calls.find((call) => call.method === 'POST' && call.path === '/api/platform/accounts');
  assert.deepEqual(created.body, { loginName: 'alice', password: generated, admin: false });
  assert.match(card.textContent, /共 3 个账号/);
  const note = card.querySelector('[data-account-created]');
  assert.equal(note.hidden, false);
  assert.match(note.textContent, /已创建账号 alice/);
  assert.equal(note.querySelector('code').textContent, generated);
  assert.equal(form.elements.loginName.value, '', 'the form clears after creating');

  card.querySelector('[data-reset-account="3"]').click();
  const reset = card.querySelector('[data-reset-form="3"]');
  reset.elements.password.value = 'second-pass';
  reset.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(6);
  const resetCall = app.router.calls.find((call) => call.path === '/api/platform/accounts/3/password');
  assert.deepEqual(resetCall.body, { password: 'second-pass' });
  assert.match(note.textContent, /已重置账号 alice/);
  assert.ok(!card.querySelector('[data-reset-form]'), 'the inline reset closes');

  const search = card.querySelector('[data-account-search]');
  search.value = 'octo';
  search.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(card.querySelectorAll('[data-account-row]').length, 1);
});

test('platform admins share a CodeBuddy key limited to the models they pick', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.currentUser={...state.data.users[0],isGuest:false,isPlatformAdmin:true}`);
  const catalogs = {
    '': [{ id: 'default', name: 'CLI 默认模型' }, { id: 'glm-5.1', name: 'GLM-5.1', credits: '0.2x' }, { id: 'kimi-k2.6', name: 'Kimi-K2.6' }],
    internal: [{ id: 'default', name: 'CLI 默认模型' }, { id: 'deepseek-v4.1-flash', name: 'Deepseek-V4.1-Flash' }, { id: 'glm-5.1', name: 'GLM-5.1' }],
  };
  let saved = { configured: false, internetEnvironment: '', models: [], legacySponsor: 'sponsor', legacySponsorModel: 'deepseek-v4.1-flash' };
  app.router.get('/api/platform/admins', []);
  app.router.get('/api/platform/accounts', { accounts: [], signupEnabled: true });
  app.router.get(/^\/api\/platform\/codebuddy-key/, (call) => {
    const env = call.url.searchParams.has('environment') ? call.url.searchParams.get('environment') : saved.internetEnvironment;
    return { ...saved, catalog: catalogs[env] || [] };
  });
  app.router.put('/api/platform/codebuddy-key', (call) => {
    saved = { ...saved, configured: true, internetEnvironment: call.body.internetEnvironment, models: call.body.models, updatedBy: 'owner' };
    return saved;
  });
  app.router.delete('/api/platform/codebuddy-key', () => {
    saved = { ...saved, configured: false, models: [] };
    return saved;
  });
  app.evaluate(`renderPlatformAdminPanel()`);
  await settle(8);
  const card = app.document.querySelector('#platformSharedKeyCard');
  const form = card.querySelector('#platformSharedKeyForm');
  assert.match(card.querySelector('[data-shared-key-badge]').textContent, /使用旧配置/);
  assert.match(card.querySelector('[data-shared-key-meta]').textContent, /AGENTFLOW_ASSISTANT_SPONSOR_LOGIN.*sponsor/);
  assert.ok(!card.querySelector('[data-shared-models] input[value="default"]'), 'the CLI default is not offered');
  assert.ok(card.querySelector('[data-shared-models] input[value="deepseek-v4.1-flash"]').checked, 'the legacy sponsor model starts selected');
  assert.ok(card.querySelector('[data-shared-key-clear]').hidden);

  form.elements.internetEnvironment.value = 'internal';
  form.elements.internetEnvironment.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await settle(6);
  assert.equal(card.querySelector('[data-shared-key-link]').getAttribute('href'), 'https://copilot.tencent.com/profile/');
  const box = (id) => card.querySelector(`[data-shared-models] input[value="${id}"]`);
  box('glm-5.1').checked = true;
  box('glm-5.1').dispatchEvent(new app.window.Event('change', { bubbles: true }));
  form.elements.extraModels.value = 'custom-model';
  form.elements.extraModels.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.deepEqual([...form.elements.defaultModel.options].map((o) => o.value), ['deepseek-v4.1-flash', 'glm-5.1', 'custom-model']);
  form.elements.defaultModel.value = 'glm-5.1';

  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(4);
  assert.equal(app.router.calls.filter((call) => call.method === 'PUT').length, 0, 'a first save needs the key');

  form.elements.apiKey.value = 'sk-third-party';
  form.elements.apiKey.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(card.querySelector('[data-shared-key-warning]').hidden, false);
  form.elements.apiKey.value = 'ck_shared';
  form.elements.apiKey.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(card.querySelector('[data-shared-key-warning]').hidden, true);
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(6);
  const put = app.router.calls.find((call) => call.method === 'PUT' && call.path === '/api/platform/codebuddy-key');
  assert.deepEqual(put.body, { apiKey: 'ck_shared', internetEnvironment: 'internal', models: ['glm-5.1', 'deepseek-v4.1-flash', 'custom-model'] });
  assert.equal(form.elements.apiKey.value, '', 'the key is not kept in the page');
  assert.equal(form.elements.apiKey.placeholder, '已配置，留空保持不变');
  assert.match(card.querySelector('[data-shared-key-badge]').textContent, /已启用/);
  assert.match(card.querySelector('[data-shared-key-meta]').textContent, /国内站，3 个模型，默认 glm-5.1/);
  assert.deepEqual(app.evaluate(`state.data.sponsoredModels`), ['glm-5.1', 'deepseek-v4.1-flash', 'custom-model']);
  assert.equal(app.evaluate(`sharedModelSummary()`), 'glm-5.1、Deepseek-V4.1-Flash、custom-model');

  const clear = card.querySelector('[data-shared-key-clear]');
  assert.equal(clear.hidden, false);
  clear.click();
  await settle();
  app.document.querySelector('#confirmOkBtn').click();
  await settle(6);
  assert.ok(app.router.calls.some((call) => call.method === 'DELETE' && call.path === '/api/platform/codebuddy-key'));
  assert.match(card.querySelector('[data-shared-key-badge]').textContent, /使用旧配置/);
  assert.equal(app.evaluate(`state.data.sponsoredModel`), 'deepseek-v4.1-flash');
});
