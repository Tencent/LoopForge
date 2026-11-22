import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('run header exposes complete input through a native disclosure and escapes markup', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  for (const input of [
    'Short input',
    'First requirement\nSecond requirement\n' + 'Full original input. '.repeat(100) + '\nEND',
    '<img src=x onerror="alert(1)"> & "quoted"\n<script>danger()</script>',
  ]) {
    const run = { ...app.evaluate('structuredClone(state.data.runs[0])'), status: 'succeeded', input };
    app.router.get('/api/runs/run-1', run);
    app.evaluate(`state.data.runs=[${JSON.stringify(run)}];showRunDetail('run-1')`);
    await settle(6);
    const disclosure = app.document.querySelector('.run-header-main .run-input-details');
    assert.ok(disclosure, 'run input needs a visible disclosure control');
    assert.equal(disclosure.open, false);
    assert.match(disclosure.querySelector('summary').getAttribute('aria-label'), /运行输入/);
    const full = disclosure.querySelector('.run-input-full');
    assert.equal(full.textContent, input);
    assert.equal(full.querySelector('img,script'), null, 'input must remain text');
  }
});

test('run header keeps the empty-input fallback without an empty disclosure', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const run = { ...app.evaluate('structuredClone(state.data.runs[0])'), status: 'succeeded', input: '' };
  app.router.get('/api/runs/run-1', run);
  app.evaluate(`state.data.runs=[${JSON.stringify(run)}];showRunDetail('run-1')`);
  await settle(6);
  assert.equal(app.document.querySelector('.run-input-details'), null);
  assert.equal(app.document.querySelector('.run-header-main p').textContent, '无运行输入');
});

test('live output keeps the expanded input, reading position, and keyboard focus', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const workflow = app.evaluate('structuredClone(state.data.workflows[0])');
  const nodeId = workflow.nodes[0].id;
  const run = {
    ...app.evaluate('structuredClone(state.data.runs[0])'), status: 'running',
    input: 'Original input\n' + 'Long original requirement. '.repeat(100), workflowSnapshot: workflow,
    nodes: { [nodeId]: { status: 'running', attempt: 1, output: 'First output' } },
  };
  app.router.get('/api/runs/run-1', run);
  app.evaluate(`state.data.runs=[${JSON.stringify(run)}];showRunDetail('run-1')`);
  await settle(6);
  const disclosure = app.document.querySelector('.run-input-details');
  const full = disclosure.querySelector('.run-input-full');
  disclosure.open = true;
  full.scrollTop = 80;
  full.focus();
  run.nodes[nodeId].output = 'Second output';
  app.evaluate(`patchRunLive(${JSON.stringify(run)})`);
  assert.equal(app.document.querySelector('.run-input-details'), disclosure);
  assert.equal(disclosure.open, true);
  assert.equal(full.scrollTop, 80);
  assert.equal(app.document.activeElement, full);
  app.evaluate(`renderRunDetail(${JSON.stringify({ ...run, id: 'run-other' })})`);
  assert.notEqual(app.document.querySelector('.run-input-details'), disclosure);
  assert.equal(app.document.querySelector('.run-input-details').open, false);
});
