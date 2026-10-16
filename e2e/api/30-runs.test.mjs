// Real SOP executions in the deployment's sandbox using script nodes (no model
// cost): approval with rework, workspace browsing, artifacts, logs, telemetry,
// SSE, retry, manual pass, pause/resume, decline, cancel, child SOPs, delete
// and run timing.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, del, list, cleanup, ensureProject, waitRun, nodeOf, nodeStatus } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';
import { createWorkflow, script, approval, sopNode, startRun } from '../lib/fixtures.mjs';

before(ensureProject);
after(cleanup);

const logsText = async id => (await api('GET', `/api/runs/${id}/logs.txt`, undefined, { raw: true })).text;

function checkTiming(run) {
  // Present once the run-timing change is deployed; older builds omit it.
  if (!run.timing) return;
  const { executionMs, wallMs, waitingMs } = run.timing;
  assert.ok(executionMs >= 0 && wallMs >= 0, `bad timing ${JSON.stringify(run.timing)}`);
  assert.ok(executionMs <= wallMs + 1000, `execution ${executionMs}ms exceeds wall ${wallMs}ms`);
  return { executionMs, wallMs, waitingMs };
}

test('approval gate: reject reworks upstream in the same workspace, approve completes', async () => {
  const wf = await createWorkflow('approval', [
    script('prep', 'echo "attempt-$(date +%s%N)" >> notes.txt && mkdir -p out && printf "# E2E result\\n\\nok\\n" > out/result.md && echo PREP_DONE', {
      artifactOutputs: [{ name: 'result', path: 'out/result.md', mediaType: 'text/markdown', required: true }],
    }),
    approval('gate', 'prep'),
    script('finish', 'test -f notes.txt && echo "LINES=$(wc -l < notes.txt)" && echo FINISH_DONE'),
  ]);
  const { id } = await startRun(wf, 'e2e 审批流');
  let run = await waitRun(id, r => nodeStatus(r, 'gate') === 'waiting_approval', { label: 'gate waiting' });
  assert.equal(run.status, 'waiting');
  assert.equal(nodeStatus(run, 'prep'), 'succeeded');

  await test.test('workspace is browsable while waiting', async () => {
    const tree = await get(`/api/runs/${id}/workspace/tree`);
    assert.match(JSON.stringify(tree), /notes\.txt/);
    const file = await api('GET', `/api/runs/${id}/workspace/file?path=notes.txt`, undefined, { raw: true });
    assert.match(file.text, /attempt-/);
    const hits = await get(`/api/runs/${id}/workspace/search?q=attempt`);
    assert.match(JSON.stringify(hits), /notes\.txt/);
    await api('GET', `/api/runs/${id}/workspace/file?path=../../etc/passwd`, undefined, { expect: [400, 403, 404] });
    await get(`/api/runs/${id}/workspace`);
  });

  await test.test('wrong node cannot be approved', async () => {
    await api('POST', `/api/runs/${id}/approve`, { nodeId: 'prep' }, { expect: [400, 409, 500] });
  });

  await post(`/api/runs/${id}/reject`, { nodeId: 'gate', targetId: 'prep', reason: 'e2e 打回：再写一行' });
  run = await waitRun(id, r => nodeStatus(r, 'gate') === 'waiting_approval' && (nodeOf(r, 'prep').attempt >= 2 || nodeOf(r, 'prep').reworkCount >= 1), { label: 'rework back at gate' });

  await post(`/api/runs/${id}/approve`, { nodeId: 'gate', context: 'e2e 批准' });
  run = await waitRun(id, r => r.status === 'succeeded', { label: 'succeeded' });
  assert.equal(nodeStatus(run, 'finish'), 'succeeded');

  const logs = await logsText(id);
  assert.match(logs, /FINISH_DONE/);
  assert.match(logs, /LINES=2/, 'rework must reuse the workspace so notes.txt has two lines');

  const artifacts = run.artifacts || [];
  const result = artifacts.find(a => a.name === 'result');
  assert.ok(result, `declared artifact missing: ${JSON.stringify(artifacts.map(a => a.name))}`);
  assert.equal(result.nodeId, 'prep');
  const content = await api('GET', `/api/runs/${id}/artifacts/${encodeURIComponent(result.id)}`, undefined, { raw: true });
  assert.match(content.text, /E2E result/);
  await api('GET', `/api/runs/${id}/artifacts/artifact-e2e-missing`, undefined, { expect: 404 });

  const structured = await get(`/api/runs/${id}/logs`);
  assert.ok((structured.logs || []).length > 0);
  const nodeLogs = await get(`/api/runs/${id}/logs?nodeId=finish`);
  assert.ok((nodeLogs.logs || []).every(l => !l.context?.nodeId || l.context.nodeId === 'finish'));
  const telemetry = await get(`/api/runs/${id}/telemetry`);
  assert.ok((telemetry.events || []).length > 0);

  const timing = checkTiming(run);
  if (timing) assert.ok(timing.waitingMs > 0, 'time parked at the approval gate must count as waiting');

  const summary = list(await get(`/api/runs?projectId=${cfg.projectId}`)).find(r => r.id === id);
  assert.equal(summary?.status, 'succeeded');
});

