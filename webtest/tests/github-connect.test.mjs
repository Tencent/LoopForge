import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('GitHub connection displays a PIN, honors slow polling, and stores the connected public user', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/auth/github', { enabled: true });
  app.router.post('/api/auth/github/device', { flowId: 'opaque-flow', userCode: 'ABCD-EFGH', expiresIn: 900, interval: 5 });
  app.router.post('/api/auth/github/poll', { status: 'connected', login: 'my-github-account', user: { id: 1, loginName: 'owner', gitTokenConfigured: true } });
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    const host=document.createElement('div');host.id='github-test';host.innerHTML=githubConnectHTML();document.body.append(host);
    window.githubTimers=[];window.setTimeout=(callback,delay)=>{window.githubTimers.push({callback,delay});return window.githubTimers.length};
  `);
  const host = app.document.querySelector('#github-test');
  await app.evaluate('hydrateGitHubConnections(document)');
  host.querySelector('[data-github-start]').click();
  await settle(5);
  assert.match(host.textContent, /ABCD-EFGH/);
  assert.equal(host.querySelector('a').href, 'https://github.com/login/device');
  assert.equal(app.router.calls.filter(call => call.path === '/api/auth/github/poll').length, 0);
  assert.equal(app.window.githubTimers.at(-1).delay, 5000);
  await app.window.githubTimers.at(-1).callback();
  assert.match(host.textContent, /已连接 GitHub · my-github-account/);
  assert.equal(app.evaluate('userById(1).gitTokenConfigured'), true);
  assert.equal(host.querySelector('[data-github-start]').disabled, false);
  const call = app.router.calls.find(call => call.path === '/api/auth/github/poll');
  assert.deepEqual(call.body, { flowId: 'opaque-flow' });
});

async function openDeviceCode(t, { secure, execResult = true }) {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/auth/github', { enabled: true });
  app.router.post('/api/auth/github/device', { flowId: 'opaque-flow', userCode: 'ABCD-EFGH', expiresIn: 900, interval: 5 });
  Object.defineProperty(app.window, 'isSecureContext', { configurable: true, value: secure });
  const clipboardWrites = [], execCalls = [];
  Object.defineProperty(app.window.navigator, 'clipboard', { configurable: true, value: { writeText: async (value) => { clipboardWrites.push(value); } } });
  app.document.execCommand = (command) => {
    const active = app.document.activeElement;
    execCalls.push({ command, value: active?.value, selected: active?.value?.slice(active.selectionStart, active.selectionEnd), inStatus: !!active?.closest?.('[data-github-status]') });
    return execResult;
  };
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    const host=document.createElement('div');host.id='github-copy-test';host.innerHTML=githubConnectHTML();document.body.append(host);
    window.setTimeout=()=>0;
  `);
  const host = app.document.querySelector('#github-copy-test');
  await app.evaluate('hydrateGitHubConnections(document)');
  host.querySelector('[data-github-start]').click();
  await settle(5);
  const copy = [...host.querySelectorAll('button')].find((button) => button.textContent === '复制授权码');
  const link = host.querySelector('a[href="https://github.com/login/device"]');
  link.addEventListener('click', (event) => event.preventDefault());
  return { app, host, copy, link, clipboardWrites, execCalls };
}

test('GitHub device code copies over plain HTTP where the Clipboard API is unavailable', async (t) => {
  const { app, host, copy, link, clipboardWrites, execCalls } = await openDeviceCode(t, { secure: false });
  copy.click();
  await settle(3);
  assert.equal(copy.textContent, '已复制');
  assert.deepEqual(execCalls, [{ command: 'copy', value: 'ABCD-EFGH', selected: 'ABCD-EFGH', inStatus: true }]);
  assert.deepEqual(clipboardWrites, []);
  assert.equal(host.querySelector('textarea'), null);
  assert.equal(app.document.activeElement, copy);
  copy.textContent = '复制授权码';
  link.click();
  await settle(3);
  assert.equal(execCalls.length, 2);
  assert.equal(execCalls[1].value, 'ABCD-EFGH');
  assert.equal(copy.textContent, '已复制');
});

