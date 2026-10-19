function renderProjectSwitcher(){
  const projects=state.data.projects||[];
  const switcher=$('#projectSwitcher');if(!switcher)return;
  switcher.innerHTML=projects.map(p=>`<option value="${escapeHTML(p.id)}" ${p.id===state.projectFilter?'selected':''}>${escapeHTML(p.key||'PROJECT')} · ${escapeHTML(p.name)}</option>`).join('')||'<option value="">暂无项目</option>';
  if(!projects.some(p=>p.id===state.projectFilter))state.projectFilter=projects[0]?.id||'';
}
function renderProjectContext(projectIssues){
  const projects=state.data.projects||[],project=projects.find(p=>p.id===state.projectFilter);renderProjectSwitcher();
  $('#projectContextMark').textContent=(project?.key||project?.name||'P').slice(0,2).toUpperCase();$('#projectContextMark').style.background=safeColor(project?.color);
  $('#projectContextName').textContent=project?.name||'暂无项目';$('#projectContextDescription').textContent=project?.description||'工作台数据会按项目整体切换';
  const active=projectIssues.filter(i=>issueRunHealth(i)==='active').length,attention=projectIssues.filter(i=>issueRunHealth(i)==='attention').length,artifacts=projectIssues.reduce((n,i)=>n+runArtifactCount(issueExecution(i).run),0),repo=(state.data.repositories||[]).find(r=>r.id===project?.defaultRepositoryId);
  const repoCount=(state.data.repositories||[]).filter(r=>r.projectId===project?.id).length;
  $('#projectContextMetrics').innerHTML=`<button type="button" data-issue-filter="" class="${!state.issueRunFilter?'on':''}"><span>工作项</span><b>${projectIssues.length}</b></button><button type="button" data-issue-filter="active" class="${state.issueRunFilter==='active'?'on':''}"><span>正在推进</span><b>${active}</b></button><button type="button" data-issue-filter="attention" class="${attention?'danger':''} ${state.issueRunFilter==='attention'?'on':''}" title="点此只看澄清未达标、门禁失败、运行失败或已中断的工作项"><span>需要介入</span><b>${attention}</b></button><button type="button" class="plain" disabled><span>交付物</span><b>${artifacts}</b></button><div class="project-default-sop"><span>代码仓库（${repoCount}）</span><b title="${escapeHTML(repo?.gongfengProjectId||'未绑定')}">默认：${escapeHTML(repo?.name||'未绑定')}</b></div>`;
  $('#projectContextMetrics').querySelectorAll('[data-issue-filter]').forEach(btn=>btn.onclick=()=>setIssueRunFilter(btn.dataset.issueFilter));
}
function defaultClarification(){return{enabled:true,skillId:'requirement-clarification',threshold:80,dimensions:[{id:'goal',name:'目标与价值',question:'要解决什么问题，预期产生什么业务价值？',weight:20,minLength:30},{id:'user',name:'用户与场景',question:'谁会在什么场景下使用？',weight:15,minLength:20},{id:'scope',name:'范围与边界',question:'本次包含什么、不包含什么？',weight:20,minLength:30},{id:'acceptance',name:'验收标准',question:'如何用可测试的条件判断需求完成？',weight:25,minLength:40},{id:'constraints',name:'约束与依赖',question:'有哪些技术约束、依赖、风险或截止时间？',weight:20,minLength:25}]}}
function clarificationPolicy(project){return project?.clarification?.dimensions?.length?project.clarification:defaultClarification()}
function requirementNeedsClarification(i){
  if(!i||i.kind!=='requirement')return false;
  if(!i.clarification?.gated)return false;
  if(i.clarification?.status==='analyzing'||i.clarification?.status==='ready')return false;
  if(latestIssueTask(i)||latestIssueRun(i))return false;
  const project=(state.data.projects||[]).find(p=>p.id===i.projectId);
  if(!clarificationPolicy(project).enabled)return false;
  return !i.clarification?.passed;
}
function clarificationNeedsConfirm(i){
  const c=i?.clarification||{};
  if(c.status!=='ready')return false;
  if(c.writeBackStatus!=='pending')return false;
  return (i.source==='tapd'||i.source==='gongfeng')&&!!i.externalRef;
}
function userCombobox(name,selected,emptyLabel='请选择用户',attributes=''){
  const current=userById(selected),label=current?userDisplay(current):emptyLabel;
  const options=(state.data.users||[]).map(u=>`<button type="button" class="user-combobox-option${Number(u.id)===Number(selected)?' selected':''}" data-user-option="${u.id}" data-search="${escapeHTML(`${u.loginName||''} ${u.chineseName||''}`.toLowerCase())}"><span>${escapeHTML(userDisplay(u))}</span>${Number(u.id)===Number(selected)?'<i>✓</i>':''}</button>`).join('');
  return `<div class="user-combobox" data-user-combobox><input type="hidden" name="${escapeHTML(name)}" value="${selected?escapeHTML(String(selected)):''}" ${attributes}><button type="button" class="user-combobox-trigger" data-user-combobox-trigger aria-haspopup="listbox" aria-expanded="false"><span>${escapeHTML(label)}</span><i>⌄</i></button><div class="user-combobox-panel" data-user-combobox-panel hidden><input type="search" data-user-combobox-search placeholder="搜索中文名 / 英文名" autocomplete="off"><div class="user-combobox-options" role="listbox">${options}</div><p class="user-combobox-empty" hidden>没有匹配的用户</p></div></div>`;
}
function bindUserSearches(root=document){
  if(!document.body.dataset.userComboboxBound){document.body.dataset.userComboboxBound='true';document.addEventListener('click',()=>document.querySelectorAll('[data-user-combobox].is-open').forEach(box=>{box.classList.remove('is-open');const panel=box.querySelector('[data-user-combobox-panel]'),trigger=box.querySelector('[data-user-combobox-trigger]');if(panel)panel.hidden=true;if(trigger)trigger.setAttribute('aria-expanded','false')}))}
  root.querySelectorAll('[data-user-combobox]').forEach(box=>{
    const value=box.querySelector('input[type="hidden"]'),trigger=box.querySelector('[data-user-combobox-trigger]'),panel=box.querySelector('[data-user-combobox-panel]'),search=box.querySelector('[data-user-combobox-search]'),empty=box.querySelector('.user-combobox-empty');
    const close=()=>{panel.hidden=true;box.classList.remove('is-open');trigger.setAttribute('aria-expanded','false')};
    trigger.onclick=e=>{e.stopPropagation();document.querySelectorAll('[data-user-combobox].is-open').forEach(other=>{if(other!==box){other.classList.remove('is-open');const p=other.querySelector('[data-user-combobox-panel]');if(p)p.hidden=true;other.querySelector('[data-user-combobox-trigger]')?.setAttribute('aria-expanded','false')}});const opening=panel.hidden;panel.hidden=!opening;box.classList.toggle('is-open',opening);trigger.setAttribute('aria-expanded',String(opening));if(opening){search.value='';box.querySelectorAll('[data-user-option]').forEach(o=>o.hidden=false);empty.hidden=true;setTimeout(()=>search.focus(),0)}};
    trigger.addEventListener('click',()=>{if(panel.hidden)return;const rect=trigger.getBoundingClientRect(),below=window.innerHeight-rect.bottom-14,above=rect.top-14,up=below<240&&above>below,height=Math.min(250,Math.max(100,up?above:below));Object.assign(panel.style,{position:'fixed',left:rect.left+'px',right:'auto',width:rect.width+'px',top:up?'auto':(rect.bottom+5)+'px',bottom:up?(window.innerHeight-rect.top+5)+'px':'auto'});box.querySelector('.user-combobox-options').style.maxHeight=Math.max(50,height-55)+'px'});
    panel.onclick=e=>e.stopPropagation();search.onclick=e=>e.stopPropagation();
    search.oninput=()=>{const query=search.value.trim().toLowerCase();let visible=0;box.querySelectorAll('[data-user-option]').forEach(option=>{option.hidden=Boolean(query&&!String(option.dataset.search||'').includes(query));if(!option.hidden)visible++});empty.hidden=visible>0};
    box.querySelectorAll('[data-user-option]').forEach(option=>option.onclick=e=>{e.stopPropagation();value.value=option.dataset.userOption;trigger.querySelector('span').textContent=option.querySelector('span').textContent;box.querySelectorAll('[data-user-option]').forEach(o=>{const selected=o===option;o.classList.toggle('selected',selected);o.setAttribute('aria-selected',String(selected));o.querySelector('i')?.remove();if(selected){const mark=document.createElement('i');mark.textContent='✓';o.append(mark)}});const avatar=box.closest('.member-row')?.querySelector('.member-avatar'),user=userById(value.value);if(avatar)avatar.innerHTML=userAvatarHTML(user,48);value.dispatchEvent(new Event('change',{bubbles:true}));close()});
    box.addEventListener('keydown',e=>{if(e.key==='Escape')close()});
  });
}
function repoDisplayURL(r){return String(r?.webUrl||r?.cloneUrl||r?.gongfengProjectId||'').trim()}
function emptyRepoRow(){return {name:'',repoUrl:'',gongfengProjectId:'',webUrl:'',cloneUrl:'',defaultBranch:'main',integrateBranch:'',localPath:''}}
function repoRowFromBound(r){const row={...emptyRepoRow(),id:r.id,name:r.name||'',repoUrl:repoDisplayURL(r),gongfengProjectId:r.gongfengProjectId||'',webUrl:r.webUrl||'',cloneUrl:r.cloneUrl||'',defaultBranch:r.defaultBranch||'main',integrateBranch:r.integrateBranch||'',localPath:r.localPath||''};const info=parseGongfengRepoURL(row.repoUrl||row.cloneUrl||row.gongfengProjectId);if(info){if(!row.name)row.name=info.name;if(!row.webUrl)row.webUrl=info.webUrl;if(!row.cloneUrl)row.cloneUrl=info.cloneUrl;if(!row.gongfengProjectId)row.gongfengProjectId=info.project}return row}
function applyRepoLinkToRow(raw,row){const info=parseGongfengRepoURL(raw);if(!info)return false;row.gongfengProjectId=info.project;row.name=info.name;row.webUrl=info.webUrl;row.cloneUrl=info.cloneUrl;return true}
function repoRowName(r){const info=parseGongfengRepoURL(r?.repoUrl||r?.webUrl||r?.cloneUrl||r?.gongfengProjectId||'');return String(r?.name||'').trim()||info?.name||''}
function repoRowLink(r){return String(r?.webUrl||r?.repoUrl||r?.cloneUrl||'').trim()}
function snapshotRepoRow(r){r.snapshot={name:r.name||'',repoUrl:r.repoUrl||'',defaultBranch:r.defaultBranch||'main',integrateBranch:r.integrateBranch||'',gongfengProjectId:r.gongfengProjectId||'',webUrl:r.webUrl||'',cloneUrl:r.cloneUrl||''}}
function restoreRepoSnapshot(r){if(!r.snapshot)return;Object.assign(r,r.snapshot);delete r.snapshot;delete r.editing;delete r.editingField}
function repoRowWebHref(r){const info=parseGongfengRepoURL(r?.webUrl||r?.repoUrl||r?.cloneUrl||r?.gongfengProjectId||'');return info?.webUrl||''}
function repoColValue(r,key){if(key==='name')return repoRowName(r);if(key==='repoUrl')return repoRowWebHref(r)||repoRowLink(r);if(key==='defaultBranch')return r.defaultBranch||'main';return String(r.integrateBranch||'').trim()}
async function persistRepoRow(row,projectId){const url=String(row.repoUrl||row.gongfengProjectId||'').trim();if(!url){toast('请填写GitHub 仓库地址');return null}if(!parseGongfengRepoURL(url)&&!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(url)){toast('Git 仓库链接无法识别，请粘贴 github.com 的 Web / SSH / clone URL');return null}if(!projectId||!(state.data.projects||[]).some(p=>p.id===projectId)){toast('请先保存项目');return null}const payload=repoPayloadFromRow(row,projectId);delete payload.gitToken;if(!payload.id)delete payload.id;const repo=await api('/api/repositories',{method:payload.id?'PUT':'POST',body:JSON.stringify(payload)});const ri=(state.data.repositories||[]).findIndex(x=>x.id===repo.id);if(ri>=0)state.data.repositories[ri]=repo;else(state.data.repositories||(state.data.repositories=[])).push(repo);return repo}
function repoAdminRowHTML(r,i){
  const cols=[{key:'name',ph:'项目名称'},{key:'repoUrl',ph:'GitHub Web / SSH / clone URL'},{key:'defaultBranch',ph:'主分支'},{key:'integrateBranch',ph:'集成分支（可选）'}];
  const editing=!!r.editing;
  const href=repoRowWebHref(r);
  const cells=cols.map(col=>{
    if(editing){
      const val=col.key==='name'?repoRowName(r):col.key==='repoUrl'?(r.repoUrl||repoRowLink(r)):col.key==='defaultBranch'?(r.defaultBranch||'main'):(r.integrateBranch||'');
      return `<div class="repo-cell repo-cell-edit"><input data-r="${col.key}" value="${escapeHTML(val)}" placeholder="${col.ph}" ${col.key==='integrateBranch'?'':'required'}></div>`;
    }
    const raw=repoColValue(r,col.key),empty=col.key==='integrateBranch'&&!raw;
    const text=empty?'未配置':escapeHTML(raw);
    if((col.key==='name'||col.key==='repoUrl')&&href){
      return `<a class="repo-cell repo-cell-link" href="${escapeHTML(href)}" target="_blank" rel="noopener noreferrer" title="${escapeHTML(href)}">${text}</a>`;
    }
    return `<span class="repo-cell ${empty?'is-empty':''}" title="${escapeHTML(raw)}">${text}</span>`;
  }).join('');
  const actions=editing
    ?`<div class="repo-row-actions"><button type="button" data-save-repo="${i}">保存</button><button type="button" data-cancel-repo="${i}">取消</button></div>`
    :`<div class="repo-row-actions"><button type="button" data-edit-repo="${i}">编辑</button><button type="button" data-remove-repo="${i}">删除</button></div>`;
  return `<div class="repo-draft-row${r.id&&!editing?' repo-bound-row':''}" data-repo-row="${i}" data-repo-id="${escapeHTML(r.id||'')}">${cells}${actions}</div>`;
}
function captureRepoRows(form,draft){
  if(!form||!draft)return;
  if(!Array.isArray(draft.repoRows))draft.repoRows=[];
  form.querySelectorAll('[data-repo-row]').forEach(row=>{
    const i=+row.dataset.repoRow,id=row.dataset.repoId||'';
    const bound=id?(state.data.repositories||[]).find(x=>x.id===id):null;
    if(!draft.repoRows[i])draft.repoRows[i]=bound?repoRowFromBound(bound):emptyRepoRow();
    const d=draft.repoRows[i];
    if(id)d.id=id;
    // Display rows have no inputs; keep the saved repository instead of an empty stub.
    if(!row.querySelector('[data-r]')){
      if(bound){Object.assign(d,repoRowFromBound(bound));return;}
      const href=row.querySelector('a.repo-cell-link')?.getAttribute('href')||'';
      if(href){d.repoUrl=href;d.webUrl=href;applyRepoLinkToRow(href,d)}
      return;
    }
    row.querySelectorAll('[data-r]').forEach(el=>{d[el.dataset.r]=el.value});
    const urlInput=row.querySelector('[data-r="repoUrl"]');
    if(urlInput)applyRepoLinkToRow(urlInput.value,d);
  });
}
function repoPayloadFromRow(r,projectId){const url=String(r.repoUrl||r.gongfengProjectId||r.webUrl||r.cloneUrl||'').trim();const info=parseGongfengRepoURL(url);const projectPath=(info&&info.project)||String(r.gongfengProjectId||'').trim();const existing=(state.data.repositories||[]).find(x=>x.id===r.id)||{};return {...existing,id:r.id||undefined,projectId,provider:(parseGongfengRepoURL(url)?.provider||'gongfeng'),name:String(r.name||'').trim()||(info&&info.name)||projectPath.slice(projectPath.lastIndexOf('/')+1),gongfengProjectId:projectPath,webUrl:(info&&info.webUrl)||String(r.webUrl||'').trim(),cloneUrl:(info&&info.cloneUrl)||String(r.cloneUrl||'').trim(),defaultBranch:String(r.defaultBranch||'main').trim(),integrateBranch:String(r.integrateBranch||'').trim(),localPath:String(r.localPath||existing.localPath||'').trim(),repoUrl:url}}
function renderProjectAdmin(){
  const list=$('#projectAdminList'),form=$('#projectAdminForm');if(!list||!form||!state.data)return;
  const projects=state.data.projects||[];$('#projectAdminCount').textContent=`${projects.length} 个`;
  list.innerHTML=projects.map(p=>{const issues=(state.data.issues||[]).filter(i=>i.projectId===p.id),active=issues.filter(i=>issueRunHealth(i)==='active').length,people=new Set([p.ownerId,...(p.members||[]).map(m=>m.userId)].filter(Boolean)).size;return`<button type="button" data-project-admin="${escapeHTML(p.id)}" class="${!state.projectDraft&&p.id===state.projectFilter?'active':''}"><i style="background:${safeColor(p.color)}">${escapeHTML((p.key||'P').slice(0,2))}</i><span><b>${escapeHTML(p.name)}</b><small>${people} 位成员 · ${issues.length} 个需求 · ${active} 推进中</small></span><em>›</em></button>`}).join('')||'<div class="empty-inline">还没有项目</div>';
  list.querySelectorAll('[data-project-admin]').forEach(button=>button.onclick=()=>{state.projectDraft=null;state.projectFilter=button.dataset.projectAdmin;localStorage.setItem('agentflow.activeProject',state.projectFilter);renderProjectAdmin()});
  const project=clone(state.projectDraft||projects.find(p=>p.id===state.projectFilter)||{id:'',key:'',name:'',description:'',color:'#536dfe',ownerId:'',members:[],clarification:defaultClarification(),runEval:defaultRunEval()});project.members=(project.members||[]).filter(m=>Number(m.userId)!==Number(project.ownerId));
  const repos=(state.data.repositories||[]).filter(r=>r.projectId===project.id);
  if(!Array.isArray(project.repoRows)){
    project.repoRows=repos.map(repoRowFromBound);
    if(Array.isArray(project.repoDrafts)&&project.repoDrafts.length){
      project.repoRows=project.repoRows.concat(project.repoDrafts.map(d=>({...emptyRepoRow(),...d,id:'',repoUrl:d.repoUrl||d.gongfengProjectId||d.webUrl||d.cloneUrl||''})));
    }
  }
  delete project.repoDrafts;
  const repoRows=project.repoRows;
  if(String(project.defaultRepositoryId||'').startsWith('draft:'))project.defaultRepositoryId='';
  const savedRepoOptions=repoRows.filter(r=>r.id).map(r=>`<option value="${escapeHTML(r.id)}" ${r.id===project.defaultRepositoryId?'selected':''}>${escapeHTML(repoRowName(r)||'（未命名仓库）')}</option>`).join('');
  const draftRepoRows=repoRows.map((r,i)=>repoAdminRowHTML(r,i)).join('');
  const members=project.members.map((m,i)=>{const u=userById(m.userId);return`<div class="member-row" data-member-row="${i}"><span class="member-avatar">${userAvatarHTML(u,48)}</span><span class="member-kind">成员</span>${userCombobox(`member-${i}`,m.userId,'请选择项目成员','data-m="userId"')}<button type="button" data-remove-member="${i}" title="移除成员">×</button></div>`}).join('');
  const owner=userById(project.ownerId);
  const ownerRow=`<div class="member-row owner-row"><span class="member-avatar owner">${userAvatarHTML(owner,48)}</span><span class="member-kind owner">Owner</span>${userCombobox('ownerId',project.ownerId,'请选择项目 Owner')}<span></span></div>`;
  form.innerHTML=`<div class="admin-form-head"><div><span>${project.id?'编辑项目':'创建项目'}</span><h2>${escapeHTML(project.name||'新项目')}</h2></div></div><section><div class="admin-section-title"><div><b>项目信息</b><span>工作空间名称、默认 SOP 与代码仓库</span></div></div><div class="admin-grid"><label>项目标识<input name="id" value="${escapeHTML(project.id)}" ${project.id?'readonly':''} required placeholder="project-id"></label><label>项目 Key<input name="key" value="${escapeHTML(project.key||'')}" required placeholder="PROJ"></label><label>项目名称<input name="name" value="${escapeHTML(project.name||'')}" required></label><label>品牌色<span class="color-field"><input name="color" type="color" value="${safeColor(project.color)}"><code data-color-value>${safeColor(project.color)}</code><small>用于项目头像和标签</small></span></label><label class="wide">项目说明<textarea name="description">${escapeHTML(project.description||'')}</textarea></label><label>默认代码仓库<select name="defaultRepositoryId" class="select"><option value="">暂未绑定</option>${savedRepoOptions}</select></label>${projectBusinessDomainField(project)}<div class="wide repo-inline-block"><div class="repo-inline-table"><div class="repo-row-head"${repoRows.length?'':' hidden'}><span>仓库</span><span>链接</span><span>主分支</span><span>集成分支</span><span></span></div><div id="repoDraftRows">${draftRepoRows||'<div class="admin-empty">还没有绑定仓库，用下方「＋ 添加仓库」直接添加。</div>'}</div></div><button id="addRepoDraft" type="button" class="button ghost small">＋ 添加仓库</button><span class="repo-inline-tip">已保存仓库按列展示，也只有这些会出现在上方默认仓库下拉。新增行随「保存项目设置」写库，未绑定默认时自动用第一个新仓库。点项目名称或链接打开仓库页面；点「编辑」可改整行。新增请粘贴 GitHub Web、SSH 或 clone URL，名称会自动识别。集成分支用于检查多个 SOP 执行后工作分支之间是否存在合并冲突；不填则跳过冲突对齐（部分仓库不允许创建 agentflow/ 分支）。</span></div>${project.tapdWorkspaceId||project.tapdIterationId?`<label>TAPD 需求池 Workspace<input name="tapdWorkspaceId" value="${escapeHTML(project.tapdWorkspaceId||'')}" placeholder=""></label><label>TAPD 迭代 Iteration<input name="tapdIterationId" value="${escapeHTML(project.tapdIterationId||'')}" placeholder=""></label><label class="wide clear-toggle"><input name="tapdClear" type="checkbox"> 清除已保存的 TAPD 配置（Workspace + Iteration）${project.tapdWorkspaceId||project.tapdIterationId?' <em>当前已配置</em>':''}</label>`:''}</div></section><section><div class="admin-section-title"><div><b>项目成员</b><span>Owner 是项目的默认责任人；其他成员无需设置角色</span></div><button id="addMember" type="button" class="button ghost small">＋ 添加成员</button></div><div id="memberRows">${ownerRow}${members||'<div class="admin-empty member-empty">尚未添加其他成员</div>'}</div></section><div class="admin-bottom">${project.id?'<button id="adminRepositories" type="button" class="button ghost">管理Git 仓库</button>':''}<button class="button primary" type="submit">保存项目设置</button></div>`;
  const draft=project;
  // 重渲染前把表单里未保存的文本输入（含密码框）同步进草稿，避免重绘时丢失
  const captureProjectForm=()=>{const el=form.elements;['key','name','description','color','workflowId','tapdWorkspaceId','tapdIterationId'].forEach(n=>{if(el[n])draft[n]=el[n].value});draft.ownerId=Number(el.ownerId?.value)||0;if(el.defaultRepositoryId)draft.defaultRepositoryId=el.defaultRepositoryId.value;if(el.businessDomainId)draft.businessDomainId=el.businessDomainId.value;captureRepoRows(form,draft);state.projectDraft=draft};
  const domainSelect=form.elements.businessDomainId;if(domainSelect)domainSelect.onchange=captureProjectForm;
  const colorInput=form.elements.color;if(colorInput)colorInput.oninput=()=>{form.querySelector('[data-color-value]').textContent=colorInput.value};
  bindUserSearches(form);form.querySelectorAll('[data-member-row]').forEach(row=>row.querySelectorAll('[data-m]').forEach(el=>el.onchange=()=>{draft.members[+row.dataset.memberRow].userId=Number(el.value);draft.members[+row.dataset.memberRow].role='member';state.projectDraft=draft}));form.querySelectorAll('[data-remove-member]').forEach(b=>b.onclick=()=>{captureProjectForm();draft.members.splice(+b.dataset.removeMember,1);state.projectDraft=draft;renderProjectAdmin()});$('#addMember').onclick=()=>{captureProjectForm();draft.members.push({userId:0,role:'member'});state.projectDraft=draft;renderProjectAdmin()};
  form.querySelectorAll('[data-repo-row]').forEach(row=>row.querySelectorAll('[data-r]').forEach(el=>{
    el.onchange=()=>{const d=draft.repoRows[+row.dataset.repoRow];if(!d)return;d[el.dataset.r]=el.value;if(el.dataset.r==='repoUrl'&&applyRepoLinkToRow(el.value,d)){const nameInput=row.querySelector('[data-r="name"]');if(nameInput&&d.name)nameInput.value=d.name}state.projectDraft=draft};
    el.onkeydown=e=>{if(e.key!=='Enter')return;e.preventDefault();row.querySelector('[data-save-repo]')?.click()};
  }));
  const projectId=()=>draft.id||state.projectFilter||'';
  const finishRepoRow=(i,repo)=>{const next=repoRowFromBound(repo);draft.repoRows[i]=next;if(!draft.defaultRepositoryId||String(draft.defaultRepositoryId).startsWith('draft:'))draft.defaultRepositoryId=next.id;state.projectDraft=draft;renderProjectAdmin()};
  form.querySelectorAll('[data-edit-repo]').forEach(b=>b.onclick=()=>{captureProjectForm();const d=draft.repoRows[+b.dataset.editRepo];if(!d)return;snapshotRepoRow(d);d.editing=true;d.editingField='';state.projectDraft=draft;renderProjectAdmin()});
  form.querySelectorAll('[data-cancel-repo]').forEach(b=>b.onclick=()=>{captureProjectForm();const i=+b.dataset.cancelRepo,d=draft.repoRows[i];if(!d)return;if(!d.id){if(draft.defaultRepositoryId===`draft:${i}`)draft.defaultRepositoryId='';draft.repoRows.splice(i,1)}else restoreRepoSnapshot(d);state.projectDraft=draft;renderProjectAdmin()});
  form.querySelectorAll('[data-save-repo]').forEach(b=>b.onclick=async()=>{captureProjectForm();const i=+b.dataset.saveRepo,d=draft.repoRows[i];if(!d)return;try{const repo=await persistRepoRow(d,projectId());if(!repo)return;toast(d.id?'仓库已保存':'仓库已添加');finishRepoRow(i,repo)}catch(err){toast(err.message)}});
  form.querySelectorAll('[data-remove-repo]').forEach(b=>b.onclick=async()=>{captureProjectForm();const i=+b.dataset.removeRepo,row=draft.repoRows[i];if(row?.id){if(!(await afConfirm({title:'删除关联仓库',message:`确定删除仓库「${repoRowName(row)||row.repoUrl||row.id}」吗？该操作不可恢复。`,danger:true,confirmText:'确定删除'})))return;try{await api('/api/repositories?id='+encodeURIComponent(row.id),{method:'DELETE'});state.data.repositories=(state.data.repositories||[]).filter(r=>r.id!==row.id);if(draft.defaultRepositoryId===row.id)draft.defaultRepositoryId='';toast('仓库已删除')}catch(err){return toast(err.message)}}else if(draft.defaultRepositoryId===`draft:${i}`)draft.defaultRepositoryId='';draft.repoRows.splice(i,1);state.projectDraft=draft;renderProjectAdmin()});
  const addRepoBtn=$('#addRepoDraft');if(addRepoBtn)addRepoBtn.onclick=()=>{captureProjectForm();draft.repoRows.push({...emptyRepoRow(),editing:true});state.projectDraft=draft;renderProjectAdmin()};
  const adminReposBtn=$('#adminRepositories');if(adminReposBtn)adminReposBtn.onclick=()=>{state.projectFilter=project.id;openRepositoryModal()};
  const editingInput=form.querySelector('.repo-cell-edit input');
  if(editingInput){editingInput.scrollIntoView({block:'nearest',inline:'center'});editingInput.focus();}
}
async function saveProjectAdmin(e){e.preventDefault();const f=e.currentTarget,base=clone(state.projectDraft||(state.data.projects||[]).find(p=>p.id===state.projectFilter)||{});captureRepoRows(f,base);const repoRows=Array.isArray(base.repoRows)?base.repoRows:[],rawDefault=f.elements.defaultRepositoryId.value,ownerId=Number(f.elements.ownerId.value)||0,projectId=f.elements.id.value.trim(),repoKnown=id=>(state.data.repositories||[]).some(r=>r.id===id&&r.projectId===projectId),value={...base,id:projectId,key:f.elements.key.value.trim().toUpperCase(),name:f.elements.name.value.trim(),description:f.elements.description.value,color:f.elements.color.value,ownerId,workflowId:base.workflowId||f.elements.workflowId?.value||'',defaultRepositoryId:(!rawDefault||rawDefault.startsWith('draft:')||!repoKnown(rawDefault))?'':rawDefault,members:(base.members||[]).map(m=>({userId:Number(m.userId)||0,role:'member'})).filter(m=>m.userId!==ownerId),clarification:base.clarification||defaultClarification(),runEval:base.runEval,tapdWorkspaceId:(f.elements.tapdWorkspaceId?.value||'').trim(),tapdIterationId:(f.elements.tapdIterationId?.value||'').trim()};const savedBefore=state.data.projects.find(p=>p.id===value.id),expectedDomainId=savedBefore?.businessDomainId||'',targetDomainId=f.elements.businessDomainId?.value??expectedDomainId;delete value.businessDomainId;delete value.repoRows;delete value.repoDrafts;delete value.gitToken;delete value.codeBuddyApiKey;delete value.codeBuddyInternetEnvironment;if(f.elements.tapdClear?.checked)value.tapdClear=true;if(!value.id||!value.name)return toast('项目标识和名称不能为空');if(!value.ownerId)return toast('请选择项目 Owner');if(value.members.some(m=>!m.userId))return toast('请选择有效的项目成员');if(new Set(value.members.map(m=>m.userId)).size!==value.members.length)return toast('项目成员不能重复');
  const safeOpaqueRepoId=s=>/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(s)&&!s.includes('..');
  for(const r of repoRows){const url=String(r.repoUrl||r.gongfengProjectId||r.webUrl||r.cloneUrl||'').trim();if(!url)return toast('每个仓库都需要填写GitHub 仓库地址，可粘贴 Web / SSH / clone URL');if(!parseGongfengRepoURL(url)&&!safeOpaqueRepoId(url))return toast(`仓库「${url}」无法识别为Git 仓库链接，请粘贴 github.com 的链接或填写 group/repository 项目路径`)}
  try{const saved=await api('/api/projects',{method:state.data.projects.some(p=>p.id===value.id)?'PUT':'POST',body:JSON.stringify(value)});const savedProjectId=(saved&&saved.id)||value.id;const i=state.data.projects.findIndex(p=>p.id===saved.id||p.id===value.id);if(i>=0)state.data.projects[i]=saved&&saved.id?saved:value;else state.data.projects.push(saved&&saved.id?saved:value);
    if(targetDomainId!==expectedDomainId){
      try{const assigned=await api('/api/projects/'+encodeURIComponent(savedProjectId)+'/business-domain',{method:'PUT',body:JSON.stringify({businessDomainId:targetDomainId,expectedDomainId})});const pi=state.data.projects.findIndex(p=>p.id===savedProjectId);if(pi>=0)state.data.projects[pi]=assigned;businessState.report=null;businessState.seq++}catch(err){throw new Error('项目设置已保存，业务域保存失败：'+err.message)}
    }
    let defaultRepoId=value.defaultRepositoryId;const draftIdMap={};
    for(let idx=0;idx<repoRows.length;idx++){const row=repoRows[idx];if(row.id&&!row.editing){draftIdMap[`draft:${idx}`]=row.id;continue;}const payload=repoPayloadFromRow(row,savedProjectId);delete payload.gitToken;if(!payload.id)delete payload.id;const repo=await api('/api/repositories',{method:payload.id?'PUT':'POST',body:JSON.stringify(payload)});const ri=(state.data.repositories||[]).findIndex(x=>x.id===repo.id);if(ri>=0)state.data.repositories[ri]=repo;else(state.data.repositories||(state.data.repositories=[])).push(repo);draftIdMap[`draft:${idx}`]=repo.id;}
    const createdIds=repoRows.map((r,idx)=>r.id?'':draftIdMap[`draft:${idx}`]).filter(Boolean);
    if(rawDefault.startsWith('draft:')&&draftIdMap[rawDefault])defaultRepoId=draftIdMap[rawDefault];
    if(!defaultRepoId&&!rawDefault&&createdIds.length)defaultRepoId=createdIds[0];
    if(defaultRepoId&&defaultRepoId!==value.defaultRepositoryId){const proj=(state.data.projects||[]).find(p=>p.id===savedProjectId)||value;const saved2=await api('/api/projects',{method:'PUT',body:JSON.stringify({...proj,defaultRepositoryId:defaultRepoId})});const pi=(state.data.projects||[]).findIndex(p=>p.id===savedProjectId);if(saved2&&saved2.id&&pi>=0)state.data.projects[pi]=saved2;else if(pi>=0)state.data.projects[pi].defaultRepositoryId=defaultRepoId;}
    state.projectFilter=savedProjectId;state.projectDraft=null;localStorage.setItem('agentflow.activeProject',savedProjectId);renderAll();toast(repoRows.length?'项目与仓库设置已保存':'项目、Owner 和成员已保存')}catch(err){toast(err.message)}}


// Project attribution is edited here; the business-domain page owns the catalog and reports.
function projectBusinessDomainField(project){
  const saved=(state.data.projects||[]).find(p=>p.id===project.id);
  const admin=!state.authRequired||isPlatformAdmin();
  const uid=Number(state.currentUser?.id)||0;
  const canSetInitialDomain=uid>0&&(!saved||(!saved.businessDomainId&&Number(saved.ownerId)===uid));
  const canAssign=admin||canSetInitialDomain;
  const current=project.businessDomainId||'';
  const domains=state.data.businessDomains||[];
  const options=domains
    .filter(d=>d.active||d.id===current)
    .map(d=>`<option value="${escapeHTML(d.id)}" ${d.id===current?'selected':''}>${escapeHTML(d.name)}${d.active?'':'（已停用）'}</option>`)
    .join('');
  const missing=current&&!domains.some(d=>d.id===current)
    ?`<option value="${escapeHTML(current)}" selected>${escapeHTML(current)}</option>`:'';
  return `<div class="project-domain-field"><label for="projectBusinessDomainSelect">主业务域</label><select id="projectBusinessDomainSelect" name="businessDomainId" class="select" ${canAssign?'':'disabled'}><option value="">未分类</option>${options}${missing}</select></div>`;
}
