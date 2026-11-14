import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

test('designer configures routing conditions and join behavior', async (t) => {
  const app = await loadApp(); t.after(app.close);
  app.evaluate(`switchView('flow');state.workflow={id:'route',projectId:'project-1',name:'Routing',status:'draft',sandboxProfileId:'local',nodes:[
    {id:'route',name:'Route',type:'script',script:'echo ok',position:{x:20,y:30}},
    {id:'small',name:'Small',type:'script',script:'echo small',position:{x:300,y:30}},
    {id:'large',name:'Large',type:'script',script:'echo large',position:{x:300,y:150}},
    {id:'join',name:'Join',type:'script',script:'echo join',position:{x:600,y:80}}
  ],edges:[{id:'small-edge',source:'route',target:'small',when:'success'},{id:'large-edge',source:'route',target:'large',when:'success'},{id:'sj',source:'small',target:'join',when:'success'},{id:'lj',source:'large',target:'join',when:'success'}]};state.selectedNode='route';renderCanvas();renderProperties()`);
  let type=app.document.querySelector('[data-route-edge="small-edge"] [data-route-field="type"]');
  type.value='route';type.dispatchEvent(new app.window.Event('change'));
  const labels=app.document.querySelector('[data-route-edge="small-edge"] [data-route-field="values"]');
  labels.value='small, medium';labels.dispatchEvent(new app.window.Event('change'));
  assert.deepEqual(Array.from(app.evaluate(`state.workflow.edges[0].condition.values`)),['small','medium']);
  assert.match(app.document.querySelector('#edges').textContent,/route: small/);
  app.evaluate(`state.selectedNode='join';renderProperties()`);
  const join=app.document.querySelector('#properties [data-p="joinRule"]');
  assert.match(app.document.querySelector('#properties').textContent,/汇合条件分支/);
  assert.equal(app.evaluate(`flowDiagnostics().some(x=>x.title==='Join'&&x.detail.includes('汇合条件分支'))`),true);
  join.value='none_failed_min_one_success';join.dispatchEvent(new app.window.Event('input'));
  assert.equal(app.evaluate(`state.workflow.nodes.find(n=>n.id==='join').joinRule`),'none_failed_min_one_success');
  app.evaluate(`state.selectedNode='route';renderProperties()`);
  const fallback=app.document.querySelector('#properties [data-p="defaultTarget"]');
  fallback.value='large';fallback.dispatchEvent(new app.window.Event('input'));
  assert.equal(app.evaluate(`state.workflow.nodes[0].defaultTarget`),'large');
  app.evaluate(`state.workflow.edges[0].condition={type:'artifact',artifact:'classification',pointer:'/size',equals:null};renderProperties()`);
  assert.equal(app.document.querySelector('[data-route-edge="small-edge"] [data-route-field="equals"]').value,'null');
});

test('agent drawer keeps failure controls before branch configuration', async (t) => {
  const app = await loadApp(); t.after(app.close);
  app.evaluate(`switchView('flow');state.workflow={id:'drawer',projectId:'project-1',name:'Drawer',nodes:[
    {id:'agent',name:'Agent',type:'agent',agentId:'agent-1',position:{x:20,y:30}},
    {id:'next',name:'Next',type:'script',script:'echo ok',position:{x:300,y:30}}
  ],edges:[{id:'edge',source:'agent',target:'next',when:'success'}]};state.selectedNode='agent';renderProperties()`);
  const body=app.document.querySelector('#properties .fd-db');
  const failure=body.querySelector('[data-fail="reject"]');
  const routing=body.querySelector('[data-route-edge="edge"]');
  assert.ok(failure && routing);
  assert.ok(failure.compareDocumentPosition(routing) & app.window.Node.DOCUMENT_POSITION_FOLLOWING);
});

