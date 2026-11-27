function syncRunModalKnowledgeSummary(){
  const summary=$('#runKnowledgeRefsSummary'),reset=$('#runKnowledgeRefsResetBtn');
  const override=state.runModalKnowledgeRefs;
  if(summary){
    if(override===null||override===undefined)summary.textContent='默认按节点或角色配置';
    else if(override.length)summary.textContent=`本次覆盖：已选 ${override.length} 个连接器`;
    else summary.textContent='本次覆盖：不挂载知识库';
  }
  if(reset)reset.hidden=override===null||override===undefined;
}
function openRunModal(workflowId){
  const select=$('#runWorkflowChoice'),workflows=runnableWorkflowOptions();
  select.innerHTML=workflows.map(w=>`<option value="${escapeHTML(w.id)}" ${w.id===workflowId?'selected':''}>${escapeHTML(w.name)}</option>`).join('')||'<option value="">暂无可运行 LOOP</option>';
  state.runModalKnowledgeRefs=null;
  syncRunModalKnowledgeSummary();
  const refBtn=$('#runKnowledgeRefsBtn');
  if(refBtn)refBtn.onclick=()=>{
    if(typeof window.editKnowledgeRefs!=='function')return toast('知识库选择器尚未加载');
    window.editKnowledgeRefs(currentProject()?.id||state.projectFilter||'',state.runModalKnowledgeRefs||[],refs=>{
      state.runModalKnowledgeRefs=refs;
      syncRunModalKnowledgeSummary();
    });
  };
  const resetBtn=$('#runKnowledgeRefsResetBtn');
  if(resetBtn)resetBtn.onclick=()=>{state.runModalKnowledgeRefs=null;syncRunModalKnowledgeSummary()};
  $('#confirmRun').disabled=!workflows.length;
  $('#runModal').classList.add('open');
  void loadRunExecutionTargets(currentProject()?.id||'');
  setTimeout(()=>$('#runInput').focus(),0);
}

