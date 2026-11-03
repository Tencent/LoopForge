// AI assistant conversations without a model: the client-saved transcript,
// rename, archive, linking a requirement, branching and editing a user turn
// with optimistic timestamps, delete, and how project conversations are
// shared — readable by anyone signed in, writable by members, deletable and
// archivable only by their creator.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, put, post, del, list, track, cleanup, ensureProject, tokenOwner, tokenBSkip, asB, withProjectMember } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';

const P = cfg.projectId;
const S = '/api/assistant/sessions';
let me;
let session;
before(async () => {
  await ensureProject();
  me = await tokenOwner();
});
after(cleanup);

const newId = tag => `af-e2e-${tag}-${cfg.runTag}-${Math.random().toString(36).slice(2, 7)}`;
async function saveSession(id, body, opts) {
  const saved = await put(`${S}/${id}`, body, opts);
  track(`assistant session ${id}`, () => del(`${S}/${id}`, { expect: [200, 404] }));
  return saved;
}

test('save a project conversation; user turns are stamped with the caller', async () => {
  const id = newId('main');
  session = await saveSession(id, {
    title: 'E2E 会话', projectId: P,
    messages: [
      { role: 'user', content: '第一问', authorId: 1, authorName: 'spoofed' },
      { role: 'assistant', content: '第一答' },
      { role: 'user', content: '第二问' },
    ],
  });
  assert.equal(session.id, id);
  assert.equal(session.projectId, P);
  assert.equal(session.messages.length, 3);
  assert.equal(session.messages[0].authorId, me.id, 'the author of a user turn cannot be forged');
  assert.equal(session.messages[0].authorName, me.loginName);
  const listed = list(await get(S)).find(s => s.id === id);
  assert.equal(listed?.title, 'E2E 会话');
  assert.equal(listed.generationStatus, 'idle');
  await api('GET', `${S}/af-e2e-does-not-exist`, undefined, { expect: 404 });
});

test('rename marks the title edited; rebinding to another project is refused', async () => {
  const renamed = await put(`${S}/${session.id}`, { ...session, title: 'E2E 会话 改名' });
  assert.equal(renamed.title, 'E2E 会话 改名');
  assert.equal(renamed.titleEdited, true);
  await api('PUT', `${S}/${session.id}`, { ...renamed, projectId: 'e2e-missing-project' }, { expect: 403 });
  await api('PUT', `${S}/${session.id}`, { ...renamed, effort: 'ludicrous' }, { expect: 400 });
  session = await get(`${S}/${session.id}`);
  assert.equal(session.projectId, P);
});

test('link a requirement with optimistic concurrency; foreign issues are refused', async () => {
  const issue = await post('/api/issues', { projectId: P, title: `E2E 会话关联 ${cfg.runTag}`, description: 'e2e' });
  const issueId = issue.id || issue.issue?.id;
  track(`issue ${issueId}`, () => del(`/api/issues/${issueId}`, { expect: [204, 404] }));
  await api('PUT', `${S}/${session.id}/issue`, { issueId: 'iss-does-not-exist', updatedAt: session.updatedAt }, { expect: 403 });
  await api('PUT', `${S}/${session.id}/issue`, { issueId, updatedAt: '2000-01-01T00:00:00Z' }, { expect: 409 });
  const linked = await put(`${S}/${session.id}/issue`, { issueId, updatedAt: session.updatedAt });
  assert.equal(linked.issueId, issueId);
  const conversations = list(await get(`/api/issues/${issueId}/conversations`));
  assert.ok(conversations.some(c => c.id === session.id), 'the requirement must list its linked conversation');
  session = await get(`${S}/${session.id}`);
  const unlinked = await put(`${S}/${session.id}/issue`, { issueId: '', updatedAt: session.updatedAt });
  assert.ok(!unlinked.issueId);
  session = await get(`${S}/${session.id}`);
});

test('branch from a user turn keeps the prefix and leaves the source intact', async () => {
  const fork = body => api('POST', `${S}/${session.id}/fork`, body, { expect: [201] });
  await api('POST', `${S}/${session.id}/fork`, { messageIndex: 1, content: 'x', updatedAt: session.updatedAt }, { expect: 400 });
  await api('POST', `${S}/${session.id}/fork`, { messageIndex: 9, content: 'x', updatedAt: session.updatedAt }, { expect: 400 });
  await api('POST', `${S}/${session.id}/fork`, { messageIndex: 2, content: '  ', updatedAt: session.updatedAt }, { expect: 400 });
  await api('POST', `${S}/${session.id}/fork`, { messageIndex: 2, content: 'x', updatedAt: '2000-01-01T00:00:00Z' }, { expect: 409 });
  const branch = await fork({ messageIndex: 2, content: '第二问（分支）', updatedAt: session.updatedAt });
  track(`assistant session ${branch.id}`, () => del(`${S}/${branch.id}`, { expect: [200, 404] }));
  assert.equal(branch.parentSessionId, session.id);
  assert.equal(branch.rootSessionId, session.id);
  assert.equal(branch.forkMessageIndex, 2);
  assert.deepEqual(branch.messages.map(m => m.content), ['第一问', '第一答', '第二问（分支）']);
  assert.match(branch.title, /^分支 · /);
  const source = await get(`${S}/${session.id}`);
  assert.equal(source.messages[2].content, '第二问', 'branching must not rewrite the source');
});

