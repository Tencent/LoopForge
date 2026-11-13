import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('DeepSeek has an independent settings row and saves its key and endpoint', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const user = { id: 1, loginName: 'alice', isGuest: false, agentCliCredentials: { deepseek: { apiKeyConfigured: true } } };
  app.router.put('/api/users/1/credentials', user);
  app.evaluate(`state.currentUser=${JSON.stringify(user)};state.data.users=[state.currentUser];renderPersonalSettings()`);
  let row = app.document.querySelector('[data-credential-row="agentCli.deepseek"]');
  assert.ok(row, 'DeepSeek must appear under model credentials');
  row.querySelector('.credential-row-main').click();
  row = app.document.querySelector('[data-credential-row="agentCli.deepseek"]');
  const secret = row.querySelector('input[name="secret"]');
  secret.value = 'deepseek-settings-test-key';
  secret.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  const endpoint = row.querySelector('input[name="extra"]');
  assert.equal(endpoint.placeholder, 'https://api.deepseek.com');
  endpoint.value = 'https://gateway.example/v1';
  endpoint.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  row.querySelector('[data-credential-act="save"]').click();
  await settle(6);
  const body = app.router.calls.find(c => c.path === '/api/users/1/credentials').body;
  assert.deepEqual(body, { agentCliCredentials: { deepseek: { apiKey: 'deepseek-settings-test-key', baseUrl: 'https://gateway.example/v1' } } });
  assert.equal(app.evaluate(`credentialHint('责任人未配置 DeepSeek API Key').field`), 'agentCli.deepseek');
  assert.equal(app.evaluate(`credentialHint('需求责任人未配置 DeepSeek Harness API Key').field`), 'agentCli.dsh');
  const clear = app.evaluate(`credentialClearPayload(credentialKind('agentCli.deepseek'))`);
  assert.deepEqual(JSON.parse(JSON.stringify(clear)).agentCliCredentials.deepseek, { apiKeyClear: true, baseUrl: '', protocol: '', models: [] });
  assert.match(app.evaluate(`state.data.executors.push({id:'deepseek',name:'DeepSeek'});executorOptions('deepseek')`), /data-meta="via Codex"/);
});

test('SOP role model picker uses the personal DeepSeek catalog', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const items = app.evaluate(`
    state.currentUser = {id:1};
    state.data.users = [{id:1,agentCliCredentials:{deepseek:{models:[{id:'custom-v4',name:'Custom V4'}]}}}];
    window.AgentFlowExecutorModels = {deepseek:{models:[{id:'deepseek-flash'}],customModel:true}};
    agentModelItems('deepseek','previous-model')
  `);
  assert.deepEqual(Array.from(items, x => x.id), ['custom-v4', 'previous-model', '__custom__']);
  assert.equal(items[0].name, 'Custom V4');
  assert.equal(app.evaluate(`applyAgentModelUI('deepseek','custom-v4','');document.querySelector('#agentModelPicker [name=modelChoice]').value`), 'custom-v4');
  assert.equal(app.evaluate(`state.data.users=[];agentModelItems('deepseek','')[0].id`), 'deepseek-flash');
});
