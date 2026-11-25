(function () {
  const HIDDEN_EXTRA_KEYS = new Set([
    'vendorKey',
    'oauthMode',
    'oauthPreset',
    'oauthClientId',
    'oauthAuthorizeUrl',
    'oauthTokenUrl',
    'oauthScopes',
    'oauthTokenHeader',
    'oauthTokenPrefix',
    'oauthResource',
  ]);

  const KB_VENDORS = [
    {
      key: 'obsidian',
      name: 'Obsidian 知识库',
      badge: '本地插件 API',
      desc: '通过 Local REST API 插件只读检索 Obsidian Vault 顶层目录、笔记全文与标签元数据',
      type: 'obsidian',
      defaultEndpoint: 'https://127.0.0.1:27124',
      defaultHeaders: {},
      defaultExtra: { tlsInsecureSkipVerify: 'true' },
      defaultAllowedTools: [],
      guide: '在 Obsidian 安装并启用社区插件 Local REST API，复制 API Key 填入下方密钥框；HTTPS 默认端口 27124（本地自签证书自动信任），Docker 部署请在插件设置中绑定 0.0.0.0。',
      brandTone: 'obsidian',
      iconHTML: '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polygon points="12 2 19 7 17 20 12 22 6 18 5 8 12 2"/><polyline points="12 2 11 12 17 20"/><polyline points="5 8 11 12 19 7"/></svg>',
    },
    {
      key: 'notion',
      name: 'Notion 知识库',
      badge: 'Integration Token',
      desc: '通过 Notion Internal Integration Token 只读检索已授权页面、数据库与结构化块正文',
      type: 'notion',
      defaultEndpoint: 'https://api.notion.com',
      defaultHeaders: {},
      defaultExtra: {},
      defaultAllowedTools: [],
      guide: '前往 notion.so/profile/integrations 创建只读 Internal Integration 并复制 ntn_... 密钥；然后在目标页面右上角 ••• → Connections 关联该集成。',
      brandTone: 'notion',
      iconHTML: '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="4" y="3" width="16" height="18" rx="2"/><path d="M9 16V8l6 8V8"/></svg>',
    },
    {
      key: 'siyuan',
      name: '思源笔记（SiYuan）',
      badge: '内置内核 API',
      desc: '通过思源内置内核 API（:6806）只读检索本地或局域网笔记本、块全文与 Markdown 原文',
      type: 'siyuan',
      defaultEndpoint: 'http://127.0.0.1:6806',
      defaultHeaders: {},
      defaultExtra: {},
      defaultAllowedTools: [],
      guide: '打开思源笔记「设置 → 关于 → API token」复制密钥填入下方；若 LoopForge 运行在 Docker 容器内，请同时在思源「设置 → 关于」中开启「网络伺服」。',
      brandTone: 'siyuan',
      iconHTML: '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/><path d="M9 7h6M9 11h6M9 15h4"/></svg>',
    },
    {
      key: 'feishu',
      name: '飞书知识库 / 云文档',
      badge: '自建应用免签',
      desc: '通过飞书企业自建应用（App ID + App Secret）自动续期读取已授权知识空间与新版云文档',
      type: 'feishu',
      defaultEndpoint: 'https://open.feishu.cn',
      defaultHeaders: {},
      defaultExtra: {},
      defaultAllowedTools: [],
      guide: '在飞书开放平台创建企业自建应用、开启机器人能力并开通 wiki:wiki:readonly 与 docx:document:readonly 权限，发布后将机器人添加为目标知识空间的可阅读成员。',
      brandTone: 'feishu',
      iconHTML: '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3L3 8l9 5 9-5-9-5z"/><path d="M3 13l9 5 9-5"/><path d="M3 17.5l9 4.5 9-4.5"/></svg>',
    },
  ];

  const kbState = {
    connectorTypes: [],
    connectors: [],
    sourcesByConn: {},
    checksByConn: {},
    loading: false,
    error: '',
    confirmDeleteId: '',
    drawerTriggerEl: null,
    drawerEditingConn: null,
    drawerSessionSeq: 0,
  };

  let kbDrawerA11yBound = false;

  function defaultConnectorTypes() {
    return [
      {
        type: 'obsidian',
        name: 'Obsidian 本地知识库',
        description: '通过 Local REST API 插件只读检索 Obsidian Vault 目录、笔记全文与标签元数据',
        supportsSubSources: true,
        defaultAuthType: 'bearer',
        fields: [
          {
            key: 'endpoint',
            label: 'Local REST API 地址',
            kind: 'url',
            placeholder: 'https://127.0.0.1:27124',
            required: true,
            help: 'Local REST API 地址（HTTPS 默认 27124，HTTP 默认 27123；Docker 部署需在插件设置绑定 0.0.0.0）',
          },
          {
            key: 'token',
            label: 'API Key',
            kind: 'secret',
            placeholder: '填写 Local REST API Key（编辑留空则保留原密钥）',
            required: true,
            secret: true,
            help: '在 Obsidian「设置 → Local REST API → Copy API Key」获取',
          },
          {
            key: 'tlsInsecureSkipVerify',
            label: '信任本地自签名证书（true/false）',
            kind: 'text',
            placeholder: 'true',
            required: false,
            help: '默认 true（仅对本地/内网地址生效），用于直连插件默认的 27124 自签名 HTTPS 端口',
          },
        ],
      },
      {
        type: 'notion',
        name: 'Notion 知识库',
        description: '通过 Notion Internal Integration Token 只读检索已授权的页面、数据库与结构化块正文',
        supportsSubSources: true,
        defaultAuthType: 'bearer',
        fields: [
          {
            key: 'endpoint',
            label: 'Notion API 地址（可选）',
            kind: 'url',
            placeholder: 'https://api.notion.com',
            required: false,
            help: '默认 https://api.notion.com，留空自动使用官方地址',
          },
          {
            key: 'token',
            label: 'Integration Token',
            kind: 'secret',
            placeholder: 'ntn_...',
            required: true,
            secret: true,
            help: '填入 Notion Internal Integration Secret，并在目标页面右上角 ••• → Connections 添加该集成',
          },
        ],
      },
      {
        type: 'siyuan',
        name: '思源笔记（SiYuan）',
        description: '通过思源内置内核 API（:6806）只读检索本地或局域网笔记本、块全文与 Markdown 原文',
        supportsSubSources: true,
        defaultAuthType: 'api_key',
        fields: [
          {
            key: 'endpoint',
            label: '思源服务地址',
            kind: 'url',
            placeholder: 'http://127.0.0.1:6806',
            required: true,
            help: '思源笔记服务地址；Docker 部署请开启思源「设置 → 关于 → 网络伺服」',
          },
          {
            key: 'token',
            label: 'API Token',
            kind: 'secret',
            placeholder: '填写思源 API Token（编辑留空则保留原密钥）',
            required: true,
            secret: true,
            help: '在思源笔记「设置 → 关于 → API token」复制，使用 Authorization: Token 鉴权',
          },
        ],
      },
      {
        type: 'feishu',
        name: '飞书知识库 / 云文档',
        description: '通过飞书企业自建应用（App ID + App Secret）自动续期读取已授权知识空间与新版云文档',
        supportsSubSources: true,
        defaultAuthType: 'headers',
        fields: [
          {
            key: 'endpoint',
            label: '飞书开放平台地址（可选）',
            kind: 'url',
            placeholder: 'https://open.feishu.cn',
            required: false,
            help: '默认 https://open.feishu.cn（国际版 Lark 请填 https://open.larksuite.com）',
          },
          {
            key: 'appId',
            label: 'App ID',
            kind: 'text',
            placeholder: 'cli_a58291...',
            required: true,
            help: '飞书开放平台企业自建应用的 App ID',
          },
          {
            key: 'token',
            label: 'App Secret',
            kind: 'secret',
            placeholder: '填写自建应用 App Secret（加密存储，自动换取并续期访问令牌）',
            required: true,
            secret: true,
            help: '开启应用机器人能力并开通 wiki:wiki:readonly、docx:document:readonly 后，将机器人添加为目标知识空间成员',
          },
        ],
      },
      {
        type: 'weknora',
        name: 'WeKnora 知识库',
        description: '通过只读 REST API 接入个人或团队 WeKnora 实例（支持列子库、混合检索与分块原文读取）',
        supportsSubSources: true,
        defaultAuthType: 'api_key',
        fields: [
          {
            key: 'endpoint',
            label: 'WeKnora 服务地址',
            kind: 'url',
            placeholder: 'https://weknora.example.com',
            required: true,
            help: '填写 WeKnora 基础地址（例如 http://host:8080，不能带查询参数）',
          },
          {
            key: 'token',
            label: 'API Key',
            kind: 'secret',
            placeholder: '填写 WeKnora API Key（编辑留空则保留原密钥）',
            required: true,
            secret: true,
            help: '使用 X-API-Key 请求头鉴权，保存后 AES-GCM 加密存储',
          },
        ],
      },
      {
        type: 'mcp',
        name: '通用知识 MCP（Streamable HTTP）',
        description: '接入遵循 Streamable HTTP MCP 协议的只读知识库服务（免授权 / 静态 Token / 本地桥接）',
        supportsSubSources: true,
        defaultAuthType: 'bearer',
        fields: [
          {
            key: 'endpoint',
            label: 'MCP 服务地址',
            kind: 'url',
            placeholder: 'https://kb-mcp.example.com/mcp',
            required: true,
            help: '填写 http(s):// Streamable HTTP MCP Endpoint（不能内嵌账号密码）',
          },
          {
            key: 'token',
            label: 'Bearer Token（可选）',
            kind: 'secret',
            placeholder: 'Bearer Token（留空则不发送或使用自定义请求头）',
            required: false,
            secret: true,
            help: '将自动放入 Authorization: Bearer 头，保存后 AES-GCM 加密存储',
          },
          {
            key: 'headers',
            label: '认证请求头（可选）',
            kind: 'kv',
            placeholder: 'X-API-Key: your-key',
            required: false,
            secret: true,
            help: '每行填写一个 Header: Value（例如 Authorization: <TOKEN> 或 X-Lark-MCP-TAT: xxx），保存后加密存储',
          },
          {
            key: 'allowedTools',
            label: '允许工具白名单（可选）',
            kind: 'text',
            placeholder: '例如：fetch-doc, search-doc 或 manage.search_file, get_content',
            required: false,
            help: '逗号或换行分隔；留空按默认只读策略放行，配置后仅放行列表内的工具（推荐用于飞书远程端点与腾讯文档）',
          },
        ],
      },
    ];
  }

  function kbEsc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  function kbToast(msg, isError) {
    if (typeof toast === 'function') {
      toast(msg, isError ? 'err' : 'ok');
    }
  }

  async function kbAPI(path, opts) {
    const res = await fetch(path, opts);
    const text = await res.text();
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch {}
    if (!res.ok) {
      const err = new Error((data && (data.error || data.message)) || text || ('HTTP ' + res.status));
      err.httpStatus = res.status;
      throw err;
    }
    return data;
  }

  function typeMetaFor(typeName) {
    const key = String(typeName || '').trim().toLowerCase();
    return (kbState.connectorTypes || []).find(t => String(t.type || '').toLowerCase() === key) || null;
  }

  function vendorMetaFor(vendorKey) {
    const key = String(vendorKey || '').trim().toLowerCase();
    return KB_VENDORS.find(v => v.key === key) || null;
  }

  function formatKV(map) {
    if (!map || typeof map !== 'object') return '';
    return Object.entries(map)
      .filter(([k, v]) => String(k || '').trim() && String(v || '').trim())
      .map(([k, v]) => `${k}: ${v}`)
      .join('\n');
  }

  function parseKVLines(raw) {
    const out = {};
    for (const line of String(raw || '').split(/\r?\n/)) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      const idx = trimmed.indexOf(':');
      if (idx <= 0) continue;
      const k = trimmed.slice(0, idx).trim();
      const v = trimmed.slice(idx + 1).trim();
      if (k && v) out[k] = v;
    }
    return out;
  }

  function parseListInput(raw) {
    const seen = new Set();
    const out = [];
    for (const part of String(raw || '').split(/[,\r\n]+/)) {
      const item = part.trim();
      if (!item || seen.has(item)) continue;
      seen.add(item);
      out.push(item);
    }
    return out;
  }

  function kbDrawerFocusables(drawer) {
    return drawer.querySelectorAll('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])');
  }

  function closeKBDrawer() {
    kbState.drawerSessionSeq += 1;
    kbState.drawerEditingConn = null;
    const drawer = document.getElementById('kbDrawer');
    const scrim = document.getElementById('kbDrawerScrim');
    if (!drawer) return;
    drawer.classList.remove('open');
    drawer.hidden = true;
    drawer.inert = true;
    drawer.setAttribute('inert', '');
    drawer.setAttribute('aria-hidden', 'true');
    if (scrim) {
      scrim.hidden = true;
      scrim.setAttribute('aria-hidden', 'true');
    }
    drawer.innerHTML = '';
    const trigger = kbState.drawerTriggerEl;
    kbState.drawerTriggerEl = null;
    if (trigger && typeof trigger.focus === 'function' && document.contains(trigger)) {
      trigger.focus();
    }
  }

  function openKBDrawer(title, bodyHTML, footHTML) {
    const drawer = document.getElementById('kbDrawer');
    const scrim = document.getElementById('kbDrawerScrim');
    if (!drawer) return;
    const active = document.activeElement;
    if (active && active !== document.body && !drawer.contains(active)) {
      kbState.drawerTriggerEl = active;
    }
    drawer.innerHTML = `
      <div class="kb-drawer-head">
        <h3 id="kbDrawerTitleHeading">${kbEsc(title)}</h3>
        <button type="button" class="kb-btn" id="kbDrawerCloseBtn" data-kb-drawer-close>关闭</button>
      </div>
      <div class="kb-drawer-body">${bodyHTML}</div>
      ${footHTML ? `<div class="kb-drawer-foot">${footHTML}</div>` : ''}
    `;
    drawer.hidden = false;
    drawer.inert = false;
    drawer.removeAttribute('inert');
    drawer.setAttribute('aria-hidden', 'false');
    if (scrim) {
      scrim.hidden = false;
      scrim.setAttribute('aria-hidden', 'false');
    }
    const reveal = () => drawer.classList.add('open');
    if (typeof requestAnimationFrame === 'function') requestAnimationFrame(reveal);
    else reveal();
    drawer.querySelectorAll('[data-kb-drawer-close]').forEach(el => el.addEventListener('click', closeKBDrawer));
    const first = kbDrawerFocusables(drawer)[0];
    if (first && typeof first.focus === 'function') first.focus();
  }

  function initKBDrawerA11y() {
    if (kbDrawerA11yBound) return;
    kbDrawerA11yBound = true;
    document.addEventListener('keydown', e => {
      if (e.key !== 'Escape') return;
      const drawer = document.getElementById('kbDrawer');
      if (drawer && !drawer.hidden) {
        e.preventDefault();
        closeKBDrawer();
      }
    });
    const scrim = document.getElementById('kbDrawerScrim');
    if (scrim) scrim.addEventListener('click', closeKBDrawer);
  }

  function hydrateFieldValue(field, formConn) {
    if (!formConn || !field) return '';
    if (field.key === 'allowedTools') {
      return Array.isArray(formConn.config?.allowedTools) ? formConn.config.allowedTools.join(', ') : '';
    }
    if (field.secret) {
      // Allow vendor drafts (unsaved templates) to prefill header templates.
      if (!formConn.id && (field.key === 'headers' || field.kind === 'kv') && formConn.draftHeaders) {
        return formatKV(formConn.draftHeaders);
      }
      return '';
    }
    if (field.key === 'endpoint') return formConn.endpoint || '';
    if (field.key === 'headers' || field.kind === 'kv') {
      return formatKV(formConn.config?.headers);
    }
    const extra = formConn.config?.extra;
    return (extra && extra[field.key] != null) ? String(extra[field.key]) : '';
  }

  function renderDynamicFieldsHTML(meta, formConn) {
    const fields = Array.isArray(meta?.fields) ? meta.fields : [];
    const visibleFields = fields.filter(f => String(f?.kind || 'text') !== 'oauth');
    if (!visibleFields.length) {
      return '<p class="kb-muted">该连接器类型无需额外字段。</p>';
    }
    return visibleFields.map(field => {
      const key = String(field.key || '');
      const kind = String(field.kind || 'text');
      const isSecret = Boolean(field.secret || kind === 'secret');
      const id = 'kbField-' + key.replace(/[^A-Za-z0-9_-]/g, '_');
      const val = hydrateFieldValue(field, formConn);
      let placeholder = field.placeholder || '';
      if (isSecret && formConn?.id && formConn?.secretConfigured) {
        placeholder = '已配置密钥，留空则保持不变';
      }
      const requiredMark = field.required && !(isSecret && formConn?.id && formConn?.secretConfigured)
        ? ' <span class="kb-req">*</span>'
        : '';
      const control = kind === 'kv'
        ? `<textarea id="${kbEsc(id)}" class="kb-input kb-kv-input" rows="3" data-field-key="${kbEsc(key)}" data-field-kind="${kbEsc(kind)}" data-field-secret="${isSecret ? '1' : '0'}" placeholder="${kbEsc(placeholder)}">${kbEsc(val)}</textarea>`
        : `<input id="${kbEsc(id)}" class="kb-input" type="${kbEsc(kind === 'url' ? 'url' : isSecret ? 'password' : kind === 'number' ? 'number' : 'text')}" data-field-key="${kbEsc(key)}" data-field-kind="${kbEsc(kind)}" data-field-secret="${isSecret ? '1' : '0'}" value="${kbEsc(val)}" placeholder="${kbEsc(placeholder)}" autocomplete="off">`;
      const vendorKey = String(formConn?.config?.extra?.vendorKey || '').trim().toLowerCase();
      const vendorRec = (key === 'allowedTools' && vendorKey) ? vendorMetaFor(vendorKey)?.defaultAllowedTools : null;
      const fillBtn = Array.isArray(vendorRec) && vendorRec.length
        ? ` <button type="button" class="btn sm ghost" data-kb-fill-tools="${kbEsc(vendorRec.join(', '))}">填入推荐白名单</button>`
        : '';
      return `
        <div class="kb-field" data-kb-field-wrap="${kbEsc(key)}">
          <label for="${kbEsc(id)}">${kbEsc(field.label || key)}${requiredMark}${fillBtn}</label>
          ${control}
          ${field.help ? `<small>${kbEsc(field.help)}</small>` : ''}
        </div>
      `;
    }).join('');
  }

  function renderCheckBannerHTML(res) {
    if (!res) return '';
    if (res.ok === false) {
      return `<div class="kb-check-banner err"><strong>连接失败</strong>：${kbEsc(res.error || '未知错误')}</div>`;
    }
    const latency = Number(res.latencyMs) || 0;
    const tools = Array.isArray(res.tools) ? res.tools : [];
    const sources = Array.isArray(res.sources) ? res.sources : [];
    const toolsHTML = tools.length
      ? `<div class="kb-chip-row"><span class="kb-muted">只读工具：</span>${tools.map(t => `<code class="kb-chip">${kbEsc(t)}</code>`).join('')}</div>`
      : '';
    const srcNote = sources.length ? ` · 发现 ${sources.length} 个子知识库` : '';
    return `
      <div class="kb-check-banner ok">
        <div><strong>探测通过 · 连接正常</strong> · ${latency} ms${srcNote}</div>
        ${toolsHTML}
      </div>
    `;
  }

  function renderSourcesListHTML(sources) {
    if (!Array.isArray(sources)) return '';
    if (!sources.length) {
      return '<div class="kb-sources-box"><span class="kb-muted">当前连接器未返回可细分列表（默认按全局范围只读检索）。</span></div>';
    }
    return `
      <div class="kb-sources-box">
        <div class="kb-sources-head"><strong>可用子知识库</strong><span>共 ${sources.length} 项</span></div>
        <div class="kb-sources-grid">
          ${sources.map(src => `
            <div class="kb-source-pill">
              <div class="kb-source-title">${kbEsc(src.name || src.id)}</div>
              <div class="kb-source-sub"><code>${kbEsc(src.id)}</code>${src.docCount != null ? ` · ${Number(src.docCount)} 篇文档` : ''}</div>
            </div>
          `).join('')}
        </div>
      </div>
    `;
  }

  function collectDrawerPayload(drawer, editingConn, vendorKey) {
    const typeSelect = drawer.querySelector('#kbConnType');
    const type = editingConn ? editingConn.type : String(typeSelect?.value || '').trim();
    const meta = typeMetaFor(type);
    const name = String(drawer.querySelector('#kbConnName')?.value || '').trim();
    const enabled = Boolean(drawer.querySelector('#kbConnEnabled')?.checked);
    const timeoutRaw = String(drawer.querySelector('#kbConnTimeout')?.value || '').trim();
    const timeoutSec = timeoutRaw ? Math.max(0, Math.min(300, Math.trunc(Number(timeoutRaw) || 0))) : 0;

    const baseExtra = Object.assign({}, editingConn?.config?.extra || {});
    if (vendorKey && !baseExtra.vendorKey) {
      baseExtra.vendorKey = vendorKey;
    }
    // Frontend hides OAuth: ensure no stale OAuth mode is inadvertently enabled on save.
    delete baseExtra.oauthMode;
    delete baseExtra.oauthPreset;

    const config = {
      timeoutSec,
      headers: Object.assign({}, editingConn?.config?.headers || {}),
      defaultSources: Array.isArray(editingConn?.config?.defaultSources) ? [...editingConn.config.defaultSources] : [],
      allowedTools: Array.isArray(editingConn?.config?.allowedTools) ? [...editingConn.config.allowedTools] : [],
      extra: baseExtra,
    };
    const secret = {
      authType: meta?.defaultAuthType || '',
    };
    let hasSecretInput = false;
    let endpoint = editingConn ? editingConn.endpoint : '';

    drawer.querySelectorAll('#kbDynamicFields [data-field-key]').forEach(el => {
      const key = el.dataset.fieldKey || '';
      const kind = el.dataset.fieldKind || 'text';
      const isSecret = el.dataset.fieldSecret === '1';
      const raw = String(el.value || '');
      const trimmed = raw.trim();

      if (key === 'endpoint') {
        endpoint = trimmed;
        return;
      }
      if (key === 'allowedTools') {
        config.allowedTools = parseListInput(raw);
        return;
      }
      if (isSecret) {
        if (key === 'token') {
          if (trimmed) {
            secret.token = trimmed;
            hasSecretInput = true;
          }
          return;
        }
        if (key === 'headers' || kind === 'kv') {
          const parsed = parseKVLines(raw);
          if (Object.keys(parsed).length) {
            secret.headers = Object.assign({}, secret.headers || {}, parsed);
            hasSecretInput = true;
          }
          return;
        }
        if (trimmed) {
          secret.headers = Object.assign({}, secret.headers || {}, { [key]: trimmed });
          hasSecretInput = true;
        }
        return;
      }

      if (key === 'headers' || kind === 'kv') {
        config.headers = parseKVLines(raw);
        return;
      }
      if (trimmed) {
        config.extra[key] = trimmed;
      } else {
        delete config.extra[key];
      }
    });

    if (!config.allowedTools.length) {
      delete config.allowedTools;
    }

    const clearSecret = Boolean(drawer.querySelector('#kbConnClearSecret')?.checked);
    const payload = {
      name,
      type,
      endpoint,
      enabled,
      config,
    };
    if (clearSecret) {
      payload.clearSecret = true;
    } else if (hasSecretInput) {
      payload.secret = secret;
    }
    return payload;
  }

  function buildVendorDraftConnector(vendorKey) {
    const v = vendorMetaFor(vendorKey);
    if (!v) return null;
    return {
      name: v.name,
      type: v.type || 'mcp',
      endpoint: v.defaultEndpoint,
      enabled: true,
      draftHeaders: Object.assign({}, v.defaultHeaders || {}),
      config: {
        timeoutSec: 60,
        headers: {},
        defaultSources: [],
        allowedTools: Array.isArray(v.defaultAllowedTools) ? [...v.defaultAllowedTools] : [],
        extra: Object.assign({}, v.defaultExtra || {}, {
          vendorKey: v.key,
        }),
      },
    };
  }

  function findConnectorForVendor(vendorKey) {
    const v = vendorMetaFor(vendorKey);
    if (!v) return null;
    return (kbState.connectors || []).find(conn => {
      const tagged = String(conn.config?.extra?.vendorKey || conn.config?.extra?.oauthPreset || '').trim().toLowerCase();
      if (tagged === v.key) return true;
      const connType = String(conn.type || '').trim().toLowerCase();
      if (v.type && v.type !== 'mcp' && connType === v.type) return true;
      return false;
    }) || null;
  }

  function openConnectorDrawer(editingConn, preselectedType, preselectedVendorKey) {
    kbState.drawerSessionSeq += 1;
    const drawerSeq = kbState.drawerSessionSeq;
    const activeEditingConn = editingConn || null;
    kbState.drawerEditingConn = activeEditingConn;

    const resolvedVendorKey = preselectedVendorKey || String(activeEditingConn?.config?.extra?.vendorKey || '').trim().toLowerCase();
    const vendorMeta = resolvedVendorKey ? vendorMetaFor(resolvedVendorKey) : null;
    const vendorDraft = (!activeEditingConn && vendorMeta)
      ? buildVendorDraftConnector(vendorMeta.key)
      : null;
    const formTargetConn = activeEditingConn || vendorDraft;

    const types = kbState.connectorTypes.length ? kbState.connectorTypes : defaultConnectorTypes();
    const initialType = formTargetConn
      ? formTargetConn.type
      : (preselectedType && typeMetaFor(preselectedType) ? preselectedType : (types[0]?.type || 'weknora'));
    const initialMeta = typeMetaFor(initialType) || types[0];
    const typeOptions = types.map(t => `<option value="${kbEsc(t.type)}" ${t.type === initialType ? 'selected' : ''}>${kbEsc(t.name)} (${kbEsc(t.type)})</option>`).join('');

    const guideHTML = vendorMeta?.guide
      ? `<div class="kb-vendor-guide"><strong>${kbEsc(vendorMeta.name)}接入指引（${kbEsc(vendorMeta.badge)}）</strong><span>${kbEsc(vendorMeta.guide)}</span></div>`
      : '';

    const bodyHTML = `
      ${guideHTML}
      <div class="kb-field">
        <label for="kbConnType">连接器类型</label>
        <select id="kbConnType" class="kb-input" ${activeEditingConn ? 'disabled' : ''}>${typeOptions}</select>
        <small id="kbConnTypeDesc">${kbEsc(initialMeta?.description || '')}</small>
      </div>
      <div class="kb-field">
        <label for="kbConnName">连接器名称 <span class="kb-req">*</span></label>
        <input id="kbConnName" class="kb-input" type="text" value="${kbEsc(formTargetConn?.name || '')}" placeholder="例如：Obsidian 个人笔记 / 飞书知识库" autocomplete="off">
      </div>
      <div id="kbDynamicFields" class="kb-dynamic-fields">${renderDynamicFieldsHTML(initialMeta, formTargetConn)}</div>
      <div class="kb-drawer-row">
        <label class="kb-check-inline">
          <input id="kbConnEnabled" type="checkbox" ${formTargetConn ? (formTargetConn.enabled !== false ? 'checked' : '') : 'checked'}>
          <span>启用此连接器（供 AI 对话和 SOP 选用）</span>
        </label>
        <div class="kb-field kb-timeout-field">
          <label for="kbConnTimeout">请求超时（秒）</label>
          <input id="kbConnTimeout" class="kb-input" type="number" min="0" max="300" placeholder="默认 60" value="${formTargetConn?.config?.timeoutSec ? kbEsc(formTargetConn.config.timeoutSec) : ''}">
        </div>
      </div>
      ${activeEditingConn?.secretConfigured ? `
        <label class="kb-check-inline">
          <input id="kbConnClearSecret" type="checkbox">
          <span>清除已保存的密钥（勾选后将移除原加密凭据）</span>
        </label>
      ` : ''}
      <div id="kbDrawerFeedback" role="status" aria-live="polite"></div>
    `;
    const footHTML = `
      <button type="button" class="kb-btn" id="kbDrawerCheckBtn">测试连接</button>
      <span class="kb-drawer-spacer"></span>
      <button type="button" class="kb-btn" data-kb-drawer-close>取消</button>
      <button type="button" class="kb-btn primary" id="kbDrawerSaveBtn">保存</button>
    `;

    openKBDrawer(activeEditingConn ? '编辑连接器' : (vendorMeta ? `接入 ${vendorMeta.name}` : '添加连接器'), bodyHTML, footHTML);

    const drawer = document.getElementById('kbDrawer');
    if (!drawer) return;

    const isStaleDrawer = () => kbState.drawerSessionSeq !== drawerSeq || drawer.hidden;

    const dynHost = drawer.querySelector('#kbDynamicFields');
    if (dynHost) {
      dynHost.addEventListener('click', e => {
        const btn = e.target.closest('[data-kb-fill-tools]');
        if (!btn) return;
        const input = dynHost.querySelector('[data-field-key="allowedTools"]');
        if (input) {
          input.value = btn.getAttribute('data-kb-fill-tools') || '';
          input.focus();
        }
      });
    }

    const typeSel = drawer.querySelector('#kbConnType');
    if (typeSel && !activeEditingConn) {
      typeSel.addEventListener('change', () => {
        const nextMeta = typeMetaFor(typeSel.value);
        const descEl = drawer.querySelector('#kbConnTypeDesc');
        if (descEl) descEl.textContent = nextMeta?.description || '';
        const host = drawer.querySelector('#kbDynamicFields');
        if (host) {
          host.innerHTML = renderDynamicFieldsHTML(nextMeta, null);
        }
        const fb = drawer.querySelector('#kbDrawerFeedback');
        if (fb) fb.innerHTML = '';
      });
    }

    const feedbackEl = drawer.querySelector('#kbDrawerFeedback');
    const checkBtn = drawer.querySelector('#kbDrawerCheckBtn');
    const saveBtn = drawer.querySelector('#kbDrawerSaveBtn');

    if (checkBtn) {
      checkBtn.addEventListener('click', async () => {
        checkBtn.disabled = true;
        if (feedbackEl) feedbackEl.innerHTML = '<div class="kb-muted">正在探测连接器…</div>';
        try {
          const payload = collectDrawerPayload(drawer, activeEditingConn, vendorMeta?.key || '');
          const path = activeEditingConn
            ? `/api/knowledge/connectors/${encodeURIComponent(activeEditingConn.id)}/check`
            : '/api/knowledge/connectors/check';
          const res = await kbAPI(path, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload),
          });
          if (isStaleDrawer()) return;
          if (feedbackEl) feedbackEl.innerHTML = renderCheckBannerHTML(res);
          if (activeEditingConn) {
            kbState.checksByConn[activeEditingConn.id] = res;
            if (Array.isArray(res?.sources)) kbState.sourcesByConn[activeEditingConn.id] = res.sources;
          }
        } catch (err) {
          if (!isStaleDrawer() && feedbackEl) feedbackEl.innerHTML = renderCheckBannerHTML({ ok: false, error: err.message });
        } finally {
          if (!isStaleDrawer()) checkBtn.disabled = false;
        }
      });
    }

    if (saveBtn) {
      saveBtn.addEventListener('click', async () => {
        const payload = collectDrawerPayload(drawer, activeEditingConn, vendorMeta?.key || '');
        if (!payload.name) {
          if (feedbackEl) feedbackEl.innerHTML = renderCheckBannerHTML({ ok: false, error: '请填写连接器名称' });
          drawer.querySelector('#kbConnName')?.focus();
          return;
        }
        saveBtn.disabled = true;
        try {
          const path = activeEditingConn
            ? `/api/knowledge/connectors/${encodeURIComponent(activeEditingConn.id)}`
            : '/api/knowledge/connectors';
          const method = activeEditingConn ? 'PUT' : 'POST';
          await kbAPI(path, {
            method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload),
          });
          if (isStaleDrawer()) return;
          closeKBDrawer();
          kbToast(activeEditingConn ? '连接器已更新' : '连接器已添加');
          await loadKnowledgeView();
        } catch (err) {
          if (!isStaleDrawer() && feedbackEl) feedbackEl.innerHTML = renderCheckBannerHTML({ ok: false, error: err.message });
        } finally {
          if (!isStaleDrawer()) saveBtn.disabled = false;
        }
      });
    }
  }

  function renderConnectorAuthBadgeHTML(conn) {
    return `<span class="hui-badge" data-size="sm" data-tone="${conn.secretConfigured ? 'info' : 'neutral'}">${conn.secretConfigured ? '密钥已加密保存' : '免鉴权 / 无密钥'}</span>`;
  }

  function renderConnectorExtraHTML(conn, meta) {
    const items = [];
    const allowedTools = Array.isArray(conn.config?.allowedTools) ? conn.config.allowedTools.filter(Boolean) : [];
    if (allowedTools.length) {
      items.push(`<span class="kb-extra-item"><span>工具白名单：</span><code>${kbEsc(allowedTools.join(', '))}</code></span>`);
    }
    const extra = conn.config?.extra;
    if (extra && typeof extra === 'object') {
      const fieldsByKey = {};
      (meta?.fields || []).forEach(f => { fieldsByKey[f.key] = f; });
      Object.entries(extra)
        .filter(([k, v]) => String(v || '').trim() && !HIDDEN_EXTRA_KEYS.has(k))
        .forEach(([k, v]) => {
          const label = fieldsByKey[k]?.label || k;
          items.push(`<span class="kb-extra-item"><span>${kbEsc(label)}：</span><code>${kbEsc(v)}</code></span>`);
        });
    }
    if (!items.length) return '';
    return `<div class="kb-extra-row">${items.join('')}</div>`;
  }

  function renderConnectorCardHTML(conn) {
    const meta = typeMetaFor(conn.type);
    const checkRes = kbState.checksByConn[conn.id];
    const sources = kbState.sourcesByConn[conn.id];
    const supportsSources = !meta || meta.supportsSubSources !== false;
    const confirmingDelete = kbState.confirmDeleteId === conn.id;

    return `
      <article class="sh-card kb-card" data-kb-card="${kbEsc(conn.id)}" data-connector-id="${kbEsc(conn.id)}">
        <div class="kb-card-top">
          <div class="kb-card-title-group">
            <h3>${kbEsc(conn.name)}</h3>
            <div class="kb-card-badges">
              <span class="hui-badge" data-size="sm" data-tone="neutral">${kbEsc(meta?.name || conn.type)}</span>
              <span class="hui-badge" data-size="sm" data-tone="${conn.enabled ? 'success' : 'neutral'}">${conn.enabled ? '已启用' : '已停用'}</span>
              ${renderConnectorAuthBadgeHTML(conn)}
            </div>
          </div>
          <div class="kb-row-actions">
            <button type="button" class="kb-btn xs" data-kb-check="${kbEsc(conn.id)}">测试连接</button>
            ${supportsSources ? `<button type="button" class="kb-btn xs" data-kb-sources="${kbEsc(conn.id)}">查看子库</button>` : ''}
            <button type="button" class="kb-btn xs" data-kb-toggle="${kbEsc(conn.id)}">${conn.enabled ? '停用' : '启用'}</button>
            <button type="button" class="kb-btn xs" data-kb-edit="${kbEsc(conn.id)}">编辑</button>
            ${confirmingDelete
              ? `<button type="button" class="kb-btn xs danger-outline" data-kb-confirm-delete="${kbEsc(conn.id)}">确认删除</button><button type="button" class="kb-btn xs" data-kb-cancel-delete>取消</button>`
              : `<button type="button" class="kb-btn xs danger-outline" data-kb-delete="${kbEsc(conn.id)}">删除</button>`}
          </div>
        </div>
        <div class="kb-card-meta">
          <div class="kb-endpoint-line"><span>服务地址：</span><code>${kbEsc(conn.endpoint)}</code></div>
          <span class="kb-muted">ID: <code>${kbEsc(conn.id)}</code></span>
          ${conn.config?.timeoutSec ? `<span class="kb-muted">超时: ${Number(conn.config.timeoutSec)}s</span>` : ''}
        </div>
        ${renderConnectorExtraHTML(conn, meta)}
        ${checkRes ? renderCheckBannerHTML(checkRes) : ''}
        ${sources ? renderSourcesListHTML(sources) : ''}
      </article>
    `;
  }

  function renderVendorGalleryHTML() {
    const cardsHTML = KB_VENDORS.map(v => {
      const conn = findConnectorForVendor(v.key);
      const isConfigured = Boolean(conn);
      const stateClass = isConfigured ? 'is-authorized' : 'is-unauthorized';
      const statusBadgeHTML = isConfigured
        ? `<span class="hui-badge" data-size="sm" data-tone="success">已接入</span>`
        : `<span class="hui-badge" data-size="sm" data-tone="muted">未配置</span>`;

      const actionsHTML = isConfigured
        ? `
          <button type="button" class="kb-btn xs" data-kb-check="${kbEsc(conn.id)}">测试连接</button>
          <button type="button" class="kb-btn xs" data-kb-sources="${kbEsc(conn.id)}">查看子库</button>
          <button type="button" class="kb-btn xs" data-kb-edit="${kbEsc(conn.id)}">编辑配置</button>
        `
        : `
          <button type="button" class="kb-btn xs primary" data-kb-vendor-setup="${kbEsc(v.key)}">快速配置</button>
        `;

      return `
        <div class="kb-vendor-card ${stateClass}" data-vendor-key="${kbEsc(v.key)}" data-vendor-state="${isConfigured ? 'configured' : 'unconfigured'}">
          <div class="kb-vendor-main">
            <div class="kb-vendor-icon" data-brand="${kbEsc(v.brandTone)}">${v.iconHTML}</div>
            <div class="kb-vendor-info">
              <div class="kb-vendor-title-row">
                <strong class="kb-vendor-name">${kbEsc(v.name)}</strong>
                <span class="hui-badge" data-size="sm" data-tone="neutral">${kbEsc(v.badge)}</span>
                ${statusBadgeHTML}
              </div>
              <p class="kb-vendor-desc">${kbEsc(v.desc)}</p>
              ${isConfigured ? `<div class="kb-vendor-meta">已绑定连接器：${kbEsc(conn.name)}</div>` : ''}
            </div>
          </div>
          <div class="kb-vendor-actions">
            ${actionsHTML}
          </div>
        </div>
      `;
    }).join('');

    return `
      <section class="sh-card kb-vendor-section" aria-label="常用知识库模板">
        <div class="kb-vendor-section-head">
          <div>
            <h2>常用知识库 · 快速接入</h2>
            <p class="kb-muted">统一原生只读连接器，免 OAuth 回调并自动管理鉴权、子库范围隔离与出处引用；WeKnora、RAGFlow、乐享等可从「添加连接器」继续接入。</p>
          </div>
        </div>
        <div class="kb-vendor-grid">
          ${cardsHTML}
        </div>
      </section>
    `;
  }

  function renderEmptyStateHTML() {
    const types = kbState.connectorTypes.length ? kbState.connectorTypes : defaultConnectorTypes();
    return `
      <div class="sh-card kb-empty-panel">
        <div class="kb-empty-head">
          <h2>还没有接入个人知识库连接器</h2>
          <p class="kb-muted">可从上方常用知识库模板快速配置，或选择一个连接器类型接入你的自有知识库；配置仅对你本人可见，可在 AI 对话、SOP 节点与手动运行中引用。</p>
        </div>
        <div class="kb-type-grid">
          ${types.map(t => `
            <div class="kb-type-card">
              <div class="kb-type-card-top">
                <strong>${kbEsc(t.name)}</strong>
                <code>${kbEsc(t.type)}</code>
              </div>
              <p class="kb-muted">${kbEsc(t.description || '')}</p>
              <button type="button" class="kb-btn primary xs" data-kb-add-type="${kbEsc(t.type)}">接入此类型</button>
            </div>
          `).join('')}
        </div>
      </div>
    `;
  }

  function renderKnowledgeView() {
    const root = document.getElementById('knowledgeRoot');
    if (!root) return;

    const notice = `
      <div class="kb-notice">
        <span><strong>个人隔离</strong>：连接器与凭据仅归属当前账号，其他成员无法查看或调用。</span>
        <span><strong>只读安全代理</strong>：密钥 AES-GCM 加密托管，沙箱通过无状态代理检索并标注出处，支持按工具白名单精准放行只读工具。</span>
      </div>
    `;

    let body = '';
    if (kbState.loading && !kbState.connectors.length) {
      body = '<div class="sh-card kb-panel"><p class="kb-muted">正在加载个人知识库连接器…</p></div>';
    } else if (kbState.error && !kbState.connectors.length) {
      body = `<div class="sh-card kb-panel"><div class="kb-check-banner err">${kbEsc(kbState.error)}</div></div>`;
    } else if (!kbState.connectors.length) {
      body = renderVendorGalleryHTML() + renderEmptyStateHTML();
    } else {
      body = renderVendorGalleryHTML() + `<div class="kb-connector-list">${kbState.connectors.map(renderConnectorCardHTML).join('')}</div>`;
    }

    root.innerHTML = notice + body;
    bindKnowledgeRootEvents(root);
  }

  function bindKnowledgeRootEvents(root) {
    root.querySelectorAll('[data-kb-add-type]').forEach(btn => {
      btn.addEventListener('click', () => openConnectorDrawer(null, btn.dataset.kbAddType));
    });
    root.querySelectorAll('[data-kb-vendor-setup]').forEach(btn => {
      btn.addEventListener('click', () => {
        const vendorKey = btn.dataset.kbVendorSetup;
        const existing = findConnectorForVendor(vendorKey);
        const v = vendorMetaFor(vendorKey);
        openConnectorDrawer(existing, v?.type || 'mcp', vendorKey);
      });
    });
    root.querySelectorAll('[data-kb-edit]').forEach(btn => {
      btn.addEventListener('click', () => {
        const conn = kbState.connectors.find(c => c.id === btn.dataset.kbEdit);
        if (conn) openConnectorDrawer(conn);
      });
    });
    root.querySelectorAll('[data-kb-toggle]').forEach(btn => {
      btn.addEventListener('click', async () => {
        const conn = kbState.connectors.find(c => c.id === btn.dataset.kbToggle);
        if (!conn) return;
        btn.disabled = true;
        try {
          const updated = await kbAPI(`/api/knowledge/connectors/${encodeURIComponent(conn.id)}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              name: conn.name,
              type: conn.type,
              endpoint: conn.endpoint,
              config: conn.config || {},
              enabled: !conn.enabled,
            }),
          });
          kbState.connectors = kbState.connectors.map(c => (c.id === conn.id ? updated : c));
          kbToast(updated.enabled ? '已启用连接器' : '已停用连接器');
          renderKnowledgeView();
        } catch (err) {
          kbToast(err.message, true);
          btn.disabled = false;
        }
      });
    });
    root.querySelectorAll('[data-kb-check]').forEach(btn => {
      btn.addEventListener('click', async () => {
        const id = btn.dataset.kbCheck;
        btn.disabled = true;
        try {
          const res = await kbAPI(`/api/knowledge/connectors/${encodeURIComponent(id)}/check`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: '{}',
          });
          kbState.checksByConn[id] = res;
          if (Array.isArray(res?.sources)) kbState.sourcesByConn[id] = res.sources;
          kbToast(res.ok ? `连接正常（${res.latencyMs || 0} ms）` : (res.error || '连接失败'), !res.ok);
          renderKnowledgeView();
        } catch (err) {
          kbState.checksByConn[id] = { ok: false, error: err.message };
          kbToast(err.message, true);
          renderKnowledgeView();
        }
      });
    });
    root.querySelectorAll('[data-kb-sources]').forEach(btn => {
      btn.addEventListener('click', async () => {
        const id = btn.dataset.kbSources;
        btn.disabled = true;
        try {
          const sources = await kbAPI(`/api/knowledge/connectors/${encodeURIComponent(id)}/sources`);
          kbState.sourcesByConn[id] = Array.isArray(sources) ? sources : [];
          renderKnowledgeView();
        } catch (err) {
          kbToast(err.message, true);
          btn.disabled = false;
        }
      });
    });
    root.querySelectorAll('[data-kb-delete]').forEach(btn => {
      btn.addEventListener('click', () => {
        kbState.confirmDeleteId = btn.dataset.kbDelete || '';
        renderKnowledgeView();
      });
    });
    root.querySelectorAll('[data-kb-cancel-delete]').forEach(btn => {
      btn.addEventListener('click', () => {
        kbState.confirmDeleteId = '';
        renderKnowledgeView();
      });
    });
    root.querySelectorAll('[data-kb-confirm-delete]').forEach(btn => {
      btn.addEventListener('click', async () => {
        const id = btn.dataset.kbConfirmDelete;
        btn.disabled = true;
        try {
          await kbAPI(`/api/knowledge/connectors/${encodeURIComponent(id)}`, { method: 'DELETE' });
          delete kbState.checksByConn[id];
          delete kbState.sourcesByConn[id];
          kbState.confirmDeleteId = '';
          kbToast('连接器已删除');
          await loadKnowledgeView();
        } catch (err) {
          kbToast(err.message, true);
          btn.disabled = false;
        }
      });
    });
  }

  function bindHeaderActions() {
    const refreshBtn = document.getElementById('kbRefreshBtn');
    if (refreshBtn && !refreshBtn.dataset.kbBound) {
      refreshBtn.dataset.kbBound = '1';
      refreshBtn.addEventListener('click', loadKnowledgeView);
    }
    const addBtn = document.getElementById('kbAddConnectorBtn');
    if (addBtn && !addBtn.dataset.kbBound) {
      addBtn.dataset.kbBound = '1';
      addBtn.addEventListener('click', () => openConnectorDrawer(null));
    }
  }

  async function loadKnowledgeView() {
    initKBDrawerA11y();
    bindHeaderActions();
    kbState.loading = true;
    kbState.error = '';
    renderKnowledgeView();
    try {
      const [types, connectors] = await Promise.all([
        kbAPI('/api/knowledge/connector-types').catch(() => defaultConnectorTypes()),
        kbAPI('/api/knowledge/connectors'),
      ]);
      kbState.connectorTypes = Array.isArray(types) && types.length ? types : defaultConnectorTypes();
      kbState.connectors = Array.isArray(connectors) ? connectors : [];
    } catch (err) {
      kbState.error = err.message || '加载个人知识库连接器失败';
      kbToast(kbState.error, true);
    } finally {
      kbState.loading = false;
      renderKnowledgeView();
    }
  }

  initKBDrawerA11y();
  bindHeaderActions();
  window.loadKnowledgeView = loadKnowledgeView;
})();
