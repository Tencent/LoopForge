const platformDiagnosticsEndpoint='/api/platform/diagnostics';
const platformDiagnosticCategoryOrder=['database','artifacts','browser','runtime'];
const platformDiagnosticCategoryLabels={
  database:['数据库','Database'],
  artifacts:['产物','Artifacts'],
  browser:['Agent 浏览器','Agent browser'],
  runtime:['执行环境','Runtime'],
};
const platformDiagnosticStatusLabels={
  ok:['正常','OK'],
  warn:['需要处理','Action needed'],
  error:['错误','Error'],
  info:['说明','Info'],
};

function platformDiagnosticStatusClass(status){
  if(status==='ok')return 'is-ok';
  if(status==='warn')return 'is-warn';
  if(status==='error')return 'is-error';
  return 'is-info';
}

function platformDiagnosticCounts(checks){
  const counts={ok:0,warn:0,error:0,info:0};
  for(const check of checks||[]){
    if(Object.prototype.hasOwnProperty.call(counts,check.status))counts[check.status]++;
    else counts.info++;
  }
  return counts;
}

function platformDiagnosticCheckHTML(check){
  const status=platformDiagnosticStatusClass(check.status);
  const label=platformDiagnosticStatusLabels[check.status]||platformDiagnosticStatusLabels.info;
  const detail=check.detail?`<p class="platform-diagnostic-detail">${escapeHTML(check.detail)}</p>`:'';
  const hint=check.hint?`<p class="platform-diagnostic-hint">${escapeHTML(check.hint)}</p>`:'';
  const command=check.command?`<div class="platform-diagnostic-command"><code>${escapeHTML(check.command)}</code><button type="button" class="button ghost small" data-diagnostic-copy>${lfT('复制命令','Copy')}</button></div>`:'';
  return `<article class="platform-diagnostic-item ${status}" data-diagnostic-id="${escapeHTML(check.id||'')}" data-diagnostic-command="${check.command?escapeHTML(check.command):''}">
    <div class="platform-diagnostic-head">
      <span class="platform-diagnostic-status ${status}">${lfT(label[0],label[1])}</span>
      <h3>${escapeHTML(check.title||check.id||'')}</h3>
    </div>
    ${detail}${hint}${command}
  </article>`;
}

function platformDiagnosticGroupHTML(category,checks){
  const label=platformDiagnosticCategoryLabels[category]||[category,category];
  return `<section class="platform-diagnostic-group">
    <h3 class="platform-diagnostic-group-title">${lfT(label[0],label[1])}</h3>
    ${checks.map(platformDiagnosticCheckHTML).join('')}
  </section>`;
}

function platformDiagnosticsHTML(){
  return `<section class="platform-admin-main-card" id="platformDiagnosticsCard">
    <div class="platform-admin-head">
      <div class="platform-admin-head-meta">
        <h2>${lfT('运维诊断','Deployment diagnostics')}</h2>
        <p>${lfT('检查数据库对象、产物上限与 Agent 浏览器镜像。只做检查并给出处理建议，不会自动执行迁移。','Checks the database objects, artifact limits and the Agent browser image. Checks are read-only and never apply migrations.')}</p>
      </div>
      <button type="button" class="button primary" data-diagnostics-refresh>${lfT('重新检查','Run checks')}</button>
    </div>
    <div class="platform-diagnostic-summary" data-diagnostics-summary aria-live="polite"></div>
    <div class="platform-diagnostic-list" data-diagnostics-list aria-live="polite"></div>
  </section>`;
}

function platformDiagnosticsRender(view){
  const checks=Array.isArray(view?.checks)?view.checks:[];
  const counts=platformDiagnosticCounts(checks);
  const summary=counts.error?lfT(`发现 ${counts.error} 项错误，${counts.warn} 项需要处理`,`${counts.error} error(s), ${counts.warn} item(s) need attention`)
    :counts.warn?lfT(`${counts.warn} 项需要处理，其它正常`,`${counts.warn} item(s) need attention, the rest are OK`)
    :lfT('全部检查通过','All checks passed');
  const summaryHTML=`<span class="platform-diagnostic-summary-text">${escapeHTML(summary)}</span><span class="platform-diagnostic-summary-meta">${escapeHTML(view?.generatedAt||'')}</span>`;
  const groups=[];
  for(const category of platformDiagnosticCategoryOrder){
    const items=checks.filter(check=>check.category===category);
    if(items.length)groups.push(platformDiagnosticGroupHTML(category,items));
  }
  const rest=checks.filter(check=>!platformDiagnosticCategoryOrder.includes(check.category));
  if(rest.length)groups.push(platformDiagnosticGroupHTML('runtime',rest));
  return {summaryHTML, bodyHTML:groups.join('')};
}

async function loadPlatformDiagnostics(host){
  const card=host.querySelector('#platformDiagnosticsCard');
  if(!card)return;
  const list=card.querySelector('[data-diagnostics-list]');
  const summary=card.querySelector('[data-diagnostics-summary]');
  const refresh=card.querySelector('[data-diagnostics-refresh]');
  let busy=false;
  const bindCopy=()=>{
    list.querySelectorAll('[data-diagnostic-copy]').forEach(button=>{
      button.onclick=async()=>{
        const item=button.closest('[data-diagnostic-item]');
        const command=item?.dataset.diagnosticCommand||'';
        try{
          await navigator.clipboard.writeText(command);
          button.textContent=lfT('已复制','Copied');
        }catch{
          button.textContent=lfT('复制失败','Copy failed');
        }
        setTimeout(()=>{button.textContent=lfT('复制命令','Copy')},1500);
      };
    });
  };
  const load=async()=>{
    if(busy)return;
    busy=true;
    refresh.disabled=true;
    summary.innerHTML='';
    list.innerHTML=`<div class="platform-diagnostic-placeholder">${lfT('检查中…','Running checks…')}</div>`;
    try{
      const view=await api(platformDiagnosticsEndpoint,{silent:true});
      const rendered=platformDiagnosticsRender(view);
      summary.innerHTML=rendered.summaryHTML;
      list.innerHTML=rendered.bodyHTML||`<div class="platform-diagnostic-placeholder">${lfT('没有检查项','No checks')}</div>`;
      bindCopy();
    }catch(err){
      list.innerHTML=`<div class="platform-diagnostic-placeholder is-error">${escapeHTML(err?.message||lfT('检查失败','Checks failed'))}</div>`;
    }finally{
      busy=false;
      refresh.disabled=false;
    }
  };
  refresh.onclick=load;
  await load();
}