test('editing a user turn truncates the transcript from there', async () => {
  await api('POST', `${S}/${session.id}/edit`, { messageIndex: 1, content: 'x', updatedAt: session.updatedAt }, { expect: 400 });
  await api('POST', `${S}/${session.id}/edit`, { messageIndex: 0, content: 'x', updatedAt: '2000-01-01T00:00:00Z' }, { expect: 409 });
  const edited = await post(`${S}/${session.id}/edit`, { messageIndex: 0, content: '第一问（改）', updatedAt: session.updatedAt });
  assert.deepEqual(edited.messages.map(m => m.content), ['第一问（改）']);
  session = await get(`${S}/${session.id}`);
});

test('archive flags the conversation and can be undone', async () => {
  await api('POST', `${S}/${session.id}/archive`, {}, { expect: 400 });
  await post(`${S}/${session.id}/archive`, { archived: true });
  assert.equal(list(await get(S)).find(s => s.id === session.id)?.archived, true);
  await post(`${S}/${session.id}/archive`, { archived: false });
  assert.ok(!list(await get(S)).find(s => s.id === session.id)?.archived);
  await api('POST', `${S}/af-e2e-does-not-exist/archive`, { archived: true }, { expect: 404 });
});

test('generation endpoints answer for an idle conversation', async () => {
  await api('GET', `${S}/${session.id}/generation`, undefined, { expect: 404 });
  await api('POST', `${S}/${session.id}/generation/cancel`, {}, { expect: [404, 409] });
});

test('project conversations: non-members read only, members share full access', { skip: tokenBSkip }, async () => {
  const userB = await tokenOwner(cfg.tokenB);
  await withProjectMember(userB.id, null);
  const B = (method, path, body, expect) => api(method, path, body, asB({ expect }));

  const view = await B('GET', `${S}/${session.id}`, undefined, 200);
  assert.equal(view.readOnly, true, 'a non-member sees the conversation read-only');
  await B('PUT', `${S}/${session.id}`, { ...session, title: 'hijacked' }, 403);
  await B('POST', `${S}/${session.id}/fork`, { messageIndex: 0, content: 'x', updatedAt: session.updatedAt }, 403);
  await B('DELETE', `${S}/${session.id}`, undefined, 403);
  assert.ok(!list(await B('GET', S, undefined, 200)).some(s => s.id === session.id), 'non-members must not get it listed');

  const restore = await withProjectMember(userB.id, 'member');
  const asMember = await B('GET', `${S}/${session.id}`, undefined, 200);
  assert.ok(!asMember.readOnly);
  assert.ok(list(await B('GET', S, undefined, 200)).some(s => s.id === session.id), 'members see project conversations');
  const edited = await B('POST', `${S}/${session.id}/edit`, { messageIndex: 0, content: 'B 改写', updatedAt: asMember.updatedAt }, 200);
  assert.equal(edited.messages[0].content, 'B 改写');
  assert.equal(edited.messages[0].authorId, userB.id, 'a member may rewrite any turn and becomes its author');
  await B('POST', `${S}/${session.id}/archive`, { archived: true }, 200);
  await B('POST', `${S}/${session.id}/archive`, { archived: false }, 200);
  const current = await B('GET', `${S}/${session.id}`, undefined, 200);
  const branch = await B('POST', `${S}/${session.id}/fork`, { messageIndex: 0, content: 'B 的分支', updatedAt: current.updatedAt }, 201);
  assert.equal(branch.messages[0].authorId, userB.id);
  await B('DELETE', `${S}/${branch.id}`, undefined, 200);
  await restore();
  assert.equal((await get(`${S}/${session.id}`)).messages[0].content, 'B 改写');
});

test('delete removes the conversation', async () => {
  await del(`${S}/${session.id}`);
  await api('GET', `${S}/${session.id}`, undefined, { expect: 404 });
  assert.ok(!list(await get(S)).some(s => s.id === session.id));
});
