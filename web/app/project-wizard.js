// New-project wizard: a modal over the project list, laid out like the workbench quick-access wizard
// (chatbot/src/QuickAccessWizard.jsx). Step 1 creates the project; later steps edit the saved project
// and can be skipped. Progress lives only in memory.
const PROJECT_WIZARD_STEPS=[
  {id:'basic',title:'基本信息',desc:'名称与默认 SOP'},
  {id:'repos',title:'代码仓库',desc:'绑定 GitHub 仓库'},
  {id:'members',title:'项目成员',desc:'Owner 与协作成员'},
  {id:'tapd',title:'TAPD 集成',desc:'可选，导入 TAPD 需求'},
  {id:'done',title:'完成',desc:'开始使用新项目'},
];
function projectWizardSteps(){return PROJECT_WIZARD_STEPS.filter(s=>s.id!=='tapd'||tapdIntegrationAvailable(projectWizardProject()))}
const PW_ICON_PATHS={
  check:'<path d="M5 12.5 10 17 19 7"/>',
  x:'<path d="M18 6 6 18M6 6l12 12"/>',
  folder:'<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M12 11v5M9.5 13.5h5"/>',
  warn:'<circle cx="12" cy="12" r="9"/><path d="M12 8v5M12 16h.01"/>',
  play:'<path d="M8 5v14l11-7z" fill="currentColor" stroke="none"/>',
  gear:'<path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12"/><circle cx="16" cy="6" r="2"/><circle cx="10" cy="12" r="2"/><circle cx="18" cy="18" r="2"/>',
  next:'<path d="m9 6 6 6-6 6"/>',
  flag:'<path d="M5 21V4"/><path d="M5 4h11l-2 4 2 4H5"/>',
  back:'<path d="m15 6-6 6 6 6"/>',
  repo:'<circle cx="6" cy="6" r="2.3"/><circle cx="6" cy="18" r="2.3"/><circle cx="18" cy="8" r="2.3"/><path d="M6 8.3v7.4M18 10.3c0 3.4-4.5 3.6-9.8 6"/>',
};
function pwIcon(name,size=14,width=2){return `<svg width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="${width}" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${PW_ICON_PATHS[name]||''}</svg>`}

let projectWizard=null;
function projectWizardStepIndex(step){return projectWizardSteps().findIndex(s=>s.id===step)}
function projectWizardProject(){const id=projectWizard?.projectId;return id?(state.data.projects||[]).find(p=>p.id===id)||null:null}
function projectWizardRepos(){const id=projectWizard?.projectId;return id?(state.data.repositories||[]).filter(r=>r.projectId===id):[]}
function blankProjectWizardRepo(){return {url:'',branch:'main',integrate:''}}

function openProjectWizard(){
  if(projectWizard){focusProjectWizard();return}
  const draft=blankProjectDraft();
  projectWizard={step:'basic',projectId:'',draft,base:clone(draft),done:{},skipped:{},saving:false,error:'',focus:'name',
    seed:Math.random().toString(36).slice(2,6),repo:blankProjectWizardRepo(),returnFocus:document.activeElement||null,hash:location.hash};
  renderProjectWizard();
}
function projectWizardDirty(){
  const w=projectWizard;if(!w||w.step==='done')return false;
  if(w.step==='repos')return !!String(w.repo.url||'').trim();
  return projectDraftDirty(w.draft,w.base);
}
function projectWizardConfirmDiscard(message){
  if(!projectWizardDirty())return Promise.resolve(true);
  return afConfirm({title:'放弃未保存的修改？',message,danger:true,confirmText:'放弃修改',cancelText:'继续编辑'});
}
function projectWizardMark(step,done){
  const w=projectWizard;
  if(done){w.done[step]=true;delete w.skipped[step]}else{w.skipped[step]=true;delete w.done[step]}
}
function projectWizardEnter(step){
  const w=projectWizard;if(!w)return;
  w.step=step;w.error='';w.focus='title';w.repo=blankProjectWizardRepo();
  const project=projectWizardProject();
  if(project&&(step==='members'||step==='tapd')){w.draft=projectDraftFrom(project);w.base=clone(w.draft)}
  if(step==='done')w.done.done=true;
  renderProjectWizard();
}
async function closeProjectWizard(opt={}){
  const w=projectWizard;if(!w||w.saving)return false;
  if(!opt.force&&!(await projectWizardConfirmDiscard(w.step==='basic'?'新项目还没有创建，关闭后填写的内容会丢失。':'项目已创建并保留，但本步骤未保存的修改会丢失。')))return false;
  if(projectWizard!==w)return false;
  projectWizard=null;
  $('#projectWizard')?.remove?.();
  document.body?.classList?.remove('pw-open');
  if(parseProjectAdminRoute(location.hash)?.id==='new')history.replaceState(null,'',projectAdminHash(''));
  if(activeViewName()==='projects')renderProjectAdmin();
  const back=w.returnFocus;
  if(!opt.noFocus&&back&&document.contains(back))back.focus?.();
  return true;
}
// Runs one save request with the footer locked; failures land in the error banner.
async function projectWizardRequest(run,prefix=''){
  const w=projectWizard;if(!w||w.saving)return null;
  w.saving=true;w.error='';renderProjectWizard();
  try{return await run()}
  catch(err){if(projectWizard===w)w.error=prefix+(err?.message||String(err));return null}
  finally{w.saving=false;if(projectWizard===w)renderProjectWizard()}
}
function projectWizardFail(message,focus=''){
  const w=projectWizard;w.error=message;w.focus=focus;renderProjectWizard();return false;
}

