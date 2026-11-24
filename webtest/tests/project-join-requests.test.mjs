import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle, chooseOption } from '../lib/load-app.mjs';

function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}

async function projectApp(t) {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/project-join-requests', []);
  app.evaluate(`
    state.authRequired=true;state.currentUser={id:1,loginName:'owner'};
    state.data.users=[state.currentUser,{id:2,loginName:'member'},{id:3,loginName:'applicant'}];
    state.data.projects=[{...state.data.projects[0],key:'ATL',workflowId:'flow-1',ownerId:1,
      members:[{userId:2,role:'member'}],membersVersion:'before'}];
    state.projectAdminDetailId='project-1';state.projectAdminTab='members';
    state.projectDraft=null;state.projectJoinRequests=[];state.projectJoinLoadedAt=Date.now();
    activeViewName=()=> 'projects';renderAll=()=>{};
    afConfirm=async()=>true;afPrompt=async()=> 'join reason';
    renderProjectDetail({force:true});
  `);
  return app;
}

test('a late member read cannot undo a completed project save', async t => {
  const app = await projectApp(t);
  const old = app.evaluate('clone(state.data.projects[0])');
  const read = deferred();
  app.router.get('/api/projects/project-1', () => read.promise);
  app.router.put('/api/projects', call => ({ ...call.body, membersVersion: 'after-save' }));
  const loading = app.evaluate("refreshProjectJoinMembers('project-1')");
  const form = app.document.querySelector('#projectAdminForm');
  chooseOption(form.querySelector('[data-m="role"]'), 'viewer');
  form.elements.description.value = 'saved description';
  form.elements.description.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  await app.evaluate("saveProjectAdmin({preventDefault(){},currentTarget:document.querySelector('#projectAdminForm')})");
  assert.ok(app.router.calls.some(c => c.method === 'PUT' && c.path === '/api/projects'), 'project settings were saved');
  read.resolve(old);
  await loading;
  const saved = app.evaluate('state.data.projects[0]');
  assert.equal(saved.description, 'saved description');
  assert.equal(saved.members[0].role, 'viewer');
  assert.equal(saved.membersVersion, 'after-save');
});

test('a late member read cannot restore an archived project', async t => {
  const app = await projectApp(t);
  const old = app.evaluate('clone(state.data.projects[0])');
  const read = deferred();
  app.router.get('/api/projects/project-1', () => read.promise);
  app.router.delete('/api/projects/project-1', { ok: true });
  const loading = app.evaluate("refreshProjectJoinMembers('project-1')");
  await app.evaluate("deleteProjectAdmin('project-1')");
  read.resolve(old);
  await loading;
  assert.equal(app.evaluate("state.data.projects.some(p=>p.id==='project-1')"), false);
  app.evaluate('renderProjectList()');
  assert.equal(app.document.querySelector('[data-project-open="project-1"]'), null);
});

test('a late member read cannot undo a saved default repository', async t => {
  const app = await projectApp(t);
  const old = app.evaluate('clone(state.data.projects[0])');
  const read = deferred();
  app.router.get('/api/projects/project-1', () => read.promise);
  app.router.put('/api/projects', call => call.body);
  const loading = app.evaluate("refreshProjectJoinMembers('project-1')");
  await app.evaluate("saveProjectDefaultRepository(state.data.projects[0],'repo-new')");
  read.resolve(old);
  await loading;
  assert.equal(app.evaluate('state.data.projects[0].defaultRepositoryId'), 'repo-new');
});

function startProjectSave(app, mode) {
  if (mode === 'default repository') {
    return app.evaluate("saveProjectDefaultRepository(state.data.projects[0],'repo-new')");
  }
  if (mode === 'wizard') {
    app.evaluate("openProjectWizard();projectWizard.projectId='project-1';projectWizardEnter('members');projectWizard.draft.members[0].role='viewer'");
    return app.evaluate('projectWizardSaveProject()');
  }
  const form = app.document.querySelector('#projectAdminForm');
  form.elements.description.value = 'saved description';
  form.elements.description.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  return app.evaluate("saveProjectAdmin({preventDefault(){},currentTarget:document.querySelector('#projectAdminForm')})");
}

