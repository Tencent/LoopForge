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

  // 连接级五项管理能力：库管理 / 文档写入 / 分块读取 / 预览下载 / 重解析。
  const KB_CAPABILITIES = [
    { key: 'kb_manage', label: '库管理' },
    { key: 'doc_write', label: '文档写入' },
    { key: 'chunk_read', label: '分块读取' },
    { key: 'preview_download', label: '预览下载' },
    { key: 'reparse', label: '重解析' }
  ];
  // 能力不可用原因与探测在途标记不进 kbState，避免触发整页重绘。
  const kbCapabilityReasons = {};
  const kbCapabilityFetching = {};
  const kbCapabilityProbed = {};
  let kbRenderTimer = 0;

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
    return kbCapabilityReasons[connId + '|' + cap] || '';
  }

  // 能力门禁：未探测或后端未提供能力接口时沿用旧行为（乐观放行），
  // 后端明确声明不足或返回 503 后才禁用对应写操作。
  function kbCapabilityAllowed(connId, cap) {
    if (kbCapabilityProbed[connId] !== 'ok') return true;
    return kbCapabilityState(connId, cap) === 'available';
  }

  function kbCapabilityTone(status) {
    if (status === 'available') return 'success';
    if (status === 'unavailable') return 'error';
    return 'neutral';
  }

  function markCapabilityUnavailable(connId, cap, reason) {
    if (!cap) return;
    kbCapabilityProbed[connId || ''] = 'ok';
    kbCapabilitiesFor(connId)[cap] = 'unavailable';
    if (reason) kbCapabilityReasons[connId + '|' + cap] = reason;
  }

  function kbNormalizeCapabilities(raw, connId) {
    const out = {};
    if (!raw || typeof raw !== 'object') {
      KB_CAPABILITIES.forEach(c => { out[c.key] = 'unknown'; });
      return out;
    }
    const src = raw.capabilities && typeof raw.capabilities === 'object' ? raw.capabilities : raw;
    KB_CAPABILITIES.forEach(c => {
      const v = src[c.key];
      let status = 'unknown';
      let reason = '';
      if (typeof v === 'string') status = v;
      else if (v && typeof v === 'object') {
        status = v.status || 'unknown';
        reason = v.reason || v.message || '';
      }
      if (['available', 'unavailable', 'unknown'].indexOf(status) < 0) status = 'unknown';
      out[c.key] = status;
      if (reason) kbCapabilityReasons[connId + '|' + c.key] = reason;
    });
    return out;
  }

  function kbHasCapabilityPayload(raw) {
    if (!raw || typeof raw !== 'object') return false;
    if (raw.capabilities && typeof raw.capabilities === 'object') return true;
    return KB_CAPABILITIES.some(c => raw[c.key] !== undefined);
  }

  function kbMergeCapabilities(connId, raw) {
    const cid = connId || '';
    kbState.capabilitiesByConn[cid] = kbNormalizeCapabilities(raw, cid);
    kbCapabilityProbed[cid] = kbHasCapabilityPayload(raw) ? 'ok' : 'legacy';
    return kbState.capabilitiesByConn[cid];
  }

  // 503 capability_unavailable：把对应连接的能力就地降级为不可用，并提示原因。
  function handleCapabilityError(err) {
    if (!err || err.code !== 'capability_unavailable') return false;
    markCapabilityUnavailable(err.connectionId || kbState.connectionFilter || '', err.capability, err.reason || err.message);
    kbToast((err.message || '当前连接缺少该操作所需权限') + (err.reason ? '：' + err.reason : ''), true);
    scheduleKBRender();
    return true;
  }

  function kbToastError(err) {
    if (!err) return;
    if (handleCapabilityError(err)) return;
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
        kbCapabilityProbed[connId] = 'legacy';
      } else if (!handleCapabilityError(err)) {
        kbCapabilityProbed[connId] = 'unknown';
      }
    } finally {
      delete kbCapabilityFetching[connId];
    }
  }

  function kbEnsureConnectionCapabilities() {
    kbConnections().forEach(c => {
      if (!c || !c.id || kbCapabilityProbed[c.id] || kbCapabilityFetching[c.id]) return;
      loadConnectionCapabilities(c.id).then(() => {
        if (kbState.tab === 'settings') scheduleKBRender();
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
      kbToastError(err);
      scheduleKBRender();
    } finally {
      if (button) button.disabled = false;
    }
  }

  function isWeKnora() {
    return (kbState.settings && (kbState.settings.provider || 'lightcloud')) === 'weknora';
  }

  function resolveConnectionForSource(sourceId) {
    const conns = kbState.settings?.connections || [];
    if (!conns.length) return null;
    const idx = String(sourceId || '').indexOf('::');
    if (idx > 0) {
      const cid = sourceId.slice(0, idx);
      return conns.find(c => c.id === cid) || conns[0];
    }
    return conns[0];
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
    drawer.querySelector('[data-kb-drawer-close]')?.addEventListener('click', closeKBDrawer);
    const first = kbDrawerFocusables(drawer)[0];
    if (first && typeof first.focus === 'function') first.focus();
  }

  async function loadKnowledgeView() {
    const root = document.getElementById('knowledgeRoot');
    if (!root) return;
    kbState.loading = true;
    renderKnowledgeShell();
    try {
      const pid = encodeURIComponent(kbProjectId());
      kbState.settings = await kbAPI('/api/knowledge/settings');
      if (kbState.settings.ready) {
        const [sources, usage, candidates, submissions] = await Promise.all([
          kbAPI('/api/knowledge/sources?projectId=' + pid).catch(() => []),
          kbAPI('/api/knowledge/usage?projectId=' + pid).catch(() => ({ agents: [], totalAgents: 0, activity: [] })),
          kbAPI('/api/knowledge/candidates?projectId=' + pid).catch(() => []),
          kbAPI('/api/knowledge/submissions?projectId=' + pid).catch(() => [])
        ]);
        kbState.sources = Array.isArray(sources) ? sources : [];
        kbState.usage = usage;
        kbState.candidates = Array.isArray(candidates) ? candidates : [];
        kbState.submissions = Array.isArray(submissions) ? submissions : [];
        if (!kbState.selectedSourceId || !kbState.sources.some(s => s.id === kbState.selectedSourceId)) {
          const firstAllowed = kbState.sources.find(s => s.allowed) || kbState.sources[0];
          kbState.selectedSourceId = firstAllowed ? firstAllowed.id : '';
        }
        if (kbState.selectedSourceId && isWeKnora()) {
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
      kbToast(err.message, true);
    } finally {
      kbState.loading = false;
      renderKnowledgeShell();
    }
  }

  async function loadSelectedDocuments() {
    if (!kbState.selectedSourceId || !isWeKnora()) {
      kbState.docs = null;
      return;
    }
    const pid = encodeURIComponent(kbProjectId());
    const q = new URLSearchParams({
      projectId: kbProjectId(),
      keyword: kbState.docKeyword,
      page: String(kbState.docPage),
      pageSize: '20'
    });
    try {
      kbState.docs = await kbAPI(`/api/knowledge/sources/${encodeURIComponent(kbState.selectedSourceId)}/documents?${q.toString()}`);
    } catch (err) {
      kbState.docs = { error: err.message, documents: [], total: 0 };
    }
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
    if (s === 'pending') return `<span class="hui-badge" data-tone="warn"${tip}>${status === 'pending' ? '排队中' : kbEsc(status)}</span>`;
    return `<span class="hui-badge" data-tone="muted"${tip}>${kbEsc(status || '-')}</span>`;
  }

  function renderKnowledgeShell() {
    const root = document.getElementById('knowledgeRoot');
    if (!root) return;
    const s = kbState.settings || {};
    const pendingSubs = kbState.submissions.filter(x => x.status === 'pending').length;
    const allowedCount = kbState.sources.filter(x => x.allowed).length;
    const engineLabel = isWeKnora() ? 'WeKnora' : 'LightCloud KB';

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
        <div class="kb-metric"><span class="kb-metric-label">接入状态</span><span class="kb-metric-val">${s.ready ? '已连接' : '待配置'}</span></div>
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

  function renderSourcesTab(container) {
    if (!kbState.settings?.ready) {
      container.innerHTML = `<div class="kb-panel"><p class="kb-muted">尚未完成知识库接入配置。请前往「接入配置与用量」填写服务地址与 API Key / Token。</p></div>`;
      return;
    }
    const sourcesHTML = kbState.sources.length
      ? kbState.sources.map(src => {
        const conn = resolveConnectionForSource(src.id);
        const accountText = conn?.account || '';
        const isActive = src.id === kbState.selectedSourceId;
        const displayName = cleanSourceName(src.name || src.id);
        return `
        <button type="button" class="kb-source-item ${isActive ? 'active' : ''}" data-kb-pick="${kbEsc(src.id)}" aria-current="${isActive ? 'true' : 'false'}">
          <div class="kb-source-top">
            <span class="kb-source-name">${isActive ? '<span aria-hidden="true">✓</span>' : ''}${kbEsc(displayName)}</span>
            <span class="hui-badge" data-tone="${src.allowed ? 'success' : 'muted'}">${src.allowed ? '已授权' : '未授权'}</span>
          </div>
          <div class="kb-source-meta">
            <span>${src.doc_count || 0} 篇文档</span>
            ${accountText ? `<span>${kbEsc(accountText)}</span>` : ''}
          </div>
        </button>
      `;
      }).join('')
      : '<p class="kb-muted">暂无上游知识库。</p>';

    const selected = kbState.sources.find(s => s.id === kbState.selectedSourceId);
    let rightHTML = '<p class="kb-muted">请从左侧选择一个知识库。</p>';
    if (selected && !isWeKnora()) {
      rightHTML = `<p class="kb-muted">当前为 LightCloud KB 引擎，请使用「排障调试」按路径检索并查看原文。</p>`;
    } else if (selected && isWeKnora()) {
      const docs = kbState.docs?.documents || [];
      const rows = docs.length ? docs.map(d => `
        <tr>
          <td><button type="button" class="kb-btn" data-kb-preview-doc="${kbEsc(d.id)}" data-kb-doc-title="${kbEsc(d.title || d.file_name || d.id)}">${kbEsc(d.title || d.file_name || d.id)}</button></td>
          <td>${kbEsc(d.file_type || d.type || '-')}</td>
          <td>${parseStatusBadge(d.parse_status, d.error_message)}</td>
          <td class="kb-muted">${kbEsc((d.updated_at || d.created_at || '').slice(0, 19).replace('T', ' '))}</td>
        </tr>
      `).join('') : `<tr><td colspan="4" class="kb-muted">${kbState.docs?.error ? kbEsc(kbState.docs.error) : '暂无文档'}</td></tr>`;

      const activeConn = resolveConnectionForSource(selected.id);
      const consoleHref = resolveWeKnoraConsoleURL(kbState.settings, activeConn);
      const connAccount = activeConn?.account ? ` · 登录邮箱：${activeConn.account}` : '';
      const selectedTitle = cleanSourceName(selected.name || selected.id);
      rightHTML = `
        <div class="kb-panel-head">
          <div>
            <h2>${kbEsc(selectedTitle)}</h2>
            <span class="kb-muted">共 ${kbState.docs?.total || 0} 篇文档${kbEsc(connAccount)}</span>
          </div>
          <div class="kb-toolbar">
            <input type="search" class="kb-input" id="kbDocSearchInput" placeholder="搜索文档标题…" value="${kbEsc(kbState.docKeyword)}">
            <button type="button" class="kb-btn" id="kbDocSearchBtn">搜索</button>
            ${consoleHref ? `<a class="kb-btn" href="${kbEsc(consoleHref)}" target="_blank" rel="noopener">管理此知识库 ↗</a>` : ''}
          </div>
        </div>
        <table class="kb-table">
          <thead><tr><th>标题 / 文件名</th><th>类型</th><th>解析状态</th><th>更新时间</th></tr></thead>
          <tbody>${rows}</tbody>
        </table>
      `;
    }

    container.innerHTML = `
      <div class="kb-grid-2">
        <div class="kb-panel">
          <div class="kb-panel-head">
            <h2>知识库列表</h2>
          </div>
          <div class="kb-source-list">${sourcesHTML}</div>
        </div>
        <div class="kb-panel">${rightHTML}</div>
      </div>
    `;

    container.querySelectorAll('[data-kb-pick]').forEach(btn => {
      btn.addEventListener('click', async () => {
        kbState.selectedSourceId = btn.dataset.kbPick;
        kbState.docPage = 1;
        await loadSelectedDocuments();
        renderKnowledgeShell();
      });
    });

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
    container.querySelectorAll('[data-kb-err-msg]').forEach(btn => {
      btn.addEventListener('click', () => {
        openKBDrawer('文档解析失败原因', `<pre class="kb-doc-pre">${kbEsc(btn.dataset.kbErrMsg)}</pre>`);
      });
    });

    container.querySelectorAll('[data-kb-preview-doc]').forEach(btn => {
      btn.addEventListener('click', async () => {
        const docId = btn.dataset.kbPreviewDoc;
        const title = btn.dataset.kbDocTitle;
        try {
          const doc = await kbAPI(`/api/knowledge/document?projectId=${encodeURIComponent(kbProjectId())}&source=${encodeURIComponent(kbState.selectedSourceId)}&path=${encodeURIComponent(docId)}`);
          const text = (doc.lines || []).map(l => l.text).join('\n\n---\n\n') || '暂无文本分块';
          openKBDrawer(title, `<pre class="kb-doc-pre">${kbEsc(text)}</pre>`);
        } catch (err) {
          kbToast(err.message, true);
        }
      });
    });
  }

  function renderSearchTab(container) {
    if (!kbState.settings?.ready) {
      container.innerHTML = `<div class="kb-panel"><p class="kb-muted">请先完成知识库接入配置。</p></div>`;
      return;
    }
    const allowedSources = kbState.sources.filter(s => s.allowed);
    const checks = allowedSources.map(s => `
      <label class="kb-check-chip">
        <input type="checkbox" name="kbSearchSource" value="${kbEsc(s.id)}" ${kbState.searchSources.includes(s.id) ? 'checked' : ''}>
        <span>${kbEsc(s.name || s.id)}</span>
      </label>
    `).join('');

    const hits = kbState.searchResult?.result?.results || [];
    const hitsHTML = kbState.searchResult ? (hits.length ? `
      <p class="kb-muted" style="margin:0 0 10px">共命中 ${hits.length} 条，耗时 ${kbState.searchResult.latencyMs} ms</p>
      <div class="kb-hit-list">
        ${hits.map(h => `
          <div class="kb-hit">
            <div class="kb-hit-head">
              <span>${kbEsc(h.title || h.path)}</span>
              <span class="hui-badge" data-tone="muted">${kbEsc(h.source)} · score ${Number(h.score || 0).toFixed(3)}</span>
            </div>
            <p class="kb-hit-snippet">${kbEsc(h.snippet)}</p>
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
            ${item.error ? `<p class="kb-muted" style="color:#dc2626">${kbEsc(item.error)}</p>` : ''}
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
        <span>${kbEsc(src.name || src.id)}</span>
      </label>
    `).join('');

    const agentsRows = (kbState.usage?.agents || []).map(a => `
      <tr><td>${kbEsc(a.name)}</td><td class="kb-muted">${kbEsc((a.workflows || []).join('、') || '-')}</td></tr>
    `).join('') || '<tr><td colspan="2" class="kb-muted">暂无开启团队知识库的角色</td></tr>';

    const activityRows = (kbState.usage?.activity || []).slice(0, 20).map(act => `
      <tr>
        <td>${kbEsc(act.tool)}</td>
        <td class="kb-muted">${kbEsc(act.query || '-')}</td>
        <td>${act.hits}</td>
        <td>${act.ok ? '<span class="hui-badge" data-tone="success">成功</span>' : '<span class="hui-badge" data-tone="error">失败</span>'}</td>
        <td class="kb-muted">${act.latencyMs} ms</td>
      </tr>
    `).join('') || '<tr><td colspan="5" class="kb-muted">暂无代理检索记录</td></tr>';

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
          <thead><tr><th>工具</th><th>检索词</th><th>命中</th><th>结果</th><th>耗时</th></tr></thead>
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
          delete kbCapabilityProbed[connId];
          kbToast('已移除连接');
          await loadKnowledgeView();
        } catch (err) { kbToastError(err); }
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
