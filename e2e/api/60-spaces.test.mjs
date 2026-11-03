// Requirement spaces (the conversation workbench): space CRUD, timeline and
// threads, the one-requirement-per-space rule, artifacts with optimistic
// versioning and their sandboxed raw views, members, and read-only panels.
// Spaces are rooms and rooms have no delete endpoint, so the suite reuses
// fixed spaces across runs instead of leaking one per run.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, put, del, list, track, cleanup, ensureProject, tokenOwner } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';

const P = cfg.projectId;
const MAIN = 'E2E 自动化会话';
const REQ = 'E2E 自动化需求会话';
const patch = (path, body, opts) => api('PATCH', path, body, opts);

async function fixedSpace(title) {
  const found = list(await get(`/api/spaces?projectId=${P}`)).find(s => s.title === title);
  return found ? get(`/api/spaces/${found.id}`) : post('/api/spaces', { projectId: P, title, summary: 'e2e 自动化用例复用的会话（勿删）' });
}

let space;
before(async () => {
  await ensureProject();
  space = await fixedSpace(MAIN);
});
after(cleanup);

test('create validates project, title, summary and invitees', async () => {
  await api('GET', '/api/spaces', undefined, { expect: 400 });
  await api('GET', '/api/spaces?projectId=e2e-missing-project', undefined, { expect: 404 });
  await api('POST', '/api/spaces', { projectId: P }, { expect: 400 });
  await api('POST', '/api/spaces', { projectId: P, title: '   ' }, { expect: 400 });
  await api('POST', '/api/spaces', { projectId: P, title: 'x'.repeat(81) }, { expect: 400 });
  await api('POST', '/api/spaces', { projectId: P, title: 't', summary: 'x'.repeat(4001) }, { expect: 400 });
  await api('POST', '/api/spaces', { projectId: 'e2e-missing-project', title: 't' }, { expect: 400 });
  await api('POST', '/api/spaces', { projectId: P, title: 't', members: [{ userId: 987654321 }] }, { expect: 400 });
  await api('POST', '/api/spaces', { projectId: P, title: 't', issueId: 'iss-does-not-exist' }, { expect: 400 });
  await api('GET', '/api/spaces/room-does-not-exist', undefined, { expect: 404 });
});

test('the creator is a member and the space is listed with its title', async () => {
  const me = await tokenOwner();
  const view = await get(`/api/spaces/${space.id}`);
  assert.equal(view.title, MAIN);
  assert.equal(view.projectId, P);
  assert.ok(Array.isArray(view.members) && Array.isArray(view.artifacts) && Array.isArray(view.threads) && Array.isArray(view.lanes));
  // Spaces created before this run may predate the creator-member rule.
  if (view.createdBy === me.id) assert.ok(view.members.some(m => m.userId === me.id), 'creator must be a space member');
  const listed = list(await get(`/api/spaces?projectId=${P}`)).find(s => s.id === space.id);
  assert.equal(listed?.title, MAIN);
});

test('PATCH edits title, summary and pin, and validates', async () => {
  const summary = `e2e summary ${cfg.runTag}`;
  let view = await patch(`/api/spaces/${space.id}`, { summary, pinned: true });
  assert.equal(view.summary, summary);
  assert.equal(view.pinned, true);
  view = await patch(`/api/spaces/${space.id}`, { pinned: false, title: MAIN });
  assert.ok(!view.pinned);
  assert.equal(view.title, MAIN);
  await patch(`/api/spaces/${space.id}`, { title: ' ' }, { expect: 400 });
  await patch(`/api/spaces/${space.id}`, { title: 'x'.repeat(81) }, { expect: 400 });
  await patch(`/api/spaces/${space.id}`, { summary: 'x'.repeat(4001) }, { expect: 400 });
  assert.equal((await get(`/api/spaces/${space.id}`)).title, MAIN, 'a rejected edit must not be stored');
});

