// SOP 维度 RSI 工作台：首页按 SOP 展示状态/偏好/核心指标/开关；详情页展示核心指标、版本与灰度趋势、本次修改与问题。
// 视觉令牌与图标沿用 Harness UI（MIT），见 harness-ui-LICENSE.txt。

const EVO_SOP_PREFS = [
  { key: 'tokens', label: '减少 Token', short: '省 Token', desc: '压缩冗余上下文、重复调研与无效输出' },
  { key: 'tools', label: '减少工具调用', short: '省工具', desc: '减少同一工具高频重复调用与报错重试' },
  { key: 'duration', label: '缩短执行耗时', short: '提速度', desc: '减少节点串行返工与等待耗时' },
  { key: 'clean', label: '提高一次成功率', short: '一次成功', desc: '降低失败与打回返工，提升一次通过率' },
];
const EVO_SOP_PREF_MAP = Object.fromEntries(EVO_SOP_PREFS.map(p => [p.key, p]));
const EVO_SOP_PREF_PRESETS = [
  { id: 'cost', label: '省成本', prefs: ['tokens', 'tools'] },
  { id: 'speed', label: '提速度', prefs: ['duration', 'tools'] },
  { id: 'quality', label: '求稳定', prefs: ['clean', 'tokens'] },
];
const EVO_SOP_BATCH_PRESETS = [5, 10, 15, 20, 30];
const EVO_SOP_STAGES = {
  monitor: ['监测中', ''],
  optimize: ['优化中', 'warn'],
  gray: ['灰度中', 'ok'],
};
const EVO_SOP_METRICS = [
  { key: 'score', label: 'SOP 评分', higherBetter: true, format: v => v == null ? '—' : `${Math.round(v)}`, unit: '分' },
  { key: 'tokens', label: '平均 Token', higherBetter: false, format: v => evoSOPFormatTokens(v), unit: '' },
  { key: 'tools', label: '平均工具调用', higherBetter: false, format: v => v == null ? '—' : `${evoSOPNum(v, 1)}`, unit: '次' },
  { key: 'duration', label: '平均执行耗时', higherBetter: false, format: v => evoSOPFormatSec(v), unit: '' },
  { key: 'clean', label: '一次成功率', higherBetter: true, format: v => v == null ? '—' : `${Math.round(Number(v) * 100)}%`, unit: '' },
];

const evoSOPState = {
  filter: 'all',
  q: '',
  mode: 'sop', // 'sop' | 'assets'
  view: null,  // { workflowId, tab, scope, metric, fileIndex, diffMode, detail, loading, error, expandedGray, seq }
  prefTarget: null,
  busyIds: new Set(),
};

function evoSOPNum(v, d = 1) {
  const n = Number(v);
  if (!Number.isFinite(n)) return '—';
  return Number.isInteger(n) ? String(n) : n.toFixed(d);
}

