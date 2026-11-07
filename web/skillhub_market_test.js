const assert = require('assert');
const fs = require('fs');
const vm = require('vm');

// argv[2]: app/skillhub.js, argv[3]: app/assets.js
const skillhubSrc = fs.readFileSync(process.argv[2], 'utf8');
const assetsSrc = fs.readFileSync(process.argv[3], 'utf8');

function fakeEl(id) {
  return { id, hidden: false, value: '', innerHTML: '', textContent: '', classList: { toggle() {}, add() {}, remove() {}, contains: () => false }, setAttribute() {}, querySelector: () => null, querySelectorAll: () => [] };
}
const els = {};
const scopeButtons = { project: fakeEl('project'), shared: fakeEl('shared'), skillhub: fakeEl('skillhub') };
for (const b of Object.values(scopeButtons)) b.dataset = { assetScope: b.id };
const ctx = {
  state: {},
  console,
  URL,
  escapeHTML: v => String(v ?? '').replace(/[&<>'"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[c])),
  huiIcon: name => `<i data-icon="${name}"></i>`,
  catalogOwned: projectId => !!ctx.state.projectFilter && projectId === ctx.state.projectFilter,
  catalogForeign: projectId => !!projectId && projectId !== ctx.state.projectFilter,
  catalogShared: (projectId, shared) => !projectId || (!!shared && projectId !== ctx.state.projectFilter),
  catalogVisible: () => true,
  renderMarkdown: md => `<p>${md}</p>`,
  highlightCode: code => code,
  toast() {},
  api: async () => ({}),
  $: sel => els[sel] || null,
  $$: sel => (sel === '[data-asset-scope="skillhub"]' ? [scopeButtons.skillhub] : sel === '[data-asset-scope]' ? Object.values(scopeButtons) : []),
  document: { activeElement: null, addEventListener() {} },
  window: {},
};
vm.createContext(ctx);
vm.runInContext(assetsSrc, ctx);
vm.runInContext(skillhubSrc, ctx);

function reset(extra = {}) {
  ctx.state = {
    projectFilter: 'p1', assetType: 'skills', skillScope: 'project', ruleScope: 'project', selectedAsset: null, assetQuery: '',
    data: { projects: [{ id: 'p1', name: '本项目' }, { id: 'p2', name: '数据中台' }], agents: [], skills: [] },
    ...extra,
  };
}

// ---------- TRACE total ----------
const dims = {
  trust: { items: { scan: { score: 4.5 }, domestic: { score: 4.0 } } },
  reliability: { items: { stability: { score: 4.4 }, func: { score: null }, errorHandling: { score: '3.9' } } },
  adaptability: { items: { boundary: { score: 4.7 }, trigger: { score: 4.9 } } },
  convention: { items: { a: { score: null } } },
};
assert.strictEqual(ctx.skillhubDimensionScore(dims.trust), 4.25);
assert.ok(Math.abs(ctx.skillhubDimensionScore(dims.reliability) - 4.15) < 1e-9, 'null item scores are skipped');
assert.strictEqual(ctx.skillhubDimensionScore(dims.convention), null, 'all-null dimension has no score');
// (4.25 + 4.15 + 4.8) / 3 = 4.4; convention (all null) and effectiveness (missing) are left out.
assert.strictEqual(ctx.skillhubTraceScore(dims), 4.4);
assert.strictEqual(ctx.skillhubTraceScore({}), null);
assert.strictEqual(ctx.skillhubTraceScore(null), null);
assert.strictEqual(ctx.skillhubTraceScore({ trust: { items: { x: { score: 3 } } }, effectiveness: { items: { y: { score: 4 }, z: { score: 4.2 } } } }), 3.6);
const trace = ctx.skillhubTraceHTML({ available: true, dimensions: dims, userSummary: '<b>整体良好</b>' }, 'trust');
assert.ok(trace.includes('<b>4.4</b>') && trace.includes('&lt;b&gt;整体良好'), 'score shown, summary escaped');
assert.ok(trace.includes('安全扫描') && trace.includes('data-trace-dim="trust"') && trace.includes('is-open'), trace);
assert.ok(!trace.includes('data-trace-dim="effectiveness"'), 'missing dimension is not drawn');
assert.ok(ctx.skillhubTraceHTML({ available: false }).includes('暂无质量评测'));

// ---------- Installed / update state is per project ----------
reset();
const src = (slug, version, namespace = 'team') => ({ provider: 'skillhub', slug, version, namespace });
ctx.state.data.skills = [
  { id: 'pdf', projectId: 'p1', source: src('pdf-tools', '1.0.0') },
  { id: 'review', projectId: 'p2', shared: true, source: src('code-review', '2.0.0') },
  { id: 'plain', projectId: 'p1' },
  { id: 'other-ns', projectId: 'p1', source: src('find-skills', '1.0.0', 'indiv-a') },
];
assert.strictEqual(ctx.skillhubCardState({ slug: 'pdf-tools', namespace: 'team', version: '1.3.0' }).state, 'update');
assert.strictEqual(ctx.skillhubCardState({ slug: 'pdf-tools', namespace: 'team', version: '1.0.0' }).state, 'installed');
assert.strictEqual(ctx.skillhubCardState({ slug: 'pdf-tools', namespace: '', version: '1.0.0' }).state, 'installed', 'empty namespace matches');
assert.strictEqual(ctx.skillhubCardState({ slug: 'code-review', namespace: 'team', version: '2.0.0' }).state, 'none', 'another project install does not count');
assert.strictEqual(ctx.skillhubCardState({ slug: 'find-skills', namespace: 'org-b', version: '1.0.0' }).state, 'none', 'same slug in another namespace is a different skill');
assert.strictEqual(ctx.skillhubCardState({ slug: 'plain', version: '1' }).state, 'none', 'hand-made skills never match');
ctx.state.projectFilter = 'p2';
assert.strictEqual(ctx.skillhubCardState({ slug: 'code-review', namespace: 'team', version: '2.0.0' }).state, 'installed');
assert.strictEqual(ctx.skillhubCardState({ slug: 'pdf-tools', namespace: 'team', version: '1.0.0' }).state, 'none');

// ---------- Card HTML: escaping and actions ----------
reset();
const evil = { slug: 'x"><img src=x onerror=alert(1)>', name: '<script>alert(1)</script>', description: '<img src=x>', version: '1.0.0', ownerName: '<b>o</b>', downloads: 98000, stars: 412, labels: { requires_api_key: 'true' } };
let card = ctx.skillhubCardHTML(evil, 0);
assert.ok(!/<script|<img|<b>/.test(card), card);
assert.ok(card.includes('&lt;script&gt;') && card.includes('9.8 万') && card.includes('需要 API Key'), card);
assert.ok(card.includes('data-skillhub-install="0"') && !card.includes('disabled'), 'install enabled with a project');
ctx.state.projectFilter = '';
card = ctx.skillhubCardHTML({ slug: 'a', name: 'A', version: '1' }, 3);
assert.ok(card.includes('data-skillhub-install="3" disabled title="请先选择项目"'), card);
reset();
ctx.state.data.skills = [{ id: 'pdf', projectId: 'p1', source: src('pdf-tools', '1.0.0') }];
card = ctx.skillhubCardHTML({ slug: 'pdf-tools', namespace: 'team', name: 'PDF', version: '1.3.0' }, 1);
assert.ok(card.includes('data-tone="warn"') && card.includes('hui-lamp') && card.includes('有新版本 v1.3.0') && card.includes('data-skillhub-update="1"'), card);
card = ctx.skillhubCardHTML({ slug: 'pdf-tools', namespace: 'team', name: 'PDF', version: '1.0.0' }, 1);
assert.ok(card.includes('data-tone="success"') && card.includes('已安装 v1.0.0') && !card.includes('<button'), card);

// ---------- Security reports degrade ----------
assert.ok(ctx.skillhubSecurityHTML(null).includes('暂无安全认证信息'));
assert.ok(ctx.skillhubSecurityHTML({}).includes('暂无安全认证信息'));
const sec = ctx.skillhubSecurityHTML({
  keen: { status: 'benign', statusText: '安全，无风险', reportUrl: 'https://r.example/1', safe: true },
  other: { status: 'suspicious', statusText: '<i>可疑</i>', reportUrl: 'javascript:alert(1)', safe: false },
});
assert.ok(sec.includes('data-tone="success"') && sec.includes('href="https://r.example/1"'), sec);
assert.ok(sec.includes('data-tone="warn"') && sec.includes('&lt;i&gt;可疑') && !sec.includes('javascript:'), sec);
assert.ok(ctx.skillhubSecuritySummary({ keen: { safe: true }, sanbu: { safe: true } }).includes('2 家通过'));
assert.ok(ctx.skillhubSecuritySummary({ keen: { safe: false, status: 'malicious' } }).includes('1 家提示风险'));

// ---------- Install id hints ----------
reset();
ctx.state.data.skills = [{ id: 'mine', projectId: 'p1' }, { id: 'theirs', projectId: 'p2', shared: true }];
assert.strictEqual(ctx.skillhubIdProblem('fresh-id'), '');
assert.ok(ctx.skillhubIdProblem('mine').includes('本项目'));
assert.ok(ctx.skillhubIdProblem('theirs').includes('项目「数据中台」'));
assert.ok(ctx.skillhubIdProblem('../x').includes('只能包含'));
assert.ok(ctx.skillhubIdProblem('  ').includes('请填写'));

// ---------- Small helpers ----------
assert.deepStrictEqual({ ...ctx.skillhubSplitFrontMatter('---\nname: a\n---\n# Hi') }, { front: 'name: a', body: '# Hi' });
assert.deepStrictEqual({ ...ctx.skillhubSplitFrontMatter('# Hi') }, { front: '', body: '# Hi' });
assert.deepStrictEqual([...ctx.skillhubPageList(1, 521)], [1, 2, '…', 521]);
assert.deepStrictEqual([...ctx.skillhubPageList(5, 9)], [1, '…', 4, 5, 6, '…', 9]);
assert.strictEqual(ctx.skillhubDate(1700000000000), ctx.skillhubDate(1700000000), 'seconds and milliseconds both parse');
assert.strictEqual(ctx.skillhubSafeURL('javascript:alert(1)'), '');
assert.strictEqual(ctx.skillhubSubCategoryName({ key: 'agent-workflow', name: '工作流编排' }), '工作流编排', 'subCategories arrive as {key,name}');
assert.strictEqual(ctx.skillhubSubCategoryName('plain'), 'plain');
assert.ok(ctx.skillhubFileWarnings([{ path: 'a.bin', size: 2 * 1024 * 1024 }])[0].includes('a.bin'));
assert.strictEqual(ctx.skillhubFileWarnings([{ path: 'SKILL.md', size: 10 }]).length, 0);

// ---------- Third tab only on the skills page ----------
for (const id of ['#skillhubMarket', '#assetCount', '#assetList', '#assetSearchBox']) els[id] = fakeEl(id);
reset({ assetType: 'rules' });
ctx.skillhubSyncChrome();
assert.strictEqual(scopeButtons.skillhub.hidden, true, 'rules page hides the SkillHub tab');
assert.strictEqual(els['#skillhubMarket'].hidden, true);
reset({ skillScope: 'project' });
ctx.skillhubSyncChrome();
assert.strictEqual(scopeButtons.skillhub.hidden, false, 'skills page shows the SkillHub tab');
assert.strictEqual(els['#skillhubMarket'].hidden, true, 'market stays hidden on the project scope');
assert.strictEqual(els['#assetList'].hidden, false);
reset({ skillScope: 'skillhub' });
assert.strictEqual(ctx.skillhubSyncChrome(), true);
assert.strictEqual(els['#skillhubMarket'].hidden, false);
assert.ok(els['#assetList'].hidden && els['#assetCount'].hidden && els['#assetSearchBox'].hidden, 'local list and search give way to the market');
reset({ skillScope: 'skillhub', selectedAsset: 'pdf' });
assert.strictEqual(ctx.skillhubSyncChrome(), false, 'an open skill detail hides the market');

// ---------- assets.js: SkillHub-sourced project skills ----------
reset();
const sourced = { id: 'pdf', name: 'PDF', projectId: 'p1', fileRefs: [{ path: 'SKILL.md', size: 3 }], source: { provider: 'skillhub', slug: 'pdf-tools', namespace: 'team', version: '1.0.0', homepage: 'https://skillhub.cn/skills/team/pdf-tools', installedAt: '2026-10-01T00:00:00Z', updatedAt: '2026-10-01T00:00:00Z' } };
let meta = ctx.assetMetaHTML(sourced);
assert.ok(meta.includes('data-tone="accent"') && meta.includes('SkillHub v1.0.0'), meta);
ctx.skillhubState().latest['team/pdf-tools'] = '1.3.0';
meta = ctx.assetMetaHTML(sourced);
assert.ok(meta.includes('data-tone="warn"') && meta.includes('可更新'), meta);
const overview = ctx.assetOverviewCardHTML(sourced);
assert.ok(overview.includes('SkillHub · pdf-tools') && overview.includes('href="https://skillhub.cn/skills/team/pdf-tools"') && overview.includes('data-skillhub-source-update') && overview.includes('更新到 v1.3.0'), overview);
delete ctx.skillhubState().latest['team/pdf-tools'];
assert.ok(ctx.assetOverviewCardHTML(sourced).includes('data-skillhub-source-check'), 'check button when no newer version is known');
assert.ok(!ctx.assetOverviewCardHTML({ ...sourced, projectId: 'p2', shared: true }).includes('data-skillhub-source'), 'no update action on a shared copy');
const panes = ctx.assetMainPanesHTML(sourced, { creating: false, readonly: false });
assert.ok(panes.includes('文件来自 SkillHub v1.0.0，只读'), panes);
assert.ok(/class="skill-upload-actions" hidden/.test(panes), 'upload buttons hidden');
assert.ok(!panes.includes('data-skill-file-del'), 'no delete buttons');
assert.ok(!panes.includes('每次上传都是'), 'upload help hidden');
assert.ok(panes.includes('data-skill-file-preview="SKILL.md"'), 'preview stays available');
const plain = ctx.assetMainPanesHTML({ id: 'p', projectId: 'p1', fileRefs: [{ path: 'SKILL.md', size: 3 }] }, { creating: false, readonly: false });
assert.ok(plain.includes('data-skill-file-del') && !plain.includes('只读；'), 'hand-made skills keep editing');
assert.ok(!ctx.assetMetaHTML({ id: 'p', projectId: 'p1' }).includes('SkillHub'));
ctx.state.assetType = 'rules';
assert.strictEqual(ctx.assetSkillSource(sourced), null, 'source badge is skill-only');

console.log('skillhub market ok');
