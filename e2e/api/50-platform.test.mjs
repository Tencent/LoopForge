// Knowledge center (read + review queue, never written back), collaboration
// rooms, scheduled jobs, assistant tasks, runners, the platform MCP endpoint
// and user endpoints.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, put, del, list, track, cleanup, ensureProject, waitFor, waitRun } from '../lib/api.mjs';
import { cfg, uid } from '../lib/env.mjs';
import { createWorkflow, script, trackRun } from '../lib/fixtures.mjs';

const P = cfg.projectId;
before(ensureProject);
after(cleanup);

test('knowledge: search and fetch a cited document', async t => {
  const settings = await get('/api/knowledge/settings');
  if (!settings.enabled || !settings.ready) return t.skip('knowledge base not configured on this deployment');
  assert.equal(settings.writeEnabled, false, 'e2e refuses to run while KB write-back is enabled');
  const sources = list(await get(`/api/knowledge/sources?projectId=${P}`));
  assert.ok(sources.length > 0, 'no knowledge sources visible');
  const result = await post('/api/knowledge/search', { query: '部署', projectId: P }, { timeoutMs: 90_000 });
  const hits = list(result.result?.results ?? result.results);
  assert.ok(hits.length > 0, `search returned nothing: ${JSON.stringify(result).slice(0, 300)}`);
  const hit = hits[0];
  assert.ok(hit.source && hit.path, `hit lacks a citation: ${JSON.stringify(hit).slice(0, 200)}`);
  const doc = await get(`/api/knowledge/document?projectId=${P}&source=${encodeURIComponent(hit.source)}&path=${encodeURIComponent(hit.path)}&start=${hit.start_line || 1}&end=${hit.end_line || 40}`, { timeoutMs: 60_000 });
  assert.ok(JSON.stringify(doc).length > 50, 'document fetch returned no content');
  await api('POST', '/api/knowledge/search', { query: '', projectId: P }, { expect: [400, 200] });
});

test('knowledge: review queue create, edit, reject, delete (no write-back)', async () => {
  const item = await post('/api/knowledge/submissions', {
    projectId: P, title: `E2E 知识条目 ${cfg.runTag}`, content: '---\ntitle: e2e\n---\n自动化测试条目，会被驳回并删除。',
  });
  assert.ok(item.id);
  track(`knowledge submission ${item.id}`, () => del(`/api/knowledge/submissions/${item.id}`));
  assert.equal((await get(`/api/knowledge/submissions/${item.id}`)).id, item.id);
  const edited = await put(`/api/knowledge/submissions/${item.id}`, { title: 'E2E 已编辑', content: '改过的内容' });
  assert.equal(edited.title, 'E2E 已编辑');
  const pending = list(await get(`/api/knowledge/submissions?projectId=${P}`));
  assert.ok(pending.some(s => s.id === item.id));
  const settings = await get('/api/knowledge/settings');
  if (!settings.writeEnabled) {
    // Publishing is the write-back path; with writes disabled it must refuse.
    await api('POST', `/api/knowledge/submissions/${item.id}/publish`, {}, { expect: settings.enabled && settings.ready ? [400,403,409] : 503 });
  }
  const rejected = await post(`/api/knowledge/submissions/${item.id}/reject`, { reviewNote: 'e2e 驳回' });
  assert.equal(rejected.status, 'rejected');
  await del(`/api/knowledge/submissions/${item.id}`, { expect: [200, 204] });
  await api('GET', `/api/knowledge/submissions/${item.id}`, undefined, { expect: [404, 400] });
});

test('knowledge: usage and candidates answer for the project', async () => {
  assert.ok(await get(`/api/knowledge/usage?projectId=${P}`));
  assert.ok(await get(`/api/knowledge/candidates?projectId=${P}`));
});

