let roomsPollTimer = 0;

function roomProjectId() {
  return state.projectFilter || '';
}

function roomWorkflowChoices() {
  return (state.data?.workflows || []).filter(w => catalogGeneric(w)
    ? w.status !== 'draft'
    : w.status === 'published' && (catalogOwned(w.projectId) || catalogShared(w.projectId, w.shared)));
}

function roomWorkflowLabel(w) {
  return (w.name || w.id) + (catalogGeneric(w) ? '（通用）' : '');
}

function activeRoom() {
  const rs = roomState();
  return (rs.items || []).find(item => item.id === rs.activeId) || null;
}

function roomRepoChoices() {
  const projectId = roomProjectId();
  return (state.data?.repositories || []).filter(r => r.projectId === projectId);
}

function roomSkillChoices() {
  return (state.data?.skills || []).filter(s => catalogVisible(s.projectId, s.shared));
}

function roomGreeting() {
  const hour = new Date().getHours();
  const part = hour < 6 ? '凌晨好' : hour < 11 ? '早上好' : hour < 14 ? '中午好' : hour < 18 ? '下午好' : '晚上好';
  const user = state.currentUser;
  const name = user && !user.isGuest ? (user.chineseName || user.loginName || '') : '';
  return name ? part + '，' + name : part;
}

function roomState() {
  if (!state.rooms) state.rooms = {items: [], activeId: '', sops: [], active: false};
  if (!Array.isArray(state.rooms.sops)) state.rooms.sops = [];
  return state.rooms;
}

function roomRunTerminal(status) {
  return status === 'succeeded' || status === 'failed' || status === 'cancelled';
}

function bindRooms() {
  const create = $('#newRoom');
  const form = $('#roomComposer');
  if (!create || create.dataset.bound) return;
  create.dataset.bound = '1';
  create.onclick = createRoom;
  form.onsubmit = event => {
    event.preventDefault();
    submitRoomComposer();
  };
  $('#roomDispatch').onclick = () => {
    if (roomTargetPicked()) dispatchRoomIssue();
    else openRoomMention({targetsOnly: true});
  };
  const input = $('#roomInput');
  input.addEventListener('keydown', event => {
    if (roomMentionKey(event)) return;
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      submitRoomComposer();
    }
  });
  input.addEventListener('input', () => {
    syncRoomComposer();
    updateRoomMention();
  });
  input.addEventListener('click', updateRoomMention);
  document.addEventListener('mousedown', event => {
    if (!event.target.closest?.('.room-composer-box')) closeRoomMention();
  });
  $('#roomSOP').addEventListener('change', event => {
    const id = event.target.value;
    if (id) toggleRoomSOP(id, true);
    event.target.value = '';
  });
  $('#roomRole').addEventListener('change', syncRoomComposer);
  document.addEventListener('change', event => {
    if (event.target?.id === 'projectSwitcher' && state.currentView === 'rooms') loadRooms();
  });
}

function toggleRoomSOP(id, force) {
  const rs = roomState();
  const has = rs.sops.includes(id);
  const want = force === undefined ? !has : force;
  if (want && !has) {
    if (rs.sops.length >= 5) return toast('一次最多派给 5 条 SOP');
    rs.sops.push(id);
  }
  if (!want && has) rs.sops = rs.sops.filter(item => item !== id);
  syncRoomComposer();
}

function roomTargetPicked() {
  return roomState().sops.length > 0 || !!$('#roomRole')?.value;
}

