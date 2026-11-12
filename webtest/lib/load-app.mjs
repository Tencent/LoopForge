import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';

export const WEB_ROOT = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '../../web',
);

const clone = (value) => value === undefined ? undefined : structuredClone(value);

function response(body, status = 200, headers = {}) {
  const text = typeof body === 'string' ? body : JSON.stringify(body ?? null);
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? 'OK' : 'Error',
    headers: new Headers({ 'content-type': 'application/json', ...headers }),
    json: async () => JSON.parse(text),
    text: async () => text,
    blob: async () => new Blob([text]),
    arrayBuffer: async () => new TextEncoder().encode(text).buffer,
    clone() { return response(body, status, headers); },
  };
}

export function createFetchRouter(initialRoutes = []) {
  const routes = [];
  const calls = [];
  const route = (method, matcher, reply, status = 200) => {
    routes.push({ method: method.toUpperCase(), matcher, reply, status });
    return api;
  };
  const fetch = async (input, options = {}) => {
    const url = new URL(typeof input === 'string' ? input : input.url, 'http://127.0.0.1/');
    const method = (options.method || input?.method || 'GET').toUpperCase();
    let body = options.body;
    try { body = typeof body === 'string' ? JSON.parse(body) : body; } catch {}
    const call = { method, url, path: url.pathname, search: url.search, body, options };
    calls.push(call);
    const found = [...routes].reverse().find((item) =>
      (item.method === method || item.method === '*') &&
      (typeof item.matcher === 'string'
        ? item.matcher === url.pathname || item.matcher === `${url.pathname}${url.search}`
        : item.matcher.test(`${url.pathname}${url.search}`)));
    if (!found) return response(defaultPayload(url.pathname, method));
    const value = typeof found.reply === 'function' ? await found.reply(call) : clone(found.reply);
    if (value && typeof value === 'object' && 'status' in value && 'body' in value) {
      return response(value.body, value.status, value.headers);
    }
    return response(value, found.status);
  };
  const api = { fetch, calls, routes, route, get: (m, r, s) => route('GET', m, r, s),
    post: (m, r, s) => route('POST', m, r, s),
    put: (m, r, s) => route('PUT', m, r, s),
    patch: (m, r, s) => route('PATCH', m, r, s),
    delete: (m, r, s) => route('DELETE', m, r, s) };
  for (const item of initialRoutes) route(item.method || 'GET', item.path, item.body, item.status);
  return api;
}

function defaultPayload(pathname, method) {
  if (method !== 'GET') return { ok: true, success: true, id: 'created-1' };
  if (pathname === '/api/auth/current') return {
    user: { id: 'user-1', name: 'Test User', username: 'tester', isGuest: false, isPlatformAdmin: true },
    authRequired: false,
  };
  if (pathname === '/api/bootstrap') return sampleBootstrap();
  if (pathname === '/api/assistant/sessions') return [];
  if (/\/workflows\/metrics$/.test(pathname)) return sampleBootstrap().workflowMetrics;
  if (pathname === '/api/workflow-metrics') return sampleBootstrap().workflowMetrics;
  if (pathname === '/api/notification-bots') return sampleBootstrap().bots;
  if (pathname === '/api/notification-deliveries') return [];
  if (pathname === '/api/business-domains') return {
    items: sampleBootstrap().businessDomains.map((domain) => ({ domain, canView: true, canManage: true })),
    canAdmin: true,
    userId: 1,
  };
  if (pathname === '/api/business-domain-report') return {
    summary: { uniqueIssues: 1, totalRuns: 1, succeededRuns: 1, failedRuns: 0, successRate: 100,
      totalTokens: 1200, costReportedCny: 0.2, costCoveragePct: 100 },
    deliveryMetrics: { completedIssues: 1, mergedMrs: 1, cycleSamples: 1,
      averageCycleDurationMs: 3600000, p95CycleDurationMs: 3600000 },
    businessDomains: [], projects: [], trend: [], deliveryTrend: [], grain: 'day',
    timeZone: 'UTC', generatedAt: '2026-10-21T10:00:00Z',
  };
  if (pathname === '/api/knowledge/settings') return {
    ready: true, url: 'https://knowledge.example/mcp', tokenConfigured: true,
    defaultSources: ['handbook'], projectSources: {},
  };
  if (/^\/api\/knowledge\/(submissions|candidates|sources)$/.test(pathname)) return [];
  if (pathname === '/api/knowledge/usage') return { searches: 3, fetches: 2, agents: 1 };
  if (/^\/api\/(projects|issues|tasks|runs|flows|workflows|sops|agents|runners|assets|bots|rooms|users|notifications|revisions|approvals|business-domains|rules|skills|mcp|mcp-servers|executors|dockerfiles|environments|sandboxes|schedule-jobs)$/.test(pathname)) {
    const key = pathname.split('/').at(-1).replace('flows', 'workflows').replace('mcp-servers', 'mcpServers').replace('schedule-jobs', 'scheduleJobs');
    return sampleBootstrap()[key] || [];
  }
  const collection = pathname.match(/\/(projects|issues|tasks|runs|workflows|agents|runners|bots|users|rules|skills|mcp-servers|dockerfiles|environments|sandboxes|schedule-jobs)$/)?.[1];
  if (collection) {
    const key = collection.replace('mcp-servers', 'mcpServers').replace('schedule-jobs', 'scheduleJobs');
    return sampleBootstrap()[key] || [];
  }
  if (/\/(health|status)$/.test(pathname)) return { ok: true, status: 'ready' };
  if (/\/(assets|rooms|notifications|revisions|approvals|domains|servers|sessions)$/.test(pathname)) {
    return { items: [], projects: [], issues: [], runs: [], flows: [], sops: [], agents: [],
      runners: [], assets: [], bots: [], rooms: [], users: [], notifications: [], total: 0 };
  }
  if (/insight|metric|observ|dashboard|analytics/.test(pathname)) {
    return { items: [], series: [], points: [], labels: [], datasets: [], summary: {}, total: 0 };
  }
  return { items: [], data: [], results: [], total: 0, ok: true };
}

