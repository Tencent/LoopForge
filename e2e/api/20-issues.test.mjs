// Requirement lifecycle: create, list/filter/paginate, edit, comment,
// clarification, SOP binding, dispatch into a real run, close and reopen.
// Issues have no delete endpoint, so they stay closed in the e2e project.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, put, list, track, cleanup, ensureProject, setClarification, waitFor, waitRun } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';
import { createWorkflow, script, trackRun } from '../lib/fixtures.mjs';

const P = cfg.projectId;
let issue;
let workflowId;

before(async () => {
  await ensureProject();
  workflowId = await createWorkflow('issue-sop', [script('hello', 'echo "issue-run-ok $AGENTFLOW_RUN_ID"')]);
});
after(cleanup);

function closeLater(id) {
  track(`issue ${id}`, () => post(`/api/issues/${id}/close`, {}, { expect: [200, 404, 409] }));
}

test('create validates required fields and assigns the caller', async () => {
  await api('POST', '/api/issues', { projectId: P }, { expect: 400 });
  await api('POST', '/api/issues', { title: 'no project' }, { expect: 400 });
  issue = await post('/api/issues', {
    projectId: P, title: `E2E 需求 ${cfg.runTag}`, description: '自动化实测创建，会被关闭。\n\n验收：脚本输出 issue-run-ok。',
    priority: 'medium', kind: 'requirement', status: 'todo', workflowId,
  });
  closeLater(issue.id);
  assert.ok(issue.id && issue.number > 0);
  assert.equal(issue.projectId, P);
  assert.ok(issue.assigneeUserId > 0, 'creator should become the default assignee');
});

test('list supports project filter, search and pagination', async () => {
  const all = list(await get(`/api/issues?projectId=${P}`));
  assert.ok(all.some(i => i.id === issue.id));
  const found = list(await get(`/api/issues?projectId=${P}&q=${encodeURIComponent(cfg.runTag)}`));
  assert.ok(found.some(i => i.id === issue.id), 'search by title did not find the issue');
  const page = await get(`/api/issues?projectId=${P}&limit=1&offset=0`);
  assert.equal(page.limit, 1);
  assert.equal(page.items.length, Math.min(1, page.total));
  await api('GET', `/api/issues?limit=abc`, undefined, { expect: 400 });
  await api('GET', `/api/issues?limit=1&offset=-1`, undefined, { expect: 400 });
});

const detailOf = async id => get(`/api/issues/${id}`);

test('edit keeps identity and records an operation', async () => {
  const { issue: current } = await detailOf(issue.id);
  const saved = await put('/api/issues', { ...current, title: `${current.title}（已编辑）`, priority: 'high' });
  assert.equal(saved.id, issue.id);
  assert.equal(saved.number, issue.number);
  assert.equal(saved.priority, 'high');
  const { issue: again } = await detailOf(issue.id);
  assert.match(again.title, /已编辑/);
  assert.ok((again.operations || []).some(o => o.action === 'update'), `edit not audited: ${JSON.stringify(again.operations)}`);
});

test('comments are stored on the issue', async () => {
  await post(`/api/issues/${issue.id}/comments`, { body: 'e2e 评论' });
  const { comments } = await detailOf(issue.id);
  assert.ok((comments || []).some(c => c.body === 'e2e 评论'), 'comment missing from detail');
  await api('POST', `/api/issues/${issue.id}/comments`, { body: '' }, { expect: [400, 200] });
});

test('unsupported sub-resource methods answer 404, unknown issue 404', async () => {
  await api('GET', `/api/issues/${issue.id}/dispatch`, undefined, { expect: 404 });
  await api('GET', '/api/issues/issue-does-not-exist', undefined, { expect: 404 });
});