function syncRoomComposer() {
  const rs = roomState();
  const role = $('#roomRole');
  const names = new Map(roomWorkflowChoices().map(w => [w.id, roomWorkflowLabel(w)]));
  rs.sops = rs.sops.filter(id => names.has(id));
  const picks = $('#roomSOPPicks');
  if (picks) {
    picks.innerHTML = rs.sops.map(id => `<button type="button" class="room-pick" data-unpick="${escapeHTML(id)}" title="移除">${escapeHTML(names.get(id))}<i>×</i></button>`).join('');
    picks.querySelectorAll('[data-unpick]').forEach(button => { button.onclick = () => toggleRoomSOP(button.dataset.unpick, false); });
  }
  $('#roomSOP')?.closest('.room-chip')?.classList.toggle('on', rs.sops.length > 0);
  role?.closest('.room-chip')?.classList.toggle('on', !!role.value);
  const picked = roomTargetPicked();
  const dispatch = $('#roomDispatch');
  if (dispatch) {
    dispatch.hidden = false;
    dispatch.textContent = !picked ? '建 Issue' : rs.sops.length > 1 ? `建单并派发 ${rs.sops.length} 条 SOP` : '建单并派发';
    dispatch.title = picked ? '第一行作为 Issue 标题，其余作为描述' : '先选派给哪个角色或 SOP';
  }
  const hint = $('#roomTargetHint');
  if (hint) {
    const parts = [];
    if (role?.value) parts.push('角色「' + role.selectedOptions[0].textContent + '」');
    if (rs.sops.length) parts.push(rs.sops.length > 1 ? rs.sops.length + ' 条 SOP 并行' : 'SOP「' + names.get(rs.sops[0]) + '」');
    hint.textContent = parts.length ? '· 回车将建单派给 ' + parts.join(' + ') : '· 输入 @ 选角色或 SOP 就能建 Issue';
  }
  $('#roomSend')?.classList.toggle('ready', !!$('#roomInput')?.value.trim());
  document.querySelectorAll('[data-room-pick]').forEach(button => {
    const [kind, id] = button.dataset.roomPick.split(':');
    const on = kind === 'sop' ? rs.sops.includes(id) : role?.value === id;
    button.classList.toggle('picked', !!id && on);
  });
}

async function loadRooms() {
  bindRooms();
  const rs = roomState();
  const projectId = roomProjectId();
  const project = (state.data?.projects || []).find(item => item.id === projectId);
  const name = $('#roomsProjectName');
  if (name) name.textContent = project?.name || '未选择项目';
  fillRoomTargets();
  if (!projectId) {
    renderRoomList([]);
    renderRoomThread(null, []);
    renderRoomResources(null);
    return;
  }
  try {
    const rooms = await api('/api/rooms?projectId=' + encodeURIComponent(projectId), {silent: true});
    rs.items = rooms || [];
    if (!rs.items.some(room => room.id === rs.activeId)) rs.activeId = rs.items[0]?.id || '';
    renderRoomList(rs.items);
    await openRoom(rs.activeId);
  } catch (error) {
    toast(error.message);
  }
}

// Poll faster while a dispatched run is still moving so progress feels live.
// Idle rooms back off to 15s; the server only reloads runs whose revision moved.
function scheduleRoomPoll() {
  clearTimeout(roomsPollTimer);
  const delay = roomState().active ? 4000 : 15000;
  roomsPollTimer = setTimeout(async () => {
    if (state.currentView === 'rooms' && roomState().activeId && !document.hidden) await refreshRoomMessages();
    if (state.currentView === 'rooms') scheduleRoomPoll();
  }, delay);
}

function fillRoomTargets() {
  const sop = $('#roomSOP');
  const role = $('#roomRole');
  if (!sop || !role) return;
  const roleValue = role.value;
  sop.innerHTML = '<option value="">添加 SOP…</option>' + roomWorkflowChoices().map(item => `<option value="${escapeHTML(item.id)}">${escapeHTML(roomWorkflowLabel(item))}</option>`).join('');
  role.innerHTML = '<option value="">不指定</option>' + catalogAgents().map(item => `<option value="${escapeHTML(item.id)}">${escapeHTML(item.name)}</option>`).join('');
  if ([...role.options].some(option => option.value === roleValue)) role.value = roleValue;
  syncRoomComposer();
}

function renderRoomList(rooms) {
  const host = $('#roomList');
  const count = $('#roomCount');
  if (count) count.textContent = String(rooms.length);
  if (!host) return;
  if (!rooms.length) {
    host.innerHTML = '<div class="room-empty"><b>还没有群</b><p>点右上角 ＋ 建一个群</p></div>';
    return;
  }
  host.innerHTML = rooms.map(room => `<button type="button" class="room-item ${room.id === roomState().activeId ? 'active' : ''}" data-room="${escapeHTML(room.id)}"><i>#</i><span>${escapeHTML(room.name)}</span><small title="群成员（即项目成员）">${(room.members || []).length} 人</small></button>`).join('');
  host.querySelectorAll('[data-room]').forEach(button => {
    button.onclick = () => openRoom(button.dataset.room);
  });
}

