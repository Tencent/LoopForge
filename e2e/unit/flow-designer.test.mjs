// Unit checks for the flow designer graph operations, run against the real
// web/app sources in a vm context (no browser, no deployment):
//   node --test e2e/unit/*.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';

const web = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'web', 'app');
const files = ['flow-history.js', 'flow-viewport.js', 'flow-palette.js', 'flows.js'];

const fakeEl = (extra = {}) => ({
  value: '', innerHTML: '', textContent: '', hidden: false, dataset: {},
  classList: { toggle() {}, add() {}, remove() {}, contains: () => false },
  querySelectorAll: () => [],
  ...extra,
});

function load({ workflows = [], projectId = 'p1', elements = {}, groups = [], agents = [] } = {}) {
  const toasts = [];
  const store = new Map();
  const canvas = { getBoundingClientRect: () => ({ left: 100, top: 50, width: 800, height: 600 }), clientWidth: 800, clientHeight: 600 };
  const document = {
    getElementById: id => (id === 'canvas' ? canvas : elements[id] ?? null),
    querySelector: () => null,
    querySelectorAll: sel => (sel === '#flowPaletteBody .fd-pal-group' ? groups : []),
  };
  const ctx = {
    console, JSON, Math, Date, Set, Map, setTimeout: () => 0, clearTimeout: () => {}, requestAnimationFrame: () => 0,
    document,
    localStorage: { getItem: k => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)) },
    CSS: { escape: s => s },
    state: { workflow: null, data: { workflows, agents: [], executors: [], projects: [], sandboxes: [] }, projectFilter: projectId },
    $: () => null, $$: () => [],
    toast: m => toasts.push(m),
    uid: p => `${p}-${Math.random().toString(36).slice(2, 8)}`,
    clone: v => JSON.parse(JSON.stringify(v)),
    escapeHTML: v => String(v ?? ''),
    afConfirm: async () => true,
    catalogForeign: pid => !!pid && pid !== projectId,
    catalogGeneric: wf => !!wf?.generic,
    catalogVisible: () => true,
    isPlatformAdmin: () => false,
    catalogWorkflows: () => workflows.filter(w => !w.generic),
    workflowAgents: () => agents,
    workflowAgent: id => agents.find(a => a.id === id) || null,
    workflowStages: wf => [wf.nodes],
    rememberOpenWorkflow: () => {},
    renderAll: () => {},
  };
  vm.createContext(ctx);
  for (const f of files) vm.runInContext(fs.readFileSync(path.join(web, f), 'utf8'), ctx, { filename: f });
  const run = code => vm.runInContext(code, ctx);
  // vm objects live in another realm; compare plain JSON copies.
  const json = code => JSON.parse(run(`JSON.stringify(${code})`) ?? 'null');
  return { ctx, run, json, toasts };
}

const node = (id, extra = {}) => ({ id, name: id, type: 'script', position: { x: 0, y: 0 }, rejectTo: '', onFailure: 'fail', ...extra });
const graph = () => ({
  id: 'wf1', projectId: 'p1', name: 'demo', version: 1,
  nodes: [node('a'), node('b'), node('c', { rejectTo: 'a', onFailure: 'reject' })],
  edges: [{ id: 'e1', source: 'a', target: 'b', when: 'success' }, { id: 'e2', source: 'b', target: 'c', when: 'success' }],
});

function withWorkflow(wf = graph(), opts) {
  const env = load(opts);
  env.ctx.state.workflow = wf;
  return env;
}

test('setRejectTo sets one reject route with onFailure=reject and clears back to fail', () => {
  const { ctx, run } = withWorkflow();
  const b = ctx.state.workflow.nodes[1];
  assert.equal(run("setRejectTo('b','a')"), true);
  assert.deepEqual([b.rejectTo, b.onFailure], ['a', 'reject']);
  assert.equal(run("setRejectTo('b','c')"), true);
  assert.deepEqual([b.rejectTo, b.onFailure], ['c', 'reject'], 'a second drag replaces the single reject target');
  assert.equal(run("setRejectTo('b','b')"), false, 'cannot reject to itself');
  assert.equal(run("setRejectTo('b','missing')"), false);
  assert.equal(run("setRejectTo('b','')"), true);
  assert.deepEqual([b.rejectTo, b.onFailure], ['', 'fail']);
  assert.equal(ctx.state.workflowDirty, true);
});

