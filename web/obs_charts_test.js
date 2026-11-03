'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// ---------- 极简 DOM ----------
class FakeClassList {
  constructor(el) { this.el = el; this.set = new Set(); }
  add(...c) { c.forEach(x => this.set.add(x)); }
  remove(...c) { c.forEach(x => this.set.delete(x)); }
  contains(c) { return this.set.has(c); }
  toggle(c, on) { if (on === undefined) on = !this.set.has(c); on ? this.set.add(c) : this.set.delete(c); return on; }
}
function camel(name) { return name.replace(/-([a-z])/g, (_, c) => c.toUpperCase()); }
function matches(el, sel) {
  return sel.split(',').some(part => {
    part = part.trim();
    const re = /(#[\w-]+)|(\.[\w-]+)|\[([\w-]+)(?:="([^"]*)")?\]|^([a-z]+)/g;
    let m, ok = true, any = false;
    while ((m = re.exec(part))) {
      any = true;
      if (m[1]) ok = ok && el.id === m[1].slice(1);
      else if (m[2]) ok = ok && el.classList.contains(m[2].slice(1));
      else if (m[3]) {
        const key = camel(m[3].replace(/^data-/, ''));
        const val = m[3].startsWith('data-') ? el.dataset[key] : el.attrs[m[3]];
        ok = ok && val !== undefined && (m[4] === undefined || String(val) === m[4]);
      } else if (m[5]) ok = ok && el.tagName === m[5].toUpperCase();
    }
    return any && ok;
  });
}
class FakeNode {
  constructor(doc) { this.ownerDocument = doc; this.parentNode = null; this.children = []; }
  get isConnected() { let n = this; while (n.parentNode) n = n.parentNode; return n === this.ownerDocument.root; }
  remove() { if (this.parentNode) { const list = this.parentNode.children; list.splice(list.indexOf(this), 1); this.parentNode = null; } }
  before(node) { node.remove(); const list = this.parentNode.children; list.splice(list.indexOf(this), 0, node); node.parentNode = this.parentNode; }
  replaceWith(node) { this.before(node); this.remove(); }
  appendChild(node) { node.remove(); this.children.push(node); node.parentNode = this; return node; }
}
class FakeComment extends FakeNode {}
class FakeElement extends FakeNode {
  constructor(doc, tag) {
    super(doc);
    this.tagName = String(tag || 'div').toUpperCase();
    this.dataset = {}; this.attrs = {}; this.style = {}; this.id = '';
    this.classList = new FakeClassList(this);
    this._text = ''; this.clientWidth = 400; this.hidden = false; this.listeners = {};
  }
  set innerHTML(v) { this.children.forEach(c => { c.parentNode = null; }); this.children = []; this._text = String(v); }
  get innerHTML() { return this._text; }
  set textContent(v) { this._text = String(v); }
  get textContent() { return this._text; }
  get elementChildren() { return this.children.filter(c => c instanceof FakeElement); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return this.attrs[k]; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  insertAdjacentHTML(_pos, html) { this._text += html; }
  focus() {}
  scrollIntoView() {}
  contains(node) { for (let n = node; n; n = n.parentNode) if (n === this) return true; return false; }
  closest(sel) { for (let n = this; n instanceof FakeElement; n = n.parentNode) if (matches(n, sel)) return n; return null; }
  querySelectorAll(sel) {
    const out = [];
    const walk = n => n.elementChildren.forEach(c => { if (matches(c, sel)) out.push(c); walk(c); });
    walk(this);
    return out;
  }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}
function makeDocument() {
  const doc = {};
  doc.root = new FakeElement(doc, 'html');
  doc.documentElement = doc.root;
  doc.documentElement.dataset.theme = 'dark';
  doc.head = doc.root.appendChild(new FakeElement(doc, 'head'));
  doc.body = doc.root.appendChild(new FakeElement(doc, 'body'));
  doc.listeners = {};
  doc.createElement = tag => new FakeElement(doc, tag);
  doc.createComment = () => new FakeComment(doc);
  doc.getElementById = id => doc.root.querySelectorAll('#' + id)[0] || null;
  doc.querySelector = sel => doc.root.querySelector(sel);
  doc.querySelectorAll = sel => doc.root.querySelectorAll(sel);
  doc.addEventListener = (type, fn) => { (doc.listeners[type] ||= []).push(fn); };
  return doc;
}
function el(doc, tag, opts) {
  const node = doc.createElement(tag);
  Object.assign(node.dataset, opts?.data || {});
  if (opts?.id) node.id = opts.id;
  (opts?.cls || []).forEach(c => node.classList.add(c));
  if (opts?.text != null) node.textContent = opts.text;
  return node;
}

// ---------- 假 echarts / 观察者 ----------
function makeEcharts() {
  const stats = { init: 0, dispose: 0, setOption: 0, clear: 0, actions: [] };
  const live = new Set();
  return {
    stats, live,
    init(node, theme, opts) {
      stats.init++;
      assert.equal(opts.renderer, 'svg');
      const handlers = {}, zr = {};
      const chart = {
        node, options: [], disposed: false,
        setOption(o, notMerge) { stats.setOption++; this.options.push({ o, notMerge }); },
        on(name, fn) { (handlers[name] ||= []).push(fn); },
        emit(name, p) { (handlers[name] || []).forEach(fn => fn(p)); },
        getZr() { return { on: (n, fn) => { zr[n] = fn; } }; },
        zrClick(x) { zr.click && zr.click({ offsetX: x, offsetY: 1 }); },
        containPixel() { return true; },
        convertFromPixel(_g, p) { return [p[0]]; },
        dispatchAction(a) { stats.actions.push(a); },
        resize() {}, clear() { stats.clear++; },
        dispose() { stats.dispose++; this.disposed = true; live.delete(this); },
        isDisposed() { return this.disposed; },
      };
      live.add(chart);
      return chart;
    },
  };
}
function makeIO() {
  const observed = new Set();
  class IO {
    constructor(cb) { this.cb = cb; IO.instances.push(this); }
    observe(t) { observed.add(t); }
    unobserve(t) { observed.delete(t); }
    disconnect() { observed.clear(); }
    fire(targets) { this.cb(targets.map(target => ({ target, isIntersecting: true }))); }
  }
  IO.instances = [];
  IO.observed = observed;
  return IO;
}

function makeContext({ echarts, reduced } = {}) {
  const document = makeDocument();
  const IO = makeIO();
  const frames = [];
  const ctx = {
    console, document, Map, Set, WeakMap, Promise, Math, Date, Number, String, Object, Array, JSON, Intl, URLSearchParams,
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout() {},
    requestAnimationFrame: fn => { frames.push(fn); return frames.length; },
    performance: { now: () => ctx.__now || 0 },
    matchMedia: () => ({ matches: !!reduced }),
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    IntersectionObserver: IO,
    ResizeObserver: class { observe() {} unobserve() {} },
    MutationObserver: class { observe() {} },
    localStorage: { getItem() { return null; }, setItem() {} },
    state: { obsTab: 'overview', analytics: { summary: {}, trend: [] }, data: { users: [], runs: [], issues: [] } },
    escapeHTML: v => String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])),
    safeURL: v => String(v || ''),
    formatTokens: v => String(Math.round(Number(v) || 0)),
    percent: v => (Number(v) || 0).toFixed(1) + '%',
    duration: v => `${Math.round((Number(v) || 0) / 1000)}s`,
    projectQuery: () => '', currentProject: () => ({ id: 'p', name: 'P' }),
    toast() {}, userById() { return null; }, userDisplay: u => String(u?.id || ''),
    paginateList: (key, items) => ({ items, total: items.length, size: 20, page: 1, pages: 1, start: 0, end: items.length }),
    pagerHTML: () => '', bindPager() {}, resetListPage() {}, resetListPages() {}, listPageSize: () => 20,
  };
  ctx.window = ctx;
  if (echarts) ctx.echarts = echarts;
  ctx.__frames = frames;
  ctx.__IO = IO;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync('app/obs-charts.js', 'utf8'), ctx);
  vm.runInContext(fs.readFileSync('app/observability.js', 'utf8'), ctx);
  return ctx;
}
const plain = v => JSON.parse(JSON.stringify(v));
function same(a, b, msg) { assert.deepEqual(plain(a), plain(b), msg); }
async function flush() { for (let i = 0; i < 5; i++) await Promise.resolve(); }
function fireVisible(ctx) {
  const io = ctx.__IO.instances[0];
  if (io) io.fire([...ctx.__IO.observed]);
}