async function openRoom(id, keepScroll) {
  const rs = roomState();
  rs.activeId = id || '';
  renderRoomList(rs.items || []);
  const room = (rs.items || []).find(item => item.id === id);
  renderRoomResources(room);
  const title = $('#roomTitle');
  const scope = $('#roomScopeName');
  if (title) title.textContent = room?.name || '选择一个群';
  if (scope) scope.textContent = room?.name || '未选择群';
  if (!id) {
    renderRoomThread(null, []);
    return;
  }
  try {
    const messages = await api('/api/rooms/' + encodeURIComponent(id) + '/messages', {silent: true});
    renderRoomThread(room, messages || [], keepScroll);
  } catch (error) {
    toast(error.message);
  }
  scheduleRoomPoll();
}

if (!window.__roomsVisibilityBound) {
  window.__roomsVisibilityBound = true;
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && state.currentView === 'rooms' && roomState().activeId) {
      refreshRoomMessages().then(scheduleRoomPoll);
    }
  });
}

async function refreshRoomMessages() {
  const rs = roomState();
  const id = rs.activeId;
  const log = $('#roomLog');
  if (!id || !log) return;
  const near = log.scrollHeight - log.scrollTop - log.clientHeight < 80;
  try {
    const messages = await api('/api/rooms/' + encodeURIComponent(id) + '/messages', {silent: true});
    if (rs.activeId !== id) return;
    const room = (rs.items || []).find(item => item.id === id);
    renderRoomThread(room, messages || [], !near);
  } catch {}
}

function roomResGroup(title, count, body) {
  return `<section class="room-res-group"><div class="room-res-head"><span>${escapeHTML(title)}</span><em>${count}</em></div>${body || '<div class="room-res-more">暂无</div>'}</section>`;
}

const ROOM_MEMBER_ROLE_LABELS = {owner: '群主', maintainer: '维护者', member: '成员', viewer: '只读'};
function roomMemberRoleLabel(role) {
  const key = String(role || '').trim().toLowerCase();
  return ROOM_MEMBER_ROLE_LABELS[key] || role || '成员';
}
function roomResItem(icon, iconClass, name, sub, pick) {
  const tag = pick ? 'button' : 'div';
  const attrs = pick ? ` type="button" data-room-pick="${escapeHTML(pick)}" title="点击选为派发对象"` : '';
  return `<${tag} class="room-res-item ${pick ? '' : 'static'}"${attrs}><span class="room-res-icon ${iconClass}">${escapeHTML(icon)}</span><span class="room-res-text"><b>${escapeHTML(name)}</b>${sub ? `<small>${escapeHTML(sub)}</small>` : ''}</span></${tag}>`;
}

function roomResList(items, limit, render) {
  const shown = items.slice(0, limit).map(render).join('');
  return shown + (items.length > limit ? `<div class="room-res-more">还有 ${items.length - limit} 个</div>` : '');
}

function renderRoomResources(room) {
  const host = $('#roomResources');
  if (!host) return;
  if (!roomProjectId()) {
    host.innerHTML = '<div class="room-empty"><b>先选择项目</b><p>项目下的角色、SOP、仓库会出现在这里。</p></div>';
    return;
  }
  const agents = catalogAgents();
  const workflows = roomWorkflowChoices();
  const repos = roomRepoChoices();
  const skills = roomSkillChoices();
  const members = room?.members || [];
  host.innerHTML = [
    roomResGroup('角色', agents.length, roomResList(agents, 6, a => roomResItem((a.name || '?').slice(0, 1), 'role', a.name, a.description || a.id, 'role:' + a.id))),
    roomResGroup('SOP', workflows.length, workflows.length
      ? roomResList(workflows, 6, w => roomResItem('≡', 'sop', roomWorkflowLabel(w), ((w.nodes || []).length ? (w.nodes.length + ' 个节点') : (w.description || '')), 'sop:' + w.id))
      : roomNoSOPHint()),
    roomResGroup('仓库', repos.length, roomResList(repos, 4, r => roomResItem((r.name || 'R').slice(0, 1).toUpperCase(), 'repo', r.name, r.gongfengProjectId || r.webUrl || '', ''))),
    roomResGroup('技能', skills.length, roomResList(skills, 4, s => roomResItem('⚡', '', s.name, s.description || '', ''))),
    roomResGroup('成员', members.length, members.map(m => `<div class="room-res-item static"><span class="room-res-text"><b>${escapeHTML(m.name || ('用户 ' + m.userId))}<span class="room-member-tag">${escapeHTML(roomMemberRoleLabel(m.role))}</span></b></span></div>`).join(''))
  ].join('');
  bindRoomGoSOPs(host);
  host.querySelectorAll('[data-room-pick]').forEach(button => {
    button.onclick = () => {
      const [kind, id] = button.dataset.roomPick.split(':');
      if (kind === 'sop') toggleRoomSOP(id);
      else {
        const select = $('#roomRole');
        select.value = select.value === id ? '' : id;
        syncRoomComposer();
      }
      $('#roomInput')?.focus();
    };
  });
  syncRoomComposer();
}

