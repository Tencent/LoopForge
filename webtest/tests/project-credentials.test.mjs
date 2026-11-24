import test from 'node:test';
import assert from 'node:assert/strict';
import { chooseOption, loadApp, settle } from '../lib/load-app.mjs';

const endpoint = '/api/projects/project-1/credentials';

test('project credentials save write-only values and preserve them on edit', async (t) => {
  const app = await loadApp({ hash: 'projectCredentials', routes: [
    { path: endpoint, body: { credentials: [], canManage: true } },
  ] });
  t.after(app.close);
  const { document, router, window } = app;
  const panel = document.querySelector('#projectCredentialsContent');
  const create = document.querySelector('#newProjectCredential');
  assert.equal(create.hidden, false);
  assert.match(panel.textContent, /当前项目还没有凭据/);
  create.click();

  let form = panel.querySelector('form');
  assert.deepEqual([...form.querySelectorAll('[name=env]')].map(input => input.value), ['SERVICE_USERNAME', 'SERVICE_PASSWORD']);
  chooseOption(form.elements.kind, 'aksk');
  form.querySelector('[data-add-variable]').click();
  assert.deepEqual([...form.querySelectorAll('[name=env]')].map(input => input.value), ['ACCESS_KEY_ID', 'SECRET_ACCESS_KEY', 'SESSION_TOKEN']);
  form.querySelector('[data-remove-variable]').click();
  assert.equal(form.querySelectorAll('[name=env]').length, 2);
  chooseOption(form.elements.kind, 'token');
  form.elements.name.value = 'Artifact registry';
  form.elements.secretValue.value = 'test-only-credential';

  let credentials = [];
  router.get(endpoint, () => ({ credentials, canManage: true }));
  const itemPath = `${endpoint}/registry`;
  router.post(endpoint, ({ body }) => {
    credentials = [{ id: 'registry', name: body.name, kind: body.kind, variables: body.fields.map(field => field.name) }];
    return credentials[0];
  });
  const submit = () => form.dispatchEvent(new window.Event('submit', { bubbles: true, cancelable: true }));
  submit();
  await settle();
  const saved = router.calls.find(call => call.method === 'POST' && call.path === endpoint);
  assert.deepEqual(saved.body.fields, [{ name: 'SERVICE_API_TOKEN', value: 'test-only-credential' }]);
  assert.match(panel.textContent, /SERVICE_API_TOKEN/);
  assert.doesNotMatch(panel.textContent, /test-only-credential/);

  panel.querySelector('[data-edit-credential]').click();
  form = panel.querySelector('form');
  assert.equal(form.elements.secretValue.value, '');
  assert.equal(form.elements.secretValue.required, false);
  form.elements.env.value = 'ARTIFACT_TOKEN';
  router.put(itemPath, ({ body }) => {
    credentials[0].variables = body.fields.map(field => field.name);
    return credentials[0];
  });
  submit();
  await settle();
  const updated = router.calls.find(call => call.method === 'PUT' && call.path === itemPath);
  assert.deepEqual(updated.body.fields, [{ name: 'ARTIFACT_TOKEN', previousName: 'SERVICE_API_TOKEN' }]);
  assert.match(panel.textContent, /ARTIFACT_TOKEN/);

  router.delete(itemPath, () => { credentials = []; }, 204);
  panel.querySelector('[data-edit-credential]').click();
  panel.querySelector('[data-delete-credential]').click();
  assert.equal(router.calls.some(call => call.method === 'DELETE' && call.path === itemPath), false);
  document.querySelector('#confirmOkBtn').click();
  await settle();
  assert.equal(router.calls.some(call => call.method === 'DELETE' && call.path === itemPath), true);
  assert.match(panel.textContent, /当前项目还没有凭据/);
});

test('SOP credential selection shows project credentials and allows clearing a stale binding', async (t) => {
  const app = await loadApp({ routes: [{ path: endpoint, body: { credentials: [
    { id: 'active', name: 'Active credential' },
    { id: 'disabled', name: 'Disabled credential', disabled: true },
  ] } }] });
  t.after(app.close);
  await app.evaluate(`
    state.currentView = 'flow';
    state.workflow = { ...state.data.workflows[0], projectCredentialIds: ['missing'] };
    renderWorkflowProjectCredentials(state.workflow);
  `);
  const group = app.document.querySelector('#workflowProjectCredentials');
  assert.equal(group.hidden, false);
  assert.equal(group.querySelector('[value=disabled]'), null);
  const stale = group.querySelector('[value=missing]');
  assert.equal(stale.checked, true);
  stale.click();
  assert.equal(stale.disabled, true);
  group.querySelector('[value=active]').click();
  assert.equal(app.evaluate('state.workflow.projectCredentialIds.join()'), 'active');
  assert.equal(app.evaluate('state.workflowDirty'), true);
});
