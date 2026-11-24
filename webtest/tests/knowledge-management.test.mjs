import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const DEFAULT_TYPES = [
  {
    type: 'weknora',
    name: 'WeKnora 知识库',
    description: '通过只读 REST API 接入个人或团队 WeKnora 实例（支持列子库、混合检索与分块原文读取）',
    supportsSubSources: true,
    defaultAuthType: 'api_key',
    fields: [
      { key: 'endpoint', label: 'WeKnora 服务地址', kind: 'url', required: true },
      { key: 'token', label: 'API Key', kind: 'secret', required: true, secret: true },
    ],
  },
  {
    type: 'mcp',
    name: '通用知识 MCP（Streamable HTTP）',
    description: '接入遵循 Streamable HTTP MCP 协议的任意只读知识库服务',
    supportsSubSources: true,
    defaultAuthType: 'bearer',
    fields: [
      { key: 'endpoint', label: 'MCP 服务地址', kind: 'url', required: true },
      { key: 'token', label: 'Bearer Token（可选）', kind: 'secret', required: false, secret: true },
      { key: 'headers', label: '认证请求头（可选）', kind: 'kv', required: false, secret: true },
    ],
  },
];

async function openKnowledge(t, routes) {
  const app = await loadApp({ routes });
  t.after(app.close);
  app.evaluate('window.__kbToasts = []; toast = (m, k) => window.__kbToasts.push({ m, k });');
  app.evaluate("switchView('knowledge')");
  await settle(10);
  return app;
}

const callsTo = (app, method, fragment) => app.router.calls
  .filter((call) => call.method === method && `${call.path}${call.search}`.includes(fragment));
const lastCall = (app, method, fragment) => callsTo(app, method, fragment).at(-1);
const normalizeRealm = (v) => structuredClone(v);

test('我的知识库页展示空状态与已注册连接器类型卡片', async (t) => {
  const app = await openKnowledge(t, [
    { method: 'GET', path: '/api/knowledge/connector-types', body: DEFAULT_TYPES },
    { method: 'GET', path: '/api/knowledge/connectors', body: [] },
  ]);

  const { document } = app.window;
  assert.equal(document.querySelector('#knowledgeView .kb-head h1').textContent.trim(), '我的知识库');
  assert.ok(lastCall(app, 'GET', '/api/knowledge/connector-types'));
  assert.ok(lastCall(app, 'GET', '/api/knowledge/connectors'));

  const typeCards = [...document.querySelectorAll('#knowledgeRoot .kb-type-card')];
  assert.equal(typeCards.length, 2);
  assert.ok(typeCards.some((el) => el.textContent.includes('WeKnora 知识库')));
  assert.ok(typeCards.some((el) => el.textContent.includes('通用知识 MCP')));
});

