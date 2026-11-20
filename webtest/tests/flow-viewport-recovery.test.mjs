import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

async function flowApp(t) {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    switchView('flow');
    state.workflow={...clone(state.data.workflows[0]),id:'viewport-flow',projectId:state.projectFilter,
      nodes:[
        {id:'a',name:'A',type:'script',script:'echo a',position:{x:80,y:80},artifactOutputs:[],onFailure:'fail'},
        {id:'b',name:'B',type:'script',script:'echo b',position:{x:560,y:80},artifactOutputs:[],onFailure:'fail'}
      ],edges:[{id:'e',source:'a',target:'b',when:'success'}],
      sandboxProfileId:state.data.sandboxes[0]?.id||'local'};
    state.workflowDirty=false;renderCanvas();
    const c=document.getElementById('canvas');
    Object.defineProperty(c,'clientWidth',{configurable:true,value:960});
    Object.defineProperty(c,'clientHeight',{configurable:true,value:640});
  `);
  return app;
}

test('a non-finite viewport heals instead of freezing pan and zoom', async (t) => {
  const app = await flowApp(t);
  app.evaluate(`flowViewport._set({x:NaN,y:NaN,zoom:NaN});flowViewport.zoomBy(1.2)`);
  const view = JSON.parse(app.evaluate(`JSON.stringify({
    x:flowViewport.x,y:flowViewport.y,zoom:flowViewport.zoom,
    transform:document.getElementById('canvasStage').style.transform,
    label:document.getElementById('canvasZoomValue').textContent})`));
  assert.ok(Number.isFinite(view.x) && Number.isFinite(view.y) && Number.isFinite(view.zoom));
  assert.doesNotMatch(view.transform, /NaN|Infinity/);
  assert.match(view.transform, new RegExp(`scale\\(${view.zoom}\\)`));
  assert.equal(view.label, `${Math.round(view.zoom * 100)}%`);
});

test('freeSpot returns finite coordinates from a broken viewport', async (t) => {
  const app = await flowApp(t);
  app.evaluate(`flowViewport._set({x:NaN,y:NaN,zoom:NaN});window.__spot=flowViewport.freeSpot()`);
  assert.equal(app.evaluate(`Number.isFinite(window.__spot.x)&&Number.isFinite(window.__spot.y)`), true);
});

test('a node with a broken position is repaired instead of freezing fit', async (t) => {
  const app = await flowApp(t);
  app.evaluate(`
    state.workflow.nodes.push({id:'broken',name:'Broken',type:'script',script:'echo x',position:{x:NaN,y:NaN},artifactOutputs:[],onFailure:'fail'});
    state.workflow.edges.push({id:'e2',source:'b',target:'broken',when:'success'});
    renderCanvas();
  `);
  assert.equal(app.evaluate(`state.workflow.nodes.every(n=>Number.isFinite(n.position.x)&&Number.isFinite(n.position.y))`), true);
  assert.equal(app.evaluate(`/NaN|Infinity/.test([...document.querySelectorAll('#nodes .flow-node')].map(el=>el.getAttribute('style')).join(''))`), false);
  app.evaluate(`flowViewport.fit({smooth:false})`);
  assert.equal(app.evaluate(`[flowViewport.x,flowViewport.y,flowViewport.zoom].every(Number.isFinite)`), true);
  assert.equal(app.evaluate(`/NaN|Infinity/.test(document.getElementById('canvasStage').style.transform)`), false);
});

test('AI edits keep a node position when the draft omits a coordinate', async (t) => {
  const app = await flowApp(t);
  const kept = app.evaluate(`(() => {
    const base={id:'w',nodes:[{id:'A',name:'A',type:'agent',position:{x:10,y:20}}],edges:[]};
    const out=mergeAIEdit(base,{nodes:[{id:'A',name:'A',type:'agent',position:{x:99}}],edges:[]},'',[]);
    return JSON.stringify(out.nodes[0].position);
  })()`);
  assert.equal(kept, '{"x":10,"y":20}');
});