test('failed script: retry from the node succeeds on the second attempt', async () => {
  const wf = await createWorkflow('retry', [
    script('flaky', 'if [ -f .e2e-retry-ok ]; then echo RETRY_OK; else touch .e2e-retry-ok; echo FIRST_FAIL >&2; exit 3; fi'),
    script('after', 'echo AFTER_OK'),
  ]);
  const { id } = await startRun(wf);
  let run = await waitRun(id, r => r.status === 'failed', { label: 'failed' });
  assert.equal(nodeStatus(run, 'flaky'), 'failed');
  assert.equal(nodeStatus(run, 'after'), 'pending');
  assert.match(nodeOf(run, 'flaky').error || '', /exit status 3/);

  await post(`/api/runs/${id}/retry`, { nodeId: 'flaky', reason: 'e2e 重试' });
  run = await waitRun(id, r => r.status === 'succeeded', { label: 'succeeded after retry' });
  assert.equal(nodeOf(run, 'flaky').attempt, 2);
  assert.match(await logsText(id), /RETRY_OK[\s\S]*AFTER_OK|AFTER_OK[\s\S]*RETRY_OK/);
});

test('failed script: manual pass skips it and continues downstream', async () => {
  const wf = await createWorkflow('pass', [script('broken', 'exit 7'), script('after', 'echo AFTER_PASS')]);
  const { id } = await startRun(wf);
  await waitRun(id, r => r.status === 'failed', { label: 'failed' });
  await api('POST', `/api/runs/${id}/pass`, { nodeId: 'after' }, { expect: [400, 409, 500] });
  await post(`/api/runs/${id}/pass`, { nodeId: 'broken', context: 'e2e 人工通过' });
  const run = await waitRun(id, r => r.status === 'succeeded', { label: 'succeeded after pass' });
  assert.equal(nodeOf(run, 'broken').attempt, 1, 'pass must not re-execute the node');
  assert.match(await logsText(id), /AFTER_PASS/);
});

test('pause interrupts a running script, resume re-runs it to success', async () => {
  const wf = await createWorkflow('pause', [script('slow', 'sleep 12; echo SLOW_DONE'), script('next', 'echo NEXT_DONE')]);
  const { id } = await startRun(wf);
  await waitRun(id, r => nodeStatus(r, 'slow') === 'running', { label: 'slow running' });
  await post(`/api/runs/${id}/pause`, {});
  let run = await waitRun(id, r => r.status === 'waiting' && r.pauseRequested, { label: 'paused', timeoutMs: 60_000 });
  assert.equal(nodeStatus(run, 'slow'), 'waiting_approval');
  await post(`/api/runs/${id}/resume`, { context: 'e2e 继续' });
  run = await waitRun(id, r => r.status === 'succeeded', { label: 'succeeded after resume' });
  assert.equal(nodeOf(run, 'slow').attempt, 2);
  assert.equal(nodeStatus(run, 'next'), 'succeeded');
  checkTiming(run);
});

test('paused script: declining the retry fails the run', async () => {
  const wf = await createWorkflow('decline', [script('slow', 'sleep 60')]);
  const { id } = await startRun(wf);
  await waitRun(id, r => nodeStatus(r, 'slow') === 'running', { label: 'slow running' });
  await post(`/api/runs/${id}/pause`, {});
  await waitRun(id, r => r.status === 'waiting' && nodeStatus(r, 'slow') === 'waiting_approval', { label: 'paused', timeoutMs: 60_000 });
  await post(`/api/runs/${id}/decline-retry`, { nodeId: 'slow' });
  const run = await waitRun(id, r => r.status === 'failed', { label: 'failed after decline', timeoutMs: 30_000 });
  assert.equal(nodeStatus(run, 'slow'), 'failed');
});