export function sampleBootstrap() {
  const project = { id: 'project-1', name: 'Atlas', identifier: 'ATL', description: 'Test project',
    status: 'active', defaultRepositoryId: 'repo-1', businessDomainId: 'domain-1', memberCount: 2 };
  const repository = { id: 'repo-1', projectId: project.id, name: 'atlas-web', provider: 'gongfeng',
    gongfengProjectId: '1001', webUrl: 'https://git.example/atlas', cloneUrl: 'git@example:atlas.git',
    defaultBranch: 'main', integrateBranch: 'develop', localPath: '/workspace/atlas' };
  const agent = { id: 'agent-1', projectId: project.id, name: 'Developer', role: 'developer',
    description: 'Implements changes', model: 'gpt', prompt: 'Build safely', enabled: true, tools: [] };
  const workflow = { id: 'flow-1', projectId: project.id, name: 'Delivery Flow', description: 'Ship work',
    enabled: true, nodes: [
      { id: 'node-1', type: 'agent', name: 'Implement', agentId: agent.id, position: { x: 120, y: 100 }, config: {} },
      { id: 'node-2', type: 'approval', name: 'Review', position: { x: 420, y: 100 }, config: {} },
    ], edges: [{ id: 'edge-1', source: 'node-1', target: 'node-2' }] };
  const issue = { id: 'issue-1', projectId: project.id, title: 'Improve dashboard', description: 'Add metrics',
    status: 'open', priority: 'high', assignee: 'tester', labels: ['frontend'], repositoryIds: [repository.id],
    createdAt: '2026-10-20T10:00:00Z', updatedAt: '2026-10-21T10:00:00Z', tasks: [] };
  const run = { id: 'run-1', projectId: project.id, issueId: issue.id, workflowId: workflow.id,
    workflowName: workflow.name, status: 'running', currentNodeId: 'node-1', progress: 45,
    createdAt: '2026-10-21T10:00:00Z', startedAt: '2026-10-21T10:01:00Z',
    nodes: [{ id: 'node-1', name: 'Implement', status: 'running' }] };
  const asset = (id, name) => ({ id, projectId: project.id, name, description: `${name} fixture`,
    content: '# fixture', scope: 'project', updatedAt: '2026-10-20T10:00:00Z' });
  return {
    projects: [project], repositories: [repository], users: [
      { id: 'user-1', name: 'Test User', username: 'tester', role: 'admin', isPlatformAdmin: true },
    ], issues: [issue], tasks: [{ id: 'task-1', issueId: issue.id, runId: run.id, status: 'running' }],
    runs: [run], workflows: [workflow], agents: [agent], workflowMetrics: [
      { workflowId: workflow.id, total: 4, succeeded: 3, failed: 1 },
    ], rules: [asset('rule-1', 'Code style')], skills: [asset('skill-1', 'Testing')],
    dockerfiles: [asset('docker-1', 'Node image')], environments: [asset('env-1', 'Node setup')],
    sandboxes: [asset('sandbox-1', 'Restricted')], mcpServers: [
      { id: 'mcp-1', projectId: project.id, name: 'Filesystem', command: 'npx', args: ['server'], enabled: true },
    ], executors: [{ id: 'runner-1', name: 'Linux Runner', status: 'online', maxConcurrent: 2 }],
    runners: [{ id: 'runner-1', name: 'Linux Runner', status: 'online', maxConcurrent: 2 }],
    scheduleJobs: [{ id: 'job-1', name: 'Nightly sync', cron: '0 2 * * *', enabled: true }],
    syncLogs: [{ id: 'log-1', status: 'success', message: 'Synced', createdAt: '2026-10-20T10:00:00Z' }],
    bots: [{ id: 'bot-1', name: 'Helper', type: 'wecom', enabled: true }],
    businessDomains: [{ id: 'domain-1', name: 'Commerce', description: 'Commerce systems' }],
  };
}