function evoSOPFormatTokens(v) {
  const n = Number(v);
  if (!Number.isFinite(n) || n <= 0) return '—';
  if (n >= 1000000) return `${(n / 1000000).toFixed(2)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(Math.round(n));
}

function evoSOPFormatSec(v) {
  const n = Number(v);
  if (!Number.isFinite(n) || n <= 0) return '—';
  if (n >= 60) {
    const m = Math.floor(n / 60);
    const s = Math.round(n % 60);
    return s ? `${m}m ${s}s` : `${m}m`;
  }
  return `${evoSOPNum(n, 1)}s`;
}

function evoSOPMetricValue(metrics, key) {
  if (!metrics || !(metrics.runs > 0)) return null;
  switch (key) {
    case 'score': return metrics.score != null ? Number(metrics.score) : null;
    case 'tokens': return metrics.avgTokens > 0 ? Number(metrics.avgTokens) : null;
    case 'tools': return Number.isFinite(metrics.avgTools) ? Number(metrics.avgTools) : null;
    case 'duration': return metrics.avgDuration > 0 ? Number(metrics.avgDuration) : null;
    case 'clean': return Number.isFinite(metrics.cleanRate) ? Number(metrics.cleanRate) : null;
    default: return null;
  }
}

function evoSOPDeltaHTML(cur, prev, higherBetter, formatFn) {
  if (cur == null || prev == null || prev === 0) return '';
  const diff = cur - prev;
  if (Math.abs(diff) < 1e-6) return '<span class="evo-sop-delta flat">持平</span>';
  const improved = higherBetter ? diff > 0 : diff < 0;
  const pct = Math.round(Math.abs(diff / prev) * 100);
  const arrow = diff > 0 ? '↑' : '↓';
  const text = formatFn ? formatFn(diff, pct) : `${arrow}${pct}%`;
  return `<span class="evo-sop-delta ${improved ? 'good' : 'bad'}">${escapeHTML(text)}</span>`;
}

function evoSOPNormalizePrefs(prefs) {
  const seen = new Set();
  const out = [];
  for (const raw of (Array.isArray(prefs) ? prefs : [])) {
    const k = String(raw || '').trim().toLowerCase();
    if (!EVO_SOP_PREF_MAP[k] || seen.has(k)) continue;
    seen.add(k);
    out.push(k);
  }
  return out.length ? out : ['tokens'];
}

function evoSOPPrefsText(prefs) {
  const list = evoSOPNormalizePrefs(prefs).map(k => EVO_SOP_PREF_MAP[k]?.short || k);
  if (list.length <= 2) return list.join(' › ');
  return `${list[0]} › ${list[1]} +${list.length - 2}`;
}

function evoSOPPrefsTitle(prefs) {
  return evoSOPNormalizePrefs(prefs).map((k, idx) => `P${idx + 1} ${EVO_SOP_PREF_MAP[k]?.label || k}`).join(' > ');
}

function evoSOPStageBadge(stage) {
  const [label, tone] = EVO_SOP_STAGES[stage] || ['监测中', ''];
  return kbBadge(escapeHTML(label), tone);
}

function evoSOPIsEnabledView() {
  return Array.isArray(evo.overview?.sops) && evoSOPState.mode !== 'assets';
}

// ---------- 首页（SOP 维度） ----------

function evoSOPHomeHTML() {
  const sops = evo.overview?.sops || [];
  const counts = {
    all: sops.length,
    monitor: sops.filter(s => s.stage === 'monitor').length,
    optimize: sops.filter(s => s.stage === 'optimize').length,
    gray: sops.filter(s => s.stage === 'gray').length,
  };

  const filters = [
    ['all', '全部', counts.all],
    ['monitor', '监测中', counts.monitor],
    ['optimize', '优化中', counts.optimize],
    ['gray', '灰度中', counts.gray],
  ];

  return `<div class="evo-sop-home">
    ${evoHomeStatusHTML()}
    <section class="evo-sop-panel" aria-label="SOP 进化列表">
      <div class="evo-sop-toolbar">
        <div class="evo-sop-filters" role="group" aria-label="按 SOP 状态筛选">
          ${filters.map(([k, label, n]) => `<button type="button" class="evo-sop-filter-btn" data-evo-sop-filter="${k}" aria-pressed="${evoSOPState.filter === k}">${label}<span>${n}</span></button>`).join('')}
        </div>
        <div class="evo-sop-toolbar-right">
          <label class="evo-home-search">
            ${evoReviewIcon('search')}
            <input class="kb-input" type="search" data-evo-sop-search data-evo-key="sop-search" aria-label="搜索 SOP" placeholder="搜索 SOP 名称或说明" value="${escapeHTML(evoSOPState.q)}">
          </label>
        </div>
      </div>
      <div id="evoSopListHost">${evoSOPListHTML(sops)}</div>
    </section>
    ${evoSOPPrefDialogHTML()}
  </div>`;
}

function evoSOPListHTML(sops) {
  const q = evoSOPState.q.trim().toLowerCase();
  const filtered = sops.filter(s =>
    (evoSOPState.filter === 'all' || s.stage === evoSOPState.filter) &&
    (!q || [s.workflowName, s.workflowId, s.description, s.lastRunNote].some(v => String(v || '').toLowerCase().includes(q)))
  );
  if (!sops.length) {
    return `<section class="evo-home-empty" aria-labelledby="evoSopEmptyTitle">
      <div class="evo-home-empty-mark">${evoReviewIcon('workflow')}</div>
      <h2 id="evoSopEmptyTitle">当前项目暂无 SOP</h2>
      <p>创建并运行 SOP 后，这里会按 SOP 持续统计核心消耗与评分，并按偏好自动沉淀优化建议。</p>
      <div class="evo-home-empty-actions">${evoHomeButton(`${evoReviewIcon('play')}查看运行`, 'data-evo-home-runs')}</div>
    </section>`;
  }
  if (!filtered.length) {
    return `<div class="evo-home-filter-empty">暂无匹配的 SOP ${evoHomeButton('清除筛选', 'data-evo-sop-reset')}</div>`;
  }
  const head = `<div class="evo-sop-columns" aria-hidden="true">
    <span>SOP 与最近分析</span>
    <span>优化偏好 · 周期</span>
    <span>近批指标（Token / 工具 / 评分）</span>
    <span>状态</span>
    <span>自动进化</span>
    <span></span>
  </div>`;
  const rows = filtered.map(evoSOPRowHTML).join('');
  return `${head}<div class="evo-sop-rows">${rows}</div>`;
}

function evoSOPRowHTML(s) {
  const m = s.latestMetrics || {};
  const busy = evoSOPState.busyIds.has(s.workflowId) ? ' disabled' : '';
  const score = m.score != null ? `${m.score} 分` : '—';
  const metricsHTML = m.runs > 0
    ? `<span class="evo-sop-cell-metrics"><b>${escapeHTML(evoSOPFormatTokens(m.avgTokens))}</b><small>Token</small><i>·</i><b>${escapeHTML(evoSOPNum(m.avgTools, 1))}</b><small>工具</small><i>·</i><b class="is-score">${escapeHTML(score)}</b></span>`
    : '<span class="evo-muted">暂无运行数据</span>';
  return `<div class="evo-sop-row${s.enabled ? '' : ' is-paused'}" data-evo-sop-row="${escapeHTML(s.workflowId)}">
    <button type="button" class="evo-sop-row-main" data-evo-sop-open="${escapeHTML(s.workflowId)}" data-evo-key="sop:${escapeHTML(s.workflowId)}">
      <span class="evo-sop-row-title">
        <b>${escapeHTML(s.workflowName || s.workflowId)}</b>
        <span class="evo-sop-ver mono">${escapeHTML(s.currentVersion || 'v1')}</span>
        ${!s.enabled ? '<span class="evo-sop-paused-tag">已暂停</span>' : ''}
      </span>
      <span class="evo-sop-row-note">${escapeHTML(s.lastRunNote || '暂无运行记录')}</span>
    </button>
    <div class="evo-sop-row-pref">
      <button type="button" class="evo-sop-pref-trigger" data-evo-sop-pref-open="${escapeHTML(s.workflowId)}" title="点击调整优化优先级、汇总周期与分析模型（当前：${escapeHTML(evoSOPPrefsTitle(s.preferences))}）"${busy}>
        <span class="evo-sop-pref-pills">${escapeHTML(evoSOPPrefsText(s.preferences))}</span>
        <small>每 ${Number(s.batchSize) || 10} 次</small>
      </button>
    </div>
    <div class="evo-sop-row-metrics">${metricsHTML}</div>
    <div class="evo-sop-row-stage">${evoSOPStageBadge(s.stage)}</div>
    <div class="evo-sop-row-switch">
      <input type="checkbox" role="switch" class="hui-switch" data-evo-sop-toggle="${escapeHTML(s.workflowId)}" aria-label="开启或关闭 ${escapeHTML(s.workflowName || s.workflowId)} 自动进化"${s.enabled ? ' checked' : ''}${busy}>
    </div>
    <button type="button" class="evo-sop-row-go" data-evo-sop-open="${escapeHTML(s.workflowId)}" aria-label="查看 ${escapeHTML(s.workflowName || s.workflowId)} 详情">${evoIcon('chevron')}</button>
  </div>`;
}

function evoSOPUpdateList() {
  const host = $('#evoSopListHost');
  if (!host) return;
  const sops = evo.overview?.sops || [];
  const counts = {
    all: sops.length,
    monitor: sops.filter(s => s.stage === 'monitor').length,
    optimize: sops.filter(s => s.stage === 'optimize').length,
    gray: sops.filter(s => s.stage === 'gray').length,
  };
  host.innerHTML = evoSOPListHTML(sops);
  $$('[data-evo-sop-filter]').forEach(btn => {
    const key = btn.dataset.evoSopFilter;
    btn.setAttribute('aria-pressed', String(key === evoSOPState.filter));
    const badge = btn.querySelector('span');
    if (badge && Object.prototype.hasOwnProperty.call(counts, key)) badge.textContent = String(counts[key]);
  });
  window.AgentFlowHarnessIcons?.hydrate(host);
}

// ---------- 偏好、周期与专属模型设置弹窗 ----------

function evoSOPPrefDialogHTML() {
  return `<dialog id="evoSopPrefDialog" class="evo-sop-dialog" aria-labelledby="evoSopPrefTitle">
    <form method="dialog" id="evoSopPrefForm" class="evo-sop-dialog-card">
      <header class="evo-sop-dialog-head">
        <div class="evo-sop-dialog-title-wrap">
          <h2 id="evoSopPrefTitle">进化偏好与优先级</h2>
          <p id="evoSopPrefSub">按优先级从高到低生效：冲突时优先保障 P1 主目标，支持拖拽或点 ↑↓ 调序。</p>
        </div>
        <button type="button" class="evo-sop-dialog-close" data-evo-sop-pref-close aria-label="关闭弹窗">
          <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8"/></svg>
        </button>
      </header>
      <div class="evo-sop-pref-presets-row">
        <span class="evo-sop-pref-presets-label">快捷预设</span>
        <div class="evo-sop-pref-presets" id="evoSopPrefPresets" role="group" aria-label="优化优先级预设"></div>
      </div>
      <div class="evo-sop-pref-list" id="evoSopPrefOptions"></div>
      <div class="evo-sop-batch-editor">
        <label for="evoSopBatchCustom">每轮汇总运行次数</label>
        <div class="evo-sop-batch-presets" id="evoSopBatchPresets"></div>
        <div class="evo-sop-batch-custom-row">
          <input id="evoSopBatchCustom" type="number" class="kb-input" min="1" max="100" step="1" value="10" aria-label="自定义每轮运行次数">
          <span>次运行后汇总数据并触发优化判断（1–100）</span>
        </div>
      </div>
      <div class="evo-sop-batch-editor">
        <div class="evo-sop-model-head">
          <label>分析与优化模型</label>
          <div class="evo-sop-seg" role="group" aria-label="模型选择方式">
            <button type="button" data-evo-sop-model-mode="default" aria-pressed="true">跟随默认</button>
            <button type="button" data-evo-sop-model-mode="custom" aria-pressed="false">单独指定</button>
          </div>
        </div>
        <p class="evo-sop-model-hint" id="evoSopModelDefaultHint">使用项目/个人默认模型进行分析与优化。</p>
        <div id="evoSopModelPickerHost" class="evo-sop-model-picker" hidden></div>
      </div>
      <footer class="evo-sop-dialog-foot">
        <button type="button" class="kb-btn quiet" data-evo-sop-pref-close>取消</button>
        <button type="submit" class="kb-btn primary" id="evoSopPrefSave">保存设置</button>
      </footer>
    </form>
  </dialog>`;
}

function evoSOPPrefPresetMatches(presetPrefs, currentPrefs) {
  if (!Array.isArray(currentPrefs) || presetPrefs.length !== currentPrefs.length) return false;
  return presetPrefs.every((k, idx) => currentPrefs[idx] === k);
}

function evoSOPRenderPrefOptions() {
  const target = evoSOPState.prefTarget;
  const opts = $('#evoSopPrefOptions');
  if (!target || !opts) return;
  const selected = (target.preferences || []).filter(k => !!EVO_SOP_PREF_MAP[k]);
  const selectedSet = new Set(selected);
  const unselected = EVO_SOP_PREFS.filter(p => !selectedSet.has(p.key));

  const presetsHost = $('#evoSopPrefPresets');
  if (presetsHost) {
    presetsHost.innerHTML = EVO_SOP_PREF_PRESETS.map(pr => {
      const active = evoSOPPrefPresetMatches(pr.prefs, selected);
      const chain = pr.prefs.map(k => EVO_SOP_PREF_MAP[k]?.short || k).join(' › ');
      return `<button type="button" class="evo-sop-pref-preset" data-evo-sop-pref-preset="${pr.id}" aria-pressed="${active}" title="${escapeHTML(chain)}">${escapeHTML(pr.label)} <small>${escapeHTML(chain)}</small></button>`;
    }).join('');
  }

  const selectedHTML = selected.length
    ? selected.map((key, idx) => {
        const p = EVO_SOP_PREF_MAP[key];
        const isP1 = idx === 0;
        return `<div class="evo-sop-pref-option is-checked${isP1 ? ' is-p1' : ''}" data-pref-key="${p.key}" draggable="true">
          <span class="evo-sop-pref-grip" title="拖拽调整优先级" aria-hidden="true">⋮⋮</span>
          <label class="evo-sop-pref-check">
            <input type="checkbox" value="${p.key}" checked>
            <span class="evo-sop-rank${isP1 ? ' is-p1' : ''}">${isP1 ? 'P1 主目标' : `P${idx + 1}`}</span>
            <span class="evo-sop-pref-copy"><b>${escapeHTML(p.label)}</b><small>${escapeHTML(p.desc)}</small></span>
          </label>
          <span class="evo-sop-pref-order">
            <button type="button" class="evo-sop-order-btn" data-evo-sop-pref-move="up" data-pref-key="${p.key}" title="上移优先级" aria-label="提高 ${escapeHTML(p.label)} 优先级"${idx === 0 ? ' disabled' : ''}>↑</button>
            <button type="button" class="evo-sop-order-btn" data-evo-sop-pref-move="down" data-pref-key="${p.key}" title="下移优先级" aria-label="降低 ${escapeHTML(p.label)} 优先级"${idx === selected.length - 1 ? ' disabled' : ''}>↓</button>
          </span>
        </div>`;
      }).join('')
    : '<div class="evo-sop-pref-empty">请至少勾选一项作为 P1 主目标</div>';

  const unselectedHTML = unselected.length
    ? `<div class="evo-sop-pref-divider">未启用（勾选后追加为下一优先级）</div>${unselected.map(p => `<div class="evo-sop-pref-option is-unselected" data-pref-key="${p.key}">
        <label class="evo-sop-pref-check">
          <input type="checkbox" value="${p.key}">
          <span class="evo-sop-pref-copy"><b>${escapeHTML(p.label)}</b><small>${escapeHTML(p.desc)}</small></span>
        </label>
      </div>`).join('')}`
    : '';

  opts.innerHTML = selectedHTML + unselectedHTML;
}

function evoSOPPrefToggle(key, checked) {
  const target = evoSOPState.prefTarget;
  if (!target || !EVO_SOP_PREF_MAP[key]) return;
  const list = (target.preferences || []).filter(k => k !== key && !!EVO_SOP_PREF_MAP[k]);
  if (checked) list.push(key);
  target.preferences = list;
  evoSOPRenderPrefOptions();
}

function evoSOPPrefMove(key, delta) {
  const target = evoSOPState.prefTarget;
  if (!target) return;
  const list = [...(target.preferences || [])];
  const idx = list.indexOf(key);
  const next = idx + delta;
  if (idx < 0 || next < 0 || next >= list.length) return;
  [list[idx], list[next]] = [list[next], list[idx]];
  target.preferences = list;
  evoSOPRenderPrefOptions();
}

function evoSOPPrefReorder(fromKey, toKey) {
  const target = evoSOPState.prefTarget;
  if (!target || !fromKey || !toKey || fromKey === toKey) return;
  const list = [...(target.preferences || [])];
  const fromIdx = list.indexOf(fromKey);
  const toIdx = list.indexOf(toKey);
  if (fromIdx < 0 || toIdx < 0) return;
  list.splice(fromIdx, 1);
  list.splice(toIdx, 0, fromKey);
  target.preferences = list;
  evoSOPRenderPrefOptions();
}

function evoSOPPrefApplyPreset(presetId) {
  const target = evoSOPState.prefTarget;
  const preset = EVO_SOP_PREF_PRESETS.find(p => p.id === presetId);
  if (!target || !preset) return;
  target.preferences = [...preset.prefs];
  evoSOPRenderPrefOptions();
}

function evoSOPSyncPrefModelUI(mode, savedModel = '') {
  const custom = mode === 'custom';
  if (evoSOPState.prefTarget) evoSOPState.prefTarget.modelMode = custom ? 'custom' : 'default';
  $$('[data-evo-sop-model-mode]').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.evoSopModelMode === (custom ? 'custom' : 'default'))));
  const hint = $('#evoSopModelDefaultHint');
  const host = $('#evoSopModelPickerHost');
  if (hint) hint.hidden = custom;
  if (!host) return;
  host.hidden = !custom;
  if (!custom) return;

  if (!host.dataset.bound) {
    host.dataset.bound = '1';
    if (window.AgentFlowAssistantModels?.featurePickerHTML && window.AgentFlowAssistantModels?.bindFeaturePicker) {
      host.innerHTML = window.AgentFlowAssistantModels.featurePickerHTML('sopModel');
      host.modelPickerReady = null;
      window.AgentFlowAssistantModels.bindFeaturePicker(host, {
        model: savedModel || '',
        keepSaved: true,
        onChange: id => {
          if (evoSOPState.prefTarget) evoSOPState.prefTarget.model = id || '';
        },
      });
    } else {
      host.innerHTML = `<input type="text" class="kb-input" data-feature-model name="sopModel" placeholder="输入模型 ID，如 claude-sonnet-4" value="${escapeHTML(savedModel || '')}">`;
    }
  }
}

function evoSOPOpenPrefDialog(workflowId) {
  const s = evoSOPFindSummary(workflowId);
  const dlg = $('#evoSopPrefDialog');
  if (!s || !dlg) return;
  const projectModel = evo.overview?.sopDetection?.model || '';
  const hasCustomModel = !!s.model && s.model !== projectModel;
  evoSOPState.prefTarget = {
    workflowId: s.workflowId,
    enabled: !!s.enabled,
    preferences: evoSOPNormalizePrefs(s.preferences),
    batchSize: Number(s.batchSize) || 10,
    model: hasCustomModel ? s.model : '',
    modelMode: hasCustomModel ? 'custom' : 'default',
  };
  $('#evoSopPrefTitle').textContent = `${s.workflowName || s.workflowId} · 优化优先级`;
  evoSOPRenderPrefOptions();
  const presets = $('#evoSopBatchPresets');
  presets.innerHTML = EVO_SOP_BATCH_PRESETS.map(n => `<button type="button" class="evo-sop-preset-btn" data-evo-sop-preset="${n}" aria-pressed="${evoSOPState.prefTarget.batchSize === n}">每 ${n} 次</button>`).join('');
  $('#evoSopBatchCustom').value = String(evoSOPState.prefTarget.batchSize);
  const host = $('#evoSopModelPickerHost');
  if (host) { delete host.dataset.bound; host.modelPickerReady = null; host.innerHTML = ''; }
  const hint = $('#evoSopModelDefaultHint');
  if (hint) hint.textContent = projectModel ? `跟随默认模型（当前：${projectModel}）。` : '跟随项目/个人默认模型进行分析与优化。';
  evoSOPSyncPrefModelUI(evoSOPState.prefTarget.modelMode, evoSOPState.prefTarget.model);
  if (typeof dlg.showModal === 'function') dlg.showModal();
  else dlg.setAttribute('open', '');
}

function evoSOPFindSummary(workflowId) {
  if (evoSOPState.view?.detail?.workflowId === workflowId) return evoSOPState.view.detail;
  return (evo.overview?.sops || []).find(x => x.workflowId === workflowId) || null;
}

async function evoSOPSavePolicy(workflowId, patch, control) {
  const pid = evoProjectId();
  if (!pid || evoSOPState.busyIds.has(workflowId)) return null;
  const cur = evoSOPFindSummary(workflowId) || { enabled: true, preferences: ['tokens'], batchSize: 10, model: '' };
  const body = {
    projectId: pid,
    enabled: patch.enabled !== undefined ? !!patch.enabled : !!cur.enabled,
    preferences: patch.preferences !== undefined ? patch.preferences : (cur.preferences || ['tokens']),
    batchSize: patch.batchSize !== undefined ? Number(patch.batchSize) : (Number(cur.batchSize) || 10),
    model: patch.model !== undefined ? patch.model : (cur.model || ''),
  };
  evoSOPState.busyIds.add(workflowId);
  if (control) control.disabled = true;
  try {
    const saved = await api(`/api/evolution/sops/${encodeURIComponent(workflowId)}/policy`, {
      method: 'PUT',
      body: JSON.stringify(body),
    });
    evoSOPState.busyIds.delete(workflowId);
    if (pid === evoProjectId()) {
      if (Array.isArray(evo.overview?.sops)) {
        const idx = evo.overview.sops.findIndex(x => x.workflowId === workflowId);
        if (idx >= 0) {
          evo.overview.sops[idx] = {
            ...evo.overview.sops[idx],
            enabled: saved.enabled,
            preferences: saved.preferences,
            batchSize: saved.batchSize,
            model: saved.model,
          };
        }
      }
      if (evoSOPState.view?.workflowId === workflowId) {
        await evoSOPLoadDetail(evoSOPState.view);
      } else {
        evoSOPUpdateList();
      }
    }
    toast('SOP 进化配置已更新', 'ok');
    return saved;
  } catch (err) {
    evoSOPState.busyIds.delete(workflowId);
    toast(err.message, 'err');
    if (pid === evoProjectId()) {
      if (evoSOPState.view?.workflowId === workflowId) evoSOPRenderDetail();
      else evoSOPUpdateList();
    }
    return null;
  } finally {
    evoSOPState.busyIds.delete(workflowId);
    if (control?.isConnected) control.disabled = false;
  }
}

// ---------- SOP 详情页 ----------

function evoCloseSOP(restore = false) {
  evoSOPState.view = null;
  if (restore && evoActive()) {
    if (history.state?.evolutionFrom === '#/evolution') history.back();
    else history.replaceState(null, '', '#/evolution');
    evoRender();
  }
}

function evoOpenSOP(workflowId, tab = 'trend', scope = 'versions', fromRoute = false) {
  if (!fromRoute) {
    const hash = `#/evolution/sops/${encodeURIComponent(workflowId)}${tab && tab !== 'trend' ? `/${encodeURIComponent(tab)}` : ''}`;
    return evoNavigate(hash);
  }
  evoCloseFinding(false);
  if (evoDrawerOwned()) evoDrawerClose();
  const prev = evoSOPState.view?.workflowId === workflowId ? evoSOPState.view : null;
  const view = {
    workflowId,
    tab: tab === 'change' ? 'change' : 'trend',
    scope: scope === 'gray' ? 'gray' : (prev?.scope || 'versions'),
    metric: prev?.metric || 'score',
    fileIndex: prev?.fileIndex || 0,
    diffMode: prev?.diffMode || 'split',
    expandedGray: prev?.expandedGray || null,
    detail: prev?.detail || null,
    loading: true,
    error: '',
    seq: (prev?.seq || 0) + 1,
  };
  evoSOPState.view = view;
  evoRenderOverviewHeader();
  evoSOPRenderDetail();
  evoSOPLoadDetail(view);
}

