import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('project identity, filtering, payload validation, and project form behavior are deterministic', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.put('/api/projects', (call) => call.body);
  const values = app.evaluate(`({
    identity:generateProjectIdentity('Web Console',[{id:'web-console',key:'WC'}],'seed'),
    chinese:generateProjectIdentity('控制台',[],'a1b2'),
    route:parseProjectAdminRoute('#/projects/project%201/members'),
    hash:projectAdminHash('project 1','repos'),
    people:projectPeopleIds({ownerId:1,members:[{userId:1},{userId:2},{userId:2}]}),
    filtered:filterAdminProjects(
      [{id:'p1',name:'Alpha',ownerId:1,members:[]},{id:'p2',name:'Beta',ownerId:2,members:[]}],
      [{id:'r1',projectId:'p1',name:'frontend'}],'front','all',1).map(p=>p.id)
  })`);
  assert.equal(JSON.stringify(values.identity), JSON.stringify({ id: 'web-console-2', key: 'WCA' }));
  assert.equal(JSON.stringify(values.chinese), JSON.stringify({ id: 'proj-a1b2', key: 'PRJ' }));
  assert.equal(JSON.stringify(values.route), JSON.stringify({ id: 'project 1', tab: 'members' }));
  assert.equal(values.hash, '#/projects/project%201/repos');
  assert.equal(JSON.stringify(values.people), JSON.stringify([1, 2]));
  assert.equal(JSON.stringify(values.filtered), JSON.stringify(['p1']));

  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    state.data.users=[state.currentUser,{id:2,loginName:'member'}];
    state.data.workflows.push({id:'generic-backend-delivery',name:'Generic',generic:true});
    state.projectAdminDetailId='new';state.projectAdminTab='basic';state.projectDraft=null;
    renderProjectDetail({force:true});
  `);
  const form = app.document.querySelector('#projectAdminForm');
  form.elements.name.value = 'Payments API';
  form.elements.name.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(form.elements.id.value, 'payments-api');
  assert.equal(form.elements.key.value, 'PA');
  form.querySelector('[data-pa-color="#10b981"]').click();
  assert.equal(app.evaluate(`state.projectDraft.color`), '#10b981');
  assert.equal(app.evaluate(`setProjectAdminTab('repos')`), false);
  const payloadResult = app.evaluate(`
    (() => {
      state.data.repositories=[{id:'repo-x',projectId:'payments-api'}];
      const payload=buildProjectPayload({id:'payments-api',name:' Payments ',key:'pay',ownerId:1,
        workflowId:'generic-backend-delivery',members:[{userId:1},{userId:2}],defaultRepositoryId:'repo-x'});
      const valid=validateProjectPayload(payload,{creating:true,projects:[],workflows:state.data.workflows});
      const duplicate=validateProjectPayload({...payload,members:[{userId:2},{userId:2}]},
        {creating:true,projects:[],workflows:state.data.workflows});
      return {payload,valid,duplicate};
    })()
  `);
  assert.equal(payloadResult.payload.name, 'Payments');
  assert.equal(payloadResult.payload.key, 'PAY');
  assert.equal(payloadResult.payload.members.length, 1);
  assert.equal(payloadResult.valid, null);
  assert.match(payloadResult.duplicate.message, /不能重复/);

  app.evaluate(`
    resetProjectAdminDetail();
    state.data.projects[0].ownerId=1;state.data.projects[0].key='ATL';state.data.projects[0].workflowId='flow-1';
    state.projectAdminDetailId='project-1';state.projectAdminTab='basic';
    renderProjectDetail({force:true});
  `);
  const edit = app.document.querySelector('#projectAdminForm');
  edit.querySelector('[data-pa-ident-edit]').click();
  assert.equal(app.evaluate(`state.projectIdentCustom`), true);
  edit.querySelector('[data-pa-ident-collapse]').click();
  edit.elements.description.value = 'Updated project scope';
  edit.elements.description.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  edit.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  const update = app.router.calls.find(c => c.method === 'PUT' && c.path === '/api/projects');
  assert.equal(update.body.description, 'Updated project scope');
  assert.equal(app.evaluate(`state.data.projects[0].description`), 'Updated project scope');
});

test('repository drawer detects Gongfeng URLs and persists a default repository', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.post('/api/repositories', (call) => ({ ...call.body, id: 'repo-new' }));
  app.router.put('/api/projects', (call) => call.body);
  app.router.delete('/api/repositories', { ok: true });
  app.evaluate(`
    state.projectAdminDetailId='project-1';
    state.projectDraftFor='project-1';
    state.projectDraft=projectDraftFrom(state.data.projects[0]);
    state.projectDraftBase=clone(state.projectDraft);
    state.data.repositories=[];
    openRepoDrawer('');
  `);
  const form = app.document.querySelector('#repoDrawerForm');
  form.elements.repoUrl.value = 'git@git.example.com:platform/payments.git';
  form.elements.repoUrl.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(app.document.querySelector('#repoDrawerDetect').hidden, false);
  assert.equal(form.elements.name.value, 'payments');
  form.elements.defaultBranch.value = 'main';
  form.elements.integrateBranch.value = 'release';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  const create = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/repositories');
  assert.equal(create.body.gongfengProjectId, 'platform/payments');
  assert.equal(create.body.defaultBranch, 'main');
  assert.equal(app.evaluate(`state.data.repositories[0].id`), 'repo-new');
  assert.equal(app.evaluate(`state.data.projects[0].defaultRepositoryId`), 'repo-new');
  assert.equal(app.document.querySelector('#repoDrawer').classList.contains('open'), false);
  app.evaluate(`window.__deleteRepo=deleteProjectRepository('repo-new')`);
  await settle(2);
  app.document.querySelector('#confirmOkBtn').click();
  await app.evaluate(`window.__deleteRepo`);
  assert.equal(app.evaluate(`state.data.repositories.length`), 0);
  assert.ok(app.router.calls.some(c => c.method === 'DELETE' && c.path === '/api/repositories'));
});

test('project owner deletes a project from its danger zone and the list moves on', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.delete('/api/projects/okr-system', (call) => ({ id: 'okr-system', archivedAt: '2026-11-01T00:00:00Z' }));
  app.evaluate(`
    afConfirm=async()=>true;
    state.authRequired=true;
    state.currentUser={id:7,loginName:'owner'};
    state.data.users=[state.currentUser];
    state.data.projects=[...state.data.projects,{id:'okr-system',name:'OKR系统',ownerId:7,members:[]}];
    state.projectFilter='okr-system';
  `);
  const values = app.evaluate(`({
    owner:canDeleteProject({id:'okr-system',ownerId:7}),
    other:canDeleteProject({id:'x',ownerId:8}),
    unsaved:canDeleteProject(null),
    row:projectDeleteRow({id:'okr-system',ownerId:7}).includes('data-project-delete="okr-system"'),
    hidden:projectDeleteRow({id:'x',ownerId:8})
  })`);
  assert.deepEqual({ ...values }, { owner: true, other: false, unsaved: false, row: true, hidden: '' });
  await app.evaluate(`deleteProjectAdmin('okr-system')`);
  await settle(4);
  assert.ok(app.router.calls.some(c => c.method === 'DELETE' && c.path === '/api/projects/okr-system'));
  assert.equal(app.evaluate(`state.data.projects.some(p=>p.id==='okr-system')`), false);
  assert.notEqual(app.evaluate(`state.projectFilter`), 'okr-system');
  assert.equal(app.evaluate(`localStorage.getItem('agentflow.activeProject')`), app.evaluate(`state.projectFilter`));
  assert.equal(app.evaluate(`state.projectAdminDetailId||''`), '');
});
