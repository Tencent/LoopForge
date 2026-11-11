'use strict';

class LoopForgeError extends Error {
  constructor(message, details = {}) {
    super(message);
    this.name = 'LoopForgeError';
    Object.assign(this, details);
  }
}

function normalizeBaseUrl(value) {
  const raw = String(value || '').trim();
  if (!raw) throw new LoopForgeError('LoopForge server URL is empty. Set loopforge.serverUrl first.');
  let url;
  try { url = new URL(raw); } catch (_) { throw new LoopForgeError(`Invalid LoopForge server URL: ${raw}`); }
  if (!['http:', 'https:'].includes(url.protocol)) {
    throw new LoopForgeError('LoopForge server URL must use http:// or https://.');
  }
  if (url.username || url.password || url.search || url.hash) throw new LoopForgeError('Server URL must not include credentials, a query, or a fragment.');
  return url.toString().replace(/\/$/, '');
}

function apiPath(path) {
  return String(path || '').startsWith('/') ? String(path) : `/${path}`;
}

class LoopForgeClient {
  constructor({ baseUrl, token = '', fetchImpl = globalThis.fetch, timeoutMs = 30000 } = {}) {
    this.baseUrl = normalizeBaseUrl(baseUrl);
    this.token = String(token || '').trim();
    this.fetch = fetchImpl;
    this.timeoutMs = timeoutMs;
    if (typeof this.fetch !== 'function') throw new LoopForgeError('This VS Code runtime does not provide fetch.');
  }

  async request(path, { method = 'GET', body, signal, parseJson = true } = {}) {
    const headers = { Accept: 'application/json' };
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (this.token) headers.Authorization = `Bearer ${this.token}`;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    if (signal) {
      if (signal.aborted) controller.abort();
      signal.addEventListener('abort', () => controller.abort(), { once: true });
    }
    let response, raw;
    try {
      response = await this.fetch(`${this.baseUrl}${apiPath(path)}`, {
        method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal, redirect: 'error'
      });
      raw = await response.text();
    } catch (error) {
      if (error?.name === 'AbortError') throw new LoopForgeError(`LoopForge request timed out after ${this.timeoutMs}ms.`, { cause: error, code: 'timeout' });
      throw new LoopForgeError(`Cannot connect to LoopForge at ${this.baseUrl}: ${error?.message || error}. Check that the LoopForge server is running and that loopforge.serverUrl is reachable from the VS Code extension host.`, { cause: error, code: 'connection' });
    } finally { clearTimeout(timer); }
    const contentType = response.headers?.get?.('content-type') || '';
    let payload = raw;
    if ((parseJson || !response.ok) && (contentType.includes('json') || raw.trim().startsWith('{') || raw.trim().startsWith('['))) {
      try { payload = raw ? JSON.parse(raw) : {}; } catch (_) { payload = raw; }
    }
    if (!response.ok) {
      const message = typeof payload === 'object' && payload?.error ? payload.error : `LoopForge returned HTTP ${response.status}.`;
      const code = typeof payload === 'object' ? payload.code : undefined;
      const suffix = response.status === 401 ? ' Configure a valid API token with “LoopForge: Configure API Token”.' : '';
      throw new LoopForgeError(`${message}${suffix}`, { status: response.status, code, payload });
    }
    return payload;
  }

  listProjects() { return this.request('/api/projects'); }
  listWorkflows() { return this.request('/api/workflows'); }
  listRuns(projectId, maxRuns = 20) {
    const query = new URLSearchParams();
    if (projectId) query.set('projectId', projectId);
    const suffix = query.toString() ? `?${query}` : '';
    return this.request(`/api/runs${suffix}`).then(items => Array.isArray(items) ? items.slice(0, maxRuns) : []);
  }
  createRun({ workflowId, input, projectId }) {
    return this.request('/api/runs', { method: 'POST', body: { workflowId, input, ...(projectId ? { projectId } : {}) } });
  }
  getRun(runId) { return this.request(`/api/runs/${encodeURIComponent(runId)}`); }
  getLogs(runId) { return this.request(`/api/runs/${encodeURIComponent(runId)}/logs?limit=200`); }
  approve(runId, decision) { return this.request(`/api/runs/${encodeURIComponent(runId)}/approve`, { method: 'POST', body: decision }); }
  reject(runId, decision) { return this.request(`/api/runs/${encodeURIComponent(runId)}/reject`, { method: 'POST', body: decision }); }
  getWorkspace(runId) { return this.request(`/api/runs/${encodeURIComponent(runId)}/workspace`); }
  getWorkspaceFile(runId, path) {
    return this.request(`/api/runs/${encodeURIComponent(runId)}/workspace/file?path=${encodeURIComponent(path)}`);
  }
  getDiff(runId, path) {
    const query = path ? `?path=${encodeURIComponent(path)}` : '';
    return this.request(`/api/runs/${encodeURIComponent(runId)}/workspace/diff${query}`);
  }
  getArtifact(runId, name) {
    return this.request(`/api/runs/${encodeURIComponent(runId)}/artifacts/${encodeURIComponent(name)}`, { method: 'GET', parseJson: false });
  }
  artifactUrl(runId, name) { return `${this.baseUrl}/api/runs/${encodeURIComponent(runId)}/artifacts/${encodeURIComponent(name)}`; }
}

module.exports = { LoopForgeClient, LoopForgeError, normalizeBaseUrl };
