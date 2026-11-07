function openRunModal(workflowId){const select=$('#runWorkflowChoice'),workflows=runnableWorkflowOptions();select.innerHTML=workflows.map(w=>`<option value="${escapeHTML(w.id)}" ${w.id===workflowId?'selected':''}>${escapeHTML(w.name)}</option>`).join('')||'<option value="">暂无可运行 SOP</option>';$('#confirmRun').disabled=!workflows.length;$('#runModal').classList.add('open');setTimeout(()=>$('#runInput').focus(),0)}

// ---------- 执行机器（bind-cli，个人维度） ----------
let runnerRefreshTimer=null;
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
    renderRunnersView();
  }catch(e){if(!silent)toast(e.message)}
}
// 接入命令携带一次性绑定码（10 分钟有效）：复制即用，无需先创建个人 Token。
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
function runnerStatusLabel(status){
  switch(status){
    case 'online':return '<span class="runner-status online"><i></i>在线</span>';
    case 'offline':return '<span class="runner-status offline"><i></i>离线</span>';
    default:return `<span class="runner-status"><i></i>${escapeHTML(status||'unknown')}</span>`;
  }
}
const runnerCLILabels={'claude-code':'Claude Code','codex':'Codex','codebuddy':'CodeBuddy'};
const runnerCLIGlyphs={'claude-code':'C','codex':'X','codebuddy':'B'};
const runnerOSLabels={darwin:'macOS',linux:'Linux',windows:'Windows'};
function runnerPersonalCredentialConfigured(provider){
  const user=userById(state.currentUser?.id)||{};
  if(provider==='codex')return !!user.codexApiKeyConfigured;
  if(provider==='claude-code')return !!user.claudeCodeApiKeyConfigured;
  if(provider==='codebuddy')return !!user.codeBuddyApiKeyConfigured;
  return false;
}
function runnerLastSeen(value){
  if(!value)return '尚无心跳';
  const seconds=Math.max(0,Math.round((Date.now()-new Date(value).getTime())/1000));
  if(seconds<60)return `${seconds} 秒前`;
  if(seconds<3600)return `${Math.floor(seconds/60)} 分钟前`;
  return new Date(value).toLocaleString();
}
function runnerLastSeenHTML(value){
  if(!value)return runnerLastSeen(value);
  return `<span title="${escapeHTML(new Date(value).toLocaleString())}">${escapeHTML(runnerLastSeen(value))}</span>`;
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
  list.innerHTML=runners.map(r=>{
    const isDefault=r.id===defaultId;
    const discoveredCLIs=(r.runtimes||[]).filter(rt=>runnerCLILabels[rt.provider]);
    const cliRows=discoveredCLIs.map(rt=>{
      const authMode=rt.authMode||(rt.provider==='codebuddy'?'personal':'local');
      const credentialReady=runnerPersonalCredentialConfigured(rt.provider);
      const personalLabel=credentialReady?'个人凭证':'个人凭证（未配置）';
      const credentialMissing=authMode==='personal'&&!credentialReady;
      const authControl=rt.provider==='codebuddy'
        ?`<span class="runner-auth-fixed ${credentialReady?'':'missing'}" title="CodeBuddy 使用个人配置中保存的 API Key">${personalLabel}</span>`
        :`<select class="runner-auth-select ${credentialMissing?'missing':''}" data-runner-auth="${escapeHTML(r.id)}|${escapeHTML(rt.provider)}" aria-label="${runnerCLILabels[rt.provider]} 认证来源"${credentialMissing?` title="到个人配置补充 ${runnerCLILabels[rt.provider]} API Key"`:''}>
            <option value="local" ${authMode==='local'?'selected':''}>本机登录</option>
            <option value="personal" ${authMode==='personal'?'selected':''}>${personalLabel}</option>
          </select>`;
      return `
      <label class="runner-runtime-row ${rt.status==='disabled'?'off':''}">
        <input type="checkbox" data-runner-cli="${escapeHTML(r.id)}|${escapeHTML(rt.provider)}" ${rt.status!=='disabled'?'checked':''}>
        <span class="runner-runtime-icon ${escapeHTML(rt.provider)}" aria-hidden="true">${runnerCLIGlyphs[rt.provider]||'?'}</span>
        <span class="runner-runtime-name"><b>${runnerCLILabels[rt.provider]}</b><small title="${escapeHTML(rt.binaryPath||'')}">${escapeHTML(rt.version||rt.binaryPath||'已发现')}</small></span>
        ${authControl}
      </label>`}).join('');
    const cliSection=cliRows||'<div class="runner-runtime-empty">未发现支持的 Agent CLI</div>';
    const shared=!(Number(r.ownerUserId)>0);
    const osLabel=runnerOSLabels[r.os]||r.os||'?';
    return `<article class="runner-card panel ${isDefault?'is-default':''}">
      <div class="runner-card-head">
        <div class="runner-card-title"><div class="runner-card-name"><b title="${escapeHTML(r.id)}">${escapeHTML(r.name||r.id)}</b>${isDefault?'<span class="runner-tag default">默认</span>':''}${shared?'<span class="runner-tag">共享</span>':''}</div><small>${escapeHTML(osLabel)} · ${escapeHTML(r.arch||'?')} · ${runnerLastSeenHTML(r.lastSeenAt)}</small></div>
        ${runnerStatusLabel(r.status)}
      </div>
      <div class="runner-runtime-list">${cliSection}</div>
      <div class="runner-card-action">
        ${runnerRunLimitHTML(r)}
        <span class="runner-card-spacer"></span>
        ${isDefault?'<button class="button ghost small runner-text-button" type="button" data-set-default="">切回云端</button>':''}
        ${r.status==='online'?'':`<button class="button ghost small runner-text-button" type="button" data-remove-runner="${escapeHTML(r.id)}" title="机器已不再使用时移除绑定">移除</button>`}
        ${isDefault?'':`<button class="button hui-btn-soft small" type="button" ${r.status==='online'?'':'disabled title="机器离线"'} data-set-default="${escapeHTML(r.id)}">设为默认</button>`}
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
  if(!runnerRefreshTimer)runnerRefreshTimer=setInterval(()=>{if(!document.hidden&&activeViewName()==='runners'&&!document.activeElement?.matches?.('[data-runner-run-limit]'))refreshRunners(true)},15000);
}
async function removeRunner(runnerId){
  const runner=(state.runners||[]).find(r=>r.id===runnerId);
  if(!await afConfirm({title:'移除执行机器',message:`确定移除 ${runner?.name||runnerId}？之后如需再用这台机器，重新复制接入命令执行即可。`,confirmText:'移除',isDanger:true}))return;
  try{
    await api(`/api/runners/${encodeURIComponent(runnerId)}`,{method:'DELETE'});
    toast('已移除','ok');refreshRunners(true);
  }catch(err){toast(err.message)}
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
  try{
    await api('/api/runners/runtimes',{method:'PUT',body:JSON.stringify({runnerId,provider,authMode})});
    const ready=runnerPersonalCredentialConfigured(provider);
    toast(authMode==='personal'?(ready?'将使用个人配置中的 API 凭证':'已切换；请先到个人配置补充该 CLI 的 API Key'):'将使用执行机器上的 CLI 登录态',ready||authMode!=='personal'?'ok':undefined);refreshRunners(true);
  }catch(err){toast(err.message);refreshRunners(true)}
}
