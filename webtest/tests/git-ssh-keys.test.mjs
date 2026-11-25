import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('configured Git SSH credentials accept additional keys and support write-only edits and confirmed deletion', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const endpoint = '/api/users/1/git-ssh-keys';
  let keys = [{ id: 'gitlab', name: 'GitLab', host: 'gitlab.example.org:22' }];
  app.router.get(endpoint, () => keys);
  app.router.put(endpoint, ({ body }) => {
    const key = { id: body.id || 'other-host', name: body.name, host: body.host };
    keys = [...keys.filter(item => item.id !== key.id), key];
    return keys;
  });
  app.router.delete(`${endpoint}/other-host`, () => {
    keys = keys.filter(item => item.id !== 'other-host');
    return keys;
  });
  app.evaluate(`
    state.currentUser={...state.data.users[0],id:1,isGuest:false,gitSshKeyConfigured:true,codeBuddyApiKeyConfigured:true};
    state.data.users=[state.currentUser];
    renderPersonalSettings();
    openCredentialWizard();
  `);
  const wizard = app.document.querySelector('#credentialWizard');
  assert.equal(wizard.querySelector('[data-wizard-kind="codeBuddyApiKey"]').disabled, true);
  const sshOption = wizard.querySelector('[data-wizard-kind="gitSshKey"]');
  assert.equal(sshOption.disabled, false, 'SSH supports adding more keys after the first one');
  sshOption.click();
  wizard.querySelector('[data-wizard-next]').click();
  await settle(6);

  const dialog = app.document.querySelector('#gitSSHKeyDialog');
  const form = dialog.querySelector('form');
  assert.equal(app.document.querySelector('#credentialWizard'), null);
  assert.equal(dialog.querySelectorAll('.git-ssh-key-row').length, 1);
  assert.equal(dialog.querySelector('[data-ssh-key-scope]'), null);
  dialog.querySelector('[data-ssh-key-add]').click();
  form.elements.name.value = 'Other Git host';
  form.elements.host.value = 'git.example.org:2222';
  form.elements.privateKey.value = 'test-only-private-key';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(6);
  const saves = () => app.router.calls.filter(call => call.method === 'PUT' && call.path === endpoint);
  assert.deepEqual(saves()[0].body, {
    name: 'Other Git host', host: 'git.example.org:2222', privateKey: 'test-only-private-key',
  });
  assert.equal(dialog.querySelectorAll('.git-ssh-key-row').length, 2);
  assert.equal(form.hidden, true);
  assert.equal(form.elements.privateKey.value, '', 'saving clears the temporary private key input');

  dialog.querySelector('[data-ssh-key-edit="gitlab"]').click();
  assert.equal(form.elements.privateKey.value, '', 'editing never reads back the stored private key');
  assert.match(form.elements.privateKey.placeholder, /留空保留/);
  assert.equal(form.elements.privateKey.required, false);
  assert.equal(form.elements.host.readOnly, true);
  form.elements.name.value = 'GitLab team';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(6);
  assert.deepEqual(saves()[1].body, {
    id: 'gitlab', name: 'GitLab team', host: 'gitlab.example.org:22', privateKey: '',
  });
  assert.equal(dialog.querySelectorAll('.git-ssh-key-row').length, 2);

  dialog.querySelector('[data-ssh-key-delete="other-host"]').click();
  assert.equal(app.router.calls.some(call => call.method === 'DELETE'), false);
  dialog.querySelector('[data-ssh-key-keep]').click();
  assert.equal(dialog.querySelector('[data-ssh-key-remove]'), null);
  dialog.querySelector('[data-ssh-key-delete="other-host"]').click();
  dialog.querySelector('[data-ssh-key-remove="other-host"]').click();
  await settle(6);
  assert.equal(dialog.querySelectorAll('.git-ssh-key-row').length, 1);
  assert.match(dialog.querySelector('.git-ssh-key-list').textContent, /GitLab team/);
  dialog.querySelector('[data-ssh-key-close]').click();
  assert.equal(app.document.querySelector('#gitSSHKeyDialog'), null);
  assert.equal(app.evaluate('state.currentUser.gitSshKeyConfigured'), true);
});

