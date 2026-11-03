const assert = require('assert');
const fs = require('fs');
const vm = require('vm');

const src = fs.readFileSync(process.argv[2], 'utf8');
function slice(from, to) {
  const start = src.indexOf(from);
  assert.ok(start >= 0, `${from} not found`);
  const end = src.indexOf(to, start);
  assert.ok(end > start, `${to} not found after ${from}`);
  return src.slice(start, end);
}

const ctx = {
  escapeHTML: v => String(v ?? '').replace(/[&<>'"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[c])),
  issuePriorityLabel: v => ({ urgent: '紧急', high: '高', medium: '中', low: '低' }[v] || v),
  issueAssigneeName: i => i.name || '',
  issueKeyLabel: i => i.key,
};
vm.createContext(ctx);
vm.runInContext(slice('function issueStatusIcon(', 'function issueStatusText('), ctx);
vm.runInContext(slice('function issueKeyColumnWidth(', 'function issueTagTone('), ctx);

const tones = [
  [{ key: 'active', label: '编码实现' }, 'info', 'lamp'],
  [{ key: 'active', label: '排队中' }, 'info', 'clock'],
  [{ key: 'waiting', label: '方案设计' }, 'warn', 'user-hold'],
  [{ key: 'confirm', label: '待确认结论 · 82 分' }, 'warn', 'pending-review'],
  [{ key: 'attention', label: '待澄清 · 47 分' }, 'warn', 'user-hold'],
  [{ key: 'attention', label: '失败 · 编码实现' }, 'error', 'alert'],
  [{ key: 'attention', label: '已取消 · 方案设计' }, 'error', 'alert'],
  [{ key: 'closed', label: 'MR 已合入' }, 'success', 'check'],
  [{ key: 'closed', label: '交付闭环' }, 'success', 'check'],
  [{ key: 'closed', label: '已完成' }, 'success', 'check'],
  [{ key: 'closed', label: '已合入基线' }, 'success', 'check'],
  [{ key: 'closed', label: '已关闭' }, 'neutral', 'close'],
  [{ key: 'closed', label: 'MR 已关闭' }, 'neutral', 'close'],
  [{ key: 'closed', label: '工蜂已关闭' }, 'neutral', 'close'],
  [{ key: 'not_started', label: '等待执行' }, 'neutral', 'circle'],
  [undefined, 'neutral', 'circle'],
];
for (const [stage, tone, icon] of tones) {
  assert.deepStrictEqual({ ...ctx.issueStageTone(stage) }, { tone, icon }, `stage ${JSON.stringify(stage)}`);
}

const running = ctx.issueStageBadge({ key: 'active', label: '编码实现' });
assert.match(running, /^<span class="hui-badge" data-tone="info" data-stage-icon="lamp"><span class="hui-lamp"/);
assert.ok(!running.includes('data-icon='), 'running badge uses the breathing lamp instead of an icon');
const failed = ctx.issueStageBadge({ key: 'attention', label: '失败 · <b>x</b>' });
assert.ok(failed.includes('class="hui-badge" data-tone="error"'));
assert.ok(failed.includes('data-icon="alert"'));
assert.ok(failed.includes('<span class="hui-badge-label">失败 · &lt;b&gt;x&lt;/b&gt;</span>'), failed);

assert.ok(ctx.huiIcon('search').startsWith('<svg class="agent-icon harness-icon" data-icon="search" width="14" height="14"'));
assert.ok(ctx.huiIcon('edit', 'popover-icon').startsWith('<svg class="agent-icon harness-icon popover-icon" data-icon="edit"'));
assert.ok(ctx.huiIcon('x"><img>').includes('data-icon="x&quot;&gt;&lt;img&gt;"'));

for (const [pri, icon] of [['urgent', 'priority-bolt'], ['high', 'priority-high'], ['medium', 'priority-medium'], ['low', 'priority-low'], ['', 'priority-medium']]) {
  const html = ctx.issuePriorityMarkup(pri);
  assert.ok(html.includes(`class="issue-priority ${pri || 'medium'}"`), html);
  assert.ok(html.includes(`data-icon="${icon}"`), html);
}

assert.ok(ctx.issueOwnerAvatar({ name: 'example-maintainer(示例用户)' }).includes('>示</span>'));
assert.ok(ctx.issueOwnerAvatar({ name: 'river' }).includes('>R</span>'));
assert.ok(ctx.issueOwnerAvatar({ name: '<x>' }).includes('>&lt;</span>'));
assert.ok(ctx.issueOwnerAvatar({ assigneeId: 'a1', assigneeType: 'agent' }).includes('hui-avatar is-agent'));
assert.ok(ctx.issueOwnerAvatar({ assigneeId: 'a1', assigneeType: 'agent' }).includes('data-icon="robot"'));
assert.ok(ctx.issueOwnerAvatar({}).includes('hui-avatar is-empty'));

assert.strictEqual(ctx.issueKeyColumnWidth([{ key: 'AF-36' }]), '76px');
assert.strictEqual(ctx.issueKeyColumnWidth([{ key: 'AF-1036' }]), '96px');
assert.strictEqual(ctx.issueKeyColumnWidth([{ key: '1010000000100012345' }]), '120px');
assert.strictEqual(ctx.issueKeyColumnWidth([{ key: '10100000001000123456789012' }]), '120px');

console.log('issues list markup ok');