// ---------- 执行机器 ----------
let runnerRefreshTimer=null;
let runnerRefreshRequest=0;
const runnerRuntimeTests=new Set();
const runnerAuthChanges=new Map();
const runnerMaintenancePending=new Set();
const runnerPrivilegedSandboxPending=new Map();
let runnerRuntimeMenu=null,runnerRuntimeTrigger=null;
async function refreshRunners(silent){
  const request=++runnerRefreshRequest;
  const projectId=currentProject()?.id||'';
  const query=projectId?`?projectId=${encodeURIComponent(projectId)}`:'';
  if(state.runnerProjectId!==projectId)renderRunnersView();
  void ensureRunnerBindCode(silent);
  try{
    const [runners,def]=await Promise.all([
      api('/api/runners'+(query||'?mine=1'),{silent:true}),
      api('/api/runners/default'+query,{silent:true})
    ]);
    if(request!==runnerRefreshRequest||projectId!==(currentProject()?.id||''))return;
    state.runners=runners||[];
    state.runnerProjectId=projectId;
    state.defaultRunnerId=def?.runnerId||'';
    state.personalDefaultRunnerId=def?.personalRunnerId??state.defaultRunnerId;
    state.platformRunner=def?.platformRunner||null;
    state.minRunnerVersion=def?.minRunnerVersion||'';
    state.minPrivilegedSandboxRunnerVersion=def?.minPrivilegedSandboxRunnerVersion||'';
    renderRunnersView();
  }catch(e){if(!silent)toast(e.message)}
}
// 接入命令携带一次性绑定码（10 分钟有效）：复制即用，无需先创建访问密钥。
async function ensureRunnerBindCode(silent){
  const cached=state.runnerBindCode;
  if(cached&&new Date(cached.expiresAt).getTime()>Date.now()+60_000)return;
  try{
    const out=await api('/api/runners/bind-codes',{method:'POST',silent:true,body:'{}'});
    if(out?.code){state.runnerBindCode=out;renderRunnersView()}
  }catch(e){if(!silent)toast(e.message)}
}
function runnerConcurrencyLabel(runner){
  const limit=Number(runner?.runLimit)||0;
  return limit>0?`最多同时运行 ${limit} 条`:'不限同时运行数';
}
function runnerRunLimitHTML(runner,platform=false){
  if(platform?!runner.canEditLimit:!(Number(runner?.ownerUserId)>0))return '';
  const limit=Number(runner?.runLimit)||0;
  const scope=platform?'默认 10 条，平台所有项目共用此名额。按顶层 LOOP 计数。':'新绑定机器默认 10 条。按顶层 LOOP 计数，共享项目共用机器名额。';
  const tip=scope+'超出名额后排队。失败、暂停、待确认的 LOOP 空闲 1 小时后自动归档并回收工作区，释放名额。继续或重试时自动恢复工作区。输入 0 或留空表示不限。';
  const id=platform?'runLimit-platform-runner':`runLimit-${escapeHTML(runner.id)}`;
  const control=platform?'data-platform-run-limit':`data-runner-run-limit="${escapeHTML(runner.id)}"`;
  return `<div class="runner-run-limit"><label for="${id}">最多同时运行<input id="${id}" type="number" inputmode="numeric" min="0" step="1" placeholder="不限" value="${limit>0?limit:''}" ${control} aria-label="${escapeHTML(runner.name||runner.id)} 最多同时运行条数">条</label><span class="runner-tip" tabindex="0" role="note" aria-label="${tip}" data-tip="${tip}">?</span></div>`;
}
async function saveRunnerRunLimit(runnerId,raw,platform=false){
  const text=String(raw??'').trim();
  const value=text===''?0:Number(text);
  if(!Number.isSafeInteger(value)||value<0){toast('请输入非负整数，0 或留空表示不限');refreshRunners(true);return}
  try{
    const target=platform?'platform':encodeURIComponent(runnerId);
    const projectId=platform?currentProject()?.id||'':'';
    const query=projectId?`?projectId=${encodeURIComponent(projectId)}`:'';
    const runner=await api(`/api/runners/${target}/run-limit${query}`,{method:'PUT',body:JSON.stringify({runLimit:value})});
    if(platform){
      if(state.runnerProjectId===projectId)state.platformRunner=runner;
    }else{
      const i=(state.runners||[]).findIndex(r=>r.id===runnerId);
      if(i>=0&&runner?.id)state.runners[i]={...state.runners[i],...runner};
    }
    toast(value>0?`已设置：最多同时运行 ${value} 条`:'已设为不限','ok');renderRunnersView();
    void refreshRunners(true);
  }catch(err){toast(err.message);refreshRunners(true)}
}
function runnerPrivilegedSandboxHTML(runner,canEdit,platform=false){
  const allowed=runner.allowPrivilegedSandbox;
  if(typeof allowed!=='boolean')return '';
  const target=platform?'platform':runner.id;
  const minVersion=state.minPrivilegedSandboxRunnerVersion;
  const supported=platform||minVersion&&runnerVersionAtLeast(runner.runnerVersion,minVersion);
  const pending=runnerPrivilegedSandboxPending.get(target);
  const editable=canEdit&&supported;
  const status=supported?(allowed?lfT('已允许','Allowed'):lfT('已关闭','Off')):lfT('需升级 Runner','Runner upgrade required');
  const help=lfT('只影响后续启动的特权容器。特权容器可以访问宿主机设备。','Applies to future starts. Privileged containers can access host devices.');
  const toggle=editable?`<input type="checkbox" role="switch" data-runner-privileged-sandbox="${escapeHTML(target)}" ${(pending??allowed)?'checked':''} ${pending!==undefined?'disabled':''}>`:'';
  const label=toggle?`<label>${toggle}<span>${lfT('允许特权 Docker 沙箱','Allow privileged Docker sandboxes')}</span></label>`:`<span>${lfT('特权 Docker 沙箱','Privileged Docker sandboxes')}</span>`;
  return `<div class="runner-privileged-sandbox">
    ${label}
    ${editable?'':`<span class="runner-privileged-state">${status}</span>`}
    <span class="runner-tip" tabindex="0" role="note" aria-label="${help}" data-tip="${help}">?</span>
  </div>`;
}
async function saveRunnerPrivilegedSandbox(target,allowed){
  if(runnerPrivilegedSandboxPending.has(target))return;
  runnerPrivilegedSandboxPending.set(target,allowed);
  renderRunnersView();
  try{
    await api(`/api/runners/${encodeURIComponent(target)}/privileged-sandbox`,{method:'PUT',body:JSON.stringify({allowed}),silent:true});
    const runner=target==='platform'?state.platformRunner:(state.runners||[]).find(r=>r.id===target);
    if(runner)runner.allowPrivilegedSandbox=allowed;
    toast(lfT('特权 Docker 沙箱设置已保存','Privileged Docker sandbox setting saved'),'ok');
    await refreshRunners(true);
  }catch(err){
    toast(err.message,'err');
  }finally{
    runnerPrivilegedSandboxPending.delete(target);
    renderRunnersView();
  }
}
function runnerLoadHTML(runner){
  const parts=[];
  const limit=Number(runner.runLimit)||0;
  if(runner.load){
    parts.push(`运行中 ${Number(runner.load.runningRuns)||0}`);
    parts.push(limit>0?`已占 ${Number(runner.load.occupiedRuns)||0} / ${limit}`:'不限');
    if(runner.load.queuedRuns>0)parts.push(`排队 ${Number(runner.load.queuedRuns)}`);
  }
  if(runner.cloudPool)parts.push(`云端池 ${Number(runner.cloudPoolOnline)||0} 台在线`);
  return parts.length?`<small class="runner-load">${parts.join(' · ')}</small>`:'';
}
function runnerStatusLabel(status,lastSeen){
  const label=status==='online'?'在线':status==='offline'?'离线':(status||'未知');
  const style=status==='online'?' online':status==='offline'?' offline':'';
  const seen=lastSeen?` title="${escapeHTML(runnerLastSeen(lastSeen))}"`:'';
  return `<span class="runner-status${style}"${seen}><i aria-hidden="true"></i>${escapeHTML(label)}</span>`;
}
const runnerCLILabels={'claude-code':'Claude Code','codex':'Codex','codebuddy':'CodeBuddy',opencode:'OpenCode',cursor:'Cursor CLI',agy:'Antigravity',kimi:'Kimi Code',pi:'Pi',dsh:'DeepSeek Harness',zcode:'ZCode'};
const runnerAuthLabel={local:'账号登录',personal:'API Key'};
const runnerAuthDescription={local:'Claude Code、Codex 和 Antigravity 可以使用运行 Runner 的系统账号在这台机器上的已有登录，对话与 LOOP 的 native、Docker 沙箱均可复用。共享对话中，只有机器所有者发言时会使用此登录。',personal:'读取「个人配置」中保存的 API Key。对话使用当前发言人的 Key，LOOP 使用运行责任人的 Key。Antigravity 的 Gemini API Key 按 API 单独计费。'};
const runnerOSLabels={darwin:'macOS',linux:'Linux',windows:'Windows'};
function runnerKeyDescription(provider){
  if(provider==='zcode')return 'GLM Coding Plan 的 API Key 配合套餐专用地址可使用订阅额度，请在「个人配置」中填写。Start Plan 额度需要登录 ZCode 账号，无法通过 API Key 使用。';
  return '该 CLI 支持 API Key。使用服务商订阅套餐提供的 Key，执行用量会计入对应的套餐额度。请在「个人配置」中填写 API Key。';
}
function runnerPersonalCredentialConfigured(provider){
  const user=userById(state.currentUser?.id)||{};
  if(provider==='codex')return !!user.codexApiKeyConfigured;
  if(provider==='claude-code')return !!user.claudeCodeApiKeyConfigured;
  if(provider==='codebuddy')return !!user.codeBuddyApiKeyConfigured;
  return !!user.agentCliCredentials?.[provider]?.apiKeyConfigured;
}
function runnerRuntimeSupportsLogin(runtime){
  return runtime.provider==='codex'||runtime.provider==='claude-code'||runtime.provider==='agy';
}
function runnerRuntimeAuthMode(runtime){
  return runnerRuntimeSupportsLogin(runtime)&&runtime.authMode!=='personal'?'local':'personal';
}
function runnerAuthHTML(runtime,key,busy,shared){
  const supportsLogin=runnerRuntimeSupportsLogin(runtime);
  const mode=shared?'personal':runnerAuthChanges.get(key)||runnerRuntimeAuthMode(runtime);
  const missing=mode==='personal'&&!runnerPersonalCredentialConfigured(runtime.provider);
  const title=missing?' title="请在个人配置中填写 API Key"':'';
  if(!shared&&supportsLogin)return `<select class="runner-auth-select${mode==='personal'?' is-personal':''}" data-runner-auth="${escapeHTML(key)}" aria-label="${runnerCLILabels[runtime.provider]} 认证方式"${title} ${busy?'disabled':''}>
    <option value="local" ${mode==='local'?'selected':''}>${runnerAuthLabel.local}</option>
    <option value="personal" ${mode==='personal'?'selected':''}>${runnerAuthLabel.personal}</option>
  </select>`;
  const keyHelp=supportsLogin?'':`<button class="runner-auth-tip" type="button" data-runner-key-help="${runtime.provider}" aria-label="${runnerCLILabels[runtime.provider]} API Key 说明" aria-haspopup="dialog" title="${escapeHTML(runnerKeyDescription(runtime.provider))}"><span aria-hidden="true">?</span></button>`;
  return `<span class="runner-auth-fixed"${title}><span>${runnerAuthLabel[mode]}</span>${keyHelp}</span>`;
}
function runnerRuntimeHTML(runner,runtime,shared){
  const provider=runtime.provider,label=runnerCLILabels[provider];
  const key=`${runner.id}|${provider}`,safeKey=escapeHTML(key);
  const testing=runnerRuntimeTests.has(key),updating=runnerMaintenancePending.has(key);
  const busy=testing||updating||runnerAuthChanges.has(key);
  const disabled=runtime.status==='disabled';
  const icon=window.AgentFlowAssistantModels?.cliIcon(provider);
  return `<div class="runner-runtime-row ${disabled?'off':''}">
    <label class="runner-runtime-label">
      <input type="checkbox" data-runner-cli="${safeKey}" ${disabled?'':'checked'} ${shared?'disabled':''} aria-label="启用 ${label}">
      <span class="runner-runtime-icon ${provider}" aria-hidden="true">${icon?`<img src="${icon}" alt="">`:''}</span>
      <span class="runner-runtime-name"><b>${label}</b><small title="${escapeHTML(runtime.binaryPath||'')}">${escapeHTML(runtime.version||runtime.binaryPath||'已发现')}</small></span>
    </label>
    ${runnerAuthHTML(runtime,key,busy,shared)}
    ${shared?'':`<button class="runner-more" type="button" data-runner-runtime-menu="${safeKey}" aria-label="${label} 更多操作" aria-haspopup="menu" aria-expanded="false" aria-busy="${testing||updating}">${huiIcon(testing||updating?'arc-spinner':'more-horizontal')}</button>`}
  </div>`;
}
function runnerLastSeen(value){
  if(!value)return '尚无心跳';
  const seconds=Math.max(0,Math.round((Date.now()-new Date(value).getTime())/1000));
  if(seconds<60)return `${seconds} 秒前`;
  if(seconds<3600)return `${Math.floor(seconds/60)} 分钟前`;
  return new Date(value).toLocaleString();
}
function runnerCopyCommand(btn,text,message){
  const done=()=>{
    toast(message,'ok');
    if(!btn.dataset.label)btn.dataset.label=btn.textContent;
    btn.textContent='已复制';btn.classList.add('copied');
    clearTimeout(btn._copiedTimer);
    btn._copiedTimer=setTimeout(()=>{btn.textContent=btn.dataset.label;btn.classList.remove('copied')},1400);
  };
  if(!navigator.clipboard){toast('复制失败，请手动选择复制');return}
  navigator.clipboard.writeText(text).then(done).catch(()=>toast('复制失败，请手动选择复制'));
}
function runnerProjectWritable(project){
  const userId=Number(state.currentUser?.id)||0;
  return !!(userId>0&&project&&!project.archivedAt&&(Number(project.ownerId)===userId||
    project.members?.some(m=>Number(m.userId)===userId&&String(m.role||'').trim().toLowerCase()!=='viewer')));
}
function platformRunnerCardHTML(runner){
  const name=escapeHTML(runner.name||'平台执行机器（云端）');
  const limit=runner.canEditLimit?runnerRunLimitHTML(runner,true):`<span class="runner-limit-label">${runnerConcurrencyLabel(runner)}</span>`;
  return `<article class="runner-card runner-platform-card panel">
    <div class="runner-platform-main">
      <span class="runner-machine-icon" aria-hidden="true">${huiIcon('server')}</span>
      <div class="runner-card-title">
        <div class="runner-card-name"><b title="${name}">${name}</b><span class="runner-tag">平台自带</span></div>
        ${runnerLoadHTML(runner)}
      </div>
    </div>
    <div class="runner-platform-controls">
      ${runnerStatusLabel(runner.status)}
      <span class="runner-platform-sep" aria-hidden="true"></span>
      ${limit}
      ${runnerPrivilegedSandboxHTML(runner,runner.canEditLimit,true)}
    </div>
  </article>`;
}
function renderRunnersView(){
  const list=$('#runnersList');if(!list)return;
  const host=location.host;
  const httpsOrigin=`https://${host}`,httpOrigin=`http://${host}`;
  const bindHTTPS=$('#bindHttpsOrigin'),bindHTTP=$('#bindHttpOrigin'),bindHost=$('#bindServerHost');
  if(bindHTTPS)bindHTTPS.textContent=httpsOrigin;if(bindHTTP)bindHTTP.textContent=httpOrigin;if(bindHost)bindHost.textContent=host;
  const tokenSpan=$('#bindToken');
  if(tokenSpan)tokenSpan.textContent=state.runnerBindCode?.code||'生成中…';
  const copyBtn=$('#copyBindCommand');
  if(copyBtn)copyBtn.onclick=()=>{
    const text=$('#bindCliCommand')?.innerText||'';
    runnerCopyCommand(copyBtn,text,'已复制，10 分钟内有效');
  };
  const uninstallHTTPS=$('#uninstallHttpsOrigin'),uninstallHTTP=$('#uninstallHttpOrigin');
  if(uninstallHTTPS)uninstallHTTPS.textContent=httpsOrigin;if(uninstallHTTP)uninstallHTTP.textContent=httpOrigin;
  const copyUnBtn=$('#copyUninstallCommand');
  if(copyUnBtn)copyUnBtn.onclick=(e)=>{
    const text=e.currentTarget.parentElement?.querySelector('pre')?.innerText||'';
    runnerCopyCommand(copyUnBtn,text,'已复制卸载命令');
  };
  const projectId=currentProject()?.id||'';
  const projectLoaded=state.runnerProjectId===projectId;
  const platform=projectLoaded?state.platformRunner:null;
  const own=r=>r.ownerUserId===state.currentUser?.id;
  const runners=(state.runners||[]).filter(r=>own(r)||projectLoaded);
  const personalDefaultId=state.personalDefaultRunnerId??state.defaultRunnerId??'';
  $('#runnerCount').textContent=String(runners.length);
  const focusedMenu=document.activeElement?.dataset.runnerRuntimeMenu;
  const focusedUpdate=document.activeElement?.dataset.runnerCheckUpdate;
  const focusedShare=document.activeElement?.dataset.runnerShare;
  const focusedPrivilege=document.activeElement?.dataset.runnerPrivilegedSandbox;
  closeRunnerRuntimeMenu();
  const platformSection=$('#platformRunnerSection');
  const platformHost=$('#platformRunnerHost');
  if(platformSection&&platformHost){
    platformSection.hidden=!platform;
    platformHost.innerHTML=platform?platformRunnerCardHTML(platform):'';
  }
  const runnerCards=runners.map(r=>{
    const discoveredCLIs=(r.runtimes||[]).filter(rt=>runnerCLILabels[rt.provider]);
    const shared=!own(r);
    const isPersonalDefault=r.id===personalDefaultId;
    const cliRows=discoveredCLIs.map(rt=>runnerRuntimeHTML(r,rt,shared)).join('');
    const cliSection=cliRows||'<div class="runner-runtime-empty">未发现支持的 Agent CLI</div>';
    const osLabel=runnerOSLabels[r.os]||r.os||'?';
    const defaultBlocker=runnerDefaultBlocker(r);
    const checking=runnerMaintenancePending.has(r.id);
    return `<article class="runner-card panel ${isPersonalDefault?'is-default':''}">
      <div class="runner-card-head">
        <span class="runner-machine-icon" aria-hidden="true">${huiIcon('monitor')}</span>
        <div class="runner-card-title">
          <div class="runner-card-name"><b title="${escapeHTML(r.name||r.id)}">${escapeHTML(r.name||r.id)}</b>${isPersonalDefault?'<span class="runner-tag default">个人默认</span>':''}${shared?'<span class="runner-tag">项目共享</span>':''}</div>
          <small>${escapeHTML(osLabel)} · ${escapeHTML(r.arch||'?')} · Runner ${escapeHTML(r.runnerVersion||'未知')}</small>
          ${runnerLoadHTML(r)}
        </div>
        <div class="runner-head-actions">${runnerStatusLabel(r.status,r.lastSeenAt)}${shared?'':`<button class="button ghost runner-check-update" type="button" data-runner-check-update="${escapeHTML(r.id)}" ${r.status!=='online'?'disabled':checking?'aria-disabled="true"':''}>${huiIcon(checking?'arc-spinner':'refresh')}${checking?'处理中…':'检查更新'}</button>`}</div>
      </div>
      <div class="runner-runtime-list">
        ${cliRows?`<div class="runner-runtime-heading"><span>CLI</span><button class="runner-auth-help" type="button" data-runner-auth-help aria-label="模型认证说明" aria-haspopup="dialog">模型认证 ${huiIcon('info')}</button></div>`:''}
        ${cliSection}
      </div>
      <div class="runner-card-action">
        ${shared?`<span class="runner-limit-label">${runnerConcurrencyLabel(r)}</span>`:runnerRunLimitHTML(r)}
        ${runnerPrivilegedSandboxHTML(r,!shared)}
        <span class="runner-card-spacer"></span>
        ${shared?'':`<button class="button ghost small runner-text-button" type="button" data-runner-share="${escapeHTML(r.id)}" aria-haspopup="dialog">共享项目${r.sharedProjectIds?.length?`（${r.sharedProjectIds.length}）`:''}</button>`}
        ${isPersonalDefault?'<button class="button ghost small runner-text-button" type="button" data-set-default="">清除个人默认</button>':''}
        ${shared||r.status==='online'?'':`<button class="button ghost small runner-text-button" type="button" data-remove-runner="${escapeHTML(r.id)}" title="机器已不再使用时移除绑定">移除</button>`}
        ${!shared&&!isPersonalDefault?`<button class="button hui-btn-soft small" type="button" ${defaultBlocker?`disabled title="${escapeHTML(defaultBlocker)}"`:''} data-set-default="${escapeHTML(r.id)}">设为个人默认</button>`:''}
      </div>
    </article>`;
  }).join('');
  list.innerHTML=runnerCards||'<div class="runner-empty"><b>还没有机器</b><span>执行上方命令后自动出现</span></div>';
  // Bind click handlers via JS (CSP blocks inline onclick attributes).
  list.querySelectorAll('[data-set-default]').forEach(btn=>btn.onclick=()=>{
    setDefaultRunner(btn.dataset.setDefault||'');
  });
  list.querySelectorAll('[data-runner-share]').forEach(btn=>{
    btn.onclick=()=>editRunnerSharing(runners.find(r=>r.id===btn.dataset.runnerShare));
    if(btn.dataset.runnerShare===focusedShare)btn.focus({preventScroll:true});
  });
  list.querySelectorAll('[data-remove-runner]').forEach(btn=>btn.onclick=()=>removeRunner(btn.dataset.removeRunner));
  list.querySelectorAll('[data-runner-cli]').forEach(cb=>cb.onchange=()=>{
    cb.closest('.runner-runtime-row')?.classList.toggle('off',!cb.checked);
    const [runnerId,provider]=cb.dataset.runnerCli.split('|');
    toggleRunnerRuntime(runnerId,provider,cb.checked);
  });
  for(const scope of [platformHost,list]){
    scope?.querySelectorAll('[data-runner-run-limit],[data-platform-run-limit]').forEach(input=>{
      input.onchange=()=>saveRunnerRunLimit(input.dataset.runnerRunLimit,input.value,input.hasAttribute('data-platform-run-limit'));
      input.onkeydown=e=>{if(e.key==='Enter'){e.preventDefault();input.blur()}};
    });
    scope?.querySelectorAll('[data-runner-privileged-sandbox]').forEach(input=>{
      input.onchange=()=>saveRunnerPrivilegedSandbox(input.dataset.runnerPrivilegedSandbox,input.checked);
      if(input.dataset.runnerPrivilegedSandbox===focusedPrivilege&&!input.disabled)input.focus({preventScroll:true});
    });
  }
  list.querySelectorAll('[data-runner-auth]').forEach(select=>select.onchange=()=>{
    const [runnerId,provider]=select.dataset.runnerAuth.split('|');
    setRunnerRuntimeAuth(runnerId,provider,select.value);
  });
  list.querySelectorAll('[data-runner-auth-help]').forEach(btn=>btn.onclick=()=>afAlert({
    title:'模型认证方式',
    message:`${runnerAuthLabel.local}\n${runnerAuthDescription.local}\n\n${runnerAuthLabel.personal}\n${runnerAuthDescription.personal}`
  }));
  list.querySelectorAll('[data-runner-key-help]').forEach(btn=>btn.onclick=()=>{
    const provider=btn.dataset.runnerKeyHelp;
    return afAlert({
      title:`${runnerCLILabels[provider]} API Key 说明`,
      message:runnerKeyDescription(provider)
    });
  });
  list.querySelectorAll('[data-runner-runtime-menu]').forEach(btn=>{
    btn.onclick=()=>showRunnerRuntimeMenu(btn);
    btn.onkeydown=e=>{if(e.key==='ArrowDown'||e.key==='ArrowUp'){e.preventDefault();showRunnerRuntimeMenu(btn,e.key==='ArrowUp')}};
    if(btn.dataset.runnerRuntimeMenu===focusedMenu)btn.focus({preventScroll:true});
  });
  list.querySelectorAll('[data-runner-check-update]').forEach(btn=>{
    btn.onclick=()=>checkRunnerUpdate(btn.dataset.runnerCheckUpdate);
    if(!btn.disabled&&btn.dataset.runnerCheckUpdate===focusedUpdate)btn.focus({preventScroll:true});
  });
  [platformHost,list].forEach(el=>el&&window.AgentFlowHarnessIcons?.hydrate?.(el));
  if(!runnerRefreshTimer)runnerRefreshTimer=setInterval(()=>{
    if(!document.hidden&&activeViewName()==='runners'&&!runnerRuntimeTrigger&&!$('#confirmModal.open')&&!document.activeElement?.matches?.('[data-runner-run-limit],[data-platform-run-limit],[data-runner-auth],[data-runner-key-help]'))refreshRunners(true);
  },15000);
}
async function editRunnerSharing(runner){
  const projects=(state.data.projects||[]).filter(runnerProjectWritable);
  const shared=new Set(runner.sharedProjectIds||[]);
  const projectIds=await afOpenDialog(modal=>{
    $('#confirmTitle').textContent=`共享 ${runner.name||runner.id}`;
    $('#confirmMessage').textContent=projects.length
      ?'所选项目的可写成员可以在这台机器上运行代码，执行时使用各自的凭据。'
      :'加入项目并获得写权限后，即可共享这台机器。';
    const ok=$('#confirmOkBtn');
    ok.textContent='保存';
    ok.className='button primary';
    $('#confirmCancelBtn').textContent='取消';
    setConfirmIcon('info','monitor');
    const fields=document.createElement('fieldset');
    fields.className='runner-share-projects';
    fields.setAttribute('aria-label','共享项目');
    fields.innerHTML=projects.map(p=>`<label><input type="checkbox" value="${escapeHTML(p.id)}" ${shared.has(p.id)?'checked':''}> <span>${escapeHTML(p.name||p.id)}</span></label>`).join('');
    if(projects.length)modal.querySelector('.confirm-modal-footer').before(fields);
    return {focus:fields.querySelector('input')||ok,value:accepted=>{
      fields.remove();
      return accepted?[...fields.querySelectorAll('input:checked')].map(input=>input.value):null;
    }};
  });
  if(!projectIds)return;
  try{
    await api(`/api/runners/${encodeURIComponent(runner.id)}/shared-projects`,{method:'PUT',body:JSON.stringify({projectIds})});
    toast('已更新共享项目','ok');
    await refreshRunners(true);
  }catch(err){toast(err.message,'err')}
}
async function removeRunner(runnerId){
  const runner=(state.runners||[]).find(r=>r.id===runnerId);
  if(!await afConfirm({title:'移除执行机器',message:`确定移除 ${runner?.name||runnerId}？之后如需再用这台机器，重新复制接入命令执行即可。`,confirmText:'移除',isDanger:true}))return;
  try{
    await api(`/api/runners/${encodeURIComponent(runnerId)}`,{method:'DELETE'});
    toast('已移除','ok');refreshRunners(true);
  }catch(err){toast(err.message)}
}
// Mirrors domain.Runner.DefaultSelectionBlocker so the page and
// PUT /api/runners/default refuse the same machines with the same reason.
function runnerVersionAtLeast(have,want){
  const parse=v=>{const parts=String(v||'').trim().replace(/^v/,'').split('.');return parts.every(p=>/^\d+$/.test(p))?parts.map(Number):null};
  const h=parse(have),w=parse(want);
  if(!w)return true;
  if(!h)return false;
  for(let i=0;i<w.length;i++){const hv=h[i]||0;if(hv!==w[i])return hv>w[i]}
  return true;
}
function runnerDefaultBlocker(r,min=state.minRunnerVersion||''){
  if(r.status!=='online')return '机器离线';
  if(min&&!runnerVersionAtLeast(r.runnerVersion,min))return `执行机器版本 ${r.runnerVersion||'未知'} 过低，需升级至 ${min} 及以上`;
  return '';
}
let runnerPreferenceSaving=false;
async function setDefaultRunner(runnerId){
  if(runnerPreferenceSaving)return;
  runnerPreferenceSaving=true;
  try{
    const out=await api('/api/runners/default',{method:'PUT',body:JSON.stringify({runnerId})});
    notifyExecutionSettingsChanged();
    toast(out?.message||'已更新','ok');await refreshRunners(true);
  }catch(err){toast(err.message)}
  finally{runnerPreferenceSaving=false}
}
function toggleRunnerRuntime(runnerId,provider,enabled){
  return runnerRuntimeRequest(runnerId,provider,enabled?'online':'disabled');
}
async function runnerRuntimeRequest(runnerId,provider,status){
  try{
    await api('/api/runners/runtimes',{method:'PUT',body:JSON.stringify({runnerId,provider,status})});
    toast(`${runnerCLILabels[provider]||provider} 已${status==='online'?'启用':'停用'}`,'ok');refreshRunners(true);
  }catch(err){toast(err.message);refreshRunners(true)}
}
async function setRunnerRuntimeAuth(runnerId,provider,authMode){
  const key=`${runnerId}|${provider}`;
  runnerAuthChanges.set(key,authMode);
  renderRunnersView();
  try{
    await api('/api/runners/runtimes',{method:'PUT',body:JSON.stringify({runnerId,provider,authMode})});
    const ready=runnerPersonalCredentialConfigured(provider);
    toast(authMode==='personal'?(ready?'将使用「个人配置」中的 API Key':'已切换，请先到「个人配置」补充该 CLI 的 API Key'):'将使用这台机器上已登录的 CLI 账号',ready||authMode!=='personal'?'ok':undefined);
  }catch(err){toast(err.message,'err')}
  finally{
    await refreshRunners(true);
    runnerAuthChanges.delete(key);
    renderRunnersView();
  }
}
async function testRunnerRuntime(runnerId,provider){
  const key=`${runnerId}|${provider}`;
  if(runnerRuntimeTests.has(key)||runnerAuthChanges.has(key))return;
  runnerRuntimeTests.add(key);
  renderRunnersView();
  try{
    await api('/api/runners/runtimes/test',{method:'POST',silent:true,body:JSON.stringify({runnerId,provider})});
    toast(`${runnerCLILabels[provider]} 测试通过`,'ok');
  }catch(err){toast(`${runnerCLILabels[provider]}：${err.message}`,'err',5000)}
  finally{
    runnerRuntimeTests.delete(key);
    renderRunnersView();
  }
}