test('repository forms only warn for the current user missing the selected SSH host', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/users/1/git-ssh-keys', [
    { id: 'gitlab', name: 'GitLab team', host: 'gitlab.example.org:22' },
    { id: 'ipv6', name: 'IPv6', host: '[2001:db8::1]:22' },
  ]);
  app.evaluate(`
    state.currentUser={...state.data.users[0],id:1,isGuest:false,gitSshKeyConfigured:true};
    state.data.users=[state.currentUser];
    state.projectAdminDetailId='project-1';
    state.projectDraftFor='project-1';
    state.projectDraft=projectDraftFrom(state.data.projects[0]);
    state.projectDraftBase=clone(state.projectDraft);
    openRepoDrawer('');
  `);
  const drawer = app.document.querySelector('#repoDrawer');
  const form = drawer.querySelector('form');
  const hint = drawer.querySelector('#repoDrawerSSHMissing');
  const remote = 'ssh://git@git.example.org:2222/team/inventory.git';
  const webUrl = 'https://git.example.org/team/inventory';
  const input = (value) => {
    form.elements.repoUrl.value = value;
    form.elements.repoUrl.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  };
  input(remote);
  form.elements.webUrl.value = webUrl;
  form.elements.integrateBranch.value = 'release';
  await settle(6);
  assert.equal(hint.hidden, false, 'a key for a different host does not match');
  assert.equal(hint.classList.contains('git-ssh-missing'), true);
  assert.match(hint.textContent, /尚未配置 git.example.org:2222 的 SSH 密钥/);
  assert.equal(drawer.querySelector('#repoDrawerSSHSettings'), null);
  assert.equal(hint.querySelector('button'), null);

  input('git@GitLab.example.org:team/app.git');
  await settle(6);
  assert.equal(hint.hidden, true, 'the host key covers every repository on that host');
  input('ssh://git@gitlab.example.org:2222/team/app.git');
  await settle(6);
  assert.equal(hint.hidden, false, 'a different port needs its own host key');
  input('git@[2001:0DB8:0000::1]:team/app.git');
  await settle(6);
  assert.equal(hint.hidden, true, 'equivalent IPv6 addresses match');
  input(remote);
  input('https://github.com/team/app');
  await settle(6);
  assert.equal(hint.hidden, true, 'a late SSH check must not overwrite a new HTTPS address');
  input(remote);
  await settle(6);
  assert.equal(form.elements.webUrl.value, webUrl);
  assert.equal(form.elements.integrateBranch.value, 'release');
  assert.equal(drawer.classList.contains('open'), true);
  assert.equal(app.router.calls.some(call => call.method !== 'GET' && call.path.includes('git-ssh-keys')), false);

  app.evaluate(`state.projectFilter='project-1';openRepositoryModal();`);
  const legacy = app.document.querySelector('#repositoryForm');
  const legacyHint = app.document.querySelector('#repositoryCredentialHint');
  legacy.elements.repoUrl.value = remote;
  legacy.elements.repoUrl.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  await settle(6);
  assert.equal(legacyHint.hidden, false);
  assert.match(legacyHint.textContent, /尚未配置 git.example.org:2222/);
  assert.equal(legacyHint.querySelector('button'), null);
  legacy.elements.repoUrl.value = 'git@gitlab.example.org:team/app.git';
  legacy.elements.repoUrl.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  await settle(6);
  assert.equal(legacyHint.hidden, true);

  app.router.get('/api/users/2/git-ssh-keys', []);
  app.evaluate(`state.currentUser={id:2,isGuest:false};renderRepositoryCredentialHint();`);
  await settle(6);
  assert.equal(legacyHint.hidden, false, 'another user cannot borrow the first user’s host metadata');
  assert.match(legacyHint.textContent, /尚未配置 gitlab.example.org:22/);
});