test('flow editor manages preview contracts, nodes, connections, diagnostics, and properties', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`(()=>{
    switchView('flow');
    state.workflow={id:'edit-flow',projectId:'project-1',name:'Editor',status:'draft',version:1,
      sandboxProfileId:'sandbox-1',nodes:[
        {id:'a',name:'Build',type:'script',script:'echo ok',position:{x:20,y:30}},
        {id:'b',name:'Approve',type:'approval',position:{x:320,y:30}}
      ],edges:[]};
    state.workflowDirty=false;renderAll();
    const contract=ensurePreviewContract();syncPreviewBuildGraph();
    const preview=state.workflow.nodes.find(n=>n.id===PREVIEW_BUILD_NODE_ID);
    const sinks=state.workflow.edges.filter(e=>e.target===PREVIEW_BUILD_NODE_ID).length;
    const cycle=flowConnectCheck(state.workflow.nodes[0],state.workflow.nodes[1],'success');
    connect('a','b');
    const back=flowConnectCheck(state.workflow.nodes[1],state.workflow.nodes[0],'success');
    setRejectTo('b','a');
    const duplicate=duplicateNode('a');
    const beforeRemove=state.workflow.nodes.length;
    removeNode(duplicate.id);
    state.selectedNode='a';renderCanvas();renderProperties();
    const scriptMeta=nodeMeta(state.workflow.nodes.find(n=>n.id==='a'));
    const diagnostics=flowDiagnostics();
    return {contract,preview,sinks,cycle,back,beforeRemove,afterRemove:state.workflow.nodes.length,
      reject:state.workflow.nodes.find(n=>n.id==='b').rejectTo,scriptMeta,diagnostics,
      properties:$('#properties').textContent,dirty:state.workflowDirty};
  })()`);
  assert.equal(result.contract.enabled, true);
  assert.equal(result.contract.kind, 'static');
  assert.equal(result.preview.agentId, 'af-preview-builder');
  assert.equal(result.preview.joinRule, 'none_failed_min_one_success');
  assert.equal(result.sinks, 2);
  assert.equal(result.cycle.ok, true);
  assert.equal(result.back.ok, false);
  assert.match(result.back.why, /循环/);
  assert.equal(result.afterRemove, result.beforeRemove - 1);
  assert.equal(result.reject, 'a');
  assert.match(result.scriptMeta.subtitle, /Shell/);
  assert.ok(result.diagnostics.some((d) => d.title === '运行沙箱'));
  assert.match(result.properties, /脚本内容|失败后/);
  assert.equal(result.dirty, true);
});

test('flow editor handles artifact/list editors and saves workflow changes', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`switchView('flow');state.workflow={id:'save-flow',projectId:'project-1',name:'Save me',
    status:'draft',version:1,revision:0,sandboxProfileId:'sandbox-1',nodes:[
      {id:'sub',name:'Sub SOP',type:'sop',sopWorkflowId:'flow-1',position:{x:10,y:10},
       artifactOutputs:[{name:'report',path:'report.md',mediaType:'text/markdown',required:true}]}
    ],edges:[]};state.selectedNode='sub';state.workflowDirty=true;renderAll();renderProperties()`);
  const name = app.document.querySelector('#properties [data-ak="name"]');
  name.value = 'handoff';
  name.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  app.document.querySelector('#addSOPHandoff')?.click();
  assert.equal(app.evaluate(`state.workflow.nodes[0].artifactOutputs.length`), 2);
  assert.equal(app.evaluate(`state.workflow.nodes[0].artifactOutputs[0].name`), 'handoff');

  app.router.put('/api/workflows', (call) => ({ status: 200, body: { ...call.body, revision: 1 } }));
  await app.evaluate(`saveWorkflow()`);
  const call = app.router.calls.find((c) => c.path === '/api/workflows' && c.method === 'PUT');
  assert.equal(call.body.id, 'save-flow');
  assert.equal(call.body.nodes[0].artifactOutputs[0].name, 'handoff');
  assert.equal(app.evaluate(`state.workflowDirty`), false);
});

test('flow preview properties update model and SPA fallback in observable state', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`switchView('flow');addNode('preview',{at:{x:200,y:100}});renderProperties()`);
  const model = app.document.querySelector('#previewNodeModel');
  model.value = model.options[model.options.length - 1].value;
  model.dispatchEvent(new app.window.Event('change'));
  const spa = app.document.querySelector('#previewNodeSpa');
  spa.checked = false;
  spa.dispatchEvent(new app.window.Event('change'));
  assert.equal(app.evaluate(`state.workflow.preview.model`), model.value);
  assert.equal(app.evaluate(`state.workflow.preview.spaFallback`), false);
  assert.match(app.document.querySelector('#properties').textContent, /CodeBuddy 模型/);
});

