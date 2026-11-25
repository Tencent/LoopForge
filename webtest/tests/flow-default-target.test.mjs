import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

async function loadRoutingDesigner(t) {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    switchView('flow');
    state.workflow={id:'default-target',projectId:'project-1',name:'Routing',status:'draft',sandboxProfileId:'sandbox-1',nodes:[
      {id:'route',name:'Route',type:'script',script:'echo route',defaultTarget:'large',position:{x:20,y:30}},
      {id:'small',name:'Small',type:'script',script:'echo small',position:{x:300,y:30}},
      {id:'large',name:'Large',type:'script',script:'echo large',position:{x:300,y:150}}
    ],edges:[
      {id:'small-edge',source:'route',target:'small',condition:{type:'route',values:['small']}},
      {id:'large-edge',source:'route',target:'large'}
    ]};
    state.selectedNode='route';renderCanvas();renderProperties();flowHistory.clear();
  `);
  return app;
}

const clearCondition = (app) => {
  const control = app.document.querySelector('[data-route-edge="small-edge"] [data-route-field="type"]');
  control.value = '';
  control.dispatchEvent(new app.window.Event('change'));
};

const edits = [
  ['deleting the last conditional target node', app => app.evaluate(`removeNode('small')`)],
  ['changing the last conditional edge to unconditional', clearCondition],
  ['deleting the last conditional edge', app => app.evaluate(`deleteEdgeById('small-edge')`)],
  ['deleting the default target node', app => app.evaluate(`removeNode('large')`)],
  ['deleting the default edge', app => app.evaluate(`deleteEdgeById('large-edge')`)],
];

for (const [name, edit] of edits) {
  test(`designer clears the default branch when ${name}`, async (t) => {
    const app = await loadRoutingDesigner(t);
    const before = app.evaluate(`JSON.stringify({nodes:state.workflow.nodes,edges:state.workflow.edges})`);
    await edit(app);
    assert.equal(app.evaluate(`state.workflow.nodes[0].defaultTarget`), '');
    const defaultTarget=app.document.querySelector('[data-p="defaultTarget"]');
    if(defaultTarget)assert.equal(defaultTarget.value, '');
    assert.doesNotMatch(app.document.querySelector('#edges').textContent, /默认/);

    assert.equal(app.evaluate(`flowHistory.undo()`), true);
    assert.equal(app.evaluate(`JSON.stringify({nodes:state.workflow.nodes,edges:state.workflow.edges})`), before);
    assert.equal(app.document.querySelector('[data-p="defaultTarget"]').value, 'large');
    assert.equal(app.evaluate(`flowHistory.redo()`), true);
    assert.equal(app.evaluate(`state.workflow.nodes[0].defaultTarget`), '');

    app.router.put('/api/workflows', call => ({ ...call.body, revision: 1 }));
    await app.evaluate(`saveWorkflow()`);
    const request = app.router.calls.find(call => call.path === '/api/workflows' && call.method === 'PUT');
    assert.equal(request.body.nodes[0].defaultTarget, '');
    assert.equal(app.evaluate(`state.workflowDirty`), false);
  });
}

for (const [name, edit] of edits.slice(0, 3)) {
  test(`designer preserves a valid default branch when ${name}`, async (t) => {
    const app = await loadRoutingDesigner(t);
    app.evaluate(`
      state.workflow.nodes.push({id:'medium',name:'Medium',type:'script',script:'echo medium',position:{x:300,y:270}});
      state.workflow.edges.push({id:'medium-edge',source:'route',target:'medium',condition:{type:'route',values:['medium']}});
      renderCanvas();renderProperties();
    `);
    await edit(app);
    assert.equal(app.evaluate(`state.workflow.nodes[0].defaultTarget`), 'large');
    assert.equal(app.document.querySelector('[data-p="defaultTarget"]').value, 'large');
  });
}

test('choosing the last conditional edge as the default clears the default branch', async (t) => {
  const app = await loadRoutingDesigner(t);
  const control = app.document.querySelector('[data-p="defaultTarget"]');
  control.value = 'small';
  control.dispatchEvent(new app.window.Event('input'));
  assert.equal(app.evaluate(`state.workflow.edges[0].condition`), undefined);
  assert.equal(app.evaluate(`state.workflow.nodes[0].defaultTarget`), '');
  assert.equal(app.document.querySelector('[data-p="defaultTarget"]').value, '');
});