function renderRoomThread(room, messages, keepScroll) {
  const log = $('#roomLog');
  if (!log) return;
  const previous = log.scrollTop;
  const rs = roomState();
  rs.active = messages.some(m => m.kind === 'dispatch' && m.runId && !m.runFinal && !roomRunTerminal(m.runStatus || ''));
  // Polls usually return the same thread; skip the rebuild so open 产出摘要,
  // text selection and scroll position survive.
  const signature = (room?.id || '') + '|' + JSON.stringify(messages);
  if (signature === rs.threadSignature && log.childElementCount) return;
  rs.threadSignature = signature;
  if (!room) {
    log.innerHTML = `<div class="room-hero"><h2>${escapeHTML(roomGreeting())}</h2><p>${roomProjectId() ? '在左侧新建或选择一个群开始协作' : '先在顶栏选择项目'}</p></div>`;
    return;
  }
  const mine = Number(state.currentUser?.id) || 0;
  let prevRun = null;
  log.innerHTML = messages.map(message => {
    const html = roomMessageHTML(message, mine, prevRun);
    if (['dispatch', 'progress', 'approval'].includes(message.kind)) prevRun = message;
    else if (message.kind !== 'decision') prevRun = null;
    return html;
  }).join('') || roomGuideHTML();
  log.querySelector('[data-room-try]')?.addEventListener('click', () => {
    const input = $('#roomInput');
    if (!input) return;
    input.focus();
    if (!/(^|\s)@$/.test(input.value.slice(0, input.selectionStart))) input.setRangeText((input.value && !/\s$/.test(input.value) ? ' ' : '') + '@', input.selectionStart, input.selectionEnd, 'end');
    updateRoomMention();
  });
  bindRoomGoSOPs(log);
  log.querySelectorAll('[data-open-issue]').forEach(button => {
    button.onclick = () => openIssueDetail(button.dataset.openIssue);
  });
  log.querySelectorAll('[data-open-run]').forEach(button => {
    button.onclick = () => { location.hash = '#/runs/' + encodeURIComponent(button.dataset.openRun); };
  });
  log.querySelectorAll('[data-decide]').forEach(button => {
    button.onclick = () => decideRoomStep(button.dataset.decide, button.dataset.run, button.dataset.node, button.dataset.name);
  });
  if (keepScroll) log.scrollTop = previous;
  else log.scrollTop = log.scrollHeight;
}

function roomRunStatusLabel(status) {
  return {running: '运行中', waiting: '等待处理', succeeded: '已完成', failed: '失败', cancelled: '已取消', queued: '排队中'}[status] || status || '排队中';
}

function roomIsSOPAuthor(name) {
  return /^SOP「.*」$/.test(String(name || ''));
}

function roomAuthorName(name) {
  return roomIsSOPAuthor(name) ? String(name).slice(4, -1) : String(name || '');
}

function roomAvatar(name) {
  if (roomIsSOPAuthor(name)) return '<span class="room-avatar sop">SOP</span>';
  return `<span class="room-avatar">${escapeHTML(String(name || '?').slice(0, 1))}</span>`;
}

