import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('Git HTTPS credentials are managed per host with write-only tokens and verification', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const endpoint = '/api/users/1/git-http-credentials';
  let creds = [
    { id: 'gh', name: 'GitHub', platform: 'github', host: 'github.com', username: 'x-access-token' },
    { id: 'managed-internal', name: '内部托管凭据', platform: 'gitlab', host: 'git.internal.example', username: 'oauth2', managed: true },
  ];
  app.router.get(endpoint, () => creds);
  app.router.put(endpoint, ({ body }) => {
    const cred = { id: body.id || 'corp', name: body.name, platform: body.platform, host: body.host, username: body.username };
    creds = [...creds.filter(item => item.id !== cred.id), cred];
    return creds;
  });
  app.router.delete(`${endpoint}/corp`, () => {
    creds = creds.filter(item => item.id !== 'corp');
    return creds;
  });
  app.router.post(`${endpoint}/gh/verify`, { valid: true, login: 'octo' });
  app.evaluate(`
    state.currentUser={...state.data.users[0],id:1,isGuest:false,gitHttpCredentialsConfigured:true};
    state.data.users=[state.currentUser];
    renderPersonalSettings();
    openCredentialWizard();
  `);
  const wizard = app.document.querySelector('#credentialWizard');
  const option = wizard.querySelector('[data-wizard-kind="gitHttpCredentials"]');
  assert.equal(option.disabled, false, 'more hosts can be added after the first credential');
  option.click();
  wizard.querySelector('[data-wizard-next]').click();
  await settle(6);

  const dialog = app.document.querySelector('#gitHTTPCredentialDialog');
  const form = dialog.querySelector('form');
  assert.equal(form.elements.exposeToWorkspace, undefined, 'workspace credentials are always available');
  assert.match(dialog.textContent, /Git 认证用户名（选填）/);
  assert.equal(dialog.querySelectorAll('.git-ssh-key-row').length, 2);
  assert.equal(dialog.querySelector('[data-http-cred-edit="managed-internal"]'), null, 'the managed credential is read-only');
  assert.equal(dialog.querySelector('[data-http-cred-delete="managed-internal"]'), null);

  dialog.querySelector('[data-http-cred-verify="gh"]').click();
  await settle(6);
  assert.match(dialog.querySelector('.git-ssh-status').textContent, /octo/);

  dialog.querySelector('[data-http-cred-add]').click();
  assert.equal(form.elements.exposeToWorkspace, undefined);
  form.elements.host.value = 'https://gitlab.corp.example/';
  form.elements.host.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  assert.equal(form.elements.platform.value, 'gitlab');
  assert.equal(form.elements.name.value, '');
  assert.equal(form.elements.name.placeholder, 'gitlab.corp.example');
  form.elements.token.value = 'glpat-test-only';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(6);
  const saves = () => app.router.calls.filter(call => call.method === 'PUT' && call.path === endpoint);
  assert.equal(app.evaluate('credentialUser().gitHubCredentialConfigured'), true, 'saving the host list refreshes the GitHub authorization row');
  assert.deepEqual(saves()[0].body, {
    host: 'https://gitlab.corp.example/', platform: 'gitlab', name: 'gitlab.corp.example',
    username: 'oauth2', token: 'glpat-test-only',
  });
  assert.equal(form.hidden, true);
  assert.equal(form.elements.token.value, '', 'saving clears the token input');
  assert.doesNotMatch(dialog.querySelector('.git-ssh-key-list').textContent, /仅控制面/);

  dialog.querySelector('[data-http-cred-edit="gh"]').click();
  assert.equal(form.elements.token.value, '', 'editing never reads back the stored token');
  assert.equal(form.elements.token.required, false);
  assert.equal(form.elements.host.readOnly, true);
  form.elements.name.value = 'GitHub personal';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(6);
  assert.deepEqual(saves()[1].body, {
    id: 'gh', host: 'github.com', platform: 'github', name: 'GitHub personal',
    username: 'x-access-token', token: '',
  });

  dialog.querySelector('[data-http-cred-delete="corp"]').click();
  assert.equal(app.router.calls.some(call => call.method === 'DELETE'), false);
  dialog.querySelector('[data-http-cred-remove="corp"]').click();
  await settle(6);
  assert.equal(dialog.querySelectorAll('.git-ssh-key-row').length, 2);
  dialog.querySelector('[data-http-cred-close]').click();
  assert.equal(app.document.querySelector('#gitHTTPCredentialDialog'), null);
  assert.equal(app.evaluate('state.currentUser.gitHttpCredentialsConfigured'), true);
});

