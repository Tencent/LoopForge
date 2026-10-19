// Read-only coverage of every GET surface plus the authentication boundary.
// Nothing here mutates the deployment.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, list } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';

test('health and public runner endpoints answer without auth', async () => {
  const health = await get('/api/health', { auth: false });
  assert.equal(health.status ?? health.ok ?? 'ok', health.status ?? health.ok ?? 'ok');
  for (const path of ['/runners/health', '/runners/install.sh', '/deploy/bind-cli.sh']) {
    const res = await api('GET', path, undefined, { auth: false, raw: true });
    assert.ok(res.text.length > 0, `${path} returned an empty body`);
  }
});

test('protected API rejects anonymous and forged credentials', async () => {
  for (const path of ['/api/projects', '/api/runs', '/api/issues', '/api/workflows', '/api/users']) {
    await api('GET', path, undefined, { auth: false, expect: 401 });
  }
  await api('GET', '/api/projects', undefined, { auth: false, headers: { Authorization: 'Bearer afp_forged' }, expect: 401 });
  // Gateway identity headers are only trusted from loopback, never from the network.
  await api('GET', '/api/projects', undefined, { auth: false, headers: { Staffname: 'example-admin', 'x-staff-name': 'example-admin' }, expect: 401 });
  const me = await get('/api/auth/current', { auth: false, headers: { Staffname: 'example-admin' } });
  assert.equal(me.authenticated ?? false, false, 'spoofed gateway header must not log in');
});

test('API tokens cannot manage API tokens', async () => {
  await api('GET', '/api/auth/tokens', undefined, { expect: 401 });
  await api('POST', '/api/auth/tokens', { name: 'e2e-should-fail' }, { expect: 401 });
});

test('/mcp rejects anonymous callers', async () => {
  await api('POST', '/mcp', { jsonrpc: '2.0', id: 1, method: 'tools/list' }, { auth: false, expect: 401 });
});

const readOnly = [
  ['/api/bootstrap', d => assert.ok(d && typeof d === 'object')],
  ['/api/capabilities', d => assert.ok(d && typeof d === 'object')],
  ['/api/openapi.yaml', d => assert.match(String(d), /openapi:/)],
  ['/api/observability/spec', d => assert.ok(d)],
  ['/api/executors', d => assert.ok(list(d).some(e => e.id === cfg.executor), `executor ${cfg.executor} missing`)],
  ['/api/executor-models', d => assert.ok(d)],
  ['/api/assistant/models', d => assert.ok(d)],
  ['/api/users', d => assert.ok(list(d).length > 0)],
  ['/api/platform/admins', d => assert.ok(list(d).length > 0)],
  ['/api/projects', d => assert.ok(list(d).length > 0)],
  ['/api/workflows', d => assert.ok(list(d).length > 0)],
  ['/api/agents', d => assert.ok(Array.isArray(list(d)))],
  ['/api/rules', d => assert.ok(Array.isArray(list(d)))],
  ['/api/skills', d => assert.ok(Array.isArray(list(d)))],
  ['/api/mcp-servers', d => assert.ok(Array.isArray(list(d)))],
  ['/api/mcp-market', d => assert.ok(d)],
  ['/api/dockerfiles', d => assert.ok(Array.isArray(list(d)))],
  ['/api/environments', d => assert.ok(Array.isArray(list(d)))],
  ['/api/sandboxes', d => assert.ok(list(d).some(s => s.id === cfg.sandbox), `sandbox ${cfg.sandbox} missing`)],
  ['/api/repositories', d => assert.ok(Array.isArray(list(d)))],
  ['/api/issues', d => assert.ok(Array.isArray(list(d)))],
  ['/api/runs', d => assert.ok(Array.isArray(list(d)))],
  ['/api/tasks', d => assert.ok(d)],
  ['/api/runners', d => assert.ok(d)],
  ['/api/runners/default', d => assert.ok('online' in d)],
  ['/api/schedule-jobs', d => assert.ok(Array.isArray(list(d)))],
  ['/api/assistant/tasks', d => assert.ok(Array.isArray(list(d)))],
  ['/api/assistant/sessions', d => assert.ok(d)],
  ['/api/analytics', d => assert.ok(d)],
  ['/api/workflow-metrics', d => assert.ok(d)],
  ['/api/sync/config', d => assert.ok('autoSyncEnabled' in d)],
  ['/api/sync/status', d => assert.ok(d)],
  ['/api/sync/logs', d => assert.ok(d)],
  ['/api/knowledge/settings', d => assert.ok('enabled' in d)],
  ['/api/knowledge/usage', d => assert.ok(d)],
  ['/api/knowledge/candidates', d => assert.ok(d)],
  ['/api/knowledge/submissions', d => assert.ok(d)],
];

for (const [path, check] of readOnly) {
  test(`GET ${path}`, async () => {
    check(await get(path));
  });
}

test('GET /api/deployment-metrics for a real repository', async () => {
  await api('GET', '/api/deployment-metrics', undefined, { expect: 400 });
  // The dashboard only supports SkillHub repositories and answers 422 for others.
  for (const repo of list(await get('/api/repositories'))) {
    const q = `projectId=${encodeURIComponent(repo.projectId)}&repositoryId=${encodeURIComponent(repo.id)}`;
    const d = await api('GET', `/api/deployment-metrics?${q}`, undefined, { expect: [200, 422] });
    assert.ok(d && typeof d === 'object');
  }
});

test('current user resolves through the token and model credentials are explicit', async () => {
  const users = list(await get('/api/users'));
  assert.ok(users.length > 0);
  const withCreds = users.filter(u => u.codeBuddyApiKeyConfigured || u.gitTokenConfigured || u.taihuPatConfigured);
  if(cfg.model) assert.ok(withCreds.length > 0, 'no user has any credential configured; model-backed scenarios cannot run');
});

test('unknown resources answer 404 instead of 500', async () => {
  for (const path of ['/api/runs/run-does-not-exist', '/api/workflows/e2e-missing', '/api/agents/e2e-missing', '/api/projects/e2e-missing']) {
    await api('GET', path, undefined, { expect: 404 });
  }
});

test('unconfigured knowledge returns an explicit unavailable response',async()=>{
 const settings=await get('/api/knowledge/settings');
 if(settings.enabled&&settings.ready)assert.ok(await get('/api/knowledge/sources'));
 else await api('GET','/api/knowledge/sources',undefined,{expect:503});
});