function roomDispatchHTML(message, author) {
  const failed = message.dispatchError ? `<p class="room-card-error">派发未成功：${escapeHTML(message.dispatchError)}</p>` : '';
  const key = message.issueNumber ? '#' + message.issueNumber + ' ' : '';
  let run = '';
  if (message.runId) {
    const total = message.runTotal || 0;
    const done = message.runCompleted || 0;
    const pct = total ? Math.round(done * 100 / total) : 0;
    const status = message.runStatus || message.runFinal || 'queued';
    const current = (message.runCurrent || []).length ? '正在：' + message.runCurrent.join('、') : '';
    run = `<div class="room-run ${escapeHTML(status)}"><div class="room-run-head"><b>${escapeHTML(message.workflowName || 'SOP')}</b><span>${escapeHTML(roomRunStatusLabel(status))}${total ? ` · ${done}/${total}` : ''}</span></div><div class="room-run-track"><i style="width:${pct}%"></i></div>${current ? `<small>${escapeHTML(current)}</small>` : ''}</div>`;
  } else if (!message.dispatchError && !message.workflowId) {
    run = '<small class="room-card-note">已指派给角色，没有启动运行；再选一条 SOP 才会开始执行</small>';
  }
  const head = message.lane ? `${author} 并行派发` : `${author} 建了 Issue`;
  const actions = `<div class="room-card-actions"><button type="button" class="button ghost small" data-open-issue="${escapeHTML(message.issueId)}">打开 Issue</button>${message.runId ? `<button type="button" class="button ghost small" data-open-run="${escapeHTML(message.runId)}">查看运行</button>` : ''}</div>`;
  return `<article class="room-message room-card ${message.dispatchError ? 'failed' : ''}"><header>${head}</header><b>${escapeHTML(key + (message.issueTitle || 'Issue'))}</b>${message.lane ? '' : `<p>派给 ${escapeHTML(message.targetName || '未指定')}</p>`}${message.body ? `<p>${escapeHTML(message.body)}</p>` : ''}${failed}${run}${actions}</article>`;
}

function roomProgressHTML(message, prev) {
  const status = message.nodeStatus || '';
  const icon = status === 'succeeded' ? '✓' : status === 'failed' ? '✗' : status === 'cancelled' ? '■' : '•';
  const lines = String(message.body || '').split('\n');
  const head = lines.shift();
  const rest = lines.join('\n').trim();
  const detail = rest ? `<details><summary>产出摘要</summary><p>${escapeHTML(rest)}</p></details>` : '';
  const final = !message.nodeId;
  // Consecutive steps of one run already sit under its card; repeat the
  // issue / SOP only when the thread switches to another run or chat.
  const sameRun = prev && prev.runId && prev.runId === message.runId;
  const sop = roomIsSOPAuthor(message.author);
  const where = sameRun ? '' : [message.issueNumber ? '#' + message.issueNumber : '', sop ? '' : message.workflowName || ''].filter(Boolean).join(' · ');
  return `<article class="room-progress ${escapeHTML(status)} ${final ? 'final' : ''}">${roomAvatar(message.author)}<div><header><b>${escapeHTML(roomAuthorName(message.author) || 'SOP')}</b>${where ? `<span>${escapeHTML(where)}</span>` : ''}</header><p><i>${icon}</i>${escapeHTML(head)}</p>${detail}</div></article>`;
}

function roomApprovalHTML(message) {
  const attrs = `data-run="${escapeHTML(message.runId)}" data-node="${escapeHTML(message.nodeId)}" data-name="${escapeHTML(message.nodeName || message.nodeId)}"`;
  const actions = message.resolved
    ? `<div class="room-card-actions"><button type="button" class="button ghost small" data-open-run="${escapeHTML(message.runId)}">看详情</button></div>`
    : `<div class="room-card-actions"><button type="button" class="button primary small" data-decide="approve" ${attrs}>批准</button>${message.canReject ? `<button type="button" class="button ghost small" data-decide="reject" ${attrs}>打回</button>` : ''}<button type="button" class="button ghost small" data-open-run="${escapeHTML(message.runId)}">看详情</button></div>`;
  const where = [message.issueNumber ? '#' + message.issueNumber : '', message.issueTitle || ''].filter(Boolean).join(' ');
  return `<article class="room-message room-card room-approval ${message.resolved ? 'resolved' : ''}"><header>${escapeHTML(roomAuthorName(message.author) || 'SOP')} · ${escapeHTML(where)}</header><b>${message.resolved ? '✓' : '⏸'} ${escapeHTML(message.nodeName || '')} ${message.resolved ? '审批已处理' : '等待审批'}</b>${message.resolved ? '' : `<p>${escapeHTML(message.body || '')}</p>`}${actions}</article>`;
}

