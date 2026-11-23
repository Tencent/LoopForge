import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle, chooseOption } from '../lib/load-app.mjs';

// 知识库接入管理端（连接筛选 / 库 CRUD / 三模式导入 / 文档详情抽屉）的行为矩阵。
// 全部通过真实 DOM 事件驱动 web/app/knowledge.js，避免直接调用内部函数。

const CONN_A = 'conn-a';
const CONN_B = 'conn-b';
const SRC_A = `${CONN_A}::kb-1`;
const SRC_B = `${CONN_B}::kb-2`;

// 真实后端（scopeWeKnoraDoc）会给非 default 连接的文档 ID 加连接前缀；
// 测试桩必须同形，否则多连接下的文档寻址无法被真实验证。
const DOC = (raw) => `${CONN_A}::${raw}`;
const DOC_PATH = (raw) => '/api/knowledge/documents/' + encodeURIComponent(DOC(raw));
const docRoute = (raw, tail = '') => new RegExp('^' + DOC_PATH(raw) + tail);

const ALL_AVAILABLE = {
  kb_manage: 'available',
  doc_write: 'available',
  chunk_read: 'available',
  preview_download: 'available',
  reparse: 'available',
};

function capabilities(over = {}) {
  return { checkedAt: '2026-11-20T10:00:00Z', capabilities: { ...ALL_AVAILABLE, ...over } };
}

function settingsFixture(over = {}) {
  return {
    ready: true,
    provider: 'weknora',
    enabled: true,
    writeEnabled: true,
    canManage: true,
    url: 'http://weknora-app:8080',
    tokenConfigured: true,
    defaultSources: [],
    projectSources: {},
    connections: [
      { id: CONN_A, name: '研发空间', url: 'http://weknora-app:8080', account: 'dev@example.com', tokenConfigured: true, enabled: true },
      { id: CONN_B, name: '测试空间', url: 'https://kb.example.com', tokenConfigured: true, enabled: false },
    ],
    ...over,
  };
}

const SOURCES = [
  { id: SRC_A, name: '[研发空间] 产品手册', doc_count: 45, allowed: true },
  { id: SRC_B, name: '测试库', doc_count: 3, allowed: false },
];

const DOCS = [
  { id: DOC('doc-1'), title: '安装指南.md', file_type: 'md', type: 'manual', parse_status: 'completed', updated_at: '2026-11-20T10:00:00Z' },
  { id: DOC('doc-2'), title: '接口文档.pdf', file_type: 'pdf', type: 'file', parse_status: 'failed', error_message: '字体缺失导致解析失败', updated_at: '2026-11-19T08:30:00Z' },
  { id: DOC('doc-3'), title: 'FAQ', type: 'manual', parse_status: 'processing' },
  { id: DOC('doc-4'), title: '草稿', parse_status: 'pending' },
  { id: DOC('doc-5'), title: '旧版说明', parse_status: 'draft' },
  { id: DOC('doc-img'), title: '截图.png', file_type: 'png', type: 'file', parse_status: 'completed' },
  { id: DOC('doc-pdf'), title: '手册.pdf', file_type: 'pdf', type: 'file', parse_status: 'completed' },
  { id: DOC('doc-bin'), title: '归档.zip', file_type: 'zip', type: 'file', parse_status: 'completed' },
  { id: DOC('doc-405'), title: '临时.md', file_type: 'md', type: 'file', parse_status: 'completed' },
];

// 默认知识库路由：两个连接（conn-a 全能力、conn-b 能力接口 404 回退 legacy）、两个知识库。
// extra 后置注册，按「后注册优先」覆盖默认桩。
const ROUTES = (extra = []) => [
  { method: 'GET', path: /^\/api\/knowledge\/connections\/conn-a\/capabilities$/, body: capabilities() },
  { method: 'GET', path: /^\/api\/knowledge\/connections\/conn-b\/capabilities$/, body: { error: 'not found' }, status: 404 },
  { method: 'GET', path: '/api/knowledge/settings', body: settingsFixture() },
  // 默认列表只返回启用连接的知识库；显式 ?connectionId= 才允许查看已停用连接。
  { method: 'GET', path: /^\/api\/knowledge\/sources(\?|$)/, body: (call) =>
    call.url.searchParams.get('connectionId') === CONN_B
      ? [{ id: SRC_B, name: '测试库', doc_count: 3, allowed: true }]
      : [{ id: SRC_A, name: '[研发空间] 产品手册', doc_count: 45, allowed: true }] },
  { method: 'GET', path: /^\/api\/knowledge\/usage/, body: { searches: 3, fetches: 2, agents: [{ name: 'SRE', workflows: ['wf-1', 'wf-2'] }, { name: 'QA', workflows: [] }], activity: [] } },
  { method: 'GET', path: /^\/api\/knowledge\/candidates/, body: [] },
  { method: 'GET', path: '/api/knowledge/submissions', body: [] },
  { method: 'GET', path: /^\/api\/knowledge\/sources\/[^/]+\/documents/, body: { documents: DOCS, total: 45, page: 1, pageSize: 20 } },
  ...extra,
];

async function openKnowledge(t, routes) {
  const app = await loadApp({ routes });
  t.after(app.close);
  // 记录 toast 文案：kbToast 会在重绘后丢失 DOM 文本，但调用序列仍可断言。
  app.evaluate('window.__kbToasts = []; window.__origToast = toast; toast = (m, k) => window.__kbToasts.push({ m, k });');
  app.evaluate("switchView('knowledge')");
  await settle(10);
  return app;
}

const callsTo = (app, method, fragment) => app.router.calls
  .filter((call) => call.method === method && `${call.path}${call.search}`.includes(fragment));
const lastCall = (app, method, fragment) => callsTo(app, method, fragment).at(-1);
const param = (call, key) => (call ? call.url.searchParams.get(key) : null);
const lastToast = (app) => app.evaluate('window.__kbToasts[window.__kbToasts.length - 1]');

function docRow(app, id) {
  return app.document.querySelector(`[data-kb-preview-doc="${id}"]`);
}

async function openDoc(app, id) {
  const btn = docRow(app, id);
  assert.ok(btn, `文档行存在：${id}`);
  btn.focus();
  btn.click();
  await settle(10);
  return btn;
}

test('知识库工作区：连接能力徽标、连接筛选与文档列表分页', async (t) => {
  // 本用例聚焦徽标/分页/作用域前缀：两个连接都返回同一份知识库列表，
  // 让已选中的 conn-a 知识库在切到 conn-b 筛选后仍然存在（前缀优先）。
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: /^\/api\/knowledge\/sources(\?|$)/, body: SOURCES },
  ]));
  const { document } = app;
  const pid = app.evaluate('state.projectFilter');

  // 连接筛选只在多连接或存在停用连接时出现。
  const filter = document.querySelector('#kbConnFilter');
  assert.ok(filter, '多连接时展示连接筛选');
  assert.equal(filter.options.length, 3);
  assert.match(filter.options[2].textContent, /测试空间（已停用）/);

  // conn-a 五项能力全部 available，conn-b 走 legacy 回退显示未知。
  const leftBadges = [...document.querySelectorAll('#kbTabBody .kb-cap-bar .hui-badge')];
  assert.equal(leftBadges.length, 5, '当前连接渲染五项能力徽标');
  assert.deepEqual(leftBadges.map((el) => el.dataset.tone), ['success', 'success', 'success', 'success', 'success']);
  assert.equal(leftBadges[1].textContent, '文档写入');
  assert.equal(leftBadges[1].title, '文档写入可用');

  // 文档列表：状态徽标覆盖已完成/失败/解析中/排队中/未知，失败行提供原因按钮。
  const rows = [...document.querySelectorAll('.kb-table tbody tr')];
  assert.equal(rows.length, DOCS.length);
  const tableText = document.querySelector('.kb-table tbody').textContent;
  assert.match(tableText, /已解析/);
  assert.match(tableText, /解析失败/);
  assert.match(tableText, /解析中/);
  assert.match(tableText, /排队中/);
  const errBtn = document.querySelector('[data-kb-err-msg]');
  assert.ok(errBtn, '解析失败行展示原因按钮');
  errBtn.click();
  await settle(2);
  const drawer = document.querySelector('#kbDrawer');
  assert.equal(drawer.hidden, false);
  assert.match(drawer.textContent, /文档解析失败原因/);
  assert.match(drawer.textContent, /字体缺失导致解析失败/);
  drawer.querySelector('[data-kb-drawer-close]').click();
  assert.equal(drawer.hidden, true);
  assert.equal(drawer.getAttribute('aria-hidden'), 'true');

  // 切换到停用连接：列表请求带 connectionId，能力徽标变为未知。
  chooseOption(document.querySelector('#kbConnFilter'), CONN_B);
  await settle(12);
  const filtered = lastCall(app, 'GET', '/api/knowledge/sources?');
  assert.equal(param(filtered, 'connectionId'), CONN_B);
  assert.equal(param(filtered, 'projectId'), pid);
  const legacyBadges = [...document.querySelectorAll('#kbTabBody .kb-cap-bar .hui-badge')];
  assert.deepEqual(legacyBadges.map((el) => el.dataset.tone), ['neutral', 'neutral', 'neutral', 'neutral', 'neutral']);
  assert.equal(legacyBadges[0].title, '库管理能力未知');

  // 回到全部连接。
  chooseOption(document.querySelector('#kbConnFilter'), '');
  await settle(12);
  assert.equal(param(lastCall(app, 'GET', '/api/knowledge/sources?'), 'connectionId'), null);

  // 搜索：按钮与 Enter 两种提交方式都会重置页码并携带 keyword。
  document.querySelector('#kbDocSearchInput').value = '安装';
  document.querySelector('#kbDocSearchBtn').click();
  await settle(10);
  let docsCall = lastCall(app, 'GET', '/documents?');
  assert.equal(param(docsCall, 'keyword'), '安装');
  assert.equal(param(docsCall, 'page'), '1');

  document.querySelector('#kbDocSearchInput').value = '接口';
  document.querySelector('#kbDocSearchInput').dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  await settle(10);
  docsCall = lastCall(app, 'GET', '/documents?');
  assert.equal(param(docsCall, 'keyword'), '接口');

  // 刷新按钮。
  const before = callsTo(app, 'GET', '/documents?').length;
  document.querySelector('#kbDocRefreshBtn').click();
  await settle(10);
  assert.equal(callsTo(app, 'GET', '/documents?').length, before + 1);

  // 分页：共 45 篇 = 3 页，上一页在首页禁用。
  assert.equal(document.querySelector('#kbDocPrevBtn').disabled, true);
  document.querySelector('#kbDocNextBtn').click();
  await settle(10);
  assert.equal(param(lastCall(app, 'GET', '/documents?'), 'page'), '2');
  assert.match(document.querySelector('#kbTabBody').textContent, /第 2 \/ 3 页/);
  document.querySelector('#kbDocPrevBtn').click();
  await settle(10);
  assert.equal(param(lastCall(app, 'GET', '/documents?'), 'page'), '1');

  // 行级下载链接拼接 connectionId 与 projectId。
  const link = document.querySelector('.kb-row-actions a[href*="/download?"]');
  assert.ok(link);
  const href = new URL(link.getAttribute('href'), 'http://127.0.0.1');
  assert.equal(href.searchParams.get('connectionId'), CONN_A);
  assert.equal(href.searchParams.get('projectId'), pid);

  // 高级配置外链把容器内网地址改写为浏览器可达地址。
  const consoleLink = document.querySelector('.kb-panel-head a[href*="18081"]');
  assert.ok(consoleLink, '存在高级配置外链');

  // 能力探测 404 后标记 legacy，多次重绘不再重复请求。
  assert.equal(callsTo(app, 'GET', '/connections/conn-b/capabilities').length, 1, '404 后不重复探测能力');

  // 作用域前缀优先于当前连接筛选：筛到 CONN_B 时带前缀的知识库仍走 CONN_A。
  chooseOption(document.querySelector('#kbConnFilter'), CONN_B);
  await settle(12);
  assert.equal(param(lastCall(app, 'GET', '/documents?'), 'connectionId'), CONN_A, 'connID::rawID 前缀优先于筛选连接');
  chooseOption(document.querySelector('#kbConnFilter'), '');
  await settle(12);
});

