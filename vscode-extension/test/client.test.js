'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { LoopForgeClient, LoopForgeError, normalizeBaseUrl } = require('../lib/client');

function response(status, body, contentType = 'application/json') {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get(name) { return name.toLowerCase() === 'content-type' ? contentType : ''; } },
    async text() { return typeof body === 'string' ? body : JSON.stringify(body); }
  };
}

test('normalizes and validates server URLs', () => {
  assert.equal(normalizeBaseUrl('http://localhost:8090/'), 'http://localhost:8090');
  assert.throws(() => normalizeBaseUrl(''), /server URL is empty/);
  assert.throws(() => normalizeBaseUrl('file:///tmp/loopforge'), /must use http/);
});

test('allows HTTP only for loopback addresses and requires HTTPS elsewhere', () => {
  for (const url of ['http://localhost:8090', 'http://LOCALHOST:8090', 'http://127.0.0.1:8090', 'http://127.255.255.255:8090', 'http://[::1]:8090', 'http://127.1:8090']) {
    assert.doesNotThrow(() => normalizeBaseUrl(url), url);
  }
  for (const url of ['http://remote.example:8090', 'http://10.0.0.5:8090', 'http://localhost.example:8090', 'http://127.0.0.1.example:8090', 'http://[::ffff:127.0.0.1]:8090']) {
    assert.throws(() => new LoopForgeClient({ baseUrl: url, token: 'secret-token' }), /must use HTTPS for non-loopback hosts/, url);
  }
  assert.equal(normalizeBaseUrl('https://remote.example:8090/'), 'https://remote.example:8090');
});

test('sends bearer token and JSON body to the Runs API', async () => {
  const calls = [];
  const client = new LoopForgeClient({
    baseUrl: 'https://loopforge.example.test/',
    token: 'secret-token',
    fetchImpl: async (url, options) => {
      calls.push({ url, options });
      return response(201, { id: 'run-1', status: 'running' });
    }
  });
  const run = await client.createRun({ workflowId: 'wf-1', input: 'fix it', projectId: 'p-1' });
  assert.equal(run.id, 'run-1');
  assert.equal(calls[0].url, 'https://loopforge.example.test/api/runs');
  assert.equal(calls[0].options.headers.Authorization, 'Bearer secret-token');
  assert.deepEqual(JSON.parse(calls[0].options.body), { workflowId: 'wf-1', input: 'fix it', projectId: 'p-1' });
});

test('converts authentication and connection failures into actionable errors', async () => {
  const unauthorized = new LoopForgeClient({ baseUrl: 'http://localhost:8090', fetchImpl: async () => response(401, { error: 'authentication required' }) });
  await assert.rejects(() => unauthorized.listRuns(), error => error instanceof LoopForgeError && /Configure a valid API token/.test(error.message) && error.status === 401);
  const disconnected = new LoopForgeClient({ baseUrl: 'http://localhost:8090', fetchImpl: async () => { throw new Error('ECONNREFUSED'); } });
  await assert.rejects(() => disconnected.listRuns(), error => error instanceof LoopForgeError && /Cannot connect/.test(error.message) && /server is running/.test(error.message) && /extension host/.test(error.message) && error.code === 'connection');
});

test('encodes run, artifact, and diff paths', async () => {
  const urls = [];
  const client = new LoopForgeClient({ baseUrl: 'http://localhost:8090', fetchImpl: async url => { urls.push(url); return response(200, { content: 'ok' }, 'text/plain'); } });
  await client.getRun('run/1');
  await client.getArtifact('run/1', 'report final.md');
  await client.getDiff('run/1', 'src/a b.go');
  assert.deepEqual(urls, [
    'http://localhost:8090/api/runs/run%2F1',
    'http://localhost:8090/api/runs/run%2F1/artifacts/report%20final.md',
    'http://localhost:8090/api/runs/run%2F1/workspace/diff?path=src%2Fa%20b.go'
  ]);
});

test('keeps plain-text artifact responses intact', async () => {
  const client = new LoopForgeClient({ baseUrl: 'http://localhost:8090', fetchImpl: async () => response(200, 'a report: {not json}', 'text/plain') });
  assert.equal(await client.getArtifact('run-1', 'report.md'), 'a report: {not json}');
});