test('高扩展动态表单：新增第 3 方插件类型 feishu_wiki 时无需改前端即可探测与创建', async (t) => {
  const customTypes = [
    ...DEFAULT_TYPES,
    {
      type: 'feishu_wiki',
      name: '飞书知识库（只读）',
      description: '通过 OpenAPI 接入个人可见飞书知识空间',
      supportsSubSources: true,
      defaultAuthType: 'oauth',
      fields: [
        { key: 'endpoint', label: '飞书网关地址', kind: 'url', required: true },
        { key: 'space_id', label: '知识空间 ID', kind: 'text', required: true },
        { key: 'token', label: 'App Token', kind: 'secret', required: true, secret: true },
      ],
    },
  ];
  const saved = [];
  const app = await openKnowledge(t, [
    { method: 'GET', path: '/api/knowledge/connector-types', body: customTypes },
    { method: 'GET', path: '/api/knowledge/connectors', body: () => saved },
    {
      method: 'POST',
      path: '/api/knowledge/connectors/check',
      body: { ok: true, latencyMs: 14, tools: ['search_knowledge', 'read_document'], sources: [{ id: 'sp-1', name: '产研手册' }] },
    },
    {
      method: 'POST',
      path: '/api/knowledge/connectors',
      status: 201,
      body: (call) => {
        const body = call.body;
        const created = {
          id: 'conn-feishu-1',
          ownerUserId: 1,
          name: body.name,
          type: body.type,
          endpoint: body.endpoint,
          config: body.config,
          secretConfigured: true,
          enabled: body.enabled !== false,
        };
        saved.push(created);
        return created;
      },
    },
  ]);

  const { document, Event } = app.window;
  document.querySelector('#kbAddConnectorBtn').click();
  await settle(5);

  const typeSelect = document.querySelector('#kbConnType');
  typeSelect.value = 'feishu_wiki';
  typeSelect.dispatchEvent(new Event('change', { bubbles: true }));
  await settle(2);

  assert.ok(document.querySelector('#kbField-space_id'), '动态渲染自定义字段 space_id');
  document.querySelector('#kbConnName').value = '我的飞书空间';
  document.querySelector('#kbField-endpoint').value = 'https://feishu.example.com';
  document.querySelector('#kbField-space_id').value = 'space-9527';
  document.querySelector('#kbField-token').value = 'secret-feishu-tok';

  document.querySelector('#kbDrawerCheckBtn').click();
  await settle(8);
  const checkCall = lastCall(app, 'POST', '/api/knowledge/connectors/check');
  assert.ok(checkCall);
  assert.equal(checkCall.body.type, 'feishu_wiki');
  assert.equal(checkCall.body.config.extra.space_id, 'space-9527');
  assert.equal(checkCall.body.secret.token, 'secret-feishu-tok');
  assert.ok(document.querySelector('#kbDrawerFeedback').textContent.includes('连接正常'));

  document.querySelector('#kbDrawerSaveBtn').click();
  await settle(10);

  const createCall = lastCall(app, 'POST', '/api/knowledge/connectors');
  assert.ok(createCall);
  assert.equal(createCall.body.type, 'feishu_wiki');
  assert.equal(createCall.body.endpoint, 'https://feishu.example.com');
  assert.equal(createCall.body.config.extra.space_id, 'space-9527');
  assert.equal(createCall.body.secret.token, 'secret-feishu-tok');

  assert.ok(document.querySelector('[data-connector-id="conn-feishu-1"]'));
  assert.ok(document.querySelector('#knowledgeRoot').textContent.includes('我的飞书空间'));
  assert.ok(document.querySelector('#knowledgeRoot').textContent.includes('space-9527'));
});