test('文档详情抽屉：分块分页、文本预览、手工编辑与关闭焦点归还', async (t) => {
  const chunkPage = (page) => ({
    chunks: Array.from({ length: page === 1 ? 10 : 5 }, (_, i) => ({
      chunkIndex: (page - 1) * 10 + i,
      content: `分块正文 ${(page - 1) * 10 + i}`,
      startAt: (page - 1) * 1000 + i * 10,
      endAt: (page - 1) * 1000 + i * 10 + 50,
    })),
    total: 15,
    page,
    pageSize: 10,
  });
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: docRoute('doc-1', '/chunks'), body: (call) => chunkPage(Number(call.url.searchParams.get('page') || '1')) },
    { method: 'GET', path: docRoute('doc-1', '/preview'), body: { status: 200, body: '# 安装指南\n\n<b>加粗标记</b><img src=x onerror="alert(1)">', headers: { 'content-type': 'text/markdown; charset=utf-8' } } },
    { method: 'PUT', path: docRoute('doc-1'), body: { title: '安装指南 v2', type: 'manual', content: '# 更新后的正文' } },
    { method: 'GET', path: docRoute('doc-1', '\\?'), body: { id: DOC('doc-1'), title: '安装指南', type: 'manual', parse_status: 'published', content: '# 安装指南原始正文', updatedAt: '2026-11-20T10:00:00Z' } },
  ]));
  const { document } = app;
  const pid = app.evaluate('state.projectFilter');

  // 打开抽屉：焦点进入抽屉首个可聚焦元素。
  const btn = await openDoc(app, DOC('doc-1'));
  const drawer = document.querySelector('#kbDrawer');
  assert.equal(drawer.hidden, false);
  assert.equal(drawer.getAttribute('aria-hidden'), 'false');
  assert.equal(document.activeElement, drawer.querySelector('[data-kb-drawer-close]'), '打开后焦点进入抽屉');

  const chunkCalls = callsTo(app, 'GET', DOC_PATH('doc-1') + '/chunks');
  assert.equal(chunkCalls.length, 1);
  assert.equal(param(chunkCalls[0], 'page'), '1');
  assert.equal(param(chunkCalls[0], 'pageSize'), '10');
  assert.equal(param(chunkCalls[0], 'connectionId'), CONN_A);
  assert.match(document.querySelector('#kbDocBody').textContent, /共 15 个分块/);
  assert.match(document.querySelector('#kbDocBody').textContent, /第 1 \/ 2 页/);
  assert.equal(document.querySelector('[data-kb-chunk-page="0"]').disabled, true);

  // 分块分页。
  document.querySelector('[data-kb-chunk-page="2"]').click();
  await settle(10);
  assert.equal(param(lastCall(app, 'GET', DOC_PATH('doc-1') + '/chunks'), 'page'), '2');
  assert.match(document.querySelector('#kbDocBody').textContent, /第 2 \/ 2 页/);
  assert.match(document.querySelector('#kbDocBody').textContent, /分块正文 14/);
  assert.match(document.querySelector('#kbDocBody').textContent, /字符 1040–1090/);

  // 原文预览：text/* 走 pre 安全转义，不注入 HTML。
  document.querySelector('[data-kb-doc-view="preview"]').click();
  await settle(10);
  const pre = document.querySelector('#kbDocBody pre.kb-doc-pre');
  assert.ok(pre, '文本预览使用 pre 渲染');
  assert.match(pre.textContent, /# 安装指南/);
  assert.match(pre.textContent, /<b>加粗标记<\/b>/, '预览保留标签原文');
  assert.equal(pre.querySelector('b'), null, '标签不被解析为元素');
  assert.equal(pre.querySelector('img'), null, '危险标签同样不落地为元素');
  assert.equal(param(lastCall(app, 'GET', DOC_PATH('doc-1') + '/preview'), 'connectionId'), CONN_A);

  // 元信息徽标。
  const bodyText = document.querySelector('#kbDocBody').textContent;
  assert.match(bodyText, /manual/);
  assert.match(bodyText, /已发布/);
  assert.match(bodyText, /更新于 2026-11-20 10:00:00/);

  // Escape 关闭：抽屉 inert + aria-hidden，焦点归还触发行。
  document.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  await settle(2);
  assert.equal(drawer.hidden, true);
  assert.equal(drawer.hasAttribute('inert'), true);
  assert.equal(drawer.getAttribute('aria-hidden'), 'true');
  assert.equal(document.activeElement, btn, '焦点归还「查看」按钮');

  // 重新打开进入手工文档编辑：detail 内联正文直接填入，保存走 PUT 并刷新标题。
  await openDoc(app, DOC('doc-1'));
  document.querySelector('[data-kb-doc-view="edit"]').click();
  await settle(6);
  const content = document.querySelector('#kbDocEditContent');
  assert.equal(content.value, '# 安装指南原始正文');
  document.querySelector('#kbDocEditTitle').value = '安装指南 v2';
  content.value = '# 更新后的正文';
  document.querySelector('#kbDocEditPublish').checked = false;
  document.querySelector('#kbDocSaveBtn').click();
  await settle(12);
  const put = lastCall(app, 'PUT', DOC_PATH('doc-1'));
  assert.ok(put, '保存手工文档发出 PUT');
  assert.deepEqual(put.body, { title: '安装指南 v2', content: '# 更新后的正文', publish: false });
  assert.equal(param(put, 'connectionId'), CONN_A);
  assert.equal(param(put, 'projectId'), pid);
  assert.equal(document.querySelector('#kbDrawer .kb-drawer-head h3').textContent, '安装指南 v2');

  // 标题为空时不提交。
  const before = callsTo(app, 'PUT', DOC_PATH('doc-1')).length;
  document.querySelector('#kbDocEditTitle').value = '   ';
  document.querySelector('#kbDocSaveBtn').click();
  await settle(4);
  assert.equal(callsTo(app, 'PUT', DOC_PATH('doc-1')).length, before);
  assert.match(lastToast(app).m, /请填写文档标题/);

  // 遮罩点击同样关闭。
  document.querySelector('#kbDrawerScrim').click();
  await settle(2);
  assert.equal(drawer.hidden, true);
});

