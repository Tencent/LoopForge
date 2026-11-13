// Repository add/edit drawer for the project detail page. Saves write straight
// to /api/repositories; #repositoryModal stays for its other callers.
const repoDrawerState={projectId:'',repoId:'',nameTouched:false,saving:false,returnFocus:null};
function repoDrawerOpen(){return !!$('#repoDrawer')?.classList.contains('open')}
function setRepoDrawerSwitch(on){const sw=$('#repoDrawerDefault');if(!sw)return;sw.classList.toggle('on',!!on);sw.setAttribute('aria-checked',String(!!on))}
function repoDrawerError(show){
  const f=$('#repoDrawerForm');if(!f)return;
  f.elements.repoUrl.classList.toggle('bad',!!show);
  $('#repoDrawerUrlError').hidden=!show;
}
function syncRepoDrawerDetect(){
  const f=$('#repoDrawerForm'),info=repoDrawerURLInfo(f),chosen=$('#repoDrawerChosen');
  if(info){
    repoDrawerError(false);
    if(!repoDrawerState.nameTouched)f.elements.name.value=info.name;
    const path=chosen.querySelector('code');
    path.textContent=info.project||info.name;
    path.title=path.textContent;
  }
  chosen.hidden=!info;
  $('#repoDrawerBranches').hidden=!info;
  $('#repoDrawerDefaultRow').hidden=!info;
  $('#repoDrawerAdvanced').hidden=!info||info.provider==='gitlab';
  $('#repoDrawerNameField').hidden=!info||!repoDrawerState.nameTouched;
  $('#repoDrawerSSH').hidden=info?.provider!=='ssh';
  renderGitSSHMissingHint($('#repoDrawerSSHMissing'),info);
  if(!repoDrawerState.saving)$('#repoDrawerSave').disabled=!info&&!(repoDrawerState.source==='manual'&&f.elements.repoUrl.value.trim());
  const source=repoDrawerState.source;
  $$('[data-repo-panel="'+source+'"] [data-repo-url]').forEach(row=>{
    row.setAttribute('aria-pressed',String(row.dataset.repoUrl===f.elements.repoUrl.value));
  });
}
function repoDrawerURLInfo(form){
  const selected=repoDrawerState.gitLabRepository;
  return selected?.webUrl===form.elements.repoUrl.value.trim()?selected:parseGongfengRepoURL(form.elements.repoUrl.value);
}

const repoDrawerDraftFields=['repoUrl','name','webUrl','defaultBranch','integrateBranch','localPath'];
function showRepoDrawerSource(source){
  const drawer=$('#repoDrawer'),form=$('#repoDrawerForm'),previous=repoDrawerState.source;
  if(previous===source)return;
  if(previous){
    repoDrawerState.drafts[previous]={
      values:repoDrawerDraftFields.map(name=>form.elements[name].value),
      nameTouched:repoDrawerState.nameTouched,gitLabRepository:repoDrawerState.gitLabRepository,
    };
  }
  const draft=repoDrawerState.drafts[source];
  repoDrawerState.source=source;
  repoDrawerState.nameTouched=!!draft?.nameTouched;
  repoDrawerState.gitLabRepository=draft?.gitLabRepository||null;
  repoDrawerDraftFields.forEach((name,i)=>{form.elements[name].value=draft?.values[i]??(name==='defaultBranch'?'main':'')});
  drawer.querySelectorAll('[data-repo-source]').forEach(tab=>{
    const active=tab.dataset.repoSource===source;
    tab.setAttribute('aria-selected',String(active));tab.tabIndex=active?0:-1;
  });
  drawer.querySelectorAll('[data-repo-panel]').forEach(panel=>{panel.hidden=panel.dataset.repoPanel!==source});
  const panel=drawer.querySelector('[data-repo-panel="'+source+'"]');
  if(source!=='manual'&&!panel.childElementCount)mountRepoDrawerPicker(panel,source);
  repoDrawerError(false);
  syncRepoDrawerDetect();
}