test('连接器列表、编辑不清空旧密钥、启停、探测、查看子库与删除', async (t) => {
  let connectors = [
    {
      id: 'mcp-1',
      ownerUserId: 1,
      name: '个人研发库',
      type: 'mcp',
      endpoint: 'https://kb.example.com/mcp',
      config: { timeoutSec: 30 },
      secretConfigured: true,
      enabled: true,
    },
  ];
  const app = await openKnowledge(t, [
    { method: 'GET', path: '/api/knowledge/connector-types', body: DEFAULT_TYPES },
    { method: 'GET', path: '/api/knowledge/connectors', body: () => connectors },
    {
      method: 'PUT',
      path: '/api/knowledge/connectors/mcp-1',
      body: (call) => {
        const b = call.body;
        connectors = connectors.map((c) => (c.id === 'mcp-1' ? { ...c, name: b.name ?? c.name, enabled: b.enabled ?? c.enabled, config: b.config ?? c.config } : c));
        return connectors[0];
      },
    },
    {
      method: 'POST',
      path: '/api/knowledge/connectors/mcp-1/check',
      body: { ok: true, latencyMs: 11, tools: ['kb_search', 'kb_fetch'] },
    },
    {
      method: 'GET',
      path: '/api/knowledge/connectors/mcp-1/sources',
      body: [{ id: 'arch', name: '系统架构', docCount: 12 }, { id: 'faq', name: '排障手册', docCount: 5 }],
    },
    {
      method: 'DELETE',
      path: '/api/knowledge/connectors/mcp-1',
      body: () => {
        connectors = [];
        return { ok: true };
      },
    },
  ]);

  const { document } = app.window;
  assert.ok(document.querySelector('[data-connector-id="mcp-1"]').textContent.includes('密钥已加密保存'));

  // 编辑名称时密钥框留空，不得发送空 token 或 clearSecret: true
  document.querySelector('[data-kb-edit="mcp-1"]').click();
  await settle(4);
  document.querySelector('#kbConnName').value = '个人研发库 v2';
  document.querySelector('#kbDrawerSaveBtn').click();
  await settle(8);

  const putEdit = lastCall(app, 'PUT', '/api/knowledge/connectors/mcp-1');
  assert.equal(putEdit.body.name, '个人研发库 v2');
  assert.equal(putEdit.body.clearSecret, undefined);
  assert.equal(putEdit.body.secret, undefined);

  // 停用连接器
  document.querySelector('[data-kb-toggle="mcp-1"]').click();
  await settle(8);
  assert.equal(lastCall(app, 'PUT', '/api/knowledge/connectors/mcp-1').body.enabled, false);

  // 探测与查看子库
  document.querySelector('[data-kb-check="mcp-1"]').click();
  await settle(8);
  assert.ok(document.querySelector('[data-connector-id="mcp-1"]').textContent.includes('kb_search'));

  document.querySelector('[data-kb-sources="mcp-1"]').click();
  await settle(8);
  assert.ok(document.querySelector('[data-connector-id="mcp-1"]').textContent.includes('系统架构'));
  assert.ok(document.querySelector('[data-connector-id="mcp-1"]').textContent.includes('12 篇文档'));

  // 两步内联删除
  document.querySelector('[data-kb-delete="mcp-1"]').click();
  await settle(4);
  document.querySelector('[data-kb-confirm-delete="mcp-1"]').click();
  await settle(10);
  assert.ok(lastCall(app, 'DELETE', '/api/knowledge/connectors/mcp-1'));
  assert.equal(document.querySelector('[data-connector-id="mcp-1"]'), null);
});

test('editKnowledgeRefs 兼容旧 mcpServerId 并保存 connectorId 与 allowlist', async (t) => {
  const app = await loadApp({
    routes: [
      {
        method: 'GET',
        path: /^\/api\/knowledge\/reference-options/,
        body: [
          { id: 'conn-wk', name: '我的 WeKnora', type: 'weknora', typeName: 'WeKnora 知识库', scoped: true },
        ],
      },
      {
        method: 'GET',
        path: '/api/knowledge/connectors/conn-wk/sources',
        body: [
          { id: 'kb-a', name: '[研发] API 规范' },
          { id: 'kb-b', name: '旧草稿' },
        ],
      },
      { method: 'POST', path: /^\/api\/knowledge\/reference-check/, body: { ok: true } },
    ],
  });
  t.after(app.close);

  const { window } = app;
  const { document, Event } = window;
  let savedRefs = null;
  const done = window.editKnowledgeRefs('project-1', [{ mcpServerId: 'conn-wk', mode: 'all' }], (next) => {
    savedRefs = next;
  });
  await settle(12);

  const dialog = document.querySelector('dialog.knowledge-ref-dialog');
  assert.ok(dialog);
  const serviceCheckbox = dialog.querySelector('.kr-service-head input[type="checkbox"]');
  assert.equal(serviceCheckbox.checked, true, '兼容历史 mcpServerId 回显勾选状态');

  const modeSelect = dialog.querySelector('select.kr-control');
  modeSelect.value = 'allowlist';
  modeSelect.dispatchEvent(new Event('change', { bubbles: true }));
  await settle(12);

  const rows = [...dialog.querySelectorAll('.kr-source-row input[type="checkbox"]')];
  assert.equal(rows.length, 2);
  rows[0].checked = true;
  rows[0].dispatchEvent(new Event('change', { bubbles: true }));
  await settle(4);

  dialog.querySelector('.knowledge-ref-footer .button.primary').click();
  await settle(12);
  await done;

  assert.deepEqual(normalizeRealm(savedRefs), [{ connectorId: 'conn-wk', mode: 'allowlist', sources: ['kb-a'] }]);
  const checkCall = lastCall(app, 'POST', '/api/knowledge/reference-check');
  assert.deepEqual(normalizeRealm(checkCall.body.knowledgeRefs), normalizeRealm(savedRefs));
});

