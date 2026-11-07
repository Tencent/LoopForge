// Unit checks for the e2e route coverage calculator:
//   node --test e2e/unit/*.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { collapseAny, computeCoverage, matchRoute, parseArgs } from '../coverage/route-coverage.mjs';

const ANY = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS', 'CONNECT', 'TRACE'];
const routes = [
  { method: 'GET', path: '/api/issues' },
  { method: 'GET', path: '/api/issues/:id/events' },
  { method: 'GET', path: '/api/issues/bulk' },
  { method: 'POST', path: '/api/runs/:id/approve' },
  { method: 'GET', path: '/api/runs/:id/workspace/*path' },
  ...ANY.map(method => ({ method, path: '/api/issues/:id' })),
];

test('Any() routes collapse into one ANY entry', () => {
  const collapsed = collapseAny(routes);
  assert.equal(collapsed.length, 6);
  assert.ok(collapsed.some(r => r.method === 'ANY' && r.path === '/api/issues/:id'));
});

test('static segments win over params, params over wildcards', () => {
  const compiled = collapseAny(routes).map(r => ({ ...r, segs: r.path.split('/').slice(1), key: `${r.method} ${r.path}` }));
  assert.equal(matchRoute(compiled, 'GET', '/api/issues/bulk').path, '/api/issues/bulk');
  assert.equal(matchRoute(compiled, 'GET', '/api/issues/42').key, 'ANY /api/issues/:id');
  assert.equal(matchRoute(compiled, 'GET', '/api/runs/r1/workspace/a/b.txt').path, '/api/runs/:id/workspace/*path');
  assert.equal(matchRoute(compiled, 'GET', '/api/runs/r1/approve'), null);
  assert.equal(matchRoute(compiled, 'GET', '/api/issues?limit=5').path, '/api/issues');
});

test('coverage counts each route once and lists the uncovered ones', () => {
  const log = [
    '2026/10/30 21:17:14 [HTTP] GET /api/issues -> 200 (10 bytes, 1ms)',
    '2026/10/30 21:17:14 [HTTP] GET /api/issues -> 200 (10 bytes, 1ms)',
    '2026/10/30 21:17:15 [HTTP] DELETE /api/issues/7 -> 404 (10 bytes, 1ms)',
    '2026/10/30 21:17:15 [HTTP] POST /api/runs/r1/approve -> 409 (10 bytes, 1ms)',
    '2026/10/30 21:17:15 [HTTP] GET /index.html -> 200 (10 bytes, 1ms)',
    'unrelated line',
  ].join('\n');
  const res = computeCoverage(routes, log);
  assert.equal(res.total, 6);
  assert.equal(res.expandedTotal, routes.length);
  assert.equal(res.covered, 3);
  assert.equal(res.requests, 5);
  assert.equal(res.unmatched, 1);
  assert.deepEqual(res.uncovered, ['GET /api/issues/:id/events', 'GET /api/issues/bulk', 'GET /api/runs/:id/workspace/*path']);
  assert.equal(res.hits['GET /api/issues'].count, 2);
  assert.equal(res.succeeded, 1);
  assert.deepEqual(res.errorOnly, ['ANY /api/issues/:id', 'POST /api/runs/:id/approve']);
});

test('arguments require routes and at least one log', () => {
  assert.throws(() => parseArgs(['--routes', 'r.json']), /required/);
  assert.deepEqual(parseArgs(['--routes', 'r.json', '--log', 'a', '--log', 'b', '--min', '85']).logs, ['a', 'b']);
});