// ---------- option 纯函数 ----------
(() => {
  const ctx = makeContext();
  const theme = vm.runInContext('OBS_THEME_PRESETS.dark', ctx);
  const points = [
    { date: '2026-08', tokens: 100, costUsd: 1, costCny: 7, runs: 2, issues: 1, succeeded: 2, failed: 0, inputTokens: 60, outputTokens: 20, cacheReadTokens: 15, cacheWriteTokens: 5 },
    { date: '2026-09', tokens: 300, costUsd: 3, costCny: 21, runs: 5, issues: 2, succeeded: 3, failed: 1, inputTokens: 200, outputTokens: 60, cacheReadTokens: 30, cacheWriteTokens: 10 },
    { date: '2026-10', tokens: 200, costUsd: 2, costCny: 14, runs: 4, issues: 2, succeeded: 4, failed: 0, inputTokens: 100, outputTokens: 60, cacheReadTokens: 30, cacheWriteTokens: 10 },
  ];
  const opts = { theme, reduced: false, fmtTokens: v => String(v) };
  const prev = [null, { date: '2026-05', tokens: 40, costUsd: 0.4, costCny: 2.8, runs: 1, issues: 1, succeeded: 1, failed: 1 }, { date: '2026-06', tokens: 0, costUsd: 0, costCny: 0, runs: 0, issues: 0, succeeded: 0, failed: 0 }];
  for (const mode of ['tokens', 'cost', 'runs', 'issues', 'rate']) {
    const o = ctx.obsTrendOption(points, mode, '2026-09', prev, opts);
    assert.equal(o.series.length, 2, mode);
    assert.equal(o.series[0].name, '上一周期');
    assert.equal(o.series[0].lineStyle.type, 'dashed', 'previous period is dashed');
    same(o.series[0].data, prev.map(p => p ? ctx.obsTrendModeValue(p, mode) : null), 'dashed line comes from prev');
    assert.equal(o.series[1].data.length, points.length);
    assert.ok(o.series[1].markPoint, 'selected point marked');
    assert.equal(o.series[1].markPoint.data[0].coord[0], 1);
    assert.equal(o.aria.enabled, true);
    const bare = ctx.obsTrendOption(points, mode, '2026-09', null, opts);
    assert.equal(bare.series.length, 1, 'no prev → no dashed line');
    assert.equal(bare.series[0].name, '本期');
  }
  assert.equal(ctx.obsTrendOption(points, 'tokens', '', [null, null, null], opts).series.length, 1, 'all-null prev → no dashed line');
  const prevTip = ctx.obsTrendOption(points, 'tokens', '', prev, { ...opts, label: p => p.date }).tooltip.formatter([{ dataIndex: 1 }]);
  assert.ok(prevTip.includes('上一周期 2026-05') && prevTip.includes('>40<'), 'tooltip shows previous-period value');
  assert.ok(!ctx.obsTrendOption(points, 'tokens', '', prev, opts).tooltip.formatter([{ dataIndex: 0 }]).includes('上一周期'));
  same(ctx.obsTrendOption(points, 'tokens', '', null, opts).series[0].data, [100, 300, 200]);
  same(ctx.obsTrendOption(points, 'rate', '', null, opts).series[0].data, [100, 75, 100]);
  const costs = ctx.obsTrendOption(points, 'cost', '', prev, opts);
  same(costs.series[0].data, [null, 2.8, 0], 'previous costs use CNY');
  same(costs.series[1].data, [7, 21, 14], 'current costs use CNY, not legacy USD equivalents');
  assert.equal(costs.yAxis.axisLabel.formatter(21), '¥21.00');
  assert.match(costs.tooltip.formatter([{ dataIndex: 1 }]), /¥21\.00/);

  // 上一周期按日期回退窗口跨度对齐；trend 稀疏时区间内缺失记 0，早于首个周期记 null
  ctx.state.obsTrendGrain = 'day';
  const days = ['2026-10-01', '2026-10-02', '2026-10-04', '2026-10-05', '2026-10-06'].map((date, i) => ({ date, tokens: (i + 1) * 10, runs: 1 }));
  const dayPrev = ctx.obsTrendPrevPoints(days, days.slice(3), 'day');
  same(dayPrev.map(p => p && p.date), ['2026-10-03', '2026-10-04']);
  same(dayPrev.map(p => p.tokens), [0, 30]);
  same(ctx.obsTrendPrevPoints(days, days.slice(1), 'day').map(p => p && p.date), [null, null, null, '2026-10-01'], 'span=5 days');
  assert.equal(ctx.obsTrendPrevPoints(days, days, 'day'), null, 'nothing before first period');
  const weeks = ['2026-W40', '2026-W41', '2026-W42', '2026-W43'].map((date, i) => ({ date, tokens: i + 1 }));
  same(ctx.obsTrendPrevPoints(weeks, weeks.slice(2), 'week').map(p => p.date), ['2026-W40', '2026-W41']);
  assert.equal(ctx.obsTrendPrevPoints(points, points, 'month'), null, 'month window covers whole trend');
  assert.equal(ctx.obsTrendPrevPoints(points, points, 'all'), null);
  const yearEdge = [{ date: '2025-12-30', tokens: 5 }, { date: '2025-12-31', tokens: 6 }, { date: '2026-01-01', tokens: 7 }, { date: '2026-01-02', tokens: 8 }];
  same(ctx.obsTrendPrevPoints(yearEdge, yearEdge.slice(2), 'day').map(p => p.tokens), [5, 6], 'crosses year boundary');
  const comp = ctx.obsTrendOption(points, 'comp', '', null, opts);
  assert.equal(comp.series.length, 4);
  assert.ok(comp.series.every(s => s.stack === 'tokens'));
  same(comp.series.map(s => s.data[1]), [200, 60, 30, 10]);
  const tip = ctx.obsTrendOption([{ date: 'x', label: '<script>alert(1)</script>', tokens: 1 }], 'tokens', '', null, { theme, label: p => p.label }).tooltip.formatter([{ dataIndex: 0 }]);
  assert.ok(!tip.includes('<script>'), 'tooltip escapes labels');
  assert.ok(tip.includes('&lt;script&gt;'));

  const reduced = ctx.obsTrendOption(points, 'tokens', '', null, { theme, reduced: true });
  assert.equal(reduced.animation, false);
  assert.equal(reduced.animationDuration, 0);
  assert.equal(ctx.obsTrendOption(points, 'tokens', '', null, opts).animation, true);

  const rows = [{ name: 'a', tokens: 70, costUsd: 0, costCny: 0 }, { name: '<b>', tokens: 30, costUsd: 2, costCny: 14 }];
  const pie = ctx.obsBreakdownOption(rows, 'tokens', opts);
  assert.equal(pie.series[0].data.reduce((n, d) => n + d.value, 0), ctx.obsBreakdownTotal(rows, 'tokens'));
  assert.equal(ctx.obsBreakdownOption(rows, 'cost', opts).series[0].data[0].value, 0, 'unknown cost stays 0');
  assert.equal(ctx.obsBreakdownOption(rows, 'cost', opts).series[0].data[1].value, 14, 'pie uses CNY amounts');
  assert.ok(!pie.tooltip.formatter({ name: '<b>', value: 30 }).includes('<b><'), 'pie tooltip escapes');
  assert.equal(ctx.obsBreakdownOption(rows, 'tokens', { theme, reduced: true }).animation, false);

  const cells = [{ weekday: 0, hour: 9, runs: 2, tokens: 50 }, { weekday: 6, hour: 23, runs: 1, tokens: 10 }, { weekday: 9, hour: 1, runs: 5, tokens: 5 }];
  const heat = ctx.obsHeatOption(cells, 'tokens', opts);
  assert.equal(heat.series[0].data.length, 7 * 24);
  assert.equal(heat.series[0].data.reduce((n, d) => n + d.value[2], 0), 60, 'invalid weekday dropped, total kept');
  assert.equal(ctx.obsHeatPeak(cells, 'runs').hour, 9);

  const stages = [{ name: 'A', value: 10 }, { name: 'B', value: 8 }, { name: 'C', value: 2 }, { name: 'D', value: 2 }];
  const funnel = ctx.obsFunnelStats(stages);
  assert.equal(funnel.worst, 2);
  assert.equal(Math.round(funnel.stages[1].conv), 80);
  assert.equal(funnel.stages[2].lost, 6);
  assert.equal(ctx.obsFunnelOption(stages, opts).series[0].data.length, 4);

  const owners = ctx.obsOwnersOption([{ name: 'u', delivered: 2, active: 1, undelivered: 3 }], opts);
  same(owners.series.map(s => s.data[0]), [2, 1, 3]);

  const sh = { gongfeng: { mergedMonths: { '2026-01': 1, '2026-02': 1 }, monthlyLegacy: { '2026-01': 3, '2026-02': 1 }, monthlyMonorepo: { '2026-01': 1, '2026-02': 4 },
    weeklyMerged: Object.fromEntries(Array.from({ length: 14 }, (_, i) => [`2026-W${String(i + 1).padStart(2, '0')}`, 1])), weeklyLegacy: {}, weeklyMonorepo: { '2026-W14': 6 } } };
  const monthly = ctx.obsSkillhubMonthlyOption(sh, opts);
  same(monthly.series.map(s => s.data), [[3, 1], [1, 4]]);
  assert.equal(monthly.series[1].label.formatter({ dataIndex: 1 }), 5, 'bar top shows stack total');
  const weekly = ctx.obsSkillhubWeeklyOption(sh, opts);
  assert.equal(weekly.xAxis.data.length, 12);
  same(weekly.series[0].markArea.data[0].map(x => x.xAxis), ['W11', 'W14']);
  const deploy = ctx.obsSkillhubDeployOption({ dailySample: { labels: ['a', 'b'], backend: [1, 2], frontend: [0, 1], e2e: [3, 0] } }, opts);
  assert.equal(deploy.series.length, 3);
  assert.equal(ctx.obsSkillhubDeployOption({}, { theme, reduced: true }).animation, false);
})();