test('an authorized IPv6 HTTPS instance needs only a personal token', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const endpoint = '/api/users/1/git-http-credentials';
  const instance = { host: '[fd00::2]:8443', platform: 'gitlab' };
  assert.equal(app.evaluate("LoopForgeGitHTTPSHost('FD00::0443')"), '[fd00::443]');
  assert.equal(app.evaluate("LoopForgeGitHTTPRepository('https://[FD00:0::2]:8443/team/app.git').host"), instance.host);
  assert.equal(app.evaluate("LoopForgeGitHTTPRepository('https://[::ffff:10.0.0.1]/team/app.git')"), null);
  app.router.get('/api/git-https-hosts', [instance]);
  app.router.get(endpoint, []);
  app.router.put(endpoint, [{ ...instance, id: 'instance-token', name: instance.host }]);
  app.evaluate(`
    state.currentUser={...state.data.users[0],id:1,isGuest:false};
    openGitHTTPCredentials({add:true});
  `);
  await settle(6);

  const dialog = app.document.querySelector('#gitHTTPCredentialDialog');
  const form = dialog.querySelector('form');
  const submit = form.querySelector('button[type="submit"]');
  assert.equal(submit.disabled, true);
  form.elements.instance.value = instance.host;
  form.elements.instance.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  for (const key of ['host', 'platform', 'name', 'username']) {
    assert.equal(form.elements[key].closest('.credential-field').hidden, true);
  }
  assert.equal(submit.disabled, false);
  form.elements.token.value = 'test-only-token';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(6);
  const save = app.router.calls.find(call => call.method === 'PUT' && call.path === endpoint);
  assert.deepEqual(save.body, { ...instance, name: instance.host, token: 'test-only-token' });
  assert.equal(form.hidden, true);
  assert.equal(form.elements.token.value, '');
});

test('generic HTTP credentials select the authorized endpoint and check a repository URL', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const endpoint = '/api/users/1/git-http-credentials';
  const instance = {host:'http://192.0.2.13:8080', platform:'other'};
  const remote = instance.host + '/team/app.git';
  assert.equal(app.evaluate(`LoopForgeGitHTTPRepository(${JSON.stringify(remote)}).host`), instance.host);
  assert.equal(app.evaluate("parseGongfengRepoURL('http://github.com/team/app.git').provider"), 'github');
  assert.equal(app.evaluate("parseGongfengRepoURL('git clone http://github.com/team/app.git').provider"), 'http');
  assert.equal(app.evaluate("LoopForgeGitHTTPRepository('http://0x7f000001/team/app.git')"), null);
  app.router.get('/api/git-http-hosts', [instance]);
  app.router.get(endpoint, []);
  app.router.put(endpoint, [{...instance, id:'plain-token', name:'HTTP instance', username:'git-user'}]);
  app.router.post(`${endpoint}/plain-token/verify`, {valid:false, repositoryReadable:true});
  app.evaluate(`
    state.currentUser={...state.data.users[0],id:1,isGuest:false};
    openGitHTTPCredentials({add:true});
  `);
  await settle(6);
  const dialog = app.document.querySelector('#gitHTTPCredentialDialog');
  const form = dialog.querySelector('form');
  form.elements.instance.value = instance.host;
  form.elements.instance.dispatchEvent(new app.window.Event('change', {bubbles:true}));
  assert.equal(form.elements.username.closest('.credential-field').hidden, false);
  form.elements.username.value = 'git-user';
  form.elements.token.value = 'fixture-token';
  form.dispatchEvent(new app.window.Event('submit', {bubbles:true, cancelable:true}));
  await settle(6);
  assert.deepEqual(app.router.calls.find(call => call.method === 'PUT' && call.path === endpoint).body,
    {...instance, name:instance.host, username:'git-user', token:'fixture-token'});
  const row = app.evaluate(`credentialGitRowsHTML([${JSON.stringify({...instance,id:'plain-token',name:'HTTP instance'})}], [], false)`);
  assert.match(row, / · http:\/\/192\.0\.2\.13:8080/);

  const verify = dialog.querySelector('[data-http-cred-verify="plain-token"]');
  const repoCheck = dialog.querySelector('[data-http-repo-check]');
  const repoUrl = repoCheck.elements.repoUrl;
  verify.click();
  assert.equal(repoCheck.hidden, false);
  assert.equal(app.document.activeElement, repoUrl, 'the native credential dialog owns the focused URL field');
  repoCheck.querySelector('[data-http-repo-check-cancel]').click();
  assert.equal(repoCheck.hidden, true);
  assert.equal(app.document.activeElement, verify);
  assert.equal(app.router.calls.some(call => call.path === `${endpoint}/plain-token/verify`), false);

  verify.click();
  repoUrl.value = remote;
  repoCheck.querySelector('button[type="submit"]').click();
  await settle(6);
  assert.equal(dialog.open, true);
  assert.equal(repoCheck.hidden, true);
  assert.deepEqual(app.router.calls.find(call => call.path === `${endpoint}/plain-token/verify`).body, {repoUrl:remote});
  assert.match(dialog.querySelector('.git-ssh-status').textContent, /仓库可读/);
});
