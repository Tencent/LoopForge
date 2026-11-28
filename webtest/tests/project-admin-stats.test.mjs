import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// 项目配置 counts requirements from the requirement list. Opening it first
// must fetch that list instead of showing 0 until 需求列表 has been visited.
async function projectsApp(t) {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/project-join-requests', []);
  app.router.get('/api/issues', [
    { id: 'issue-1', projectId: 'project-1', title: 'one', status: 'backlog' },
    { id: 'issue-2', projectId: 'project-1', title: 'two', status: 'backlog' },
    { id: 'issue-3', projectId: 'other', title: 'elsewhere', status: 'backlog' },
  ]);
  app.router.get('/api/tasks', []);
  app.router.get('/api/runs', []);
  app.evaluate(`state.data.issues=undefined;state.data.projects=[{...state.data.projects[0],id:'project-1',name:'Atlas',workflowId:'flow-1',ownerId:1,members:[]}]`);
  return app;
}

test('the project list counts requirements when opened directly', async t => {
  const app = await projectsApp(t);
  app.evaluate(`resetProjectAdminDetail();switchView('projects')`);
  await settle();
  assert.ok(app.router.calls.some(c => c.method === 'GET' && c.path === '/api/issues'), 'requirements were fetched');
  const stats = app.document.querySelector('[data-project-open="project-1"] .pa-stats').textContent.replace(/\s+/g, ' ').trim();
  assert.match(stats, /^2 需求/);
});

test('the project header counts requirements when opened directly', async t => {
  const app = await projectsApp(t);
  app.evaluate(`state.projectAdminDetailId='project-1';state.projectAdminTab='basic';state.projectDraft=null;switchView('projects')`);
  await settle();
  const meta = app.document.querySelector('#projectAdminHead .pa-dmeta').textContent.replace(/\s+/g, ' ');
  assert.match(meta, /2 个需求/);
});
