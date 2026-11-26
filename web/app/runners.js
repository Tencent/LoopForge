function openRunModal(workflowId){const select=$('#runWorkflowChoice'),workflows=runnableWorkflowOptions();select.innerHTML=workflows.map(w=>`<option value="${escapeHTML(w.id)}" ${w.id===workflowId?'selected':''}>${escapeHTML(w.name)}</option>`).join('')||'<option value="">Chưa có SOP nào chạy được</option>';$('#confirmRun').disabled=!workflows.length;$('#runModal').classList.add('open');setTimeout(()=>$('#runInput').focus(),0)}

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
  if(!(Number(runner?.ownerUserId)>0))return 'Máy thực thi dùng chung của dự án';
  const limit=Number(runner?.runLimit)||0;
  return limit>0?`Máy cá nhân · tối đa ${limit} SOP chạy đồng thời (không bị giới hạn 10 của dự án)`:'Máy cá nhân · không giới hạn số SOP chạy đồng thời (không bị giới hạn 10 của dự án)';
}
function runnerRunLimitHTML(runner){
  if(!(Number(runner?.ownerUserId)>0))return '';
  const limit=Number(runner?.runLimit)||0;
  const tip='Tính theo SOP cấp cao nhất, vượt quá thì vào hàng chờ; thất bại, tạm dừng và chờ xác nhận cũng chiếm suất. Để trống nghĩa là không giới hạn, không bị ràng buộc hạn mức 10 của dự án.';
  return `<div class="runner-run-limit"><label for="runLimit-${escapeHTML(runner.id)}">Tối đa chạy đồng thời<input id="runLimit-${escapeHTML(runner.id)}" type="number" inputmode="numeric" min="1" max="100" step="1" placeholder="Không giới hạn" value="${limit>0?limit:''}" data-runner-run-limit="${escapeHTML(runner.id)}" aria-label="${escapeHTML(runner.name||runner.id)} — số SOP chạy đồng thời tối đa"> SOP</label><span class="runner-tip" tabindex="0" role="note" aria-label="${tip}" data-tip="${tip}">?</span></div>`;
}
async function saveRunnerRunLimit(runnerId,raw){
  const text=String(raw??'').trim();
  const value=text===''?0:Number(text);
  if(!Number.isInteger(value)||value<0||value>100){toast('Nhập số nguyên 1-100, để trống nghĩa là không giới hạn');refreshRunners(true);return}
  try{
    const runner=await api(`/api/runners/${encodeURIComponent(runnerId)}/run-limit`,{method:'PUT',body:JSON.stringify({runLimit:value})});
    const i=(state.runners||[]).findIndex(r=>r.id===runnerId);
    if(i>=0&&runner?.id)state.runners[i]=runner;
    toast(value>0?`Đã đặt: tối đa ${value} tác vụ chạy đồng thời`:'Đã đặt thành không giới hạn','ok');renderRunnersView();
  }catch(err){toast(err.message);refreshRunners(true)}
}
function runnerStatusLabel(status,lastSeen){
  const label=status==='online'?'Trực tuyến':status==='offline'?'Ngoại tuyến':(status||'Không rõ');
  const style=status==='online'?' online':status==='offline'?' offline':'';
  return `<span class="runner-status${style}" title="${escapeHTML(runnerLastSeen(lastSeen))}"><i></i>${escapeHTML(label)}</span>`;
}
const runnerCLILabels={'claude-code':'Claude Code','codex':'Codex','codebuddy':'CodeBuddy',opencode:'OpenCode',cursor:'Cursor CLI',agy:'Antigravity',kimi:'Kimi Code',pi:'Pi',dsh:'DeepSeek Harness',zcode:'ZCode'};
const runnerAuthLabel={local:'Đăng nhập tài khoản',personal:'API Key'};
const runnerAuthDescription={local:'Claude Code, Codex và Antigravity có thể dùng phiên đăng nhập sẵn có trên máy này của tài khoản hệ thống chạy Runner; hội thoại và SOP trong sandbox native lẫn Docker đều tái sử dụng được. Trong hội thoại dùng chung, chỉ khi chủ máy phát ngôn mới dùng phiên đăng nhập này.',personal:'Đọc API Key đã lưu trong「Cài đặt cá nhân」. Hội thoại dùng Key của người đang nói, SOP dùng Key của người phụ trách lần chạy. Gemini API Key của Antigravity được tính phí riêng theo API.'};
const runnerOSLabels={darwin:'macOS',linux:'Linux',windows:'Windows'};
function runnerKeyDescription(provider){
  if(provider==='zcode')return 'API Key của GLM Coding Plan kết hợp địa chỉ dành riêng cho gói có thể dùng hạn mức đăng ký; hãy điền trong「Cài đặt cá nhân」. Hạn mức Start Plan cần đăng nhập tài khoản ZCode, không dùng được qua API Key.';
  return 'CLI này hỗ trợ API Key. Dùng Key do gói đăng ký của nhà cung cấp cấp, lượng thực thi sẽ tính vào hạn mức gói tương ứng. Hãy điền API Key trong「Cài đặt cá nhân」.';
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
  const title=missing?' title="Hãy điền API Key trong cài đặt cá nhân"':'';
  if(!shared&&supportsLogin)return `<select class="runner-auth-select${mode==='personal'?' is-personal':''}" data-runner-auth="${escapeHTML(key)}" aria-label="${runnerCLILabels[runtime.provider]} — cách xác thực"${title} ${busy?'disabled':''}>
    <option value="local" ${mode==='local'?'selected':''}>${runnerAuthLabel.local}</option>
    <option value="personal" ${mode==='personal'?'selected':''}>${runnerAuthLabel.personal}</option>
  </select>`;
  const keyHelp=supportsLogin?'':`<button class="runner-auth-tip" type="button" data-runner-key-help="${runtime.provider}" aria-label="${runnerCLILabels[runtime.provider]} — giải thích API Key" aria-haspopup="dialog" title="${escapeHTML(runnerKeyDescription(runtime.provider))}"><span aria-hidden="true">?</span></button>`;
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
      <input type="checkbox" data-runner-cli="${safeKey}" ${disabled?'':'checked'} ${shared?'disabled':''} aria-label="Bật ${label}">
      <span class="runner-runtime-icon ${provider}" aria-hidden="true">${icon?`<img src="${icon}" alt="">`:''}</span>
      <span class="runner-runtime-name"><b>${label}</b><small title="${escapeHTML(runtime.binaryPath||'')}">${escapeHTML(runtime.version||runtime.binaryPath||'Đã phát hiện')}</small></span>
    </label>
    ${runnerAuthHTML(runtime,key,busy,shared)}
    ${shared?'':`<button class="runner-more" type="button" data-runner-runtime-menu="${safeKey}" aria-label="${label} — thêm thao tác" aria-haspopup="menu" aria-expanded="false" aria-busy="${testing||updating}">${huiIcon(testing||updating?'arc-spinner':'more-horizontal')}</button>`}
  </div>`;
}
function runnerLastSeen(value){
  if(!value)return 'Chưa có heartbeat';
  const seconds=Math.max(0,Math.round((Date.now()-new Date(value).getTime())/1000));
  if(seconds<60)return `${seconds} giây trước`;
  if(seconds<3600)return `${Math.floor(seconds/60)} phút trước`;
  return new Date(value).toLocaleString();
}
function runnerCopyCommand(btn,text,message){
  const done=()=>{
    toast(message,'ok');
    if(!btn.dataset.label)btn.dataset.label=btn.textContent;
    btn.textContent='Đã sao chép';btn.classList.add('copied');
    clearTimeout(btn._copiedTimer);
    btn._copiedTimer=setTimeout(()=>{btn.textContent=btn.dataset.label;btn.classList.remove('copied')},1400);
  };
  if(!navigator.clipboard){toast('Sao chép thất bại, hãy chọn và sao chép thủ công');return}
  navigator.clipboard.writeText(text).then(done).catch(()=>toast('Sao chép thất bại, hãy chọn và sao chép thủ công'));
}
function renderRunnersView(){
  const list=$('#runnersList');if(!list)return;
  const host=location.host;
  const httpsOrigin=`https://${host}`,httpOrigin=`http://${host}`;
  const bindHTTPS=$('#bindHttpsOrigin'),bindHTTP=$('#bindHttpOrigin'),bindHost=$('#bindServerHost');
  if(bindHTTPS)bindHTTPS.textContent=httpsOrigin;if(bindHTTP)bindHTTP.textContent=httpOrigin;if(bindHost)bindHost.textContent=host;
  const tokenSpan=$('#bindToken');
  if(tokenSpan)tokenSpan.textContent=state.runnerBindCode?.code||'Đang tạo…';
  const copyBtn=$('#copyBindCommand');
  if(copyBtn)copyBtn.onclick=()=>{
    const text=$('#bindCliCommand')?.innerText||'';
    runnerCopyCommand(copyBtn,text,'Đã sao chép, còn hiệu lực trong 10 phút');
  };
  const uninstallHTTPS=$('#uninstallHttpsOrigin'),uninstallHTTP=$('#uninstallHttpOrigin');
  if(uninstallHTTPS)uninstallHTTPS.textContent=httpsOrigin;if(uninstallHTTP)uninstallHTTP.textContent=httpOrigin;
  const copyUnBtn=$('#copyUninstallCommand');
  if(copyUnBtn)copyUnBtn.onclick=(e)=>{
    const text=e.currentTarget.parentElement?.querySelector('pre')?.innerText||'';
    runnerCopyCommand(copyUnBtn,text,'Đã sao chép lệnh gỡ cài đặt');
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
      text=warn?'Máy mặc định đang ngoại tuyến, tác vụ mới sẽ thất bại. Hãy khởi động máy hoặc chuyển lại về đám mây':`Chạy trên ${def.name||def.id}`;
    }else if(pool?.configured){
      warn=!pool.online;
      text=pool.online?`Chạy trên đám mây · ${pool.online} máy trực tuyến`:'Đám mây hiện không có máy trực tuyến, tác vụ mới có thể phải chờ tối đa 10 phút';
    }else{
      text='Chạy trên đám mây của nền tảng';
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
    const cliSection=cliRows||'<div class="runner-runtime-empty">Không tìm thấy Agent CLI được hỗ trợ</div>';
    const osLabel=runnerOSLabels[r.os]||r.os||'?';
    const defaultBlocker=isDefault?'':runnerDefaultBlocker(r);
    const checking=runnerMaintenancePending.has(r.id);
    return `<article class="runner-card panel ${isDefault?'is-default':''}">
      <div class="runner-card-head">
        <span class="runner-machine-icon" aria-hidden="true">${huiIcon('monitor')}</span>
        <div class="runner-card-title">
          <div class="runner-card-name"><b title="${escapeHTML(r.name||r.id)}">${escapeHTML(r.name||r.id)}</b>${isDefault?'<span class="runner-tag default">Mặc định</span>':''}${shared?'<span class="runner-tag">Dùng chung</span>':''}</div>
          <small>${escapeHTML(osLabel)} · ${escapeHTML(r.arch||'?')} · Runner ${escapeHTML(r.runnerVersion||'Không rõ')}</small>
        </div>
        <div class="runner-head-actions">${runnerStatusLabel(r.status,r.lastSeenAt)}${shared?'':`<button class="button ghost runner-check-update" type="button" data-runner-check-update="${escapeHTML(r.id)}" ${r.status!=='online'?'disabled':checking?'aria-disabled="true"':''}>${huiIcon(checking?'arc-spinner':'refresh')}${checking?'Đang xử lý…':'Kiểm tra cập nhật'}</button>`}</div>
      </div>
      <div class="runner-runtime-list">
        ${cliRows?`<div class="runner-runtime-heading"><span>CLI</span><button class="runner-auth-help" type="button" data-runner-auth-help aria-label="Giải thích xác thực model" aria-haspopup="dialog">Xác thực model ${huiIcon('info')}</button></div>`:''}
        ${cliSection}
      </div>
      <div class="runner-card-action">
        ${runnerRunLimitHTML(r)}
        <span class="runner-card-spacer"></span>
        ${isDefault?'<button class="button ghost small runner-text-button" type="button" data-set-default="">Chuyển lại về đám mây</button>':''}
        ${r.status==='online'?'':`<button class="button ghost small runner-text-button" type="button" data-remove-runner="${escapeHTML(r.id)}" title="Gỡ liên kết khi máy không còn được dùng">Gỡ</button>`}
        ${isDefault?'':`<button class="button hui-btn-soft small" type="button" ${defaultBlocker?`disabled title="${escapeHTML(defaultBlocker)}"`:''} data-set-default="${escapeHTML(r.id)}">Đặt làm mặc định</button>`}
      </div>
    </article>`;
  }).join('')||'<div class="runner-empty"><b>Chưa có máy nào</b><span>Máy sẽ xuất hiện tự động sau khi chạy lệnh ở trên</span></div>';
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
    title:'Cách xác thực model',
    message:`${runnerAuthLabel.local}\n${runnerAuthDescription.local}\n\n${runnerAuthLabel.personal}\n${runnerAuthDescription.personal}`
  }));
  list.querySelectorAll('[data-runner-key-help]').forEach(btn=>btn.onclick=()=>{
    const provider=btn.dataset.runnerKeyHelp;
    return afAlert({
      title:`${runnerCLILabels[provider]} — giải thích API Key`,
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
  if(!await afConfirm({title:'Gỡ máy thực thi',message:`Gỡ ${runner?.name||runnerId}? Nếu cần dùng lại máy này sau đó, hãy sao chép lại lệnh kết nối và chạy là được.`,confirmText:'Gỡ',isDanger:true}))return;
  try{
    await api(`/api/runners/${encodeURIComponent(runnerId)}`,{method:'DELETE'});
    toast('Đã gỡ','ok');refreshRunners(true);
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
  if(r.status!=='online')return 'Máy ngoại tuyến';
  const min=state.minRunnerVersion||'';
  if(min&&!runnerVersionAtLeast(r.runnerVersion,min))return `Phiên bản máy thực thi ${r.runnerVersion||'Không rõ'} quá thấp, cần nâng lên ${min} trở lên`;
  return '';
}
async function setDefaultRunner(runnerId){
  try{
    const out=await api('/api/runners/default',{method:'PUT',body:JSON.stringify({runnerId})});
    toast(out?.message||'Đã cập nhật','ok');refreshRunners(true);
  }catch(err){toast(err.message)}
}
function toggleRunnerRuntime(runnerId,provider,enabled){
  return runnerRuntimeRequest(runnerId,provider,enabled?'online':'disabled');
}
async function runnerRuntimeRequest(runnerId,provider,status){
  try{
    await api('/api/runners/runtimes',{method:'PUT',body:JSON.stringify({runnerId,provider,status})});
    toast(`${runnerCLILabels[provider]||provider} đã ${status==='online'?'bật':'tắt'}`,'ok');refreshRunners(true);
  }catch(err){toast(err.message);refreshRunners(true)}
}
async function setRunnerRuntimeAuth(runnerId,provider,authMode){
  const key=`${runnerId}|${provider}`;
  runnerAuthChanges.set(key,authMode);
  renderRunnersView();
  try{
    await api('/api/runners/runtimes',{method:'PUT',body:JSON.stringify({runnerId,provider,authMode})});
    const ready=runnerPersonalCredentialConfigured(provider);
    toast(authMode==='personal'?(ready?'Sẽ dùng API Key trong「Cài đặt cá nhân」':'Đã chuyển; hãy bổ sung API Key của CLI này trong「Cài đặt cá nhân」trước'):'Sẽ dùng tài khoản CLI đã đăng nhập trên máy này',ready||authMode!=='personal'?'ok':undefined);
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
    toast(`${runnerCLILabels[provider]} đã vượt kiểm tra`,'ok');
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
    {label:testBusy?'Đang kiểm tra…':'Kiểm tra',icon:'flask',disabled:testBusy||runnerAuthChanges.has(key)||runner.status!=='online'||runtime.status==='disabled',run:()=>testRunnerRuntime(id,provider)},
    {label:updateBusy?'Đang cập nhật…':'Cập nhật',icon:'download',disabled:updateBusy||runner.status!=='online',run:()=>updateRunnerCLI(runner,runtime)},
    {label:'Danh sách model',icon:'layers',disabled:runnerAuthChanges.has(key),run:()=>showRunnerModels(runner,runtime)}
  ];
  menu.innerHTML=actions.map((action,index)=>`<button type="button" role="menuitem" tabindex="-1" class="issue-popover-item" data-action="${index}" ${action.disabled?'disabled':''}>${huiIcon(action.icon)}${action.label}</button>`).join('');
  menu.setAttribute('aria-label',`${runnerCLILabels[provider]} — thêm thao tác`);
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
    $('#confirmTitle').textContent=`${runnerCLILabels[provider]} Danh sách model`;
    const keySource=kind.extra?.url?'API Key và địa chỉ dịch vụ':kind.extra?'API Key và môi trường mạng':'API Key';
    $('#confirmMessage').textContent=local
      ?'Dùng chung danh mục model với cài đặt cá nhân. Khi lấy danh sách sẽ dùng tài khoản đã đăng nhập trên máy này.'
      :`Dùng chung danh mục model với cài đặt cá nhân. Khi lấy danh sách sẽ dùng ${keySource} trong cài đặt cá nhân.`;
    const ok=$('#confirmOkBtn'),cancel=$('#confirmCancelBtn'),card=modal.querySelector('.confirm-modal-card');
    const panel=document.createElement('div');
    panel.className='credential-panel runner-model-editor';
    panel.dataset.credentialRow=kind.key;
    panel.innerHTML=credentialCatalogHTML(kind,credentialUser(),{runnerLogin:local,label:local?'Đăng nhập tài khoản':'API Key'});
    modal.querySelector('.confirm-modal-footer').before(panel);
    card.classList.add('runner-model-card');
    const catalog=panel.querySelector('[data-credential-catalog]');
    loadCredentialEffortCatalogs().then(()=>refreshCredentialCatalogEfforts(catalog));
    if(local){
      catalog.probeModels=()=>api(`/api/runners/${encodeURIComponent(runner.id)}/models/probe`,{
        method:'POST',body:JSON.stringify({provider}),silent:true
      });
    }
    ok.textContent='Lưu';ok.className='button primary';ok.disabled=true;
    cancel.textContent='Hủy';
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
      ok.textContent='Đang lưu…';
      catalog.inert=true;
      try{
        await saveCredential(kind.key,{models});
        toast('Đã lưu danh sách model','ok');
        if(panel.isConnected)confirmPromiseResolve?.(true);
      }catch(err){
        if(panel.isConnected){
          toast(err.message,'err');
          ok.textContent='Lưu';ok.disabled=false;cancel.disabled=false;catalog.inert=false;
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
      const confirmed=await afConfirm({title:'Runner có bản cập nhật',message:`Phiên bản hiện tại ${result.current||'Không rõ'}. Cập nhật sẽ khởi động lại Runner đang rỗi.`,confirmText:'Cập nhật ngay',info:true});
      if(confirmed){
        await api(runnerMaintenancePath(id),{method:'POST',silent:true});
        await followRunnerUpdate(id);
      }
    }else if(result.status==='current'){
      toast(`Runner ${result.current||''} đã là phiên bản hiện tại của nền tảng`.replace('  ',' '),'ok');
    }else{
      toast(result.message||'Tạm thời không kiểm tra được cập nhật','err',5000);
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
    await afAlert({title:`Cập nhật ${label}`,message:'Hãy cập nhật trên máy thực thi này bằng cách cài đặt ban đầu. Sau khi cập nhật, làm mới danh sách máy để thấy phiên bản.'});
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
      toast(`${label}${version?' '+version:''} đã sẵn sàng`,'ok');
      await refreshRunners(true);
    }else if(provider&&result.status==='manual'){
      await afAlert({title:`Cập nhật ${label}`,message:result.message||'Hãy cập nhật CLI này trên máy thực thi bằng cách cài đặt ban đầu'});
    }else if(result.status==='ready'){
      toast(`${label} — trạng thái cập nhật đã bị gián đoạn, hãy làm mới để xác nhận phiên bản`,'err',5000);
    }else{
      toast(result.message||`${label} vẫn còn bản cập nhật, hãy kiểm tra lại`,result.status==='failed'?'err':'info',5000);
    }
    return;
  }
  toast(`Hết thời gian chờ cập nhật ${label}, hãy kiểm tra phiên bản sau`,'err',5000);
}