test('文档详情降级：详情 404 静默、分块回退旧接口、图片/PDF/二进制预览', async (t) => {
  const app = await openKnowledge(t, ROUTES([
    // 通用兜底先注册，具体路径后注册以覆盖它。
    { method: 'GET', path: /^\/api\/knowledge\/documents\/[^/]+\/chunks/, body: { chunks: [{ chunkIndex: 0, content: '通用分块' }], total: 1, page: 1, pageSize: 10 } },
    { method: 'GET', path: /^\/api\/knowledge\/documents\//, body: { title: '文档', type: 'file', parse_status: 'completed' } },
    // doc-img：分块 404 → 回退旧版 /api/knowledge/document。
    { method: 'GET', path: docRoute('doc-img', '/chunks'), body: { error: 'not found' }, status: 404 },
    { method: 'GET', path: /^\/api\/knowledge\/document\?/, body: { lines: [{ text: '旧版第一行' }, { text: '旧版第二行' }] } },
    // 自定义 content-type 需把 status/body/headers 一起放进回复对象。
    { method: 'GET', path: docRoute('doc-img', '/preview'), body: { status: 200, body: 'PNGDATA', headers: { 'content-type': 'image/png' } } },
    { method: 'GET', path: docRoute('doc-pdf', '/preview'), body: { status: 200, body: '%PDF-1.4', headers: { 'content-type': 'application/pdf' } } },
    { method: 'GET', path: docRoute('doc-bin', '/preview'), body: { status: 200, body: 'PK', headers: { 'content-type': 'application/zip' } } },
    { method: 'GET', path: docRoute('doc-405', '/preview'), body: { error: 'method not allowed' }, status: 405 },
    // doc-img 详情 404：抽屉保持可用，分块阶段再回退。
    { method: 'GET', path: docRoute('doc-img', '\\?'), body: { error: 'not found' }, status: 404 },
  ]));
  const { document } = app;

  // 详情 404 + 分块 404 → legacy 原文回退。
  await openDoc(app, DOC('doc-img'));
  assert.match(document.querySelector('#kbDocBody').textContent, /旧版第一行/);
  assert.match(document.querySelector('#kbDocBody').textContent, /旧版原文分块/);
  const legacy = lastCall(app, 'GET', '/api/knowledge/document?');
  assert.equal(param(legacy, 'source'), SRC_A);
  assert.equal(param(legacy, 'path'), DOC('doc-img'));

  // 图片预览：Blob + objectURL，切换到其他视图时释放。
  document.querySelector('[data-kb-doc-view="preview"]').click();
  await settle(10);
  const img = document.querySelector('#kbDocBody img.kb-preview-img');
  assert.ok(img, '图片走 img 预览');
  assert.equal(img.getAttribute('src'), 'blob:test');
  document.querySelector('[data-kb-doc-view="chunks"]').click();
  await settle(4);
  assert.equal(document.querySelector('#kbDocBody img'), null);

  await openDoc(app, DOC('doc-pdf'));
  document.querySelector('[data-kb-doc-view="preview"]').click();
  await settle(10);
  assert.ok(document.querySelector('#kbDocBody iframe.kb-preview-frame'), 'PDF 走 iframe 预览');

  await openDoc(app, DOC('doc-bin'));
  document.querySelector('[data-kb-doc-view="preview"]').click();
  await settle(10);
  assert.match(document.querySelector('#kbDocBody').textContent, /不支持在线预览/);

  await openDoc(app, DOC('doc-405'));
  document.querySelector('[data-kb-doc-view="preview"]').click();
  await settle(10);
  assert.match(document.querySelector('#kbDocBody').textContent, /原文预览接口尚未合入/);
});

test('503 capability_unavailable：连接能力就地降级并禁用写操作', async (t) => {
  const app = await openKnowledge(t, ROUTES([
    { method: 'POST', path: /^\/api\/knowledge\/sources\/[^/]+\/upload/, status: 503, body: { error: '文档写入不可用', message: '文档写入不可用', code: 'capability_unavailable', capability: 'doc_write', connectionId: CONN_A, reason: 'API Key 缺少写权限' } },
  ]));
  const { document } = app;

  // 导入抽屉：上传失败后能力缓存刷新为不可用，导入/删除按钮禁用。
  document.querySelector('#kbImportDocBtn').click();
  await settle(4);
  const drawer = document.querySelector('#kbDrawer');
  assert.equal(drawer.hidden, false);
  Object.defineProperty(drawer.querySelector('#kbImportFiles'), 'files', {
    configurable: true,
    value: [new app.window.File(['# 内容'], 'guide.md', { type: 'text/markdown' })],
  });
  drawer.querySelector('#kbImportUploadBtn').click();
  await settle(14);

  const badge = [...document.querySelectorAll('#kbTabBody .kb-cap-bar .hui-badge')]
    .find((el) => el.textContent === '文档写入');
  assert.equal(badge.dataset.tone, 'error', '文档写入能力降级为不可用');
  assert.equal(badge.title, 'API Key 缺少写权限', '徽标提示后端返回的原因');
  assert.equal(document.querySelector('#kbImportDocBtn').disabled, true);
  assert.equal(document.querySelector('#kbDeleteSourceBtn').disabled, false, '库管理能力不受影响');
  assert.equal(document.querySelector('[data-kb-delete-doc]').disabled, true, '文档删除同样按 doc_write 门禁');
  assert.match(drawer.querySelector('#kbImportStatus').textContent, /成功 0 个，失败 1 个/);
  assert.match((app.evaluate('window.__kbToasts') || []).map((item) => item.m).join('\n'), /文档写入不可用/);

  // 门禁拦截：即使再次点击也不会再请求上传。
  const before = callsTo(app, 'POST', '/upload').length;
  document.querySelector('#kbImportDocBtn').click();
  await settle(4);
  assert.equal(callsTo(app, 'POST', '/upload').length, before);
});

test('知识库 CRUD：模型分流、显式 connectionId、PATCH 404 回退 PUT、删除确认', async (t) => {
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: /^\/api\/knowledge\/models\?/, body: (call) => {
      if (call.url.searchParams.get('connectionId') === CONN_B) return { status: 500, body: { error: 'upstream unavailable' } };
      return [{ id: 'm-emb', name: 'bge-m3', type: 'Embedding' }, { id: 'm-qa', name: 'qwen-plus', type: 'KnowledgeQA' }];
    } },
    { method: 'POST', path: '/api/knowledge/sources', body: { id: `${CONN_A}::kb-new`, name: '新产品库' } },
    { method: 'PATCH', path: /^\/api\/knowledge\/sources\/[^/]+\?/, status: 404, body: { error: 'not found' } },
    { method: 'PUT', path: /^\/api\/knowledge\/sources\/[^/]+\?/, body: { ok: true } },
    { method: 'DELETE', path: /^\/api\/knowledge\/sources\/[^/]+\?/, body: { ok: true } },
    { method: 'GET', path: /^\/api\/knowledge\/sources\/[^/]+\?/, body: { name: '产品手册', description: '旧描述' } },
  ]));
  const { document } = app;
  const pid = app.evaluate('state.projectFilter');
  const drawer = document.querySelector('#kbDrawer');

  // 新建：模型按 Embedding / KnowledgeQA 分流。
  document.querySelector('#kbCreateSourceBtn').click();
  await settle(8);
  assert.equal(drawer.hidden, false);
  assert.match(document.querySelector('#kbCreateModelNote').textContent, /Embedding 1 个 · KnowledgeQA 1 个/);
  const embSelect = document.querySelector('#kbCreateEmbedding');
  // Embedding 列表非空时必选并默认选中第一个，不再保留「使用上游默认」空选项。
  assert.equal(embSelect.options.length, 1);
  assert.equal(embSelect.value, 'm-emb');
  assert.equal(embSelect.dataset.required, '1');
  assert.equal(embSelect.querySelector('option[value=""]'), null);
  assert.equal(document.querySelector('#kbCreateEmbeddingManual').hidden, true);
  chooseOption(document.querySelector('#kbCreateSummary'), 'm-qa');

  // 缺名称不提交。
  document.querySelector('#kbCreateSubmit').click();
  await settle(4);
  assert.equal(callsTo(app, 'POST', '/api/knowledge/sources').length, 0);
  assert.match(lastToast(app).m, /请填写知识库名称/);

  document.querySelector('#kbCreateName').value = '新产品库';
  document.querySelector('#kbCreateDesc').value = '面向新产品的 FAQ';
  document.querySelector('#kbCreateSubmit').click();
  await settle(12);
  const create = lastCall(app, 'POST', '/api/knowledge/sources');
  assert.ok(create);
  assert.equal(param(create, 'projectId'), pid);
  assert.deepEqual(create.body, {
    connectionId: CONN_A,
    name: '新产品库',
    description: '面向新产品的 FAQ',
    embedding_model_id: 'm-emb',
    summary_model_id: 'm-qa',
  });
  assert.equal(drawer.hidden, true, '创建成功后关闭抽屉');

  // 切到 conn-b 时模型列表加载失败，回退手工输入。
  document.querySelector('#kbCreateSourceBtn').click();
  await settle(8);
  chooseOption(document.querySelector('#kbCreateConn'), CONN_B);
  await settle(8);
  assert.match(document.querySelector('#kbCreateModelNote').textContent, /模型列表加载失败/);
  assert.equal(document.querySelector('#kbCreateEmbeddingManual').hidden, false);
  document.querySelector('#kbCreateEmbeddingManual').value = 'custom-embedding';
  document.querySelector('#kbCreateName').value = '测试空间新库';
  document.querySelector('#kbCreateSubmit').click();
  await settle(12);
  const createB = lastCall(app, 'POST', '/api/knowledge/sources');
  assert.equal(createB.body.connectionId, CONN_B);
  assert.equal(createB.body.embedding_model_id, 'custom-embedding');
  assert.equal(createB.body.summary_model_id, '');

  // 编辑：详情预填，PATCH 404 后回退 PUT，均带显式 connectionId。
  document.querySelector('#kbEditSourceBtn').click();
  await settle(8);
  assert.equal(document.querySelector('#kbEditName').value, '产品手册');
  assert.equal(document.querySelector('#kbEditDesc').value, '旧描述');
  document.querySelector('#kbEditName').value = '产品手册（新版）';
  document.querySelector('#kbEditDesc').value = '更新后的描述';
  document.querySelector('#kbEditSubmit').click();
  await settle(12);
  const patch = lastCall(app, 'PATCH', '/api/knowledge/sources/');
  const put = lastCall(app, 'PUT', '/api/knowledge/sources/');
  assert.equal(callsTo(app, 'PATCH', '/api/knowledge/sources/').length, 1);
  assert.equal(param(patch, 'connectionId'), CONN_A);
  assert.equal(param(patch, 'projectId'), pid);
  assert.equal(param(put, 'connectionId'), CONN_A);
  assert.deepEqual(put.body, { name: '产品手册（新版）', description: '更新后的描述' });
  assert.equal(drawer.hidden, true);

  // 删除知识库：取消不发请求，确认后 DELETE 携带 connectionId。
  document.querySelector('#kbDeleteSourceBtn').click();
  await settle(4);
  assert.match(document.querySelector('#confirmModal').textContent, /删除知识库/);
  document.querySelector('#confirmCancelBtn').click();
  await settle(6);
  assert.equal(callsTo(app, 'DELETE', '/api/knowledge/sources/').length, 0);

  document.querySelector('#kbDeleteSourceBtn').click();
  await settle(4);
  document.querySelector('#confirmOkBtn').click();
  await settle(12);
  const del = lastCall(app, 'DELETE', '/api/knowledge/sources/');
  assert.ok(del, '确认后删除知识库');
  assert.equal(param(del, 'connectionId'), CONN_A);
});

test('三模式导入：文件顺序上传/超限拦截、URL 校验、手工 Markdown', async (t) => {
  const uploads = [];
  const app = await openKnowledge(t, ROUTES([
    { method: 'POST', path: /^\/api\/knowledge\/sources\/[^/]+\/upload/, body: (call) => {
      const name = call.body && call.body.get ? call.body.get('file').name : 'unknown';
      uploads.push(name);
      if (name !== 'a.md' && name !== 'c.md') return { status: 500, body: { error: '解析服务超时' } };
      return { ok: true, id: 'doc-new' };
    } },
    { method: 'POST', path: /^\/api\/knowledge\/sources\/[^/]+\/url/, body: { ok: true } },
    { method: 'POST', path: /^\/api\/knowledge\/sources\/[^/]+\/manual/, body: { ok: true, id: 'doc-manual' } },
  ]));
  const { document } = app;
  const pid = app.evaluate('state.projectFilter');

  document.querySelector('#kbImportDocBtn').click();
  await settle(4);
  const drawer = document.querySelector('#kbDrawer');
  assert.equal(drawer.hidden, false);

  // 分段切换（文件 / 链接 / 手工）。
  const segs = [...drawer.querySelectorAll('[data-kb-import-mode]')];
  assert.deepEqual(segs.map((el) => el.textContent), ['本地上传', '在线链接', '手工录入']);
  segs[1].click();
  assert.equal(drawer.querySelector('[data-kb-import-pane="url"]').hidden, false);
  assert.equal(drawer.querySelector('[data-kb-import-pane="file"]').hidden, true);
  segs[0].click();
  assert.equal(drawer.querySelector('[data-kb-import-pane="file"]').hidden, false);

  // 空选择与超过 50 MiB 都在本地拦截。
  const setFiles = (files) => Object.defineProperty(drawer.querySelector('#kbImportFiles'), 'files', { configurable: true, value: files });
  drawer.querySelector('#kbImportUploadBtn').click();
  await settle(2);
  assert.match(drawer.querySelector('#kbImportStatus').textContent, /请选择要上传的文件/);

  setFiles([new app.window.File([new Uint8Array(51 * 1024 * 1024)], 'huge.pdf', { type: 'application/pdf' })]);
  drawer.querySelector('#kbImportUploadBtn').click();
  await settle(2);
  assert.match(drawer.querySelector('#kbImportStatus').textContent, /以下文件超过 50 MiB：huge\.pdf/);
  assert.equal(uploads.length, 0);

  // 顺序上传：两个都失败时汇总失败文件名并保持抽屉打开。
  setFiles([
    new app.window.File(['# b'], 'b.md', { type: 'text/markdown' }),
    new app.window.File(['# d'], 'd.md', { type: 'text/markdown' }),
  ]);
  drawer.querySelector('#kbImportUploadBtn').click();
  await settle(16);
  assert.deepEqual(uploads, ['b.md', 'd.md']);
  assert.match(drawer.querySelector('#kbImportStatus').textContent, /上传完成：成功 0 个，失败 2 个（b\.md、d\.md）/);
  assert.equal(param(lastCall(app, 'POST', '/upload'), 'connectionId'), CONN_A);

  // 成功的上传会关闭抽屉并刷新文档列表。
  const docsBefore = callsTo(app, 'GET', '/documents?').length;
  setFiles([new app.window.File(['# a'], 'a.md', { type: 'text/markdown' })]);
  drawer.querySelector('#kbImportUploadBtn').click();
  await settle(16);
  assert.equal(drawer.hidden, true);
  assert.equal(callsTo(app, 'GET', '/documents?').length, docsBefore + 1, '上传成功后刷新文档列表');

  // 在线链接：非法协议本地拦截，合法链接提交 JSON。
  document.querySelector('#kbImportDocBtn').click();
  await settle(4);
  const drawer2 = document.querySelector('#kbDrawer');
  drawer2.querySelector('[data-kb-import-mode="url"]').click();
  drawer2.querySelector('#kbImportUrl').value = 'ftp://example.com/doc';
  drawer2.querySelector('#kbImportUrlBtn').click();
  await settle(2);
  assert.match(drawer2.querySelector('#kbImportStatus').textContent, /请输入合法的 http\/https 链接/);
  assert.equal(callsTo(app, 'POST', '/url').length, 0);

  drawer2.querySelector('#kbImportUrl').value = 'https://example.com/doc.html';
  drawer2.querySelector('#kbImportUrlBtn').click();
  await settle(14);
  const urlCall = lastCall(app, 'POST', '/url');
  assert.ok(urlCall);
  assert.deepEqual(urlCall.body, { url: 'https://example.com/doc.html' });
  assert.equal(param(urlCall, 'connectionId'), CONN_A);
  assert.equal(param(urlCall, 'projectId'), pid);

  // 手工录入：标题/正文必填，创建时携带 publish。
  document.querySelector('#kbImportDocBtn').click();
  await settle(4);
  const drawer3 = document.querySelector('#kbDrawer');
  drawer3.querySelector('[data-kb-import-mode="manual"]').click();
  drawer3.querySelector('#kbImportManualBtn').click();
  await settle(2);
  assert.match(drawer3.querySelector('#kbImportStatus').textContent, /请填写文档标题/);
  drawer3.querySelector('#kbImportTitle').value = '部署说明';
  drawer3.querySelector('#kbImportManualBtn').click();
  await settle(2);
  assert.match(drawer3.querySelector('#kbImportStatus').textContent, /请填写 Markdown 正文/);
  drawer3.querySelector('#kbImportContent').value = '# 部署说明\n\n步骤一';
  drawer3.querySelector('#kbImportPublish').checked = false;
  drawer3.querySelector('#kbImportManualBtn').click();
  await settle(14);
  const manual = lastCall(app, 'POST', '/manual');
  assert.deepEqual(manual.body, { title: '部署说明', content: '# 部署说明\n\n步骤一', publish: false });
  assert.equal(param(manual, 'connectionId'), CONN_A);
  assert.equal(param(manual, 'projectId'), pid);
  assert.equal(drawer3.hidden, true);
});

