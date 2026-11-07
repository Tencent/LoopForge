// Authentication and authorization boundaries.
//  - Browser session (loopback acceptance login, local.sh only): personal API
//    token lifecycle, which Bearer tokens can never manage themselves.
//  - A second, non-admin user (AGENTFLOW_E2E_TOKEN_B) walks the e2e project as
//    non-member, viewer, member and maintainer; each role must see exactly its
//    own permissions. The project roster is restored afterwards.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, put, del, list, cleanup, ensureProject, tokenOwner, browserSession, tokenBSkip, asB, withProjectMember } from '../lib/api.mjs';
import { cfg, uid } from '../lib/env.mjs';

const P = cfg.projectId;
let me;
before(async () => {
  await ensureProject();
  me = await tokenOwner();
});
after(cleanup);

test('acceptance login is loopback + same-origin only', async t => {
  const probe = await get('/api/auth/local', { auth: false });
  if (!probe.enabled) return t.skip('acceptance login disabled (run through local.sh)');
  await api('POST', '/api/auth/local', {}, { auth: false, expect: 403 });
  await api('POST', '/api/auth/local', {}, { auth: false, headers: { Origin: 'https://evil.example' }, expect: 403 });
  // A forwarded request is not a direct loopback caller.
  await api('POST', '/api/auth/local', {}, { auth: false, headers: { Origin: cfg.base, 'X-Forwarded-For': '203.0.113.9' }, expect: 403 });
});

test('personal API tokens: create, use, list without secret, revoke', async t => {
  const cookie = await browserSession();
  if (!cookie) return t.skip('acceptance login disabled (run through local.sh)');
  const s = opts => ({ cookie, ...opts });
  const current = await get('/api/auth/current', s());
  assert.equal(current.user?.loginName, me.loginName, 'the session must belong to the token owner');

  await api('POST', '/api/auth/tokens', { name: '' }, s({ expect: 400 }));
  await api('POST', '/api/auth/tokens', { name: 'x'.repeat(129) }, s({ expect: 400 }));
  await api('POST', '/api/auth/tokens', { name: 'e2e', expiresInDays: 366 }, s({ expect: 400 }));
  await api('POST', '/api/auth/tokens', { name: 'e2e', expiresInDays: -1 }, s({ expect: 400 }));

  const created = await api('POST', '/api/auth/tokens', { name: uid('token'), expiresInDays: 1 }, s({ expect: 201 }));
  const { token, metadata } = created;
  assert.match(token, /^afp_/);
  let revoked = false;
  try {
    // The fresh token authenticates as the same user.
    const probe = await post('/api/knowledge/submissions', { projectId: P, title: 'e2e-auto token probe', content: 'noop' }, { token });
    await del(`/api/knowledge/submissions/${probe.id}`, { token });
    assert.equal(probe.createdBy, me.loginName);
    // ...but cannot manage tokens itself.
    await api('GET', '/api/auth/tokens', undefined, { token, expect: 401 });
    await api('DELETE', `/api/auth/tokens/${metadata.id}`, undefined, { token, expect: 401 });

    const listed = await api('GET', '/api/auth/tokens', undefined, s({ raw: true }));
    assert.ok(list(listed.data).some(x => x.id === metadata.id), 'new token missing from the list');
    assert.ok(!listed.text.includes(token), 'the token secret must never be listed');

    await api('DELETE', `/api/auth/tokens/${metadata.id}`, undefined, s({ expect: 200 }));
    revoked = true;
    await api('GET', '/api/projects', undefined, { token, expect: 401 });
    await api('DELETE', `/api/auth/tokens/${metadata.id}`, undefined, s({ expect: 404 }));
  } finally {
    if (!revoked) await api('DELETE', `/api/auth/tokens/${metadata.id}`, undefined, s({ expect: [200, 404] }));
  }
});

test('logout clears the session cookie', async t => {
  const cookie = await browserSession();
  if (!cookie) return t.skip('acceptance login disabled (run through local.sh)');
  const res = await api('POST', '/api/auth/logout', {}, { cookie, raw: true });
  const cleared = res.headers.getSetCookie().join('\n');
  assert.match(cleared, /Max-Age=0|Expires=Thu, 01 Jan 1970/i, `logout did not expire the cookie: ${cleared}`);
  assert.ok(res.data.user?.isGuest ?? true);
});

let userB;
const B = (method, path, body, expect) => api(method, path, body, asB({ expect }));