async function evoSOPLoadDetail(view) {
  const pid = evoProjectId();
  if (!pid || evoSOPState.view !== view) return;
  const seq = ++view.seq;
  view.loading = true;
  view.error = '';
  try {
    const detail = await api(`/api/evolution/sops/${encodeURIComponent(view.workflowId)}?projectId=${encodeURIComponent(pid)}`, { silent: true });
    if (evoSOPState.view !== view || view.seq !== seq || pid !== evoProjectId()) return;
    view.detail = detail;
    view.loading = false;
    if (view.fileIndex >= (detail.changeFiles?.length || 0)) view.fileIndex = 0;
    evoSOPRenderDetail();
  } catch (err) {
    if (evoSOPState.view !== view || view.seq !== seq || pid !== evoProjectId()) return;
    view.loading = false;
    view.error = err.message || '读取 SOP 详情失败';
    evoSOPRenderDetail();
  }
}

function evoSOPRenderDetail() {
  const root = $('#evolutionBody');
  const view = evoSOPState.view;
  if (!root || !view) return;
  root.innerHTML = evoSOPDetailHTML(view) + evoSOPPrefDialogHTML();
  window.AgentFlowHarnessIcons?.hydrate(root);
  if (view.detail && view.tab === 'trend') {
    evoSOPDrawTrendChart(view);
  }
}