test('文档行操作：重解析、编辑入参、分页回退与删除确认', async (t) => {
  let docPage = '1';
  const app = await openKnowledge(t, ROUTES([
    // 单连接场景：文档行未带作用域前缀时回落到唯一连接。
    { method: 'GET', path: '/api/knowledge/settings', body: settingsFixture({ connections: [settingsFixture().connections[0]] }) },
    { method: 'GET', path: /^\/api\/knowledge\/sources\/[^/]+\/documents/, body: (call) => {
      docPage = call.url.searchParams.get('page') || '1';
      if (docPage === '2') return { documents: [DOCS[0]], total: 21, page: 2, pageSize: 20 };
      return { documents: DOCS, total: 21, page: 1, pageSize: 20 };
    } },
    { method: 'POST', path: /^\/api\/knowledge\/documents\/[^/]+\/reparse/, body: { ok: true } },
    { method: 'DELETE', path: /^\/api\/knowledge\/documents\/[^/]+\?/, body: { ok: true } },
    { method: 'GET', path: docRoute('doc-1', '/chunks'), body: { chunks: [{ chunkIndex: 0, content: '正文' }], total: 1, page: 1, pageSize: 10 } },
    { method: 'GET', path: docRoute('doc-1', '\\?'), body: { title: '安装指南', type: 'manual', content: '# 手册' } },
  ]));
  const { document } = app;

  // 重解析：POST + connectionId 后刷新列表。
  document.querySelector(`[data-kb-reparse-doc="${DOC('doc-2')}"]`).click();
  await settle(12);
  const reparse = lastCall(app, 'POST', DOC_PATH('doc-2') + '/reparse');
  assert.ok(reparse);
  assert.equal(param(reparse, 'connectionId'), CONN_A);

  // 行内「编辑」直接进入手工文档编辑视图。
  const editBtn = document.querySelector(`[data-kb-edit-doc="${DOC('doc-1')}"]`);
  assert.ok(editBtn);
  editBtn.focus();
  editBtn.click();
  await settle(12);
  assert.equal(document.querySelector('#kbDocEditContent').value, '# 手册');
  assert.equal(document.querySelector('[data-kb-doc-view="edit"]').getAttribute('aria-selected'), 'true');
  document.querySelector('#kbDrawer [data-kb-drawer-close]').click();
  await settle(2);

  // 翻到第 2 页（仅 1 篇）后删除，页码回退到 1。
  document.querySelector('#kbDocNextBtn').click();
  await settle(10);
  assert.equal(docPage, '2');
  document.querySelector(`[data-kb-delete-doc="${DOC('doc-1')}"]`).click();
  await settle(4);
  assert.match(document.querySelector('#confirmModal').textContent, /删除文档/);
  document.querySelector('#confirmCancelBtn').click();
  await settle(6);
  assert.equal(callsTo(app, 'DELETE', '/documents/').length, 0);

  document.querySelector(`[data-kb-delete-doc="${DOC('doc-1')}"]`).click();
  await settle(4);
  document.querySelector('#confirmOkBtn').click();
  await settle(14);
  const del = lastCall(app, 'DELETE', DOC_PATH('doc-1'));
  assert.ok(del);
  assert.equal(param(del, 'connectionId'), CONN_A);
  assert.equal(param(lastCall(app, 'GET', '/documents?'), 'page'), '1', '删除末页最后一篇后回到上一页');
});

test('接入配置页：连接探测、移除、一键接入、审计标签与保存配置', async (t) => {
  let providerChecks = 0;
  let checkBody = null;
  const app = await openKnowledge(t, ROUTES([
    { method: 'POST', path: '/api/knowledge/check', body: (call) => {
      checkBody = call.body;
      if (call.body && call.body.connectionId === CONN_B) return { ok: false, error: '连接超时' };
      if (call.body && call.body.provider) {
        providerChecks += 1;
        return providerChecks === 1 ? { ok: true, sources: [1, 2], latencyMs: 12 } : { ok: false, error: '鉴权失败' };
      }
      return capabilities({ reparse: 'unavailable' });
    } },
    { method: 'POST', path: '/api/knowledge/weknora/provision', body: (call) => ({ message: call.body.mode === 'managed' ? '内置 WeKnora 已就绪' : '外部 WeKnora 已链接' }) },
    { method: 'DELETE', path: /^\/api\/knowledge\/connections\/[^/]+$/, body: { ok: true } },
    { method: 'PUT', path: '/api/knowledge/settings', body: { ok: true } },
  ]));
  const { document } = app;
  const pid = app.evaluate('state.projectFilter');
  // 重新加载带上「已检测到内置 WeKnora」与项目授权范围。
  app.router.get('/api/knowledge/settings', settingsFixture({
    detectedWeKnoraUrl: 'http://weknora-app:8080',
    projectSources: {},
  }));
  app.router.get(/^\/api\/knowledge\/usage/, {
    agents: [{ name: 'SRE', workflows: ['wf-1', 'wf-2'] }, { name: 'QA', workflows: [] }],
    activity: [
      { tool: 'admin:kb.create', query: '创建知识库', hits: 0, ok: true, latencyMs: 12 },
      { tool: 'admin:conn.provision', query: 'wk-provision', hits: 0, ok: true, latencyMs: 9 },
      { tool: 'admin:unknown.action', query: '未知动作', hits: 1, ok: false, latencyMs: 30 },
      { tool: 'knowledge_search', query: '部署', hits: 3, ok: true, latencyMs: 40 },
    ],
  });
  app.evaluate('loadKnowledgeView()');
  await settle(14);

  document.querySelector('[data-kb-tab="settings"]').click();
  await settle(8);

  // 连接表 + 能力徽标 + 管理审计标签。
  const tableText = document.querySelector('#kbTabBody').textContent;
  assert.match(tableText, /已连接的知识库实例（2）/);
  assert.match(tableText, /研发空间/);
  assert.equal(document.querySelectorAll('#kbTabBody .kb-cap-bar').length, 2, '两个连接各有能力徽标栏');
  const auditBadge = [...document.querySelectorAll('#kbTabBody .hui-badge')].find((el) => el.textContent === '创建知识库');
  assert.ok(auditBadge, 'admin:* 审计动作渲染为可读徽标');
  assert.equal(auditBadge.dataset.tone, 'info');
  assert.equal(auditBadge.title, 'admin:kb.create');
  assert.ok([...document.querySelectorAll('#kbTabBody .hui-badge')].some((el) => el.textContent === 'admin:unknown.action'));
  assert.match(document.querySelector('#kbTabBody').textContent, /knowledge_search/);

  // 活动表：查询/目标表头，admin:* 的命中与耗时以 — 占位。
  const activityTable = [...document.querySelectorAll('#kbTabBody table')]
    .find((tbl) => /查询 \/ 目标/.test(tbl.querySelector('thead')?.textContent || ''));
  assert.ok(activityTable, '活动表表头为「查询 / 目标」');
  const provisionBadge = [...activityTable.querySelectorAll('.hui-badge')]
    .find((el) => el.textContent === '自动初始化 WeKnora');
  assert.ok(provisionBadge, 'admin:conn.provision 渲染为可读标签');
  assert.equal(provisionBadge.title, 'admin:conn.provision');
  const provisionRow = provisionBadge.closest('tr');
  assert.equal(provisionRow.children[2].textContent.trim(), '—', '管理审计不计命中');
  assert.equal(provisionRow.children[4].textContent.trim(), '—', '管理审计不计耗时');
  const searchRow = [...activityTable.querySelectorAll('tbody tr')]
    .find((tr) => tr.textContent.includes('knowledge_search'));
  assert.equal(searchRow.children[2].textContent.trim(), '3', '检索行保留命中数');
  assert.match(searchRow.children[4].textContent, /40 ms/);

  // 探测成功：POST body 携带 connectionId，能力徽标刷新。
  document.querySelector(`[data-kb-probe-conn="${CONN_A}"]`).click();
  await settle(14);
  assert.deepEqual(checkBody, { connectionId: CONN_A });
  const reparseBadge = [...document.querySelectorAll('#kbTabBody .kb-cap-bar .hui-badge')]
    .find((el) => el.textContent === '重解析');
  assert.equal(reparseBadge.dataset.tone, 'error', '探测返回 unavailable 后徽标变为不可用');

  // 探测失败：ok:false 只提示不崩溃，按钮恢复可用。
  document.querySelector(`[data-kb-probe-conn="${CONN_B}"]`).click();
  await settle(14);
  assert.equal(lastToast(app).m, '连接超时');
  assert.equal(document.querySelector(`[data-kb-probe-conn="${CONN_B}"]`).disabled, false);

  // 一键接入：托管模式。
  document.querySelector('#kbProvisionManagedBtn').click();
  await settle(14);
  let prov = lastCall(app, 'POST', '/weknora/provision');
  assert.deepEqual(prov.body, { mode: 'managed', writeEnabled: true });

  // 外部模式：切换单选后展示表单并提交全部凭据。
  const radios = [...document.querySelectorAll('input[name="kbProvisionMode"]')];
  radios[1].checked = true;
  radios[1].dispatchEvent(new app.window.Event('change', { bubbles: true }));
  assert.equal(document.querySelector('#kbProvExternalBox').hidden, false);
  assert.equal(document.querySelector('#kbProvManagedBox').hidden, true);
  document.querySelector('#kbExtName').value = '测试空间';
  document.querySelector('#kbExtUrl').value = 'https://kb.example.com';
  document.querySelector('#kbExtToken').value = 'sk-test';
  document.querySelector('#kbExtEmail').value = 'qa@example.com';
  document.querySelector('#kbExtPass').value = 'secret-pass';
  document.querySelector('#kbProvisionExternalBtn').click();
  await settle(14);
  prov = lastCall(app, 'POST', '/weknora/provision');
  assert.deepEqual(prov.body, {
    mode: 'external',
    name: '测试空间',
    url: 'https://kb.example.com',
    token: 'sk-test',
    email: 'qa@example.com',
    password: 'secret-pass',
    writeEnabled: true,
  });

  // 一键填入内置 WeKnora。
  document.querySelector('#kbFillWeKnoraBtn').click();
  assert.equal(document.querySelector('#kbCfgProvider').value, 'weknora');
  assert.equal(document.querySelector('#kbCfgUrl').value, 'http://weknora-app:8080');
  assert.equal(document.querySelector('#kbCfgEnabled').checked, true);

  // 检查连接：成功提示知识库数量，失败展示错误。
  document.querySelector('#kbCheckBtn').click();
  await settle(12);
  assert.equal(document.querySelector('#kbConfigFeedback').textContent, '连接成功：发现 2 个知识库（12 ms）');
  document.querySelector('#kbCheckBtn').click();
  await settle(12);
  assert.equal(document.querySelector('#kbConfigFeedback').textContent, '鉴权失败');

  // 保存配置：勾选项目范围后写入 projectSources。
  const scope = document.querySelector(`input[name="kbProjectScope"][value="${SRC_A}"]`);
  assert.ok(scope);
  assert.equal(scope.checked, false, '项目初始未授权任何知识库');
  scope.click();
  assert.equal(scope.checked, true, '勾选后纳入项目授权范围');
  document.querySelector('#kbCfgWrite').checked = false;
  document.querySelector('#kbSaveCfgBtn').click();
  await settle(14);
  const put = lastCall(app, 'PUT', '/api/knowledge/settings');
  assert.ok(put);
  assert.equal(put.body.provider, 'weknora');
  assert.equal(put.body.writeEnabled, false);
  assert.deepEqual(put.body.projectSources, { [pid]: [SRC_A] });
  assert.ok(callsTo(app, 'GET', '/api/knowledge/settings').length >= 2, '保存后重新加载配置');

  // 移除连接：取消不发请求，确认后 DELETE。
  document.querySelector(`[data-kb-del-conn="${CONN_B}"]`).click();
  await settle(4);
  assert.match(document.querySelector('#confirmModal').textContent, /移除知识库连接/);
  document.querySelector('#confirmCancelBtn').click();
  await settle(6);
  assert.equal(callsTo(app, 'DELETE', '/api/knowledge/connections/').length, 0);
  document.querySelector(`[data-kb-del-conn="${CONN_B}"]`).click();
  await settle(4);
  document.querySelector('#confirmOkBtn').click();
  await settle(14);
  assert.ok(lastCall(app, 'DELETE', '/api/knowledge/connections/conn-b'));
});

