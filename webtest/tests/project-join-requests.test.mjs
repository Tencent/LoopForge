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