function evoSOPDetailHTML(view) {
  const d = view.detail;
  const back = `<button type="button" class="evo-back" data-evo-sop-back>‹ 返回 SOP 列表</button>`;
  if (view.error) {
    return `<section id="evoSopDetailPage" class="evo-sop-detail">${back}<div class="evo-home-check-error" role="alert">读取 SOP 详情失败：${escapeHTML(view.error)} <button type="button" class="kb-btn small" data-evo-sop-reload>重试</button></div></section>`;
  }
  if (!d) {
    return `<section id="evoSopDetailPage" class="evo-sop-detail">${back}<div class="kb-empty">正在读取 SOP 详情…</div></section>`;
  }

  const busy = evoSOPState.busyIds.has(d.workflowId) ? ' disabled' : '';
  const active = d.activeProposal;
  const versions = d.versions || [];
  const curPoint = versions.find(v => v.current) || versions[versions.length - 1];
  const prevPoint = versions.length >= 2 ? versions[versions.length - 2] : null;
  const curM = (curPoint?.metrics?.runs > 0 ? curPoint.metrics : d.latestMetrics) || {};
  const prevM = prevPoint && prevPoint !== curPoint ? prevPoint.metrics : null;

  const normPrefs = evoSOPNormalizePrefs(d.preferences);
  const primaryPref = normPrefs[0];
  const primaryAsset = (d.assets || [])[0] || null;
  const headActions = [
    `<button type="button" class="kb-btn quiet small" data-evo-sop-pref-open="${escapeHTML(d.workflowId)}" title="${escapeHTML(evoSOPPrefsTitle(normPrefs))}"${busy}>偏好 · P1 ${escapeHTML(EVO_SOP_PREF_MAP[primaryPref]?.short || '省 Token')}</button>`,
  ];
  if (primaryAsset) {
    const verSuffix = primaryAsset.currentVersion ? ` · v${primaryAsset.currentVersion}` : '';
    headActions.push(`<button type="button" class="kb-btn quiet small" data-evo-asset="${escapeHTML(primaryAsset.kind)}" data-id="${escapeHTML(primaryAsset.id)}" title="查看历史版本对比并一键回滚到指定版本">版本与回滚${escapeHTML(verSuffix)}</button>`);
  }
  if (d.stage === 'monitor' && d.enabled) {
    headActions.push(`<button type="button" class="kb-btn small" data-evo-sop-optimize="${escapeHTML(d.workflowId)}"${busy}>立即发起优化</button>`);
  }
  if (active?.id) {
    headActions.push(`<button type="button" class="kb-btn quiet small" data-evo-proposal="${escapeHTML(active.id)}">查看完整提案</button>`);
  }

  return `<section id="evoSopDetailPage" class="evo-sop-detail" aria-labelledby="evoSopDetailTitle">
    ${back}
    <header class="evo-sop-detail-head">
      <div class="evo-sop-detail-id">
        <div class="evo-sop-detail-title-row">
          <h1 id="evoSopDetailTitle">${escapeHTML(d.workflowName || d.workflowId)}</h1>
          <span class="evo-sop-ver mono">${escapeHTML(d.currentVersion || 'v1')} 生效中</span>
          ${evoSOPStageBadge(d.stage)}
        </div>
        ${d.description ? `<p class="evo-sop-detail-desc">${escapeHTML(d.description)}</p>` : ''}
      </div>
      <div class="evo-sop-detail-actions">${headActions.join('')}</div>
    </header>

    ${evoSOPCalloutHTML(d)}
    ${evoSOPMetricsGridHTML(curM, prevM, primaryPref)}

    <div class="evo-sop-tabs" role="tablist" aria-label="SOP 详情视图">
      <button type="button" role="tab" data-evo-sop-tab="trend" aria-selected="${view.tab === 'trend'}">历史版本趋势</button>
      <button type="button" role="tab" data-evo-sop-tab="change" aria-selected="${view.tab === 'change'}">本次修改与问题${d.problems?.length ? `<span class="kb-count">${d.problems.length}</span>` : ''}</button>
    </div>

    <div class="evo-sop-tab-panel" role="tabpanel">
      ${view.tab === 'trend' ? evoSOPTrendTabHTML(view, d) : evoSOPChangeTabHTML(view, d)}
    </div>
  </section>`;
}

function evoSOPCalloutHTML(d) {
  if (!d.enabled) {
    return `<div class="evo-sop-callout is-muted" role="status">
      <div><strong>已关闭此 SOP 的自动进化</strong><p>仍会记录每次运行的消耗与评分，但不会自动生成新的优化提案。</p></div>
    </div>`;
  }
  const p = d.activeProposal;
  if (d.stage === 'optimize' && p) {
    if (p.status === 'drafting' || p.status === 'validating') {
      const s = p.summary || {};
      const detail = p.status === 'drafting'
        ? '正在结合本轮问题与优化偏好生成修改稿，完成后将自动回放历史样本验证。'
        : `正在回放历史运行验证修改稿（已完成 ${s.finished || 0}/${s.total || 0} 条）。`;
      return `<div class="evo-sop-callout is-info" role="status">
        <div><strong>正在优化中 · ${escapeHTML(p.title || '生成修改稿')}</strong><p>${escapeHTML(detail)}</p></div>
        <div class="evo-sop-callout-actions">
          <button type="button" class="kb-btn small" data-evo-proposal="${escapeHTML(p.id)}">查看验证进展</button>
        </div>
      </div>`;
    }
    if (p.status === 'pending') {
      const passed = !!p.summary?.passed && !!p.candidateContent;
      const msg = passed
        ? `已完成回放验证（${p.summary.verdict || `修复 ${p.summary.fixed || 0} 条，无退化`}）。开启灰度后将以 50% 流量对比新旧版本，发布后再进入下一轮统计。`
        : `回放结果尚需人工核对（${p.summary?.verdict || '未达自动通过条件'}），建议先查看修改与样本细节。`;
      return `<div class="evo-sop-callout ${passed ? 'is-ready' : 'is-warn'}" role="status">
        <div><strong>${passed ? '优化完成，是否开始灰度？' : '优化稿待核对'}</strong><p>${escapeHTML(msg)}</p></div>
        <div class="evo-sop-callout-actions">
          <button type="button" class="kb-btn quiet small" data-evo-sop-tab="change">看本次修改</button>
          ${passed ? `<button type="button" class="kb-btn primary small" data-evo-sop-start-gray="${escapeHTML(p.id)}">开始灰度</button>` : `<button type="button" class="kb-btn small" data-evo-proposal="${escapeHTML(p.id)}">核对样本</button>`}
        </div>
      </div>`;
    }
  }
  if (d.stage === 'gray' && p) {
    const c = p.canary || {};
    const latestGray = (d.grayRounds || [])[d.grayRounds.length - 1];
    const ctrlRuns = latestGray?.control?.runs ?? c.control?.runs ?? 0;
    const candRuns = latestGray?.candidate?.runs ?? c.canary?.runs ?? 0;
    const nextVer = latestGray?.target || `v${(parseInt(String(d.currentVersion || 'v1').replace(/^v/, ''), 10) || 1) + 1}`;
    const ready = c.status === 'passed';
    return `<div class="evo-sop-callout is-gray" role="status">
      <div>
        <strong>正在灰度验证（${c.percent || 50}% 流量 · 候选 ${escapeHTML(nextVer)}）</strong>
        <p>对照组 ${ctrlRuns} 次 · 候选组 ${candRuns} 次（目标每组 ≥${c.minRuns || 5} 次）${c.verdict ? ` · ${escapeHTML(c.verdict)}` : ''}。${ready ? '点击发布后生效并开启下一轮统计。' : '达到通过条件后可全量发布并开启下一轮统计。'}</p>
      </div>
      <div class="evo-sop-callout-actions">
        <button type="button" class="kb-btn quiet small" data-evo-sop-stop-gray="${escapeHTML(p.id)}">停止灰度</button>
        <button type="button" class="kb-btn primary small" data-evo-sop-publish="${escapeHTML(p.id)}" data-next-ver="${escapeHTML(nextVer)}" data-ready="${ready ? '1' : '0'}"${ready ? '' : ' disabled title="灰度尚未达到通过条件（每组需满足最低运行数且判定通过）"'}>发布 ${escapeHTML(nextVer)}</button>
      </div>
    </div>`;
  }
  if (d.stage === 'monitor') {
    const last = d.lastProposal;
    if (last?.status === 'failed') {
      const reason = last.error || last.summary?.verdict || '优化生成或回放未通过';
      return `<div class="evo-sop-callout is-warn" role="status">
        <div><strong>上次优化未通过</strong><p>${escapeHTML(reason)}</p></div>
        <div class="evo-sop-callout-actions">
          ${last.id ? `<button type="button" class="kb-btn quiet small" data-evo-proposal="${escapeHTML(last.id)}">查看失败详情</button>` : ''}
          <button type="button" class="kb-btn primary small" data-evo-sop-optimize="${escapeHTML(d.workflowId)}">重试优化</button>
        </div>
      </div>`;
    }
    const batchSize = Number(d.batchSize) || 10;
    const pending = Number(d.pendingRuns) || 0;
    const totalRuns = Number(d.latestMetrics?.runs) || 0;
    if (pending >= batchSize) {
      return `<div class="evo-sop-callout is-ready" role="status">
        <div><strong>本轮已达触发阈值（${pending}/${batchSize} 次）</strong><p>系统将在后台自动发起优化；若未自动启动，可点击右侧按钮立即触发并查看具体提示。</p></div>
        <div class="evo-sop-callout-actions">
          <button type="button" class="kb-btn primary small" data-evo-sop-optimize="${escapeHTML(d.workflowId)}">立即发起优化</button>
        </div>
      </div>`;
    }
    if (totalRuns >= batchSize && pending < batchSize) {
      return `<div class="evo-sop-callout is-muted" role="status">
        <div><strong>本轮待积累新运行（${pending}/${batchSize} 次）</strong><p>此前已有结束的提案，新运行累计达到 ${batchSize} 次后将自动进入优化，也可直接手动发起。</p></div>
      </div>`;
    }
  }
  return '';
}

function evoSOPMetricsGridHTML(cur, prev, primaryPref = '') {
  const runs = cur.runs || 0;
  const scoreNote = runs > 0
    ? (cur.scoreSource === 'eval' ? `近 ${cur.scoredRuns || runs} 次运行评测均分` : `结果稳定性 65 分 + Token/工具/耗时连续效率 35 分`)
    : '完成至少 1 次运行后自动计算';
  const cards = [
    {
      key: 'score',
      primary: true,
      label: 'SOP 评分',
      value: cur.score != null ? `${cur.score}` : '—',
      unit: cur.score != null ? '/ 100' : '',
      delta: evoSOPDeltaHTML(evoSOPMetricValue(cur, 'score'), evoSOPMetricValue(prev, 'score'), true, d => `${d > 0 ? '+' : ''}${Math.round(d)} 分`),
      note: scoreNote,
    },
    {
      key: 'tokens',
      label: '平均 Token',
      value: evoSOPFormatTokens(cur.avgTokens),
      delta: evoSOPDeltaHTML(evoSOPMetricValue(cur, 'tokens'), evoSOPMetricValue(prev, 'tokens'), false),
      note: runs > 0 ? `近 ${runs} 次总计 ${evoSOPFormatTokens(cur.totalTokens)} ÷ ${runs}` : '暂无 Token 记录',
    },
    {
      key: 'tools',
      label: '平均工具调用',
      value: runs > 0 ? `${evoSOPNum(cur.avgTools, 1)}` : '—',
      unit: runs > 0 ? '次' : '',
      delta: evoSOPDeltaHTML(evoSOPMetricValue(cur, 'tools'), evoSOPMetricValue(prev, 'tools'), false),
      note: runs > 0 ? `近 ${runs} 次累计 ${cur.totalTools || 0} 次 ÷ ${runs}` : '暂无工具调用记录',
    },
    {
      key: 'duration',
      label: '平均执行耗时',
      value: evoSOPFormatSec(cur.avgDuration),
      delta: evoSOPDeltaHTML(evoSOPMetricValue(cur, 'duration'), evoSOPMetricValue(prev, 'duration'), false),
      note: runs > 0 ? `近 ${runs} 次有效执行耗时均值` : '暂无耗时记录',
    },
    {
      key: 'clean',
      label: '一次成功率',
      value: runs > 0 ? `${Math.round((cur.cleanRate || 0) * 100)}%` : '—',
      delta: evoSOPDeltaHTML(evoSOPMetricValue(cur, 'clean'), evoSOPMetricValue(prev, 'clean'), true, d => `${d > 0 ? '+' : ''}${Math.round(d * 100)}pp`),
      note: runs > 0 ? `无返工一次成功 ${cur.cleanCount || 0}/${runs} 次` : '暂无终态运行记录',
    },
  ];
  return `<div class="evo-sop-kpi-grid" aria-label="核心指标">${cards.map(c => {
    const isP1Target = !!primaryPref && c.key === primaryPref;
    return `
    <article class="evo-sop-kpi${c.primary ? ' is-primary' : ''}${isP1Target ? ' is-p1-target' : ''}">
      <div class="evo-sop-kpi-top"><span class="evo-sop-kpi-label">${c.label}${isP1Target ? '<em class="evo-sop-kpi-p1">P1 主目标</em>' : ''}</span>${c.delta || ''}</div>
      <div class="evo-sop-kpi-val"><b>${escapeHTML(c.value)}</b>${c.unit ? `<small>${escapeHTML(c.unit)}</small>` : ''}</div>
      <p class="evo-sop-kpi-note">${escapeHTML(c.note)}</p>
    </article>`;
  }).join('')}</div>`;
}