test('排障调试：检索命中、查看原文与错误提示', async (t) => {
  let searchFail = false;
  const app = await openKnowledge(t, ROUTES([
    { method: 'POST', path: '/api/knowledge/search', body: () => {
      if (searchFail) return { status: 500, body: { error: '上游检索超时' } };
      return { result: { results: [
        { title: '安装指南', path: 'docs/install.md', source: SRC_A, score: 0.91234, snippet: '执行 start.sh' },
        { title: null, path: 'docs/faq.md', source: SRC_A, score: 0.5, snippet: '常见问题' },
      ] }, latencyMs: 37 };
    } },
    { method: 'GET', path: /^\/api\/knowledge\/document\?/, body: () => ({ lines: [] }) },
  ]));
  const { document } = app;

  document.querySelector('[data-kb-tab="search"]').click();
  await settle(4);
  const checks = [...document.querySelectorAll('input[name="kbSearchSource"]')];
  assert.equal(checks.length, 1, '仅授权知识库出现在检索范围');
  checks[0].checked = true;
  document.querySelector('#kbSearchQueryInput').value = '如何安装';
  document.querySelector('#kbSearchRunBtn').click();
  await settle(12);
  const search = lastCall(app, 'POST', '/api/knowledge/search');
  assert.equal(search.body.query, '如何安装');
  assert.deepEqual(search.body.sources, [SRC_A]);
  assert.match(document.querySelector('#kbTabBody').textContent, /共命中 2 条，耗时 37 ms/);
  assert.match(document.querySelector('#kbTabBody').textContent, /score 0\.912/);
  assert.match(document.querySelector('#kbTabBody').textContent, /常见问题/);

  // 查看原文：旧版 document 接口按 source+path 拉取并渲染 pre。
  document.querySelector('[data-kb-open-hit-path]').click();
  await settle(10);
  const drawer = document.querySelector('#kbDrawer');
  assert.equal(drawer.hidden, false);
  assert.match(drawer.textContent, /暂无正文/);
  const docCall = lastCall(app, 'GET', '/api/knowledge/document?');
  assert.equal(param(docCall, 'source'), SRC_A);
  assert.equal(param(docCall, 'path'), 'docs/install.md');
  document.querySelector('#kbDrawer [data-kb-drawer-close]').click();
  await settle(2);

  // 检索失败提示。
  searchFail = true;
  document.querySelector('#kbSearchRunBtn').click();
  await settle(12);
  assert.equal(lastToast(app).m, '上游检索超时');

  // 命中为空时给出空态。
  searchFail = false;
  app.router.post('/api/knowledge/search', { result: { results: [] }, latencyMs: 5 });
  document.querySelector('#kbSearchRunBtn').click();
  await settle(12);
  assert.match(document.querySelector('#kbTabBody').textContent, /未检索到匹配片段/);
});

test('知识沉淀：候选入队、审核保存/驳回/发布与只读态', async (t) => {
  const submissions = [
    { id: 'sub-1', title: '部署 SOP', source: SRC_A, status: 'pending', updatedAt: '2026-11-20T09:00:00Z' },
    { id: 'sub-2', title: '已发布 SOP', source: SRC_A, status: 'published', updatedAt: '2026-11-19T09:00:00Z' },
  ];
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: '/api/knowledge/candidates', body: [
      { artifact: { id: 'art-1', name: 'knowledge.md', runId: 'run-1' }, submissionId: 'sub-1' },
      { artifact: { id: 'art-2', name: 'deploy.md', runId: 'run-2' } },
    ] },
    { method: 'GET', path: '/api/knowledge/submissions', body: submissions },
    { method: 'GET', path: /^\/api\/knowledge\/submissions\/sub-1$/, body: { id: 'sub-1', title: '部署 SOP', source: SRC_A, status: 'pending', path: 'sop/deploy.md', content: '# 部署 SOP' } },
    { method: 'GET', path: /^\/api\/knowledge\/submissions\/sub-2$/, body: { id: 'sub-2', title: '已发布 SOP', source: SRC_A, status: 'published', content: '# 已发布' } },
    { method: 'POST', path: '/api/knowledge/submissions', body: { id: 'sub-3' } },
    { method: 'PUT', path: /^\/api\/knowledge\/submissions\/sub-1$/, body: { ok: true } },
    { method: 'POST', path: /^\/api\/knowledge\/submissions\/sub-1\/publish$/, body: { ok: true } },
    { method: 'POST', path: /^\/api\/knowledge\/submissions\/sub-1\/reject$/, body: { ok: true } },
  ]));
  const { document } = app;
  const pid = app.evaluate('state.projectFilter');

  document.querySelector('[data-kb-tab="distill"]').click();
  await settle(4);
  const tabText = document.querySelector('#kbTabBody').textContent;
  assert.match(tabText, /知识候选/);
  assert.match(tabText, /已入队/);
  assert.match(tabText, /部署 SOP/);

  // 加入审核队列。
  document.querySelector('[data-kb-submit-cand="art-2"]').click();
  await settle(14);
  const submit = lastCall(app, 'POST', '/api/knowledge/submissions');
  assert.deepEqual(submit.body, { runId: 'run-2', artifactId: 'art-2', projectId: pid });

  // 状态筛选触发重渲染。
  chooseOption(document.querySelector('#kbSubStatusFilter'), 'published');
  await settle(4);
  assert.doesNotMatch(document.querySelector('#kbTabBody').textContent, /部署 SOP/);
  chooseOption(document.querySelector('#kbSubStatusFilter'), '');
  await settle(4);
  assert.match(document.querySelector('#kbTabBody').textContent, /部署 SOP/);

  // 审核保存：PUT 携带编辑后的字段。
  document.querySelector('[data-kb-review-sub="sub-1"]').click();
  await settle(10);
  const drawer = document.querySelector('#kbDrawer');
  assert.equal(document.querySelector('#kbSubTitle').value, '部署 SOP');
  assert.equal(document.querySelector('#kbSubSource').value, SRC_A);
  assert.equal(document.querySelector('#kbSubPath'), null, 'WeKnora 引擎不展示相对路径字段');
  document.querySelector('#kbSubTitle').value = '部署 SOP（终稿）';
  document.querySelector('#kbSubContent').value = '# 终稿';
  document.querySelector('#kbSubSaveBtn').click();
  await settle(14);
  const put = lastCall(app, 'PUT', '/api/knowledge/submissions/sub-1');
  assert.deepEqual(put.body, { title: '部署 SOP（终稿）', source: SRC_A, path: '', content: '# 终稿' });
  assert.equal(drawer.hidden, true);

  // 通过并发布：先 PUT 再 POST /publish。
  document.querySelector('[data-kb-review-sub="sub-1"]').click();
  await settle(10);
  document.querySelector('#kbSubPublishBtn').click();
  await settle(16);
  assert.ok(lastCall(app, 'POST', '/api/knowledge/submissions/sub-1/publish'));
  assert.equal(callsTo(app, 'PUT', '/api/knowledge/submissions/sub-1').length, 2);

  // 驳回。
  document.querySelector('[data-kb-review-sub="sub-1"]').click();
  await settle(10);
  document.querySelector('#kbSubRejectBtn').click();
  await settle(16);
  const reject = lastCall(app, 'POST', '/api/knowledge/submissions/sub-1/reject');
  assert.ok(reject);
  assert.deepEqual(reject.body, {});

  // 已发布条目只读。
  document.querySelector('[data-kb-review-sub="sub-2"]').click();
  await settle(10);
  assert.equal(document.querySelector('#kbSubTitle').hasAttribute('readonly'), true);
  assert.equal(document.querySelector('#kbSubSaveBtn'), null);
  assert.equal(document.querySelector('#kbSubPublishBtn'), null);
});

