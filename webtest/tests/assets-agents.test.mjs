import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

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
  form.elements.id.value = 'sandbox-new';
  form.elements.name.value = 'Docker sandbox';
  form.elements.environment.value = '{"CI":"1"}';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(2);
  const sandbox = app.router.calls.find((c) => c.path === '/api/sandboxes' && c.method === 'PUT').body;
  assert.equal(sandbox.driver, 'local');
  assert.deepEqual(sandbox.environment, { CI: '1' });
});

test('agent catalog search, presets, validation, persistence, clone, and delete update state', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`afConfirm=async()=>true;afPrompt=async({defaultValue}={})=>defaultValue||'Developer copy'`);
  app.evaluate(`switchView('agents');renderAgents();openAgentWizard()`);
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
  assert.ok(app.evaluate(`state.data.agents.some(a=>a.name==='研发工程师')`));

  app.router.post('/api/agents/agent-1/clone', { id: 'agent-copy', projectId: 'project-1', name: 'Developer copy' });
  await app.evaluate(`cloneAgent('agent-1')`);
  assert.ok(app.evaluate(`state.data.agents.some(a=>a.id==='agent-copy')`));
  app.router.delete('/api/agents/agent-copy', { ok: true });
  await app.evaluate(`deleteAgent('agent-copy')`);
  assert.equal(app.evaluate(`state.data.agents.some(a=>a.id==='agent-copy')`), false);
});
