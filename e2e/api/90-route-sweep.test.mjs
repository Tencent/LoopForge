// Route sweep: the endpoints the scenario files do not reach, exercised through
// their deterministic edges (validation, unknown resources, missing
// credentials) plus one runner session lifecycle. Safe for a shared
// deployment: nothing here starts a model generation, claims a queued runner
// task, creates TAPD/Gongfeng tickets, writes the knowledge base, or changes
// platform-wide sync/knowledge settings.
import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import { api, get, post, put, cleanup, track } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';

after(cleanup);

const MISSING = `${cfg.prefix}-missing-${cfg.runTag}`;

// status sends one request and returns the status after checking it is one of
// the accepted ones (api throws with the body otherwise).
async function status(method, path, body, expect, opts = {}) {
  const res = await api(method, path, body, { expect, raw: true, ...opts });
  return res;
}

// binary fetches a download endpoint without reading the (large) body.
async function binary(path) {
  const res = await fetch(cfg.base + path, { headers: { Authorization: `Bearer ${cfg.token}` }, signal: AbortSignal.timeout(60_000) });
  await res.body?.cancel();
  return res;
}

test('assistant entry points refuse to start a generation without a model key', { skip: cfg.model && 'a model key is configured: these calls would start real generations' }, async () => {
  for (const path of ['/api/assistant/chat', '/api/assistant/generations', `/api/assistant/tasks/${MISSING}/run`]) {
    const res = await status('POST', path, {}, [400, 404]);
    assert.ok(res.data.error, `${path} explains the refusal`);
  }
  assert.ok(Array.isArray(await get('/api/assistant/references')));
});

test('auth edges: code exchange needs a code, unconfigured OAuth and photo answer cleanly', async () => {
  for (const method of ['GET', 'POST']) {
    const res = await status(method, '/api/auth/exchange', method === 'POST' ? {} : undefined, [400]);
    assert.match(res.data.error, /code/);
  }
  for (const path of ['/api/auth/gongfeng/start', '/api/auth/gongfeng/callback']) {
    const res = await fetch(cfg.base + path, { redirect: 'manual', headers: { Authorization: `Bearer ${cfg.token}` } });
    await res.body?.cancel();
    assert.ok([302, 303, 400, 503].includes(res.status), `${path} -> ${res.status}`);
  }
  await status('GET', '/api/auth/photo', undefined, [200, 404, 502]);
});

test('clarification endpoints validate the project and the issue', async () => {
  assert.match((await status('POST', '/api/chat-clarify', {}, [400])).data.error, /projectId/);
  assert.equal((await post('/api/chat-clarify/cancel', {})).cancelled, true);
  for (const sub of ['chat-clarify', 'chat-clarify/cancel', 'clarification']) {
    await status('GET', `/api/issues/${MISSING}/${sub}`, undefined, [404]);
  }
});

test('TAPD import and tracker sync reject incomplete requests before calling out', async () => {
  assert.match((await status('POST', '/api/issues/import-tapd', {}, [400])).data.error, /projectId/);
  assert.match((await status('POST', '/api/sync/create', {}, [400])).data.error, /issueId/);
});

test('knowledge: check reports its state, search and document validate input, the runner proxy is guarded', async () => {
  const check = await post('/api/knowledge/check', {});
  assert.equal(typeof check.ok, 'boolean');
  await status('GET', '/api/knowledge/document', undefined, [400]);
  await status('POST', '/api/knowledge/search', {}, [400]);
  await status('GET', '/runners/knowledge/mcp', undefined, [200, 400, 401, 405, 406, 503]);
});

test('MCP market install and import validate their payloads', async () => {
  await status('POST', '/api/mcp-market/install', {}, [400]);
  await status('POST', '/api/mcp-servers/import', {}, [400]);
  const res = await status('POST', '/api/mcp-servers/import', { projectId: cfg.projectId, json: 'not json' }, [400]);
  assert.ok(res.data.error);
});

test('unknown bots, rooms and spaces answer 404', async () => {
  await status('GET', `/api/notification-bots/${MISSING}/sessions`, undefined, [404]);
  await status('PATCH', `/api/rooms/${MISSING}`, {}, [404]);
  await status('POST', `/api/rooms/${MISSING}/decisions`, {}, [404]);
  for (const sub of ['decisions', 'flows', 'sops']) await status('POST', `/api/spaces/${MISSING}/${sub}`, {}, [404]);
});

test('OTLP ingest accepts an empty export', async () => {
  await status('POST', '/api/otel/v1/traces', {}, [200, 202]);
});

test('platform admins and repository registration validate required fields', async () => {
  await status('PUT', '/api/platform/admins', {}, [400, 403]);
  assert.match((await status('PUT', '/api/repositories', {}, [400])).data.error, /projectId/);
});