for (const mode of ['settings', 'default repository', 'wizard']) {
  test(`a late ${mode} response cannot undo an approved member refresh`, async t => {
    const app = await projectApp(t);
    const write = deferred();
    let written, server, reads = 0;
    app.router.put('/api/projects', call => {
      written = { ...call.body, membersVersion: 'saved-before-approval' };
      server = structuredClone(written);
      return write.promise;
    });
    app.router.post('/api/project-join-requests/71/approve', () => {
      server = { ...server, members: [...server.members, { userId: 3, role: 'viewer' }], membersVersion: 'approved' };
      return { id: 71, projectId: 'project-1', userId: 3, role: 'viewer', status: 'approved' };
    });
    app.router.get('/api/projects/project-1', () => { reads++; return server; });
    const saving = startProjectSave(app, mode);
    await settle();
    assert.ok(written, 'the project write committed before approval');
    await app.evaluate("reviewProjectJoin(71,true,'viewer')");
    assert.equal(app.evaluate('state.data.projects[0].membersVersion'), 'approved');
    write.resolve(written);
    await saving;
    const current = app.evaluate('state.data.projects[0]');
    assert.deepEqual(JSON.parse(JSON.stringify(current)), server);
    assert.equal(reads, 2, 'conflicting save responses trigger an authoritative read');
    assert.equal(current.members.find(m => m.userId === 3)?.role, 'viewer');
    assert.equal(app.evaluate('state.projectJoinRequests[0].status'), 'approved');
    if (mode === 'wizard') {
      assert.equal(app.evaluate('projectWizard.draft.members.some(m=>m.userId===3)'), true);
    } else {
      app.evaluate('renderProjectDetail({force:true});captureProjectAdminForm()');
      assert.equal(app.evaluate('buildProjectPayload(state.projectDraft).membersVersion'), 'approved');
    }
  });

  test(`a late ${mode} response cannot restore an archived project`, async t => {
    const app = await projectApp(t);
    const write = deferred();
    let written;
    app.router.put('/api/projects', call => { written = call.body; return write.promise; });
    app.router.delete('/api/projects/project-1', { ok: true });
    const saving = startProjectSave(app, mode);
    await settle();
    assert.ok(written);
    await app.evaluate("deleteProjectAdmin('project-1')");
    write.resolve(written);
    await saving;
    assert.equal(app.evaluate("state.data.projects.some(p=>p.id==='project-1')"), false);
  });
}

test('the wizard members step replaces the roster it loaded, not a newer one', async t => {
  const app = await projectApp(t);
  app.evaluate("openProjectWizard();projectWizard.projectId='project-1';projectWizardEnter('members')");
  const roster = app.evaluate('clone(state.data.projects[0])');
  const approved = { ...roster, members: [...roster.members, { userId: 3, role: 'member' }], membersVersion: 'after-approval' };
  // Another session approves a join request and a project list refresh installs the new roster,
  // while the wizard step still shows the members it loaded.
  app.evaluate(`storeSavedProject(${JSON.stringify(approved)})`);
  app.router.get('/api/projects/project-1', approved);
  let written = null;
  app.router.put('/api/projects', call => {
    written = call.body;
    return call.body.membersVersion === approved.membersVersion
      ? { ...call.body, membersVersion: 'replaced' }
      : { status: 409, body: { error: '项目成员已变化，请刷新项目后重试' } };
  });
  app.evaluate('projectWizard.draft.members=[]');
  await app.evaluate('projectWizardSaveProject()');
  assert.equal(written.membersVersion, roster.membersVersion, 'the step submits the token of the roster it edited');
  assert.equal(app.evaluate('state.data.projects[0].members.some(m=>Number(m.userId)===3)'), true, 'the approved member survives');
  assert.equal(app.evaluate('projectWizard.draft.membersVersion'), approved.membersVersion, 'the step reloads the current roster');
  assert.equal(app.evaluate('projectWizard.draft.members.some(m=>Number(m.userId)===3)'), true);
  assert.match(app.document.querySelector('#projectWizard [role="alert"]').textContent, /项目成员已经变化/);
});

test('a failed read after a late save reports the page is out of sync', async t => {
  const app = await projectApp(t);
  const write = deferred(), roster = app.evaluate('clone(state.data.projects[0])');
  let written, reads = 0;
  app.router.put('/api/projects', call => { written = { ...call.body, membersVersion: 'saved' }; return write.promise; });
  app.router.get('/api/projects/project-1', () => ++reads === 1
    ? { ...roster, members: [...roster.members, { userId: 3, role: 'member' }], membersVersion: 'approved' }
    : { status: 503, body: { error: '读取项目失败' } });
  app.router.post('/api/project-join-requests/71/approve', { id: 71, projectId: 'project-1', userId: 3, role: 'member', status: 'approved' });
  const saving = startProjectSave(app, 'settings');
  await settle();
  await app.evaluate("reviewProjectJoin(71,true,'member')");
  write.resolve(written);
  await saving;
  assert.equal(reads, 2, 'the save response is reconciled through an authoritative read');
  assert.notEqual(app.evaluate('state.data.projects[0].description'), 'saved description', 'the cache never adopted the save snapshot');
  assert.match(app.document.querySelector('#toastContainer').textContent, /没能同步到最新内容/);
  assert.equal(app.evaluate('state.projectDraft.description'), 'saved description', 'the edit is kept for a retry');
  assert.equal(app.evaluate('projectAdminDirty()'), true);
});