test('降级路径：未就绪自动跳转配置页、LightCloud 引擎与无管理权限', async (t) => {
  const notReady = await openKnowledge(t, ROUTES([
    { method: 'GET', path: '/api/knowledge/settings', body: { ready: false, canManage: true, connections: [] } },
  ]));
  // 未就绪且有管理权限：自动落到接入配置页。
  assert.ok(notReady.document.querySelector('#kbProvisionManagedBtn'), '未就绪自动切到配置页');
  assert.equal(notReady.document.querySelector('#kbTabBody').textContent.includes('快速接入 WeKnora'), true);

  const light = await openKnowledge(t, ROUTES([
    { method: 'GET', path: '/api/knowledge/settings', body: settingsFixture({ provider: 'lightcloud', connections: [] }) },
  ]));
  const doc2 = light.document;
  assert.match(doc2.querySelector('#kbTabBody').textContent, /LightCloud KB 引擎/);
  assert.equal(doc2.querySelector('#kbConnFilter'), null, '无连接时隐藏筛选');
  // 无连接时新建给出引导，不打开抽屉。
  doc2.querySelector('#kbCreateSourceBtn').click();
  await settle(4);
  assert.equal(doc2.querySelector('#kbDrawer').hidden, true);
  assert.match(lastToast(light).m, /没有可新建知识库的 WeKnora 连接，请先在「接入配置与用量」添加或启用连接/);
  assert.equal(light.router.calls.filter((call) => call.path.endsWith('/documents')).length, 0, 'LightCloud 不请求文档列表');

  const readonly = await openKnowledge(t, ROUTES([
    { method: 'GET', path: '/api/knowledge/settings', body: settingsFixture({ canManage: false, canWrite: false }) },
  ]));
  const doc3 = readonly.document;
  assert.equal(doc3.querySelector('#kbCreateSourceBtn').disabled, true, '无管理权限禁用新建');
  assert.match(doc3.querySelector('#kbCreateSourceBtn').title, /没有知识库写权限/);
  doc3.querySelector('[data-kb-tab="settings"]').click();
  await settle(6);
  assert.match(doc3.querySelector('#kbTabBody').textContent, /仅平台管理员可修改接入配置/);
  assert.equal(doc3.querySelector('#kbProvisionManagedBtn'), null);
  assert.equal(doc3.querySelector('#kbCfgUrl').hasAttribute('readonly'), true);
});

test('连接寻址：单连接回落唯一连接并贯穿文档与详情请求', async (t) => {
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: '/api/knowledge/settings', body: settingsFixture({
      connections: [{ id: CONN_A, name: '研发空间', url: 'http://weknora-app:8080', tokenConfigured: true, enabled: true }],
    }) },
    { method: 'GET', path: '/api/knowledge/sources', body: [{ id: 'kb-bare', name: '裸 ID 知识库', allowed: true }] },
    { method: 'GET', path: /^\/api\/knowledge\/sources\/[^/]+\/documents/, body: { documents: [{ id: 'doc-bare', title: '裸文档', type: 'file', parse_status: 'completed' }], total: 1 } },
    { method: 'GET', path: /^\/api\/knowledge\/documents\/doc-bare\/chunks/, body: { chunks: [{ chunkIndex: 0, content: '正文' }], total: 1, page: 1, pageSize: 10 } },
    { method: 'GET', path: /^\/api\/knowledge\/documents\/doc-bare\?/, body: { title: '裸文档', type: 'file', parse_status: 'completed' } },
  ]));
  const { document } = app;
  const pid = app.evaluate('state.projectFilter');

  assert.equal(document.querySelector('#kbConnFilter'), null, '单连接隐藏筛选');
  const docsCall = lastCall(app, 'GET', '/documents?');
  assert.ok(docsCall);
  assert.equal(param(docsCall, 'connectionId'), CONN_A, '裸知识库 ID 回落唯一连接');
  assert.equal(param(docsCall, 'projectId'), pid);

  await openDoc(app, 'doc-bare');
  assert.equal(param(lastCall(app, 'GET', '/documents/doc-bare/chunks'), 'connectionId'), CONN_A, '裸文档 ID 同样回落唯一连接');
  assert.equal(param(lastCall(app, 'GET', '/documents/doc-bare?'), 'connectionId'), CONN_A);
});

test('全连接停用：管理面保留可用，Agent 检索提示未启用', async (t) => {
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: '/api/knowledge/settings', body: settingsFixture({
      ready: false,
      enabled: false,
      connections: [
        { id: CONN_A, name: '研发空间', url: 'http://weknora-app:8080', tokenConfigured: true, enabled: false },
        { id: CONN_B, name: '测试空间', url: 'https://kb.example.com', tokenConfigured: true, enabled: false },
      ],
    }) },
    // 全停用时后端列表接口返回不可用：前端不得据此清空管理入口。
    { method: 'GET', path: /^\/api\/knowledge\/sources/, body: { status: 503, body: { error: '知识库能力不可用', code: 'capability_unavailable', capability: 'kb_manage' } } },
  ]));
  const { document } = app;

  assert.equal(document.querySelector('[data-kb-tab="sources"]').classList.contains('active'), true, '仍停留在知识库管理页');
  const body = document.querySelector('#kbTabBody');
  assert.match(body.textContent, /所有已配置连接均未启用/);
  assert.match(body.textContent, /当前没有已启用的连接/);
  assert.match(document.querySelector('.kb-metric-val').textContent, /未启用/);

  const create = document.querySelector('#kbCreateSourceBtn');
  assert.equal(create.disabled, false, '停用连接仍允许新建知识库');
  assert.equal(create.classList.contains('primary'), false, 'Tab 1 仅导入文档一个实心主按钮');

  create.click();
  await settle(8);
  const drawer = document.querySelector('#kbDrawer');
  assert.equal(drawer.hidden, false, '停用连接下可打开新建抽屉');
  const opts = [...drawer.querySelectorAll('#kbCreateConn option')].map((o) => o.textContent);
  assert.equal(opts.length, 2);
  assert.match(opts[0], /已停用/);

  // 检索调试仍要求先启用连接。
  document.querySelector('#kbDrawer [data-kb-drawer-close]').click();
  document.querySelector('[data-kb-tab="search"]').click();
  await settle(4);
  assert.match(document.querySelector('#kbTabBody').textContent, /检索调试需先在「接入配置与用量」启用连接/);
});

test('能力降级只认 503：其他状态与未知能力键不误禁用管理面', async (t) => {
  const app = await openKnowledge(t, ROUTES([
    { method: 'POST', path: /^\/api\/knowledge\/sources\/[^/]+\/upload/, status: 500, body: { error: '上游异常', message: '上游异常', code: 'capability_unavailable', capability: 'doc_write', connectionId: CONN_A, reason: '临时故障' } },
  ]));
  const { document } = app;
  document.querySelector('#kbImportDocBtn').click();
  await settle(4);
  const drawer = document.querySelector('#kbDrawer');
  Object.defineProperty(drawer.querySelector('#kbImportFiles'), 'files', {
    configurable: true,
    value: [new app.window.File(['# 内容'], 'guide.md', { type: 'text/markdown' })],
  });
  drawer.querySelector('#kbImportUploadBtn').click();
  await settle(14);

  const badge = () => [...document.querySelectorAll('#kbTabBody .kb-cap-bar .hui-badge')]
    .find((el) => el.textContent === '文档写入');
  assert.equal(badge().dataset.tone, 'success', '非 503 不降级');
  assert.equal(document.querySelector('#kbImportDocBtn').disabled, false);
  assert.match(drawer.querySelector('#kbImportStatus').textContent, /失败 1 个/);

  // 503 但能力键不在白名单：同样不降级。
  const second = await openKnowledge(t, ROUTES([
    { method: 'POST', path: /^\/api\/knowledge\/sources\/[^/]+\/upload/, status: 503, body: { error: '未知能力', message: '未知能力', code: 'capability_unavailable', capability: 'not_a_capability', connectionId: CONN_A } },
  ]));
  const doc2 = second.document;
  doc2.querySelector('#kbImportDocBtn').click();
  await settle(4);
  const drawer2 = doc2.querySelector('#kbDrawer');
  Object.defineProperty(drawer2.querySelector('#kbImportFiles'), 'files', {
    configurable: true,
    value: [new second.window.File(['# 内容'], 'guide.md', { type: 'text/markdown' })],
  });
  drawer2.querySelector('#kbImportUploadBtn').click();
  await settle(14);
  assert.equal(doc2.querySelector('#kbImportDocBtn').disabled, false, '未知能力键不降级');
});

test('文档列表乱序响应：慢的旧知识库不覆盖当前选择', async (t) => {
  let releaseSlow = () => {};
  const slow = new Promise((resolve) => { releaseSlow = resolve; });
  let aCalls = 0;
  const app = await openKnowledge(t, ROUTES([
    // 本用例聚焦乱序响应，知识库列表桩保留两库（不受默认「仅启用连接」限制）。
    { method: 'GET', path: /^\/api\/knowledge\/sources(\?|$)/, body: SOURCES },
    { method: 'GET', path: /^\/api\/knowledge\/sources\/[^/]+\/documents/, body: async (call) => {
      if (call.url.searchParams.get('connectionId') === CONN_B) {
        return { documents: [{ id: `${CONN_B}::doc-b`, title: 'B 库文档', type: 'file', parse_status: 'completed' }], total: 1, page: 1, pageSize: 20 };
      }
      aCalls += 1;
      if (aCalls > 1) await slow;
      return { documents: [{ id: DOC('doc-1'), title: 'A 库文档', type: 'file', parse_status: 'completed' }], total: 1, page: 1, pageSize: 20 };
    } },
  ]));
  const { document } = app;

  assert.match(document.querySelector('#kbTabBody').textContent, /A 库文档/);

  // A 的第二次请求挂起时切到 B，B 先返回；随后释放 A，旧响应必须被丢弃。
  document.querySelector(`[data-kb-pick="${SRC_A}"]`).click();
  await settle(2);
  document.querySelector(`[data-kb-pick="${SRC_B}"]`).click();
  await settle(10);
  assert.match(document.querySelector('#kbTabBody').textContent, /B 库文档/);

  releaseSlow();
  await settle(10);
  assert.match(document.querySelector('#kbTabBody').textContent, /B 库文档/, '晚到的 A 响应被丢弃');
  assert.doesNotMatch(document.querySelector('#kbTabBody').textContent, /A 库文档/);
});

test('Blob 预览生命周期：切换视图、换文档与关闭抽屉都释放 objectURL', async (t) => {
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: /^\/api\/knowledge\/documents\//, body: { title: '文档', type: 'file', parse_status: 'completed' } },
    { method: 'GET', path: /^\/api\/knowledge\/documents\/[^/]+\/chunks/, body: { chunks: [{ chunkIndex: 0, content: '正文' }], total: 1, page: 1, pageSize: 10 } },
    { method: 'GET', path: docRoute('doc-img', '/preview'), body: { status: 200, body: 'PNGDATA', headers: { 'content-type': 'image/png' } } },
    { method: 'GET', path: docRoute('doc-pdf', '/preview'), body: { status: 200, body: '%PDF-1.4', headers: { 'content-type': 'application/pdf' } } },
  ]));
  const { document } = app;
  app.evaluate('window.__revoked = []; window.__seq = 0; window.URL.revokeObjectURL = (u) => window.__revoked.push(u); window.URL.createObjectURL = () => "blob:test-" + (++window.__seq);');

  await openDoc(app, DOC('doc-img'));
  document.querySelector('[data-kb-doc-view="preview"]').click();
  await settle(10);
  assert.equal(document.querySelector('#kbDocBody img').getAttribute('src'), 'blob:test-1');

  document.querySelector('[data-kb-doc-view="chunks"]').click();
  await settle(4);
  assert.deepEqual([...app.evaluate('window.__revoked')], ['blob:test-1'], '切换视图释放图片 URL');

  document.querySelector('[data-kb-doc-view="preview"]').click();
  await settle(10);
  assert.equal(document.querySelector('#kbDocBody img').getAttribute('src'), 'blob:test-2', '重新进入预览重新拉取并创建 URL');

  // 打开另一篇文档：先释放上一篇的 URL。
  await openDoc(app, DOC('doc-pdf'));
  assert.deepEqual([...app.evaluate('window.__revoked')], ['blob:test-1', 'blob:test-2']);

  document.querySelector('[data-kb-doc-view="preview"]').click();
  await settle(10);
  assert.ok(document.querySelector('#kbDocBody iframe.kb-preview-frame'));

  document.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  await settle(2);
  assert.deepEqual([...app.evaluate('window.__revoked')], ['blob:test-1', 'blob:test-2', 'blob:test-3'], '关闭抽屉释放 PDF URL');
});