test('run sub-resources of an unknown run answer 404 (or 400 for body-validated actions)', async () => {
  const r = `/api/runs/${MISSING}`;
  const notFound = [
    ['POST', `${r}/browser`], ['GET', `${r}/browser/stream`],
    ['POST', `${r}/eval`], ['POST', `${r}/eval/apply`], ['GET', `${r}/eval/stream`], ['GET', `${r}/evals`],
    ['GET', `${r}/preview`], ['DELETE', `${r}/preview`], ['PUT', `${r}/preview/backend`], ['GET', `${r}/preview/download`], ['POST', `${r}/preview/session`],
    ['GET', `${r}/workspace/commit`], ['POST', `${r}/workspace/restore`], ['POST', `${r}/workspace/terminal`],
  ];
  for (const [method, path] of notFound) {
    await status(method, path, ['GET', 'DELETE'].includes(method) ? undefined : {}, [404]);
  }
  await status('POST', `${r}/refresh-sop`, {}, [400, 404]);
  await status('POST', `${r}/signals`, {}, [400]);
  // Closing a preview session that does not exist is idempotent.
  await status('DELETE', `${r}/preview/session/${MISSING}`, undefined, [200, 204, 404]);
});

test('unknown tasks and workflows answer 404; token verification needs a valid user id', async () => {
  await status('GET', `/api/tasks/${MISSING}`, undefined, [404]);
  await status('GET', `/api/workflows/${MISSING}/agents`, undefined, [404]);
  await status('PUT', `/api/workflows/${MISSING}/agents`, {}, [404]);
  for (const method of ['GET', 'POST']) {
    const res = await status(method, '/api/users/not-a-number/git-token/verify', method === 'POST' ? {} : undefined, [400]);
    assert.equal(res.data.valid, false);
  }
});

test('runner and gate MCP downloads are served or explain how to enable them', async () => {
  for (const path of ['/deploy/agentflow-runner', '/deploy/agentflow-gate-mcp', '/runners/agentflow-gate-mcp']) {
    const res = await binary(path);
    assert.ok([200, 404].includes(res.status), `${path} -> ${res.status}`);
  }
});

test('internal agent-tool and checkpoint endpoints reject callers without their credentials', async () => {
  await status('POST', `/internal/agent-tools/${MISSING}/mcp`, {}, [401]);
  await status('DELETE', `/internal/agent-tools/${MISSING}/mcp`, undefined, [401]);
  await status('POST', '/internal/workspace-checkpoint', {}, [401]);
});

test('runner task plane requires a runner session', async () => {
  const noSession = { auth: false };
  const routes = [
    ['POST', '/api/runners/heartbeat'], ['POST', '/api/runners/deregister'], ['POST', '/api/runners/disconnect'],
    ['POST', '/api/runners/tasks/claim'], ['GET', `/api/runners/tasks/${MISSING}`],
    ['POST', `/api/runners/tasks/${MISSING}/start`], ['POST', `/api/runners/tasks/${MISSING}/lease/renew`],
    ['GET', `/api/runners/tasks/${MISSING}/events`], ['POST', `/api/runners/tasks/${MISSING}/events`],
    ['POST', `/api/runners/tasks/${MISSING}/complete`], ['GET', `/api/runners/tasks/${MISSING}/control`],
    ['POST', '/runners/tasks/claim'], ['GET', `/runners/tasks/${MISSING}/control`],
    ['POST', `/runners/previews/${MISSING}/complete`], ['PUT', `/runners/previews/${MISSING}/content`],
  ];
  for (const [method, path] of routes) {
    await status(method, path, ['GET'].includes(method) ? undefined : {}, [401], noSession);
  }
});

test('runner management: register, heartbeat, runtime status, run limit, unknown tasks, disconnect and deregister', async (t) => {
  const reg = await status('POST', '/api/runners/register', { name: `${cfg.prefix}-route-sweep-${cfg.runTag}`, hostname: `${cfg.prefix}-host` }, [200, 401, 503]);
  if (reg.status !== 200) {
    t.skip(`runner registration is not available to this token (${reg.status})`);
    return;
  }
  const { runnerId, sessionToken } = reg.data;
  assert.match(runnerId, /^runner-/);
  assert.match(sessionToken, /^afr_/);
  track(`runner ${runnerId}`, () => api('DELETE', `/api/runners/${runnerId}`, undefined, { expect: [200, 204, 404] }));
  const session = { token: sessionToken };

  const beat = await post('/api/runners/heartbeat', {}, session);
  assert.equal(beat.ok, true);
  await status('POST', '/api/runners/runtimes', {}, [400]);
  await status('PUT', '/api/runners/runtimes', { runnerId, provider: 'codebuddy' }, [400]);
  await status('PUT', `/api/runners/${runnerId}/run-limit`, { runLimit: 500 }, [400]);
  await status('PUT', `/api/runners/${runnerId}/run-limit`, { runLimit: 2 }, [200, 403]);
  assert.ok(Array.isArray(await get('/api/runners/tasks')));
  await status('POST', '/api/runners/tasks', {}, [400]);
  await status('POST', `/api/runners/tasks/${MISSING}/cancel`, {}, [200, 404]);
  for (const [method, path] of [
    ['GET', `/api/runners/tasks/${MISSING}`], ['POST', `/api/runners/tasks/${MISSING}/start`],
    ['POST', `/api/runners/tasks/${MISSING}/lease/renew`], ['GET', `/api/runners/tasks/${MISSING}/control`],
  ]) {
    await status(method, path, method === 'GET' ? undefined : {}, [400, 403, 404, 409], session);
  }
  await status('POST', '/api/runners/disconnect', {}, [200], session);
  await status('POST', '/api/runners/deregister', {}, [200], session);
  await status('POST', '/api/runners/heartbeat', {}, [401], session);
  await status('DELETE', `/api/runners/${runnerId}`, undefined, [200, 204, 404]);
});