async function projectWizardCreate(){
  const w=projectWizard,d=w.draft,projects=state.data.projects||[];
  const ident=generateProjectIdentity(d.name,projects,w.seed);
  const value=buildProjectPayload(clone(d),{id:ident.id,key:ident.key,defaultRepositoryId:''});
  const invalid=validateProjectPayload(value,{creating:true,projects,workflows:state.data.workflows||[]});
  if(invalid)return projectWizardFail(invalid.field==='id'||invalid.field==='key'?'无法根据名称生成可用的项目标识，请换个名称重试':invalid.message,['name','workflowId','ownerId'].includes(invalid.field)?invalid.field:'');
  let domainError='';
  const stored=await projectWizardRequest(async()=>{
    const result=await api('/api/projects',{method:'POST',body:JSON.stringify(value)});
    const saved=storeSavedProject(result,value);
    domainError=await saveProjectBusinessDomain(saved.id,String(d.businessDomainId||''),'');
    return saved;
  },'创建失败，可修改项目名称后重试：');
  if(!stored||projectWizard!==w)return false;
  w.projectId=stored.id||value.id;
  if(!state.projectFilter){state.projectFilter=w.projectId;localStorage.setItem('agentflow.activeProject',w.projectId)}
  projectWizardMark('basic',true);
  renderAll();
  toast(domainError?`项目已创建，业务域保存失败：${domainError}`:`项目已创建 · 标识 ${w.projectId} · Key ${stored.key||value.key}`,domainError?'err':'ok');
  projectWizardEnter('repos');
  return true;
}
async function projectWizardAddRepo(){
  const w=projectWizard,project=projectWizardProject();if(!w||w.saving||!project)return false;
  const url=String(w.repo.url||'').trim(),info=parseGongfengRepoURL(url);
  if(!info)return projectWizardFail(url?'无法识别为 GitHub 仓库地址，请检查后重试':'请先粘贴 GitHub 仓库地址','repoUrl');
  if(projectWizardRepos().some(r=>r.gongfengProjectId===info.project))return projectWizardFail(`仓库 ${info.project} 已经绑定到这个项目`,'repoUrl');
  const payload={projectId:project.id,provider:info.provider||'gongfeng',name:info.name,gongfengProjectId:info.project,webUrl:info.webUrl,cloneUrl:info.cloneUrl,defaultBranch:String(w.repo.branch||'').trim()||'main',integrateBranch:String(w.repo.integrate||'').trim(),localPath:'',repoUrl:url};
  const makeDefault=projectNeedsDefaultRepository(project);
  let defaultError='';
  const repo=await projectWizardRequest(async()=>{
    const saved=await api('/api/repositories',{method:'POST',body:JSON.stringify(payload)});
    const list=state.data.repositories||(state.data.repositories=[]),ri=list.findIndex(r=>r.id===saved.id);
    if(ri>=0)list[ri]=saved;else list.push(saved);
    if(makeDefault){try{await saveProjectDefaultRepository(project,saved.id)}catch(err){defaultError='仓库已保存，设为默认失败：'+err.message}}
    return saved;
  });
  if(!repo||projectWizard!==w)return false;
  w.repo=blankProjectWizardRepo();w.focus='repoUrl';
  renderAll();renderProjectWizard();
  toast(defaultError||(makeDefault?'仓库已保存，并设为默认仓库':'仓库已保存'),defaultError?'err':'ok');
  return true;
}
async function projectWizardRemoveRepo(repoId){
  const w=projectWizard,project=projectWizardProject(),repo=projectWizardRepos().find(r=>r.id===repoId);
  if(!w||w.saving||!project||!repo)return false;
  const name=repo.name||repo.gongfengProjectId||repo.id,isDefault=project.defaultRepositoryId===repo.id;
  const confirmed=await afConfirm({title:'移除仓库',message:`确定移除仓库「${name}」吗？${isDefault?'\n\n这是当前的默认仓库，移除后会把剩余的第一个仓库设为默认。':''}`,danger:true,confirmText:'移除',cancelText:'取消'});
  if(!confirmed||projectWizard!==w)return false;
  let notice=`已移除仓库「${name}」`,tone='ok';
  const ok=await projectWizardRequest(async()=>{
    await api('/api/repositories?id='+encodeURIComponent(repo.id),{method:'DELETE'});
    state.data.repositories=(state.data.repositories||[]).filter(r=>r.id!==repo.id);
    if(isDefault){
      project.defaultRepositoryId='';
      const next=projectWizardRepos()[0];
      if(next){
        try{await saveProjectDefaultRepository(project,next.id);notice+=`，已将「${next.name||next.gongfengProjectId||next.id}」设为默认仓库`}
        catch(err){notice+='，设置新的默认仓库失败：'+err.message;tone='err'}
      }
    }
    return true;
  });
  if(!ok)return false;
  renderAll();toast(notice,tone);
  return true;
}
async function projectWizardSaveProject(){
  const w=projectWizard,saved=projectWizardProject();
  if(!saved)return projectWizardFail('项目不存在或无权访问');
  const d=w.draft,fields=w.step==='members'?{ownerId:d.ownerId,members:d.members}:{tapdWorkspaceId:d.tapdWorkspaceId,tapdIterationId:d.tapdIterationId};
  const value=buildProjectPayload(clone(saved),fields);
  const invalid=validateProjectPayload(value,{creating:false,projects:state.data.projects||[],workflows:state.data.workflows||[],saved});
  if(invalid)return projectWizardFail(invalid.message);
  const stored=await projectWizardRequest(async()=>storeSavedProject(await api('/api/projects',{method:'PUT',body:JSON.stringify(value)}),value));
  if(stored)renderAll();
  return !!stored;
}
async function projectWizardNext(){
  const w=projectWizard;if(!w||w.saving)return;
  if(w.step==='basic')return projectWizardCreate();
  if(w.step==='repos'){
    if(String(w.repo.url||'').trim()&&!(await projectWizardAddRepo()))return;
    projectWizardMark('repos',projectWizardRepos().length>0);
    return projectWizardEnter('members');
  }
  if(w.step!=='members'&&w.step!=='tapd')return;
  const changed=projectWizardDirty();
  if(changed&&!(await projectWizardSaveProject()))return;
  if(projectWizard!==w)return;
  const p=projectWizardProject()||{};
  if(w.step==='members'){
    projectWizardMark('members',changed||(p.members||[]).some(m=>Number(m.userId)!==Number(p.ownerId)));
    return projectWizardEnter(projectWizardStepIndex('tapd')>=0?'tapd':'done');
  }
  projectWizardMark('tapd',!!(p.tapdWorkspaceId||p.tapdIterationId));
  return projectWizardEnter('done');
}
async function projectWizardSkip(){
  const w=projectWizard;if(!w||w.saving||!['repos','members','tapd'].includes(w.step))return;
  if(!(await projectWizardConfirmDiscard('跳过会放弃本步骤未保存的修改。'))||projectWizard!==w)return;
  const step=w.step;
  projectWizardMark(step,step==='repos'&&projectWizardRepos().length>0);
  projectWizardEnter(projectWizardSteps()[projectWizardStepIndex(step)+1].id);
}
async function projectWizardBack(){
  const w=projectWizard;if(!w||w.saving||!['members','tapd'].includes(w.step))return;
  if(!(await projectWizardConfirmDiscard('返回上一步会放弃本步骤未保存的修改。'))||projectWizard!==w)return;
  projectWizardEnter(projectWizardSteps()[projectWizardStepIndex(w.step)-1].id);
}
async function projectWizardStartIssues(){
  const id=projectWizard?.projectId;if(!id)return;
  await closeProjectWizard({force:true,noFocus:true});
  const switcher=$('#projectSwitcher');
  if(switcher){renderProjectSwitcher();switcher.value=id;switcher.dispatchEvent(new Event('change',{bubbles:true}))}
  else{state.projectFilter=id;localStorage.setItem('agentflow.activeProject',id)}
  switchView('issues');
}
async function projectWizardOpenSettings(){
  const id=projectWizard?.projectId;if(!id)return;
  await closeProjectWizard({force:true,noFocus:true});
  openProjectAdminDetail(id);
}
function projectWizardAddMember(){
  const w=projectWizard;if(!w||w.saving||w.step!=='members')return;
  w.draft.members.push({userId:0,role:'member'});w.error='';
  renderProjectWizard();
  setTimeout(()=>[...($('#projectWizard')?.querySelectorAll('[data-pw-member] [data-user-combobox-trigger]')||[])].pop()?.click(),0);
}
function projectWizardRemoveMember(index){
  const w=projectWizard;if(!w||w.saving||w.step!=='members')return;
  w.draft.members.splice(index,1);w.error='';renderProjectWizard();
}