function roomMessageHTML(message, mine, prev) {
  const own = mine > 0 && Number(message.userId) === mine;
  const author = escapeHTML(message.author || '群成员');
  switch (message.kind) {
    case 'dispatch': return roomDispatchHTML(message, author);
    case 'progress': return roomProgressHTML(message, prev);
    case 'approval': return roomApprovalHTML(message);
    case 'decision': return `<div class="room-decision"><b>${author}</b> ${escapeHTML(message.body || '')}</div>`;
  }
  return `<article class="room-message ${own ? 'mine' : ''}"><header>${author}</header><p>${roomTextHTML(message.body || '')}</p></article>`;
}

// roomTextHTML escapes a chat line and highlights @mentions; mentions of the
// current user get an extra class so they stand out.
function roomTextHTML(text) {
  const me = String(state.currentUser?.loginName || '').toLowerCase();
  const names = (activeRoom()?.members || []).map(m => escapeHTML(m.name || '')).filter(Boolean)
    .sort((a, b) => b.length - a.length)
    .map(name => name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
  const pattern = new RegExp(`(^|\\s)@(${names.length ? names.join('|') + '|' : ''}[A-Za-z0-9_.-]+)`, 'g');
  return escapeHTML(text).replace(pattern, (all, lead, name) => {
    const mine = me && name.toLowerCase().startsWith(me);
    return `${lead}<span class="room-at${mine ? ' me' : ''}">@${name}</span>`;
  });
}

function roomNoSOPHint() {
  return '<div class="room-res-more">当前项目还没有可派发的 SOP。<button type="button" class="room-link" data-room-go-sops>去「SOP 流程」新建或复制一条</button></div>';
}

function bindRoomGoSOPs(host) {
  host?.querySelectorAll('[data-room-go-sops]').forEach(button => {
    button.onclick = () => switchView('sops');
  });
}

function roomGuideHTML() {
  const noSOP = !roomWorkflowChoices().length;
  return `<div class="room-guide">
    <h2>${escapeHTML(roomGreeting())}</h2>
    <p class="room-guide-lead">这个群可以聊天，也可以直接建 Issue 交给角色或 SOP 去做。</p>
    <ol>
      <li><b>聊天</b><span>输入文字回车发送，Shift+Enter 换行；<code>@成员</code> 可以点名同事。</span></li>
      <li><b>建 Issue</b><span>输入 <code>@</code> 选一个角色或 SOP（SOP 可选多条并行），第一行写标题、下面写描述，回车就建单并派发。也可以点输入框右下角「建 Issue」。</span></li>
      <li><b>跟进度</b><span>SOP 每完成一步都会在群里说一声；需要人确认的步骤会出现审批卡片，群里直接批准或打回。</span></li>
    </ol>
    <div class="room-guide-example"><small>示例</small><pre>@上线确认 v2.3 配置热更新上线
只需要最后确认一下即可上线</pre></div>
    <div class="room-guide-actions"><button type="button" class="button primary small" data-room-try>试试 @ 一个 SOP</button>${noSOP ? '<button type="button" class="button ghost small" data-room-go-sops>当前项目还没有 SOP，去流程模板</button>' : ''}</div>
  </div>`;
}

function roomMentionState() {
  const rs = roomState();
  if (!rs.mention) rs.mention = {open: false, items: [], index: 0, start: -1, targetsOnly: false};
  return rs.mention;
}

function roomMentionToken(input) {
  const before = input.value.slice(0, input.selectionStart);
  const match = before.match(/(^|\s)@([^\s@]{0,30})$/);
  return match ? {start: before.length - match[2].length - 1, query: match[2]} : null;
}

function roomMentionItems(query, targetsOnly) {
  const q = String(query || '').trim().toLowerCase();
  const hit = (...values) => !q || values.some(value => String(value || '').toLowerCase().includes(q));
  const items = [];
  catalogAgents().forEach(a => {
    if (hit(a.name, a.id)) items.push({kind: 'role', id: a.id, name: a.name || a.id, sub: a.description || '派给这个角色'});
  });
  roomWorkflowChoices().forEach(w => {
    if (hit(w.name, w.id)) items.push({kind: 'sop', id: w.id, name: roomWorkflowLabel(w), sub: (w.nodes || []).length ? w.nodes.length + ' 个步骤，派发后立即运行' : '派发后立即运行'});
  });
  if (!targetsOnly) {
    (activeRoom()?.members || []).forEach(m => {
      if (hit(m.name)) items.push({kind: 'member', id: String(m.userId), name: m.name || '用户 ' + m.userId, sub: roomMemberRoleLabel(m.role)});
    });
  }
  return items.slice(0, 40);
}

function openRoomMention({targetsOnly = false, token = null} = {}) {
  const ms = roomMentionState();
  ms.open = true;
  ms.targetsOnly = targetsOnly;
  ms.start = token ? token.start : -1;
  ms.items = roomMentionItems(token?.query || '', targetsOnly);
  ms.index = 0;
  renderRoomMention(token?.query || '');
}

function closeRoomMention() {
  const ms = roomMentionState();
  ms.targetsOnly = false;
  if (!ms.open) return;
  ms.open = false;
  const box = $('#roomMention');
  if (box) box.hidden = true;
}

function updateRoomMention() {
  const input = $('#roomInput');
  if (!input) return;
  const token = roomMentionToken(input);
  if (token && (!token.query || roomMentionItems(token.query, false).length)) openRoomMention({token});
  else if (!roomMentionState().targetsOnly) closeRoomMention();
}

function renderRoomMention(query) {
  const box = $('#roomMention');
  const ms = roomMentionState();
  if (!box) return;
  const groups = [['role', '角色 · 建单并指派'], ['sop', 'SOP · 建单并运行'], ['member', '成员 · 只是提醒']];
  let html = ms.targetsOnly ? '<div class="room-mention-title">先选这条 Issue 派给谁</div>' : '';
  groups.forEach(([kind, label]) => {
    const rows = ms.items.map((item, index) => ({item, index})).filter(row => row.item.kind === kind);
    if (!rows.length && !(kind === 'sop' && !query && !roomWorkflowChoices().length)) return;
    html += `<div class="room-mention-group">${escapeHTML(label)}</div>`;
    if (!rows.length) html += roomNoSOPHint();
    html += rows.map(({item, index}) => `<button type="button" class="room-mention-item ${index === ms.index ? 'active' : ''}" data-mention="${index}"><span class="room-res-icon ${item.kind === 'member' ? '' : item.kind}">${escapeHTML(item.kind === 'sop' ? '≡' : (item.name || '?').slice(0, 1))}</span><span><b>${escapeHTML(item.name)}</b><small>${escapeHTML(item.sub || '')}</small></span></button>`).join('');
  });
  if (!ms.items.length && !html.includes('room-mention-group')) html += `<div class="room-mention-empty">没有匹配「${escapeHTML(query)}」的角色、SOP 或成员</div>`;
  box.innerHTML = html;
  box.hidden = false;
  box.querySelectorAll('[data-mention]').forEach(button => {
    button.onmousedown = event => event.preventDefault();
    button.onclick = () => pickRoomMention(ms.items[Number(button.dataset.mention)]);
  });
  bindRoomGoSOPs(box);
  box.querySelector('.room-mention-item.active')?.scrollIntoView({block: 'nearest'});
}

function roomMentionKey(event) {
  const ms = roomMentionState();
  if (!ms.open || event.isComposing) return false;
  if (event.key === 'Escape') {
    event.preventDefault();
    ms.targetsOnly = false;
    closeRoomMention();
    return true;
  }
  if (!ms.items.length) return false;
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault();
    ms.index = (ms.index + (event.key === 'ArrowDown' ? 1 : ms.items.length - 1)) % ms.items.length;
    renderRoomMention(roomMentionToken($('#roomInput'))?.query || '');
    return true;
  }
  if ((event.key === 'Enter' && !event.shiftKey) || event.key === 'Tab') {
    event.preventDefault();
    pickRoomMention(ms.items[ms.index]);
    return true;
  }
  return false;
}

