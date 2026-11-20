import test from 'node:test';
import assert from 'node:assert/strict';
import { chooseOption, loadApp, settle } from '../lib/load-app.mjs';

test('asset catalog filters, opens, marks dirty, shares, saves, and deletes assets', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`afConfirm=async()=>true`);
  app.evaluate(`state.assetType='rules';state.ruleScope='project';state.assetQuery='style';switchView('rules');renderAssets()`);
  assert.match(app.document.querySelector('#assetList').textContent, /Code style/);
  app.evaluate(`state.assetQuery='missing';renderAssets()`);
  assert.match(app.document.querySelector('#assetList').textContent, /没有匹配/);

  app.evaluate(`state.assetQuery='';openAssetDetail('rule-1')`);
  const name = app.document.querySelector('#assetForm [name="name"]');
  name.value = 'Strict style';
  name.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  app.evaluate(`markAssetDirty()`);
  assert.equal(app.evaluate(`state.assetDirty`), true);
  app.router.put('/api/rules', (call) => ({ status: 200, body: call.body }));
  await app.evaluate(`toggleAssetShare('rule-1')`);
  assert.equal(app.evaluate(`state.data.rules[0].shared`), true);
  await app.evaluate(`toggleAssetShare('rule-1')`);
  assert.equal(app.evaluate(`state.data.rules[0].shared`), false);

  app.document.querySelector('#assetForm [name="content"]').value = '# Strict';
  app.document.querySelector('#assetForm [name="priority"]').value = '250';
  app.document.querySelector('#assetForm').dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(3);
  const save = app.router.calls.filter((c) => c.path === '/api/rules' && c.method === 'PUT').at(-1);
  assert.equal(save.body.name, 'Strict style');
  assert.equal(save.body.priority, 250);

  app.router.delete('/api/rules?id=rule-1', { ok: true });
  await app.evaluate(`deleteAsset('rule-1')`);
  assert.equal(app.evaluate(`state.data.rules.some(x=>x.id==='rule-1')`), false);
});

test('asset forms save environment and sandbox-specific JSON fields', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.put(/^\/api\/(environments|sandboxes)$/, (call) => ({ status: 200, body: call.body }));
  app.evaluate(`state.assetType='environments';state.selectedAsset='__new__';renderAssets()`);
  let form = app.document.querySelector('#assetForm');
  form.elements.id.value = 'env-new';
  form.elements.name.value = 'Node tools';
  form.elements.variables.value = '{"NODE_ENV":"test"}';
  form.elements.pathEntries.value = ['bin', 'tools/bin'].join('\n');
  form.elements.installScript.value = 'npm ci';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(2);
  const env = app.router.calls.find((c) => c.path === '/api/environments' && c.method === 'PUT').body;
  assert.deepEqual(env.variables, { NODE_ENV: 'test' });
  assert.deepEqual(env.pathEntries, ['bin', 'tools/bin']);

  app.evaluate(`state.assetType='sandboxes';state.selectedAsset='__new__';state.assetDetailKey='';renderAssets()`);
  form = app.document.querySelector('#assetForm');
  const readOnlyHelp = form.querySelector('#assetReadOnlyRootFSHelp');
  const dockerFields = readOnlyHelp.closest('[data-sandbox-drivers="docker"]');
  assert.equal(dockerFields.hidden, true);
  chooseOption(form.elements.driver, 'docker');
  assert.equal(dockerFields.hidden, false);
  readOnlyHelp.click();
  const tooltip = app.document.querySelector('#huiTooltip:not([hidden])');
  assert.match(tooltip?.textContent ?? '', /缺少的 CLI 无法在容器内自动安装。工作区 \/workspace 仍可写/);
  assert.equal(form.elements.readOnlyRootFS.checked, false);
  assert.equal(app.router.calls.some((c) => c.path === '/api/sandboxes' && c.method === 'PUT'), false);

  form.elements.id.value = 'sandbox-new';
  form.elements.name.value = 'Docker sandbox';
  form.elements.environment.value = '{"CI":"1"}';
  chooseOption(form.elements.dockerfileId, 'docker-1');
  form.elements.readOnlyRootFS.checked = true;
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(2);
  const sandbox = app.router.calls.find((c) => c.path === '/api/sandboxes' && c.method === 'PUT').body;
  assert.equal(sandbox.driver, 'docker');
  assert.equal(sandbox.dockerfileId, 'docker-1');
  assert.equal(sandbox.readOnlyRootFS, true);
  assert.deepEqual(sandbox.environment, { CI: '1' });
});

