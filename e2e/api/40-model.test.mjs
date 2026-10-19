// Scenarios that call the real model with the token owner's CodeBuddy key:
// an agent node run, AI run evaluation and the AI assistant. Skipped with
// AGENTFLOW_E2E_MODEL=0.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, del, list, cleanup, ensureProject, waitRun, waitFor, nodeOf, track } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';
import { createWorkflow, createAgent, agentNode, script, startRun } from '../lib/fixtures.mjs';

const skip = cfg.model ? false : 'AGENTFLOW_E2E_MODEL=0';
let agentRunId;

before(ensureProject);
after(cleanup);

test('agent node: the real executor writes the declared artifact', { skip, timeout: 900_000 }, async () => {
  const agentId = await createAgent('writer', [
    '你是 e2e 自动化测试角色。严格按以下步骤执行，不要做任何额外的事：',
    '1. 在当前工作目录创建文件 answer.md，内容只有一行：MODEL-OK',
    '2. 回复一句话说明已完成。',
    '不要访问网络，不要安装依赖，不要修改其他文件。',
  ].join('\n'));
  const wf = await createWorkflow('model', [
    script('seed', 'echo "seed" > seed.txt'),
    agentNode('write', agentId, { artifactOutputs: [{ name: 'answer', path: 'answer.md', mediaType: 'text/markdown', required: true }] }),
    script('verify', 'grep -q MODEL-OK answer.md && echo VERIFY_OK'),
  ]);
  const { id } = await startRun(wf, 'e2e 真实模型执行');
  agentRunId = id;
  const run = await waitRun(id, r => r.status === 'succeeded', { label: 'succeeded', timeoutMs: 840_000 });
  const node = nodeOf(run, 'write');
  assert.equal(node.status, 'succeeded');
  assert.ok((run.usage?.totalTokens || node.usage?.totalTokens || 0) > 0, `no token usage recorded: ${JSON.stringify(run.usage)}`);
  const artifact = (run.artifacts || []).find(a => a.name === 'answer');
  assert.ok(artifact, 'declared artifact missing');
  const body = await api('GET', `/api/runs/${id}/artifacts/${encodeURIComponent(artifact.id)}`, undefined, { raw: true });
  assert.match(body.text, /MODEL-OK/);
  const logs = await api('GET', `/api/runs/${id}/logs.txt`, undefined, { raw: true });
  assert.match(logs.text, /VERIFY_OK/);
  const telemetry = await get(`/api/runs/${id}/telemetry?nodeId=write`);
  assert.ok((telemetry.events || []).length > 0, 'agent node produced no telemetry');
  if (run.timing) assert.ok(run.timing.executionMs > 0);
});

test('AI run evaluation completes for a finished run', { skip, timeout: 600_000 }, async () => {
  assert.ok(agentRunId, 'depends on the agent node run');
  await post(`/api/runs/${agentRunId}/eval`, {}, { expect: [200, 201, 202] });
  await api('POST', `/api/runs/${agentRunId}/eval`, {}, { expect: [200, 201, 202, 409] });
  const done = await waitFor('evaluation to finish', async () => {
    const evals = list(await get(`/api/runs/${agentRunId}/evals`));
    const latest = evals[0];
    return latest && latest.status !== 'running' ? latest : null;
  }, { timeoutMs: 540_000, intervalMs: 5_000 });
  assert.equal(done.status, 'succeeded', `evaluation ended ${done.status}: ${done.error || ''}`);
  const current = await get(`/api/runs/${agentRunId}/eval`);
  assert.ok(current);
});

test('evaluation refuses a run that is still active', { skip, timeout: 120_000 }, async () => {
  const wf = await createWorkflow('eval-active', [script('slow', 'sleep 30')]);
  const { id } = await startRun(wf);
  await waitRun(id, r => r.nodes?.slow?.status === 'running', { label: 'running' });
  await api('POST', `/api/runs/${id}/eval`, {}, { expect: 409 });
  await post(`/api/runs/${id}/cancel`, {});
});

test('AI assistant answers a read-only question and keeps the session', { skip, timeout: 300_000 }, async () => {
  const models = await get('/api/assistant/models');
  assert.ok(models);
  await api('POST', '/api/assistant/chat', { projectId: cfg.projectId, messages: [] }, { expect: 400 });
  await api('POST', '/api/assistant/chat', { projectId: cfg.projectId, messages: [{ role: 'assistant', content: 'x' }] }, { expect: 400 });
  const reply = await post('/api/assistant/chat', {
    projectId: cfg.projectId,
    messages: [{ role: 'user', content: '这是自动化测试。请只回答一句话：当前项目叫什么名字？不要创建、修改或删除任何东西。' }],
  }, { timeoutMs: 280_000 });
  assert.ok(reply.message && reply.message.length > 0, 'empty assistant reply');
  assert.equal((reply.drafts || []).filter(d => d.saved).length, 0, 'a read-only question must not save anything');
  if (reply.sessionId) {
    track(`assistant session ${reply.sessionId}`, () => del(`/api/assistant/sessions/${reply.sessionId}`));
    const session = await get(`/api/assistant/sessions/${reply.sessionId}`);
    assert.ok(session);
    const sessions = await get(`/api/assistant/sessions?projectId=${cfg.projectId}`);
    assert.ok(JSON.stringify(sessions).includes(reply.sessionId));
  }
});

test('AI assistant streams over SSE', { skip, timeout: 300_000 }, async () => {
  const res = await fetch(`${cfg.base}/api/assistant/chat`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${cfg.token}`, Accept: 'text/event-stream', 'Content-Type': 'application/json' },
    body: JSON.stringify({ projectId: cfg.projectId, messages: [{ role: 'user', content: '自动化测试，请只回复：OK' }] }),
    signal: AbortSignal.timeout(280_000),
  });
  assert.equal(res.status, 200);
  const text = await res.text();
  assert.match(text, /event: /);
  const sessionId = (text.match(/"sessionId":"([^"]+)"/) || [])[1];
  if (sessionId) await del(`/api/assistant/sessions/${sessionId}`);
});