test('rooms: create, message, and turn a message into a dispatched requirement', { timeout: 300_000 }, async () => {
  await api('GET', '/api/rooms', undefined, { expect: 400 });
  await api('POST', '/api/rooms', { projectId: P }, { expect: 400 });
  // Rooms have no delete endpoint; reuse one fixed room across runs.
  const rooms = list(await get(`/api/rooms?projectId=${P}`));
  const room = rooms.find(r => r.name === 'E2E 自动化') || await post('/api/rooms', { projectId: P, name: 'E2E 自动化' });
  assert.ok(room.id);
  assert.equal((await get(`/api/rooms/${room.id}`)).id, room.id);

  const msg = await post(`/api/rooms/${room.id}/messages`, { body: `e2e 消息 ${cfg.runTag}` });
  assert.ok(msg.id);
  await api('POST', `/api/rooms/${room.id}/messages`, { body: '' }, { expect: 400 });
  const messages = list(await get(`/api/rooms/${room.id}/messages`));
  assert.ok(messages.some(m => m.id === msg.id));

  const wf = await createWorkflow('room-sop', [script('r', 'echo ROOM_RUN_OK')]);
  await api('POST', `/api/rooms/${room.id}/issues`, { title: 'x' }, { expect: 400 });
  const created = await post(`/api/rooms/${room.id}/issues`, { title: `E2E 群需求 ${cfg.runTag}`, description: '自动化测试', workflowId: wf });
  const issueId = created.issue?.id;
  assert.ok(issueId, `room issue not created: ${JSON.stringify(created).slice(0, 300)}`);
  track(`issue ${issueId}`, () => post(`/api/issues/${issueId}/close`, {}, { expect: [200, 404, 409] }));
  assert.ok(!created.dispatchError, `room dispatch failed: ${created.dispatchError}`);
  const runId = created.task?.runId || created.tasks?.[0]?.runId;
  assert.ok(runId, 'room dispatch started no run');
  trackRun(runId);
  await waitRun(runId, r => r.status === 'succeeded', { label: 'room run succeeded' });
});

test('schedule job: a manual trigger dispatches todo requirements of the project', { timeout: 300_000 }, async () => {
  const wf = await createWorkflow('sched-sop', [script('s', 'echo SCHEDULED_OK')]);
  const issue = await post('/api/issues', { projectId: P, title: `E2E 定时触发 ${cfg.runTag}`, status: 'todo', workflowId: wf });
  track(`issue ${issue.id}`, () => post(`/api/issues/${issue.id}/close`, {}, { expect: [200, 404, 409] }));

  const id = uid('job');
  track(`schedule job ${id}`, () => del(`/api/schedule-jobs/${id}`));
  await api('POST', '/api/schedule-jobs', { id: uid('job-bad'), name: 'bad', projectId: P, enabled: false, cron: 'not a cron' }, { expect: 400 });
  const job = await post('/api/schedule-jobs', { id, name: 'E2E 定时任务', projectId: P, enabled: false, workflowId: wf, useIssueWorkflow: true, issueStatuses: ['todo'], cron: '0 3 * * *' });
  assert.equal(job.enabled, false, 'e2e jobs must stay disabled');
  const saved = await put('/api/schedule-jobs', { ...job, name: 'E2E 定时任务（改）' });
  assert.equal(saved.name, 'E2E 定时任务（改）');
  assert.ok(list(await get('/api/schedule-jobs')).some(j => j.id === id));

  await post(`/api/schedule-jobs/${id}/run`, {});
  const runs = await waitFor('scheduled dispatch', async () => {
    const items = list(await get(`/api/issues/${issue.id}/runs`));
    return items.length ? items : null;
  }, { timeoutMs: 120_000 });
  trackRun(runs[0].id);
  await waitRun(runs[0].id, r => r.status === 'succeeded', { label: 'scheduled run succeeded' });
  const after = list(await get('/api/schedule-jobs')).find(j => j.id === id);
  assert.ok(['success', 'waiting', 'running'].includes(after.lastStatus), `job status ${after.lastStatus}: ${after.lastError || ''}`);
});