// ---------- 研发效能 option ----------
(() => {
  const ctx = makeContext();
  const theme = vm.runInContext('OBS_THEME_PRESETS.light', ctx);
  const fmtDuration = v => `${Math.round(v / 60000)}m`;
  const opts = { theme, fmtDuration };
  const phase = [
    { label: '10/28', clarification: null, development: null, testing: null, total: null, deliveries: 0 },
    { label: '10/29', clarification: 6 * 60000, development: 3 * 60000, testing: 4 * 60000, total: 10 * 60000, deliveries: 2, samples: { clarification: 1, development: 2, testing: 2 } },
    { label: '10/30', clarification: null, development: 5 * 60000, testing: 0, total: 5 * 60000, deliveries: 1 },
  ];
  const stack = ctx.obsPhaseStackOption(phase, opts);
  const bars = stack.series.filter(s => s.name !== '合计');
  same(bars.map(s => s.name), ['澄清', '研发', '测试']);
  same(bars.map(s => s.itemStyle.color), [theme.c1, theme.c2, theme.c3], 'phases use c1/c2/c3');
  assert.ok(stack.series.every(s => s.stack === 'phase'));
  same(bars.map(s => s.data[0]), [null, null, null], 'day without samples has no bar');
  same(bars.map(s => s.data[1]), [360000, 180000, 240000]);
  same(bars.map(s => s.data[2]), [0, 300000, 0], 'missing phase on a sampled day is 0 height');
  same(ctx.obsPhaseSums(phase), [null, 13 * 60000, 5 * 60000]);
  const top = stack.series.find(s => s.name === '合计');
  assert.equal(top.label.show, true);
  assert.equal(top.label.formatter({ dataIndex: 1 }), '13m', 'bar top shows sum of phase averages');
  assert.equal(top.label.formatter({ dataIndex: 0 }), '');
  same(stack.legend.data, ['澄清', '研发', '测试'], 'legend toggles phases only');
  same([stack.yAxis.interval, stack.yAxis.max], [5 * 60000, 15 * 60000], 'duration axis snaps to whole minutes');
  same(ctx.obsDurationAxis(100 * 60000), { interval: 30 * 60000, max: 120 * 60000 });
  same(ctx.obsDurationAxis(0), {});
  const tip = stack.tooltip.formatter([{ dataIndex: 1 }]);
  assert.ok(tip.includes('三阶段均值之和') && tip.includes('>13m<'));
  assert.ok(tip.includes('已采集耗时（按条平均）') && tip.includes('>10m<'), 'collected total comes from point.total');
  assert.ok(tip.includes('2 条') && tip.includes('6m（1 条）'));
  const partial = stack.tooltip.formatter([{ dataIndex: 2 }]);
  assert.ok(partial.includes('待采集'), 'missing phase shows 待采集');
  assert.ok(partial.includes('>0m<'), 'observed zero is not 待采集');
  assert.ok(stack.tooltip.formatter([{ dataIndex: 0 }]).includes('没有完成项样本'));
  const year = Array.from({ length: 46 }, (_, i) => ({ label: String(i), development: 60000, deliveries: 1 }));
  assert.equal(ctx.obsPhaseStackOption(year, opts).series.find(s => s.name === '合计').label.show, false, '>45 days hides bar-top labels');
  assert.equal(ctx.obsPhaseStackOption(year.slice(0, 45), opts).series.find(s => s.name === '合计').label.show, true);
  assert.equal(ctx.obsPhaseStackOption(phase, { theme, reduced: true }).animation, false);

  const trend = [{ label: 'a', deliveries: 0, firstPass: 0 }, { label: 'b', deliveries: 2, firstPass: 50 }, { label: 'c', deliveries: 1, firstPass: 100 }];
  const bar = ctx.obsMetricTrendOption(trend, { name: '交付', kind: 'bar', integer: true, get: p => p.deliveries, format: v => String(v) }, opts);
  assert.equal(bar.series[0].type, 'bar');
  same(bar.series[0].data, [0, 2, 1]);
  assert.equal(bar.yAxis.minInterval, 1);
  assert.equal(bar.series[0].label.formatter({ value: 0 }), '', 'zero bars carry no label');
  const line = ctx.obsMetricTrendOption(trend, { name: '一次通过率', kind: 'line', percent: true, get: p => p.deliveries ? p.firstPass : null, format: v => v + '%', rows: p => [['完成项', String(p.deliveries)]] }, opts);
  assert.equal(line.series[0].type, 'line');
  assert.ok(line.series[0].areaStyle, 'line has light area');
  same(line.series[0].data, [null, 50, 100]);
  assert.equal(line.yAxis.min, 0);
  assert.equal(line.yAxis.max, 100, 'percent axis is 0–100');
  assert.equal(line.series[0].connectNulls, false, 'days without completions break the line');
  const lineTip = line.tooltip.formatter([{ dataIndex: 0 }]);
  assert.ok(lineTip.includes('—') && lineTip.includes('完成项'));
  assert.equal(ctx.obsMetricTrendOption([], {}, opts).series[0].data.length, 0);

  const slices = [{ label: '一次通过', value: 2 }, { label: '返工后完成', value: 0 }, { label: '<进行中>', value: 1 }];
  const donut = ctx.obsSliceDonutOption(slices, { ...opts, center: { value: 3, unit: '需求' } });
  same(donut.series[0].data.map(d => [d.name, d.value]), [['一次通过', 2], ['<进行中>', 1]], 'zero slices are not drawn');
  assert.equal(donut.series[0].data[1].itemStyle.color, ctx.obsBreakdownPalette(theme)[2], 'color follows the original slice index');
  assert.equal(donut.title.text, '3');
  assert.equal(donut.title.subtext, '需求');
  const donutTip = donut.tooltip.formatter({ name: '<进行中>', value: 1 });
  assert.ok(donutTip.includes('33.3%') && !donutTip.includes('<进行中>'), 'tooltip shows share and escapes names');
  assert.equal(ctx.obsSliceDonutOption([], opts).title, undefined);
})();