// Rendering.
function pwCard(title,desc,pill,body){return `<section class="pw-card"><header class="pw-card-h"><div><strong>${title}</strong><span>${desc}</span></div>${pill}</header><div class="pw-card-b">${body}</div></section>`}
function pwPill(text,tone='',attrs=''){return `<span class="pw-pill${tone?' is-'+tone:''}"${attrs}>${text}</span>`}
function pwField(label,control,opt={}){return `<div class="pw-field"><label class="pw-label"${opt.for?` for="${opt.for}"`:''}>${label}${opt.required?' <em>*</em>':''}</label>${control}${opt.hint!=null?`<div class="pw-hint"${opt.hintAttrs||''}>${opt.hint}</div>`:''}</div>`}
function pwSelect(html){return html.replace(' id="projectBusinessDomainSelect"','').replace('class="select"','class="pw-input pw-select"')}
function projectWizardRailHTML(w){
  const cur=projectWizardStepIndex(w.step);
  const steps=projectWizardSteps().map((s,i)=>{
    const active=i===cur,done=!active&&!!w.done[s.id],skipped=!active&&!done&&!!w.skipped[s.id];
    return `<li class="pw-step${active?' is-active':''}${done?' is-done':''}${skipped?' is-skipped':''}"${active?' aria-current="step"':''}><span class="pw-step-mark">${done?pwIcon('check',13,2.6):s.id==='done'?pwIcon('flag',13,2.2):i+1}</span><div class="pw-step-copy"><div class="pw-step-title">${s.title}</div><div class="pw-step-desc">${skipped?'<i>已跳过</i> · 可在项目设置补充':s.desc}</div></div></li>`;
  }).join('');
  return `<aside class="pw-rail"><div class="pw-rail-head"><div class="pw-brand">${pwIcon('folder',20,1.9)}</div><h2 id="projectWizardLabel">新建项目</h2><p>只需名称即可创建，再按步骤绑定仓库、邀请成员。</p></div><ol class="pw-steps">${steps}</ol><p class="pw-rail-foot">所有配置之后都能在项目设置里修改。</p></aside>`;
}
function projectWizardHeadHTML(w,title,lede){
  return `<div class="pw-kicker">第 ${projectWizardStepIndex(w.step)+1} 步，共 ${projectWizardSteps().length-1} 步</div><h3 class="pw-title" id="projectWizardTitle" tabindex="-1">${title}</h3><p class="pw-lede">${lede}</p>${w.error?`<p class="pw-note is-err" role="alert">${escapeHTML(w.error)}</p>`:''}`;
}
function projectWizardNamePill(name){const filled=!!String(name||'').trim();return pwPill(filled?'已填写':'必填',filled?'ok':'need',' data-pw-name-pill')}
function projectWizardBasicHTML(w){
  const d=w.draft,color=String(safeColor(d.color)).toLowerCase();
  const swatches=`<div class="pw-swatches" role="group" aria-label="品牌色">${PROJECT_SWATCHES.map(c=>`<button type="button" class="pw-sw${c===color?' is-on':''}" data-pw-color="${c}" style="background:${c};color:${c}" aria-label="品牌色 ${c}" aria-pressed="${c===color}"></button>`).join('')}</div>`;
  const identity=pwCard('项目身份','项目在工作台、需求和通知里的显示方式',projectWizardNamePill(d.name),
    pwField('项目名称',`<input class="pw-input" id="pwName" name="name" value="${escapeHTML(d.name||'')}" maxlength="80" placeholder="例如 Web Console" autocomplete="off">`,{for:'pwName',required:true})
    +pwField('品牌色',swatches)
    +pwField('项目说明',`<textarea class="pw-input" id="pwDescription" name="description" rows="3" placeholder="可选，这个项目负责什么？Agent 会把它作为上下文。">${escapeHTML(d.description||'')}</textarea>`,{for:'pwDescription'}));
  const owner=Number(state.currentUser?.id)?'':pwField('项目 Owner',userCombobox('ownerId',d.ownerId,'请选择项目 Owner'),{required:true,hint:'项目的默认责任人，创建后可在项目成员中调整。'});
  const defaults=pwCard('默认设置','新建需求时自动带上的流程',pwPill('已预选','ok'),
    `<div class="pw-row2">${pwField('默认 SOP',pwSelect(projectDefaultSOPField(d)),{required:true})}${pwField('主业务域',pwSelect(projectBusinessDomainField(d)))}</div>${owner}`);
  return projectWizardHeadHTML(w,'基本信息','只需填写项目名称即可创建，其余都有默认值。')+`<div class="pw-pane">${identity}${defaults}</div>`;
}
function projectWizardRepoHint(url){
  const raw=String(url||'').trim(),info=raw?parseGongfengRepoURL(raw):null;
  if(!raw)return '支持 https / ssh 地址，例如 https://github.com/owner/repo';
  return info?`识别为 <b>${escapeHTML(info.name)}</b> · ${escapeHTML(repoDisplayPath(info,info.project))}`:'无法识别为 GitHub 仓库地址';
}
function projectWizardReposHTML(w){
  const project=projectWizardProject()||{},repos=projectWizardRepos();
  const list=repos.map(r=>{const path=r.gongfengProjectId||'',name=r.name||path||r.id,shown=repoDisplayPath(parseGongfengRepoURL(r.webUrl||r.cloneUrl||path),path);return `<div class="pw-repo" data-pw-repo="${escapeHTML(r.id)}"><span class="pw-repo-ic">${pwIcon('repo',14,1.9)}</span><b title="${escapeHTML(name)}">${escapeHTML(name)}</b><code title="${escapeHTML(shown)}">${escapeHTML(shown)}</code><small>${escapeHTML(r.defaultBranch||'main')}</small>${r.integrateBranch?`<small title="集成分支">${escapeHTML(r.integrateBranch)}</small>`:''}${r.id===project.defaultRepositoryId?'<small class="is-def">默认</small>':''}<button type="button" class="pw-repo-rm" data-pw-repo-remove="${escapeHTML(r.id)}" title="移除" aria-label="移除仓库 ${escapeHTML(name)}">${pwIcon('x',14)}</button></div>`}).join('');
  const body=(list?`<div class="pw-repo-list">${list}</div>`:'')
    +'<div data-github-repositories></div>'
    +pwField('GitHub 仓库地址',`<input class="pw-input pw-mono" id="pwRepoUrl" name="repoUrl" value="${escapeHTML(w.repo.url)}" placeholder="https://github.com/owner/repo" spellcheck="false" autocomplete="off">`,{for:'pwRepoUrl',hint:projectWizardRepoHint(w.repo.url),hintAttrs:' data-pw-repo-hint'})
    +`<div class="pw-row2">${pwField('主分支',`<input class="pw-input pw-mono" id="pwRepoBranch" name="repoBranch" value="${escapeHTML(w.repo.branch)}" placeholder="main" spellcheck="false">`,{for:'pwRepoBranch'})}${pwField('集成分支',`<input class="pw-input pw-mono" id="pwRepoIntegrate" name="repoIntegrate" value="${escapeHTML(w.repo.integrate)}" placeholder="可选" spellcheck="false">`,{for:'pwRepoIntegrate'})}</div>`
    +`<div><button type="button" class="pw-btn pw-btn-outline pw-btn-sm" data-pw-action="add-repo"${w.saving?' disabled':''}>＋ 添加仓库</button></div>`;
  return projectWizardHeadHTML(w,'代码仓库','Agent 执行 SOP 时会克隆这些仓库并在工作分支上提交。第一个仓库会自动设为默认。')
    +`<div class="pw-pane"><p class="pw-note is-ok">✓ 项目「${escapeHTML(project.name||'')}」已创建。仓库添加后立即生效，也可以先跳过。</p>${pwCard('绑定 GitHub 仓库','粘贴仓库地址，自动识别名称',pwPill(repos.length?`已绑定 ${repos.length} 个`:'未绑定',repos.length?'ok':''),body)}</div>`;
}
function projectWizardPeoplePill(d){const count=projectPeopleIds(d).length;return pwPill(`${count} 位`,count>1?'ok':'',' data-pw-people-pill')}
function projectWizardMembersHTML(w){
  const d=w.draft,owner=userById(d.ownerId);
  const rows=(d.members||[]).map((m,i)=>`<div class="member-row pw-member" data-pw-member="${i}"><span class="member-avatar">${userAvatarHTML(userById(m.userId),48)}</span><span class="pw-member-kind">成员</span>${userCombobox(`member-${i}`,m.userId,'请选择项目成员')}<button type="button" class="pw-member-rm" data-pw-member-remove="${i}" title="移除成员" aria-label="移除成员">${pwIcon('x',14)}</button></div>`).join('');
  const body=`<div class="member-row pw-member is-owner"><span class="member-avatar owner">${userAvatarHTML(owner,48)}</span><span class="pw-member-kind is-owner">Owner</span>${userCombobox('ownerId',d.ownerId,'请选择项目 Owner')}<span></span></div>${rows}<div><button type="button" class="pw-btn pw-btn-outline pw-btn-sm" data-pw-action="add-member"${w.saving?' disabled':''}>＋ 添加成员</button></div>`;
  return projectWizardHeadHTML(w,'项目成员','Owner 是项目的默认责任人；成员无需设置角色，可查看和推进项目内所有需求。')
    +`<div class="pw-pane">${pwCard('负责人与成员','保存后成员即可在工作台切换到该项目',projectWizardPeoplePill(d),body)}</div>`;
}
function projectWizardTapdHTML(w){
  const d=w.draft,filled=!!String(d.tapdWorkspaceId||'').trim();
  const body=`<div class="pw-row2">${pwField('Workspace ID',`<input class="pw-input pw-mono" id="pwTapdWorkspace" name="tapdWorkspaceId" value="${escapeHTML(d.tapdWorkspaceId||'')}" placeholder="例如 20375341" spellcheck="false" autocomplete="off">`,{for:'pwTapdWorkspace'})}${pwField('Iteration ID',`<input class="pw-input pw-mono" id="pwTapdIteration" name="tapdIterationId" value="${escapeHTML(d.tapdIterationId||'')}" placeholder="留空导入全部" spellcheck="false" autocomplete="off">`,{for:'pwTapdIteration'})}</div>`;
  return projectWizardHeadHTML(w,'TAPD 集成','可选。配置后可在需求页从 TAPD 导入需求，澄清结论会回写到 TAPD 单。')
    +`<div class="pw-pane">${pwCard('TAPD 需求池','Workspace ID 是 TAPD 项目 URL 中 /tapd_fe/ 后的数字',pwPill(filled?'已填写':'未配置',filled?'ok':''),body)}</div>`;
}
function projectWizardSummary(project,repos){
  const p=project||{},def=repos.find(r=>r.id===p.defaultRepositoryId),others=(p.members||[]).filter(m=>Number(m.userId)&&Number(m.userId)!==Number(p.ownerId)).length;
  const sop=projectWorkflowName(p.workflowId)||p.workflowId||'';
  return [
    {key:'project',ok:true,label:'项目',value:`${escapeHTML(p.name||'')} · <code>${escapeHTML(p.id||'')}</code> · <code>${escapeHTML(p.key||'')}</code>`},
    {key:'sop',ok:!!sop,label:'默认 SOP',value:sop?escapeHTML(sop):'未配置'},
    {key:'repos',ok:repos.length>0,label:'代码仓库',value:repos.length?`${repos.length} 个，默认 ${escapeHTML(def?.name||def?.gongfengProjectId||'未设置')}`:'未配置，可稍后在项目设置补充'},
    {key:'members',ok:others>0,label:'项目成员',value:others?`${others+1} 位（含 Owner）`:'仅 Owner，可稍后邀请成员'},
    {key:'tapd',ok:!!p.tapdWorkspaceId,label:'TAPD',value:p.tapdWorkspaceId?`Workspace <code>${escapeHTML(p.tapdWorkspaceId)}</code>${p.tapdIterationId?` · Iteration <code>${escapeHTML(p.tapdIterationId)}</code>`:''}`:'未配置，可稍后在项目设置补充'},
  ].filter(i=>i.key!=='tapd'||tapdIntegrationAvailable(p));
}
function projectWizardDoneHTML(){
  const project=projectWizardProject()||{},items=projectWizardSummary(project,projectWizardRepos());
  const list=items.map(i=>`<li class="${i.ok?'is-ok':'is-warn'}" data-pw-summary="${i.key}">${pwIcon(i.ok?'check':'warn',13,i.ok?2.6:2.2)}<span><b>${i.label}</b> · ${i.value}</span></li>`).join('');
  return `<div class="pw-success-mark">${pwIcon('check',24,2.4)}</div><h3 class="pw-title" id="projectWizardTitle" tabindex="-1">项目「${escapeHTML(project.name||'')}」已创建</h3><p class="pw-lede">默认 SOP 已就绪，现在就可以开始第一条需求。跳过的配置随时可以在项目设置里补充。</p>
    <section class="pw-summary"><div class="pw-summary-title">已生效配置</div><ul class="pw-status-list">${list}</ul></section>
    <div class="pw-actions"><button type="button" class="pw-action is-primary" data-pw-action="start"><span class="pw-action-icon">${pwIcon('play',16)}</span><span><strong>开始第一条需求</strong><small>切换到该项目，去需求列表新建或导入需求</small></span>${pwIcon('next',14,2.2)}</button><button type="button" class="pw-action" data-pw-action="settings"><span class="pw-action-icon">${pwIcon('gear',16,1.9)}</span><span><strong>进入项目设置</strong><small>修改基本信息，补充仓库、成员和 TAPD</small></span>${pwIcon('next',14,2.2)}</button></div>`;
}
function projectWizardFooterHTML(w){
  const dis=w.saving?' disabled':'';
  if(w.step==='done')return `<footer class="pw-footer"><div class="pw-footer-spacer"></div><button type="button" class="pw-btn pw-btn-outline" data-pw-action="close">关闭</button></footer>`;
  const left=w.step==='basic'?`<button type="button" class="pw-btn pw-btn-link" data-pw-action="cancel"${dis}>取消</button>`:`<button type="button" class="pw-btn pw-btn-link" data-pw-action="skip"${dis}>跳过此步</button>`;
  const back=w.step==='members'||w.step==='tapd'?`<button type="button" class="pw-btn pw-btn-outline" data-pw-action="back"${dis}>${pwIcon('back',14,2.2)}上一步</button>`:'';
  const label=w.saving?'正在保存…':w.step==='basic'?'创建并继续':w.step==='tapd'?'完成':'下一步';
  return `<footer class="pw-footer">${left}<div class="pw-footer-spacer"></div><div class="pw-footer-nav">${back}<button type="button" class="pw-btn pw-btn-primary" data-pw-action="next"${dis}>${label}${w.saving?'':pwIcon('next',14,2.2)}</button></div></footer>`;
}
function projectWizardBodyHTML(w){
  if(w.step==='basic')return projectWizardBasicHTML(w);
  if(w.step==='repos')return projectWizardReposHTML(w);
  if(w.step==='members')return projectWizardMembersHTML(w);
  if(w.step==='tapd')return projectWizardTapdHTML(w);
  return projectWizardDoneHTML();
}
function projectWizardHTML(){
  const w=projectWizard;if(!w)return '';
  return `<div class="pw-backdrop" data-pw-backdrop><div class="pw-dialog" role="dialog" aria-modal="true" aria-labelledby="projectWizardLabel" data-pw-step="${w.step}"${w.saving?' aria-busy="true"':''}>${projectWizardRailHTML(w)}<div class="pw-main"><button type="button" class="pw-close" data-pw-action="close" aria-label="关闭"${w.saving?' disabled':''}>${pwIcon('x',16)}</button><div class="pw-body">${projectWizardBodyHTML(w)}</div>${projectWizardFooterHTML(w)}</div></div></div>`;
}
function projectWizardHost(){
  let host=$('#projectWizard');
  if(host)return host;
  host=document.createElement('div');host.id='projectWizard';host.className='pw-root';
  document.body.appendChild(host);
  bindProjectWizardHost(host);
  return host;
}
// The backdrop and dialog carry the entrance animation, so they are mounted once per open; later
// renders only swap the rail, body and footer inside them.
function renderProjectWizard(){
  const w=projectWizard;if(!w)return;
  const host=projectWizardHost(),dialog=host.querySelector('.pw-dialog'),body=dialog?.querySelector('.pw-body');
  if(!dialog||!body){
    host.innerHTML=projectWizardHTML();
  }else{
    const keepScroll=dialog.dataset.pwStep===w.step?body.scrollTop:0;
    dialog.dataset.pwStep=w.step;
    if(w.saving)dialog.setAttribute('aria-busy','true');else dialog.removeAttribute('aria-busy');
    const close=dialog.querySelector('.pw-close');if(close)close.disabled=!!w.saving;
    const rail=dialog.querySelector('.pw-rail');if(rail)rail.outerHTML=projectWizardRailHTML(w);
    body.innerHTML=projectWizardBodyHTML(w);
    body.scrollTop=keepScroll;
    const footer=dialog.querySelector('.pw-footer');if(footer)footer.outerHTML=projectWizardFooterHTML(w);
  }
  document.body?.classList?.add('pw-open');
  bindUserSearches(host);
  if(w.step==='repos'&&typeof mountGitHubRepoPicker==='function')mountGitHubRepoPicker(host.querySelector('[data-github-repositories]'),repository=>{
    w.repo.url=repository.html_url;w.repo.branch=repository.default_branch||'main';
    const url=host.querySelector('[name="repoUrl"]'),branch=host.querySelector('[name="repoBranch"]');
    url.value=w.repo.url;branch.value=w.repo.branch;syncProjectWizardField(url);
  });
  focusProjectWizard();
}
function focusProjectWizard(){
  const w=projectWizard,host=$('#projectWizard');if(!w||!host)return;
  const target=w.focus==='title'?host.querySelector('#projectWizardTitle'):w.focus?host.querySelector(`[name="${w.focus}"]`):null;
  w.focus='';
  if(!target)return;
  const el=target.type==='hidden'?target.closest('[data-user-combobox]')?.querySelector('[data-user-combobox-trigger]'):target;
  setTimeout(()=>el?.focus?.(),0);
}
function syncProjectWizardField(t){
  const w=projectWizard,d=w.draft,name=t.name;
  if(name==='repoUrl'||name==='repoBranch'||name==='repoIntegrate'){
    w.repo[{repoUrl:'url',repoBranch:'branch',repoIntegrate:'integrate'}[name]]=t.value;
    if(name==='repoUrl'){const hint=$('#projectWizard')?.querySelector('[data-pw-repo-hint]');if(hint)hint.innerHTML=projectWizardRepoHint(t.value)}
    return true;
  }
  if(name==='ownerId'||/^member-\d+$/.test(name)){
    if(name==='ownerId')d.ownerId=Number(t.value)||0;
    else{const m=d.members[Number(name.slice(7))];if(m)m.userId=Number(t.value)||0}
    const pill=$('#projectWizard')?.querySelector('[data-pw-people-pill]');if(pill)pill.outerHTML=projectWizardPeoplePill(d);
    return true;
  }
  if(!['name','description','workflowId','businessDomainId','tapdWorkspaceId','tapdIterationId'].includes(name))return false;
  d[name]=t.value;
  if(name==='name'){const pill=$('#projectWizard')?.querySelector('[data-pw-name-pill]');if(pill)pill.outerHTML=projectWizardNamePill(t.value)}
  return true;
}
function bindProjectWizardHost(host){
  const onField=e=>{
    if(!projectWizard||!e.target?.name||!syncProjectWizardField(e.target))return;
    if(projectWizard.error){projectWizard.error='';host.querySelector('.pw-note.is-err')?.remove()}
  };
  host.addEventListener('input',onField);
  host.addEventListener('change',onField);
  let downOnBackdrop=false;
  host.addEventListener('mousedown',e=>{downOnBackdrop=e.target?.matches?.('[data-pw-backdrop]')});
  host.addEventListener('click',e=>{
    const w=projectWizard,t=e.target instanceof Element?e.target:null;if(!w||!t)return;
    if(t.matches('[data-pw-backdrop]')){if(downOnBackdrop&&!w.saving)closeProjectWizard({force:w.step==='done'});downOnBackdrop=false;return}
    const sw=t.closest('[data-pw-color]');
    if(sw){w.draft.color=sw.dataset.pwColor;host.querySelectorAll('[data-pw-color]').forEach(b=>{const on=b===sw;b.classList.toggle('is-on',on);b.setAttribute('aria-pressed',String(on))});return}
    const rmRepo=t.closest('[data-pw-repo-remove]');if(rmRepo){projectWizardRemoveRepo(rmRepo.dataset.pwRepoRemove);return}
    const rmMember=t.closest('[data-pw-member-remove]');if(rmMember){projectWizardRemoveMember(Number(rmMember.dataset.pwMemberRemove));return}
    const action=t.closest('[data-pw-action]')?.dataset.pwAction;
    if(action==='next')projectWizardNext();
    else if(action==='skip')projectWizardSkip();
    else if(action==='back')projectWizardBack();
    else if(action==='cancel'||action==='close')closeProjectWizard({force:w.step==='done'});
    else if(action==='add-repo')projectWizardAddRepo();
    else if(action==='add-member')projectWizardAddMember();
    else if(action==='start')projectWizardStartIssues();
    else if(action==='settings')projectWizardOpenSettings();
  });
  host.addEventListener('keydown',e=>{
    if(e.key!=='Enter'||e.isComposing||!projectWizard)return;
    const name=e.target?.name;
    if(name==='repoUrl'||name==='repoBranch'||name==='repoIntegrate'){e.preventDefault();projectWizardAddRepo()}
    else if(name==='name'||name==='tapdWorkspaceId'||name==='tapdIterationId'){e.preventDefault();projectWizardNext()}
  });
}
// Back/forward or a typed hash moves away from where the wizard was opened; the overlay must not
// linger over another view. Saved steps are already on the server.
function projectWizardOnHashChange(){
  const w=projectWizard;if(!w||location.hash===w.hash)return;
  if(parseProjectAdminRoute(location.hash)?.id==='new'){w.hash=location.hash;return}
  w.saving=false;
  closeProjectWizard({force:true,noFocus:true});
}
window.addEventListener('hashchange',projectWizardOnHashChange);
function projectWizardFocusables(host){
  return [...host.querySelectorAll('button:not([disabled]),input:not([type="hidden"]):not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex="0"]')].filter(el=>el.offsetParent!==null&&!el.closest('[hidden]'));
}
// Capture phase: an open user picker keeps Escape, and the global handler in app.js (which drops
// .open from the topmost modal without settling afConfirm) must not see the keys handled here.
document.addEventListener('keydown',e=>{
  const w=projectWizard,host=$('#projectWizard');if(!w||!host)return;
  if($('#confirmModal')?.classList.contains('open')){
    if(e.key==='Escape'&&typeof confirmPromiseResolve==='function'){e.preventDefault();e.stopPropagation();confirmPromiseResolve(false)}
    return;
  }
  if(e.key==='Escape'){
    if(host.querySelector('[data-user-combobox].is-open'))return;
    e.preventDefault();e.stopPropagation();
    if(!w.saving)closeProjectWizard({force:w.step==='done'});
    return;
  }
  if(e.key!=='Tab')return;
  const items=projectWizardFocusables(host);if(!items.length)return;
  const first=items[0],last=items[items.length-1],inside=host.contains(document.activeElement);
  if(e.shiftKey&&(document.activeElement===first||!inside)){e.preventDefault();last.focus()}
  else if(!e.shiftKey&&(document.activeElement===last||!inside)){e.preventDefault();first.focus()}
},true);