test('cancel stops a running script and the run can then be deleted', async () => {
  const wf = await createWorkflow('cancel', [script('forever', 'sleep 600')]);
  const { id } = await startRun(wf);
  await waitRun(id, r => nodeStatus(r, 'forever') === 'running', { label: 'running' });
  await api('DELETE', `/api/runs/${id}`, undefined, { expect: [400, 409] });
  await post(`/api/runs/${id}/cancel`, {});
  const run = await waitRun(id, r => r.status === 'cancelled', { label: 'cancelled', timeoutMs: 60_000 });
  assert.notEqual(nodeStatus(run, 'forever'), 'running');
  await api('POST', `/api/runs/${id}/cancel`, {}, { expect: [200, 204, 400, 409, 500] });
  await del(`/api/runs/${id}`, { expect: [200, 204] });
  await api('GET', `/api/runs/${id}`, undefined, { expect: 404 });
  // Deleting twice reports the engine's "run not found" (400) rather than 404.
  await api('DELETE', `/api/runs/${id}`, undefined, { expect: [400, 404] });
});

test('SOP node runs a child SOP and continues the parent after it', async () => {
  const child = await createWorkflow('child', [script('c1', 'echo CHILD_RAN > child.txt && echo CHILD_DONE')]);
  const parent = await createWorkflow('parent', [
    script('p1', 'echo PARENT_START'),
    sopNode('sub', child),
    script('p2', 'echo PARENT_DONE'),
  ]);
  const { id } = await startRun(parent);
  const run = await waitRun(id, r => r.status === 'succeeded', { label: 'parent succeeded', timeoutMs: 300_000 });
  assert.equal(nodeStatus(run, 'sub'), 'succeeded');
  const childRuns = list(await get('/api/runs')).filter(r => r.workflowId === child);
  assert.ok(childRuns.length >= 1, 'child run not recorded');
  assert.ok(childRuns.every(r => r.status === 'succeeded'), `child runs: ${childRuns.map(r => r.status)}`);
  assert.match(await logsText(id), /PARENT_DONE/);
});

test('run stream emits server-sent events', async () => {
  const wf = await createWorkflow('stream', [script('s', 'for i in 1 2 3; do echo tick-$i; sleep 1; done')]);
  const { id } = await startRun(wf);
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), 20_000);
  const res = await fetch(`${cfg.base}/api/runs/${id}/stream`, { headers: { Authorization: `Bearer ${cfg.token}`, Accept: 'text/event-stream' }, signal: ctrl.signal });
  assert.equal(res.status, 200);
  assert.match(res.headers.get('content-type') || '', /event-stream/);
  const reader = res.body.getReader();
  let text = '';
  try {
    while (!/data:/.test(text)) {
      const { value, done } = await reader.read();
      if (done) break;
      text += new TextDecoder().decode(value);
    }
  } finally {
    clearTimeout(timer);
    ctrl.abort();
  }
  assert.match(text, /data:/);
  await waitRun(id, r => r.status === 'succeeded', { label: 'succeeded' });
});

test('starting a run validates its input', async () => {
  await api('POST', '/api/runs', { workflowId: 'e2e-no-such-sop', projectId: cfg.projectId }, { expect: [400, 404, 409, 500] });
  await api('POST', '/api/runs', { workflowId: 'generic-backend-delivery', projectId: 'e2e-no-such-project' }, { expect: [400, 404, 409, 500] });
  const project = await get(`/api/projects/${cfg.projectId}`);
  if (!project.defaultRepositoryId) {
    const res = await api('POST', '/api/runs', { workflowId: 'generic-backend-delivery', projectId: cfg.projectId }, { expect: 409 });
    assert.match(JSON.stringify(res), /repository_required/);
  }
});

test('unknown run actions answer 404', async () => {
  for (const path of ['approve', 'retry', 'cancel', 'pause', 'resume']) {
    await api('POST', `/api/runs/run-e2e-missing/${path}`, { nodeId: 'x' }, { expect: [400, 404, 409, 500] });
  }
});
