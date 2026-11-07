import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('flow designer adds, duplicates, connects, rejects, renames, arranges, and removes nodes', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    switchView('flow');
    state.workflow={...clone(state.data.workflows[0]),id:'editable-flow',projectId:state.projectFilter,
      nodes:[
        {id:'plan',name:'Plan',type:'agent',agentId:state.data.agents[0]?.id||'',position:{x:80,y:80},artifactOutputs:[],onFailure:'fail'},
        {id:'gate',name:'Gate',type:'approval',position:{x:400,y:80},artifactOutputs:[],onFailure:'fail'}
      ],edges:[],sandboxProfileId:state.data.sandboxes[0]?.id||'local'};
    state.workflowDirty=false;state.selectedNode='plan';renderCanvas();renderProperties();
  `);
  assert.equal(app.evaluate(`connect('plan','gate')`), true);
  assert.equal(app.evaluate(`connect('gate','plan')`), false);
  assert.equal(app.evaluate(`setRejectTo('gate','plan')`), true);
  const copyId = app.evaluate(`duplicateNode('plan').id`);
  app.evaluate(`
    addNode('script',{at:{x:700,y:240}});
    addNode('approval',{at:{x:700,y:420}});
    addNode('sop',{at:{x:900,y:80}});
    addNode('preview',{at:{x:900,y:280}});
    addNode('preview',{at:{x:920,y:300}});
    arrange();setFlowView('check');setFlowView('canvas');
    startNodeRename('plan');
  `);
  const rename = app.document.querySelector('.fd-rename');
  rename.value = 'Plan updated';
  rename.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  app.evaluate(`openNodeDrawer('gate');closeNodeDrawer();removeNode(${JSON.stringify(copyId)})`);
  await settle(6);
  assert.equal(app.evaluate(`state.workflow.nodes.find(n=>n.id==='plan').name`), 'Plan updated');
  assert.ok(app.evaluate(`flowDiagnostics().length`) > 2);
});

test('flow node metadata, properties, artifact editors, menus, and edge deletion are interactive', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    switchView('flow');
    state.workflow={...clone(state.data.workflows[0]),id:'properties-flow',projectId:state.projectFilter,
      nodes:[
        {id:'agent-node',name:'Agent',type:'agent',agentId:state.data.agents[0]?.id||'',position:{x:80,y:80},
          artifactOutputs:[{name:'report',path:'report.md',required:true}],onFailure:'reject',rejectTo:'script-node',
          retryStrategy:'targeted_fix',maxAttempts:2,maxReworks:2},
        {id:'script-node',name:'Script',type:'script',scriptLanguage:'python',script:'print("ok")',
          position:{x:420,y:80},artifactOutputs:[],onFailure:'fail'},
        {id:'sop-node',name:'Nested',type:'sop',sopWorkflowId:state.data.workflows[1]?.id||'missing',
          position:{x:740,y:80},artifactOutputs:[],onFailure:'fail'}
      ],edges:[{id:'edge-1',source:'agent-node',target:'script-node',when:'success'}],
      sandboxProfileId:state.data.sandboxes[0]?.id||'local'};
    state.selectedNode='agent-node';renderCanvas();renderProperties();
  `);
  const menuButton = app.document.querySelector('[data-id="agent-node"] [data-node-menu]');
  menuButton?.click();
  app.document.querySelector('#flowNodeMenu [data-a="edit"]')?.click();
  app.evaluate(`
    nodeMeta(state.workflow.nodes[0]);nodeMeta(state.workflow.nodes[1]);nodeMeta(state.workflow.nodes[2]);
    artifactRowsHTML(state.workflow.nodes[0]);
    listEditorHTML(['one','two'],'item');
    ensureNodeDefaults(state.workflow.nodes[0]);
    renderAgentSummary();renderAgentPreview(state.data.agents[0]);
    deleteEdgeById('edge-1');deleteRejectEdgeByNodeId('agent-node');
  `);
  await settle(4);
  assert.equal(app.evaluate(`state.workflow.edges.length`), 0);
  assert.equal(app.evaluate(`state.workflow.nodes[0].rejectTo`), '');
  assert.match(app.document.querySelector('#properties').textContent, /Agent|角色|节点/);
});
