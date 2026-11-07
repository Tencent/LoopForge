import assert from 'node:assert/strict';
import { cfg, requireToken } from './env.mjs';

export class HTTPError extends Error {
  constructor(method, path, status, body) {
    super(`${method} ${path} -> HTTP ${status}: ${typeof body === 'string' ? body.slice(0, 400) : JSON.stringify(body).slice(0, 400)}`);
    this.status = status;
    this.body = body;
  }
}

// api performs one request against the target deployment. `expect` lists the
// acceptable statuses; anything else throws with the response body so a
// failing assertion explains itself. `token` replaces the suite token (e.g.
// cfg.tokenB for a second user); `cookie` sends a browser session instead.
export async function api(method, path, body, opts = {}) {
  const { expect = [200, 201, 202, 204], auth = true, headers = {}, raw = false, timeoutMs = 60_000, token, cookie } = opts;
  const init = { method, headers: { Accept: 'application/json', ...headers }, signal: AbortSignal.timeout(timeoutMs) };
  if (cookie) init.headers.Cookie = cookie;
  else if (auth) init.headers.Authorization = `Bearer ${token || requireToken()}`;
  if (body instanceof FormData) {
    init.body = body;
  } else if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = typeof body === 'string' ? body : JSON.stringify(body);
  }
  const res = await fetch(cfg.base + path, init);
  const text = await res.text();
  let data = text;
  if ((res.headers.get('content-type') || '').includes('json') && text) {
    try { data = JSON.parse(text); } catch { /* keep text */ }
  }
  const expected = Array.isArray(expect) ? expect : [expect];
  if (!expected.includes(res.status)) throw new HTTPError(method, path, res.status, data);
  return raw ? { status: res.status, headers: res.headers, data, text } : data;
}

export const get = (path, opts) => api('GET', path, undefined, opts);
export const post = (path, body, opts) => api('POST', path, body ?? {}, opts);
export const put = (path, body, opts) => api('PUT', path, body, opts);
export const del = (path, opts) => api('DELETE', path, undefined, { expect: [200, 204, 404], ...opts });

export function list(data) {
  if (Array.isArray(data)) return data;
  for (const key of ['items', 'data', 'list']) if (Array.isArray(data?.[key])) return data[key];
  return [];
}

export async function waitFor(label, fn, { timeoutMs = 120_000, intervalMs = 2_000 } = {}) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    last = await fn();
    if (last) return last;
    await new Promise(r => setTimeout(r, intervalMs));
  }
  throw new Error(`timed out after ${timeoutMs}ms waiting for ${label}`);
}

const TERMINAL = new Set(['succeeded', 'failed', 'cancelled', 'rejected']);

// waitRun polls a run until `predicate(run)` holds. It fails fast when the run
// reaches a terminal status the caller did not ask for.
export async function waitRun(id, predicate, { timeoutMs = 300_000, label = 'run condition' } = {}) {
  let run;
  return waitFor(`${id}: ${label}`, async () => {
    run = await get(`/api/runs/${id}`);
    if (predicate(run)) return run;
    if (TERMINAL.has(run.status)) {
      const nodes = Object.values(run.nodes || {}).map(n => `${n.nodeId}=${n.status}${n.error ? `(${String(n.error).slice(0, 160)})` : ''}`).join(', ');
      throw new Error(`${id} ended ${run.status} before ${label}; nodes: ${nodes}; error: ${run.error || ''}`);
    }
    return null;
  }, { timeoutMs, intervalMs: 3_000 });
}

export function nodeOf(run, nodeId) {
  const node = run.nodes?.[nodeId];
  assert.ok(node, `run ${run.id} has no node ${nodeId}; nodes: ${Object.keys(run.nodes || {}).join(',')}`);
  return node;
}

export const nodeStatus = (run, nodeId) => run.nodes?.[nodeId]?.status;

// Cleanup registry: every created resource registers its own teardown so a
// failing test still leaves the deployment clean.
const cleanups = [];
export function track(label, fn) {
  cleanups.push({ label, fn });
}
export async function cleanup() {
  if (cfg.keep) {
    console.log(`AGENTFLOW_E2E_KEEP=1: leaving ${cleanups.length} resources in place`);
    cleanups.length = 0;
    return;
  }
  const failures = [];
  while (cleanups.length) {
    const { label, fn } = cleanups.pop();
    try { await fn(); } catch (err) { failures.push(`${label}: ${err.message}`); }
  }
  if (failures.length) console.warn(`cleanup failures:\n  ${failures.join('\n  ')}`);
}

