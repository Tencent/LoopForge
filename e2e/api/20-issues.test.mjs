// Requirement lifecycle: create, list/filter/paginate, edit, comment,
// clarification, SOP binding, dispatch into a real run, close and reopen, and
// deleting a local requirement and its settled execution history.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, put, list, track, cleanup, ensureProject, waitFor, waitRun } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';
import { createWorkflow, script, approval, trackRun } from '../lib/fixtures.mjs';

const P = cfg.projectId;
let issue;
let workflowId;

before(async () => {
  await ensureProject();
  workflowId = await createWorkflow('issue-sop', [script('hello', 'echo "issue-run-ok $AGENTFLOW_RUN_ID"')]);
});
after(cleanup);

// Clean up the test's own requirements, including their execution history.
function removeLater(id) {
  track(`issue ${id}`, async () => {
    const res = await api('DELETE', `/api/issues/${id}?force=true`, undefined, { expect: [204, 400, 404, 409], raw: true });
    if (res.status === 400 || res.status === 409) await post(`/api/issues/${id}/close`, {}, { expect: [200, 400, 404, 409] });
  });
}

test('create validates required fields and assigns the caller', async () => {
  await api('POST', '/api/issues', { projectId: P }, { expect: 400 });
  await api('POST', '/api/issues', { title: 'no project' }, { expect: 400 });
  issue = await post('/api/issues', {
    projectId: P, title: `E2E 需求 ${cfg.runTag}`, description: '自动化实测创建，会被删除。\n\n验收：脚本输出 issue-run-ok。',
    priority: 'medium', kind: 'requirement', status: 'todo', workflowId,
  });
  removeLater(issue.id);
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
  await api('PATCH', `/api/issues/${issue.id}`, {}, { expect: 404 });
  await api('GET', `/api/issues/${issue.id}/dispatch`, undefined, { expect: 404 });
  await api('GET', '/api/issues/issue-does-not-exist', undefined, { expect: 404 });
  await api('DELETE', '/api/issues/issue-does-not-exist', undefined, { expect: 404 });
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

test('clarification is optional: no score endpoint, no auto-start on create', async () => {
  await api('POST', `/api/issues/${issue.id}/clarify`, { answers: {} }, { expect: 404 });
  const vague = await post('/api/issues', { projectId: P, kind: 'requirement', title: `E2E 澄清可选 ${cfg.runTag}`, description: '做个东西', workflowId });
  removeLater(vague.id);
  const { issue: created } = await get(`/api/issues/${vague.id}`);
  const c = created.clarification || {};
  assert.ok(!c.status && !c.gated && c.score === undefined, `a new requirement must not be scored or auto-clarified: ${JSON.stringify(c)}`);
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
  removeLater(closeable.id);
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
 assert.equal(draft.deletionAllowed, true);
 assert.equal(draft.deletionBlock ?? null, null);
 await api('DELETE',`/api/issues/${draft.id}`,undefined,{expect:204});
 await api('GET',`/api/issues/${draft.id}`,undefined,{expect:404});
});

// waitingIssue dispatches a fresh requirement onto a SOP that stops at a human gate.
async function waitingIssue(label) {
  const gated = await createWorkflow(label, [script('prep', 'echo prep'), approval('gate', 'prep')]);
  const created = await post('/api/issues', { projectId: P, title: `E2E 删除需求 ${cfg.runTag}`, description: 'e2e 删除用例', workflowId: gated });
  removeLater(created.id);
  await post(`/api/issues/${created.id}/dispatch`, { workflowId: gated });
  const [first] = await waitFor('issue run to appear', async () => {
    const items = list(await get(`/api/issues/${created.id}/runs`));
    return items.length ? items : null;
  }, { timeoutMs: 60_000 });
  const runId = first.id || first.runId;
  trackRun(runId);
  await waitRun(runId, r => r.nodes?.gate?.status === 'waiting_approval', { label: 'gate waiting' });
  return { id: created.id, runId };
}

test('delete: execution history requires force; a waiting run blocks deletion', {
  todo: 'POST /api/runs/:id/cancel tears the sandbox down inside the request (docker rm -f of the agentflow-run-*-scripts sidecar, up to dockerTeardownTimeout=60s); under nested Docker that rm hangs, so the call outlives the 60s client timeout',
}, async () => {
  // Listing settles task status before the detail affordance is shown.
  await get(`/api/issues?projectId=${P}`);
  const deliveredDetail = await detailOf(issue.id);
  assert.equal(deliveredDetail.issue.deletionAllowed, true);
  assert.equal(deliveredDetail.issue.deletionRequiresForce, true);
  assert.equal(deliveredDetail.issue.deletionBlock ?? null, null);
  const listed = list(await get(`/api/issues?projectId=${P}`)).find(i => i.id === issue.id);
  assert.equal(listed?.deletionRequiresForce, true);
  assert.equal(listed?.deletionAllowed, true);
  const delivered = await api('DELETE', `/api/issues/${issue.id}`, undefined, { expect: 409 });
  assert.equal(delivered.deletionRequiresForce, true);

  const doomed = await waitingIssue('issue-delete');
  await post(`/api/issues/${doomed.id}/comments`, { body: 'e2e 删除前的评论' });
  const waiting = await detailOf(doomed.id);
  assert.equal(waiting.issue.deletionAllowed, false);
  assert.equal(waiting.issue.deletionBlock?.code, 'active');
  assert.equal(waiting.issue.deletionBlock?.runId, doomed.runId);
  assert.match(waiting.issue.deletionBlock.reason, /未结束的运行/);
  const busy = await api('DELETE', `/api/issues/${doomed.id}?force=true`, undefined, { expect: 409 });
  assert.equal(busy.error, waiting.issue.deletionBlock.reason);
  assert.equal(busy.deletionBlock?.runId, doomed.runId);
  assert.notEqual(busy.deletionRequiresForce, true);
  assert.equal((await get(`/api/issues/${doomed.id}`)).issue.id, doomed.id);

  await post(`/api/runs/${doomed.runId}/cancel`, {});
  await waitRun(doomed.runId, r => r.status === 'cancelled', { label: 'cancelled' });
  // Task status follows the run lazily: the issue list syncs it, and the page
  // always lists before offering 删除 (see the todo below for the bare API).
  await waitFor('requirement deletable', async () => {
    await get(`/api/issues?projectId=${P}`);
    return (await api('DELETE', `/api/issues/${doomed.id}?force=true`, undefined, { expect: [204, 409], raw: true })).status === 204 || null;
  }, { timeoutMs: 30_000 });
  await api('GET', `/api/issues/${doomed.id}`, undefined, { expect: 404 });
  await api('DELETE', `/api/issues/${doomed.id}`, undefined, { expect: 404 });
  assert.ok(!list(await get(`/api/issues?projectId=${P}`)).some(i => i.id === doomed.id), 'a deleted requirement is still listed');
  await api('GET', `/api/runs/${doomed.runId}`, undefined, { expect: 404 });
});

// Delete settles the dispatch task from its run first (like close), so it
// does not need a list call to sync tasks.
test('delete right after the run is cancelled, without listing issues first', async () => {
  const doomed = await waitingIssue('issue-delete-now');
  await post(`/api/runs/${doomed.runId}/cancel`, {});
  await waitRun(doomed.runId, r => r.status === 'cancelled', { label: 'cancelled' });
  await api('DELETE', `/api/issues/${doomed.id}?force=true`, undefined, { expect: 204 });
});