// ---------- 挂载 / 释放 / 泄漏 ----------
(async () => {
  const ec = makeEcharts();
  const ctx = makeContext({ echarts: ec });
  const doc = ctx.document;
  const host = doc.body.appendChild(el(doc, 'div', { id: 'obsBody' }));
  const render = n => {
    ctx.obsDisposeCharts(host);
    host.innerHTML = '';
    for (let i = 0; i < n; i++) {
      ctx.obsChartSlot('c' + i, { option: () => ({ series: [] }), on: { click: () => {} } }, 200);
      host.appendChild(el(doc, 'div', { data: { obsChart: 'c' + i } }));
    }
    ctx.obsMountCharts(host);
  };
  render(3);
  assert.equal(ec.stats.init, 0, 'charts wait until visible');
  fireVisible(ctx);
  await flush();
  assert.equal(ctx.obsChartCount(), 3);
  assert.equal(ec.stats.init, 3);
  ctx.obsMountCharts(host);
  fireVisible(ctx);
  await flush();
  assert.equal(ec.stats.init, 3, 'repeat mount does not re-init');
  ctx.obsDisposeCharts();
  assert.equal(ctx.obsChartCount(), 0);
  assert.equal(ec.live.size, 0);
  for (let i = 0; i < 50; i++) { render(3); fireVisible(ctx); await flush(); }
  assert.equal(ctx.obsChartCount(), 3, 'no leaked instances after 50 renders');
  assert.equal(ec.live.size, 3);
  assert.equal(ctx.__IO.observed.size, 0, 'no stale observers');
  assert.ok(ctx.obsReplayChart('c0'));
  assert.equal(ec.stats.clear, 1);
  assert.equal(ctx.obsReplayChart('missing'), false);

  // axisClick 走 zr 点击 → 下标
  let clicked = -1;
  ctx.obsDisposeCharts(host); host.innerHTML = '';
  ctx.obsChartSlot('axis', { option: () => ({}), axisClick: idx => { clicked = idx; } }, 200);
  host.appendChild(el(doc, 'div', { data: { obsChart: 'axis' } }));
  ctx.obsMountCharts(host); fireVisible(ctx); await flush();
  ctx.obsChartEntry('axis').chart.zrClick(2.2);
  assert.equal(clicked, 2);
  ctx.obsDisposeCharts();

  // 空数据不 init
  ctx.obsChartSlot('empty', { empty: true, option: () => ({}) }, 100);
  const emptyEl = host.appendChild(el(doc, 'div', { data: { obsChart: 'empty', empty: '1' } }));
  ctx.obsMountCharts(host); fireVisible(ctx); await flush();
  assert.equal(ctx.obsChartEntry('empty'), null);
  emptyEl.remove();
})().then(async () => {
  // ---------- 加载失败 → SVG 兜底 ----------
  const ctx = makeContext();
  const doc = ctx.document;
  const script = { set onerror(fn) { this._err = fn; } };
  const origCreate = doc.createElement;
  let inserted = null;
  doc.createElement = tag => { const n = origCreate(tag); if (tag === 'script') inserted = n; return n; };
  doc.head.appendChild = n => { if (n.tagName === 'SCRIPT') setImmediate(() => n.onerror && n.onerror()); return n; };
  const card = doc.body.appendChild(el(doc, 'section', { data: { obsCard: 'trend' } }));
  const head = card.appendChild(el(doc, 'header', { cls: ['obs-card-head'] }));
  head.appendChild(el(doc, 'div', { cls: ['obs-card-tools'] }));
  const points = [{ date: '2026-09', tokens: 10 }, { date: '2026-10', tokens: 20 }];
  ctx.state.analytics.trend = points;
  ctx.obsChartSlot('trend', { option: () => ({}), fallback: () => ctx.obsTrendChart(points, 'tokens') }, 200);
  const slot = card.appendChild(el(doc, 'div', { data: { obsChart: 'trend' } }));
  ctx.obsMountCharts(card); fireVisible(ctx);
  await new Promise(r => setImmediate(r)); await flush();
  assert.ok(inserted && inserted.src.startsWith('/vendor/echarts/echarts.min.js?v=6.1.0'), 'loads same-origin echarts');
  assert.ok(slot.innerHTML.includes('obs-chart-line'), 'falls back to SVG trend');
  assert.ok(slot.classList.contains('is-fallback'));
  assert.ok(head.querySelector('.obs-card-tools').innerHTML.includes('简版图表'));
  assert.equal(ctx.obsChartCount(), 0);
  const firstScript = inserted;
  ctx.obsMountCharts(card);
  const retry = ctx.obsEnsureECharts();
  assert.notEqual(inserted, firstScript, 'next mount retries loading after a failure');
  await new Promise(r => setImmediate(r));
  assert.equal(await retry, null);
}).then(() => {
  // ---------- 数字滚动 ----------
  const ctx = makeContext();
  const doc = ctx.document;
  const num = doc.body.appendChild(el(doc, 'b', { text: '1,234.5k' }));
  ctx.obsCountUp(num);
  assert.equal(num.textContent, '0.0k');
  ctx.__now = 350; ctx.__frames.shift()(350);
  assert.notEqual(num.textContent, '1,234.5k');
  ctx.__now = 900; ctx.__frames.shift()(900);
  assert.equal(num.textContent, '1,234.5k', 'ends on original text');
  const dur = doc.body.appendChild(el(doc, 'b', { text: '3m 12s' }));
  ctx.obsCountUp(dur);
  assert.equal(dur.textContent, '3m 12s', 'multi-number values are left alone');
  const rctx = makeContext({ reduced: true });
  const still = rctx.document.body.appendChild(el(rctx.document, 'b', { text: '42' }));
  rctx.obsCountUp(still);
  assert.equal(still.textContent, '42');
  assert.equal(rctx.__frames.length, 0, 'reduced motion skips animation');
}).then(() => {
  // ---------- 放大窗口 ----------
  const ctx = makeContext({ echarts: makeEcharts() });
  const doc = ctx.document;
  const view = doc.body.appendChild(el(doc, 'section', { id: 'observabilityView' }));
  const grid = view.appendChild(el(doc, 'div'));
  const before = grid.appendChild(el(doc, 'div'));
  const card = grid.appendChild(el(doc, 'section', { data: { obsCard: 'heat' } }));
  const btn = card.appendChild(el(doc, 'button', { data: { obsExpand: 'heat' } }));
  const after = grid.appendChild(el(doc, 'div'));
  const modal = view.appendChild(el(doc, 'div', { id: 'obsExpand' }));
  modal.hidden = true;
  const box = modal.appendChild(el(doc, 'div', { cls: ['obs-expand-box'] }));
  ctx.obsExpand(card);
  assert.equal(card.parentNode, box);
  assert.equal(modal.hidden, false);
  assert.ok(doc.body.classList.contains('obs-expand-open'));
  assert.ok(card.classList.contains('is-expanded'));
  assert.equal(btn.attrs['aria-label'], '关闭放大窗口');
  doc.listeners.keydown.forEach(fn => fn({ key: 'Escape' }));
  assert.equal(card.parentNode, grid);
  assert.ok(grid.children.length === 3 && grid.children[0] === before && grid.children[1] === card && grid.children[2] === after, 'card returns to its slot');
  assert.equal(modal.hidden, true);
  assert.ok(!doc.body.classList.contains('obs-expand-open'));
  assert.ok(!card.classList.contains('is-expanded'));
  assert.equal(btn.attrs['aria-label'], '放大查看');
}).then(() => {
  // ---------- CSV ----------
  const ctx = makeContext();
  const csv = ctx.obsCsvContent([{ headers: ['名称', '值'], rows: [ctx.obsRow(['<b>=SUM(A1)</b>', '1,2']), ctx.obsRow(['a &amp; b', '"q"'])] }]);
  assert.ok(csv.startsWith('\ufeff'), 'BOM for Excel');
  assert.ok(csv.includes("'=SUM(A1)"), 'formula injection guarded');
  assert.ok(csv.includes('"1,2"'));
  assert.ok(csv.includes('a & b'));
  assert.ok(csv.includes('"""q"""'));
  assert.match(ctx.obsCsvFileName('SOP 一览'), /^SOP_一览-P-\d{4}-\d{2}-\d{2}\.csv$/);
}).then(() => {
  // ---------- 改版不丢统计：原表头全部仍在明细中 ----------
  const ctx = makeContext();
  ctx.state.obsScope = 'project';
  ctx.state.analytics = {
    summary: { totalRuns: 1, failedRuns: 1 }, trend: [{ date: '2026-10', runs: 1, tokens: 5 }],
    runTraces: [{ runId: 'r1', status: 'failed', tokens: 5, createdAt: '2026-10-01T00:00:00Z', errorMessage: 'boom' }],
    workflows: [{ key: 'w', name: 'W', runs: 1, tokens: 5, nodeCount: 2, nodeTypes: { agent: 2 } }], models: [{ key: 'm', runs: 1, tokens: 5 }],
    userUsage: [{ date: '2026-10-01', userId: 'u', runs: 1, tokens: 5 }], issueSources: [{ key: 'local', runs: 1 }], categories: [{ key: 'other', runs: 1 }],
    workflowVersions: [{ workflowId: 'w', version: 1, runs: 1 }], nodes: [{ key: 'n', runs: 1, averageExecutionDurationMs: 10 }], failures: [{ nodeId: 'n', code: 'boom', count: 1 }],
    deliveries: [{ issueId: 'i', source: 'tapd', delivered: true, mrState: 'merged' }], delivery: { issues: 1, delivered: 1 },
  };
  ctx.relativeTime = () => '';
  ctx.runStatus = s => s;
  const groups = ctx.obsUsageDetailGroups(ctx.obsScopedView());
  const tables = groups.flatMap(g => g.items).flatMap(i => i.tables || []);
  const has = headers => tables.some(t => headers.every((h, i) => t.headers[i] === h));
  const legacy = {
    '关联与追溯': ['TAPD / 需求', 'workflow_task_id', 'Run / Trace', '状态', '耗时', 'Tokens', '成本', '工具', '人工', '错误位置'],
    '最近运行': ['需求 / 流程', '状态', '耗时', 'Tokens', '成本'],
    '趋势': ['月', '运行', '需求', '成功', '失败', 'Tokens', '输入 / 输出', '成本', '执行耗时'],
    'SOP 一览': ['SOP', '运行', '成功率', '返工率', '取消率', 'Tokens', '输入 / 输出', 'Token 占比', '成本', '成本占比', '均执行'],
    '各模型用量': ['模型', '调用', 'Tokens', '输入 / 输出', 'Token 占比', '成本', '成本占比', '成功率', '均执行'],
    '成员汇总': ['成员', '运行', '成功', '失败', 'Tokens', '输入 / 输出', '成本', '执行耗时'],
    '每人每日用量': ['日期', '成员', '运行', 'Tokens', '输入 / 输出', '成本', '执行耗时'],
    '需求类型分布': ['来源', '关联执行', 'SOP 成功', '已交付需求', 'Tokens', '成本', '平均执行'],
    '编排类型分布': ['统计标签', '执行', '成功', '完成需求', 'Tokens', '成本'],
    'SOP 版本对比': ['SOP', '版本', '运行', '成功率', '均执行耗时', 'Tokens', '成本', '提示'],
    'SOP 编排画像': ['SOP', '节点数', '节点类型构成', '统计标签', '使用频次'],
    '节点质量明细': ['节点', '执行', '成功', '失败', '返工', '重试率', '均执行耗时', 'Tokens', '工具调用'],
    '失败原因': ['节点', '错误', '次数'],
    '慢节点': ['节点', '执行', '均耗时', 'P95', '成功率', 'Tokens'],
  };
  for (const [name, headers] of Object.entries(legacy)) assert.ok(has(headers), `detail keeps ${name}`);
  const keys = groups.flatMap(g => g.items).map(i => i.key);
  for (const key of ['traces', 'recent', 'trend', 'sops', 'models', 'users', 'dist', 'nodes', 'fails', 'versions']) assert.ok(keys.includes(key), key);
  const delivery = ctx.obsDeliveryTableDef();
  assert.equal(delivery.tables[0].headers.length, 13);
  assert.equal(delivery.tables[0].pageKey, 'obs-deliveries');
  assert.equal(ctx.obsDeliveryTableDef(true).tables[0].pageKey, '', 'CSV exports all rows');
  const insights = ctx.obsInsights(ctx.obsScopedView(), 'usage');
  assert.ok(insights.includes('1 次失败'));
  ctx.state.analytics.failures = [{ nodeId: 'n', code: 'max tokens exceeded', count: 1 }];
  assert.ok(!ctx.obsInsights(ctx.obsScopedView(), 'usage').includes('配置凭据'), 'token-limit errors are not credential errors');
  ctx.state.analytics.failures = [{ nodeId: 'n', code: '401 Unauthorized: invalid api key', count: 1 }];
  assert.ok(ctx.obsInsights(ctx.obsScopedView(), 'usage').includes('配置凭据'));
  ctx.state.analytics.delivery = { issues: 2, delivered: 1, averageDurationMs: 7 * 60000, averageExecutionDurationMs: 21 * 60000 };
  const deliveryInsights = ctx.obsInsights(ctx.obsScopedView(), 'delivery');
  assert.ok(!deliveryInsights.includes('其中') && !deliveryInsights.includes('等待'), 'per-issue and per-run averages are not nested');
  assert.ok(deliveryInsights.includes('需求平均端到端'));
  ctx.state.analytics = { summary: {}, trend: [] };
  assert.equal(ctx.obsInsights(ctx.obsScopedView(), 'usage'), '', 'no data, no insights');
  console.log('obs charts ok');
}).catch(err => { console.error(err); process.exit(1); });
