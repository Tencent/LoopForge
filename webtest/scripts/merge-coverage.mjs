#!/usr/bin/env node
// Merges the webtest/ unit coverage (istanbul JSON written by c8) with the
// Chromium V8 coverage that the e2e UI specs record (AGENTFLOW_E2E_JS_COVERAGE=1)
// and prints per-file and total line coverage of the web/ scripts loaded by
// web/index.html.
//
//   node scripts/merge-coverage.mjs [--unit coverage/coverage-final.json]
//        [--e2e ../e2e/test-results/web-coverage] [--min 85] [--json out.json]
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import libCoverage from 'istanbul-lib-coverage';
import v8toIstanbul from 'v8-to-istanbul';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..', '..');
const web = path.join(root, 'web');

const args = { unit: path.join(here, '..', 'coverage', 'coverage-final.json'), e2e: path.join(root, 'e2e', 'test-results', 'web-coverage'), min: null, json: '' };
for (let i = 2; i < process.argv.length; i++) {
  const a = process.argv[i];
  if (a === '--unit') args.unit = path.resolve(process.argv[++i]);
  else if (a === '--e2e') args.e2e = path.resolve(process.argv[++i]);
  else if (a === '--min') args.min = Number(process.argv[++i]);
  else if (a === '--json') args.json = path.resolve(process.argv[++i]);
  else throw new Error(`unknown argument: ${a}`);
}

// The scope is exactly the platform scripts web/index.html loads.
const html = fs.readFileSync(path.join(web, 'index.html'), 'utf8');
const scope = [...html.matchAll(/<script[^>]*\bsrc="([^"]+)"/g)]
  .map(m => m[1].split('?')[0])
  .filter(src => src.startsWith('/') && !/^\/(vendor|monaco|vscode-web|extensions|native-chat|chatbot)\//.test(src))
  .map(src => path.join(web, src));
const inScope = new Set(scope);

function emptyFileCoverage(file) {
  return { path: file, statementMap: {}, fnMap: {}, branchMap: {}, s: {}, f: {}, b: {} };
}

async function e2eCoverage() {
  const map = libCoverage.createCoverageMap({});
  if (!fs.existsSync(args.e2e)) return { map, dumps: 0 };
  const dumps = fs.readdirSync(args.e2e).filter(f => f.endsWith('.json'));
  const sources = new Map();
  for (const dump of dumps) {
    for (const entry of JSON.parse(fs.readFileSync(path.join(args.e2e, dump), 'utf8'))) {
      const file = path.join(web, entry.path);
      if (!inScope.has(file)) continue;
      if (!sources.has(file)) sources.set(file, fs.readFileSync(file, 'utf8'));
      const source = sources.get(file);
      if (entry.sourceLength != null && entry.sourceLength !== source.length) {
        console.warn(`skip ${entry.path} in ${dump}: served source differs from the checkout`);
        continue;
      }
      const converter = v8toIstanbul(file, 0, { source });
      await converter.load();
      converter.applyCoverage(entry.functions);
      map.merge(converter.toIstanbul());
    }
  }
  return { map, dumps: dumps.length };
}

function lineStats(map, file) {
  if (!map.files().includes(file)) return { total: 0, covered: 0 };
  const lines = map.fileCoverageFor(file).getLineCoverage();
  const total = Object.keys(lines).length;
  return { total, covered: Object.values(lines).filter(n => n > 0).length };
}

function totals(map) {
  let total = 0;
  let covered = 0;
  for (const file of scope) {
    const s = lineStats(map, file);
    total += s.total;
    covered += s.covered;
  }
  return { total, covered, pct: total ? (100 * covered) / total : 0 };
}

const unit = libCoverage.createCoverageMap(fs.existsSync(args.unit) ? JSON.parse(fs.readFileSync(args.unit, 'utf8')) : {});
const { map: e2e, dumps } = await e2eCoverage();
const merged = libCoverage.createCoverageMap({});
for (const file of scope) merged.addFileCoverage(emptyFileCoverage(file));
merged.merge(unit);
merged.merge(e2e);

// A file that one source never loaded has no line map there; take the line
// set from whichever source has it so every denominator is the same.
const rows = scope.map(file => {
  const m = lineStats(merged, file);
  const u = lineStats(unit, file);
  const e = lineStats(e2e, file);
  const total = Math.max(m.total, u.total, e.total);
  return { file: path.relative(root, file), total, unit: u.covered, e2e: e.covered, merged: m.covered };
});
const sum = key => rows.reduce((n, r) => n + r[key], 0);
const total = sum('total');
const pct = key => (total ? (100 * sum(key)) / total : 0);

console.log(`${'file'.padEnd(36)} ${'lines'.padStart(6)} ${'unit'.padStart(7)} ${'e2e'.padStart(7)} ${'merged'.padStart(7)}`);
for (const r of rows) {
  const p = n => (r.total ? `${((100 * n) / r.total).toFixed(1)}%` : '-').padStart(7);
  console.log(`${r.file.padEnd(36)} ${String(r.total).padStart(6)} ${p(r.unit)} ${p(r.e2e)} ${p(r.merged)}`);
}
console.log(`${'total'.padEnd(36)} ${String(total).padStart(6)} ${pct('unit').toFixed(1).padStart(6)}% ${pct('e2e').toFixed(1).padStart(6)}% ${pct('merged').toFixed(1).padStart(6)}%`);
console.log(`(${dumps} e2e coverage dumps from ${path.relative(root, args.e2e)})`);
if (args.json) {
  fs.writeFileSync(args.json, JSON.stringify({ total, unit: pct('unit'), e2e: pct('e2e'), merged: pct('merged'), files: rows }, null, 2) + '\n');
}
if (args.min != null && pct('merged') + 1e-9 < args.min) {
  console.error(`FAIL: merged web/ line coverage ${pct('merged').toFixed(1)}% is below ${args.min}%`);
  process.exit(1);
}
