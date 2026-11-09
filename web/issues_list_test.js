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
  [{ key: 'clarifying', label: '待澄清' }, 'warn', 'user-hold'],
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

// 责任人头像统一走平台头像组件（真实头像 + 首字回退），不再自绘姓氏。
ctx.userById = id => (Number(id) === 7 ? { id: 7, loginName: 'example-maintainer', chineseName: '示例用户' } : null);
ctx.userAvatarHTML = (user, spec = 100) => {
  const loginName = user && typeof user === 'object' ? user.loginName : String(user ?? '');
  const initial = ((user && user.chineseName) || loginName || '用')[0] || '访';
  return `<img class="user-avatar-img" src="/api/auth/photo/${Number(spec) || 100}/${encodeURIComponent(loginName)}.png" alt=""><span class="user-avatar-text">${ctx.escapeHTML(initial)}</span>`;
};
const ownerAvatar = ctx.issueOwnerAvatar({ assigneeUserId: 7, name: 'example-maintainer(示例用户)' });
assert.ok(ownerAvatar.includes('class="issue-assignee-avatar"'), ownerAvatar);
assert.ok(ownerAvatar.includes('user-avatar-img'), ownerAvatar);
assert.ok(ownerAvatar.includes('/api/auth/photo/48/example-maintainer.png'), ownerAvatar);
assert.ok(ownerAvatar.includes('>示</span>'), ownerAvatar);
assert.ok(ctx.issueOwnerAvatar({ assigneeUserId: 404 }).includes('hui-avatar is-empty'));
assert.ok(ctx.issueOwnerAvatar({ assigneeId: 'a1', assigneeType: 'agent' }).includes('hui-avatar is-agent'));
assert.ok(ctx.issueOwnerAvatar({ assigneeId: 'a1', assigneeType: 'agent' }).includes('data-icon="robot"'));
// 未指定责任人：只给文字，不画占位头像框。
assert.strictEqual(ctx.issueOwnerAvatar({}), '');

// 来源标记：区分工蜂 / TAPD / 平台新建，取值与来源筛选器一致。
ctx.metricsIssueSource = issue => String(issue?.source || issue?.externalRef?.source || '').toLowerCase();
for (const [issue, label, tone] of [
  [{ source: 'gongfeng' }, '工蜂', 'gongfeng'],
  [{ source: 'tapd' }, 'TAPD', 'tapd'],
  [{ source: 'local' }, '平台新建', 'local'],
  [{}, '平台新建', 'local'],
  [{ externalRef: { source: 'tapd' } }, 'TAPD', 'tapd'],
]) {
  assert.strictEqual(ctx.issueSourceLabel(issue), label, JSON.stringify(issue));
  const chip = ctx.issueSourceMarkup(issue);
  assert.ok(chip.includes(`class="issue-source-chip tone-${tone}"`), chip);
  assert.ok(chip.includes(`>${label}</span>`), chip);
}

assert.strictEqual(ctx.issueKeyColumnWidth([{ key: 'AF-36' }]), '76px');
assert.strictEqual(ctx.issueKeyColumnWidth([{ key: 'AF-1036' }]), '96px');
assert.strictEqual(ctx.issueKeyColumnWidth([{ key: '1010000000100012345' }]), '120px');
assert.strictEqual(ctx.issueKeyColumnWidth([{ key: '10100000001000123456789012' }]), '120px');

console.log('issues list markup ok');
