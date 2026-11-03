import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

function fakeTransfer(initial = {}) {
  const data = { ...initial };
  return {
    get types() { return Object.keys(data); },
    setData(type, value) { data[type] = value; },
    getData(type) { return data[type] || ''; },
    dropEffect: 'none',
    effectAllowed: 'all',
  };
}

function dragEvent(app, type, transfer, init = {}) {
  const event = new app.window.MouseEvent(type, { bubbles: true, cancelable: true, ...init });
  Object.defineProperty(event, 'dataTransfer', { value: transfer });
  return event;
}

function editableFlow(app) {
  app.evaluate(`
    switchView('flow');
    state.workflow={...clone(state.data.workflows[0]),id:'palette-flow',projectId:state.projectFilter,
      nodes:[{id:'plan',name:'Plan',type:'agent',agentId:state.data.agents[0]?.id||'',position:{x:80,y:80},artifactOutputs:[],onFailure:'fail'}],
      edges:[],sandboxProfileId:state.data.sandboxes[0]?.id||'local'};
    state.workflowDirty=false;renderCanvas();renderProperties();flowPalette.bind();flowPalette.render();
  `);
}

test('flow palette collapses, adds nodes by click, keyboard and drag-and-drop', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  editableFlow(app);
  const doc = app.document;
  const nodeCount = () => app.evaluate(`state.workflow.nodes.length`);

  const collapse = doc.getElementById('flowPaletteCollapse');
  collapse.click();
  assert.ok(doc.getElementById('flowPalette').classList.contains('collapsed'));
  assert.equal(app.window.localStorage.getItem('agentflow.flowPaletteCollapsed'), '1');
  assert.equal(collapse.dataset.tip, '展开节点库');
  collapse.click();
  assert.equal(doc.getElementById('flowPalette').classList.contains('collapsed'), false);

  const search = doc.getElementById('flowPaletteQ');
  search.value = 'zz-no-such-node';
  search.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(doc.getElementById('flowPaletteEmpty').hidden, false);
  search.value = '';
  search.dispatchEvent(new app.window.Event('input', { bubbles: true }));

  const item = doc.querySelector('#flowPaletteBody .fd-pal-item[data-add]');
  assert.ok(item, 'palette renders node items');
  let before = nodeCount();
  item.click();
  assert.equal(nodeCount(), before + 1);

  before = nodeCount();
  item.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  item.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'x', bubbles: true }));
  assert.equal(nodeCount(), before + 1);

  const transfer = fakeTransfer();
  item.dispatchEvent(dragEvent(app, 'dragstart', transfer));
  const mime = app.evaluate(`flowPalette.MIME`);
  assert.equal(JSON.parse(transfer.getData(mime)).type, item.dataset.add);

  const canvas = doc.getElementById('canvas');
  canvas.dispatchEvent(dragEvent(app, 'dragover', transfer));
  assert.ok(canvas.classList.contains('dragover'));
  canvas.dispatchEvent(dragEvent(app, 'dragleave', transfer));
  assert.equal(canvas.classList.contains('dragover'), false);
  canvas.dispatchEvent(dragEvent(app, 'dragover', fakeTransfer({ 'text/plain': 'x' })));
  assert.equal(canvas.classList.contains('dragover'), false);

  before = nodeCount();
  canvas.dispatchEvent(dragEvent(app, 'drop', fakeTransfer({ [mime]: 'not json' })));
  canvas.dispatchEvent(dragEvent(app, 'drop', fakeTransfer()));
  assert.equal(nodeCount(), before);
  canvas.dispatchEvent(dragEvent(app, 'drop', transfer, { clientX: 300, clientY: 200 }));
  assert.equal(nodeCount(), before + 1);

  app.evaluate(`state.workflow.projectId='other-project';state.workflow.generic=true;`);
  const readonlyTransfer = fakeTransfer();
  item.dispatchEvent(dragEvent(app, 'dragstart', readonlyTransfer));
  app.evaluate(`state.workflow.projectId=state.projectFilter;delete state.workflow.generic;`);

  doc.querySelector('#flowPaletteBody [data-pal-agents]')?.click();
  await settle(4);
});

test('SOP switcher lists, filters, switches and opens the template wizard', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  editableFlow(app);
  const doc = app.document;

  const groups = app.evaluate(`flowPalette.switcherGroups().map(g=>g.items.length)`);
  assert.equal(groups.length, 3);
  assert.ok(groups[0] >= 1, 'the open workflow is listed under the project');

  doc.getElementById('flowIdent').click();
  assert.equal(app.evaluate(`flowPalette.isSwitcherOpen()`), true);
  assert.equal(doc.getElementById('flowSwitch').getAttribute('aria-expanded'), 'true');
  assert.match(doc.getElementById('flowSwitchList').innerHTML, /data-switch-workflow/);

  const q = doc.getElementById('flowSwitchQ');
  q.value = 'zz-no-such-sop';
  q.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.match(doc.getElementById('flowSwitchList').textContent, /没有匹配的 SOP/);
  q.value = '';
  q.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  q.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  assert.equal(app.evaluate(`flowPalette.isSwitcherOpen()`), false);

  doc.getElementById('flowIdent').click();
  doc.body.dispatchEvent(new app.window.MouseEvent('pointerdown', { bubbles: true }));
  assert.equal(app.evaluate(`flowPalette.isSwitcherOpen()`), false);
  assert.equal(app.evaluate(`flowPalette.closeSwitcher()`), false);

  app.evaluate(`flowPalette.setSwitcher(true)`);
  const other = [...doc.querySelectorAll('#flowSwitchList [data-switch-workflow]')]
    .find((el) => el.dataset.switchWorkflow !== 'palette-flow');
  if (other) other.click();
  await settle(6);

  app.evaluate(`switchView('flow');flowPalette.setSwitcher(true)`);
  doc.getElementById('newSOPFromFlow')?.click();
  await settle(2);
  app.evaluate(`flowPalette.setSwitcher(true)`);
  doc.getElementById('flowFromTemplate')?.click();
  await settle(2);
  assert.equal(app.evaluate(`flowPalette.isSwitcherOpen()`), false);
});
