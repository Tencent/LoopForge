import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

function installPromptEditor(app) {
  let options;
  app.window.LoopForgePromptEditor = {
    mount(_host, input) {
      options = input;
      return {
        setValue(next) { options.value = next; },
        destroy() {},
      };
    },
  };
  return () => options;
}

test('asset references use the shared prompt editor and save with session settings', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const getEditor = installPromptEditor(app);
  app.router.put('/api/schedule-jobs', call => ({ ...call.body }));
  const reference = { type: 'skill', id: 'skill-1', name: 'Current title', projectId: 'project-1' };
  const job = { id: 'referenced-job', projectId: 'project-1', name: 'Inspect', skipDispatch: true,
    assistantPrompt: 'Inspect the asset', enabled: false, intervalSec: 3600,
    sessionMode: 'fixed', sessionKeep: -1, references: [reference] };
  app.router.get('/api/schedule-jobs', () => [job]);
  await app.evaluate(`openScheduleJobModal('assistant',${JSON.stringify(job)})`);
  const form = app.document.querySelector('#scheduleJobForm');
  assert.equal(form.querySelector('[data-schedule-reference-editor]').hidden, false);
  assert.equal(form.querySelector('[data-schedule-prompt]').hidden, true);
  assert.equal(getEditor().value.text, 'Inspect the asset');
  assert.deepEqual(getEditor().value.references, [reference]);
  getEditor().onChange({ text: 'Inspect @Current title ', references: [reference] });
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(4);
  const save = app.router.calls.filter(call => call.method === 'PUT' && call.path === '/api/schedule-jobs').at(-1);
  assert.equal(save.body.assistantPrompt, 'Inspect @Current title');
  assert.deepEqual(save.body.references, [{ type: 'skill', id: 'skill-1' }]);
  assert.equal(save.body.sessionMode, 'fixed');
  assert.equal(save.body.sessionKeep, -1);
  assert.equal(save.body.enabled, false);
});

test('reopening an AI task resets the shared prompt editor draft', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const getEditor = installPromptEditor(app);
  const first = { id: 'first', projectId: 'project-1', name: 'First', skipDispatch: true,
    assistantPrompt: 'first prompt', references: [{ type: 'skill', id: 'skill-1', name: 'Testing' }] };
  await app.evaluate(`openScheduleJobModal('assistant',${JSON.stringify(first)})`);
  assert.equal(getEditor().value.text, 'first prompt');
  const second = { id: 'second', projectId: 'project-1', name: 'Second', skipDispatch: true,
    assistantPrompt: 'second prompt', references: [] };
  await app.evaluate(`openScheduleJobModal('assistant',${JSON.stringify(second)})`);
  assert.equal(getEditor().value.text, 'second prompt');
  assert.deepEqual(getEditor().value.references, []);
});

test('subscription references follow the pinned version and cannot retain a local draft', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const getEditor = installPromptEditor(app);
  const first = { type: 'skill', id: 'published-skill', name: 'Published skill' };
  const second = { type: 'agent', id: 'published-role', name: 'Published role' };
  const v1 = { id: 'shared-ref', version: 1, name: 'Shared', sourceProjectId: 'source', prompt: 'Version one', references: [first], parameters: [] };
  const v2 = { ...v1, version: 2, prompt: 'Version two', references: [second] };
  app.router.get('/api/shared-schedule-tasks/shared-ref/versions', () => [v2, v1]);
  const job = { id: 'sub', projectId: 'project-1', name: 'Subscription', skipDispatch: true,
    sharedTaskId: v1.id, sharedTaskVersion: 1, references: [{ type: 'issue', id: 'forged-local' }] };
  await app.evaluate(`openScheduleJobModal('assistant',${JSON.stringify(job)})`);
  assert.equal(getEditor().value.readOnly, true);
  assert.equal(getEditor().value.text, v1.prompt);
  assert.deepEqual(getEditor().value.references, [first]);
  await app.evaluate("openSharedScheduleVersions('shared-ref',true)");
  assert.equal(getEditor().value.text, v2.prompt);
  assert.deepEqual(getEditor().value.references, [second]);
  const selector = app.document.querySelector('#sharedScheduleVersionEditor select');
  selector.value = '1';
  selector.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  assert.deepEqual(getEditor().value.references, [first]);
  await app.evaluate("openScheduleJobModal('assistant')");
  assert.equal(getEditor().value.readOnly, false);
  assert.deepEqual(getEditor().value.references, []);
  await app.evaluate(`sharedScheduleRowInfo(${JSON.stringify(job)});syncSchedulePromptEditor()`);
  assert.deepEqual(getEditor().value.references, []);
});