// ---------- Tab 1: 历史版本趋势 + 近几次灰度数据 ----------

function evoSOPTrendTabHTML(view, d) {
  const grayCount = (d.grayRounds || []).length;
  const scope = view.scope === 'gray' ? 'gray' : 'versions';
  return `<div class="evo-sop-trend-wrap">
    <div class="evo-sop-trend-controls">
      <div class="evo-sop-seg" role="group" aria-label="趋势数据范围">
        <button type="button" data-evo-sop-scope="versions" aria-pressed="${scope === 'versions'}">历史正式版本</button>
        <button type="button" data-evo-sop-scope="gray" aria-pressed="${scope === 'gray'}">近几次灰度数据${grayCount ? `<span>${grayCount}</span>` : ''}</button>
      </div>
      <div class="evo-sop-seg" role="group" aria-label="选择趋势指标">
        ${EVO_SOP_METRICS.map(m => `<button type="button" data-evo-sop-metric="${m.key}" aria-pressed="${view.metric === m.key}">${m.label}</button>`).join('')}
      </div>
    </div>

    <div class="evo-sop-chart-box">
      <canvas id="evoSopTrendCanvas" height="220" aria-label="SOP 核心指标趋势图"></canvas>
      <div id="evoSopChartEmpty" class="evo-sop-chart-empty" hidden></div>
    </div>

    ${scope === 'versions' ? evoSOPVersionsTableHTML(d.versions || [], d.assets || []) : evoSOPGrayTableHTML(d.grayRounds || [], view.expandedGray)}

    <details class="evo-sop-method">
      <summary>指标与 SOP 评分是怎么算出来的？</summary>
      <ul>
        <li><b>SOP 评分（0–100）</b>：当运行存在自动评测结果（RunEval）时，直接取该批次评测均分；暂无评测时按连续公式加权：一次成功运行为结果稳定性 65 分（成功 40 + 无返工 15 + 工具无报错 10）+ 连续效率 35 分（Token 15 + 工具 10 + 耗时 10，平滑曲线计分）；返工成功按 0.6 倍计入效率分；失败运行不计效率加分（8–18 分），确保「失败 &lt; 返工成功 &lt; 一次成功」。</li>
        <li><b>核心指标口径</b>：所有统计仅包含非回放的终态业务运行；每个正式版本取该版本最近一轮（最多 N 次）运行的均值，灰度数据则按同一时间窗内的旧版对照组与候选版分流运行分别汇总。</li>
      </ul>
    </details>
  </div>`;
}

function evoSOPSourceBadge(source) {
  if (source === 'chatbot') return kbBadge('Chatbot 修改', 'info');
  if (source === 'manual') return kbBadge('手动修改', '');
  if (source === 'evolution') return kbBadge('自动进化', 'ok');
  if (source === 'rollback') return kbBadge('已回滚', 'warn');
  return '';
}

function evoSOPVersionsTableHTML(versions, assets = []) {
  if (!versions.length) return '<div class="kb-empty">暂无历史版本数据</div>';
  const primaryAsset = assets[0] || null;
  const assetByKey = Object.fromEntries((assets || []).map(a => [`${a.kind}:${a.id}`, a]));
  const hasActions = !!primaryAsset || versions.some(v => v.rollbackVersion > 0 && v.assetKind && v.assetId);
  const rows = [...versions].reverse().map(v => {
    const m = v.metrics || {};
    const srcBadge = evoSOPSourceBadge(v.source);
    let actionCell = '';
    if (hasActions) {
      let actionHTML = '<span class="evo-muted">—</span>';
      const targetAsset = v.assetKind && v.assetId ? assetByKey[`${v.assetKind}:${v.assetId}`] : null;
      const canDirectRollback = !v.current && v.rollbackVersion > 0 && v.assetKind && v.assetId && !targetAsset?.readOnly;
      if (canDirectRollback) {
        const assetLabel = v.assetName || targetAsset?.name || v.assetId;
        actionHTML = `<button type="button" class="kb-btn quiet small" data-evo-rollback="${v.rollbackVersion}" data-kind="${escapeHTML(v.assetKind)}" data-id="${escapeHTML(v.assetId)}" title="将「${escapeHTML(assetLabel)}」回滚到 v${v.rollbackVersion}">回滚「${escapeHTML(assetLabel)}」到 v${v.rollbackVersion}</button>`;
      } else if (primaryAsset) {
        actionHTML = `<button type="button" class="kb-btn quiet small" data-evo-asset="${escapeHTML(primaryAsset.kind)}" data-id="${escapeHTML(primaryAsset.id)}">${v.current || primaryAsset.readOnly ? '版本历史' : '选择版本回滚'}</button>`;
      }
      actionCell = `<td class="num">${actionHTML}</td>`;
    }
    return `<tr>
      <td><span class="mono">${escapeHTML(v.version)}</span>${v.current ? ` ${kbBadge('生效中', 'ok')}` : ''}${srcBadge ? ` ${srcBadge}` : ''}${v.changeNote ? `<small class="evo-sop-cell-sub">${escapeHTML(v.changeNote)}</small>` : ''}</td>
      <td class="num">${m.runs || 0}</td>
      <td class="num"><b>${m.score != null ? `${m.score} 分` : '—'}</b></td>
      <td class="num">${escapeHTML(evoSOPFormatTokens(m.avgTokens))}</td>
      <td class="num">${m.runs > 0 ? `${escapeHTML(evoSOPNum(m.avgTools, 1))} 次` : '—'}</td>
      <td class="num">${escapeHTML(evoSOPFormatSec(m.avgDuration))}</td>
      <td class="num">${m.runs > 0 ? `${Math.round((m.cleanRate || 0) * 100)}%` : '—'}</td>
      ${actionCell}
    </tr>`;
  }).join('');
  return `<div class="evo-sop-table-wrap"><table class="evo-sop-data-table">
    <thead><tr><th>版本</th><th class="num">统计运行数</th><th class="num">SOP 评分</th><th class="num">平均 Token</th><th class="num">平均工具调用</th><th class="num">平均耗时</th><th class="num">一次成功率</th>${hasActions ? '<th class="num">版本回滚</th>' : ''}</tr></thead>
    <tbody>${rows}</tbody>
  </table></div>`;
}

function evoSOPGrayTableHTML(rounds, expandedId) {
  if (!rounds.length) {
    return '<div class="kb-empty">暂无灰度轮次数据。优化完成后点击「开始灰度」，系统将在此持续记录旧版与候选版的对比数据。</div>';
  }
  const statusLabel = { running: ['灰度中', 'warn'], passed: ['通过待发布', 'ok'], published: ['已发布', 'ok'], failed: ['未通过', 'err'], aborted: ['已终止', ''] };
  const pair = (a, b, fmt) => `${escapeHTML(fmt(a))} → <b>${escapeHTML(fmt(b))}</b>`;
  const rows = [...rounds].reverse().map(r => {
    const [stLabel, stTone] = statusLabel[r.status] || [r.status || '灰度', ''];
    const open = expandedId === r.proposalId;
    const detailRow = open ? `<tr class="evo-sop-gray-detail-row"><td colspan="7">${evoSOPGrayRunsHTML(r.recentRuns || [])}</td></tr>` : '';
    return `<tr>
      <td><span class="mono">${escapeHTML(r.baseVersion)} → ${escapeHTML(r.target)}</span><small class="evo-sop-cell-sub">对照 ${r.control?.runs || 0} / 候选 ${r.candidate?.runs || 0} 次</small></td>
      <td>${kbBadge(escapeHTML(stLabel), stTone)}</td>
      <td class="num">${pair(r.control?.score, r.candidate?.score, v => v != null ? `${v}` : '—')}</td>
      <td class="num">${pair(r.control?.avgTokens, r.candidate?.avgTokens, evoSOPFormatTokens)}</td>
      <td class="num">${pair(r.control?.avgTools, r.candidate?.avgTools, v => v != null && v > 0 ? evoSOPNum(v, 1) : '—')}</td>
      <td class="num">${pair(r.control?.cleanRate, r.candidate?.cleanRate, v => Number.isFinite(v) ? `${Math.round(v * 100)}%` : '—')}</td>
      <td class="num"><button type="button" class="kb-btn quiet small" data-evo-sop-gray-expand="${escapeHTML(r.proposalId)}" aria-expanded="${open}">${open ? '收起明细' : `明细 (${(r.recentRuns || []).length})`}</button></td>
    </tr>${detailRow}`;
  }).join('');
  return `<div class="evo-sop-table-wrap"><table class="evo-sop-data-table">
    <thead><tr><th>灰度轮次（旧版 → 候选）</th><th>状态</th><th class="num">评分</th><th class="num">平均 Token</th><th class="num">工具调用</th><th class="num">一次成功率</th><th class="num">运行明细</th></tr></thead>
    <tbody>${rows}</tbody>
  </table></div>`;
}