test('the preview build node has no failure outlet', () => {
  const wf = graph();
  wf.nodes.push(node('af-preview-build', { type: 'agent' }));
  const { run } = withWorkflow(wf);
  assert.equal(run("setRejectTo('af-preview-build','a')"), false);
  assert.equal(run("flowConnectCheck(state.workflow.nodes[3], state.workflow.nodes[0], 'reject').ok"), false);
});

test('connect refuses duplicates and cycles; reject routes stay out of edges', () => {
  const { ctx, run } = withWorkflow();
  assert.equal(run("connect('b','a')"), false, 'b→a would close a cycle');
  assert.equal(run("connect('a','b')"), false, 'already connected');
  assert.equal(run("connect('a','c')"), true);
  assert.equal(ctx.state.workflow.edges.length, 3);
  assert.deepEqual(Object.keys(ctx.state.workflow.edges[2]).sort(), ['id', 'source', 'target', 'when']);
});

test('removeNode drops its edges and reject routes pointing at it, and undo restores them', () => {
  const { ctx, run } = withWorkflow();
  ctx.state.selectedNode = 'a';
  assert.equal(run("removeNode('a')"), true);
  const wf = ctx.state.workflow;
  assert.deepEqual([...wf.nodes.map(n => n.id)], ['b', 'c']);
  assert.deepEqual([...wf.edges.map(e => e.id)], ['e2']);
  assert.deepEqual([wf.nodes[1].rejectTo, wf.nodes[1].onFailure], ['', 'fail']);
  assert.equal(ctx.state.selectedNode, null);
  assert.equal(run('flowHistory.undo()'), true);
  assert.deepEqual(ctx.state.workflow.nodes.map(n => n.id), ['a', 'b', 'c']);
  assert.equal(ctx.state.workflow.edges.length, 2);
  assert.equal(ctx.state.workflow.nodes[2].rejectTo, 'a');
  assert.equal(run('flowHistory.redo()'), true);
  assert.equal(ctx.state.workflow.nodes.length, 2);
});

test('duplicateNode copies config without edges or reject route', () => {
  const { ctx, run, json } = withWorkflow();
  const copy = json("duplicateNode('c')");
  assert.notEqual(copy.id, 'c');
  assert.equal(copy.name, 'c 副本');
  assert.deepEqual(copy.position, { x: 40, y: 40 });
  assert.deepEqual([copy.rejectTo, copy.onFailure], ['', 'fail']);
  assert.equal(ctx.state.workflow.edges.some(e => e.source === copy.id || e.target === copy.id), false);
  assert.equal(ctx.state.selectedNode, copy.id);
  assert.equal(run("duplicateNode('af-preview-build')"), null);
});

test('read-only workflows reject graph edits', () => {
  const wf = { ...graph(), projectId: 'other' };
  const { run, toasts } = withWorkflow(wf);
  assert.equal(run("removeNode('a')"), false);
  assert.equal(run("duplicateNode('a')"), null);
  assert.equal(wf.nodes.length, 3);
  assert.ok(toasts.some(t => t.includes('共享')));
});

test('flowHistory caps at 50, merges bursts by key and resets per workflow', () => {
  const { ctx, run } = withWorkflow();
  assert.equal(run('flowHistory.limit'), 50);
  for (let i = 0; i < 60; i++) {
    run('flowHistory.record()');
    ctx.state.workflow.nodes[0].position.x = i + 1;
  }
  let undos = 0;
  while (run('flowHistory.undo()')) undos++;
  assert.equal(undos, 50);
  assert.equal(ctx.state.workflow.nodes[0].position.x, 10, 'the oldest 10 snapshots were dropped');

  run('flowHistory.clear()');
  run("flowHistory.record({merge:'p:a:name'})");
  run("flowHistory.record({merge:'p:a:name'})");
  run("flowHistory.record({merge:'p:a:name'})");
  assert.equal(run('flowHistory.undo()'), true);
  assert.equal(run('flowHistory.canUndo()'), false, 'typing bursts collapse into one entry');

  run('flowHistory.record()');
  ctx.state.workflow = { ...graph(), id: 'wf2' };
  assert.equal(run('flowHistory.canUndo()'), false, 'switching workflow drops history');
});

