(() => {
  const params = new URLSearchParams(location.search);
  const runId = String(params.get('run') || '').trim();
  const embedded = params.get('embed') === '1';
  const host = document.getElementById('workbench') || document.body;
  if (embedded) document.body.classList.add('embed');
  const toolbar=document.createElement('header');toolbar.className='vscode-toolbar';
  const status=document.createElement('span'),edit=document.createElement('button'),terminal=document.createElement('button');
  status.textContent='任务 IDE';edit.textContent='启用编辑';terminal.textContent='+ 终端';terminal.disabled=true;toolbar.append(status,edit,terminal);host.before(toolbar);
  const environment=document.createElement('section');environment.className='vscode-environment';environment.setAttribute('aria-live','polite');environment.hidden=true;toolbar.after(environment);
  let environmentState=null,preparingEnvironment=false,environmentPoll=0,environmentEpoch=0,environmentRetry=0;
  function paintEnvironment(info){
    environmentState=info;environment.hidden=info.status!=='failed';environment.replaceChildren();
    terminal.disabled=!editing||preparingEnvironment||info.status==='preparing'||info.status==='busy';
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
  let currentInfo={},editing=false;
  async function action(name,body={}){const res=await fetch('/api/runs/'+encodeURIComponent(runId)+'/workspace/'+name,{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});const data=await res.json().catch(()=>({}));if(!res.ok){const err=new Error(data.error||'操作失败');err.code=data.code;err.retryAfterMs=data.retryAfterMs;throw err}return data}
  function paintEditing(info){currentInfo=info;editing=info.readOnly===false;status.textContent=editing?'任务 IDE · 可编辑':'任务 IDE · 只读';status.textContent+=' · 空闲 1 小时后回收';if(info.checkpointWarning)status.textContent+=' · '+info.checkpointWarning;edit.textContent=editing?'刷新':'启用编辑';terminal.disabled=!editing;}
  async function releaseEdit(keepalive=false){
    if(!editing)return;
    const opts={method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:'{}'};
    if(keepalive)opts.keepalive=true;
    try{await fetch('/api/runs/'+encodeURIComponent(runId)+'/workspace/release',opts)}catch(_){/* page is closing */}
    editing=false;
  }
  edit.onclick=async()=>{edit.disabled=true;try{
    if(editing){location.reload();return}
    // takeover pauses agents server-side; retry while the runner drains
    let lastErr;
    for(let i=0;i<40;i++){
      try{await action('takeover');location.reload();return}catch(err){
        lastErr=err;
        if(!/停止|稍后|仍有任务/.test(String(err.message||err)))throw err;
        await new Promise(resolve=>setTimeout(resolve,300));
      }
    }
    throw lastErr||new Error('角色仍在停止，请稍后重试');
  }catch(err){status.textContent=err.message;edit.disabled=false}};
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
      workspaceReadOnly = info.readOnly !== false;paintEditing(info);
      refreshEnvironment().then(()=>{if(!workspaceReadOnly)prepareEnvironment()}).catch(err=>paintEnvironment({status:'failed',error:err.message}));
      // No edit lease to renew; writable once the run is paused.
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
      window.agentflowIDEWorkbench=workbench;workbench.create(host, {
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
          nameShort: 'AgentFlow VS Code',
          nameLong: 'AgentFlow VS Code',
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
          tooltip: workspaceReadOnly ? '任务工作区 · 只读，点击启用编辑' : '任务工作区 · 可读写及交互式终端',
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
    }, err => {
      showError('VS Code 启动失败', err?.message || String(err));
    });
  })().catch(err => {
    showError('VS Code 启动失败', err?.message || String(err));
  });
})();