test('手动发起 SOP 运行弹窗支持覆盖个人知识库并在重开后重置', async (t) => {
  const app = await loadApp({
    routes: [
      {
        method: 'GET',
        path: /^\/api\/knowledge\/reference-options/,
        body: [{ id: 'conn-1', name: '个人资料库', type: 'mcp', typeName: '通用知识 MCP', scoped: false }],
      },
      { method: 'POST', path: /^\/api\/knowledge\/reference-check/, body: { ok: true } },
      { method: 'POST', path: '/api/runs', status: 201, body: (call) => ({ id: 'run-new', status: 'running', executionContext: { knowledgeRefs: call.body.knowledgeRefs } }) },
      { method: 'GET', path: '/api/runs/run-new', body: { id: 'run-new', status: 'succeeded', nodes: {} } },
    ],
  });
  t.after(app.close);

  const { window } = app;
  const { document } = window;

  window.openRunModal('flow-1');
  await settle(4);
  assert.equal(document.querySelector('#runKnowledgeRefsSummary').textContent, '默认按节点或角色配置');

  document.querySelector('#runKnowledgeRefsBtn').click();
  await settle(10);
  const dialog = document.querySelector('dialog.knowledge-ref-dialog');
  dialog.querySelector('.kr-service-head input[type="checkbox"]').click();
  dialog.querySelector('.knowledge-ref-footer .button.primary').click();
  await settle(10);

  assert.equal(document.querySelector('#runKnowledgeRefsSummary').textContent, '本次覆盖：已选 1 个连接器');
  assert.equal(document.querySelector('#runKnowledgeRefsResetBtn').hidden, false);

  document.querySelector('#runInput').value = '查一下文档';
  document.querySelector('#confirmRun').click();
  await settle(12);

  const runCall = lastCall(app, 'POST', '/api/runs');
  assert.deepEqual(normalizeRealm(runCall.body.knowledgeRefs), [{ connectorId: 'conn-1', mode: 'all', sources: [] }]);

  // 再次打开弹窗应恢复默认，不带上次覆盖
  window.openRunModal('flow-1');
  await settle(4);
  assert.equal(document.querySelector('#runKnowledgeRefsSummary').textContent, '默认按节点或角色配置');
  assert.equal(document.querySelector('#runKnowledgeRefsResetBtn').hidden, true);
});