function pickRoomMention(item) {
  const input = $('#roomInput');
  const ms = roomMentionState();
  if (!item || !input) return;
  if (ms.start >= 0) {
    const caret = input.selectionStart;
    const insert = item.kind === 'member' ? '@' + item.name + ' ' : '';
    input.value = input.value.slice(0, ms.start) + insert + input.value.slice(caret).replace(/^ /, insert ? '' : '');
    const next = ms.start + insert.length;
    input.setSelectionRange(next, next);
  }
  if (item.kind === 'role') $('#roomRole').value = item.id;
  if (item.kind === 'sop') toggleRoomSOP(item.id, true);
  ms.targetsOnly = false;
  closeRoomMention();
  syncRoomComposer();
  input.focus();
  if (item.kind !== 'member' && !input.value.trim()) toast('已选好派给谁，第一行写 Issue 标题，回车建单');
}

async function decideRoomStep(action, runId, nodeId, name) {
  const id = roomState().activeId;
  if (!id) return;
  let reason = '';
  if (action === 'reject') {
    reason = await afPrompt({title: '打回「' + name + '」', message: '说明要改什么，会带给被打回的步骤。', label: '打回原因', placeholder: '例如：边界情况没覆盖', confirmText: '打回'});
    if (reason == null) return;
  }
  try {
    await api('/api/rooms/' + encodeURIComponent(id) + '/decisions', {method: 'POST', body: JSON.stringify({runId, nodeId, action, reason: String(reason || '').trim()})});
    toast(action === 'approve' ? '已批准' : '已打回');
    await refreshRoomMessages();
  } catch (error) {
    toast(error.message);
  }
}

