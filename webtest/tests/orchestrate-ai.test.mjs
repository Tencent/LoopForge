import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

// Values from the page's realm compare by content, not by prototype.
const plain = (value) => JSON.parse(JSON.stringify(value));

test('orchestrator normalizes graphs and parses streamed drafts and operations', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`(()=>{
    const graph=normalizeAIWorkflow({nodes:[{id:'a',name:'Plan'},{id:'b',type:'approval',position:{x:9,y:8}}],
      edges:[{source:'a',target:'b'},{source:'missing',target:'b'},{source:'a',target:'a'}]});
    const escaped=aiDecodePartialString('hello\\\\nworld"',0);
    const encoded=JSON.stringify({body:JSON.stringify({name:'Quoted',nodes:[{id:'q',name:'Q'}],edges:[]})});
    const parsed=parseStreamingWorkflow(encoded);
    const ops=parseStreamingOperations('{"message":"x","drafts":[{"tool":"workflows_save","body":{"operations":[{"op":"add_node","node":{"id":"r","name":"Review"}},{"op":"update_node","id":"a","set":{}},{"op":"add_edge","source":"a","target":"r"},{"op":"add_node","node":{"id":"half"');
    const merged=mergeAIEdit({name:'Old',nodes:[{id:'old',name:'Build',script:'keep',position:{x:1,y:2}}],edges:[]},
      {name:'New',nodes:[{id:'old',name:'Build v2',type:'script'},{id:'new',name:'Build',type:'approval'}],edges:[{source:'old',target:'new'}]});
    return {graph,escaped,parsed,ops,merged};
  })()`);
  const plainResult = plain(result);
  assert.equal(result.graph.edges.length, 1);
  assert.equal(`${result.graph.nodes[0].position.x},${result.graph.nodes[0].position.y}`, '80,80');
  assert.equal(result.escaped, 'hello\nworld');
  assert.equal(result.parsed.name, 'Quoted');
  assert.equal(result.parsed.nodes[0].id, 'q');
  assert.deepEqual(plainResult.ops.nodes.map((n) => n.id), ['r'], 'only complete add_node operations are placed');
  assert.deepEqual(plainResult.ops.edges, [{ source: 'a', target: 'r', when: 'success' }]);
  // Ids are stable now: a node with the same name is a new node, not a match.
  assert.deepEqual(plainResult.merged.nodes.map((n) => n.id), ['old', 'new']);
  assert.equal(result.merged.nodes[0].name, 'Build v2');
  assert.equal(result.merged.nodes[0].script, 'keep');
  assert.equal(result.merged.nodes[0].position.x, 1);
  assert.equal(result.merged.edges.length, 1);
});

function bridge(app) {
  const frame = app.document.querySelector('#aiOrchestratorFrame');
  const posted = [];
  frame.contentWindow.postMessage = (data) => posted.push(plain(data));
  const send = (data) => app.evaluate(`handleAIOrchestratorMessage({data:${JSON.stringify(data)},source:$('#aiOrchestratorFrame').contentWindow,origin:location.origin})`);
  return { frame, posted, send };
}

test('starting a turn includes canvas edits made while refreshing roles', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`switchView('flow');renderAll();refreshWorkflowAgents=()=>new Promise(resolve=>{window.finishRoleRefresh=resolve})`);
  app.document.querySelector('#openAIOrchestrator')?.click();
  const { posted, send } = bridge(app);
  const begun = send({ type: 'agentflow-sop-designer-begin', requestId: 'refreshing' });
  app.evaluate(`state.workflow.nodes.push({id:'manual-node',name:'My change',type:'approval',position:{x:100,y:100}});window.finishRoleRefresh()`);
  await begun;
  assert.equal(posted.at(-1).ok, true);
  assert.ok(posted.at(-1).canvas.nodes.some(node => node.id === 'manual-node'));
});

test('the panel embeds the designer chat and builds the streamed draft on an empty canvas', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`switchView('flow');state.workflow={...state.workflow,nodes:[],edges:[]};state.workflowDirty=false;renderAll()`);
  app.document.querySelector('#openAIOrchestrator')?.click();
  const { frame, posted, send } = bridge(app);
  const workflowId = app.evaluate('state.workflow.id');
  assert.match(frame.src, new RegExp(`/chatbot/\\?surface=sop-designer&projectId=[^&]+&workflowId=${workflowId}$`));
  for (const legacy of ['#aiOrchestratorInput', '#aiOrchestratorForm', '#aiOrchestratorSuggest', '#aiOrchestratorRuntime']) {
    assert.equal(app.document.querySelector(legacy), null, `${legacy} still rendered`);
  }

  // A message from another window is ignored.
  await app.evaluate(`handleAIOrchestratorMessage({data:{type:'agentflow-sop-designer-begin',requestId:'x'},source:window,origin:location.origin})`);
  assert.equal(posted.length, 0);

  await send({ type: 'agentflow-sop-designer-begin', requestId: 'r1' });
  const begun = posted.find((m) => m.type === 'agentflow-sop-designer-begun');
  assert.equal(begun.requestId, 'r1');
  assert.equal(begun.ok, true);
  assert.equal(begun.workflowId, workflowId);
  assert.deepEqual(begun.canvas.nodes, []);
  assert.equal(app.evaluate('aiBuild.mode'), 'create');

  await send({ type: 'agentflow-sop-designer-begin', requestId: 'r2' });
  assert.equal(posted.at(-1).ok, false, 'a second turn waits for the running build');

  const body = { id: workflowId, name: 'Streamed', nodes: [{ id: 'one', name: 'Only step', type: 'approval' }, { id: 'two', name: 'Ship', type: 'approval' }], edges: [{ source: 'one', target: 'two', when: 'success' }] };
  await send({ type: 'agentflow-sop-designer-draft', buffer: JSON.stringify({ message: '好', drafts: [{ tool: 'workflows_save', body: { name: 'Streamed', nodes: [body.nodes[0]] } }] }) });
  await send({ type: 'agentflow-sop-designer-reply', draft: { body } });
  assert.equal(app.evaluate('state.workflow.name'), 'Streamed');
  assert.deepEqual(plain(app.evaluate('state.workflow.nodes.map(n=>n.id)')), ['one', 'two']);
  assert.equal(app.evaluate('state.workflow.edges.length'), 1);
  assert.equal(app.evaluate('aiBuild'), null);
});

