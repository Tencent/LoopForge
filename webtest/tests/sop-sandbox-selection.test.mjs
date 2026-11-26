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

test('blank SOP prefers project isolation over local and shared profiles and saves the selected environment', async t=>{
  const app=await loadApp();t.after(app.close);
  openBlank(app,[
    {id:'local',name:'Local',driver:'local'},
    {id:'foreign-private',name:'Other project',projectId:'other',driver:'docker'},
    {id:'shared-docker',name:'Shared Docker',projectId:'other',shared:true,driver:'docker'},
    {id:'project-docker',name:'Project Docker',projectId:'project-1',driver:'docker'},
  ]);
  const select=app.document.querySelector('#sopCreateSandbox');
  assert.equal(select.value,'project-docker');
  assert.deepEqual([...select.options].filter(o=>o.value.trim()).map(o=>o.value),['local','shared-docker','project-docker']);
  select.value='shared-docker';select.dispatchEvent(new app.window.Event('change',{bubbles:true}));
  app.evaluate('setSOPWizardStep(3)');
  assert.match(app.document.querySelector('#sopCreateSummary').textContent,/Shared Docker/);
  app.router.put('/api/workflows',call=>({status:200,body:{...call.body,revision:1}}));
  await submit(app);
  const call=app.router.calls.find(c=>c.path==='/api/workflows'&&c.method==='PUT');
  assert.equal(call.body.sandboxProfileId,'shared-docker');
  assert.equal(app.evaluate('state.workflow.sandboxProfileId'),'shared-docker');
});

test('automatic sandbox selection saves and reopens in the SOP designer', async t=>{
  const app=await loadApp();t.after(app.close);
  openBlank(app,[{id:'project-docker',name:'Project Docker',projectId:'project-1',driver:'docker'}]);
  const select=app.document.querySelector('#sopCreateSandbox');
  const automatic=[...select.options].find(option=>option.textContent==='由 Agent 选择');
  assert.ok(automatic);
  select.value=automatic.value;
  select.dispatchEvent(new app.window.Event('change',{bubbles:true}));
  app.evaluate('setSOPWizardStep(3)');
  assert.match(app.document.querySelector('#sopCreateSummary').textContent,/由 Agent 选择/);
  app.router.put('/api/workflows',call=>({status:200,body:{...call.body,revision:1}}));
  await submit(app);
  const call=app.router.calls.find(c=>c.path==='/api/workflows'&&c.method==='PUT');
  assert.equal(call.body.autoSelectSandbox,true);
  assert.equal(call.body.sandboxProfileId,'');
  const editor=app.document.querySelector('#workflowSandbox');
  assert.equal(editor.selectedOptions[0].textContent,'由 Agent 选择');
  editor.value='project-docker';
  editor.dispatchEvent(new app.window.Event('change',{bubbles:true}));
  assert.equal(app.evaluate('state.workflow.sandboxProfileId'),'project-docker');
  assert.equal(app.evaluate('!!state.workflow.autoSelectSandbox'),false);
});

test('manual SOP start shows sandbox selection while admission is pending', async t=>{
  const app=await loadApp();t.after(app.close);
  app.evaluate(`state.data.workflows[0].autoSelectSandbox=true;openRunModal('flow-1')`);
  const reply=Promise.withResolvers();
  app.router.post('/api/runs',()=>reply.promise);
  const button=app.document.querySelector('#confirmRun');
  const pending=app.evaluate('startRun()');
  assert.equal(button.disabled,true);
  assert.equal(button.textContent,'正在选择沙箱…');
  const repeated=app.evaluate('startRun()');
  assert.equal(app.router.calls.filter(call=>call.method==='POST'&&call.path==='/api/runs').length,1);
  reply.resolve({status:400,body:{error:'Run could not start'}});
  await Promise.all([pending,repeated]);
  assert.equal(button.disabled,false);
  assert.equal(button.textContent,'开始运行');
});