test('messages: validation, main timeline and threads stay separate', async () => {
  const base = `/api/spaces/${space.id}`;
  await api('POST', `${base}/messages`, { body: '  ' }, { expect: 400 });
  await api('POST', `${base}/messages`, { body: 'x'.repeat(4001) }, { expect: 400 });
  await api('POST', `${base}/messages`, { body: 'hi', threadId: 'thr-missing' }, { expect: 404 });
  await api('POST', `${base}/messages`, { body: 'hi', attachments: [{ id: 'att-missing' }] }, { expect: 400 });
  await api('GET', `${base}/messages?threadId=thr-missing`, undefined, { expect: 404 });

  const main = await post(`${base}/messages`, { body: `e2e 主线消息 ${cfg.runTag}` });
  assert.ok(main.message?.id);
  assert.equal(main.reply, undefined, 'a message not addressed to AI gets no reply');

  await api('POST', `${base}/threads`, { title: '' }, { expect: 400 });
  await api('POST', `${base}/threads`, { title: 'x'.repeat(81) }, { expect: 400 });
  await api('POST', `${base}/threads`, { title: 'run', runId: 'run-not-from-this-space' }, { expect: 400 });
  const thread = await post(`${base}/threads`, { title: `e2e 子线程 ${cfg.runTag}` });
  assert.ok(thread.id);
  const inThread = await post(`${base}/messages`, { body: `e2e 子线程消息 ${cfg.runTag}`, threadId: thread.id });
  assert.equal(inThread.message.threadId, thread.id);

  const timeline = list(await get(`${base}/messages`));
  assert.ok(timeline.some(m => m.id === main.message.id), 'main message missing from the timeline');
  assert.ok(!timeline.some(m => m.id === inThread.message.id), 'thread message leaked into the main timeline');
  const threadMessages = list(await get(`${base}/messages?threadId=${thread.id}`));
  assert.deepEqual(threadMessages.map(m => m.id), [inThread.message.id]);
  const threads = list(await get(`${base}/threads`));
  assert.equal(threads.find(t => t.id === thread.id)?.count, 1);

  // A thread opened from a main-line message is idempotent and titled from it.
  const fromMsg = await post(`${base}/threads`, { messageId: main.message.id });
  assert.equal(fromMsg.messageId, main.message.id);
  assert.match(fromMsg.title, /e2e 主线消息/);
  assert.equal((await post(`${base}/threads`, { messageId: main.message.id })).id, fromMsg.id);
  await api('POST', `${base}/threads`, { messageId: inThread.message.id }, { expect: 400 });
});

test('requirement: one per space, linked issue, and reopening by issue returns the same space', async () => {
  const req = await fixedSpace(REQ);
  const base = `/api/spaces/${req.id}/requirement`;
  if (!req.requirement) {
    await api('POST', base, { title: '' }, { expect: 400 });
    await api('POST', base, { title: 'x'.repeat(201) }, { expect: 400 });
    await api('POST', base, { title: 't', description: 'x'.repeat(8001) }, { expect: 400 });
    await api('POST', base, { title: 't', priority: 'p0' }, { expect: 400 });
    const view = await post(base, { title: `E2E 会话需求 ${cfg.runTag}`, description: '由 e2e 会话创建', priority: 'high' });
    assert.ok(view.requirement?.id && view.requirement.number > 0);
    assert.equal(view.requirement.priority, 'high');
    const timeline = list(await get(`/api/spaces/${req.id}/messages`));
    assert.ok(timeline.some(m => m.issueId === view.requirement.id), 'creating a requirement must note it on the timeline');
    req.requirement = view.requirement;
  }
  const { issue } = await get(`/api/issues/${req.requirement.id}`);
  assert.equal(issue.projectId, P);
  assert.equal(issue.title, req.requirement.title);
  track(`issue ${issue.id}`, () => post(`/api/issues/${issue.id}/close`, {}, { expect: [200, 404, 409] }));
  await api('POST', base, { title: 'second' }, { expect: 409 });
  const reopened = await api('POST', '/api/spaces', { projectId: P, title: 'ignored', issueId: issue.id }, { expect: 200 });
  assert.equal(reopened.id, req.id, 'opening a space for a linked issue must return the existing space');
});