test('an edit keeps the canvas, applies the expanded draft, and a failed turn restores it', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`switchView('flow');renderAll()`);
  app.document.querySelector('#openAIOrchestrator')?.click();
  const { posted, send } = bridge(app);
  const original = app.evaluate('structuredClone(state.workflow)');
  assert.ok(original.nodes.length > 0);

  await send({ type: 'agentflow-sop-designer-begin', requestId: 'e1' });
  assert.equal(posted.at(-1).ok, true);
  assert.equal(posted.at(-1).canvas.nodes.length, original.nodes.length, 'the full canvas goes to the server');
  assert.equal(app.evaluate('aiBuild.mode'), 'edit');
  const last = original.nodes.at(-1).id;
  await send({ type: 'agentflow-sop-designer-draft', buffer: `{"drafts":[{"tool":"workflows_save","body":{"operations":[{"op":"add_node","node":{"id":"final","name":"Final approval","type":"approval"}}` });
  assert.ok(app.evaluate(`state.workflow.nodes.some(n=>n.id==='final')`), 'an added node appears while the edit streams');
  const body = { ...original, nodes: [...original.nodes, { id: 'final', name: 'Final approval', type: 'approval' }], edges: [...original.edges, { id: 'edge-final', source: last, target: 'final', when: 'success' }] };
  await send({ type: 'agentflow-sop-designer-reply', draft: { body } });
  assert.equal(app.evaluate('state.workflow.nodes.length'), original.nodes.length + 1);
  assert.equal(app.evaluate(`state.workflow.nodes.find(n=>n.id===${JSON.stringify(original.nodes[0].id)}).position.x`), original.nodes[0].position.x);
  assert.ok(app.evaluate(`Number.isFinite(state.workflow.nodes.find(n=>n.id==='final').position.x)`));

  const edited = app.evaluate('structuredClone(state.workflow)');
  await send({ type: 'agentflow-sop-designer-begin', requestId: 'e2' });
  await send({ type: 'agentflow-sop-designer-draft', buffer: `{"drafts":[{"tool":"workflows_save","body":{"operations":[{"op":"add_node","node":{"id":"tmp","name":"Temp"}}` });
  await send({ type: 'agentflow-sop-designer-end', error: '模型超时' });
  assert.deepEqual(plain(app.evaluate('state.workflow.nodes.map(n=>n.id)')), plain(edited.nodes.map((n) => n.id)));
  assert.equal(app.document.querySelector('#aiBuildStatus').textContent, '模型超时');

  // 新对话 in the panel header tells the chat to start over.
  app.document.querySelector('#aiOrchestratorReset').click();
  assert.equal(posted.at(-1).type, 'agentflow-sop-designer-new');
});

test('the chat learns when the canvas switches to another SOP', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`switchView('flow');renderAll()`);
  app.document.querySelector('#openAIOrchestrator')?.click();
  const { posted } = bridge(app);
  app.evaluate(`state.workflow={...state.workflow,id:'other-sop'};renderCanvas()`);
  const context = posted.filter((m) => m.type === 'agentflow-sop-designer-context').at(-1);
  assert.equal(context.workflowId, 'other-sop');
  const count = posted.length;
  app.evaluate('renderCanvas()');
  assert.equal(posted.length, count, 'an unchanged SOP is not announced again');
});

test('orchestration refreshes project roles and restores the canvas for an unavailable role', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    switchView('flow');
    state.workflow = {...state.workflow, nodes: [], edges: []};
    state.workflowDirty = false;
    state.data.agents.push({id:'private', name:'Private role', projectId:'other'});
    renderAll();
  `);
  const workflowName = app.evaluate('state.workflow.name');
  app.router.get('/api/agents?projectId=project-1', [{id:'new-role', name:'New role', projectId:'project-1'}]);
  app.document.querySelector('#openAIOrchestrator')?.click();
  const { posted, send } = bridge(app);
  await send({ type: 'agentflow-sop-designer-begin', requestId: 'roles' });
  assert.equal(posted.at(-1).ok, true);
  assert.equal(app.evaluate("!!workflowAgent('new-role')"), true);
  assert.equal(app.evaluate("!!workflowAgent('private')"), false);
  await send({ type: 'agentflow-sop-designer-reply', draft: { tool: 'workflows_save', body: {
    name: 'Unavailable draft', nodes: [{id:'bad', name:'Review', type:'agent', agentId:'private'}], edges: [],
  } } });
  assert.equal(app.evaluate('state.workflow.name'), workflowName);
  assert.equal(app.evaluate('state.workflow.nodes.length'), 0);
  assert.equal(app.evaluate('state.workflowDirty'), false);
  assert.match(app.document.querySelector('#aiBuildStatus').textContent, /Review（private）/);
});
