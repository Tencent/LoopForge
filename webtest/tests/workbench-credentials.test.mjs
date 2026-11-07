import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

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

test('CodeBuddy wizard picks the network environment from a list and its key link follows the choice', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false};state.data.users[0]=state.currentUser`);
  app.router.put('/api/users/1/credentials', { id: 1, username: 'tester', codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: 'internal' });
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
  secret.value = 'ck_placeholder';
  secret.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(pane().querySelector('[data-credential-hint="secret"]').className, 'is-ok');
  next.click();
  await settle(3);
  assert.match(pane().textContent, /国内站 · copilot\.tencent\.com/);
  app.document.querySelector('#credentialWizard [data-wizard-next]').click();
  await settle(6);
  const saved = app.router.calls.find((call) => call.path === '/api/users/1/credentials');
  assert.deepEqual(saved.body, { codeBuddyApiKey: 'ck_placeholder', codeBuddyInternetEnvironment: 'internal' });
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

test('credential panel filters, expands, validates, reveals secrets, and confirms deletion', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.data.users[0].id=1;
    state.currentUser={...state.data.users[0],id:1,isGuest:false,gitTokenConfigured:true,codeBuddyApiKeyConfigured:true};
    state.data.users[0]=state.currentUser;
  `);
  app.router.get('/api/auth/gongfeng', { enabled: true });
  app.router.get('/api/users/1/git-token/verify', { valid: true, username: 'tester', scopes: ['repo'] });
  app.router.put('/api/users/1/credentials', {
    id: 1, name: 'Test User', username: 'tester', gitTokenConfigured: false, codeBuddyApiKeyConfigured: true,
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
  app.router.put('/api/users/1/credentials', {
    id: 1, username: 'tester', gitTokenConfigured: true,
  });
  const invalid = await app.evaluate(`verifyAndSaveGitToken('bad')`);
  const valid = await app.evaluate(`verifyAndSaveGitToken('valid-token')`);
  assert.equal(invalid.saved, false);
  assert.equal(valid.saved, true);
});
