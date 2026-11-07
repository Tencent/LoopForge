import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

test('orchestrator normalizes graphs, parses streamed drafts, and preserves preview contracts', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`(()=>{
    const graph=normalizeAIWorkflow({nodes:[{id:'a',name:'Plan'},{id:'b',type:'approval',position:{x:9,y:8}}],
      edges:[{source:'a',target:'b'},{source:'missing',target:'b'},{source:'a',target:'a'}]});
    const escaped=aiDecodePartialString('hello\\\\nworld"',0);
    const encoded=JSON.stringify({body:JSON.stringify({name:'Quoted',nodes:[{id:'q',name:'Q'}],edges:[]})});
    const parsed=parseStreamingWorkflow(encoded);
    const donor={preview:{enabled:true,model:'gpt'},nodes:[
      {id:'a',name:'Build'},{id:'af-preview-build',name:'Preview',agentId:'af-preview-builder'}
    ],edges:[{id:'to-preview',source:'a',target:'af-preview-build'}]};
    const carried=ensurePreviewCarried({nodes:[{id:'a',name:'Build'}],edges:[]},[donor],'保留前端预览');
    const removed=ensurePreviewCarried({nodes:[],edges:[]},[donor],'删除前端预览');
    const merged=mergeAIEdit({name:'Old',nodes:[{id:'old',name:'Build',script:'keep',position:{x:1,y:2}}],edges:[]},
      {name:'New',nodes:[{id:'new',name:'Build',type:'script'}],edges:[]},'调整',[]);
    return {graph,escaped,parsed,carried,removed,merged,
      fresh:aiWantsFreshWorkflow('从头搭一条流程'),drop:aiWantsPreviewRemoved('请移除前端预览节点')};
  })()`);
  assert.equal(result.graph.edges.length, 1);
  assert.equal(`${result.graph.nodes[0].position.x},${result.graph.nodes[0].position.y}`, '80,80');
  assert.equal(result.escaped, 'hello\nworld');
  assert.equal(result.parsed.name, 'Quoted');
  assert.equal(result.parsed.nodes[0].id, 'q');
  assert.equal(result.carried.preview.model, 'gpt');
  assert.ok(result.carried.nodes.some((n) => n.id === 'af-preview-build'));
  assert.equal(result.removed.nodes.length, 0);
  assert.equal(result.merged.nodes[0].id, 'old');
  assert.equal(result.merged.nodes[0].script, 'keep');
  assert.equal(result.fresh && result.drop, true);
});

test('orchestrator renders scenarios and consumes an AI draft into a generated workflow', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`switchView('flow');state.workflow={...state.workflow,nodes:[],edges:[]};state.workflowDirty=false;renderAll()`);
  app.document.querySelector('#openAIOrchestrator')?.click();
  const suggestions = [...app.document.querySelectorAll('#aiOrchestratorSuggest button')];
  assert.equal(suggestions.length, 4);
  suggestions[0].click();
  assert.match(app.document.querySelector('#aiOrchestratorInput').value, /需求澄清/);
  app.document.querySelector('#aiSuggestMore')?.click();
  assert.match(app.document.querySelector('#aiOrchestratorSuggest').textContent, /前后端并行开发/);

  const raw = { name: 'Streamed', nodes: [{ id: 'one', name: 'Only step', type: 'approval' }], edges: [] };
  app.router.post('/api/assistant/chat', {
    sessionId: 'ai-1', message: '完成', drafts: [{ tool: 'workflows_save', body: raw }],
  });
  app.document.querySelector('#aiOrchestratorInput').value = '搭建一个审批流程';
  await app.evaluate(`runAIOrchestrator()`);
  assert.equal(app.evaluate(`state.workflow.name`), 'Streamed');
  assert.equal(app.evaluate(`state.workflow.nodes[0].name`), 'Only step');
  assert.match(app.document.querySelector('#aiOrchestratorLog').textContent, /完成/);
});
