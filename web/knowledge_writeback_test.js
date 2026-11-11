'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ctx = {
  escapeHTML: value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])),
  kbBadge: (label, tone) => `<span class="hui-badge" data-tone="${tone}">${label}</span>`,
  document: { addEventListener() {}, getElementById() { return null; } },
  MutationObserver: class { observe() {} },
};
vm.createContext(ctx);
const source = fs.readFileSync(path.join(__dirname, 'knowledge.js'), 'utf8');
vm.runInContext(source, ctx);
assert.match(source, /确认写回 Git 来源/);
assert.match(source, /不支持的平台只推送分支/);

const html = ctx.kbWriteInfoHTML({
  branch: 'kb/writeback-<kbs-1>',
  mrUrl: 'https://github.com/example/repo/pull/7',
  mrStatus: 'mr_open',
  writeNote: '已创建合并请求，合并后会自动同步索引。',
});
assert.match(html, /kb-write-info/);
assert.match(html, /合并请求待合并/);
assert.match(html, /href="https:\/\/github.com\/example\/repo\/pull\/7"/);
assert.match(html, /kb\/writeback-&lt;kbs-1&gt;/);
assert.match(html, /已创建合并请求/);

const unsafe = ctx.kbWriteInfoHTML({ mrUrl: 'javascript:alert(1)', mrStatus: 'merged' });
assert.doesNotMatch(unsafe, /href=/);
assert.match(unsafe, /已合并/);

const empty = ctx.kbWriteInfoHTML({});
assert.equal(empty, '');

const failed = ctx.kbWriteInfoHTML({ mrStatus: 'failed', writeError: '推送被仓库拒绝' });
assert.match(failed, /写回失败/);
assert.match(failed, /推送被仓库拒绝/);

assert.equal(ctx.kbWriteStatus({ mrStatus: 'branch_pushed' })[0], '已推分支');
assert.equal(ctx.kbSafeHref('ftp://example.com/x'), '');
console.log('knowledge writeback ok');
