// Runs without a personal execution machine: on deployments whose cloud
// runner pool covers the e2e project they execute on the platform's builtin
// runner, otherwise on the control-plane host. Either way the nodes of one
// run share a sandbox, so state left outside the workspace by one node is
// visible to the next.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { get, cleanup, ensureProject, waitRun, nodeStatus } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';
import { createWorkflow, script, startRun } from '../lib/fixtures.mjs';

before(async () => {
  await ensureProject();
  const def = await get('/api/runners/default');
  assert.ok(!def.runnerId, `token owner has default runner ${def.runnerId}; this file needs the cloud path`);
});
after(cleanup);

test('cloud path: nodes of one run share the sandbox and the run succeeds', async () => {
  const marker = `/tmp/af-e2e-${Date.now()}`;
  const wf = await createWorkflow('cloud-shared-sandbox', [
    script('first', `printf shared > ${marker} && printf 'first\\n' > first.md`, {
      artifactOutputs: [{ name: 'first', path: 'first.md', mediaType: 'text/markdown', required: true }],
    }),
    script('second', `test "$(cat ${marker})" = shared && rm -f ${marker} && cat .agentflow/inputs/first/first.md > second.md`, {
      artifactOutputs: [{ name: 'second', path: 'second.md', mediaType: 'text/markdown', required: true }],
    }),
  ]);
  const { id } = await startRun(wf, 'e2e 云端执行共享沙箱');
  const run = await waitRun(id, r => r.status === 'succeeded', { label: 'succeeded', timeoutMs: 900_000 });
  assert.equal(nodeStatus(run, 'second'), 'succeeded');
  const ctx = run.executionContext || {};
  if (ctx.executionPool === 'cloud') {
    assert.equal(ctx.executionMode, 'runner');
    assert.ok(ctx.executionRunnerId, 'pool run has no runner');
    console.log(`# ran on builtin runner ${ctx.executionRunnerId}`);
  } else {
    assert.equal(ctx.executionMode, 'cloud');
    console.log(`# cloud runner pool not enabled for ${cfg.projectId}; ran on the control-plane host`);
  }
});
