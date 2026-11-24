function openRunModal(workflowId){const select=$('#runWorkflowChoice'),workflows=runnableWorkflowOptions();select.innerHTML=workflows.map(w=>`<option value="${escapeHTML(w.id)}" ${w.id===workflowId?'selected':''}>${escapeHTML(w.name)}</option>`).join('')||'<option value="">暂无可运行 SOP</option>';$('#confirmRun').disabled=!workflows.length;$('#runModal').classList.add('open');setTimeout(()=>$('#runInput').focus(),0)}

// ---------- 执行机器（bind-cli，个人维度） ----------
let runnerRefreshTimer=null;
const runnerRuntimeTests=new Set();
const runnerAuthChanges=new Map();
const runnerMaintenancePending=new Set();
let runnerRuntimeMenu=null,runnerRuntimeTrigger=null;
async function refreshRunners(silent){
  try{
    await ensureRunnerBindCode(silent);
    const [runners,def]=await Promise.all([
      api('/api/runners?mine=1',{silent:true}).catch(()=>[]),
      api('/api/runners/default',{silent:true}).catch(()=>({runnerId:''}))
    ]);
    state.runners=runners||[];
    state.defaultRunnerId=def?.runnerId||'';
    state.cloudPool=def?.cloudPool||null;
    state.minRunnerVersion=def?.minRunnerVersion||'';
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
  if(!(Number(runner?.ownerUserId)>0))return '项目共享执行机器';
  const limit=Number(runner?.runLimit)||0;
  return limit>0?`个人机器 · 最多同时运行 ${limit} 条 SOP（不受项目 10 条限额）`:'个人机器 · 不限同时运行数（不受项目 10 条限额）';
}
function runnerRunLimitHTML(runner){
  if(!(Number(runner?.ownerUserId)>0))return '';
  const limit=Number(runner?.runLimit)||0;
  const tip='按顶层 SOP 计数，超出排队；失败、暂停、待确认也占用名额。留空表示不限，不受项目 10 条限额约束。';
  return `<div class="runner-run-limit"><label for="runLimit-${escapeHTML(runner.id)}">最多同时运行<input id="runLimit-${escapeHTML(runner.id)}" type="number" inputmode="numeric" min="1" max="100" step="1" placeholder="不限" value="${limit>0?limit:''}" data-runner-run-limit="${escapeHTML(runner.id)}" aria-label="${escapeHTML(runner.name||runner.id)} 最多同时运行条数">条</label><span class="runner-tip" tabindex="0" role="note" aria-label="${tip}" data-tip="${tip}">?</span></div>`;
}
async function saveRunnerRunLimit(runnerId,raw){
  const text=String(raw??'').trim();
  const value=text===''?0:Number(text);
  if(!Number.isInteger(value)||value<0||value>100){toast('请输入 1-100 的整数，留空表示不限');refreshRunners(true);return}
  try{
    const runner=await api(`/api/runners/${encodeURIComponent(runnerId)}/run-limit`,{method:'PUT',body:JSON.stringify({runLimit:value})});
    const i=(state.runners||[]).findIndex(r=>r.id===runnerId);
    if(i>=0&&runner?.id)state.runners[i]=runner;
    toast(value>0?`已设置：最多同时运行 ${value} 条`:'已设为不限','ok');renderRunnersView();
  }catch(err){toast(err.message);refreshRunners(true)}
}
function runnerStatusLabel(status,lastSeen){
  const label=status==='online'?'在线':status==='offline'?'离线':(status||'未知');
  const style=status==='online'?' online':status==='offline'?' offline':'';
  return `<span class="runner-status${style}" title="${escapeHTML(runnerLastSeen(lastSeen))}"><i></i>${escapeHTML(label)}</span>`;
}
const runnerCLILabels={'claude-code':'Claude Code','codex':'Codex','codebuddy':'CodeBuddy',opencode:'OpenCode',cursor:'Cursor CLI',agy:'Antigravity',kimi:'Kimi Code',pi:'Pi',dsh:'DeepSeek Harness',zcode:'ZCode'};
const runnerAuthLabel={local:'账号登录',personal:'API Key'};
const runnerAuthDescription={local:'Claude Code、Codex 和 Antigravity 可以使用运行 Runner 的系统账号在这台机器上的已有登录，对话与 SOP 的 native、Docker 沙箱均可复用。共享对话中，只有机器所有者发言时会使用此登录。',personal:'读取「个人配置」中保存的 API Key。对话使用当前发言人的 Key，SOP 使用运行责任人的 Key。Antigravity 的 Gemini API Key 按 API 单独计费。'};
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
  const mode=runnerAuthChanges.get(key)||runnerRuntimeAuthMode(runtime);
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
  const runners=state.runners||[];
  const defaultId=state.defaultRunnerId||'';
  $('#runnerCount').textContent=String(runners.length);
  const poolStatus=$('#runnerPoolStatus');
  if(poolStatus){
    const def=runners.find(r=>r.id===defaultId),pool=state.cloudPool;
    let text='',warn=false;
    if(def){
      warn=def.status!=='online';
      text=warn?'默认机器已离线，新任务会失败。请启动它或切回云端':`在 ${def.name||def.id} 上执行`;
    }else if(pool?.configured){
      warn=!pool.online;
      text=pool.online?`云端执行 · ${pool.online} 台在线`:'云端暂无在线机器，新任务最多等待 10 分钟';
    }else{
      text='在平台云端执行';
    }
    poolStatus.textContent=text;poolStatus.classList.toggle('warn',warn);
  }
  const focusedMenu=document.activeElement?.dataset.runnerRuntimeMenu;
  const focusedUpdate=document.activeElement?.dataset.runnerCheckUpdate;
  closeRunnerRuntimeMenu();
  list.innerHTML=runners.map(r=>{
    const isDefault=r.id===defaultId;
    const discoveredCLIs=(r.runtimes||[]).filter(rt=>runnerCLILabels[rt.provider]);
    const shared=!(Number(r.ownerUserId)>0);
    const cliRows=discoveredCLIs.map(rt=>runnerRuntimeHTML(r,rt,shared)).join('');
    const cliSection=cliRows||'<div class="runner-runtime-empty">未发现支持的 Agent CLI</div>';
    const osLabel=runnerOSLabels[r.os]||r.os||'?';
    const defaultBlocker=isDefault?'':runnerDefaultBlocker(r);
    const checking=runnerMaintenancePending.has(r.id);
    return `<article class="runner-card panel ${isDefault?'is-default':''}">
      <div class="runner-card-head">
        <span class="runner-machine-icon" aria-hidden="true">${huiIcon('monitor')}</span>
        <div class="runner-card-title">
          <div class="runner-card-name"><b title="${escapeHTML(r.name||r.id)}">${escapeHTML(r.name||r.id)}</b>${isDefault?'<span class="runner-tag default">默认</span>':''}${shared?'<span class="runner-tag">共享</span>':''}</div>
          <small>${escapeHTML(osLabel)} · ${escapeHTML(r.arch||'?')} · Runner ${escapeHTML(r.runnerVersion||'未知')}</small>
        </div>
        <div class="runner-head-actions">${runnerStatusLabel(r.status,r.lastSeenAt)}${shared?'':`<button class="button ghost runner-check-update" type="button" data-runner-check-update="${escapeHTML(r.id)}" ${r.status!=='online'?'disabled':checking?'aria-disabled="true"':''}>${huiIcon(checking?'arc-spinner':'refresh')}${checking?'处理中…':'检查更新'}</button>`}</div>
      </div>
      <div class="runner-runtime-list">
        ${cliRows?`<div class="runner-runtime-heading"><span>CLI</span><button class="runner-auth-help" type="button" data-runner-auth-help aria-label="模型认证说明" aria-haspopup="dialog">模型认证 ${huiIcon('info')}</button></div>`:''}
        ${cliSection}
      </div>
      <div class="runner-card-action">
        ${runnerRunLimitHTML(r)}
        <span class="runner-card-spacer"></span>
        ${isDefault?'<button class="button ghost small runner-text-button" type="button" data-set-default="">切回云端</button>':''}
        ${r.status==='online'?'':`<button class="button ghost small runner-text-button" type="button" data-remove-runner="${escapeHTML(r.id)}" title="机器已不再使用时移除绑定">移除</button>`}
        ${isDefault?'':`<button class="button hui-btn-soft small" type="button" ${defaultBlocker?`disabled title="${escapeHTML(defaultBlocker)}"`:''} data-set-default="${escapeHTML(r.id)}">设为默认</button>`}
      </div>
    </article>`;
  }).join('')||'<div class="runner-empty"><b>还没有机器</b><span>执行上方命令后自动出现</span></div>';
  // Bind click handlers via JS (CSP blocks inline onclick attributes).
  list.querySelectorAll('[data-set-default]').forEach(btn=>btn.onclick=()=>{
    setDefaultRunner(btn.dataset.setDefault||'');
  });
  list.querySelectorAll('[data-remove-runner]').forEach(btn=>btn.onclick=()=>removeRunner(btn.dataset.removeRunner));
  list.querySelectorAll('[data-runner-cli]').forEach(cb=>cb.onchange=()=>{
    cb.closest('.runner-runtime-row')?.classList.toggle('off',!cb.checked);
    const [runnerId,provider]=cb.dataset.runnerCli.split('|');
    toggleRunnerRuntime(runnerId,provider,cb.checked);
  });
  list.querySelectorAll('[data-runner-run-limit]').forEach(input=>{
    input.onchange=()=>saveRunnerRunLimit(input.dataset.runnerRunLimit,input.value);
    input.onkeydown=e=>{if(e.key==='Enter'){e.preventDefault();input.blur()}};
  });
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
  window.AgentFlowHarnessIcons?.hydrate?.(list);
  if(!runnerRefreshTimer)runnerRefreshTimer=setInterval(()=>{
    if(!document.hidden&&activeViewName()==='runners'&&!runnerRuntimeTrigger&&!document.activeElement?.matches?.('[data-runner-run-limit],[data-runner-auth],[data-runner-key-help]'))refreshRunners(true);
  },15000);
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
function runnerDefaultBlocker(r){
  if(r.status!=='online')return '机器离线';
  const min=state.minRunnerVersion||'';
  if(min&&!runnerVersionAtLeast(r.runnerVersion,min))return `执行机器版本 ${r.runnerVersion||'未知'} 过低，需升级至 ${min} 及以上`;
  return '';
}
async function setDefaultRunner(runnerId){
  try{
    const out=await api('/api/runners/default',{method:'PUT',body:JSON.stringify({runnerId})});
    toast(out?.message||'已更新','ok');refreshRunners(true);
  }catch(err){toast(err.message)}
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
  const kind=CREDENTIAL_KINDS.find(item=>item.models?.engine===provider);
  if(!kind)return;
  return afOpenDialog(modal=>{
    $('#confirmTitle').textContent=`${runnerCLILabels[provider]} 模型列表`;
    const keySource=kind.extra?.url?'API Key 和服务地址':kind.extra?'API Key 和网络环境':'API Key';
    $('#confirmMessage').textContent=local
      ?'与个人配置共用模型目录。获取时使用这台机器的账号登录。'
      :`与个人配置共用模型目录。获取时使用个人配置里的 ${keySource}。`;
    const ok=$('#confirmOkBtn'),cancel=$('#confirmCancelBtn'),card=modal.querySelector('.confirm-modal-card');
    const panel=document.createElement('div');
    panel.className='credential-panel runner-model-editor';
    panel.dataset.credentialRow=kind.key;
    panel.innerHTML=credentialCatalogHTML(kind,credentialUser(),{runnerLogin:local,label:local?'账号登录':'API Key'});
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
        await saveCredential(kind.key,{models});
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
