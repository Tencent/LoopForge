import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// The project asset bundle dialog on the LOOP page: who may open it, what it
// offers to export, and what the preview/import notes tell the user to do next.
async function bundleApp(t, { user = { id: 'user-1', isPlatformAdmin: true }, project = {} } = {}) {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    window.__toasts = [];
    toast = message => window.__toasts.push(message);
    renderAll = () => {};
    state.currentUser = ${JSON.stringify(user)};
    state.projectFilter = 'project-1';
    Object.assign(state.data.projects[0], ${JSON.stringify(project)});
    state.data.workflows = [
      { id: 'loop-a', projectId: 'project-1', name: 'Bugfix LOOP' },
      { id: 'loop-generic', projectId: 'project-1', name: 'Generic', generic: true },
      { id: 'loop-other', projectId: 'project-2', name: 'Other project' },
    ];
    state.data.agents = [
      { id: 'role-a', projectId: 'project-1', name: 'Developer' },
      { id: 'role-personal', projectId: 'project-1', name: 'Mine', scope: 'personal' },
    ];
    state.data.skills = [];
    state.data.mcpServers = [{ id: 'mcp-a', projectId: 'project-1', name: 'Tracker MCP' }];
  `);
  const dialog = app.document.querySelector('#assetBundleDialog');
  return {
    app,
    dialog,
    notes: () => app.document.querySelector('#assetBundleNotes').textContent,
    toasts: () => JSON.parse(app.evaluate('JSON.stringify(window.__toasts)')),
    open: async () => {
      app.document.querySelector('#assetBundleButton').click();
      await settle();
    },
  };
}

function chooseFile(app, text, size = text.length) {
  app.evaluate(`
    Object.defineProperty(document.querySelector('#assetBundleFile'), 'files', {
      configurable: true,
      value: [{ name: 'bundle.json', size: ${size}, text: async () => ${JSON.stringify(text)} }],
    });
    document.querySelector('#assetBundleFile').dispatchEvent(new Event('change'));
  `);
}

test('only project managers can open the asset bundle dialog', async t => {
  const viewer = await bundleApp(t, {
    user: { id: 'user-9', isPlatformAdmin: false },
    project: { ownerId: 'user-1', members: [{ userId: 'user-9', role: 'member' }] },
  });
  await viewer.open();
  assert.equal(viewer.dialog.open, false);
  assert.deepEqual(viewer.toasts(), ['资产包需要项目管理员权限']);

  const maintainer = await bundleApp(t, {
    user: { id: 9, isPlatformAdmin: false },
    project: { ownerId: 1, members: [{ userId: '9', role: ' Maintainer ' }] },
  });
  await maintainer.open();
  assert.equal(maintainer.dialog.open, true);

  const noProject = await bundleApp(t);
  noProject.app.evaluate("state.projectFilter = ''");
  await noProject.open();
  assert.equal(noProject.dialog.open, false);
  assert.deepEqual(noProject.toasts(), ['请选择一个项目']);
});

test('export offers only this project\'s shared assets and lists the follow-up work', async t => {
  const { app, dialog, notes, open } = await bundleApp(t);
  const downloads = [];
  app.window.HTMLAnchorElement.prototype.click = function () { downloads.push(this.download); };
  app.router.post('/api/projects/project-1/asset-bundles/export', call => ({
    workflows: [{ id: 'loop-a', name: 'Bugfix LOOP' }],
    mcpServers: [{ id: 'mcp-a', name: 'Tracker MCP' }],
    bindings: [
      { kind: 'loop-integrations', assetId: 'loop-a' },
      { kind: 'mcp-connection', assetId: 'mcp-a' },
      { kind: 'custom-kind', assetId: 'raw-id' },
    ],
    echo: call.body,
  }));
  await open();
  assert.equal(dialog.open, true);
  assert.equal(app.document.querySelector('#assetBundleProject').textContent, 'Atlas');
  const boxes = [...dialog.querySelectorAll('#assetBundleChoices input')];
  assert.deepEqual(boxes.map(box => `${box.dataset.kind}:${box.value}`), ['sop:loop-a', 'agent:role-a', 'mcp:mcp-a']);
  assert.deepEqual([...dialog.querySelectorAll('legend')].map(l => l.textContent), ['LOOP', '角色', 'MCP']);
  const exportButton = dialog.querySelector('#exportAssetBundle');
  assert.equal(exportButton.disabled, true, 'nothing selected yet');
  assert.equal(dialog.querySelector('#importAssetBundle').disabled, true, 'no bundle chosen yet');

  boxes[0].checked = true;
  boxes[2].checked = true;
  dialog.querySelector('#assetBundleChoices').dispatchEvent(new app.window.Event('change'));
  assert.equal(exportButton.disabled, false);
  exportButton.click();
  await settle(8);

  const call = app.router.calls.find(c => c.path === '/api/projects/project-1/asset-bundles/export');
  assert.deepEqual(call.body, { assets: [{ kind: 'sop', id: 'loop-a' }, { kind: 'mcp', id: 'mcp-a' }] });
  assert.deepEqual(downloads, ['loopforge-assets.json']);
  assert.deepEqual(notes().split('\n'), [
    '已导出所选资产及项目内依赖。',
    '重新配置 LOOP 凭据、通知和知识库 · Bugfix LOOP',
    '配置 MCP 连接及凭据 · Tracker MCP',
    'raw-id',
  ]);
  assert.equal(dialog.getAttribute('aria-busy'), 'false');
});

test('a project with nothing to export says so', async t => {
  const { app, dialog, open } = await bundleApp(t);
  app.evaluate('state.data.workflows = []; state.data.agents = []; state.data.mcpServers = [];');
  await open();
  assert.equal(dialog.querySelector('#assetBundleChoices').textContent, '本项目暂无可导出的资产。');
  assert.equal(dialog.querySelector('#exportAssetBundle').disabled, true);
});

test('preview then import shows each imported asset with its new ID', async t => {
  const { app, dialog, notes, open } = await bundleApp(t);
  const bundle = '{"version":1}';
  app.router.post('/api/projects/project-1/asset-bundles/preview', {
    assets: [{ kind: 'sop', id: 'loop-a', name: 'Bugfix LOOP' }, { kind: 'agent', id: 'role-a', name: 'Developer' }],
    bindings: [{ kind: 'role-integrations', assetId: 'role-a' }],
  });
  app.router.post('/api/projects/project-1/asset-bundles/import', {
    assets: [
      { kind: 'sop', id: 'loop-a', name: 'Bugfix LOOP', targetId: 'loop-a-2' },
      { kind: 'sandbox', id: 'box', name: 'Docker box', targetId: 'box-2' },
    ],
    bindings: [{ kind: 'sandbox-privileged', assetId: 'box-2' }],
  });
  await open();
  chooseFile(app, bundle);
  await settle(8);
  assert.deepEqual(notes().split('\n'), [
    '将导入 2 项资产，LOOP 保存为项目私有草稿。',
    'LOOP · Bugfix LOOP',
    '角色 · Developer',
    '核对角色的平台 MCP 和知识库绑定 · Developer',
  ]);
  const importButton = dialog.querySelector('#importAssetBundle');
  assert.equal(importButton.disabled, false);
  const preview = app.router.calls.find(c => c.path.endsWith('/asset-bundles/preview'));
  assert.deepEqual(preview.body, { version: 1 });

  importButton.click();
  await settle(10);
  const imported = app.router.calls.find(c => c.path.endsWith('/asset-bundles/import'));
  assert.deepEqual(imported.body, { version: 1 });
  assert.deepEqual(notes().split('\n'), [
    '已导入 2 项私有资产（名称 → 新 ID）：',
    'LOOP · Bugfix LOOP → loop-a-2',
    '沙箱 · Docker box → box-2',
    '导入后需配置：',
    '确认 Docker 特权运行设置 · Docker box (box-2)',
  ]);
  assert.ok(app.router.calls.some(c => c.path === '/api/bootstrap' && c.method === 'GET'), 'assets reloaded after import');
  assert.equal(importButton.disabled, true, 'the same bundle cannot be imported twice');
});

test('a rejected or oversized bundle cannot be imported', async t => {
  const { app, dialog, notes, open } = await bundleApp(t);
  app.router.post('/api/projects/project-1/asset-bundles/preview', { status: 400, body: { error: '资产包格式不正确' } });
  await open();
  chooseFile(app, '{"broken":true}');
  await settle(8);
  assert.match(notes(), /资产包格式不正确/);
  assert.equal(dialog.querySelector('#importAssetBundle').disabled, true);

  chooseFile(app, '{}', 97 * 1024 * 1024);
  await settle(8);
  assert.equal(notes(), '资产包超过 96 MiB。');
  assert.equal(dialog.querySelector('#importAssetBundle').disabled, true);
  assert.equal(app.router.calls.filter(c => c.path.endsWith('/asset-bundles/preview')).length, 1, 'oversized file is not uploaded');
});

test('the dialog stays open while a request is in flight', async t => {
  const { app, dialog, open } = await bundleApp(t);
  let release;
  app.router.post('/api/projects/project-1/asset-bundles/preview', () => new Promise(done => { release = () => done({ assets: [], bindings: [] }); }));
  await open();
  chooseFile(app, '{}');
  await settle();
  assert.equal(dialog.getAttribute('aria-busy'), 'true');
  dialog.querySelector('#closeAssetBundle').click();
  const cancel = new app.window.Event('cancel', { cancelable: true });
  dialog.dispatchEvent(cancel);
  assert.equal(dialog.open, true);
  assert.equal(cancel.defaultPrevented, true);
  release();
  await settle(8);
  dialog.querySelector('#closeAssetBundle').click();
  assert.equal(dialog.open, false);
});
