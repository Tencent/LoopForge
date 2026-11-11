import test from 'node:test';
import assert from 'node:assert/strict';
import {loadApp} from '../lib/load-app.mjs';

async function submit(app) {
  await app.evaluate('createSOPFromWizard').call(null, {
    preventDefault() {}, currentTarget: app.document.querySelector('#sopCreateForm'),
  });
}

function openBlank(app, sandboxes) {
  app.evaluate(`state.data.workflows=[];state.data.sandboxes=${JSON.stringify(sandboxes)};openSOPWizard()`);
  const form=app.document.querySelector('#sopCreateForm');
  form.elements.name.value='Custom SOP';
  form.elements.description.value='Keep this draft';
  app.evaluate('setSOPWizardStep(2)');
  return form;
}

test('blank SOP uses an existing project sandbox when local is absent and respects the selected environment', async t=>{
  const app=await loadApp();t.after(app.close);
  openBlank(app,[
    {id:'foreign-private',name:'Other project',projectId:'other',driver:'local'},
    {id:'project-docker',name:'Project Docker',projectId:'project-1',driver:'docker'},
    {id:'shared-docker',name:'Shared Docker',projectId:'other',shared:true,driver:'docker'},
  ]);
  const select=app.document.querySelector('#sopCreateSandbox');
  assert.equal(select.value,'project-docker');
  assert.deepEqual([...select.options].filter(o=>o.value).map(o=>o.value),['project-docker','shared-docker']);
  select.value='shared-docker';select.dispatchEvent(new app.window.Event('change',{bubbles:true}));
  app.evaluate('setSOPWizardStep(3)');
  assert.match(app.document.querySelector('#sopCreateSummary').textContent,/Shared Docker/);
  app.router.put('/api/workflows',call=>({status:200,body:{...call.body,revision:1}}));
  await submit(app);
  const call=app.router.calls.find(c=>c.path==='/api/workflows'&&c.method==='PUT');
  assert.equal(call.body.sandboxProfileId,'shared-docker');
  assert.equal(app.evaluate('state.workflow.sandboxProfileId'),'shared-docker');
});

test('missing sandboxes block submission and refresh keeps the entered SOP fields', async t=>{
  const app=await loadApp();t.after(app.close);
  const form=openBlank(app,[{id:'foreign-private',projectId:'other',name:'Private',driver:'docker'}]);
  assert.equal(app.document.querySelector('#sopFinish').disabled,true);
  assert.match(app.document.querySelector('#sopSandboxHint').textContent,/没有可用.*沙箱/);
  await submit(app);
  assert.equal(app.router.calls.some(c=>c.path==='/api/workflows'&&c.method==='PUT'),false);
  assert.equal(form.elements.name.value,'Custom SOP');
  assert.equal(app.document.querySelector('#sopCreateModal').classList.contains('open'),true);
  app.router.get('/api/sandboxes',[{id:'installed-docker',name:'Installed Docker',driver:'docker'}]);
  await app.evaluate('refreshSOPSandboxes()');
  assert.equal(form.elements.name.value,'Custom SOP');
  assert.equal(form.elements.description.value,'Keep this draft');
  assert.equal(app.document.querySelector('#sopCreateSandbox').value,'installed-docker');
  assert.equal(app.document.querySelector('#sopFinish').disabled,false);
  app.router.put('/api/workflows',call=>({status:200,body:{...call.body,revision:1}}));
  await submit(app);
  assert.equal(app.evaluate('state.workflow.sandboxProfileId'),'installed-docker');
});