test('toWorld converts client points through pan and zoom', () => {
  const { run, json } = load();
  run('flowViewport._set({zoom:2,x:30,y:10})');
  assert.deepEqual(json('flowViewport.toWorld(330,260)'), { x: 100, y: 100 });
  run('flowViewport._set({zoom:.5,x:0,y:0})');
  assert.deepEqual(json('flowViewport.toWorld(150,100)'), { x: 100, y: 100 });
});

test('the SOP switcher groups own, shared and generic workflows and keeps an unsaved draft', () => {
  const workflows = [
    { id: 'own', name: '本项目流程', projectId: 'p1', nodes: [] },
    { id: 'shared', name: '共享流程', projectId: 'p2', nodes: [] },
    { id: 'tpl', name: '模板', generic: true, nodes: [] },
  ];
  const { ctx, json } = load({ workflows });
  ctx.state.workflow = { id: 'draft', name: 'AI 草稿', projectId: 'p1', nodes: [] };
  const groups = json('flowPalette.switcherGroups()').map(g => [g.title, g.items.map(w => w.id)]);
  assert.deepEqual(groups, [['本项目', ['draft', 'own']], ['共享给本项目', ['shared']], ['通用模板 · 只读', ['tpl']]]);
});

test('the node library search filters roles and built-in items and shows an empty state', () => {
  const item = text => fakeEl({ textContent: text });
  const control = [item('脚本 Shell / Python 命令'), item('子 SOP 跑完另一条流程再继续'), item('预览构建 构建静态站点并归档')];
  const collab = [item('人工门禁 停下等待人工确认')];
  const groups = [
    fakeEl({ dataset: { palGroup: 'role' } }),
    fakeEl({ dataset: { palGroup: 'control' }, querySelectorAll: () => control }),
    fakeEl({ dataset: { palGroup: 'collab' }, querySelectorAll: () => collab }),
  ];
  const q = fakeEl(), roles = fakeEl(), empty = fakeEl({ hidden: true }), count = fakeEl();
  const elements = { flowPaletteQ: q, flowPaletteRoles: roles, flowPaletteEmpty: empty, flowPaletteRoleCount: count, flowPalette: fakeEl() };
  const agents = [{ id: 'dev', name: '开发工程师', projectId: 'p1' }, { id: 'qa', name: '测试工程师', projectId: 'p1' }];
  const { ctx, run } = load({ elements, groups, agents });
  ctx.state.workflow = graph();

  q.value = '测试';
  run('flowPalette.render()');
  assert.match(roles.innerHTML, /data-agent-id="qa"/);
  assert.doesNotMatch(roles.innerHTML, /data-agent-id="dev"/);
  assert.equal(count.textContent, '1');
  assert.deepEqual(control.map(i => i.hidden), [true, true, true]);
  assert.equal(groups[1].hidden, true, 'a group without matches is hidden');
  assert.equal(empty.hidden, true);

  q.value = '脚本';
  run('flowPalette.render()');
  assert.equal(roles.innerHTML, '');
  assert.equal(groups[0].hidden, true);
  assert.deepEqual(control.map(i => i.hidden), [false, true, true]);
  assert.equal(groups[2].hidden, true);

  q.value = '不存在的节点';
  run('flowPalette.render()');
  assert.equal(empty.hidden, false);
  assert.match(empty.textContent, /没有匹配「不存在的节点」/);

  q.value = '';
  run('flowPalette.render()');
  assert.match(roles.innerHTML, /data-agent-id="dev"[\s\S]*data-agent-id="qa"/);
  assert.match(roles.innerHTML, /role="button" tabindex="0"/, 'library items are keyboard focusable');
  assert.equal(empty.hidden, true);
  assert.deepEqual([...control, ...collab].map(i => i.hidden), [false, false, false, false]);
});

