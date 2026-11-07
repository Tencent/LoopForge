#!/usr/bin/env node
// e2e API route coverage: registered gin routes (denominator) versus the
// requests the platform logged while the suite ran (numerator).
//
//   node coverage/route-coverage.mjs --routes routes.json --log platform.log [--log more.log]
//        [--min 85] [--json out.json]
//
// routes.json comes from TestRouteInventoryExport (internal/httpapi). The log
// lines are the platform's access log: `[HTTP] METHOD PATH -> CODE (...)`.
// Paths registered with gin's Any() expand to all nine HTTP methods; the handler
// dispatches by method itself, so they count as a single ANY route here (the
// expanded count is printed too).
import fs from 'node:fs';

const HTTP_LINE = /\[HTTP\] ([A-Z]+) (\S+) -> (\d{3})/;
const ANY_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS', 'CONNECT', 'TRACE'];

export function parseArgs(argv) {
  const args = { routes: '', logs: [], min: null, json: '' };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--routes') args.routes = argv[++i];
    else if (a === '--log') args.logs.push(argv[++i]);
    else if (a === '--min') args.min = Number(argv[++i]);
    else if (a === '--json') args.json = argv[++i];
    else throw new Error(`unknown argument: ${a}`);
  }
  if (!args.routes || args.logs.length === 0) throw new Error('--routes and --log are required');
  return args;
}

// collapseAny folds a path registered for all nine methods into one ANY route.
export function collapseAny(routes) {
  const byPath = new Map();
  for (const r of routes) {
    if (!byPath.has(r.path)) byPath.set(r.path, new Set());
    byPath.get(r.path).add(r.method);
  }
  const out = [];
  for (const [path, methods] of byPath) {
    if (ANY_METHODS.every(m => methods.has(m))) out.push({ method: 'ANY', path });
    else for (const m of methods) out.push({ method: m, path });
  }
  return out.sort((a, b) => (a.path === b.path ? a.method.localeCompare(b.method) : a.path.localeCompare(b.path)));
}

function compile(route) {
  const segs = route.path.split('/').slice(1);
  return { ...route, segs, key: `${route.method} ${route.path}` };
}

// matchScore returns null when the request path does not match the route, or a
// score array where static segments outrank :params, which outrank *wildcards,
// mirroring gin's priority.
function matchScore(route, parts) {
  const score = [];
  for (let i = 0; i < route.segs.length; i++) {
    const seg = route.segs[i];
    if (seg.startsWith('*')) {
      score.push(0);
      return score;
    }
    if (i >= parts.length) return null;
    if (seg.startsWith(':')) {
      if (parts[i] === '') return null;
      score.push(1);
    } else if (seg === parts[i]) score.push(2);
    else return null;
  }
  return parts.length === route.segs.length ? score : null;
}

function better(a, b) {
  for (let i = 0; i < Math.min(a.length, b.length); i++) if (a[i] !== b[i]) return a[i] > b[i];
  return a.length > b.length;
}

export function matchRoute(compiled, method, path) {
  const parts = path.split('?')[0].split('/').slice(1);
  let best = null;
  let bestScore = null;
  for (const r of compiled) {
    if (r.method !== method && r.method !== 'ANY') continue;
    const s = matchScore(r, parts);
    if (s && (!best || better(s, bestScore))) {
      best = r;
      bestScore = s;
    }
  }
  return best;
}

export function computeCoverage(routes, logText) {
  const compiled = collapseAny(routes).map(compile);
  const hits = new Map();
  let requests = 0;
  let unmatched = 0;
  for (const line of logText.split('\n')) {
    const m = HTTP_LINE.exec(line);
    if (!m) continue;
    requests++;
    const r = matchRoute(compiled, m[1], m[2]);
    if (!r) {
      unmatched++;
      continue;
    }
    const h = hits.get(r.key) || { count: 0, codes: {} };
    h.count++;
    h.codes[m[3]] = (h.codes[m[3]] || 0) + 1;
    hits.set(r.key, h);
  }
  const covered = compiled.filter(r => hits.has(r.key));
  const uncovered = compiled.filter(r => !hits.has(r.key)).map(r => r.key);
  // A route only counts as successfully exercised when some request got a 2xx
  // (or 3xx for redirects); 4xx/5xx-only routes were reached but not proven to work.
  const succeeded = covered.filter(r => Object.keys(hits.get(r.key).codes).some(c => c < '400'));
  const errorOnly = covered.filter(r => !succeeded.includes(r)).map(r => r.key);
  const total = compiled.length;
  return {
    total,
    covered: covered.length,
    pct: total ? (100 * covered.length) / total : 0,
    succeeded: succeeded.length,
    successPct: total ? (100 * succeeded.length) / total : 0,
    errorOnly,
    expandedTotal: routes.length,
    requests,
    unmatched,
    uncovered,
    hits: Object.fromEntries([...hits].sort()),
  };
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  const routes = JSON.parse(fs.readFileSync(args.routes, 'utf8'));
  const log = args.logs.map(f => fs.readFileSync(f, 'utf8')).join('\n');
  const res = computeCoverage(routes, log);
  console.log(`e2e route coverage: ${res.covered}/${res.total} = ${res.pct.toFixed(1)}% ` +
    `(${res.requests} logged requests, ${res.unmatched} not matching any route; ` +
    `${res.expandedTotal} routes with Any() expanded per method)`);
  console.log(`routes with a 2xx/3xx response: ${res.succeeded}/${res.total} = ${res.successPct.toFixed(1)}%`);
  console.log(`uncovered routes (${res.uncovered.length}):`);
  for (const r of res.uncovered) console.log(`  ${r}`);
  console.log(`routes reached only with 4xx/5xx (${res.errorOnly.length}):`);
  for (const r of res.errorOnly) console.log(`  ${r}`);
  if (args.json) fs.writeFileSync(args.json, JSON.stringify(res, null, 2) + '\n');
  if (args.min != null && res.pct + 1e-9 < args.min) {
    console.error(`FAIL: route coverage ${res.pct.toFixed(1)}% is below ${args.min}%`);
    process.exit(1);
  }
}

if (import.meta.url === `file://${process.argv[1]}`) main();