test('requirement view and SOP binding round-trip', async () => {
  const requirement = await get(`/api/issues/${issue.id}/requirement`);
  assert.ok(requirement);
  const sops = await get(`/api/issues/${issue.id}/sops`);
  assert.ok(sops);
  const saved = await put(`/api/issues/${issue.id}/sops`, { bindings: [{ workflowId, order: 1 }] });
  assert.equal(saved.workflowId, workflowId);
  const { bindings } = await get(`/api/issues/${issue.id}/sops`);
  assert.ok(bindings.some(b => b.workflowId === workflowId), `binding not stored: ${JSON.stringify(bindings)}`);
  await api('PUT', `/api/issues/${issue.id}/sops`, { bindings: [{ workflowId: 'e2e-no-such-sop', order: 1 }] }, { expect: 400 });
  await api('PUT', `/api/issues/${issue.id}/sops`, { playbookId: 'e2e-no-such-playbook' }, { expect: 400 });
});

test('clarification assessment answers with a structured verdict', async () => {
  const verdict = await post(`/api/issues/${issue.id}/clarify`, { answers: {} }, { expect: [200, 409], timeoutMs: 120_000 });
  assert.ok(verdict && typeof verdict === 'object');
});

test('clarification gate blocks dispatch of an assessed, under-scored requirement', async () => {
  // A requirement is gated once it has been assessed under an enabled policy.
  await setClarification(true);
  try {
    const vague = await post('/api/issues', { projectId: P, kind: 'requirement', title: `E2E 澄清门禁 ${cfg.runTag}`, description: '做个东西', workflowId });
    const verdict = await post(`/api/issues/${vague.id}/clarify`, { answers: {} });
    assert.equal(verdict.passed ?? verdict.clarification?.passed ?? false, false, `a two-word requirement must not pass: ${JSON.stringify(verdict).slice(0, 300)}`);
    const { issue: assessed } = await get(`/api/issues/${vague.id}`);
    assert.ok(assessed.clarification?.gated, `assessed requirement is not gated: ${JSON.stringify(assessed.clarification)}`);
    closeLater(vague.id);
    const res = await api('POST', `/api/issues/${vague.id}/dispatch`, { workflowId }, { expect: 409 });
    assert.match(JSON.stringify(res), /澄清/);
    assert.equal(list(await get(`/api/issues/${vague.id}/runs`)).length, 0, 'a refused dispatch must not start a run');
  } finally {
    await setClarification(false);
  }
});

test('dispatch starts a run linked to the issue and it succeeds', async () => {
  const task = await post(`/api/issues/${issue.id}/dispatch`, { workflowId });
  assert.ok(task);
  const runs = await waitFor('issue run to appear', async () => {
    const items = list(await get(`/api/issues/${issue.id}/runs`));
    return items.length ? items : null;
  }, { timeoutMs: 60_000 });
  const runId = runs[0].id || runs[0].runId;
  trackRun(runId);
  const run = await waitRun(runId, r => r.status === 'succeeded', { timeoutMs: 240_000, label: 'succeeded' });
  const logs = await api('GET', `/api/runs/${run.id}/logs.txt`, undefined, { raw: true });
  assert.match(logs.text, /issue-run-ok/);
  const operations = (await detailOf(issue.id)).issue.operations || [];
  assert.ok(operations.some(o => o.action === 'dispatch'), `dispatch not audited: ${JSON.stringify(operations)}`);
});

test('close then reopen', async () => {
  const closeable=await post('/api/issues',{projectId:P,title:'E2E close and reopen',kind:'issue',status:'todo'});
  closeLater(closeable.id);
  const closed = await post(`/api/issues/${closeable.id}/close`, {});
  assert.equal(closed.status, 'closed');
  const again = await post(`/api/issues/${closeable.id}/close`, {});
  assert.equal(again.status, 'closed', 'closing twice must be idempotent');
  const reopened = await post(`/api/issues/${closeable.id}/reopen`, {}, { expect: [200, 409] });
  if (reopened.status) assert.notEqual(reopened.status, 'closed');
});

test('TAPD resync is refused for a local issue', async () => {
  await api('POST', `/api/issues/${issue.id}/resync-tapd`, {}, { expect: 400 });
});

test('deleting a separate draft issue returns 204 and removes it',async()=>{
 const draft=await post('/api/issues',{projectId:P,title:'E2E deletable draft',kind:'issue',status:'todo'});
 await api('DELETE',`/api/issues/${draft.id}`,undefined,{expect:204});
 await api('GET',`/api/issues/${draft.id}`,undefined,{expect:404});
});
