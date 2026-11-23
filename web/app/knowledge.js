(function () {
  const kbState = {
    tab: 'sources',
    settings: null,
    sources: [],
    selectedSourceId: '',
    docKeyword: '',
    docPage: 1,
    docs: null,
    searchQuery: '',
    searchSources: [],
    searchResult: null,
    candidates: [],
    submissions: [],
    subFilter: 'pending',
    usage: null,
    loading: false,
    connectionFilter: '',
    capabilitiesByConn: {},
    modelsByConn: {},
    drawerTriggerEl: null
  };

  // 连接级管理能力徽标：库管理 / 文档写入 / 分块读取 / 预览下载 / 重解析。
  const KB_CAPABILITIES = [
    { key: 'kb_manage', label: '库管理' },
    { key: 'doc_write', label: '文档写入' },
    { key: 'chunk_read', label: '分块读取' },
    { key: 'preview_download', label: '预览下载' },
    { key: 'reparse', label: '重解析' }
  ];
  // 后端还会独立探测 preview / download 两个端点权限，徽标不展示它们，
  // 但门禁必须记录：某一端点被拒时只禁用该动作，不影响另一个。
  const KB_ENDPOINT_CAPABILITIES = ['preview', 'download'];
  const KB_ALL_CAPABILITY_KEYS = KB_CAPABILITIES.map(c => c.key).concat(KB_ENDPOINT_CAPABILITIES);
  const KB_ENDPOINT_FALLBACK = { preview: 'preview_download', download: 'preview_download' };
  // 管理类审计动作（tool 前缀 admin:）的可读名称，非管理类工具原样展示。
  const KB_ADMIN_ACTION_LABELS = {
    'admin:kb.create': '创建知识库',
    'admin:kb.update': '更新知识库',
    'admin:kb.delete': '删除知识库',
    'admin:model.create': '配置向量模型',
    'admin:conn.probe': '探测连接',
    'admin:conn.provision': '自动初始化 WeKnora',
    'admin:conn.remove': '移除连接',
    'admin:doc.upload': '上传文档',
    'admin:doc.url': '链接导入文档',
    'admin:doc.create': '创建手工文档',
    'admin:doc.update': '更新手工文档',
    'admin:doc.reparse': '重解析文档',
    'admin:doc.delete': '删除文档',
    'admin:doc.download': '下载文档'
  };

  function kbAuditTool(tool) {
    const name = String(tool || '');
    if (name.indexOf('admin:') !== 0) return kbEsc(name);
    const label = KB_ADMIN_ACTION_LABELS[name] || name;
    return `<span class="hui-badge" data-tone="info" data-size="sm" title="${kbEsc(name)}">${kbEsc(label)}</span>`;
  }

  // 能力不可用原因与探测在途标记不进 kbState，避免触发整页重绘。
  const kbCapabilityReasons = {};
  const kbCapabilityFetching = {};
  const kbCapabilityProbed = {};
  // 后端本次探测实际返回了哪些能力键：只有键缺失（旧后端）才允许回退到
  // 聚合 preview_download；键存在但状态为 unknown 时不得回退。
  const kbCapabilityPresent = {};
  let kbRenderTimer = 0;
  // 文档解析/删除的中间态：命中时 4s 轮询刷新文档列表，直到收敛到终态。
  const KB_POLL_STATUSES = ['pending', 'queued', 'processing', 'parsing', 'finalizing', 'deleting'];
  let kbDocsPollTimer = 0;

  // 文档详情抽屉状态：分段视图、分块分页、预览 blob 与手工文档编辑。
  const kbDocView = {
    docId: '',
    connId: '',
    title: '',
    docLoading: false,
    detail: null,
    view: 'chunks',
    chunkPage: 1,
    chunkPageSize: 10,
    chunks: null,
    chunkLegacy: false,
    chunkError: '',
    chunkLoading: false,
    preview: null,
    previewError: '',
    previewLoading: false,
    objectUrl: '',
    editContent: '',
    editContentState: 'idle',
    editPublish: true
  };

  function scheduleKBRender() {
    if (kbRenderTimer) return;
    kbRenderTimer = window.setTimeout(() => {
      kbRenderTimer = 0;
      renderKnowledgeShell();
    }, 0);
  }

  function kbProjectId() {
    return (typeof state !== 'undefined' && state && state.projectFilter) || '';
  }

  function kbEsc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  // 动态文案跟随界面语言：lfT 由 i18n.js 注入，缺失时保留中文。
  function kbLfText(zh, en) {
    return typeof window.lfT === 'function' ? window.lfT(zh, en) : zh;
  }

  function kbToast(msg, isError) {
    if (typeof toast === 'function') {
      toast(msg, isError ? 'err' : 'ok');
    }
    const status = document.getElementById('kbStatusBar');
    if (status) {
      status.textContent = msg;
      status.style.color = isError ? 'var(--hui-error)' : 'var(--hui-success)';
    }
    const fb = document.getElementById('kbConfigFeedback');
    if (fb) {
      fb.textContent = msg;
      fb.style.color = isError ? 'var(--hui-error)' : 'var(--hui-success)';
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
      err.code = (data && data.code) || '';
      err.capability = (data && data.capability) || '';
      err.connectionId = (data && data.connectionId) || '';
      err.reason = (data && data.reason) || '';
      // 能力载荷中的 status 表示能力状态（available/unavailable/unknown），与 HTTP 状态码无关。
      err.capabilityStatus = (data && data.status) || '';
      err.notFound = res.status === 404;
      throw err;
    }
    return data;
  }

  function kbConnections() {
    const conns = kbState.settings && kbState.settings.connections;
    return Array.isArray(conns) ? conns : [];
  }

  // 已配置连接：有服务地址且凭据已配置（或连接本身启用）即可管理。
  // enabled=false 只关闭 Agent 检索，不关闭人用管理面。
  function kbConfiguredConnections() {
    const conns = kbConnections();
    if (conns.length) {
      return conns.filter(c => c && String(c.url || '').trim() && (c.tokenConfigured || c.enabled !== false));
    }
    const s = kbState.settings || {};
    if (String(s.url || '').trim() && s.tokenConfigured) {
      return [{ id: 'default', name: '默认连接', url: s.url, tokenConfigured: true, enabled: !!s.ready }];
    }
    return [];
  }

  function kbHasConfiguredConnections() {
    return kbConfiguredConnections().length > 0;
  }

  function kbDisabledNoticeHTML() {
    return `<div class="kb-panel kb-notice" style="margin-bottom:12px"><p class="kb-muted">所有已配置连接均未启用，Agent 检索暂不可用；仍可在此管理知识库与文档，启用连接后即可恢复检索。</p></div>`;
  }

  function stripScopedId(id) {
    const raw = String(id || '');
    const idx = raw.indexOf('::');
    return idx > 0 ? raw.slice(idx + 2) : raw;
  }

  // 显式连接寻址：作用域前缀 > 当前筛选连接 > 调用方显式传入 > 唯一已配置连接。
  // 已配置凭据但 enabled=false 的连接同样参与解析，便于先建库传文档再开启 Agent 检索。
  function resolveExplicitConnectionId(sourceOrDocId, fallbackConnId) {
    const raw = String(sourceOrDocId || '');
    const idx = raw.indexOf('::');
    if (idx > 0) return raw.slice(0, idx);
    if (kbState.connectionFilter) return kbState.connectionFilter;
    if (fallbackConnId) return fallbackConnId;
    const conns = kbConnections();
    if (conns.length === 1) return conns[0].id || '';
    return '';
  }

  function kbConnQuery(connId, extra) {
    const q = new URLSearchParams();
    q.set('projectId', kbProjectId());
    const cid = connId || '';
    if (cid) q.set('connectionId', cid);
    Object.keys(extra || {}).forEach(k => {
      const v = extra[k];
      if (v === undefined || v === null || v === '') return;
      q.set(k, String(v));
    });
    return q.toString();
  }

  function kbCapabilitiesFor(connId) {
    return (kbState.capabilitiesByConn[connId] = kbState.capabilitiesByConn[connId] || {});
  }

  function kbCapabilityState(connId, cap) {
    const caps = kbState.capabilitiesByConn[connId];
    return (caps && caps[cap]) || 'unknown';
  }

  function kbCapabilityReason(connId, cap) {
    const reason = kbCapabilityReasons[connId + '|' + cap];
    if (reason) return reason;
    const fallback = KB_ENDPOINT_FALLBACK[cap];
    return fallback ? (kbCapabilityReasons[connId + '|' + fallback] || '') : '';
  }

  function kbCapabilityKeyPresent(connId, cap) {
    const present = kbCapabilityPresent[connId];
    return !!(present && present[cap]);
  }

  // 能力门禁：未探测或后端未提供能力接口时沿用旧行为（乐观放行），
  // 后端明确声明不足或返回 503 后才禁用对应写操作。端点级能力键缺失
  // （旧后端）时回退到聚合 preview_download；键存在但为 unknown 时
  // 它是后端的明确结论，不能再被聚合结果覆盖。
  function kbCapabilityAllowed(connId, cap) {
    if (kbCapabilityProbed[connId] !== 'ok') return true;
    const state = kbCapabilityState(connId, cap);
    if (state === 'available') return true;
    // 键存在即后端的明确结论：unknown / unavailable 都不放行。
    if (kbCapabilityKeyPresent(connId, cap)) return false;
    // 键缺失：能力接口 404 的旧后端，或 404 后由 503 局部降级过的连接。
    // 端点键缺失时先看聚合键；连聚合也不存在才回退到旧的乐观放行语义。
    const fallback = KB_ENDPOINT_FALLBACK[cap];
    if (fallback && kbCapabilityKeyPresent(connId, fallback)) {
      return kbCapabilityState(connId, fallback) === 'available';
    }
    return true;
  }

  function kbCapabilityTone(status) {
    if (status === 'available') return 'success';
    if (status === 'unavailable') return 'error';
    return 'neutral';
  }

  function markCapabilityUnavailable(connId, cap, reason) {
    if (!cap) return;
    const cid = connId || '';
    kbCapabilityProbed[cid] = 'ok';
    kbCapabilitiesFor(cid)[cap] = 'unavailable';
    kbCapabilityPresent[cid] = kbCapabilityPresent[cid] || {};
    kbCapabilityPresent[cid][cap] = true;
    if (reason) kbCapabilityReasons[cid + '|' + cap] = reason;
    // 端点被实时拒绝后同步重算聚合结论：两个端点都不可用时聚合也必须
    // 不可用，避免徽标仍显示「可用」；只缺一个时聚合保持可用，让另一个
    // 动作继续工作。
    if (cap === 'preview' || cap === 'download') {
      const other = cap === 'preview' ? 'download' : 'preview';
      if (kbCapabilityState(cid, other) === 'unavailable') {
        kbCapabilitiesFor(cid).preview_download = 'unavailable';
        kbCapabilityPresent[cid].preview_download = true;
        if (reason) kbCapabilityReasons[cid + '|preview_download'] = reason;
      }
    }
  }

  function kbNormalizeCapabilities(raw, connId) {
    const out = {};
    const cid = connId || '';
    // 每次全量归一化先清掉旧 reason，避免后端本次未返回原因时残留上一次的失效说明。
    KB_ALL_CAPABILITY_KEYS.forEach(key => { delete kbCapabilityReasons[cid + '|' + key]; });
    if (!raw || typeof raw !== 'object') {
      kbCapabilityPresent[cid] = {};
      KB_ALL_CAPABILITY_KEYS.forEach(key => { out[key] = 'unknown'; });
      return out;
    }
    const src = raw.capabilities && typeof raw.capabilities === 'object' ? raw.capabilities : raw;
    const present = {};
    KB_ALL_CAPABILITY_KEYS.forEach(key => {
      const v = src[key];
      if (v !== undefined && v !== null) present[key] = true;
      let status = 'unknown';
      let reason = '';
      if (typeof v === 'string') status = v;
      else if (v && typeof v === 'object') {
        status = v.status || 'unknown';
        reason = v.reason || v.message || '';
      }
      if (['available', 'unavailable', 'unknown'].indexOf(status) < 0) status = 'unknown';
      out[key] = status;
      if (reason) kbCapabilityReasons[cid + '|' + key] = reason;
    });
    kbCapabilityPresent[cid] = present;
    return out;
  }

  function kbHasCapabilityPayload(raw) {
    if (!raw || typeof raw !== 'object') return false;
    if (raw.capabilities && typeof raw.capabilities === 'object') return true;
    return KB_ALL_CAPABILITY_KEYS.some(key => raw[key] !== undefined);
  }

  function kbMergeCapabilities(connId, raw) {
    const cid = connId || '';
    kbState.capabilitiesByConn[cid] = kbNormalizeCapabilities(raw, cid);
    kbCapabilityProbed[cid] = kbHasCapabilityPayload(raw) ? 'ok' : 'legacy';
    return kbState.capabilitiesByConn[cid];
  }

  // 当前抽屉操作的目标连接：普通抽屉（新建/编辑/导入）显式登记，避免降级误记到上一个文档连接。
  let kbOperationConnId = '';

  // 503 capability_unavailable：把对应连接的能力就地降级为不可用，并提示原因。
  // 仅当 HTTP 503 + 错误码 + 合法能力键同时成立时才降级，避免上游普通错误误禁用管理面。
  function handleCapabilityError(err, fallbackConnId) {
    if (!err || err.code !== 'capability_unavailable' || err.httpStatus !== 503) return false;
    if (!KB_ALL_CAPABILITY_KEYS.includes(err.capability)) return false;
    const drawer = document.getElementById('kbDrawer');
    const docDrawerOpen = !!(drawer && !drawer.hidden && kbDocView.docId);
    // 优先使用请求发起时捕获的连接，避免在途请求跨越抽屉生命周期后误记到另一个连接。
    const connId = err.connectionId
      || fallbackConnId
      || (docDrawerOpen ? kbDocView.connId : '')
      || kbOperationConnId
      || kbState.connectionFilter
      || '';
    markCapabilityUnavailable(connId, err.capability, err.reason || err.message);
    kbToast((err.message || '当前连接缺少该操作所需权限') + (err.reason ? '：' + err.reason : ''), true);
    if (docDrawerOpen) renderKBDocBody();
    scheduleKBRender();
    return true;
  }

  function kbToastError(err, fallbackConnId) {
    if (!err) return;
    if (handleCapabilityError(err, fallbackConnId)) return;
    kbToast(err.message, true);
  }

  let kbDrawerA11yBound = false;
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

  async function loadConnectionCapabilities(connId) {
    if (!connId || kbCapabilityFetching[connId] || kbCapabilityProbed[connId]) return;
    kbCapabilityFetching[connId] = true;
    try {
      const raw = await kbAPI('/api/knowledge/connections/' + encodeURIComponent(connId) + '/capabilities');
      kbMergeCapabilities(connId, raw);
    } catch (err) {
      if (err.notFound) {
        // 能力探测路由尚未合入：标记为 legacy，徽标展示「未知」且不重复请求。
        kbState.capabilitiesByConn[connId] = {};
        kbCapabilityPresent[connId] = {};
        kbCapabilityProbed[connId] = 'legacy';
      } else if (!handleCapabilityError(err, connId)) {
        kbCapabilityProbed[connId] = 'unknown';
      }
    } finally {
      delete kbCapabilityFetching[connId];
    }
  }

  function kbEnsureConnectionCapabilities() {
    // 含单连接 legacy 合成的 {id:'default'}：否则 legacy 模式永远探测不到能力。
    kbConfiguredConnections().forEach(c => {
      if (!c || !c.id || kbCapabilityProbed[c.id] || kbCapabilityFetching[c.id]) return;
      loadConnectionCapabilities(c.id).then(() => {
        if (kbState.tab === 'settings' || kbState.tab === 'sources') scheduleKBRender();
      });
    });
  }

  function kbCapabilityBadges(connId) {
    return `<div class="kb-cap-bar">${KB_CAPABILITIES.map(c => {
      const st = kbCapabilityState(connId, c.key);
      const reason = kbCapabilityReason(connId, c.key);
      const fallback = st === 'available' ? `${c.label}可用` : (st === 'unavailable' ? `${c.label}不可用` : `${c.label}能力未知`);
      return `<span class="hui-badge" data-tone="${kbCapabilityTone(st)}" data-size="sm" title="${kbEsc(reason || fallback)}">${kbEsc(c.label)}</span>`;
    }).join('')}</div>`;
  }

  async function kbProbeConnection(connId, button) {
    if (!connId) return;
    if (button) button.disabled = true;
    try {
      const res = await kbAPI('/api/knowledge/check', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ connectionId: connId })
      });
      kbMergeCapabilities(connId, res);
      const ok = !res || res.ok !== false;
      kbToast(ok ? '连接探测完成' : (res.error || '连接不可用'), !ok);
      scheduleKBRender();
    } catch (err) {
      kbToastError(err, connId);
      scheduleKBRender();
    } finally {
      if (button) button.disabled = false;
    }
  }

  function isWeKnora() {
    return (kbState.settings && (kbState.settings.provider || 'lightcloud')) === 'weknora';
  }

  function kbConnectionById(connId) {
    const id = String(connId || '');
    if (!id) return null;
    return kbConnections().find(c => c.id === id) || kbConfiguredConnections().find(c => c.id === id) || null;
  }

  // 按连接判定引擎：连接未写 provider 时继承顶层配置，与后端 EffectiveProvider 一致。
  // 单连接 legacy 的 {id:'default'} 走同一回退，因此能力探测与建库抽屉都能用。
  function kbConnProvider(connId) {
    const conn = kbConnectionById(connId);
    const provider = (conn && conn.provider) || (kbState.settings && kbState.settings.provider) || 'lightcloud';
    return String(provider).toLowerCase();
  }

  function isConnWeKnora(connId) {
    return kbConnProvider(connId) === 'weknora';
  }

  function isSourceWeKnora(src) {
    if (!src) return false;
    return isConnWeKnora(resolveExplicitConnectionId(src.id));
  }

  // 写权限：优先用后端与 requireKnowledgeWriter 同源透传的 canWrite；
  // 旧后端无该字段时按 bots.js 的同一判定思路回退，最后退到 canManage。
  function kbCanWrite() {
    const s = kbState.settings || {};
    if (s.canWrite === true || s.canWrite === false) return s.canWrite;
    if (typeof state === 'undefined' || !state || !state.authRequired || !state.currentUser) return true;
    if (typeof isPlatformAdmin === 'function' && isPlatformAdmin()) return true;
    const project = typeof currentProject === 'function' ? currentProject() : null;
    if (!project) return !!s.canManage;
    const user = Number(state.currentUser.id);
    if (Number(project.ownerId) === user) return true;
    return (project.members || []).some(m => Number(m.userId) === user && m.role !== 'viewer');
  }

  function kbCanWriteReason() {
    return '当前账号在该项目没有知识库写权限（需项目写成员或平台管理员）';
  }

  function resolveConnectionForSource(sourceId) {
    const conns = kbState.settings?.connections || [];
    if (!conns.length) return null;
    const idx = String(sourceId || '').indexOf('::');
    if (idx > 0) {
      const cid = sourceId.slice(0, idx);
      // 未知连接前缀必须安全失败，绝不回退到第一个连接（避免跨实例误操作）。
      return conns.find(c => c.id === cid) || null;
    }
    return conns.length === 1 ? conns[0] : null;
  }

  function resolveWeKnoraConsoleURL(s, conn) {
    const raw = (conn && conn.url) || s?.weKnoraUiUrl || (isWeKnora() && s?.url ? s.url : '');
    if (!raw) return '';
    try {
      const u = new URL(raw);
      const internalHosts = ['weknora-app', '172.17.0.1', 'host.docker.internal', 'localhost', '127.0.0.1'];
      if (internalHosts.includes(u.hostname)) {
        const wasAppContainer = u.hostname === 'weknora-app';
        u.hostname = window.location.hostname;
        if (wasAppContainer) u.port = '18081';
        else if (u.port === '8080') u.port = '';
      }
      return u.toString();
    } catch {
      return raw;
    }
  }

  function kbDrawerFocusables(drawer) {
    return drawer.querySelectorAll('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])');
  }

  function closeKBDrawer() {
    const drawer = document.getElementById('kbDrawer');
    const scrim = document.getElementById('kbDrawerScrim');
    if (!drawer) return;
    kbReleasePreviewURL();
    kbCancelDocPreview();
    kbDocView.docId = '';
    kbDocView.connId = '';
    kbOperationConnId = '';
    kbDocView.detail = null;
    kbDocView.chunks = null;
    kbDocView.preview = null;
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
    if (trigger && typeof trigger.focus === 'function' && document.contains(trigger)) trigger.focus();
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
        <h3>${kbEsc(title)}</h3>
        <button type="button" class="kb-btn" data-kb-drawer-close>关闭</button>
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

  // 控制台凭据：只对平台管理员展示内置 WeKnora 实例的登录账号。
  // 外部连接的密码平台从未保存，这里明确说明而不是猜测。
  let kbCredEpoch = 0;

  async function openKBConsoleCredentials(conn) {
    const epoch = ++kbCredEpoch;
    const consoleHref = resolveWeKnoraConsoleURL(kbState.settings, conn);
    const openLink = consoleHref
      ? `<a class="kb-btn primary" href="${kbEsc(consoleHref)}" target="_blank" rel="noopener">打开 WeKnora 控制台 ↗</a>`
      : '';
    let data = null;
    let error = null;
    try {
      const credPath = conn?.id
        ? `/api/knowledge/weknora/console-credentials?connectionId=${encodeURIComponent(conn.id)}`
        : '/api/knowledge/weknora/console-credentials';
      data = await kbAPI(credPath);
    } catch (err) {
      error = err || { message: '' };
    }
    // 连续点击时只采用最后一次请求的结果。
    if (epoch !== kbCredEpoch) return;
    const email = (data && data.email) || conn?.account || '';
    if (error) {
      const status = Number(error.httpStatus) || 0;
      const hint = status === 401
        ? '登录状态已失效，请重新登录后再试。'
        : status === 403
          ? '只有平台管理员可以查看登录凭据。'
          : '请稍后重试，或到「高级配置」中直接登录。';
      openKBDrawer('WeKnora 控制台登录凭据', `
        <p class="kb-muted kb-cred-error">读取登录凭据失败：${kbEsc(error.message || '未知错误')}</p>
        <p class="kb-muted">${kbEsc(hint)}</p>
      `, openLink);
      return;
    }
    if (!data || !data.configured) {
      openKBDrawer('WeKnora 控制台登录凭据', `
        <p class="kb-muted">平台没有保存这个连接的登录密码。外部 WeKnora 请使用接入时填写的管理员账号；内置实例需要配置 LOOPFORGE_WEKNORA_ADMIN_PASSWORD，或完成一次自动初始化。</p>
        ${email ? `<div class="kb-cred-row"><span>登录邮箱</span><code data-i18n-data>${kbEsc(email)}</code></div>` : ''}
      `, openLink);
      return;
    }
    const password = data.password || '';
    openKBDrawer('WeKnora 控制台登录凭据', `
      <p class="kb-muted">仅平台管理员可见。${data.source === 'environment'
        ? '密码来自部署环境变量 LOOPFORGE_WEKNORA_ADMIN_PASSWORD。'
        : '密码在首次自动初始化时生成，并保存在平台配置中。'}</p>
      <div class="kb-cred-row">
        <span>登录邮箱</span>
        <code data-i18n-data>${kbEsc(email)}</code>
        <button type="button" class="kb-btn xs" id="kbConsoleCredCopyEmail">复制</button>
      </div>
      <div class="kb-cred-row">
        <span>登录密码</span>
        <code id="kbConsoleCredValue" data-revealed="0">••••••••••</code>
        <button type="button" class="kb-btn xs" id="kbConsoleCredToggle">显示</button>
        <button type="button" class="kb-btn xs" id="kbConsoleCredCopy">复制密码</button>
      </div>
      <p class="kb-muted">若在 WeKnora 里改过密码，这里显示的值会失效；请同步修改 WeKnora 账号、.env 与平台配置。</p>
    `, openLink);
    document.getElementById('kbConsoleCredCopyEmail')?.addEventListener('click', () => kbCopyText(email, '邮箱已复制'));
    const valueEl = document.getElementById('kbConsoleCredValue');
    const toggleBtn = document.getElementById('kbConsoleCredToggle');
    toggleBtn?.addEventListener('click', () => {
      const revealed = valueEl?.dataset.revealed === '1';
      if (valueEl) {
        valueEl.textContent = revealed ? '••••••••••' : password;
        valueEl.dataset.revealed = revealed ? '0' : '1';
      }
      if (toggleBtn) toggleBtn.textContent = revealed ? kbLfText('显示', 'Show') : kbLfText('隐藏', 'Hide');
    });
    document.getElementById('kbConsoleCredCopy')?.addEventListener('click', () => kbCopyText(password, '密码已复制'));
  }

  // 平台多用 http 内网地址访问，navigator.clipboard 在非安全上下文不可用，
  // 因此保留 textarea + execCommand 的兜底。
  function kbCopyText(value, okMsg) {
    if (!value) return;
    const failMsg = '复制失败，请手动选择文本复制';
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(value)
        .then(() => kbToast(okMsg))
        .catch(() => {
          const ok = kbFallbackCopy(value);
          kbToast(ok ? okMsg : failMsg, !ok);
        });
      return;
    }
    const ok = kbFallbackCopy(value);
    kbToast(ok ? okMsg : failMsg, !ok);
  }

  function kbFallbackCopy(value) {
    try {
      const ta = document.createElement('textarea');
      ta.value = value;
      ta.setAttribute('readonly', '');
      ta.style.cssText = 'position:fixed;top:-9999px;left:-9999px';
      document.body.appendChild(ta);
      ta.select();
      let ok = false;
      try { ok = document.execCommand('copy'); } catch { ok = false; }
      document.body.removeChild(ta);
      return !!ok;
    } catch {
      return false;
    }
  }

  // 整页加载与文档列表各自维护单调序号：晚到的旧响应直接丢弃，避免 A 库内容覆盖 B 库。
  let kbViewEpoch = 0;
  let kbDocsEpoch = 0;
  let kbAutoProvisionAttempted = false;

  async function loadKnowledgeView() {
    const root = document.getElementById('knowledgeRoot');
    if (!root) return;
    const epoch = ++kbViewEpoch;
    kbState.loading = true;
    renderKnowledgeShell();
    try {
      const pid = encodeURIComponent(kbProjectId());
      // canWrite 与后端写接口同源：按当前项目计算，切换项目刷新时同步更新。
      let settings = await kbAPI('/api/knowledge/settings?projectId=' + pid);
      if (epoch !== kbViewEpoch) return;
      if (
        !kbAutoProvisionAttempted &&
        settings &&
        !settings.ready &&
        settings.canManage &&
        settings.detectedWeKnoraUrl &&
        !settings.updatedAt &&
        !settings.updatedBy &&
        !settings.url &&
        !settings.tokenConfigured &&
        !(settings.connections && settings.connections.length)
      ) {
        kbAutoProvisionAttempted = true;
        try {
          await kbAPI('/api/knowledge/weknora/provision', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              mode: 'managed',
              name: '内置 WeKnora',
              writeEnabled: true,
              onlyIfFresh: true
            })
          });
          settings = await kbAPI('/api/knowledge/settings?projectId=' + pid);
          if (epoch !== kbViewEpoch) return;
        } catch {
          // 首次自动绑定失败时不阻断页面加载，管理员仍可在接入配置页手动重试。
        }
      }
      kbState.settings = settings;
      // Agent 检索开关（ready）不等于人用管理面开关：只要存在已配置连接就保留管理入口。
      const canBrowse = !!kbState.settings.ready || kbHasConfiguredConnections();
      if (canBrowse) {
        const sourcesPath = '/api/knowledge/sources?projectId=' + pid
          + (kbState.connectionFilter ? '&connectionId=' + encodeURIComponent(kbState.connectionFilter) : '');
        const [sources, usage, candidates, submissions] = await Promise.all([
          kbAPI(sourcesPath).catch(() => []),
          kbAPI('/api/knowledge/usage?projectId=' + pid).catch(() => ({ agents: [], totalAgents: 0, activity: [] })),
          kbAPI('/api/knowledge/candidates?projectId=' + pid).catch(() => []),
          kbAPI('/api/knowledge/submissions?projectId=' + pid).catch(() => [])
        ]);
        if (epoch !== kbViewEpoch) return;
        kbState.sources = Array.isArray(sources) ? sources : [];
        kbState.usage = usage;
        kbState.candidates = Array.isArray(candidates) ? candidates : [];
        kbState.submissions = Array.isArray(submissions) ? submissions : [];
        if (!kbState.selectedSourceId || !kbState.sources.some(s => s.id === kbState.selectedSourceId)) {
          const firstAllowed = kbState.sources.find(s => s.allowed) || kbState.sources[0];
          kbState.selectedSourceId = firstAllowed ? firstAllowed.id : '';
        }
        if (kbState.selectedSourceId && isSourceWeKnora({ id: kbState.selectedSourceId })) {
          await loadSelectedDocuments();
        }
      } else {
        kbState.sources = [];
        kbState.docs = null;
        if (kbState.tab === 'sources' && kbState.settings.canManage) {
          kbState.tab = 'settings';
        }
      }
    } catch (err) {
      if (epoch !== kbViewEpoch) return;
      kbToast(err.message, true);
    } finally {
      if (epoch === kbViewEpoch) {
        kbState.loading = false;
        renderKnowledgeShell();
      }
    }
  }

  async function loadSelectedDocuments() {
    const epoch = ++kbDocsEpoch;
    const sourceId = String(kbState.selectedSourceId || '');
    if (!sourceId || !isSourceWeKnora({ id: sourceId })) {
      kbState.docs = null;
      return;
    }
    // 捕获请求发起时的筛选条件，晚到的旧响应不得覆盖新知识库的列表。
    const keyword = kbState.docKeyword;
    const page = kbState.docPage;
    const q = new URLSearchParams({
      projectId: kbProjectId(),
      keyword: keyword,
      page: String(page),
      pageSize: '20'
    });
    const connId = resolveExplicitConnectionId(sourceId);
    if (connId) q.set('connectionId', connId);
    try {
      const res = await kbAPI(`/api/knowledge/sources/${encodeURIComponent(sourceId)}/documents?${q.toString()}`);
      if (epoch !== kbDocsEpoch) return;
      kbState.docs = res;
      kbScheduleDocsPoll();
    } catch (err) {
      if (epoch !== kbDocsEpoch) return;
      kbState.docs = { error: err.message, documents: [], total: 0 };
    }
  }

  // 列表存在 pending/queued/processing/parsing/finalizing/deleting 时每 4s 刷新一次。
  function kbScheduleDocsPoll() {
    if (kbDocsPollTimer) return;
    const docs = (kbState.docs && kbState.docs.documents) || [];
    const pending = docs.some(d => KB_POLL_STATUSES.indexOf(String((d && (d.parse_status || d.status)) || '').toLowerCase()) >= 0);
    if (!pending) return;
    kbDocsPollTimer = window.setTimeout(async () => {
      kbDocsPollTimer = 0;
      if (kbState.tab !== 'sources') return;
      await loadSelectedDocuments();
      renderKnowledgeShell();
    }, 4000);
  }

  function cleanSourceName(name) {
    return String(name || '').replace(/^\[[^\]]+\]\s*/, '').trim();
  }

  function parseStatusBadge(status, errMsg) {
    const s = String(status || '').toLowerCase();
    const tip = errMsg ? ` title="${kbEsc(errMsg)}"` : '';
    if (s === 'completed' || s === 'ready') return `<span class="hui-badge" data-tone="success"${tip}>已解析</span>`;
    if (s === 'published') return `<span class="hui-badge" data-tone="success"${tip}>已发布</span>`;
    if (s === 'failed' || s === 'error') {
      return `<span class="hui-badge" data-tone="error"${tip}>解析失败</span>${errMsg ? ` <button type="button" class="kb-btn" style="height:22px;padding:0 6px;font-size:11px" data-kb-err-msg="${kbEsc(errMsg)}">原因</button>` : ''}`;
    }
    if (s === 'rejected') return `<span class="hui-badge" data-tone="error"${tip}>已驳回</span>`;
    if (s === 'processing') return `<span class="hui-badge" data-tone="warn"${tip}>解析中</span>`;
    if (s === 'finalizing') return `<span class="hui-badge" data-tone="info"${tip}>索引收尾中</span>`;
    if (s === 'deleting') return `<span class="hui-badge" data-tone="warn"${tip}>删除中</span>`;
    if (s === 'draft') return `<span class="hui-badge" data-tone="neutral"${tip}>草稿</span>`;
    if (s === 'cancelled') return `<span class="hui-badge" data-tone="neutral"${tip}>已取消</span>`;
    if (s === 'pending') return `<span class="hui-badge" data-tone="warn"${tip}>${status === 'pending' ? '排队中' : kbEsc(status)}</span>`;
    return `<span class="hui-badge" data-tone="muted"${tip}>${kbEsc(status || '-')}</span>`;
  }

  function renderKnowledgeShell() {
    const root = document.getElementById('knowledgeRoot');
    if (!root) return;
    const s = kbState.settings || {};
    const pendingSubs = kbState.submissions.filter(x => x.status === 'pending').length;
    const allowedCount = kbState.sources.filter(x => x.allowed).length;
    const engineLabel = (isConnWeKnora(kbActiveConnId()) || (!kbState.sources.length && isWeKnora())) ? 'WeKnora' : 'LightCloud KB';

    const descEl = document.getElementById('kbHeadDesc');
    if (descEl) {
      descEl.innerHTML = `管理团队知识库、检索调试与 SOP 知识沉淀；当前引擎：<b>${kbEsc(engineLabel)}</b>`;
    }
    const actionsEl = document.getElementById('kbHeadActions');
    if (actionsEl) {
      actionsEl.innerHTML = `<button type="button" class="kb-btn" id="kbRefreshBtn">刷新</button>`;
      actionsEl.querySelector('#kbRefreshBtn')?.addEventListener('click', loadKnowledgeView);
    }

    root.innerHTML = `
      <div id="kbStatusBar" class="kb-muted" role="status" style="margin-bottom:10px"></div>

      <div class="kb-metrics">
        <div class="kb-metric"><span class="kb-metric-label">接入状态</span><span class="kb-metric-val">${s.ready ? '已连接' : (kbHasConfiguredConnections() ? '未启用' : '待配置')}</span></div>
        <div class="kb-metric"><span class="kb-metric-label">项目可用知识库（已授权 / 总数）</span><span class="kb-metric-val">${allowedCount} / ${kbState.sources.length}</span></div>
        <div class="kb-metric"><span class="kb-metric-label">待审核沉淀</span><span class="kb-metric-val">${pendingSubs}</span></div>
        <div class="kb-metric"><span class="kb-metric-label">已启用角色</span><span class="kb-metric-val">${kbState.usage?.agents?.length || 0}</span></div>
      </div>

      <div class="kb-tabs" role="tablist">
        <button type="button" class="kb-tab ${kbState.tab === 'sources' ? 'active' : ''}" data-kb-tab="sources">知识库与文档</button>
        <button type="button" class="kb-tab ${kbState.tab === 'distill' ? 'active' : ''}" data-kb-tab="distill">知识沉淀</button>
        <button type="button" class="kb-tab ${kbState.tab === 'settings' ? 'active' : ''}" data-kb-tab="settings">接入配置与用量</button>
        <button type="button" class="kb-tab ${kbState.tab === 'search' ? 'active' : ''}" data-kb-tab="search">排障调试</button>
      </div>

      <div id="kbTabBody"></div>
    `;

    root.querySelector('#kbRefreshBtn')?.addEventListener('click', loadKnowledgeView);
    root.querySelectorAll('[data-kb-tab]').forEach(btn => {
      btn.addEventListener('click', () => {
        kbState.tab = btn.dataset.kbTab;
        renderKnowledgeShell();
      });
    });

    const body = root.querySelector('#kbTabBody');
    if (kbState.tab === 'sources') renderSourcesTab(body);
    else if (kbState.tab === 'search') renderSearchTab(body);
    else if (kbState.tab === 'distill') renderDistillTab(body);
    else renderSettingsTab(body);
  }

  // ---- 知识库与文档管理（连接筛选 / 库 CRUD / 三模式导入 / 文档行操作） ----

  function kbActiveConnId() {
    if (kbState.connectionFilter) return kbState.connectionFilter;
    const selected = kbState.sources.find(s => s.id === kbState.selectedSourceId);
    if (selected) return resolveExplicitConnectionId(selected.id);
    const conns = kbConnections();
    if (conns.length === 1) return conns[0].id || '';
    const configured = kbConfiguredConnections();
    return configured.length === 1 ? (configured[0].id || '') : '';
  }

  function kbConnLabel(conn) {
    const name = (conn && (conn.name || conn.id)) || '';
    return conn && conn.enabled === false ? name + '（已停用）' : name;
  }

  // 仅多连接或存在停用连接时展示筛选，避免单连接场景增加噪音。
  function kbConnFilterHTML() {
    const conns = kbConnections();
    if (!conns.length) return '';
    if (conns.length < 2 && !conns.some(c => c.enabled === false)) return '';
    const opts = ['<option value="">全部连接</option>'].concat(conns.map(c => (
      `<option value="${kbEsc(c.id)}" ${c.id === kbState.connectionFilter ? 'selected' : ''}>${kbEsc(kbConnLabel(c))}</option>`
    ))).join('');
    return `<div class="kb-field" style="margin-bottom:10px">
      <label for="kbConnFilter">连接筛选</label>
      <select class="kb-input" id="kbConnFilter">${opts}</select>
    </div>`;
  }

  // 能力门禁：available / 未探测时放行，只有后端明确声明 unavailable 才禁用。
  function kbGate(connId, cap, what) {
    if (kbCapabilityAllowed(connId, cap)) return { disabled: false, reason: '' };
    const reason = kbCapabilityReason(connId, cap) || ('当前连接缺少' + what + '能力');
    return { disabled: true, reason };
  }

  function kbGateAttrs(gate) {
    return gate.disabled ? ` disabled title="${kbEsc(gate.reason)}"` : '';
  }

  // 写操作门禁：项目写权限 + 连接能力位联合判定，任一不足都给出可读原因。
  function kbWriteGate(connId, cap, what) {
    if (!kbCanWrite()) return { disabled: true, reason: kbCanWriteReason() };
    return kbGate(connId, cap, what);
  }

  // 可建库连接：已配置 + WeKnora 引擎 + 库管理能力未判定为不可用。
  function kbCreatableWeKnoraConnections() {
    return kbConfiguredConnections().filter(c => c && c.id && isConnWeKnora(c.id) && kbCapabilityAllowed(c.id, 'kb_manage'));
  }

  function kbModelFieldHTML(id, label, manualHint) {
    return `<div class="kb-field">
      <label for="${id}">${label}</label>
      <select class="kb-input" id="${id}"><option value="">使用上游默认</option></select>
      <input class="kb-input" id="${id}Manual" placeholder="${kbEsc(manualHint)}" hidden>
    </div>`;
  }

  // 按 WeKnora 模型类型（Embedding / KnowledgeQA）拆分下拉；空列表时回退到手工输入。
  // required=true（新建库的 Embedding）时：列表非空则移除空选项并默认选中第一个；
  // 列表为空或加载失败时保留空选项，由远端默认处理。编辑抽屉不传 required。
  function kbFillModelSelect(select, manual, models, type, required) {
    const list = (models || []).filter(m => String(m.type || '').toLowerCase() === type.toLowerCase());
    const mandatory = !!required && list.length > 0;
    if (select) {
      select.innerHTML = (mandatory ? '' : '<option value="">使用上游默认</option>') + list.map(m =>
        `<option value="${kbEsc(m.id)}">${kbEsc(m.name || m.id)}</option>`).join('');
      select.dataset.required = mandatory ? '1' : '';
    }
    if (manual) {
      manual.hidden = list.length > 0;
      manual.value = '';
    }
    return list.length;
  }

  function kbPickModel(manual, select) {
    const manualVal = manual && !manual.hidden ? String(manual.value || '').trim() : '';
    if (manualVal) return manualVal;
    return select ? String(select.value || '').trim() : '';
  }

  async function kbLoadModels(connId) {
    if (!connId) return { models: [], error: '请选择连接' };
    if (Object.prototype.hasOwnProperty.call(kbState.modelsByConn, connId)) {
      return { models: kbState.modelsByConn[connId], error: '' };
    }
    try {
      const list = await kbAPI('/api/knowledge/models?connectionId=' + encodeURIComponent(connId));
      kbState.modelsByConn[connId] = Array.isArray(list) ? list : [];
      return { models: kbState.modelsByConn[connId], error: '' };
    } catch (err) {
      return { models: [], error: err.message };
    }
  }

  function openKBCreateDrawer(opts) {
    const forceExpandEmb = !!(opts && opts.expandEmbedding);
    if (!kbCanWrite()) {
      kbToast(kbCanWriteReason(), true);
      return;
    }
    const conns = kbCreatableWeKnoraConnections();
    if (!conns.length) {
      kbToast('没有可新建知识库的 WeKnora 连接，请先在「接入配置与用量」添加或启用连接', true);
      return;
    }
    // 默认选中当前筛选连接（可建库时），否则首个可建库的 WeKnora 连接。
    const preferred = kbState.connectionFilter ? conns.find(c => c.id === kbState.connectionFilter) : null;
    const active = conns.find(c => c.id === kbActiveConnId());
    const connId = (preferred || active || conns[0]).id || '';
    kbOperationConnId = connId;
    const options = conns.map(c => (
      `<option value="${kbEsc(c.id)}" ${c.id === connId ? 'selected' : ''}>${kbEsc(kbConnLabel(c))}</option>`
    )).join('');
    openKBDrawer('新建知识库', `
      <div class="kb-field">
        <label for="kbCreateConn">目标连接</label>
        <select class="kb-input" id="kbCreateConn">${options}</select>
      </div>
      <div class="kb-field">
        <label for="kbCreateName">知识库名称</label>
        <input class="kb-input" id="kbCreateName" maxlength="120" placeholder="例如：产品手册">
      </div>
      <div class="kb-field">
        <label for="kbCreateDesc">描述</label>
        <input class="kb-input" id="kbCreateDesc" maxlength="400" placeholder="可选，说明该知识库的用途">
      </div>
      ${kbModelFieldHTML('kbCreateEmbedding', '向量模型（Embedding）', '手动填写 Embedding 模型 ID')}
      <div class="kb-toolbar" style="margin:-4px 0 8px;justify-content:space-between">
        <span class="kb-muted" id="kbCreateEmbConfigHint">尚未配置向量模型？可直接在此一键添加</span>
        <button type="button" class="kb-btn xs" id="kbToggleEmbConfigBtn">配置向量模型</button>
      </div>
      <div class="kb-panel" id="kbCreateEmbConfigBox" hidden style="margin-bottom:12px;padding:12px">
        <div style="font-weight:600;margin-bottom:8px">内置向量模型配置（Embedding）</div>
        <div class="kb-field">
          <label for="kbEmbSource">模型来源</label>
          <select class="kb-input" id="kbEmbSource">
            <option value="remote">OpenAI 兼容接口（Remote API）</option>
            <option value="local">本地 Ollama（Local）</option>
          </select>
        </div>
        <div class="kb-field">
          <label for="kbEmbName">模型名称（Model Name）</label>
          <input class="kb-input" id="kbEmbName" maxlength="160" placeholder="例如：text-embedding-3-small 或 bge-m3">
        </div>
        <div class="kb-field" id="kbEmbBaseUrlWrap">
          <label for="kbEmbBaseUrl">API Base URL</label>
          <input class="kb-input" id="kbEmbBaseUrl" maxlength="300" placeholder="例如：https://api.openai.com/v1">
        </div>
        <div class="kb-field" id="kbEmbApiKeyWrap">
          <label for="kbEmbApiKey">API Key</label>
          <input class="kb-input" id="kbEmbApiKey" type="password" maxlength="300" placeholder="选填，远端接口密钥">
        </div>
        <div class="kb-field">
          <label for="kbEmbDims">向量维度（可选）</label>
          <input class="kb-input" id="kbEmbDims" type="number" min="0" max="16384" placeholder="例如：1536 或 1024（可留空）">
        </div>
        <div class="kb-toolbar" style="justify-content:flex-end">
          <button type="button" class="kb-btn sm primary" id="kbSaveEmbModelBtn">保存并选用该向量模型</button>
        </div>
      </div>
      ${kbModelFieldHTML('kbCreateSummary', '摘要模型（KnowledgeQA）', '手动填写 KnowledgeQA 模型 ID')}
      <p class="kb-muted" id="kbCreateModelNote">正在加载该连接的模型列表…</p>
    `, `<button type="button" class="kb-btn" data-kb-drawer-close>取消</button>
        <button type="button" class="kb-btn primary" id="kbCreateSubmit">创建知识库</button>`);
    const drawer = document.getElementById('kbDrawer');
    if (!drawer) return;
    const connSelect = drawer.querySelector('#kbCreateConn');
    const note = drawer.querySelector('#kbCreateModelNote');
    const embSelect = drawer.querySelector('#kbCreateEmbedding');
    const embManual = drawer.querySelector('#kbCreateEmbeddingManual');
    const sumSelect = drawer.querySelector('#kbCreateSummary');
    const sumManual = drawer.querySelector('#kbCreateSummaryManual');
    const embConfigBox = drawer.querySelector('#kbCreateEmbConfigBox');
    const toggleEmbBtn = drawer.querySelector('#kbToggleEmbConfigBtn');
    const embSourceSel = drawer.querySelector('#kbEmbSource');
    const embBaseUrlWrap = drawer.querySelector('#kbEmbBaseUrlWrap');
    const embApiKeyWrap = drawer.querySelector('#kbEmbApiKeyWrap');
    const saveEmbBtn = drawer.querySelector('#kbSaveEmbModelBtn');
    let modelSeq = 0;
    let embCountState = 0;
    const syncEmbSourceFields = () => {
      const isLocal = embSourceSel && embSourceSel.value === 'local';
      if (embBaseUrlWrap) embBaseUrlWrap.hidden = !!isLocal;
      if (embApiKeyWrap) embApiKeyWrap.hidden = !!isLocal;
    };
    embSourceSel?.addEventListener('change', syncEmbSourceFields);
    syncEmbSourceFields();
    toggleEmbBtn?.addEventListener('click', () => {
      if (!embConfigBox) return;
      embConfigBox.hidden = !embConfigBox.hidden;
    });
    const syncModels = async (preferModelId) => {
      const mine = ++modelSeq;
      const cid = connSelect ? connSelect.value : '';
      if (note) note.textContent = '正在加载该连接的模型列表…';
      const res = await kbLoadModels(cid);
      if (mine !== modelSeq || !document.contains(drawer)) return;
      const embCount = kbFillModelSelect(embSelect, embManual, res.models, 'Embedding', true);
      const sumCount = kbFillModelSelect(sumSelect, sumManual, res.models, 'KnowledgeQA');
      embCountState = embCount;
      if (preferModelId && embSelect) {
        embSelect.value = preferModelId;
      }
      if (embConfigBox && (forceExpandEmb || (!res.error && embCount === 0))) {
        embConfigBox.hidden = false;
      }
      if (!note) return;
      if (res.error) {
        embCountState = 0;
        note.textContent = kbLfText('模型列表加载失败：' + res.error + '，可直接填写模型 ID。', 'Failed to load models: ' + res.error + '. Enter a model ID manually.');
        if (embManual) embManual.hidden = false;
        if (sumManual) sumManual.hidden = false;
      } else if (!embCount) {
        note.textContent = kbLfText('当前 WeKnora 尚未配置向量模型（Embedding），请在上方直接填写并保存向量模型后再创建知识库。', 'No embedding model is configured in WeKnora yet. Fill in and save an embedding model above before creating a knowledge base.');
      } else {
        note.textContent = kbLfText(
          `已加载模型：Embedding ${embCount} 个 · KnowledgeQA ${sumCount} 个；向量模型必选，摘要模型可留空使用上游默认。`,
          `Models loaded: ${embCount} embedding · ${sumCount} KnowledgeQA. An embedding model is required; the summary model may stay blank to use the upstream default.`
        );
      }
    };
    saveEmbBtn?.addEventListener('click', async () => {
      const cid = connSelect ? String(connSelect.value || '').trim() : '';
      const source = embSourceSel ? String(embSourceSel.value || 'remote') : 'remote';
      const modelName = String(drawer.querySelector('#kbEmbName')?.value || '').trim();
      const baseURL = String(drawer.querySelector('#kbEmbBaseUrl')?.value || '').trim();
      const apiKey = String(drawer.querySelector('#kbEmbApiKey')?.value || '').trim();
      const dims = Number(drawer.querySelector('#kbEmbDims')?.value || 0) || 0;
      if (!cid) { kbToast('请选择目标连接', true); return; }
      if (!modelName) { kbToast('请填写向量模型名称', true); return; }
      if (source === 'remote' && !baseURL) { kbToast('请填写向量模型 API Base URL', true); return; }
      saveEmbBtn.disabled = true;
      try {
        const created = await kbAPI('/api/knowledge/models?projectId=' + encodeURIComponent(kbProjectId()), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            connectionId: cid,
            name: modelName,
            type: 'Embedding',
            source: source,
            baseUrl: source === 'remote' ? baseURL : '',
            apiKey: source === 'remote' ? apiKey : '',
            embeddingDimensions: dims > 0 ? dims : 0
          })
        });
        delete kbState.modelsByConn[cid];
        if (embConfigBox) embConfigBox.hidden = true;
        kbToast('向量模型已配置');
        await syncModels(created && created.id ? created.id : '');
      } catch (err) {
        kbToastError(err, cid);
      } finally {
        saveEmbBtn.disabled = false;
      }
    });
    connSelect?.addEventListener('change', () => {
      kbOperationConnId = String(connSelect.value || '');
      syncModels();
    });
    syncModels();

    const submit = drawer.querySelector('#kbCreateSubmit');
    submit?.addEventListener('click', async () => {
      const cid = connSelect ? String(connSelect.value || '').trim() : '';
      const name = String(drawer.querySelector('#kbCreateName')?.value || '').trim();
      const description = String(drawer.querySelector('#kbCreateDesc')?.value || '').trim();
      if (!cid) { kbToast('请选择目标连接', true); return; }
      if (!name) { kbToast('请填写知识库名称', true); return; }
      if (!kbCanWrite()) { kbToast(kbCanWriteReason(), true); return; }
      if (!kbCapabilityAllowed(cid, 'kb_manage')) {
        kbToast(kbCapabilityReason(cid, 'kb_manage') || '当前连接缺少库管理能力', true);
        return;
      }
      const embeddingModelID = kbPickModel(embManual, embSelect);
      if (embCountState > 0 && !embeddingModelID) {
        kbToast('请选择向量模型（Embedding）', true);
        return;
      }
      submit.disabled = true;
      try {
        const created = await kbAPI('/api/knowledge/sources?projectId=' + encodeURIComponent(kbProjectId()), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            connectionId: cid,
            name: name,
            description: description,
            embedding_model_id: embeddingModelID,
            summary_model_id: kbPickModel(sumManual, sumSelect)
          })
        });
        if (created && created.id) kbState.selectedSourceId = created.id;
        closeKBDrawer();
        kbToast('知识库已创建');
        await loadKnowledgeView();
      } catch (err) {
        kbToastError(err, cid);
      } finally {
        submit.disabled = false;
      }
    });
  }

  async function openKBEditDrawer(src) {
    if (!kbCanWrite()) { kbToast(kbCanWriteReason(), true); return; }
    const connId = resolveExplicitConnectionId(src.id);
    kbOperationConnId = connId;
    let detail = null;
    try {
      detail = await kbAPI('/api/knowledge/sources/' + encodeURIComponent(src.id) + '?' + kbConnQuery(connId));
    } catch (err) {
      // 后端详情路由尚未合入时退回列表数据，保存阶段再对 404/405 回退 PUT。
      if (!err.notFound) { kbToastError(err, connId); return; }
    }
    const name = String((detail && (detail.name || detail.title)) || cleanSourceName(src.name || src.id));
    const description = String((detail && detail.description) || '');
    openKBDrawer('编辑知识库', `
      <div class="kb-field">
        <label for="kbEditName">名称</label>
        <input class="kb-input" id="kbEditName" maxlength="120" value="${kbEsc(name)}">
      </div>
      <div class="kb-field">
        <label for="kbEditDesc">描述</label>
        <input class="kb-input" id="kbEditDesc" maxlength="400" value="${kbEsc(description)}" placeholder="可选">
      </div>
      <p class="kb-muted">仅更新名称与描述，不修改分块与索引配置。</p>
    `, `<button type="button" class="kb-btn" data-kb-drawer-close>取消</button>
        <button type="button" class="kb-btn primary" id="kbEditSubmit">保存</button>`);
    const drawer = document.getElementById('kbDrawer');
    if (!drawer) return;
    const submit = drawer.querySelector('#kbEditSubmit');
    submit?.addEventListener('click', async () => {
      const nextName = String(drawer.querySelector('#kbEditName')?.value || '').trim();
      if (!nextName) { kbToast('请填写知识库名称', true); return; }
      const payload = JSON.stringify({
        name: nextName,
        description: String(drawer.querySelector('#kbEditDesc')?.value || '').trim()
      });
      const path = '/api/knowledge/sources/' + encodeURIComponent(src.id) + '?' + kbConnQuery(connId);
      submit.disabled = true;
      try {
        try {
          await kbAPI(path, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: payload });
        } catch (err) {
          if (err.httpStatus !== 404 && err.httpStatus !== 405) throw err;
          await kbAPI(path, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: payload });
        }
        closeKBDrawer();
        kbToast('知识库已更新');
        await loadKnowledgeView();
      } catch (err) {
        kbToastError(err, connId);
      } finally {
        submit.disabled = false;
      }
    });
  }

  async function kbDeleteSource(src) {
    const connId = resolveExplicitConnectionId(src.id);
    if (!kbCanWrite()) { kbToast(kbCanWriteReason(), true); return; }
    if (!kbCapabilityAllowed(connId, 'kb_manage')) {
      kbToast(kbCapabilityReason(connId, 'kb_manage') || '当前连接缺少库管理能力', true);
      return;
    }
    const name = cleanSourceName(src.name || src.id);
    const confirmed = typeof afConfirm === 'function'
      ? await afConfirm({
        title: '删除知识库',
        message: `确定删除知识库「${name}」吗？其全部文档与索引将一并删除，且不可恢复。`,
        danger: true,
        confirmText: '删除知识库'
      })
      : false;
    if (!confirmed) return;
    try {
      await kbAPI('/api/knowledge/sources/' + encodeURIComponent(src.id) + '?' + kbConnQuery(connId), { method: 'DELETE' });
      kbState.selectedSourceId = '';
      kbState.docs = null;
      kbToast('知识库已删除');
      await loadKnowledgeView();
    } catch (err) {
      kbToastError(err, connId);
    }
  }

  async function kbDeleteDoc(docId, title) {
    const connId = resolveExplicitConnectionId(docId);
    if (!kbCanWrite()) { kbToast(kbCanWriteReason(), true); return; }
    if (!kbCapabilityAllowed(connId, 'doc_write')) {
      kbToast(kbCapabilityReason(connId, 'doc_write') || '当前连接缺少文档写入能力', true);
      return;
    }
    const confirmed = typeof afConfirm === 'function'
      ? await afConfirm({
        title: '删除文档',
        message: `确定删除「${title}」吗？该操作不可恢复。`,
        danger: true,
        confirmText: '删除文档'
      })
      : false;
    if (!confirmed) return;
    try {
      await kbAPI('/api/knowledge/documents/' + encodeURIComponent(docId) + '?' + kbConnQuery(connId), { method: 'DELETE' });
      if (kbState.docPage > 1 && (kbState.docs?.documents || []).length <= 1) kbState.docPage -= 1;
      // WeKnora 删除是异步入队：文档仍在列表里说明后台还在清理索引，保持轮询。
      await loadSelectedDocuments();
      const still = ((kbState.docs && kbState.docs.documents) || []).some(d => String(d.id) === String(docId));
      kbToast(still ? '已提交删除任务，正在后台清理索引' : '文档已删除');
      if (still) kbScheduleDocsPoll();
      renderKnowledgeShell();
    } catch (err) {
      kbToastError(err, connId);
    }
  }

  async function kbReparseDoc(docId) {
    const connId = resolveExplicitConnectionId(docId);
    if (!kbCanWrite()) { kbToast(kbCanWriteReason(), true); return; }
    if (!kbCapabilityAllowed(connId, 'reparse')) {
      kbToast(kbCapabilityReason(connId, 'reparse') || '当前连接缺少重解析能力', true);
      return;
    }
    try {
      await kbAPI('/api/knowledge/documents/' + encodeURIComponent(docId) + '/reparse?' + kbConnQuery(connId), { method: 'POST' });
      kbToast('已触发重解析');
      await loadSelectedDocuments();
      renderKnowledgeShell();
    } catch (err) {
      kbToastError(err, connId);
    }
  }

  function openKBImportDrawer(src) {
    const connId = resolveExplicitConnectionId(src.id);
    kbOperationConnId = connId;
    if (!kbCanWrite()) { kbToast(kbCanWriteReason(), true); return; }
    if (!kbCapabilityAllowed(connId, 'doc_write')) {
      kbToast(kbCapabilityReason(connId, 'doc_write') || '当前连接缺少文档写入能力', true);
      return;
    }
    const q = kbConnQuery(connId);
    const sourcePath = '/api/knowledge/sources/' + encodeURIComponent(src.id);
    const title = cleanSourceName(src.name || src.id);
    openKBDrawer('导入文档 · ' + title, `
      <div class="kb-seg" role="tablist">
        <button type="button" class="active" data-kb-import-mode="file">本地上传</button>
        <button type="button" data-kb-import-mode="url">在线链接</button>
        <button type="button" data-kb-import-mode="manual">手工录入</button>
      </div>
      <div data-kb-import-pane="file">
        <div class="kb-field">
          <label for="kbImportFiles">选择文件</label>
          <input type="file" id="kbImportFiles" multiple>
          <small>单文件不超过 50 MiB，支持多选后按顺序上传。</small>
          <div class="kb-toolbar"><button type="button" class="kb-btn primary" id="kbImportUploadBtn">开始上传</button></div>
        </div>
      </div>
      <div data-kb-import-pane="url" hidden>
        <div class="kb-field">
          <label for="kbImportUrl">文档链接</label>
          <input type="url" class="kb-input" id="kbImportUrl" placeholder="https://example.com/doc.html">
          <small>仅支持 http/https 页面，由上游抓取并解析。</small>
          <div class="kb-toolbar"><button type="button" class="kb-btn primary" id="kbImportUrlBtn">导入链接</button></div>
        </div>
      </div>
      <div data-kb-import-pane="manual" hidden>
        <div class="kb-field">
          <label for="kbImportTitle">标题</label>
          <input class="kb-input" id="kbImportTitle" maxlength="200">
          <label for="kbImportContent">Markdown 正文</label>
          <textarea class="kb-input" id="kbImportContent" placeholder="# 标题&#10;正文…"></textarea>
          <label class="kb-check-chip">
            <input type="checkbox" role="switch" id="kbImportPublish" checked>
            <span>创建后立即发布</span>
          </label>
          <div class="kb-toolbar"><button type="button" class="kb-btn primary" id="kbImportManualBtn">创建文档</button></div>
        </div>
      </div>
      <p class="kb-muted" id="kbImportStatus" role="status"></p>
    `);
    const drawer = document.getElementById('kbDrawer');
    if (!drawer) return;
    const status = drawer.querySelector('#kbImportStatus');
    const setStatus = (text, isError) => {
      if (!status) return;
      status.textContent = text;
      status.style.color = isError ? 'var(--hui-error)' : 'var(--hui-success)';
    };

    drawer.querySelectorAll('[data-kb-import-mode]').forEach(btn => {
      btn.addEventListener('click', () => {
        drawer.querySelectorAll('[data-kb-import-mode]').forEach(b => b.classList.toggle('active', b === btn));
        drawer.querySelectorAll('[data-kb-import-pane]').forEach(pane => {
          pane.hidden = pane.dataset.kbImportPane !== btn.dataset.kbImportMode;
        });
      });
    });

    const uploadBtn = drawer.querySelector('#kbImportUploadBtn');
    uploadBtn?.addEventListener('click', async () => {
      const files = Array.from(drawer.querySelector('#kbImportFiles')?.files || []);
      if (!files.length) { setStatus('请选择要上传的文件', true); return; }
      const tooLarge = files.filter(f => f.size > 50 * 1024 * 1024);
      if (tooLarge.length) {
        setStatus('以下文件超过 50 MiB：' + tooLarge.map(f => f.name).join('、'), true);
        return;
      }
      uploadBtn.disabled = true;
      let done = 0;
      const failed = [];
      for (let i = 0; i < files.length; i += 1) {
        const file = files[i];
        setStatus(`正在上传 ${i + 1}/${files.length}：${file.name}`);
        const form = new FormData();
        form.append('file', file);
        try {
          await kbAPI(`${sourcePath}/upload?${q}`, { method: 'POST', body: form });
          done += 1;
        } catch (err) {
          failed.push(file.name);
          if (err.code === 'capability_unavailable') {
            handleCapabilityError(err, connId);
            break;
          }
        }
      }
      uploadBtn.disabled = false;
      const summary = `上传完成：成功 ${done} 个` + (failed.length ? `，失败 ${failed.length} 个（${failed.join('、')}）` : '');
      setStatus(summary, failed.length > 0);
      kbToast(summary, failed.length > 0);
      if (done > 0) {
        closeKBDrawer();
        await loadSelectedDocuments();
        renderKnowledgeShell();
      }
    });

    const urlBtn = drawer.querySelector('#kbImportUrlBtn');
    urlBtn?.addEventListener('click', async () => {
      const raw = String(drawer.querySelector('#kbImportUrl')?.value || '').trim();
      let parsed = null;
      try { parsed = new URL(raw); } catch { parsed = null; }
      if (!parsed || !/^https?:$/.test(parsed.protocol)) {
        setStatus('请输入合法的 http/https 链接', true);
        return;
      }
      urlBtn.disabled = true;
      try {
        await kbAPI(`${sourcePath}/url?${q}`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ url: raw })
        });
        closeKBDrawer();
        kbToast('链接已提交导入');
        await loadSelectedDocuments();
        renderKnowledgeShell();
      } catch (err) {
        kbToastError(err, connId);
        setStatus(err.message, true);
      } finally {
        urlBtn.disabled = false;
      }
    });

    const manualBtn = drawer.querySelector('#kbImportManualBtn');
    manualBtn?.addEventListener('click', async () => {
      const manualTitle = String(drawer.querySelector('#kbImportTitle')?.value || '').trim();
      const content = String(drawer.querySelector('#kbImportContent')?.value || '');
      if (!manualTitle) { setStatus('请填写文档标题', true); return; }
      if (!content.trim()) { setStatus('请填写 Markdown 正文', true); return; }
      manualBtn.disabled = true;
      try {
        await kbAPI(`${sourcePath}/manual?${q}`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            title: manualTitle,
            content: content,
            publish: !!drawer.querySelector('#kbImportPublish')?.checked
          })
        });
        closeKBDrawer();
        kbToast('手工文档已创建');
        await loadSelectedDocuments();
        renderKnowledgeShell();
      } catch (err) {
        kbToastError(err, connId);
        setStatus(err.message, true);
      } finally {
        manualBtn.disabled = false;
      }
    });
  }

  // ---- 文档详情抽屉（分块 / 原文预览 / 手工文档编辑） ----

  function kbDocField(d, camel, snake) {
    if (!d) return '';
    if (d[camel] != null && d[camel] !== '') return String(d[camel]);
    if (snake && d[snake] != null && d[snake] !== '') return String(d[snake]);
    return '';
  }

  function kbDocTypeText(d) {
    return kbDocField(d, 'fileType', 'file_type') || kbDocField(d, 'type');
  }

  function kbDocIsManual(d) {
    return String(kbDocField(d, 'type') || '').toLowerCase() === 'manual';
  }

  function kbReleasePreviewURL() {
    if (kbDocView.objectUrl) {
      try { URL.revokeObjectURL(kbDocView.objectUrl); } catch {}
      kbDocView.objectUrl = '';
    }
    // URL 释放后同步丢弃引用该 blob 的预览态，避免切回预览时渲染已失效地址。
    if (kbDocView.preview && kbDocView.preview.url) kbDocView.preview = null;
  }

  // 预览请求代际：切出预览、重开文档、关闭抽屉都会使在途响应失效，避免越生命周期回写 blob。
  let kbPreviewEpoch = 0;
  function kbCancelDocPreview() {
    kbPreviewEpoch += 1;
    kbDocView.previewLoading = false;
  }

  function kbPreviewKind(contentType) {
    const t = String(contentType || '').toLowerCase().split(';')[0].trim();
    if (!t) return 'binary';
    if (t.indexOf('text/') === 0) return 'text';
    if ([
      'application/json', 'application/xml', 'application/javascript',
      'application/yaml', 'application/x-yaml', 'application/x-ndjson'
    ].indexOf(t) >= 0) return 'text';
    if (['image/png', 'image/jpeg', 'image/jpg', 'image/gif', 'image/webp', 'image/bmp', 'image/avif'].indexOf(t) >= 0) return 'image';
    if (t === 'application/pdf') return 'pdf';
    return 'binary';
  }

  async function openKBDocumentDrawer(docId, title, initialView) {
    const id = String(docId || '');
    if (!id) return;
    const connId = resolveExplicitConnectionId(id) || kbActiveConnId();
    kbReleasePreviewURL();
    kbCancelDocPreview();
    kbOperationConnId = '';
    Object.assign(kbDocView, {
      docId: id,
      connId: connId,
      title: title || id,
      docLoading: true,
      detail: null,
      view: 'chunks',
      chunkPage: 1,
      chunks: null,
      chunkLegacy: false,
      chunkError: '',
      chunkLoading: false,
      preview: null,
      previewError: '',
      previewLoading: false,
      objectUrl: '',
      editContent: '',
      editContentState: 'idle',
      editPublish: true
    });
    openKBDrawer(title || '文档详情', '<div id="kbDocBody"></div>');
    renderKBDocBody();
    await kbLoadDocDetail();
    if (kbDocView.docId !== id) return;
    // 首次打开时按远端 publishStatus 同步「保存后立即发布」，此后用户的勾选不被覆盖。
    const publishStatus = String(kbDocField(kbDocView.detail, 'publishStatus', 'publish_status') || '').toLowerCase();
    if (publishStatus) kbDocView.editPublish = publishStatus !== 'draft';
    const manual = kbDocIsManual(kbDocView.detail);
    const canEdit = manual && kbCanWrite() && kbCapabilityAllowed(connId, 'doc_write');
    if (initialView === 'edit' && canEdit) {
      kbDocView.view = 'edit';
    } else if (kbCapabilityAllowed(connId, 'chunk_read')) {
      kbDocView.view = 'chunks';
    } else if (kbCapabilityAllowed(connId, 'preview')) {
      kbDocView.view = 'preview';
    } else {
      kbDocView.view = canEdit ? 'edit' : 'chunks';
    }
    kbDocView.docLoading = false;
    if (kbDocView.view !== 'edit') kbDocView.editContentState = 'idle';
    kbLoadDocView();
  }

  async function kbLoadDocDetail() {
    const id = kbDocView.docId;
    if (!id) return;
    // 捕获请求发起时的连接：响应到达时抽屉可能已切到别的文档/连接。
    const connId = kbDocView.connId;
    try {
      const detail = await kbAPI('/api/knowledge/documents/' + encodeURIComponent(id) + '?' + kbConnQuery(connId));
      if (kbDocView.docId !== id) return;
      kbDocView.detail = detail && typeof detail === 'object' && !Array.isArray(detail) ? detail : null;
      const nextTitle = kbDocField(detail, 'title');
      if (nextTitle) kbDocView.title = nextTitle;
    } catch (err) {
      // 先按请求发起连接完成能力降级，再判断响应是否已过期。
      const handled = handleCapabilityError(err, connId);
      if (kbDocView.docId !== id) return;
      // 详情路由尚未合入时保持抽屉可用，分块阶段再回退旧版接口。
      if (!handled && !err.notFound) kbToastError(err, connId);
      kbDocView.detailMissing = true;
    }
  }

  function kbLoadDocView() {
    if (!kbDocView.docId || kbDocView.docLoading) return;
    const connId = kbDocView.connId;
    if (kbDocView.view === 'preview') {
      if (kbCapabilityAllowed(connId, 'preview') && !kbDocView.preview && !kbDocView.previewLoading && !kbDocView.previewError) {
        kbLoadDocPreview();
      }
      return;
    }
    if (kbDocView.view === 'edit') {
      kbLoadDocEditContent();
      return;
    }
    if (kbCapabilityAllowed(connId, 'chunk_read') && !kbDocView.chunks && !kbDocView.chunkLoading && !kbDocView.chunkError) {
      kbLoadDocChunks(kbDocView.chunkPage || 1);
    }
  }

  async function kbLoadDocChunks(page) {
    const id = kbDocView.docId;
    if (!id) return;
    const connId = kbDocView.connId;
    kbDocView.chunkPage = Math.max(1, Number(page) || 1);
    kbDocView.chunkLoading = true;
    kbDocView.chunkError = '';
    kbDocView.chunkLegacy = false;
    renderKBDocBody();
    try {
      const q = kbConnQuery(connId, { page: kbDocView.chunkPage, pageSize: kbDocView.chunkPageSize });
      const res = await kbAPI('/api/knowledge/documents/' + encodeURIComponent(id) + '/chunks?' + q);
      if (kbDocView.docId !== id) return;
      const chunks = Array.isArray(res) ? res : (Array.isArray(res && res.chunks) ? res.chunks : []);
      kbDocView.chunks = {
        chunks: chunks,
        total: Number(res && res.total != null ? res.total : chunks.length),
        page: Number((res && res.page) || kbDocView.chunkPage),
        pageSize: Number((res && res.pageSize) || kbDocView.chunkPageSize),
        paginated: !Array.isArray(res)
      };
    } catch (err) {
      if (err.notFound) {
        if (kbDocView.docId !== id) return;
        await kbLoadLegacyDocChunks(id);
      } else {
        // 先按请求发起连接降级，再丢弃过期响应对当前抽屉的写入。
        handleCapabilityError(err, connId);
        if (kbDocView.docId !== id) return;
        kbDocView.chunkError = err.message;
      }
    } finally {
      if (kbDocView.docId === id) {
        kbDocView.chunkLoading = false;
        renderKBDocBody();
      }
    }
  }

  // 分块路由尚未合入时回退旧版 /api/knowledge/document 原文行。
  async function kbLoadLegacyDocChunks(id) {
    const q = 'projectId=' + encodeURIComponent(kbProjectId())
      + '&source=' + encodeURIComponent(kbState.selectedSourceId || '')
      + '&path=' + encodeURIComponent(id);
    try {
      const doc = await kbAPI('/api/knowledge/document?' + q);
      if (kbDocView.docId !== id) return;
      const lines = Array.isArray(doc && doc.lines) ? doc.lines : [];
      const chunks = lines.map((line, i) => ({
        id: 'legacy-' + i,
        chunkIndex: i,
        content: (line && line.text) || '',
        startAt: 0,
        endAt: 0
      }));
      kbDocView.chunks = { chunks: chunks, total: chunks.length, page: 1, pageSize: chunks.length || 1, paginated: false };
      kbDocView.chunkLegacy = true;
    } catch (err) {
      if (kbDocView.docId !== id) return;
      kbDocView.chunkError = err.message;
    }
  }

  async function kbLoadDocPreview() {
    const id = kbDocView.docId;
    if (!id || kbDocView.previewLoading) return;
    // 捕获请求发起时的连接：预览在途时抽屉可能已切到另一文档/连接。
    const connId = kbDocView.connId;
    const epoch = kbPreviewEpoch;
    const stale = () => epoch !== kbPreviewEpoch || kbDocView.docId !== id || kbDocView.view !== 'preview';
    kbDocView.previewLoading = true;
    kbDocView.previewError = '';
    kbReleasePreviewURL();
    renderKBDocBody();
    try {
      const res = await fetch('/api/knowledge/documents/' + encodeURIComponent(id) + '/preview?' + kbConnQuery(connId));
      // 不在最前面丢弃过期响应：错误分支仍需按请求发起连接完成能力降级。
      if (!res.ok) {
        const text = await res.text().catch(() => '');
        let data = null;
        try { data = text ? JSON.parse(text) : null; } catch {}
        const err = new Error((data && (data.error || data.message)) || text || ('HTTP ' + res.status));
        err.httpStatus = res.status;
        err.notFound = res.status === 404;
        err.code = (data && data.code) || '';
        err.capability = (data && data.capability) || '';
        err.connectionId = (data && data.connectionId) || '';
        err.reason = (data && data.reason) || '';
        throw err;
      }
      const contentType = res.headers.get('content-type') || '';
      const kind = kbPreviewKind(contentType);
      if (kind === 'text') {
        const text = await res.text();
        if (stale()) return;
        kbDocView.preview = { kind: kind, text: text };
      } else if (kind === 'image' || kind === 'pdf') {
        const blob = await res.blob();
        if (stale()) return;
        kbReleasePreviewURL();
        kbDocView.objectUrl = URL.createObjectURL(blob);
        kbDocView.preview = { kind: kind, url: kbDocView.objectUrl };
      } else {
        if (stale()) return;
        kbDocView.preview = { kind: 'binary', contentType: contentType };
      }
    } catch (err) {
      // 先按请求发起连接完成能力降级，再判断响应是否对当前抽屉过期。
      const handled = handleCapabilityError(err, connId);
      if (stale()) return;
      if (err.notFound || err.httpStatus === 405) {
        kbDocView.previewError = '原文预览接口尚未合入，可先下载原文件查看。';
      } else if (!handled) {
        kbDocView.previewError = err.message;
      }
    } finally {
      if (kbDocView.docId === id && epoch === kbPreviewEpoch) {
        kbDocView.previewLoading = false;
        renderKBDocBody();
      }
    }
  }

  async function kbLoadDocEditContent() {
    const id = kbDocView.docId;
    if (!id || kbDocView.editContentState === 'loading' || kbDocView.editContentState === 'ready') return;
    const inline = kbDocField(kbDocView.detail, 'content');
    if (inline) {
      kbDocView.editContent = inline;
      kbDocView.editContentState = 'ready';
      renderKBDocBody();
      return;
    }
    // 详情接口缺 content（旧版 WeKnora）：依次回退 /preview、分块拼接；
    // 两者都取不到时进入 failed 并禁用保存，避免空正文覆盖原文。
    kbDocView.editContentState = 'loading';
    renderKBDocBody();
    let text = '';
    if (kbCapabilityAllowed(kbDocView.connId, 'preview')) {
      try {
        const res = await fetch('/api/knowledge/documents/' + encodeURIComponent(id) + '/preview?' + kbConnQuery(kbDocView.connId));
        if (kbDocView.docId !== id) return;
        if (res.ok && kbPreviewKind(res.headers.get('content-type')) === 'text') text = await res.text();
      } catch {}
    }
    if (!text && kbDocView.docId === id && kbCapabilityAllowed(kbDocView.connId, 'chunk_read')) {
      try { text = await kbLoadDocEditChunks(id); } catch {}
    }
    if (kbDocView.docId !== id) return;
    kbDocView.editContent = text;
    kbDocView.editContentState = text ? 'ready' : 'failed';
    renderKBDocBody();
  }

  // 手工文档无内联正文且预览不可用时的最后回退：按分页拉取分块并拼接。
  async function kbLoadDocEditChunks(id) {
    const parts = [];
    let page = 1;
    for (;;) {
      const q = kbConnQuery(kbDocView.connId, { page: String(page), pageSize: '50' });
      const res = await fetch('/api/knowledge/documents/' + encodeURIComponent(id) + '/chunks?' + q);
      if (!res.ok) throw new Error('HTTP ' + res.status);
      const data = await res.json();
      const chunks = (data && data.chunks) || [];
      chunks.forEach(chunk => { if (chunk && chunk.content) parts.push(chunk.content); });
      const total = Number((data && data.total) || 0);
      const size = Number((data && data.pageSize) || 50) || 50;
      if (!chunks.length || page * size >= total) break;
      // 已知还有分页却达到上限：宁可失败并禁用保存，也不能拿截断正文覆盖原文。
      if (page >= 20) throw new Error('chunks truncated at page ' + page);
      page += 1;
    }
    if (!parts.length) throw new Error('empty chunks');
    return parts.join('\n\n');
  }

  function kbSwitchDocView(view) {
    if (!view || kbDocView.view === view) return;
    kbDocView.view = view;
    if (view !== 'preview') {
      kbCancelDocPreview();
      kbReleasePreviewURL();
    }
    renderKBDocBody();
    kbLoadDocView();
  }

  function kbRenderDocChunks() {
    if (kbDocView.chunkLoading) return '<p class="kb-muted" role="status">正在加载分块…</p>';
    if (kbDocView.chunkError) return `<p class="kb-muted">分块加载失败：${kbEsc(kbDocView.chunkError)}</p>`;
    const page = kbDocView.chunks;
    const chunks = (page && page.chunks) || [];
    if (!chunks.length) return '<p class="kb-muted">该文档暂无分块，可能尚未完成解析。</p>';
    const cards = chunks.map((ch, i) => {
      const index = ch.chunkIndex != null ? ch.chunkIndex : ((page.page || 1) - 1) * (page.pageSize || 10) + i;
      const start = ch.startAt != null ? ch.startAt : ch.start_at;
      const end = ch.endAt != null ? ch.endAt : ch.end_at;
      const range = (start != null && end != null && (start || end)) ? `<span class="kb-muted">字符 ${kbEsc(start)}–${kbEsc(end)}</span>` : '';
      return `<div class="kb-chunk-card">
        <div class="kb-chunk-head">
          <span>分块 #${kbEsc(index)}</span>
          ${range}
        </div>
        <pre class="kb-doc-pre" data-i18n-data>${kbEsc(ch.content || ch.text || '')}</pre>
      </div>`;
    }).join('');
    const total = Number((page && page.total) || chunks.length);
    const pages = page.paginated ? Math.max(1, Math.ceil(total / (page.pageSize || kbDocView.chunkPageSize))) : 1;
    const pager = page.paginated ? `<div class="kb-pagination">
        <span class="kb-muted">第 ${kbDocView.chunkPage} / ${pages} 页 · 共 ${total} 个分块</span>
        <button type="button" class="kb-btn xs" data-kb-chunk-page="${kbDocView.chunkPage - 1}" ${kbDocView.chunkPage <= 1 ? 'disabled' : ''}>上一页</button>
        <button type="button" class="kb-btn xs" data-kb-chunk-page="${kbDocView.chunkPage + 1}" ${kbDocView.chunkPage >= pages ? 'disabled' : ''}>下一页</button>
      </div>` : '';
    const legacyNote = kbDocView.chunkLegacy ? '<p class="kb-muted">分块接口尚未合入，当前展示旧版原文分块。</p>' : '';
    return `<div class="kb-chunk-list">${cards}</div>${pager}${legacyNote}`;
  }

  function kbRenderDocPreview() {
    if (kbDocView.previewLoading) return '<p class="kb-muted" role="status">正在加载原文预览…</p>';
    if (kbDocView.previewError) return `<p class="kb-muted">${kbEsc(kbDocView.previewError)}</p>`;
    const preview = kbDocView.preview;
    if (!preview) return '<p class="kb-muted">暂无预览。</p>';
    if (preview.kind === 'text') return `<pre class="kb-doc-pre" data-i18n-data>${kbEsc(preview.text)}</pre>`;
    if (preview.kind === 'image') {
      return `<div class="kb-preview-box"><img class="kb-preview-img" alt="${kbEsc(kbDocView.title)}" src="${kbEsc(preview.url)}"></div>`;
    }
    if (preview.kind === 'pdf') {
      return `<div class="kb-preview-box"><iframe class="kb-preview-frame" title="${kbEsc(kbDocView.title)}" src="${kbEsc(preview.url)}"></iframe></div>`;
    }
    return '<p class="kb-muted">该文件类型不支持在线预览，请使用上方「下载」查看原文件。</p>';
  }

  function kbRenderDocEdit() {
    const loading = kbDocView.editContentState === 'loading';
    const failed = kbDocView.editContentState === 'failed';
    return `<div class="kb-field">
        <label for="kbDocEditTitle">标题</label>
        <input class="kb-input" id="kbDocEditTitle" maxlength="200" value="${kbEsc(kbDocView.title)}">
      </div>
      <div class="kb-field">
        <label for="kbDocEditContent">Markdown 正文</label>
        <textarea class="kb-input" id="kbDocEditContent" rows="14" ${loading ? 'disabled' : ''} placeholder="${loading ? '正在加载原文…' : '# 标题&#10;正文…'}">${kbEsc(kbDocView.editContent || '')}</textarea>
        ${failed ? '<small class="kb-muted">未能读取原文，已禁用保存以避免空正文覆盖；请检查连接权限或稍后重试。</small>' : ''}
      </div>
      <label class="kb-check-chip">
        <input type="checkbox" role="switch" id="kbDocEditPublish" ${kbDocView.editPublish ? 'checked' : ''}>
        <span>保存后立即发布</span>
      </label>
      <p class="kb-muted">保存会替换文档正文并触发重新解析。</p>
      <div class="kb-toolbar">
        <button type="button" class="kb-btn primary" id="kbDocSaveBtn" ${loading || failed ? 'disabled' : ''}>保存文档</button>
      </div>`;
  }

  function renderKBDocBody() {
    const host = document.getElementById('kbDocBody');
    if (!host || !kbDocView.docId) return;
    if (kbDocView.docLoading) {
      host.innerHTML = '<p class="kb-muted" role="status">正在加载文档详情…</p>';
      return;
    }
    const detail = kbDocView.detail;
    const connId = kbDocView.connId;
    const manual = kbDocIsManual(detail);
    const segs = [];
    if (kbCapabilityAllowed(connId, 'chunk_read')) segs.push({ key: 'chunks', label: '分块' });
    if (kbCapabilityAllowed(connId, 'preview')) segs.push({ key: 'preview', label: '原文预览' });
    if (manual && kbCanWrite() && kbCapabilityAllowed(connId, 'doc_write')) segs.push({ key: 'edit', label: '编辑文档' });
    if (segs.length && !segs.some(s => s.key === kbDocView.view)) {
      kbDocView.view = segs[0].key;
      kbReleasePreviewURL();
    }

    const downloadGate = kbGate(connId, 'download', '下载');
    const href = '/api/knowledge/documents/' + encodeURIComponent(kbDocView.docId) + '/download?' + kbConnQuery(connId);
    const downloadHTML = downloadGate.disabled
      ? `<span class="kb-btn xs" aria-disabled="true" title="${kbEsc(downloadGate.reason)}" style="opacity:.55;cursor:not-allowed">下载</span>`
      : `<a class="kb-btn xs" href="${kbEsc(href)}" target="_blank" rel="noopener" download>下载</a>`;

    const typeText = kbDocTypeText(detail);
    const parseText = kbDocField(detail, 'parseStatus', 'parse_status');
    const updatedText = kbDocField(detail, 'updatedAt', 'updated_at');
    const metaHTML = detail ? `
      ${typeText ? `<span class="hui-badge" data-tone="muted" data-size="sm">${kbEsc(typeText)}</span>` : ''}
      ${parseText ? parseStatusBadge(parseText, kbDocField(detail, 'errorMessage', 'error_message')) : ''}
      ${updatedText ? `<span class="kb-muted">更新于 ${kbEsc(updatedText.slice(0, 19).replace('T', ' '))}</span>` : ''}
    ` : '';

    const segHTML = segs.length ? `<div class="kb-seg" role="tablist">${segs.map(s => (
      `<button type="button" role="tab" aria-selected="${s.key === kbDocView.view}" class="${s.key === kbDocView.view ? 'active' : ''}" data-kb-doc-view="${s.key}">${s.label}</button>`
    )).join('')}</div>` : '';

    let paneHTML = '';
    if (!segs.length) paneHTML = '<p class="kb-muted">当前连接缺少查看文档内容所需的能力（分块读取 / 预览下载）。</p>';
    else if (kbDocView.view === 'preview') paneHTML = kbRenderDocPreview();
    else if (kbDocView.view === 'edit') paneHTML = kbRenderDocEdit();
    else paneHTML = kbRenderDocChunks();

    host.innerHTML = `
      <div class="kb-doc-summary">
        <div class="kb-doc-meta">${metaHTML}</div>
        <div class="kb-toolbar">${downloadHTML}</div>
      </div>
      ${segHTML}
      <div class="kb-doc-pane">${paneHTML}</div>
    `;

    host.querySelectorAll('[data-kb-doc-view]').forEach(btn => {
      btn.addEventListener('click', () => kbSwitchDocView(btn.dataset.kbDocView));
    });
    host.querySelectorAll('[data-kb-chunk-page]').forEach(btn => {
      btn.addEventListener('click', () => {
        if (btn.disabled) return;
        kbLoadDocChunks(Number(btn.dataset.kbChunkPage) || 1);
      });
    });
    const saveBtn = host.querySelector('#kbDocSaveBtn');
    if (saveBtn) saveBtn.addEventListener('click', kbSaveDocEdit);
    host.querySelectorAll('[data-kb-err-msg]').forEach(btn => {
      btn.addEventListener('click', () => kbToast(btn.dataset.kbErrMsg, true));
    });
  }

  async function kbSaveDocEdit() {
    const host = document.getElementById('kbDocBody');
    if (!host || !kbDocView.docId) return;
    const connId = kbDocView.connId;
    if (!kbCanWrite()) { kbToast(kbCanWriteReason(), true); return; }
    if (!kbCapabilityAllowed(connId, 'doc_write')) {
      kbToast(kbCapabilityReason(connId, 'doc_write') || '当前连接缺少文档写入能力', true);
      return;
    }
    const nextTitle = String(host.querySelector('#kbDocEditTitle')?.value || '').trim();
    if (!nextTitle) { kbToast('请填写文档标题', true); return; }
    const content = String(host.querySelector('#kbDocEditContent')?.value || '');
    const publish = !!host.querySelector('#kbDocEditPublish')?.checked;
    const id = kbDocView.docId;
    const btn = host.querySelector('#kbDocSaveBtn');
    if (btn) btn.disabled = true;
    try {
      const updated = await kbAPI('/api/knowledge/documents/' + encodeURIComponent(id) + '?' + kbConnQuery(connId), {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: nextTitle, content: content, publish: publish })
      });
      if (kbDocView.docId !== id) return;
      if (updated && typeof updated === 'object' && !Array.isArray(updated)) kbDocView.detail = updated;
      kbDocView.title = nextTitle;
      kbDocView.editContent = content;
      kbDocView.editContentState = 'ready';
      kbDocView.editPublish = publish;
      kbDocView.chunks = null;
      kbDocView.preview = null;
      kbReleasePreviewURL();
      const head = document.querySelector('#kbDrawer .kb-drawer-head h3');
      if (head) head.textContent = nextTitle;
      kbToast('文档已保存');
      renderKBDocBody();
      await loadSelectedDocuments();
      renderKnowledgeShell();
    } catch (err) {
      kbToastError(err, connId);
      if (btn && document.contains(btn)) btn.disabled = false;
    }
  }

  function renderSourcesTab(container) {
    if (!kbHasConfiguredConnections()) {
      container.innerHTML = `<div class="kb-panel"><p class="kb-muted">尚未完成知识库接入配置。请前往「接入配置与用量」填写服务地址与 API Key / Token。</p></div>`;
      return;
    }
    kbEnsureConnectionCapabilities();
    const notReadyNote = kbState.settings?.ready ? '' : kbDisabledNoticeHTML();
    const activeConnId = kbActiveConnId();
    const activeConn = kbConnections().find(c => c.id === activeConnId) || null;
    const capsHTML = activeConnId
      ? `<div style="margin-bottom:12px">
          <div class="kb-muted" style="margin-bottom:6px">${kbEsc(activeConn ? kbConnLabel(activeConn) : activeConnId)} · 连接能力</div>
          ${kbCapabilityBadges(activeConnId)}
        </div>`
      : '';
    const creatableConns = kbCreatableWeKnoraConnections();
    const createGate = creatableConns.length
      ? { disabled: false, reason: '' }
      : kbGate(activeConnId, 'kb_manage', '库管理');
    const createDenied = !kbCanWrite() || createGate.disabled;
    const createReason = !kbCanWrite()
      ? kbCanWriteReason()
      : (createGate.reason || '没有可新建知识库的 WeKnora 连接（需已配置且具备库管理能力）');
    const allDisabledHint = (!kbState.settings?.ready && kbHasConfiguredConnections() && !kbState.connectionFilter)
      ? '<p class="kb-muted">当前连接均已停用，可在上方选择具体已停用连接进行预配置，或前往接入配置启用。</p>'
      : '';
    const sourcesHTML = kbState.sources.length
      ? kbState.sources.map(src => {
        const conn = resolveConnectionForSource(src.id);
        const accountText = conn?.account || '';
        const isActive = src.id === kbState.selectedSourceId;
        const displayName = cleanSourceName(src.name || src.id);
        return `
        <button type="button" class="kb-source-item ${isActive ? 'active' : ''}" data-kb-pick="${kbEsc(src.id)}" aria-current="${isActive ? 'true' : 'false'}">
          <div class="kb-source-top">
            <span class="kb-source-name">${isActive ? '<span aria-hidden="true">✓</span>' : ''}<span data-i18n-data>${kbEsc(displayName)}</span></span>
            <span class="hui-badge" data-tone="${src.allowed ? 'success' : 'muted'}">${src.allowed ? '已授权' : '未授权'}</span>
          </div>
          <div class="kb-source-meta">
            <span>${src.doc_count || 0} 篇文档</span>
            ${accountText ? `<span data-i18n-data>${kbEsc(accountText)}</span>` : ''}
          </div>
        </button>
      `;
      }).join('')
      : `<p class="kb-muted">${kbState.settings?.ready ? '暂无上游知识库。' : '当前没有已启用的连接，知识库列表需启用连接后加载；下方仍可新建并管理知识库。'}</p>${allDisabledHint}`;

    const selected = kbState.sources.find(s => s.id === kbState.selectedSourceId);
    let rightHTML = '<p class="kb-muted">请从左侧选择一个知识库。</p>';
    if (selected && !isSourceWeKnora(selected)) {
      rightHTML = `<p class="kb-muted">当前为 LightCloud KB 引擎，请使用「排障调试」按路径检索并查看原文。</p>`;
    } else if (selected && isSourceWeKnora(selected)) {
      const connId = resolveExplicitConnectionId(selected.id);
      const docs = kbState.docs?.documents || [];
      const page = Math.max(1, kbState.docPage || 1);
      const pageSize = 20;
      const total = Number(kbState.docs?.total || docs.length || 0);
      const pages = Math.max(1, Math.ceil(total / pageSize));
      const docGate = kbWriteGate(connId, 'doc_write', '文档写入');
      const manageGate = kbWriteGate(connId, 'kb_manage', '库管理');
      const reparseGate = kbWriteGate(connId, 'reparse', '重解析');
      const downloadGate = kbGate(connId, 'download', '下载');
      // 「查看」负责打开文档抽屉（默认分块），只要任一内容视图可用就放行；
      // preview 端点被拒时只隐藏抽屉内的原文预览页签。
      const canOpenDoc = ['chunk_read', 'preview', 'doc_write'].some(cap => kbCapabilityAllowed(connId, cap));
      const previewGate = canOpenDoc ? { disabled: false, reason: '' } : kbGate(connId, 'preview', '预览');
      const rows = docs.length ? docs.map(d => {
        const docTitle = d.title || d.file_name || d.id;
        const manualDoc = String(d.type || '').toLowerCase() === 'manual';
        const editAction = manualDoc
          ? `<button type="button" class="kb-btn xs" data-kb-edit-doc="${kbEsc(d.id)}" data-kb-doc-title="${kbEsc(docTitle)}"${kbGateAttrs(docGate)}>编辑</button>`
          : '';
        const downloadHref = '/api/knowledge/documents/' + encodeURIComponent(d.id) + '/download?' + kbConnQuery(connId);
        const downloadAction = downloadGate.disabled
          ? `<span class="kb-btn xs" aria-disabled="true" title="${kbEsc(downloadGate.reason)}" style="opacity:.55;cursor:not-allowed">下载</span>`
          : `<a class="kb-btn xs" href="${kbEsc(downloadHref)}" target="_blank" rel="noopener" download>下载</a>`;
        return `
        <tr>
          <td data-i18n-data>${kbEsc(docTitle)}</td>
          <td>${kbEsc(d.file_type || d.type || '-')}</td>
          <td>${parseStatusBadge(d.parse_status, d.error_message)}</td>
          <td class="kb-muted">${kbEsc((d.updated_at || d.created_at || '').slice(0, 19).replace('T', ' '))}</td>
          <td>
            <div class="kb-row-actions">
              <button type="button" class="kb-btn xs" data-kb-preview-doc="${kbEsc(d.id)}" data-kb-doc-title="${kbEsc(docTitle)}"${kbGateAttrs(previewGate)}>查看</button>
              ${editAction}
              <button type="button" class="kb-btn xs" data-kb-reparse-doc="${kbEsc(d.id)}"${kbGateAttrs(reparseGate)}>重解析</button>
              ${downloadAction}
              <button type="button" class="kb-btn xs danger-outline" data-kb-delete-doc="${kbEsc(d.id)}" data-kb-doc-title="${kbEsc(docTitle)}"${kbGateAttrs(docGate)}>删除</button>
            </div>
          </td>
        </tr>
      `;
      }).join('') : `<tr><td colspan="5" class="kb-muted">${kbState.docs?.error ? kbEsc(kbState.docs.error) : '暂无文档'}</td></tr>`;

      // 连接列表存在但当前知识库无法解析连接时，不展示可能指向其他实例的控制台入口。
      const selectedConn = resolveConnectionForSource(selected.id);
      const consoleHref = (selectedConn || !kbConnections().length)
        ? resolveWeKnoraConsoleURL(kbState.settings, selectedConn)
        : '';
      const connAccount = selectedConn?.account || '';
      // 登录凭据只给平台管理员：外部 WeKnora 的密码平台从不保存，抽屉里会说明。
      const consoleCredHTML = consoleHref && kbState.settings?.canManage
        ? '<button type="button" class="kb-btn" id="kbConsoleCredBtn">登录凭据</button>'
        : '';
      const selectedTitle = cleanSourceName(selected.name || selected.id);
      rightHTML = `
        <div class="kb-panel-head">
          <div>
            <h2 data-i18n-data>${kbEsc(selectedTitle)}</h2>
            <span class="kb-muted">共 ${total} 篇文档${connAccount ? ` · 登录邮箱：<span data-i18n-data>${kbEsc(connAccount)}</span>` : ''}</span>
          </div>
          <div class="kb-toolbar">
            <button type="button" class="kb-btn primary" id="kbImportDocBtn"${kbGateAttrs(docGate)}>导入文档</button>
            <button type="button" class="kb-btn" id="kbEditSourceBtn"${kbGateAttrs(manageGate)}>编辑知识库</button>
            <button type="button" class="kb-btn danger-outline" id="kbDeleteSourceBtn"${kbGateAttrs(manageGate)}>删除知识库</button>
            ${consoleCredHTML}
            ${consoleHref ? `<a class="kb-btn" href="${kbEsc(consoleHref)}" target="_blank" rel="noopener">高级配置 ↗</a>` : ''}
          </div>
        </div>
        <div class="kb-toolbar" style="margin-bottom:12px">
          <input type="search" class="kb-input" id="kbDocSearchInput" placeholder="搜索文档标题…" value="${kbEsc(kbState.docKeyword)}">
          <button type="button" class="kb-btn" id="kbDocSearchBtn">搜索</button>
          <button type="button" class="kb-btn" id="kbDocRefreshBtn">刷新</button>
        </div>
        <table class="kb-table">
          <thead><tr><th>标题 / 文件名</th><th>类型</th><th>解析状态</th><th>更新时间</th><th>操作</th></tr></thead>
          <tbody>${rows}</tbody>
        </table>
        <div class="kb-toolbar" style="margin-top:12px;justify-content:flex-end">
          <span class="kb-muted">第 ${page} / ${pages} 页 · 共 ${total} 篇</span>
          <button type="button" class="kb-btn xs" id="kbDocPrevBtn" ${page <= 1 ? 'disabled' : ''}>上一页</button>
          <button type="button" class="kb-btn xs" id="kbDocNextBtn" ${page >= pages ? 'disabled' : ''}>下一页</button>
        </div>
      `;
    }

    container.innerHTML = `
      ${notReadyNote}
      <div class="kb-grid-2">
        <div class="kb-panel">
          <div class="kb-panel-head">
            <h2>知识库列表</h2>
          </div>
          ${kbConnFilterHTML()}
          ${capsHTML}
          <div class="kb-toolbar" style="margin-bottom:12px">
            <button type="button" class="kb-btn" id="kbCreateSourceBtn" ${createDenied ? `disabled title="${kbEsc(createReason)}"` : ''}>新建知识库</button>
            <button type="button" class="kb-btn" id="kbOpenEmbConfigBtn" ${createDenied ? `disabled title="${kbEsc(createReason)}"` : ''}>配置向量模型</button>
          </div>
          <div class="kb-source-list">${sourcesHTML}</div>
        </div>
        <div class="kb-panel">${rightHTML}</div>
      </div>
    `;

    container.querySelector('#kbCreateSourceBtn')?.addEventListener('click', () => openKBCreateDrawer());
    container.querySelector('#kbOpenEmbConfigBtn')?.addEventListener('click', () => openKBCreateDrawer({ expandEmbedding: true }));
    container.querySelector('#kbConnFilter')?.addEventListener('change', async e => {
      kbState.connectionFilter = e.target.value || '';
      kbState.docKeyword = '';
      kbState.docPage = 1;
      await loadKnowledgeView();
    });

    container.querySelectorAll('[data-kb-pick]').forEach(btn => {
      btn.addEventListener('click', async () => {
        kbState.selectedSourceId = btn.dataset.kbPick;
        kbState.docPage = 1;
        await loadSelectedDocuments();
        renderKnowledgeShell();
      });
    });

    if (selected) {
      container.querySelector('#kbImportDocBtn')?.addEventListener('click', () => openKBImportDrawer(selected));
      container.querySelector('#kbEditSourceBtn')?.addEventListener('click', () => openKBEditDrawer(selected));
      container.querySelector('#kbDeleteSourceBtn')?.addEventListener('click', () => kbDeleteSource(selected));
      container.querySelector('#kbConsoleCredBtn')?.addEventListener('click', () => openKBConsoleCredentials(resolveConnectionForSource(selected.id)));
    }

    const runDocSearch = async () => {
      kbState.docKeyword = container.querySelector('#kbDocSearchInput')?.value || '';
      kbState.docPage = 1;
      await loadSelectedDocuments();
      renderKnowledgeShell();
    };
    container.querySelector('#kbDocSearchBtn')?.addEventListener('click', runDocSearch);
    container.querySelector('#kbDocSearchInput')?.addEventListener('keydown', e => {
      if (e.key === 'Enter') { e.preventDefault(); runDocSearch(); }
    });
    container.querySelector('#kbDocRefreshBtn')?.addEventListener('click', async () => {
      await loadSelectedDocuments();
      renderKnowledgeShell();
    });

    const goDocPage = async page => {
      kbState.docPage = Math.max(1, page);
      await loadSelectedDocuments();
      renderKnowledgeShell();
    };
    container.querySelector('#kbDocPrevBtn')?.addEventListener('click', () => goDocPage((kbState.docPage || 1) - 1));
    container.querySelector('#kbDocNextBtn')?.addEventListener('click', () => goDocPage((kbState.docPage || 1) + 1));

    container.querySelectorAll('[data-kb-err-msg]').forEach(btn => {
      btn.addEventListener('click', () => {
        openKBDrawer('文档解析失败原因', `<pre class="kb-doc-pre" data-i18n-data>${kbEsc(btn.dataset.kbErrMsg)}</pre>`);
      });
    });

    container.querySelectorAll('[data-kb-preview-doc]').forEach(btn => {
      btn.addEventListener('click', () => openKBDocumentDrawer(btn.dataset.kbPreviewDoc, btn.dataset.kbDocTitle));
    });
    container.querySelectorAll('[data-kb-edit-doc]').forEach(btn => {
      btn.addEventListener('click', () => openKBDocumentDrawer(btn.dataset.kbEditDoc, btn.dataset.kbDocTitle, 'edit'));
    });
    container.querySelectorAll('[data-kb-reparse-doc]').forEach(btn => {
      btn.addEventListener('click', () => kbReparseDoc(btn.dataset.kbReparseDoc));
    });
    container.querySelectorAll('[data-kb-delete-doc]').forEach(btn => {
      btn.addEventListener('click', () => kbDeleteDoc(btn.dataset.kbDeleteDoc, btn.dataset.kbDocTitle));
    });
  }

  function renderSearchTab(container) {
    if (!kbState.settings?.ready) {
      const hint = kbHasConfiguredConnections()
        ? '当前没有已启用的连接，检索调试需先在「接入配置与用量」启用连接。'
        : '请先完成知识库接入配置。';
      container.innerHTML = `<div class="kb-panel"><p class="kb-muted">${hint}</p></div>`;
      return;
    }
    const allowedSources = kbState.sources.filter(s => s.allowed);
    const checks = allowedSources.map(s => `
      <label class="kb-check-chip">
        <input type="checkbox" name="kbSearchSource" value="${kbEsc(s.id)}" ${kbState.searchSources.includes(s.id) ? 'checked' : ''}>
        <span data-i18n-data>${kbEsc(s.name || s.id)}</span>
      </label>
    `).join('');

    const hits = kbState.searchResult?.result?.results || [];
    const hitsHTML = kbState.searchResult ? (hits.length ? `
      <p class="kb-muted" style="margin:0 0 10px">共命中 ${hits.length} 条，耗时 ${kbState.searchResult.latencyMs} ms</p>
      <div class="kb-hit-list">
        ${hits.map(h => `
          <div class="kb-hit">
            <div class="kb-hit-head">
              <span data-i18n-data>${kbEsc(h.title || h.path)}</span>
              <span class="hui-badge" data-tone="muted" data-i18n-data>${kbEsc(h.source)} · score ${Number(h.score || 0).toFixed(3)}</span>
            </div>
            <p class="kb-hit-snippet" data-i18n-data>${kbEsc(h.snippet)}</p>
            <div><button type="button" class="kb-btn" data-kb-open-hit-src="${kbEsc(h.source)}" data-kb-open-hit-path="${kbEsc(h.path)}" data-kb-open-hit-title="${kbEsc(h.title || h.path)}">查看原文分块</button></div>
          </div>
        `).join('')}
      </div>
    ` : '<p class="kb-muted">未检索到匹配片段。</p>') : '';

    container.innerHTML = `
      <div class="kb-panel">
        <div class="kb-panel-head">
          <div>
            <h2>排障调试（检索召回测试）</h2>
            <span class="kb-muted">用于排查知识库召回效果、验证项目权限隔离及确认新文档是否已建索引</span>
          </div>
        </div>
        <div class="kb-toolbar" style="margin-bottom:12px">
          <input type="text" class="kb-input" id="kbSearchQueryInput" style="flex:1;min-width:240px" placeholder="输入自然语言问题或关键词…" value="${kbEsc(kbState.searchQuery)}">
          <button type="button" class="kb-btn primary" id="kbSearchRunBtn">执行检索</button>
        </div>
        ${checks ? `<div class="kb-checks" style="margin-bottom:14px">${checks}</div>` : ''}
        ${hitsHTML}
      </div>
    `;

    container.querySelector('#kbSearchRunBtn')?.addEventListener('click', async () => {
      kbState.searchQuery = container.querySelector('#kbSearchQueryInput')?.value || '';
      kbState.searchSources = [...container.querySelectorAll('input[name="kbSearchSource"]:checked')].map(el => el.value);
      try {
        kbState.searchResult = await kbAPI('/api/knowledge/search', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            projectId: kbProjectId(),
            query: kbState.searchQuery,
            sources: kbState.searchSources
          })
        });
        renderKnowledgeShell();
      } catch (err) {
        kbToast(err.message, true);
      }
    });

    container.querySelectorAll('[data-kb-open-hit-path]').forEach(btn => {
      btn.addEventListener('click', async () => {
        try {
          const doc = await kbAPI(`/api/knowledge/document?projectId=${encodeURIComponent(kbProjectId())}&source=${encodeURIComponent(btn.dataset.kbOpenHitSrc)}&path=${encodeURIComponent(btn.dataset.kbOpenHitPath)}`);
          const text = (doc.lines || []).map(l => l.text).join('\n\n---\n\n') || '暂无正文';
          openKBDrawer(btn.dataset.kbOpenHitTitle, `<pre class="kb-doc-pre">${kbEsc(text)}</pre>`);
        } catch (err) {
          kbToast(err.message, true);
        }
      });
    });
  }

  function renderDistillTab(container) {
    const candidatesRows = kbState.candidates.length ? kbState.candidates.map(c => `
      <tr>
        <td>${kbEsc(c.artifact?.name || 'knowledge.md')}</td>
        <td class="kb-muted">${kbEsc(c.artifact?.runId || '-')}</td>
        <td>${c.submissionId ? '<span class="hui-badge" data-tone="success">已入队</span>' : `<button type="button" class="kb-btn" data-kb-submit-cand="${kbEsc(c.artifact.id)}" data-kb-submit-run="${kbEsc(c.artifact.runId)}">加入审核队列</button>`}</td>
      </tr>
    `).join('') : '<tr><td colspan="3" class="kb-muted">暂无 SOP 产出的 knowledge.md 候选。</td></tr>';

    const subs = kbState.submissions.filter(x => !kbState.subFilter || x.status === kbState.subFilter);
    const subRows = subs.length ? subs.map(item => `
      <tr>
        <td>${kbEsc(item.title)}</td>
        <td>${kbEsc(item.source || '未指定')}</td>
        <td>${parseStatusBadge(item.status)}</td>
        <td class="kb-muted">${kbEsc((item.updatedAt || '').slice(0, 19).replace('T', ' '))}</td>
        <td><button type="button" class="kb-btn" data-kb-review-sub="${kbEsc(item.id)}">审核 / 详情</button></td>
      </tr>
    `).join('') : '<tr><td colspan="5" class="kb-muted">当前筛选条件下暂无沉淀条目。</td></tr>';

    container.innerHTML = `
      <div class="kb-panel">
        <div class="kb-panel-head"><h2>SOP 知识候选（knowledge.md）</h2></div>
        <table class="kb-table">
          <thead><tr><th>产物</th><th>Run ID</th><th>操作</th></tr></thead>
          <tbody>${candidatesRows}</tbody>
        </table>
      </div>
      <div class="kb-panel">
        <div class="kb-panel-head">
          <h2>知识沉淀审核队列</h2>
          <div class="kb-toolbar">
            <select id="kbSubStatusFilter" class="kb-input">
              <option value="pending" ${kbState.subFilter === 'pending' ? 'selected' : ''}>待审核</option>
              <option value="published" ${kbState.subFilter === 'published' ? 'selected' : ''}>已发布</option>
              <option value="rejected" ${kbState.subFilter === 'rejected' ? 'selected' : ''}>已驳回</option>
              <option value="" ${kbState.subFilter === '' ? 'selected' : ''}>全部</option>
            </select>
          </div>
        </div>
        <table class="kb-table">
          <thead><tr><th>标题</th><th>目标知识库</th><th>状态</th><th>更新时间</th><th>操作</th></tr></thead>
          <tbody>${subRows}</tbody>
        </table>
      </div>
    `;

    container.querySelector('#kbSubStatusFilter')?.addEventListener('change', e => {
      kbState.subFilter = e.target.value;
      renderKnowledgeShell();
    });

    container.querySelectorAll('[data-kb-submit-cand]').forEach(btn => {
      btn.addEventListener('click', async () => {
        try {
          await kbAPI('/api/knowledge/submissions', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              runId: btn.dataset.kbSubmitRun,
              artifactId: btn.dataset.kbSubmitCand,
              projectId: kbProjectId()
            })
          });
          kbToast('已加入审核队列');
          await loadKnowledgeView();
        } catch (err) {
          kbToast(err.message, true);
        }
      });
    });

    container.querySelectorAll('[data-kb-review-sub]').forEach(btn => {
      btn.addEventListener('click', async () => {
        try {
          const item = await kbAPI('/api/knowledge/submissions/' + encodeURIComponent(btn.dataset.kbReviewSub));
          const options = kbState.sources.filter(s => s.allowed).map(s => `<option value="${kbEsc(s.id)}" ${item.source === s.id ? 'selected' : ''}>${kbEsc(s.name || s.id)} (${kbEsc(s.id)})</option>`).join('');
          const readonly = item.status === 'published';
          const bodyHTML = `
            <div class="kb-field"><label>标题</label><input id="kbSubTitle" class="kb-input" value="${kbEsc(item.title)}" ${readonly ? 'readonly' : ''}></div>
            <div class="kb-field"><label>写入目标知识库</label><select id="kbSubSource" class="kb-input" ${readonly ? 'disabled' : ''}><option value="">请选择知识库</option>${options}</select></div>
            ${!isWeKnora() ? `<div class="kb-field"><label>相对路径（.md）</label><input id="kbSubPath" class="kb-input" value="${kbEsc(item.path || '')}" ${readonly ? 'readonly' : ''}></div>` : ''}
            <div class="kb-field"><label>Markdown 正文</label><textarea id="kbSubContent" class="kb-input" rows="12" ${readonly ? 'readonly' : ''}>${kbEsc(item.content || '')}</textarea></div>
            ${item.error ? `<p class="kb-muted" style="color:var(--hui-error)">${kbEsc(item.error)}</p>` : ''}
          `;
          const footHTML = readonly ? '' : `
            <button type="button" class="kb-btn" id="kbSubRejectBtn">驳回</button>
            <button type="button" class="kb-btn" id="kbSubSaveBtn">保存草稿</button>
            <button type="button" class="kb-btn primary" id="kbSubPublishBtn">通过并发布</button>
          `;
          openKBDrawer('审核沉淀条目', bodyHTML, footHTML);

          const saveEdit = async () => {
            await kbAPI('/api/knowledge/submissions/' + encodeURIComponent(item.id), {
              method: 'PUT',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({
                title: document.getElementById('kbSubTitle')?.value || '',
                source: document.getElementById('kbSubSource')?.value || '',
                path: document.getElementById('kbSubPath')?.value || '',
                content: document.getElementById('kbSubContent')?.value || ''
              })
            });
          };
          document.getElementById('kbSubSaveBtn')?.addEventListener('click', async () => {
            try { await saveEdit(); kbToast('已保存'); closeKBDrawer(); await loadKnowledgeView(); }
            catch (err) { kbToast(err.message, true); }
          });
          document.getElementById('kbSubRejectBtn')?.addEventListener('click', async () => {
            try {
              await kbAPI('/api/knowledge/submissions/' + encodeURIComponent(item.id) + '/reject', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
              kbToast('已驳回'); closeKBDrawer(); await loadKnowledgeView();
            } catch (err) { kbToast(err.message, true); }
          });
          document.getElementById('kbSubPublishBtn')?.addEventListener('click', async () => {
            try {
              await saveEdit();
              await kbAPI('/api/knowledge/submissions/' + encodeURIComponent(item.id) + '/publish', { method: 'POST' });
              kbToast('发布成功，已写入知识库'); closeKBDrawer(); await loadKnowledgeView();
            } catch (err) { kbToast(err.message, true); }
          });
        } catch (err) {
          kbToast(err.message, true);
        }
      });
    });
  }

  function renderSettingsTab(container) {
    const s = kbState.settings || {};
    const provider = s.provider || 'lightcloud';
    const pid = kbProjectId();
    const projectAllowed = (s.projectSources && pid && s.projectSources[pid]) || s.defaultSources || [];
    const sourceChecks = kbState.sources.map(src => `
      <label class="kb-check-chip">
        <input type="checkbox" name="kbProjectScope" value="${kbEsc(src.id)}" ${projectAllowed.includes(src.id) ? 'checked' : ''} ${!s.canManage ? 'disabled' : ''}>
        <span data-i18n-data>${kbEsc(src.name || src.id)}</span>
      </label>
    `).join('');

    const agentsRows = (kbState.usage?.agents || []).map(a => `
      <tr><td>${kbEsc(a.name)}</td><td class="kb-muted">${kbEsc((a.workflows || []).join('、') || '-')}</td></tr>
    `).join('') || '<tr><td colspan="2" class="kb-muted">暂无开启团队知识库的角色</td></tr>';

    const activityRows = (kbState.usage?.activity || []).slice(0, 20).map(act => {
      const admin = String(act.tool || '').indexOf('admin:') === 0;
      return `
      <tr>
        <td>${kbAuditTool(act.tool)}</td>
        <td class="kb-muted">${kbEsc(act.query || '-')}</td>
        <td>${admin ? '—' : kbEsc(act.hits)}</td>
        <td>${act.ok ? '<span class="hui-badge" data-tone="success">成功</span>' : '<span class="hui-badge" data-tone="error">失败</span>'}</td>
        <td class="kb-muted">${admin ? '—' : kbEsc(act.latencyMs) + ' ms'}</td>
      </tr>
    `;
    }).join('') || '<tr><td colspan="5" class="kb-muted">暂无代理检索记录</td></tr>';

    container.innerHTML = `
      ${s.canManage ? `
      <div class="kb-panel">
        <div class="kb-panel-head">
          <h2>快速接入 WeKnora（一键开启）</h2>
          <span class="kb-muted">选择新建内置 WeKnora 容器实例，或链接已有 WeKnora 服务</span>
        </div>
        <div class="kb-checks" style="margin-bottom:12px">
          <label class="kb-check-chip"><input type="radio" name="kbProvisionMode" value="managed" checked> <span>新建内置 WeKnora 实例（自动拉起服务并写入配置）</span></label>
          <label class="kb-check-chip"><input type="radio" name="kbProvisionMode" value="external"> <span>链接已有 WeKnora 实例</span></label>
        </div>
        <div id="kbProvManagedBox" class="kb-toolbar">
          <button type="button" class="kb-btn primary" id="kbProvisionManagedBtn">一键启动并接入 WeKnora</button>
          <span class="kb-muted">将在同 Docker 网络自动拉起 WeKnora、初始化账号并签发 API Key</span>
        </div>
        <div id="kbProvExternalBox" class="kb-form-grid" hidden>
          <div class="kb-field">
            <label for="kbExtName">连接名称（可选，用于区分多个空间）</label>
            <input id="kbExtName" class="kb-input" placeholder="例如：研发空间 / 测试空间">
          </div>
          <div class="kb-field">
            <label for="kbExtUrl">已有 WeKnora 地址</label>
            <input id="kbExtUrl" class="kb-input" placeholder="http://weknora-app:8080 或 https://weknora.example.com" value="${kbEsc(s.url || '')}">
          </div>
          <div class="kb-field">
            <label for="kbExtToken">API Key（可选，留空可用下方账号密码自动获取）</label>
            <input id="kbExtToken" type="password" class="kb-input" placeholder="sk-...">
          </div>
          <div class="kb-field">
            <label for="kbExtEmail">WeKnora 登录邮箱（未填 Key 时使用）</label>
            <input id="kbExtEmail" class="kb-input" placeholder="admin@example.com">
          </div>
          <div class="kb-field">
            <label for="kbExtPass">WeKnora 登录密码</label>
            <input id="kbExtPass" type="password" class="kb-input" placeholder="••••••••">
          </div>
          <div class="kb-toolbar" style="grid-column:1 / -1">
            <button type="button" class="kb-btn primary" id="kbProvisionExternalBtn">一键链接并追加保存</button>
          </div>
        </div>
      </div>

      ${(s.connections && s.connections.length) ? `
      <div class="kb-panel">
        <div class="kb-panel-head"><h2>已连接的知识库实例（${s.connections.length}）</h2></div>
        <table class="kb-table">
          <thead><tr><th>名称</th><th>登录邮箱</th><th>引擎</th><th>服务地址</th><th>凭据</th><th>能力状态</th><th>操作</th></tr></thead>
          <tbody>
            ${s.connections.map(c => `
              <tr>
                <td><b>${kbEsc(c.name || c.id)}</b></td>
                <td>${kbEsc(c.account || '-')}</td>
                <td>${kbEsc(c.provider || 'weknora')}</td>
                <td class="kb-muted">${kbEsc(c.url)}</td>
                <td>${c.tokenConfigured ? '<span class="hui-badge" data-tone="success">已配置</span>' : '<span class="hui-badge" data-tone="warn">未配置</span>'}</td>
                <td>${kbCapabilityBadges(c.id)}</td>
                <td>
                  <div class="kb-row-actions">
                    <button type="button" class="kb-btn xs" data-kb-probe-conn="${kbEsc(c.id)}">探测</button>
                    <button type="button" class="kb-btn danger-outline xs" data-kb-del-conn="${kbEsc(c.id)}">移除</button>
                  </div>
                </td>
              </tr>
            `).join('')}
          </tbody>
        </table>
      </div>
      ` : ''}
      ` : ''}

      <div class="kb-panel">
        <div class="kb-panel-head">
          <h2>上游知识库接入配置</h2>
          ${s.detectedWeKnoraUrl && s.canManage ? `<button type="button" class="kb-btn" id="kbFillWeKnoraBtn">一键填入内置 WeKnora</button>` : ''}
        </div>
        <div class="kb-form-grid">
          <div class="kb-field">
            <label for="kbCfgProvider">知识库引擎</label>
            <select id="kbCfgProvider" class="kb-input" ${!s.canManage ? 'disabled' : ''}>
              <option value="weknora" ${provider === 'weknora' ? 'selected' : ''}>WeKnora（推荐，原生支持）</option>
              <option value="lightcloud" ${provider === 'lightcloud' ? 'selected' : ''}>LightCloud KB（MCP）</option>
            </select>
          </div>
          <div class="kb-field">
            <label for="kbCfgUrl">服务地址</label>
            <input id="kbCfgUrl" class="kb-input" value="${kbEsc(s.url || '')}" placeholder="http://weknora-app:8080" ${!s.canManage ? 'readonly' : ''}>
            <small>WeKnora 填服务根地址即可，无需手动拼接 /mcp</small>
          </div>
          <div class="kb-field">
            <label for="kbCfgToken">API Key / Token</label>
            <input id="kbCfgToken" type="password" class="kb-input" placeholder="${s.tokenConfigured ? '已配置（留空保持不变）' : '填写 WeKnora X-API-Key 或 MCP Token'}" ${!s.canManage ? 'readonly' : ''}>
          </div>
          <div class="kb-field">
            <label>开关选项</label>
            <label class="kb-check-chip"><input type="checkbox" id="kbCfgEnabled" ${s.enabled ? 'checked' : ''} ${!s.canManage ? 'disabled' : ''}> 启用平台知识库</label>
            <label class="kb-check-chip"><input type="checkbox" id="kbCfgWrite" ${s.writeEnabled ? 'checked' : ''} ${!s.canManage ? 'disabled' : ''}> 允许审核后写回</label>
          </div>
        </div>
        ${kbState.sources.length ? `
          <div class="kb-field" style="margin-top:14px">
            <label>当前项目（${kbEsc(pid || '默认')}）授权知识库范围 <small>不勾选表示允许访问全部</small></label>
            <div class="kb-checks">${sourceChecks}</div>
          </div>
        ` : ''}
        <div class="kb-toolbar" style="margin-top:16px">
          ${s.canManage ? `
            <button type="button" class="kb-btn" id="kbCheckBtn">检查连接</button>
            <button type="button" class="kb-btn primary" id="kbSaveCfgBtn">保存配置</button>
            <span id="kbConfigFeedback" class="kb-muted" role="status"></span>
          ` : '<span class="kb-muted">仅平台管理员可修改接入配置</span>'}
        </div>
      </div>

      <div class="kb-panel">
        <div class="kb-panel-head"><h2>角色用量与最近检索审计</h2></div>
        <table class="kb-table" style="margin-bottom:14px">
          <thead><tr><th>角色</th><th>关联 SOP</th></tr></thead>
          <tbody>${agentsRows}</tbody>
        </table>
        <table class="kb-table">
          <thead><tr><th colspan="2">查询 / 目标</th><th>命中</th><th>结果</th><th>耗时</th></tr></thead>
          <tbody>${activityRows}</tbody>
        </table>
      </div>
    `;

    container.querySelectorAll('input[name="kbProvisionMode"]').forEach(radio => {
      radio.addEventListener('change', () => {
        const isManaged = radio.value === 'managed' && radio.checked;
        const mBox = container.querySelector('#kbProvManagedBox');
        const eBox = container.querySelector('#kbProvExternalBox');
        if (mBox) mBox.hidden = !isManaged;
        if (eBox) eBox.hidden = isManaged;
      });
    });

    container.querySelector('#kbProvisionManagedBtn')?.addEventListener('click', async btn => {
      const target = btn.currentTarget;
      target.disabled = true;
      try {
        kbToast('正在拉起内置 WeKnora 服务并初始化配置（首次拉取镜像可能需 1~2 分钟）…');
        const res = await kbAPI('/api/knowledge/weknora/provision', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ mode: 'managed', writeEnabled: true })
        });
        kbToast(res.message || '已开启内置 WeKnora');
        await loadKnowledgeView();
      } catch (err) {
        kbToast(err.message, true);
        target.disabled = false;
      }
    });

    container.querySelector('#kbProvisionExternalBtn')?.addEventListener('click', async btn => {
      const target = btn.currentTarget;
      target.disabled = true;
      try {
        const res = await kbAPI('/api/knowledge/weknora/provision', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            mode: 'external',
            name: container.querySelector('#kbExtName')?.value || '',
            url: container.querySelector('#kbExtUrl')?.value || '',
            token: container.querySelector('#kbExtToken')?.value || '',
            email: container.querySelector('#kbExtEmail')?.value || '',
            password: container.querySelector('#kbExtPass')?.value || '',
            writeEnabled: true
          })
        });
        kbToast(res.message || '已链接外部 WeKnora');
        await loadKnowledgeView();
      } catch (err) {
        kbToast(err.message, true);
        target.disabled = false;
      }
    });

    container.querySelectorAll('[data-kb-probe-conn]').forEach(btn => {
      btn.addEventListener('click', () => kbProbeConnection(btn.dataset.kbProbeConn, btn));
    });

    container.querySelectorAll('[data-kb-del-conn]').forEach(btn => {
      btn.addEventListener('click', async () => {
        const connId = btn.dataset.kbDelConn;
        const conn = kbConnections().find(c => c.id === connId);
        const name = (conn && (conn.name || conn.id)) || connId;
        const confirmed = typeof afConfirm === 'function'
          ? await afConfirm({
            title: '移除知识库连接',
            message: `确定移除「${name}」吗？仅解除平台接入，不会删除远端知识库与文档。`,
            danger: true,
            confirmText: '移除连接'
          })
          : false;
        if (!confirmed) return;
        try {
          await kbAPI('/api/knowledge/connections/' + encodeURIComponent(connId), { method: 'DELETE' });
          delete kbState.capabilitiesByConn[connId];
          delete kbCapabilityPresent[connId];
          delete kbCapabilityProbed[connId];
          kbToast('已移除连接');
          await loadKnowledgeView();
        } catch (err) { kbToastError(err, connId); }
      });
    });

    container.querySelector('#kbFillWeKnoraBtn')?.addEventListener('click', () => {
      container.querySelector('#kbCfgProvider').value = 'weknora';
      container.querySelector('#kbCfgUrl').value = s.detectedWeKnoraUrl;
      container.querySelector('#kbCfgEnabled').checked = true;
    });

    container.querySelector('#kbCheckBtn')?.addEventListener('click', async () => {
      try {
        const res = await kbAPI('/api/knowledge/check', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            provider: container.querySelector('#kbCfgProvider').value,
            url: container.querySelector('#kbCfgUrl').value,
            token: container.querySelector('#kbCfgToken').value
          })
        });
        if (res.ok) kbToast(`连接成功：发现 ${res.sources?.length || 0} 个知识库（${res.latencyMs} ms）`);
        else kbToast(res.error || '连接失败', true);
      } catch (err) {
        kbToast(err.message, true);
      }
    });

    container.querySelector('#kbSaveCfgBtn')?.addEventListener('click', async () => {
      const nextProjectSources = Object.assign({}, s.projectSources || {});
      if (pid && kbState.sources.length) {
        const picked = [...container.querySelectorAll('input[name="kbProjectScope"]:checked')].map(el => el.value);
        if (picked.length) nextProjectSources[pid] = picked;
        else delete nextProjectSources[pid];
      }
      try {
        await kbAPI('/api/knowledge/settings', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            provider: container.querySelector('#kbCfgProvider').value,
            enabled: container.querySelector('#kbCfgEnabled').checked,
            writeEnabled: container.querySelector('#kbCfgWrite').checked,
            url: container.querySelector('#kbCfgUrl').value,
            token: container.querySelector('#kbCfgToken').value,
            defaultSources: s.defaultSources || [],
            projectSources: nextProjectSources
          })
        });
        kbToast('配置已保存');
        await loadKnowledgeView();
      } catch (err) {
        kbToast(err.message, true);
      }
    });

    kbEnsureConnectionCapabilities();
  }

  initKBDrawerA11y();
  window.loadKnowledgeView = loadKnowledgeView;
})();
