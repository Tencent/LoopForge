'use strict';
const assert = require('node:assert/strict');
const { LoopForgeClient } = require('../lib/client');
(async () => {
  const api = new LoopForgeClient({ baseUrl: process.argv[2] });
  const input = 'VS Code acceptance\nfile: src/main.js\nselection:\nline one\nline two';
  const run = await api.createRun({ workflowId: 'vscode-acceptance', projectId: 'platform', input });
  assert.ok(run.id);
  let detail;
  for (let i = 0; i < 100; i++) {
    detail = await api.getRun(run.id);
    if (detail.nodes?.gate?.status === 'waiting_approval') break;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  assert.equal(detail.nodes.gate.status, 'waiting_approval');
  assert.equal(detail.input, input);
  const gate = detail.nodes.gate;
  await assert.rejects(
    () => api.approve(run.id, { nodeId: 'gate', expected: { status: gate.status, attempt: gate.attempt + 1, reworkCount: gate.reworkCount } }),
    error => error.status === 400 && /节点状态已变化/.test(error.message)
  );
  assert.equal((await api.getRun(run.id)).nodes.gate.status, 'waiting_approval');
  await api.approve(run.id, { nodeId: 'gate', context: 'Reviewed', expected: { status: gate.status, attempt: gate.attempt, reworkCount: gate.reworkCount } });
  for (let i = 0; i < 100; i++) {
    detail = await api.getRun(run.id);
    if (detail.status === 'succeeded') break;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  assert.equal(detail.status, 'succeeded');
  const artifact = detail.artifacts.find(item => item.name === 'decision');
  assert.ok(artifact?.id, 'approval decision artifact missing');
  const content = await api.getArtifact(run.id, artifact.id);
  assert.ok(content.includes('Reviewed'));
  assert.ok(Array.isArray((await api.getLogs(run.id)).logs));
  console.log('Real platform: created run, preserved context, approved, inspected result and retrieved artifact.');
})().catch(error => { console.error(error); process.exitCode = 1; });