// ensureProject returns the long-lived e2e project. Projects have no delete
// endpoint, so the suite reuses one fixed project instead of leaking one per run.
// A policy with enabled=false and no threshold counts as "unset" and falls back
// to the platform default (enabled, 80 points), so disabling needs a threshold.
export const CLARIFICATION_OFF = { enabled: false, skillId: 'requirement-clarification', threshold: 80 };

export async function ensureProject() {
  let project = await get(`/api/projects/${cfg.projectId}`, { expect: [200, 404] });
  if (project?.id !== cfg.projectId) {
    project = await post('/api/projects', {
      id: cfg.projectId,
      key: 'E2E',
      name: 'E2E 自动化测试（勿用）',
      description: 'e2e/ 目录下的实测套件专用项目，资源会被自动创建和清理。',
      color: '#8b5cf6',
      clarification: CLARIFICATION_OFF,
    });
  }
  // AI analysis and dispatch need a responsible user; default to the token owner.
  const ownerId = project.ownerId || (await tokenOwner()).id;
  if (project.clarification?.enabled || project.ownerId !== ownerId) {
    project = await put('/api/projects', { ...project, ownerId, clarification: { ...CLARIFICATION_OFF, ...project.clarification, enabled: false } });
  }
  return project;
}

// tokenOwner resolves who the API token belongs to. There is no "whoami" for
// tokens, so a knowledge review item (stamped with the caller's login) is
// created and deleted at once; it never reaches the knowledge base. It has no
// project, so tokens that cannot write the e2e project can still probe.
const owners = new Map();
export async function tokenOwner(token = cfg.token) {
  if (owners.has(token)) return owners.get(token);
  const probe = await post('/api/knowledge/submissions', { title: 'e2e-auto whoami', content: 'noop' }, { token });
  await del(`/api/knowledge/submissions/${probe.id}`, { token });
  const user = list(await get('/api/users')).find(u => u.loginName === probe.createdBy);
  if (!user) throw new Error(`token owner ${probe.createdBy} not found in /api/users`);
  const admins = list(await get('/api/platform/admins'));
  user.isPlatformAdmin = user.platformRole === 'admin' || admins.some(a => a.loginName === user.loginName);
  owners.set(token, user);
  return user;
}

export const tokenBSkip = cfg.tokenB ? false : 'AGENTFLOW_E2E_TOKEN_B not set (a second, non-admin user)';
export const asB = (opts = {}) => ({ ...opts, token: cfg.tokenB });

// browserSession signs in through the loopback-only acceptance login and
// returns the session cookie, or null when the deployment does not enable it
// (only local.sh does). Session-only endpoints (personal API tokens, WeCom
// bindings) refuse Bearer tokens, so this is the only way to reach them.
export async function browserSession() {
  const probe = await get('/api/auth/local', { auth: false });
  if (!probe?.enabled) return null;
  const res = await api('POST', '/api/auth/local', {}, { auth: false, headers: { Origin: cfg.base }, raw: true });
  const cookie = res.headers.getSetCookie().map(c => c.split(';')[0]).join('; ');
  if (!cookie) throw new Error('acceptance login set no cookie');
  return cookie;
}

// withProjectMember temporarily makes user a member of the e2e project with
// role ('viewer', 'member', 'maintainer' or null to remove) and restores the
// original roster when the returned function runs (also on cleanup).
export async function withProjectMember(userId, role) {
  const original = await get(`/api/projects/${cfg.projectId}`);
  const members = (original.members || []).filter(m => m.userId !== userId);
  if (role) members.push({ userId, role });
  await put('/api/projects', { ...original, members });
  let restored = false;
  const restore = async () => {
    if (restored) return;
    restored = true;
    const now = await get(`/api/projects/${cfg.projectId}`);
    await put('/api/projects', { ...now, members: original.members || [] });
  };
  track(`project members ${cfg.projectId}`, restore);
  return restore;
}

export async function setClarification(enabled) {
  const project = await get(`/api/projects/${cfg.projectId}`);
  return put('/api/projects', { ...project, clarification: { ...CLARIFICATION_OFF, ...project.clarification, enabled } });
}