test('a save waits for the refresh that superseded its own read', async t => {
  const app = await projectApp(t);
  const write = deferred(), read = deferred(), roster = app.evaluate('clone(state.data.projects[0])');
  const unavailable = { status: 503, body: { error: '读取项目失败' } };
  let written, reads = 0;
  app.router.put('/api/projects', call => { written = { ...call.body, membersVersion: 'saved' }; return write.promise; });
  app.router.get('/api/projects/project-1', () => ++reads === 1 ? read.promise : unavailable);
  app.router.post('/api/project-join-requests/71/approve', { id: 71, projectId: 'project-1', userId: 3, role: 'member', status: 'approved' });
  const saving = startProjectSave(app, 'settings');
  await settle();
  // A project list refresh installs a newer snapshot, so the save has to reconcile through a read.
  app.evaluate(`storeSavedProject(${JSON.stringify({ ...roster, membersVersion: 'refreshed' })})`);
  write.resolve(written);
  await settle();
  assert.equal(reads, 1, 'the save reconciles through its own read');
  // An approval replaces that read before it returns, and neither read succeeds.
  const approving = app.evaluate("reviewProjectJoin(71,true,'member')");
  await settle();
  assert.equal(reads, 2);
  read.resolve(unavailable);
  await approving;
  await saving;
  assert.notEqual(app.evaluate('state.data.projects[0].description'), 'saved description', 'no read installed the save snapshot');
  assert.match(app.document.querySelector('#toastContainer').textContent, /没能同步到最新内容/);
  assert.equal(app.evaluate('state.projectDraft.description'), 'saved description', 'the edit is kept for a retry');
  assert.equal(app.evaluate('projectAdminDirty()'), true);
});

test('out-of-order settings saves retain the latest server snapshot', async t => {
  const app = await projectApp(t);
  const first = deferred();
  let written, server, writes = 0;
  app.router.put('/api/projects', call => {
    server = { ...call.body, membersVersion: `save-${++writes}` };
    if (writes === 1) { written = server; return first.promise; }
    return server;
  });
  app.router.get('/api/projects/project-1', () => server);
  const saving = startProjectSave(app, 'settings');
  await settle();
  await app.evaluate("saveProjectDefaultRepository(state.data.projects[0],'repo-new')");
  first.resolve(written);
  await saving;
  const current = app.evaluate('state.data.projects[0]');
  assert.equal(current.defaultRepositoryId, 'repo-new');
  assert.equal(current.membersVersion, 'save-2');
});

for (const firstRead of ['delayed', 'failed']) {
  test(`a late save reconciles an approval whose first member read is ${firstRead}`, async t => {
    const app = await projectApp(t);
    const write = deferred(), read = deferred();
    let written, server, reads = 0;
    app.router.put('/api/projects', call => {
      written = { ...call.body, membersVersion: 'before-approval' };
      server = structuredClone(written);
      return write.promise;
    });
    app.router.post('/api/project-join-requests/71/approve', () => {
      server = { ...server, members: [...server.members, { userId: 3, role: 'viewer' }], membersVersion: 'approved' };
      return { id: 71, projectId: 'project-1', userId: 3, role: 'viewer', status: 'approved' };
    });
    app.router.get('/api/projects/project-1', () => {
      if (++reads > 1) return server;
      return firstRead === 'delayed' ? read.promise : { status: 500, body: { error: 'read failed' } };
    });
    const saving = startProjectSave(app, 'settings');
    await settle();
    const approving = app.evaluate("reviewProjectJoin(71,true,'viewer')");
    await settle();
    assert.equal(reads, 1);
    write.resolve(written);
    await saving;
    read.resolve(server);
    await approving;
    assert.equal(reads, 2);
    assert.equal(app.evaluate('state.data.projects[0].membersVersion'), 'approved');
    assert.equal(app.evaluate('state.data.projects[0].members.some(m=>m.userId===3)'), true);
  });
}

