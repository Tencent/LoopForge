import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('AI canvas builder places nodes, wires edges, relayouts, and completes', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.workflow={...state.workflow,nodes:[],edges:[]};state.workflowDirty=false;switchView('flow')`);
  const raw = {
    id: 'flow-ai', projectId: 'project-1', name: 'AI Delivery', description: 'Generated',
    nodes: [
      { id: 'plan', name: 'Plan', type: 'agent', agentId: 'agent-1' },
      { id: 'implement', name: 'Implement', type: 'agent', agentId: 'agent-1' },
      { id: 'review', name: 'Review', type: 'approval' },
    ],
    edges: [
      { id: 'edge-plan', source: 'plan', target: 'implement', when: 'success' },
      { id: 'edge-review', source: 'implement', target: 'review', when: 'success' },
    ],
  };
  const ok = await app.evaluate(`playWorkflowOnCanvas(${JSON.stringify(raw)},{confirmed:true,source:'assistant'})`);
  assert.equal(ok, true);
  assert.equal(app.evaluate(`state.workflow.nodes.length`), 3);
  assert.equal(app.evaluate(`state.workflow.edges.length`), 2);
  assert.match(app.document.querySelector('#flowView').textContent, /Plan|Implement|Review/);
});

test('AI edit mode merges existing IDs, adds nodes, and restores failed builds', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = await app.evaluate(`
    (async()=>{
      const original=structuredClone(state.workflow);
      const build=await beginAIOrchestration('add final approval');
      const incoming={...structuredClone(original),name:'Edited delivery',
        nodes:[...original.nodes,{id:'final',name:'Final approval',type:'approval'}],
        edges:[...original.edges,{id:'edge-final',source:'node-2',target:'final',when:'success'}]};
      const ok=await completeAIEdit(build,incoming);
      const failed=await beginAIOrchestration('adjust without result');
      const noResult=await completeAIEdit(failed,null);
      restoreAIBuildFailure(failed);
      return {ok,noResult,count:state.workflow.nodes.length};
    })()
  `);
  assert.equal(result.ok, true);
  assert.equal(result.noResult, false);
  assert.ok(result.count >= 2);
  await settle();
});

test('preview carry-forward and streaming parser handle partial drafts', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const values = app.evaluate(`
    (() => {
      const donor={name:'Preview donor',preview:{enabled:true},nodes:[
        {id:'build',name:'Build',type:'agent'},{id:'af-preview-build',name:'Preview',type:'agent',agentId:'af-preview-builder'}
      ],edges:[{source:'build',target:'af-preview-build',when:'success'}]};
      const kept=ensurePreviewCarried({name:'Next',nodes:[{id:'build',name:'Build',type:'agent'}],edges:[]},[donor],'keep preview');
      const removed=ensurePreviewCarried({name:'Next',nodes:[],edges:[]},[donor],'去掉预览节点');
      const partial=parseStreamingWorkflow('{"name":"Partial","nodes":[{"id":"a","name":"Plan","type":"agent"},{"id":"b","name":"Review"');
      return {kept,removed,partial,body:aiDraftBodyText('prefix "body":{"name":"X","nodes":[]} suffix')};
    })()
  `);
  assert.ok(values.kept.nodes.some((node) => node.id === 'af-preview-build'));
  assert.ok(!values.removed.nodes.some((node) => node.id === 'af-preview-build'));
});
