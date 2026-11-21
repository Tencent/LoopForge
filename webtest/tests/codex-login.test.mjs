import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

function openDialog(app, user = {}) {
  app.evaluate(`
    state.currentUser={...state.data.users[0],id:1,isGuest:false,...${JSON.stringify(user)}};
    state.data.users=[state.currentUser];
    window.codexTimers=[];window.setTimeout=(callback,delay)=>{window.codexTimers.push({callback,delay});return window.codexTimers.length};
    openCodexChatGPTLogin();
  `);
  return app.document.querySelector('#codexLoginDialog');
}

test('Codex ChatGPT device login shows the code, polls, and stores only the public user', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.post('/api/auth/codex/device', { flowId: 'opaque-flow', userCode: 'WXYZ-1234', verificationUri: 'https://auth.openai.com/codex/device', expiresIn: 900, interval: 5 });
  let polls = 0;
  app.router.post('/api/auth/codex/poll', () => (++polls === 1
    ? { status: 'pending', interval: 7 }
    : { status: 'connected', email: 'dev@example.com', user: { id: 1, loginName: 'owner', codexChatGptLoginConfigured: true, codexChatGptLoginStatus: '' } }));
  const dialog = openDialog(app);
  assert.match(dialog.querySelector('[data-codex-status]').textContent, /尚未登录 ChatGPT/);
  assert.equal(dialog.querySelector('[data-codex-signout]').hidden, true);

  dialog.querySelector('[data-codex-device]').click();
  await settle(5);
  assert.match(dialog.textContent, /WXYZ-1234/);
  assert.equal(dialog.querySelector('a').href, 'https://auth.openai.com/codex/device');
  assert.equal(dialog.querySelector('[data-codex-actions]').hidden, true);
  assert.equal(app.window.codexTimers.at(-1).delay, 5000);

  await app.window.codexTimers.at(-1).callback();
  assert.equal(app.window.codexTimers.at(-1).delay, 7000);
  await app.window.codexTimers.at(-1).callback();
  await settle(3);
  assert.match(dialog.querySelector('[data-codex-status]').textContent, /已登录 ChatGPT · dev@example.com/);
  assert.equal(dialog.querySelector('[data-codex-signout]').hidden, false);
  assert.equal(dialog.querySelector('[data-codex-device-panel]').hidden, true);
  assert.equal(app.evaluate('userById(1).codexChatGptLoginConfigured'), true);
  assert.equal(app.evaluate('userById(1).codexChatGptLoginEmail'), undefined);
  const calls = app.router.calls.filter(call => call.path === '/api/auth/codex/poll');
  assert.deepEqual(calls.map(call => call.body), [{ flowId: 'opaque-flow' }, { flowId: 'opaque-flow' }]);
});

test('cancelling the Codex device login stops polling and discards the server session', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.post('/api/auth/codex/device', { flowId: 'opaque-flow', userCode: 'WXYZ-1234', verificationUri: 'https://auth.openai.com/codex/device', expiresIn: 900, interval: 5 });
  app.router.delete('/api/auth/codex/device', { status: 'cancelled' });
  const dialog = openDialog(app);
  dialog.querySelector('[data-codex-device]').click();
  await settle(5);
  dialog.querySelector('[data-codex-device-cancel]').click();
  await settle(3);
  assert.equal(dialog.querySelector('[data-codex-actions]').hidden, false);
  assert.equal(dialog.querySelector('[data-codex-device-panel]').hidden, true);
  assert.ok(app.router.calls.some(call => call.method === 'DELETE' && call.path === '/api/auth/codex/device'));
  await app.window.codexTimers.at(-1).callback();
  assert.equal(app.router.calls.filter(call => call.path === '/api/auth/codex/poll').length, 0);
});

test('pasting auth.json saves the login and signing out clears it', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.put('/api/auth/codex', { status: 'connected', email: 'dev@example.com', user: { id: 1, codexChatGptLoginConfigured: true } });
  app.router.delete('/api/auth/codex', { status: 'signed_out', user: { id: 1, codexChatGptLoginConfigured: false } });
  const dialog = openDialog(app);
  dialog.querySelector('[data-codex-paste]').click();
  const form = dialog.querySelector('[data-codex-paste-form]');
  assert.equal(form.hidden, false);
  form.elements.authJson.value = '{"tokens":{"refresh_token":"r1"}}';
  form.requestSubmit();
  await settle(5);
  const put = app.router.calls.find(call => call.method === 'PUT' && call.path === '/api/auth/codex');
  assert.deepEqual(put.body, { authJson: '{"tokens":{"refresh_token":"r1"}}' });
  assert.equal(form.hidden, true);
  assert.equal(form.elements.authJson.value, '');
  assert.match(dialog.querySelector('[data-codex-status]').textContent, /dev@example.com/);

  dialog.querySelector('[data-codex-signout]').click();
  await settle(5);
  assert.match(dialog.querySelector('[data-codex-status]').textContent, /已退出 ChatGPT 登录/);
  assert.equal(dialog.querySelector('[data-codex-signout]').hidden, true);
  assert.equal(app.evaluate('userById(1).codexChatGptLoginConfigured'), false);
});

test('an expired login is flagged and the account email comes from the owner-only status endpoint', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/auth/codex', { configured: true, email: 'dev@example.com', loginStatus: 'needs_relogin' });
  const dialog = openDialog(app, { codexChatGptLoginConfigured: true, codexChatGptLoginStatus: 'needs_relogin' });
  await settle(5);
  const status = dialog.querySelector('[data-codex-status]');
  assert.match(status.textContent, /登录已失效，请重新登录 · dev@example.com/);
  assert.equal(status.classList.contains('is-error'), true);
  assert.equal(dialog.querySelector('[data-codex-device]').textContent, '重新登录');
  dialog.querySelector('[data-codex-close]').click();
  assert.equal(app.document.querySelector('#codexLoginDialog'), null);
});