test('设置加载乱序：晚到的旧 /settings 不覆盖新的连接状态', async (t) => {
  let releaseOld = () => {};
  const gate = new Promise((resolve) => { releaseOld = resolve; });
  let settingsCalls = 0;
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: '/api/knowledge/settings', body: async () => {
      settingsCalls += 1;
      if (settingsCalls === 1) {
        await gate;
        return settingsFixture({ connections: [{ id: CONN_A, name: '旧连接', url: 'http://old.example', tokenConfigured: true, enabled: true }] });
      }
      return settingsFixture({ connections: [{ id: CONN_B, name: '新连接', url: 'http://new.example', tokenConfigured: true, enabled: true }] });
    } },
  ]));
  const { document } = app;

  // 首次 settings 仍挂起时点击刷新，触发第二次（新连接）并先完成。
  document.querySelector('#kbRefreshBtn').click();
  await settle(10);
  releaseOld();
  await settle(10);

  document.querySelector('[data-kb-tab="settings"]').click();
  await settle(4);
  const text = document.querySelector('#kbTabBody').textContent;
  assert.match(text, /新连接/, '新设置的连接生效');
  assert.doesNotMatch(text, /旧连接/, '晚到的旧设置被丢弃');
});

test('在途预览：切出预览后晚到的 Blob 不再创建 objectURL', async (t) => {
  let releasePreview = () => {};
  const gate = new Promise((resolve) => { releasePreview = resolve; });
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: docRoute('doc-2', '/preview'), body: async () => {
      await gate;
      return { status: 200, headers: { 'content-type': 'image/png' }, body: 'PNG' };
    } },
    { method: 'GET', path: docRoute('doc-2'), body: { title: '架构图', type: 'file', parse_status: 'completed' } },
  ]));
  const { document } = app;
  app.evaluate('window.__created = 0; window.URL.createObjectURL = () => { window.__created += 1; return "blob:test-" + window.__created; };');

  await openDoc(app, DOC('doc-2'));
  document.querySelector('[data-kb-doc-view="preview"]').click();
  await settle(4);
  document.querySelector('[data-kb-doc-view="chunks"]').click();
  await settle(4);
  releasePreview();
  await settle(10);

  assert.equal(app.evaluate('window.__created'), 0, '离开预览后不再创建 objectURL');
  assert.equal(document.querySelector('.kb-preview-img'), null);
});

test('未知连接：不生成账号与控制台链接，也不误用其它连接', async (t) => {
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: '/api/knowledge/sources', body: [
      { id: 'ghost::kb-x', name: '幽灵库', allowed: true, doc_count: 1 },
      { id: SRC_A, name: '研发手册', allowed: true, doc_count: 1 },
    ] },
  ]));
  const { document } = app;

  document.querySelector('[data-kb-pick="ghost::kb-x"]').click();
  await settle(10);
  const text = document.querySelector('#kbTabBody').textContent;
  const links = [...document.querySelectorAll('#kbTabBody a')].map((a) => a.textContent);
  assert.equal(links.some((label) => label.includes('高级配置')), false, '未知连接不生成控制台链接');
  assert.doesNotMatch(text, /登录邮箱/, '未知连接不展示其它连接的账号');
});

test('legacy 能力接口 404 后：预览 503 只禁用预览，不连带禁用下载与写入', async (t) => {
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: /^\/api\/knowledge\/connections\/conn-a\/capabilities$/, status: 404, body: { error: 'not found' } },
    { method: 'GET', path: docRoute('doc-1', '/preview'), status: 503, body: { error: '无预览权限', message: '无预览权限', code: 'capability_unavailable', capability: 'preview', reason: 'API Key 不允许预览' } },
  ]));
  const { document } = app;

  await openDoc(app, DOC('doc-1'));
  const previewTab = () => document.querySelector('#kbDrawer [data-kb-doc-view="preview"]');
  assert.ok(previewTab(), 'legacy 连接先乐观展示预览入口');

  previewTab().click();
  await settle(10);

  assert.equal(previewTab(), null, '503 后仅预览入口被禁用');
  const download = [...document.querySelectorAll('#kbDrawer a')].find((a) => a.textContent === '下载');
  assert.ok(download, '下载不受端点级预览拒绝影响');
  assert.ok(document.querySelector('#kbDrawer [data-kb-doc-view="chunks"]'), '未显式拒绝的分块读取不被连带禁用');
});

test('普通抽屉降级：错误不带 connectionId 时不记到上一个文档连接', async (t) => {
  const app = await openKnowledge(t, ROUTES([
    // 本用例需要切到停用连接的 B 库，列表桩保留两库。
    { method: 'GET', path: /^\/api\/knowledge\/sources(\?|$)/, body: SOURCES },
    { method: 'POST', path: /^\/api\/knowledge\/sources\/[^/]+\/url/, status: 503, body: { error: '缺少写入权限', message: '缺少写入权限', code: 'capability_unavailable', capability: 'doc_write', reason: '无权限' } },
  ]));
  const { document } = app;

  await openDoc(app, DOC('doc-1'));
  document.querySelector('#kbDrawer [data-kb-drawer-close]').click();
  await settle(4);

  document.querySelector(`[data-kb-pick="${SRC_B}"]`).click();
  await settle(10);
  document.querySelector('#kbImportDocBtn').click();
  await settle(4);
  const drawer = document.querySelector('#kbDrawer');
  drawer.querySelector('[data-kb-import-mode="url"]').click();
  drawer.querySelector('#kbImportUrl').value = 'https://example.com/a';
  drawer.querySelector('#kbImportUrlBtn').click();
  await settle(10);

  assert.equal(document.querySelector('#kbImportDocBtn').disabled, true, '降级落在当前操作的 B 连接');
});

test('端点级能力：preview 显式 unknown 时不回退聚合，只禁用预览', async (t) => {
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: /^\/api\/knowledge\/connections\/conn-a\/capabilities$/, body: capabilities({ preview: 'unknown', download: 'available', preview_download: 'available' }) },
  ]));
  const { document } = app;

  await openDoc(app, DOC('doc-1'));
  assert.equal(
    document.querySelector('#kbDrawer [data-kb-doc-view="preview"]'),
    null,
    '端点键存在且为 unknown 时，不得被聚合 available 覆盖'
  );
  const download = [...document.querySelectorAll('#kbDrawer a')].find((el) => el.textContent === '下载');
  assert.ok(download, '下载端点可用时下载入口保留');
  assert.equal(callsTo(app, 'GET', DOC_PATH('doc-1') + '/preview').length, 0, '被禁用的预览不发起请求');
});

test('端点级能力：旧后端未返回端点键时回退聚合 preview_download', async (t) => {
  const app = await openKnowledge(t, ROUTES([
    {
      method: 'GET',
      path: /^\/api\/knowledge\/connections\/conn-a\/capabilities$/,
      body: {
        checkedAt: '2026-11-20T10:00:00Z',
        capabilities: {
          kb_manage: 'available',
          doc_write: 'available',
          chunk_read: 'available',
          preview_download: 'available',
          reparse: 'available'
        }
      }
    },
  ]));
  const { document } = app;

  await openDoc(app, DOC('doc-1'));
  assert.ok(
    document.querySelector('#kbDrawer [data-kb-doc-view="preview"]'),
    '端点键缺失（旧后端）时按聚合结论放行预览'
  );
  const download = [...document.querySelectorAll('#kbDrawer a')].find((el) => el.textContent === '下载');
  assert.ok(download, '端点键缺失时按聚合结论放行下载');
});

test('在途 503 无 connectionId：按请求发起连接降级，不污染随后打开的 B 连接抽屉', async (t) => {
  let releaseDelete;
  const deleteGate = new Promise((resolve) => { releaseDelete = resolve; });
  // 本用例需要切到停用连接的 B 库，列表桩保留两库。
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: /^\/api\/knowledge\/sources(\?|$)/, body: SOURCES },
  ]));
  app.router.route('DELETE', /\/api\/knowledge\/documents\/.*doc-1/, async () => {
    await deleteGate;
    return {
      status: 503,
      body: {
        error: '文档写入不可用',
        message: '文档写入不可用',
        code: 'capability_unavailable',
        capability: 'doc_write'
      }
    };
  });
  const { document } = app;

  // 发起 A 连接上的删除文档请求，令其挂起。
  document.querySelector(`[data-kb-delete-doc="${DOC('doc-1')}"]`).click();
  await settle(4);
  document.querySelector('#confirmOkBtn').click();
  await settle(4);
  assert.equal(callsTo(app, 'DELETE', '/documents/').length, 1, '删除请求已发出并挂起');

  // 请求在途时切到 B 并打开 B 的导入抽屉，登记 B 为当前操作连接。
  document.querySelector(`[data-kb-pick="${SRC_B}"]`).click();
  await settle(10);
  document.querySelector('#kbImportDocBtn').click();
  await settle(6);
  assert.ok(document.querySelector('#kbImportUploadBtn'));

  // A 的 503 响应此刻才到且不带 connectionId：必须记到 A，不得禁用 B 的文档写入。
  releaseDelete();
  await settle(12);
  assert.equal(document.querySelector('#kbImportUploadBtn').disabled, false, 'B 的 doc_write 不受 A 的 503 影响');
  assert.match(lastToast(app).m, /文档写入不可用/);

  // 回到 A：文档写入能力已降级，行内删除按钮被禁用。
  document.querySelector('#kbDrawer [data-kb-drawer-close]').click();
  await settle(2);
  document.querySelector(`[data-kb-pick="${SRC_A}"]`).click();
  await settle(10);
  const deleteBtn = document.querySelector(`[data-kb-delete-doc="${DOC('doc-1')}"]`);
  assert.equal(deleteBtn.disabled, true, '503 被记到请求发起连接 A');
  const badge = [...document.querySelectorAll('.kb-cap-bar .hui-badge')].find((el) => el.textContent === '文档写入');
  assert.equal(badge.getAttribute('data-tone'), 'error');
});

