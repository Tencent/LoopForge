import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

async function flowApp(t) {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    switchView('flow');
    state.workflow={...clone(state.data.workflows[0]),id:'resize-flow',projectId:state.projectFilter,
      nodes:[
        {id:'a',name:'A',type:'script',script:'echo a',position:{x:80,y:80},artifactOutputs:[],onFailure:'fail'},
        {id:'b',name:'B',type:'script',script:'echo b',position:{x:360,y:80},artifactOutputs:[],onFailure:'fail'}
      ],edges:[{id:'e',source:'a',target:'b',when:'success'}],
      sandboxProfileId:state.data.sandboxes[0]?.id||'local'};
    state.workflowDirty=false;renderCanvas();
  `);
  return app;
}

test('cards default to the compact width and render a stored footprint', async (t) => {
  const app = await flowApp(t);
  assert.equal(app.evaluate(`flowNodeEl('a').style.width`), '200px');
  assert.equal(app.evaluate(`flowNodeEl('a').style.height`), '');
  app.evaluate(`state.workflow.nodes[0].size={w:320,h:260};renderCanvas()`);
  assert.equal(app.evaluate(`flowNodeEl('a').style.width`), '320px');
  assert.equal(app.evaluate(`flowNodeEl('a').style.height`), '260px');
  assert.equal(app.evaluate(`JSON.stringify(flowNodeSize(state.workflow.nodes[0]))`), '{"w":320,"h":260}');
});

test('free resizing clamps both axes and survives a re-render', async (t) => {
  const app = await flowApp(t);
  app.evaluate(`setFlowNodeSize(state.workflow.nodes[0],500,900)`);
  assert.equal(app.evaluate(`JSON.stringify(state.workflow.nodes[0].size)`), '{"w":480,"h":640}');
  app.evaluate(`setFlowNodeSize(state.workflow.nodes[0],60,40)`);
  assert.equal(app.evaluate(`JSON.stringify(state.workflow.nodes[0].size)`), '{"w":180,"h":96}');
  app.evaluate(`renderCanvas()`);
  assert.equal(app.evaluate(`flowNodeEl('a').style.width`), '180px');
  assert.equal(app.evaluate(`flowNodeEl('a').style.height`), '96px');
  assert.equal(app.evaluate(`flowNodeWidth(state.workflow.nodes[0])`), 180);
});

test('tall cards expand the node detail instead of showing whitespace', async (t) => {
  const app = await flowApp(t);
  app.evaluate(`state.workflow.nodes[0].script='echo one\\necho two';renderCanvas()`);
  assert.equal(app.evaluate(`!!flowNodeEl('a').querySelector('.fd-detail')`), false);
  app.evaluate(`setFlowNodeSize(state.workflow.nodes[0],260,280);renderCanvas()`);
  assert.equal(app.evaluate(`!!flowNodeEl('a').querySelector('.fd-detail')`), true);
  assert.match(app.evaluate(`flowNodeEl('a').querySelector('.fd-detail').textContent`), /echo one/);
  app.evaluate(`setFlowNodeSize(state.workflow.nodes[0],260,120);renderCanvas()`);
  assert.equal(app.evaluate(`!!flowNodeEl('a').querySelector('.fd-detail')`), false);
});

test('only the selected card shows the eight-handle resize frame', async (t) => {
  const app = await flowApp(t);
  assert.equal(app.evaluate(`document.querySelectorAll('#nodes .flow-node [data-resize]').length`), 0);
  app.evaluate(`openNodeDrawer('a')`);
  assert.equal(app.evaluate(`document.querySelectorAll('#nodes .flow-node[data-id="a"] [data-resize]').length`), 8);
  assert.equal(app.evaluate(`[...document.querySelectorAll('#nodes .flow-node[data-id="a"] [data-resize]')].every(h=>h.classList.contains('fd-rz'))`), true);
  app.evaluate(`closeNodeDrawer();state.workflow.projectId='someone-else';state.selectedNode='a';renderCanvas()`);
  assert.equal(app.evaluate(`!state.workflow.nodes.length||document.querySelectorAll('#nodes .flow-node [data-resize]').length`), 0);
});
