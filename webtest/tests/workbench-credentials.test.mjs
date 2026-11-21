import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('personal settings lists a managed Git credential even without personal Git records', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/users/1/git-http-credentials', [
    {id:'managed',name:'内部托管凭据',host:'git.example.test',platform:'gitlab',managed:true},
  ]);
  app.evaluate(`
    state.currentUser={...state.data.users[0],id:1,isGuest:false,gitHttpCredentialsConfigured:false,gitHubCredentialConfigured:false};
    state.data.users[0]=state.currentUser;
    renderPersonalSettings();
  `);
  await settle(4);
  const group = app.document.querySelector('[data-credential-group="platform"]');
  assert.match(group.querySelector('[data-credential-git-rows]').textContent, /内部托管凭据/);
  assert.equal(group.querySelector('[data-credential-git-id="managed"]').textContent.trim(), '查看');
  assert.equal(group.querySelector('.credential-group-note').hidden, false);
});

test('credential wizard selects a provider, validates input, saves, and finishes', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false};state.data.users[0]=state.currentUser`);
  app.router.get('/api/auth/gongfeng', { enabled: true });
  app.router.put('/api/users/1/credentials', {
    id: 1, name: 'Test User', username: 'tester', codeBuddyApiKeyConfigured: true,
  });
  await app.evaluate(`renderPersonalSettings()`);
  app.evaluate(`openCredentialWizard()`);
  const wizard = () => app.document.querySelector('#credentialWizard');
  wizard().querySelector('[data-wizard-kind="codeBuddyApiKey"]').click();
  wizard().querySelector('[data-wizard-next]').click();
  await settle(3);
  const secret = wizard().querySelector('input[name="secret"]');
  secret.value = 'placeholder-codebuddy-key';
  secret.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  wizard().querySelector('[data-wizard-next]').click();
  await settle(3);
  wizard().querySelector('[data-wizard-next]').click();
  await settle(6);
  assert.match(wizard().textContent, /已保存/);
  wizard().querySelector('[data-wizard-done]').click();
  assert.ok(app.router.calls.some((call) => call.path === '/api/users/1/credentials'));
});

test('credential wizard focuses the first field on its own but never pulls focus from a field the user picked', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false};state.data.users[0]=state.currentUser`);
  await app.evaluate(`renderPersonalSettings()`);
  app.evaluate(`openCredentialWizard()`);
  const wizard = app.document.querySelector('#credentialWizard');
  const pane = () => wizard.querySelector('.credential-pane:not(.is-leaving)');
  const afterFocusTimers = () => new Promise((resolve) => setTimeout(resolve, 300));
  wizard.querySelector('[data-wizard-kind="codexApiKey"]').click();
  wizard.querySelector('[data-wizard-next]').click();
  await afterFocusTimers();
  assert.equal(app.document.activeElement, pane().querySelector('input[name="secret"]'));

  wizard.querySelector('[data-wizard-back]').click();
  wizard.querySelector('[data-wizard-next]').click();
  const baseURL = pane().querySelector('input[name="extra"]');
  baseURL.focus();
  await afterFocusTimers();
  assert.equal(app.document.activeElement, baseURL);
  baseURL.value = 'not-a-url';
  baseURL.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.match(pane().querySelector('[data-credential-hint="extra"]').textContent, /http:\/\//);
  assert.equal(pane().querySelector('input[name="secret"]').value, '');
});

test('CodeBuddy wizard supports a custom Base URL alongside its network environment', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false};state.data.users[0]=state.currentUser`);
  const baseUrl = 'https://gateway.example/v1';
  app.router.put('/api/users/1/credentials', { id: 1, username: 'tester', codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: 'internal', codeBuddyBaseUrl: baseUrl });
  await app.evaluate(`renderPersonalSettings()`);
  app.evaluate(`openCredentialWizard('codeBuddyApiKey')`);
  const pane = () => app.document.querySelector('#credentialWizard .credential-pane:not(.is-leaving)');
  const select = pane().querySelector('select[name="extra"]');
  assert.ok(select, 'network environment is a list, not free text');
  assert.equal(pane().querySelector('input[name="extra"]'), null);
  assert.deepEqual([...select.options].map((option) => option.value), ['', 'internal', 'ioa']);
  const guideLink = () => pane().querySelector('[data-environment-link]');
  const hintLink = () => pane().querySelector('[data-credential-key-link]');
  assert.equal(guideLink().getAttribute('href'), 'https://www.codebuddy.ai/profile/keys');
  select.value = 'internal';
  select.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(guideLink().getAttribute('href'), 'https://copilot.tencent.com/profile/');
  assert.match(guideLink().textContent, /国内站/);
  assert.equal(hintLink().getAttribute('href'), 'https://copilot.tencent.com/profile/');

  const secret = pane().querySelector('input[name="secret"]');
  secret.value = 'sk-third-party';
  secret.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  const hint = pane().querySelector('[data-credential-hint="secret"]');
  assert.equal(hint.className, 'is-warn');
  assert.match(hint.textContent, /第三方/);
  const next = app.document.querySelector('#credentialWizard [data-wizard-next]');
  assert.equal(next.disabled, false, 'the warning never blocks saving');
  const endpoint = pane().querySelector('input[name="baseUrl"]');
  assert.equal(endpoint.value, '');
  endpoint.value = 'ftp://gateway.example';
  endpoint.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(next.disabled, true);
  endpoint.value = baseUrl;
  endpoint.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(next.disabled, false);
  assert.equal(pane().querySelector('[data-credential-hint="secret"]').className, 'is-ok');
  next.click();
  await settle(6);
  assert.equal(app.document.querySelector('#credentialWizard'), null);
  const saved = app.router.calls.find((call) => call.path === '/api/users/1/credentials');
  assert.deepEqual(saved.body, { codeBuddyApiKey: 'sk-third-party', codeBuddyInternetEnvironment: 'internal', codeBuddyBaseUrl: baseUrl });
});

test('CodeBuddy inline editor changes only the network environment and keeps the key link in step', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.data.users[0].id=1;
    state.currentUser={...state.data.users[0],id:1,isGuest:false,codeBuddyApiKeyConfigured:true,codeBuddyInternetEnvironment:'ioa'};
    state.data.users[0]=state.currentUser;
  `);
  app.router.put('/api/users/1/credentials', { id: 1, username: 'tester', codeBuddyApiKeyConfigured: true });
  await app.evaluate(`renderPersonalSettings()`);
  const row = app.document.querySelector('[data-credential-row="codeBuddyApiKey"]');
  assert.match(row.querySelector('.credential-row-who small').textContent, /网络环境iOA/);
  row.querySelector('.credential-row-main').click();
  const select = row.querySelector('select[name="extra"]');
  const save = row.querySelector('[data-credential-act="save"]');
  assert.equal(select.value, 'ioa');
  assert.equal(save.disabled, true);
  select.value = '';
  select.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(save.disabled, false);
  assert.equal(row.querySelector('[data-credential-key-link]').getAttribute('href'), 'https://www.codebuddy.ai/profile/keys');
  save.click();
  await settle(6);
  const saved = app.router.calls.find((call) => call.path === '/api/users/1/credentials');
  assert.deepEqual(saved.body, { codeBuddyApiKey: '', codeBuddyInternetEnvironment: '' });
});