function closeRunnerRuntimeMenu(restoreFocus=false){
  if(!runnerRuntimeTrigger)return;
  const trigger=runnerRuntimeTrigger;
  runnerRuntimeTrigger=null;
  runnerRuntimeMenu.hidden=true;
  trigger.setAttribute('aria-expanded','false');
  window.HuiPopover?.release(closeRunnerRuntimeMenu);
  if(restoreFocus&&trigger.isConnected)trigger.focus({preventScroll:true});
}
function showRunnerRuntimeMenu(trigger,last=false){
  if(runnerRuntimeTrigger===trigger)return closeRunnerRuntimeMenu(true);
  closeRunnerRuntimeMenu();
  const [id,provider]=trigger.dataset.runnerRuntimeMenu.split('|'),key=trigger.dataset.runnerRuntimeMenu;
  const runner=(state.runners||[]).find(item=>item.id===id);
  const runtime=runner?.runtimes?.find(item=>item.provider===provider);
  if(!runtime)return;
  if(!runnerRuntimeMenu){
    runnerRuntimeMenu=document.createElement('div');
    runnerRuntimeMenu.className='issue-action-popover runner-runtime-menu';
    runnerRuntimeMenu.setAttribute('role','menu');
    document.body.appendChild(runnerRuntimeMenu);
  }
  const menu=runnerRuntimeMenu;
  const testBusy=runnerRuntimeTests.has(key),updateBusy=runnerMaintenancePending.has(key);
  const actions=[
    {label:testBusy?'测试中…':'测试',icon:'flask',disabled:testBusy||runnerAuthChanges.has(key)||runner.status!=='online'||runtime.status==='disabled',run:()=>testRunnerRuntime(id,provider)},
    {label:updateBusy?'更新中…':'更新',icon:'download',disabled:updateBusy||runner.status!=='online',run:()=>updateRunnerCLI(runner,runtime)},
    {label:'模型列表',icon:'layers',disabled:runnerAuthChanges.has(key),run:()=>showRunnerModels(runner,runtime)}
  ];
  menu.innerHTML=actions.map((action,index)=>`<button type="button" role="menuitem" tabindex="-1" class="issue-popover-item" data-action="${index}" ${action.disabled?'disabled':''}>${huiIcon(action.icon)}${action.label}</button>`).join('');
  menu.setAttribute('aria-label',`${runnerCLILabels[provider]} 更多操作`);
  menu.hidden=false;
  runnerRuntimeTrigger=trigger;
  trigger.setAttribute('aria-expanded','true');
  const enabled=[...menu.querySelectorAll('button:not(:disabled)')];
  menu.onclick=e=>{
    const item=e.target.closest('[data-action]');
    if(!item||item.disabled)return;
    closeRunnerRuntimeMenu(true);
    actions[Number(item.dataset.action)].run();
  };
  menu.onkeydown=e=>{
    const at=enabled.indexOf(document.activeElement),step=e.key==='ArrowUp'?-1:1;
    if(e.key==='Escape'){e.preventDefault();e.stopPropagation();closeRunnerRuntimeMenu(true)}
    else if(e.key==='Tab'){closeRunnerRuntimeMenu(true)}
    else if(e.key==='ArrowDown'||e.key==='ArrowUp'){
      e.preventDefault();enabled[(at+step+enabled.length)%enabled.length]?.focus();
    }else if(e.key==='Home'||e.key==='End'){
      e.preventDefault();enabled[e.key==='Home'?0:enabled.length-1]?.focus();
    }
  };
  window.HuiPopover?.claim(closeRunnerRuntimeMenu,target=>menu.contains(target)||trigger.contains(target));
  window.HuiPopover?.place(trigger,menu,{width:166,align:'end'});
  window.AgentFlowHarnessIcons?.hydrate?.(menu);
  (last?enabled.at(-1):enabled[0])?.focus({preventScroll:true});
}
function showRunnerModels(runner,runtime){
  const provider=runtime.provider,local=runnerRuntimeAuthMode(runtime)==='local';
  const separateCatalog=local&&(provider==='codex'||provider==='claude-code');
  const kind=CREDENTIAL_KINDS.find(item=>item.models?.engine===provider);
  if(!kind)return;
  return afOpenDialog(modal=>{
    $('#confirmTitle').textContent=`${runnerCLILabels[provider]} 模型列表`;
    const keySource=kind.extra?.url?'API Key 和服务地址':kind.extra?'API Key 和网络环境':'API Key';
    $('#confirmMessage').textContent=separateCatalog
      ?'用于这台机器的账号登录。清空目录后使用 CLI 自带的模型。'
      :local
      ?'与个人配置共用模型目录。获取时使用这台机器的账号登录。'
      :`与个人配置共用模型目录。获取时使用个人配置里的 ${keySource}。`;
    const ok=$('#confirmOkBtn'),cancel=$('#confirmCancelBtn'),card=modal.querySelector('.confirm-modal-card');
    const panel=document.createElement('div');
    panel.className='credential-panel runner-model-editor';
    panel.dataset.credentialRow=kind.key;
    const savedModels=separateCatalog?runner.loginModels?.[provider]||[]:undefined;
    panel.innerHTML=credentialCatalogHTML(kind,credentialUser(),{runnerLogin:local,label:local?'账号登录':'API Key',savedModels});
    modal.querySelector('.confirm-modal-footer').before(panel);
    card.classList.add('runner-model-card');
    const catalog=panel.querySelector('[data-credential-catalog]');
    loadCredentialEffortCatalogs().then(()=>refreshCredentialCatalogEfforts(catalog));
    if(local){
      catalog.probeModels=()=>api(`/api/runners/${encodeURIComponent(runner.id)}/models/probe`,{
        method:'POST',body:JSON.stringify({provider}),silent:true
      });
    }
    ok.textContent='保存';ok.className='button primary';ok.disabled=true;
    cancel.textContent='取消';
    setConfirmIcon('info','layers');
    let saving=false;
    const syncSave=()=>{if(!saving)ok.disabled=!credentialCatalogDirty(catalog)};
    catalog.onCatalogChange=syncSave;
    catalog.oninput=event=>{
      if(event.target.closest('[data-catalog-probe]'))return syncCredentialCatalogProbe(catalog);
      event.target.closest('.credential-input')?.classList.remove('is-bad');
      syncCredentialCatalogEfforts(event.target.closest('[data-catalog-row]'));
      syncSave();
    };
    catalog.onclick=event=>{
      const button=event.target.closest('[data-catalog-act]');
      if(button&&!button.disabled)credentialCatalogAction(button.dataset.catalogAct,panel,catalog,button);
    };
    catalog.onpointerdown=event=>{
      const grip=event.target.closest('[data-catalog-grip]');
      if(grip)startCredentialCatalogDrag(event,grip);
    };
    catalog.onkeydown=event=>{
      credentialCatalogKeydown(event);
      if(event.key==='Enter'&&event.target.tagName==='INPUT'){
        event.preventDefault();
        event.stopPropagation();
        if(event.target.matches('[data-catalog-field]')&&!ok.disabled)ok.click();
      }
    };
    const submit=async event=>{
      event.stopImmediatePropagation();
      if(saving)return;
      const models=credentialCatalogPayload(catalog);
      if(!models||!validateCredentialCatalog(catalog,models))return;
      saving=true;
      ok.disabled=true;
      cancel.disabled=true;
      ok.textContent='保存中…';
      catalog.inert=true;
      try{
        if(separateCatalog){
          const updated=await api(`/api/runners/${encodeURIComponent(runner.id)}/models`,{
            method:'PUT',body:JSON.stringify({provider,models})
          });
          state.runners=(state.runners||[]).map(item=>item.id===runner.id?{...updated,canUseInProject:item.canUseInProject}:item);
          $('#chatNextFrame')?.contentWindow?.postMessage({type:'agentflow-environments-changed'},location.origin);
        }else{
          await saveCredential(kind.key,{models});
        }
        toast('模型列表已保存','ok');
        if(panel.isConnected)confirmPromiseResolve?.(true);
      }catch(err){
        if(panel.isConnected){
          toast(err.message,'err');
          ok.textContent='保存';ok.disabled=false;cancel.disabled=false;catalog.inert=false;
        }
      }finally{saving=false}
    };
    hydrateCredentialIcons(panel);
    ok.addEventListener('click',submit,true);
    return {focus:catalog.querySelector('[data-catalog-act="probe"]'),value:()=>{
      ok.removeEventListener('click',submit,true);
      ok.disabled=false;cancel.disabled=false;
      panel.remove();
      card.classList.remove('runner-model-card');
    }};
  });
}
function runnerMaintenancePath(id,provider,status=false){
  const params=new URLSearchParams();
  if(provider)params.set('provider',provider);
  if(status)params.set('status','1');
  return `/api/runners/${encodeURIComponent(id)}/maintenance${params.size?'?'+params.toString():''}`;
}
async function checkRunnerUpdate(id){
  if(runnerMaintenancePending.has(id))return;
  runnerMaintenancePending.add(id);
  renderRunnersView();
  try{
    const result=await api(runnerMaintenancePath(id),{silent:true});
    if(result.status==='updating'||result.status==='restarting'){
      await followRunnerUpdate(id);
    }else if(result.status==='available'&&result.canUpdate){
      const confirmed=await afConfirm({title:'Runner 有可用更新',message:`当前版本 ${result.current||'未知'}。更新会重启空闲的 Runner。`,confirmText:'立即更新',info:true});
      if(confirmed){
        await api(runnerMaintenancePath(id),{method:'POST',silent:true});
        await followRunnerUpdate(id);
      }
    }else if(result.status==='current'){
      toast(`Runner ${result.current||''} 已是平台当前版本`.replace('  ',' '),'ok');
    }else{
      toast(result.message||'暂时无法检查更新','err',5000);
    }
  }catch(err){toast(err.message,'err',5000)}
  finally{
    runnerMaintenancePending.delete(id);
    renderRunnersView();
  }
}
async function updateRunnerCLI(runner,runtime){
  const id=runner.id,provider=runtime.provider,label=runnerCLILabels[provider],key=`${id}|${provider}`;
  if(!runtime.capabilities?.some(capability=>capability==='native_update'||capability==='managed_update')){
    await afAlert({title:`更新 ${label}`,message:'请在这台执行机器上使用原来的安装方式更新。更新后刷新机器列表即可看到版本。'});
    return;
  }
  if(runnerMaintenancePending.has(key))return;
  runnerMaintenancePending.add(key);
  renderRunnersView();
  try{
    await api(runnerMaintenancePath(id,provider),{method:'POST',silent:true});
    await followRunnerUpdate(id,provider);
  }catch(err){toast(err.message,'err',5000)}
  finally{
    runnerMaintenancePending.delete(key);
    renderRunnersView();
  }
}
async function followRunnerUpdate(id,provider){
  const label=provider?runnerCLILabels[provider]:'Runner';
  const deadline=Date.now()+(provider?45:8)*60000;
  while(Date.now()<deadline){
    await new Promise(resolve=>setTimeout(resolve,3000));
    const result=await api(runnerMaintenancePath(id,provider,true),{silent:true}).catch(()=>null);
    if(!result||result.status==='updating'||result.status==='restarting')continue;
    if(result.status==='current'||result.status==='updated'){
      const version=result.version||result.current;
      toast(`${label}${version?' '+version:''} 已就绪`,'ok');
      await refreshRunners(true);
    }else if(provider&&result.status==='manual'){
      await afAlert({title:`更新 ${label}`,message:result.message||'请在执行机器上使用该 CLI 原来的安装方式更新'});
    }else if(result.status==='ready'){
      toast(`${label} 更新状态已中断，请刷新确认版本`,'err',5000);
    }else{
      toast(result.message||`${label} 还有可用更新，请再次检查`,result.status==='failed'?'err':'info',5000);
    }
    return;
  }
  toast(`等待 ${label} 更新超时，请稍后检查版本`,'err',5000);
}
