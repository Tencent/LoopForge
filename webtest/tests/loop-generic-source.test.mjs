import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// `generic-source:<template>` is the provenance the server writes when a generic
// template is adopted. It decides whether copied roles follow the responsible
// user's default CLI, so user tag edits must never drop it.
const MARKER = 'generic-source:tpl-backend';

function withTemplate(app) {
  app.evaluate(`
    state.data.sandboxes=[{id:'project-native',name:'Native',projectId:'project-1',driver:'native'}];
    state.data.workflows=[{id:'tpl-backend',name:'Backend template',generic:true,tags:['public'],nodes:[],edges:[]}];
  `);
}

test('wizard keeps the adoption marker when the user enters tags', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  withTemplate(app);
  app.router.post('/api/workflows/tpl-backend/clone', (call) => ({
    status: 201, body: { id: call.body.id, name: call.body.name, projectId: 'project-1', revision: 1, status: 'draft', sandboxProfileId: 'project-native', tags: ['public', MARKER], nodes: [], edges: [] },
  }));
  app.router.put('/api/workflows', (call) => ({ status: 200, body: { ...call.body, revision: 2 } }));
  app.evaluate(`openSOPWizard()`);
  const form = app.document.querySelector('#sopCreateForm');
  form.elements.name.value = 'Tagged copy';
  form.elements.tags.value = '走查, 后端';
  app.evaluate(`state.sopTemplate='tpl-backend';setSOPWizardStep(2)`);
  app.document.querySelector('#sopCreateSandbox').value = 'project-native';
  await app.evaluate('createSOPFromWizard').call(null, { preventDefault() {}, currentTarget: form });
  await settle(2);
  const put = app.router.calls.find((c) => c.path === '/api/workflows' && c.method === 'PUT');
  assert.ok(put, 'the wizard saved the entered tags');
  assert.deepEqual(put.body.tags, ['走查', '后端', MARKER]);
});

test('designer tag field hides the marker and keeps it on edit', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.workflow={id:'copy',name:'Copy',projectId:'project-1',status:'draft',tags:['public','${MARKER}'],nodes:[],edges:[]};renderWorkflowMeta()`);
  const input = app.document.querySelector('#workflowTags');
  assert.equal(input.value, 'public');
  input.value = '';
  input.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.deepEqual(JSON.parse(app.evaluate(`JSON.stringify(state.workflow.tags)`)), [MARKER]);
  input.value = '前端, 回归';
  input.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.deepEqual(JSON.parse(app.evaluate(`JSON.stringify(state.workflow.tags)`)), ['前端', '回归', MARKER]);
  // Typing a marker by hand is ignored: provenance only comes from adoption.
  input.value = 'x, generic-source:other';
  input.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.deepEqual(JSON.parse(app.evaluate(`JSON.stringify(state.workflow.tags)`)), ['x', MARKER]);
});

test('catalog reads provenance through one helper', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = JSON.parse(app.evaluate(`JSON.stringify({
    source:workflowGenericSource({tags:['a','${MARKER}']}),
    none:workflowGenericSource({tags:['a']}),
    user:workflowUserTags(['a','${MARKER}','b']),
  })`));
  assert.deepEqual(out, { source: 'tpl-backend', none: '', user: ['a', 'b'] });
});