class ObserverStub {
  constructor(callback) { this.callback = callback; }
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() { return []; }
}

export async function loadApp({ routes = [], hash = '', dispatchReady = true, origin = 'http://127.0.0.1' } = {}) {
  const html = await readFile(path.join(WEB_ROOT, 'index.html'), 'utf8');
  const dom = new JSDOM(html, {
    runScripts: 'outside-only',
    pretendToBeVisual: true,
    url: `${origin}/${hash ? `#${hash.replace(/^#/, '')}` : ''}`,
  });
  const { window } = dom;
  const router = createFetchRouter(routes);

  window.fetch = router.fetch;
  window.structuredClone = globalThis.structuredClone;
  window.alert = () => {};
  window.confirm = () => true;
  window.prompt = (_message, fallback = '') => fallback;
  window.matchMedia = (query) => ({ matches: false, media: query, onchange: null,
    addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {}, dispatchEvent() { return true; } });
  window.ResizeObserver = ObserverStub;
  window.IntersectionObserver = ObserverStub;
  window.MutationObserver = ObserverStub;
  window.requestAnimationFrame = (callback) => window.setTimeout(() => {
    try { callback(Date.now()); } catch {}
  }, 0);
  window.cancelAnimationFrame = window.clearTimeout;
  window.scrollTo = () => {};
  window.HTMLElement.prototype.scrollTo = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
  window.HTMLElement.prototype.animate = () => ({ finished: Promise.resolve(), cancel() {}, finish() {}, reverse() {} });
  if (window.HTMLDialogElement) {
    window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
    window.HTMLDialogElement.prototype.show = function () { this.open = true; };
    window.HTMLDialogElement.prototype.close = function () {
      this.open = false;
      this.dispatchEvent(new window.Event('close'));
    };
  }
  Object.defineProperty(window.navigator, 'clipboard', {
    configurable: true,
    value: { writeText: async () => {}, readText: async () => '' },
  });
  window.URL.createObjectURL ||= () => 'blob:test';
  window.URL.revokeObjectURL ||= () => {};
  window.CSS ||= {};
  window.CSS.escape ||= (value) => String(value).replace(/[^a-zA-Z0-9_-]/g, (char) => `\\${char}`);
  window.DOMMatrixReadOnly ||= class {
    constructor() { this.m41 = 0; this.m42 = 0; }
  };
  window.open = () => ({ close() {}, focus() {}, location: { href: '' } });

  class EventSourceStub extends window.EventTarget {
    static CONNECTING = 0; static OPEN = 1; static CLOSED = 2;
    constructor(url) { super(); this.url = url; this.readyState = 1; EventSourceStub.instances.push(this); }
    close() { this.readyState = 2; }
    emit(data, type = 'message') {
      const event = new window.MessageEvent(type, { data: typeof data === 'string' ? data : JSON.stringify(data) });
      this.dispatchEvent(event);
      this[`on${type}`]?.(event);
    }
  }
  EventSourceStub.instances = [];
  class WebSocketStub extends window.EventTarget {
    static CONNECTING = 0; static OPEN = 1; static CLOSING = 2; static CLOSED = 3;
    constructor(url) { super(); this.url = url; this.readyState = 1; this.sent = []; WebSocketStub.instances.push(this); }
    send(value) { this.sent.push(value); }
    close() { this.readyState = 3; this.onclose?.(new window.CloseEvent('close')); }
  }
  WebSocketStub.instances = [];
  window.EventSource = EventSourceStub;
  window.WebSocket = WebSocketStub;

  const scriptElements = [...window.document.querySelectorAll('script[src]')];
  const files = scriptElements.map((element) => {
    const src = new URL(element.getAttribute('src'), window.location.href);
    return path.join(WEB_ROOT, decodeURIComponent(src.pathname).replace(/^\/+/, ''));
  });
  const context = dom.getInternalVMContext();
  const sources = await Promise.all(files.map((filename) => readFile(filename, 'utf8')));
  for (let index = 0; index < files.length; index += 1) {
    new vm.Script(sources[index], { filename: files[index] }).runInContext(context);
  }
  if (dispatchReady) {
    window.document.dispatchEvent(new window.Event('DOMContentLoaded', { bubbles: true }));
    window.dispatchEvent(new window.Event('load'));
    await settle();
  }
  return { dom, window, document: window.document, router, files, context,
    evaluate: (source) => new vm.Script(source).runInContext(context),
    close: async () => { await settle(6); dom.window.close(); } };
}

// hui-select redefines `value` on enhanced selects, which jsdom's proxied
// HTMLSelectElement rejects from outside the page realm; pick the option itself.
export function chooseOption(select, value) {
  const option = [...select.options].find((item) => item.value === value);
  if (!option) throw new Error(`select has no option ${value}`);
  option.selected = true;
  select.dispatchEvent(new select.ownerDocument.defaultView.Event('change', { bubbles: true }));
}

export async function settle(turns = 4) {
  for (let i = 0; i < turns; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
}