test('agent catalog search, presets, validation, persistence, clone, and delete update state', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/executor-models', {
    codebuddy: { models: [{ id: 'cb-model', name: 'CB model' }] },
    codex: { models: [] }, claude: { models: [] },
  });
  app.evaluate(`afConfirm=async()=>true;afPrompt=async({defaultValue}={})=>defaultValue||'Developer copy'`);
  app.evaluate(`switchView('agents');renderAgents();openAgentWizard()`);
  await settle();
  app.evaluate(`applyAgentPreset('developer')`);
  const form = app.document.querySelector('#agentForm');
  form.elements.executorId.append(new app.window.Option('CodeBuddy', 'codebuddy'));
  form.elements.executorId.value = 'codebuddy';
  assert.equal(form.elements.name.value, '研发工程师');
  assert.match(form.elements.id.value, /^role-/);
  app.evaluate(`setAgentCreateStep(3)`);
  assert.match(app.document.querySelector('#agentCreateReview').textContent, /研发工程师|开发实现/);

  app.router.put('/api/agents', (call) => ({ status: 200, body: call.body }));
  await app.evaluate(`persistAgent($('#agentForm'))`);
  const created = app.router.calls.find((c) => c.path === '/api/agents' && c.method === 'PUT');
  assert.equal(created.body.name, '研发工程师');
  assert.equal(created.body.executorId, 'codebuddy');
  assert.equal(created.body.model, 'cb-model');
  assert.ok(app.evaluate(`state.data.agents.some(a=>a.name==='研发工程师')`));

  app.router.post('/api/agents/agent-1/clone', { id: 'agent-copy', projectId: 'project-1', name: 'Developer copy' });
  await app.evaluate(`cloneAgent('agent-1')`);
  assert.ok(app.evaluate(`state.data.agents.some(a=>a.id==='agent-copy')`));
  app.router.delete('/api/agents/agent-copy', { ok: true });
  await app.evaluate(`deleteAgent('agent-copy')`);
  assert.equal(app.evaluate(`state.data.agents.some(a=>a.id==='agent-copy')`), false);
});

test('role model picker reloads the editor catalog and preserves a CLI change while loading', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  let complete;
  const delayed = new Promise(resolve => { complete = resolve; });
  const catalog = (models) => ({
    codebuddy: { models: [], efforts: [] },
    claude: { models: [], efforts: [] },
    codex: { customModel: true, models, efforts: [] },
  });
  app.router.get('/api/executor-models', () => delayed);
  app.evaluate(`
    state.data.executors=[
      {id:'codebuddy',name:'CodeBuddy'},{id:'codex',name:'Codex'},{id:'claude',name:'Claude Code'}
    ];
    openAgentWizard();
  `);
  const f = app.document.querySelector('#agentForm');
  f.elements.executorId.value = 'codex';
  f.elements.executorId.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  complete(catalog([{ id: 'private/second', name: 'Second' }, { id: 'private/first', name: 'First' }]));
  await settle();
  const menu = () => [...app.document.querySelectorAll('#agentModelPicker [role=option]')];
  assert.deepEqual(menu().map(row => row.dataset.id), ['private/second', 'private/first', '__custom__']);
  assert.equal(menu()[0].querySelector('.ai-model-name').textContent, 'Second');
  assert.equal(f.elements.executorId.value, 'codex');
  assert.equal(app.evaluate(`agentEffectiveModel($('#agentForm'))`), 'private/second');

  app.router.get('/api/executor-models', () => catalog([{ id: 'private/second', name: 'Renamed model' }]));
  app.evaluate(`openAgentWizard({id:'role-review',name:'Review',prompt:'Review changes',executorId:'codex',model:'private/second'})`);
  await settle();
  assert.deepEqual(menu().map(row => row.dataset.id), ['private/second', '__custom__']);
  assert.equal(app.document.querySelector('#agentModelPicker [data-model-label]').textContent, 'Renamed model');

  let refresh;
  app.router.get('/api/executor-models', () => new Promise(resolve => { refresh = resolve; }));
  app.evaluate(`openAgentWizard()`);
  f.elements.executorId.value = 'codex';
  f.elements.executorId.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  menu().find(row => row.dataset.id === 'private/second').click();
  await settle();
  refresh(catalog([{ id: 'private/new', name: 'New model' }]));
  await settle();
  assert.equal(app.evaluate(`agentEffectiveModel($('#agentForm'))`), 'private/second');
});

