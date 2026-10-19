// Business-domain permissions come from the server, independent of the project switcher.
const businessState={items:[],canAdmin:false,userId:0,scope:'all',from:'',to:'',report:null,error:'',loading:false,editorId:null,seq:0,rangeDays:30};
function businessDate(d){return `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')}`}
function businessDomainName(id){return businessState.items.find(x=>x.domain.id===id)?.domain.name||(state.data?.businessDomains||[]).find(d=>d.id===id)?.name||id||'未分类'}
function businessUserName(id){const u=(state.data?.users||[]).find(u=>Number(u.id)===Number(id));return u?.chineseName||u?.loginName||String(id)}
function businessCost(value,known=true){return `$${Number(value||0).toFixed(2)}${known?'':'（部分未识别）'}`}
function businessUserOptions(ids){return(state.data?.users||[]).map(u=>`<option value="${Number(u.id)}" ${ids.includes(Number(u.id))?'selected':''}>${escapeHTML(u.chineseName||u.loginName)} · ${escapeHTML(u.loginName)}</option>`).join('')}
async function loadBusinessDomains(){
  if(!businessState.from&&!businessState.to&&!businessState.initialized){const to=new Date(),from=new Date(to);from.setDate(from.getDate()-29);businessState.from=businessDate(from);businessState.to=businessDate(to);businessState.initialized=true}
  businessState.loading=true;businessState.error='';renderBusinessDomains();
  try{
    const [access,projects]=await Promise.all([api('/api/business-domains',{silent:true}),api('/api/projects',{silent:true})]);
    businessState.items=access.items;state.data.businessDomains=access.items.map(x=>x.domain);businessState.canAdmin=access.canAdmin;businessState.userId=access.userId;state.data.projects=projects;renderProjectSwitcher();
    if(businessState.scope!=='all'&&businessState.scope!=='unclassified'&&!access.items.some(x=>x.domain.id===businessState.scope&&x.canView))businessState.scope='all';
    await refreshBusinessReport();
  }catch(e){businessState.error=e.message;businessState.loading=false;businessState.report=null;renderBusinessDomains()}
}
async function refreshBusinessReport(){
  const seq=++businessState.seq;businessState.loading=true;businessState.error='';businessState.report=null;renderBusinessDomains();
  const q=new URLSearchParams({businessDomainId:businessState.scope,grain:'day'});
  try{if(businessState.from)q.set('from',new Date(`${businessState.from}T00:00:00`).toISOString());
  if(businessState.to)q.set('to',new Date(`${businessState.to}T23:59:59.999`).toISOString());
  const report=await api('/api/business-domain-report?'+q,{silent:true});if(seq!==businessState.seq)return;businessState.report=report}catch(e){if(seq===businessState.seq)businessState.error=e.message}
  finally{if(seq===businessState.seq){businessState.loading=false;renderBusinessDomains()}}
}
function businessScopeLabel(){return businessState.scope==='all'?'全部可查看业务':businessState.scope==='unclassified'?'未分类':businessDomainName(businessState.scope)}
function businessTrendChart(trend){
  if(!trend.length)return '<div class="business-chart-empty">所选时间段暂无运行数据</div>';
  const width=720,height=200,left=38,right=22,top=20,bottom=32,plotW=width-left-right,plotH=height-top-bottom,max=Math.max(2,...trend.map(t=>Number(t.runs||0))),dayMs=86400000;
  const start=Date.parse(`${businessState.from||trend[0].date}T00:00:00Z`),end=Date.parse(`${businessState.to||trend[trend.length-1].date}T00:00:00Z`),days=Math.max(1,Math.round((end-start)/dayMs)+1),step=plotW/days,barW=Math.max(2,Math.min(28,step*.6));
  const grid=[0,Math.ceil(max/2),max].map(value=>{const y=top+plotH*(1-value/max);return `<line x1="${left}" x2="${width-right}" y1="${y}" y2="${y}"/><text x="${left-8}" y="${y+4}" text-anchor="end">${value}</text>`}).join('');
  const bars=trend.map(t=>{const offset=(Date.parse(`${t.date}T00:00:00Z`)-start)/dayMs,x=left+step*(offset+.5),h=Number(t.runs||0)/max*plotH;return `<g><title>${escapeHTML(t.date)}：${t.runs} 次运行</title><rect x="${x-barW/2}" y="${top+plotH-h}" width="${barW}" height="${Math.max(1,h)}" rx="2"/>${days<=60&&trend.length<=12?`<text class="business-chart-value" x="${x}" y="${top+plotH-h-6}" text-anchor="middle">${t.runs}</text>`:''}</g>`}).join('');
  const crossYear=new Date(start).getUTCFullYear()!==new Date(end).getUTCFullYear(),ticks=Math.min(5,days-1),axis=Array.from({length:ticks+1},(_,i)=>{const offset=ticks?Math.round((days-1)*i/ticks):0,date=new Date(start+offset*dayMs).toISOString().slice(0,10),x=left+step*(offset+.5);return `<text x="${x}" y="${height-9}" text-anchor="middle">${escapeHTML(crossYear?date:date.slice(5))}</text>`}).join('');
  return `<svg class="business-trend-svg" viewBox="0 0 ${width} ${height}" role="img" aria-label="每日运行次数，按实际日期间隔展示">${grid}${bars}${axis}</svg>`;
}
function businessReportHTML(){
  const r=businessState.report;if(!r)return '';const singleDomain=businessState.scope!=='all';const s=r.summary,d=r.delivery,groups=r.businessDomains||[],projects=r.projects||[],trend=r.trend||[];
  const kpis=[['项目数',projects.length,'按项目当前归属'],['关联需求',s.uniqueIssues,'统计期内关联执行的需求'],['已交付',d.delivered,'已完成交付的需求'],['运行成功率',s.succeededRuns+s.failedRuns?`${Number(s.successRate||0).toFixed(1)}%`:'—',`${s.succeededRuns} 成功 · ${s.failedRuns} 失败`],['Token 用量',formatTokens(s.totalTokens),`${s.totalRuns||groups.reduce((n,g)=>n+g.runs,0)} 次运行`],['已识别成本',businessCost(s.costReportedUsd),`识别率 ${Number(s.costCoveragePct||0).toFixed(1)}%`]];
  const table=(head,rows)=>`<div class="business-table-wrap"><table class="business-table"><thead><tr>${head.map(h=>`<th>${h}</th>`).join('')}</tr></thead><tbody>${rows.length?rows.join(''):`<tr><td colspan="${head.length}" class="business-table-empty">所选范围暂无数据</td></tr>`}</tbody></table></div>`;
  const groupRows=groups.map(g=>{const attempts=g.success+g.failed,rate=attempts?`${(100*g.success/attempts).toFixed(1)}%`:'—',share=s.totalTokens?100*g.tokens/s.totalTokens:0;return `<tr><td><b>${escapeHTML(g.name)}</b>${g.active?'':' <span class="business-status">已停用</span>'}</td><td>${g.projectCount}</td><td>${g.uniqueIssues}</td><td>${g.delivered}</td><td>${g.runs}</td><td>${rate}</td><td><b>${formatTokens(g.tokens)}</b><small>${share.toFixed(1)}% 的用量</small></td><td>${businessCost(g.costUsd,g.costKnown)}</td></tr>`});
  const projectRows=projects.map(p=>`<tr><td><b>${escapeHTML(p.name)}</b><small>${escapeHTML(p.projectId)}</small></td>${singleDomain?'':`<td><span class="business-domain-tag">${escapeHTML(businessDomainName(p.businessDomainId))}</span></td>`}<td>${p.uniqueIssues}</td><td>${p.runs}</td><td><span class="business-success-count">${p.success}</span><span class="business-count-divider"> / </span>${p.failed}</td><td>${formatTokens(p.tokens)}</td><td>${businessCost(p.costUsd,p.costKnown)}</td></tr>`);
  const distributionItems=singleDomain?[...projects].sort((a,b)=>b.tokens-a.tokens||a.name.localeCompare(b.name)):groups;
  const distribution=distributionItems.map((g,i)=>{const share=s.totalTokens?100*g.tokens/s.totalTokens:0;return `<div class="business-mix-row"><div><span class="business-mix-dot business-color-${i%4}"></span><b>${escapeHTML(g.name)}</b><span>${formatTokens(g.tokens)} <small>${share.toFixed(1)}%</small></span></div><div class="business-mix-track"><i class="business-color-${i%4}" style="width:${share.toFixed(2)}%"></i></div><small>${singleDomain?`${g.uniqueIssues} 个关联需求`:`${g.projectCount} 个项目`} · ${g.runs} 次运行</small></div>`}).join('')||'<div class="business-chart-empty">暂无业务用量</div>';
  return `<div class="business-report"><div class="business-report-meta"><b>${escapeHTML(businessScopeLabel())}</b><span>${escapeHTML(businessState.from||'不限开始日期')} 至 ${escapeHTML(businessState.to||'不限结束日期')}</span><small>按项目当前归属统计</small></div><div class="business-kpis">${kpis.map(([label,value,foot],i)=>`<article><span><i class="business-kpi-dot business-color-${i%4}"></i>${label}</span><b>${value}</b><small>${foot}</small></article>`).join('')}</div><div class="business-chart-grid"><section class="business-card"><div class="business-card-heading"><div><h2>运行趋势</h2><p>每日运行次数</p></div><span class="business-domain-tag">${trend.length} 个活跃日</span></div>${businessTrendChart(trend)}</section><section class="business-card"><div class="business-card-heading"><div><h2>${singleDomain?'项目用量分布':'业务用量分布'}</h2><p>${singleDomain?'各项目占所选业务域的 Token 比例':'占当前可查看范围的 Token 比例'}</p></div></div><div class="business-mix-list">${distribution}</div></section></div>${singleDomain?'':`<section class="business-card business-data-card"><div class="business-card-heading"><div><h2>业务分布</h2><p>查看各业务的规模、交付与资源投入</p></div><span class="business-domain-tag">${groups.length} 个业务分类</span></div>${table(['业务域','项目','关联需求','已交付','运行','成功率','Token / 占比','成本'],groupRows)}</section>`}<section class="business-card business-data-card"><div class="business-card-heading"><div><h2>项目贡献</h2><p>${singleDomain?'所选业务域内各项目的执行与用量':'按项目当前主业务域汇总'}</p></div><span class="business-domain-tag">${projects.length} 个项目</span></div>${table(['项目',...(singleDomain?[]:['主业务域']),'关联需求','运行','成功 / 失败','Tokens','成本'],projectRows)}</section><div class="business-report-foot">运行按创建时间筛选 · 取消运行不计入成功率 · 生成于 ${escapeHTML(new Date(r.generatedAt).toLocaleString())}</div></div>`;
}
function renderBusinessDomains(){
  const host=$('#businessDomainsBody');if(!host)return;const b=businessState;
  const report=b.loading?'<div class="business-card business-loading"><span class="business-loading-dot"></span>正在查询业务数据…</div>':b.error?`<div class="business-card business-error">${escapeHTML(b.error)}</div>`:businessReportHTML();
  host.innerHTML=`<form id="businessReportFilter" class="business-filters"><div class="business-periods">${[7,30,90].map(days=>`<button type="button" data-business-days="${days}" class="${b.rangeDays===days?'active':''}">${days} 天</button>`).join('')}<button id="businessAllTime" type="button" class="${!b.from&&!b.to?'active':''}">全部</button></div><label>业务范围<select name="scope" class="select"><option value="all">全部可查看业务</option><option value="unclassified" ${b.scope==='unclassified'?'selected':''}>未分类</option>${b.items.filter(x=>x.canView).map(x=>`<option value="${escapeHTML(x.domain.id)}" ${b.scope===x.domain.id?'selected':''}>${escapeHTML(x.domain.name)}${x.domain.active?'':'（已停用）'}</option>`).join('')}</select></label><label>开始日期<input type="date" name="from" value="${b.from}"></label><span class="business-filter-arrow">→</span><label>结束日期<input type="date" name="to" value="${b.to}"></label><button class="button ghost" type="submit">应用筛选</button></form>${report}`;
  $('#businessExport').disabled=!b.report||b.loading;$('#businessExport').onclick=exportBusinessReport;$('#businessRefresh').onclick=loadBusinessDomains;
  $('#businessReportFilter').onsubmit=e=>{e.preventDefault();const f=e.currentTarget;b.scope=f.elements.scope.value;b.from=f.elements.from.value;b.to=f.elements.to.value;b.rangeDays=null;refreshBusinessReport()};
  $('#businessAllTime').onclick=()=>{b.scope=$('#businessReportFilter').elements.scope.value;b.from='';b.to='';b.rangeDays=null;refreshBusinessReport()};
  host.querySelectorAll('[data-business-days]').forEach(button=>button.onclick=()=>{b.scope=$('#businessReportFilter').elements.scope.value;const to=new Date(),from=new Date(to);from.setDate(from.getDate()-Number(button.dataset.businessDays)+1);b.from=businessDate(from);b.to=businessDate(to);b.rangeDays=Number(button.dataset.businessDays);refreshBusinessReport()});
}
async function exportBusinessReport(){
  if(!businessState.report||businessState.loading)return;
  try{const css=await fetch('/business-domains.css').then(r=>{if(!r.ok)throw new Error('报告样式加载失败');return r.text()});const html=`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>${escapeHTML(businessScopeLabel())} · 业务报告</title><style>${css}body{padding:32px;background:#f5f7fb;font-family:system-ui,sans-serif;color:#172033;max-width:1400px;margin:auto}</style></head><body><h1>${escapeHTML(businessScopeLabel())} · 业务报告</h1>${businessReportHTML()}</body></html>`,url=URL.createObjectURL(new Blob([html],{type:'text/html;charset=utf-8'})),a=document.createElement('a');a.href=url;a.download=`业务报告-${businessDate(new Date())}.html`;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);toast('报告已导出')}catch(e){toast(e.message)}
}

