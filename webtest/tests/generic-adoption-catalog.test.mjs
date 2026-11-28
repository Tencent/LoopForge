import test from 'node:test';
import assert from 'node:assert/strict';
import {loadApp, sampleBootstrap} from '../lib/load-app.mjs';

// Adopting a built-in template makes the server copy the template's roles and
// rules into the project. The designer and the role editor read those from
// state.data, so the page has to reload them instead of only adding the SOP.
const template = {
  id: 'generic-delivery', name: '通用交付 SOP', generic: true, sandboxProfileId: 'project-box',
  nodes: [{id: 'build', name: '开发实现', type: 'agent', agentId: 'generic-delivery-build'}], edges: [],
};
const adoptedRole = {id: 'generic-delivery-build', projectId: 'project-1', name: '开发实现', executorId: 'codebuddy', rules: ['generic-delivery-rule']};
const adoptedRule = {id: 'generic-delivery-rule', projectId: 'project-1', name: '工程约束', content: '# rules'};

function adoptedCopy(body) {
  return {...template, ...body, generic: false, nodes: template.nodes, tags: ['generic-source:generic-delivery']};
}

async function adoptionApp(t) {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.projectFilter='project-1';state.data.workflows=[${JSON.stringify(template)}];
    state.data.sandboxes=[{id:'project-box',name:'Project box',projectId:'project-1',driver:'bwrap'}];`);
  let copy = null;
  app.router.post('/api/workflows/generic-delivery/clone', call => (copy = adoptedCopy(call.body)));
  app.router.put('/api/workflows', call => (copy = {...copy, ...call.body}));
  // Reloading after the clone returns what the server holds by then.
  app.router.get('/api/bootstrap', () => {
    const shell = sampleBootstrap();
    return {...shell, workflows: [template, copy], agents: [...shell.agents, adoptedRole], rules: [...shell.rules, adoptedRule]};
  });
  return app;
}

function assertAdoptedRolesResolve(app) {
  const id = app.evaluate('state.workflow.id');
  assert.equal(app.evaluate(`state.data.workflows.filter(w=>w.id==='${id}').length`), 1, 'the adopted SOP is listed once');
  assert.equal(app.evaluate(`workflowAgent('generic-delivery-build')?.name`), '开发实现');
  assert.equal(app.evaluate(`state.data.rules.some(r=>r.id==='generic-delivery-rule')`), true);
  assert.doesNotMatch(app.document.querySelector('#canvas')?.textContent || '', /角色不可用/);
}

test('copying a built-in template from the SOP catalog reloads the roles and rules it brought', async t => {
  const app = await adoptionApp(t);
  app.evaluate(`state.genericTemplateId='generic-delivery';`);
  await app.evaluate('adoptGenericTemplate()');
  assert.ok(app.router.calls.some(c => c.method === 'GET' && c.path === '/api/bootstrap'));
  assertAdoptedRolesResolve(app);
});

test('creating an SOP from a built-in template in the wizard reloads the roles and rules it brought', async t => {
  const app = await adoptionApp(t);
  app.evaluate('openSOPWizard()');
  const form = app.document.querySelector('#sopCreateForm');
  form.elements.name.value = '交付 SOP';
  form.elements.description.value = '带说明';
  await app.evaluate('createSOPFromWizard').call(null, {preventDefault() {}, currentTarget: form});
  assert.ok(app.router.calls.some(c => c.method === 'POST' && c.path === '/api/workflows/generic-delivery/clone'));
  assert.equal(app.evaluate('state.workflow.description'), '带说明');
  assertAdoptedRolesResolve(app);
});

test('duplicating a built-in template from the designer reloads the roles and rules it brought', async t => {
  const app = await adoptionApp(t);
  app.evaluate(`afPrompt=async({defaultValue}={})=>defaultValue||'交付 SOP - 副本'`);
  await app.evaluate(`cloneWorkflow('generic-delivery')`);
  assert.ok(app.router.calls.some(c => c.method === 'POST' && c.path === '/api/workflows/generic-delivery/clone'));
  assertAdoptedRolesResolve(app);
});

test('copying a project SOP keeps the catalog as it is and lists the copy once', async t => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.projectFilter='project-1';afPrompt=async({defaultValue}={})=>defaultValue||'Delivery copy'`);
  app.router.post('/api/workflows/flow-1/clone', call => ({...call.body, nodes: [], edges: [], status: 'draft'}));
  await app.evaluate(`cloneWorkflow('flow-1')`);
  const id = app.evaluate('state.workflow.id');
  assert.notEqual(id, 'flow-1');
  assert.equal(app.evaluate(`state.data.workflows.filter(w=>w.id==='${id}').length`), 1);
  assert.equal(app.router.calls.filter(c => c.path === '/api/bootstrap').length, 1, 'only the boot load fetched the shell');
});

test('the adopted SOP stays open when the catalog reload fails', async t => {
  const app = await adoptionApp(t);
  app.router.get('/api/bootstrap', {status: 500, body: {error: 'boom'}});
  app.evaluate(`state.genericTemplateId='generic-delivery';`);
  await app.evaluate('adoptGenericTemplate()');
  const id = app.evaluate('state.workflow.id');
  assert.match(id, /^sop/);
  assert.equal(app.evaluate(`state.data.workflows.filter(w=>w.id==='${id}').length`), 1);
});