test('在途预览 503 无 connectionId：切走后仍按发起连接降级，不污染 B 连接', async (t) => {
  let releasePreview;
  const previewGate = new Promise((resolve) => { releasePreview = resolve; });
  // 本用例需要切到停用连接的 B 库，列表桩保留两库。
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: /^\/api\/knowledge\/sources(\?|$)/, body: SOURCES },
  ]));
  app.router.route('GET', /\/api\/knowledge\/documents\/.*doc-1\/preview/, async () => {
    await previewGate;
    return {
      status: 503,
      body: {
        error: '预览下载不可用',
        message: '预览下载不可用',
        code: 'capability_unavailable',
        capability: 'preview_download'
      }
    };
  });
  const { document } = app;

  await openDoc(app, DOC('doc-1'));
  document.querySelector('#kbDrawer [data-kb-doc-view="preview"]').click();
  await settle(6);
  assert.equal(callsTo(app, 'GET', '/preview').length, 1, '预览请求已发出并挂起');

  // 请求在途时关闭抽屉并切到 B，随后打开 B 的导入抽屉。
  document.querySelector('#kbDrawer [data-kb-drawer-close]').click();
  await settle(2);
  document.querySelector(`[data-kb-pick="${SRC_B}"]`).click();
  await settle(10);
  document.querySelector('#kbImportDocBtn').click();
  await settle(6);

  releasePreview();
  await settle(12);
  assert.equal(document.querySelector('#kbImportUploadBtn').disabled, false, 'B 的 doc_write 不受 A 预览 503 影响');

  // 回到 A：预览下载能力已按发起连接降级。
  document.querySelector('#kbDrawer [data-kb-drawer-close]').click();
  await settle(2);
  document.querySelector(`[data-kb-pick="${SRC_A}"]`).click();
  await settle(10);
  const badge = [...document.querySelectorAll('.kb-cap-bar .hui-badge')].find((el) => el.textContent === '预览下载');
  assert.equal(badge.getAttribute('data-tone'), 'error', 'A 的 preview_download 被降级');
});

test('写权限门禁与停用连接显式浏览：Writer 可写、Viewer 全禁用', async (t) => {
  const routes = (settings) => ROUTES([
    { method: 'GET', path: /^\/api\/knowledge\/sources(\?|$)/, body: (call) =>
      call.url.searchParams.get('connectionId') === CONN_B
        ? [{ id: SRC_B, name: '测试库', doc_count: 3, allowed: true }]
        : [{ id: SRC_A, name: '产品手册', doc_count: 45, allowed: true }] },
    { method: 'GET', path: '/api/knowledge/settings', body: settingsFixture(settings) },
  ]);

  // 项目 Writer：canManage=false 但 canWrite=true，写按钮全部可用。
  const writer = await openKnowledge(t, routes({ canManage: false, canWrite: true }));
  const wdoc = writer.document;

  // 默认视图不传 connectionId，只返回启用连接的知识库。
  const sourcesCalls = () => writer.router.calls
    .filter((call) => call.method === 'GET' && call.path === '/api/knowledge/sources');
  const defaultCall = sourcesCalls().at(-1);
  assert.equal(param(defaultCall, 'connectionId'), null, '默认列表不指定连接');
  assert.match(wdoc.querySelector('#kbTabBody').textContent, /产品手册/);
  assert.doesNotMatch(wdoc.querySelector('#kbTabBody').textContent, /测试库/);

  const createBtn = wdoc.querySelector('#kbCreateSourceBtn');
  assert.equal(createBtn.disabled, false, '项目 Writer 可新建知识库');
  assert.equal(wdoc.querySelector('#kbImportDocBtn').disabled, false);
  assert.equal(wdoc.querySelector('#kbEditSourceBtn').disabled, false);
  assert.equal(wdoc.querySelector('#kbDeleteSourceBtn').disabled, false);
  assert.equal(wdoc.querySelector(`[data-kb-edit-doc="${DOC('doc-1')}"]`).disabled, false);

  // 显式切到已停用连接：请求带 connectionId，列表返回该连接的知识库。
  chooseOption(wdoc.querySelector('#kbConnFilter'), CONN_B);
  await settle(12);
  assert.equal(param(sourcesCalls().at(-1), 'connectionId'), CONN_B);
  assert.match(wdoc.querySelector('#kbTabBody').textContent, /测试库/);
  chooseOption(wdoc.querySelector('#kbConnFilter'), '');
  await settle(12);

  // 只读 Viewer：canWrite=false，所有写入口禁用且给出原因。
  const viewer = await openKnowledge(t, routes({ canManage: false, canWrite: false }));
  const vdoc = viewer.document;
  const vCreate = vdoc.querySelector('#kbCreateSourceBtn');
  assert.equal(vCreate.disabled, true, 'Viewer 不可新建知识库');
  assert.match(vCreate.title, /没有知识库写权限/);
  assert.equal(vdoc.querySelector('#kbImportDocBtn').disabled, true);
  assert.equal(vdoc.querySelector('#kbEditSourceBtn').disabled, true);
  assert.equal(vdoc.querySelector('#kbDeleteSourceBtn').disabled, true);
  assert.equal(vdoc.querySelector(`[data-kb-edit-doc="${DOC('doc-1')}"]`)?.disabled, true, 'Viewer 不可编辑文档');
  assert.equal(vdoc.querySelector(`[data-kb-reparse-doc="${DOC('doc-1')}"]`).disabled, true, 'Viewer 不可重解析');
  assert.equal(vdoc.querySelector(`[data-kb-delete-doc="${DOC('doc-1')}"]`).disabled, true, 'Viewer 不可删除文档');
  assert.equal(docRow(viewer, DOC('doc-1')).disabled, false, '只读仍可查看文档');

  // 点击被禁用的新建按钮不打开抽屉、不发请求。
  vCreate.click();
  await settle(4);
  assert.equal(vdoc.querySelector('#kbDrawer').hidden, true);
  assert.equal(callsTo(viewer, 'POST', '/api/knowledge/sources').length, 0);

  // 「查看」抽屉内的编辑页签同样受写权限约束：Viewer 只能看，没有编辑入口。
  await openDoc(viewer, DOC('doc-1'));
  assert.equal(vdoc.querySelector('#kbDrawer [data-kb-doc-view="edit"]'), null, 'Viewer 查看抽屉无编辑页签');
  assert.match(vdoc.querySelector('#kbDrawer').textContent, /分块|原文预览/);
  vdoc.querySelector('[data-kb-drawer-close]').click();
  await settle(2);
});

test('端点能力拆分：预览可用而下载不可用时只禁用下载', async (t) => {
  const app = await openKnowledge(t, ROUTES([
    { method: 'GET', path: /^\/api\/knowledge\/connections\/conn-a\/capabilities$/, body: capabilities({ preview: 'available', download: 'unavailable' }) },
  ]));
  const { document } = app;

  const viewBtn = docRow(app, DOC('doc-1'));
  assert.equal(viewBtn.disabled, false, '预览可用时「查看」保持可用');

  const download = [...document.querySelectorAll('.kb-row-actions span[aria-disabled="true"]')]
    .find((el) => el.textContent === '下载');
  assert.ok(download, '下载不可用时渲染为禁用占位');
  assert.match(download.title, /下载/);
  assert.equal(document.querySelector('.kb-row-actions a[href*="/download?"]'), null, '不生成裸 503 下载链接');
});

test('手工草稿与解析中间态：正文/发布状态回显、徽标与自动轮询收敛', async (t) => {
  let docLoads = 0;
  const app = await loadApp({ routes: ROUTES([
    { method: 'GET', path: /^\/api\/knowledge\/sources\/[^/]+\/documents/, body: () => {
      docLoads += 1;
      return docLoads === 1
        ? { documents: [
            { id: DOC('doc-draft'), title: '草稿文档', type: 'manual', parse_status: 'draft' },
            { id: DOC('doc-fin'), title: '收尾文档', type: 'file', parse_status: 'finalizing' },
            { id: DOC('doc-del'), title: '删除文档', type: 'file', parse_status: 'deleting' },
          ], total: 3, page: 1, pageSize: 20 }
        : { documents: [{ id: DOC('doc-draft'), title: '草稿文档', type: 'manual', parse_status: 'draft' }], total: 1, page: 1, pageSize: 20 };
    } },
    { method: 'GET', path: docRoute('doc-draft'), body: { id: DOC('doc-draft'), title: '草稿文档', type: 'manual', parse_status: 'draft', content: '# 草稿正文\n\n第二段', publishStatus: 'draft' } },
    { method: 'GET', path: docRoute('doc-draft', '/chunks'), body: { chunks: [], total: 0, page: 1, pageSize: 10 } },
  ]) });
  t.after(app.close);
  const { document } = app;
  app.evaluate('window.__kbToasts = []; window.__origToast = toast; toast = (m, k) => window.__kbToasts.push({ m, k });');
  // 把 4s 轮询压缩到 150ms（远大于首屏 settle，避免抢在断言前刷新）。
  app.evaluate('window.__pollScheduled = 0; window.__origSetTimeout = window.setTimeout; window.setTimeout = function (fn, ms, ...rest) { if (ms === 4000) { window.__pollScheduled += 1; ms = 150; } return window.__origSetTimeout(fn, ms, ...rest); };');
  app.evaluate("switchView('knowledge')");
  await settle(10);

  const firstText = document.querySelector('#kbTabBody').textContent;
  assert.match(firstText, /草稿/);
  assert.match(firstText, /索引收尾中/);
  assert.match(firstText, /删除中/);
  const tones = [...document.querySelectorAll('.kb-table tbody .hui-badge')].map((el) => el.dataset.tone);
  assert.deepEqual(tones, ['neutral', 'info', 'warn']);

  assert.equal(app.evaluate('window.__pollScheduled'), 1, '中间态安排一次 4s 轮询');
  await new Promise((resolve) => setTimeout(resolve, 200));
  await settle(10);
  assert.ok(docLoads >= 2, '轮询刷新文档列表');
  assert.doesNotMatch(document.querySelector('#kbTabBody').textContent, /索引收尾中/, '收敛后不再渲染中间态');

  // 手工草稿：详情返回 content + publishStatus=draft，编辑精确回显且不默认勾选发布。
  await openDoc(app, DOC('doc-draft'));
  document.querySelector('[data-kb-doc-view="edit"]').click();
  await settle(6);
  assert.equal(document.querySelector('#kbDocEditContent').value, '# 草稿正文\n\n第二段');
  assert.equal(document.querySelector('#kbDocEditPublish').checked, false, '草稿不默认勾选发布');
  assert.equal(callsTo(app, 'GET', DOC_PATH('doc-draft') + '/preview').length, 0, '有内联正文时不请求 /preview');
});

test('手工文档正文回退：分块分页被截断时禁用保存，不覆盖原文', async (t) => {
  const id = DOC('doc-trunc');
  const app = await loadApp({ routes: ROUTES([
    {
      method: 'GET',
      path: /^\/api\/knowledge\/sources\/[^/]+\/documents/,
      body: { documents: [{ id, title: '超长手册', type: 'manual', parse_status: 'completed' }], total: 1, page: 1, pageSize: 20 },
    },
    { method: 'GET', path: docRoute('doc-trunc'), body: { id, title: '超长手册', type: 'manual', parse_status: 'completed' } },
    { method: 'GET', path: docRoute('doc-trunc', '/preview'), body: { error: 'unavailable' }, status: 503 },
    {
      method: 'GET',
      path: docRoute('doc-trunc', '/chunks'),
      body: {
        chunks: Array.from({ length: 50 }, (_, i) => ({ chunkIndex: i, content: 'chunk ' + i })),
        total: 1200,
        page: 1,
        pageSize: 50,
      },
    },
  ]) });
  t.after(app.close);
  const { document } = app;
  app.evaluate("switchView('knowledge')");
  await settle(10);
  await openDoc(app, id);
  document.querySelector('[data-kb-doc-view="edit"]').click();
  await settle(60);

  assert.match(document.querySelector('#kbDocBody').textContent, /未能读取原文/);
  const save = document.querySelector('#kbDocSaveBtn');
  assert.equal(save.disabled, true, '正文不完整时不得保存');
  save.click();
  await settle(4);
  assert.equal(callsTo(app, 'PUT', DOC_PATH('doc-trunc')).length, 0, '禁用后不发送保存请求');
});
