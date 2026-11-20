import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const levels = ['low', 'medium', 'high', 'xhigh', 'max', 'ultra'];
const catalogs = {
  codebuddy: { models: [], efforts: levels.map(value => ({ value, label: value })) },
  codex: { models: [], efforts: levels.map(value => ({ value, label: value })) },
  claude: { models: [], efforts: levels.map(value => ({ value, label: value })) },
  cursor: { models: [], efforts: levels.map(value => ({ value, label: value })) },
};

async function editor(t, models, engine='codex', catalogReply=catalogs) {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/executor-models?builtin=1', catalogReply);
  const credential = engine==='cursor'
    ? {agentCliCredentials:{cursor:{apiKeyConfigured:true,models,modelsCustomized:true}}}
    : {codexApiKeyConfigured:true,codexModels:models};
  app.evaluate(`
    state.currentUser={...state.data.users[0],id:1,isGuest:false,...${JSON.stringify(credential)}};
    state.data.users[0]=state.currentUser;
  `);
  app.router.put('/api/users/1/credentials', ({body}) => ({ id:1, codexApiKeyConfigured:true, ...body }));
  await app.evaluate('renderPersonalSettings()');
  await settle();
  const kind=engine==='cursor'?'agentCli.cursor':'codexApiKey';
  app.evaluate(`toggleCredentialRow(${JSON.stringify(kind)},true)`);
  const row = app.document.querySelector(`[data-credential-row="${kind}"]`);
  return { app, row, catalog: row.querySelector('[data-credential-catalog]') };
}

function input(app, element, value) {
  if(element.type === 'checkbox') element.checked = value;
  else element.value = value;
  element.dispatchEvent(new app.window.Event('input', { bubbles:true }));
}

test('model effort edits save independently of ID and name, and automatic differs from default-only', async t => {
  const {app,row,catalog} = await editor(t, [{id:'gateway',reasoningEfforts:['high','low']}, {id:'legacy'}]);
  const save = () => row.querySelector('[data-credential-act="save"]');
  assert.equal(save().disabled, true, 'opening a stored catalog never reorders it into a dirty state');
  const model = catalog.querySelector('[data-catalog-row]');
  model.querySelector('details').open = true;
  input(app, model.querySelector('[data-catalog-effort="high"]'), false);
  assert.equal(save().disabled, false, 'capability-only edits enable save');
  assert.equal(model.querySelector('[data-catalog-effort-summary]').textContent, 'low');
  save().click();
  await settle();
  const saved = app.router.calls.find(call => call.method === 'PUT').body;
  assert.deepEqual(saved.codexModels, [{id:'gateway',reasoningEfforts:['low']}, {id:'legacy'}]);

  app.evaluate(`toggleCredentialRow('codexApiKey',true)`);
  const fresh = app.document.querySelector('[data-credential-row="codexApiKey"] [data-catalog-row]');
  input(app, fresh.querySelector('[data-catalog-effort="low"]'), false);
  assert.deepEqual(JSON.parse(app.evaluate(`JSON.stringify(readCredentialCatalog(document.querySelector('[data-credential-row="codexApiKey"] [data-credential-catalog]')))`))[0].reasoningEfforts, []);
  input(app, fresh.querySelector('[data-catalog-effort-mode]'), 'auto');
  const automatic = JSON.parse(app.evaluate(`JSON.stringify(readCredentialCatalog(document.querySelector('[data-credential-row="codexApiKey"] [data-credential-catalog]')))`));
  assert.equal(Object.hasOwn(automatic[0], 'reasoningEfforts'), false);
  assert.equal(fresh.querySelector('[data-catalog-effort-levels]').hidden, true);
});

test('re-probing changes automatic capabilities and keeps explicit overrides', async t => {
  const {app,row,catalog} = await editor(t, [
    {id:'automatic',name:'My name',discoveredReasoningEfforts:['low']},
    {id:'custom',discoveredReasoningEfforts:['low'],reasoningEfforts:['low']},
    {id:'default-only',reasoningEfforts:[]},
  ]);
  app.router.post('/api/users/1/cli-models/probe', {
    source:'remote', models:[
      {id:'automatic',name:'Remote name',discoveredReasoningEfforts:['high','xhigh']},
      {id:'custom',discoveredReasoningEfforts:['high']},
      {id:'default-only',discoveredReasoningEfforts:['high']},
    ],
  });
  catalog.querySelector('[data-catalog-act="probe"]').click();
  await settle();
  catalog.querySelector('[data-catalog-act="probe-apply"]').click();
  const modes = [...catalog.querySelectorAll('[data-catalog-effort-mode]')];
  assert.deepEqual(modes.map(mode=>mode.value), ['auto','custom','custom']);
  assert.equal(catalog.querySelector('[data-catalog-effort-summary]').textContent, '自动 · high / xhigh');
  const save = row.querySelector('[data-credential-act="save"]');
  assert.equal(save.disabled, false);
  save.click();
  await settle();
  assert.deepEqual(app.router.calls.find(call => call.method === 'PUT').body.codexModels, [
    {id:'automatic',name:'My name',discoveredReasoningEfforts:['high','xhigh']},
    {id:'custom',discoveredReasoningEfforts:['high'],reasoningEfforts:['low']},
    {id:'default-only',discoveredReasoningEfforts:['high'],reasoningEfforts:[]},
  ]);
});

test('metadata-only refresh is dirty and leaves Cursor model-ID mappings intact', async t => {
  const {app,catalog} = await editor(t, [{id:'family',efforts:{high:'family-high',low:'family-low'},defaultEffort:'low'}], 'cursor');
  const row = catalog.querySelector('[data-catalog-row]');
  input(app, row.querySelector('[data-catalog-effort-mode]'), 'custom');
  input(app, row.querySelector('[data-catalog-effort="high"]'), true);
  const payload = JSON.parse(app.evaluate(`JSON.stringify(credentialCatalogPayload(document.querySelector('[data-credential-row="agentCli.cursor"] [data-credential-catalog]')))`));
  assert.deepEqual(payload[0].efforts, {high:'family-high',low:'family-low'});
  assert.equal(payload[0].defaultEffort, 'low');
  assert.deepEqual(payload[0].reasoningEfforts, ['high']);
});

test('late CLI capabilities fill in missing choices without replacing unsaved effort edits', async t => {
  let finish;
  const pending=new Promise(resolve=>{finish=resolve});
  const {app,catalog}=await editor(t,[{id:'slow-model',reasoningEfforts:['high']}],'codex',()=>pending);
  const high=catalog.querySelector('[data-catalog-effort="high"]');
  assert.ok(high);
  assert.equal(catalog.querySelector('[data-catalog-effort="xhigh"]'),null);
  input(app,high,false);
  high.focus();
  finish(catalogs);
  await settle();
  assert.ok(catalog.querySelector('[data-catalog-effort="xhigh"]'));
  assert.equal(high.checked,false);
  assert.equal(app.document.activeElement,high);
  const payload=JSON.parse(app.evaluate(`JSON.stringify(credentialCatalogPayload(document.querySelector('[data-credential-row="codexApiKey"] [data-credential-catalog]')))`));
  assert.deepEqual(payload[0].reasoningEfforts,[]);
});