test('SOP wizard creates a workflow and publish/delete actions send expected requests', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`afConfirm=async()=>true;openSOPWizard()`);
  const form = app.document.querySelector('#sopCreateForm');
  form.elements.name.value = 'Release checks';
  form.elements.description.value = 'Verify release';
  form.elements.owner.value = 'QA';
  form.elements.tags.value = 'release, qa';
  app.evaluate(`state.sopTemplate='blank';setSOPWizardStep(2);setSOPWizardStep(3)`);
  app.router.put('/api/workflows', (call) => ({ status: 200, body: { ...call.body, revision: 1 } }));
  await app.evaluate(`createSOPFromWizard`).call(null, {
    preventDefault() {},
    currentTarget: form,
  });
  assert.equal(app.evaluate(`state.workflow.name`), 'Release checks');
  assert.deepEqual(app.evaluate(`state.workflow.tags`).join(','), 'release,qa');

  const current = app.evaluate(`structuredClone(state.workflow)`);
  app.router.post(/^\/api\/workflows\/[^/]+\/publish$/, { ...current, status: 'published', version: 2 });
  await app.evaluate(`publishWorkflow()`);
  assert.equal(app.evaluate(`state.workflow.status`), 'published');
  const id = app.evaluate(`state.workflow.id`);
  app.router.delete(`/api/workflows/${id}`, { ok: true });
  await app.evaluate(`deleteWorkflow(${JSON.stringify(id)})`);
  assert.equal(app.evaluate(`state.data.workflows.some(w=>w.id===${JSON.stringify(id)})`), false);
});

test('publishing blocks double clicks and stays disabled until new changes are saved', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.workflow={id:'publish-check',projectId:'project-1',name:'Check',status:'published',version:2,revision:4,publishedRevision:4,nodes:[],edges:[]};markWorkflowClean()`);
  assert.equal(app.document.querySelector('#publishWorkflow').disabled, true);
  await app.evaluate(`publishWorkflow()`);
  assert.equal(app.router.calls.filter(c=>c.path.endsWith('/publish')).length, 0);
  app.evaluate(`state.workflow.revision=5;state.workflowSaved=clone(state.workflow);renderFlowHeader()`);
  assert.equal(app.document.querySelector('#publishWorkflow').disabled, false);
  let finish;
  app.router.post('/api/workflows/publish-check/publish', () => new Promise(resolve=>{finish=resolve}));
  const first=app.evaluate(`publishWorkflow()`);
  await app.evaluate(`publishWorkflow()`);
  assert.equal(app.document.querySelector('#publishWorkflow').disabled, true);
  assert.equal(app.router.calls.filter(c=>c.path.endsWith('/publish')).length, 1);
  assert.equal(app.router.calls.filter(c=>c.path==='/api/workflows'&&c.method==='PUT').length, 0);
  finish({...app.evaluate(`structuredClone(state.workflow)`),version:3,revision:6,publishedRevision:6});
  await first;
  assert.equal(app.document.querySelector('#publishWorkflow').disabled, true);
  app.evaluate(`state.workflow.description='Changed';dirty()`);
  assert.equal(app.document.querySelector('#publishWorkflow').disabled, false);
});

test('switching workflows explicitly offers to discard edits or continue editing', async (t) => {
  const app=await loadApp();t.after(app.close);
  app.evaluate(`state.data.workflows.push({id:'another-flow',name:'Another',nodes:[],edges:[]});state.workflowDirty=true`);
  const pending=app.evaluate(`switchFlowWorkflow('another-flow')`);
  assert.equal(app.document.querySelector('#confirmOkBtn').textContent,'放弃修改并切换');
  assert.equal(app.document.querySelector('#confirmCancelBtn').textContent,'继续编辑');
  app.document.querySelector('#confirmCancelBtn').click();
  assert.equal(await pending,false);
});
