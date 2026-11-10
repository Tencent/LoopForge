// Collaboration rooms and group-mode run detail, without a model.
// rooms.js is not on index.html, so this loads that script after a minimal
// host exists. The copy and the create request come from createRoom.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { loadApp, settle, WEB_ROOT } from '../lib/load-app.mjs';

async function loadRooms(app) {
  app.evaluate(`
    const host = document.createElement('div');
    host.innerHTML = [
      '<button type="button" id="newRoom"></button>',
      '<button type="button" id="renameRoom" hidden></button>',
      '<button type="button" id="roomModeChat"></button>',
      '<button type="button" id="roomModeIssue"></button>',
      '<form id="roomComposer">',
      '<input id="roomInput">',
      '<button type="button" id="roomDispatch"></button>',
      '<select id="roomSOP"></select>',
      '</form>',
      '<div id="roomList"></div>',
      '<div id="roomLog"></div>',
      '<div id="roomResources"></div>',
      '<h2 id="roomTitle"></h2>',
      '<p id="roomSubtitle"></p>',
    ].join('');
    document.body.appendChild(host);
    state.projectFilter = 'project-1';
    state.currentView = 'home';
  `);
  const source = await readFile(path.join(WEB_ROOT, 'rooms.js'), 'utf8');
  new vm.Script(source, { filename: path.join(WEB_ROOT, 'rooms.js') }).runInContext(app.context);
}

test('creating a collaboration room uses the existing prompt and posts the room', async (t) => {
  const app = await loadApp();
  t.after(() => {
    app.evaluate('clearTimeout(roomsPollTimer)');
    return app.close();
  });
  app.router.post('/api/rooms', (call) => ({
    id: 'room-1', name: call.body.name, projectId: call.body.projectId, members: [],
  }));
  app.router.get(/^\/api\/rooms\?/, [{
    id: 'room-1', name: '需求值班群', projectId: 'project-1', members: [],
  }]);
  app.router.get(/^\/api\/rooms\/room-1\/messages/, []);
  await loadRooms(app);

  app.document.querySelector('#newRoom').click();
  await settle(4);
  assert.equal(app.document.querySelector('#confirmTitle').textContent, '新建协作群');
  assert.equal(app.document.querySelector('#confirmMessage').textContent, '群名称会显示在当前项目的群列表里。');
  assert.equal(app.document.querySelector('#confirmPromptLabel').textContent, '群名称');
  assert.equal(app.document.querySelector('#confirmPromptInput').placeholder, '例如：需求值班群');
  assert.equal(app.document.querySelector('#confirmOkBtn').textContent, '创建');

  app.document.querySelector('#confirmPromptInput').value = '  需求值班群  ';
  app.document.querySelector('#confirmOkBtn').click();
  await settle(8);

  const created = app.router.calls.filter((call) => call.method === 'POST' && call.path === '/api/rooms');
  assert.equal(created.length, 1);
  assert.deepEqual(created[0].body, { projectId: 'project-1', name: '需求值班群' });
  assert.match(app.document.body.textContent, /已创建协作群/);
});

test('a group-mode run detail shows the existing collaboration copy', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const workflow = app.evaluate('structuredClone(state.data.workflows[0])');
  workflow.collaborationMode = 'group';
  workflow.version = 3;
  workflow.nodes = [
    { id: 'node-1', type: 'agent', name: 'Implement' },
    { id: 'task-1', type: 'agent', name: 'Room task', requestId: 'req-1' },
  ];
  const run = {
    ...app.evaluate('structuredClone(state.data.runs[0])'),
    status: 'succeeded',
    kind: 'sop',
    finishedAt: '2026-10-21T10:05:00Z',
    workflowSnapshot: workflow,
    nodes: { 'node-1': { status: 'succeeded', attempt: 1 } },
  };
  app.router.get('/api/runs/run-1', run);
  app.evaluate(`state.data.runs=[${JSON.stringify(run)}];state.selectedRun='run-1';state.activeRun=state.data.runs[0]`);
  app.evaluate(`switchView('runs');showRunDetail('run-1')`);
  await settle(6);
  app.evaluate('renderRunDetail(state.activeRun)');

  const detail = app.document.querySelector('#runDetail').textContent;
  assert.match(detail, /协作模式/);
  assert.match(detail, /历史协作记录/);
  assert.match(detail, /SOP 主干已冷冻/);
  assert.match(detail, /1 步可展开/);
  assert.match(detail, /群内任务 1 个/);
  assert.match(detail, /冷冻 SOP v3/);
  assert.doesNotMatch(app.evaluate(`document.querySelector('#runDetail .run-progress-cell span').textContent`), /执行进度/);
});
