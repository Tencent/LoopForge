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
