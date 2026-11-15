// Repository add/edit drawer for the project detail page. Saves write straight
// to /api/repositories; #repositoryModal stays for its other callers.
const repoDrawerState={projectId:'',repoId:'',nameTouched:false,saving:false,returnFocus:null};
function repoDrawerOpen(){return !!$('#repoDrawer')?.classList.contains('open')}
function setRepoDrawerSwitch(on){const sw=$('#repoDrawerDefault');if(!sw)return;sw.classList.toggle('on',!!on);sw.setAttribute('aria-checked',String(!!on))}
function repoDrawerError(show){
  const f=$('#repoDrawerForm');if(!f)return;
  f.elements.repoUrl.classList.toggle('bad',!!show);
  const error=$('#repoDrawerUrlError');
  if(show)error.textContent=f.elements.provider?.value==='forgejo'
    ?'请输入同一个 Forgejo 仓库的 SSH 克隆地址和 HTTPS 网页地址。'
    :'请输入有效的 GitHub 链接或 Git SSH 克隆地址。CNB 只支持 HTTPS + 访问令牌。';
  error.hidden=!show;
}
function repoDrawerInfo(form){
  const {provider,repoUrl,webUrl}=form.elements;
  if(provider?.value==='forgejo')return globalThis.LoopForgeForgejoRepository(repoUrl.value,webUrl.value);
  return parseGongfengRepoURL(repoUrl.value);
}
function syncRepoDrawerDetect(){
  const f=$('#repoDrawerForm');if(!f)return;
  const forgejo=f.elements.provider?.value==='forgejo';
  const info=repoDrawerInfo(f),box=$('#repoDrawerDetect');
  f.querySelector('[data-github-repositories]').hidden=forgejo;
  f.querySelector('[data-repo-address-label]').textContent=forgejo?'SSH 克隆地址':'GitHub 链接或 Git SSH 地址';
  f.querySelector('.pa-fmt').hidden=forgejo;
  f.elements.repoUrl.placeholder=forgejo?'git@forge.example.org:team/repo.git':'https://github.com/owner/repo';
  $('#repoDrawerSSH').hidden=!forgejo&&info?.provider!=='ssh';
  $('#repoDrawerForgejoHint').hidden=!forgejo;
  $('#repoDrawerSSH [data-optional-web-url]').hidden=forgejo;
  f.elements.webUrl.placeholder=forgejo?'https://forge.example.org/team/repo':'https://gitlab.com/group/repo';
  renderGitSSHMissingHint($('#repoDrawerSSHMissing'),forgejo?globalThis.LoopForgeGitSSH(f.elements.repoUrl.value):info);
  if(!info){box.hidden=true;return}
  repoDrawerError(false);
  if(!repoDrawerState.nameTouched)f.elements.name.value=info.name;
  $('#repoDrawerDetectName').textContent=f.elements.name.value.trim()||info.name;
  $('#repoDrawerDetectPath').textContent=forgejo?repoDisplayPath(info):info.project;
  box.hidden=false;
}
function openRepoDrawer(repoId=''){
  const projectId=state.projectAdminDetailId;
  const project=(state.data.projects||[]).find(p=>p.id===projectId);
  if(!project||projectId==='new')return toast('保存项目后可用','warn');
  const repo=repoId?(state.data.repositories||[]).find(r=>r.id===repoId&&r.projectId===projectId):null;
  if(repoId&&!repo)return toast('仓库不存在或已被删除','err');
  const f=$('#repoDrawerForm');if(!f)return;
  const hasDefault=!projectNeedsDefaultRepository(project);
  const forgejo=repo?.provider==='forgejo',usesSSH=forgejo||repo?.provider==='ssh';
  const repoAddress=usesSSH?repo.cloneUrl:repo?.webUrl||repo?.cloneUrl||repo?.gongfengProjectId||'';
  const parsedName=(forgejo?globalThis.LoopForgeForgejoRepository(repoAddress,repo.webUrl):parseGongfengRepoURL(repoAddress))?.name||'';
  Object.assign(repoDrawerState,{projectId,repoId:repo?.id||'',nameTouched:!!repo&&!!repo.name&&repo.name!==parsedName,saving:false,returnFocus:document.activeElement});
  f.elements.id.value=repo?.id||'';
  f.elements.provider.value=forgejo?'forgejo':'';
  f.elements.repoUrl.value=repoAddress;
  f.elements.webUrl.value=usesSSH?repo.webUrl||'':'';
  f.elements.name.value=repo?.name||'';
  f.elements.defaultBranch.value=repo?.defaultBranch||'main';
  f.elements.integrateBranch.value=repo?.integrateBranch||'';
  f.elements.localPath.value=repo?.localPath||'';
  $('#repoDrawerAdvanced').open=!!repo?.localPath;
  $('#repoDrawerNameField').hidden=!repoDrawerState.nameTouched;
  $('#repoDrawerTitle').textContent=repo?'编辑代码仓库':'添加代码仓库';
  $('#repoDrawerSave').disabled=false;
  setRepoDrawerSwitch(repo?project.defaultRepositoryId===repo.id:!hasDefault);
  gitSSHKeyCache=null;
  repoDrawerError(false);syncRepoDrawerDetect();
  const drawer=$('#repoDrawer');
  drawer.inert=false;drawer.setAttribute('aria-hidden','false');
  drawer.classList.add('open');$('#repoDrawerScrim').classList.add('open');
  if(typeof mountGitHubRepoPicker==='function')mountGitHubRepoPicker(drawer.querySelector('[data-github-repositories]'),repository=>{
    f.elements.provider.value='';
    f.elements.repoUrl.value=repository.html_url;
    f.elements.defaultBranch.value=repository.default_branch||'main';
    syncRepoDrawerDetect();
  });
  setTimeout(()=>f.elements.repoUrl.focus(),120);
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
  const f=e.currentTarget,info=repoDrawerInfo(f);
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
  f.elements.provider.addEventListener('change',syncRepoDrawerDetect);
  f.elements.webUrl.addEventListener('input',syncRepoDrawerDetect);
  f.elements.repoUrl.addEventListener('input',syncRepoDrawerDetect);
  f.elements.name.addEventListener('input',()=>{repoDrawerState.nameTouched=!!f.elements.name.value.trim();syncRepoDrawerDetect()});
  $('#repoDrawerRename').onclick=()=>{repoDrawerState.nameTouched=true;$('#repoDrawerNameField').hidden=false;f.elements.name.focus();f.elements.name.select()};
  $('#repoDrawerDefault').onclick=()=>setRepoDrawerSwitch(!$('#repoDrawerDefault').classList.contains('on'));
  $$('[data-repo-drawer-close]').forEach(b=>b.onclick=closeRepoDrawer);
  $('#repoDrawerScrim').onclick=closeRepoDrawer;
  document.addEventListener('keydown',e=>{
    if(e.key!=='Escape'||!repoDrawerOpen()||$('#gitSSHKeyDialog')?.open||$('#confirmModal')?.classList.contains('open'))return;
    e.preventDefault();e.stopImmediatePropagation();closeRepoDrawer();
  },true);
}
