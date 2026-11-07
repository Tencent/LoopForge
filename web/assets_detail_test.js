const assert = require('assert');
const fs = require('fs');
const vm = require('vm');

// argv[2]: app/assets.js, argv[3]: app.js
const assetsSrc = fs.readFileSync(process.argv[2], 'utf8');
const appSrc = fs.readFileSync(process.argv[3], 'utf8');
function slice(src, from, to) {
  const start = src.indexOf(from);
  assert.ok(start >= 0, `${from} not found`);
  const end = src.indexOf(to, start);
  assert.ok(end > start, `${to} not found after ${from}`);
  return src.slice(start, end);
}

const calls = [];
const ctx = {
  state: {},
  location: { hash: '' },
  history: { state: null, pushState() {}, replaceState() {}, back() {} },
  escapeHTML: v => String(v ?? '').replace(/[&<>'"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[c])),
  huiIcon: name => `<i data-icon="${name}"></i>`,
  catalogOwned: projectId => !!ctx.state.projectFilter && projectId === ctx.state.projectFilter,
  catalogForeign: projectId => !!projectId && projectId !== ctx.state.projectFilter,
  catalogShared: (projectId, shared) => !projectId || (!!shared && projectId !== ctx.state.projectFilter),
  catalogVisible: (projectId, shared) => ctx.catalogOwned(projectId) || ctx.catalogShared(projectId, shared),
  catalogGeneric: () => false,
  runnableWorkflowOptions: () => [],
  renderMarkdown: md => `<p>${md}</p>`,
  lines: v => String(v || '').split('\n').map(x => x.trim()).filter(Boolean),
  toast: msg => calls.push(['toast', msg]),
  api: async (url, opts) => { calls.push(['api', url, opts]); return {}; },
  afConfirm: async () => true,
  activeViewName: () => ctx.state.assetType,
  $: () => null,
  $$: () => [],
  renderWorkflowMeta() {},
  renderProperties() {},
  renderAgentAssetsPanel() {},
};
vm.createContext(ctx);
vm.runInContext(assetsSrc, ctx);
vm.runInContext(slice(appSrc, 'function routePart(', 'function runRouteId('), ctx);
vm.runInContext(slice(appSrc, 'function assetRouteId(', 'async function applyAgentRoute('), ctx);

function reset(type, data) {
  ctx.state = {
    projectFilter: 'p1',
    assetType: type,
    selectedAsset: null,
    assetQuery: '',
    ruleScope: 'project', skillScope: 'project', dockerfileScope: 'project', environmentScope: 'project', sandboxScope: 'project',
    data: { projects: [{ id: 'p1', name: '本项目' }, { id: 'p2', name: '平台公共' }], agents: [], sandboxes: [], workflows: [], rules: [], skills: [], dockerfiles: [], environments: [], ...data },
  };
}

// ---------- Cards: three share states, no actions on the card ----------
reset('rules', {
  rules: [
    { id: 'core', name: 'LoopForge 核心规则', description: '通用纪律', projectId: 'p1', shared: false, priority: 110, global: true, workflowIds: ['a', 'b'] },
    { id: 'go-sql', name: 'Go/SQL', description: '', projectId: 'p1', shared: true, priority: 100 },
    { id: 'tapd', name: 'TAPD 同步', description: '回写约定', projectId: 'p2', shared: true, priority: 80 },
  ],
  agents: [{ id: 'clarify', name: '需求澄清', rules: ['core'] }],
});
const own = ctx.assetCardHTML(ctx.state.data.rules[0]);
assert.ok(own.includes('class="asset-card" role="link" tabindex="0" data-asset-id="core"'), own);
assert.ok(own.includes('<span class="asset-status "><i></i>未共享</span>'), own);
assert.ok(own.includes('编辑<i data-icon="chevron-right">'), own);
assert.ok(own.includes('data-icon="gate"'), 'rules use the gate icon');
assert.ok(own.includes('优先级 110') && own.includes('全局 · 2 个 SOP') && own.includes('1 个角色引用'), own);
const shared = ctx.assetCardHTML(ctx.state.data.rules[1]);
assert.ok(shared.includes('<span class="asset-status is-on"><i></i>已共享</span>'), shared);
assert.ok(shared.includes('暂无说明') && shared.includes('未被引用'), shared);
const foreign = ctx.assetCardHTML(ctx.state.data.rules[2]);
assert.ok(foreign.includes('<span class="asset-status is-from"><i></i>来自 平台公共</span>'), foreign);
assert.ok(foreign.includes('查看<i data-icon="chevron-right">'), foreign);
for (const html of [own, shared, foreign]) {
  assert.ok(!/<button/.test(html), 'cards carry no buttons');
  assert.ok(!/data-(share|delete)-asset/.test(html), 'share/delete moved off the card');
  assert.ok(!/class="[^"]*\bopen\b/.test(html), 'card classes must not collide with .open menu state');
}
for (const [type, icon] of [['rules', 'gate'], ['skills', 'skills'], ['dockerfiles', 'layers'], ['environments', 'terminal'], ['sandboxes', 'server']]) {
  assert.strictEqual(ctx.assetIconName(type), icon);
}

// ---------- Search filter and scope counts ----------
assert.deepStrictEqual(ctx.assetFilteredItems().map(a => a.id), ['core', 'go-sql']);
ctx.state.assetQuery = 'GO';
assert.deepStrictEqual(ctx.assetFilteredItems().map(a => a.id), ['go-sql']);
ctx.state.assetQuery = '纪律';
assert.deepStrictEqual(ctx.assetFilteredItems().map(a => a.id), ['core'], 'description matches');
ctx.state.assetQuery = 'zzz';
assert.strictEqual(ctx.assetFilteredItems().length, 0);
assert.ok(ctx.assetGridEmptyHTML().includes('没有匹配「zzz」的规则'), 'search empty state wins');
ctx.state.assetQuery = '';
assert.ok(ctx.assetGridEmptyHTML().includes('当前项目还没有规则') && ctx.assetGridEmptyHTML().includes('data-empty-new'));
ctx.state.ruleScope = 'shared';
assert.deepStrictEqual(ctx.assetFilteredItems().map(a => a.id), ['tapd']);
assert.ok(ctx.assetGridEmptyHTML().includes('其他项目还没有共享规则'));
assert.ok(!ctx.assetGridEmptyHTML().includes('data-empty-new'), 'no create in shared scope');
assert.strictEqual(ctx.assetScopeItems('project').length, 2);
assert.strictEqual(ctx.assetScopeItems('shared').length, 1);
assert.deepStrictEqual({ ...ctx.assetFindVisible('tapd') }.scope, 'shared');
assert.deepStrictEqual({ ...ctx.assetFindVisible('core') }.scope, 'project');
assert.strictEqual(ctx.assetFindVisible('missing'), null);
ctx.state.ruleScope = 'project';

// ---------- Detail head: owned, new, read-only, delete blocked ----------
const buttons = html => (html.match(/data-asset-(back|cancel|share|delete|save)\b/g) || []).map(s => s.replace('data-asset-', ''));
let head = ctx.assetDetailHeadHTML({ id: 'plain', name: 'Plain', projectId: 'p1', shared: false }, {});
assert.deepStrictEqual(buttons(head), ['back', 'share', 'delete', 'save']);
assert.ok(head.includes('共享给其他项目') && head.includes('未共享') && head.includes('删除规则'));
assert.ok(head.includes('data-asset-dirty hidden'), 'dirty hint hidden until edited');
assert.ok(head.includes('type="submit" form="assetForm"'), 'head save submits the persistent form');
assert.ok(!head.includes('data-asset-delete disabled'), 'unblocked delete stays enabled');
assert.ok(ctx.assetDetailHeadHTML({ id: 'plain', name: 'Plain', projectId: 'p1' }, { dirty: true }).includes('data-asset-dirty >'));

head = ctx.assetDetailHeadHTML({ id: 'go-sql', name: 'Go', projectId: 'p1', shared: true }, {});
assert.ok(head.includes('取消共享') && head.includes('asset-badge is-on">已共享'));
assert.ok(head.includes('data-asset-delete disabled aria-disabled="true"'));
assert.ok(head.includes('共享状态的规则不能删除，请先取消共享'), 'shared reason is written out');

head = ctx.assetDetailHeadHTML(ctx.state.data.rules[0], {});
assert.ok(head.includes('data-asset-delete disabled') && head.includes('仍被 1 个角色引用（需求澄清）'), head);

reset('sandboxes', { sandboxes: [{ id: 'generic-docker', name: '通用 Docker', projectId: 'p1', driver: 'docker' }] });
head = ctx.assetDetailHeadHTML(ctx.state.data.sandboxes[0], {});
assert.ok(head.includes('内置沙箱 generic-docker 不能删除'), 'builtin reason');
assert.ok(ctx.assetOverviewCardHTML(ctx.state.data.sandboxes[0]).includes('<dt>类型</dt><dd>内置</dd>'));

reset('skills', {});
ctx.state.selectedAsset = '__new__';
head = ctx.assetDetailHeadHTML({ id: '', name: '' }, { creating: true });
assert.deepStrictEqual(buttons(head), ['back', 'cancel', 'save']);
assert.ok(head.includes('新建技能') && head.includes('创建技能') && head.includes('创建后即可上传文件'));
assert.ok(!head.includes('asset-badge'), 'no status badge while creating');

reset('rules', { rules: [{ id: 'tapd', name: 'TAPD', projectId: 'p2', shared: true }] });
head = ctx.assetDetailHeadHTML(ctx.state.data.rules[0], {});
assert.deepStrictEqual(buttons(head), ['back']);
assert.ok(head.includes('来自 平台公共') && head.includes('只读 · 可直接在本项目使用'));

// ---------- Routes ----------
const route = hash => { ctx.location.hash = hash; return ctx.assetRouteId(); };
assert.strictEqual(route('#/rules/a%2Fb'), 'a/b');
assert.strictEqual(route('#/rules'), '');
assert.strictEqual(route('#/agents/x'), '');
assert.strictEqual(route('#/sandboxes/new'), 'new');
assert.strictEqual(ctx.assetViewHash('rules', 'a/b'), '#/rules/a%2Fb');
assert.strictEqual(ctx.assetViewHash('skills', '__new__'), '#/skills/new');
assert.strictEqual(ctx.assetViewHash('skills'), '#/skills');

// ---------- Form fields and save payloads per type ----------
function fakeForm(values) {
  const elements = {};
  for (const [name, value] of Object.entries(values)) elements[name] = typeof value === 'boolean' ? { checked: value, value: '' } : { value };
  return { elements, querySelectorAll: () => [], querySelector: () => null };
}
function renderedFieldNames(type, asset) {
  const form = { innerHTML: '', elements: { driver: { value: asset.driver || 'local', form: null } }, querySelectorAll: () => [], querySelector: () => null };
  const saved$ = ctx.$;
  ctx.$ = sel => (sel === '#assetForm' ? form : null);
  ctx.state.selectedAsset = asset.id;
  ctx.renderAssetForm(asset);
  ctx.$ = saved$;
  return [...form.innerHTML.matchAll(/ name="([^"]+)"/g)].map(m => m[1]).sort();
}
async function save(type, existing, values) {
  reset(type, { [type]: [existing] });
  ctx.state.selectedAsset = existing.id;
  calls.length = 0;
  await ctx.saveAsset({ preventDefault() {}, currentTarget: fakeForm(values) });
  const put = calls.find(c => c[0] === 'api');
  assert.ok(put, `${type} save issued no request: ${JSON.stringify(calls)}`);
  assert.strictEqual(put[1], `/api/${type}`);
  return JSON.parse(put[2].body);
}

(async () => {
  const base = { name: 'N', description: 'D' };
  const cases = [
    ['rules', { id: 'r', projectId: 'p1', shared: true, priority: 100, content: 'x' },
      { id: 'r', ...base, priority: '120', content: '# hi', global: false },
      { id: 'r', name: 'N', description: 'D', projectId: 'p1', shared: true, priority: 120, content: '# hi', global: false, workflowIds: [] },
      ['content', 'description', 'global', 'id', 'name', 'priority']],
    ['skills', { id: 's', projectId: 'p1', fileRefs: [{ path: 'SKILL.md', size: 3 }], content: 'c', files: {} },
      { id: 's', ...base },
      { id: 's', name: 'N', description: 'D', projectId: 'p1', shared: false },
      ['description', 'id', 'name']],
    ['dockerfiles', { id: 'd', projectId: 'p1' },
      { id: 'd', ...base, content: 'FROM x', files: '{"a":"b"}' },
      { id: 'd', name: 'N', description: 'D', projectId: 'p1', shared: false, content: 'FROM x', files: { a: 'b' } },
      ['content', 'description', 'files', 'id', 'name']],
    ['environments', { id: 'e', projectId: 'p1' },
      { id: 'e', ...base, variables: '{"V":"1"}', pathEntries: 'bin\n sbin ', installScript: 'echo' },
      { id: 'e', name: 'N', description: 'D', projectId: 'p1', shared: false, variables: { V: '1' }, pathEntries: ['bin', 'sbin'], installScript: 'echo' },
      ['description', 'id', 'installScript', 'name', 'pathEntries', 'variables']],
    ['sandboxes', { id: 'x', projectId: 'p1', driver: 'docker' },
      { id: 'x', ...base, driver: 'docker', environment: '{}', dockerfileId: 'df', readOnlyRootFS: true, environmentId: 'env' },
      { id: 'x', name: 'N', description: 'D', projectId: 'p1', shared: false, driver: 'docker', environment: {}, dockerfileId: 'df', readOnlyRootFS: true },
      ['description', 'dockerfileId', 'driver', 'environment', 'environmentId', 'id', 'name', 'readOnlyRootFS']],
  ];
  for (const [type, existing, values, body, fields] of cases) {
    assert.deepStrictEqual(await save(type, existing, values), body, `${type} save payload`);
    assert.deepStrictEqual(renderedFieldNames(type, existing), fields, `${type} form fields`);
    assert.ok(calls.some(c => c[0] === 'toast' && c[1] === '资产修改已保存'), `${type} save toast`);
    assert.strictEqual(ctx.state.assetDirty, false, `${type} save clears dirty`);
  }
  reset('rules', { rules: [{ id: 'dup', projectId: 'p1' }] });
  ctx.state.selectedAsset = '__new__';
  calls.length = 0;
  await ctx.saveAsset({ preventDefault() {}, currentTarget: fakeForm({ id: 'dup', name: 'N', description: '' }) });
  assert.ok(calls.some(c => c[0] === 'toast' && /该标识已存在/.test(c[1])), 'duplicate id stays on the create page');
  assert.ok(!calls.some(c => c[0] === 'api'));
  console.log('assets detail ok');
})().catch(err => { console.error(err); process.exit(1); });