test('a new role saves the first model from the refreshed catalog', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const catalog = id => ({ codebuddy: { models: [{ id }] }, codex: { models: [] }, claude: { models: [] } });
  app.router.get('/api/executor-models', catalog('retired-model'));
  app.router.put('/api/agents', call => ({ status: 200, body: call.body }));
  app.evaluate(`openAgentWizard()`);
  await settle();
  const model = () => app.evaluate(`agentEffectiveModel($('#agentForm'))`);
  assert.equal(model(), 'retired-model');
  app.document.querySelector('#agentModal .modal-close').click();

  let complete;
  app.router.get('/api/executor-models', () => new Promise(resolve => { complete = resolve; }));
  app.evaluate(`openAgentWizard(); applyAgentPreset('developer')`);
  await settle();
  assert.equal(model(), '');
  await app.evaluate(`persistAgent($('#agentForm'))`);
  assert.equal(app.router.calls.some(call => call.path === '/api/agents' && call.method === 'PUT'), false);
  app.evaluate(`setAgentCreateStep(3)`);
  complete(catalog('current-model'));
  await settle();
  assert.equal(model(), 'current-model');
  assert.match(app.document.querySelector('#agentCreateReview').textContent, /current-model/);
  await app.evaluate(`persistAgent($('#agentForm'))`);
  assert.equal(app.router.calls.find(call => call.path === '/api/agents' && call.method === 'PUT').body.model, 'current-model');
});

test('role effort picker follows each model and resets an unsupported level on model changes', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const xhigh = { value:'xhigh', label:'超高 xhigh' };
  app.router.get('/api/executor-models', {
    codebuddy:{models:[],efforts:[]}, claude:{models:[],efforts:[]},
    codex:{customModel:true,models:[{id:'reasoner',name:'Reasoner',efforts:[xhigh]}, {id:'plain',name:'Plain',efforts:[]}],efforts:[{value:'low',label:'低 low'},xhigh]},
  });
  app.evaluate(`
    state.data.executors=[{id:'codebuddy',name:'CodeBuddy'}, {id:'codex',name:'Codex'}];
    openAgentWizard({id:'test-role',name:'Test',prompt:'Test',executorId:'codex',model:'reasoner',effort:'xhigh'});
  `);
  await settle();
  const choices = () => [...app.document.querySelectorAll('#agentEffortPicker [role=option]')].map(row=>row.dataset.id);
  assert.deepEqual(choices(), ['', 'xhigh']);
  assert.equal(app.document.querySelector('#agentEffortPicker [name="effort"]').value, 'xhigh');
  app.document.querySelector('#agentModelPicker [data-id="plain"]').click();
  assert.deepEqual(choices(), ['']);
  assert.equal(app.document.querySelector('#agentEffortPicker [name="effort"]').value, '');
});

test('a personal catalog does not rewrite an existing role effort during a name edit', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  let loaded;
  app.router.get('/api/executor-models', () => new Promise(resolve => { loaded = resolve; }));
  app.router.put('/api/agents', call => ({ status: 200, body: call.body }));
  app.evaluate(`
    state.data.executors=[{id:'codebuddy',name:'CodeBuddy'}, {id:'codex',name:'Codex'}];
    openAgentWizard({id:'agent-1',name:'Developer',prompt:'Review',executorId:'codex',model:'reasoner',effort:'xhigh'});
  `);
  await settle();
  loaded({codex:{customModel:true,models:[{id:'reasoner',name:'Reasoner',efforts:[]}],efforts:[]}});
  await settle();
  const picker = app.document.querySelector('#agentEffortPicker');
  assert.deepEqual([...picker.querySelectorAll('[role=option]')].map(item=>item.dataset.id), ['']);
  assert.equal(picker.querySelector('[data-model-label]').textContent, 'xhigh');
  await app.evaluate(`
    $('#agentForm').elements.name.value='Renamed';
    persistAgent($('#agentForm'));
  `);
  const saved = app.router.calls.find(call=>call.path==='/api/agents'&&call.method==='PUT').body;
  assert.equal(saved.name, 'Renamed');
  assert.equal(saved.effort, 'xhigh');
});