test('user B as a non-member: project data and admin surfaces are refused', { skip: tokenBSkip }, async () => {
  userB = await tokenOwner(cfg.tokenB);
  assert.ok(!userB.isPlatformAdmin, 'AGENTFLOW_E2E_TOKEN_B must belong to a non-admin user');
  assert.notEqual(userB.id, me.id);
  await withProjectMember(userB.id, null);
  const project = await get(`/api/projects/${P}`);

  await B('PUT', '/api/projects', { ...project, name: 'hijacked' }, 403);
  await B('POST', '/api/projects', { id: uid('proj'), key: 'X', name: 'x', ownerId: me.id }, 403);
  await B('GET', '/api/platform/admins', undefined, 403);
  await B('GET', `/api/spaces?projectId=${P}`, undefined, 403);
  await B('POST', '/api/spaces', { projectId: P, title: 'x' }, 403);
  await B('GET', `/api/notification-bots?projectId=${P}`, undefined, 403);
  await B('GET', `/api/insights/v1/sources?projectId=${P}`, undefined, 403);
  await B('POST', '/api/insights/v1/sources', { projectId: P, name: 'x' }, 403);
  await B('PUT', '/api/business-domains', { id: uid('bd'), name: uid('bd'), active: true }, 403);
  await B('GET', `/api/projects/${P}/business-domain/history`, undefined, 403);
  await B('PUT', `/api/projects/${P}/business-domain`, { businessDomainId: '' }, 403);
  const form = new FormData();
  form.set('projectId', P);
  form.set('file', new Blob(['x']), 'x.txt');
  await B('POST', '/api/assistant/attachments', form, 403);
  assert.equal((await get(`/api/projects/${P}`)).name, project.name, 'a refused write must not change the project');
});

test('user B as viewer reads the project space but cannot speak or manage', { skip: tokenBSkip }, async () => {
  userB ||= await tokenOwner(cfg.tokenB);
  const restore = await withProjectMember(userB.id, 'viewer');
  const space = list(await get(`/api/spaces?projectId=${P}`))[0] || await post('/api/spaces', { projectId: P, title: 'E2E 自动化会话' });
  assert.equal((await B('GET', `/api/spaces/${space.id}`, undefined, 200)).id, space.id);
  assert.ok(Array.isArray(list(await B('GET', `/api/spaces?projectId=${P}`, undefined, 200))));
  await B('POST', `/api/spaces/${space.id}/messages`, { body: 'viewer says hi' }, 403);
  await B('PATCH', `/api/spaces/${space.id}`, { title: 'renamed by viewer' }, 403);
  await B('POST', `/api/spaces/${space.id}/artifacts`, { kind: 'doc', title: 'x' }, 403);
  assert.ok(Array.isArray(list(await B('GET', `/api/notification-bots?projectId=${P}`, undefined, 200))));
  await B('POST', '/api/notification-bots', { projectId: P, name: 'x', botId: uid('bot'), secret: 's' }, 403);
  await B('GET', `/api/insights/v1/sources?projectId=${P}`, undefined, 403);
  await restore();
});

test('user B as member speaks in spaces; only maintainers manage insight sources', { skip: tokenBSkip }, async () => {
  userB ||= await tokenOwner(cfg.tokenB);
  let restore = await withProjectMember(userB.id, 'member');
  const space = list(await get(`/api/spaces?projectId=${P}`))[0];
  const sent = await B('POST', `/api/spaces/${space.id}/messages`, { body: `e2e member B ${cfg.runTag}` }, 201);
  assert.equal(sent.message.userId, userB.id);
  await B('GET', `/api/insights/v1/sources?projectId=${P}`, undefined, 403);
  // Project settings stay with the owner even for members.
  await B('PUT', '/api/projects', { ...(await get(`/api/projects/${P}`)), name: 'hijacked' }, 403);
  await restore();

  restore = await withProjectMember(userB.id, 'maintainer');
  assert.ok(Array.isArray(list(await B('GET', `/api/insights/v1/sources?projectId=${P}`, undefined, 200))));
  await restore();
  const after = await get(`/api/projects/${P}`);
  assert.ok(!(after.members || []).some(m => m.userId === userB.id), 'the roster must be restored');
});

test('assistant attachments are private to the uploader', { skip: tokenBSkip }, async () => {
  userB ||= await tokenOwner(cfg.tokenB);
  const form = new FormData();
  form.set('projectId', P);
  form.set('file', new Blob([`e2e attachment ${cfg.runTag}`], { type: 'text/plain' }), 'e2e.txt');
  const att = await api('POST', '/api/assistant/attachments', form, { expect: 201 });
  assert.ok(att.id);
  const mine = await api('GET', `/api/assistant/attachments/${att.id}`, undefined, { raw: true });
  assert.equal(mine.text, `e2e attachment ${cfg.runTag}`);
  await B('GET', `/api/assistant/attachments/${att.id}`, undefined, [403, 404]);
  const restore = await withProjectMember(userB.id, 'member');
  // Even a project member cannot read another user's private upload.
  await B('GET', `/api/assistant/attachments/${att.id}`, undefined, [403, 404]);
  await restore();
});