async function createRoom() {
  const projectId = roomProjectId();
  if (!projectId) return toast('先在顶栏选择项目');
  const name = await afPrompt({title: '新建协作群', message: '群名称会显示在当前项目的群列表里。', label: '群名称', placeholder: '例如：需求值班群', confirmText: '创建'});
  if (!name || !String(name).trim()) return;
  try {
    const room = await api('/api/rooms', {method: 'POST', body: JSON.stringify({projectId, name: String(name).trim()})});
    roomState().activeId = room.id;
    await loadRooms();
    toast('已创建协作群');
  } catch (error) {
    toast(error.message);
  }
}

function submitRoomComposer() {
  if (roomTargetPicked()) dispatchRoomIssue();
  else sendRoomMessage();
}

async function sendRoomMessage() {
  const id = roomState().activeId;
  const input = $('#roomInput');
  const body = input?.value.trim();
  if (!id) return toast('先选择一个群');
  if (!body) return;
  try {
    await api('/api/rooms/' + encodeURIComponent(id) + '/messages', {method: 'POST', body: JSON.stringify({body})});
    input.value = '';
    closeRoomMention();
    syncRoomComposer();
    await openRoom(id);
  } catch (error) {
    toast(error.message);
  }
}

function splitRoomIssueText(text) {
  const lines = String(text || '').split('\n');
  const title = (lines.shift() || '').trim().slice(0, 200);
  return {title, description: lines.join('\n').trim()};
}

async function dispatchRoomIssue() {
  const rs = roomState();
  const id = rs.activeId;
  if (!id) return toast('先选择一个群');
  const {title, description} = splitRoomIssueText($('#roomInput')?.value);
  const workflowIds = rs.sops.slice();
  const agentId = $('#roomRole')?.value || '';
  if (!title) return toast('第一行写 Issue 标题');
  if (!workflowIds.length && !agentId) return toast('请选择要派给的 SOP 或角色');
  const button = $('#roomDispatch');
  button.disabled = true;
  try {
    const result = await api('/api/rooms/' + encodeURIComponent(id) + '/issues', {method: 'POST', body: JSON.stringify({title, description, workflowIds, agentId})});
    $('#roomInput').value = '';
    rs.sops = [];
    $('#roomRole').value = '';
    closeRoomMention();
    syncRoomComposer();
    if (result.issue) {
      const items = state.data.issues || [];
      const index = items.findIndex(issue => issue.id === result.issue.id);
      if (index >= 0) items[index] = result.issue;
      else items.unshift(result.issue);
      state.data.issues = items;
    }
    await openRoom(id);
    const started = (result.tasks || []).length;
    toast(result.dispatchError ? 'Issue 已创建，部分派发未成功：' + result.dispatchError : (started ? `已建单并启动 ${started} 条 SOP，进度会回到群里` : '已建单并指派给角色'));
  } catch (error) {
    toast(error.message);
  } finally {
    button.disabled = false;
  }
}

bindRooms();
if ((state.currentView || activeViewName()) === 'rooms') loadRooms();
