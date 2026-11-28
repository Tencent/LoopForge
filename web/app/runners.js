function syncRunModalKnowledgeSummary(){
  const summary=$('#runKnowledgeRefsSummary'),reset=$('#runKnowledgeRefsResetBtn');
  const override=state.runModalKnowledgeRefs;
  if(summary){
    if(override===null||override===undefined)summary.textContent=lfT('默认按节点或角色配置','Defaults to each step or role');
    else if(override.length)summary.textContent=lfT(`本次覆盖：已选 ${override.length} 个连接器`,`Override for this run: ${override.length} ${override.length===1?'connector':'connectors'} selected`);
    else summary.textContent=lfT('本次覆盖：不挂载知识库','Override for this run: no knowledge base');
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
    if(typeof window.editKnowledgeRefs!=='function')return toast(lfT('知识库选择器尚未加载','The knowledge base picker has not loaded yet'));
    window.editKnowledgeRefs(currentProject()?.id||state.projectFilter||'',state.runModalKnowledgeRefs||[],refs=>{
      state.runModalKnowledgeRefs=refs;
      syncRunModalKnowledgeSummary();
    });
  };
  const resetBtn=$('#runKnowledgeRefsResetBtn');
  if(resetBtn)resetBtn.onclick=()=>{state.runModalKnowledgeRefs=null;syncRunModalKnowledgeSummary()};
  $('#confirmRun').disabled=!workflows.length;
  $('#runModal').classList.add('open');
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
    state.projectDefaultRunnerId=def?.projectRunnerId||'';
    state.projectRunnerMode=def?.projectRunnerMode||'';
    state.platformRunner=def?.platformRunner||null;
    state.cloudPool=def?.cloudPool||null;
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
  return limit>0?lfT(`最多同时运行 ${limit} 条`,`Up to ${limit} concurrent ${limit===1?'run':'runs'}`):lfT('不限同时运行数','No concurrency limit');
}
function runnerRunLimitHTML(runner,platform=false){
  if(platform?!runner.canEditLimit:!(Number(runner?.ownerUserId)>0))return '';
  const limit=Number(runner?.runLimit)||0;
  const scope=platform
    ?lfT('默认 10 条，平台所有项目共用此名额。按顶层 LOOP 计数。','Defaults to 10, shared by every project on the platform. Counts top-level LOOPs. ')
    :lfT('新绑定机器默认 10 条。按顶层 LOOP 计数，共享项目共用机器名额。','Newly bound machines default to 10. Counts top-level LOOPs; shared projects share the machine\'s slots. ');
  const tip=scope+lfT('超出名额后排队。失败、暂停、待确认的 LOOP 空闲 1 小时后自动归档并回收工作区，释放名额。继续或重试时自动恢复工作区。输入 0 或留空表示不限。','Runs over the limit queue. Failed, paused and awaiting-confirmation LOOPs idle for 1 hour are archived and their workspaces reclaimed to free slots; continuing or retrying restores the workspace. Enter 0 or leave blank for no limit.');
  const id=platform?'runLimit-platform-runner':`runLimit-${escapeHTML(runner.id)}`;
  const control=platform?'data-platform-run-limit':`data-runner-run-limit="${escapeHTML(runner.id)}"`;
  return `<div class="runner-run-limit"><label for="${id}">${lfT('最多同时运行','Max concurrent')}<input id="${id}" type="number" inputmode="numeric" min="0" step="1" placeholder="${lfT('不限','Unlimited')}" value="${limit>0?limit:''}" ${control} data-i18n-data aria-label="${escapeHTML(platform?platformRunnerLabel():runner.name||runner.id)} ${lfT('最多同时运行条数','max concurrent runs')}">${lfT('条','runs')}</label><span class="runner-tip" tabindex="0" role="note" aria-label="${tip}" data-tip="${tip}">?</span></div>`;
}
async function saveRunnerRunLimit(runnerId,raw,platform=false){
  const text=String(raw??'').trim();
  const value=text===''?0:Number(text);
  if(!Number.isSafeInteger(value)||value<0){toast(lfT('请输入非负整数，0 或留空表示不限','Enter a non-negative integer; 0 or blank means no limit'));refreshRunners(true);return}
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
    toast(value>0?lfT(`已设置：最多同时运行 ${value} 条`,`Saved: up to ${value} concurrent ${value===1?'run':'runs'}`):lfT('已设为不限','Set to unlimited'),'ok');renderRunnersView();
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
    parts.push(`${lfT('运行中','Running')} ${Number(runner.load.runningRuns)||0}`);
    parts.push(limit>0?`${lfT('已占','Occupied')} ${Number(runner.load.occupiedRuns)||0} / ${limit}`:lfT('不限','Unlimited'));
    if(runner.load.queuedRuns>0)parts.push(`${lfT('排队','Queued')} ${Number(runner.load.queuedRuns)}`);
  }
  if(runner.cloudPool)parts.push(lfT(`云端池 ${Number(runner.cloudPoolOnline)||0} 台在线`,`Cloud pool: ${Number(runner.cloudPoolOnline)||0} online`));
  return parts.length?`<small class="runner-load">${parts.join(' · ')}</small>`:'';
}
function runnerStatusLabel(status,lastSeen){
  const label=status==='online'?lfT('在线','Online'):status==='offline'?lfT('离线','Offline'):(status||lfT('未知','Unknown'));
  const style=status==='online'?' online':status==='offline'?' offline':'';
  const seen=lastSeen?` title="${escapeHTML(runnerLastSeen(lastSeen))}"`:'';
  return `<span class="runner-status${style}"${seen}><i aria-hidden="true"></i>${escapeHTML(label)}</span>`;
}
const runnerCLILabels={'claude-code':'Claude Code','codex':'Codex','codebuddy':'CodeBuddy',opencode:'OpenCode',cursor:'Cursor CLI',agy:'Antigravity',kimi:'Kimi Code',pi:'Pi',dsh:'DeepSeek Harness',zcode:'ZCode'};
// Getters: the page language is read when the copy is shown, not when this file loads.
const runnerAuthLabel={get local(){return lfT('账号登录','Account login')},personal:'API Key'};
const runnerAuthDescription={
  get local(){return lfT('Claude Code、Codex 和 Antigravity 可以使用运行 Runner 的系统账号在这台机器上的已有登录，对话与 LOOP 的 native、Docker 沙箱均可复用。共享对话中，只有机器所有者发言时会使用此登录。','Claude Code, Codex and Antigravity can reuse the login of the system user running the Runner, in both native and Docker sandboxes for chats and LOOPs. In a shared chat, that login applies to messages from the machine owner.')},
  get personal(){return lfT('读取「个人配置」中保存的 API Key。对话使用当前发言人的 Key，LOOP 使用运行责任人的 Key。Antigravity 的 Gemini API Key 按 API 单独计费。','Uses the API Key saved in Personal settings. Chats use the current speaker\'s key; LOOPs use the run owner\'s key. Antigravity\'s Gemini API Key is billed separately per API call.')},
};
const runnerOSLabels={darwin:'macOS',linux:'Linux',windows:'Windows'};
function runnerKeyDescription(provider){
  if(provider==='zcode')return lfT('GLM Coding Plan 的 API Key 配合套餐专用地址可使用订阅额度，请在「个人配置」中填写。Start Plan 额度需要登录 ZCode 账号，无法通过 API Key 使用。','Enter the GLM Coding Plan API key and its dedicated endpoint in Personal Settings to use your subscription allowance. Start Plan allowance requires signing in to ZCode.');
  return lfT('该 CLI 支持 API Key。使用服务商订阅套餐提供的 Key，执行用量会计入对应的套餐额度。请在「个人配置」中填写 API Key。','This CLI supports API keys. With a key included in your provider\'s subscription, usage counts toward that plan\'s allowance. Enter the API key in Personal Settings.');
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
  const title=missing?` title="${lfT('请在个人配置中填写 API Key','Enter an API Key in Personal settings')}"`:'';
  if(!shared&&supportsLogin)return `<select class="runner-auth-select${mode==='personal'?' is-personal':''}" data-runner-auth="${escapeHTML(key)}" aria-label="${runnerCLILabels[runtime.provider]} ${lfT('认证方式','authentication')}"${title} ${busy?'disabled':''}>
    <option value="local" ${mode==='local'?'selected':''}>${runnerAuthLabel.local}</option>
    <option value="personal" ${mode==='personal'?'selected':''}>${runnerAuthLabel.personal}</option>
  </select>`;
  const keyHelp=supportsLogin?'':`<button class="runner-auth-tip" type="button" data-runner-key-help="${runtime.provider}" aria-label="${runnerCLILabels[runtime.provider]} ${lfT('API Key 说明','API Key help')}" aria-haspopup="dialog" title="${escapeHTML(runnerKeyDescription(runtime.provider))}"><span aria-hidden="true">?</span></button>`;
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
      <input type="checkbox" data-runner-cli="${safeKey}" ${disabled?'':'checked'} ${shared?'disabled':''} aria-label="${lfT('启用','Enable')} ${label}">
      <span class="runner-runtime-icon ${provider}" aria-hidden="true">${icon?`<img src="${icon}" alt="">`:''}</span>
      <span class="runner-runtime-name"><b>${label}</b><small title="${escapeHTML(runtime.binaryPath||'')}">${escapeHTML(runtime.version||runtime.binaryPath||lfT('已发现','Detected'))}</small></span>
    </label>
    ${runnerAuthHTML(runtime,key,busy,shared)}
    ${shared?'':`<button class="runner-more" type="button" data-runner-runtime-menu="${safeKey}" aria-label="${label} ${lfT('更多操作','more actions')}" aria-haspopup="menu" aria-expanded="false" aria-busy="${testing||updating}">${huiIcon(testing||updating?'arc-spinner':'more-horizontal')}</button>`}
  </div>`;
}
function runnerLastSeen(value){
  if(!value)return lfT('尚无心跳','No heartbeat yet');
  const seconds=Math.max(0,Math.round((Date.now()-new Date(value).getTime())/1000));
  if(seconds<60)return lfT(`${seconds} 秒前`,`${seconds}s ago`);
  if(seconds<3600)return lfT(`${Math.floor(seconds/60)} 分钟前`,`${Math.floor(seconds/60)} min ago`);
  return new Date(value).toLocaleString();
}
function runnerCopyCommand(btn,text,message){
  const done=()=>{
    toast(message,'ok');
    if(!btn.dataset.label)btn.dataset.label=btn.textContent;
    btn.textContent=lfT('已复制','Copied');btn.classList.add('copied');
    clearTimeout(btn._copiedTimer);
    btn._copiedTimer=setTimeout(()=>{btn.textContent=btn.dataset.label;btn.classList.remove('copied')},1400);
  };
  const failed=()=>toast(lfT('复制失败，请手动选择复制','Copy failed; select the text and copy it manually'));
  if(!navigator.clipboard){failed();return}
  navigator.clipboard.writeText(text).then(done).catch(failed);
}
function runnerProjectWritable(project){
  const userId=Number(state.currentUser?.id)||0;
  return !!(userId>0&&project&&!project.archivedAt&&(Number(project.ownerId)===userId||
    project.members?.some(m=>Number(m.userId)===userId&&String(m.role||'').trim().toLowerCase()!=='viewer')));
}
// The server always names the platform runner domain.PlatformRunnerName; show it in the page language.
function platformRunnerLabel(){
  return lfT('平台执行机器（云端）','Platform runner (cloud)');
}
function platformRunnerCardHTML(runner,isDefault,canSetDefault){
  const name=escapeHTML(platformRunnerLabel());
  const limit=runner.canEditLimit?runnerRunLimitHTML(runner,true):`<span class="runner-limit-label">${runnerConcurrencyLabel(runner)}</span>`;
  return `<article class="runner-card runner-platform-card panel ${isDefault?'is-default':''}">
    <div class="runner-platform-main">
      <span class="runner-machine-icon" aria-hidden="true">${huiIcon('server')}</span>
      <div class="runner-card-title">
        <div class="runner-card-name"><b title="${name}">${name}</b><span class="runner-tag">${lfT('平台自带','Built-in')}</span>${isDefault?`<span class="runner-tag default">${lfT('此项目默认','Project default')}</span>`:''}</div>
        ${runnerLoadHTML(runner)}
      </div>
    </div>
    <div class="runner-platform-controls">
      ${runnerStatusLabel(runner.status)}
      <span class="runner-platform-sep" aria-hidden="true"></span>
      ${limit}
      ${runnerPrivilegedSandboxHTML(runner,runner.canEditLimit,true)}
      ${canSetDefault?`<button type="button" class="button small ${isDefault?'ghost runner-text-button':'hui-btn-soft'}" data-runner-platform-default>${isDefault?'清除项目默认':'设为项目默认'}</button>`:''}
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
  if(tokenSpan)tokenSpan.textContent=state.runnerBindCode?.code||lfT('生成中…','Generating…');
  const copyBtn=$('#copyBindCommand');
  if(copyBtn)copyBtn.onclick=()=>{
    const text=$('#bindCliCommand')?.innerText||'';
    runnerCopyCommand(copyBtn,text,lfT('已复制，10 分钟内有效','Copied; valid for 10 minutes'));
  };
  const uninstallHTTPS=$('#uninstallHttpsOrigin'),uninstallHTTP=$('#uninstallHttpOrigin');
  if(uninstallHTTPS)uninstallHTTPS.textContent=httpsOrigin;if(uninstallHTTP)uninstallHTTP.textContent=httpOrigin;
  const copyUnBtn=$('#copyUninstallCommand');
  if(copyUnBtn)copyUnBtn.onclick=(e)=>{
    const text=e.currentTarget.parentElement?.querySelector('pre')?.innerText||'';
    runnerCopyCommand(copyUnBtn,text,lfT('已复制卸载命令','Uninstall command copied'));
  };
  const project=currentProject();
  const projectId=project?.id||'';
  const projectLoaded=state.runnerProjectId===projectId;
  const canSetMode=projectLoaded&&!!projectId&&runnerProjectWritable(project);
  const projectMode=projectLoaded?state.projectRunnerMode||'':'';
  const platform=projectLoaded?state.platformRunner:null;
  const own=r=>r.ownerUserId===state.currentUser?.id;
  const runners=(state.runners||[]).filter(r=>own(r)||projectLoaded);
  const personalDefaultId=state.personalDefaultRunnerId??state.defaultRunnerId??'';
  const projectDefaultId=projectLoaded?state.projectDefaultRunnerId||'':'';
  const defaultId=projectLoaded?state.defaultRunnerId||'':'';
  $('#runnerCount').textContent=String(runners.length);
  const dispatchBar=$('#runnerDispatchBar');
  if(dispatchBar)dispatchBar.classList.toggle('is-auto',projectMode==='auto');
  const dispatchLabel=$('#runnerDispatchLabel');
  if(dispatchLabel)dispatchLabel.textContent=projectId?lfT('本项目执行调度','Project dispatch'):lfT('当前执行调度','Execution dispatch');
  const dispatchHint=$('#runnerDispatchHint');
  if(dispatchHint){
    dispatchHint.hidden=!canSetMode;
    dispatchHint.textContent=!canSetMode?'':projectMode==='auto'
      ?lfT('已开启自动分配：新建顶层 LOOP 将在「平台机器」和兼容的「可用机器」间自动调度。','Auto-assign is on: new top-level LOOPs are scheduled across the platform machine and compatible available runners.')
      :lfT('开启「自动分配」后，新建顶层 LOOP 将在「平台机器」和兼容的「可用机器」间自动调度；也可在下方指定单台机器为项目默认。','Turn on Auto-assign to schedule new top-level LOOPs across the platform machine and compatible available runners, or set a single runner below as the project default.');
  }
  const autoAssign=$('#runnerAutoAssign');
  if(autoAssign){
    const active=projectMode==='auto';
    autoAssign.hidden=!canSetMode;
    autoAssign.classList.toggle('hui-btn-soft',active);
    autoAssign.classList.toggle('ghost',!active);
    autoAssign.setAttribute('aria-pressed',String(active));
    autoAssign.textContent=active?lfT('已开启自动分配','Auto-assign on'):lfT('自动分配','Auto-assign');
    autoAssign.title=lfT('自动分配涵盖平台机器与兼容的可用机器','Auto-assign covers the platform machine and compatible available runners');
    autoAssign.onclick=()=>setDefaultRunner('',projectId,active?'':'auto');
  }
  const poolStatus=$('#runnerPoolStatus');
  if(poolStatus){
    const def=runners.find(r=>r.id===defaultId),pool=state.cloudPool;
    let text='',warn=false;
    if(projectMode==='auto'){
      const usable=runners.filter(r=>r.canUseInProject&&!runnerDefaultBlocker(r)).length+(platform?.status==='online'?1:0);
      text=lfT(`此项目 LOOP 自动分配 · ${usable} 台在线机器可用`,`Project LOOPs auto-assigned · ${usable} online ${usable===1?'runner':'runners'} available`);
      warn=usable===0;
    }else if(projectMode==='platform'){
      text=`${lfT('此项目默认','Project default')} · ${platformRunnerLabel()}`;
      warn=platform?.status==='offline';
    }else if(def){
      warn=def.status!=='online';
      const label=projectDefaultId?lfT('此项目默认','Project default'):lfT('个人默认','Personal default');
      text=warn?lfT(`${label}执行机器已离线，请启动机器或更改默认`,`${label} runner is offline; start it or change the default`):`${label} · ${def.name||def.id}`;
    }else if(platform){
      warn=platform.status!=='online';
      text=warn?lfT('平台执行机器离线','Platform runner offline'):lfT('在平台云端执行','Runs in the platform cloud');
    }else if(pool?.configured){
      warn=!pool.online;
      text=pool.online?lfT(`云端执行 · ${pool.online} 台在线`,`Cloud execution · ${pool.online} online`):lfT('云端暂无在线机器，新任务最多等待 10 分钟','No cloud runner online; new tasks wait up to 10 minutes');
    }else{
      text=lfT('在平台云端执行','Runs in the platform cloud');
    }
    poolStatus.textContent=text;poolStatus.classList.toggle('warn',warn);
  }
  const focusedMenu=document.activeElement?.dataset.runnerRuntimeMenu;
  const focusedUpdate=document.activeElement?.dataset.runnerCheckUpdate;
  const focusedShare=document.activeElement?.dataset.runnerShare;
  const focusedPrivilege=document.activeElement?.dataset.runnerPrivilegedSandbox;
  const focusedPlatform=document.activeElement?.hasAttribute('data-runner-platform-default');
  closeRunnerRuntimeMenu();
  const platformSection=$('#platformRunnerSection');
  const platformHost=$('#platformRunnerHost');
  if(platformSection&&platformHost){
    platformSection.hidden=!platform;
    platformHost.innerHTML=platform?platformRunnerCardHTML(platform,projectMode==='platform',canSetMode):'';
  }
  const runnerCards=runners.map(r=>{
    const isDefault=r.id===defaultId;
    const discoveredCLIs=(r.runtimes||[]).filter(rt=>runnerCLILabels[rt.provider]);
    const shared=!own(r);
    const isPersonalDefault=r.id===personalDefaultId;
    const isProjectDefault=r.id===projectDefaultId;
    const canSetProjectDefault=projectLoaded&&projectId&&r.canUseInProject&&!isProjectDefault;
    const cliRows=discoveredCLIs.map(rt=>runnerRuntimeHTML(r,rt,shared)).join('');
    const localAgent=!shared&&(r.runtimes||[]).some(rt=>rt.provider==='a2a'&&rt.status!=='disabled');
    const agentRow=localAgent?'<div class="runner-runtime-row"><span class="runner-runtime-name"><b>'+lfT('本机 A2A Agent','Local A2A Agent')+'</b></span><span class="runner-auth-fixed">'+lfT('本机服务','Local service')+'</span></div>':'';
    const cliSection=(cliRows+agentRow)||`<div class="runner-runtime-empty">${lfT('未发现支持的 Agent CLI','No supported Agent CLI found')}</div>`;
    const osLabel=runnerOSLabels[r.os]||r.os||'?';
    const defaultBlocker=runnerDefaultBlocker(r);
    const checking=runnerMaintenancePending.has(r.id);
    return `<article class="runner-card panel ${isDefault?'is-default':''}">
      <div class="runner-card-head">
        <span class="runner-machine-icon" aria-hidden="true">${huiIcon('monitor')}</span>
        <div class="runner-card-title">
          <div class="runner-card-name"><b title="${escapeHTML(r.name||r.id)}" data-i18n-data>${escapeHTML(r.name||r.id)}</b>${isProjectDefault?`<span class="runner-tag default">${lfT('此项目默认','Project default')}</span>`:isPersonalDefault?`<span class="runner-tag default">${lfT('个人默认','Personal default')}</span>`:''}${shared?`<span class="runner-tag">${lfT('项目共享','Shared with project')}</span>`:''}</div>
          <small>${escapeHTML(osLabel)} · ${escapeHTML(r.arch||'?')} · Runner ${escapeHTML(r.runnerVersion||lfT('未知','unknown'))}</small>
          ${runnerLoadHTML(r)}
        </div>
        <div class="runner-head-actions">${runnerStatusLabel(r.status,r.lastSeenAt)}${shared?'':`<button class="button ghost runner-check-update" type="button" data-runner-check-update="${escapeHTML(r.id)}" ${r.status!=='online'?'disabled':checking?'aria-disabled="true"':''}>${huiIcon(checking?'arc-spinner':'refresh')}${checking?lfT('处理中…','Working…'):lfT('检查更新','Check for updates')}</button>`}</div>
      </div>
      <div class="runner-runtime-list">
        ${cliRows?`<div class="runner-runtime-heading"><span>CLI</span><button class="runner-auth-help" type="button" data-runner-auth-help aria-label="${lfT('模型认证说明','Model authentication help')}" aria-haspopup="dialog">${lfT('模型认证','Model auth')} ${huiIcon('info')}</button></div>`:''}
        ${cliSection}
      </div>
      <div class="runner-card-action">
        ${shared?`<span class="runner-limit-label">${runnerConcurrencyLabel(r)}</span>`:runnerRunLimitHTML(r)}
        ${runnerPrivilegedSandboxHTML(r,!shared)}
        <span class="runner-card-spacer"></span>
        ${shared?'':`<button class="button ghost small runner-text-button" type="button" data-runner-share="${escapeHTML(r.id)}" aria-haspopup="dialog">${lfT('共享项目','Shared projects')}${r.sharedProjectIds?.length?lfT(`（${r.sharedProjectIds.length}）`,` (${r.sharedProjectIds.length})`):''}</button>`}
        ${isPersonalDefault?'<button class="button ghost small runner-text-button" type="button" data-set-default="">清除个人默认</button>':''}
        ${shared||r.status==='online'?'':`<button class="button ghost small runner-text-button" type="button" data-remove-runner="${escapeHTML(r.id)}" title="机器已不再使用时移除绑定">移除</button>`}
        ${!shared&&!isPersonalDefault?`<button class="button hui-btn-soft small" type="button" ${defaultBlocker?`disabled title="${escapeHTML(defaultBlocker)}"`:''} data-set-default="${escapeHTML(r.id)}">设为个人默认</button>`:''}
        ${isProjectDefault?'<button class="button ghost small runner-text-button" type="button" data-set-project-default="">清除项目默认</button>':''}
        ${canSetProjectDefault?`<button class="button hui-btn-soft small" type="button" ${defaultBlocker?`disabled title="${escapeHTML(defaultBlocker)}"`:''} data-set-project-default="${escapeHTML(r.id)}">设为项目默认</button>`:''}
      </div>
    </article>`;
  }).join('');
  list.innerHTML=runnerCards||`<div class="runner-empty"><b>${lfT('还没有机器','No runners yet')}</b><span>${lfT('执行上方命令后自动出现','Runners appear here after you run the command above')}</span></div>`;
  const platformDefault=platformHost?.querySelector('[data-runner-platform-default]');
  if(platformDefault){
    platformDefault.onclick=()=>setDefaultRunner('',projectId,projectMode==='platform'?'':'platform');
    if(focusedPlatform)platformDefault.focus({preventScroll:true});
  }
  // Bind click handlers via JS (CSP blocks inline onclick attributes).
  list.querySelectorAll('[data-set-default]').forEach(btn=>btn.onclick=()=>{
    setDefaultRunner(btn.dataset.setDefault||'');
  });
  list.querySelectorAll('[data-set-project-default]').forEach(btn=>{
    btn.onclick=()=>setDefaultRunner(btn.dataset.setProjectDefault,projectId);
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
    title:lfT('模型认证方式','Model authentication'),
    message:`${runnerAuthLabel.local}\n${runnerAuthDescription.local}\n\n${runnerAuthLabel.personal}\n${runnerAuthDescription.personal}`
  }));
  list.querySelectorAll('[data-runner-key-help]').forEach(btn=>btn.onclick=()=>{
    const provider=btn.dataset.runnerKeyHelp;
    return afAlert({
      title:`${runnerCLILabels[provider]} ${lfT('API Key 说明','API Key help')}`,
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
    $('#confirmTitle').innerHTML=`${lfT('共享','Share')} <span data-i18n-data>${escapeHTML(runner.name||runner.id)}</span>`;
    $('#confirmMessage').textContent=projects.length
      ?lfT('所选项目的可写成员可以在这台机器上运行代码，执行时使用各自的凭据。','Members with write access to the selected projects can run code on this machine, each with their own credentials.')
      :lfT('加入项目并获得写权限后，即可共享这台机器。','Join a project with write access to share this machine with it.');
    const ok=$('#confirmOkBtn');
    ok.textContent='保存';
    ok.className='button primary';
    $('#confirmCancelBtn').textContent='取消';
    setConfirmIcon('info','monitor');
    const fields=document.createElement('fieldset');
    fields.className='runner-share-projects';
    fields.setAttribute('aria-label',lfT('共享项目','Shared projects'));
    fields.innerHTML=projects.map(p=>`<label><input type="checkbox" value="${escapeHTML(p.id)}" ${shared.has(p.id)?'checked':''}> <span data-i18n-data>${escapeHTML(p.name||p.id)}</span></label>`).join('');
    if(projects.length)modal.querySelector('.confirm-modal-footer').before(fields);
    return {focus:fields.querySelector('input')||ok,value:accepted=>{
      fields.remove();
      return accepted?[...fields.querySelectorAll('input:checked')].map(input=>input.value):null;
    }};
  });
  if(!projectIds)return;
  try{
    await api(`/api/runners/${encodeURIComponent(runner.id)}/shared-projects`,{method:'PUT',body:JSON.stringify({projectIds})});
    toast(lfT('已更新共享项目','Shared projects updated'),'ok');
    await refreshRunners(true);
  }catch(err){toast(err.message,'err')}
}
async function removeRunner(runnerId){
  const runner=(state.runners||[]).find(r=>r.id===runnerId);
  if(!await afConfirm({title:lfT('移除执行机器','Remove runner'),message:lfT(`确定移除 ${runner?.name||runnerId}？之后如需再用这台机器，重新复制接入命令执行即可。`,`Remove ${runner?.name||runnerId}? To use this machine again later, copy and run the connect command again.`),confirmText:lfT('移除','Remove'),isDanger:true}))return;
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
function runnerDefaultBlocker(r){
  if(r.status!=='online')return lfT('机器离线','Runner offline');
  const min=state.minRunnerVersion||'';
  if(min&&!runnerVersionAtLeast(r.runnerVersion,min))return lfT(`执行机器版本 ${r.runnerVersion||'未知'} 过低，需升级至 ${min} 及以上`,`Runner version ${r.runnerVersion||'unknown'} is too old; upgrade to ${min} or later`);
  return '';
}
let runnerPreferenceSaving=false;
async function setDefaultRunner(runnerId,projectId='',mode=''){
  if(runnerPreferenceSaving)return;
  runnerPreferenceSaving=true;
  try{
    const query=projectId?`?projectId=${encodeURIComponent(projectId)}`:'';
    const out=await api('/api/runners/default'+query,{method:'PUT',body:JSON.stringify(mode?{mode}:{runnerId,mode})});
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
    toast(status==='online'?lfT(`${runnerCLILabels[provider]||provider} 已启用`,`${runnerCLILabels[provider]||provider} enabled`):lfT(`${runnerCLILabels[provider]||provider} 已停用`,`${runnerCLILabels[provider]||provider} disabled`),'ok');refreshRunners(true);
  }catch(err){toast(err.message);refreshRunners(true)}
}
async function setRunnerRuntimeAuth(runnerId,provider,authMode){
  const key=`${runnerId}|${provider}`;
  runnerAuthChanges.set(key,authMode);
  renderRunnersView();
  try{
    await api('/api/runners/runtimes',{method:'PUT',body:JSON.stringify({runnerId,provider,authMode})});
    const ready=runnerPersonalCredentialConfigured(provider);
    toast(authMode==='personal'
      ?(ready?lfT('将使用「个人配置」中的 API Key','Will use the API Key from Personal settings'):lfT('已切换，请先到「个人配置」补充该 CLI 的 API Key','Switched; add an API Key for this CLI in Personal settings'))
      :lfT('将使用这台机器上已登录的 CLI 账号','Will use the CLI account signed in on this machine'),ready||authMode!=='personal'?'ok':undefined);
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
    toast(lfT(`${runnerCLILabels[provider]} 测试通过`,`${runnerCLILabels[provider]} test passed`),'ok');
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
    {label:testBusy?lfT('测试中…','Testing…'):lfT('测试','Test'),icon:'flask',disabled:testBusy||runnerAuthChanges.has(key)||runner.status!=='online'||runtime.status==='disabled',run:()=>testRunnerRuntime(id,provider)},
    {label:updateBusy?lfT('更新中…','Updating…'):lfT('更新','Update'),icon:'download',disabled:updateBusy||runner.status!=='online',run:()=>updateRunnerCLI(runner,runtime)},
    {label:lfT('模型列表','Models'),icon:'layers',disabled:runnerAuthChanges.has(key),run:()=>showRunnerModels(runner,runtime)}
  ];
  menu.innerHTML=actions.map((action,index)=>`<button type="button" role="menuitem" tabindex="-1" class="issue-popover-item" data-action="${index}" ${action.disabled?'disabled':''}>${huiIcon(action.icon)}${action.label}</button>`).join('');
  menu.setAttribute('aria-label',`${runnerCLILabels[provider]} ${lfT('更多操作','more actions')}`);
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
    $('#confirmTitle').textContent=`${runnerCLILabels[provider]} ${lfT('模型列表','models')}`;
    const keySource=kind.extra?.url?lfT('API Key 和服务地址','API Key and service URL'):kind.extra?lfT('API Key 和网络环境','API Key and network environment'):'API Key';
    $('#confirmMessage').textContent=separateCatalog
      ?lfT('用于这台机器的账号登录。清空目录后使用 CLI 自带的模型。','Used with this machine\'s account login. Clear the list to use the CLI\'s built-in models.')
      :local
      ?lfT('与个人配置共用模型目录。获取时使用这台机器的账号登录。','Shares the model list with Personal settings. Fetching uses this machine\'s account login.')
      :lfT(`与个人配置共用模型目录。获取时使用个人配置里的 ${keySource}。`,`Shares the model list with Personal settings. Fetching uses the ${keySource} from Personal settings.`);
    const ok=$('#confirmOkBtn'),cancel=$('#confirmCancelBtn'),card=modal.querySelector('.confirm-modal-card');
    const panel=document.createElement('div');
    panel.className='credential-panel runner-model-editor';
    panel.dataset.credentialRow=kind.key;
    const savedModels=separateCatalog?runner.loginModels?.[provider]||[]:undefined;
    panel.innerHTML=credentialCatalogHTML(kind,credentialUser(),{runnerLogin:local,label:local?runnerAuthLabel.local:'API Key',savedModels});
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
        toast(lfT('模型列表已保存','Model list saved'),'ok');
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
      const confirmed=await afConfirm({title:lfT('Runner 有可用更新','Runner update available'),message:lfT(`当前版本 ${result.current||'未知'}。更新会重启空闲的 Runner。`,`Current version ${result.current||'unknown'}. Updating restarts the Runner when it is idle.`),confirmText:lfT('立即更新','Update now'),info:true});
      if(confirmed){
        await api(runnerMaintenancePath(id),{method:'POST',silent:true});
        await followRunnerUpdate(id);
      }
    }else if(result.status==='current'){
      toast(lfT(`Runner ${result.current||''} 已是平台当前版本`,`Runner ${result.current||''} is already the platform version`).replace('  ',' '),'ok');
    }else{
      toast(result.message||lfT('暂时无法检查更新','Cannot check for updates right now'),'err',5000);
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
    await afAlert({title:lfT(`更新 ${label}`,`Update ${label}`),message:lfT('请在这台执行机器上使用原来的安装方式更新。更新后刷新机器列表即可看到版本。','Update it on this runner the way it was installed, then refresh the runner list to see the new version.')});
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
      toast(lfT(`${label}${version?' '+version:''} 已就绪`,`${label}${version?' '+version:''} is ready`),'ok');
      await refreshRunners(true);
    }else if(provider&&result.status==='manual'){
      await afAlert({title:lfT(`更新 ${label}`,`Update ${label}`),message:result.message||lfT('请在执行机器上使用该 CLI 原来的安装方式更新','Update this CLI on the runner the way it was installed')});
    }else if(result.status==='ready'){
      toast(lfT(`${label} 更新状态已中断，请刷新确认版本`,`${label} update status was interrupted; refresh to confirm the version`),'err',5000);
    }else{
      toast(result.message||lfT(`${label} 还有可用更新，请再次检查`,`${label} still has an update available; check again`),result.status==='failed'?'err':'info',5000);
    }
    return;
  }
  toast(lfT(`等待 ${label} 更新超时，请稍后检查版本`,`Timed out waiting for the ${label} update; check the version later`),'err',5000);
}