test('template sandbox is preserved and a missing template sandbox requires an explicit replacement', async t=>{
  const app=await loadApp();t.after(app.close);
  app.evaluate(`state.data.workflows=[{id:'template',name:'Template',status:'published',sandboxProfileId:'template-box',nodes:[],edges:[]}];
    state.data.sandboxes=[{id:'local',name:'Local'},{id:'template-box',name:'Template Box'}];openSOPWizard()`);
  const form=app.document.querySelector('#sopCreateForm');form.elements.name.value='Copied SOP';
  assert.equal(app.document.querySelector('#sopCreateSandbox').value,'template-box');
  app.evaluate(`state.data.sandboxes=state.data.sandboxes.filter(s=>s.id!=='template-box');setSOPWizardStep(2)`);
  assert.equal(app.document.querySelector('#sopCreateSandbox').value,'');
  assert.equal(app.document.querySelector('#sopFinish').disabled,true);
  await submit(app);
  assert.equal(app.router.calls.some(c=>c.path==='/api/workflows'&&c.method==='PUT'),false);
  const select=app.document.querySelector('#sopCreateSandbox');select.value='local';
  select.dispatchEvent(new app.window.Event('change',{bubbles:true}));
  app.router.put('/api/workflows',call=>({status:200,body:{...call.body,revision:1}}));
  await submit(app);
  assert.equal(app.evaluate('state.workflow.sandboxProfileId'),'local');
});

test('generic templates keep their sandbox and continue through the clone endpoint', async t=>{
  const app=await loadApp();t.after(app.close);
  app.evaluate(`state.data.workflows=[{id:'generic-template',name:'Generic',generic:true,sandboxProfileId:'generic-docker',nodes:[],edges:[]}];
    state.data.sandboxes=[];openSOPWizard()`);
  const form=app.document.querySelector('#sopCreateForm');form.elements.name.value='Generic copy';
  await submit(app);
  assert.equal(app.router.calls.some(c=>c.method==='POST'&&c.path.endsWith('/clone')),false);
  assert.equal(app.document.querySelector('#sopFinish').disabled,true);
  app.router.get('/api/sandboxes',[{id:'generic-docker',name:'Docker',driver:'docker'}]);
  await app.evaluate('refreshSOPSandboxes()');
  assert.equal(app.document.querySelector('#sopCreateSandbox').disabled,true);
  app.router.post('/api/workflows/generic-template/clone',call=>({status:200,body:{...call.body,id:'generic-copy',sandboxProfileId:'generic-docker',nodes:[],edges:[]}}));
  await submit(app);
  assert.equal(app.router.calls.some(c=>c.path==='/api/workflows/generic-template/clone'&&c.method==='POST'),true);
  assert.equal(app.evaluate('state.workflow.sandboxProfileId'),'generic-docker');
});

test('generic adoption retains its existing server fallback when the original sandbox is absent', async t=>{
  const app=await loadApp();t.after(app.close);
  app.evaluate(`state.data.workflows=[{id:'generic-template',name:'Generic',generic:true,sandboxProfileId:'missing-box',nodes:[],edges:[]}];
    state.data.sandboxes=[{id:'project-box',name:'Project Sandbox',projectId:'project-1'}];openSOPWizard()`);
  app.document.querySelector('#sopCreateForm').elements.name.value='Fallback copy';
  assert.equal(app.document.querySelector('#sopFinish').disabled,false);
  assert.equal(app.document.querySelector('#sopCreateSandbox').value,'project-box');
  app.router.post('/api/workflows/generic-template/clone',call=>({status:201,body:{...call.body,id:'fallback-copy',sandboxProfileId:'project-box',nodes:[],edges:[]}}));
  await submit(app);
  assert.equal(app.evaluate('state.workflow.sandboxProfileId'),'project-box');
  assert.equal(app.router.calls.some(c=>c.path==='/api/workflows/generic-template/clone'&&c.method==='POST'),true);
});

test('direct blank drafts choose an existing sandbox and stay unchanged when none is available', async t=>{
  const app=await loadApp();t.after(app.close);
  app.evaluate(`state.data.sandboxes=[{id:'project-native',name:'Native',projectId:'project-1',driver:'native'}];newWorkflow()`);
  assert.equal(app.evaluate('state.workflow.sandboxProfileId'),'project-native');
  const id=app.evaluate('state.workflow.id');
  app.evaluate('state.data.sandboxes=[];newWorkflow()');
  assert.equal(app.evaluate('state.workflow.id'),id);
});
