// Run 清理与保留：列出超过保留期的已结束运行、展示 workspace 占用与磁盘剩余，
// 确认后批量调用 POST /api/run-cleanup 删除（平台会回收工作区/沙箱/产物）。

const RUN_CLEANUP_DEFAULT_DAYS = 30;
const RUN_CLEANUP_DAY_OPTIONS = [7, 14, 30, 90];
const runCleanupState = {
  view: null,
  days: RUN_CLEANUP_DEFAULT_DAYS,
  loading: false,
  selected: new Set(),
  hint: null,
  hintLoaded: false,
  bound: false,
};

function formatRunCleanupBytes(bytes) {
  const value = Math.max(0, Number(bytes) || 0);
  if (value >= 1024 * 1024 * 1024) return `${(value / (1024 * 1024 * 1024)).toFixed(1)} GB`;
  if (value >= 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MB`;
  if (value >= 1024) return `${(value / 1024).toFixed(0)} KB`;
  return `${value} B`;
}

function runCleanupProjectID() {
  try {
    return currentProject()?.id || '';
  } catch {
    return '';
  }
}

function runCleanupCandidateRowsHTML(view, selected) {
  const candidates = view?.candidates || [];
  if (!candidates.length) {
    return '<div class="run-cleanup-empty">没有超过保留期的已结束运行，暂时不用清理。</div>';
  }
  return candidates.map((candidate) => {
    const checked = selected && selected.has(candidate.id) ? ' checked' : '';
    const title = candidate.input || candidate.workflowName || candidate.id;
    const meta = [candidate.workflowName, candidate.status, (candidate.createdAt || '').replace('T', ' ').slice(0, 16)].filter(Boolean).join(' · ');
    return `<label class="run-cleanup-candidate">
      <input type="checkbox" data-run-cleanup-id="${escapeHTML(candidate.id)}"${checked}>
      <span class="run-cleanup-title" title="${escapeHTML(title)}">${escapeHTML(title)}</span>
      <span class="run-cleanup-meta" title="${escapeHTML(meta)}">${escapeHTML(meta)}</span>
      <span class="run-cleanup-size">${formatRunCleanupBytes(candidate.workspaceBytes)}</span>
    </label>`;
  }).join('');
}

function runCleanupPanelHTML(state) {
  const view = state?.view;
  const totals = view?.totals || {};
  const selected = state?.selected || new Set();
  const days = Number(state?.days) || RUN_CLEANUP_DEFAULT_DAYS;
  const dayOptions = RUN_CLEANUP_DAY_OPTIONS.map((option) => `<option value="${option}"${option === days ? ' selected' : ''}>${option} 天</option>`).join('');
  const selectedCount = selected.size || 0;
  const selectedBytes = (view?.candidates || []).reduce((sum, candidate) => sum + (selected.has(candidate.id) ? Number(candidate.workspaceBytes || 0) : 0), 0);
  const metricCells = [
    ['超过保留期', `${totals.candidateCount || 0} 条运行`],
    ['workspace 占用', formatRunCleanupBytes(totals.workspaceBytes)],
    ['磁盘剩余', totals.diskTotalBytes ? `${formatRunCleanupBytes(totals.diskFreeBytes)} / ${formatRunCleanupBytes(totals.diskTotalBytes)}` : '—'],
    ['已选', `${selectedCount} 条 · ${formatRunCleanupBytes(selectedBytes)}`],
  ];
  return `<header class="run-cleanup-head">
      <div><b>清理旧运行</b><span>删除超过保留期的已结束运行，并回收对应工作区、沙箱与产物。</span></div>
      <div class="run-cleanup-head-actions">
        <label class="run-cleanup-days">保留期<select data-run-cleanup-days>${dayOptions}</select></label>
        <button type="button" class="button ghost small" data-run-cleanup-close>关闭</button>
      </div>
    </header>
    <div class="run-cleanup-metrics">${metricCells.map(([label, value]) => `<div class="run-cleanup-metric"><span>${escapeHTML(label)}</span><b>${escapeHTML(value)}</b></div>`).join('')}</div>
    <div class="run-cleanup-toolbar">
      <button type="button" class="button ghost small" data-run-cleanup-select-all>${selectedCount === (view?.candidates || []).length && selectedCount > 0 ? '取消全选' : '全选'}</button>
      <button type="button" class="button danger-outline small" data-run-cleanup-delete${selectedCount ? '' : ' disabled'}>删除选中（${selectedCount}）</button>
    </div>
    <div class="run-cleanup-list">${runCleanupCandidateRowsHTML(view, selected)}</div>
    <p class="run-cleanup-note">删除不可恢复；保留期只用于筛选，不会自动删除。运行中/待审批的节点不会被列出。</p>`;
}

function runCleanupHintHTML(view) {
  const totals = view?.totals || {};
  const count = Number(totals.candidateCount || 0);
  if (!count) return '';
  return `<span class="run-cleanup-hint-text">有 <b>${count}</b> 个超过 ${Number(totals.retentionDays) || RUN_CLEANUP_DEFAULT_DAYS} 天的已结束运行，占用 <b>${formatRunCleanupBytes(totals.workspaceBytes)}</b></span><button type="button" class="button ghost small" data-open-cleanup>清理旧运行</button>`;
}

function renderRunCleanupHint() {
  const host = document.querySelector('#runCleanupHint');
  if (!host) return;
  const html = runCleanupHintHTML(runCleanupState.hint);
  host.hidden = !html;
  host.innerHTML = html;
}

async function loadRunCleanupHint(force = false) {
  if (runCleanupState.hintLoaded && !force) return;
  try {
    const view = await api(`/api/run-cleanup/candidates?olderThanDays=${RUN_CLEANUP_DEFAULT_DAYS}&limit=1&projectId=${encodeURIComponent(runCleanupProjectID())}`, { silent: true });
    runCleanupState.hint = view;
    runCleanupState.hintLoaded = true;
    renderRunCleanupHint();
  } catch {
    // Older platforms do not expose the cleanup API; keep the run list unaffected.
  }
}

function renderRunCleanupPanel() {
  const host = document.querySelector('#runCleanupPanel');
  if (!host) return;
  host.hidden = !runCleanupState.view;
  if (!runCleanupState.view) return;
  host.innerHTML = runCleanupPanelHTML(runCleanupState);
}

async function reloadRunCleanupCandidates() {
  const host = document.querySelector('#runCleanupPanel');
  if (!host) return;
  runCleanupState.loading = true;
  host.hidden = false;
  host.innerHTML = `<div class="run-cleanup-loading">正在统计可清理的运行…</div>`;
  try {
    const view = await api(`/api/run-cleanup/candidates?olderThanDays=${runCleanupState.days}&limit=200&projectId=${encodeURIComponent(runCleanupProjectID())}`, { silent: true });
    runCleanupState.view = view;
    const available = new Set((view.candidates || []).map((candidate) => candidate.id));
    runCleanupState.selected = new Set([...runCleanupState.selected].filter((id) => available.has(id)));
  } catch (error) {
    host.innerHTML = `<div class="run-cleanup-loading is-error">清理检查失败：${escapeHTML(error?.message || '未知错误')}</div>`;
    runCleanupState.loading = false;
    return;
  }
  runCleanupState.loading = false;
  renderRunCleanupPanel();
}

async function openRunCleanupPanel() {
  runCleanupState.selected = new Set();
  runCleanupState.hintLoaded = false;
  await reloadRunCleanupCandidates();
  document.querySelector('#runCleanupPanel')?.scrollIntoView({ block: 'nearest' });
}

function closeRunCleanupPanel() {
  runCleanupState.view = null;
  runCleanupState.selected = new Set();
  renderRunCleanupPanel();
}

async function deleteSelectedRunsCleanup() {
  const ids = [...runCleanupState.selected];
  if (!ids.length || runCleanupState.loading) return;
  const bytes = (runCleanupState.view?.candidates || []).reduce((sum, candidate) => sum + (runCleanupState.selected.has(candidate.id) ? Number(candidate.workspaceBytes || 0) : 0), 0);
  const ok = await afConfirm({
    title: '删除运行记录',
    message: `确定删除选中的 ${ids.length} 条运行？其节点状态、日志、产物、执行工作区和沙箱会一并清理（约 ${formatRunCleanupBytes(bytes)}），且不可恢复。`,
    danger: true,
    confirmText: '删除',
  });
  if (!ok) return;
  runCleanupState.loading = true;
  try {
    const result = await api('/api/run-cleanup', { method: 'POST', body: JSON.stringify({ ids }) });
    const deleted = result?.deleted?.length || 0;
    const failed = result?.failed?.length || 0;
    toast(failed ? `已删除 ${deleted} 条，${failed} 条未能删除` : `已删除 ${deleted} 条运行，释放约 ${formatRunCleanupBytes(result?.freedBytes)}`, failed ? 'warn' : 'ok');
    runCleanupState.selected = new Set();
    await Promise.all([refreshRuns(), refreshIssues()]);
    await reloadRunCleanupCandidates();
    runCleanupState.hintLoaded = false;
    await loadRunCleanupHint(true);
  } catch (error) {
    toast(error?.message || '删除失败', 'err');
  } finally {
    runCleanupState.loading = false;
  }
}

function bindRunCleanupControls() {
  if (runCleanupState.bound) return;
  runCleanupState.bound = true;
  document.addEventListener('click', (event) => {
    const open = event.target.closest('[data-open-cleanup]');
    if (open) {
      event.preventDefault();
      openRunCleanupPanel();
      return;
    }
    const panel = document.querySelector('#runCleanupPanel');
    if (!panel || panel.hidden || !panel.contains(event.target)) return;
    if (event.target.closest('[data-run-cleanup-close]')) return closeRunCleanupPanel();
    if (event.target.closest('[data-run-cleanup-select-all]')) {
      const candidates = runCleanupState.view?.candidates || [];
      const allSelected = runCleanupState.selected.size === candidates.length && candidates.length > 0;
      runCleanupState.selected = allSelected ? new Set() : new Set(candidates.map((candidate) => candidate.id));
      return renderRunCleanupPanel();
    }
    if (event.target.closest('[data-run-cleanup-delete]')) return deleteSelectedRunsCleanup();
  });
  document.addEventListener('change', (event) => {
    const daySelect = event.target.closest('[data-run-cleanup-days]');
    if (daySelect) {
      runCleanupState.days = Number(daySelect.value) || RUN_CLEANUP_DEFAULT_DAYS;
      reloadRunCleanupCandidates();
      return;
    }
    const checkbox = event.target.closest('[data-run-cleanup-id]');
    if (checkbox) {
      const id = checkbox.dataset.runCleanupId;
      if (checkbox.checked) runCleanupState.selected.add(id);
      else runCleanupState.selected.delete(id);
      renderRunCleanupPanel();
    }
  });
}
