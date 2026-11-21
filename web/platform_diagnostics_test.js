const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const bundle = fs.readFileSync(process.argv[2], 'utf8');

function source(name) {
  const match = new RegExp('^(?:async )?function ' + name + '\\(', 'm').exec(bundle);
  assert(match, name + ' not found in the app bundle');
  const tail = bundle.slice(match.index);
  const next = /\n(?:async )?function |\n(?:let|const) [A-Za-z]|\n\/\/ /.exec(tail);
  return next ? tail.slice(0, next.index) : tail;
}

const context = {
  lfT: (zh) => zh,
  escapeHTML: (value) => String(value ?? ''),
  platformDiagnosticsEndpoint: '/api/platform/diagnostics',
  platformDiagnosticCategoryOrder: ['database', 'artifacts', 'browser', 'runtime'],
  platformDiagnosticCategoryLabels: {
    database: ['数据库', 'Database'],
    artifacts: ['产物', 'Artifacts'],
    browser: ['Agent 浏览器', 'Agent browser'],
    runtime: ['执行环境', 'Runtime'],
  },
  platformDiagnosticStatusLabels: {
    ok: ['正常', 'OK'],
    warn: ['需要处理', 'Action needed'],
    error: ['错误', 'Error'],
    info: ['说明', 'Info'],
  },
};
vm.createContext(context);
for (const name of ['platformDiagnosticStatusClass', 'platformDiagnosticCounts', 'platformDiagnosticCheckHTML', 'platformDiagnosticGroupHTML', 'platformDiagnosticsHTML', 'platformDiagnosticsRender']) {
  vm.runInContext(source(name), context);
}

assert.equal(context.platformDiagnosticStatusClass('ok'), 'is-ok');
assert.equal(context.platformDiagnosticStatusClass('warn'), 'is-warn');
assert.equal(context.platformDiagnosticStatusClass('error'), 'is-error');
assert.equal(context.platformDiagnosticStatusClass('weird'), 'is-info');

const counts = context.platformDiagnosticCounts([
  { status: 'ok' }, { status: 'warn' }, { status: 'warn' }, { status: 'error' }, { status: 'other' },
]);
assert.deepEqual(JSON.parse(JSON.stringify(counts)), { ok: 1, warn: 2, error: 1, info: 1 });

const panel = context.platformDiagnosticsHTML();
assert(panel.includes('platformDiagnosticsCard'), 'panel card id');
assert(panel.includes('运维诊断'), 'panel title');
assert(panel.includes('data-diagnostics-refresh'), 'refresh button');

const check = {
  id: 'database.artifact_content',
  category: 'database',
  status: 'warn',
  title: '产物正文列容量不足',
  detail: 'run_artifacts.content 为 TEXT',
  hint: '把该列手动扩宽为 MEDIUMTEXT',
  command: 'docker compose exec platform /app/bin/agentflow -browser-image',
};
const item = context.platformDiagnosticCheckHTML(check);
assert(item.includes('is-warn'), 'warn status class');
assert(item.includes('产物正文列容量不足'), 'title rendered');
assert(item.includes('data-diagnostic-command'), 'command data attribute');
assert(item.includes('docker compose exec platform'), 'command rendered');

const rendered = context.platformDiagnosticsRender({
  generatedAt: '2026-01-01T00:00:00Z',
  checks: [check, { id: 'database.connection', category: 'database', status: 'ok', title: '数据库连接正常' }],
});
assert(rendered.summaryHTML.includes('1'), 'summary mentions the warning count');
assert(rendered.bodyHTML.includes('数据库'), 'checks grouped under the database heading');
assert(!rendered.bodyHTML.includes('产物上限'), 'unknown categories stay out of the fixed groups');
assert(rendered.bodyHTML.includes('database.artifact_content'), 'check rendered');
