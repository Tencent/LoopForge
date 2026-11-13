(() => {
  const params = new URLSearchParams(location.search);
  const runId = String(params.get('run') || '').trim();
  const embedded = params.get('embed') === '1';
  const host = document.getElementById('workbench') || document.body;
  if (embedded) document.body.classList.add('embed');
  const toolbar=document.createElement('header');toolbar.className='vscode-toolbar';
  // Like an AI IDE, the workspace is editable whenever the server allows it,
  // also while SOP agents run; there is no separate "enable edit" step.
  // The toolbar reads as VS Code chrome (Dark Modern title bar, ghost action
  // buttons). Codicon glyphs only exist after the workbench boots, so the
  // icons are inline SVGs drawn in the same 16px style.
  const svg=d=>`<svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.1" stroke-linecap="round" stroke-linejoin="round">${d}</svg>`;
  const ICONS={
    terminal:svg('<rect x="1.5" y="2.5" width="13" height="11" rx="1.5"/><path d="M4.5 6l2 2-2 2M8 10.5h3.5"/>'),
    popout:svg('<path d="M9 2.5h4.5V7M13.5 2.5L7.5 8.5M11.5 9.5v3a1 1 0 0 1-1 1h-7a1 1 0 0 1-1-1v-7a1 1 0 0 1 1-1h3"/>')
  };
  function toolbarAction(icon,label,title){const b=document.createElement('button');b.type='button';b.className='vscode-action';b.title=title;b.setAttribute('aria-label',title);b.innerHTML=ICONS[icon]+'<span>'+label+'</span>';return b}
  const status=document.createElement('span');status.className='vscode-toolbar-status';status.textContent='任务 IDE';status.setAttribute('aria-live','polite');
  const actions=document.createElement('div');actions.className='vscode-toolbar-actions';
  const terminal=toolbarAction('terminal','终端','新建终端');terminal.disabled=true;actions.append(terminal);
  // Opening the IDE in its own window used to hide behind the status bar
  // corner; embedded views now offer it right in the toolbar.
  if(embedded){const popout=toolbarAction('popout','新窗口打开','在新窗口打开 VS Code');popout.onclick=()=>postParent('popout');actions.append(popout)}
  toolbar.append(status,actions);host.before(toolbar);
  const environment=document.createElement('section');environment.className='vscode-environment';environment.setAttribute('aria-live','polite');environment.hidden=true;toolbar.after(environment);
  let environmentState=null,preparingEnvironment=false,environmentPoll=0,environmentEpoch=0,environmentRetry=0;
  function paintEnvironment(info){
    environmentState=info;environment.hidden=info.status!=='failed';environment.replaceChildren();
    paintToolbar();
    if(environment.hidden)return;
    const copy=document.createElement('div'),heading=document.createElement('b'),detail=document.createElement('span');heading.textContent='工作区准备失败';detail.textContent=info.error||'请重试，现有文件仍可浏览';copy.append(heading,detail);environment.append(copy);
    const retry=document.createElement('button');retry.type='button';retry.textContent='重试';retry.disabled=!editing||preparingEnvironment;retry.onclick=prepareEnvironment;environment.append(retry);
  }
  async function refreshEnvironment(){
    const epoch=environmentEpoch;
    const res=await fetch('/api/runs/'+encodeURIComponent(runId)+'/workspace/preparation',{credentials:'same-origin',headers:{Accept:'application/json'}});const data=await res.json().catch(()=>({}));if(!res.ok)throw new Error(data.error||'环境状态读取失败');if(epoch===environmentEpoch)paintEnvironment(data);return data;
  }
  async function prepareEnvironment(){
    if(preparingEnvironment||!editing)return;
    clearTimeout(environmentRetry);environmentRetry=0;preparingEnvironment=true;environmentEpoch++;paintEnvironment({status:'preparing'});
    environmentPoll=setInterval(()=>refreshEnvironment().catch(()=>{}),1500);
    try{const result=await action('preparation');environmentEpoch++;paintEnvironment(result)}catch(err){environmentEpoch++;if(err.code==='workspace_busy'){paintEnvironment({status:'busy'});environmentRetry=setTimeout(()=>prepareEnvironment(),Math.min(5000,Math.max(1000,Number(err.retryAfterMs)||2000)))}else{paintEnvironment({status:'failed',error:err.message})}}finally{preparingEnvironment=false;clearInterval(environmentPoll);if(environmentState)paintEnvironment(environmentState)}
  }
  let currentInfo=null,editing=false;
  async function action(name,body={}){const res=await fetch('/api/runs/'+encodeURIComponent(runId)+'/workspace/'+name,{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});const data=await res.json().catch(()=>({}));if(!res.ok){const err=new Error(data.error||'操作失败');err.code=data.code;err.retryAfterMs=data.retryAfterMs;throw err}return data}
  function paintToolbar() {
    const busy = environmentState?.status === 'busy';
    const preparing = preparingEnvironment || environmentState?.status === 'preparing';
    terminal.disabled = !editing || !window.agentflowIDEWorkbench || busy || preparing;
    const details = [editing ? '任务 IDE · 可编辑' : '任务 IDE · 只读'];
    if (editing && busy) details.push('等待工作区空闲…');
    else if (editing && preparing) details.push('正在准备终端环境…');
    details.push('空闲 1 小时后回收');
    if (currentInfo.checkpointWarning) details.push(currentInfo.checkpointWarning);
    const summary = details.join(' · ');
    if (status.textContent !== summary) status.textContent = summary;
  }
  async function releaseEdit(keepalive=false){
    if(!editing)return;
    const opts={method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:'{}'};
    if(keepalive)opts.keepalive=true;
    try{await fetch('/api/runs/'+encodeURIComponent(runId)+'/workspace/release',opts)}catch(_){/* page is closing */}
    editing=false;
  }
  terminal.onclick=()=>window.agentflowIDEWorkbench?.commands.executeCommand('agentflow.newTerminal');
  window.addEventListener('pagehide',()=>{clearInterval(environmentPoll);clearTimeout(environmentRetry);releaseEdit(true)});


  function vscodeWorkbenchURL(id, options = {}) {
    const next = new URLSearchParams();
    if (id) next.set('run', id);
    if (options.embed) next.set('embed', '1');
    return `/vscode.html?${next.toString()}`;
  }
  function vscodeWindowName(id) {
    return 'agentflow-vscode-' + String(id || '').replace(/[^A-Za-z0-9_-]/g, '');
  }
  function openVSCodeWindow(id) {
    const win = window.open(vscodeWorkbenchURL(id), vscodeWindowName(id), 'popup=yes,width=1440,height=900');
    if (!win) return false;
    try { win.focus(); } catch (e) {}
    return true;
  }
  function postParent(action, extra = {}) {
    if (!embedded || !window.parent || window.parent === window) return;
    window.parent.postMessage({ type: 'agentflow-vscode', runId, action, ...extra }, location.origin);
  }
  function escapeHTML(value) {
    return String(value || '').replace(/[&<>"']/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]));
  }
  function showError(title, message) {
    host.innerHTML = `<div class="vscode-error"><b>${escapeHTML(title)}</b><p>${escapeHTML(message)}</p></div>`;
  }
  async function workspaceInfo() {
    const res = await fetch(`/api/runs/${encodeURIComponent(runId)}/workspace`, {
      credentials: 'same-origin',
      headers: { Accept: 'application/json' }
    });
    const body = await res.json().catch(() => ({}));
    if (res.status === 401) throw new Error('请先登录平台后再打开工作区');
    if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
    return body;
  }
  function loadScript(src) {
    return new Promise((resolve, reject) => {
      const el = document.createElement('script');
      el.src = src;
      el.onload = () => resolve();
      el.onerror = () => reject(new Error('加载失败: ' + src));
      document.head.appendChild(el);
    });
  }
  function loadStyle(href) {
    const el = document.createElement('link');
    el.rel = 'stylesheet';
    el.href = href;
    document.head.appendChild(el);
  }

  window.vscodeWorkbenchURL = vscodeWorkbenchURL;
  window.vscodeWindowName = vscodeWindowName;
  window.openVSCodeWindow = openVSCodeWindow;

  if (!runId) {
    showError('缺少 Run ID', '请从 SOP 运行详情打开工作区。');
    return;
  }

  host.innerHTML = `<div class="vscode-boot"><span class="vscode-spinner"></span><b>正在启动 VS Code…</b></div>`;

  const baseUrl = `${location.origin}/vscode-web`;
  loadStyle(`${baseUrl}/out/vs/workbench/workbench.web.main.css`);

  (async () => {
    let repositories = [];
    let workspaceChanges = [];
    let workspaceReadOnly = true;
    try {
      let info = await workspaceInfo();
      if (!info.available && info.restorable) {
        status.textContent='正在恢复工作区…';
        await action('restore');
        info=await workspaceInfo();
      }
      if (!info.available) {
        showError('当前无法浏览工作区', info.reason || '工作区不可用');
        return;
      }
      await action('use').catch(()=>{});
      currentInfo = info;
      workspaceReadOnly = info.readOnly !== false;
      editing = !workspaceReadOnly;
      paintToolbar();
      // Opening a shell revalidates the environment on the server.
      refreshEnvironment().then(state => {
        if (state.status !== 'ready') return prepareEnvironment();
      }).catch(err => paintEnvironment({ status: 'failed', error: err.message }));
      // No edit lease to renew; writable whenever the server says so.
      workspaceChanges = Array.isArray(info.changes) ? info.changes : [];
      const names = new Set();
      for (const name of info.repositories || []) {
        const repo = String(name || '').trim();
        if (repo) names.add(repo === '.' ? 'workspace' : repo);
      }
      if (!names.size) {
        for (const change of info.changes || []) {
          const repo = String(change.repository || '').trim();
          names.add(!repo || repo === '.' ? 'workspace' : repo);
        }
      }
      repositories = [...names];
    } catch (err) {
      showError('无法打开工作区', err?.message || String(err));
      return;
    }
    await loadScript(`${baseUrl}/out/vs/loader.js`);
    await loadScript(`${baseUrl}/out/vs/webPackagePaths.js`);
    const paths = {};
    Object.keys(self.webPackagePaths || {}).forEach(key => {
      paths[key] = `${baseUrl}/node_modules/${key}/${self.webPackagePaths[key]}`;
    });
    window.require.config({
      baseUrl: `${baseUrl}/out`,
      recordStats: true,
      urlArgs: 'v=native-scm-47',
      trustedTypesPolicy: window.trustedTypes?.createPolicy('amdLoader', {
        createScriptURL(value) {
          if (value.startsWith(location.origin) || value.startsWith('/')) return value;
          throw new Error('Invalid script url: ' + value);
        }
      }),
      paths
    });
    await loadScript(`${baseUrl}/out/vs/workbench/workbench.web.main.nls.js?v=native-scm-47`);
    await loadScript(`${baseUrl}/out/vs/workbench/workbench.web.main.js?v=native-scm-47`);
    window.require(['vs/workbench/workbench.web.main', 'vs/base/common/uri'], (workbench, uriMod) => {
      const URI = uriMod.URI;
      const folderUri = URI.from({
        scheme: 'agentflow',
        authority: runId,
        path: '/',
        query: (() => {
          const params = new URLSearchParams();
          params.set('writable',workspaceReadOnly?'0':'1');
          if (repositories.length) params.set('repos', JSON.stringify(repositories));
          if (workspaceChanges.length) params.set('changes', JSON.stringify(workspaceChanges));
          const encoded = params.toString();
          return encoded || undefined;
        })()
      });
      const extensionUri = URI.parse(`${location.origin}/extensions/agentflow-workspace`);
      host.replaceChildren();
      workbench.create(host, {
        additionalBuiltinExtensions: [extensionUri],
        folderUri,
        workspaceProvider: {
          workspace: { folderUri },
          trusted: true,
          open: async () => true
        },
        configurationDefaults: {
          'workbench.colorTheme': 'Default Dark Modern',
          'workbench.startupEditor': 'none',
          'window.commandCenter': true,
          'workbench.layoutControl.enabled': true,
          'workbench.tips.enabled': false,
          'files.autoSave': 'off',
          'editor.readOnly': workspaceReadOnly,
          'git.enabled': false,
          'scm.alwaysShowRepositories': true,
          'scm.defaultViewMode': 'list',
          'scm.experimental.showHistoryGraph': false,
          'scm.showIncomingChanges': 'never',
          'scm.showOutgoingChanges': 'auto',
          'scm.showChangesSummary': false,
          'scm.repositories.visible': 10,
          'agentflow.repositories': repositories
        },
        productConfiguration: {
          nameShort: 'LoopForge VS Code',
          nameLong: 'LoopForge VS Code',
          applicationName: 'agentflow-vscode',
          dataFolderName: '.agentflow-vscode',
          version: '1.91.1',
          quality: 'stable',
          enableTelemetry: false,
          extensionEnabledApiProposals: {
            'agentflow.workspace': ['fileSearchProvider', 'textSearchProvider', 'scmHistoryProvider']
          }
        },
        windowIndicator: {
          label: `$(remote) SOP ${runId.slice(-12)}`,
          tooltip: workspaceReadOnly ? '任务工作区 · 只读' : '任务工作区 · 可读写及交互式终端',
          command: embedded ? 'agentflow.popout' : undefined
        },
        commands: [
          {
            id: 'agentflow.popout',
            label: '在新窗口打开 VS Code',
            menu: 1,
            handler: () => {
              if (embedded) postParent('popout');
              else openVSCodeWindow(runId);
            }
          },
          {
            id: 'agentflow.maximize',
            label: '沉浸模式',
            menu: 1,
            handler: () => postParent('maximize')
          }
        ]
      });
      window.agentflowIDEWorkbench = workbench;
      paintToolbar();
    }, err => {
      showError('VS Code 启动失败', err?.message || String(err));
    });
  })().catch(err => {
    showError('VS Code 启动失败', err?.message || String(err));
  });
})();
