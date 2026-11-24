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
      const build=await beginAIOrchestration();
      const incoming={...structuredClone(original),name:'Edited delivery',
        nodes:[...original.nodes,{id:'final',name:'Final approval',type:'approval'}],
        edges:[...original.edges,{id:'edge-final',source:'node-2',target:'final',when:'success'}]};
      const ok=await completeAIEdit(build,incoming);
      const failed=await beginAIOrchestration();
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

test('streaming parser handles partial drafts', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const values = JSON.parse(app.evaluate(`
    JSON.stringify({
      partial: parseStreamingWorkflow('{"name":"Partial","nodes":[{"id":"a","name":"Plan","type":"agent"},{"id":"b","name":"Review"'),
      body: aiDraftBodyText('prefix "body":{"name":"X","nodes":[]} suffix')
    })
  `));
  assert.equal(values.partial.name, 'Partial');
  assert.deepEqual(values.partial.nodes.map((node) => node.id), ['a'], 'an unfinished node waits for its closing brace');
  assert.match(values.body, /"name":"X"/);
});

test('AI edit keeps streamed node positions and applies only the final graph', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = await app.evaluate(`
    (async () => {
      const build = await beginAIOrchestration('add two approvals');
      const streamedNode = {id: 'first-approval', name: 'First approval', type: 'approval'};
      ingestStreamingWorkflow({nodes: [streamedNode], edges: []});
      await drainAIBuild();
      const placed = state.workflow.nodes.find(node => node.id === streamedNode.id).position;

      const finalNode = {id: 'final-approval', name: 'Final approval', type: 'approval'};
      const finalEdge = {id: 'to-final', source: streamedNode.id, target: finalNode.id, when: 'success'};
      ingestStreamingWorkflow({
        nodes: [{...finalNode, name: 'Preview label'}, {id: 'extra', name: 'Extra', type: 'approval'}],
        edges: [{id: 'to-extra', source: finalNode.id, target: 'extra'}],
      });
      const completed = await completeAIEdit(build, {
        nodes: [...build.base.nodes, streamedNode, finalNode],
        edges: [...build.base.edges, finalEdge],
      });
      const {nodes, edges} = state.workflow;
      const final = nodes.find(node => node.id === finalNode.id);
      const first = nodes.find(node => node.id === streamedNode.id);
      return {
        completed,
        keptPosition: first.position.x === placed.x && first.position.y === placed.y,
        finalName: final.name,
        finalPositioned: Number.isFinite(final.position.x) && Number.isFinite(final.position.y),
        hasFinalEdge: edges.some(edge => edge.id === finalEdge.id),
        hasExtra: nodes.some(node => node.id === 'extra') || edges.some(edge => edge.id === 'to-extra'),
      };
    })()
  `);
  assert.equal(result.completed, true);
  assert.equal(result.keptPosition, true);
  assert.equal(result.finalName, 'Final approval');
  assert.equal(result.finalPositioned, true);
  assert.equal(result.hasFinalEdge, true);
  assert.equal(result.hasExtra, false);
});
