(function () {
  const OAUTH_CHANNEL_NAME = 'agentflow-knowledge-oauth';
  const OAUTH_SESSION_TIMEOUT_MS = 10 * 60 * 1000;

  const KB_VENDORS = [
    {
      key: 'lexiang',
      name: '腾讯乐问',
      badge: '乐享知识库',
      desc: '支持检索和读取有权限的乐享知识库文档',
      type: 'mcp',
      defaultEndpoint: 'https://lexiang.tencent.com/mcp',
      defaultOAuthMode: 'auto',
      brandTone: 'lexiang',
      iconHTML: '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/><path d="M9 7h6M9 11h4"/></svg>',
    },
    {
      key: 'feishu',
      name: '飞书知识库',
      badge: 'Feishu / Lark',
      desc: '授权后读取个人可见的飞书云文档与知识空间（自动单飞刷新）',
      type: 'mcp',
      defaultEndpoint: 'https://mcp.feishu.cn/mcp',
      defaultOAuthMode: 'preset',
      brandTone: 'feishu',
      iconHTML: '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3L3 8l9 5 9-5-9-5z"/><path d="M3 13l9 5 9-5"/><path d="M3 17.5l9 4.5 9-4.5"/></svg>',
    },
    {
      key: 'tencent_docs',
      name: '腾讯文档',
      badge: 'Tencent Docs',
      desc: '支持检索和读取个人及团队授权范围内的腾讯文档内容',
      type: 'mcp',
      defaultEndpoint: 'https://docs.qq.com/mcp',
      defaultOAuthMode: 'preset',
      brandTone: 'tencent_docs',
      iconHTML: '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="8" y1="13" x2="16" y2="13"/><line x1="8" y1="17" x2="13" y2="17"/></svg>',
    },
    {
      key: 'notion',
      name: 'Notion',
      badge: 'Notion MCP',
      desc: '连接 Notion 工作区，只读检索授权页面与数据库条目',
      type: 'mcp',
      defaultEndpoint: 'https://mcp.notion.com/mcp',
      defaultOAuthMode: 'auto',
      brandTone: 'notion',
      iconHTML: '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="4" y="3" width="16" height="18" rx="2.5"/><path d="M8.5 16V8l7 8V8"/></svg>',
    },
  ];

  const DEFAULT_OAUTH_PRESETS = [
    { key: 'feishu', name: '飞书 / Lark', configured: false, tokenHeader: 'X-Lark-MCP-UAT', tokenPrefix: '' },
    { key: 'tencent_docs', name: '腾讯文档', configured: false, scopes: ['all'], tokenHeader: 'Authorization', tokenPrefix: 'Bearer ' },
    { key: 'lexiang', name: '乐享知识库', configured: false, scopes: ['knowledge:read'], tokenHeader: 'Authorization', tokenPrefix: 'Bearer ' },
    { key: 'notion', name: 'Notion', configured: false, tokenHeader: 'Authorization', tokenPrefix: 'Bearer ' },
  ];

  const OAUTH_ERROR_CODE_MESSAGES = {
    oauth_failed: '第三方授权未完成或令牌交换失败，请重试',
    connector_changed: '授权期间连接器配置已发生变更，请重新发起授权',
    state_invalid: '授权会话已过期或状态校验失败，请重新发起授权',
  };

  const OAUTH_EXTRA_KEYS = new Set([
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

  const kbState = {
    connectorTypes: [],
    connectors: [],
    oauthPresets: DEFAULT_OAUTH_PRESETS.map(p => ({ ...p })),
    oauthRedirectUri: '',
    sourcesByConn: {},
    checksByConn: {},
    loading: false,
    error: '',
    confirmDeleteId: '',
    drawerTriggerEl: null,
    drawerEditingConn: null,
    drawerSessionSeq: 0,
    activeOAuthConnId: '',
    oauthPendingKeys: new Set(),
    oauthPopupWin: null,
    oauthChannel: null,
    oauthSessions: new Map(),
  };

  let kbDrawerA11yBound = false;

  function defaultConnectorTypes() {
    return [
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
        description: '接入遵循 Streamable HTTP MCP 协议的任意只读知识库服务（含 LightCloud 等），仅放行 readOnlyHint 工具',
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
            help: '每行填写一个 Header: Value（例如 X-API-Key: xxx），保存后加密存储',
          },
          {
            key: 'oauth',
            label: 'OAuth 2.0 授权',
            kind: 'oauth',
            required: false,
            help: '开启后按 OAuth 2.0 / MCP OAuth 2.1 完成授权，访问令牌到期自动刷新',
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

  function presetMetaFor(presetKey) {
    const key = String(presetKey || '').trim().toLowerCase();
    return (kbState.oauthPresets || []).find(p => String(p.key || '').toLowerCase() === key)
      || DEFAULT_OAUTH_PRESETS.find(p => p.key === key)
      || null;
  }

  function vendorMetaFor(vendorKey) {
    const key = String(vendorKey || '').trim().toLowerCase();
    return KB_VENDORS.find(v => v.key === key) || null;
  }

  function effectiveOAuthMode(conn) {
    if (!conn) return 'none';
    const raw = String(conn.oauthMode || conn.config?.extra?.oauthMode || '').trim().toLowerCase();
    if (raw === 'auto' || raw === 'preset' || raw === 'custom') return raw;
    return 'none';
  }

  function effectiveOAuthStatus(conn) {
    const mode = effectiveOAuthMode(conn);
    if (mode === 'none') return '';
    const st = String(conn?.oauthStatus || '').trim().toLowerCase();
    if (st === 'authorized' || st === 'expired' || st === 'unauthorized') return st;
    return 'unauthorized';
  }

  function formatOAuthExpiry(iso) {
    if (!iso) return '';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return String(iso);
    const pad = n => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }

  function mapOAuthErrorCode(code) {
    const key = String(code || '').trim().toLowerCase();
    if (key && OAUTH_ERROR_CODE_MESSAGES[key]) {
      return OAUTH_ERROR_CODE_MESSAGES[key];
    }
    return '未检测到有效的 OAuth 授权结果，请确认已在弹窗中完成授权后重试';
  }

  function readPopupErrorCode(popupWin) {
    if (!popupWin) return '';
    try {
      const search = popupWin.location && popupWin.location.search;
      if (!search) return '';
      const params = new URLSearchParams(search);
      if (params.get('status') === 'error') {
        return params.get('code') || 'oauth_failed';
      }
    } catch {}
    return '';
  }

  function resolveRedirectUriForType(typeName) {
    const type = encodeURIComponent(String(typeName || 'mcp').trim() || 'mcp');
    const raw = String(kbState.oauthRedirectUri || '').trim();
    if (raw) {
      if (/\/api\/knowledge\/oauth\/callback$/i.test(raw)) {
        return raw.replace(/\/api\/knowledge\/oauth\/callback$/i, `/api/knowledge/oauth/${type}/callback`);
      }
      return raw;
    }
    const origin = (typeof window !== 'undefined' && window.location && window.location.origin && window.location.origin !== 'null')
      ? window.location.origin
      : '';
    return `${origin}/api/knowledge/oauth/${type}/callback`;
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

  function kbDrawerFocusables(drawer) {
    return drawer.querySelectorAll('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])');
  }

  function ensureOAuthChannel() {
    if (kbState.oauthChannel) return;
    if (typeof BroadcastChannel !== 'function') return;
    try {
      const ch = new BroadcastChannel(OAUTH_CHANNEL_NAME);
      const onMessage = ev => {
        const msg = ev && ev.data;
        if (!msg || (msg.type && msg.type !== 'knowledge-oauth-completed')) return;
        handleOAuthCompletedSignal(msg);
      };
      if (typeof ch.addEventListener === 'function') {
        ch.addEventListener('message', onMessage);
      } else {
        ch.onmessage = onMessage;
      }
      kbState.oauthChannel = ch;
    } catch {}
  }

  async function handleOAuthCompletedSignal(msg) {
    const popupCode = (msg && msg.code) || readPopupErrorCode(kbState.oauthPopupWin);
    const explicitError = (msg && msg.status === 'error') || Boolean(popupCode);
    const targetId = String((msg && msg.connectorId) || kbState.activeOAuthConnId || kbState.drawerEditingConn?.id || '').trim();
    if (!targetId) return;
    kbState.activeOAuthConnId = '';
    kbState.oauthPendingKeys.delete(`conn:${targetId}`);
    endOAuthSessionForConn(targetId);

    try {
      const connectors = await kbAPI('/api/knowledge/connectors');
      kbState.connectors = Array.isArray(connectors) ? connectors : [];
    } catch (err) {
      kbToast(err.message || '刷新连接器授权状态失败', true);
      return;
    }

    const updated = kbState.connectors.find(c => c.id === targetId) || null;
    if (!updated) return;

    const drawer = document.getElementById('kbDrawer');
    const drawerOpen = Boolean(drawer && !drawer.hidden);
    const isCurrentDrawerConn = Boolean(drawerOpen && kbState.drawerEditingConn && kbState.drawerEditingConn.id === updated.id);
    const feedbackEl = isCurrentDrawerConn ? drawer.querySelector('#kbDrawerFeedback') : null;

    if (!explicitError && effectiveOAuthStatus(updated) === 'authorized') {
      kbToast('OAuth 授权成功');
      if (isCurrentDrawerConn) {
        kbState.drawerEditingConn = updated;
        refreshDrawerOAuthStatusUI(drawer, updated);
      }
      renderKnowledgeView();

      try {
        const checkRes = await kbAPI(`/api/knowledge/connectors/${encodeURIComponent(updated.id)}/check`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: '{}',
        });
        kbState.checksByConn[updated.id] = checkRes;
        if (Array.isArray(checkRes?.sources)) {
          kbState.sourcesByConn[updated.id] = checkRes.sources;
        }
        if (feedbackEl) {
          feedbackEl.innerHTML = renderCheckBannerHTML(checkRes);
        }
        renderKnowledgeView();
      } catch (err) {
        if (feedbackEl) {
          feedbackEl.innerHTML = `<div class="kb-check-banner ok"><strong>OAuth 已授权</strong> · 令牌已就绪（探测提示：${kbEsc(err.message)}）</div>`;
        }
      }
      return;
    }

    const friendlyErr = mapOAuthErrorCode(popupCode);
    if (feedbackEl) {
      feedbackEl.innerHTML = renderCheckBannerHTML({ ok: false, error: friendlyErr });
    }
    if (isCurrentDrawerConn) {
      kbState.drawerEditingConn = updated;
      refreshDrawerOAuthStatusUI(drawer, updated);
    }
    renderKnowledgeView();
    kbToast(friendlyErr, true);
  }

  function prepareOAuthPopup() {
    if (typeof window === 'undefined' || typeof window.open !== 'function') return null;
    const w = 620;
    const h = 720;
    const dualLeft = window.screenLeft !== undefined ? window.screenLeft : (window.screenX || 0);
    const dualTop = window.screenTop !== undefined ? window.screenTop : (window.screenY || 0);
    const width = window.innerWidth || document.documentElement?.clientWidth || 1280;
    const height = window.innerHeight || document.documentElement?.clientHeight || 800;
    const left = Math.max(0, Math.round(dualLeft + (width - w) / 2));
    const top = Math.max(0, Math.round(dualTop + (height - h) / 2));
    const features = `popup=yes,width=${w},height=${h},left=${left},top=${top},resizable=yes,scrollbars=yes`;
    const win = window.open('about:blank', 'loopforge_kb_oauth', features);
    kbState.oauthPopupWin = win || null;
    return win || null;
  }

  function navigateOAuthPopup(popupWin, authorizeUrl) {
    if (!popupWin) return false;
    try {
      if (popupWin.location && typeof popupWin.location.replace === 'function') {
        popupWin.location.replace(authorizeUrl);
      } else if (popupWin.location) {
        popupWin.location.href = authorizeUrl;
      } else {
        window.open(authorizeUrl, 'loopforge_kb_oauth');
      }
      if (typeof popupWin.focus === 'function') {
        popupWin.focus();
      }
      return true;
    } catch {
      return false;
    }
  }

  function closeOAuthPopup(popupWin) {
    if (!popupWin) return;
    try {
      if (typeof popupWin.close === 'function') popupWin.close();
    } catch {}
    if (kbState.oauthPopupWin === popupWin) {
      kbState.oauthPopupWin = null;
    }
  }

  function endOAuthSession(key, opts = {}) {
    const session = kbState.oauthSessions.get(key);
    if (!session) return null;
    kbState.oauthSessions.delete(key);
    if (session.timer) clearTimeout(session.timer);
    if (session.connId && kbState.activeOAuthConnId === session.connId) {
      kbState.activeOAuthConnId = '';
    }
    if (opts.closePopup !== false) closeOAuthPopup(session.popupWin);
    return session;
  }

  function endOAuthSessionForConn(connId, opts = {}) {
    const id = String(connId || '').trim();
    if (!id) return;
    for (const session of [...kbState.oauthSessions.values()]) {
      if (session.connId === id || session.key === `conn:${id}`) {
        endOAuthSession(session.key, opts);
      }
    }
  }

  function beginOAuthSession(key, { connId = '', popupWin = null } = {}) {
    endOAuthSession(key);
    if (connId) endOAuthSessionForConn(connId);
    const session = { key, connId, popupWin, timer: null };
    session.timer = setTimeout(() => expireOAuthSession(key), OAUTH_SESSION_TIMEOUT_MS);
    kbState.oauthSessions.set(key, session);
    return session;
  }

  function expireOAuthSession(key) {
    const session = endOAuthSession(key);
    if (!session) return;
    const timeoutMsg = 'OAuth 授权超时，请重新发起授权';
    kbToast(timeoutMsg, true);
    const drawer = document.getElementById('kbDrawer');
    const fb = drawer && !drawer.hidden ? drawer.querySelector('#kbDrawerFeedback') : null;
    if (fb) fb.innerHTML = renderCheckBannerHTML({ ok: false, error: timeoutMsg });
    renderKnowledgeView();
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

  function hydrateFieldValue(field, editingConn) {
    if (!editingConn || !field) return '';
    if (field.secret) return '';
    if (field.key === 'endpoint') return editingConn.endpoint || '';
    if (field.key === 'headers' || field.kind === 'kv') {
      return formatKV(editingConn.config?.headers);
    }
    const extra = editingConn.config?.extra;
    return (extra && extra[field.key] != null) ? String(extra[field.key]) : '';
  }

  function renderOAuthStatusPillHTML(conn) {
    const mode = effectiveOAuthMode(conn);
    if (mode === 'none') {
      return '<span class="hui-badge" data-size="sm" data-tone="neutral">静态凭据模式</span>';
    }
    const status = effectiveOAuthStatus(conn);
    if (status === 'authorized') {
      const exp = formatOAuthExpiry(conn?.oauthExpiresAt);
      return `
        <span class="hui-badge" data-size="sm" data-tone="success">OAuth 已授权</span>
        ${exp ? `<span class="kb-oauth-exp"><span>令牌过期时间：</span>${kbEsc(exp)}<span>（到期自动刷新）</span></span>` : '<span class="kb-oauth-exp">访问令牌已加密托管（到期自动刷新）</span>'}
      `;
    }
    if (status === 'expired') {
      return `
        <span class="hui-badge" data-size="sm" data-tone="error">OAuth 已过期</span>
        <span class="kb-oauth-exp">刷新令牌已失效，请重新授权</span>
      `;
    }
    return `
      <span class="hui-badge" data-size="sm" data-tone="warn">OAuth 待授权</span>
      <span class="kb-oauth-exp">保存并完成一次第三方授权后即可自动刷新令牌</span>
    `;
  }

  function renderOAuthFieldHTML(field, editingConn) {
    const extra = editingConn?.config?.extra || {};
    const rawMode = effectiveOAuthMode(editingConn);
    const uiSegment = (rawMode === 'preset' || rawMode === 'custom') ? 'preset' : (rawMode === 'auto' ? 'auto' : 'none');
    const initialPreset = rawMode === 'custom'
      ? 'custom'
      : (String(extra.oauthPreset || '').trim().toLowerCase() || 'feishu');
    const presetMeta = presetMetaFor(initialPreset);
    const isPresetConfigured = Boolean(presetMeta && presetMeta.configured && initialPreset !== 'custom');
    const redirectUri = String(kbState.oauthRedirectUri || '').trim() || resolveRedirectUriForType(editingConn?.type || 'mcp');
    const status = effectiveOAuthStatus(editingConn);
    const authBtnLabel = (status === 'authorized' || status === 'expired')
      ? '重新授权'
      : '一键前往第三方授权';
    const hasCustomAdvanced = Boolean(
      rawMode === 'custom' ||
      extra.oauthClientId ||
      extra.oauthAuthorizeUrl ||
      extra.oauthTokenUrl ||
      extra.oauthScopes ||
      extra.oauthTokenHeader ||
      extra.oauthTokenPrefix
    );

    const presetsList = kbState.oauthPresets.length ? kbState.oauthPresets : DEFAULT_OAUTH_PRESETS;
    const presetOptionsHTML = [
      ...presetsList.map(p => `<option value="${kbEsc(p.key)}" ${p.key === initialPreset ? 'selected' : ''}>${kbEsc(p.name)}${p.configured ? '（平台已配公共应用）' : ''}</option>`),
      `<option value="custom" ${initialPreset === 'custom' ? 'selected' : ''}>自建应用（自定义 OAuth 端点）</option>`,
    ].join('');

    return `
      <div class="kb-field kb-oauth-card" id="kbOAuthCard" data-kb-field-wrap="${kbEsc(field.key || 'oauth')}" data-kb-oauth-card data-oauth-segment="${kbEsc(uiSegment)}">
        <div class="kb-oauth-card-head">
          <div class="kb-oauth-card-title">
            <label>${kbEsc(field.label || 'OAuth 2.0 授权')}</label>
            ${field.help ? `<small>${kbEsc(field.help)}</small>` : ''}
          </div>
          <div class="kb-oauth-status-line" id="kbOAuthStatusLine">
            ${renderOAuthStatusPillHTML(editingConn)}
          </div>
        </div>

        <div class="kb-oauth-segments" role="tablist" aria-label="鉴权方式">
          <button type="button" class="kb-oauth-seg ${uiSegment === 'none' ? 'active' : ''}" data-kb-oauth-segment="none" data-oauth-seg="none" aria-pressed="${uiSegment === 'none' ? 'true' : 'false'}">
            <span class="kb-oauth-seg-title">免授权 / 静态 Token</span>
            <span class="kb-oauth-seg-sub">使用 API Key 或固定 Header</span>
          </button>
          <button type="button" class="kb-oauth-seg ${uiSegment === 'auto' ? 'active' : ''}" data-kb-oauth-segment="auto" data-oauth-seg="auto" aria-pressed="${uiSegment === 'auto' ? 'true' : 'false'}">
            <span class="kb-oauth-seg-title">MCP OAuth 自动发现</span>
            <span class="kb-oauth-seg-sub">乐享、Notion 推荐</span>
          </button>
          <button type="button" class="kb-oauth-seg ${uiSegment === 'preset' ? 'active' : ''}" data-kb-oauth-segment="preset" data-oauth-seg="preset" aria-pressed="${uiSegment === 'preset' ? 'true' : 'false'}">
            <span class="kb-oauth-seg-title">预注册 OAuth 2.0</span>
            <span class="kb-oauth-seg-sub">飞书、腾讯文档、自建应用</span>
          </button>
        </div>

        <div class="kb-oauth-pane" data-kb-oauth-pane="auto" ${uiSegment === 'auto' ? '' : 'hidden'}>
          <div class="kb-oauth-hint-box">
            <strong>零配置自动发现（MCP OAuth 2.1 + PKCE）</strong>
            <p class="kb-muted">仅需填写上方 MCP 服务地址，无需填写 Client ID；点击下方授权按钮后将自动探测授权端点并完成动态注册。</p>
          </div>
        </div>

        <div class="kb-oauth-pane" id="kbOAuthPresetPane" data-kb-oauth-pane="preset" ${uiSegment === 'preset' ? '' : 'hidden'}>
          <div class="kb-field">
            <label for="kbOAuthPreset">服务商预设</label>
            <select id="kbOAuthPreset" class="kb-input">${presetOptionsHTML}</select>
          </div>

          <div id="kbOAuthPublicNotice" class="kb-check-banner ok" ${isPresetConfigured ? '' : 'hidden'}>
            <strong>已启用平台公共应用，可直接授权</strong>
            <span class="kb-muted">（如需改用自有应用凭据，可展开下方高级参数覆盖）</span>
          </div>

          <div class="kb-field">
            <label for="kbOAuthRedirectUri">OAuth 回调地址（只读，请登记至第三方应用后台）</label>
            <div class="kb-oauth-copy-row">
              <input id="kbOAuthRedirectUri" class="kb-input kb-mono-input" type="text" readonly value="${kbEsc(redirectUri)}">
              <button type="button" class="kb-btn xs" id="kbOAuthCopyRedirectBtn">复制回调地址</button>
            </div>
          </div>

          <details class="kb-oauth-advanced" id="kbOAuthAdvanced" ${hasCustomAdvanced ? 'open' : ''}>
            <summary>高级参数（可选覆盖 Client ID / 授权端点 / Header）</summary>
            <div class="kb-oauth-advanced-grid">
              <div class="kb-field">
                <label for="kbOAuthClientId">Client ID</label>
                <input id="kbOAuthClientId" class="kb-input" type="text" data-kb-oauth-extra="oauthClientId" value="${kbEsc(extra.oauthClientId || '')}" placeholder="覆盖默认或自建应用的 Client ID" autocomplete="off">
              </div>
              <div class="kb-field">
                <label for="kbOAuthScopes">权限范围 Scopes（空格或逗号分隔）</label>
                <input id="kbOAuthScopes" class="kb-input" type="text" data-kb-oauth-extra="oauthScopes" value="${kbEsc(extra.oauthScopes || '')}" placeholder="例如：knowledge:read 或留空使用预设" autocomplete="off">
              </div>
              <div class="kb-field">
                <label for="kbOAuthAuthorizeUrl">授权地址 Authorize URL</label>
                <input id="kbOAuthAuthorizeUrl" class="kb-input" type="url" data-kb-oauth-extra="oauthAuthorizeUrl" value="${kbEsc(extra.oauthAuthorizeUrl || '')}" placeholder="https://example.com/oauth/authorize" autocomplete="off">
              </div>
              <div class="kb-field">
                <label for="kbOAuthTokenUrl">令牌地址 Token URL</label>
                <input id="kbOAuthTokenUrl" class="kb-input" type="url" data-kb-oauth-extra="oauthTokenUrl" value="${kbEsc(extra.oauthTokenUrl || '')}" placeholder="https://example.com/oauth/token" autocomplete="off">
              </div>
              <div class="kb-field">
                <label for="kbOAuthTokenHeader">Token 请求头（默认 Authorization）</label>
                <input id="kbOAuthTokenHeader" class="kb-input" type="text" data-kb-oauth-extra="oauthTokenHeader" value="${kbEsc(extra.oauthTokenHeader || '')}" placeholder="Authorization（飞书为 X-Lark-MCP-UAT）" autocomplete="off">
              </div>
              <div class="kb-field">
                <label for="kbOAuthTokenPrefix">Token 前缀（默认 Bearer ）</label>
                <input id="kbOAuthTokenPrefix" class="kb-input" type="text" data-kb-oauth-extra="oauthTokenPrefix" value="${kbEsc(extra.oauthTokenPrefix || '')}" placeholder="Bearer （飞书留空）" autocomplete="off">
              </div>
            </div>
          </details>
        </div>

        <div class="kb-oauth-actions" id="kbOAuthActionsRow" ${uiSegment === 'none' ? 'hidden' : ''}>
          <button type="button" class="kb-btn primary" id="kbOAuthAuthorizeBtn" data-kb-oauth-authorize>
            ${kbEsc(authBtnLabel)}
          </button>
          <span class="kb-muted">点击后若当前为未保存草稿，将先自动保存连接器再拉起授权窗口</span>
        </div>
      </div>
    `;
  }

  function renderDynamicFieldsHTML(meta, editingConn) {
    const fields = Array.isArray(meta?.fields) ? meta.fields : [];
    if (!fields.length) {
      return '<p class="kb-muted">该连接器类型无需额外字段。</p>';
    }
    const hideStaticToken = effectiveOAuthMode(editingConn) !== 'none';
    return fields.map(field => {
      const key = String(field.key || '');
      const kind = String(field.kind || 'text');
      if (kind === 'oauth') {
        return renderOAuthFieldHTML(field, editingConn);
      }
      const isSecret = Boolean(field.secret || kind === 'secret');
      const id = 'kbField-' + key.replace(/[^A-Za-z0-9_-]/g, '_');
      const val = hydrateFieldValue(field, editingConn);
      let placeholder = field.placeholder || '';
      if (isSecret && editingConn?.secretConfigured) {
        placeholder = '已配置密钥，留空则保持不变';
      }
      const requiredMark = field.required && !(isSecret && editingConn?.secretConfigured)
        ? ' <span class="kb-req">*</span>'
        : '';
      const control = kind === 'kv'
        ? `<textarea id="${kbEsc(id)}" class="kb-input kb-kv-input" rows="3" data-field-key="${kbEsc(key)}" data-field-kind="${kbEsc(kind)}" data-field-secret="${isSecret ? '1' : '0'}" placeholder="${kbEsc(placeholder)}">${kbEsc(val)}</textarea>`
        : `<input id="${kbEsc(id)}" class="kb-input" type="${kbEsc(kind === 'url' ? 'url' : isSecret ? 'password' : kind === 'number' ? 'number' : 'text')}" data-field-key="${kbEsc(key)}" data-field-kind="${kbEsc(kind)}" data-field-secret="${isSecret ? '1' : '0'}" value="${kbEsc(val)}" placeholder="${kbEsc(placeholder)}" autocomplete="off">`;
      const shouldHide = hideStaticToken && key === 'token';
      return `
        <div class="kb-field" data-kb-field-wrap="${kbEsc(key)}" ${shouldHide ? 'hidden' : ''}>
          <label for="${kbEsc(id)}">${kbEsc(field.label || key)}${requiredMark}</label>
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

  function collectDrawerPayload(drawer, editingConn) {
    const typeSelect = drawer.querySelector('#kbConnType');
    const type = editingConn ? editingConn.type : String(typeSelect?.value || '').trim();
    const meta = typeMetaFor(type);
    const name = String(drawer.querySelector('#kbConnName')?.value || '').trim();
    const enabled = Boolean(drawer.querySelector('#kbConnEnabled')?.checked);
    const timeoutRaw = String(drawer.querySelector('#kbConnTimeout')?.value || '').trim();
    const timeoutSec = timeoutRaw ? Math.max(0, Math.min(300, Math.trunc(Number(timeoutRaw) || 0))) : 0;

    const baseExtra = Object.assign({}, editingConn?.config?.extra || {});
    const config = {
      timeoutSec,
      headers: Object.assign({}, editingConn?.config?.headers || {}),
      defaultSources: Array.isArray(editingConn?.config?.defaultSources) ? [...editingConn.config.defaultSources] : [],
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

    const oauthCard = drawer.querySelector('[data-kb-oauth-card]');
    if (oauthCard) {
      const seg = String(oauthCard.dataset.oauthSegment || 'none').trim().toLowerCase();
      if (seg === 'none') {
        const hadOAuth = Boolean(editingConn?.oauthMode || editingConn?.config?.extra?.oauthMode);
        if (hadOAuth) {
          config.extra.oauthMode = 'none';
        } else {
          delete config.extra.oauthMode;
        }
        delete config.extra.oauthPreset;
        drawer.querySelectorAll('[data-kb-oauth-extra]').forEach(inp => {
          const k = inp.dataset.kbOauthExtra;
          if (k) delete config.extra[k];
        });
      } else if (seg === 'auto') {
        config.extra.oauthMode = 'auto';
        drawer.querySelectorAll('[data-kb-oauth-extra]').forEach(inp => {
          const k = inp.dataset.kbOauthExtra;
          if (!k) return;
          const v = String(inp.value || '').trim();
          if (v) config.extra[k] = v;
          else delete config.extra[k];
        });
      } else if (seg === 'preset') {
        const presetSel = drawer.querySelector('#kbOAuthPreset');
        const chosenPreset = String(presetSel?.value || 'feishu').trim().toLowerCase();
        if (chosenPreset === 'custom') {
          config.extra.oauthMode = 'custom';
          delete config.extra.oauthPreset;
        } else {
          config.extra.oauthMode = 'preset';
          config.extra.oauthPreset = chosenPreset;
        }
        drawer.querySelectorAll('[data-kb-oauth-extra]').forEach(inp => {
          const k = inp.dataset.kbOauthExtra;
          if (!k) return;
          const rawVal = String(inp.value || '');
          const val = k === 'oauthTokenPrefix' && rawVal.trim() === 'Bearer' ? 'Bearer ' : rawVal.trim();
          if (val) config.extra[k] = val;
          else delete config.extra[k];
        });
      }
    }

    const switchingStaticToOAuth = Boolean(
      editingConn &&
      editingConn.secretConfigured &&
      effectiveOAuthMode(editingConn) === 'none' &&
      config.extra.oauthMode &&
      config.extra.oauthMode !== 'none'
    );
    const clearSecret = Boolean(drawer.querySelector('#kbConnClearSecret')?.checked) || switchingStaticToOAuth;
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

  function refreshDrawerOAuthStatusUI(drawer, conn) {
    if (!drawer) return;
    const statusLine = drawer.querySelector('#kbOAuthStatusLine');
    if (statusLine) {
      statusLine.innerHTML = renderOAuthStatusPillHTML(conn);
    }
    const authBtn = drawer.querySelector('#kbOAuthAuthorizeBtn');
    if (authBtn) {
      const st = effectiveOAuthStatus(conn);
      authBtn.textContent = (st === 'authorized' || st === 'expired') ? '重新授权' : '一键前往第三方授权';
    }
  }

  function bindOAuthCardEvents(drawer, getEditingConn, setEditingConn, drawerSeq) {
    const card = drawer.querySelector('[data-kb-oauth-card]');
    if (!card) return;
    const isStaleDrawer = () => kbState.drawerSessionSeq !== drawerSeq || drawer.hidden;

    const syncSegmentUI = nextSeg => {
      card.dataset.oauthSegment = nextSeg;
      card.querySelectorAll('[data-kb-oauth-segment]').forEach(btn => {
        const active = btn.dataset.kbOauthSegment === nextSeg;
        btn.classList.toggle('active', active);
        btn.setAttribute('aria-pressed', active ? 'true' : 'false');
      });
      card.querySelectorAll('[data-kb-oauth-pane]').forEach(pane => {
        pane.hidden = pane.dataset.kbOauthPane !== nextSeg;
      });
      const actionsRow = card.querySelector('#kbOAuthActionsRow');
      if (actionsRow) actionsRow.hidden = nextSeg === 'none';
      const tokenWrap = drawer.querySelector('[data-kb-field-wrap="token"]');
      if (tokenWrap) tokenWrap.hidden = nextSeg !== 'none';
      const currentConn = getEditingConn();
      if (nextSeg !== 'none' && currentConn && currentConn.secretConfigured && effectiveOAuthMode(currentConn) === 'none') {
        const clearChk = drawer.querySelector('#kbConnClearSecret');
        if (clearChk) clearChk.checked = true;
      }
    };

    card.querySelectorAll('[data-kb-oauth-segment]').forEach(btn => {
      btn.addEventListener('click', () => {
        syncSegmentUI(btn.dataset.kbOauthSegment || 'none');
      });
    });

    const presetSel = card.querySelector('#kbOAuthPreset');
    if (presetSel) {
      presetSel.addEventListener('change', () => {
        const val = String(presetSel.value || '').trim().toLowerCase();
        const meta = presetMetaFor(val);
        const notice = card.querySelector('#kbOAuthPublicNotice');
        if (notice) {
          notice.hidden = !(meta && meta.configured && val !== 'custom');
        }
        const adv = card.querySelector('#kbOAuthAdvanced');
        if (adv && val === 'custom') {
          adv.open = true;
        }
      });
    }

    const copyBtn = card.querySelector('#kbOAuthCopyRedirectBtn');
    const redirectInput = card.querySelector('#kbOAuthRedirectUri');
    if (copyBtn && redirectInput) {
      copyBtn.addEventListener('click', async () => {
        const text = String(redirectInput.value || '').trim();
        if (!text) return;
        try {
          if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
            await navigator.clipboard.writeText(text);
          } else {
            redirectInput.focus();
            redirectInput.select();
            document.execCommand('copy');
          }
          kbToast('回调地址已复制');
        } catch {
          redirectInput.focus();
          redirectInput.select();
          kbToast('复制失败，请手动选择文本复制', true);
        }
      });
    }

    const authBtn = card.querySelector('#kbOAuthAuthorizeBtn');
    if (authBtn) {
      authBtn.addEventListener('click', async () => {
        const feedbackEl = drawer.querySelector('#kbDrawerFeedback');
        const saveBtn = drawer.querySelector('#kbDrawerSaveBtn');
        let currentConn = getEditingConn();
        const pendingKey = currentConn?.id ? `conn:${currentConn.id}` : 'drawer:draft';
        if (kbState.oauthPendingKeys.has(pendingKey)) return;

        const payload = collectDrawerPayload(drawer, currentConn);

        if (!payload.endpoint) {
          if (feedbackEl) feedbackEl.innerHTML = renderCheckBannerHTML({ ok: false, error: '请先填写 MCP 服务地址' });
          drawer.querySelector('#kbField-endpoint')?.focus();
          return;
        }

        if (!payload.name) {
          const presetKey = payload.config?.extra?.oauthPreset;
          const vMeta = vendorMetaFor(presetKey);
          const pMeta = presetMetaFor(presetKey);
          let fallbackName = vMeta?.name || pMeta?.name || '';
          if (!fallbackName) {
            try {
              fallbackName = 'MCP · ' + new URL(payload.endpoint).hostname;
            } catch {
              fallbackName = '通用知识 MCP';
            }
          }
          payload.name = fallbackName;
          const nameInput = drawer.querySelector('#kbConnName');
          if (nameInput) nameInput.value = fallbackName;
        }

        ensureOAuthChannel();
        const popupWin = prepareOAuthPopup();
        if (!popupWin) {
          if (feedbackEl) {
            feedbackEl.innerHTML = renderCheckBannerHTML({ ok: false, error: '浏览器拦截了授权弹窗，请允许本站弹出窗口后重试' });
          }
          return;
        }

        kbState.oauthPendingKeys.add(pendingKey);
        let savedPendingKey = '';
        let oauthSession = beginOAuthSession(pendingKey, { popupWin });
        authBtn.disabled = true;
        if (saveBtn) saveBtn.disabled = true;
        if (feedbackEl) feedbackEl.innerHTML = '<div class="kb-muted">正在保存连接器配置并准备 OAuth 授权…</div>';

        try {
          const savePath = currentConn
            ? `/api/knowledge/connectors/${encodeURIComponent(currentConn.id)}`
            : '/api/knowledge/connectors';
          const saveMethod = currentConn ? 'PUT' : 'POST';
          const savedConn = await kbAPI(savePath, {
            method: saveMethod,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload),
          });
          currentConn = savedConn;
          savedPendingKey = `conn:${savedConn.id}`;
          kbState.oauthPendingKeys.add(savedPendingKey);
          kbState.activeOAuthConnId = savedConn.id;
          endOAuthSession(pendingKey, { closePopup: false });
          oauthSession = beginOAuthSession(savedPendingKey, { connId: savedConn.id, popupWin });

          const existsIdx = kbState.connectors.findIndex(c => c.id === savedConn.id);
          if (existsIdx >= 0) {
            kbState.connectors[existsIdx] = savedConn;
          } else {
            kbState.connectors.push(savedConn);
          }

          if (!isStaleDrawer()) {
            setEditingConn(savedConn);
            const heading = drawer.querySelector('#kbDrawerTitleHeading');
            if (heading) heading.textContent = '编辑连接器';
            const typeSel = drawer.querySelector('#kbConnType');
            if (typeSel) typeSel.disabled = true;
            refreshDrawerOAuthStatusUI(drawer, savedConn);
          }
          renderKnowledgeView();

          const authType = encodeURIComponent(savedConn.type || payload.type || 'mcp');
          const authRes = await kbAPI(`/api/knowledge/oauth/${authType}/authorize`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ connectorId: savedConn.id }),
          });

          if (!authRes || !authRes.authorizeUrl) {
            throw new Error('未获取到第三方 OAuth 授权地址');
          }
          if (!isStaleDrawer() && authRes.redirectUri && redirectInput) {
            redirectInput.value = authRes.redirectUri;
          }

          if (!navigateOAuthPopup(popupWin, authRes.authorizeUrl)) {
            closeOAuthPopup(popupWin);
            throw new Error('浏览器拦截了授权弹窗，请允许本站弹出窗口后重试');
          }
          if (!isStaleDrawer() && feedbackEl) {
            feedbackEl.innerHTML = '<div class="kb-check-banner ok"><strong>已打开第三方授权窗口</strong> · 请在弹出窗口中完成授权，完成后此页面将自动刷新状态。</div>';
          }
        } catch (err) {
          endOAuthSession(pendingKey);
          if (savedPendingKey && savedPendingKey !== pendingKey) endOAuthSession(savedPendingKey);
          oauthSession = null;
          if (!isStaleDrawer() && feedbackEl) {
            feedbackEl.innerHTML = renderCheckBannerHTML({ ok: false, error: err.message });
          }
        } finally {
          kbState.oauthPendingKeys.delete(pendingKey);
          if (savedPendingKey) kbState.oauthPendingKeys.delete(savedPendingKey);
          if (!isStaleDrawer()) {
            authBtn.disabled = false;
            if (saveBtn) saveBtn.disabled = false;
          }
        }
      });
    }
  }

  function buildVendorDraftConnector(vendorKey) {
    const v = vendorMetaFor(vendorKey);
    if (!v) return null;
    const presetMeta = presetMetaFor(v.key);
    const mode = (presetMeta && presetMeta.configured) ? 'preset' : v.defaultOAuthMode;
    return {
      name: v.name,
      type: v.type || 'mcp',
      endpoint: v.defaultEndpoint,
      enabled: true,
      oauthMode: mode,
      oauthStatus: 'unauthorized',
      config: {
        timeoutSec: 60,
        headers: {},
        defaultSources: [],
        extra: {
          oauthMode: mode,
          oauthPreset: v.key,
        },
      },
    };
  }

  function findConnectorForVendor(vendorKey) {
    const v = vendorMetaFor(vendorKey);
    if (!v) return null;
    return (kbState.connectors || []).find(conn => {
      const preset = String(conn.config?.extra?.oauthPreset || '').trim().toLowerCase();
      if (preset === v.key) return true;
      const endpoint = String(conn.endpoint || '').toLowerCase();
      if (v.key === 'lexiang' && (endpoint.includes('lexiang.tencent.com') || endpoint.includes('lexiangla.com'))) return true;
      if (v.key === 'feishu' && (endpoint.includes('feishu.cn') || endpoint.includes('larksuite.com'))) return true;
      if (v.key === 'tencent_docs' && endpoint.includes('docs.qq.com')) return true;
      if (v.key === 'notion' && endpoint.includes('notion.com')) return true;
      return false;
    }) || null;
  }

  async function triggerVendorOneClickAuth(vendorKey) {
    const v = vendorMetaFor(vendorKey);
    if (!v) return;
    const vendorLockKey = `vendor:${v.key}`;
    if (kbState.oauthPendingKeys.has(vendorLockKey)) return;

    ensureOAuthChannel();
    const popupWin = prepareOAuthPopup();
    if (!popupWin) {
      kbToast('浏览器拦截了授权弹窗，请允许本站弹出窗口后重试', true);
      return;
    }

    kbState.oauthPendingKeys.add(vendorLockKey);
    let conn = findConnectorForVendor(vendorKey);
    let connLockKey = conn?.id ? `conn:${conn.id}` : '';
    if (connLockKey) kbState.oauthPendingKeys.add(connLockKey);
    beginOAuthSession(vendorLockKey, { popupWin });

    try {
      if (!conn) {
        const draft = buildVendorDraftConnector(vendorKey);
        conn = await kbAPI('/api/knowledge/connectors', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            name: draft.name,
            type: draft.type,
            endpoint: draft.endpoint,
            enabled: true,
            config: draft.config,
          }),
        });
        connLockKey = `conn:${conn.id}`;
        kbState.oauthPendingKeys.add(connLockKey);
        kbState.connectors.push(conn);
        renderKnowledgeView();
      } else if (effectiveOAuthMode(conn) === 'none') {
        const presetMeta = presetMetaFor(v.key);
        const mode = (presetMeta && presetMeta.configured) ? 'preset' : v.defaultOAuthMode;
        const nextConfig = Object.assign({}, conn.config || {});
        nextConfig.extra = Object.assign({}, nextConfig.extra || {}, {
          oauthMode: mode,
          oauthPreset: v.key,
        });
        const updateBody = {
          name: conn.name,
          type: conn.type,
          endpoint: conn.endpoint,
          enabled: conn.enabled !== false,
          config: nextConfig,
        };
        if (conn.secretConfigured) {
          updateBody.clearSecret = true;
        }
        conn = await kbAPI(`/api/knowledge/connectors/${encodeURIComponent(conn.id)}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(updateBody),
        });
        kbState.connectors = kbState.connectors.map(c => (c.id === conn.id ? conn : c));
        renderKnowledgeView();
      }

      kbState.activeOAuthConnId = conn.id;
      endOAuthSession(vendorLockKey, { closePopup: false });
      beginOAuthSession(connLockKey, { connId: conn.id, popupWin });
      const authType = encodeURIComponent(conn.type || 'mcp');
      const authRes = await kbAPI(`/api/knowledge/oauth/${authType}/authorize`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ connectorId: conn.id }),
      });
      if (!authRes || !authRes.authorizeUrl) {
        throw new Error('未获取到第三方 OAuth 授权地址');
      }
      if (!navigateOAuthPopup(popupWin, authRes.authorizeUrl)) {
        closeOAuthPopup(popupWin);
        throw new Error('浏览器拦截了授权弹窗，请允许本站弹出窗口后重试');
      }
      kbToast('已打开第三方授权窗口，请在弹窗中完成授权');
    } catch (err) {
      endOAuthSession(vendorLockKey);
      if (connLockKey) endOAuthSession(connLockKey);
      openConnectorDrawer(conn || null, 'mcp', vendorKey);
      const drawer = document.getElementById('kbDrawer');
      const fb = drawer?.querySelector('#kbDrawerFeedback');
      if (fb) {
        fb.innerHTML = renderCheckBannerHTML({ ok: false, error: err.message });
      }
    } finally {
      kbState.oauthPendingKeys.delete(vendorLockKey);
      if (connLockKey) kbState.oauthPendingKeys.delete(connLockKey);
    }
  }

  function openConnectorDrawer(editingConn, preselectedType, preselectedVendorKey) {
    kbState.drawerSessionSeq += 1;
    const drawerSeq = kbState.drawerSessionSeq;
    let activeEditingConn = editingConn || null;
    kbState.drawerEditingConn = activeEditingConn;
    ensureOAuthChannel();

    const vendorDraft = (!activeEditingConn && preselectedVendorKey)
      ? buildVendorDraftConnector(preselectedVendorKey)
      : null;
    const formTargetConn = activeEditingConn || vendorDraft;

    const types = kbState.connectorTypes.length ? kbState.connectorTypes : defaultConnectorTypes();
    const initialType = formTargetConn
      ? formTargetConn.type
      : (preselectedType && typeMetaFor(preselectedType) ? preselectedType : (types[0]?.type || 'weknora'));
    const initialMeta = typeMetaFor(initialType) || types[0];
    const typeOptions = types.map(t => `<option value="${kbEsc(t.type)}" ${t.type === initialType ? 'selected' : ''}>${kbEsc(t.name)} (${kbEsc(t.type)})</option>`).join('');

    const bodyHTML = `
      <div class="kb-field">
        <label for="kbConnType">连接器类型</label>
        <select id="kbConnType" class="kb-input" ${activeEditingConn ? 'disabled' : ''}>${typeOptions}</select>
        <small id="kbConnTypeDesc">${kbEsc(initialMeta?.description || '')}</small>
      </div>
      <div class="kb-field">
        <label for="kbConnName">连接器名称 <span class="kb-req">*</span></label>
        <input id="kbConnName" class="kb-input" type="text" value="${kbEsc(formTargetConn?.name || '')}" placeholder="例如：研发规范库 / 个人 Notion" autocomplete="off">
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

    openKBDrawer(activeEditingConn ? '编辑连接器' : '添加连接器', bodyHTML, footHTML);

    const drawer = document.getElementById('kbDrawer');
    if (!drawer) return;

    const getEditingConn = () => activeEditingConn;
    const setEditingConn = next => {
      activeEditingConn = next;
      kbState.drawerEditingConn = next;
    };
    const isStaleDrawer = () => kbState.drawerSessionSeq !== drawerSeq || drawer.hidden;

    bindOAuthCardEvents(drawer, getEditingConn, setEditingConn, drawerSeq);

    const typeSel = drawer.querySelector('#kbConnType');
    if (typeSel && !activeEditingConn) {
      typeSel.addEventListener('change', () => {
        const nextMeta = typeMetaFor(typeSel.value);
        const descEl = drawer.querySelector('#kbConnTypeDesc');
        if (descEl) descEl.textContent = nextMeta?.description || '';
        const host = drawer.querySelector('#kbDynamicFields');
        if (host) {
          host.innerHTML = renderDynamicFieldsHTML(nextMeta, null);
          bindOAuthCardEvents(drawer, getEditingConn, setEditingConn, drawerSeq);
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
          const payload = collectDrawerPayload(drawer, activeEditingConn);
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
        if (kbState.oauthPendingKeys.has('drawer:draft') || (activeEditingConn?.id && kbState.oauthPendingKeys.has(`conn:${activeEditingConn.id}`))) {
          return;
        }
        const payload = collectDrawerPayload(drawer, activeEditingConn);
        if (!payload.name) {
          if (feedbackEl) feedbackEl.innerHTML = renderCheckBannerHTML({ ok: false, error: '请填写连接器名称' });
          drawer.querySelector('#kbConnName')?.focus();
          return;
        }
        const authBtn = drawer.querySelector('#kbOAuthAuthorizeBtn');
        saveBtn.disabled = true;
        if (authBtn) authBtn.disabled = true;
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
          if (!isStaleDrawer()) {
            saveBtn.disabled = false;
            if (authBtn) authBtn.disabled = false;
          }
        }
      });
    }
  }

  function renderConnectorAuthBadgeHTML(conn) {
    const mode = effectiveOAuthMode(conn);
    if (mode !== 'none') {
      const status = effectiveOAuthStatus(conn);
      if (status === 'authorized') {
        return '<span class="hui-badge" data-size="sm" data-tone="success">OAuth 已授权</span>';
      }
      if (status === 'expired') {
        return '<span class="hui-badge" data-size="sm" data-tone="error">OAuth 已过期</span>';
      }
      return '<span class="hui-badge" data-size="sm" data-tone="warn">OAuth 待授权</span>';
    }
    return `<span class="hui-badge" data-size="sm" data-tone="${conn.secretConfigured ? 'info' : 'neutral'}">${conn.secretConfigured ? '密钥已加密保存' : '无密钥'}</span>`;
  }

  function renderConnectorExtraHTML(conn, meta) {
    const extra = conn.config?.extra;
    if (!extra || typeof extra !== 'object') return '';
    const entries = Object.entries(extra).filter(([k, v]) => String(v || '').trim() && !OAUTH_EXTRA_KEYS.has(k));
    if (!entries.length) return '';
    const fieldsByKey = {};
    (meta?.fields || []).forEach(f => { fieldsByKey[f.key] = f; });
    return `
      <div class="kb-extra-row">
        ${entries.map(([k, v]) => {
          const label = fieldsByKey[k]?.label || k;
          return `<span class="kb-extra-item"><span>${kbEsc(label)}：</span><code>${kbEsc(v)}</code></span>`;
        }).join('')}
      </div>
    `;
  }

  function renderConnectorOAuthMetaHTML(conn) {
    const mode = effectiveOAuthMode(conn);
    if (mode === 'none') return '';
    const presetKey = String(conn.config?.extra?.oauthPreset || '').trim().toLowerCase();
    const presetMeta = presetMetaFor(presetKey);
    const modeLabel = mode === 'auto'
      ? 'MCP OAuth 自动发现'
      : (mode === 'preset' && presetMeta ? `OAuth 预设（${presetMeta.name}）` : '自定义 OAuth 2.0');
    const exp = formatOAuthExpiry(conn.oauthExpiresAt);
    return `
      <span class="kb-muted">鉴权：${kbEsc(modeLabel)}</span>
      ${exp ? `<span class="kb-muted">令牌过期：${kbEsc(exp)}</span>` : ''}
    `;
  }

  function renderConnectorCardHTML(conn) {
    const meta = typeMetaFor(conn.type);
    const checkRes = kbState.checksByConn[conn.id];
    const sources = kbState.sourcesByConn[conn.id];
    const supportsSources = !meta || meta.supportsSubSources !== false;
    const confirmingDelete = kbState.confirmDeleteId === conn.id;
    const oauthMode = effectiveOAuthMode(conn);
    const oauthStatus = effectiveOAuthStatus(conn);

    return `
      <article class="sh-card kb-card" data-kb-card="${kbEsc(conn.id)}" data-connector-id="${kbEsc(conn.id)}" data-oauth-status="${kbEsc(oauthStatus || 'static')}">
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
            ${oauthMode !== 'none'
              ? `<button type="button" class="kb-btn xs ${oauthStatus === 'authorized' ? '' : 'primary'}" data-kb-authorize="${kbEsc(conn.id)}">${oauthStatus === 'authorized' ? '重新授权' : '立即授权'}</button>`
              : ''}
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
          ${renderConnectorOAuthMetaHTML(conn)}
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
      const status = conn ? effectiveOAuthStatus(conn) : '';
      const isOAuthAuthorized = Boolean(conn && status === 'authorized');
      const isStaticConfigured = Boolean(conn && !isOAuthAuthorized && effectiveOAuthMode(conn) === 'none' && conn.secretConfigured);
      const isAuthorized = isOAuthAuthorized || isStaticConfigured;
      const isExpired = Boolean(conn && status === 'expired');
      const stateKey = isAuthorized ? 'authorized' : (isExpired ? 'expired' : 'unauthorized');
      const stateClass = isAuthorized ? 'is-authorized' : (isExpired ? 'is-expired' : 'is-unauthorized');
      const expText = isOAuthAuthorized && conn?.oauthExpiresAt ? formatOAuthExpiry(conn.oauthExpiresAt) : '';

      let statusBadgeHTML = '<span class="hui-badge" data-size="sm" data-tone="muted">未授权</span>';
      if (isOAuthAuthorized) {
        statusBadgeHTML = '<span class="hui-badge" data-size="sm" data-tone="success">OAuth 已授权</span>';
      } else if (isStaticConfigured) {
        statusBadgeHTML = '<span class="hui-badge" data-size="sm" data-tone="info">静态凭据已配置</span>';
      } else if (isExpired) {
        statusBadgeHTML = '<span class="hui-badge" data-size="sm" data-tone="error">OAuth 已过期</span>';
      } else if (conn) {
        statusBadgeHTML = '<span class="hui-badge" data-size="sm" data-tone="warn">OAuth 待授权</span>';
      }

      const actionsHTML = isAuthorized && conn
        ? `
          <button type="button" class="kb-btn xs" data-kb-check="${kbEsc(conn.id)}">测试连接</button>
          <button type="button" class="kb-btn xs" data-kb-sources="${kbEsc(conn.id)}">查看子库</button>
          <button type="button" class="kb-btn xs" data-kb-vendor-auth="${kbEsc(v.key)}">${isOAuthAuthorized ? '重新授权' : '一键授权'}</button>
          <button type="button" class="kb-btn xs" data-kb-edit="${kbEsc(conn.id)}">设置</button>
        `
        : `
          <button type="button" class="kb-btn xs primary" data-kb-vendor-auth="${kbEsc(v.key)}">${isExpired ? '重新授权' : '一键授权'}</button>
          <button type="button" class="kb-btn xs" data-kb-vendor-setup="${kbEsc(v.key)}">设置</button>
        `;

      return `
        <div class="kb-vendor-card ${stateClass}" data-vendor-key="${kbEsc(v.key)}" data-oauth-state="${kbEsc(stateKey)}">
          <div class="kb-vendor-main">
            <div class="kb-vendor-icon" data-brand="${kbEsc(v.brandTone)}">${v.iconHTML}</div>
            <div class="kb-vendor-info">
              <div class="kb-vendor-title-row">
                <strong class="kb-vendor-name">${kbEsc(v.name)}</strong>
                <span class="hui-badge" data-size="sm" data-tone="neutral">官方</span>
                <span class="hui-badge" data-size="sm" data-tone="neutral">一键连接</span>
                ${statusBadgeHTML}
              </div>
              <p class="kb-vendor-desc">${kbEsc(v.desc)}</p>
              ${expText ? `<div class="kb-vendor-meta"><span>令牌过期 </span>${kbEsc(expText)}<span>（自动刷新）</span></div>` : (isStaticConfigured ? '<div class="kb-vendor-meta">使用静态凭据连接</div>' : '')}
            </div>
          </div>
          <div class="kb-vendor-actions">
            ${actionsHTML}
          </div>
        </div>
      `;
    }).join('');

    return `
      <section class="sh-card kb-vendor-section" aria-label="预设知识源">
        <div class="kb-vendor-section-head">
          <div>
            <h2>预设知识源 · 一键连接</h2>
            <p class="kb-muted">预先配置主流知识库厂商，未授权时呈灰阶待机状态，完成 OAuth 授权后自动点亮并托管刷新令牌。</p>
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
          <p class="kb-muted">可从上方预设知识源一键授权，或选择一个连接器类型接入你的自有知识库；配置仅对你本人可见，可在 AI 对话、SOP 节点与手动运行中引用。</p>
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
        <span><strong>只读安全代理</strong>：密钥 AES-GCM 加密托管，沙箱通过无状态代理检索并标注出处，仅放行只读工具。</span>
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
    root.querySelectorAll('[data-kb-vendor-auth]').forEach(btn => {
      btn.addEventListener('click', async () => {
        const vendorKey = btn.dataset.kbVendorAuth;
        btn.disabled = true;
        try {
          await triggerVendorOneClickAuth(vendorKey);
        } finally {
          btn.disabled = false;
        }
      });
    });
    root.querySelectorAll('[data-kb-vendor-setup]').forEach(btn => {
      btn.addEventListener('click', () => {
        const vendorKey = btn.dataset.kbVendorSetup;
        const existing = findConnectorForVendor(vendorKey);
        openConnectorDrawer(existing, 'mcp', vendorKey);
      });
    });
    root.querySelectorAll('[data-kb-authorize]').forEach(btn => {
      btn.addEventListener('click', async () => {
        const conn = kbState.connectors.find(c => c.id === btn.dataset.kbAuthorize);
        if (!conn) return;
        const lockKey = `conn:${conn.id}`;
        if (kbState.oauthPendingKeys.has(lockKey)) return;

        ensureOAuthChannel();
        const popupWin = prepareOAuthPopup();
        if (!popupWin) {
          kbToast('浏览器拦截了授权弹窗，请允许本站弹出窗口后重试', true);
          return;
        }

        kbState.oauthPendingKeys.add(lockKey);
        btn.disabled = true;
        kbState.activeOAuthConnId = conn.id;
        beginOAuthSession(lockKey, { connId: conn.id, popupWin });
        try {
          const authType = encodeURIComponent(conn.type || 'mcp');
          const res = await kbAPI(`/api/knowledge/oauth/${authType}/authorize`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ connectorId: conn.id }),
          });
          if (!res || !res.authorizeUrl) {
            throw new Error('未获取到第三方 OAuth 授权地址');
          }
          if (!navigateOAuthPopup(popupWin, res.authorizeUrl)) {
            closeOAuthPopup(popupWin);
            throw new Error('浏览器拦截了授权弹窗，请允许本站弹出窗口后重试');
          }
          kbToast('已打开第三方授权窗口，请在弹窗中完成授权');
        } catch (err) {
          endOAuthSession(lockKey);
          openConnectorDrawer(conn);
          const drawer = document.getElementById('kbDrawer');
          const fb = drawer?.querySelector('#kbDrawerFeedback');
          if (fb) fb.innerHTML = renderCheckBannerHTML({ ok: false, error: err.message });
        } finally {
          kbState.oauthPendingKeys.delete(lockKey);
          btn.disabled = false;
        }
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
    ensureOAuthChannel();
    kbState.loading = true;
    kbState.error = '';
    renderKnowledgeView();
    try {
      const [types, connectors, presetsPayload] = await Promise.all([
        kbAPI('/api/knowledge/connector-types').catch(() => defaultConnectorTypes()),
        kbAPI('/api/knowledge/connectors'),
        kbAPI('/api/knowledge/oauth/presets').catch(() => null),
      ]);
      kbState.connectorTypes = Array.isArray(types) && types.length ? types : defaultConnectorTypes();
      kbState.connectors = Array.isArray(connectors) ? connectors : [];
      if (presetsPayload && typeof presetsPayload === 'object') {
        if (Array.isArray(presetsPayload.presets) && presetsPayload.presets.length) {
          kbState.oauthPresets = presetsPayload.presets;
        }
        if (typeof presetsPayload.redirectUri === 'string' && presetsPayload.redirectUri.trim()) {
          kbState.oauthRedirectUri = presetsPayload.redirectUri.trim();
        }
      }
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