test('assistant task CRUD (disabled, never scheduled)', async () => {
  const task = await put('/api/assistant/tasks', { name: 'E2E 助手任务', prompt: '自动化测试，不要执行任何操作', projectId: P, enabled: false, cron: '0 4 * * *' });
  assert.ok(task.id);
  track(`assistant task ${task.id}`, () => del(`/api/assistant/tasks/${task.id}`));
  const tasks = list(await get(`/api/assistant/tasks?projectId=${P}`));
  assert.ok(tasks.some(t => t.id === task.id));
  const edited = await put('/api/assistant/tasks', { ...task, name: 'E2E 助手任务（改）' });
  assert.equal(edited.name, 'E2E 助手任务（改）');
  await del(`/api/assistant/tasks/${task.id}`, { expect: [200, 204] });
});

test('runners: list, default runner and bind code issuance', async () => {
  assert.ok(await get('/api/runners'));
  const def = await get('/api/runners/default');
  assert.ok('online' in def);
  const code = await post('/api/runners/bind-codes', {});
  assert.ok(code.code || code.bindCode || code.token, `no bind code: ${JSON.stringify(code)}`);
  const script = await api('GET', '/deploy/bind-cli.sh', undefined, { auth: false, raw: true });
  assert.match(script.text, /#!/);
});

async function mcp(method, params, id = 1) {
  const res = await api('POST', '/mcp', { jsonrpc: '2.0', id, method, params }, {
    headers: { Accept: 'application/json, text/event-stream' }, raw: true, expect: [200, 202],
  });
  const text = res.text;
  const json = text.trim().startsWith('{') ? JSON.parse(text) : JSON.parse((text.match(/^data: (.*)$/m) || [])[1] || '{}');
  if (json.error) throw new Error(`${method}: ${JSON.stringify(json.error)}`);
  return json.result;
}

test('platform MCP: initialize, list tools and call read-only tools', async () => {
  const init = await mcp('initialize', { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 'agentflow-e2e', version: '1' } });
  assert.ok(init.serverInfo || init.capabilities);
  const tools = (await mcp('tools/list', {}, 2)).tools || [];
  const names = tools.map(t => t.name);
  for (const expected of ['projects_list', 'workflows_list', 'runs_get', 'issues_list']) {
    assert.ok(names.includes(expected), `MCP tool ${expected} missing`);
  }
  const projects = await mcp('tools/call', { name: 'projects_list', arguments: {} }, 3);
  assert.match(JSON.stringify(projects), new RegExp(P));
  const project = await mcp('tools/call', { name: 'projects_get', arguments: { path: { id: P } } }, 4);
  assert.ok(!project.isError, `projects_get failed: ${JSON.stringify(project).slice(0, 300)}`);
  assert.match(JSON.stringify(project), new RegExp(P));
  const missing = await mcp('tools/call', { name: 'projects_get', arguments: {} }, 5);
  assert.ok(missing.isError, 'a tool call missing required args must report isError');
});

test('users: list, detail and credential flags never leak secrets', async () => {
  const users = list(await get('/api/users'));
  const raw = JSON.stringify(users);
  assert.doesNotMatch(raw, /afp_[a-z0-9]{8,}/i);
  assert.doesNotMatch(raw, /"(gitToken|codeBuddyApiKey|taihuPat)":"[^"]+"/);
  const me = users.find(u => u.codeBuddyApiKeyConfigured) || users[0];
  // Per-user endpoints are write-only; reads go through the list.
  await api('GET', `/api/users/${me.id}`, undefined, { expect: 405 });
  await api('GET', `/api/users/${me.id}/credentials`, undefined, { expect: [404, 405] });
});

test('chat clarify and sync endpoints are reachable read-only', async () => {
  assert.ok(await get('/api/chat-clarify', { expect: [200, 400] }));
  const status = await get('/api/sync/status');
  assert.ok(status);
});