test('artifacts: validation, versioning, revisions and sandboxed raw views', async () => {
  const base = `/api/spaces/${space.id}/artifacts`;
  await api('POST', base, { kind: 'nope', title: 't' }, { expect: 400 });
  await api('POST', base, { kind: 'doc', title: '' }, { expect: 400 });
  await api('POST', base, { kind: 'doc', title: 'x'.repeat(121) }, { expect: 400 });
  await api('POST', base, { kind: 'doc', title: 't', format: 'exe' }, { expect: 400 });
  await api('POST', base, { kind: 'doc', title: 't', url: 'javascript:alert(1)' }, { expect: 400 });
  await api('POST', base, { kind: 'doc', title: 't', url: 'file:///etc/passwd' }, { expect: 400 });

  const v1 = await api('POST', base, { kind: 'doc', title: `E2E 文档 ${cfg.runTag}`, format: 'markdown', body: '# v1\n<script>alert(1)</script>', summary: 's' }, { expect: 201 });
  track(`artifact ${v1.id}`, () => del(`${base}/${v1.id}`));
  assert.equal(v1.version, 1);
  const listed = list(await get(base)).find(a => a.id === v1.id);
  assert.ok(listed, 'artifact missing from list');
  assert.ok(!listed.body, 'the list must not carry artifact bodies');
  assert.equal((await get(`${base}/${v1.id}`)).body, '# v1\n<script>alert(1)</script>');
  const announced = list(await get(`/api/spaces/${space.id}/messages`));
  assert.ok(announced.some(m => m.artifactId === v1.id && m.artifactVersion === 1), 'publishing must announce on the timeline');

  const v2 = await put(`${base}/${v1.id}`, { title: v1.title, format: 'markdown', body: '# v2', baseVersion: 1 });
  assert.equal(v2.version, 2);
  assert.equal(v2.kind, 'doc', 'PUT without kind keeps the kind');
  await api('PUT', `${base}/${v1.id}`, { title: v1.title, format: 'markdown', body: '# stale', baseVersion: 1 }, { expect: 409 });
  assert.equal((await get(`${base}/${v1.id}`)).body, '# v2', 'a stale write must not overwrite');

  const revisions = list(await get(`${base}/${v1.id}/revisions`));
  assert.ok(revisions.length >= 2, `expected 2 revisions, got ${revisions.length}`);
  assert.match((await get(`${base}/${v1.id}/revisions/1`)).body, /# v1/);
  await api('GET', `${base}/${v1.id}/revisions/abc`, undefined, { expect: 400 });
  await api('GET', `${base}/${v1.id}/revisions/99`, undefined, { expect: 404 });

  // Raw views are served with headers that keep user content from running
  // with the platform's origin.
  const text = await api('GET', `${base}/${v1.id}/raw`, undefined, { raw: true });
  assert.match(text.headers.get('content-type'), /text\/plain/);
  assert.equal(text.headers.get('x-content-type-options'), 'nosniff');
  assert.match(text.headers.get('content-security-policy') || '', /default-src 'none'/);

  const html = await api('POST', base, { kind: 'prototype', title: `E2E 原型 ${cfg.runTag}`, format: 'html', body: '<h1>proto</h1>' }, { expect: 201 });
  track(`artifact ${html.id}`, () => del(`${base}/${html.id}`));
  const page = await api('GET', `${base}/${html.id}/raw`, undefined, { raw: true });
  assert.match(page.headers.get('content-type'), /text\/html/);
  assert.match(page.headers.get('content-security-policy') || '', /sandbox/);
  assert.equal(page.text, '<h1>proto</h1>');

  await del(`${base}/${v1.id}`, { expect: 200 });
  await api('GET', `${base}/${v1.id}`, undefined, { expect: 404 });
  await api('PUT', `${base}/${v1.id}`, { title: 't' }, { expect: 404 });
  await api('GET', `${base}/art-missing/raw`, undefined, { expect: 404 });
});

test('members: roster view and invite validation', async () => {
  const base = `/api/spaces/${space.id}/members`;
  const view = await get(base);
  assert.ok(Array.isArray(view.members) && Array.isArray(view.roster));
  await api('POST', base, { userId: 987654321 }, { expect: 400 });
  await api('DELETE', base, undefined, { expect: 400 });
  await api('DELETE', `${base}?userId=abc`, undefined, { expect: 400 });
});

test('read-only panels: catalog, lanes and usage answer for the space', async () => {
  const base = `/api/spaces/${space.id}`;
  assert.ok(await get(`${base}/catalog`));
  assert.ok(Array.isArray(list(await get(`${base}/lanes`))));
  await api('GET', `${base}/lanes/run-does-not-exist`, undefined, { expect: [404] });
  assert.ok(await get(`${base}/usage`));
  await api('GET', `${base}/attachments/att-missing`, undefined, { expect: 404 });
});