function evoSOPGrayRunsHTML(runs) {
  if (!runs.length) return '<p class="evo-muted">暂无逐条灰度运行记录。</p>';
  return `<table class="evo-sop-sub-table">
    <thead><tr><th>运行 ID</th><th>分组</th><th class="num">Token</th><th class="num">工具调用</th><th class="num">耗时</th><th>一次成功</th><th class="num">评分</th></tr></thead>
    <tbody>${runs.map(r => `<tr>
      <td>${evoRunLink(r.runId, r.runId.slice(-12))}</td>
      <td>${kbBadge(r.group === 'candidate' ? '候选版' : '旧版对照', r.group === 'candidate' ? 'ok' : '')}</td>
      <td class="num">${escapeHTML(evoSOPFormatTokens(r.tokens))}</td>
      <td class="num">${r.tools || 0}</td>
      <td class="num">${escapeHTML(evoSOPFormatSec(r.duration))}</td>
      <td>${r.clean ? '是' : '否'}</td>
      <td class="num">${r.score != null ? r.score : '—'}</td>
    </tr>`).join('')}</tbody>
  </table>`;
}

// ---------- Canvas 趋势图（适配明暗主题 --hui-*） ----------

function evoSOPDrawTrendChart(view) {
  const canvas = $('#evoSopTrendCanvas');
  const empty = $('#evoSopChartEmpty');
  const d = view.detail;
  if (!canvas || !d || typeof canvas.getContext !== 'function') return;
  const ctx = canvas.getContext('2d');
  if (!ctx) return;

  const metricDef = EVO_SOP_METRICS.find(m => m.key === view.metric) || EVO_SOP_METRICS[0];
  const scope = view.scope === 'gray' ? 'gray' : 'versions';

  let labels = [];
  let series = [];
  if (scope === 'versions') {
    const pts = (d.versions || []).filter(v => evoSOPMetricValue(v.metrics, view.metric) != null);
    labels = pts.map(v => v.version);
    series = [{ name: metricDef.label, colorToken: '--hui-accent', values: pts.map(v => evoSOPMetricValue(v.metrics, view.metric)) }];
  } else {
    const rounds = (d.grayRounds || []).filter(r =>
      evoSOPMetricValue(r.control, view.metric) != null || evoSOPMetricValue(r.candidate, view.metric) != null
    );
    labels = rounds.map(r => `${r.baseVersion}→${r.target}`);
    series = [
      { name: '旧版对照', colorToken: '--hui-muted', dashed: true, values: rounds.map(r => evoSOPMetricValue(r.control, view.metric)) },
      { name: '候选版本', colorToken: '--hui-success', values: rounds.map(r => evoSOPMetricValue(r.candidate, view.metric)) },
    ];
  }

  const allVals = series.flatMap(s => s.values).filter(v => v != null && Number.isFinite(v));
  if (!labels.length || !allVals.length) {
    canvas.hidden = true;
    if (empty) {
      empty.hidden = false;
      empty.textContent = scope === 'gray' ? '暂无可绘制的灰度对比数据' : '暂无足够的历史运行数据以绘制趋势图';
    }
    return;
  }
  canvas.hidden = false;
  if (empty) empty.hidden = true;

  const style = getComputedStyle(document.documentElement);
  const tok = (name, fallback) => style.getPropertyValue(name).trim() || fallback;
  const cText = tok('--hui-muted', '#6f6f79');
  const cGrid = tok('--hui-border-soft', '#ecebef');
  const cSurface = tok('--hui-surface', '#ffffff');

  const cssW = Math.max(320, canvas.parentElement?.clientWidth || 640);
  const cssH = 220;
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  canvas.width = Math.round(cssW * dpr);
  canvas.height = Math.round(cssH * dpr);
  canvas.style.width = `${cssW}px`;
  canvas.style.height = `${cssH}px`;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, cssW, cssH);

  const pad = { top: 28, right: 24, bottom: 32, left: 56 };
  const plotW = Math.max(80, cssW - pad.left - pad.right);
  const plotH = Math.max(80, cssH - pad.top - pad.bottom);

  let minV = Math.min(...allVals);
  let maxV = Math.max(...allVals);
  if (view.metric === 'score') { minV = Math.max(0, Math.floor((minV - 10) / 10) * 10); maxV = 100; }
  else if (view.metric === 'clean') { minV = 0; maxV = 1; }
  else {
    const span = Math.max(maxV - minV, maxV * 0.25, 1);
    minV = Math.max(0, minV - span * 0.2);
    maxV = maxV + span * 0.2;
  }
  if (maxV <= minV) maxV = minV + 1;

  const xAt = i => labels.length === 1 ? pad.left + plotW / 2 : pad.left + (i / (labels.length - 1)) * plotW;
  const yAt = v => pad.top + (1 - (v - minV) / (maxV - minV)) * plotH;

  ctx.font = '11px Inter, system-ui, sans-serif';
  ctx.textAlign = 'right';
  ctx.textBaseline = 'middle';
  for (let i = 0; i <= 4; i++) {
    const val = minV + ((4 - i) / 4) * (maxV - minV);
    const y = pad.top + (i / 4) * plotH;
    ctx.strokeStyle = cGrid;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(pad.left, y);
    ctx.lineTo(cssW - pad.right, y);
    ctx.stroke();
    ctx.fillStyle = cText;
    ctx.fillText(metricDef.format(val), pad.left - 8, y);
  }

  ctx.textAlign = 'center';
  ctx.textBaseline = 'top';
  ctx.fillStyle = cText;
  labels.forEach((lbl, i) => ctx.fillText(lbl, xAt(i), cssH - pad.bottom + 10));

  if (series.length > 1) {
    let lx = pad.left;
    ctx.textAlign = 'left';
    ctx.textBaseline = 'middle';
    for (const s of series) {
      const col = tok(s.colorToken, '#7b61ff');
      ctx.fillStyle = col;
      ctx.fillRect(lx, 10, 12, 3);
      ctx.fillStyle = cText;
      ctx.fillText(s.name, lx + 16, 12);
      lx += ctx.measureText(s.name).width + 32;
    }
  }

  for (const s of series) {
    const col = tok(s.colorToken, '#7b61ff');
    const coords = s.values.map((v, i) => v != null ? [xAt(i), yAt(v), v] : null).filter(Boolean);
    if (!coords.length) continue;

    if (series.length === 1 && coords.length > 1) {
      ctx.save();
      ctx.beginPath();
      coords.forEach(([x, y], idx) => idx ? ctx.lineTo(x, y) : ctx.moveTo(x, y));
      ctx.lineTo(coords[coords.length - 1][0], pad.top + plotH);
      ctx.lineTo(coords[0][0], pad.top + plotH);
      ctx.closePath();
      ctx.globalAlpha = 0.10;
      ctx.fillStyle = col;
      ctx.fill();
      ctx.restore();
    }

    ctx.save();
    ctx.strokeStyle = col;
    ctx.lineWidth = 2;
    if (s.dashed) ctx.setLineDash([4, 4]);
    ctx.beginPath();
    coords.forEach(([x, y], idx) => idx ? ctx.lineTo(x, y) : ctx.moveTo(x, y));
    ctx.stroke();
    ctx.restore();

    ctx.textAlign = 'center';
    ctx.textBaseline = 'bottom';
    for (const [x, y, v] of coords) {
      ctx.beginPath();
      ctx.arc(x, y, 4, 0, Math.PI * 2);
      ctx.fillStyle = cSurface;
      ctx.fill();
      ctx.lineWidth = 2;
      ctx.strokeStyle = col;
      ctx.stroke();
      ctx.fillStyle = col;
      ctx.fillText(metricDef.format(v), x, y - 7);
    }
  }
}

// ---------- Tab 2: 本次修改与问题（默认左右对比 Diff） ----------

function evoSOPBuildSplitRows(lines) {
  const rows = [];
  let oldNo = 0;
  let newNo = 0;
  let i = 0;
  while (i < lines.length) {
    const [kind, text] = lines[i];
    if (kind === 'ctx') {
      oldNo++;
      newNo++;
      rows.push({
        left: { kind: 'ctx', no: oldNo, text },
        right: { kind: 'ctx', no: newNo, text },
      });
      i++;
      continue;
    }
    const dels = [];
    const adds = [];
    while (i < lines.length && lines[i][0] === 'del') {
      oldNo++;
      dels.push({ kind: 'del', no: oldNo, text: lines[i][1] });
      i++;
    }
    while (i < lines.length && lines[i][0] === 'add') {
      newNo++;
      adds.push({ kind: 'add', no: newNo, text: lines[i][1] });
      i++;
    }
    const count = Math.max(dels.length, adds.length);
    for (let k = 0; k < count; k++) {
      rows.push({
        left: dels[k] || { kind: 'empty', no: '', text: '' },
        right: adds[k] || { kind: 'empty', no: '', text: '' },
      });
    }
  }
  return rows;
}

function evoSOPSplitDiffHTML(lines) {
  const rows = evoSOPBuildSplitRows(lines || []);
  const cell = (c, side) => {
    const sign = c.kind === 'del' ? '−' : c.kind === 'add' ? '+' : ' ';
    return `<div class="evo-sop-split-cell is-${c.kind} is-${side}"><span class="evo-sop-split-no">${c.no}</span><span class="evo-sop-split-sign" aria-hidden="true">${sign}</span><code>${escapeHTML(c.text || ' ')}</code></div>`;
  };
  return `<div class="evo-sop-split-diff" role="group" aria-label="左右对比差异">
    <div class="evo-sop-split-head"><span>修改前（原版）</span><span>修改后（候选版）</span></div>
    <div class="evo-sop-split-body">${rows.map(r => `<div class="evo-sop-split-row">${cell(r.left, 'left')}${cell(r.right, 'right')}</div>`).join('')}</div>
  </div>`;
}