test('issue dispatch shows sandbox selection until admission returns', async t=>{
  const app=await loadApp();t.after(app.close);
  app.evaluate(`state.data.workflows[0].autoSelectSandbox=true;state.data.issues[0].workflowId='flow-1'`);
  const reply=Promise.withResolvers();
  app.router.post('/api/issues/issue-1/dispatch',()=>reply.promise);
  const pending=app.evaluate(`dispatchIssueById('issue-1')`);
  const notice=[...app.document.querySelectorAll('#toastContainer .toast-item')]
    .find(item=>item.querySelector('.toast-item-msg')?.textContent==='正在选择沙箱…');
  assert.ok(notice);
  assert.equal(notice.classList.contains('leaving'),false);
  reply.resolve({status:400,body:{error:'Dispatch failed'}});
  await pending;
  assert.equal(notice.classList.contains('leaving'),true);
  assert.match(app.document.querySelector('#toastContainer').textContent,/Dispatch failed/);
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
    state.data.sandboxes=[{id:'local',name:'Local',driver:'local'},{id:'template-box',name:'Template Box',driver:'native'}];openSOPWizard()`);
  const form=app.document.querySelector('#sopCreateForm');form.elements.name.value='Copied SOP';
  assert.equal(app.document.querySelector('#sopCreateSandbox').value,'template-box');
  app.evaluate(`state.data.sandboxes=[{id:'local',name:'Local',driver:'local'},{id:'another-box',name:'Another Box',driver:'docker'}];setSOPWizardStep(2)`);
  assert.match(app.document.querySelector('#sopSandboxHint').textContent,/模板原有沙箱不可用/);
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
  assert.equal(app.document.querySelector('#sopCreateSandbox').value,'generic-docker');
  app.router.post('/api/workflows/generic-template/clone',call=>({status:200,body:{...call.body,id:'generic-copy',sandboxProfileId:'generic-docker',nodes:[],edges:[]}}));
  await submit(app);
  assert.equal(app.router.calls.find(c=>c.path==='/api/workflows/generic-template/clone'&&c.method==='POST').body.sandboxProfileId,'generic-docker');
  assert.equal(app.evaluate('state.workflow.sandboxProfileId'),'generic-docker');
});

test('generic adoption sends the isolated replacement when its original sandbox is absent', async t=>{
  const app=await loadApp();t.after(app.close);
  app.evaluate(`state.data.workflows=[{id:'generic-template',name:'Generic',generic:true,sandboxProfileId:'missing-box',nodes:[],edges:[]}];
    state.data.sandboxes=[{id:'local',name:'Local',driver:'local'},{id:'project-box',name:'Project Sandbox',projectId:'project-1',driver:'bwrap'}];openSOPWizard()`);
  app.document.querySelector('#sopCreateForm').elements.name.value='Fallback copy';
  assert.equal(app.document.querySelector('#sopFinish').disabled,false);
  assert.equal(app.document.querySelector('#sopCreateSandbox').value,'project-box');
  app.router.post('/api/workflows/generic-template/clone',call=>({status:201,body:{...call.body,id:'fallback-copy',sandboxProfileId:'project-box',nodes:[],edges:[]}}));
  await submit(app);
  assert.equal(app.evaluate('state.workflow.sandboxProfileId'),'project-box');
  assert.equal(app.router.calls.find(c=>c.path==='/api/workflows/generic-template/clone'&&c.method==='POST').body.sandboxProfileId,'project-box');
});

test('templates start from their own sandbox, then a project sandbox',async t=>{
  const app=await loadApp();t.after(app.close);
  app.evaluate(`state.data.workflows=[{id:'template',name:'Template',status:'published',sandboxProfileId:'shared-native',nodes:[],edges:[]}];
    state.data.sandboxes=[{id:'project-box',name:'Project',projectId:'project-1',driver:'docker'},{id:'shared-native',name:'Shared native',shared:true,driver:'seatbelt'}];openSOPWizard()`);
  const select=app.document.querySelector('#sopCreateForm').elements.sandboxProfileId;
  assert.equal(select.value,'shared-native');
  app.evaluate(`state.data.workflows[0].sandboxProfileId='';delete document.querySelector('#sopCreateSandbox').dataset.source;openSOPWizard()`);
  assert.equal(select.value,'project-box');
});

test('direct blank drafts choose an existing sandbox and stay unchanged when none is available', async t=>{
  const app=await loadApp();t.after(app.close);
  app.evaluate(`state.data.sandboxes=[{id:'local',name:'Local',driver:'local'},{id:'project-native',name:'Native',projectId:'project-1',driver:'native'}];newWorkflow()`);
  assert.equal(app.evaluate('state.workflow.sandboxProfileId'),'project-native');
  const id=app.evaluate('state.workflow.id');
  app.evaluate('state.data.sandboxes=[];newWorkflow()');
  assert.equal(app.evaluate('state.workflow.id'),id);
});