function mountRepoDrawerPicker(panel,source){
  const user=userById(state.currentUser?.id),gitLab=source==='gitlab';
  if(!(gitLab?user?.gitLabTokenConfigured:user?.gitTokenConfigured)){
    if(gitLab){
      panel.innerHTML='<div class="pa-repo-connect"><p>配置 GitLab 实例地址和个人访问令牌后，即可搜索有权限的仓库。</p><button type="button" class="button ghost small">配置 GitLab</button></div>';
      panel.querySelector('button').onclick=()=>{closeRepoDrawer({restoreFocus:false});gotoCredential('gitLabToken')};
    }else{
      panel.innerHTML=githubConnectHTML()+'<button type="button" class="button ghost small pa-repo-token">配置访问令牌</button>';
      panel.querySelector('.pa-repo-token').onclick=()=>{closeRepoDrawer({restoreFocus:false});gotoCredential('gitToken')};
      panel.addEventListener('github-connected',()=>mountRepoDrawerPicker(panel,source),{once:true});
      hydrateGitHubConnections(panel);
    }
    return;
  }

  panel.innerHTML=`<div class="pa-repo-instance" ${gitLab?'':'hidden'}><span></span><code></code></div>
    <label class="pa-repo-search">${repoDrawerIcon('search')}<input class="pa-input" type="search" aria-label="搜索仓库" placeholder="${gitLab?'搜索仓库名或分组':'搜索已加载的仓库'}"></label>
    <div class="pa-repo-results"><div data-repo-rows role="group" aria-label="仓库搜索结果"></div>
      <p class="pa-repo-status" role="status"></p>
      <button type="button" class="pa-repo-more" hidden>加载更多</button>
      <button type="button" class="pa-repo-retry" hidden>重试</button>
    </div>`;
  const form=$('#repoDrawerForm'),input=panel.querySelector('input'),rows=panel.querySelector('[data-repo-rows]');
  const status=panel.querySelector('[role="status"]'),more=panel.querySelector('.pa-repo-more'),retry=panel.querySelector('.pa-repo-retry');
  const actor=state.currentUser?.id,projectId=repoDrawerState.projectId;
  const bound=(state.data.repositories||[]).filter(repo=>repo.projectId===projectId&&repo.provider===source);
  let repositories=[],instanceURL='',page=1,hasMore=false,request=0,timer,failedPage=1;
  const showInstance=base=>{
    if(!gitLab)return;
    const instance=panel.querySelector('.pa-repo-instance');
    instance.hidden=!base;
    instance.querySelector('code').textContent=base?.replace(/^https:\/\//,'')||'';
  };
  const paint=()=>{
    const query=input.value.trim().toLowerCase();
    const visible=gitLab?repositories:repositories.filter(repo=>repo.project.toLowerCase().includes(query));
    const selected=repoDrawerState.source===source?form.elements.repoUrl.value:repoDrawerState.drafts[source]?.values[0];
    rows.innerHTML=visible.map((repo,index)=>`<button type="button" class="pa-repo-result" data-repo-row="${index}" data-repo-url="${escapeHTML(repo.webUrl)}" aria-pressed="${repo.webUrl===selected}">
      <span class="pa-repo-result-icon">${repoDrawerIcon('folder')}</span>
      <span class="pa-repo-result-text"><b>${escapeHTML(repo.name)}</b><code>${escapeHTML(repo.project)}</code></span>
      <code class="pa-repo-result-branch">${escapeHTML(repo.branch)}</code>
      ${repoDrawerIcon('check')}
    </button>`).join('');
    status.textContent=visible.length?'':query?'没有匹配的仓库':'没有可添加的仓库';
    status.hidden=!status.textContent;
    more.hidden=!hasMore;
    window.AgentFlowHarnessIcons?.hydrate(panel);
    rows.onclick=event=>{
      const row=event.target.closest('[data-repo-row]');
      if(!row||repoDrawerState.source!==source||repoDrawerState.saving)return;
      const repo=visible[Number(row.dataset.repoRow)];
      if(form.elements.repoUrl.value!==repo.webUrl){
        form.elements.repoUrl.value=repo.webUrl;
        form.elements.defaultBranch.value=repo.branch;
        repoDrawerState.nameTouched=false;
        repoDrawerState.gitLabRepository=gitLab?repo:null;
      }
      syncRepoDrawerDetect();
    };
  };
  const load=async requested=>{
    const seq=++request,q=input.value.trim();
    if(requested===1){repositories=[];rows.replaceChildren()}
    const isCurrent=()=>seq===request&&rows.isConnected&&state.currentUser?.id===actor;
    rows.inert=true;
    more.hidden=retry.hidden=true;
    status.hidden=false;status.textContent='正在加载仓库…';
    try{
      const endpoint=gitLab?'/api/gitlab/repositories?'+new URLSearchParams({q,page:requested}):'/api/auth/github/repositories?page='+requested;
      const result=await api(endpoint,{silent:true});
      if(!isCurrent())return;
      if(gitLab&&requested>1&&instanceURL!==result.gitLabUrl){void load(1);return}
      instanceURL=result.gitLabUrl;
      showInstance(instanceURL);
      for(const repo of result.repositories){
        const info=gitLab?gitLabRepositoryInfo({...repo,gitLabUrl:result.gitLabUrl}):{name:repo.name||repo.full_name.split('/').at(-1),project:repo.full_name,webUrl:repo.html_url};
        const alreadyBound=bound.some(item=>{
          if(item.id===repoDrawerState.repoId)return false;
          if(!gitLab)return item.gongfengProjectId?.toLowerCase()==='github:'+repo.full_name.toLowerCase()||item.webUrl===info.webUrl;
          return item.gitLabProjectId===repo.id&&(item.gitLabUrl?item.gitLabUrl===instanceURL:item.cloneUrl?.startsWith(instanceURL+'/'));
        });
        if(!alreadyBound&&!repositories.some(item=>item.webUrl===info.webUrl))repositories.push({...info,branch:repo.default_branch||'main'});
      }
      page=requested;hasMore=result.hasMore;
      paint();
    }catch(error){
      if(!isCurrent())return;
      failedPage=requested;
      status.textContent=error.message;
      retry.hidden=false;
    }finally{
      if(isCurrent())rows.inert=false;
    }
  };
  input.onkeydown=event=>{if(event.key==='Enter')event.preventDefault()};
  input.oninput=()=>{
    if(!gitLab){if(!rows.inert&&retry.hidden)paint();return}
    ++request;
    clearTimeout(timer);
    rows.replaceChildren();rows.inert=true;more.hidden=retry.hidden=true;
    status.hidden=false;status.textContent='正在搜索仓库…';
    timer=setTimeout(()=>{if(rows.isConnected)void load(1)},250);
  };
  more.onclick=()=>load(page+1);
  retry.onclick=()=>load(failedPage);
  showInstance(state.data.gitLabURL);
  void load(1);
}

function repoDrawerIcon(name){
  return `<svg class="agent-icon harness-icon" data-icon="${name}" width="16" height="16" aria-hidden="true"></svg>`;
}

function openRepoDrawer(repoId=''){
  const projectId=state.projectAdminDetailId;
  const project=(state.data.projects||[]).find(p=>p.id===projectId);
  if(!project||projectId==='new')return toast('保存项目后可用','warn');
  const repo=repoId?(state.data.repositories||[]).find(r=>r.id===repoId&&r.projectId===projectId):null;
  if(repoId&&!repo)return toast('仓库不存在或已被删除','err');
  const f=$('#repoDrawerForm');if(!f)return;
  const hasDefault=!projectNeedsDefaultRepository(project);
  const repoAddress=repo?.provider==='ssh'?repo.cloneUrl:repo?.webUrl||repo?.cloneUrl||repo?.gongfengProjectId||'';
  const gitLabRepository=repo?.provider==='gitlab'?gitLabRepositoryInfo(repo):null;
  const parsedName=gitLabRepository?.name||parseGongfengRepoURL(repoAddress)?.name||'';
  const user=userById(state.currentUser?.id),source=repo
    ?(['gitlab','github'].includes(repo.provider)?repo.provider:'manual')
    :user?.gitLabTokenConfigured?'gitlab':user?.gitTokenConfigured?'github':'manual';
  Object.assign(repoDrawerState,{projectId,repoId:repo?.id||'',source:null,drafts:{},saving:false,returnFocus:document.activeElement});
  repoDrawerState.drafts[source]={
    values:[repoAddress,repo?.name||'',repo?.provider==='ssh'?repo.webUrl||'':'',repo?.defaultBranch||'main',repo?.integrateBranch||'',repo?.localPath||''],
    nameTouched:!!repo?.name&&repo.name!==parsedName,gitLabRepository,
  };
  f.elements.id.value=repo?.id||'';
  $('#repoDrawerAdvanced').open=!!repo?.localPath;
  $('#repoDrawerTitle').textContent=repo?'编辑代码仓库':'添加代码仓库';
  setRepoDrawerSwitch(repo?project.defaultRepositoryId===repo.id:!hasDefault);
  gitSSHKeyCache=null;
  const drawer=$('#repoDrawer');
  drawer.querySelectorAll('[data-repo-panel]:not([data-repo-panel="manual"])').forEach(panel=>{
    // Detach the entire picker so old requests cannot repaint the next opening.
    panel.replaceWith(panel.cloneNode(false));
  });
  drawer.inert=false;drawer.setAttribute('aria-hidden','false');
  drawer.classList.add('open');$('#repoDrawerScrim').classList.add('open');
  showRepoDrawerSource(source);
  drawer.querySelector('[data-repo-source="'+source+'"]').focus({preventScroll:true});
}
function closeRepoDrawer(opt){
  const drawer=$('#repoDrawer');if(!drawer||!drawer.classList.contains('open'))return;
  drawer.classList.remove('open');$('#repoDrawerScrim')?.classList.remove('open');
  drawer.setAttribute('aria-hidden','true');drawer.inert=true;
  const back=repoDrawerState.returnFocus;repoDrawerState.returnFocus=null;
  if(opt?.restoreFocus!==false&&back&&document.contains(back))back.focus?.();
}
function repoDrawerPayload(f,info){
  const existing=(state.data.repositories||[]).find(r=>r.id===repoDrawerState.repoId)||{};
  const payload={...existing,id:existing.id||undefined,projectId:repoDrawerState.projectId,provider:info.provider||'gongfeng',name:f.elements.name.value.trim()||info.name,gongfengProjectId:info.project,webUrl:info.webUrl,cloneUrl:info.cloneUrl,defaultBranch:f.elements.defaultBranch.value.trim()||'main',integrateBranch:f.elements.integrateBranch.value.trim(),localPath:f.elements.localPath.value.trim(),repoUrl:f.elements.repoUrl.value.trim()};
  if(info.provider==='gitlab'){payload.gitLabProjectId=info.gitLabProjectId;payload.gitLabUrl=info.gitLabUrl;payload.gongfengProjectId='';payload.localPath=''}
  if(info.provider==='ssh'){payload.gongfengProjectId='';payload.webUrl=f.elements.webUrl.value.trim()}
  delete payload.gitToken;if(!payload.id)delete payload.id;
  return payload;
}
// A project with no valid default repository adopts the next repository saved into it.
function projectNeedsDefaultRepository(project){
  return !(state.data.repositories||[]).some(r=>r.projectId===project?.id&&r.id===project?.defaultRepositoryId);
}
async function saveProjectDefaultRepository(project,repoId){
  const saved=await api('/api/projects',{method:'PUT',body:JSON.stringify({...project,defaultRepositoryId:repoId})});
  const projects=state.data.projects||[],pi=projects.findIndex(p=>p.id===project.id),next=saved&&saved.id?saved:{...project,defaultRepositoryId:repoId};
  if(pi>=0)projects[pi]=next;
  syncProjectDraftDefaultRepo(project.id,repoId);
  return next;
}
async function saveRepoDrawer(e){
  e.preventDefault();
  if(repoDrawerState.saving)return;
  const f=e.currentTarget,info=repoDrawerURLInfo(f);
  if(!info){repoDrawerError(true);f.elements.repoUrl.focus();return}
  const projectId=repoDrawerState.projectId,project=(state.data.projects||[]).find(p=>p.id===projectId);
  if(!project)return toast('请先保存项目','warn');
  const makeDefault=$('#repoDrawerDefault').classList.contains('on'),payload=repoDrawerPayload(f,info),editing=!!payload.id;
  repoDrawerState.saving=true;$('#repoDrawerSave').disabled=true;
  try{
    captureProjectAdminForm();
    const repo=await api('/api/repositories',{method:editing?'PUT':'POST',body:JSON.stringify(payload)});
    const list=state.data.repositories||(state.data.repositories=[]),ri=list.findIndex(r=>r.id===repo.id);
    if(ri>=0)list[ri]=repo;else list.push(repo);
    const wasDefault=project.defaultRepositoryId===repo.id,nextDefault=makeDefault?repo.id:(wasDefault?'':project.defaultRepositoryId||'');
    let defaultError='';
    if(nextDefault!==(project.defaultRepositoryId||'')){
      try{await saveProjectDefaultRepository(project,nextDefault)}
      catch(err){defaultError=`仓库已保存，${makeDefault?'设为默认':'取消默认'}失败：${err.message}`}
    }
    closeRepoDrawer({restoreFocus:false});
    renderAll();
    refreshProjectAdminRepos(repo.id);
    toast(defaultError||(editing?'仓库已保存':`仓库已添加${makeDefault&&!wasDefault?'，并设为默认':''}`),defaultError?'err':'ok');
  }catch(err){toast(err.message,'err')}
  finally{repoDrawerState.saving=false;const btn=$('#repoDrawerSave');if(btn)btn.disabled=false}
}
function bindRepoDrawer(){
  const f=$('#repoDrawerForm');if(!f||f.dataset.bound)return;f.dataset.bound='true';
  f.onsubmit=saveRepoDrawer;
  const tabs=[...f.querySelectorAll('[data-repo-source]')];
  tabs.forEach((tab,index)=>{
    tab.onclick=()=>{if(!repoDrawerState.saving)showRepoDrawerSource(tab.dataset.repoSource)};
    tab.onkeydown=event=>{
      const next={ArrowLeft:(index+tabs.length-1)%tabs.length,ArrowRight:(index+1)%tabs.length,Home:0,End:tabs.length-1}[event.key];
      if(next===undefined||repoDrawerState.saving)return;
      event.preventDefault();
      showRepoDrawerSource(tabs[next].dataset.repoSource);
      tabs[next].focus();
    };
  });
  f.elements.repoUrl.addEventListener('input',syncRepoDrawerDetect);
  f.elements.name.addEventListener('input',()=>{repoDrawerState.nameTouched=true;syncRepoDrawerDetect()});
  $('#repoDrawerRename').onclick=()=>{repoDrawerState.nameTouched=true;$('#repoDrawerNameField').hidden=false;f.elements.name.focus();f.elements.name.select()};
  $('#repoDrawerDefault').onclick=()=>setRepoDrawerSwitch(!$('#repoDrawerDefault').classList.contains('on'));
  $$('[data-repo-drawer-close]').forEach(b=>b.onclick=closeRepoDrawer);
  $('#repoDrawerScrim').onclick=closeRepoDrawer;
  document.addEventListener('keydown',e=>{
    if(e.key!=='Escape'||!repoDrawerOpen()||$('#gitSSHKeyDialog')?.open||$('#confirmModal')?.classList.contains('open'))return;
    e.preventDefault();e.stopImmediatePropagation();closeRepoDrawer();
  },true);
}
