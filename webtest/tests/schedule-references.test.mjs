import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('asset references save alongside session settings without enabling a paused AI job', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.put('/api/schedule-jobs', call => ({ ...call.body }));
  let jobs = [];
  app.router.get('/api/schedule-jobs', () => jobs);
  const reference = { type: 'issue', id: 'issue-1', name: 'Current title', projectId: 'project-1' };
  for (const sessionMode of ['', 'fixed']) {
    const job = { id: 'referenced-job', projectId: 'project-1', name: 'Inspect', skipDispatch: true,
      assistantPrompt: 'Inspect the asset', enabled: false, intervalSec: 3600,
      sessionMode, sessionKeep: -1, references: [reference] };
    jobs = [job];
    await app.evaluate(`openScheduleJobModal('assistant',${JSON.stringify(job)})`);
    const form = app.document.querySelector('#scheduleJobForm');
    assert.match(app.document.querySelector('#scheduleReferenceList').textContent, /Current title/);
    form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
    await settle(4);
    const save = app.router.calls.filter(call => call.method === 'PUT' && call.path === '/api/schedule-jobs').at(-1);
    assert.deepEqual(save.body.references, [{ type: 'issue', id: 'issue-1' }]);
    assert.equal(save.body.sessionMode, sessionMode);
    assert.equal(save.body.sessionKeep, -1);
    assert.equal(save.body.enabled, false);
  }
});

test('Escape closes the asset picker first and preserves the schedule draft and focus', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/assistant/references', { items: [{ type: 'issue', id: 'issue-1', name: 'Current title' }] });
  await app.evaluate("openScheduleJobModal('assistant')");
  const form = app.document.querySelector('#scheduleJobForm');
  const modal = app.document.querySelector('#scheduleModal');
  const add = app.document.querySelector('#scheduleAddReference');
  const picker = app.document.querySelector('#scheduleReferencePicker');
  const query = app.document.querySelector('#scheduleReferenceQuery');
  form.elements.assistantPrompt.value = 'Unsaved schedule draft';
  const escape = target => target.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
  for (const target of ['query', 'option', 'selection']) {
    add.click();
    await settle(4);
    const option = app.document.querySelector('[data-schedule-reference]');
    assert.ok(option);
    if (target === 'option') option.focus();
    if (target === 'selection') {
      option.click();
      await settle(4);
      assert.equal(app.document.activeElement, query, 'selection restores search focus');
    }
    escape(app.document.activeElement);
    assert.equal(picker.hidden, true);
    assert.equal(add.getAttribute('aria-expanded'), 'false');
    assert.equal(app.document.activeElement, add);
    assert.equal(modal.classList.contains('open'), true);
    assert.equal(form.elements.assistantPrompt.value, 'Unsaved schedule draft');
  }
  escape(add);
  assert.equal(modal.classList.contains('open'), false, 'Escape still closes the editor when the picker is hidden');
});
