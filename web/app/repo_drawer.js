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
  const f=$('#repoDrawerForm');if(!f)return;
  const info=parseGongfengRepoURL(f.elements.repoUrl.value),box=$('#repoDrawerDetect');
  $('#repoDrawerSSH').hidden=info?.provider!=='ssh';
  renderGitSSHMissingHint($('#repoDrawerSSHMissing'),info);
  if(!info){box.hidden=true;return}
  repoDrawerError(false);
  if(!repoDrawerState.nameTouched)f.elements.name.value=info.name;
  $('#repoDrawerDetectName').textContent=f.elements.name.value.trim()||info.name;
  $('#repoDrawerDetectPath').textContent=info.project;
  box.hidden=false;
}
function fillRepoBranchNaming(form,naming){
  const fields=form.elements;
  const prefix=naming?.mode==='prefix'?(naming.prefix??''):'feature/';
  const preset=Array.from(fields.workBranchPrefix.options).some(option=>option.value===prefix&&option.value!=='custom');
  fields.workBranchNamingMode.value=naming?.mode==='ai'?'ai':'prefix';
  fields.workBranchPrefix.value=preset?prefix:'custom';
  fields.workBranchCustomPrefix.value=preset?'':prefix;
  fields.workBranchPrompt.value=naming?.prompt||'';
  const executors=(state.data.executors||[]).filter(executor=>executor.kind);
  fields.workBranchExecutorId.replaceChildren(
    new Option(lfT('选择 CLI','Choose a CLI'),''),
    ...executors.map(executor=>repoBranchOption(executor.id,executor.name||executor.id,window.AgentFlowAssistantModels.cliIcon(executor.kind))),
  );
  fields.workBranchExecutorId.value=naming?.executorId||'';
  fillRepoBranchModels(form,naming?.model||'');
  syncRepoBranchNaming(form);
}
function repoBranchOption(id,name,icon){
  const option=new Option(name,id);
  if(icon)option.dataset.brand=icon.split('/').at(-1);
  return option;
}
function fillRepoBranchModels(form,model='',custom=false){
  const fields=form.elements,executor=fields.workBranchExecutorId.value,choice=fields.workBranchModelChoice;
  const catalog=window.AgentFlowAssistantModels;
  const items=(executorModelCatalogs()[executor]?.models||[]).filter(item=>catalog.concreteModelId(item.id));
  const listed=items.some(item=>item.id===model);
  choice.replaceChildren(
    new Option(lfT('选择模型','Choose a model'),''),
    ...items.map(item=>repoBranchOption(item.id,item.name||item.id,catalog.modelIcon(item))),
  );
  if(executor)choice.add(new Option(lfT('手动输入模型 ID…','Enter a model ID…'),AGENT_CUSTOM_MODEL));
  const manual=!!executor&&(custom||!!model&&!listed);
  choice.disabled=!executor;
  choice.value=manual?AGENT_CUSTOM_MODEL:listed?model:'';
  fields.workBranchCustomModel.value=manual?model:'';
  form.querySelector('[data-work-branch-custom-model]').hidden=!manual;
}
function repoBranchModel(form){
  const fields=form.elements,choice=fields.workBranchModelChoice.value;
  return choice===AGENT_CUSTOM_MODEL?fields.workBranchCustomModel.value.trim():choice;
}
function syncRepoBranchNaming(form){
  const isAI=form.elements.workBranchNamingMode.value==='ai';
  form.querySelector('[data-work-branch-prefix]').hidden=isAI;
  form.querySelector('[data-work-branch-prompt]').hidden=!isAI;
  form.querySelector('[data-work-branch-custom-prefix]').hidden=form.elements.workBranchPrefix.value!=='custom';
  if(isAI){
    const user=state.currentUser?.id;
    loadExecutorModelCatalogs().then(()=>{
      if(repoDrawerOpen()&&state.currentUser?.id===user&&form.elements.workBranchNamingMode.value==='ai'){
        const manual=form.elements.workBranchModelChoice.value===AGENT_CUSTOM_MODEL;
        const model=repoBranchModel(form);
        fillRepoBranchModels(form,model,manual&&!model);
      }
    });
  }
}
function repoBranchNamingPayload(form){
  const fields=form.elements;
  if(fields.workBranchNamingMode.value==='ai'){
    return {mode:'ai',executorId:fields.workBranchExecutorId.value,model:repoBranchModel(form),prompt:fields.workBranchPrompt.value.trim()};
  }
  const prefix=fields.workBranchPrefix.value;
  return {mode:'prefix',prefix:prefix==='custom'?fields.workBranchCustomPrefix.value.trim():prefix};
}
function openRepoDrawer(repoId=''){
  const projectId=state.projectAdminDetailId;
  const project=(state.data.projects||[]).find(p=>p.id===projectId);
  if(!project||projectId==='new')return toast('保存项目后可用','warn');
  const repo=repoId?(state.data.repositories||[]).find(r=>r.id===repoId&&r.projectId===projectId):null;
  if(repoId&&!repo)return toast('仓库不存在或已被删除','err');
  const f=$('#repoDrawerForm');if(!f)return;
  const hasDefault=!projectNeedsDefaultRepository(project);
  const repoAddress=globalThis.LoopForgeGitRepositoryReference(repo);
  const parsedName=parseGongfengRepoURL(repoAddress)?.name||'';
  Object.assign(repoDrawerState,{projectId,repoId:repo?.id||'',nameTouched:!!repo&&!!repo.name&&repo.name!==parsedName,saving:false,returnFocus:document.activeElement});
  f.elements.id.value=repo?.id||'';
  f.elements.repoUrl.value=repoAddress;
  f.elements.webUrl.value=repo?.provider==='ssh'?repo.webUrl||'':'';
  f.elements.name.value=repo?.name||'';
  f.elements.defaultBranch.value=repo?.defaultBranch||'main';
  f.elements.integrateBranch.value=repo?.integrateBranch||'';
  fillRepoBranchNaming(f,repo?.workBranchNaming);
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
  payload.workBranchNaming=repoBranchNamingPayload(f);
  if(['ssh','http','https'].includes(info.provider)){payload.gongfengProjectId='';payload.webUrl=f.elements.webUrl.value.trim()}
  delete payload.gitToken;if(!payload.id)delete payload.id;
  return payload;
}
// A project with no valid default repository adopts the next repository saved into it.
function projectNeedsDefaultRepository(project){
  return !(state.data.repositories||[]).some(r=>r.projectId===project?.id&&r.id===project?.defaultRepositoryId);
}
async function saveProjectDefaultRepository(project,repoId){
  const next=await persistProject({...project,defaultRepositoryId:repoId});
  if(next)syncProjectDraftDefaultRepo(project.id,next.defaultRepositoryId||'');
  return next;
}
async function saveRepoDrawer(e){
  e.preventDefault();
  if(repoDrawerState.saving)return;
  const f=e.currentTarget,info=parseGongfengRepoURL(f.elements.repoUrl.value);
  if(!info){repoDrawerError(true);f.elements.repoUrl.focus();return}
  const projectId=repoDrawerState.projectId,project=(state.data.projects||[]).find(p=>p.id===projectId);
  if(!project)return toast('请先保存项目','warn');
  const makeDefault=$('#repoDrawerDefault').classList.contains('on'),payload=repoDrawerPayload(f,info),editing=!!payload.id;
  if(payload.workBranchNaming.mode==='prefix'&&f.elements.workBranchPrefix.value==='custom'&&!payload.workBranchNaming.prefix){
    f.elements.workBranchCustomPrefix.focus();
    return toast('请输入自定义前缀，或选择无前缀','warn');
  }
  if(payload.workBranchNaming.mode==='ai'&&(!payload.workBranchNaming.executorId||!payload.workBranchNaming.model)){
    return toast(lfT('请选择 AI 分支命名的 CLI 和模型','Choose the CLI and model for AI branch naming'),'warn');
  }
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
  f.elements.repoUrl.addEventListener('input',syncRepoDrawerDetect);
  f.elements.name.addEventListener('input',()=>{repoDrawerState.nameTouched=!!f.elements.name.value.trim();syncRepoDrawerDetect()});
  f.elements.workBranchNamingMode.addEventListener('change',()=>syncRepoBranchNaming(f));
  f.elements.workBranchPrefix.addEventListener('change',()=>syncRepoBranchNaming(f));
  f.elements.workBranchExecutorId.addEventListener('change',()=>fillRepoBranchModels(f));
  f.elements.workBranchModelChoice.addEventListener('change',()=>{
    const manual=f.elements.workBranchModelChoice.value===AGENT_CUSTOM_MODEL;
    f.querySelector('[data-work-branch-custom-model]').hidden=!manual;
    if(manual)f.elements.workBranchCustomModel.focus();
  });
  $('#repoDrawerRename').onclick=()=>{repoDrawerState.nameTouched=true;$('#repoDrawerNameField').hidden=false;f.elements.name.focus();f.elements.name.select()};
  $('#repoDrawerDefault').onclick=()=>setRepoDrawerSwitch(!$('#repoDrawerDefault').classList.contains('on'));
  $$('[data-repo-drawer-close]').forEach(b=>b.onclick=closeRepoDrawer);
  $('#repoDrawerScrim').onclick=closeRepoDrawer;
  document.addEventListener('keydown',e=>{
    if(e.key!=='Escape'||!repoDrawerOpen()||$('#gitSSHKeyDialog')?.open||$('#confirmModal')?.classList.contains('open'))return;
    e.preventDefault();e.stopImmediatePropagation();closeRepoDrawer();
  },true);
}