function evoSOPInlineDiffHTML(lines) {
  return `<div class="evo-diff evo-sop-compact-diff" role="group" aria-label="文件改动差异">
    ${(lines || []).map(([kind, line]) => {
      const sign = kind === 'add' ? '+' : kind === 'del' ? '−' : ' ';
      return `<div class="evo-diff-line ${escapeHTML(kind)}"><span class="evo-diff-sign" aria-hidden="true">${sign}</span><code>${escapeHTML(line || ' ')}</code></div>`;
    }).join('')}
  </div>`;
}

function evoSOPExternalEditsCardHTML(edits, editIndex, diffMode) {
  const idx = Math.min(Math.max(0, Number(editIndex) || 0), edits.length - 1);
  const active = edits[idx] || edits[0];
  if (!active) return '';
  const when = evoTime(active.createdAt) ? relativeTime(active.createdAt) : '';
  return `<div class="evo-sop-diff-card">
    <div class="evo-sop-file-bar" role="tablist" aria-label="外部修改记录">
      <div class="evo-sop-file-tabs">
        ${edits.map((ed, i) => `<button type="button" role="tab" class="evo-sop-file-tab" data-evo-sop-edit="${i}" aria-selected="${i === idx}">${evoSOPSourceBadge(ed.source)}<span class="mono">${escapeHTML(ed.diff?.path || ed.assetId)}</span><small>v${ed.version}</small></button>`).join('')}
      </div>
      <div class="evo-sop-file-actions">
        ${when ? `<span class="evo-sop-edit-time">${escapeHTML(when)}</span>` : ''}
        <div class="evo-sop-seg" role="group" aria-label="差异展示方式">
          <button type="button" data-evo-sop-diff-mode="split" aria-pressed="${diffMode === 'split'}">左右对比</button>
          <button type="button" data-evo-sop-diff-mode="inline" aria-pressed="${diffMode === 'inline'}">行内视图</button>
        </div>
      </div>
    </div>
    ${diffMode === 'split' ? evoSOPSplitDiffHTML(active.diff?.lines) : evoSOPInlineDiffHTML(active.diff?.lines)}
  </div>`;
}

function evoSOPChangeTabHTML(view, d) {
  const bm = d.batchMetrics || d.latestMetrics || {};
  const problems = d.problems || [];
  const files = d.changeFiles || [];
  const edits = d.recentEdits || [];
  const activeFile = files[view.fileIndex] || files[0] || null;
  const prop = d.activeProposal || d.lastProposal;
  const diffMode = view.diffMode === 'inline' ? 'inline' : 'split';

  const batchStrip = `<section class="evo-sop-batch-summary" aria-label="本批统计证据">
    <div><small>本批样本</small><b>${bm.runs || 0} 次</b></div>
    <div><small>总 Token 消耗</small><b>${escapeHTML(evoSOPFormatTokens(bm.totalTokens))}</b></div>
    <div><small>总工具调用</small><b>${bm.totalTools || 0} 次</b></div>
    <div><small>平均耗时</small><b>${escapeHTML(evoSOPFormatSec(bm.avgDuration))}</b></div>
    <div><small>一次成功率</small><b>${bm.runs > 0 ? `${Math.round((bm.cleanRate || 0) * 100)}%` : '—'}</b></div>
  </section>`;

  const normPrefs = evoSOPNormalizePrefs(d.preferences);
  const problemHTML = problems.length
    ? `<div class="evo-sop-problem-list">${problems.map(item => {
        const pref = EVO_SOP_PREF_MAP[item.key];
        const rankIdx = normPrefs.indexOf(item.key);
        const badgeText = pref ? (rankIdx >= 0 ? `P${rankIdx + 1} · ${pref.short}` : pref.short) : '';
        const badgeTone = rankIdx === 0 ? 'info' : '';
        return `<article class="evo-sop-problem-card">
          <div class="evo-sop-problem-head">
            ${badgeText ? kbBadge(escapeHTML(badgeText), badgeTone) : ''}
            <b>${escapeHTML(item.title)}</b>
            <span class="evo-sop-problem-hits">出现 ${item.hits || 1} 次${item.runs ? ` · ${item.runs} 次运行` : ''}</span>
          </div>
          ${item.detail ? `<p>${escapeHTML(item.detail)}</p>` : ''}
          ${item.fix ? `<p class="evo-sop-problem-fix">对应优化：${escapeHTML(item.fix)}</p>` : ''}
        </article>`;
      }).join('')}</div>`
    : '<div class="evo-sop-empty-card">本轮暂未扫描出重复步骤、工具反复调用或失败问题。</div>';

  const diffHTML = activeFile
    ? `<div class="evo-sop-diff-card"><div class="evo-sop-file-bar" role="tablist" aria-label="修改的文件">
        <div class="evo-sop-file-tabs">
          ${files.map((f, idx) => `<button type="button" role="tab" class="evo-sop-file-tab" data-evo-sop-file="${idx}" aria-selected="${idx === (view.fileIndex || 0)}"><span class="mono">${escapeHTML(f.path)}</span>${f.assetName ? `<small>${escapeHTML(f.assetName)}</small>` : ''}</button>`).join('')}
        </div>
        <div class="evo-sop-file-actions">
          <div class="evo-sop-seg" role="group" aria-label="差异展示方式">
            <button type="button" data-evo-sop-diff-mode="split" aria-pressed="${diffMode === 'split'}">左右对比</button>
            <button type="button" data-evo-sop-diff-mode="inline" aria-pressed="${diffMode === 'inline'}">行内视图</button>
          </div>
          ${prop?.id ? `<button type="button" class="kb-btn quiet small" data-evo-proposal="${escapeHTML(prop.id)}">查看完整提案 ↗</button>` : ''}
        </div>
      </div>
      ${diffMode === 'split' ? evoSOPSplitDiffHTML(activeFile.lines) : evoSOPInlineDiffHTML(activeFile.lines)}</div>`
    : edits.length
      ? evoSOPExternalEditsCardHTML(edits, view.editIndex, diffMode)
      : `<div class="evo-sop-empty-card">本轮尚未生成修改文件；当运行达到 ${Number(d.batchSize) || 10} 次、点击「立即发起优化」，或通过 Chatbot / 手动修改关联角色与规则后，这里会展示左右对比 Diff。</div>`;

  const extraExternalSection = activeFile && edits.length
    ? `<section class="evo-sop-section">
        <h3>外部修改记录（Chatbot / 手动）</h3>
        ${evoSOPExternalEditsCardHTML(edits, view.editIndex, diffMode)}
      </section>`
    : '';

  return `<div class="evo-sop-change-wrap">
    ${batchStrip}
    <section class="evo-sop-section">
      <h3>本轮发现的问题</h3>
      ${problemHTML}
    </section>
    <section class="evo-sop-section">
      <h3>${activeFile ? '本次修改的文件' : edits.length ? '最近修改的文件（Chatbot / 手动）' : '本次修改的文件'}</h3>
      ${diffHTML}
    </section>
    ${extraExternalSection}
  </div>`;
}

// ---------- SOP 详情操作 ----------

async function evoSOPStartGray(proposalId, btn) {
  const view = evoSOPState.view;
  if (!view || !proposalId) return;
  if (!await afConfirm({
    title: '开始灰度验证',
    message: '将以 50% 流量让新运行试用候选修改稿，其余 50% 保持当前生效版本作为对照。确认开始灰度？',
    confirmText: '开始灰度',
  })) return;
  if (btn) btn.disabled = true;
  try {
    await api(`/api/evolution/proposals/${encodeURIComponent(proposalId)}/approve`, {
      method: 'POST',
      body: JSON.stringify({ mode: 'canary', percent: 50, minRuns: 5 }),
    });
    toast('已进入灰度验证', 'ok');
    await evoRefresh();
    if (evoSOPState.view === view) await evoSOPLoadDetail(view);
  } catch (err) {
    toast(err.message, 'err');
    if (btn?.isConnected) btn.disabled = false;
  }
}

async function evoSOPStopGray(proposalId, btn) {
  const view = evoSOPState.view;
  if (!view || !proposalId) return;
  const note = await afPrompt({
    title: '停止灰度',
    message: '停止后新运行将全部恢复使用当前正式版本。原因（可选）：',
    defaultValue: '',
  });
  if (note === null || note === undefined) return;
  if (btn) btn.disabled = true;
  try {
    await api(`/api/evolution/proposals/${encodeURIComponent(proposalId)}/reject`, {
      method: 'POST',
      body: JSON.stringify({ note: String(note).trim() }),
    });
    toast('已停止灰度', 'ok');
    await evoRefresh();
    if (evoSOPState.view === view) await evoSOPLoadDetail(view);
  } catch (err) {
    toast(err.message, 'err');
    if (btn?.isConnected) btn.disabled = false;
  }
}

async function evoSOPPublishGray(proposalId, nextVer, ready, btn) {
  const view = evoSOPState.view;
  if (!view || !proposalId || !ready) return;
  const msg = `确认将候选方案全量发布为 ${nextVer}？发布后所有新运行将使用新版，并开启下一轮统计周期。`;
  if (!await afConfirm({ title: `发布 ${nextVer}`, message: msg, confirmText: `确认发布 ${nextVer}` })) return;
  if (btn) btn.disabled = true;
  try {
    await api(`/api/evolution/proposals/${encodeURIComponent(proposalId)}/approve`, {
      method: 'POST',
      body: JSON.stringify({ note: `灰度确认发布 ${nextVer}` }),
    });
    toast(`已发布 ${nextVer}，开启下一轮监测`, 'ok');
    await evoRefresh();
    if (evoSOPState.view === view) await evoSOPLoadDetail(view);
  } catch (err) {
    toast(err.message, 'err');
    if (btn?.isConnected) btn.disabled = false;
  }
}

async function evoSOPTriggerOptimize(workflowId, btn) {
  const view = evoSOPState.view;
  const pid = evoProjectId();
  if (!pid || !workflowId) return;
  if (btn) btn.disabled = true;
  try {
    await api(`/api/evolution/sops/${encodeURIComponent(workflowId)}/optimize`, {
      method: 'POST',
      body: JSON.stringify({ projectId: pid }),
    });
    toast('已发起 SOP 优化，正在生成修改稿并回放验证', 'ok');
    await evoRefresh();
    if (evoSOPState.view === view) await evoSOPLoadDetail(view);
  } catch (err) {
    toast(err.message, 'err');
    if (btn?.isConnected) btn.disabled = false;
  }
}

