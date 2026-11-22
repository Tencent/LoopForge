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

const context = { escapeHTML: (value) => String(value ?? '') };
vm.createContext(context);
for (const name of ['RUN_CLEANUP_DEFAULT_DAYS', 'RUN_CLEANUP_DAY_OPTIONS']) {
  const match = new RegExp('^const ' + name + ' = .*$', 'm').exec(bundle);
  assert(match, name + ' not found in the app bundle');
  vm.runInContext(match[0], context);
}
for (const name of ['formatRunCleanupBytes', 'runCleanupCandidateRowsHTML', 'runCleanupPanelHTML', 'runCleanupHintHTML']) {
  vm.runInContext(source(name), context);
}

assert.equal(context.formatRunCleanupBytes(0), '0 B');
assert.equal(context.formatRunCleanupBytes(512), '512 B');
assert.equal(context.formatRunCleanupBytes(2048), '2 KB');
assert.equal(context.formatRunCleanupBytes(1572864), '1.5 MB');
assert.equal(context.formatRunCleanupBytes(5 * 1024 * 1024 * 1024), '5.0 GB');

assert(context.runCleanupCandidateRowsHTML(null, new Set()).includes('没有超过保留期'));

const view = {
  candidates: [
    { id: 'run-old-1', input: 'assetgraph 资产图谱', workflowName: 'secops-delivery', status: 'succeeded', createdAt: '2026-01-01T10:00:00Z', workspaceBytes: 6.4 * 1024 * 1024 * 1024 },
    { id: 'run-old-2', input: '游戏需求', workflowName: 'game-delivery', status: 'failed', createdAt: '2026-01-02T10:00:00Z', workspaceBytes: 1024 },
  ],
  totals: { candidateCount: 2, workspaceBytes: 6.4 * 1024 * 1024 * 1024 + 1024, diskFreeBytes: 13 * 1024 * 1024 * 1024, diskTotalBytes: 460 * 1024 * 1024 * 1024, retentionDays: 30 },
};
const rows = context.runCleanupCandidateRowsHTML(view, new Set(['run-old-1']));
assert(rows.includes('data-run-cleanup-id="run-old-1"'));
assert(rows.includes('checked'), 'selected row is checked');
assert(rows.includes('assetgraph 资产图谱'));
assert(rows.includes('6.4 GB'));
assert(rows.includes('run-old-2'));

const panel = context.runCleanupPanelHTML({ view, days: 30, selected: new Set(['run-old-1']) });
assert(panel.includes('清理旧运行'));
assert(panel.includes('30 天'));
assert(panel.includes('删除选中（1）'));
assert(!panel.includes('data-run-cleanup-delete disabled'), 'delete is enabled when rows are selected');
assert(panel.includes('超过保留期'));
assert(panel.includes('workspace 占用'));
assert(panel.includes('磁盘剩余'));

const emptyPanel = context.runCleanupPanelHTML({
  view: { candidates: [], totals: { candidateCount: 0, workspaceBytes: 0, diskFreeBytes: 0, diskTotalBytes: 0, retentionDays: 30 } },
  days: 30,
  selected: new Set(),
});
assert(emptyPanel.includes('没有超过保留期'));
assert(emptyPanel.includes('data-run-cleanup-delete disabled'));

assert.equal(context.runCleanupHintHTML({ totals: { candidateCount: 0 } }), '');
const hint = context.runCleanupHintHTML({ totals: { candidateCount: 3, workspaceBytes: 100 * 1024 * 1024, retentionDays: 30 } });
assert(hint.includes('有 <b>3</b> 个超过 30 天'));
assert(hint.includes('100.0 MB'));
assert(hint.includes('data-open-cleanup'));