for (const archived of [false, true]) {
  test(`a delayed create response respects a project ${archived ? 'archived' : 'refreshed'} before it returns`, async t => {
    const app = await projectApp(t);
    const write = deferred();
    let written;
    app.router.post('/api/projects', call => {
      written = { ...call.body, membersVersion: 'created' };
      return write.promise;
    });
    app.evaluate("state.data.workflows.push({id:'generic-backend-delivery',name:'Generic',generic:true});openProjectWizard();projectWizard.draft.name='Created Race'");
    const creating = app.evaluate('projectWizardCreate()');
    await settle();
    assert.ok(written);
    const fresh = { ...written, members: [{ userId: 3, role: 'viewer' }], membersVersion: 'approved' };
    app.evaluate(`storeSavedProject(${JSON.stringify(fresh)})`);
    app.router.get(`/api/projects/${written.id}`, fresh);
    if (archived) {
      app.router.delete(`/api/projects/${written.id}`, { ok: true });
      await app.evaluate(`deleteProjectAdmin(${JSON.stringify(written.id)})`);
    }
    write.resolve(written);
    await creating;
    const current = app.evaluate(`state.data.projects.find(p=>p.id===${JSON.stringify(written.id)})`);
    if (archived) assert.equal(current, undefined);
    else assert.equal(current.membersVersion, 'approved');
  });
}

for (const action of ['approve', 'reject', 'withdraw', 'apply']) {
  test(`a stale join list cannot undo a successful ${action}`, async t => {
    const app = await projectApp(t);
    const pending = { id: 71, projectId: 'project-1', userId: 3, role: 'member', status: 'pending' };
    const updated = { ...pending, status: { approve: 'approved', reject: 'rejected', withdraw: 'withdrawn', apply: 'pending' }[action] };
    const roster = app.evaluate('clone(state.data.projects[0])');
    app.router.get('/api/projects/project-1', { ...roster, members: [...roster.members, { userId: 3, role: 'member' }], membersVersion: 'approved' });
    app.router.post(action === 'apply' ? '/api/projects/project-1/join-requests' : `/api/project-join-requests/71/${action}`, updated);
    app.evaluate(`state.projectJoinRequests=${JSON.stringify(action === 'apply' ? [] : [pending])}`);
    const read = deferred();
    let reads = 0;
    app.router.get('/api/project-join-requests', () => ++reads === 1 ? read.promise : [updated]);
    const loading = app.evaluate('loadProjectJoinRequests(true)');
    await app.evaluate(action === 'apply' ? "applyProjectJoin('project-1')" : action === 'withdraw' ? 'withdrawProjectJoin(71)' : `reviewProjectJoin(71,${action === 'approve'},'member')`);
    read.resolve(action === 'apply' ? [] : [pending]);
    await loading;
    assert.equal(reads, 2, 'an invalidated list is read again');
    assert.equal(app.evaluate('state.projectJoinRequests[0]?.status'), updated.status);
    const approveButton = app.document.querySelector('[data-join-approve="71"]');
    if (updated.status !== 'pending') assert.equal(approveButton, null);
  });
}

test('only the newest join list may render even when an older request returns last', async t => {
  const app = await projectApp(t);
  const pending = { id: 71, projectId: 'project-1', userId: 3, role: 'member', status: 'pending' };
  const read = deferred();
  let reads = 0;
  app.router.get('/api/project-join-requests', () => ++reads === 1 ? read.promise : [{ ...pending, status: 'approved' }]);
  const first = app.evaluate('loadProjectJoinRequests(true)');
  await app.evaluate('loadProjectJoinRequests(true)');
  read.resolve([pending]);
  await first;
  assert.equal(reads, 2);
  assert.equal(app.evaluate('state.projectJoinRequests[0].status'), 'approved');
  assert.equal(app.document.querySelector('[data-join-approve="71"]'), null);
});

test('a join mutation also invalidates a list waiting for the applicant roster', async t => {
  const app = await projectApp(t);
  const approved = { id: 70, projectId: 'project-1', userId: 1, role: 'member', status: 'approved' };
  const pending = { id: 71, projectId: 'project-1', userId: 3, role: 'member', status: 'pending' };
  const read = deferred();
  let reads = 0;
  app.router.get('/api/projects/project-1', () => read.promise);
  app.router.get('/api/project-join-requests', () => [approved, { ...pending, status: ++reads === 1 ? 'pending' : 'rejected' }]);
  app.router.post('/api/project-join-requests/71/reject', { ...pending, status: 'rejected' });
  app.evaluate(`state.projectJoinRequests=[${JSON.stringify(pending)}]`);
  const loading = app.evaluate('loadProjectJoinRequests(true)');
  await settle();
  assert.ok(app.router.calls.some(c => c.path === '/api/projects/project-1'));
  await app.evaluate('reviewProjectJoin(71,false)');
  read.resolve(app.evaluate('clone(state.data.projects[0])'));
  await loading;
  assert.equal(reads, 2);
  assert.equal(app.evaluate('state.projectJoinRequests.find(r=>r.id===71).status'), 'rejected');
  assert.equal(app.document.querySelector('[data-join-approve="71"]'), null);
});
