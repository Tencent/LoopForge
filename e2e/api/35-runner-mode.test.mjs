// Runs script SOPs on an execution machine the suite starts itself: the runner
// binary served by the deployment registers with a bind code and is the token
// owner's default machine for the duration of this file (the previous default
// is restored afterwards). Upstream artifacts must reach downstream nodes on
// the runner, and the control plane registers what the runner reports.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtempSync, writeFileSync, readFileSync, chmodSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { api, get, post, put, list, cleanup, ensureProject, waitFor, waitRun, nodeStatus } from '../lib/api.mjs';
import { cfg, uid } from '../lib/env.mjs';
import { createWorkflow, script, startRun } from '../lib/fixtures.mjs';

const dir = mkdtempSync(join(tmpdir(), 'e2e-runner-'));
let proc;
let runnerId = '';
let previousDefault = '';
let sandboxProfileId = cfg.sandbox;

before(async () => {
  await ensureProject();
  const profiles = list(await get('/api/sandboxes'));
  if (profiles.some(p => p.id === 'local')) sandboxProfileId = 'local';
  previousDefault = (await get('/api/runners/default')).runnerId || '';

  const binary = join(dir, 'agentflow-runner');
  const res = await fetch(`${cfg.base}/runners/agentflow-runner?os=linux&arch=amd64`, { signal: AbortSignal.timeout(120_000) });
  assert.equal(res.status, 200, 'runner binary download');
  writeFileSync(binary, Buffer.from(await res.arrayBuffer()));
  chmodSync(binary, 0o755);

  const { code } = await post('/api/runners/bind-codes', {});
  const name = uid('runner');
  proc = spawn(binary, [
    '--server', cfg.base, '--token', code, '--name', name,
    '--state', join(dir, 'state.json'), '--workspace', join(dir, 'workspace'),
    '--heartbeat', '5s', '--long-poll', '40s', '--auto-update=false',
  ], { stdio: ['ignore', 'pipe', 'pipe'] });
  let log = '';
  proc.stdout.on('data', d => { log += d; });
  proc.stderr.on('data', d => { log += d; });
  proc.on('exit', code => { if (code) log += `\nexited ${code}`; });
  process.on('exit', () => proc?.kill('SIGKILL'));

  const runner = await waitFor('runner online', async () => {
    const found = list(await get('/api/runners')).find(r => r.name === name);
    return found?.status === 'online' ? found : null;
  }, { timeoutMs: 60_000 }).catch(err => { throw new Error(`${err.message}\n${log.slice(-2000)}`); });
  runnerId = runner.id;
  assert.ok(runner.runnerVersion >= '0.3.0', `runner ${runner.runnerVersion} predates reported outputs`);
  await put('/api/runners/default', { runnerId });
});

after(async () => {
  await put('/api/runners/default', { runnerId: previousDefault }, { expect: [200, 404] }).catch(() =>
    put('/api/runners/default', { runnerId: '' }));
  await cleanup();
  const state = existsSync(join(dir, 'state.json')) ? JSON.parse(readFileSync(join(dir, 'state.json'), 'utf8')) : null;
  proc?.kill('SIGTERM');
  await new Promise(r => setTimeout(r, 1500));
  if (state?.sessionToken) {
    await api('POST', '/runners/deregister', {}, { auth: false, headers: { Authorization: `Bearer ${state.sessionToken}` }, expect: [200, 401] })
      .catch(err => console.log(`# runner deregister skipped: ${err.message.slice(0, 120)}`));
  }
  rmSync(dir, { recursive: true, force: true });
});

const artifactText = async (runId, artifact) =>
  (await api('GET', `/api/runs/${runId}/artifacts/${encodeURIComponent(artifact.id)}`, undefined, { raw: true })).text;

test('runner mode: upstream artifact reaches the downstream node on the runner', async () => {
  const wf = await createWorkflow('runner-handoff', [
    script('plan', 'printf "plan from runner\\n" > plan.md && echo PLAN_DONE', {
      artifactOutputs: [{ name: 'plan', path: 'plan.md', mediaType: 'text/markdown', required: true }],
    }),
    script('review', 'cat .agentflow/inputs/plan/plan.md > review.md && echo REVIEW_DONE', {
      artifactOutputs: [{ name: 'review', path: 'review.md', mediaType: 'text/markdown', required: true }],
    }),
  ], { sandboxProfileId });
  const { id } = await startRun(wf, 'e2e 执行机器产物交接');
  const run = await waitRun(id, r => r.status === 'succeeded', { label: 'succeeded on runner' });
  assert.equal(run.executionContext?.executionMode, 'runner');
  assert.equal(run.executionContext?.executionRunnerId, runnerId);
  assert.equal(nodeStatus(run, 'review'), 'succeeded');
  const review = (run.artifacts || []).find(a => a.nodeId === 'review' && a.name === 'review');
  assert.ok(review, `review artifact missing: ${JSON.stringify((run.artifacts || []).map(a => a.nodeId + '/' + a.name))}`);
  assert.equal(await artifactText(id, review), 'plan from runner\n');
});

test('runner mode: a failed node keeps the artifact it already wrote', async () => {
  const wf = await createWorkflow('runner-failure', [
    script('diagnose', 'printf "why it failed\\n" > diagnosis.md && exit 3', {
      artifactOutputs: [{ name: 'diagnosis', path: 'diagnosis.md', mediaType: 'text/markdown', required: true }],
    }),
  ], { sandboxProfileId });
  const { id } = await startRun(wf, 'e2e 执行机器失败产物');
  const run = await waitRun(id, r => r.status === 'failed', { label: 'failed on runner' });
  const diagnosis = (run.artifacts || []).find(a => a.nodeId === 'diagnose' && a.name === 'diagnosis');
  assert.ok(diagnosis, 'failed node lost its artifact');
  assert.equal(await artifactText(id, diagnosis), 'why it failed\n');
});

test('runner mode: a missing required artifact fails the node with a contract error', async () => {
  const wf = await createWorkflow('runner-contract', [
    script('forgetful', 'echo "no file written"', {
      artifactOutputs: [{ name: 'report', path: 'report.md', mediaType: 'text/markdown', required: true }],
    }),
  ], { sandboxProfileId });
  const { id } = await startRun(wf, 'e2e 执行机器产物契约');
  const run = await waitRun(id, r => r.status === 'failed', { label: 'contract failure' });
  assert.match(String(run.nodes?.forgetful?.error || ''), /artifact contract|report/);
});
