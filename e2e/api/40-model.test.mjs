// Scenarios that call the real model with the token owner's CodeBuddy key:
// an agent node run, AI run evaluation and the AI assistant. Skipped with
// AGENTFLOW_E2E_MODEL=0.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, put, del, list, cleanup, ensureProject, waitRun, waitFor, nodeOf, track } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';
import { createWorkflow, createAgent, agentNode, script, startRun, repoSkip, childSOPHandoffScenario } from '../lib/fixtures.mjs';

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
  ], { sandboxProfileId: cfg.modelSandbox });
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

// 真实 Agent 在子 SOP 里改父流程检出的代码，父流程下一个节点能直接读到；节点不提交、不推送。
test('agent node in a child SOP edits the parent checkout', { skip: skip || repoSkip, timeout: 900_000 }, async () => {
  const agentId = await createAgent('repo-writer', [
    '你是 e2e 自动化测试角色。严格按以下步骤执行，不要做任何额外的事：',
    '1. 在当前工作目录下找到代码仓库子目录（里面有 parent.txt 文件）。',
    '2. 在 parent.txt 所在的同一个目录创建文件 child.txt，内容只有一行：child',
    '3. 不要 git add、git commit 或 git push，不要修改其他文件，不要访问网络。',
    '4. 回复一句话说明已完成。',
  ].join('\n'));
  const { run } = await childSOPHandoffScenario({
    label: 'model-repo-sop', waitRun, timeoutMs: 840_000, extra: { sandboxProfileId: cfg.modelSandbox },
    childNodes: [agentNode('edit', agentId)],
  });
  assert.equal(run.status, 'succeeded');
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

// A draft is applied by the chat UI with the platform MCP tools/call, the same
// call made here; draft cards themselves are covered by chatbot unit tests.
async function applyDraft(draft) {
  const args = {};
  for (const key of ['path', 'query', 'body']) if (draft[key]) args[key] = draft[key];
  const res = await api('POST', '/mcp', { jsonrpc: '2.0', id: Date.now(), method: 'tools/call', params: { name: draft.tool, arguments: args } }, {
    headers: { Accept: 'application/json, text/event-stream' }, raw: true, expect: [200, 202],
  });
  const json = res.text.trim().startsWith('{') ? JSON.parse(res.text) : JSON.parse((res.text.match(/^data: (.*)$/m) || [])[1] || '{}');
  assert.ok(!json.error && !json.result?.isError, `apply ${draft.tool}: ${res.text.slice(0, 300)}`);
  return json.result;
}

test('AI assistant: a new project asked for inside a project chat is drafted as a top-level project', { skip, timeout: 300_000 }, async () => {
  // A project chat once drafted projects_save with the chat's projectId, so the
  // card named the chat's project as the new project's parent. Not applied here:
  // projects have no delete endpoint.
  const tag = Date.now().toString(36);
  const id = `e2e-auto-np-${tag}`;
  const reply = await post('/api/assistant/chat', {
    projectId: cfg.projectId,
    approvalMode: 'confirm',
    messages: [{ role: 'user', content: `这是自动化测试。帮我新建一个独立项目：id 用 ${id}，名称「E2E 新建项目 ${tag}」，key 用 E2E${tag.slice(-4).toUpperCase()}。直接提交创建项目的草稿，不要做其他事情。` }],
  }, { timeoutMs: 280_000 });
  if (reply.sessionId) track(`assistant session ${reply.sessionId}`, () => del(`/api/assistant/sessions/${reply.sessionId}`));
  const draft = (reply.drafts || []).find(d => d.tool === 'projects_save');
  assert.ok(draft, `no projects_save draft: ${JSON.stringify(reply).slice(0, 500)}`);
  assert.equal(draft.body?.id, id);
  assert.equal('projectId' in (draft.body || {}), false, `new project carries the chat's project: ${JSON.stringify(draft.body)}`);
  assert.equal((reply.drafts || []).filter(d => d.saved).length, 0, 'confirm mode must not save before the user confirms');
  await api('GET', `/api/projects/${id}`, undefined, { expect: 404 });
});

test('AI assistant: a confirmed draft is applied once and the follow-up turn does not repeat it', { skip, timeout: 600_000 }, async () => {
  const id = `e2e-auto-rule-${Date.now().toString(36)}`;
  track(`rule ${id}`, () => del(`/api/rules?id=${id}`));
  const first = await post('/api/assistant/chat', {
    projectId: cfg.projectId,
    approvalMode: 'confirm',
    messages: [{ role: 'user', content: `这是自动化测试。在当前项目新建一条规则：id 用 ${id}，名称「E2E 助手规则」，内容「回答必须以 E2E-OK 结尾。」，priority 50。直接提交草稿，不要做其他事情。` }],
  }, { timeoutMs: 280_000 });
  assert.ok(first.sessionId, 'no session');
  track(`assistant session ${first.sessionId}`, () => del(`/api/assistant/sessions/${first.sessionId}`));
  const draft = (first.drafts || []).find(d => d.tool === 'rules_save');
  assert.ok(draft, `no rules_save draft: ${JSON.stringify(first).slice(0, 500)}`);
  assert.equal(draft.body?.id, id);
  assert.equal(draft.body?.projectId, cfg.projectId, 'a project resource belongs to the chat project');
  assert.equal(list(await get('/api/rules')).some(r => r.id === id), false, 'saved before confirmation');

  const result = await applyDraft(draft);
  const rule = list(await get('/api/rules')).find(r => r.id === id);
  assert.equal(rule?.projectId, cfg.projectId);

  const session = await get(`/api/assistant/sessions/${first.sessionId}`);
  const index = session.messages.findLastIndex(m => m.role === 'assistant' && String(m.content).includes(id));
  assert.ok(index >= 0, 'draft message not stored');
  const stored = JSON.parse(session.messages[index].content);
  const storedDraft = stored.drafts.find(d => d.tool === 'rules_save');
  storedDraft.saved = true;
  storedDraft.savedResult = result.structuredContent?.data || null;
  session.messages[index].content = JSON.stringify(stored);
  const saved = await put(`/api/assistant/sessions/${first.sessionId}`, session);

  const next = await post('/api/assistant/chat', {
    projectId: cfg.projectId,
    sessionId: first.sessionId,
    approvalMode: 'confirm',
    messages: [...saved.messages, { role: 'user', content: '已确认并执行 rules_save。请核对这条草稿的保存回执，继续完成原始请求；不要重复刚才的写入，下一项需要确认时提交草稿，全部完成时总结实际结果。' }],
  }, { timeoutMs: 280_000 });
  assert.ok(next.message, 'empty follow-up reply');
  assert.equal((next.drafts || []).some(d => d.tool === 'rules_save' && d.body?.id === id && !d.saved), false, `follow-up repeated the write: ${JSON.stringify(next.drafts)}`);
  assert.equal(list(await get('/api/rules')).filter(r => r.id === id).length, 1);
});