test('前端隐藏 OAuth 入口，常用知识库模板预填免授权/静态 Token/工具白名单并正常保存', async t => {
  let createdConn = null;
  let lastSavedPayload = null;

  const mcpTypes = [
    ...DEFAULT_TYPES.filter(item => item.type !== 'mcp'),
    {
      type: 'mcp',
      name: '通用知识 MCP（Streamable HTTP）',
      description: '对接任意实现 search/fetch 工具的远端 MCP 服务',
      supportsSubSources: true,
      defaultAuthType: 'bearer',
      fields: [
        { key: 'endpoint', label: 'MCP 服务地址', kind: 'url', required: true },
        { key: 'token', label: 'Bearer Token（可选）', kind: 'secret', required: false, secret: true },
        { key: 'headers', label: '认证请求头（可选）', kind: 'kv', required: false, secret: true },
        { key: 'allowedTools', label: '允许工具白名单（可选）', kind: 'text', required: false },
        { key: 'oauth', label: 'OAuth 2.0 授权', kind: 'oauth', required: false },
      ],
    },
  ];

  const app = await loadApp({
    routes: [
      { method: 'GET', path: '/api/knowledge/connector-types', body: mcpTypes },
      {
        method: 'GET',
        path: '/api/knowledge/connectors',
        body: () => (createdConn ? [createdConn] : []),
      },
      {
        method: 'POST',
        path: '/api/knowledge/connectors',
        status: 201,
        body: call => {
          const body = call.body;
          lastSavedPayload = body;
          createdConn = {
            id: 'conn-tdocs-1',
            name: body.name,
            type: body.type,
            endpoint: body.endpoint,
            enabled: body.enabled !== false,
            secretConfigured: Boolean(body.secret),
            config: body.config || {},
          };
          return createdConn;
        },
      },
      {
        method: 'POST',
        path: '/api/knowledge/connectors/conn-tdocs-1/check',
        body: {
          ok: true,
          latencyMs: 18,
          tools: ['manage.search_file', 'get_content'],
          sources: [{ id: 'space-1', name: '云栖居家庭装修项目' }],
        },
      },
    ],
  });
  t.after(app.close);

  const { window } = app;
  const { document } = window;

  app.evaluate('window.__kbToasts = []; toast = (m, k) => window.__kbToasts.push({ m, k });');
  app.evaluate("switchView('knowledge')");
  await settle(10);

  // 1. 不请求 /api/knowledge/oauth/presets，且页面不出现 OAuth 一键授权按钮
  assert.equal(callsTo(app, 'GET', '/api/knowledge/oauth/presets').length, 0);
  assert.equal(document.querySelector('[data-kb-vendor-auth]'), null);

  // 2. 展示已验证的 4 个常用知识库模板卡片
  for (const key of ['ragflow', 'lexiang', 'tencent_docs', 'feishu']) {
    const card = document.querySelector(`.kb-vendor-card[data-vendor-key="${key}"]`);
    assert.ok(card, `应渲染 ${key} 模板卡片`);
    assert.equal(card.getAttribute('data-vendor-state'), 'unconfigured');
  }

  // 3. 点击腾讯文档「快速配置」，验证端点、认证请求头模板、6 个只读工具白名单预填，且隐藏 OAuth 字段
  document.querySelector('[data-kb-vendor-setup="tencent_docs"]').click();
  await settle(6);

  const drawer = document.querySelector('#kbDrawer');
  assert.equal(drawer.querySelector('#kbOAuthCard'), null, '前端应完全隐藏 OAuth 授权卡片');
  assert.equal(drawer.querySelector('#kbField-endpoint').value, 'https://docs.qq.com/openapi/mcp');
  assert.equal(drawer.querySelector('#kbField-token').value, '');
  assert.match(drawer.querySelector('#kbField-headers').value, /Authorization:/);
  assert.match(drawer.querySelector('#kbField-allowedTools').value, /manage\.search_file/);
  assert.match(drawer.querySelector('#kbField-allowedTools').value, /sheet\.get_cell_data/);

  // 填入真实静态 Token 并保存
  drawer.querySelector('#kbField-headers').value = 'Authorization: td-static-token-123';
  drawer.querySelector('#kbDrawerSaveBtn').click();
  await settle(12);

  assert.ok(lastSavedPayload);
  assert.equal(lastSavedPayload.endpoint, 'https://docs.qq.com/openapi/mcp');
  assert.equal(lastSavedPayload.secret.headers.Authorization, 'td-static-token-123');
  assert.equal(lastSavedPayload.secret.token, undefined);
  assert.equal(lastSavedPayload.config.extra.oauthMode, undefined);
  assert.deepEqual(normalizeRealm(lastSavedPayload.config.allowedTools), [
    'manage.search_file',
    'get_content',
    'smartcanvas.read',
    'query_space_list',
    'query_space_node',
    'sheet.get_cell_data',
  ]);

  // 4. 保存后腾讯文档模板卡片变为已接入状态，连接器卡片展示工具白名单且无 OAuth 按钮
  const updatedCard = document.querySelector('.kb-vendor-card[data-vendor-key="tencent_docs"]');
  assert.equal(updatedCard.getAttribute('data-vendor-state'), 'configured');
  assert.equal(updatedCard.classList.contains('is-authorized'), true);
  assert.equal(document.querySelector('[data-kb-authorize]'), null);

  const connCard = document.querySelector('[data-kb-card="conn-tdocs-1"]');
  assert.ok(connCard);
  assert.match(connCard.textContent, /工具白名单/);
  assert.match(connCard.textContent, /manage\.search_file/);
});