test('CodeBuddy inline editor rewrites an old china value to internal without retyping the key', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.data.users[0].id=1;
    state.currentUser={...state.data.users[0],id:1,isGuest:false,codeBuddyApiKeyConfigured:true,codeBuddyInternetEnvironment:'china'};
    state.data.users[0]=state.currentUser;
  `);
  app.router.put('/api/users/1/credentials', { id: 1, username: 'tester', codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: 'internal' });
  await app.evaluate(`renderPersonalSettings()`);
  const row = () => app.document.querySelector('[data-credential-row="codeBuddyApiKey"]');
  assert.match(row().querySelector('.credential-row-who small').textContent, /「china」无法识别/);
  row().querySelector('.credential-row-main').click();
  const select = row().querySelector('select[name="extra"]');
  const save = row().querySelector('[data-credential-act="save"]');
  assert.equal(select.value, 'internal', 'the old value preselects what it meant');
  assert.equal(save.disabled, false, 'the stored text still needs rewriting, so saving is allowed as shown');
  assert.match(row().querySelector('[data-credential-env-hint]').textContent, /「china」无法识别/);
  save.click();
  await settle(6);
  const saved = app.router.calls.find((call) => call.path === '/api/users/1/credentials');
  assert.deepEqual(saved.body, { codeBuddyApiKey: '', codeBuddyInternetEnvironment: 'internal' });
  assert.match(row().querySelector('.credential-row-who small').textContent, /网络环境国内站/);
});

test('credential panel filters, expands, validates, reveals secrets, and confirms deletion', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.data.users[0].id=1;
    state.currentUser={...state.data.users[0],id:1,isGuest:false,gitHubCredentialConfigured:true,codeBuddyApiKeyConfigured:true};
    state.data.users[0]=state.currentUser;
  `);
  app.router.get('/api/auth/gongfeng', { enabled: true });
  app.router.get('/api/users/1/git-token/verify', { valid: true, username: 'tester', scopes: ['repo'] });
  app.router.put('/api/users/1/credentials', {
    id: 1, name: 'Test User', username: 'tester', gitHubCredentialConfigured: false, codeBuddyApiKeyConfigured: true,
  });
  await app.evaluate(`renderPersonalSettings()`);
  const panel = app.document.querySelector('#credentialPanel');
  panel.querySelector('[data-credential-filter="configured"]')?.click();
  panel.querySelector('[data-credential-filter="missing"]')?.click();
  panel.querySelector('[data-credential-filter="all"]')?.click();
  panel.querySelector('.credential-group-head')?.click();
  panel.querySelector('.credential-group-head')?.click();
  const row = panel.querySelector('[data-credential-row="gitToken"]');
  row.querySelector('.credential-row-main')?.click();
  row.querySelector('[data-credential-eye]')?.click();
  row.querySelector('[data-credential-act="delete"]')?.click();
  row.querySelector('[data-credential-act="undelete"]')?.click();
  await app.evaluate(`refreshGitTokenStatus({announce:true})`);
  app.evaluate(`revealCredentialRow('gitToken');repaintCredentialRow('gitToken');flashCredentialRow('gitToken')`);
  assert.match(panel.textContent, /工蜂|校验/);
});

test('Git token verification rejects invalid tokens before save and accepts valid ones', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false};state.data.users[0]=state.currentUser`);
  app.router.post('/api/users/1/git-token/verify', (call) =>
    call.body.token === 'valid-token'
      ? { valid: true, username: 'tester', scopes: ['repo'] }
      : { valid: false, error: 'invalid token' });
  app.router.get('/api/users/1/git-http-credentials', []);
  app.router.put('/api/users/1/git-http-credentials', [{id:'github',host:'github.com'}]);
  const invalid = await app.evaluate(`verifyAndSaveGitToken('bad')`);
  const valid = await app.evaluate(`verifyAndSaveGitToken('valid-token')`);
  assert.equal(invalid.saved, false);
  assert.equal(valid.saved, true);
});
