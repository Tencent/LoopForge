import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('project wizard creates a project, binds a repository, skips optional steps, and renders summary', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.post('/api/projects', (call) => ({ ...call.body, id: call.body.id, key: call.body.key }));
  app.router.post('/api/repositories', (call) => ({ ...call.body, id: 'repo-new' }));
  app.router.put('/api/projects', (call) => call.body);
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner',chineseName:'Owner'};
    state.data.users=[state.currentUser,{id:2,loginName:'member',chineseName:'Member'}];
    state.data.workflows.push({id:'generic-backend-delivery',name:'通用后端交付 SOP',generic:true});
    state.data.mcpServers=[{id:'tapd',marketId:'70'}];
    openProjectWizard();
  `);
  const host = () => app.document.querySelector('#projectWizard');
  const input = (name, value) => {
    const el = host().querySelector(`[name="${name}"]`);
    el.value = value;
    el.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  };
  assert.match(host().textContent, /基本信息/);
  input('name', 'Web Console');
  assert.match(host().querySelector('[data-pw-name-pill]').textContent, /已填写/);
  host().querySelector('[data-pw-action="next"]').click();
  await settle(8);
  assert.equal(host().querySelector('.pw-dialog').dataset.pwStep, 'repos');
  assert.equal(app.router.calls.find(c => c.method === 'POST' && c.path === '/api/projects').body.name, 'Web Console');

  input('repoUrl', 'https://github.com/example/web-console');
  input('repoBranch', 'main');
  input('repoIntegrate', 'develop');
  assert.match(host().querySelector('[data-pw-repo-hint]').textContent, /web-console/i);
  host().querySelector('[data-pw-action="add-repo"]').click();
  await settle(8);
  assert.match(host().textContent, /example\/web-console/);
  const repoCall = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/repositories');
  assert.deepEqual(
    { branch: repoCall.body.defaultBranch, integrate: repoCall.body.integrateBranch, provider: repoCall.body.provider, project: repoCall.body.gongfengProjectId },
    { branch: 'main', integrate: 'develop', provider: 'github', project: 'github:example/web-console' },
  );

  await app.evaluate(`projectWizardNext()`);
  assert.equal(host().querySelector('.pw-dialog').dataset.pwStep, 'members');
  await app.evaluate(`projectWizardSkip()`);
  assert.equal(host().querySelector('.pw-dialog').dataset.pwStep, 'tapd');
  input('tapdWorkspaceId', '20375341');
  input('tapdIterationId', '88');
  await app.evaluate(`projectWizardNext()`);
  await settle(6);
  assert.equal(host().querySelector('.pw-dialog').dataset.pwStep, 'done');
  assert.match(host().textContent, /项目「Web Console」已创建/);
  assert.match(host().querySelector('[data-pw-summary="tapd"]').textContent, /20375341/);
  assert.equal(app.evaluate(`projectWizardDirty()`), false);
});

test('wizard validates names and repository URLs with actionable errors', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    state.data.workflows.push({id:'generic-backend-delivery',name:'Generic',generic:true});
    openProjectWizard();
  `);
  await app.evaluate(`projectWizardNext()`);
  assert.match(app.document.querySelector('#projectWizard [role="alert"]').textContent, /项目名称不能为空/);
  app.evaluate(`projectWizard.step='repos';projectWizard.projectId='project-1';renderProjectWizard()`);
  await app.evaluate(`projectWizardAddRepo()`);
  assert.match(app.document.querySelector('#projectWizard [role="alert"]').textContent, /请先粘贴 GitHub 链接或 Git SSH 地址/);
});

test('without a TAPD integration the wizard goes from members straight to done', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.post('/api/projects', (call) => ({ ...call.body, id: call.body.id, key: call.body.key }));
  app.router.put('/api/projects', (call) => call.body);
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner',chineseName:'Owner'};
    state.data.users=[state.currentUser];
    state.data.workflows.push({id:'generic-backend-delivery',name:'通用后端交付 SOP',generic:true});
    state.data.mcpServers=[];
    state.data.syncConfig={};
    openProjectWizard();
  `);
  const host = () => app.document.querySelector('#projectWizard');
  const name = host().querySelector('[name="name"]');
  name.value = 'Payments';
  name.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  host().querySelector('[data-pw-action="next"]').click();
  await settle(8);
  assert.equal(host().querySelector('.pw-dialog').dataset.pwStep, 'repos');
  assert.doesNotMatch(host().textContent, /TAPD/);
  await app.evaluate(`projectWizardNext()`);
  assert.equal(host().querySelector('.pw-dialog').dataset.pwStep, 'members');
  await app.evaluate(`projectWizardSkip()`);
  await settle(6);
  assert.equal(host().querySelector('.pw-dialog').dataset.pwStep, 'done');
  assert.equal(host().querySelector('[data-pw-summary="tapd"]'), null);
});