test('the SOP switcher query filters by name or id and reports no match', () => {
  const workflows = [
    { id: 'wf-build', name: '构建发布', projectId: 'p1', nodes: [] },
    { id: 'wf-review', name: '代码评审', projectId: 'p2', nodes: [] },
    { id: 'tpl', name: '通用发布模板', generic: true, nodes: [] },
  ];
  const q = fakeEl(), list = fakeEl();
  const { ctx, run } = load({ workflows, elements: { flowSwitchQ: q, flowSwitchList: list } });
  ctx.state.workflow = workflows[0];
  const ids = () => [...list.innerHTML.matchAll(/data-switch-workflow="([^"]+)"/g)].map(m => m[1]);

  run('flowPalette.renderSwitcher()');
  assert.deepEqual(ids(), ['wf-build', 'wf-review', 'tpl']);
  q.value = '发布';
  run('flowPalette.renderSwitcher()');
  assert.deepEqual(ids(), ['wf-build', 'tpl']);
  assert.doesNotMatch(list.innerHTML, /共享给本项目/, 'empty groups drop their heading');
  q.value = 'WF-REVIEW';
  run('flowPalette.renderSwitcher()');
  assert.deepEqual(ids(), ['wf-review']);
  q.value = 'zzz';
  run('flowPalette.renderSwitcher()');
  assert.deepEqual(ids(), []);
  assert.match(list.innerHTML, /没有匹配的 SOP/);
});

test('flowHistory.current changes on every later edit so a stale toast undo is refused', () => {
  const { ctx, run } = withWorkflow();
  run("removeNode('a')");
  const at = run('flowHistory.current()');
  assert.equal(run('flowHistory.current()'), at, 'reading does not bump');
  ctx.state.workflow.nodes[0].position.x = 99;
  run('flowHistory.record()');
  assert.notEqual(run('flowHistory.current()'), at);
  for (let i = 0; i < 60; i++) run('flowHistory.record()');
  const capped = run('flowHistory.current()');
  run('flowHistory.record()');
  assert.notEqual(run('flowHistory.current()'), capped, 'still changes once the stack is at its cap');
});
test('crowded legacy rows are widened so nodes stop overlapping; normal layouts stay put', () => {
  const row = (pitch, y = 200) => ['a', 'b', 'c'].map((id, i) => node(id, { position: { x: 80 + i * pitch, y } }));
  const { ctx, run } = withWorkflow({ ...graph(), nodes: row(240) });
  assert.equal(run('spreadCrowdedNodes(state.workflow)'), true);
  assert.deepEqual(ctx.state.workflow.nodes.map(n => n.position.x), [80, 360, 640]);
  assert.equal(ctx.state.workflow.nodes[1].position.y, 200);
  assert.equal(run('spreadCrowdedNodes(state.workflow)'), false, 'idempotent');
  assert.equal(ctx.state.workflowDirty, undefined, 'a display fix does not mark the SOP unsaved');

  ctx.state.workflow = { ...graph(), nodes: row(180) };
  assert.equal(run('spreadCrowdedNodes(state.workflow)'), true);
  assert.ok(ctx.state.workflow.nodes[1].position.x - ctx.state.workflow.nodes[0].position.x >= 280);

  ctx.state.workflow = { ...graph(), nodes: row(320) };
  assert.equal(run('spreadCrowdedNodes(state.workflow)'), false);
  assert.deepEqual(ctx.state.workflow.nodes.map(n => n.position.x), [80, 400, 720]);

  ctx.state.workflow = { ...graph(), nodes: row(20) };
  assert.equal(run('spreadCrowdedNodes(state.workflow)'), false, 'deliberate pile-ups are not blown apart');

  ctx.state.workflow = { ...graph(), nodes: ['a', 'b'].map((id, i) => node(id, { position: { x: 80, y: 80 + i * 110 } })) };
  assert.equal(run('spreadCrowdedNodes(state.workflow)'), true);
  assert.equal(ctx.state.workflow.nodes[1].position.y - ctx.state.workflow.nodes[0].position.y, 160);
});