test('GitHub device code is selected for manual copy when the browser refuses to copy', async (t) => {
  const { app, copy, execCalls } = await openDeviceCode(t, { secure: false, execResult: false });
  copy.click();
  await settle(3);
  assert.equal(execCalls.length, 1);
  assert.match(copy.textContent, /已选中/);
  assert.equal(app.window.getSelection().toString(), 'ABCD-EFGH');
});

test('GitHub device code uses the Clipboard API in secure contexts', async (t) => {
  const { copy, clipboardWrites, execCalls } = await openDeviceCode(t, { secure: true });
  copy.click();
  await settle(3);
  assert.equal(copy.textContent, '已复制');
  assert.deepEqual(clipboardWrites, ['ABCD-EFGH']);
  assert.deepEqual(execCalls, []);
});

test('GitHub repository picker loads pages, filters names, and fills the actual default branch', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/auth/github/repositories?page=1', { repositories: [{ full_name: 'team/private', html_url: 'https://github.com/team/private', default_branch: 'develop', private: true }], hasMore: true });
  app.router.get('/api/auth/github/repositories?page=2', { repositories: [{ full_name: 'me/public', html_url: 'https://github.com/me/public', default_branch: 'trunk', private: false }], hasMore: false });
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    const host=document.createElement('div');host.id='github-picker-test';document.body.append(host);
    state.data.users=[{id:1,gitTokenConfigured:true}];
    mountGitHubRepoPicker(host,repository=>{window.githubSelected=repository});
  `);
  const host = app.document.querySelector('#github-picker-test');
  host.querySelector('[data-github-load]').click();
  await settle(5);
  assert.match(host.textContent, /team\/private · 私有/);
  host.querySelector('[data-github-more]').click();
  await settle(5);
  const search = host.querySelector('[data-github-search]');
  search.value = 'me/';
  search.dispatchEvent(new app.window.Event('input'));
  const select = host.querySelector('[data-github-repo]');
  assert.equal(select.options.length, 2);
  select.options[1].selected = true;
  select.dispatchEvent(new app.window.Event('change'));
  assert.equal(app.window.githubSelected.full_name, 'me/public');
  assert.equal(app.window.githubSelected.default_branch, 'trunk');
  assert.equal(host.querySelector('[data-github-more]'), null);
});

test('GitHub setup keeps deployment configuration out of personal authorization', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/auth/github', { enabled: true, canConfigure: true, clientId: 'configured-client' });
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    const host=document.createElement('div');host.id='github-error-test';host.innerHTML=githubConnectHTML();document.body.append(host);
  `);
  const host = app.document.querySelector('#github-error-test');
  await app.evaluate('hydrateGitHubConnections(document)');
  await settle(5);
  assert.doesNotMatch(host.textContent, /管理员|Client ID|configured-client/);
  assert.equal(host.querySelector('[data-github-client]'), null);
  assert.equal(host.querySelector('[data-github-start]').disabled, false);
  assert.equal(host.querySelector('[data-github-start]').hidden, false);
  assert.equal(app.router.calls.some(call=>call.path==='/api/auth/github/device'), false);
  assert.equal(app.router.calls.some(call=>call.path==='/api/auth/github/config'), false);
});

test('GitHub settings has a single row and separates authorization from manual secrets', async (t) => {
  const app=await loadApp();t.after(app.close);
  app.router.get('/api/auth/github',{enabled:false,canConfigure:false});
  app.evaluate(`state.currentUser={id:1,isGuest:false};state.data.users[0]={id:1};renderPersonalSettings();openCredentialWizard('gitToken')`);
  await settle(4);
  assert.equal(app.document.querySelector('#personalSettingsContent > .personal-settings-card'),null);
  const wizard=app.document.querySelector('#credentialWizard');
  assert.equal(wizard.querySelector('input[name="secret"]'),null);
  assert.match(wizard.textContent,/已停用/);
  assert.equal(wizard.querySelector('[data-github-start]').hidden,true);
  wizard.querySelector('[data-github-method="manual"]').click();
  assert.ok(wizard.querySelector('input[name="secret"]'));
  assert.equal(wizard.querySelector('[data-github-connect]'),null);
});
