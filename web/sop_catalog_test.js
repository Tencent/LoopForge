const assert = require('assert');
const fs = require('fs');
const vm = require('vm');

// argv[2]: app/sops.js, argv[3]: app/dashboards.js
const sopsSrc = fs.readFileSync(process.argv[2], 'utf8');
const dashboardsSrc = fs.readFileSync(process.argv[3], 'utf8');
const metricHealthStart = dashboardsSrc.indexOf('function metricHealth(');
assert.ok(metricHealthStart >= 0, 'metricHealth not found');
const metricHealthSrc = dashboardsSrc.slice(metricHealthStart, dashboardsSrc.indexOf('\n', metricHealthStart));

const frames = [];
const ctx = {
  state: {},
  admin: false,
  window: {},
  escapeHTML: v => String(v ?? '').replace(/[&<>'"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[c])),
  huiIcon: (name, cls = '') => `<svg class="agent-icon harness-icon${cls ? ` ${cls}` : ''}" data-icon="${name}"></svg>`,
  percent: n => `${Math.round(n || 0)}%`,
  formatTimestamp: v => (v ? 'T' : ''),
  catalogGeneric: item => !!item?.generic,
  catalogOwned: projectId => !!ctx.state.projectFilter && projectId === ctx.state.projectFilter,
  isPlatformAdmin: () => ctx.admin,
  lfT: zh => zh,
  userById: () => ({}),
  requestAnimationFrame: fn => frames.push(fn),
  setTimeout: () => 0,
  clock: 1000,
  performance: { now: () => ctx.clock },
};
vm.createContext(ctx);
vm.runInContext(sopsSrc, ctx);
vm.runInContext(metricHealthSrc, ctx);

ctx.state = {
  projectFilter: 'p1',
  data: {
    projects: [{ id: 'p1', name: '本项目' }, { id: 'p2', name: '平台基础设施' }],
    workflows: [
      { id: 'own', name: '研发交付', projectId: 'p1', shared: false, status: 'published', owner: 'example-owner', version: 7 },
      { id: 'own-shared', name: '已共享', projectId: 'p1', shared: true, status: 'draft' },
      { id: 'foreign', name: '接口评审', projectId: 'p2', shared: true, status: 'published', owner: 'platform', version: 5 },
      { id: 'gen', name: '通用研发交付', generic: true, version: 3, nodes: [{ type: 'agent' }, { type: 'approval' }], tags: ['研发'] },
      { id: 'gen2', name: '通用缺陷诊断', generic: true, version: 2, nodes: [] },
      { id: 'copy', name: '缺陷诊断', projectId: 'p1', tags: ['generic-source:gen2'] },
    ],
  },
};
const wf = id => ctx.state.data.workflows.find(w => w.id === id);
const count = (html, needle) => html.split(needle).length - 1;

// ---------- One primary action per card ----------
const own = ctx.projectSOPCard(wf('own'), { totalRuns: 4, succeededRuns: 3, failedRuns: 1, successRate: 75, lastRunAt: 'x' }, 0);
assert.ok(own.includes('<article class="sop-card" data-sop="own" tabindex="0" role="button"'), own);
assert.strictEqual(count(own, 'class="sop-card-primary"'), 1, own);
assert.ok(own.includes('data-open-sop="own"') && own.includes('>打开</button>'), own);
assert.strictEqual(count(own, 'data-sop-menu="own"'), 1, own);
assert.ok(own.includes('aria-haspopup="menu" aria-expanded="false"'), own);
assert.ok(!/data-(share|delete)-workflow/.test(own), 'share and delete live in the ⋯ menu');
assert.ok(own.includes('<span class="is-bad"><b>1</b>失败</span>'), 'non-zero failures are red');
assert.ok(own.includes('example-owner · v7 · 最近 T'), own);
assert.strictEqual(count(own, 'data-hover-ink-control'), 2, 'primary and ⋯ carry hover ink');

const foreign = ctx.sopCatalogCard(wf('foreign'), new Map(), []);
assert.strictEqual(count(foreign, 'class="sop-card-primary"'), 1, foreign);
assert.ok(foreign.includes('data-clone-workflow="foreign"') && foreign.includes('复制到本项目'), foreign);
assert.ok(!foreign.includes('data-sop-menu'), 'shared cards have no ⋯');
assert.ok(foreign.includes('<span class="sop-card-tag">来自 平台基础设施</span>'), 'source tag sits in the meta row');
assert.strictEqual(ctx.sopCardMenuItems(wf('foreign')).length, 0);

const generic = ctx.genericSOPCard(wf('gen'));
assert.ok(generic.includes('class="sop-card generic" data-generic-id="gen" tabindex="0" role="button"'), generic);
assert.strictEqual(count(generic, 'class="sop-card-primary"'), 1, generic);
assert.ok(generic.includes('data-adopt-generic="gen"'), generic);
assert.ok(generic.includes('data-sop-menu="gen"'), generic);
assert.ok(generic.includes('sop-badge tone-accent') && generic.includes('>通用<'), 'generic status is accent');
assert.ok(!/data-(run|edit)-generic/.test(generic), 'run / edit live in the ⋯ menu');
const adopted = ctx.genericSOPCard(wf('gen2'));
assert.ok(adopted.includes('data-open-sop="copy"') && adopted.includes('查看项目副本'), adopted);
assert.ok(!adopted.includes('data-adopt-generic'), adopted);

// ---------- ⋯ menu items ----------
const ownMenu = ctx.sopCardMenuHTML(wf('own'));
assert.ok(ownMenu.includes('data-share-workflow="own"') && ownMenu.includes('共享给其他项目'), ownMenu);
assert.ok(ownMenu.includes('class="issue-popover-item danger" data-sop-action="delete" data-delete-workflow="own"'), ownMenu);
assert.strictEqual(count(ownMenu, 'role="menuitem"'), 2, ownMenu);
assert.ok(ctx.sopCardMenuHTML(wf('own-shared')).includes('取消共享'));
ctx.admin = false;
let genMenu = ctx.sopCardMenuHTML(wf('gen'));
assert.ok(genMenu.includes('data-run-generic="gen"') && genMenu.includes('直接运行'), genMenu);
assert.ok(!genMenu.includes('data-edit-generic'), 'only platform admins edit generic templates');
ctx.admin = true;
genMenu = ctx.sopCardMenuHTML(wf('gen'));
assert.ok(genMenu.includes('data-edit-generic="gen"') && genMenu.includes('编辑模板'), genMenu);

// ---------- Badge tones ----------
const tone = (m, live = 0) => ctx.sopHealthTone(ctx.metricHealth(m, live)).tone;
assert.strictEqual(tone({ totalRuns: 3 }, 1), 'info');
assert.ok(ctx.sopHealthBadge(ctx.metricHealth({}, 1)).includes('sop-lamp'), 'running breathes');
assert.strictEqual(tone({ totalRuns: 3, waitingRuns: 1 }), 'warn');
assert.strictEqual(tone({ totalRuns: 3, succeededRuns: 3 }), 'success');
assert.strictEqual(tone({ totalRuns: 3, failedRuns: 1, succeededRuns: 2, lastRunStatus: 'failed' }), 'error');
assert.strictEqual(tone({ totalRuns: 2, cancelledRuns: 2 }), 'error');
assert.strictEqual(tone({}), 'neutral');
assert.ok(ctx.sopStatusBadge({ status: 'published' }).includes('tone-success'));
assert.ok(ctx.sopStatusBadge({ status: 'draft' }).includes('tone-neutral') && ctx.sopStatusBadge({ status: 'draft' }).includes('data-icon="edit"'));
assert.ok(ctx.sopStatusBadge({ status: 'archived' }).includes('tone-neutral'));

// ---------- Motion: enter once, FLIP on move, no replay on refresh ----------
class FakeCard {
  constructor(key, x, y) {
    this.dataset = { sop: key };
    this.rect = { left: x, top: y, width: 300, height: 280 };
    this.classes = new Set(['sop-card']);
    this.styles = {};
    this.listeners = {};
    this.attributes = {};
    this.isConnected = true;
    const card = this;
    this.classList = {
      add: c => card.classes.add(c), remove: c => card.classes.delete(c), contains: c => card.classes.has(c),
    };
    this.style = new Proxy({}, {
      get: (_, k) => (k === 'setProperty' ? (n, v) => { card.styles[n] = v; } : k === 'getPropertyValue' ? n => card.styles[n] || '' : card.styles[k] || ''),
      set: (_, k, v) => { card.styles[k] = v; return true; },
    });
  }
  getBoundingClientRect() { return this.rect; }
  addEventListener(type, fn) { this.listeners[type] = fn; }
  removeEventListener(type) { delete this.listeners[type]; }
  setAttribute(k, v) { this.attributes[k] = v; }
  removeAttribute(k) { delete this.attributes[k]; }
  remove() { this.isConnected = false; }
  end() { this.listeners.animationend?.({ target: this }); }
}
const root = {
  cards: [],
  getClientRects: () => [1],
  getBoundingClientRect: () => ({ left: 0, top: 0 }),
  querySelectorAll() { return this.cards.filter(c => !c.classes.has('is-leaving')); },
  appendChild(c) { this.cards.push(c); },
};
const motion = vm.runInContext('sopCatalogMotion', ctx);
const render = (keys, ctxKey, positions = keys.map((_, i) => [i * 320, 0])) => {
  const before = motion.snapshot(root);
  root.cards = keys.map((k, i) => new FakeCard(k, ...positions[i]));
  motion.play(root, before, ctxKey);
  return root.cards;
};

let cards = render(['a', 'b', 'c'], 'project|all|recent');
assert.deepStrictEqual(cards.map(c => c.classes.has('is-entering')), [true, true, true], 'first reveal enters');
assert.deepStrictEqual(cards.map(c => c.styles['--card-index']), ['0', '1', '2'], 'entrance is staggered');
cards.forEach(c => c.end());

cards = render(['a', 'b', 'c'], 'project|all|recent');
assert.ok(cards.every(c => !c.classes.has('is-entering') && !c.styles.transform), 'a refresh in the same context does not replay');

cards = render(['a', 'b', 'c'], 'project|all|runs', [[320, 0], [0, 0], [640, 0]]);
assert.ok(cards.every(c => !c.classes.has('is-entering')), 'kept cards do not re-enter on sort');
assert.strictEqual(cards[0].styles.transform, 'translate(-320px, 0px)', 'moved cards FLIP from their old slot');
assert.strictEqual(cards[1].styles.transform, 'translate(320px, 0px)');
assert.ok(!cards[2].styles.transform, 'unmoved cards stay put');
frames.splice(0).forEach(f => f());
frames.splice(0).forEach(f => f());
assert.ok(!cards[0].styles.transform && cards[0].styles.transition.includes('transform .58s'), 'FLIP plays back to place');

cards = render(['a', 'd'], 'project|blocked|runs', [[320, 0], [0, 0]]);
assert.ok(!cards[0].classes.has('is-entering') && cards[1].classes.has('is-entering'), 'only new keys enter');
const ghosts = root.cards.filter(c => c.classes.has('is-leaving'));
assert.deepStrictEqual(ghosts.map(c => c.dataset.sop), ['b', 'c'], 'removed cards fade out as ghosts');
assert.strictEqual(ghosts[0].attributes.inert, '', 'ghosts are inert');
assert.strictEqual(ghosts[0].styles.left, '0px');

ctx.clock += 100;
const midway = render(['a', 'd'], 'project|blocked|runs', [[320, 0], [0, 0]]);
assert.ok(midway[1].classes.has('is-entering'), 'a refresh mid-entrance keeps the entrance running');
assert.strictEqual(midway[1].styles.animationDelay, '-100ms', 'and resumes it instead of restarting');
midway.forEach(c => c.end());

motion.reset();
cards = render(['a', 'd'], 'project|blocked|runs', [[320, 0], [0, 0]]);
assert.ok(cards.every(c => c.classes.has('is-entering')), 're-entering the view replays the entrance');

// ---------- ⋯ menu closes when another card button is used ----------
class FakeEl {
  constructor(tag, attrs = {}, parent = null) {
    this.tag = tag; this.dataset = {}; this.attrs = {}; this.listeners = {}; this.parent = parent;
    this.classes = new Set(); this.isConnected = true; this.hidden = false; this.innerHTML = ''; this.style = {};
    Object.entries(attrs).forEach(([k, v]) => this.setAttribute(k, v));
    const el = this;
    this.classList = { add: c => el.classes.add(c), remove: c => el.classes.delete(c), contains: c => el.classes.has(c) };
  }
  setAttribute(k, v) {
    this.attrs[k] = String(v);
    if (k.startsWith('data-')) this.dataset[k.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase())] = String(v);
    if (k === 'class') String(v).split(' ').forEach(c => this.classes.add(c));
  }
  getAttribute(k) { return this.attrs[k] ?? null; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  closest(sel) {
    for (let el = this; el; el = el.parent) {
      if (sel === 'button' ? el.tag === 'button' : sel === '.sop-card' ? el.classes.has('sop-card') : sel === '[data-sop-menu]' ? 'sopMenu' in el.dataset : sel === '[data-sop-action]' ? 'sopAction' in el.dataset : false) return el;
    }
    return null;
  }
  contains(el) { for (; el; el = el.parent) if (el === this) return true; return false; }
  querySelector() { return null; }
  querySelectorAll() { return []; }
  getBoundingClientRect() { return { left: 0, top: 0, right: 40, bottom: 40 }; }
  focus() { ctx.document.activeElement = this; }
}
const docListeners = {};
const switched = [];
Object.assign(ctx, {
  document: {
    body: { appendChild() {} },
    createElement: tag => new FakeEl(tag),
    addEventListener: (type, fn) => (docListeners[type] ||= []).push(fn),
  },
  clone: v => JSON.parse(JSON.stringify(v)),
  renderWorkflowMeta() {}, renderCanvas() {}, renderProperties() {},
  switchView: name => switched.push(name),
  toast() {},
});
Object.assign(ctx.window, { innerWidth: 1280, innerHeight: 800, addEventListener() {} });
// Mirrors the browser: a click bubbles from the target through the catalog root, then to document.
const catalog = new FakeEl('div');
const click = target => {
  let stopped = false;
  const e = { target, stopPropagation: () => { stopped = true; } };
  (catalog.listeners.click || []).forEach(fn => fn(e));
  if (!stopped) (docListeners.click || []).forEach(fn => fn(e));
};
ctx.bindSOPCatalogEvents(catalog);
const cardA = new FakeEl('article', { class: 'sop-card', 'data-sop': 'own' }, catalog);
const moreA = new FakeEl('button', { 'data-sop-menu': 'own' }, cardA);
const cardB = new FakeEl('article', { class: 'sop-card', 'data-sop': 'own-shared' }, catalog);
const openB = new FakeEl('button', { 'data-open-sop': 'own-shared' }, cardB);
const menuOpen = () => !vm.runInContext('sopMenuPopover', ctx).hidden;

click(moreA);
assert.ok(menuOpen() && moreA.attrs['aria-expanded'] === 'true', '⋯ opens the menu');
click(openB);
assert.ok(!menuOpen(), 'opening another card closes the menu');
assert.strictEqual(moreA.attrs['aria-expanded'], 'false');
assert.deepStrictEqual(switched, ['flow']);

click(moreA);
click(cardB);
assert.ok(!menuOpen(), 'clicking another card body closes the menu');

click(moreA);
ctx.hideSOPCardMenu();
assert.ok(!menuOpen());

console.log('sop catalog ok');