// ---------- 事件绑定 ----------

document.addEventListener('click', e => {
  if (!e.target.closest?.('#evolutionView')) return;

  const filterBtn = e.target.closest('[data-evo-sop-filter]');
  if (filterBtn) {
    evoSOPState.filter = filterBtn.dataset.evoSopFilter;
    evoSOPUpdateList();
    return;
  }
  if (e.target.closest('[data-evo-sop-reset]')) {
    evoSOPState.filter = 'all';
    evoSOPState.q = '';
    const input = $('[data-evo-sop-search]');
    if (input) input.value = '';
    evoSOPUpdateList();
    return;
  }
  const modeBtn = e.target.closest('[data-evo-sop-mode]');
  if (modeBtn) {
    evoSOPState.mode = modeBtn.dataset.evoSopMode === 'assets' ? 'assets' : 'sop';
    evoRender();
    return;
  }
  const prefOpen = e.target.closest('[data-evo-sop-pref-open]');
  if (prefOpen) {
    evoSOPOpenPrefDialog(prefOpen.dataset.evoSopPrefOpen);
    return;
  }
  if (e.target.closest('[data-evo-sop-pref-close]')) {
    const dlg = $('#evoSopPrefDialog');
    if (dlg) { if (typeof dlg.close === 'function') dlg.close(); else dlg.removeAttribute('open'); }
    return;
  }
  const prefPresetBtn = e.target.closest('[data-evo-sop-pref-preset]');
  if (prefPresetBtn) {
    evoSOPPrefApplyPreset(prefPresetBtn.dataset.evoSopPrefPreset);
    return;
  }
  const prefMoveBtn = e.target.closest('[data-evo-sop-pref-move]');
  if (prefMoveBtn) {
    evoSOPPrefMove(prefMoveBtn.dataset.prefKey, prefMoveBtn.dataset.evoSopPrefMove === 'up' ? -1 : 1);
    return;
  }
  const modelModeBtn = e.target.closest('[data-evo-sop-model-mode]');
  if (modelModeBtn) {
    evoSOPSyncPrefModelUI(modelModeBtn.dataset.evoSopModelMode, evoSOPState.prefTarget?.model || '');
    return;
  }
  const presetBtn = e.target.closest('[data-evo-sop-preset]');
  if (presetBtn) {
    const val = Number(presetBtn.dataset.evoSopPreset) || 10;
    const input = $('#evoSopBatchCustom');
    if (input) input.value = String(val);
    $$('[data-evo-sop-preset]').forEach(b => b.setAttribute('aria-pressed', String(Number(b.dataset.evoSopPreset) === val)));
    return;
  }
  const openBtn = e.target.closest('[data-evo-sop-open]');
  if (openBtn) {
    evoOpenSOP(openBtn.dataset.evoSopOpen);
    return;
  }
  if (e.target.closest('[data-evo-sop-back]')) {
    evoCloseSOP(true);
    return;
  }
  if (e.target.closest('[data-evo-sop-reload]') && evoSOPState.view) {
    evoSOPLoadDetail(evoSOPState.view);
    return;
  }
  const tabBtn = e.target.closest('[data-evo-sop-tab]');
  if (tabBtn && evoSOPState.view) {
    evoSOPState.view.tab = tabBtn.dataset.evoSopTab === 'change' ? 'change' : 'trend';
    evoSOPRenderDetail();
    return;
  }
  const scopeBtn = e.target.closest('[data-evo-sop-scope]');
  if (scopeBtn && evoSOPState.view) {
    evoSOPState.view.scope = scopeBtn.dataset.evoSopScope === 'gray' ? 'gray' : 'versions';
    evoSOPRenderDetail();
    return;
  }
  const metricBtn = e.target.closest('[data-evo-sop-metric]');
  if (metricBtn && evoSOPState.view) {
    evoSOPState.view.metric = metricBtn.dataset.evoSopMetric;
    evoSOPRenderDetail();
    return;
  }
  const fileBtn = e.target.closest('[data-evo-sop-file]');
  if (fileBtn && evoSOPState.view) {
    evoSOPState.view.fileIndex = Number(fileBtn.dataset.evoSopFile) || 0;
    evoSOPRenderDetail();
    return;
  }
  const editBtn = e.target.closest('[data-evo-sop-edit]');
  if (editBtn && evoSOPState.view) {
    evoSOPState.view.editIndex = Number(editBtn.dataset.evoSopEdit) || 0;
    evoSOPRenderDetail();
    return;
  }
  const diffModeBtn = e.target.closest('[data-evo-sop-diff-mode]');
  if (diffModeBtn && evoSOPState.view) {
    evoSOPState.view.diffMode = diffModeBtn.dataset.evoSopDiffMode === 'inline' ? 'inline' : 'split';
    evoSOPRenderDetail();
    return;
  }
  const grayExp = e.target.closest('[data-evo-sop-gray-expand]');
  if (grayExp && evoSOPState.view) {
    const id = grayExp.dataset.evoSopGrayExpand;
    evoSOPState.view.expandedGray = evoSOPState.view.expandedGray === id ? null : id;
    evoSOPRenderDetail();
    return;
  }
  const startGray = e.target.closest('[data-evo-sop-start-gray]');
  if (startGray) {
    evoSOPStartGray(startGray.dataset.evoSopStartGray, startGray);
    return;
  }
  const stopGray = e.target.closest('[data-evo-sop-stop-gray]');
  if (stopGray) {
    evoSOPStopGray(stopGray.dataset.evoSopStopGray, stopGray);
    return;
  }
  const pubGray = e.target.closest('[data-evo-sop-publish]');
  if (pubGray) {
    evoSOPPublishGray(pubGray.dataset.evoSopPublish, pubGray.dataset.nextVer || '', pubGray.dataset.ready === '1', pubGray);
    return;
  }
  const optBtn = e.target.closest('[data-evo-sop-optimize]');
  if (optBtn) {
    evoSOPTriggerOptimize(optBtn.dataset.evoSopOptimize, optBtn);
    return;
  }
});

document.addEventListener('input', e => {
  if (e.target.matches?.('#evolutionView [data-evo-sop-search]')) {
    evoSOPState.q = e.target.value;
    evoSOPUpdateList();
    return;
  }
  if (e.target.matches?.('#evoSopBatchCustom')) {
    const val = Number(e.target.value);
    $$('[data-evo-sop-preset]').forEach(b => b.setAttribute('aria-pressed', String(Number(b.dataset.evoSopPreset) === val)));
  }
});

document.addEventListener('change', e => {
  if (!e.target.closest?.('#evolutionView')) return;
  if (e.target.matches('[data-evo-sop-toggle]')) {
    evoSOPSavePolicy(e.target.dataset.evoSopToggle, { enabled: !!e.target.checked }, e.target);
    return;
  }
  if (e.target.matches('#evoSopPrefOptions input[type="checkbox"]')) {
    evoSOPPrefToggle(e.target.value, !!e.target.checked);
  }
});

let evoSOPDragPrefKey = '';
document.addEventListener('dragstart', e => {
  const row = e.target.closest?.('#evoSopPrefOptions .evo-sop-pref-option[draggable="true"]');
  if (!row) return;
  evoSOPDragPrefKey = row.dataset.prefKey || '';
  row.classList.add('is-dragging');
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', evoSOPDragPrefKey);
  }
});
document.addEventListener('dragover', e => {
  const row = e.target.closest?.('#evoSopPrefOptions .evo-sop-pref-option[draggable="true"]');
  if (!row || !evoSOPDragPrefKey) return;
  e.preventDefault();
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
  $$('#evoSopPrefOptions .evo-sop-pref-option.is-drag-over').forEach(el => {
    if (el !== row) el.classList.remove('is-drag-over');
  });
  if (row.dataset.prefKey !== evoSOPDragPrefKey) row.classList.add('is-drag-over');
});
document.addEventListener('dragleave', e => {
  const row = e.target.closest?.('#evoSopPrefOptions .evo-sop-pref-option.is-drag-over');
  if (row && !row.contains(e.relatedTarget)) row.classList.remove('is-drag-over');
});
document.addEventListener('drop', e => {
  const row = e.target.closest?.('#evoSopPrefOptions .evo-sop-pref-option[draggable="true"]');
  if (!row || !evoSOPDragPrefKey) return;
  e.preventDefault();
  const toKey = row.dataset.prefKey || '';
  const fromKey = evoSOPDragPrefKey;
  evoSOPDragPrefKey = '';
  if (fromKey && toKey && fromKey !== toKey) evoSOPPrefReorder(fromKey, toKey);
});
document.addEventListener('dragend', () => {
  evoSOPDragPrefKey = '';
  $$('#evoSopPrefOptions .evo-sop-pref-option').forEach(el => el.classList.remove('is-dragging', 'is-drag-over'));
});

document.addEventListener('submit', async e => {
  if (e.target?.id !== 'evoSopPrefForm') return;
  e.preventDefault();
  const target = evoSOPState.prefTarget;
  if (!target) return;
  const prefs = (target.preferences || []).filter(k => !!EVO_SOP_PREF_MAP[k]);
  if (!prefs.length) {
    toast('请至少选择一项作为 P1 主目标', 'warn');
    return;
  }
  const batchSize = Math.max(1, Math.min(100, Math.round(Number($('#evoSopBatchCustom')?.value) || 10)));
  let model = '';
  if (target.modelMode === 'custom') {
    model = String($('#evoSopModelPickerHost [data-feature-model]')?.value || target.model || '').trim();
  }
  const saveBtn = $('#evoSopPrefSave');
  const saved = await evoSOPSavePolicy(target.workflowId, { preferences: prefs, batchSize, model }, saveBtn);
  if (saved) {
    const dlg = $('#evoSopPrefDialog');
    if (dlg) { if (typeof dlg.close === 'function') dlg.close(); else dlg.removeAttribute('open'); }
  }
});

if (typeof window !== 'undefined' && typeof MutationObserver !== 'undefined') {
  window.addEventListener('resize', () => {
    if (evoSOPState.view?.detail && evoSOPState.view.tab === 'trend') evoSOPDrawTrendChart(evoSOPState.view);
  });
  new MutationObserver(() => {
    if (evoSOPState.view?.detail && evoSOPState.view.tab === 'trend') evoSOPDrawTrendChart(evoSOPState.view);
  }).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
}