function canAccessPlatformManagement(){
  const uid=Number(state.currentUser?.id)||0;
  return isPlatformAdmin()||!state.authRequired||uid>0&&(state.data?.businessDomains||[]).some(d=>(d.managerIds||[]).includes(uid));
}
async function loadBusinessDomainManagement(){
  const host=$('#platformBusinessDomains');if(!host)return;
  host.innerHTML='<div class="business-card">正在加载业务域…</div>';
  try{
    const access=await api('/api/business-domains',{silent:true});
    businessState.items=access.items;businessState.canAdmin=access.canAdmin;businessState.userId=access.userId;
    state.data.businessDomains=access.items.map(x=>x.domain);
    businessState.report=null;businessState.seq++;
    renderBusinessDomainManagement();renderPlatformNav();
  }catch(e){host.innerHTML=`<div class="business-card business-error">${escapeHTML(e.message)}</div>`}
}
const businessManagementDrafts=new Map();
function businessManagementUserChoices(name,ids,disabled=false){
  const label=name==='managerIds'?'业务域管理员':'汇报查看者';
  return `<div class="business-user-picker" data-user-picker="${name}" data-selected="${escapeHTML(JSON.stringify(ids))}" data-disabled="${disabled}"><div class="business-selected-users"></div>${disabled?'':`<input type="search" class="business-user-search" aria-label="搜索${label}" placeholder="输入姓名或账号搜索" autocomplete="off"><div class="business-user-results" aria-label="${label}搜索结果"></div>`}<small class="business-user-selection-count" aria-live="polite"></small></div>`;
}
function bindBusinessUserPickers(host){
  const pickers=[...host.querySelectorAll('[data-user-picker]')].map(el=>({el,name:el.dataset.userPicker,ids:new Set(JSON.parse(el.dataset.selected).map(Number)),disabled:el.dataset.disabled==='true'}));
  const users=(state.data?.users||[]).map(user=>({user,search:`${user.chineseName||''} ${user.loginName||''}`.normalize('NFKC').toLocaleLowerCase()}));
  function renderSelected(picker){
    const el=picker.el;
    el.querySelector('.business-selected-users').innerHTML=[...picker.ids].map(id=>`<span class="business-user-chip"><input type="hidden" name="${picker.name}" value="${id}"><span class="business-user-avatar business-chip-avatar">${userAvatarHTML(userById(id),48)}</span><span>${escapeHTML(businessUserName(id))}</span>${picker.disabled?'':`<button type="button" data-remove-user="${id}" aria-label="移除${escapeHTML(businessUserName(id))}">×</button>`}</span>`).join('')||'<span class="business-no-selection">尚未选择</span>';
    el.querySelector('.business-user-selection-count').textContent=`已选择 ${picker.ids.size} 人${picker.disabled?' · 由平台管理员维护':''}`;
    el.querySelectorAll('[data-remove-user]').forEach(button=>button.onclick=()=>{picker.ids.delete(Number(button.dataset.removeUser));renderSelected(picker);pickers.forEach(renderResults)});
  }
  function renderResults(picker){
    if(picker.disabled)return;
    const input=picker.el.querySelector('.business-user-search'),result=picker.el.querySelector('.business-user-results'),query=input.value.trim().normalize('NFKC').toLocaleLowerCase();
    if(!query){result.innerHTML='<small>输入姓名或账号查找，可连续添加多位用户。</small>';return}
    const matches=users.filter(x=>x.search.includes(query)&&!picker.ids.has(Number(x.user.id))),shown=matches.slice(0,20),other=pickers.find(x=>x!==picker);
    result.innerHTML=shown.map(({user:u})=>{const assigned=other?.ids.has(Number(u.id));return `<button type="button" class="business-user-result" data-add-user="${Number(u.id)}" ${assigned?'disabled':''}><span class="business-user-avatar">${userAvatarHTML(u,48)}</span><span><b>${escapeHTML(u.chineseName||u.loginName)}</b><small>${escapeHTML(u.loginName)}</small></span><span class="business-user-add">${assigned?'已在另一角色':'＋ 添加'}</span></button>`}).join('')+(matches.length>20?`<small>共 ${matches.length} 个匹配，显示前 20 个，请继续输入缩小范围。</small>`:matches.length?'':'<small>没有匹配的未选用户，试试其他姓名或账号。</small>');
    result.querySelectorAll('[data-add-user]').forEach(button=>button.onclick=()=>{picker.ids.add(Number(button.dataset.addUser));input.value='';renderSelected(picker);pickers.forEach(renderResults);input.focus()});
  }
  pickers.forEach(picker=>{
    renderSelected(picker);renderResults(picker);
    const input=picker.el.querySelector('.business-user-search');if(!input)return;
    input.oninput=()=>renderResults(picker);
    input.onkeydown=e=>{if(e.key==='Enter'){e.preventDefault();picker.el.querySelector('[data-add-user]:not(:disabled)')?.focus()}else if(e.key==='ArrowDown'){e.preventDefault();picker.el.querySelector('[data-add-user]:not(:disabled)')?.focus()}else if(e.key==='Escape'){input.value='';renderResults(picker)}};
  });
}
function captureBusinessManagementDraft(){
  const f=$('#businessDomainForm');if(!f)return;
  const b=businessState,base=businessManagementDrafts.get(b.editorId)||b.items.find(x=>x.domain.id===b.editorId)?.domain||{active:true,managerIds:[],viewerIds:[]};
  const selected=name=>[...f.querySelectorAll(`input[type="hidden"][name="${name}"]`)].map(x=>Number(x.value));
  const value={...base,id:f.elements.id.value.trim(),name:f.elements.name.value.trim(),description:f.elements.description.value,active:b.canAdmin?f.elements.active.checked:base.active,managerIds:b.canAdmin?selected('managerIds'):base.managerIds,viewerIds:selected('viewerIds')};
  businessManagementDrafts.set(b.editorId,value);return value;
}
function renderBusinessDomainManagement(){
  const host=$('#platformBusinessDomains');if(!host)return;
  const b=businessState,editable=b.items.filter(x=>x.canManage);
  if(!b.canAdmin&&!editable.length){host.innerHTML='<div class="business-card">没有可管理的业务域。</div>';return}
  if(b.editorId===null||b.editorId&&!editable.some(x=>x.domain.id===b.editorId))b.editorId=editable[0]?.domain.id??'';
  const existing=editable.find(x=>x.domain.id===b.editorId),draft=businessManagementDrafts.get(b.editorId)||existing?.domain||{id:'',name:'',description:'',active:true,managerIds:[],viewerIds:[]};
  host.innerHTML=`<div class="business-management-layout"><aside class="business-catalog"><div class="business-catalog-head"><div><b>业务域</b><small>${editable.length} 个可管理</small></div>${b.canAdmin?'<button id="businessNew" class="button ghost small" type="button">＋ 新建</button>':''}</div><div class="business-catalog-list">${editable.map(x=>`<button type="button" class="business-catalog-item ${b.editorId===x.domain.id?'selected':''}" data-business-domain="${escapeHTML(x.domain.id)}" aria-pressed="${b.editorId===x.domain.id}"><span class="business-domain-avatar">${escapeHTML(x.domain.name.slice(0,1))}</span><span class="business-domain-item-text"><b>${escapeHTML(x.domain.name)}</b><small>${escapeHTML(x.domain.id)}</small></span><span class="business-status ${x.domain.active?'enabled':''}">${x.domain.active?'启用':'停用'}</span></button>`).join('')||'<p class="business-catalog-empty">还没有业务域，点击新建开始。</p>'}</div></aside><section class="business-editor"><header class="business-editor-head"><div><small>${existing?'编辑业务域':'创建业务域'}</small><h2>${escapeHTML(existing?.domain.name||'新建业务域')}</h2></div>${existing?`<span class="business-status ${existing.domain.active?'enabled':''}">${existing.domain.active?'启用中':'已停用'}</span>`:''}</header><form id="businessDomainForm" class="business-form"><div class="business-form-section business-wide"><h3>业务信息</h3><div class="business-form-grid"><label>业务域名称<input name="name" value="${escapeHTML(draft.name)}" required maxlength="100" placeholder="例如：DNS 控制台"></label><label>业务域标识<input name="id" value="${escapeHTML(draft.id)}" ${existing?'readonly':''} required maxlength="64" pattern="[a-z0-9][a-z0-9_-]{0,63}" placeholder="例如：dns-console"><small>${existing?'创建后不可修改':'使用小写字母、数字、下划线或短横线'}</small></label><label class="business-wide">业务说明<textarea name="description" maxlength="2000" placeholder="说明业务范围，便于项目选择归属">${escapeHTML(draft.description)}</textarea></label></div></div><div class="business-form-section business-wide"><h3>管理与查看权限</h3><p>管理员维护本域信息与查看者；查看者可查看本域汇总报告。</p><div class="business-form-grid"><div><div class="business-choice-heading">业务域管理员${b.canAdmin?'':'<small>由平台管理员配置</small>'}</div>${businessManagementUserChoices('managerIds',draft.managerIds||[],!b.canAdmin)}</div><div><div class="business-choice-heading">汇报查看者</div>${businessManagementUserChoices('viewerIds',draft.viewerIds||[])}</div></div></div><div class="business-form-section business-wide business-state-section"><div><h3>业务域状态</h3><p>停用后保留项目和统计，禁止新增归属。</p></div><label class="business-active"><input type="checkbox" name="active" ${draft.active?'checked':''} ${b.canAdmin?'':'disabled'}>启用业务域</label></div><footer class="business-editor-footer business-wide"><span id="businessSaveStatus" role="status" aria-live="polite">${existing?'修改后点击保存生效':'创建后，可在项目配置中设置归属'}</span><button class="button primary" type="submit">${existing?'保存修改':'创建业务域'}</button></footer></form></section></div>`;
  bindBusinessUserPickers(host);
  host.querySelectorAll('[data-business-domain]').forEach(button=>button.onclick=()=>{captureBusinessManagementDraft();b.editorId=button.dataset.businessDomain;renderBusinessDomainManagement()});
  const create=host.querySelector('#businessNew');if(create)create.onclick=()=>{captureBusinessManagementDraft();b.editorId='';renderBusinessDomainManagement();host.querySelector('[name="name"]').focus()};
  host.querySelector('#businessDomainForm').onsubmit=async e=>{
    e.preventDefault();const value=captureBusinessManagementDraft(),btn=e.currentTarget.querySelector('[type=submit]'),status=host.querySelector('#businessSaveStatus');
    if(!existing&&b.items.some(x=>x.domain.id===value.id)){status.textContent='业务域标识已存在，请使用新的标识';status.classList.add('business-error');return}
    btn.disabled=true;status.textContent='正在保存…';status.classList.remove('business-error');
    try{await api('/api/business-domains'+(existing?'':'?createOnly=true'),{method:'PUT',body:JSON.stringify(value)});businessManagementDrafts.delete(b.editorId);b.editorId=value.id;await loadBusinessDomainManagement();toast('业务域已保存')}
    catch(err){btn.disabled=false;status.textContent=err.message;status.classList.add('business-error')}
  };
}
